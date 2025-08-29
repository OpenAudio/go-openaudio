package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AudiusProject/audiusd/pkg/mediorum/server/signature"
	"go.uber.org/zap"

	"github.com/AudiusProject/audiusd/pkg/mediorum/cidutil"

	"gocloud.dev/blob"
)

func (ss *MediorumServer) replicateFileParallel(ctx context.Context, cid string, filePath string, placementHosts []string) ([]string, error) {
	replicaCount := ss.Config.ReplicationFactor

	if len(placementHosts) > 0 {
		// use all explicit placement hosts
		replicaCount = len(placementHosts)
	} else {
		// use rendezvous
		placementHosts, _ = ss.rendezvousAllHosts(cid)
	}

	queue := make(chan string, len(placementHosts))
	for _, p := range placementHosts {
		queue <- p
	}

	mu := sync.Mutex{}
	results := []string{}

	wg := sync.WaitGroup{}
	wg.Add(replicaCount)

	for i := 0; i < replicaCount; i++ {
		go func() {
			defer wg.Done()

			file, err := os.Open(filePath)
			if err != nil {
				ss.logger.Error("failed to open file", zap.String("filePath", filePath), zap.Error(err))
				return
			}
			defer file.Close()
			for peer := range queue {
				file.Seek(0, 0)
				err := ss.replicateFileToHost(ctx, peer, cid, file)
				if err == nil {
					mu.Lock()
					results = append(results, peer)
					mu.Unlock()
					break
				}
			}

		}()
	}

	wg.Wait()
	return results, nil
}

func (ss *MediorumServer) replicateFile(ctx context.Context, fileName string, file io.ReadSeeker) ([]string, error) {
	logger := ss.logger.With(zap.String("task", "replicate"), zap.String("cid", fileName))

	success := []string{}
	preferred, _ := ss.rendezvousAllHosts(fileName)
	for _, peer := range preferred {
		logger := logger.With(zap.String("to", peer))

		logger.Debug("replicating")

		file.Seek(0, 0)
		err := ss.replicateFileToHost(ctx, peer, fileName, file)
		if err != nil {
			logger.Error("replication failed", zap.Error(err))
		} else {
			logger.Debug("replicated")
			success = append(success, peer)
			if len(success) == ss.Config.ReplicationFactor {
				break
			}
		}
	}

	return success, nil
}

func (ss *MediorumServer) replicateToMyBucket(ctx context.Context, fileName string, file io.Reader) error {
	logger := ss.logger.With(zap.String("task", "replicateToMyBucket"), zap.String("cid", fileName))
	logger.Debug("replicateToMyBucket")
	key := cidutil.ShardCID(fileName)

	w, err := ss.bucket.NewWriter(ctx, key, nil)
	if err != nil {
		return err
	}

	_, err = io.Copy(w, file)
	if err != nil {
		return err
	}

	return w.Close()
}

func (ss *MediorumServer) dropFromMyBucket(fileName string) error {
	logger := ss.logger.With(zap.String("task", "dropFromMyBucket"), zap.String("cid", fileName))
	logger.Debug("deleting blob")

	key := cidutil.ShardCID(fileName)
	ctx := context.Background()
	err := ss.bucket.Delete(ctx, key)
	if err != nil {
		logger.Error("failed to delete", zap.Error(err))
	}

	return nil
}

func (ss *MediorumServer) haveInMyBucket(fileName string) bool {
	shardedCid := cidutil.ShardCID(fileName)
	ctx := context.Background()
	exists, _ := ss.bucket.Exists(ctx, shardedCid)
	return exists
}

func (ss *MediorumServer) replicateFileToHost(ctx context.Context, peer string, fileName string, file io.Reader) error {
	// logger := ss.logger.With()
	if peer == ss.Config.Self.Host {
		return ss.replicateToMyBucket(ctx, fileName, file)
	}

	client := http.Client{
		Timeout: time.Minute,
	}

	r, w := io.Pipe()
	m := multipart.NewWriter(w)
	errChan := make(chan error)

	go func() {
		defer w.Close()
		defer m.Close()
		part, err := m.CreateFormFile(filesFormFieldName, fileName)
		if err != nil {
			errChan <- err
			return
		}
		if _, err = io.Copy(part, file); err != nil {
			errChan <- err
			return
		}
		close(errChan)
	}()

	req, err := signature.SignedPost(
		ctx,
		peer+"/internal/blobs",
		m.FormDataContentType(),
		r,
		ss.Config.privateKey,
		ss.Config.Self.Host,
	)
	if err != nil {
		return err
	}

	// send it
	resp, err := client.Do(req)
	if err != nil {
		return err
	}

	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New(resp.Status)
	}

	return <-errChan
}

// hostHasBlob is a "quick check" that a host has a blob (used for checking host has blob before redirecting to it).
func (ss *MediorumServer) hostHasBlob(host, key string) bool {
	attr, err := ss.hostGetBlobInfo(host, key)
	return err == nil && attr != nil
}

func (ss *MediorumServer) hostGetBlobInfo(host, key string) (*blob.Attributes, error) {
	var attr *blob.Attributes
	u := apiPath(host, fmt.Sprintf("internal/blobs/info/%s", url.PathEscape(key)))
	resp, err := ss.reqClient.R().SetSuccessResult(&attr).Get(u)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s %s", u, resp.Status)
	}
	return attr, nil
}

func (ss *MediorumServer) pullFileFromHost(ctx context.Context, host, cid string) error {
	if host == ss.Config.Self.Host {
		return errors.New("should not pull blob from self")
	}
	client := http.Client{
		Timeout: time.Minute * 3,
	}
	u := apiPath(host, "internal/blobs", url.PathEscape(cid))

	req, err := signature.SignedGet(ctx, u, ss.Config.privateKey, ss.Config.Self.Host)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("pull blob: bad status: %d cid: %s host: %s", resp.StatusCode, cid, host)
	}

	return ss.replicateToMyBucket(ctx, cid, resp.Body)
}

// if the node is using local (disk) storage, do not replicate if there is <200GB remaining (i.e. 10% of 2TB)
func (ss *MediorumServer) diskHasSpace() bool {
	// don't worry about running out of space on dev or stage
	if ss.Config.Env != "prod" {
		return true
	}

	return !strings.HasPrefix(ss.Config.BlobStoreDSN, "file://") || ss.mediorumPathFree/uint64(1e9) > 200
}
