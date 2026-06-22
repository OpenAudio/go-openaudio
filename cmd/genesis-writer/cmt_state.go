package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	dbm "github.com/cometbft/cometbft-db"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/privval"
	cmtstate "github.com/cometbft/cometbft/state"
	cmtstore "github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
	"go.uber.org/zap"
)

// initBlockSigning loads the genesis validator key and consensus parameters
// so that flushBlock can build and sign real CometBFT blocks.
// If CMTHome is set, it also opens blockstore.db for writing.
func (w *Writer) initBlockSigning() error {
	if w.cfg.GenesisFile == "" {
		return fmt.Errorf("--genesis-file is required")
	}
	if w.cfg.PrivValidatorKeyFile == "" {
		return fmt.Errorf("--priv-validator-key-file is required")
	}

	genDoc, err := cmttypes.GenesisDocFromFile(w.cfg.GenesisFile)
	if err != nil {
		return fmt.Errorf("load genesis file: %w", err)
	}

	genesisState, err := cmtstate.MakeGenesisState(genDoc)
	if err != nil {
		return fmt.Errorf("make genesis state: %w", err)
	}

	w.validatorsHash = genesisState.Validators.Hash()
	w.nextValHash = genesisState.Validators.Hash()
	w.consensusHash = genesisState.ConsensusParams.Hash()

	pv := privval.LoadFilePVEmptyState(w.cfg.PrivValidatorKeyFile, "")
	w.cmtPrivKey = pv.Key.PrivKey
	w.proposerAddr = pv.Key.Address

	if w.cfg.CMTHome != "" {
		dataDir := filepath.Join(w.cfg.CMTHome, "data")
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return fmt.Errorf("mkdir data: %w", err)
		}
		bsDB, err := dbm.NewDB("blockstore", dbm.PebbleDBBackend, dataDir)
		if err != nil {
			return fmt.Errorf("open blockstore.db: %w", err)
		}
		w.bsDB = bsDB
		w.blockStore = cmtstore.NewBlockStore(bsDB)
	}

	return nil
}

// writeCMTState writes state.db so the genesis node can start from finalHeight
// and immediately propose block finalHeight+1. blockstore.db is already fully
// populated by flushBlock (SaveBlock called per block), so no extra work is
// needed here for blockstore.
func (w *Writer) writeCMTState(_ context.Context) error {
	genDoc, err := cmttypes.GenesisDocFromFile(w.cfg.GenesisFile)
	if err != nil {
		return fmt.Errorf("load genesis file: %w", err)
	}

	genesisState, err := cmtstate.MakeGenesisState(genDoc)
	if err != nil {
		return fmt.Errorf("make genesis state: %w", err)
	}

	// prevBlockID is the real BlockID of the last written block (set by flushBlock).
	//
	// LastHeightValidatorsChanged must equal finalHeight+1 — the height at which
	// Bootstrap stores the actual validator set bytes.  If it were set to
	// InitialHeight (e.g. 1) instead, every subsequent stateStore.Save() would
	// write validator references pointing to height 1, which has no data, causing
	// a CONSENSUS FAILURE when CometBFT tries to load the validator set.
	validatorsStoredAt := w.finalHeight + 1
	state := cmtstate.State{
		Version:       genesisState.Version,
		ChainID:       genDoc.ChainID,
		InitialHeight: genDoc.InitialHeight,

		LastBlockHeight: w.finalHeight,
		LastBlockID:     w.prevBlockID,
		LastBlockTime:   w.finalTime,

		NextValidators:              genesisState.Validators.CopyIncrementProposerPriority(1),
		Validators:                  genesisState.Validators.Copy(),
		LastValidators:              genesisState.Validators.Copy(),
		LastHeightValidatorsChanged: validatorsStoredAt,

		ConsensusParams:                  genesisState.ConsensusParams,
		LastHeightConsensusParamsChanged: validatorsStoredAt,

		AppHash: w.finalAppHash,
	}

	dataDir := filepath.Join(w.cfg.CMTHome, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("mkdir data: %w", err)
	}

	stateDB, err := dbm.NewDB("state", dbm.PebbleDBBackend, dataDir)
	if err != nil {
		return fmt.Errorf("open state.db: %w", err)
	}
	stateStore := cmtstate.NewStore(stateDB, cmtstate.StoreOptions{})
	if err := stateStore.Bootstrap(state); err != nil {
		stateDB.Close()
		return fmt.Errorf("bootstrap state.db: %w", err)
	}
	stateDB.Close()

	w.logger.Info("wrote state.db",
		zap.Int64("height", w.finalHeight),
		zap.String("block_hash", fmt.Sprintf("%x", w.prevBlockID.Hash)),
		zap.String("app_hash", fmt.Sprintf("%x", w.finalAppHash)),
	)

	return nil
}

