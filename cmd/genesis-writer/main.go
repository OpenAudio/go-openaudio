// cmd/genesis-writer: writes historical Audius state directly into Core chain
// PostgreSQL tables without going through consensus. On completion it also
// primes the CometBFT state.db and blockstore.db so the genesis node can start
// from the written height and immediately propose the next live block.
//
// Usage:
//
//	genesis-writer write \
//	  --src-dsn <audius-dp-clone-dsn> \
//	  --data-dir /data \
//	  --genesis-file <path/to/genesis.json> \
//	  --priv-validator-key-file <path/to/priv_validator_key.json>
package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/urfave/cli/v2"
	"go.uber.org/zap"
)

func main() {
	app := &cli.App{
		Name:  "genesis-writer",
		Usage: "Write historical Audius state directly into Core chain PostgreSQL tables",
		Commands: []*cli.Command{
			writeCmd(),
		},
	}
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// cmtHome returns the CometBFT home directory derived from data-dir and chain-id.
// Matches production layout: <data-dir>/core/<chain-id>
func cmtHome(dataDir, chainID string) string {
	return filepath.Join(dataDir, "core", chainID)
}

func writeCmd() *cli.Command {
	return &cli.Command{
		Name:  "write",
		Usage: "Read entities from source DP DB and write them as synthetic blocks into the Core DB",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "src-dsn",
				Usage:    "Source Audius Discovery Provider PostgreSQL DSN",
				EnvVars:  []string{"GENESIS_SRC_DSN"},
				Required: true,
			},
			&cli.StringFlag{
				Name:    "dst-dsn",
				Usage:   "Target Core chain PostgreSQL DSN. If omitted, a local postgres is started at <data-dir>/postgres/.",
				EnvVars: []string{"GENESIS_DST_DSN"},
			},
			&cli.StringFlag{
				Name:    "private-key",
				Usage:   "Genesis migration Ethereum private key (hex, with or without 0x). If omitted, a key is generated and saved for resume.",
				EnvVars: []string{"GENESIS_MIGRATION_PRIVATE_KEY"},
			},
			&cli.StringFlag{
				Name:    "data-dir",
				Usage:   "Root data directory. CometBFT state goes to <data-dir>/core/<chain-id>/, postgres to <data-dir>/postgres/. Mirrors production node layout.",
				EnvVars: []string{"GENESIS_DATA_DIR"},
			},
			&cli.StringFlag{
				Name:    "network",
				Usage:   "Network environment: prod, stage, dev",
				EnvVars: []string{"NETWORK"},
				Value:   "prod",
			},
			&cli.StringFlag{
				Name:    "chain-id",
				Usage:   "Core chain ID (e.g. audius-mainnet-beta)",
				EnvVars: []string{"CHAIN_ID"},
				Value:   "audius-mainnet-beta",
			},
			&cli.StringFlag{
				Name:    "genesis-time",
				Usage:   "Chain genesis time in RFC3339 (default: now)",
				EnvVars: []string{"GENESIS_TIME"},
			},
			&cli.StringFlag{
				Name:    "genesis-file",
				Usage:   "Path to CometBFT genesis.json (defaults to <data-dir>/core/<chain-id>/config/genesis.json)",
				EnvVars: []string{"GENESIS_FILE"},
			},
			&cli.StringFlag{
				Name:    "priv-validator-key-file",
				Usage:   "Path to CometBFT priv_validator_key.json (defaults to <data-dir>/core/<chain-id>/config/priv_validator_key.json)",
				EnvVars: []string{"PRIV_VALIDATOR_KEY_FILE"},
			},
			&cli.IntFlag{
				Name:    "max-txs-per-block",
				Usage:   "Maximum number of transactions per synthetic block",
				EnvVars: []string{"GENESIS_MAX_TXS_PER_BLOCK"},
				Value:   10000,
			},
			&cli.IntFlag{
				Name:    "batch-size",
				Usage:   "Rows fetched from source DB per batch",
				EnvVars: []string{"GENESIS_BATCH_SIZE"},
				Value:   1000,
			},
			&cli.BoolFlag{
				Name:  "run-migrations",
				Usage: "Apply the Core chain schema to dst-dsn before writing (for fresh databases)",
			},
			&cli.BoolFlag{
				Name:  "resume",
				Usage: "Resume from the last completed step of a previous run",
			},
			&cli.BoolFlag{Name: "skip-users", EnvVars: []string{"GENESIS_SKIP_USERS"}, Usage: "Skip users"},
			&cli.BoolFlag{Name: "skip-wallets", EnvVars: []string{"GENESIS_SKIP_WALLETS"}, Usage: "Skip associated wallets and dashboard wallet users"},
			&cli.BoolFlag{Name: "skip-tracks", EnvVars: []string{"GENESIS_SKIP_TRACKS"}},
			&cli.BoolFlag{Name: "skip-playlists", EnvVars: []string{"GENESIS_SKIP_PLAYLISTS"}},
			&cli.BoolFlag{Name: "skip-social", EnvVars: []string{"GENESIS_SKIP_SOCIAL"}, Usage: "Skip follows, saves, reposts, subscriptions, muted users"},
			&cli.BoolFlag{Name: "skip-plays", EnvVars: []string{"GENESIS_SKIP_PLAYS"}},
			&cli.BoolFlag{Name: "skip-apps", EnvVars: []string{"GENESIS_SKIP_APPS"}, Usage: "Skip developer apps and grants"},
			&cli.BoolFlag{Name: "skip-comments", EnvVars: []string{"GENESIS_SKIP_COMMENTS"}, Usage: "Skip comments and comment reactions"},
			&cli.BoolFlag{Name: "skip-emails", EnvVars: []string{"GENESIS_SKIP_EMAILS"}, Usage: "Skip encrypted emails and email access grants"},
			&cli.BoolFlag{Name: "skip-tip-reactions", EnvVars: []string{"GENESIS_SKIP_TIP_REACTIONS"}},
			&cli.BoolFlag{Name: "skip-rewards", EnvVars: []string{"GENESIS_SKIP_REWARDS"}, Usage: "Skip reward pools and rewards"},
			&cli.StringFlag{
				Name:    "core-cmt-home",
				Usage:   "CometBFT home directory of the OLD Core chain. Used to scan the blockstore for reward transactions to replay.",
				EnvVars: []string{"GENESIS_CORE_CMT_HOME"},
			},
		},
		Action: func(c *cli.Context) error {
			logger, _ := zap.NewProduction()
			defer logger.Sync() //nolint:errcheck

			dataDir := c.String("data-dir")
			chainID := c.String("chain-id")

			// Derive CMTHome from data-dir.
			var cHome string
			if dataDir != "" {
				cHome = cmtHome(dataDir, chainID)
			}

			privKey, keyFile, err := resolvePrivKey(c.String("private-key"), cHome, logger)
			if err != nil {
				return err
			}

			genesisTime := time.Now().UTC()
			if gt := c.String("genesis-time"); gt != "" {
				genesisTime, err = time.Parse(time.RFC3339, gt)
				if err != nil {
					return fmt.Errorf("parse genesis-time: %w", err)
				}
			}

			// Resolve config file paths — explicit flags take precedence,
			// otherwise default to conventional paths under CMTHome.
			genesisFile := c.String("genesis-file")
			if genesisFile == "" && cHome != "" {
				genesisFile = filepath.Join(cHome, "config", "genesis.json")
			}
			privValKeyFile := c.String("priv-validator-key-file")
			if privValKeyFile == "" && cHome != "" {
				privValKeyFile = filepath.Join(cHome, "config", "priv_validator_key.json")
			}

			// Auto-generate genesis.json and priv_validator_key.json if they
			// don't exist. Uses prod.json consensus params with a single
			// bootstrap validator.
			if genesisFile != "" && privValKeyFile != "" {
				if err := ensureGenesisFiles(genesisFile, privValKeyFile, chainID, genesisTime, logger); err != nil {
					return fmt.Errorf("ensure genesis files: %w", err)
				}
			}

			// Resolve destination DSN — start a managed postgres if needed.
			dstDSN := c.String("dst-dsn")
			runMigrations := c.Bool("run-migrations")
			var pg *managedPostgres
			if dstDSN == "" {
				if dataDir == "" {
					return fmt.Errorf("either --dst-dsn or --data-dir must be set")
				}
				pg, dstDSN, err = startManagedPostgres(dataDir, logger)
				if err != nil {
					return fmt.Errorf("managed postgres: %w", err)
				}
				defer pg.Stop()
				// Always run migrations for managed postgres — they're idempotent
				// and fast, so safe on both fresh runs and resume.
				runMigrations = true
			}

			cfg := &WriterConfig{
				SrcDSN:               c.String("src-dsn"),
				DstDSN:               dstDSN,
				PrivKey:              privKey,
				Network:              c.String("network"),
				ChainID:              chainID,
				GenesisTime:          genesisTime,
				GenesisFile:          genesisFile,
				PrivValidatorKeyFile: privValKeyFile,
				CMTHome:              cHome,
				MaxTxsPerBlock:       c.Int("max-txs-per-block"),
				BatchSize:            c.Int("batch-size"),
				RunMigrations:        runMigrations,
				Resume:               c.Bool("resume"),
				SkipUsers:            c.Bool("skip-users"),
				SkipWallets:          c.Bool("skip-wallets"),
				SkipTracks:           c.Bool("skip-tracks"),
				SkipPlaylists:        c.Bool("skip-playlists"),
				SkipSocial:           c.Bool("skip-social"),
				SkipPlays:            c.Bool("skip-plays"),
				SkipApps:             c.Bool("skip-apps"),
				SkipComments:         c.Bool("skip-comments"),
				SkipEmails:           c.Bool("skip-emails"),
				SkipTipReactions:     c.Bool("skip-tip-reactions"),
				SkipRewards:          c.Bool("skip-rewards"),
				CoreCMTHome:          c.String("core-cmt-home"),
			}

			w, err := NewWriter(cfg, logger)
			if err != nil {
				return fmt.Errorf("init writer: %w", err)
			}
			defer w.Close()

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			if err := w.Run(ctx); err != nil {
				return err
			}

			// Clean up the generated key file on successful completion.
			if keyFile != "" {
				if err := os.Remove(keyFile); err != nil {
					logger.Warn("could not remove migration key file", zap.String("path", keyFile), zap.Error(err))
				} else {
					logger.Info("removed migration key file", zap.String("path", keyFile))
				}
			}

			return nil
		},
	}
}

