//go:build integration

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	corev1connect "github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	genesisPkg "github.com/OpenAudio/go-openaudio/pkg/core/config/genesis"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
)

// testDelegatePrivKey is the Ethereum key for the devnet validator.
// Matches delegatePrivateKey in docker-compose.yml.
// Address: 0x73EB6d82CFB20bA669e9c178b718d770C49BB52f
// FOR LOCAL TESTING ONLY.
const testDelegatePrivKey = "d09ba371c359f10f22ccda12fd26c598c7921bda3220c9942174562bc6a36fe8"

// testMigrationPrivKey is the genesis_migration_address key (same as genesis-replay).
// Address: 0xF7Bd9D733fAD4e2f0594bb88180cef8D8D03dEbB
const testMigrationPrivKey = "fc972c220946cde07bf3c6be380cc956c3d5680a2d7b225c660866e450cf1299"

// TestGenesisWriter is a full end-to-end integration test.
//
// The test is self-contained: it starts the required Docker services
// (src-db, core-db, eth-ganache, ingress, openaudio-1), runs genesis-writer
// in-process, verifies entity counts with a lightweight indexer, confirms
// consensus is advancing, then tears everything down.
//
// Optional env overrides:
//
//	GENESIS_SRC_DSN   source DB DSN (default: postgres://postgres:postgres@localhost:5435/genesis_writer_source?sslmode=disable)
//	GENESIS_DST_DSN   core DB DSN  (default: postgres://postgres:postgres@localhost:5436/openaudio?sslmode=disable)
//	GENESIS_CHAIN_URL chain URL    (default: https://node1.oap.devnet)
//	OPENAUDIO_IMAGE   docker image  (default: openaudio/go-openaudio:dev)
//
// Run with:
//
//	go test -v -tags integration -run TestGenesisWriter -timeout 30m ./cmd/genesis-writer/...
func TestGenesisWriter(t *testing.T) {
	srcDSN := envOrDefault("GENESIS_SRC_DSN", "postgres://postgres:postgres@localhost:5435/genesis_writer_source?sslmode=disable")
	dstDSN := envOrDefault("GENESIS_DST_DSN", "postgres://postgres:postgres@localhost:5436/openaudio?sslmode=disable")
	chainURL := envOrDefault("GENESIS_CHAIN_URL", "https://node1.oap.devnet")

	migrationKey, err := parsePrivKey(testMigrationPrivKey)
	require.NoError(t, err, "parse migration private key")

	logger, _ := zap.NewDevelopment()
	defer logger.Sync() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	// ---- 0. Start infrastructure ---------------------------------------------
	// workDir is the unified work directory for this test run:
	//   gw-src-pg-data/    – postgres bind-mount for src-db
	//   gw-core-pg-data/   – postgres bind-mount for core-db
	//   audius-mainnet-v2/ – CometBFT home written by genesis-writer
	//
	// t.TempDir registers its cleanup before our teardownAll cleanup, so
	// teardownAll (stopping containers) runs first and the directory is removed
	// only after Docker has released all file handles.
	workDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "gw-src-pg-data"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "gw-core-pg-data"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "gw-core-pg-2-data"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workDir, "oap2-core"), 0o755))

	composeFile := composeFilePath(t)
	composeEnv := append(os.Environ(),
		"EXT_DATA_DIR="+workDir,
		"GENESIS_WRITER_DATA_DIR="+workDir,
		"OPENAUDIO_IMAGE="+envOrDefault("OPENAUDIO_IMAGE", "openaudio/go-openaudio:dev"),
	)

	t.Log("starting infrastructure (src-db, core-db, eth-ganache, ingress)...")
	startBaseServices(t, composeFile, composeEnv)
	t.Cleanup(func() { teardownAll(composeFile, composeEnv) })

	// ---- 1. Snapshot expected state from source DB ---------------------------
	srcPool, err := pgxpool.New(ctx, srcDSN)
	require.NoError(t, err)
	defer srcPool.Close()
	require.NoError(t, srcPool.Ping(ctx), "ping source DB")

	expected := snapshotSource(t, ctx, srcPool)
	t.Logf("source snapshot: %d users, %d tracks, %d playlists, %d follows, %d saves, %d reposts, %d plays, %d events, %d collaborators (%d accepted)",
		len(expected.users), len(expected.tracks), len(expected.playlists),
		len(expected.follows), len(expected.saves), len(expected.reposts), expected.playCount,
		len(expected.events), len(expected.collaborators), expected.acceptedCollaborators)

	// ---- 2. Set up CometBFT home directory -----------------------------------
	// Genesis-writer writes state.db + blockstore.db into cmtHome.
	// openaudio-1 mounts workDir as /data/core so that
	// /data/core/audius-mainnet-v2/ is the CometBFT root dir.
	cmtHome := filepath.Join(workDir, "audius-mainnet-v2")
	require.NoError(t, os.MkdirAll(filepath.Join(cmtHome, "config"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(cmtHome, "data"), 0o755))

	// Derive the validator's ed25519 key from the Ethereum delegate key.
	ethKey, err := common.EthToEthKey(testDelegatePrivKey)
	require.NoError(t, err, "parse delegate private key")
	cometKey, err := common.EthToCometKey(ethKey)
	require.NoError(t, err, "derive comet key")

	privValKeyFile := filepath.Join(cmtHome, "config", "priv_validator_key.json")
	privValStateFile := filepath.Join(cmtHome, "data", "priv_validator_state.json")
	pv := privval.NewFilePV(*cometKey, privValKeyFile, privValStateFile)
	pv.Save()
	t.Logf("validator address: %s", pv.GetAddress())

	// Write the genesis.json that genesis-writer needs for state.db construction.
	genDoc, err := genesisPkg.Read("dev")
	require.NoError(t, err, "read dev genesis")
	genesisFile := filepath.Join(cmtHome, "config", "genesis.json")
	require.NoError(t, genDoc.SaveAs(genesisFile), "save genesis.json")

	// ---- 3. Reset core DB schema and run genesis-writer ----------------------
	dstPool, err := pgxpool.New(ctx, dstDSN)
	require.NoError(t, err)
	defer dstPool.Close()
	require.NoError(t, dstPool.Ping(ctx), "ping core DB")
	_, err = dstPool.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public;")
	require.NoError(t, err, "reset core DB schema")

	t.Log("running genesis-writer...")
	cfg := &WriterConfig{
		SrcDSN:               srcDSN,
		DstDSN:               dstDSN,
		PrivKey:              migrationKey,
		Network:              "dev",
		ChainID:              genDoc.ChainID,
		GenesisTime:          genDoc.GenesisTime,
		GenesisFile:          genesisFile,
		PrivValidatorKeyFile: privValKeyFile,
		CMTHome:              cmtHome,
		MaxTxsPerBlock:       500,
		BatchSize:            100,
		RunMigrations:        true,
	}

	w, err := NewWriter(cfg, logger)
	require.NoError(t, err, "init writer")
	defer w.Close()
	require.NoError(t, w.Run(ctx), "run genesis-writer")

	genesisHeight := w.finalHeight
	t.Logf("genesis-writer done: height=%d, txs=%d, blocks=%d",
		genesisHeight, w.totalTxs, w.totalBlocks)

	// ---- 4. Verify core DB has synthetic blocks ------------------------------
	var blockCount, txCount int64
	require.NoError(t, dstPool.QueryRow(ctx, "SELECT count(*) FROM core_blocks").Scan(&blockCount))
	require.NoError(t, dstPool.QueryRow(ctx, "SELECT count(*) FROM core_transactions").Scan(&txCount))
	assert.Greater(t, blockCount, int64(0), "core_blocks should be populated")
	assert.Greater(t, txCount, int64(0), "core_transactions should be populated")
	assert.GreaterOrEqual(t, blockCount, genesisHeight, "core_blocks count should be at least the genesis height")
	t.Logf("core DB: %d blocks, %d transactions", blockCount, txCount)

	// state.db and blockstore.db are directories (PebbleDB), not plain files.
	require.DirExists(t, filepath.Join(cmtHome, "data", "state.db"))
	require.DirExists(t, filepath.Join(cmtHome, "data", "blockstore.db"))

	// ---- 5. Index the core DB and compare ------------------------------------
	// The lightweight indexer reads ManageEntityLegacyMigration transactions
	// directly from the core DB — no discovery-provider needed.
	t.Log("indexing chain transactions...")
	indexed := indexChain(t, ctx, dstPool, genesisHeight)
	t.Logf("indexed: %d users, %d tracks, %d playlists, %d follows, %d saves, %d reposts, %d plays, %d events, %d collaborators (%d accepted)",
		len(indexed.users), len(indexed.tracks), len(indexed.playlists),
		len(indexed.follows), len(indexed.saves), len(indexed.reposts), indexed.playCount,
		len(indexed.events), len(indexed.collaborators), indexed.acceptedCollaborators)

	assert.Equal(t, expected.users, indexed.users, "users mismatch")
	assert.Equal(t, expected.tracks, indexed.tracks, "tracks mismatch")
	assert.Equal(t, expected.playlists, indexed.playlists, "playlists mismatch")
	assert.Equal(t, expected.follows, indexed.follows, "follows mismatch")
	assert.Equal(t, expected.saves, indexed.saves, "saves mismatch")
	assert.Equal(t, expected.reposts, indexed.reposts, "reposts mismatch")
	assert.Equal(t, expected.playCount, indexed.playCount, "plays count mismatch")
	assert.Equal(t, expected.events, indexed.events, "events mismatch")
	assert.Equal(t, expected.collaborators, indexed.collaborators, "collaborators mismatch")
	assert.Equal(t, expected.acceptedCollaborators, indexed.acceptedCollaborators, "accepted collaborators mismatch")

	// ---- 6. Start openaudio-1 and verify consensus ---------------------------
	t.Log("starting openaudio-1...")
	startService(t, composeFile, composeEnv, "openaudio-1")

	t.Logf("waiting for openaudio-1 to produce blocks beyond genesis height %d...", genesisHeight)
	chainClient := newChainClient(chainURL)
	waitForHeight(t, ctx, chainClient, genesisHeight+2)
	t.Log("consensus confirmed: chain is advancing")

	// ---- 7. Wait for openaudio-1 to produce a snapshot -----------------------
	t.Log("waiting for openaudio-1 to create a snapshot...")
	waitForSnapshot(t, ctx, chainClient)

	// ---- 8. State sync openaudio-2 from openaudio-1 --------------------------
	// Read openaudio-1's node key so we can configure persistent peers.
	nodeKeyFile := filepath.Join(workDir, "audius-mainnet-v2", "config", "node_key.json")
	nodeKey, err := p2p.LoadNodeKey(nodeKeyFile)
	require.NoError(t, err, "load openaudio-1 node key")
	persistentPeers := fmt.Sprintf("%s@openaudio-1:26656", nodeKey.ID())
	t.Logf("openaudio-1 node ID: %s", nodeKey.ID())

	ssEnv := make([]string, len(composeEnv), len(composeEnv)+1)
	copy(ssEnv, composeEnv)
	ssEnv = append(ssEnv, "PERSISTENT_PEERS="+persistentPeers)

	t.Log("starting openaudio-2 (state sync from openaudio-1)...")
	startServiceWithProfile(t, composeFile, ssEnv, []string{"statesync"}, "openaudio-2")

	// Verify that openaudio-2's health check reports state sync is in progress.
	chain2URL := envOrDefault("GENESIS_CHAIN2_URL", "https://node2.oap.devnet")
	chain2Client := newChainClient(chain2URL)
	waitForStateSyncProgress(t, ctx, chain2Client)

	// ---- 9. Verify state-synced node has identical genesis data ---------------
	dst2DSN := envOrDefault("GENESIS_DST2_DSN", "postgres://postgres:postgres@localhost:5437/openaudio?sslmode=disable")
	db2Pool, err := pgxpool.New(ctx, dst2DSN)
	require.NoError(t, err)
	defer db2Pool.Close()
	require.NoError(t, db2Pool.Ping(ctx), "ping core-db-2")

	// Poll core-db-2 until state sync has restored the genesis transactions.
	// The docker healthcheck passes before the pg_restore completes.
	waitForStateSyncData(t, ctx, db2Pool, genesisHeight)

	indexed2 := indexChain(t, ctx, db2Pool, genesisHeight)
	t.Logf("state-synced node: %d users, %d tracks, %d playlists, %d follows, %d saves, %d reposts, %d plays, %d events, %d collaborators (%d accepted)",
		len(indexed2.users), len(indexed2.tracks), len(indexed2.playlists),
		len(indexed2.follows), len(indexed2.saves), len(indexed2.reposts), indexed2.playCount,
		len(indexed2.events), len(indexed2.collaborators), indexed2.acceptedCollaborators)

	assert.Equal(t, expected.users, indexed2.users, "state-synced users mismatch")
	assert.Equal(t, expected.tracks, indexed2.tracks, "state-synced tracks mismatch")
	assert.Equal(t, expected.playlists, indexed2.playlists, "state-synced playlists mismatch")
	assert.Equal(t, expected.follows, indexed2.follows, "state-synced follows mismatch")
	assert.Equal(t, expected.saves, indexed2.saves, "state-synced saves mismatch")
	assert.Equal(t, expected.reposts, indexed2.reposts, "state-synced reposts mismatch")
	assert.Equal(t, expected.playCount, indexed2.playCount, "state-synced plays count mismatch")
	assert.Equal(t, expected.events, indexed2.events, "state-synced events mismatch")
	assert.Equal(t, expected.collaborators, indexed2.collaborators, "state-synced collaborators mismatch")
	assert.Equal(t, expected.acceptedCollaborators, indexed2.acceptedCollaborators, "state-synced accepted collaborators mismatch")
	t.Log("state sync verification passed: all entities match")
}

