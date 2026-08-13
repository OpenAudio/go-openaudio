package etl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/OpenAudio/go-openaudio/pkg/etl/processors"
	em "github.com/OpenAudio/go-openaudio/pkg/etl/processors/entity_manager"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

// ChallengeStats represents storage proof challenge statistics for a validator
type ChallengeStats struct {
	ChallengesReceived int32
	ChallengesFailed   int32
}

// StorageProofState tracks storage proof challenges and their resolution
type StorageProofState struct {
	Height          int64
	Proofs          map[string]*StorageProofEntry // address -> proof entry
	ProverAddresses map[string]int                // address -> vote count for who should be provers
	Resolved        bool
}

type StorageProofEntry struct {
	Address         string
	ProverAddresses []string
	ProofSignature  []byte
	Cid             string
	SignatureValid  bool // determined during verification
}

func (e *Indexer) Run() error {
	dbUrl := e.dbURL
	if dbUrl == "" {
		return fmt.Errorf("dbUrl environment variable not set")
	}

	if !e.skipMigrations {
		err := db.RunMigrations(e.logger, dbUrl, e.runDownMigrations)
		if err != nil {
			return fmt.Errorf("error running migrations: %v", err)
		}
	}

	pgConfig, err := pgxpool.ParseConfig(dbUrl)
	if err != nil {
		return fmt.Errorf("error parsing database config: %v", err)
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), pgConfig)
	if err != nil {
		return fmt.Errorf("error creating database pool: %v", err)
	}

	e.pool = pool
	e.db = db.New(pool)

	// Initialize entity manager dispatcher and register handlers
	e.dispatcher = em.NewDispatcher(e.logger)
	if e.config.IsDataTypeEnabled(em.EntityTypeUser) {
		e.dispatcher.Register(em.UserCreate())
		e.dispatcher.Register(em.UserUpdate())
		e.dispatcher.Register(em.UserVerify())
		e.dispatcher.Register(em.MuteUser())
		e.dispatcher.Register(em.UnmuteUser())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeTrack) {
		e.dispatcher.Register(em.TrackCreate())
		e.dispatcher.Register(em.TrackUpdate())
		e.dispatcher.Register(em.TrackDelete())
		e.dispatcher.Register(em.TrackDownload())
		e.dispatcher.Register(em.TrackMute())
		e.dispatcher.Register(em.TrackUnmute())
		e.dispatcher.Register(em.TrackCollaboratorApprove())
		e.dispatcher.Register(em.TrackCollaboratorReject())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypePlaylist) {
		e.dispatcher.Register(em.PlaylistCreate())
		e.dispatcher.Register(em.PlaylistUpdate())
		e.dispatcher.Register(em.PlaylistDelete())
	}
	// Social features use wildcard entity type — actual txs carry the entity
	// being acted on (User for Follow, Track/Playlist for Save/Repost).
	e.dispatcher.Register(em.Follow())
	e.dispatcher.Register(em.Unfollow())
	e.dispatcher.Register(em.Save())
	e.dispatcher.Register(em.Unsave())
	e.dispatcher.Register(em.Repost())
	e.dispatcher.Register(em.Unrepost())
	e.dispatcher.Register(em.Subscribe())
	e.dispatcher.Register(em.Unsubscribe())
	e.dispatcher.Register(em.Share())
	if e.config.IsDataTypeEnabled(em.EntityTypeDeveloperApp) {
		e.dispatcher.Register(em.DeveloperAppCreate())
		e.dispatcher.Register(em.DeveloperAppUpdate())
		e.dispatcher.Register(em.DeveloperAppDelete())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeGrant) {
		e.dispatcher.Register(em.GrantCreate())
		e.dispatcher.Register(em.GrantDelete())
		e.dispatcher.Register(em.GrantApprove())
		e.dispatcher.Register(em.GrantReject())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeEvent) {
		e.dispatcher.Register(em.EventCreate())
		e.dispatcher.Register(em.EventUpdate())
		e.dispatcher.Register(em.EventDelete())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeAssociatedWallet) {
		e.dispatcher.Register(em.AssociatedWalletCreate())
		e.dispatcher.Register(em.AssociatedWalletDelete())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeDashboardWalletUser) {
		e.dispatcher.Register(em.DashboardWalletCreate())
		e.dispatcher.Register(em.DashboardWalletDelete())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeEncryptedEmail) {
		e.dispatcher.Register(em.EncryptedEmailCreate())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeEmailAccess) {
		e.dispatcher.Register(em.EmailAccessUpdate())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeNotification) {
		e.dispatcher.Register(em.NotificationCreate())
		e.dispatcher.Register(em.NotificationView())
		e.dispatcher.Register(em.PlaylistSeenView())
	}
	if e.config.IsDataTypeEnabled(em.EntityTypeComment) {
		e.dispatcher.Register(em.CommentCreate())
		e.dispatcher.Register(em.CommentUpdate())
		e.dispatcher.Register(em.CommentDelete())
		e.dispatcher.Register(em.CommentReact())
		e.dispatcher.Register(em.CommentUnreact())
		e.dispatcher.Register(em.CommentPin())
		e.dispatcher.Register(em.CommentUnpin())
		e.dispatcher.Register(em.CommentReport())
		e.dispatcher.Register(em.CommentMute())
		e.dispatcher.Register(em.CommentUnmute())
	}

	e.dispatcher.Register(em.PlayCountReconcile())

	// Apply any post-hooks registered by external consumers before Run().
	// Done after handlers are registered so the dispatcher is in its
	// final state when hooks attach.
	e.applyPendingPostHooks()

	// Derive the genesis migration handler set from the production one, so it
	// inherits every registration above (including post-hooks) and only the
	// handlers whose validation policy differs are replaced.
	e.migrationDispatcher = e.dispatcher.Clone()
	em.RegisterMigrationOverrides(e.migrationDispatcher)

	if e.dispatcher.HandlerCount() > 0 {
		e.logger.Info("entity manager enabled", zap.Int("handlers", e.dispatcher.HandlerCount()))
	} else {
		e.logger.Info("entity manager disabled (no data types enabled)")
	}

	// Initialize pubsub instances
	e.blockPubsub = NewPubsub[*db.EtlBlock]()
	e.playPubsub = NewPubsub[*db.EtlPlay]()

	// Initialize materialized view refresher
	e.mvRefresher = NewMaterializedViewRefresher(e.pool, e.logger)

	// Initialize scheduled release publisher (publishes scheduled tracks/albums
	// when their release_date passes).
	e.scheduledReleases = NewScheduledReleasePublisher(e.pool, e.logger)

	// Initialize chain ID from core service
	err = e.InitializeChainID(context.Background())
	if err != nil {
		e.logger.Error("error initializing chain ID", zap.Error(err))
		return fmt.Errorf("error initializing chain ID: %w", err)
	}

	// blocks.number is resolved per-block at indexing time via
	// resolveBlockNumber, which uses MAX(number)+1 in-statement. The DB is
	// the source of truth — no in-memory counter to drift from concurrent
	// writers or stale snapshots.

	e.logger.Info("starting etl service")

	if e.checkReadiness {
		err = e.awaitReadiness()
		if err != nil {
			e.logger.Error("error awaiting readiness", zap.Error(err))
		}
	}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	g, gCtx := errgroup.WithContext(ctx)

	if e.config.EnableMaterializedViewRefresh {
		g.Go(func() error {
			return e.mvRefresher.Start(gCtx)
		})
	}

	if e.config.EnableScheduledReleases {
		g.Go(func() error {
			return e.scheduledReleases.Start(gCtx)
		})
	}

	if e.config.EnablePgNotifyListener {
		g.Go(func() error {
			return e.startPgNotifyListener(gCtx)
		})
	}

	g.Go(func() error {
		// a bounded run returns nil, and an errgroup only cancels on error, so
		// without this the unconditional helpers would keep the group alive
		defer stop()
		if err := e.indexBlocks(); err != nil {
			return fmt.Errorf("error indexing blocks: %v", err)
		}

		return nil
	})

	g.Go(func() error {
		return e.syncValidatorsFromCore(gCtx)
	})

	err = g.Wait()
	// our own shutdown, not a failure
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}

