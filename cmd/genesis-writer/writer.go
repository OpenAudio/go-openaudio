package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corecfg "github.com/OpenAudio/go-openaudio/pkg/core/config"
	coredb "github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/core/server"
	cmtproto "github.com/cometbft/cometbft/api/cometbft/types/v1"
	cmtapiversion "github.com/cometbft/cometbft/api/cometbft/version/v1"
	dbm "github.com/cometbft/cometbft-db"
	cmtcrypto "github.com/cometbft/cometbft/crypto"
	cmtstore "github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
	cmtversion "github.com/cometbft/cometbft/version"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/lib/pq"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// WriterConfig holds all configuration for the genesis writer.
type WriterConfig struct {
	SrcDSN               string
	DstDSN               string
	PrivKey              *ecdsa.PrivateKey
	Network              string
	ChainID              string
	GenesisTime          time.Time
	GenesisFile          string
	PrivValidatorKeyFile string
	CMTHome              string
	MaxTxsPerBlock       int
	BatchSize            int
	SkipUsers            bool
	SkipWallets          bool
	SkipTracks           bool
	SkipPlaylists        bool
	SkipSocial           bool
	SkipPlays            bool
	SkipApps             bool
	SkipComments         bool
	SkipEmails           bool
	SkipTipReactions     bool
	// RunMigrations applies the Core chain schema to DstDSN before writing.
	// Useful when starting from a fresh database (e.g., in integration tests).
	RunMigrations bool
	// Resume picks up from the last completed step if a previous run was interrupted.
	Resume bool
}

// Writer reads Audius DP entities, signs them, and writes real CometBFT blocks
// directly to the Core chain PostgreSQL tables and blockstore.db.
type Writer struct {
	cfg        *WriterConfig
	srcDB      *pgxpool.Pool
	dstDB      *pgxpool.Pool
	sigCfg     *corecfg.Config
	privKey    *ecdsa.PrivateKey
	signerAddr string
	nonce      atomic.Uint64
	logger     *zap.Logger

	// current block being assembled
	height    int64
	blockTxs  [][]byte
	blockTime time.Time

	// block signing state — constant across all blocks
	cmtPrivKey     cmtcrypto.PrivKey // validator ed25519 key
	proposerAddr   cmttypes.Address
	validatorsHash []byte
	nextValHash    []byte
	consensusHash  []byte

	// per-block chain state — updated after each flush
	prevBlockID cmttypes.BlockID // BlockID of the last written block
	lastCommit  *cmttypes.Commit // commit over the last written block
	prevAppHash []byte           // appHash of last block (→ current block's Header.AppHash)

	// blockstore — open throughout the write phase when CMTHome is set
	blockStore *cmtstore.BlockStore
	bsDB       dbm.DB

	// running totals
	totalTxs    int64
	totalBlocks int64

	// final block state — set after the last flushBlock
	finalHeight  int64
	finalAppHash []byte
	finalTime    time.Time

	// async block writer pipeline
	blockWriteCh   chan pendingBlock
	blockWriteErr  atomic.Pointer[error]
	blockWriteDone chan struct{}

	// proto marshal buffer pool (reduces GC pressure)
	marshalPool sync.Pool

	// blockMu serializes addTx/flushBlock access so emit can be called concurrently.
	blockMu sync.Mutex
}

// pendingBlock holds a fully-built block ready for async DB+blockstore write.
type pendingBlock struct {
	height     int64
	blockTime  time.Time
	appHash    []byte
	hashHex    string
	txData     []txRow // pre-computed tx hashes and bytes
	block      *cmttypes.Block
	blockParts *cmttypes.PartSet
	seenCommit *cmttypes.Commit
}

// txRow holds pre-computed data for a single transaction insert.
type txRow struct {
	hash    string
	txBytes []byte
}

