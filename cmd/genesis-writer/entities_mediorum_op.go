package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/httputil"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/opvalidation"
	mediorumserver "github.com/OpenAudio/go-openaudio/pkg/mediorum/server"
	"github.com/google/uuid"
	"github.com/oklog/ulid/v2"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// writeMediorumOps seeds the new chain with mediorum's replicated storage state
// as MediorumOperation transactions.
//
// Why current table state rather than the ops log: every crudr op carries the
// *complete* record (crudr.jsonArrayMarshal marshals the whole struct), and
// ActionUpdate applies it as a full-record upsert with no version check. The
// final state of each record is therefore fully determined by its last op, so
// replaying the entire op log would write orders of magnitude more chain data
// to reach an identical result. Reading the tables directly also handles
// deletes for free — a deleted record simply isn't there.
//
// Each row becomes a single "create" op. On a node that already holds the
// record this is a no-op (ApplyOp creates with OnConflict DoNothing), so
// seeding is safe for existing operators as well as fresh nodes.
//
// This matters because core is becoming the only crudr transport. The peer
// sweep is what backfills a new node today; the core op syncer starts at
// currentHeight - coreOpSyncInitialLookback and never walks back further, so
// without this step a node joining the new chain would start with empty
// uploads/qm_audio_analyses/audio_previews and nothing would ever fill them.
func (w *Writer) writeMediorumOps(ctx context.Context) error {
	if w.cfg.MediorumDSN == "" {
		w.logger.Info("no --mediorum-dsn provided, skipping mediorum ops")
		return nil
	}

	host := httputil.RemoveTrailingSlash(strings.ToLower(w.cfg.MediorumHost))
	if host == "" {
		return fmt.Errorf("--mediorum-host is required when --mediorum-dsn is set")
	}

	db, err := gorm.Open(postgres.Open(w.cfg.MediorumDSN), &gorm.Config{
		Logger: gormlogger.Discard,
	})
	if err != nil {
		return fmt.Errorf("connect mediorum db: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("mediorum sql db: %w", err)
	}
	defer sqlDB.Close() //nolint:errcheck

	// storage_and_db_sizes is deliberately excluded. It is per-host disk
	// telemetry that every node rewrites within minutes of coming up, so
	// seeding historical values into genesis buys nothing.
	if err := seedMediorumTable(ctx, w, db, host, func(u mediorumserver.Upload) string { return u.ID }); err != nil {
		return err
	}
	if err := seedMediorumTable(ctx, w, db, host, func(q mediorumserver.QmAudioAnalysis) string { return q.CID }); err != nil {
		return err
	}
	if err := seedMediorumTable(ctx, w, db, host, func(a mediorumserver.AudioPreview) string { return a.CID }); err != nil {
		return err
	}

	return nil
}

// seedMediorumTable streams one crudr-registered table and emits a create op
// per row. Rows are emitted sequentially: ApplyOp's update path is a blind
// last-writer-wins upsert, so ordering is load-bearing for crudr in general and
// sequential emission keeps this step consistent with that contract.
func seedMediorumTable[T any](
	ctx context.Context,
	w *Writer,
	db *gorm.DB,
	host string,
	keyOf func(T) string,
) error {
	var model T
	table := mediorumTableName(model)

	batchSize := w.cfg.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}

	var total int64
	if err := db.WithContext(ctx).Model(&model).Count(&total).Error; err != nil {
		return fmt.Errorf("count %s: %w", table, err)
	}
	if total == 0 {
		w.logger.Info("no rows", zap.String("mediorum_table", table))
		return nil
	}
	w.logger.Info("seeding mediorum table", zap.String("mediorum_table", table), zap.Int64("total", total))

	var emitted, skippedInvalid, skippedOversize int64

	// All three models have a primary key (Upload.ID, and CID on the other
	// two), so FindInBatches paginates by key rather than by offset.
	batch := make([]T, 0, batchSize)
	res := db.WithContext(ctx).Model(&model).FindInBatches(&batch, batchSize, func(tx *gorm.DB, _ int) error {
		for i := range batch {
			if err := ctx.Err(); err != nil {
				return err
			}

			data, err := json.Marshal([]T{batch[i]})
			if err != nil {
				return fmt.Errorf("marshal %s row: %w", table, err)
			}

			op := &corev1.MediorumOperation{
				Ulid:   w.seedOpULID(table, keyOf(batch[i])),
				Host:   host,
				Action: crudr.ActionCreate,
				Table:  table,
				Data:   data,
			}

			// Mirror the checks the chain and the mediorum syncer apply, so a
			// bad row is dropped here with a count rather than silently
			// discarded by every node at apply time.
			if err := opvalidation.ValidateCorePayloadSize(op.Data); err != nil {
				skippedOversize++
				continue
			}
			if err := opvalidation.ValidateOperation(op.Ulid, op.Host, op.Action, op.Table, op.Data); err != nil {
				w.logger.Warn("skipping invalid mediorum row",
					zap.String("mediorum_table", table),
					zap.String("key", keyOf(batch[i])),
					zap.Error(err))
				skippedInvalid++
				continue
			}

			txBytes, err := w.signMediorumOperation(op)
			if err != nil {
				return err
			}
			if err := w.addTx(ctx, txBytes); err != nil {
				return fmt.Errorf("emit %s op: %w", table, err)
			}
			emitted++
		}

		if emitted%100000 == 0 && emitted > 0 {
			w.logger.Info("progress", zap.String("mediorum_table", table), zap.Int64("emitted", emitted), zap.Int64("total", total))
		}
		return nil
	})
	if res.Error != nil {
		return fmt.Errorf("scan %s: %w", table, res.Error)
	}

	w.logger.Info("done",
		zap.String("mediorum_table", table),
		zap.Int64("emitted", emitted),
		zap.Int64("skipped_oversize", skippedOversize),
		zap.Int64("skipped_invalid", skippedInvalid),
	)
	return nil
}