func (e *Indexer) awaitReadiness() error {
	e.logger.Info("awaiting readiness")
	attempts := 0

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		attempts++
		if attempts > 60 {
			return fmt.Errorf("timed out waiting for readiness")
		}

		res, err := e.core.GetStatus(context.Background(), connect.NewRequest(&corev1.GetStatusRequest{}))
		if err != nil {
			continue
		}

		if res.Msg.Ready {
			return nil
		}
	}

	return nil
}

func (e *Indexer) indexBlocks() error {
	var blocksProcessed int64
	var txsProcessed int64
	indexStart := time.Now()
	lastLog := time.Now()

	// Determine start height (chain height, not blocks.number).
	// 1. Check etl_blocks (our own tracking table) for the last chain height we indexed.
	// 2. If empty and --start was given, use that.
	// 3. Otherwise, fall back to core_indexed_blocks which tracks what chain height
	//    the production indexer last processed.
	latestHeight, err := e.db.GetLatestIndexedBlock(context.Background())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if e.startingBlockHeight > 0 {
				latestHeight = e.startingBlockHeight - 1
			} else {
				var maxHeight *int64
				err = e.pool.QueryRow(context.Background(),
					"SELECT MAX(height) FROM core_indexed_blocks WHERE chain_id = $1",
					e.ChainID).Scan(&maxHeight)
				if err != nil || maxHeight == nil {
					latestHeight = 0
				} else {
					latestHeight = *maxHeight
					e.logger.Info("resuming from core_indexed_blocks",
						zap.String("chain_id", e.ChainID),
						zap.Int64("last_chain_height", latestHeight),
					)
				}
			}
		} else {
			return fmt.Errorf("error getting latest indexed block: %w", err)
		}
	}
	startHeight := latestHeight + 1

	// Start prefetcher goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var src blockSource
	if e.config.BlockStreamEnabled && e.streamClient != nil {
		src = newStreamSource(e.streamClient, e.logger)
		e.logger.Info("block source: gRPC stream", zap.Int64("start_height", startHeight))
	} else {
		src = newPrefetcher(e.core, e.logger)
		e.logger.Info("block source: polling prefetcher", zap.Int64("start_height", startHeight), zap.Int("buffer_size", defaultPrefetchBuffer))
	}
	go src.run(ctx, startHeight)

	for pb := range src.C() {
		block := pb.Block

		res, err := e.processBlock(ctx, pb)
		if err != nil {
			// Block-level failure rolled back the entire block's tx — no
			// partial state landed. Skip; the outer loop will reprocess
			// when the prefetcher hands us this block on a future pass
			// (or it stays unindexed if the failure is persistent and we
			// crash-restart from MAX(core_indexed_blocks.height)).
			e.logger.Error("processBlock failed",
				zap.Int64("height", block.Height),
				zap.Error(err))
			continue
		}

		blocksProcessed++
		txsProcessed += int64(len(block.Transactions))

		if time.Since(lastLog) >= 10*time.Second {
			elapsed := time.Since(indexStart).Seconds()
			blocksPerSec := float64(blocksProcessed) / elapsed
			txsPerSec := float64(txsProcessed) / elapsed
			blocksBehind := pb.CurrentHeight - block.Height

			e.logger.Info("indexing progress",
				zap.Int64("block", block.Height),
				zap.Int64("blocks_behind", blocksBehind),
				zap.Int("txs_in_block", len(block.Transactions)),
				zap.Int("em_txs", res.emTxCount),
				zap.Int("em_rejected", res.emRejectCount),
				zap.Int("em_unrouted", res.emUnroutedCount),
				zap.Duration("block_time", res.blockElapsed),
				zap.Float64("blocks_per_sec", blocksPerSec),
				zap.Float64("txs_per_sec", txsPerSec),
				zap.Int64("total_blocks", blocksProcessed),
				zap.Int64("total_txs", txsProcessed),
				zap.Int("prefetch_buffered", len(src.C())),
			)
			lastLog = time.Now()
		}

		// TODO: use pgnotify to publish block and play events to pubsub

		if e.endingBlockHeight > 0 && block.Height >= e.endingBlockHeight {
			e.logger.Info("ending block height reached, stopping etl service")
			return nil
		}
	}

	return nil
}

