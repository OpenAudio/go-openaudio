package server

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/opvalidation"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

const (
	coreOpSubmitBatchSize      = 100
	coreOpSubmitInterval       = 10 * time.Second
	coreOpSubmitRetryBackoff   = time.Minute
	coreOpSyncCursorHost       = "core_mediorum_blocks"
	coreOpSyncInterval         = 10 * time.Second
	coreOpSyncInitialLookback  = int64(1000)
	coreOpSendTransactionLimit = 45 * time.Second
)

func mediorumOperationFromOp(op *crudr.Op) *corev1.MediorumOperation {
	if op == nil {
		return nil
	}
	return &corev1.MediorumOperation{
		Ulid:   op.ULID,
		Host:   op.Host,
		Action: op.Action,
		Table:  op.Table,
		Data:   []byte(op.Data),
	}
}

func opFromMediorumOperation(msg *corev1.MediorumOperation) *crudr.Op {
	if msg == nil {
		return nil
	}
	return &crudr.Op{
		ULID:         msg.Ulid,
		Host:         msg.Host,
		Action:       msg.Action,
		Table:        msg.Table,
		Data:         json.RawMessage(msg.Data),
		CoreTxStatus: crudr.CoreTxStatusConfirmed,
	}
}

func signedMediorumOperation(op *crudr.Op, privateKey *ecdsa.PrivateKey) (*corev1.SignedTransaction, string, error) {
	msg := mediorumOperationFromOp(op)
	bodyBytes, err := proto.Marshal(msg)
	if err != nil {
		return nil, "", fmt.Errorf("marshal mediorum operation: %w", err)
	}

	sig, err := common.EthSign(privateKey, bodyBytes)
	if err != nil {
		return nil, "", fmt.Errorf("sign mediorum operation: %w", err)
	}

	tx := &corev1.SignedTransaction{
		Signature: sig,
		Transaction: &corev1.SignedTransaction_MediorumOperation{
			MediorumOperation: msg,
		},
	}
	txBytes, err := proto.Marshal(tx)
	if err != nil {
		return nil, "", fmt.Errorf("marshal signed mediorum operation: %w", err)
	}

	return tx, common.ToTxHashFromBytes(txBytes), nil
}

func (ss *MediorumServer) startCoreOpSubmitter(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			ticker.Reset(coreOpSubmitInterval)
			if err := ss.submitPendingCoreOps(ctx); err != nil {
				ss.logger.Warn("core mediorum op submitter pass failed", zap.Error(err))
			}
		}
	}
}

func (ss *MediorumServer) submitPendingCoreOps(ctx context.Context) error {
	if ss.core == nil || !ss.core.IsReady() {
		return nil
	}

	ops, err := ss.crud.PendingCoreOps(ctx, coreOpSubmitBatchSize)
	if err != nil {
		return err
	}

	for _, op := range ops {
		if op.CoreAttemptedAt != nil && time.Since(*op.CoreAttemptedAt) < coreOpSubmitRetryBackoff {
			continue
		}
		if err := ss.submitCoreOp(ctx, op); err != nil {
			ss.logger.Warn("core mediorum op submit failed", zap.String("ulid", op.ULID), zap.Error(err))
		}
	}

	return nil
}

func (ss *MediorumServer) submitCoreOp(ctx context.Context, op *crudr.Op) error {
	tx, txHash, err := signedMediorumOperation(op, ss.Config.privateKey)
	now := time.Now().UTC()
	if err != nil {
		_ = ss.crud.MarkCoreError(ctx, op, txHash, now, err)
		return err
	}

	if committed, err := ss.coreTxCommitted(ctx, txHash); err == nil && committed {
		return ss.crud.MarkCoreConfirmed(ctx, op, txHash, now)
	}

	if err := ss.crud.MarkCoreAttempted(ctx, op, txHash, now); err != nil {
		return err
	}

	sendCtx, cancel := context.WithTimeout(ctx, coreOpSendTransactionLimit)
	defer cancel()
	_, err = ss.core.SendTransaction(sendCtx, connect.NewRequest(&corev1.SendTransactionRequest{
		Transaction: tx,
	}))
	if err != nil {
		_ = ss.crud.MarkCoreError(ctx, op, txHash, now, err)
		return err
	}

	return ss.crud.MarkCoreConfirmed(ctx, op, txHash, time.Now().UTC())
}

func (ss *MediorumServer) coreTxCommitted(ctx context.Context, txHash string) (bool, error) {
	_, err := ss.core.GetTransaction(ctx, connect.NewRequest(&corev1.GetTransactionRequest{
		TxHash: txHash,
	}))
	if err != nil {
		return false, err
	}
	return true, nil
}

