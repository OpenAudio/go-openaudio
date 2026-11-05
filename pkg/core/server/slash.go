package server

import (
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	ethv1 "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func (s *Server) getSlashAttestation(ctx context.Context, slash *v1.SlashRecommendation) (string, error) {
	var signature string
	if err := validateSlashRecommendation(slash); err != nil {
		return signature, err
	}
	serviceProviderAddress := slash.Address
	endpointsResp, err := s.eth.GetRegisteredEndpointsForServiceProvider(
		ctx,
		connect.NewRequest(&ethv1.GetRegisteredEndpointsForServiceProviderRequest{Owner: serviceProviderAddress}),
	)
	if err != nil {
		s.logger.Error("Failed to get service provider endpoints", zap.String("address", serviceProviderAddress), zap.Error(err))
		return signature, err
	}
	endpoints := endpointsResp.Msg.Endpoints
	if len(endpoints) == 0 {
		return signature, errors.New("no registered endpoints for slash recommendation")
	}
	totalMissedSlas, totalSlas := 0, 0
	for _, ep := range endpoints {
		// Get the comet address for each endpoint, if possible
		var cometAddress string
		validator, err := s.db.GetNodeByEndpoint(ctx, ep.Endpoint)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Error("Failed to get cometbft validator for endpoint", zap.String("endpoint", ep.Endpoint), zap.Error(err))
			return signature, err
		} else if errors.Is(err, pgx.ErrNoRows) {
			// Endpoint is registered on eth but not on comet.
			// Attempt to get comet address from validator history
			history, err := s.db.GetValidatorHistoryForID(
				ctx,
				db.GetValidatorHistoryForIDParams{
					SpID:        ep.Id,
					ServiceType: ep.ServiceType,
				},
			)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				s.logger.Error("Failed to get validator history for endpoint", zap.String("endpoint", ep.Endpoint), zap.Error(err))
				return signature, err
			} else if err == nil {
				cometAddress = history.CometAddress
			}
		} else if err == nil {
			cometAddress = validator.CometAddress
		}

		// Add individual sla reports to endpoint data
		slaRollups, err := s.db.GetRollupReportsForNodeInTimeRange(
			ctx,
			db.GetRollupReportsForNodeInTimeRangeParams{
				Address: cometAddress,
				Time:    s.db.ToPgxTimestamp(slash.Start.AsTime()),
				Time_2:  s.db.ToPgxTimestamp(slash.End.AsTime()),
			},
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Error("Failed to get rollups from db for node", zap.String("address", cometAddress), zap.Time("start_time", slash.Start.AsTime()), zap.Time("end_time", slash.End.AsTime()), zap.Error(err))
			return signature, err
		}
		for _, rollup := range slaRollups {
			// The query includes every rollup in the interval via a LEFT JOIN.
			// An endpoint had no SLA obligation before its L1 registration.
			if ep.RegisteredAt.AsTime().After(rollup.Time.Time) {
				continue
			}
			totalSlas += 1
			if rollup.BlocksProposed.Int32 == 0 {
				totalMissedSlas += 1
			}
		}
	}

	if err := validateSlashEvidence(slash, len(endpoints), totalSlas, totalMissedSlas); err != nil {
		return signature, err
	}

	signature, err = SignSlashRecommendation(s.config.EthereumKey, s.config.ChainID(), slash)
	if err != nil {
		s.logger.Error("could not sign slash recommendation", zap.Error(err))
		return signature, err
	}
	return signature, nil
}

func SignSlashRecommendation(ethKey *ecdsa.PrivateKey, chainID string, slash *v1.SlashRecommendation) (string, error) {
	var signature string
	if ethKey == nil {
		return signature, errors.New("empty eth key")
	}
	slashBytes, err := SlashRecommendationSignBytes(chainID, slash)
	if err != nil {
		return signature, fmt.Errorf("could not marshal slash recommendation: %v", err)
	}
	signature, err = common.EthSign(ethKey, slashBytes)
	if err != nil {
		return signature, fmt.Errorf("failed to sign slash recommendation: %v", err)
	}
	return signature, nil
}

// SlashRecommendationSignBytes is the versioned, type-bound, chain-bound
// payload for off-chain slash proofs. Consumers must recover signatures over
// these bytes, never over raw proto.Marshal(slash). No consensus rule uses
// slash proofs, so this is not gated on an upgrade height; the chain ID is
// what keeps a proof signed on devnet or stage from being presented as
// evidence against the same wallet on mainnet.
func SlashRecommendationSignBytes(chainID string, slash *v1.SlashRecommendation) ([]byte, error) {
	if err := validateSlashRecommendation(slash); err != nil {
		return nil, err
	}
	if chainID == "" {
		return nil, errors.New("missing slash chain ID")
	}
	return domainSeparatedAttestationBytes(chainID, slash)
}

