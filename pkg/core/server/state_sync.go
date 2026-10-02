package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	v1 "github.com/cometbft/cometbft/api/cometbft/abci/v1"
	"github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cometbft/cometbft/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

var (
	// snapshotDirPattern is the format string for creating snapshot directory names.
	// It takes a chain ID as a parameter and creates a directory like "snapshots_<chainID>".
	snapshotDirPattern = "snapshots_%s"

	// heightDirPattern is the format string for creating height-specific directory names.
	// It takes a block height as a parameter and creates a directory like "height_0000000123".
	// The %010d format ensures the height is padded with zeros to 10 digits.
	heightDirPattern = "height_%010d"

	// chunkFilePattern is the format string for creating chunk file names.
	// It takes a chunk index as a parameter and creates a file like "chunk_000001.gz".
	// The %06d format ensures the chunk index is padded with zeros to 6 digits,
	// allowing for up to 1 million chunks (11.7TB with 12MB chunks).
	chunkFilePattern = "chunk_%06d.gz"

	// metadataFileName is the name of the metadata JSON file that contains snapshot information.
	// This file is stored in each snapshot directory and contains details about the snapshot.
	metadataFileName = "metadata.json"

	// pgDumpFileName is the name of the PostgreSQL dump file.
	// This is the binary format dump file created by pg_dump and used for database restoration.
	pgDumpFileName = "data.dump"

	// tmpReconstructionDir is the name of the temporary directory used during snapshot reconstruction.
	// This directory is used to store chunks and metadata while reconstructing a snapshot.
	tmpReconstructionDir = "tmp_reconstruction"
)

type Metadata struct {
	Sender  string `json:"sender"`
	ChainID string `json:"chain_id"`
}

// Helper functions for common filepath patterns
func getSnapshotDir(rootDir, chainID string) string {
	return filepath.Join(rootDir, fmt.Sprintf(snapshotDirPattern, chainID))
}

func getHeightDir(baseDir string, height int64) string {
	return filepath.Join(baseDir, fmt.Sprintf(heightDirPattern, height))
}

func getChunkPath(baseDir string, chunkIndex int) string {
	return filepath.Join(baseDir, fmt.Sprintf(chunkFilePattern, chunkIndex))
}

func getMetadataPath(baseDir string) string {
	return filepath.Join(baseDir, metadataFileName)
}

func getPgDumpPath(baseDir string) string {
	return filepath.Join(baseDir, pgDumpFileName)
}

func (s *Server) updateStateSyncInfo(update func(info *corev1.GetStatusResponse_SyncInfo_StateSyncInfo) *corev1.GetStatusResponse_SyncInfo_StateSyncInfo) {
	if s.cache == nil {
		return
	}

	if err := upsertCache(s.cache.syncInfo, SyncInfoKey, func(syncInfo *corev1.GetStatusResponse_SyncInfo) *corev1.GetStatusResponse_SyncInfo {
		next := update(syncInfo.GetStateSync())
		if next == nil {
			syncInfo.SyncMode = nil
			return syncInfo
		}

		syncInfo.SyncMode = &corev1.GetStatusResponse_SyncInfo_StateSync{StateSync: next}
		return syncInfo
	}); err != nil {
		s.logger.Debug("failed to update state sync info", zap.Error(err))
	}
}

func (s *Server) clearStateSyncInfo() {
	s.updateStateSyncInfo(func(_ *corev1.GetStatusResponse_SyncInfo_StateSyncInfo) *corev1.GetStatusResponse_SyncInfo_StateSyncInfo {
		return nil
	})
}

func (s *Server) countReconstructionChunks(height int64) (int64, error) {
	heightDir := getHeightDir(filepath.Join(s.config.RootDir, tmpReconstructionDir), height)
	entries, err := os.ReadDir(heightDir)
	if err != nil {
		return 0, err
	}

	var count int64
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "chunk_") && strings.HasSuffix(name, ".gz") {
			count++
		}
	}

	return count, nil
}

// chunkExists checks if a chunk file already exists on disk
func (s *Server) chunkExists(height int64, chunkIndex int) bool {
	tmpDir := filepath.Join(s.config.RootDir, tmpReconstructionDir)
	heightDir := getHeightDir(tmpDir, height)
	chunkPath := getChunkPath(heightDir, chunkIndex)

	_, err := os.Stat(chunkPath)
	return err == nil
}

