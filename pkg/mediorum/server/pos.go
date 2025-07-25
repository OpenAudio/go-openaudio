package server

import (
	"context"
	"crypto/md5"
	"errors"
	"io"

	"github.com/AudiusProject/audiusd/pkg/mediorum/cidutil"
	"github.com/AudiusProject/audiusd/pkg/pos"
	"gorm.io/gorm"
)

func (ss *MediorumServer) startPoSHandler(ctx context.Context) error {
	for {
		select {
		case posReq, ok := <-ss.posChannel:
			if !ok {
				return nil // channel closed
			}
			cid, err := ss.getStorageProofCIDFromBlockhash(posReq.Hash)
			if err != nil {
				ss.logger.Error("Could not get a CID to perform proof with")
				continue
			}
			orderedHosts := ss.rendezvousHasher.Rank(cid)
			ss.logger.Info("Retrieved artifacts for proof of storage challenge", "cid", cid, "provers", orderedHosts)
			replicaSet := make([]string, 0, ss.Config.ReplicationFactor)
			mustProve := false
			for i, h := range orderedHosts {
				if i >= ss.Config.ReplicationFactor {
					break
				}
				if ss.Config.Self.Host == h {
					mustProve = true
				}
				replicaSet = append(replicaSet, h)
			}

			var proof []byte
			if mustProve {
				ss.logger.Info("Generating storage proof", "cid", cid, "blockHeight", posReq.Height)
				proof, err = ss.getStorageProof(ctx, cid, posReq.Hash)
				if err != nil {
					ss.logger.Error("Failed to get storage proof", "cid", cid, "error", err)
					continue
				}
			}
			response := pos.PoSResponse{
				CID:      cid,
				Replicas: replicaSet,
				Proof:    proof,
			}

			posReq.Response <- response
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) getStorageProof(ctx context.Context, cid string, nonce []byte) ([]byte, error) {
	key := cidutil.ShardCID(cid)
	var proof []byte
	blob, err := ss.bucket.NewReader(ctx, key, nil)
	if err != nil {
		return proof, err
	}
	defer func() {
		if blob != nil {
			blob.Close()
		}
	}()

	blobData, err := io.ReadAll(blob)
	if err != nil {
		return proof, err
	}

	augmentedDataBytes := append(blobData, nonce...)
	proofHash := md5.Sum(augmentedDataBytes)
	return proofHash[:], nil
}

func (ss *MediorumServer) getStorageProofCIDFromBlockhash(blockhash []byte) (string, error) {
	fauxCid, err := cidutil.ComputeRawDataCID(blockhash)
	if err != nil {
		return "", err
	}
	var upload Upload
	// TODO: only use CID's at least 10 minutes old?
	err = ss.crud.DB.
		Where("orig_file_cid > ?", fauxCid).
		Order("orig_file_cid").
		First(&upload).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ss.crud.DB.
			Where("orig_file_cid < ?", fauxCid).
			Order("orig_file_cid").
			First(&upload).Error
	}
	if err != nil {
		return "", err
	}
	return upload.OrigFileCID, nil
}
