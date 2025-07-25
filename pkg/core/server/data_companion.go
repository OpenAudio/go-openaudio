package server

import (
	"context"
	"fmt"
	"time"

	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/cometbft/cometbft/rpc/grpc/client/privileged"
)

func (s *Server) startDataCompanion(ctx context.Context) error {
	if s.config.Archive {
		return nil
	}

	s.logger.Info("starting data companion")

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.awaitRpcReady:
	}

	conn, err := privileged.New(ctx, "unix://"+config.PrivilegedServiceSocket, privileged.WithPruningServiceEnabled(true), privileged.WithInsecure())
	if err != nil {
		return fmt.Errorf("dc could not create privileged rpc connection: %v", err)
	}
	defer conn.Close()

	ticker := time.NewTicker(5 * time.Second)
	for {
		select {
		case <-ticker.C:
			blockRetainHeight, err := conn.GetBlockRetainHeight(ctx)
			if err != nil {
				s.logger.Errorf("dc could not get block retain height: %v", err)
				continue
			}

			if blockRetainHeight.App <= 1 {
				continue
			}

			if err := conn.SetBlockRetainHeight(ctx, blockRetainHeight.App); err != nil {
				s.logger.Errorf("dc could not set block retain height: %v", err)
			}

			if err := conn.SetBlockResultsRetainHeight(ctx, blockRetainHeight.App); err != nil {
				s.logger.Errorf("dc could not set block results retain height: %v", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
