package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/erni27/imcache"
	"go.uber.org/zap"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
)

func (ss *MediorumServer) replicateFileParallel(ctx context.Context, cid string, filePath string, placementHosts []string) ([]string, error) {
	replicaCount := ss.Config.ReplicationFactor

	// Preserve the original placement context separately from the host list
	// we iterate. bucketForCID treats any non-empty placementHosts as "force
	// primary," so we must NOT pass the rendezvous-expanded list when the
	// upload had no explicit placement.
	originalPlacement := placementHosts
	hosts := placementHosts
	if len(hosts) > 0 {
		replicaCount = len(hosts)
	} else {
		hosts, _ = ss.rendezvousAllHosts(cid)
	}

	queue := make(chan string, len(hosts))
	for _, p := range hosts {
		queue <- p
	}
	// Close after enqueueing so workers exit when the buffer drains.
	// Without this, an all-peers-fail run blocks workers forever on `range queue`.
	close(queue)

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
				err := ss.replicateFileToHost(ctx, peer, cid, file, originalPlacement)
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
		err := ss.replicateFileToHost(ctx, peer, fileName, file, nil)
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

// replicateToMyBucket writes the blob to the bucket selected by bucketForCID.
// placementHosts MUST be passed when the caller has placement context (repair,
// upload replication path) so writes go to the same bucket reads expect.
// Pass nil for opportunistic peer pushes that have no placement context.
func (ss *MediorumServer) replicateToMyBucket(ctx context.Context, fileName string, file io.Reader, placementHosts []string) error {
	logger := ss.logger.With(zap.String("task", "replicateToMyBucket"), zap.String("cid", fileName))
	logger.Debug("replicateToMyBucket")
	key := cidutil.ShardCID(fileName)
	bucket := ss.bucketForCID(fileName, placementHosts)

	w, err := bucket.NewWriter(ctx, key, nil)
	if err != nil {
		return err
	}

	n, err := io.Copy(w, file)
	if err != nil {
		// Best-effort close so we don't leak temp files / abandon multipart
		// uploads. The copy error is what matters; ignore close error here.
		_ = w.Close()
		return err
	}

	// Close is the commit step for many blob drivers (S3, GCS multipart).
	// Only record the blob as locally present after a successful close,
	// so repair's knownPresent fast-path doesn't think we have a blob whose
	// upload never actually finalized.
	if err := w.Close(); err != nil {
		return err
	}

	ss.knownPresent.Set(ss.presenceCacheKey(key, bucket), n, imcache.WithNoExpiration())
	return nil
}

// dropFromMyBucket removes the blob from both buckets (NotFound is benign).
// Reads are hot-first-then-archive, so a blob can legitimately live in either
// bucket; deleting from both ensures cleanup is complete regardless of which
// bucket the original write landed in (covers rank-flip orphans too).
func (ss *MediorumServer) dropFromMyBucket(fileName string) error {
	logger := ss.logger.With(zap.String("task", "dropFromMyBucket"), zap.String("cid", fileName))
	logger.Debug("deleting blob")

	key := cidutil.ShardCID(fileName)
	ctx := context.Background()

	deleteFrom := func(b *blob.Bucket) {
		if err := b.Delete(ctx, key); err != nil && gcerrors.Code(err) != gcerrors.NotFound {
			logger.Error("failed to delete", zap.Error(err))
			return
		}
		ss.knownPresent.Remove(ss.presenceCacheKey(key, b))
	}

	deleteFrom(ss.bucket)
	if ss.archiveBucket != nil {
		deleteFrom(ss.archiveBucket)
	}
	return nil
}

func (ss *MediorumServer) haveInMyBucket(fileName string) bool {
	return ss.blobExists(context.Background(), cidutil.ShardCID(fileName))
}

func (ss *MediorumServer) replicateFileToHost(ctx context.Context, peer string, fileName string, file io.Reader, placementHosts []string) error {
	// logger := ss.logger.With()
	if peer == ss.Config.Self.Host {
		return ss.replicateToMyBucket(ctx, fileName, file, placementHosts)
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
	if len(placementHosts) > 0 {
		req.Header.Set(placementHostsHeader, encodePlacementHosts(placementHosts))
	}

	// send it
	resp, err := ss.peerHTTPClient.Do(req)
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

// pullFileFromHost fetches a CID from a peer and writes it to the bucket
// selected by bucketForCID(cid, placementHosts). Pass placementHosts when the
// caller has placement context (repair) so the local write lands in the same
// bucket reads expect; pass nil for opportunistic pulls (e.g. serveBlob fallback).
func (ss *MediorumServer) pullFileFromHost(ctx context.Context, host, cid string, placementHosts []string) error {
	if host == ss.Config.Self.Host {
		return errors.New("should not pull blob from self")
	}
	u := apiPath(host, "internal/blobs", url.PathEscape(cid))

	req, err := signature.SignedGet(ctx, u, ss.Config.privateKey, ss.Config.Self.Host)
	if err != nil {
		return err
	}
	// Note: placementHosts is NOT sent on the wire. The peer's GET
	// endpoint uses hot-first-then-archive fallback, so it'll find the
	// blob wherever it lives without needing routing context. The
	// placementHosts param here only governs where *this node's* local
	// write lands after we receive the blob.

	resp, err := ss.peerHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("pull blob: bad status: %d cid: %s host: %s", resp.StatusCode, cid, host)
	}

	return ss.replicateToMyBucket(ctx, cid, resp.Body, placementHosts)
}

// dsnHasSpace reports whether a file:// DSN has enough free disk to accept new
// blobs. Non-file DSNs (S3/GCS/etc) always return true. The fallbackFreeBytes
// argument is used when we can't statfs the path (e.g. it doesn't exist yet);
// callers pass the most relevant cached free-bytes count for their bucket.
func (ss *MediorumServer) dsnHasSpace(dsn string, fallbackFreeBytes uint64) bool {
	if !strings.HasPrefix(dsn, "file://") {
		return true
	}

	_, uri, found := strings.Cut(dsn, "://")
	if !found {
		ss.logger.Warn("malformed blob store DSN, falling back to cached free-bytes check",
			zap.String("dsn", dsn))
		return fallbackFreeBytes/uint64(1e9) > 10
	}

	blobPath := strings.Split(uri, "?")[0]

	_, free, err := getDiskStatus(blobPath)
	if err != nil {
		ss.logger.Warn("failed to check blob storage disk space, falling back to cached free-bytes",
			zap.String("blobPath", blobPath),
			zap.Error(err))
		return fallbackFreeBytes/uint64(1e9) > 10
	}

	freeGB := free / uint64(1e9)
	hasSpace := freeGB > 10

	if !hasSpace {
		ss.logger.Warn("blob storage disk space below threshold",
			zap.String("blobPath", blobPath),
			zap.Uint64("freeGB", freeGB),
			zap.Uint64("thresholdGB", 10))
	}

	return hasSpace
}

// diskHasSpace returns true only when every configured bucket that can
// actually receive writes has headroom. Used by paths that don't have a CID
// yet (e.g. the inbound /internal/blobs precheck) so we don't accept a write
// we can't place.
func (ss *MediorumServer) diskHasSpace() bool {
	if ss.Config.Env != "prod" {
		return true
	}
	if !ss.dsnHasSpace(ss.Config.BlobStoreDSN, ss.mediorumPathFree) {
		return false
	}
	// Archive only routes when archiveBucket is open AND StoreAll is on. If
	// the DSN is set but StoreAll is false, archive is logged as "unused" at
	// startup and no CID will ever land there — don't gate writes on it.
	if ss.archiveBucket != nil && ss.Config.StoreAll {
		if !ss.dsnHasSpace(ss.Config.ArchiveBlobStoreDSN, ss.archivePathFree) {
			return false
		}
	}
	return true
}

// diskHasSpaceForCID checks only the bucket the given CID would land in. Use
// this anywhere the CID is known so we don't reject writes to a healthy primary
// because the archive bucket is full (or vice versa).
func (ss *MediorumServer) diskHasSpaceForCID(cid string, placementHosts []string) bool {
	if ss.Config.Env != "prod" {
		return true
	}
	if ss.archiveBucket != nil && ss.bucketForCID(cid, placementHosts) == ss.archiveBucket {
		return ss.dsnHasSpace(ss.Config.ArchiveBlobStoreDSN, ss.archivePathFree)
	}
	return ss.dsnHasSpace(ss.Config.BlobStoreDSN, ss.mediorumPathFree)
}
