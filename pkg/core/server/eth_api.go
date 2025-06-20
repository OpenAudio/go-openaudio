package server

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"

	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

type EthAPI struct {
	server *Server
	vars   *config.SandboxVars
}
type NetAPI struct {
	server *Server
	vars   *config.SandboxVars
}
type Web3API struct {
	server *Server
	vars   *config.SandboxVars
}

func (s *Server) createEthRPC() error {
	ethRpc := rpc.NewServer()

	// Register the "eth" namespace
	if err := ethRpc.RegisterName("eth", &EthAPI{server: s, vars: s.config.NewSandboxVars()}); err != nil {
		return fmt.Errorf("failed to register eth rpc: %v", err)
	}

	// Register the "net" namespace
	if err := ethRpc.RegisterName("net", &NetAPI{server: s, vars: s.config.NewSandboxVars()}); err != nil {
		return fmt.Errorf("failed to register net rpc: %v", err)
	}

	// Register the "web3" namespace
	if err := ethRpc.RegisterName("web3", &Web3API{server: s, vars: s.config.NewSandboxVars()}); err != nil {
		return fmt.Errorf("failed to register web3 rpc: %v", err)
	}

	e := s.GetEcho()

	e.POST("/core/erpc", echo.WrapHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Content-Type", "application/json")

		ethRpc.ServeHTTP(w, r)
	})))

	e.OPTIONS("/core/erpc", func(c echo.Context) error {
		c.Response().Header().Set("Access-Control-Allow-Origin", "*")
		c.Response().Header().Set("Access-Control-Allow-Headers", "Content-Type")
		return c.NoContent(http.StatusOK)
	})

	return nil
}

// net_version
func (api *NetAPI) Version(ctx context.Context) (string, error) {
	return fmt.Sprint(api.vars.EthChainID), nil
}

// Stub: web3_clientVersion
func (api *Web3API) ClientVersion(ctx context.Context) (string, error) {
	return "AudiusdERPC/v1.0.0", nil
}

// eth_chainId
func (api *EthAPI) ChainId(ctx context.Context) (*hexutil.Big, error) {
	return (*hexutil.Big)(big.NewInt(int64(api.vars.EthChainID))), nil
}

// eth_blockNumber
func (api *EthAPI) BlockNumber(ctx context.Context) (*hexutil.Uint64, error) {
	blockHeight := uint64(api.server.cache.currentHeight.Load())
	return (*hexutil.Uint64)(&blockHeight), nil
}

func (api *EthAPI) GetBlockByNumber(ctx context.Context, blockNumber string, fullTx bool) (map[string]any, error) {
	var height uint64

	switch blockNumber {
	case "latest":
		height = uint64(api.server.cache.currentHeight.Load())
	default:
		n := new(big.Int)
		if err := n.UnmarshalText([]byte(blockNumber)); err != nil {
			return nil, fmt.Errorf("invalid block number: %s", blockNumber)
		}
		height = n.Uint64()
	}

	dbBlock, err := api.server.db.GetBlock(ctx, int64(height))
	if err != nil {
		return nil, fmt.Errorf("block not found: %v", err)
	}

	parentBlockHeight := 1
	if height-1 > 0 {
		parentBlockHeight = int(height - 1)
	}
	parentBlock, err := api.server.db.GetBlock(ctx, int64(parentBlockHeight))
	if !errors.Is(err, pgx.ErrNoRows) && err != nil {
		return nil, fmt.Errorf("could not get parent block: %v", err)
	}

	blockTxs, err := api.server.db.GetBlockTransactions(ctx, dbBlock.Height)
	if !errors.Is(err, pgx.ErrNoRows) && err != nil {
		return nil, fmt.Errorf("could not get txs for block: %v", err)
	}

	txs := []string{}
	for _, tx := range blockTxs {
		txs = append(txs, tx.TxHash)
	}

	block := map[string]any{
		"number":           hexutil.EncodeUint64(height),
		"hash":             dbBlock.Hash,
		"parentHash":       parentBlock.Hash,
		"nonce":            "0x0000000000000000",
		"sha3Uncles":       "",
		"logsBloom":        "0x" + string(make([]byte, 256)),
		"transactionsRoot": "0x" + string(make([]byte, 32)),
		"stateRoot":        "0x" + string(make([]byte, 32)),
		"receiptsRoot":     "0x" + string(make([]byte, 32)),
		"miner":            dbBlock.Proposer,
		"difficulty":       hexutil.EncodeBig(big.NewInt(1)),
		"totalDifficulty":  hexutil.EncodeBig(big.NewInt(1)),
		"extraData":        "0x",
		"size":             hexutil.Uint64(1000),
		"gasLimit":         hexutil.Uint64(10000000),
		"gasUsed":          hexutil.Uint64(0),
		"timestamp":        hexutil.Uint64(1711830000),
		"transactions":     txs,
		"uncles":           []string{},
	}

	return block, nil
}

// eth_getBalance
func (api *EthAPI) GetBalance(ctx context.Context, address string, block string) (*hexutil.Big, error) {
	audioTokenContract, err := api.server.contracts.GetAudioTokenContract()
	if err != nil {
		return nil, fmt.Errorf("could not connect to ethereum: %v", err)
	}

	if !common.IsHexAddress(address) {
		return nil, errors.New("not valid address")
	}

	balance, err := audioTokenContract.BalanceOf(nil, common.HexToAddress(address))
	if err != nil {
		return nil, fmt.Errorf("could not get balance: %v", err)
	}

	return (*hexutil.Big)(balance), nil
}