func validateSlashRecommendation(slash *v1.SlashRecommendation) error {
	if slash == nil {
		return errors.New("empty slash recommendation")
	}
	// Unknown fields are attacker-controlled data we cannot attest to. This
	// includes unknown fields inside the timestamps, not just the outer body.
	for _, msg := range []proto.Message{slash, slash.Start, slash.End} {
		if !msg.ProtoReflect().IsValid() {
			return errors.New("missing slash timestamp")
		}
		if len(msg.ProtoReflect().GetUnknown()) != 0 {
			return errors.New("unknown fields in slash recommendation")
		}
	}
	if !ethcommon.IsHexAddress(slash.Address) {
		return errors.New("invalid slash service provider address")
	}
	if err := slash.Start.CheckValid(); err != nil {
		return fmt.Errorf("invalid slash start: %w", err)
	}
	if err := slash.End.CheckValid(); err != nil {
		return fmt.Errorf("invalid slash end: %w", err)
	}
	if !slash.Start.AsTime().Before(slash.End.AsTime()) {
		return errors.New("slash start must precede end")
	}
	if slash.MissedSLAs < 0 || slash.Amount < 0 {
		return errors.New("negative slash amount or missed SLA count")
	}
	return nil
}

func validateSlashEvidence(slash *v1.SlashRecommendation, totalEndpoints, totalSlas, missedSlas int) error {
	if totalEndpoints <= 0 || totalSlas <= 0 {
		return errors.New("no SLA evidence for slash recommendation")
	}
	if int64(slash.MissedSLAs) != int64(missedSlas) {
		return errors.New("slash missed SLA counts do not match")
	}
	amount := CalculateSlashRecommendation(slash.Start.AsTime(), slash.End.AsTime(), totalEndpoints, totalSlas, missedSlas)
	if slash.Amount != amount {
		return errors.New("slash amounts do not match")
	}
	return nil
}

// Calculate slash recommendation based on SLA performance.
// Explanation:
//
//	200k AUDIO is the minimum stake per endpoint.
//	We therefore recommend slashing 200k AUDIO for a full year of zero sla performance
//	from a single endpoint.
//	$AUDIO to slash = $200k * number of endpoints * (days in selected interval / 365) * (zeroed SLAs / total SLAs)
func CalculateSlashRecommendation(startTime, endTime time.Time, totalEndpoints, totalSlas, missedSlas int) int64 {
	// No evidence means no recommendation. In particular, never convert NaN
	// (0/0) to int64: the result differs between CPU architectures.
	if totalEndpoints <= 0 || totalSlas <= 0 || missedSlas < 0 || missedSlas > totalSlas || !startTime.Before(endTime) {
		return 0
	}
	periodDays := int64(endTime.Sub(startTime).Hours() / 24)
	amount := 200000.0 * float64(totalEndpoints) * (float64(periodDays) / 365.0) * (float64(missedSlas) / float64(totalSlas))
	if amount >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(amount)
}

func (s *Server) gatherSlashAttestations(ctx context.Context, slash *v1.SlashRecommendation) (map[string]string, error) {
	payload, err := SlashRecommendationSignBytes(s.config.ChainID(), slash)
	if err != nil {
		return nil, err
	}
	validators, err := s.db.GetAllRegisteredNodes(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("failed to get all registered nodes: %v", err)
	}
	addrs := make([]string, len(validators))
	addrToValidator := make(map[string]db.CoreValidator, len(validators))
	for i, validator := range validators {
		addrs[i] = validator.EthAddress
		addrToValidator[validator.EthAddress] = validator
	}
	keyBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(keyBytes, uint64(slash.Amount))
	// Reuse registration rendezvous size from configuration
	rendezvous := common.GetAttestorRendezvous(addrs, keyBytes, s.config.AttRegistrationRSize)
	attestations := make(map[string]string, s.config.AttRegistrationRSize)
	for addr := range rendezvous {
		if peer, ok := s.connectRPCPeers.Get(addr); ok {
			resp, err := peer.GetSlashAttestation(ctx, connect.NewRequest(&v1.GetSlashAttestationRequest{
				Data: slash,
			}))
			if err != nil {
				s.logger.Error("failed to get slash attestation from peer", zap.String("peer_address", addr), zap.Error(err))
				continue
			}
			// A peer on an older release may still sign the raw protobuf. Only
			// include proofs that verify in the advertised domain for this peer.
			_, signer, err := common.EthRecover(resp.Msg.Signature, payload)
			if err != nil || !strings.EqualFold(signer, addr) {
				s.logger.Warn("invalid slash attestation from peer", zap.String("peer_address", addr))
				continue
			}
			endpoint := addrToValidator[addr].Endpoint
			attestations[endpoint] = resp.Msg.Signature
		}
	}
	return attestations, nil
}
