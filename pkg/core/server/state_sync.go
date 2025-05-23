package server

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AudiusProject/audiusd/pkg/common"
	v1 "github.com/cometbft/cometbft/api/cometbft/abci/v1"
	"github.com/cometbft/cometbft/types"
)

func (s *Server) startStateSync() error {
	<-s.awaitRpcReady
	logger := s.logger.Child("state_sync")

	if !s.config.StateSync.ServeSnapshots {
		logger.Info("State sync is not enabled, skipping snapshot creation")
		return nil
	}

	node := s.node
	eb := node.EventBus()

	if eb == nil {
		return errors.New("event bus not ready")
	}

	subscriberID := "state-sync-subscriber"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	query := types.EventQueryNewBlock
	subscription, err := eb.Subscribe(ctx, subscriberID, query)
	if err != nil {
		return fmt.Errorf("failed to subscribe to NewBlock events: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("Stopping block event subscription")
			return nil
		case msg := <-subscription.Out():
			blockEvent := msg.Data().(types.EventDataNewBlock)
			blockHeight := blockEvent.Block.Height
			if blockHeight%s.config.StateSync.BlockInterval != 0 {
				continue
			}

			if err := s.createSnapshot(logger, blockHeight); err != nil {
				logger.Errorf("error creating snapshot: %v", err)
			}

			if err := s.pruneSnapshots(logger); err != nil {
				logger.Errorf("error pruning snapshots: %v", err)
			}
		case err := <-subscription.Canceled():
			s.logger.Errorf("Subscription cancelled: %v", err)
			return nil
		}
	}
}

func (s *Server) createSnapshot(logger *common.Logger, height int64) error {
	// create snapshot directory if it doesn't exist
	snapshotDir := filepath.Join(s.config.RootDir, fmt.Sprintf("snapshots_%s", s.config.GenesisFile.ChainID))
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

	logger.Info("Creating snapshot", "height", height)

	blockHeight := height
	blockHash := block.BlockID.Hash

	latestSnapshotDirName := fmt.Sprintf("height_%010d", blockHeight)
	latestSnapshotDir := filepath.Join(snapshotDir, latestSnapshotDirName)
	if err := os.MkdirAll(latestSnapshotDir, 0755); err != nil {
		return fmt.Errorf("error creating latest snapshot directory: %v", err)
	}

	logger.Info("Creating pg_dump", "height", blockHeight)

	if err := s.createPgDump(logger, latestSnapshotDir); err != nil {
		return fmt.Errorf("error creating pg_dump: %v", err)
	}

	logger.Info("Chunking pg_dump", "height", blockHeight)

	chunkCount, err := s.chunkPgDump(logger, latestSnapshotDir)
	if err != nil {
		return fmt.Errorf("error chunking pg_dump: %v", err)
	}

	logger.Info("Deleting pg_dump", "height", blockHeight)

	if err := s.deletePgDump(logger, latestSnapshotDir); err != nil {
		return fmt.Errorf("error deleting pg_dump: %v", err)
	}

	logger.Info("Writing snapshot metadata", "height", blockHeight)

	snapshotMetadata := v1.Snapshot{
		Height:   uint64(blockHeight),
		Format:   1,
		Chunks:   uint32(chunkCount),
		Hash:     blockHash,
		Metadata: []byte(s.config.GenesisFile.ChainID),
	}

	snapshotMetadataFile := filepath.Join(latestSnapshotDir, "metadata.json")
	jsonBytes, err := json.Marshal(snapshotMetadata)
	if err != nil {
		return fmt.Errorf("error marshalling snapshot metadata: %v", err)
	}

	if err := os.WriteFile(snapshotMetadataFile, jsonBytes, 0644); err != nil {
		return fmt.Errorf("error writing snapshot metadata: %v", err)
	}

	logger.Info("Snapshot created", "height", blockHeight)

	return nil
}

// createPgDump creates a pg_dump of the database and writes it to the latest snapshot directory
func (s *Server) createPgDump(logger *common.Logger, latestSnapshotDir string) error {
	pgString := s.config.PSQLConn
	dumpPath := filepath.Join(latestSnapshotDir, "data.dump")

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
		logger.Error("pg_dump failed", "error", err, "output", string(output))
		return fmt.Errorf("pg_dump failed: %w", err)
	}

	logger.Info("pg_dump succeeded", "output", string(output))
	return nil
}

// chunkPgDump splits the pg_dump into 16MB gzip-compressed chunks and returns the number of chunks created
func (s *Server) chunkPgDump(logger *common.Logger, latestSnapshotDir string) (int, error) {
	const chunkSize = 16 * 1024 * 1024 // 16MB
	dumpPath := filepath.Join(latestSnapshotDir, "data.dump")

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

		chunkName := fmt.Sprintf("chunk_%04d.gz", chunkIndex)
		chunkPath := filepath.Join(latestSnapshotDir, chunkName)
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

		logger.Info("Wrote chunk", "path", chunkPath, "size", n)
		chunkIndex++

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
	}

	return chunkIndex, nil
}

func (s *Server) deletePgDump(logger *common.Logger, latestSnapshotDir string) error {
	dumpPath := filepath.Join(latestSnapshotDir, "data.dump")
	if err := os.Remove(dumpPath); err != nil {
		return fmt.Errorf("error deleting pg_dump: %w", err)
	}

	return nil
}