// ---- helpers ----------------------------------------------------------------

func parsePrivKey(hexKey string) (*ecdsa.PrivateKey, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(hexKey, "0x"))
	if err != nil {
		return nil, err
	}
	return crypto.ToECDSA(b)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// composeFilePath returns the absolute path to cmd/genesis-writer/docker-compose.yml,
// derived from the location of this test file.
func composeFilePath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "docker-compose.yml")
}

// testComposeProject is the docker compose project name used by the integration
// test. It is distinct from "genesis-writer" (used by run-local.sh) so the two
// stacks can coexist on the same host without port or project conflicts.
const testComposeProject = "genesis-writer-test"

// startBaseServices tears down any previous test-project stack, then starts
// src-db, core-db, core-db-2, eth-ganache, and ingress and waits until healthy.
func startBaseServices(t *testing.T, composeFile string, env []string) {
	t.Helper()
	// Remove any remnants of a previous test run with the same project name.
	teardownAll(composeFile, env)

	cmd := exec.Command("docker", "compose", "-p", testComposeProject,
		"-f", composeFile, "up", "-d", "--wait",
		"src-db", "core-db", "core-db-2", "eth-ganache", "ingress")
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("docker compose up output:\n%s", string(out))
		require.NoError(t, err, "start base services")
	}
}