// writeGenesisFile reads the input genesis.json, sets genesis_migration_address
// and genesis_migration_end_height to lock down the migration key after the
// genesis write, then writes the updated file to CMTHome/config/genesis.json.
func (w *Writer) writeGenesisFile() error {
	raw, err := os.ReadFile(w.cfg.GenesisFile)
	if err != nil {
		return fmt.Errorf("read genesis file: %w", err)
	}

	// Decode into a generic map to preserve all fields.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse genesis file: %w", err)
	}

	// Merge migration fields into existing app_state (if any).
	appState := make(map[string]interface{})
	if existing, ok := doc["app_state"]; ok {
		if err := json.Unmarshal(existing, &appState); err != nil {
			return fmt.Errorf("unmarshal existing app_state: %w", err)
		}
	}
	appState["genesis_migration_address"] = w.signerAddr
	appState["genesis_migration_end_height"] = w.finalHeight
	appStateJSON, err := json.Marshal(appState)
	if err != nil {
		return fmt.Errorf("marshal app_state: %w", err)
	}
	doc["app_state"] = appStateJSON

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal genesis: %w", err)
	}

	outPath := filepath.Join(w.cfg.CMTHome, "config", "genesis.json")
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("mkdir config: %w", err)
	}
	if err := os.WriteFile(outPath, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("write genesis file: %w", err)
	}

	w.logger.Info("wrote genesis.json",
		zap.String("path", outPath),
		zap.String("migration_address", w.signerAddr),
		zap.Int64("migration_end_height", w.finalHeight),
	)

	return nil
}

// ensureGenesisFiles creates genesis.json and priv_validator_key.json if they
// don't exist at the expected paths. The genesis doc matches prod.json consensus
// params (15MB max_bytes, 10MB evidence max_bytes) with a single bootstrap
// validator and the provided chain ID and genesis time.
func ensureGenesisFiles(genesisFile, privValKeyFile, chainID string, genesisTime time.Time, logger *zap.Logger) error {
	genesisExists := fileExists(genesisFile)
	keyExists := fileExists(privValKeyFile)

	if genesisExists && keyExists {
		return nil
	}

	// Generate a new ed25519 validator key if needed.
	var pubKey ed25519.PubKey
	if !keyExists {
		privKey := ed25519.GenPrivKey()
		pubKey = privKey.PubKey().(ed25519.PubKey)

		if err := os.MkdirAll(filepath.Dir(privValKeyFile), 0o755); err != nil {
			return fmt.Errorf("mkdir for priv_validator_key: %w", err)
		}
		// priv_validator_state.json goes alongside the key in the data dir.
		stateFile := filepath.Join(filepath.Dir(filepath.Dir(privValKeyFile)), "data", "priv_validator_state.json")
		if err := os.MkdirAll(filepath.Dir(stateFile), 0o755); err != nil {
			return fmt.Errorf("mkdir for priv_validator_state: %w", err)
		}
		pv := privval.NewFilePV(privKey, privValKeyFile, stateFile)
		pv.Save()

		logger.Info("generated validator key",
			zap.String("path", privValKeyFile),
			zap.String("address", pv.GetAddress().String()),
		)
	} else {
		// Load existing key to get the pub key for the genesis doc.
		pv := privval.LoadFilePVEmptyState(privValKeyFile, "")
		pubKey = pv.Key.PubKey.(ed25519.PubKey)
	}

	if !genesisExists {
		// Consensus params matching prod.json.
		genDoc := &cmttypes.GenesisDoc{
			GenesisTime:   genesisTime,
			ChainID:       chainID,
			InitialHeight: 0,
			ConsensusParams: &cmttypes.ConsensusParams{
				Block: cmttypes.BlockParams{
					MaxBytes: 15728640, // 15MB
					MaxGas:   10000000,
				},
				Evidence: cmttypes.EvidenceParams{
					MaxAgeNumBlocks: 100000,
					MaxAgeDuration:  172800000000000, // 48h in ns
					MaxBytes:        1572864,         // ~1.5MB
				},
				Validator: cmttypes.ValidatorParams{
					PubKeyTypes: []string{"ed25519"},
				},
			},
			Validators: []cmttypes.GenesisValidator{
				{
					Address: pubKey.Address(),
					PubKey:  pubKey,
					Power:   100,
					Name:    "bootstrap",
				},
			},
		}

		if err := genDoc.ValidateAndComplete(); err != nil {
			return fmt.Errorf("validate genesis doc: %w", err)
		}

		if err := os.MkdirAll(filepath.Dir(genesisFile), 0o755); err != nil {
			return fmt.Errorf("mkdir for genesis.json: %w", err)
		}
		if err := genDoc.SaveAs(genesisFile); err != nil {
			return fmt.Errorf("save genesis.json: %w", err)
		}

		logger.Info("generated genesis.json",
			zap.String("path", genesisFile),
			zap.String("chain_id", chainID),
		)
	}

	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