// blockResult carries per-block stats out of processBlock for use by the
// caller's progress logging.
type blockResult struct {
	emTxCount     int
	emRejectCount int
	// Transactions whose (entity_type, action) matched no handler. Tolerated
	// during live indexing for forward compatibility, so they are counted
	// apart from rejections rather than folded into them.
	emUnroutedCount int
	blockElapsed    time.Duration
}

// processBlock indexes one CometBFT block atomically.
//
// All writes for the block — etl_blocks, blocks (chain head row),
// core_indexed_blocks, and every entity-table write performed
// by the per-tx handlers — share a single pgx.Tx and commit together or
// roll back together. On any block-level error (e.g. failure to insert
// etl_blocks, failure to resolve blocks.number even after a retry),
// nothing for this block lands.
//
// Each individual transaction within the block runs inside its own
// SAVEPOINT (via tx.Begin). A handler's failure rolls back only that
// tx's writes; other txs in the same block continue. This matches the
// Python discovery-provider's "swallow validation errors, continue
// indexing" semantics, but does it correctly: partial writes from a
// failed handler do not leak into the committed block state.
//
// On success the caller may advance its progress counters and assume
// the block is fully indexed.
// errNeedSavepoints signals that a savepoint-free attempt hit a hard error and
// poisoned the block transaction, so the block must be replayed with per-tx
// savepoints to isolate the failure.
var errNeedSavepoints = errors.New("block needs per-tx savepoints")

// txScope is the handle one transaction's writes go through.
//
// Normally it wraps a SAVEPOINT so a failing handler rolls back only its own
// writes. During a genesis replay it wraps the block transaction directly:
// every tx in the block is a migration tx, and a SAVEPOINT per tx measured
// 55-75% overhead on an insert-plus-lookup workload. Nothing is lost by
// dropping it, because a hard error there abandons the whole block anyway.
type txScope struct {
	tx       pgx.Tx
	nested   bool
	poisoned bool
}

// Rollback undoes this transaction's writes. Without a savepoint there is
// nothing to roll back to, so it records that the block transaction is
// unusable and must be retried with savepoints.
func (s *txScope) Rollback(ctx context.Context) {
	if s.nested {
		_ = s.tx.Rollback(ctx)
		return
	}
	s.poisoned = true
}

// migrationOnlyBlock reports whether every transaction in the block is a
// genesis-migration transaction. Only those blocks take the savepoint-free
// path: a mixed or live block keeps per-tx isolation.
func migrationOnlyBlock(block *corev1.Block) bool {
	if len(block.Transactions) == 0 {
		return false
	}
	for _, t := range block.Transactions {
		if t.Transaction == nil {
			return false
		}
		if _, ok := t.Transaction.Transaction.(*corev1.SignedTransaction_ManageEntityMigration); !ok {
			return false
		}
	}
	return true
}

// processBlock indexes one block, preferring the savepoint-free path for
// genesis-migration blocks and falling back to per-tx savepoints if that
// attempt hits a hard error.
func (e *Indexer) processBlock(ctx context.Context, pb prefetchedBlock) (*blockResult, error) {
	if migrationOnlyBlock(pb.Block) {
		res, err := e.processBlockAttempt(ctx, pb, false)
		if !errors.Is(err, errNeedSavepoints) {
			return res, err
		}
		e.logger.Warn("migration block hit a hard error without savepoints; retrying with per-tx savepoints",
			zap.Int64("block", pb.Block.Height))
	}
	return e.processBlockAttempt(ctx, pb, true)
}