func (s *Server) startSnapshotCreator(ctx context.Context) error {
	s.StartProcess(ProcessStateSnapshotCreator)

	select {
	case <-ctx.Done():
		s.CompleteProcess(ProcessStateSnapshotCreator)
		return ctx.Err()
	case <-s.awaitRpcReady:
	}

	logger := s.logger.With(zap.String("service", "state_sync"))

	if !s.config.StateSync.ServeSnapshots {
		logger.Info("ServeSnapshots is not enabled, skipping snapshot creation")
		s.CompleteProcess(ProcessStateSnapshotCreator)
		return nil
	}

	// Wait for block sync to complete before creating snapshots.
	// Snapshots taken while catching up would be stale, and createSnapshot
	// already skips CatchingUp=true — this makes the wait explicit.
	s.SleepingProcessWithMetadata(ProcessStateSnapshotCreator, "Waiting for block sync to complete")
	for {
		status, err := s.rpc.Status(context.Background())
		if err == nil && !status.SyncInfo.CatchingUp {
			break
		}
		select {
		case <-ctx.Done():
			s.CompleteProcess(ProcessStateSnapshotCreator)
			return ctx.Err()
		case <-time.After(30 * time.Second):
		}
	}

	// Create an immediate snapshot if all snapshot intervals were missed during
	// block sync catch-up (e.g. after a node restart). This ensures other nodes
	// can always state sync without waiting up to BlockInterval blocks.
	//
	// The snapshot is taken now, so it is labeled with the height postgres is
	// at now, not with the interval boundary that was missed. Labeling it with
	// the boundary served state from later blocks under an earlier height, and
	// every node that restored it failed CometBFT's post-restore height check.
	{
		status, err := s.rpc.Status(context.Background())
		snapshots, _ := s.getStoredSnapshots()
		if err == nil {
			latestHeight := status.SyncInfo.LatestBlockHeight
			blockInterval := s.config.StateSync.BlockInterval
			missedBoundary := latestHeight - (latestHeight % blockInterval)
			newestSnapshot := int64(0)
			if len(snapshots) > 0 {
				newestSnapshot = int64(snapshots[len(snapshots)-1].Height)
			}
			if missedBoundary > newestSnapshot {
				logger.Info("creating catch-up snapshot after block sync",
					zap.Int64("missedBoundary", missedBoundary),
					zap.Int64("latestHeight", latestHeight),
					zap.Int64("lastSnapshot", newestSnapshot))
				if err := s.createSnapshot(logger, latestHeight); err != nil {
					logger.Error("error creating catch-up snapshot", zap.Error(err))
				}
				if err := s.pruneSnapshots(logger); err != nil {
					logger.Error("error pruning snapshots after catch-up", zap.Error(err))
				}
			}
		}
	}

	node := s.node
	eb := node.EventBus()

	if eb == nil {
		s.ErrorProcess(ProcessStateSnapshotCreator, "event bus not ready")
		return errors.New("event bus not ready")
	}

	subscriberID := "state-sync-subscriber"

	query := types.EventQueryNewBlock
	subscription, err := eb.Subscribe(ctx, subscriberID, query)
	if err != nil {
		s.ErrorProcess(ProcessStateSnapshotCreator, fmt.Sprintf("failed to subscribe to NewBlock events: %v", err))
		return fmt.Errorf("failed to subscribe to NewBlock events: %v", err)
	}

	s.SleepingProcessWithMetadata(ProcessStateSnapshotCreator, "Waiting for snapshot interval")

	// Only run one snapshot creation job at a time
	snapshotSemaphore := make(chan struct{}, 1)
	snapshotSemaphore <- struct{}{}

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Stopping block event subscription")
			s.CompleteProcess(ProcessStateSnapshotCreator)
			return ctx.Err()
		case msg := <-subscription.Out():
			blockEvent := msg.Data().(types.EventDataNewBlock)
			blockHeight := blockEvent.Block.Height
			if blockHeight%s.config.StateSync.BlockInterval != 0 {
				continue
			}

			select {
			case <-snapshotSemaphore:
				go func(height int64) {
					s.RunningProcessWithMetadata(ProcessStateSnapshotCreator, fmt.Sprintf("Creating snapshot at height %d", height))
					if err := s.createSnapshot(logger, height); err != nil {
						logger.Error("error creating snapshot", zap.Error(err))
					}
					s.RunningProcessWithMetadata(ProcessStateSnapshotCreator, "Pruning old snapshots")
					if err := s.pruneSnapshots(logger); err != nil {
						logger.Error("error pruning snapshots", zap.Error(err))
					}
					snapshotSemaphore <- struct{}{}
					s.SleepingProcessWithMetadata(ProcessStateSnapshotCreator, "Waiting for snapshot interval")
				}(blockHeight)
			default:
				s.SleepingProcessWithMetadata(ProcessStateSnapshotCreator, "Snapshot creation still in progress")
			}
		case <-subscription.Canceled():
			s.logger.Error("Subscription cancelled", zap.Error(subscription.Err()))
			s.ErrorProcess(ProcessStateSnapshotCreator, fmt.Sprintf("subscription cancelled: %v", subscription.Err()))
			return subscription.Err()
		}
	}
}