// startServiceWithProfile runs docker compose up -d --wait for the named
// services, activating the given profiles.
func startServiceWithProfile(t *testing.T, composeFile string, env []string, profiles []string, services ...string) {
	t.Helper()
	args := []string{"compose", "-p", testComposeProject, "-f", composeFile}
	for _, p := range profiles {
		args = append(args, "--profile", p)
	}
	args = append(args, "up", "-d", "--wait")
	args = append(args, services...)
	cmd := exec.Command("docker", args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("docker compose up output:\n%s", string(out))
		require.NoError(t, err, "docker compose up -d %s", strings.Join(services, " "))
	}
}

// startService runs docker compose up -d --wait for the named services (chain profile).
func startService(t *testing.T, composeFile string, env []string, services ...string) {
	t.Helper()
	startServiceWithProfile(t, composeFile, env, []string{"chain"}, services...)
}

// teardownAll stops all services (all profiles) and removes volumes.
func teardownAll(composeFile string, env []string) {
	_ = exec.Command("docker", "compose", "-p", testComposeProject,
		"-f", composeFile,
		"--profile", "chain", "--profile", "statesync",
		"down", "-v").Run()
}

// newChainClient creates a CoreService gRPC client for the chain.
func newChainClient(chainURL string) corev1connect.CoreServiceClient {
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
		Timeout: 10 * time.Second,
	}
	url := chainURL
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		url = "https://" + url
	}
	return corev1connect.NewCoreServiceClient(httpClient, url)
}