// NewWriter connects to both databases and returns a ready Writer.
func NewWriter(cfg *WriterConfig, logger *zap.Logger) (*Writer, error) {
	if cfg.RunMigrations {
		if err := coredb.RunMigrations(logger, cfg.DstDSN, false); err != nil {
			return nil, fmt.Errorf("run migrations: %w", err)
		}
	}

	srcDB, err := pgxpool.New(context.Background(), cfg.SrcDSN)
	if err != nil {
		return nil, fmt.Errorf("connect src db: %w", err)
	}
	if err := srcDB.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("ping src db: %w", err)
	}

	dstDB, err := pgxpool.New(context.Background(), cfg.DstDSN)
	if err != nil {
		return nil, fmt.Errorf("connect dst db: %w", err)
	}
	if err := dstDB.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("ping dst db: %w", err)
	}

	sigCfg := signingConfig(cfg.Network)
	addr := crypto.PubkeyToAddress(cfg.PrivKey.PublicKey)

	w := &Writer{
		cfg:        cfg,
		srcDB:      srcDB,
		dstDB:      dstDB,
		sigCfg:     sigCfg,
		privKey:    cfg.PrivKey,
		signerAddr: addr.Hex(),
		logger:     logger,
		height:     1,
		blockTime:  cfg.GenesisTime,
		marshalPool: sync.Pool{
			New: func() interface{} {
				return proto.MarshalOptions{}
			},
		},
	}

	if err := w.initBlockSigning(); err != nil {
		srcDB.Close()
		dstDB.Close()
		return nil, fmt.Errorf("init block signing: %w", err)
	}

	logger.Info("genesis writer initialized",
		zap.String("genesis_keypair", addr.Hex()),
		zap.String("network", cfg.Network),
		zap.String("chain_id", cfg.ChainID),
		zap.Int("max_txs_per_block", cfg.MaxTxsPerBlock),
	)

	return w, nil
}

// Close releases database connections and the blockstore.
func (w *Writer) Close() {
	w.srcDB.Close()
	w.dstDB.Close()
	if w.bsDB != nil {
		w.bsDB.Close()
	}
}

// startBlockWriter launches the async block writer goroutine.
// Blocks are sent to blockWriteCh and written to postgres+blockstore in the background.
func (w *Writer) startBlockWriter(ctx context.Context) {
	w.blockWriteCh = make(chan pendingBlock, 4)
	w.blockWriteDone = make(chan struct{})
	go func() {
		defer close(w.blockWriteDone)
		for pb := range w.blockWriteCh {
			if err := w.writeBlockToDB(ctx, pb); err != nil {
				w.blockWriteErr.Store(&err)
				// Drain remaining blocks to unblock senders.
				for range w.blockWriteCh {
				}
				return
			}
		}
	}()
}

// stopBlockWriter closes the write channel and waits for the writer to finish.
// Returns any error from the background writer.
func (w *Writer) stopBlockWriter() error {
	if w.blockWriteCh != nil {
		close(w.blockWriteCh)
		<-w.blockWriteDone
		w.blockWriteCh = nil
	}
	if ep := w.blockWriteErr.Load(); ep != nil {
		return *ep
	}
	return nil
}

// writeBlockToDB writes a single block to postgres (using COPY for transactions)
// and blockstore.db. Postgres is committed first so that on failure the blockstore
// doesn't contain blocks that aren't in the SQL tables.
func (w *Writer) writeBlockToDB(ctx context.Context, pb pendingBlock) error {
	pgTx, err := w.dstDB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx height=%d: %w", pb.height, err)
	}
	defer pgTx.Rollback(ctx) //nolint:errcheck

	// Insert block header.
	_, err = pgTx.Exec(ctx,
		`INSERT INTO core_blocks (height, chain_id, hash, proposer, created_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (height, chain_id) DO NOTHING`,
		pb.height, w.cfg.ChainID, pb.hashHex, w.proposerAddr.String(), pb.blockTime,
	)
	if err != nil {
		return fmt.Errorf("insert core_blocks height=%d: %w", pb.height, err)
	}

	// COPY transactions in bulk — much faster than individual INSERTs.
	_, err = pgTx.CopyFrom(ctx,
		pgx.Identifier{"core_transactions"},
		[]string{"block_id", "index", "tx_hash", "transaction", "created_at"},
		&txCopySource{height: pb.height, blockTime: pb.blockTime, rows: pb.txData},
	)
	if err != nil {
		return fmt.Errorf("copy core_transactions height=%d: %w", pb.height, err)
	}

	// Insert app state.
	_, err = pgTx.Exec(ctx,
		`INSERT INTO core_app_state (block_height, app_hash)
		 VALUES ($1, $2)
		 ON CONFLICT (block_height, app_hash) DO NOTHING`,
		pb.height, pb.appHash,
	)
	if err != nil {
		return fmt.Errorf("insert core_app_state height=%d: %w", pb.height, err)
	}

	if err := pgTx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx height=%d: %w", pb.height, err)
	}

	// Write to blockstore after postgres commit so they stay in sync.
	if w.blockStore != nil {
		w.blockStore.SaveBlock(pb.block, pb.blockParts, pb.seenCommit)
	}

	return nil
}

