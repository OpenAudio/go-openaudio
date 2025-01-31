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

	"github.com/AudiusProject/audiusd/pkg/core/common"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_proto"
	"github.com/AudiusProject/audiusd/pkg/pos"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
)

const (
	mediorumPoSRequestTimeout = 1 * time.Second
	posChallengeDeadline      = 2
	posVerificationDelay      = posChallengeDeadline * 3 * time.Second
)

// Called during FinalizeBlock. Keeps Proof of Storage subsystem up to date with current block.
func (s *Server) syncPoS(ctx context.Context, latestBlockHash []byte, latestBlockHeight int64) error {
	if blockShouldTriggerNewPoSChallenge(latestBlockHash) {
		s.logger.Info("PoS Challenge triggered", "height", latestBlockHeight, "hash", hex.EncodeToString(latestBlockHash))
		qtx := s.getDb()
		err := qtx.InsertPoSChallenge(ctx, latestBlockHeight)
		if err != nil {
			return fmt.Errorf("Could not insert PoS challenge to db at height %d: %v", latestBlockHeight, err)
		}
		// TODO: disable in block sync mode
		go s.sendPoSChallengeToMediorum(latestBlockHash, latestBlockHeight)
	}
	return nil
}

func blockShouldTriggerNewPoSChallenge(blockHash []byte) bool {
	bhLen := len(blockHash)
	// Trigger if the last four bits of the blockhash are zero.
	// There is a ~6.25% chance of this happening.
	return bhLen > 0 && blockHash[bhLen-1]&0x0f == 0
}

func (s *Server) sendPoSChallengeToMediorum(blockHash []byte, blockHeight int64) {
	respChannel := make(chan pos.PoSResponse)
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
			s.logger.Error("Failed to get all registered comet nodes for endpoints", "endpoints", response.Replicas, "error", err)
		}
		prover_addresses := make([]string, 0, len(nodes))
		for _, n := range nodes {
			prover_addresses = append(prover_addresses, n.CometAddress)
		}

		// Add provers
		if err := s.db.UpdatePoSChallengeProvers(
			ctx,
			db.UpdatePoSChallengeProversParams{prover_addresses, blockHeight},
		); err != nil {
			s.logger.Error("Could not update existing PoS challenge", "hash", blockHash, "error", err)
		}

		// submit proof tx if we are part of the challenge
		if len(response.Proof) > 0 {
			err := s.submitStorageProofTx(blockHeight, blockHash, response.CID, prover_addresses, response.Proof)
			if err != nil {
				s.logger.Error("Could not submit storage proof tx", "hash", blockHash, "error", err)
			}
		}

	case <-timeout:
		s.logger.Info("No response from mediorum for PoS challenge.")
	}
}

