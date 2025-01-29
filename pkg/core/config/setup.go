package config

import (
	"crypto/sha256"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/AudiusProject/audiusd/pkg/core/common"
	"github.com/AudiusProject/audiusd/pkg/core/config/genesis"
	cconfig "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
)

/*
Reads in config, sets up comet files, and cleans up state
based on setup configuration.

- reads in env config
- determines env
- gathers chain id
*/
func SetupNode(logger *common.Logger) (*Config, *cconfig.Config, error) {
	// read in env / dotenv config
	envConfig, err := ReadConfig(logger)
	if err != nil {
		return nil, nil, fmt.Errorf("reading env config: %v", err)
	}

	// gather genesis doc based on environment
	genDoc, err := genesis.Read(envConfig.Environment)
	if err != nil {
		return nil, nil, fmt.Errorf("reading genesis: %v", err)
	}
	envConfig.GenesisFile = genDoc

	// gather chain id
	chainID := genDoc.ChainID

	// assemble comet paths
	cometRootDir := fmt.Sprintf("%s/%s", envConfig.RootDir, chainID)
	cometConfigDir := fmt.Sprintf("%s/config", cometRootDir)
	cometDataDir := fmt.Sprintf("%s/data", cometRootDir)

	// create dirs if they don't exist
	if err := common.CreateDirIfNotExist(cometRootDir); err != nil {
		return nil, nil, fmt.Errorf("created comet root dir: %v", err)
	}

	if err := common.CreateDirIfNotExist(cometConfigDir); err != nil {
		return nil, nil, fmt.Errorf("created comet config dir: %v", err)
	}

	if err := common.CreateDirIfNotExist(cometDataDir); err != nil {
		return nil, nil, fmt.Errorf("created comet data dir: %v", err)
	}

	// create default comet config
	cometConfig := cconfig.DefaultConfig()
	cometConfig.SetRoot(cometRootDir)

	// get paths to priv validator and state file
	privValKeyFile := cometConfig.PrivValidatorKeyFile()
	privValStateFile := cometConfig.PrivValidatorStateFile()

	// set validator and state file for derived comet key
	var pv *privval.FilePV
	if common.FileExists(privValKeyFile) {
		logger.Info("Found private validator", "keyFile", privValKeyFile,
			"stateFile", privValStateFile)
		pv = privval.LoadFilePV(privValKeyFile, privValStateFile)
	} else {
		pv = privval.NewFilePV(envConfig.CometKey, privValKeyFile, privValStateFile)
		pv.Save()
		logger.Info("Generated private validator", "keyFile", privValKeyFile,
			"stateFile", privValStateFile)
	}

	// now that we know proposer addr, set in config
	envConfig.ProposerAddress = pv.GetAddress().String()

	// setup p2p key from derived key
	nodeKeyFile := cometConfig.NodeKeyFile()
	if common.FileExists(nodeKeyFile) {
		logger.Info("Found node key", "path", nodeKeyFile)
	} else {
		p2pKey := p2p.NodeKey{
			PrivKey: envConfig.CometKey,
		}
		if err := p2pKey.SaveAs(nodeKeyFile); err != nil {
			return nil, nil, fmt.Errorf("creating node key %v", err)
		}
		logger.Info("Generated node key", "path", nodeKeyFile)
	}

	// save gen file if it doesn't exist
	genFile := cometConfig.GenesisFile()
	if common.FileExists(genFile) {
		logger.Info("Found genesis file", "path", genFile)
	} else {
		if err := genDoc.SaveAs(genFile); err != nil {
			return nil, nil, fmt.Errorf("saving gen file %v", err)
		}
		logger.Info("generated new genesis, running down migrations to start new")
		envConfig.RunDownMigration = true
		logger.Info("Generated genesis file", "path", genFile)
	}

	// after succesful setup, setup comet config.toml
	cometConfig.TxIndex.Indexer = "null"

	// mempool
	// block size restricted to 10mb
	// individual tx size restricted to 300kb, this should be able to carry batches of 200-300 plays
	// 2k txs which is a little over 500mb restriction for the mempool size
	// this keeps the mempool from taking up too much memory
	cometConfig.Mempool.MaxTxsBytes = 10485760
	cometConfig.Mempool.MaxTxBytes = 307200
	cometConfig.Mempool.Size = 30000

	// consensus
	// don't recheck mempool transactions, rely on CheckTx and Propose step
	cometConfig.Mempool.Recheck = false
	cometConfig.Mempool.Broadcast = false
	cometConfig.Consensus.TimeoutCommit = 400 * time.Millisecond
	cometConfig.Consensus.TimeoutPropose = 400 * time.Millisecond
	cometConfig.Consensus.TimeoutProposeDelta = 75 * time.Millisecond
	cometConfig.Consensus.TimeoutPrevote = 300 * time.Millisecond
	cometConfig.Consensus.TimeoutPrevoteDelta = 75 * time.Millisecond
	cometConfig.Consensus.TimeoutPrecommit = 300 * time.Millisecond
	cometConfig.Consensus.TimeoutPrecommitDelta = 75 * time.Millisecond
	// create empty blocks to continue heartbeat at the same interval
	cometConfig.Consensus.CreateEmptyBlocks = true
	// empty blocks wait one second to propose since plays should be a steady stream
	cometConfig.Consensus.CreateEmptyBlocksInterval = 1 * time.Second
	if envConfig.Environment == "stage" || envConfig.Environment == "dev" {
		cometConfig.Consensus.CreateEmptyBlocksInterval = 200 * time.Millisecond
	}

	cometConfig.P2P.PexReactor = true
	cometConfig.P2P.AddrBookStrict = envConfig.AddrBookStrict
	if envConfig.PersistentPeers != "" {
		cometConfig.P2P.PersistentPeers = envConfig.PersistentPeers
	}
	if envConfig.ExternalAddress != "" {
		cometConfig.P2P.ExternalAddress = envConfig.ExternalAddress
	}

	// p2p
	// set validators to higher connection settings so they have tighter conns
	// with each other, this helps get to sub 1s block times
	cometConfig.P2P.MaxNumOutboundPeers = envConfig.MaxOutboundPeers
	cometConfig.P2P.MaxNumInboundPeers = envConfig.MaxInboundPeers
	cometConfig.P2P.AllowDuplicateIP = true
	cometConfig.P2P.FlushThrottleTimeout = 50 * time.Millisecond
	cometConfig.P2P.SendRate = 5120000
	cometConfig.P2P.RecvRate = 5120000
	cometConfig.P2P.HandshakeTimeout = 3 * time.Second
	cometConfig.P2P.DialTimeout = 5 * time.Second
	cometConfig.P2P.PersistentPeersMaxDialPeriod = 15 * time.Second

	// connection settings
	if envConfig.RPCladdr != "" {
		cometConfig.RPC.ListenAddress = envConfig.RPCladdr
	}
	if envConfig.P2PLaddr != "" {
		cometConfig.P2P.ListenAddress = envConfig.P2PLaddr
	}

	return envConfig, cometConfig, nil
}

func moduloPersistentPeers(nodeAddress string, persistentPeers string, groupSize int) string {
	peerList := strings.Split(persistentPeers, ",")
	numPeers := len(peerList)

	hash := sha256.Sum256([]byte(nodeAddress))
	nodeHash := new(big.Int).SetBytes(hash[:])

	startIndex := int(nodeHash.Mod(nodeHash, big.NewInt(int64(numPeers))).Int64())

	var assignedPeers []string
	for i := 0; i < groupSize; i++ {
		index := (startIndex + i) % numPeers
		assignedPeers = append(assignedPeers, peerList[index])
	}

	return strings.Join(assignedPeers, ",")
}