// txCopySource implements pgx.CopyFromSource for bulk-inserting transactions.
type txCopySource struct {
	height    int64
	blockTime time.Time
	rows      []txRow
	idx       int
}

func (s *txCopySource) Next() bool {
	return s.idx < len(s.rows)
}

func (s *txCopySource) Values() ([]interface{}, error) {
	row := s.rows[s.idx]
	vals := []interface{}{s.height, s.idx, row.hash, row.txBytes, s.blockTime}
	s.idx++
	return vals, nil
}

func (s *txCopySource) Err() error { return nil }

// Run processes all entity types in dependency order, writes them as real
// CometBFT blocks, then writes CometBFT state files.
func (w *Writer) Run(ctx context.Context) error {
	start := time.Now()
	w.logger.Info("starting genesis write")

	// Create progress table for resume support.
	if _, err := w.dstDB.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS genesis_writer_progress (
			step_name TEXT PRIMARY KEY,
			completed_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create progress table: %w", err)
	}

	// If resuming, load the last known chain state from the database.
	if w.cfg.Resume {
		var maxHeight int64
		err := w.dstDB.QueryRow(ctx, `SELECT COALESCE(MAX(height), 0) FROM core_blocks WHERE chain_id = $1`, w.cfg.ChainID).Scan(&maxHeight)
		if err != nil {
			return fmt.Errorf("query max height for resume: %w", err)
		}
		if maxHeight > 0 {
			w.height = maxHeight + 1
			w.blockTime = w.cfg.GenesisTime.Add(time.Duration(maxHeight) * time.Second)

			// Restore prevAppHash from core_app_state (the app hash AFTER executing maxHeight).
			// Header.AppHash is the app hash after the *previous* block, so loading from
			// the block header would be off-by-one.
			var prevAppHash []byte
			err = w.dstDB.QueryRow(ctx,
				`SELECT app_hash FROM core_app_state WHERE block_height = $1`, maxHeight).
				Scan(&prevAppHash)
			if err != nil {
				return fmt.Errorf("query app hash for resume height %d: %w", maxHeight, err)
			}
			w.prevAppHash = prevAppHash

			// Restore block linkage from blockstore so new blocks chain correctly.
			if w.blockStore == nil {
				return fmt.Errorf("cannot resume without blockstore (--cmt-home required)")
			}
			lastBlock, _ := w.blockStore.LoadBlock(maxHeight)
			if lastBlock == nil {
				return fmt.Errorf("load resume block %d from blockstore: not found", maxHeight)
			}
			blockParts, err := lastBlock.MakePartSet(cmttypes.BlockPartSizeBytes)
			if err != nil {
				return fmt.Errorf("make part set for resume block %d: %w", maxHeight, err)
			}
			w.prevBlockID = cmttypes.BlockID{
				Hash:          lastBlock.Hash(),
				PartSetHeader: blockParts.Header(),
			}
			lastCommit := w.blockStore.LoadBlockCommit(maxHeight)
			if lastCommit == nil {
				return fmt.Errorf("load resume commit %d from blockstore: not found", maxHeight)
			}
			w.lastCommit = lastCommit

			w.logger.Info("resuming from height", zap.Int64("height", w.height))
		}
	}

	// Drop non-PK indexes on core_transactions for faster bulk loading
	// (only on fresh runs — on resume, indexes may already be rebuilt).
	if !w.cfg.Resume {
		if err := w.dropTransactionIndexes(ctx); err != nil {
			return fmt.Errorf("drop indexes: %w", err)
		}
	}

	// Start the async block writer pipeline.
	w.startBlockWriter(ctx)
	defer w.stopBlockWriter() //nolint:errcheck // explicit stop below captures the error

	steps := []struct {
		name string
		skip bool
		fn   func(context.Context) error
	}{
		// Phase 1: Identity — users and their linked wallets
		{"users", w.cfg.SkipUsers, w.writeUsers},
		{"associated wallets", w.cfg.SkipWallets, w.writeAssociatedWallets},
		{"dashboard wallet users", w.cfg.SkipWallets, w.writeDashboardWalletUsers},

		// Phase 2: Content — tracks and playlists
		{"tracks", w.cfg.SkipTracks, w.writeTracks},
		{"track downloads", w.cfg.SkipTracks, w.writeTrackDownloads},
		{"playlists", w.cfg.SkipPlaylists, w.writePlaylists},

		// Phase 3: Social — relationships between users and content
		{"follows", w.cfg.SkipSocial, w.writeFollows},
		{"saves", w.cfg.SkipSocial, w.writeSaves},
		{"reposts", w.cfg.SkipSocial, w.writeReposts},
		{"shares", w.cfg.SkipSocial, w.writeShares},
		{"subscriptions", w.cfg.SkipSocial, w.writeSubscriptions},
		{"muted users", w.cfg.SkipSocial, w.writeMutedUsers},

		// Phase 4: Apps — developer apps and API grants
		{"developer apps", w.cfg.SkipApps, w.writeDeveloperApps},
		{"grants", w.cfg.SkipApps, w.writeGrants},

		// Phase 5: Comments — comments and reactions on content
		{"comments", w.cfg.SkipComments, w.writeComments},
		{"comment reactions", w.cfg.SkipComments, w.writeCommentReactions},

		// Phase 6: Emails — encrypted emails and access grants
		{"encrypted emails", w.cfg.SkipEmails, w.writeEncryptedEmails},
		{"email access", w.cfg.SkipEmails, w.writeEmailAccess},

		// Phase 7: Activity — play count reconciliation, plays, and tip reactions
		{"play count reconciliation", w.cfg.SkipPlays, w.writePlayCountReconciliation},
		{"plays", w.cfg.SkipPlays, w.writePlays},
		{"tip reactions", w.cfg.SkipTipReactions, w.writeTipReactions},
	}

	// Load completed steps for resume.
	completedSteps := make(map[string]bool)
	if w.cfg.Resume {
		rows, err := w.dstDB.Query(ctx, `SELECT step_name FROM genesis_writer_progress`)
		if err != nil {
			return fmt.Errorf("query progress: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return fmt.Errorf("scan progress: %w", err)
			}
			completedSteps[name] = true
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate progress: %w", err)
		}
	}

	for _, step := range steps {
		if step.skip {
			w.logger.Info("skipping", zap.String("step", step.name))
			continue
		}
		if completedSteps[step.name] {
			w.logger.Info("already completed (resume)", zap.String("step", step.name))
			continue
		}
		// Check for async writer errors before starting next step.
		if ep := w.blockWriteErr.Load(); ep != nil {
			return fmt.Errorf("block writer: %w", *ep)
		}
		w.logger.Info("writing", zap.String("step", step.name))
		if err := step.fn(ctx); err != nil {
			if ctx.Err() != nil {
				w.logger.Info("write interrupted", zap.String("step", step.name))
				return ctx.Err()
			}
			return fmt.Errorf("write %s: %w", step.name, err)
		}
		// Record step completion for resume.
		if _, err := w.dstDB.Exec(ctx,
			`INSERT INTO genesis_writer_progress (step_name) VALUES ($1) ON CONFLICT DO NOTHING`,
			step.name); err != nil {
			return fmt.Errorf("save progress for %s: %w", step.name, err)
		}
	}

	// Flush any partial block.
	if len(w.blockTxs) > 0 {
		if err := w.flushBlock(ctx); err != nil {
			return fmt.Errorf("final flush: %w", err)
		}
	}

	// Wait for all blocks to be written.
	if err := w.stopBlockWriter(); err != nil {
		return fmt.Errorf("block writer: %w", err)
	}

	w.logger.Info("genesis write complete",
		zap.Duration("elapsed", time.Since(start)),
		zap.Int64("total_blocks", w.totalBlocks),
		zap.Int64("total_txs", w.totalTxs),
		zap.Int64("final_height", w.finalHeight),
		zap.String("final_app_hash", hex.EncodeToString(w.finalAppHash)),
		zap.String("final_block_hash", hex.EncodeToString(w.prevBlockID.Hash)),
	)

	// If resuming and no new blocks were written, recover final state from DB
	// so that writeCMTState can still run.
	if w.finalHeight == 0 && w.cfg.Resume {
		var maxHeight int64
		var appHash []byte
		err := w.dstDB.QueryRow(ctx,
			`SELECT block_height, app_hash FROM core_app_state WHERE block_height = (SELECT MAX(block_height) FROM core_app_state)`).
			Scan(&maxHeight, &appHash)
		if err == nil && maxHeight > 0 {
			w.finalHeight = maxHeight
			w.finalAppHash = appHash
			// Recover block hash from core_blocks. Only set the hash —
			// preserve any PartSetHeader already restored from blockstore.
			var blockHash string
			if err := w.dstDB.QueryRow(ctx,
				`SELECT hash FROM core_blocks WHERE height = $1`, maxHeight).
				Scan(&blockHash); err == nil {
				hashBytes, _ := hex.DecodeString(blockHash)
				w.prevBlockID.Hash = hashBytes
			}
			w.logger.Info("recovered final state for CMT write",
				zap.Int64("height", w.finalHeight),
				zap.String("app_hash", hex.EncodeToString(appHash)),
			)
		}
	}

	// Write CometBFT state and genesis file before index rebuild — fast and critical.
	if w.cfg.CMTHome != "" && w.finalHeight > 0 {
		if err := w.writeCMTState(ctx); err != nil {
			return fmt.Errorf("write cmt state: %w", err)
		}
		if err := w.writeGenesisFile(); err != nil {
			return fmt.Errorf("write genesis file: %w", err)
		}
	}

	// Rebuild indexes last — this is the slowest step and can be re-run independently.
	if err := w.createTransactionIndexes(ctx); err != nil {
		return fmt.Errorf("create indexes: %w", err)
	}

	// Print next-steps instructions.
	if w.cfg.CMTHome != "" && w.finalHeight > 0 {
		genesisPath := filepath.Join(w.cfg.CMTHome, "config", "genesis.json")
		w.logger.Info("genesis write finished — next steps:\n\n" +
			"  1. Copy the genesis file into the source tree and rebuild:\n" +
			"       cp " + genesisPath + " pkg/core/config/genesis/prod.json\n" +
			"     Then add it to pkg/core/config/genesis/genesis.go and rebuild the binary.\n\n" +
			"  2. Start the bootstrap node with the genesis-writer output as the data dir.\n" +
			"     In docker-compose.yml, mount it to /data:\n" +
			"       volumes:\n" +
			"         - " + filepath.Dir(w.cfg.CMTHome) + ":/data\n" +
			"     The node will pick up at height " + fmt.Sprintf("%d", w.finalHeight+1) + " and begin live consensus.\n\n" +
			"  3. Once the bootstrap node is running, other nodes can state-sync from it:\n" +
			"       [statesync]\n" +
			"       enable = true\n" +
			"       rpc_servers = \"<bootstrap-rpc>:26657,<bootstrap-rpc>:26657\"\n" +
			"       trust_height = " + fmt.Sprintf("%d", w.finalHeight+1) + "\n" +
			"       trust_hash = \"<block hash at height " + fmt.Sprintf("%d", w.finalHeight+1) + ">\"\n")
	}

	return nil
}

