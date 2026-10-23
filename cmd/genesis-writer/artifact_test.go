package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cometbft/cometbft/privval"
	"go.uber.org/zap"
)

// The manifest is what operators read heights and hashes from instead of log
// lines, so every field has to come from the artifact it describes.
func TestManifestDescribesTheArtifact(t *testing.T) {
	dataDir := t.TempDir()
	chainID := "audius-mainnet-beta"
	home := cmtHome(dataDir, chainID)
	genesisFile := filepath.Join(home, "config", "genesis.json")
	keyFile := filepath.Join(home, "config", "priv_validator_key.json")
	if err := ensureGenesisFiles(genesisFile, keyFile, chainID, time.Unix(1_700_000_000, 0).UTC(), zap.NewNop()); err != nil {
		t.Fatal(err)
	}

	dump := &DumpInfo{Path: chainDumpDirName, Format: "directory", PgDumpVersion: "pg_dump (PostgreSQL) 15.8", PgDumpMajor: 15, ServerVersion: "15.8", ServerMajor: 15, RestoreMinMajor: 15}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.FixedZone("PDT", -7*3600))
	m, err := newManifest(manifestInputs{
		DataDir:                dataDir,
		ChainID:                chainID,
		EndHeight:              5839,
		SourceChainID:          defaultSourceChainID,
		SourceLastIndexedBlock: 12_345_678,
		GenesisFile:            genesisFile,
		MigrationAddress:       "0x7D01Cd0A89cc73F5a6DBEd10992AA472A2312D5F",
		Dump:                   dump,
		WriterCommit:           "abc123",
		Now:                    now,
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(genesisFile)
	sum := sha256.Sum256(raw)
	pv := privval.LoadFilePVEmptyState(keyFile, "")

	checks := []struct {
		name      string
		got, want any
	}{
		{"chain_id", m.ChainID, chainID},
		{"end_height", m.EndHeight, int64(5839)},
		{"first_live_height", m.FirstLiveHeight, int64(5840)},
		{"source_chain_id", m.SourceChainID, "audius-mainnet-alpha-beta"},
		{"source_last_indexed_block", m.SourceLastIndexedBlock, int64(12_345_678)},
		// One past the snapshot: the flusher deletes confirmed_block < value,
		// so the snapshot's own last block would otherwise replay twice.
		{"new_chain_flush_from_block", m.NewChainFlushFromBlock, int64(12_345_679)},
		{"genesis_sha256", m.GenesisSHA256, hex.EncodeToString(sum[:])},
		{"genesis_validator_address", m.GenesisValidatorAddress, pv.Key.Address.String()},
		{"genesis_migration_address", m.GenesisMigrationAddress, "0x7D01Cd0A89cc73F5a6DBEd10992AA472A2312D5F"},
		{"writer_commit", m.WriterCommit, "abc123"},
		{"written_at is UTC", m.WrittenAt, now.UTC()},
		{"dump", m.Dump, dump},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	// Per-node identity is listed, and presence reflects the directory: the
	// writer itself created the validator key and state, nothing created the
	// node key or address book.
	present := map[string]bool{}
	for _, e := range m.ExcludeFromBootstrap {
		if e.Reason == "" {
			t.Errorf("%s has no reason", e.Path)
		}
		present[e.Path] = e.Present
	}
	wantPresent := map[string]bool{
		"core/audius-mainnet-beta/config/node_key.json":           false,
		"core/audius-mainnet-beta/config/priv_validator_key.json": true,
		"core/audius-mainnet-beta/config/addrbook.json":           false,
		"core/audius-mainnet-beta/data/priv_validator_state.json": true,
	}
	if len(present) != len(wantPresent) {
		t.Errorf("exclude_from_bootstrap = %v, want keys of %v", present, wantPresent)
	}
	for p, want := range wantPresent {
		got, ok := present[p]
		if !ok {
			t.Errorf("exclude_from_bootstrap is missing %s", p)
		} else if got != want {
			t.Errorf("%s present = %v, want %v", p, got, want)
		}
	}

	// The JSON keys are the contract runbooks read; pin them.
	path := filepath.Join(dataDir, manifestFileName)
	if err := writeManifest(path, m); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"chain_id", "end_height", "first_live_height", "source_chain_id",
		"source_last_indexed_block", "new_chain_flush_from_block", "genesis_sha256",
		"genesis_validator_address", "genesis_migration_address", "dump",
		"writer_commit", "written_at", "exclude_from_bootstrap",
	} {
		if _, ok := doc[k]; !ok {
			t.Errorf("MANIFEST.json missing key %q", k)
		}
	}
	d := doc["dump"].(map[string]any)
	for _, k := range []string{"path", "format", "pg_dump_version", "pg_dump_major", "server_version", "server_major", "restore_min_pg_major"} {
		if _, ok := d[k]; !ok {
			t.Errorf("MANIFEST.json dump missing key %q", k)
		}
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temp manifest left behind: %v", err)
	}
}

func TestManifestWithoutDumpRecordsNull(t *testing.T) {
	dataDir := t.TempDir()
	home := cmtHome(dataDir, "c")
	genesisFile := filepath.Join(home, "config", "genesis.json")
	if err := ensureGenesisFiles(genesisFile, filepath.Join(home, "config", "priv_validator_key.json"), "c", time.Now(), zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	m, err := newManifest(manifestInputs{DataDir: dataDir, ChainID: "c", EndHeight: 1, GenesisFile: genesisFile, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"dump":null`) {
		t.Errorf("want explicit null dump, got %s", b)
	}
}

func TestParsePgToolMajor(t *testing.T) {
	cases := map[string]int{
		"pg_dump (PostgreSQL) 15.8 (Debian 15.8-1.pgdg120+1)": 15,
		"pg_dump (PostgreSQL) 17.2 (Homebrew)\n":              17,
		"pg_ctl (PostgreSQL) 16.4":                            16,
		"pg_dump (PostgreSQL) 18beta1":                        18,
	}
	for in, want := range cases {
		got, err := parsePgToolMajor(in)
		if err != nil || got != want {
			t.Errorf("parsePgToolMajor(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	if _, err := parsePgToolMajor("garbage"); err == nil {
		t.Error("want error for unrecognized output")
	}
	if got := serverMajorFromNum(150008); got != 15 {
		t.Errorf("serverMajorFromNum(150008) = %d", got)
	}
	if got := serverMajorFromNum(170002); got != 17 {
		t.Errorf("serverMajorFromNum(170002) = %d", got)
	}
}

// server <= pg_dump <= node, or the output cannot reach the node.
func TestPostgresVersionChecks(t *testing.T) {
	const node = nodePostgresMajor
	if node != 15 {
		t.Fatalf("nodePostgresMajor = %d; the node image (cmd/openaudio/Dockerfile) bundles postgresql-15 — update both together", node)
	}

	// Server major.
	if err := checkServerMajor(15, node, false); err != nil {
		t.Errorf("same major refused: %v", err)
	}
	if err := checkServerMajor(14, node, false); err != nil {
		t.Errorf("older server refused: %v", err)
	}
	err := checkServerMajor(17, node, false)
	if err == nil {
		t.Fatal("newer server (the 2026-08-25 PG17 case) accepted without override")
	}
	if !strings.Contains(err.Error(), "--allow-newer-postgres") {
		t.Errorf("refusal does not name the override: %v", err)
	}
	if err := checkServerMajor(17, node, true); err != nil {
		t.Errorf("override ignored: %v", err)
	}

	// pg_dump major.
	cases := []struct {
		dump, server int
		allow, want  bool
	}{
		{15, 15, false, true},
		{15, 14, false, true},  // newer client, older server: fine
		{14, 15, false, false}, // pg_dump refuses a newer server
		{17, 15, false, false}, // archive unreadable by pg_restore 15
		{17, 15, true, true},
		{16, 17, true, false}, // still cannot dump a newer server
	}
	for _, c := range cases {
		if got := pgDumpFits(c.dump, c.server, node, c.allow); got != c.want {
			t.Errorf("pgDumpFits(dump=%d, server=%d, allow=%v) = %v, want %v", c.dump, c.server, c.allow, got, c.want)
		}
	}
}

func TestPgDumpKeepsPasswordOffArgv(t *testing.T) {
	conn, env := pgDumpConn("postgres://writer:s3cret@localhost:5440/openaudio?sslmode=disable")
	if strings.Contains(conn, "s3cret") {
		t.Errorf("password on argv: %s", conn)
	}
	if conn != "postgres://writer@localhost:5440/openaudio?sslmode=disable" {
		t.Errorf("conn = %s", conn)
	}
	if !slices.Equal(env, []string{"PGPASSWORD=s3cret"}) {
		t.Errorf("env = %v", env)
	}

	conn, env = pgDumpConn("postgres://writer@localhost:5440/openaudio")
	if conn != "postgres://writer@localhost:5440/openaudio" || env != nil {
		t.Errorf("passwordless DSN changed: %s %v", conn, env)
	}

	args := pgDumpArgs("CONN", "/out/chain.dump.partial", 4)
	for _, want := range []string{"--format=directory", "--jobs=4", "--file=/out/chain.dump.partial", "--exclude-table=genesis_writer_progress", "--dbname=CONN"} {
		if !slices.Contains(args, want) {
			t.Errorf("pg_dump args %v missing %s", args, want)
		}
	}
}