func (s *Server) submitStorageProofTx(height int64, hash []byte, cid string, replicaAddresses []string, proof []byte) error {
	proofSig, err := s.config.CometKey.Sign(proof)
	if err != nil {
		return fmt.Errorf("Could not sign storage proof: %v", err)
	}
	proofTx := &core_proto.StorageProof{
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

	tx := &core_proto.SignedTransaction{
		Signature: sig,
		RequestId: uuid.NewString(),
		Transaction: &core_proto.SignedTransaction_StorageProof{
			StorageProof: proofTx,
		},
	}

	req := &core_proto.SendTransactionRequest{
		Transaction: tx,
	}

	txhash, err := s.SendTransaction(context.Background(), req)
	if err != nil {
		return fmt.Errorf("send storage proof tx failed: %v", err)
	}
	s.logger.Infof("Sent storage proof for cid '%s' at height '%d', receipt '%s'", cid, height, txhash)

	// Send the verification later.
	go func() {
		time.Sleep(posVerificationDelay)
		s.submitStorageProofVerificationTx(height, proof)
	}()

	return nil
}

func (s *Server) submitStorageProofVerificationTx(height int64, proof []byte) error {
	verificationTx := &core_proto.StorageProofVerification{
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

	tx := &core_proto.SignedTransaction{
		Signature: sig,
		RequestId: uuid.NewString(),
		Transaction: &core_proto.SignedTransaction_StorageProofVerification{
			StorageProofVerification: verificationTx,
		},
	}

	req := &core_proto.SendTransactionRequest{
		Transaction: tx,
	}

	txhash, err := s.SendTransaction(context.Background(), req)
	if err != nil {
		return fmt.Errorf("send storage proof verification tx failed: %v", err)
	}
	s.logger.Infof("Sent storage proof verification for pos challenge at height '%d', receipt '%s'", height, txhash)
	return nil
}

func (s *Server) isValidStorageProofTx(ctx context.Context, tx *core_proto.SignedTransaction, currentBlockHeight int64, enforceReplicas bool) error {
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
		return fmt.Errorf("Could not get validator for address '%s': %v", address, err)
	}
	if strings.ToLower(node.CometAddress) != strings.ToLower(sp.Address) {
		return fmt.Errorf("Proof is for '%s' but was signed by '%s'", sp.Address, node.CometAddress)
	}

	// validate height
	height := sp.GetHeight()
	if height == 0 {
		return fmt.Errorf("Invalid height '%d' for storage proof", height)
	}
	if currentBlockHeight-height > posChallengeDeadline {
		return fmt.Errorf("Proof submitted at height '%d' for challenge at height '%d' which is past the deadline", currentBlockHeight, height)
	}

	// validate existing ongoing challenge
	posChallenge, err := s.db.GetPoSChallenge(ctx, height)
	if err != nil {
		return fmt.Errorf("Could not retrieve any ongoing pos challenges at height '%d': %v", height, err)
	}
	if enforceReplicas && posChallenge.ProverAddresses != nil && !slices.Contains(posChallenge.ProverAddresses, sp.Address) {
		// We think this proof does not belong to this challenge.
		// Note: this should not be enforced during the finalize step.
		return fmt.Errorf("Prover at address '%s' does not belong to replicaset.", sp.Address)
	}

	return nil
}

func (s *Server) isValidStorageProofVerificationTx(ctx context.Context, tx *core_proto.SignedTransaction, currentBlockHeight int64) error {
	spv := tx.GetStorageProofVerification()
	if spv == nil {
		return fmt.Errorf("unknown tx fell into isValidStorageProofVerficationTx: %v", tx)
	}

	// validate height
	height := spv.GetHeight()
	if height == 0 {
		return fmt.Errorf("Invalid height '%d' for storage proof", height)
	}
	if currentBlockHeight-height <= posChallengeDeadline {
		return fmt.Errorf("Proof submitted at height '%d' for challenge at height '%d' which is before the deadline", currentBlockHeight, height)
	}

	// validate existing ongoing challenge
	_, err := s.db.GetPoSChallenge(ctx, height)
	if err != nil {
		return fmt.Errorf("Could not retrieve any ongoing pos challenges at height '%d': %v", height, err)
	}

	return nil
}

func (s *Server) finalizeStorageProof(ctx context.Context, tx *core_proto.SignedTransaction, blockHeight int64) (*core_proto.StorageProof, error) {
	if err := s.isValidStorageProofTx(ctx, tx, blockHeight, false); err != nil {
		return nil, err
	}

	sp := tx.GetStorageProof()
	qtx := s.getDb()

	// ignore duplicates
	if _, err := qtx.GetStorageProof(ctx, db.GetStorageProofParams{sp.Height, sp.Address}); !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("Storage proof already exists, skipping.", "node", sp.Address, "height", sp.Height)
		return sp, nil
	}

	proofSigStr := base64.StdEncoding.EncodeToString(sp.ProofSignature)

	if err := qtx.InsertStorageProof(
		ctx,
		db.InsertStorageProofParams{
			BlockHeight:     sp.Height,
			Address:         sp.Address,
			Cid:             pgtype.Text{sp.Cid, true},
			ProofSignature:  pgtype.Text{proofSigStr, true},
			ProverAddresses: sp.ProverAddresses,
		},
	); err != nil {
		return nil, fmt.Errorf("Could not persist storage proof in db: %v", err)
	}

	return sp, nil
}