// createSnapshot dumps the snapshot tables and labels the result with the
// block height the dump actually contains. triggerHeight is the height that
// prompted the snapshot and is only logged: by the time pg_dump reads the
// database, later blocks may have committed, so the label comes from the
// dump's own view of core_app_state (see openSnapshotView).
func (s *Server) createSnapshot(logger *zap.Logger, triggerHeight int64) error {
	// create snapshot directory if it doesn't exist
	snapshotDir := getSnapshotDir(s.config.RootDir, s.config.GenesisFile.ChainID)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return fmt.Errorf("error creating snapshot directory: %v", err)
	}

	freeBytes, hasSpace, err := snapshotDirHasMinFreeBytes(snapshotDir, s.config.StateSync.SnapshotMinFreeBytes)
	if err != nil {
		return fmt.Errorf("error checking snapshot disk space: %w", err)
	}
	if !hasSpace {
		logger.Warn("skipping snapshot creation: insufficient disk space",
			zap.String("snapshotDir", snapshotDir),
			zap.Int64("freeBytes", freeBytes),
			zap.Int64("minFreeBytes", s.config.StateSync.SnapshotMinFreeBytes))
		return nil
	}

	if s.rpc == nil {
		return nil
	}

	status, err := s.rpc.Status(context.Background())
	if err != nil {
		return nil
	}

	if status.SyncInfo.CatchingUp {
		return nil
	}

	view, err := openSnapshotView(context.Background(), s.config.PSQLConn)
	if err != nil {
		return fmt.Errorf("error opening snapshot view: %w", err)
	}
	defer view.Close()

	blockHeight := view.Height

	block, err := s.rpc.Block(context.Background(), &blockHeight)
	if err != nil {
		return fmt.Errorf("error getting block %d for snapshot: %w", blockHeight, err)
	}
	blockHash := block.BlockID.Hash

	logger.Info("Creating snapshot",
		zap.Int64("height", blockHeight),
		zap.Int64("triggerHeight", triggerHeight),
		zap.String("appHash", hex.EncodeToString(view.AppHash)))

	latestSnapshotDir := getHeightDir(snapshotDir, blockHeight)
	// A snapshot at this height already exists, e.g. the catch-up snapshot and
	// an interval snapshot resolved to the same block. Leave it alone: the
	// cleanup below would otherwise delete it if this attempt failed.
	if _, err := os.Stat(getMetadataPath(latestSnapshotDir)); err == nil {
		logger.Info("snapshot already exists at height, skipping", zap.Int64("height", blockHeight))
		return nil
	}
	if err := os.MkdirAll(latestSnapshotDir, 0755); err != nil {
		return fmt.Errorf("error creating latest snapshot directory: %v", err)
	}
	snapshotComplete := false
	defer func() {
		if snapshotComplete {
			return
		}
		if err := os.RemoveAll(latestSnapshotDir); err != nil {
			logger.Warn("failed to remove incomplete snapshot",
				zap.String("path", latestSnapshotDir),
				zap.Error(err))
		}
	}()

	logger.Info("Creating pg_dump", zap.Int64("height", blockHeight))

	if err := s.createPgDump(logger, latestSnapshotDir, view.SnapshotID); err != nil {
		return fmt.Errorf("error creating pg_dump: %v", err)
	}
	// pg_dump has finished reading, so stop holding back vacuum.
	view.Close()

	logger.Info("Chunking pg_dump", zap.Int64("height", blockHeight))

	chunkCount, err := s.chunkPgDump(logger, latestSnapshotDir)
	if err != nil {
		return fmt.Errorf("error chunking pg_dump: %v", err)
	}

	logger.Info("Deleting pg_dump", zap.Int64("height", blockHeight))

	if err := s.deletePgDump(logger, latestSnapshotDir); err != nil {
		return fmt.Errorf("error deleting pg_dump: %v", err)
	}

	logger.Info("Writing snapshot metadata", zap.Int64("height", blockHeight))

	b, err := json.Marshal(Metadata{
		Sender:  s.config.ProposerAddress,
		ChainID: s.config.GenesisFile.ChainID,
	})
	if err != nil {
		return fmt.Errorf("error marshalling metadata: %v", err)
	}

	snapshotMetadata := v1.Snapshot{
		Height:   uint64(blockHeight),
		Format:   1,
		Chunks:   uint32(chunkCount),
		Hash:     blockHash,
		Metadata: b,
	}

	snapshotMetadataFile := getMetadataPath(latestSnapshotDir)
	jsonBytes, err := json.Marshal(snapshotMetadata)
	if err != nil {
		return fmt.Errorf("error marshalling snapshot metadata: %v", err)
	}

	if err := os.WriteFile(snapshotMetadataFile, jsonBytes, 0644); err != nil {
		return fmt.Errorf("error writing snapshot metadata: %v", err)
	}
	snapshotComplete = true

	logger.Info("Snapshot created", zap.Int64("height", blockHeight))

	return nil
}

func snapshotDirHasMinFreeBytes(snapshotDir string, minFreeBytes int64) (int64, bool, error) {
	if minFreeBytes <= 0 {
		return 0, true, nil
	}

	freeBytes, err := snapshotDirFreeBytes(snapshotDir)
	if err != nil {
		return 0, false, err
	}
	return freeBytes, freeBytes >= minFreeBytes, nil
}