func (ss *MediorumServer) ApplyMediorumOperation(ctx context.Context, msg *corev1.MediorumOperation, txHash string) error {
	if msg == nil {
		return fmt.Errorf("mediorum operation is nil")
	}
	if err := opvalidation.ValidateOperation(msg.GetUlid(), msg.GetHost(), msg.GetAction(), msg.GetTable(), msg.GetData()); err != nil {
		return err
	}
	op := opFromMediorumOperation(msg)
	now := time.Now().UTC()
	op.CoreTxHash = txHash
	op.CoreTxStatus = crudr.CoreTxStatusConfirmed
	op.CoreConfirmedAt = &now
	return ss.crud.ApplyOp(op)
}

func (s *StorageService) ApplyMediorumOperation(ctx context.Context, msg *corev1.MediorumOperation, txHash string) error {
	if s.mediorum == nil {
		return fmt.Errorf("mediorum not initialized")
	}
	return s.mediorum.ApplyMediorumOperation(ctx, msg, txHash)
}

func (ss *MediorumServer) applyCommittedCoreMediorumOperation(ctx context.Context, height int64, txHash string, op *corev1.MediorumOperation) error {
	if err := opvalidation.ValidateOperation(op.GetUlid(), op.GetHost(), op.GetAction(), op.GetTable(), op.GetData()); err != nil {
		logger := ss.logger
		if logger == nil {
			logger = zap.NewNop()
		}
		logger.Error("skipping invalid committed core mediorum op",
			zap.Int64("height", height),
			zap.String("tx", txHash),
			zap.String("ulid", op.GetUlid()),
			zap.Error(err))
		return nil
	}
	if err := ss.ApplyMediorumOperation(ctx, op, txHash); err != nil {
		return fmt.Errorf("apply core mediorum op at height %d tx %s: %w", height, txHash, err)
	}
	return nil
}

func (ss *MediorumServer) startCoreOpSyncer(ctx context.Context) error {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			ticker.Reset(coreOpSyncInterval)
			if err := ss.syncCoreMediorumOps(ctx); err != nil {
				ss.logger.Warn("core mediorum op sync pass failed", zap.Error(err))
			}
		}
	}
}

func (ss *MediorumServer) syncCoreMediorumOps(ctx context.Context) error {
	if ss.core == nil || !ss.core.IsReady() {
		return nil
	}

	status, err := ss.core.GetStatus(ctx, connect.NewRequest(&corev1.GetStatusRequest{}))
	if err != nil {
		return err
	}
	currentHeight := status.Msg.GetChainInfo().GetCurrentHeight()
	if currentHeight <= 0 {
		return nil
	}

	lastHeight, err := ss.getCoreOpSyncHeight(ctx, currentHeight)
	if err != nil {
		return err
	}

	for h := lastHeight + 1; h <= currentHeight; h++ {
		block, err := ss.core.GetBlock(ctx, connect.NewRequest(&corev1.GetBlockRequest{Height: h}))
		if err != nil {
			return err
		}
		if block.Msg.GetBlock().GetHeight() < 0 {
			return nil
		}
		for _, tx := range block.Msg.GetBlock().GetTransactions() {
			if tx.GetTransaction() == nil {
				continue
			}
			op := tx.GetTransaction().GetMediorumOperation()
			if op == nil {
				continue
			}
			if err := ss.applyCommittedCoreMediorumOperation(ctx, h, tx.GetHash(), op); err != nil {
				return err
			}
		}
		if err := ss.setCoreOpSyncHeight(ctx, h); err != nil {
			return err
		}
	}

	return nil
}

func (ss *MediorumServer) getCoreOpSyncHeight(ctx context.Context, currentHeight int64) (int64, error) {
	var cursor crudr.Cursor
	err := ss.crud.DB.WithContext(ctx).Where("host = ?", coreOpSyncCursorHost).First(&cursor).Error
	if err == nil {
		height, parseErr := strconv.ParseInt(cursor.LastULID, 10, 64)
		if parseErr != nil {
			return 0, parseErr
		}
		return height, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, err
	}

	start := currentHeight - coreOpSyncInitialLookback
	if start < 0 {
		start = 0
	}
	if err := ss.setCoreOpSyncHeight(ctx, start); err != nil {
		return 0, err
	}
	return start, nil
}

func (ss *MediorumServer) setCoreOpSyncHeight(ctx context.Context, height int64) error {
	return ss.crud.DB.WithContext(ctx).Save(&crudr.Cursor{
		Host:     coreOpSyncCursorHost,
		LastULID: strconv.FormatInt(height, 10),
	}).Error
}
