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

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	v1 "github.com/cometbft/cometbft/api/cometbft/abci/v1"
	"github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cometbft/cometbft/types"
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

func (s *Server) startStateSync(ctx context.Context) error {
	s.StartProcess(ProcessStateStateSync)

	select {
	case <-ctx.Done():
		s.CompleteProcess(ProcessStateStateSync)
		return ctx.Err()
	case <-s.awaitRpcReady:
	}

	logger := s.logger.With(zap.String("service", "state_sync"))

	if !s.config.StateSync.ServeSnapshots {
		logger.Info("State sync is not enabled, skipping snapshot creation")
		s.CompleteProcess(ProcessStateStateSync)
		return nil
	}

	node := s.node
	eb := node.EventBus()

	if eb == nil {
		s.ErrorProcess(ProcessStateStateSync, "event bus not ready")
		return errors.New("event bus not ready")
	}

	subscriberID := "state-sync-subscriber"

	query := types.EventQueryNewBlock
	subscription, err := eb.Subscribe(ctx, subscriberID, query)
	if err != nil {
		s.ErrorProcess(ProcessStateStateSync, fmt.Sprintf("failed to subscribe to NewBlock events: %v", err))
		return fmt.Errorf("failed to subscribe to NewBlock events: %v", err)
	}

	s.RunningProcessWithMetadata(ProcessStateStateSync, "Listening for new blocks")

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Stopping block event subscription")
			s.CompleteProcess(ProcessStateStateSync)
			return ctx.Err()
		case msg := <-subscription.Out():
			blockEvent := msg.Data().(types.EventDataNewBlock)
			blockHeight := blockEvent.Block.Height
			if blockHeight%s.config.StateSync.BlockInterval != 0 {
				s.SleepingProcessWithMetadata(ProcessStateStateSync, "Waiting for snapshot interval")
				continue
			}

			s.RunningProcessWithMetadata(ProcessStateStateSync, fmt.Sprintf("Creating snapshot at height %d", blockHeight))
			if err := s.createSnapshot(logger, blockHeight); err != nil {
				logger.Error("error creating snapshot", zap.Error(err))
			}

			s.RunningProcessWithMetadata(ProcessStateStateSync, "Pruning old snapshots")
			if err := s.pruneSnapshots(logger); err != nil {
				logger.Error("error pruning snapshots", zap.Error(err))
			}
			s.SleepingProcessWithMetadata(ProcessStateStateSync, "Waiting for next block")
		case <-subscription.Canceled():
			s.logger.Error("Subscription cancelled", zap.Error(subscription.Err()))
			s.ErrorProcess(ProcessStateStateSync, fmt.Sprintf("subscription cancelled: %v", subscription.Err()))
			return subscription.Err()
		}
	}
}

func (s *Server) createSnapshot(logger *zap.Logger, height int64) error {
	// create snapshot directory if it doesn't exist
	snapshotDir := getSnapshotDir(s.config.RootDir, s.config.GenesisFile.ChainID)
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return fmt.Errorf("error creating snapshot directory: %v", err)
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

	block, err := s.rpc.Block(context.Background(), &height)
	if err != nil {
		return nil
	}

	logger.Info("Creating snapshot", zap.Int64("height", height))

	blockHeight := height
	blockHash := block.BlockID.Hash

	latestSnapshotDir := getHeightDir(snapshotDir, blockHeight)
	if err := os.MkdirAll(latestSnapshotDir, 0755); err != nil {
		return fmt.Errorf("error creating latest snapshot directory: %v", err)
	}

	logger.Info("Creating pg_dump", zap.Int64("height", blockHeight))

	if err := s.createPgDump(logger, latestSnapshotDir); err != nil {
		return fmt.Errorf("error creating pg_dump: %v", err)
	}

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

	logger.Info("Snapshot created", zap.Int64("height", blockHeight))

	return nil
}

// createPgDump creates a pg_dump of the database and writes it to the latest snapshot directory
func (s *Server) createPgDump(logger *zap.Logger, latestSnapshotDir string) error {
	pgString := s.config.PSQLConn
	dumpPath := getPgDumpPath(latestSnapshotDir)

	// You can customize this slice with the tables you want to dump
	tables := []string{
		"access_keys",
		"core_app_state",
		"core_blocks",
		"core_db_migrations",
		"core_transactions",
		"core_tx_stats",
		"core_validators",
		"management_keys",
		"sla_node_reports",
		"sla_rollups",
		"sound_recordings",
		"storage_proof_peers",
		"storage_proofs",
		"track_releases",
	}

	// Start building the args
	args := []string{"--dbname=" + pgString, "-Fc"}
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

// RestoreDatabase restores the PostgreSQL database using the reassembled pg_dump binary file
func (s *Server) RestoreDatabase(height int64) error {
	tmpDir := filepath.Join(s.config.RootDir, tmpReconstructionDir)
	heightDir := getHeightDir(tmpDir, height)
	dumpPath := getPgDumpPath(heightDir)

	cmd := exec.Command("pg_restore",
		"--dbname="+s.config.PSQLConn,
		"--clean",
		"--if-exists",
		"--no-owner",
		"--no-privileges",
		dumpPath)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		s.logger.Error("pg_restore failed",
			zap.Error(err),
			zap.String("stderr", stderr.String()),
			zap.String("stdout", stdout.String()),
		)
		return fmt.Errorf("error restoring database: %w", err)
	}

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
		audsRpc := strings.TrimSuffix(rpcServer, "/core/crpc")
		auds := sdk.NewAudiusdSDK(audsRpc)
		snapshots, err := auds.Core.GetStoredSnapshots(context.Background(), connect.NewRequest(&corev1.GetStoredSnapshotsRequest{}))
		if err != nil {
			s.logger.Error("error getting stored snapshots", zap.String("rpcServer", rpcServer), zap.Error(err))
			continue
		}

		// get last snapshot in list, this is the latest snapshot
		lastSnapshot := snapshots.Msg.Snapshots[len(snapshots.Msg.Snapshots)-1]
		trustBuffer := 10 // number of blocks to step back
		safeHeight := lastSnapshot.Height - int64(trustBuffer)

		client, err := http.New(rpcServer)
		if err != nil {
			s.logger.Error("error creating rpc client", zap.String("rpcServer", rpcServer), zap.Error(err))
			continue
		}

		block, err := client.Block(context.Background(), &safeHeight)
		if err != nil {
			s.logger.Error("error getting latest block", zap.String("rpcServer", rpcServer), zap.Error(err))
			continue
		}

		trustHeight = block.Block.Height
		trustHash = block.Block.Hash().String()

		return trustHeight, trustHash, nil
	}

	return 0, "", fmt.Errorf("no usable block found for state sync")
}