func snapshotDirFreeBytes(snapshotDir string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(snapshotDir, &stat); err != nil {
		return 0, err
	}
	if stat.Bavail <= 0 || stat.Bsize <= 0 {
		return 0, nil
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

// stateSyncSnapshotTables are the tables pg_dump serializes into a state-sync
// snapshot. A node that state-syncs restores exactly these, so anything the
// consensus path reads and cannot rederive from the block log has to be here.
//
// This list is maintained by hand, which has bitten us before: migration
// 00028_fix_missing_tables_from_state_sync.sql exists solely to recreate tables
// that a later migration added and nobody added here. TestSnapshotTablesCoverCoreSchema
// now fails when a core_* table is created without a decision being recorded
// about it.
var stateSyncSnapshotTables = []string{
	"access_keys",
	"core_app_state",
	// Consensus auth state: enforcement-active nodes validate proposals
	// against these, so a state-synced node without them would reject
	// every valid ManageEntity transaction and split from consensus.
	// core_auth_cids is the content-authorization half of the same state:
	// which user may assert a given cid as a track's audio.
	"core_auth_cids",
	"core_auth_developer_apps",
	"core_auth_entities",
	"core_auth_grants",
	"core_auth_users",
	"core_blocks",
	"core_db_migrations",
	"core_transactions",
	"core_tx_stats",
	"core_tx_count",
	"core_validators",
	"management_keys",
	"sla_node_reports",
	"sla_rollups",
	"sound_recordings",
	"storage_proof_peers",
	"storage_proofs",
	"track_releases",
	"core_ern",
	"core_mead",
	"core_pie",
	"core_resources",
	"core_releases",
	"core_parties",
	"core_deals",
	"core_rewards",
	"core_reward_pools",
	"launchpad_authority_rm",
	"core_uploads",
	"validator_history",
}

// truncateSnapshotTablesStmt builds the statement that clears the tables COPY is
// about to load, given the tables that actually exist in `public`.
//
// Scope is the whole point. Only stateSyncSnapshotTables is dumped, so
// truncating anything else cannot prevent a COPY conflict -- nothing will COPY
// into it -- and only destroys data. This path used to truncate every table in
// `public`, which wiped mediorum's uploads, blobs, audio_previews and
// qm_audio_analyses on any node that state synced, along with every other table
// outside core's migrations.
//
// The intersection matters because pgRestore("pre-data") deliberately tolerates
// schema drift, so a listed table may not exist locally yet.
//
// One statement, and no CASCADE: truncating the set together satisfies foreign
// keys among its members, while CASCADE would follow a key OUTWARD to a table
// outside the snapshot and silently truncate that too -- the same bug through a
// different door. If something outside the set references something inside it,
// this fails loudly instead, which is the outcome we want.
func truncateSnapshotTablesStmt(existing []string) string {
	inSnapshot := make(map[string]bool, len(stateSyncSnapshotTables))
	for _, t := range stateSyncSnapshotTables {
		inSnapshot[t] = true
	}
	quoted := make([]string, 0, len(stateSyncSnapshotTables))
	for _, t := range existing {
		if inSnapshot[t] {
			quoted = append(quoted, pgx.Identifier{t}.Sanitize())
		}
	}
	if len(quoted) == 0 {
		return ""
	}
	return "TRUNCATE TABLE " + strings.Join(quoted, ", ")
}

// snapshotView pins the database state a snapshot is taken from. It holds a
// read-only REPEATABLE READ transaction open on its own connection, reads
// the latest core_app_state row inside it, and exports it with
// pg_export_snapshot() so pg_dump --snapshot reads exactly the same state.
//
// Each block's writes commit in a single postgres transaction, so any view
// sits exactly after one block, and core_app_state's latest row names it. That
// row is also what Info reports after a restore, so a snapshot labeled with
// Height restores to the height CometBFT expects.
//
// The connection is dedicated rather than taken from the core pool: the
// transaction stays open for the whole dump, which can take hours.
type snapshotView struct {
	Height     int64
	AppHash    []byte
	SnapshotID string

	conn *pgx.Conn
	tx   pgx.Tx
}

func openSnapshotView(ctx context.Context, dsn string) (*snapshotView, error) {
	// Parse through pgxpool so pool_* DSN params are stripped, not sent to
	// postgres as unknown runtime parameters.
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(connectCtx, poolConfig.ConnConfig)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	view := &snapshotView{conn: conn}
	if err := view.open(connectCtx); err != nil {
		view.Close()
		return nil, err
	}
	return view, nil
}

func (v *snapshotView) open(ctx context.Context) error {
	// The session sits idle in its transaction while pg_dump runs; a
	// server-side idle timeout would kill it and invalidate the snapshot.
	if _, err := v.conn.Exec(ctx, "SET idle_in_transaction_session_timeout = 0"); err != nil {
		return fmt.Errorf("disable idle transaction timeout: %w", err)
	}

	tx, err := v.conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	v.tx = tx

	latest, err := db.New(tx).GetLatestAppState(ctx)
	if err != nil {
		return fmt.Errorf("read latest app state: %w", err)
	}
	if latest.BlockHeight <= 0 {
		return fmt.Errorf("latest app state has height %d", latest.BlockHeight)
	}
	v.Height = latest.BlockHeight
	v.AppHash = latest.AppHash

	if err := tx.QueryRow(ctx, "SELECT pg_export_snapshot()").Scan(&v.SnapshotID); err != nil {
		return fmt.Errorf("export snapshot: %w", err)
	}
	return nil
}

// Close ends the transaction, which invalidates the exported snapshot, and
// closes the connection. It is safe to call more than once.
func (v *snapshotView) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if v.tx != nil {
		_ = v.tx.Rollback(ctx)
		v.tx = nil
	}
	if v.conn != nil {
		_ = v.conn.Close(ctx)
		v.conn = nil
	}
}

// createPgDump creates a pg_dump of the database and writes it to the latest
// snapshot directory. snapshotID is an exported postgres snapshot (see
// snapshotView); the dump reads exactly the state it names.
func (s *Server) createPgDump(logger *zap.Logger, latestSnapshotDir string, snapshotID string) error {
	pgString := s.config.PSQLConn
	dumpPath := getPgDumpPath(latestSnapshotDir)

	tables := stateSyncSnapshotTables

	// Start building the args
	args := []string{"--dbname=" + pgString, "-Fc", "--snapshot=" + snapshotID}
	for _, table := range tables {
		args = append(args, "-t", table)
	}
	args = append(args, "-f", dumpPath)

	cmd := exec.Command("pg_dump", args...)
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		logger.Error("pg_dump failed", zap.Error(err), zap.String("output", string(output)))
		return fmt.Errorf("pg_dump failed: %w", err)
	}

	logger.Info("pg_dump succeeded", zap.String("output", string(output)))
	return nil
}