// signAndMarshal signs a ManageEntityLegacy, wraps it as a migration transaction,
// and marshals to bytes. Safe for concurrent use — all inputs are per-call.
func (w *Writer) signAndMarshal(me *corev1.ManageEntityLegacy, signer string) ([]byte, error) {
	me.Nonce = w.nextNonce()
	if err := server.SignManageEntity(w.sigCfg, me, w.privKey); err != nil {
		return nil, fmt.Errorf("sign manage entity: %w", err)
	}
	// Override Signer with the entity's real wallet address. The signature was
	// computed with the migration key, so it will NOT recover to this Signer value.
	// Indexers must verify authority by recovering from the signature and checking
	// against the genesis migration authority — Signer is an identity hint only.
	me.Signer = signer

	migration := &corev1.ManageEntityLegacyMigration{
		UserId:     me.UserId,
		EntityType: me.EntityType,
		EntityId:   me.EntityId,
		Action:     me.Action,
		Metadata:   me.Metadata,
		Signature:  me.Signature,
		Signer:     me.Signer,
		Nonce:      me.Nonce,
	}

	stx := &corev1.SignedTransaction{
		RequestId: uuid.NewString(),
		Transaction: &corev1.SignedTransaction_ManageEntityMigration{
			ManageEntityMigration: migration,
		},
	}

	// Reuse marshal options from pool to reduce allocations.
	opts := w.marshalPool.Get().(proto.MarshalOptions)
	buf, err := opts.Marshal(stx)
	w.marshalPool.Put(opts)
	if err != nil {
		return nil, fmt.Errorf("marshal manage entity migration: %w", err)
	}
	return buf, nil
}

