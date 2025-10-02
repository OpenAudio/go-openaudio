package server

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/pos"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

const (
	mediorumPoSRequestTimeout = 3 * time.Second
	posChallengeDeadline      = 2
	posVerificationDelay      = posChallengeDeadline * 3 * time.Second
)

// Called during FinalizeBlock. Keeps Proof of Storage subsystem up to date with current block.
func (s *Server) syncPoS(_ context.Context, latestBlockHash []byte, latestBlockHeight int64) error {
	if !s.cache.catchingUp.Load() && blockShouldTriggerNewPoSChallenge(latestBlockHash) {
		s.logger.Info("PoS Challenge triggered", zap.Int64("height", latestBlockHeight), zap.String("hash", hex.EncodeToString(latestBlockHash)))
		go s.sendPoSChallengeToStorage(latestBlockHash, latestBlockHeight)
	}
	return nil
}

func blockShouldTriggerNewPoSChallenge(blockHash []byte) bool {
	bhLen := len(blockHash)
	// Trigger if the last four bits of the blockhash are zero.
	// There is a ~6.25% chance of this happening.
	return bhLen > 0 && blockHash[bhLen-1]&0x0f == 0
}

func (s *Server) sendPoSChallengeToStorage(blockHash []byte, blockHeight int64) {
	respChannel := make(chan pos.PoSResponse, 1)
	posReq := pos.PoSRequest{
		Hash:     blockHash,
		Height:   blockHeight,
		Response: respChannel,
	}
	s.mediorumPoSChannel <- posReq

	timeout := time.After(mediorumPoSRequestTimeout)
	select {
	case response := <-respChannel:
		ctx := context.Background()

		// get validator nodes corresponding to mediorum's replica endpoints
		nodes, err := s.db.GetNodesByEndpoints(ctx, response.Replicas)
		if err != nil {
			s.logger.Error("Failed to get all registered comet nodes for endpoints", zap.Strings("endpoints", response.Replicas), zap.Error(err))
		}
		proverAddresses := make([]string, 0, len(nodes))
		for _, n := range nodes {
			proverAddresses = append(proverAddresses, n.CometAddress)
		}

		// Add provers
		if err := s.db.InsertStorageProofPeers(
			ctx,
			db.InsertStorageProofPeersParams{BlockHeight: blockHeight, ProverAddresses: proverAddresses},
		); err != nil {
			s.logger.Error("Could not update existing PoS challenge", zap.ByteString("hash", blockHash), zap.Error(err))
		}

		// submit proof tx if we are part of the challenge
		if len(response.Proof) > 0 {
			err := s.submitStorageProofTx(blockHeight, blockHash, response.CID, proverAddresses, response.Proof)
			if err != nil {
				s.logger.Error("Could not submit storage proof tx", zap.ByteString("hash", blockHash), zap.Error(err))
			}
		}

	case <-timeout:
		s.logger.Info("No response from mediorum for PoS challenge.")
	}
}

func (s *Server) submitStorageProofTx(height int64, _ []byte, cid string, replicaAddresses []string, proof []byte) error {
	proofSig, err := s.config.CometKey.Sign(proof)
	if err != nil {
		return fmt.Errorf("could not sign storage proof: %v", err)
	}
	proofTx := &v1.StorageProof{
		Height:          height,
		Cid:             cid,
		Address:         s.config.ProposerAddress,
		ProofSignature:  proofSig,
		ProverAddresses: replicaAddresses,
	}

	txBytes, err := proto.Marshal(proofTx)
	if err != nil {
		return fmt.Errorf("failure to marshal proof tx: %v", err)
	}

	sig, err := common.EthSign(s.config.EthereumKey, txBytes)
	if err != nil {
		return fmt.Errorf("could not sign proof tx: %v", err)
	}

	tx := &v1.SignedTransaction{
		Signature: sig,
		RequestId: uuid.NewString(),
		Transaction: &v1.SignedTransaction_StorageProof{
			StorageProof: proofTx,
		},
	}

	req := &v1.SendTransactionRequest{
		Transaction: tx,
	}

	txhash, err := s.self.SendTransaction(context.Background(), connect.NewRequest(req))
	if err != nil {
		return fmt.Errorf("send storage proof tx failed: %v", err)
	}
	s.logger.Info("Sent storage proof", zap.String("cid", cid), zap.Int64("height", height), zap.String("receipt", txhash.Msg.Transaction.Hash))

	// Send the verification later.
	go func() {
		time.Sleep(posVerificationDelay)
		s.submitStorageProofVerificationTx(height, proof)
	}()

	return nil
}