// chunkPgDump splits the pg_dump into 16MB gzip-compressed chunks and returns the number of chunks created
func (s *Server) chunkPgDump(logger *zap.Logger, latestSnapshotDir string) (int, error) {
	const chunkSize = 12 * 1024 * 1024 // 12MB
	dumpPath := getPgDumpPath(latestSnapshotDir)

	dumpFile, err := os.Open(dumpPath)
	if err != nil {
		return 0, fmt.Errorf("failed to open pg_dump: %w", err)
	}
	defer dumpFile.Close()

	buffer := make([]byte, chunkSize)
	chunkIndex := 0

	for {
		n, readErr := io.ReadFull(dumpFile, buffer)
		if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
			return chunkIndex, fmt.Errorf("error reading pg_dump: %w", readErr)
		}

		if n == 0 {
			break
		}

		chunkPath := getChunkPath(latestSnapshotDir, chunkIndex)
		chunkFile, err := os.Create(chunkPath)
		if err != nil {
			return chunkIndex, fmt.Errorf("failed to create chunk: %w", err)
		}

		gw := gzip.NewWriter(chunkFile)
		_, err = gw.Write(buffer[:n])
		if err != nil {
			chunkFile.Close()
			return chunkIndex, fmt.Errorf("failed to write gzip chunk: %w", err)
		}
		gw.Close()
		chunkFile.Close()

		logger.Info("Wrote chunk", zap.String("path", chunkPath), zap.Int("size", n))
		chunkIndex++

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	return chunkIndex, nil
}

func (s *Server) deletePgDump(logger *zap.Logger, latestSnapshotDir string) error {
	dumpPath := getPgDumpPath(latestSnapshotDir)
	if err := os.Remove(dumpPath); err != nil {
		return fmt.Errorf("error deleting pg_dump: %w", err)
	}

	return nil
}

// Prunes snapshots by deleting the oldest ones while retaining the most recent ones
// based on the configured retention count
func (s *Server) pruneSnapshots(logger *zap.Logger) error {
	snapshotDir := getSnapshotDir(s.config.RootDir, s.config.GenesisFile.ChainID)
	keep := s.config.StateSync.Keep

	files, err := os.ReadDir(snapshotDir)
	if err != nil {
		return fmt.Errorf("error reading snapshot directory: %w", err)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})

	for i := range files {
		if i >= len(files)-keep {
			break
		}

		os.RemoveAll(filepath.Join(snapshotDir, files[i].Name()))
		logger.Info("Deleted snapshot", zap.String("path", filepath.Join(snapshotDir, files[i].Name())))
	}

	return nil
}

func (s *Server) getStoredSnapshots() ([]v1.Snapshot, error) {
	if !s.config.StateSync.ServeSnapshots {
		return []v1.Snapshot{}, nil
	}

	snapshotDir := getSnapshotDir(s.config.RootDir, s.config.GenesisFile.ChainID)

	dirs, err := os.ReadDir(snapshotDir)
	if err != nil {
		return nil, fmt.Errorf("error reading snapshot directory: %w", err)
	}

	snapshots := make([]v1.Snapshot, 0)
	for _, entry := range dirs {
		if !entry.IsDir() {
			continue
		}

		metadataPath := getMetadataPath(filepath.Join(snapshotDir, entry.Name()))
		info, err := os.Stat(metadataPath)
		if err != nil || info.IsDir() {
			continue
		}

		data, err := os.ReadFile(metadataPath)
		if err != nil {
			return nil, fmt.Errorf("error reading metadata file at %s: %w", metadataPath, err)
		}

		var meta v1.Snapshot
		if err := json.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("error unmarshalling metadata at %s: %w", metadataPath, err)
		}

		if meta.Height == 0 {
			continue
		}

		snapshots = append(snapshots, meta)
	}

	// sort by height, ascending
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Height < snapshots[j].Height
	})

	return snapshots, nil
}