// waitForHeight polls the chain until it has produced a block at or beyond
// the target height, confirming that consensus is making progress.
func waitForHeight(t *testing.T, ctx context.Context, client corev1connect.CoreServiceClient, targetHeight int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		if ctx.Err() != nil {
			t.Fatal("context cancelled waiting for chain height")
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for chain height >= %d", targetHeight)
		}
		resp, err := client.GetBlock(ctx, connect.NewRequest(&corev1.GetBlockRequest{Height: targetHeight}))
		if err == nil && resp.Msg.Block != nil {
			t.Logf("chain reached height %d (hash=%s)", targetHeight, resp.Msg.Block.Hash)
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// waitForStateSyncData polls core-db-2 until state sync has restored genesis
// data AND the node has produced at least one block beyond the genesis height,
// confirming that the state-synced node is fully operational.
func waitForStateSyncData(t *testing.T, ctx context.Context, pool *pgxpool.Pool, genesisHeight int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for state sync to restore data in core-db-2")
		}
		var maxBlock int64
		err := pool.QueryRow(ctx,
			"SELECT COALESCE(MAX(height), 0) FROM core_blocks").Scan(&maxBlock)
		if err == nil && maxBlock > genesisHeight {
			t.Logf("state sync restored: max block height %d (genesis was %d)", maxBlock, genesisHeight)
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// waitForSnapshot polls openaudio-1 until at least one snapshot is available.
func waitForSnapshot(t *testing.T, ctx context.Context, client corev1connect.CoreServiceClient) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for snapshot to be created")
		}
		resp, err := client.GetStoredSnapshots(ctx, connect.NewRequest(&corev1.GetStoredSnapshotsRequest{}))
		if err == nil && len(resp.Msg.Snapshots) > 0 {
			snap := resp.Msg.Snapshots[0]
			t.Logf("snapshot available: height=%d, chunks=%d", snap.Height, snap.ChunkCount)
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// waitForStateSyncProgress polls openaudio-2's GetStatus endpoint until state
// sync has completed. State sync may finish so quickly that intermediate phases
// (DOWNLOADING_CHUNKS, RESTORING_PG_DUMP) are never observed; in that case
// syncInfo.synced=true indicates completion.
func waitForStateSyncProgress(t *testing.T, ctx context.Context, client corev1connect.CoreServiceClient) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for state sync to complete")
		}
		resp, err := client.GetStatus(ctx, connect.NewRequest(&corev1.GetStatusRequest{}))
		if err == nil && resp.Msg.SyncInfo != nil {
			if ss := resp.Msg.SyncInfo.GetStateSync(); ss != nil {
				t.Logf("openaudio-2 state sync phase: %s", ss.Phase.String())
			}
			if resp.Msg.SyncInfo.Synced {
				t.Log("openaudio-2 state sync complete (synced=true)")
				return
			}
		}
		time.Sleep(2 * time.Second)
	}
}

