package console

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/AudiusProject/audiusd/pkg/core/console/views/pages"
	"github.com/AudiusProject/audiusd/pkg/core/db"
	"github.com/AudiusProject/audiusd/pkg/core/server"
	"github.com/cometbft/cometbft/rpc/client"
)

type State struct {
	rpc    client.Client
	logger *common.Logger
	db     *db.Queries

	latestBlocks       []pages.BlockView
	latestTransactions []pages.TxData

	totalBlocks         int64
	totalTransactions   int64
	totalPlays          int64
	totalValidators     int64
	totalManageEntities int64

	isSyncing bool

	chainId      string
	ethAddress   string
	cometAddress string
}

func NewState(config *config.Config, rpc client.Client, logger *common.Logger, db *db.Queries) (*State, error) {
	return &State{
		rpc:    rpc,
		logger: logger,
		db:     db,

		chainId:      config.GenesisFile.ChainID,
		ethAddress:   config.WalletAddress,
		cometAddress: config.ProposerAddress,
	}, nil
}

func (state *State) recalculateState() {
	ctx := context.Background()
	logger := state.logger

	// Get 10 most recent transactions
	recentTransactions, err := state.db.GetRecentTxs(ctx, 10)
	if err != nil {
		logger.Errorf("could not get recent txs: %v", err)
	} else {
		recentTransactionData := []pages.TxData{}
		for _, tx := range recentTransactions {
			recentTransactionData = append(recentTransactionData, pages.NewTxData(tx))
		}
		state.latestTransactions = recentTransactionData
	}

	// on initial load
	totalTxs, err := state.db.TotalTransactions(ctx)
	if err != nil {
		logger.Errorf("could not get total txs: %v", err)
	} else {
		state.totalTransactions = totalTxs
	}

	latestBlock, err := state.db.GetLatestBlock(ctx)
	if err != nil {
		logger.Debugf("could not get latest block: %v", err)
	} else {
		state.totalBlocks = latestBlock.Height
	}

	totalPlays, err := state.db.TotalTransactionsByType(ctx, server.TrackPlaysProtoName)
	if err != nil {
		logger.Errorf("could not get total plays: %v", err)
	} else {
		state.totalPlays = totalPlays
	}

	totalManageEntities, err := state.db.TotalTransactionsByType(ctx, server.ManageEntitiesProtoName)
	if err != nil {
		logger.Errorf("could not get total manage entities: %v", err)
	} else {
		state.totalManageEntities = totalManageEntities
	}

	totalValidators, err := state.db.TotalValidators(ctx)
	if err != nil {
		logger.Errorf("could not get total validators: %v", err)
	} else {
		state.totalValidators = totalValidators
	}

	latestBlocks := []pages.BlockView{}

	// Get 10 most recent blocks
	latestIndexedBlocks, err := state.db.GetRecentBlocks(context.Background(), 10)
	if err != nil {
		state.logger.Errorf("failed to get latest blocks in db: %v", err)
	}

	for _, block := range latestIndexedBlocks {
		indexedTxs, err := state.db.GetBlockTransactions(context.Background(), block.Height)
		if err != nil {
			state.logger.Errorf("could not get block txs: %v", err)
		}

		txs := [][]byte{}
		for _, tx := range indexedTxs {
			txs = append(txs, tx.Transaction)
		}

		proposer := block.Proposer
		proposerEndpoint := ""
		node, err := state.db.GetRegisteredNodeByCometAddress(ctx, strings.ToUpper(proposer))
		if err != nil {
			logger.Errorf("could not get node by proposer address: %v", err)
		} else {
			proposerEndpoint = fmt.Sprintf("%s (%s...)", node.Endpoint, node.CometAddress[:8])
		}

		latestBlocks = append(latestBlocks, pages.BlockView{
			Height:           block.Height,
			Timestamp:        block.CreatedAt.Time,
			Txs:              txs,
			Proposer:         proposer,
			ProposerEndpoint: proposerEndpoint,
			Hash:             block.Hash,
		})
	}

	state.latestBlocks = latestBlocks

	status, err := state.rpc.Status(ctx)
	if err == nil {
		state.isSyncing = status.SyncInfo.CatchingUp
	}
}

func (state *State) Start() error {
	state.recalculateState()

	for {
		time.Sleep(5 * time.Second)

		highestBlock, err := state.db.GetLatestBlock(context.Background())
		if err != nil {
			continue
		}

		if highestBlock.Height <= state.totalBlocks {
			// total blocks hasn't changed
			continue
		}

		// new block(s) present, rehydrate
		state.recalculateState()
	}
}