// addManageEntity signs and wraps a ManageEntityLegacy via the signing pool,
// then appends it to the current block.
func (w *Writer) addManageEntity(ctx context.Context, me *corev1.ManageEntityLegacy) error {
	return w.addManageEntityWithSigner(ctx, me, w.signerAddr)
}

// addManageEntityWithSigner is like addManageEntity but overrides the signer address.
// Used for entity types where the DP uses params.signer as an identity
// (e.g. DeveloperApp address, user wallet for AssociatedWallet).
func (w *Writer) addManageEntityWithSigner(ctx context.Context, me *corev1.ManageEntityLegacy, signer string) error {
	txBytes, err := w.signAndMarshal(me, signer)
	if err != nil {
		return err
	}
	return w.addTx(ctx, txBytes)
}

// addTrackPlays appends a TrackPlays transaction to the current block.
// Plays do not require EIP712 signing.
func (w *Writer) addTrackPlays(ctx context.Context, plays *corev1.TrackPlays) error {
	stx := &corev1.SignedTransaction{
		RequestId: uuid.NewString(),
		Transaction: &corev1.SignedTransaction_Plays{
			Plays: plays,
		},
	}
	opts := w.marshalPool.Get().(proto.MarshalOptions)
	txBytes, err := opts.Marshal(stx)
	w.marshalPool.Put(opts)
	if err != nil {
		return fmt.Errorf("marshal plays: %w", err)
	}
	return w.addTx(ctx, txBytes)
}