// Prunes snapshots by deleting the oldest ones while retaining the most recent ones
// based on the configured retention count
func (s *Server) pruneSnapshots(logger *common.Logger) error {
	snapshotDir := filepath.Join(s.config.RootDir, fmt.Sprintf("snapshots_%s", s.config.GenesisFile.ChainID))
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
		logger.Info("Deleted snapshot", "path", filepath.Join(snapshotDir, files[i].Name()))
	}

	return nil
}

func (s *Server) getStoredSnapshots() ([]v1.Snapshot, error) {
	snapshotDir := filepath.Join(s.config.RootDir, fmt.Sprintf("snapshots_%s", s.config.GenesisFile.ChainID))

	dirs, err := os.ReadDir(snapshotDir)
	if err != nil {
		return nil, fmt.Errorf("error reading snapshot directory: %w", err)
	}

	snapshots := make([]v1.Snapshot, 0)
	for _, entry := range dirs {
		if !entry.IsDir() {
			continue
		}

		metadataPath := filepath.Join(snapshotDir, entry.Name(), "metadata.json")
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
	snapshotDir := filepath.Join(s.config.RootDir, fmt.Sprintf("snapshots_%s", s.config.GenesisFile.ChainID))
	latestSnapshotDirName := fmt.Sprintf("height_%010d", height)
	latestSnapshotDir := filepath.Join(snapshotDir, latestSnapshotDirName)

	// Check if snapshot directory exists
	if _, err := os.Stat(latestSnapshotDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("no snapshot found for height %d", height)
	}

	// Read metadata to get chunk count
	metadataPath := filepath.Join(latestSnapshotDir, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		return nil, fmt.Errorf("error reading metadata file: %v", err)
	}

	var meta v1.Snapshot
	if err := json.Unmarshal(metadataBytes, &meta); err != nil {
		return nil, fmt.Errorf("error unmarshalling metadata: %v", err)
	}

	// Read the first chunk (chunk_0000.gz)
	chunkPath := filepath.Join(latestSnapshotDir, fmt.Sprintf("chunk_%04d.gz", chunk))
	chunkData, err := os.ReadFile(chunkPath)
	if err != nil {
		return nil, fmt.Errorf("error reading chunk file: %v", err)
	}

	return chunkData, nil
}

func (s *Server) StoreOfferedSnapshot(snapshot *v1.Snapshot) error {
	snapshotDir := filepath.Join(s.config.RootDir, "tmp_reconstruction")
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return fmt.Errorf("failed to create snapshot directory: %v", err)
	}

	metadataPath := filepath.Join(snapshotDir, "metadata.json")
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
	snapshotDir := filepath.Join(s.config.RootDir, "tmp_reconstruction")
	metadataPath := filepath.Join(snapshotDir, "metadata.json")
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
	tmpDir := filepath.Join(s.config.RootDir, "tmp_reconstruction")
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return fmt.Errorf("failed to create temporary directory: %v", err)
	}

	// Create a directory for this specific height if it doesn't exist
	heightDir := filepath.Join(tmpDir, fmt.Sprintf("height_%010d", height))
	if err := os.MkdirAll(heightDir, 0755); err != nil {
		return fmt.Errorf("failed to create height directory: %v", err)
	}

	// Write the chunk to a file
	chunkPath := filepath.Join(heightDir, fmt.Sprintf("chunk_%04d.gz", chunkIndex))
	if err := os.WriteFile(chunkPath, chunkData, 0644); err != nil {
		return fmt.Errorf("failed to write chunk file: %v", err)
	}

	return nil
}

func (s *Server) haveAllChunks(height uint64, total int) bool {
	heightDir := filepath.Join(s.config.RootDir, "tmp_reconstruction", fmt.Sprintf("height_%010d", height))

	files, err := os.ReadDir(heightDir)
	if err != nil {
		s.logger.Warn("failed to read chunk directory", "err", err)
		return false
	}

	chunkCount := 0
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "chunk_") && strings.HasSuffix(f.Name(), ".gz") {
			chunkCount++
		}
	}

	return chunkCount == total
}

// ReassemblePgDump reconstructs and decompresses a binary pg_dump file from multiple gzipped chunks
func (s *Server) ReassemblePgDump(height int64) error {
	tmpDir := filepath.Join(s.config.RootDir, "tmp_reconstruction")
	heightDir := filepath.Join(tmpDir, fmt.Sprintf("height_%010d", height))

	// Create the output pg_dump file in binary format
	outputPath := filepath.Join(heightDir, "pg_dump.dump")
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
	tmpDir := filepath.Join(s.config.RootDir, "tmp_reconstruction")
	heightDir := filepath.Join(tmpDir, fmt.Sprintf("height_%010d", height))
	dumpPath := filepath.Join(heightDir, "pg_dump.dump")

	cmd := exec.Command("pg_restore",
		"--dbname="+s.config.PSQLConn,
		"--clean",
		"--if-exists",
		dumpPath)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		s.logger.Error("pg_restore failed",
			"err", err,
			"stderr", stderr.String(),
			"stdout", stdout.String(),
		)
		return fmt.Errorf("error restoring database: %w", err)
	}

	if err := os.RemoveAll(heightDir); err != nil {
		return fmt.Errorf("error cleaning up temporary files: %w", err)
	}

	return nil
}