// migrationKeyFileName is the name of the ephemeral key file saved to CMTHome
// when a key is auto-generated. Deleted on successful completion.
const migrationKeyFileName = "genesis_migration_key.hex"

// resolvePrivKey returns the migration private key and, if a key file was
// created or loaded, the path to that file (so it can be cleaned up later).
//
// Priority:
//  1. Explicit --private-key flag / env var
//  2. Existing key file at CMTHome/genesis_migration_key.hex (for resume)
//  3. Generate a new key and save it to that file
func resolvePrivKey(flagValue, cmtHome string, logger *zap.Logger) (*ecdsa.PrivateKey, string, error) {
	// Explicit key provided — use it directly, no key file.
	if flagValue != "" {
		s := strings.TrimPrefix(flagValue, "0x")
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, "", fmt.Errorf("parse private key: %w", err)
		}
		key, err := crypto.ToECDSA(b)
		if err != nil {
			return nil, "", fmt.Errorf("parse private key: %w", err)
		}
		return key, "", nil
	}

	// Determine key file path.
	keyDir := cmtHome
	if keyDir == "" {
		keyDir = "."
	}
	keyFile := filepath.Join(keyDir, migrationKeyFileName)

	// Try loading an existing key file (resume case).
	if data, err := os.ReadFile(keyFile); err == nil {
		s := strings.TrimSpace(strings.TrimPrefix(string(data), "0x"))
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, "", fmt.Errorf("parse key file %s: %w", keyFile, err)
		}
		key, err := crypto.ToECDSA(b)
		if err != nil {
			return nil, "", fmt.Errorf("parse key file %s: %w", keyFile, err)
		}
		addr := crypto.PubkeyToAddress(key.PublicKey)
		logger.Info("loaded migration key from file", zap.String("path", keyFile), zap.String("address", addr.Hex()))
		return key, keyFile, nil
	}

	// Generate a new key.
	key, err := crypto.GenerateKey()
	if err != nil {
		return nil, "", fmt.Errorf("generate migration key: %w", err)
	}
	addr := crypto.PubkeyToAddress(key.PublicKey)

	// Save to file for resume.
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("mkdir for key file: %w", err)
	}
	keyHex := hex.EncodeToString(crypto.FromECDSA(key))
	if err := os.WriteFile(keyFile, []byte(keyHex+"\n"), 0o600); err != nil {
		return nil, "", fmt.Errorf("write key file: %w", err)
	}

	logger.Info("generated migration key", zap.String("path", keyFile), zap.String("address", addr.Hex()))
	return key, keyFile, nil
}
