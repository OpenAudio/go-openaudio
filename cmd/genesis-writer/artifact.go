package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	corecfg "github.com/OpenAudio/go-openaudio/pkg/core/config"
	cmttypes "github.com/cometbft/cometbft/types"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// The writer's output is a portable artifact rather than a live database:
//
//	<data-dir>/
//	  core/<chain-id>/   CometBFT home (genesis, blockstore, state)
//	  chain.dump/        pg_dump -Fd of the chain database
//	  MANIFEST.json      what was written, from what, and how to seed a node
//
// A live database invites anything with its DSN to connect, and a node whose
// binary does not embed this genesis runs the core migrations down on connect.
// That destroyed a 3h36m write on 2026-08-25. The dump is taken before the
// writer exits, so nothing else has had a chance to touch the database.
const (
	chainDumpDirName = "chain.dump"
	manifestFileName = "MANIFEST.json"

	// defaultSourceChainID is the old chain the production snapshot indexed.
	defaultSourceChainID = "audius-mainnet-alpha-beta"
)

// ArtifactManifest is MANIFEST.json. Operators and runbooks read values from
// it instead of copying them out of log lines.
type ArtifactManifest struct {
	ChainID string `json:"chain_id"`
	// EndHeight is the last block the writer produced, and the genesis
	// migration end height. The bootstrap node's first live block is
	// EndHeight+1.
	EndHeight       int64 `json:"end_height"`
	FirstLiveHeight int64 `json:"first_live_height"`

	// SourceLastIndexedBlock is MAX(height) in the source snapshot's
	// core_indexed_blocks for SourceChainID: the last old-chain block whose
	// effects the migration carries.
	SourceChainID          string `json:"source_chain_id"`
	SourceLastIndexedBlock int64  `json:"source_last_indexed_block"`
	// NewChainFlushFromBlock is the value for the API's flusher. It deletes
	// rows with confirmed_block < this value, so it must be one past the
	// snapshot's last block or that block is replayed onto the new chain twice.
	NewChainFlushFromBlock int64 `json:"new_chain_flush_from_block"`

	GenesisSHA256           string `json:"genesis_sha256"`
	GenesisValidatorAddress string `json:"genesis_validator_address"`
	GenesisMigrationAddress string `json:"genesis_migration_address"`

	Dump *DumpInfo `json:"dump"`

	WriterCommit string    `json:"writer_commit"`
	WrittenAt    time.Time `json:"written_at"`

	// ExcludeFromBootstrap lists per-node identity under the CometBFT home that
	// must not be seeded onto another node. Paths are relative to the data dir.
	ExcludeFromBootstrap []ExcludedFile `json:"exclude_from_bootstrap"`
}

// DumpInfo records the chain dump and the Postgres versions it is tied to.
type DumpInfo struct {
	Path            string `json:"path"` // relative to the data dir
	Format          string `json:"format"`
	PgDumpVersion   string `json:"pg_dump_version"`
	PgDumpMajor     int    `json:"pg_dump_major"`
	ServerVersion   string `json:"server_version"`
	ServerMajor     int    `json:"server_major"`
	RestoreMinMajor int    `json:"restore_min_pg_major"` // oldest pg_restore that reads it
	SizeBytes       int64  `json:"size_bytes"`
}

// ExcludedFile is one path operators must not copy onto a bootstrap node.
type ExcludedFile struct {
	Path    string `json:"path"`
	Reason  string `json:"reason"`
	Present bool   `json:"present"` // whether it existed when the manifest was written
}

// bootstrapExclusions is the per-node identity a CometBFT home accumulates.
// None of it belongs to the chain; all of it belongs to whichever process last
// ran against the directory. See ROLLOUT.md Appendix E.
func bootstrapExclusions(chainID string) []ExcludedFile {
	home := filepath.Join("core", chainID)
	return []ExcludedFile{
		{
			Path:   filepath.Join(home, "config", "node_key.json"),
			Reason: "P2P identity of whichever node last ran here; a node derives its own from its delegate key",
		},
		{
			Path:   filepath.Join(home, "config", "priv_validator_key.json"),
			Reason: "consensus key; the bootstrap derives it from OPENAUDIO_DELEGATE_PRIVATE_KEY, and it must match genesis_validator_address",
		},
		{
			Path:   filepath.Join(home, "config", "addrbook.json"),
			Reason: "peer address book of whichever node last ran here",
		},
		{
			Path:   filepath.Join(home, "data", "priv_validator_state.json"),
			Reason: "double-sign guard of whichever node last signed here; a stale one can stop a node from starting",
		},
	}
}

