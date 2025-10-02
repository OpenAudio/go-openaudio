package server

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	validatorPurgeSLAInterval   int32 = 8
	validatorPurgeMinValidators       = 50
)

func (s *Server) createRollupTx(ctx context.Context, ts time.Time, height int64) ([]byte, error) {
	rollup, err := s.createRollup(ctx, ts, height)
	if err != nil {
		return []byte{}, err
	}
	e := v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_SlaRollup{
			SlaRollup: rollup,
		},
	}
	rollupTx, err := proto.Marshal(&e)
	if err != nil {
		return []byte{}, err
	}
	return rollupTx, nil
}

func (s *Server) createRollup(ctx context.Context, timestamp time.Time, height int64) (*v1.SlaRollup, error) {
	var rollup *v1.SlaRollup
	var start int64 = 0
	latestRollup, err := s.db.GetLatestSlaRollup(ctx)
	if err == nil {
		start = latestRollup.BlockEnd + 1
	}

	reports, err := s.db.GetInProgressRollupReports(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("Error retrieving current rollup reports", zap.Error(err))
		return rollup, err
	}
	reportMap := make(map[string]db.SlaNodeReport, len(reports))
	for _, r := range reports {
		reportMap[r.Address] = r
	}

	// deterministic ordering keeps validation as simple as reflect.DeepEqual
	validators, err := s.db.GetAllRegisteredNodesSorted(ctx)
	if err != nil {
		s.logger.Error("Error retrieving validators", zap.Error(err))
		return rollup, err
	}

	rollup = &v1.SlaRollup{
		Timestamp:  timestamppb.New(timestamp),
		BlockStart: start,
		BlockEnd:   height - 1, // exclude current block
		Reports:    make([]*v1.SlaNodeReport, 0, len(validators)),
	}

	for _, v := range validators {
		var proto_rep v1.SlaNodeReport
		if r, ok := reportMap[v.CometAddress]; ok {
			proto_rep = v1.SlaNodeReport{
				Address:           r.Address,
				NumBlocksProposed: r.BlocksProposed,
			}
		} else {
			proto_rep = v1.SlaNodeReport{
				Address:           v.CometAddress,
				NumBlocksProposed: 0,
			}
		}
		rollup.Reports = append(rollup.Reports, &proto_rep)
	}

	return rollup, nil
}

// Checks if the given sla rollup matches our local tallies
func (s *Server) isValidRollup(ctx context.Context, timestamp time.Time, height int64, rollup *v1.SlaRollup) (bool, error) {
	// +1 for backwards compatibility with off-by-one legacy error, delete on next chain rollover
	if !s.shouldProposeNewRollup(ctx, height+1) {
		return false, nil
	}
	if rollup.BlockStart > rollup.BlockEnd {
		return false, nil
	}

	myRollup, err := s.createRollup(ctx, timestamp, height)
	if err != nil {
		return false, err
	}

	if myRollup.Timestamp.GetSeconds() != rollup.Timestamp.GetSeconds() || myRollup.Timestamp.GetNanos() != rollup.Timestamp.GetNanos() {
		return false, nil
	} else if myRollup.BlockStart != rollup.BlockStart {
		return false, nil
	} else if myRollup.BlockEnd != rollup.BlockEnd {
		return false, nil
	} else if !reflect.DeepEqual(myRollup.Reports, rollup.Reports) {
		return false, nil
	}
	return true, nil
}

func (s *Server) shouldProposeNewRollup(ctx context.Context, height int64) bool {
	previousHeight := int64(0)
	latestRollup, err := s.db.GetLatestSlaRollup(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("Error retrieving latest SLA rollup", zap.Error(err))
		return false
	} else {
		previousHeight = latestRollup.BlockEnd
	}
	// Rollup interval excludes block at current height.
	return height-1-previousHeight >= int64(s.config.SlaRollupInterval)
}

func (s *Server) finalizeSlaRollup(ctx context.Context, event *v1.SignedTransaction, txHash string) (*v1.SlaRollup, error) {
	appDb := s.getDb()
	rollup := event.GetSlaRollup()

	if _, err := appDb.GetSlaRollupWithTimestamp(
		ctx,
		pgtype.Timestamp{
			Time:  rollup.Timestamp.AsTime(),
			Valid: true,
		},
	); err == nil {
		s.logger.Error("Skipping duplicate sla rollup with timestamp", zap.Time("timestamp", rollup.Timestamp.AsTime()))
		return rollup, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("failed to check for existing rollup: %v", err)
	}

	id, err := appDb.CommitSlaRollup(
		ctx,
		db.CommitSlaRollupParams{
			Time: pgtype.Timestamp{
				Time:  rollup.Timestamp.AsTime(),
				Valid: true,
			},
			TxHash:     txHash,
			BlockStart: rollup.BlockStart,
			BlockEnd:   rollup.BlockEnd,
		},
	)
	if err != nil {
		return nil, err
	}

	if err = appDb.ClearUncommittedSlaNodeReports(ctx); err != nil {
		return nil, err
	}

	for _, r := range rollup.Reports {
		if err = appDb.CommitSlaNodeReport(
			ctx,
			db.CommitSlaNodeReportParams{
				Address:        r.Address,
				SlaRollupID:    pgtype.Int4{Int32: id, Valid: true},
				BlocksProposed: r.NumBlocksProposed,
			},
		); err != nil {
			return nil, err
		}
	}
	return rollup, nil
}

func (s *Server) ShouldPurgeValidatorForUnderperformance(ctx context.Context, validatorAddress string) (bool, error) {
	totalValidators, err := s.db.TotalValidators(ctx)
	if err != nil {
		return false, fmt.Errorf("could not get total validators: %v", err)
	}

	// killswitch to avoid purging too many validators
	if totalValidators <= validatorPurgeMinValidators {
		return false, nil
	}

	rollups, err := s.db.GetRecentRollupsForNode(
		ctx,
		db.GetRecentRollupsForNodeParams{
			Limit:   validatorPurgeSLAInterval,
			Address: validatorAddress,
		},
	)
	if err != nil {
		return false, fmt.Errorf("could not get recent rollups for node %s: %v", validatorAddress, err)
	}

	// if we have no SLA history for this validator, it hasn't been registered on comet during this interval
	noHistory := true
	for _, rollup := range rollups {
		if rollup.Address.Valid {
			noHistory = false
			break
		}
	}
	if noHistory {
		return false, nil
	}

	// if the validator has proposed at least one block during the interval, don't purge
	downTooLong := true
	for _, rollup := range rollups {
		if rollup.BlocksProposed.Int32 > 0 {
			downTooLong = false
			break
		}
	}
	return downTooLong, nil
}