// addTx appends raw tx bytes to the current block, flushing when full.
// Safe for concurrent use — serialized by blockMu.
func (w *Writer) addTx(ctx context.Context, txBytes []byte) error {
	w.blockMu.Lock()
	defer w.blockMu.Unlock()
	// Check for async writer errors.
	if ep := w.blockWriteErr.Load(); ep != nil {
		return fmt.Errorf("block writer: %w", *ep)
	}
	w.blockTxs = append(w.blockTxs, txBytes)
	if len(w.blockTxs) >= w.cfg.MaxTxsPerBlock {
		return w.flushBlock(ctx)
	}
	return nil
}

// flushBlock builds a real CometBFT block from the accumulated transactions
// and sends it to the async writer pipeline. Chain state is advanced immediately.
func (w *Writer) flushBlock(ctx context.Context) error {
	if len(w.blockTxs) == 0 {
		return nil
	}

	height := w.height
	blockTime := w.blockTime

	// app_hash = SHA256(all tx bytes concatenated) — convention shared with the
	// core server's FinalizeBlock implementation for genesis-range blocks.
	h := sha256.New()
	for _, tx := range w.blockTxs {
		h.Write(tx)
	}
	appHash := h.Sum(nil)

	// Pre-compute tx hashes for the COPY insert.
	// Use uppercase hex to match CometBFT's HexBytes.String() / common.ToTxHashFromBytes.
	txData := make([]txRow, len(w.blockTxs))
	for i, tx := range w.blockTxs {
		txData[i] = txRow{
			hash:    strings.ToUpper(hex.EncodeToString(sha256Bytes(tx))),
			txBytes: tx,
		}
	}

	// Build the CometBFT block. MakeBlock fills DataHash, LastCommitHash, EvidenceHash.
	txList := make(cmttypes.Txs, len(w.blockTxs))
	for i, tx := range w.blockTxs {
		txList[i] = cmttypes.Tx(tx)
	}
	lastCommit := w.lastCommit
	if lastCommit == nil {
		lastCommit = &cmttypes.Commit{} // empty commit for block 1
	}
	block := cmttypes.MakeBlock(height, txList, lastCommit, nil)

	// Populate state-derived header fields.
	// Header.AppHash is the app state AFTER executing the previous block.
	block.Header.Populate(
		cmtapiversion.Consensus{Block: cmtversion.BlockProtocol, App: 0},
		w.cfg.ChainID,
		blockTime,
		w.prevBlockID,
		w.validatorsHash,
		w.nextValHash,
		w.consensusHash,
		w.prevAppHash,
		cmttypes.ABCIResults(nil).Hash(), // LastResultsHash: empty for all genesis blocks
		w.proposerAddr,
	)

	blockHash := block.Hash()

	blockParts, err := block.MakePartSet(cmttypes.BlockPartSizeBytes)
	if err != nil {
		return fmt.Errorf("make part set height=%d: %w", height, err)
	}
	blockID := cmttypes.BlockID{
		Hash:          blockHash,
		PartSetHeader: blockParts.Header(),
	}

	// Sign a precommit vote over this block. The resulting commit becomes the
	// LastCommit included in the next block's header.
	commitTime := blockTime.Add(time.Second)
	voteProto := &cmtproto.Vote{
		Type:             cmtproto.PrecommitType,
		Height:           height,
		Round:            0,
		BlockID:          blockID.ToProto(),
		Timestamp:        commitTime,
		ValidatorAddress: w.proposerAddr,
		ValidatorIndex:   0,
	}
	signBytes := cmttypes.VoteSignBytes(w.cfg.ChainID, voteProto)
	sig, err := w.cmtPrivKey.Sign(signBytes)
	if err != nil {
		return fmt.Errorf("sign block %d: %w", height, err)
	}
	seenCommit := &cmttypes.Commit{
		Height:  height,
		Round:   0,
		BlockID: blockID,
		Signatures: []cmttypes.CommitSig{{
			BlockIDFlag:      cmttypes.BlockIDFlagCommit,
			ValidatorAddress: w.proposerAddr,
			Timestamp:        commitTime,
			Signature:        sig,
		}},
	}

	blockHashHex := hex.EncodeToString(blockHash)

	// Send to async writer pipeline.
	pb := pendingBlock{
		height:     height,
		blockTime:  blockTime,
		appHash:    appHash,
		hashHex:    blockHashHex,
		txData:     txData,
		block:      block,
		blockParts: blockParts,
		seenCommit: seenCommit,
	}
	select {
	case w.blockWriteCh <- pb:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Advance chain state for the next block.
	w.prevBlockID = blockID
	w.lastCommit = seenCommit
	w.prevAppHash = appHash

	w.finalHeight = height
	w.finalAppHash = appHash
	w.finalTime = blockTime

	w.totalTxs += int64(len(w.blockTxs))
	w.totalBlocks++

	w.height++
	w.blockTime = blockTime.Add(time.Second)
	w.blockTxs = w.blockTxs[:0]

	if w.totalBlocks%1000 == 0 {
		w.logger.Info("flush progress",
			zap.Int64("blocks", w.totalBlocks),
			zap.Int64("txs", w.totalTxs),
			zap.Int64("height", w.finalHeight),
		)
	}

	return nil
}

// dropTransactionIndexes drops non-PK indexes on core_transactions for faster bulk loading.
func (w *Writer) dropTransactionIndexes(ctx context.Context) error {
	indexes := []string{
		"idx_core_transactions_tx_hash_lower",
		"idx_core_transactions_block_id",
		"idx_core_transactions_tx_hash",
		"idx_core_transactions_created_at",
	}
	for _, idx := range indexes {
		if _, err := w.dstDB.Exec(ctx, fmt.Sprintf("DROP INDEX IF EXISTS %s", idx)); err != nil {
			return fmt.Errorf("drop index %s: %w", idx, err)
		}
	}
	w.logger.Info("dropped transaction indexes for bulk load")
	return nil
}

// createTransactionIndexes rebuilds indexes on core_transactions after bulk loading.
func (w *Writer) createTransactionIndexes(ctx context.Context) error {
	w.logger.Info("rebuilding transaction indexes (this may take a while)...")

	// Use a single connection so SET persists across all CREATE INDEX calls.
	conn, err := w.dstDB.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn for index rebuild: %w", err)
	}
	defer conn.Release()

	// Increase maintenance_work_mem for faster index builds on large tables.
	if _, err := conn.Exec(ctx, "SET maintenance_work_mem = '1GB'"); err != nil {
		w.logger.Warn("could not increase maintenance_work_mem", zap.Error(err))
	}

	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_core_transactions_tx_hash_lower ON core_transactions (LOWER(tx_hash))",
		"CREATE INDEX IF NOT EXISTS idx_core_transactions_block_id ON core_transactions(block_id)",
		"CREATE INDEX IF NOT EXISTS idx_core_transactions_tx_hash ON core_transactions(tx_hash)",
		"CREATE INDEX IF NOT EXISTS idx_core_transactions_created_at ON core_transactions(created_at)",
	}
	for _, ddl := range indexes {
		w.logger.Info("creating index", zap.String("ddl", ddl))
		if _, err := conn.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}
	w.logger.Info("transaction indexes rebuilt")
	return nil
}

func (w *Writer) nextNonce() string {
	n := w.nonce.Add(1)
	b := make([]byte, 32)
	b[24] = byte(n >> 56)
	b[25] = byte(n >> 48)
	b[26] = byte(n >> 40)
	b[27] = byte(n >> 32)
	b[28] = byte(n >> 24)
	b[29] = byte(n >> 16)
	b[30] = byte(n >> 8)
	b[31] = byte(n)
	return "0x" + hex.EncodeToString(b)
}

func sha256Bytes(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func signingConfig(network string) *corecfg.Config {
	switch network {
	case "prod", "production", "mainnet":
		return &corecfg.Config{
			AcdcEntityManagerAddress: corecfg.ProdAcdcAddress,
			AcdcChainID:              corecfg.ProdAcdcChainID,
		}
	case "stage", "staging":
		return &corecfg.Config{
			AcdcEntityManagerAddress: corecfg.StageAcdcAddress,
			AcdcChainID:              corecfg.StageAcdcChainID,
		}
	default:
		return &corecfg.Config{
			AcdcEntityManagerAddress: corecfg.DevAcdcAddress,
			AcdcChainID:              corecfg.DevAcdcChainID,
		}
	}
}