func (s *Server) submitStorageProofVerificationTx(height int64, proof []byte) error {
	verificationTx := &v1.StorageProofVerification{
		Height: height,
		Proof:  proof,
	}

	txBytes, err := proto.Marshal(verificationTx)
	if err != nil {
		return fmt.Errorf("failure to marshal proof tx: %v", err)
	}

	sig, err := common.EthSign(s.config.EthereumKey, txBytes)
	if err != nil {
		return fmt.Errorf("could not sign proof tx: %v", err)
	}

	tx := &v1.SignedTransaction{
		Signature: sig,
		RequestId: uuid.NewString(),
		Transaction: &v1.SignedTransaction_StorageProofVerification{
			StorageProofVerification: verificationTx,
		},
	}

	req := &v1.SendTransactionRequest{
		Transaction: tx,
	}

	txhash, err := s.self.SendTransaction(context.Background(), connect.NewRequest(req))
	if err != nil {
		return fmt.Errorf("send storage proof verification tx failed: %v", err)
	}
	s.logger.Info("Sent storage proof verification for challenge", zap.Int64("height", height), zap.String("receipt", txhash.Msg.Transaction.Hash))
	return nil
}

func (s *Server) isValidStorageProofTx(ctx context.Context, tx *v1.SignedTransaction, currentBlockHeight int64, enforceReplicas bool) error {
	// validate signer == prover
	sig := tx.GetSignature()
	if sig == "" {
		return fmt.Errorf("no signature provided for storage proof tx: %v", tx)
	}
	sp := tx.GetStorageProof()
	if sp == nil {
		return fmt.Errorf("unknown tx fell into isValidStorageProofTx: %v", tx)
	}
	txBytes, err := proto.Marshal(sp)
	if err != nil {
		return fmt.Errorf("could not unmarshal tx bytes: %v", err)
	}
	_, address, err := common.EthRecover(sig, txBytes)
	if err != nil {
		return fmt.Errorf("could not recover signer: %v", err)
	}
	node, err := s.db.GetRegisteredNodeByEthAddress(ctx, address)
	if err != nil {
		return fmt.Errorf("could not get validator for address '%s': %v", address, err)
	}
	if !strings.EqualFold(node.CometAddress, sp.Address) {
		return fmt.Errorf("proof is for '%s' but was signed by '%s'", sp.Address, node.CometAddress)
	}

	// validate height
	height := sp.GetHeight()
	if height == 0 {
		return fmt.Errorf("invalid height '%d' for storage proof", height)
	}
	if currentBlockHeight-height > posChallengeDeadline {
		return fmt.Errorf("proof submitted at height '%d' for challenge at height '%d' which is past the deadline", currentBlockHeight, height)
	}

	// validate height corresponds to triggered challenge
	block, err := s.db.GetBlock(ctx, height)
	if err != nil {
		return fmt.Errorf("failed to get block at height %d: %v", height, err)
	}
	blockHashBytes, err := hex.DecodeString(block.Hash)
	if err != nil {
		return fmt.Errorf("failed to decode blockhash at height %d: %v", height, err)
	}
	if !blockShouldTriggerNewPoSChallenge(blockHashBytes) {
		return fmt.Errorf("block at height %d with hash '%s' should not trigger a storage proof", height, block.Hash)
	}

	// validate proof comes from a replica peer
	peer_addresses, err := s.db.GetStorageProofPeers(ctx, height)
	if enforceReplicas && err == nil && !slices.Contains(peer_addresses, sp.Address) {
		// We think this prover does not belong to this challenge.
		// Note: this should not be enforced during the finalize step.
		return fmt.Errorf("prover at address '%s' does not belong to replicaset for PoSt challenge at height %d", sp.Address, height)
	}

	return nil
}

func (s *Server) isValidStorageProofVerificationTx(ctx context.Context, tx *v1.SignedTransaction, currentBlockHeight int64) error {
	spv := tx.GetStorageProofVerification()
	if spv == nil {
		return fmt.Errorf("unknown tx fell into isValidStorageProofVerficationTx: %v", tx)
	}

	// validate height
	height := spv.GetHeight()
	if height == 0 {
		return fmt.Errorf("invalid height '%d' for storage proof", height)
	}
	if currentBlockHeight-height <= posChallengeDeadline {
		return fmt.Errorf("proof submitted at height '%d' for challenge at height '%d' which is before the deadline", currentBlockHeight, height)
	}

	// validate height corresponds to triggered challenge
	block, err := s.db.GetBlock(ctx, height)
	if err != nil {
		return fmt.Errorf("failed to get block at height %d: %v", height, err)
	}
	blockHashBytes, err := hex.DecodeString(block.Hash)
	if err != nil {
		return fmt.Errorf("failed to decode blockhash at height %d: %v", height, err)
	}
	if !blockShouldTriggerNewPoSChallenge(blockHashBytes) {
		return fmt.Errorf("block at height %d with hash '%s' should not trigger a storage proof", height, block.Hash)
	}

	return nil
}