// ---- source snapshot --------------------------------------------------------

type sourceSnapshot struct {
	users                 []snapshotUser
	tracks                []snapshotTrack
	playlists             []snapshotPlaylist
	follows               []followPair
	saves                 []saveTuple
	reposts               []repostTuple
	playCount             int
	events                []snapshotEvent
	collaborators         []collabTuple
	acceptedCollaborators int
}

type snapshotUser struct {
	UserID   int64
	Handle   string
	Name     string
	Bio      string
	Location string
}

type snapshotTrack struct {
	TrackID int64
	OwnerID int64
	Title   string
	Genre   string
}

type snapshotPlaylist struct {
	PlaylistID      int64
	PlaylistOwnerID int64
	PlaylistName    string
	IsAlbum         bool
}

type followPair struct{ Follower, Followee int64 }

type saveTuple struct {
	UserID   int64
	ItemID   int64
	SaveType string
}

type repostTuple struct {
	UserID     int64
	ItemID     int64
	RepostType string
}

type snapshotEvent struct {
	EventID   int64
	EventType string
	UserID    int64
}

type collabTuple struct {
	TrackID int64
	UserID  int64
	Status  string
}

func snapshotSource(t *testing.T, ctx context.Context, db *pgxpool.Pool) sourceSnapshot {
	t.Helper()
	var s sourceSnapshot
	s.users = queryUsers(t, ctx, db)
	s.tracks = queryTracks(t, ctx, db)
	s.playlists = queryPlaylists(t, ctx, db)
	s.follows = queryFollows(t, ctx, db)
	s.saves = querySaves(t, ctx, db)
	s.reposts = queryReposts(t, ctx, db)
	s.playCount = queryPlayCount(t, ctx, db)
	s.events = queryEvents(t, ctx, db)
	s.collaborators, s.acceptedCollaborators = queryCollaborators(t, ctx, db)
	return s
}