// GetChunkByHeight retrieves a specific chunk for a given block height
func (s *Server) GetChunkByHeight(height int64, chunk int) ([]byte, error) {
	snapshotDir := getSnapshotDir(s.config.RootDir, s.config.GenesisFile.ChainID)
	latestSnapshotDir := getHeightDir(snapshotDir, height)

	// Check if snapshot directory exists
	if _, err := os.Stat(latestSnapshotDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("no snapshot found for height %d", height)
	}

	// Read metadata to get chunk count
	metadataPath := getMetadataPath(latestSnapshotDir)
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("error reading metadata file: %v", err)
	}

	var meta v1.Snapshot
	if err := json.Unmarshal(metadataBytes, &meta); err != nil {
		return nil, fmt.Errorf("error unmarshalling metadata: %v", err)
	}

	// Read the chunk file
	chunkPath := getChunkPath(latestSnapshotDir, chunk)

	chunkData, err := os.ReadFile(chunkPath)
	if err != nil {
		return nil, fmt.Errorf("error reading chunk file: %v", err)
	}

	return chunkData, nil
}

func (s *Server) StoreOfferedSnapshot(snapshot *v1.Snapshot) error {
	snapshotDir := filepath.Join(s.config.RootDir, tmpReconstructionDir)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return fmt.Errorf("failed to create snapshot directory: %v", err)
	}

	metadataPath := getMetadataPath(snapshotDir)
	metadataBytes, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot: %v", err)
	}

	if err := os.WriteFile(metadataPath, metadataBytes, 0644); err != nil {
		return fmt.Errorf("failed to write metadata file: %v", err)
	}

	return nil
}

func (s *Server) GetOfferedSnapshot() (*v1.Snapshot, error) {
	snapshotDir := filepath.Join(s.config.RootDir, tmpReconstructionDir)
	metadataPath := getMetadataPath(snapshotDir)
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metadata file: %v", err)
	}

	var meta v1.Snapshot
	if err := json.Unmarshal(metadataBytes, &meta); err != nil {
		return nil, fmt.Errorf("error unmarshalling metadata: %v", err)
	}

	return &meta, nil
}

// StoreChunkForReconstruction stores a single chunk in a temporary directory for later reconstruction
func (s *Server) StoreChunkForReconstruction(height int64, chunkIndex int, chunkData []byte) error {
	// Create a temporary directory for reconstruction if it doesn't exist
	tmpDir := filepath.Join(s.config.RootDir, tmpReconstructionDir)
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return fmt.Errorf("failed to create temporary directory: %v", err)
	}

	// Create a directory for this specific height if it doesn't exist
	heightDir := getHeightDir(tmpDir, height)
	if err := os.MkdirAll(heightDir, 0755); err != nil {
		return fmt.Errorf("failed to create height directory: %v", err)
	}

	// Write the chunk to a file
	chunkPath := getChunkPath(heightDir, chunkIndex)
	if err := os.WriteFile(chunkPath, chunkData, 0644); err != nil {
		return fmt.Errorf("failed to write chunk file: %v", err)
	}

	return nil
}

func (s *Server) haveAllChunks(height uint64, total int) bool {
	heightDir := getHeightDir(filepath.Join(s.config.RootDir, tmpReconstructionDir), int64(height))

	// Use a map to track which chunks we have
	chunks := make(map[int]bool, total)

	// Read directory once
	files, err := os.ReadDir(heightDir)
	if err != nil {
		return false
	}

	// Track chunks by their index
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".gz") {
			continue
		}
		// Extract chunk number from filename (e.g., "chunk_000001.gz" -> 1)
		var chunkNum int
		if _, err := fmt.Sscanf(file.Name(), "chunk_%d.gz", &chunkNum); err != nil {
			continue
		}
		if chunkNum >= 0 && chunkNum < total {
			chunks[chunkNum] = true
		}
	}

	// Check if we have exactly the right number of chunks
	return len(chunks) == total
}

// ReassemblePgDump reconstructs and decompresses a binary pg_dump file from multiple gzipped chunks
func (s *Server) ReassemblePgDump(height int64) error {
	tmpDir := filepath.Join(s.config.RootDir, tmpReconstructionDir)
	heightDir := getHeightDir(tmpDir, height)

	// Create the output pg_dump file in binary format
	outputPath := getPgDumpPath(heightDir)
	outputFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %v", err)
	}
	defer outputFile.Close()

	// Read all chunk files in order
	files, err := os.ReadDir(heightDir)
	if err != nil {
		return fmt.Errorf("failed to read directory: %v", err)
	}

	// Sort files to ensure correct order
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})

	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".gz") {
			continue
		}

		chunkPath := filepath.Join(heightDir, file.Name())
		chunkData, err := os.ReadFile(chunkPath)
		if err != nil {
			return fmt.Errorf("failed to read chunk file %s: %v", file.Name(), err)
		}

		reader := bytes.NewReader(chunkData)
		gzReader, err := gzip.NewReader(reader)
		if err != nil {
			return fmt.Errorf("failed to create gzip reader: %v", err)
		}

		if _, err := io.Copy(outputFile, gzReader); err != nil {
			gzReader.Close()
			return fmt.Errorf("failed to write decompressed data: %v", err)
		}
		gzReader.Close()
	}

	return nil
}