func (s *Server) finalizeStorageProof(ctx context.Context, tx *v1.SignedTransaction, blockHeight int64) (*v1.StorageProof, error) {
	if err := s.isValidStorageProofTx(ctx, tx, blockHeight, false); err != nil {
		return nil, err
	}

	sp := tx.GetStorageProof()
	qtx := s.getDb()

	// ignore duplicates
	if _, err := qtx.GetStorageProof(ctx, db.GetStorageProofParams{BlockHeight: sp.Height, Address: sp.Address}); !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("Storage proof already exists, skipping.", zap.String("address", sp.Address), zap.Int64("height", sp.Height))
		return sp, nil
	}

	proofSigStr := base64.StdEncoding.EncodeToString(sp.ProofSignature)

	if err := qtx.InsertStorageProof(
		ctx,
		db.InsertStorageProofParams{
			BlockHeight:     sp.Height,
			Address:         sp.Address,
			Cid:             pgtype.Text{String: sp.Cid, Valid: true},
			ProofSignature:  pgtype.Text{String: proofSigStr, Valid: true},
			ProverAddresses: sp.ProverAddresses,
		},
	); err != nil {
		return nil, fmt.Errorf("could not persist storage proof in db: %v", err)
	}

	return sp, nil
}

func (s *Server) finalizeStorageProofVerification(ctx context.Context, tx *v1.SignedTransaction, currentBlockHeight int64) (*v1.StorageProofVerification, error) {
	if err := s.isValidStorageProofVerificationTx(ctx, tx, currentBlockHeight); err != nil {
		return nil, err
	}

	spv := tx.GetStorageProofVerification()
	qtx := s.getDb()

	proofs, err := qtx.GetStorageProofs(ctx, spv.Height)
	if err != nil {
		return nil, fmt.Errorf("could not fetch storage proofs: %v", err)
	}
	if len(proofs) == 0 || proofs[0].Status != db.ProofStatusUnresolved {
		// challenge already resolved, no-op
		return spv, nil
	}

	consensusNodes := make([]string, 0, len(proofs))
	consensusPeers := make(map[string]int)
	// Check the plaintext proof against every StorageProof signature received from a prover.
	// Even if the signature matches, we don't know that the prover has passed the challenge
	// unless a majority of provers also match.
	for _, p := range proofs {
		node, err := qtx.GetRegisteredNodeByCometAddress(ctx, p.Address)
		if err != nil {
			return nil, fmt.Errorf("could not fetch node with address %s: %v", p.Address, err)
		}

		sigBytes, err := base64.StdEncoding.DecodeString(p.ProofSignature.String)
		if err != nil {
			return nil, fmt.Errorf("could not decode proof signature node at address %s: %v", node.CometAddress, err)
		}
		pubKeyBytes, err := base64.StdEncoding.DecodeString(node.CometPubKey)
		if err != nil {
			return nil, fmt.Errorf("could not decode public key for node at address %s: %v", node.CometAddress, err)
		}
		pubKey := ed25519.PubKey(pubKeyBytes)
		if pubKey.VerifySignature(spv.Proof, sigBytes) {
			// Keep track of each prover whose signature matched the proof
			consensusNodes = append(consensusNodes, p.Address)

			// Also track consensus on who the other provers allegedly were. We will
			// use this to mark unsubmitted proofs as failures later.
			for _, peer := range p.ProverAddresses {
				consensusPeers[peer]++
			}
		}
	}

	// Check if a majority of provers' signatures matched. If we have a majority,
	// we can resolve the challenge
	if len(consensusNodes) > len(proofs)/2 {
		proofStr := hex.EncodeToString(spv.Proof)
		for _, p := range proofs {
			// Mark all matching provers as passed.
			if slices.Contains(consensusNodes, p.Address) {
				err := qtx.UpdateStorageProof(
					ctx,
					db.UpdateStorageProofParams{
						Proof:       pgtype.Text{String: proofStr, Valid: true},
						Status:      db.ProofStatusPass,
						BlockHeight: spv.Height,
						Address:     p.Address,
					},
				)
				if err != nil {
					return nil, fmt.Errorf("could not update storage proof for prover %s at height %d: %v", p.Address, spv.Height, err)
				}
			} else {
				// Mark remaining provers as failed
				err := qtx.UpdateStorageProof(
					ctx,
					db.UpdateStorageProofParams{
						Proof:       pgtype.Text{String: proofStr, Valid: true},
						Status:      db.ProofStatusFail,
						BlockHeight: spv.Height,
						Address:     p.Address,
					},
				)
				if err != nil {
					return nil, fmt.Errorf("could not update storage proof for prover %s at height %d: %v", p.Address, spv.Height, err)
				}
			}
			// This peer has now been handled. Remove it from consensusPeers in preparation for the
			// next step.
			delete(consensusPeers, p.Address)
		}

		// Some provers might not have submitted a proof. So, add failed storage proofs
		// for the missing provers (based on who the correct provers claimed their peers were)
		for peer, vote := range consensusPeers {
			if vote > len(proofs)/2 {
				// A majority said this node was also a prover, but it did not provide a proof.
				qtx.InsertFailedStorageProof(
					ctx,
					db.InsertFailedStorageProofParams{BlockHeight: spv.Height, Address: peer},
				)
			}
		}
	}

	return spv, nil
}