func queryUsers(t *testing.T, ctx context.Context, db *pgxpool.Pool) []snapshotUser {
	t.Helper()
	rows, err := db.Query(ctx, `
		SELECT user_id,
		       COALESCE(handle, ''), COALESCE(name, ''),
		       COALESCE(bio, ''), COALESCE(location, '')
		FROM users
		WHERE is_current = true AND is_deactivated = false AND is_available = true
		ORDER BY user_id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []snapshotUser
	for rows.Next() {
		var u snapshotUser
		require.NoError(t, rows.Scan(&u.UserID, &u.Handle, &u.Name, &u.Bio, &u.Location))
		out = append(out, u)
	}
	require.NoError(t, rows.Err())
	return out
}

func queryTracks(t *testing.T, ctx context.Context, db *pgxpool.Pool) []snapshotTrack {
	t.Helper()
	rows, err := db.Query(ctx, `
		SELECT track_id, owner_id, COALESCE(title, ''), COALESCE(genre, '')
		FROM tracks
		WHERE is_current = true AND is_delete = false AND is_available = true
		ORDER BY track_id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []snapshotTrack
	for rows.Next() {
		var tr snapshotTrack
		require.NoError(t, rows.Scan(&tr.TrackID, &tr.OwnerID, &tr.Title, &tr.Genre))
		out = append(out, tr)
	}
	require.NoError(t, rows.Err())
	return out
}

func queryPlaylists(t *testing.T, ctx context.Context, db *pgxpool.Pool) []snapshotPlaylist {
	t.Helper()
	rows, err := db.Query(ctx, `
		SELECT playlist_id, playlist_owner_id, COALESCE(playlist_name, ''), is_album
		FROM playlists
		WHERE is_current = true AND is_delete = false
		ORDER BY playlist_id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []snapshotPlaylist
	for rows.Next() {
		var p snapshotPlaylist
		require.NoError(t, rows.Scan(&p.PlaylistID, &p.PlaylistOwnerID, &p.PlaylistName, &p.IsAlbum))
		out = append(out, p)
	}
	require.NoError(t, rows.Err())
	return out
}

func queryFollows(t *testing.T, ctx context.Context, db *pgxpool.Pool) []followPair {
	t.Helper()
	rows, err := db.Query(ctx, `
		SELECT follower_user_id, followee_user_id
		FROM follows
		WHERE is_current = true AND is_delete = false
		ORDER BY follower_user_id, followee_user_id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []followPair
	for rows.Next() {
		var f followPair
		require.NoError(t, rows.Scan(&f.Follower, &f.Followee))
		out = append(out, f)
	}
	require.NoError(t, rows.Err())
	return out
}

func querySaves(t *testing.T, ctx context.Context, db *pgxpool.Pool) []saveTuple {
	t.Helper()
	rows, err := db.Query(ctx, `
		SELECT user_id, save_item_id, save_type
		FROM saves
		WHERE is_current = true AND is_delete = false
		ORDER BY user_id, save_item_id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []saveTuple
	for rows.Next() {
		var s saveTuple
		require.NoError(t, rows.Scan(&s.UserID, &s.ItemID, &s.SaveType))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

func queryReposts(t *testing.T, ctx context.Context, db *pgxpool.Pool) []repostTuple {
	t.Helper()
	rows, err := db.Query(ctx, `
		SELECT user_id, repost_item_id, repost_type
		FROM reposts
		WHERE is_current = true AND is_delete = false
		ORDER BY user_id, repost_item_id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []repostTuple
	for rows.Next() {
		var rp repostTuple
		require.NoError(t, rows.Scan(&rp.UserID, &rp.ItemID, &rp.RepostType))
		out = append(out, rp)
	}
	require.NoError(t, rows.Err())
	return out
}

func queryPlayCount(t *testing.T, ctx context.Context, db *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM plays`).Scan(&n))
	return n
}

func queryEvents(t *testing.T, ctx context.Context, db *pgxpool.Pool) []snapshotEvent {
	t.Helper()
	rows, err := db.Query(ctx, `
		SELECT event_id, event_type::text, user_id
		FROM events
		WHERE is_deleted = false
		ORDER BY event_id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []snapshotEvent
	for rows.Next() {
		var e snapshotEvent
		require.NoError(t, rows.Scan(&e.EventID, &e.EventType, &e.UserID))
		out = append(out, e)
	}
	require.NoError(t, rows.Err())
	return out
}

func queryCollaborators(t *testing.T, ctx context.Context, db *pgxpool.Pool) ([]collabTuple, int) {
	t.Helper()
	rows, err := db.Query(ctx, `
		SELECT track_id, collaborator_user_id, status
		FROM track_collaborators
		WHERE status IN ('pending', 'accepted')
		ORDER BY track_id, collaborator_user_id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []collabTuple
	accepted := 0
	for rows.Next() {
		var c collabTuple
		require.NoError(t, rows.Scan(&c.TrackID, &c.UserID, &c.Status))
		out = append(out, c)
		if c.Status == "accepted" {
			accepted++
		}
	}
	require.NoError(t, rows.Err())
	return out, accepted
}

// ---- lightweight chain indexer ----------------------------------------------

// indexChain reads all ManageEntityLegacyMigration and TrackPlays transactions
// from core_transactions and reconstructs entity state for comparison with the
// source snapshot. No discovery-provider is needed.
func indexChain(t *testing.T, ctx context.Context, pool *pgxpool.Pool, maxHeight int64) sourceSnapshot {
	t.Helper()

	rows, err := pool.Query(ctx, `
		SELECT transaction FROM core_transactions
		WHERE block_id <= $1
		ORDER BY block_id, index`, maxHeight)
	require.NoError(t, err)
	defer rows.Close()

	userMap := map[int64]snapshotUser{}
	trackMap := map[int64]snapshotTrack{}
	playlistMap := map[int64]snapshotPlaylist{}

	type followKey struct{ Follower, Followee int64 }
	followSet := map[followKey]bool{}

	type saveKey struct {
		UserID, ItemID int64
		SaveType       string
	}
	saveSet := map[saveKey]bool{}

	type repostKey struct {
		UserID, ItemID int64
		RepostType     string
	}
	repostSet := map[repostKey]bool{}

	eventMap := map[int64]snapshotEvent{}

	type collabKey struct{ TrackID, UserID int64 }
	collabSet := map[collabKey]string{} // status

	playCount := 0

	for rows.Next() {
		var txBytes []byte
		require.NoError(t, rows.Scan(&txBytes))

		var stx corev1.SignedTransaction
		require.NoError(t, proto.Unmarshal(txBytes, &stx), "unmarshal transaction")

		switch tx := stx.Transaction.(type) {
		case *corev1.SignedTransaction_ManageEntityMigration:
			me := tx.ManageEntityMigration
			// Action is the primary discriminator; entity_type refines within each action.
			switch me.Action {
			case "Create":
				switch me.EntityType {
				case "User":
					var w struct {
						Data struct {
							Handle   string `json:"handle"`
							Name     string `json:"name"`
							Bio      string `json:"bio"`
							Location string `json:"location"`
						} `json:"data"`
					}
					_ = json.Unmarshal([]byte(me.Metadata), &w)
					userMap[me.EntityId] = snapshotUser{
						UserID:   me.EntityId,
						Handle:   w.Data.Handle,
						Name:     w.Data.Name,
						Bio:      w.Data.Bio,
						Location: w.Data.Location,
					}
				case "Track":
					var w struct {
						Data struct {
							OwnerID       int64   `json:"owner_id"`
							Title         string  `json:"title"`
							Genre         string  `json:"genre"`
							Collaborators []int64 `json:"collaborators"`
						} `json:"data"`
					}
					_ = json.Unmarshal([]byte(me.Metadata), &w)
					trackMap[me.EntityId] = snapshotTrack{
						TrackID: me.EntityId,
						OwnerID: w.Data.OwnerID,
						Title:   w.Data.Title,
						Genre:   w.Data.Genre,
					}
					// Collaborators in Track:Create metadata become pending invites.
					for _, uid := range w.Data.Collaborators {
						collabSet[collabKey{me.EntityId, uid}] = "pending"
					}
				case "Playlist":
					var w struct {
						Data struct {
							PlaylistName string `json:"playlist_name"`
							IsAlbum      bool   `json:"is_album"`
						} `json:"data"`
					}
					_ = json.Unmarshal([]byte(me.Metadata), &w)
					playlistMap[me.EntityId] = snapshotPlaylist{
						PlaylistID:      me.EntityId,
						PlaylistOwnerID: me.UserId,
						PlaylistName:    w.Data.PlaylistName,
						IsAlbum:         w.Data.IsAlbum,
					}
				case "Event":
					var meta struct {
						EventType string `json:"event_type"`
					}
					_ = json.Unmarshal([]byte(me.Metadata), &meta)
					eventMap[me.EntityId] = snapshotEvent{
						EventID:   me.EntityId,
						EventType: meta.EventType,
						UserID:    me.UserId,
					}
				}
			case "Approve":
				if me.EntityType == "TrackCollaborator" {
					collabSet[collabKey{me.EntityId, me.UserId}] = "accepted"
				}
			case "Follow":
				// UserId=follower, EntityId=followee
				followSet[followKey{me.UserId, me.EntityId}] = true
			case "Save":
				saveType := "track"
				if me.EntityType == "Playlist" {
					saveType = "playlist"
				}
				saveSet[saveKey{me.UserId, me.EntityId, saveType}] = true
			case "Repost":
				repostType := "track"
				if me.EntityType == "Playlist" {
					repostType = "playlist"
				}
				repostSet[repostKey{me.UserId, me.EntityId, repostType}] = true
			}

		case *corev1.SignedTransaction_Plays:
			playCount += len(tx.Plays.Plays)
		}
	}
	require.NoError(t, rows.Err())

	// Convert maps to sorted slices matching sourceSnapshot ordering.
	var s sourceSnapshot
	s.playCount = playCount

	for _, u := range userMap {
		s.users = append(s.users, u)
	}
	sort.Slice(s.users, func(i, j int) bool { return s.users[i].UserID < s.users[j].UserID })

	for _, tr := range trackMap {
		s.tracks = append(s.tracks, tr)
	}
	sort.Slice(s.tracks, func(i, j int) bool { return s.tracks[i].TrackID < s.tracks[j].TrackID })

	for _, p := range playlistMap {
		s.playlists = append(s.playlists, p)
	}
	sort.Slice(s.playlists, func(i, j int) bool { return s.playlists[i].PlaylistID < s.playlists[j].PlaylistID })

	for k := range followSet {
		s.follows = append(s.follows, followPair{k.Follower, k.Followee})
	}
	sort.Slice(s.follows, func(i, j int) bool {
		if s.follows[i].Follower != s.follows[j].Follower {
			return s.follows[i].Follower < s.follows[j].Follower
		}
		return s.follows[i].Followee < s.follows[j].Followee
	})

	for k := range saveSet {
		s.saves = append(s.saves, saveTuple{k.UserID, k.ItemID, k.SaveType})
	}
	sort.Slice(s.saves, func(i, j int) bool {
		if s.saves[i].UserID != s.saves[j].UserID {
			return s.saves[i].UserID < s.saves[j].UserID
		}
		return s.saves[i].ItemID < s.saves[j].ItemID
	})

	for k := range repostSet {
		s.reposts = append(s.reposts, repostTuple{k.UserID, k.ItemID, k.RepostType})
	}
	sort.Slice(s.reposts, func(i, j int) bool {
		if s.reposts[i].UserID != s.reposts[j].UserID {
			return s.reposts[i].UserID < s.reposts[j].UserID
		}
		return s.reposts[i].ItemID < s.reposts[j].ItemID
	})

	for _, e := range eventMap {
		s.events = append(s.events, e)
	}
	sort.Slice(s.events, func(i, j int) bool { return s.events[i].EventID < s.events[j].EventID })

	for k, status := range collabSet {
		s.collaborators = append(s.collaborators, collabTuple{k.TrackID, k.UserID, status})
		if status == "accepted" {
			s.acceptedCollaborators++
		}
	}
	sort.Slice(s.collaborators, func(i, j int) bool {
		if s.collaborators[i].TrackID != s.collaborators[j].TrackID {
			return s.collaborators[i].TrackID < s.collaborators[j].TrackID
		}
		return s.collaborators[i].UserID < s.collaborators[j].UserID
	})

	return s
}