// mediorumTableName resolves the table name for a model the same way crudr
// does when it mints an op (gorm's default naming strategy over the struct
// name), which is also how opvalidation keys its registered-type map. Deriving
// it identically means the seeded table name agrees with both by construction.
// None of these models override TableName.
func mediorumTableName(model any) string {
	return schema.NamingStrategy{}.TableName(reflect.TypeOf(model).Name())
}

// seedOpULID derives a stable ULID from the table and record key so that a
// resumed or re-run migration produces the same op identity. Without this a
// second run would mint fresh ULIDs and append duplicate rows to every node's
// ops table.
func (w *Writer) seedOpULID(table, key string) string {
	sum := sha256.Sum256([]byte(table + "\x00" + key))
	var id ulid.ULID
	// SetTime only fails past the 48-bit millisecond ceiling (year 10889) and
	// SetEntropy only on a short slice; 10 bytes is exactly the width.
	_ = id.SetTime(ulid.Timestamp(w.cfg.GenesisTime))
	_ = id.SetEntropy(sum[:10])
	return id.String()
}

// signMediorumOperation signs the op with the genesis migration key and
// marshals the wrapping SignedTransaction, matching the body bytes that
// mediorum's own submitter signs (proto.Marshal of the operation).
//
// The recovered signer is the migration authority, not the node registered for
// op.Host, so these transactions would not pass isValidMediorumOperationTx if
// they were submitted live. That is the same trade the writer already makes for
// ManageEntityLegacyMigration: genesis blocks are written straight to the core
// tables and blockstore without going through ABCI, and mediorum's syncer
// shape-validates committed ops rather than re-checking signatures. The
// signature is retained so the transaction is well-formed and the migration
// authority is auditable.
func (w *Writer) signMediorumOperation(op *corev1.MediorumOperation) ([]byte, error) {
	bodyBytes, err := proto.Marshal(op)
	if err != nil {
		return nil, fmt.Errorf("marshal mediorum operation: %w", err)
	}
	sig, err := common.EthSign(w.privKey, bodyBytes)
	if err != nil {
		return nil, fmt.Errorf("sign mediorum operation: %w", err)
	}

	stx := &corev1.SignedTransaction{
		Signature: sig,
		RequestId: uuid.NewString(),
		Transaction: &corev1.SignedTransaction_MediorumOperation{
			MediorumOperation: op,
		},
	}

	opts := w.marshalPool.Get().(proto.MarshalOptions)
	txBytes, err := opts.Marshal(stx)
	w.marshalPool.Put(opts)
	if err != nil {
		return nil, fmt.Errorf("marshal signed mediorum operation: %w", err)
	}
	return txBytes, nil
}
