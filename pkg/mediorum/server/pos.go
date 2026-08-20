package server

import (
	"context"
	"crypto/md5"
	"errors"
	"io"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/OpenAudio/go-openaudio/pkg/pos"
	"go.uber.org/zap"
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
			// Use Core's host list (from core_validators) when provided for deterministic PoS.
			// Otherwise fall back to Mediorum's internal peer list (from eth).
			var orderedHosts []string
			if len(posReq.Hosts) > 0 {
				rh := common.NewRendezvousHasher(posReq.Hosts, nil)
				orderedHosts = rh.Rank(cid)
			} else {
				orderedHosts = ss.rendezvousHasher.Rank(cid)
			}
			ss.logger.Info("Retrieved artifacts for proof of storage challenge", zap.String("cid", cid), zap.Strings("provers", orderedHosts))
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
				ss.logger.Info("Generating storage proof", zap.String("cid", cid), zap.Int64("blockHeight", posReq.Height))
				proof, err = ss.getStorageProof(ctx, cid, posReq.Hash)
				if err != nil {
					ss.metrics.recordPoS(PoSResult{At: time.Now().UTC(), CID: cid, OK: false, Error: err.Error()})
					ss.logger.Error("Failed to get storage proof", zap.String("cid", cid), zap.Error(err))
					ss.maybeBackgroundPull(cid)
					continue
				}
				ss.metrics.recordPoS(PoSResult{At: time.Now().UTC(), CID: cid, OK: true})
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
	blob, _, err := ss.readBlob(ctx, key)
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

// storageProofMinUploadAge is how settled an upload must be before it can be
// selected as a proof-of-storage challenge target.
const storageProofMinUploadAge = time.Hour

func (ss *MediorumServer) getStorageProofCIDFromBlockhash(blockhash []byte) (string, error) {
	fauxCid, err := cidutil.ComputeRawDataCID(blockhash)
	if err != nil {
		return "", err
	}
	// Only challenge uploads old enough to have replicated, and never ones that
	// failed. Both predicates have to appear on the wrap-around query too, or it
	// hands back exactly what the first one excluded.
	//
	// This does more than spare one node an unprovable challenge. Every node
	// picks its own CID from its own uploads table by nearest neighbour, so a
	// row that one node has and another does not changes which CID that node
	// challenges at all -- not merely whether it holds the blob. An upload that
	// has not reached the whole fleet yet desynchronises the challenge itself,
	// and the prover-address tally that produces failure rows along with it.
	//
	// An hour, rather than the ten minutes the original TODO proposed: the bound
	// has to cover op propagation to peers plus replication of a multi-gigabyte
	// blob, which is deliberately unbounded. Excluding the last hour costs
	// nothing against a corpus this size.
	//
	// status <> error drops rows whose orig_file_cid was assigned before the
	// failure that stopped them being stored (see handleUploadError) -- CIDs no
	// node ever held.
	eligibleBefore := time.Now().UTC().Add(-storageProofMinUploadAge)

	var upload Upload
	err = ss.crud.DB.
		Where("orig_file_cid > ? AND created_at < ? AND status <> ?", fauxCid, eligibleBefore, JobStatusError).
		Order("orig_file_cid").
		First(&upload).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ss.crud.DB.
			Where("orig_file_cid < ? AND created_at < ? AND status <> ?", fauxCid, eligibleBefore, JobStatusError).
			Order("orig_file_cid").
			First(&upload).Error
	}
	if err != nil {
		return "", err
	}
	return upload.OrigFileCID, nil
}