// RestoreDatabase restores the PostgreSQL database using the reassembled pg_dump binary file,
// in three phases: schema (pre-data), data, then indexes and constraints (post-data).
// The snapshot tables are UNLOGGED during the data phase so the load writes no
// WAL, which kept large restores from being OOM-killed, and LOGGED again before
// indexes are built. See restore_persistence.go.
func (s *Server) RestoreDatabase(height int64) error {
	tmpDir := filepath.Join(s.config.RootDir, tmpReconstructionDir)
	heightDir := getHeightDir(tmpDir, height)
	dumpPath := getPgDumpPath(heightDir)

	pgRestore := func(section string) error {
		args := []string{
			"--dbname=" + s.config.PSQLConn,
			"--no-owner",
			"--no-privileges",
		}
		if section != "" {
			args = append(args, "--section="+section)
		}
		// pg_dump emits COPY items in TOC order, which can place a child table
		// (e.g. sla_node_reports) before its parent (sla_rollups). The FK trigger
		// created by pre-data then rejects the child rows during COPY, pg_restore
		// exits 1, and the ABCI handler retries the whole snapshot — looping forever.
		// --disable-triggers turns FK enforcement off for the data load only;
		// constraints are restored automatically when pg_restore re-enables triggers.
		if section == "data" {
			args = append(args, "--disable-triggers")
		}
		args = append(args, dumpPath)

		var stdout, stderr bytes.Buffer
		cmd := exec.Command("pg_restore", args...)
		// Bulk-load tuning for this pg_restore session only; see restoreSessionOptions.
		cmd.Env = pgRestoreEnv(os.Environ())
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err != nil {
			s.logger.Error("pg_restore failed",
				zap.String("section", section),
				zap.Error(err),
				zap.String("stderr", stderr.String()),
			)
			return fmt.Errorf("pg_restore --%s failed: %w", section, err)
		}
		return nil
	}

	s.StartProcess(ProcessStateRestore)

	s.RunningProcessWithMetadata(ProcessStateRestore, "restoring schema")
	s.logger.Info("pg_restore: restoring schema (pre-data)")
	// pre-data errors (missing tables from schema drift) are non-fatal
	_ = pgRestore("pre-data")

	// Clear the tables COPY is about to load: migrations pre-populate some of them
	// (e.g. core_db_migrations), which makes COPY fail on duplicate keys.
	// Collect names first so the connection isn't held open (busy) during TRUNCATE.
	s.RunningProcessWithMetadata(ProcessStateRestore, "truncating tables")
	s.logger.Info("pg_restore: truncating snapshot tables to clear migration-created data")
	if db, err := s.pool.Acquire(context.Background()); err == nil {
		rows, err := db.Query(context.Background(),
			"SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename")
		var existing []string
		if err == nil {
			for rows.Next() {
				var t string
				if rows.Scan(&t) == nil {
					existing = append(existing, t)
				}
			}
			rows.Close()
		}
		if stmt := truncateSnapshotTablesStmt(existing); stmt != "" {
			if _, err := db.Exec(context.Background(), stmt); err != nil {
				s.logger.Warn("pg_restore: failed to truncate snapshot tables", zap.Error(err))
			} else {
				s.logger.Info("pg_restore: truncated snapshot tables")
			}
		}
		db.Release()
	}

	// Load without WAL. A table that cannot be made UNLOGGED (for example one
	// referenced by a table outside the snapshot) loads LOGGED, which is slower
	// but safe, so failures here only warn.
	s.logger.Info("pg_restore: setting snapshot tables to UNLOGGED for the data load")
	if snapshotTables, err := existingSnapshotTables(context.Background(), s.pool); err != nil {
		s.logger.Warn("pg_restore: could not list snapshot tables; loading them LOGGED", zap.Error(err))
	} else if failures := setTablesPersistence(context.Background(), s.pool, snapshotTables, "UNLOGGED"); len(failures) > 0 {
		s.logger.Warn("pg_restore: some snapshot tables load LOGGED", zap.Error(persistenceError("UNLOGGED", failures)))
	}

	s.RunningProcessWithMetadata(ProcessStateRestore, "data COPY: starting")
	s.logger.Info("pg_restore: restoring data")
	pollCtx, stopPoll := context.WithCancel(context.Background())
	go pollCopyProgress(pollCtx, s.pool, 2*time.Second, func(msg string) {
		s.RunningProcessWithMetadata(ProcessStateRestore, "data COPY: "+msg)
	})
	dataErr := pgRestore("data")
	stopPoll()
	if dataErr != nil {
		return dataErr
	}

	// An UNLOGGED table is emptied whenever postgres stops uncleanly, so the
	// restore does not succeed while any snapshot table is still UNLOGGED.
	s.RunningProcessWithMetadata(ProcessStateRestore, "converting tables to LOGGED")
	s.logger.Info("pg_restore: setting snapshot tables back to LOGGED")
	if err := restoreSnapshotTablesLogged(context.Background(), s.pool, s.logger); err != nil {
		s.ErrorProcess(ProcessStateRestore, err.Error())
		return fmt.Errorf("make snapshot tables logged: %w", err)
	}

	s.RunningProcessWithMetadata(ProcessStateRestore, "building indexes")
	s.logger.Info("pg_restore: restoring indexes and constraints (post-data)")
	// post-data errors (duplicate indexes etc.) are non-fatal
	_ = pgRestore("post-data")

	// Older snapshots do not contain the counter table. The local migration has
	// installed its triggers, but restore truncated its baseline. Rebuild once,
	// before accepting the snapshot and resuming block execution.
	if err := s.db.EnsureTransactionCount(context.Background()); err != nil {
		return fmt.Errorf("initialize restored transaction count: %w", err)
	}

	s.CompleteProcess(ProcessStateRestore)
	return nil
}

