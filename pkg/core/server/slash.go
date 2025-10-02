package server

import (
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	ethv1 "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

func (s *Server) getSlashAttestation(ctx context.Context, slash *v1.SlashRecommendation) (string, error) {
	var signature string
	if slash == nil {
		return signature, errors.New("nil slash recommendation")
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
					ep.Id,
					ep.ServiceType,
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
			totalSlas += 1
			if rollup.BlocksProposed.Int32 == 0 {
				totalMissedSlas += 1
			}
		}
	}

	calculatedRecommendation := CalculateSlashRecommendation(slash.Start.AsTime(), slash.End.AsTime(), len(endpoints), totalSlas, totalMissedSlas)
	if slash.Amount != calculatedRecommendation {
		s.logger.Info("slash amounts do not match", zap.Int64("requested_slash", slash.Amount), zap.Int64("calculated_slash", calculatedRecommendation))
		return signature, errors.New("slash amounts do not match")
	}

	signature, err = SignSlashRecommendation(s.config.EthereumKey, slash)
	if err != nil {
		s.logger.Error("could not sign slash recommendation", zap.Error(err))
		return signature, err
	}
	return signature, nil
}

func SignSlashRecommendation(ethKey *ecdsa.PrivateKey, slash *corev1.SlashRecommendation) (string, error) {
	var signature string
	if slash == nil {
		return signature, errors.New("empty slash recommendation")
	}
	if ethKey == nil {
		return signature, errors.New("empty eth key")
	}
	slashBytes, err := proto.Marshal(slash)
	if err != nil {
		return signature, fmt.Errorf("could not marshal slash recommendation: %v", err)
	}
	signature, err = common.EthSign(ethKey, slashBytes)
	if err != nil {
		return signature, fmt.Errorf("failed to sign slash recommendation: %v", err)
	}
	return signature, nil
}

// Calculate slash recommendation based on SLA performance.
// Explanation:
//
//	200k AUDIO is the minimum stake per endpoint.
//	We therefore recommend slashing 200k AUDIO for a full year of zero sla performance
//	from a single endpoint.
//	$AUDIO to slash = $200k * number of endpoints * (days in selected interval / 365) * (zeroed SLAs / total SLAs)
func CalculateSlashRecommendation(startTime, endTime time.Time, totalEndpoints, totalSlas, missedSlas int) int64 {
	periodDays := int64(endTime.Sub(startTime).Hours() / 24)
	return int64(200000.0 * float64(totalEndpoints) * (float64(periodDays) / 365.0) * (float64(missedSlas) / float64(totalSlas)))
}

func (s *Server) gatherSlashAttestations(ctx context.Context, slash *v1.SlashRecommendation) (map[string]string, error) {
	if slash == nil {
		return nil, fmt.Errorf("empty slash recommendation data provided for gathering attestations")
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
	slashCopy := *slash
	for addr := range rendezvous {
		if peer, ok := s.connectRPCPeers.Get(addr); ok {
			resp, err := peer.GetSlashAttestation(ctx, connect.NewRequest(&corev1.GetSlashAttestationRequest{
				Data: &slashCopy,
			}))
			if err != nil {
				s.logger.Error("failed to get slash attestation from peer", zap.String("peer_address", addr), zap.Error(err))
				continue
			}
			endpoint := addrToValidator[addr].Endpoint
			attestations[endpoint] = resp.Msg.Signature
		}
	}
	return attestations, nil
}