// manifestInputs is everything newManifest needs, gathered by the writer.
type manifestInputs struct {
	DataDir                string
	ChainID                string
	EndHeight              int64
	SourceChainID          string
	SourceLastIndexedBlock int64
	GenesisFile            string // the emitted genesis.json
	MigrationAddress       string
	Dump                   *DumpInfo
	WriterCommit           string
	Now                    time.Time
}

func newManifest(in manifestInputs) (*ArtifactManifest, error) {
	raw, err := os.ReadFile(in.GenesisFile)
	if err != nil {
		return nil, fmt.Errorf("read genesis: %w", err)
	}
	sum := sha256.Sum256(raw)

	genDoc, err := cmttypes.GenesisDocFromJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("parse genesis: %w", err)
	}
	if len(genDoc.Validators) != 1 {
		return nil, fmt.Errorf("genesis lists %d validators; the writer signs every block with one key, so it must list exactly one", len(genDoc.Validators))
	}

	excl := bootstrapExclusions(in.ChainID)
	for i := range excl {
		_, err := os.Stat(filepath.Join(in.DataDir, excl[i].Path))
		excl[i].Present = err == nil
	}

	return &ArtifactManifest{
		ChainID:                 in.ChainID,
		EndHeight:               in.EndHeight,
		FirstLiveHeight:         in.EndHeight + 1,
		SourceChainID:           in.SourceChainID,
		SourceLastIndexedBlock:  in.SourceLastIndexedBlock,
		NewChainFlushFromBlock:  in.SourceLastIndexedBlock + 1,
		GenesisSHA256:           hex.EncodeToString(sum[:]),
		GenesisValidatorAddress: genDoc.Validators[0].Address.String(),
		GenesisMigrationAddress: in.MigrationAddress,
		Dump:                    in.Dump,
		WriterCommit:            in.WriterCommit,
		WrittenAt:               in.Now.UTC(),
		ExcludeFromBootstrap:    excl,
	}, nil
}

// writeFileAtomic writes via a temp file and rename so a reader never sees a
// partial manifest.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeManifest(path string, m *ArtifactManifest) error {
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'), 0o644)
}

// querySourceLastIndexedBlock returns the last old-chain block the source
// snapshot indexed. Zero rows is an error: a snapshot that indexed nothing is
// not a production snapshot, and a zero here would become a flush-from value
// that replays the old chain's entire history.
func querySourceLastIndexedBlock(ctx context.Context, src *pgxpool.Pool, chainID string) (int64, error) {
	var h *int64
	if err := src.QueryRow(ctx,
		`SELECT MAX(height) FROM core_indexed_blocks WHERE chain_id = $1`, chainID).Scan(&h); err != nil {
		return 0, fmt.Errorf("query core_indexed_blocks: %w", err)
	}
	if h == nil {
		return 0, fmt.Errorf("source core_indexed_blocks has no rows for chain %q (set --source-chain-id)", chainID)
	}
	return *h, nil
}

// writerCommit identifies the writer build: the VCS revision Go stamps into
// `go build` binaries, else the ldflags version, else "unknown".
func writerCommit() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		var rev string
		var dirty bool
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
		if rev != "" {
			if dirty {
				rev += "-dirty"
			}
			return rev
		}
	}
	if corecfg.Version != "" {
		return corecfg.Version
	}
	return "unknown"
}

// pgDumpConn splits a DSN into the connection string pg_dump receives on argv
// and the environment it needs, so a password never lands in argv.
func pgDumpConn(dsn string) (connStr string, env []string) {
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil {
		return dsn, nil
	}
	pw, ok := u.User.Password()
	if !ok {
		return dsn, nil
	}
	u.User = url.User(u.User.Username())
	return u.String(), []string{"PGPASSWORD=" + pw}
}

// defaultDumpJobs is the pg_dump -j used when --dump-jobs is unset. Each job
// is a server connection and a backend process, so stay well under the
// default max_connections and leave CPUs for the server itself.
func defaultDumpJobs() int {
	return min(max(runtime.NumCPU()/2, 1), 8)
}

// pgDumpArgs builds the pg_dump invocation. The writer's progress table is
// internal resume state, not chain state, so it stays out. Owners and ACLs are
// dropped because the node restores as its own role.
func pgDumpArgs(connStr, outDir string, jobs int) []string {
	return []string{
		"--format=directory",
		"--jobs=" + strconv.Itoa(jobs),
		"--file=" + outDir,
		"--no-owner",
		"--no-privileges",
		"--exclude-table=genesis_writer_progress",
		"--dbname=" + connStr,
	}
}