func (e *Indexer) processBlockAttempt(ctx context.Context, pb prefetchedBlock, useSavepoints bool) (*blockResult, error) {
	block := pb.Block
	blockStart := time.Now()
	res := &blockResult{}

	tx, err := e.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin block tx: %w", err)
	}
	defer tx.Rollback(ctx) // safe no-op after Commit

	q := e.db.WithTx(tx)

	// etl_blocks: our own per-CometBFT-block tracking row.
	if err := q.InsertBlock(ctx, db.InsertBlockParams{
		ProposerAddress: block.Proposer,
		BlockHeight:     block.Height,
		BlockTime:       pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("insert etl_blocks: %w", err)
	}

	// Decide whether this block needs a `blocks` row (only blocks with EM
	// transactions get one in the legacy schema's chain-head model).
	hasEM := false
	for _, t := range block.Transactions {
		switch t.Transaction.Transaction.(type) {
		case *corev1.SignedTransaction_ManageEntity, *corev1.SignedTransaction_ManageEntityMigration:
			hasEM = true
		}
		if hasEM {
			break
		}
	}

	var emBlock int64
	if hasEM {
		n, err := e.resolveBlockNumber(ctx, tx, block.Hash)
		if err != nil {
			// One retry for the rare race where another writer claimed
			// our MAX+1 with a different hash. Within our tx, both calls
			// see the same snapshot semantics (READ COMMITTED), so the
			// retry only helps if the conflict was with a *committed*
			// outside writer — which is what could happen while a second
			// writer (e.g. the legacy Python indexer) was still running
			// during the production cutover to the Go ETL.
			n, err = e.resolveBlockNumber(ctx, tx, block.Hash)
		}
		if err != nil {
			return nil, fmt.Errorf("resolve block number: %w", err)
		}
		emBlock = n
	}

	// core_indexed_blocks: what chain height we've indexed. em_block is
	// NULL for blocks without EM transactions.
	var emBlockParam any
	if hasEM {
		emBlockParam = emBlock
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO core_indexed_blocks (blockhash, chain_id, height, em_block)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (chain_id, height) DO UPDATE SET em_block = $4, blockhash = $1`,
		block.Hash, e.ChainID, block.Height, emBlockParam); err != nil {
		return nil, fmt.Errorf("insert core_indexed_blocks: %w", err)
	}

	// Per-transaction loop. Each tx runs in a SAVEPOINT so a handler
	// error rolls back only that tx's writes — not the whole block.
	for index, t := range block.Transactions {
		insertTxParams := db.InsertTransactionParams{
			TxHash:      t.Hash,
			BlockHeight: block.Height,
			TxIndex:     int32(index),
			TxType:      "",
			Address:     pgtype.Text{Valid: false},
			CreatedAt:   pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
		}

		scope := &txScope{tx: tx}
		if useSavepoints {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return nil, fmt.Errorf("begin savepoint for tx %s: %w", t.Hash, err)
			}
			scope = &txScope{tx: sp, nested: true}
		}
		qsp := e.db.WithTx(scope.tx)

		spOK := e.processOneTx(ctx, scope, qsp, block, emBlock, &insertTxParams, t, res)
		if scope.poisoned {
			return nil, errNeedSavepoints
		}

		// Always try to persist the transactions row, even if the
		// type-specific handler rolled back its savepoint — the
		// transactions table is an audit log of what was on-chain,
		// independent of whether handlers consumed it. If the savepoint
		// is gone (rolled back), we use the outer tx's queries handle.
		if spOK {
			if err := qsp.InsertTransaction(ctx, insertTxParams); err != nil {
				e.logger.Error("error inserting transaction",
					zap.String("tx", t.Hash), zap.Error(err))
				scope.Rollback(ctx)
				if scope.poisoned {
					return nil, errNeedSavepoints
				}
				continue
			}
			if scope.nested {
				if err := scope.tx.Commit(ctx); err != nil {
					e.logger.Error("error committing savepoint for tx",
						zap.String("tx", t.Hash), zap.Error(err))
				}
			}
		} else {
			// processOneTx already rolled back sp; record the tx row in
			// the outer tx so the audit log still reflects it.
			if err := q.InsertTransaction(ctx, insertTxParams); err != nil {
				e.logger.Error("error inserting transaction (post-rollback)",
					zap.String("tx", t.Hash), zap.Error(err))
			}
		}
	}

	res.blockElapsed = time.Since(blockStart)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit block tx: %w", err)
	}
	return res, nil
}

// processOneTx dispatches a single core transaction inside its caller-
// supplied savepoint. Returns true if the savepoint is still active for
// the caller to Commit; false if processOneTx already rolled it back due
// to a handler error.
//
// Note: ManageEntity validation errors are logged but kept on the
// savepoint (they don't roll back) because the validation-failed state
// is "this tx was on-chain but our handler chose not to act on it",
// which matches Python's swallow-and-continue behavior. Hard errors
// from a handler do trigger a rollback so partial writes don't land.
func (e *Indexer) processOneTx(
	ctx context.Context,
	scope *txScope,
	q *db.Queries,
	block *corev1.Block,
	emBlock int64,
	insertTxParams *db.InsertTransactionParams,
	t *corev1.Transaction,
	res *blockResult,
) bool {
	txCtx := &processors.TxContext{
		Block:     block,
		TxHash:    t.Hash,
		TxIndex:   int(insertTxParams.TxIndex),
		BlockTime: pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
		InsertTx:  *insertTxParams,
	}

	switch signedTx := t.Transaction.Transaction.(type) {
	case *corev1.SignedTransaction_Plays:
		pr, err := processors.Play().Process(ctx, t.Transaction, txCtx, q)
		if err != nil {
			e.logger.Error("error processing plays",
				zap.String("hash", t.Hash), zap.Error(err))
			scope.Rollback(ctx)
			return false
		}
		*insertTxParams = pr.InsertTx
		e.firePlaysHooks(ctx, scope.tx, signedTx.Plays.GetPlays(), block, t.Hash)

	case *corev1.SignedTransaction_ManageEntity:
		me := signedTx.ManageEntity
		res.emTxCount++

		pr, err := processors.ManageEntity().Process(ctx, t.Transaction, txCtx, q)
		if err != nil {
			e.logger.Error("error processing manage entity",
				zap.String("hash", t.Hash), zap.Error(err))
			scope.Rollback(ctx)
			return false
		}
		*insertTxParams = pr.InsertTx

		txStart := time.Now()
		emParams := em.NewParams(me, emBlock, block.Timestamp.AsTime(),
			block.Hash, t.Hash, scope.tx, e.logger)
		if dErr := e.dispatcher.Dispatch(ctx, emParams); dErr != nil {
			switch {
			case errors.Is(dErr, em.ErrNoHandler):
				// Forward compatibility: a newer client can emit an entity
				// type this build has no handler for, and refusing those
				// would stall a node behind the network. Tolerated as before
				// -- but counted and logged, so an entity type that was
				// supposed to route is visible instead of silently dropped.
				res.emUnroutedCount++
				e.logger.Warn("entity manager unrouted",
					zap.String("entity_type", me.GetEntityType()),
					zap.String("action", me.GetAction()),
					zap.Int64("entity_id", me.GetEntityId()),
					zap.Int64("user_id", me.GetUserId()),
					zap.String("hash", t.Hash),
				)
			case em.IsValidationError(dErr):
				// Validation errors mean "the tx was on-chain but its
				// payload was malformed, so we choose not to act on it".
				// Match Python's swallow-and-continue: keep the savepoint
				// alive so the tx's audit row still lands.
				res.emRejectCount++
				e.logger.Warn("entity manager validation rejected",
					zap.String("entity_type", me.GetEntityType()),
					zap.String("action", me.GetAction()),
					zap.Int64("entity_id", me.GetEntityId()),
					zap.Int64("user_id", me.GetUserId()),
					zap.String("reason", dErr.Error()),
					zap.String("hash", t.Hash),
				)
			default:
				// Hard error from the handler: any partial writes are
				// rolled back via the savepoint. The tx's audit row is
				// still recorded by processBlock using the outer tx.
				res.emRejectCount++
				e.logger.Error("entity manager dispatch error",
					zap.String("entity_type", me.GetEntityType()),
					zap.String("action", me.GetAction()),
					zap.Int64("entity_id", me.GetEntityId()),
					zap.Int64("user_id", me.GetUserId()),
					zap.String("hash", t.Hash),
					zap.Error(dErr),
				)
				scope.Rollback(ctx)
				return false
			}
		} else {
			// One info line per successfully indexed entity-manager row,
			// mirroring the field set of the "validation rejected" / "dispatch
			// error" logs so a single grep on hash/entity_id shows the full
			// lifecycle (indexed vs rejected vs errored) of every EM tx.
			e.logger.Info("entity manager indexed",
				zap.String("entity_type", me.GetEntityType()),
				zap.String("action", me.GetAction()),
				zap.Int64("entity_id", me.GetEntityId()),
				zap.Int64("user_id", me.GetUserId()),
				zap.Int64("block", block.Height),
				zap.String("hash", t.Hash),
				zap.Duration("elapsed", time.Since(txStart)),
			)
		}

	case *corev1.SignedTransaction_ManageEntityMigration:
		me := signedTx.ManageEntityMigration
		res.emTxCount++

		pr, err := processors.ManageEntityMigration().Process(ctx, t.Transaction, txCtx, q)
		if err != nil {
			e.logger.Error("error processing manage entity migration",
				zap.String("hash", t.Hash), zap.Error(err))
			scope.Rollback(ctx)
			return false
		}
		*insertTxParams = pr.InsertTx

		txStart := time.Now()
		emParams := em.NewParams(&corev1.ManageEntityLegacy{
			UserId:     me.GetUserId(),
			EntityType: me.GetEntityType(),
			EntityId:   me.GetEntityId(),
			Action:     me.GetAction(),
			Metadata:   me.GetMetadata(),
			Signature:  me.GetSignature(),
			Signer:     me.GetSigner(),
			Nonce:      me.GetNonce(),
		}, emBlock, migrationBlockTime(me.GetMetadata(), block.Timestamp.AsTime()),
			block.Hash, t.Hash, scope.tx, e.logger)
		// Replayed historical state goes through the migration handler set.
		if dErr := e.migrationDispatcher.Dispatch(ctx, emParams); dErr != nil {
			res.emRejectCount++
			// Unlike live indexing, an unroutable migration transaction is a
			// genesis-writer bug, not a version skew: the writer and this
			// handler set ship together. It is counted as a rejection rather
			// than tolerated, so it shows up in em_rejected instead of
			// passing for an indexed row. It is deliberately not a hard
			// error -- aborting a multi-hour replay on the first bad
			// transaction would hide how many others share the defect.
			if em.IsValidationError(dErr) || errors.Is(dErr, em.ErrNoHandler) {
				e.logger.Warn("entity manager migration validation rejected",
					zap.String("entity_type", me.GetEntityType()),
					zap.String("action", me.GetAction()),
					zap.Int64("entity_id", me.GetEntityId()),
					zap.Int64("user_id", me.GetUserId()),
					zap.String("reason", dErr.Error()),
					zap.String("hash", t.Hash),
				)
			} else {
				e.logger.Error("entity manager migration dispatch error",
					zap.String("entity_type", me.GetEntityType()),
					zap.String("action", me.GetAction()),
					zap.Int64("entity_id", me.GetEntityId()),
					zap.Int64("user_id", me.GetUserId()),
					zap.String("hash", t.Hash),
					zap.Error(dErr),
				)
				scope.Rollback(ctx)
				return false
			}
		} else {
			e.logger.Info("entity manager migration indexed",
				zap.String("entity_type", me.GetEntityType()),
				zap.String("action", me.GetAction()),
				zap.Int64("entity_id", me.GetEntityId()),
				zap.Int64("user_id", me.GetUserId()),
				zap.Int64("block", block.Height),
				zap.String("hash", t.Hash),
				zap.Duration("elapsed", time.Since(txStart)),
			)
		}

	case *corev1.SignedTransaction_ValidatorRegistration:
		insertTxParams.TxType = TxTypeValidatorRegistrationLegacy
		// Legacy validator registration - no specific table insert needed

	case *corev1.SignedTransaction_ValidatorDeregistration:
		insertTxParams.TxType = TxTypeValidatorMisbehaviorDereg
		vd := signedTx.ValidatorDeregistration
		insertTxParams.Address = pgtype.Text{String: vd.CometAddress, Valid: true}
		if err := q.InsertValidatorMisbehaviorDeregistration(ctx, db.InsertValidatorMisbehaviorDeregistrationParams{
			CometAddress: vd.CometAddress,
			PubKey:       vd.PubKey,
			BlockHeight:  block.Height,
			TxHash:       t.Hash,
			CreatedAt:    pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
		}); err != nil {
			e.logger.Error("error inserting validator misbehavior deregistration",
				zap.String("hash", t.Hash), zap.Error(err))
			scope.Rollback(ctx)
			return false
		}

	case *corev1.SignedTransaction_SlaRollup:
		insertTxParams.TxType = TxTypeSlaRollup
		sr := signedTx.SlaRollup

		validatorCount := int32(len(sr.Reports))
		var blockQuota int32
		if sr.BlockEnd > sr.BlockStart && validatorCount > 0 {
			blockQuota = int32(sr.BlockEnd-sr.BlockStart) / validatorCount
		}

		blockRange := sr.BlockEnd - sr.BlockStart
		var bps, tps float64
		if blockRange > 0 {
			txCount := int64(0)
			for blockHeight := sr.BlockStart; blockHeight <= sr.BlockEnd; blockHeight++ {
				blockTxCount, err := q.GetBlockTransactionCount(ctx, blockHeight)
				if err != nil {
					e.logger.Debug("failed to get transaction count for block",
						zap.Int64("height", blockHeight), zap.Error(err))
					continue
				}
				txCount += blockTxCount
			}

			rollupTime := sr.Timestamp.AsTime()
			var duration float64
			if latestRollup, err := q.GetLatestSlaRollup(ctx); err == nil {
				if latestRollup.CreatedAt.Valid {
					duration = rollupTime.Sub(latestRollup.CreatedAt.Time).Seconds()
				}
			}
			if duration <= 0 {
				duration = float64(blockRange) * 2.0
			}
			if duration > 0 {
				bps = float64(blockRange) / duration
				tps = float64(txCount) / duration
			}
		}

		rollupId, err := q.InsertSlaRollupReturningId(ctx, db.InsertSlaRollupReturningIdParams{
			BlockStart:     sr.BlockStart,
			BlockEnd:       sr.BlockEnd,
			BlockHeight:    block.Height,
			ValidatorCount: validatorCount,
			BlockQuota:     blockQuota,
			Bps:            bps,
			Tps:            tps,
			TxHash:         t.Hash,
			CreatedAt:      pgtype.Timestamp{Time: sr.Timestamp.AsTime(), Valid: true},
		})
		if err != nil {
			e.logger.Error("error inserting SLA rollup",
				zap.String("hash", t.Hash), zap.Error(err))
			scope.Rollback(ctx)
			return false
		}

		challengeStats, err := e.calculateChallengeStatistics(ctx, q, sr.BlockStart, sr.BlockEnd)
		if err != nil {
			e.logger.Error("error calculating challenge statistics",
				zap.String("hash", t.Hash), zap.Error(err))
			challengeStats = make(map[string]ChallengeStats)
		}

		for _, report := range sr.Reports {
			stats := challengeStats[report.Address]
			if err := q.InsertSlaNodeReport(ctx, db.InsertSlaNodeReportParams{
				SlaRollupID:        rollupId,
				Address:            report.Address,
				NumBlocksProposed:  report.NumBlocksProposed,
				ChallengesReceived: stats.ChallengesReceived,
				ChallengesFailed:   stats.ChallengesFailed,
				BlockHeight:        block.Height,
				TxHash:             t.Hash,
				CreatedAt:          pgtype.Timestamp{Time: sr.Timestamp.AsTime(), Valid: true},
			}); err != nil {
				e.logger.Error("error inserting SLA node report",
					zap.String("hash", t.Hash),
					zap.String("address", report.Address),
					zap.Error(err))
				scope.Rollback(ctx)
				return false
			}
		}

	case *corev1.SignedTransaction_StorageProof:
		insertTxParams.TxType = TxTypeStorageProof
		spx := signedTx.StorageProof
		insertTxParams.Address = pgtype.Text{String: spx.Address, Valid: true}
		if err := q.InsertStorageProof(ctx, db.InsertStorageProofParams{
			Height:          spx.Height,
			Address:         spx.Address,
			ProverAddresses: spx.ProverAddresses,
			Cid:             spx.Cid,
			ProofSignature:  spx.ProofSignature,
			Proof:           nil, // Will be set during verification
			Status:          "unresolved",
			BlockHeight:     block.Height,
			TxHash:          t.Hash,
			CreatedAt:       pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
		}); err != nil {
			e.logger.Error("error inserting storage proof",
				zap.String("hash", t.Hash), zap.Error(err))
			scope.Rollback(ctx)
			return false
		}

	case *corev1.SignedTransaction_StorageProofVerification:
		insertTxParams.TxType = TxTypeStorageProofVerification
		spv := signedTx.StorageProofVerification
		if err := q.InsertStorageProofVerification(ctx, db.InsertStorageProofVerificationParams{
			Height:      spv.Height,
			Proof:       spv.Proof,
			BlockHeight: block.Height,
			TxHash:      t.Hash,
			CreatedAt:   pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
		}); err != nil {
			e.logger.Error("error inserting storage proof verification",
				zap.String("hash", t.Hash), zap.Error(err))
			scope.Rollback(ctx)
			return false
		}
		if err := e.processStorageProofConsensus(ctx, q, spv.Height, spv.Proof,
			block.Height, t.Hash, block.Timestamp.AsTime()); err != nil {
			e.logger.Error("error processing storage proof consensus",
				zap.String("hash", t.Hash), zap.Error(err))
			scope.Rollback(ctx)
			return false
		}

	case *corev1.SignedTransaction_Attestation:
		at := signedTx.Attestation
		if vr := at.GetValidatorRegistration(); vr != nil {
			insertTxParams.TxType = TxTypeValidatorRegistration
			insertTxParams.Address = pgtype.Text{String: vr.DelegateWallet, Valid: true}
			if err := q.InsertValidatorRegistration(ctx, db.InsertValidatorRegistrationParams{
				Address:      vr.DelegateWallet,
				Endpoint:     vr.Endpoint,
				CometAddress: vr.CometAddress,
				EthBlock:     fmt.Sprintf("%d", vr.EthBlock),
				NodeType:     vr.NodeType,
				Spid:         vr.SpId,
				CometPubkey:  vr.PubKey,
				VotingPower:  vr.Power,
				BlockHeight:  block.Height,
				TxHash:       t.Hash,
			}); err != nil {
				e.logger.Error("error inserting validator registration",
					zap.String("hash", t.Hash), zap.Error(err))
				scope.Rollback(ctx)
				return false
			}
			if err := q.RegisterValidator(ctx, db.RegisterValidatorParams{
				Address:        vr.DelegateWallet,
				Endpoint:       vr.Endpoint,
				CometAddress:   vr.CometAddress,
				NodeType:       vr.NodeType,
				Spid:           vr.SpId,
				VotingPower:    vr.Power,
				Status:         "active",
				RegisteredAt:   block.Height,
				DeregisteredAt: pgtype.Int8{Valid: false},
				CreatedAt:      pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
				UpdatedAt:      pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
			}); err != nil {
				e.logger.Error("error registering validator",
					zap.String("hash", t.Hash), zap.Error(err))
				scope.Rollback(ctx)
				return false
			}
		}
		if vd := at.GetValidatorDeregistration(); vd != nil {
			insertTxParams.TxType = TxTypeValidatorDeregistration
			insertTxParams.Address = pgtype.Text{String: vd.CometAddress, Valid: true}
			if err := q.InsertValidatorDeregistration(ctx, db.InsertValidatorDeregistrationParams{
				CometAddress: vd.CometAddress,
				CometPubkey:  vd.PubKey,
				BlockHeight:  block.Height,
				TxHash:       t.Hash,
			}); err != nil {
				e.logger.Error("error inserting validator deregistration",
					zap.String("hash", t.Hash), zap.Error(err))
				scope.Rollback(ctx)
				return false
			}
			if err := q.DeregisterValidator(ctx, db.DeregisterValidatorParams{
				DeregisteredAt: pgtype.Int8{Int64: block.Height, Valid: true},
				UpdatedAt:      pgtype.Timestamp{Time: block.Timestamp.AsTime(), Valid: true},
				Status:         "deregistered",
				CometAddress:   vd.CometAddress,
			}); err != nil {
				e.logger.Error("error deregistering validator",
					zap.String("hash", t.Hash), zap.Error(err))
				scope.Rollback(ctx)
				return false
			}
		}
	}

	return true
}

// firePlaysHooks invokes every registered PlaysHook for a Plays transaction
// that the play processor just wrote. Each hook runs in a nested savepoint
// under sp, so a hook's writes land atomically with the etl_plays rows when
// successful, but a hook DB error can be rolled back without poisoning sp.
//
// Errors are logged but not propagated: a hook failure must not roll back
// the savepoint or fail the block. This mirrors the em.PostHook contract —
// a buggy consumer-side hook (or a deterministic bad-data error) should not
// be able to halt the indexer. No-ops when no hooks are registered.
func (e *Indexer) firePlaysHooks(ctx context.Context, sp pgx.Tx, plays []*corev1.TrackPlay, block *corev1.Block, txHash string) {
	if len(e.playsHooks) == 0 {
		return
	}
	params := &PlaysParams{
		Plays:       plays,
		BlockHeight: block.Height,
		BlockTime:   block.Timestamp.AsTime(),
		BlockHash:   block.Hash,
		TxHash:      txHash,
		Logger:      e.logger,
	}
	for _, hook := range e.playsHooks {
		hookTx, err := sp.Begin(ctx)
		if err != nil {
			e.logger.Error("begin plays hook savepoint",
				zap.String("hash", txHash), zap.Error(err))
			continue
		}

		hookParams := *params
		hookParams.DBTX = hookTx

		if err := hook(ctx, &hookParams); err != nil {
			_ = hookTx.Rollback(ctx)
			e.logger.Error("plays hook error",
				zap.String("hash", txHash), zap.Error(err))
			continue
		}
		if err := hookTx.Commit(ctx); err != nil {
			_ = hookTx.Rollback(ctx)
			e.logger.Error("commit plays hook savepoint",
				zap.String("hash", txHash), zap.Error(err))
		}
	}
}

// resolveBlockNumber returns the blocks.number for blockHash, inserting a
// blocks row if needed, all within the caller-supplied transaction.
//
// Idempotent and tolerant of co-existing writers (e.g. the legacy Python
// indexer, which ran alongside the Go ETL during the production cutover):
// if a row for blockHash already exists, its number is adopted. If not,
// a new row is inserted at MAX(number)+1
// computed in-statement. The DB is the source of truth; no in-memory
// counter to drift from concurrent writers or stale snapshots.
//
// On a number-collision race with another writer (our INSERT no-op'd
// because some other hash claimed MAX+1 first), the final SELECT returns
// pgx.ErrNoRows wrapped in the returned error. The caller retries — the
// next attempt sees a higher MAX and proceeds.
//
// Operates on the caller's tx (or savepoint) so the block-row work
// commits atomically with the rest of the block's entity writes.
func (e *Indexer) resolveBlockNumber(ctx context.Context, tx pgx.Tx, blockHash string) (int64, error) {
	// Insert after the highest-numbered block. If a row for blockHash
	// already exists (we're re-processing, or another writer beat us), or if
	// another writer claimed the next number with a different hash,
	// ON CONFLICT silently no-ops. The subsequent SELECT then decides what
	// happened.
	if _, err := tx.Exec(ctx, `
		INSERT INTO blocks (blockhash, parenthash, number)
		SELECT $1, prev.blockhash, COALESCE(prev.number, 0) + 1
		FROM (SELECT 1) seed
		LEFT JOIN LATERAL (
			SELECT blockhash, number
			FROM blocks
			ORDER BY number DESC
			LIMIT 1
		) prev ON true
		ON CONFLICT DO NOTHING`,
		blockHash); err != nil {
		return 0, fmt.Errorf("insert block: %w", err)
	}

	var num int64
	err := tx.QueryRow(ctx,
		"SELECT number FROM blocks WHERE blockhash = $1", blockHash).Scan(&num)
	if err != nil {
		// No row for our hash: we lost a (number) race with a different
		// hash and our INSERT was the one ON CONFLICT skipped. Surface so
		// the caller retries with a higher MAX.
		return 0, fmt.Errorf("block %s not present after insert (number race): %w",
			blockHash, err)
	}

	return num, nil
}

func (e *Indexer) startPgNotifyListener(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, e.dbURL)
	if err != nil {
		return fmt.Errorf("failed to connect for notifications: %w", err)
	}
	defer conn.Close(ctx)

	// Listen to both channels
	_, err = conn.Exec(ctx, "LISTEN new_block")
	if err != nil {
		return fmt.Errorf("failed to listen to new_block: %w", err)
	}

	_, err = conn.Exec(ctx, "LISTEN new_plays")
	if err != nil {
		return fmt.Errorf("failed to listen to new_plays: %w", err)
	}

	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("error waiting for notification: %w", err)
		}

		switch notification.Channel {
		case "new_block":
			block := &db.EtlBlock{}
			err = json.Unmarshal([]byte(notification.Payload), block)
			if err != nil {
				e.logger.Error("error unmarshalling block", zap.Error(err))
				continue
			}
			if e.blockPubsub.HasSubscribers(BlockTopic) {
				e.blockPubsub.Publish(context.Background(), BlockTopic, block)
			}
		case "new_plays":
			play := &db.EtlPlay{}
			err = json.Unmarshal([]byte(notification.Payload), play)
			if err != nil {
				e.logger.Error("error unmarshalling play", zap.Error(err))
				continue
			}
			if e.playPubsub.HasSubscribers(PlayTopic) {
				e.playPubsub.Publish(context.Background(), PlayTopic, play)
			}
		}
	}
}

// calculateChallengeStatistics aggregates storage proof challenge data for validators within a block range
// NOTE: This function may be called before all storage proof data for the block range is available,
// leading to potentially inaccurate pre-calculated statistics. Consider calculating these dynamically
// in the UI instead of storing them in the database.
// calculateChallengeStatistics reads challenge stats over a block range
// using the caller's tx-scoped queries handle, so its reads see the
// in-progress block's writes.
func (e *Indexer) calculateChallengeStatistics(ctx context.Context, q *db.Queries, blockStart, blockEnd int64) (map[string]ChallengeStats, error) {
	stats := make(map[string]ChallengeStats)

	// Use the ETL database method to get challenge statistics with proper status tracking
	results, err := q.GetChallengeStatisticsForBlockRange(ctx, db.GetChallengeStatisticsForBlockRangeParams{
		Height:   blockStart,
		Height_2: blockEnd,
	})
	if err != nil {
		return stats, fmt.Errorf("error querying challenge statistics: %v", err)
	}

	// Convert results to our ChallengeStats map
	for _, result := range results {
		stats[result.Address] = ChallengeStats{
			ChallengesReceived: int32(result.ChallengesReceived),
			ChallengesFailed:   int32(result.ChallengesFailed),
		}
	}

	return stats, nil
}

// processStorageProofConsensus reads and writes storage proof state using
// the caller's tx-scoped queries handle.
func (e *Indexer) processStorageProofConsensus(ctx context.Context, q *db.Queries, height int64, proof []byte, blockHeight int64, txHash string, blockTime time.Time) error {
	// Get all storage proofs for this height
	storageProofs, err := q.GetStorageProofsForHeight(ctx, height)
	if err != nil {
		return fmt.Errorf("error getting storage proofs for height %d: %v", height, err)
	}

	if len(storageProofs) == 0 {
		// No storage proofs submitted for this height
		return nil
	}

	// In the ETL context, we can't do cryptographic verification like the core system does,
	// but we can implement simplified consensus logic based on majority agreement.

	// Count consensus on who the expected provers were
	expectedProvers := make(map[string]int)
	for _, sp := range storageProofs {
		for _, proverAddr := range sp.ProverAddresses {
			expectedProvers[proverAddr]++
		}
	}

	// Determine majority threshold (more than half of submitted proofs)
	majorityThreshold := len(storageProofs) / 2

	// Mark proofs as 'pass' if they submitted and were part of majority consensus
	passedProvers := make(map[string]bool)
	for _, sp := range storageProofs {
		if sp.Address != "" && sp.ProofSignature != nil {
			// This prover submitted a proof - mark as passed
			err = q.UpdateStorageProofStatus(ctx, db.UpdateStorageProofStatusParams{
				Status:  "pass",
				Proof:   proof,
				Height:  height,
				Address: sp.Address,
			})
			if err != nil {
				e.logger.Error("error updating storage proof status to pass", zap.Error(err))
			} else {
				passedProvers[sp.Address] = true
			}
		}
	}

	// Insert failed storage proofs for validators who were expected by majority but didn't submit
	for expectedProver, voteCount := range expectedProvers {
		if voteCount > majorityThreshold && !passedProvers[expectedProver] {
			// This validator was expected by majority consensus but didn't submit a proof
			err = q.InsertFailedStorageProof(ctx, db.InsertFailedStorageProofParams{
				Height:      height,
				Address:     expectedProver,
				BlockHeight: blockHeight,
				TxHash:      txHash,
				CreatedAt:   pgtype.Timestamp{Time: blockTime, Valid: true},
			})
			if err != nil {
				e.logger.Error("error inserting failed storage proof for", zap.String("address", expectedProver), zap.Error(err))
			}
		}
	}

	e.logger.Debug(
		"Processed storage proof consensus",
		zap.Int64("height", height),
		zap.Int("passed", len(passedProvers)),
		zap.Int("expected", len(expectedProvers)),
	)

	return nil
}

// migrationBlockTime returns the timestamp a replayed entity should be recorded
// with. For live indexing the block time is when the action happened, so every
// handler uses params.BlockTime for created_at/updated_at. For a genesis replay
// that is the migration date, which collapsed the entire history onto a single
// day: users, tracks, playlists, comments, shares and all four social tables
// showed 2019-2026 in the source and one date in the replayed database.
//
// genesis-writer already sends the original timestamp; nothing consumed it.
// Overriding the block time here fixes every entity at once rather than
// threading a parameter through ~65 call sites, and it keeps production
// untouched — only the migration branch calls this.
//
// It also corrects validation that compares against "now". event_create.go
// rejects an end_date before params.BlockTime; judged against the migration
// date every historical event is expired, which is why all 106 Event
// transactions were rejected. Judged against its own creation time, an event
// that was valid when created stays valid.
//
// The timestamp may sit at the top level or inside the "data" envelope
// depending on the entity, so both are checked. Anything unparseable falls
// back to the block time, which is the current behaviour.
func migrationBlockTime(metadata string, fallback time.Time) time.Time {
	if metadata == "" {
		return fallback
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(metadata), &m); err != nil {
		return fallback
	}
	if inner, ok := m["data"].(map[string]any); ok {
		if ts, ok := parseMigrationTimestamp(inner["created_at"]); ok {
			return ts
		}
	}
	if ts, ok := parseMigrationTimestamp(m["created_at"]); ok {
		return ts
	}
	return fallback
}

func parseMigrationTimestamp(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999", "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}
