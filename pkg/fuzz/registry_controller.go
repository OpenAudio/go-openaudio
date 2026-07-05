package fuzz

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	oaCommon "github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/eth/contracts"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	gethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

const defaultReceiptTimeout = 30 * time.Second

// EthRegistryController drives the L1 ServiceProviderFactory contract for
// devnet-style lifecycle fuzzing. It is deliberately opt-in: callers must
// provide the RPC URL, registry address, and private key.
type EthRegistryController struct {
	rpc              *ethclient.Client
	contracts        *contracts.AudiusContracts
	privateKey       string
	serviceType      [32]byte
	stakeAmount      *big.Int
	delegateOwner    gethCommon.Address
	receiptTimeout   time.Duration
	currentEndpoints map[NodeID]string
	mu               sync.Mutex
}

type EthRegistryControllerOptions struct {
	RPCURL              string
	RegistryAddress     string
	PrivateKey          string
	ServiceType         [32]byte
	StakeAmount         *big.Int
	DelegateOwnerWallet string
	ReceiptTimeout      time.Duration
}

func NewEthRegistryController(ctx context.Context, opts EthRegistryControllerOptions) (*EthRegistryController, error) {
	if strings.TrimSpace(opts.RPCURL) == "" {
		return nil, fmt.Errorf("RPCURL is required")
	}
	if strings.TrimSpace(opts.RegistryAddress) == "" {
		return nil, fmt.Errorf("RegistryAddress is required")
	}
	if strings.TrimSpace(opts.PrivateKey) == "" {
		return nil, fmt.Errorf("PrivateKey is required")
	}

	rpc, err := ethclient.DialContext(ctx, opts.RPCURL)
	if err != nil {
		return nil, fmt.Errorf("dial eth rpc: %w", err)
	}
	ac, err := contracts.NewAudiusContracts(rpc, opts.RegistryAddress)
	if err != nil {
		rpc.Close()
		return nil, fmt.Errorf("load audius contracts: %w", err)
	}

	serviceType := opts.ServiceType
	if serviceType == ([32]byte{}) {
		serviceType = contracts.Validator
	}
	stakeAmount := opts.StakeAmount
	if stakeAmount == nil {
		stakeAmount = big.NewInt(0)
	}

	privateKey := cleanHexKey(opts.PrivateKey)
	ethKey, err := oaCommon.EthToEthKey(privateKey)
	if err != nil {
		rpc.Close()
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	delegateOwner := gethCommon.HexToAddress(opts.DelegateOwnerWallet)
	if opts.DelegateOwnerWallet == "" {
		delegateOwner = gethCommon.HexToAddress(oaCommon.PrivKeyToAddress(ethKey))
	}
	receiptTimeout := opts.ReceiptTimeout
	if receiptTimeout <= 0 {
		receiptTimeout = defaultReceiptTimeout
	}

	return &EthRegistryController{
		rpc:              rpc,
		contracts:        ac,
		privateKey:       privateKey,
		serviceType:      serviceType,
		stakeAmount:      new(big.Int).Set(stakeAmount),
		delegateOwner:    delegateOwner,
		receiptTimeout:   receiptTimeout,
		currentEndpoints: map[NodeID]string{},
	}, nil
}

func (c *EthRegistryController) Close() {
	c.rpc.Close()
}

func (c *EthRegistryController) RegisterNode(ctx context.Context, node NodeSpec) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	endpoint := c.originalEndpoint(node)
	tx, err := c.transact(ctx, func(opts *bind.TransactOpts) (*types.Transaction, error) {
		spf, err := c.contracts.GetServiceProviderFactoryContract()
		if err != nil {
			return nil, err
		}
		return spf.Register(opts, c.serviceType, endpoint, new(big.Int).Set(c.stakeAmount), c.delegateOwner)
	})
	if err != nil {
		return fmt.Errorf("register %s %s: %w", node.ID, endpoint, err)
	}
	c.currentEndpoints[node.ID] = endpoint
	return c.waitMined(ctx, "register", tx)
}

func (c *EthRegistryController) DeregisterNode(ctx context.Context, node NodeSpec) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	endpoint := c.currentEndpoint(node)
	tx, err := c.transact(ctx, func(opts *bind.TransactOpts) (*types.Transaction, error) {
		spf, err := c.contracts.GetServiceProviderFactoryContract()
		if err != nil {
			return nil, err
		}
		return spf.Deregister(opts, c.serviceType, endpoint)
	})
	if err != nil {
		return fmt.Errorf("deregister %s %s: %w", node.ID, endpoint, err)
	}
	return c.waitMined(ctx, "deregister", tx)
}

func (c *EthRegistryController) SetNodeEndpoint(ctx context.Context, node NodeSpec, endpoint string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	oldEndpoint := c.currentEndpoint(node)
	newEndpoint := strings.TrimSpace(endpoint)
	if newEndpoint == "" {
		newEndpoint = c.originalEndpoint(node)
	}
	if oldEndpoint == newEndpoint {
		return nil
	}

	tx, err := c.transact(ctx, func(opts *bind.TransactOpts) (*types.Transaction, error) {
		spf, err := c.contracts.GetServiceProviderFactoryContract()
		if err != nil {
			return nil, err
		}
		return spf.UpdateEndpoint(opts, c.serviceType, oldEndpoint, newEndpoint)
	})
	if err != nil {
		return fmt.Errorf("update endpoint %s %s -> %s: %w", node.ID, oldEndpoint, newEndpoint, err)
	}
	if err := c.waitMined(ctx, "update endpoint", tx); err != nil {
		return err
	}
	c.currentEndpoints[node.ID] = newEndpoint
	return nil
}

func (c *EthRegistryController) transact(ctx context.Context, fn func(*bind.TransactOpts) (*types.Transaction, error)) (*types.Transaction, error) {
	chainID, err := c.rpc.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("get chain id: %w", err)
	}
	ethKey, err := oaCommon.EthToEthKey(c.privateKey)
	if err != nil {
		return nil, err
	}
	opts, err := bind.NewKeyedTransactorWithChainID(ethKey, chainID)
	if err != nil {
		return nil, err
	}
	opts.Context = ctx
	return fn(opts)
}

func (c *EthRegistryController) waitMined(ctx context.Context, action string, tx *types.Transaction) error {
	receiptCtx, cancel := context.WithTimeout(ctx, c.receiptTimeout)
	defer cancel()
	receipt, err := bind.WaitMined(receiptCtx, c.rpc, tx)
	if err != nil {
		return fmt.Errorf("%s tx %s not mined: %w", action, tx.Hash(), err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("%s tx %s failed with receipt status %d", action, tx.Hash(), receipt.Status)
	}
	return nil
}

func (c *EthRegistryController) originalEndpoint(node NodeSpec) string {
	return strings.TrimSpace(node.Endpoint)
}

func (c *EthRegistryController) currentEndpoint(node NodeSpec) string {
	if endpoint := c.currentEndpoints[node.ID]; endpoint != "" {
		return endpoint
	}
	return c.originalEndpoint(node)
}

func cleanHexKey(key string) string {
	key = strings.TrimSpace(key)
	key = strings.TrimPrefix(key, "0x")
	key = strings.TrimPrefix(key, "0X")
	return key
}