// dumpChainDB writes the destination database to <dataDir>/chain.dump. It
// dumps into a staging directory and renames on success, so chain.dump only
// ever holds a complete archive. A chain.dump left by an earlier run of the
// same data dir is replaced: the database just dumped supersedes it.
func dumpChainDB(ctx context.Context, tool *pgDumpTool, dsn, dataDir string, jobs int, logger *zap.Logger) (string, int64, error) {
	final := filepath.Join(dataDir, chainDumpDirName)
	staging := final + ".partial"
	if err := os.RemoveAll(staging); err != nil {
		return "", 0, fmt.Errorf("remove stale %s: %w", staging, err)
	}

	connStr, env := pgDumpConn(dsn)
	cmd := exec.CommandContext(ctx, tool.Path, pgDumpArgs(connStr, staging, jobs)...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	start := time.Now()
	logger.Info("dumping chain database",
		zap.String("pg_dump", tool.Version), zap.String("out", final), zap.Int("jobs", jobs))
	if err := cmd.Run(); err != nil {
		return "", 0, fmt.Errorf("pg_dump: %w", err)
	}

	if _, err := os.Stat(final); err == nil {
		logger.Warn("replacing chain dump from an earlier run", zap.String("path", final))
		if err := os.RemoveAll(final); err != nil {
			return "", 0, fmt.Errorf("remove old %s: %w", final, err)
		}
	}
	if err := os.Rename(staging, final); err != nil {
		return "", 0, fmt.Errorf("rename %s: %w", staging, err)
	}

	size, err := dirSize(final)
	if err != nil {
		return "", 0, fmt.Errorf("size %s: %w", final, err)
	}
	logger.Info("chain database dumped",
		zap.String("path", final), zap.Int64("bytes", size), zap.Duration("elapsed", time.Since(start)))
	return final, size, nil
}

func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

// emitArtifact dumps the chain database and writes MANIFEST.json. It runs at
// the very end of Run, after the indexes are rebuilt so the dump carries them.
func (w *Writer) emitArtifact(ctx context.Context) error {
	dataDir := w.cfg.ArtifactDir

	var dump *DumpInfo
	if w.cfg.Dump {
		pt := w.cfg.Postgres
		if pt == nil || pt.Dump == nil {
			return fmt.Errorf("dump requested without a resolved pg_dump")
		}
		jobs := w.cfg.DumpJobs
		if jobs <= 0 {
			jobs = defaultDumpJobs()
		}
		path, size, err := dumpChainDB(ctx, pt.Dump, w.cfg.DstDSN, dataDir, jobs, w.logger)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dataDir, path)
		dump = &DumpInfo{
			Path:            rel,
			Format:          "directory",
			PgDumpVersion:   pt.Dump.Version,
			PgDumpMajor:     pt.Dump.Major,
			ServerVersion:   pt.ServerVersion,
			ServerMajor:     pt.ServerMajor,
			RestoreMinMajor: pt.Dump.Major,
			SizeBytes:       size,
		}
	} else {
		w.logger.Warn("chain dump disabled (--no-dump); the chain database is only in the live postgres")
	}

	m, err := newManifest(manifestInputs{
		DataDir:                dataDir,
		ChainID:                w.cfg.ChainID,
		EndHeight:              w.finalHeight,
		SourceChainID:          w.cfg.SourceChainID,
		SourceLastIndexedBlock: w.sourceLastIndexedBlock,
		GenesisFile:            filepath.Join(w.cfg.CMTHome, "config", "genesis.json"),
		MigrationAddress:       w.signerAddr,
		Dump:                   dump,
		WriterCommit:           writerCommit(),
		Now:                    time.Now(),
	})
	if err != nil {
		return fmt.Errorf("build manifest: %w", err)
	}
	path := filepath.Join(dataDir, manifestFileName)
	if err := writeManifest(path, m); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	var present []string
	for _, e := range m.ExcludeFromBootstrap {
		if e.Present {
			present = append(present, e.Path)
		}
	}
	w.logger.Info("wrote manifest",
		zap.String("path", path),
		zap.Int64("end_height", m.EndHeight),
		zap.Int64("new_chain_flush_from_block", m.NewChainFlushFromBlock),
		zap.String("genesis_sha256", m.GenesisSHA256),
		zap.String("genesis_validator", m.GenesisValidatorAddress),
		zap.String("do_not_seed", strings.Join(present, ", ")),
	)
	return nil
}