func (s *Server) finalizeStorageProofVerification(ctx context.Context, tx *core_proto.SignedTransaction, currentBlockHeight int64) (*core_proto.StorageProofVerification, error) {
	if err := s.isValidStorageProofVerificationTx(ctx, tx, currentBlockHeight); err != nil {
		return nil, err
	}

	spv := tx.GetStorageProofVerification()
	qtx := s.getDb()

	challenge, err := qtx.GetPoSChallenge(ctx, spv.Height)
	if err != nil {
		return nil, fmt.Errorf("Could not pos challenge: %v", err)
	}
	if challenge.Status == db.ChallengeStatusComplete {
		// challenge already resolved, no-op
		return spv, nil
	}

	proofs, err := qtx.GetStorageProofs(ctx, spv.Height)
	if err != nil {
		return nil, fmt.Errorf("Could not fetch storage proofs: %v", err)
	}

	consensusNodes := make([]string, 0, len(proofs))
	consensusPeers := make(map[string]int)
	for _, p := range proofs {
		node, err := qtx.GetRegisteredNodeByCometAddress(ctx, p.Address)
		if err != nil {
			return nil, fmt.Errorf("Could not fetch node with address %s: %v", p.Address, err)
		}

		sigBytes, err := base64.StdEncoding.DecodeString(p.ProofSignature.String)
		if err != nil {
			return nil, fmt.Errorf("Could not decode proof signature node at address %s: %v", node.CometAddress, err)
		}
		pubKeyBytes, err := base64.StdEncoding.DecodeString(node.CometPubKey)
		if err != nil {
			return nil, fmt.Errorf("Could not decode public key for node at address %s: %v", node.CometAddress, err)
		}
		pubKey := ed25519.PubKey(pubKeyBytes)
		if pubKey.VerifySignature(spv.Proof, sigBytes) {
			consensusNodes = append(consensusNodes, p.Address)

			// Track consensus on who the other provers were
			for _, peer := range p.ProverAddresses {
				if _, ok := consensusPeers[peer]; ok {
					consensusPeers[peer]++
				} else {
					consensusPeers[peer] = 1
				}
			}
		}
	}

	if len(consensusNodes) > len(proofs)/2 {
		// we have a majority, we can resolve the challenge
		proofStr := hex.EncodeToString(spv.Proof)
		for _, p := range proofs {
			if slices.Contains(consensusNodes, p.Address) {
				err := qtx.UpdateStorageProof(
					ctx,
					db.UpdateStorageProofParams{
						Proof:       pgtype.Text{proofStr, true},
						Status:      db.ProofStatusPass,
						BlockHeight: spv.Height,
						Address:     p.Address,
					},
				)
				if err != nil {
					return nil, fmt.Errorf("Could not update storage proof for prover %s at height %d: %v", p.Address, spv.Height, err)
				}
			} else {
				err := qtx.UpdateStorageProof(
					ctx,
					db.UpdateStorageProofParams{
						Proof:       pgtype.Text{proofStr, true},
						Status:      db.ProofStatusFail,
						BlockHeight: spv.Height,
						Address:     p.Address,
					},
				)
				if err != nil {
					return nil, fmt.Errorf("Could not update storage proof for prover %s at height %d: %v", p.Address, spv.Height, err)
				}
			}
			delete(consensusPeers, p.Address)
		}

		// Add failed storage proofs for missing provers
		for peer, vote := range consensusPeers {
			if vote > len(proofs)/2 {
				// a majority said this node was also a prover, but it did not provide a proof
				qtx.InsertFailedStorageProof(
					ctx,
					db.InsertFailedStorageProofParams{spv.Height, peer},
				)
			}
		}

		err := qtx.CompletePoSChallenge(ctx, spv.Height)
		if err != nil {
			return nil, fmt.Errorf("Could not complete pos challenge at height %d: %v", spv.Height, err)
		}
	}

	return spv, nil
}