func (s *Server) CleanupStateSync() error {
	snapshotDir := filepath.Join(s.config.RootDir, tmpReconstructionDir)
	if err := os.RemoveAll(snapshotDir); err != nil {
		return fmt.Errorf("error cleaning up temporary files: %w", err)
	}
	return nil
}

func (s *Server) cacheSnapshots() error {
	snapshots, err := s.getStoredSnapshots()
	if err != nil {
		return fmt.Errorf("error getting stored snapshots: %w", err)
	}

	return upsertCache(s.cache.snapshotInfo, SnapshotInfoKey, func(snapshotInfo *corev1.GetStatusResponse_SnapshotInfo) *corev1.GetStatusResponse_SnapshotInfo {
		snapshotInfo.Enabled = s.config.StateSync.ServeSnapshots

		newSnapshots := make([]*corev1.SnapshotMetadata, 0, len(snapshots))
		for _, snapshot := range snapshots {
			newSnapshots = append(newSnapshots, &corev1.SnapshotMetadata{
				Height:     int64(snapshot.Height),
				Hash:       hex.EncodeToString(snapshot.Hash),
				ChunkCount: int64(snapshot.Chunks),
				ChainId:    s.config.GenesisFile.ChainID,
			})
		}

		// Sort DESC so index 0 is most recent; last element is oldest.
		sort.Slice(newSnapshots, func(i, j int) bool {
			if newSnapshots[i].Height == newSnapshots[j].Height {
				// deterministic tiebreaker (optional)
				return newSnapshots[i].Hash > newSnapshots[j].Hash
			}
			return newSnapshots[i].Height > newSnapshots[j].Height
		})

		snapshotInfo.Snapshots = newSnapshots
		return snapshotInfo
	})
}

func (s *Server) stateSyncLatestBlock(rpcServers []string) (trustHeight int64, trustHash string, err error) {
	for _, rpcServer := range rpcServers {
		oapRPC := strings.TrimSuffix(rpcServer, "/core/crpc")
		oap := sdk.NewOpenAudioSDK(oapRPC)
		snapshots, err := oap.Core.GetStoredSnapshots(context.Background(), connect.NewRequest(&corev1.GetStoredSnapshotsRequest{}))
		if err != nil {
			s.logger.Error("error getting stored snapshots", zap.String("rpcServer", rpcServer), zap.Error(err))
			continue
		}
		if len(snapshots.Msg.Snapshots) == 0 {
			s.logger.Warn("no snapshots returned from host", zap.String("rpcServer", rpcServer))
			continue
		}

		// get last snapshot in list, this is the latest snapshot
		lastSnapshot := snapshots.Msg.Snapshots[len(snapshots.Msg.Snapshots)-1]
		trustBuffer := int64(10) // number of blocks to step back
		safeHeight := lastSnapshot.Height - trustBuffer

		// Ensure safeHeight is at least 1 (block height starts at 1)
		if safeHeight < 1 {
			s.logger.Warn("snapshot height too low for trust buffer",
				zap.String("rpcServer", rpcServer),
				zap.Int64("snapshotHeight", lastSnapshot.Height),
				zap.Int64("safeHeight", safeHeight))
			safeHeight = 1
		}

		client, err := http.New(rpcServer)
		if err != nil {
			s.logger.Error("error creating rpc client", zap.String("rpcServer", rpcServer), zap.Error(err))
			continue
		}

		block, err := client.Block(context.Background(), &safeHeight)
		if err != nil {
			s.logger.Error("error getting block at safe height",
				zap.String("rpcServer", rpcServer),
				zap.Int64("safeHeight", safeHeight),
				zap.Error(err))
			continue
		}

		trustHeight = block.Block.Height
		trustHash = block.Block.Hash().String()

		s.logger.Info("found usable block for state sync",
			zap.String("rpcServer", rpcServer),
			zap.Int64("trustHeight", trustHeight),
			zap.String("trustHash", trustHash))

		return trustHeight, trustHash, nil
	}

	return 0, "", fmt.Errorf("no usable block found for state sync")
}
