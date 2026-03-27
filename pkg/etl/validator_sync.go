package etl

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/etl/db"
	"go.uber.org/zap"
)

// syncValidatorsFromCore periodically queries the core service's GetStatus
// to discover all known validators (including genesis and legacy-registered ones)
// and upserts them into etl_validators.
func (e *Indexer) syncValidatorsFromCore(ctx context.Context) error {
	// Wait for DB to be ready
	for e.db == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Run immediately on start, then every 5 minutes
	if err := e.doValidatorSync(ctx); err != nil {
		e.logger.Warn("Initial validator sync failed", zap.Error(err))
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := e.doValidatorSync(ctx); err != nil {
				e.logger.Warn("Validator sync failed", zap.Error(err))
			}
		}
	}
}

func (e *Indexer) doValidatorSync(ctx context.Context) error {
	resp, err := e.core.GetStatus(ctx, connect.NewRequest(&corev1.GetStatusRequest{}))
	if err != nil {
		return err
	}
	if resp.Msg == nil || resp.Msg.Peers == nil {
		return nil
	}

	peers := resp.Msg.Peers.Peers
	synced := 0

	// Also include self
	if resp.Msg.NodeInfo != nil {
		self := resp.Msg.NodeInfo
		if self.Endpoint != "" && self.CometAddress != "" {
			err := e.db.UpsertValidatorFromPeer(ctx, db.UpsertValidatorFromPeerParams{
				Address:      strings.ToLower(self.EthAddress),
				Endpoint:     self.Endpoint,
				CometAddress: strings.ToUpper(self.CometAddress),
				NodeType:     self.NodeType,
				Spid:         "",
				VotingPower:  0,
			})
			if err != nil {
				e.logger.Warn("Failed to upsert self as validator", zap.Error(err))
			} else {
				synced++
			}
		}
	}

	for _, peer := range peers {
		if peer.Endpoint == "" || peer.CometAddress == "" {
			continue
		}
		err := e.db.UpsertValidatorFromPeer(ctx, db.UpsertValidatorFromPeerParams{
			Address:      strings.ToLower(peer.EthAddress),
			Endpoint:     peer.Endpoint,
			CometAddress: strings.ToUpper(peer.CometAddress),
			NodeType:     peer.NodeType,
			Spid:         "",
			VotingPower:  0,
		})
		if err != nil {
			e.logger.Warn("Failed to upsert peer validator",
				zap.String("endpoint", peer.Endpoint),
				zap.Error(err),
			)
			continue
		}
		synced++
	}

	e.logger.Info("Validator sync complete",
		zap.Int("peers", len(peers)),
		zap.Int("synced", synced),
	)
	return nil
}
