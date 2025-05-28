package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/api/core/v1/v1connect"
	"github.com/AudiusProject/audiusd/pkg/common"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type CoreService struct {
	core *Server
}

func NewCoreService() *CoreService {
	return &CoreService{}
}

func (c *CoreService) SetCore(core *Server) {
	c.core = core
	c.core.setSelf(c)
}

var _ v1connect.CoreServiceHandler = (*CoreService)(nil)

// GetNodeInfo implements v1connect.CoreServiceHandler.
func (c *CoreService) GetNodeInfo(ctx context.Context, req *connect.Request[v1.GetNodeInfoRequest]) (*connect.Response[v1.GetNodeInfoResponse], error) {
	status, err := c.core.rpc.Status(ctx)
	if err != nil {
		return nil, err
	}

	res := &v1.GetNodeInfoResponse{
		Chainid:       c.core.config.GenesisFile.ChainID,
		Synced:        !status.SyncInfo.CatchingUp,
		CometAddress:  c.core.config.ProposerAddress,
		EthAddress:    c.core.config.WalletAddress,
		CurrentHeight: status.SyncInfo.LatestBlockHeight,
	}
	return connect.NewResponse(res), nil
}

// ForwardTransaction implements v1connect.CoreServiceHandler.
func (c *CoreService) ForwardTransaction(ctx context.Context, req *connect.Request[v1.ForwardTransactionRequest]) (*connect.Response[v1.ForwardTransactionResponse], error) {
	// TODO: check signature from known node

	// TODO: validate transaction in same way as send transaction

	mempoolKey, err := common.ToTxHash(req.Msg.Transaction)
	if err != nil {
		return nil, fmt.Errorf("could not get tx hash of signed tx: %v", err)
	}

	c.core.logger.Debugf("received forwarded tx: %v", req.Msg.Transaction)

	// TODO: intake block deadline from request
	status, err := c.core.rpc.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("chain not healthy: %v", err)
	}

	deadline := status.SyncInfo.LatestBlockHeight + 10
	mempoolTx := &MempoolTransaction{
		Tx:       req.Msg.Transaction,
		Deadline: deadline,
	}

	err = c.core.addMempoolTransaction(mempoolKey, mempoolTx, false)
	if err != nil {
		return nil, fmt.Errorf("could not add tx to mempool %v", err)
	}

	return connect.NewResponse(&v1.ForwardTransactionResponse{}), nil
}

// GetBlock implements v1connect.CoreServiceHandler.
func (c *CoreService) GetBlock(ctx context.Context, req *connect.Request[v1.GetBlockRequest]) (*connect.Response[v1.GetBlockResponse], error) {
	currentHeight := c.core.cache.currentHeight.Load()
	if req.Msg.Height > currentHeight {
		return connect.NewResponse(&v1.GetBlockResponse{
			Block: &v1.Block{
				ChainId: c.core.config.GenesisFile.ChainID,
				Height:  -1,
			},
		}), nil
	}

	block, err := c.core.db.GetBlock(ctx, req.Msg.Height)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// fallback to rpc for now, remove after mainnet-alpha
			return c.getBlockRpcFallback(ctx, req.Msg.Height)
		}
		c.core.logger.Errorf("error getting block: %v", err)
		return nil, err
	}

	blockTxs, err := c.core.db.GetBlockTransactions(ctx, req.Msg.Height)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	txResponses := []*v1.Transaction{}
	for _, tx := range blockTxs {
		var transaction v1.SignedTransaction
		err = proto.Unmarshal(tx.Transaction, &transaction)
		if err != nil {
			return nil, err
		}
		res := &v1.Transaction{
			Hash:        tx.TxHash,
			BlockHash:   block.Hash,
			ChainId:     c.core.config.GenesisFile.ChainID,
			Height:      block.Height,
			Timestamp:   timestamppb.New(block.CreatedAt.Time),
			Transaction: &transaction,
		}
		txResponses = append(txResponses, res)
	}

	res := &v1.Block{
		Hash:         block.Hash,
		ChainId:      c.core.config.GenesisFile.ChainID,
		Proposer:     block.Proposer,
		Height:       block.Height,
		Transactions: sortTransactionResponse(txResponses),
		Timestamp:    timestamppb.New(block.CreatedAt.Time),
	}

	return connect.NewResponse(&v1.GetBlockResponse{Block: res, CurrentHeight: c.core.cache.currentHeight.Load()}), nil
}

// GetDeregistrationAttestation implements v1connect.CoreServiceHandler.
func (c *CoreService) GetDeregistrationAttestation(ctx context.Context, req *connect.Request[v1.GetDeregistrationAttestationRequest]) (*connect.Response[v1.GetDeregistrationAttestationResponse], error) {
	dereg := req.Msg.Deregistration
	if dereg == nil {
		return nil, errors.New("empty deregistration attestation")
	}

	node, err := c.core.db.GetRegisteredNodeByCometAddress(ctx, dereg.CometAddress)
	if err != nil {
		return nil, fmt.Errorf("could not attest deregistration for '%s': %v", dereg.CometAddress, err)
	}

	ethBlock := new(big.Int)
	ethBlock, ok := ethBlock.SetString(node.EthBlock, 10)
	if !ok {
		return nil, fmt.Errorf("could not format eth block '%s' for node '%s'", node.EthBlock, node.Endpoint)
	}

	if c.core.isNodeRegisteredOnEthereum(
		ethcommon.HexToAddress(node.EthAddress),
		node.Endpoint,
		ethBlock,
	) {
		c.core.logger.Error("Could not attest to node eth deregistration: node is still registered",
			"cometAddress",
			dereg.CometAddress,
			"ethAddress",
			node.EthAddress,
			"endpoint",
			node.Endpoint,
		)
		return nil, errors.New("node is still registered on ethereum")
	}

	deregBytes, err := proto.Marshal(dereg)
	if err != nil {
		c.core.logger.Error("could not marshal deregistration", "error", err)
		return nil, err
	}
	sig, err := common.EthSign(c.core.config.EthereumKey, deregBytes)
	if err != nil {
		c.core.logger.Error("could not sign deregistration", "error", err)
		return nil, err
	}

	return connect.NewResponse(&v1.GetDeregistrationAttestationResponse{
		Signature:      sig,
		Deregistration: dereg,
	}), nil
}

// GetHealth implements v1connect.CoreServiceHandler.
func (c *CoreService) GetHealth(context.Context, *connect.Request[v1.GetHealthRequest]) (*connect.Response[v1.GetHealthResponse], error) {
	return connect.NewResponse(&v1.GetHealthResponse{}), nil
}

// GetRegistrationAttestation implements v1connect.CoreServiceHandler.
func (c *CoreService) GetRegistrationAttestation(ctx context.Context, req *connect.Request[v1.GetRegistrationAttestationRequest]) (*connect.Response[v1.GetRegistrationAttestationResponse], error) {
	reg := req.Msg.Registration
	if reg == nil {
		return nil, errors.New("empty registration attestation")
	}

	if reg.Deadline < c.core.cache.currentHeight.Load() || reg.Deadline > c.core.cache.currentHeight.Load()+maxRegistrationAttestationValidity {
		return nil, fmt.Errorf("cannot sign registration request with deadline %d (current height is %d)", reg.Deadline, c.core.cache.currentHeight.Load())
	}

	if !c.core.isNodeRegisteredOnEthereum(
		ethcommon.HexToAddress(reg.DelegateWallet),
		reg.Endpoint,
		big.NewInt(reg.EthBlock),
	) {
		c.core.logger.Error(
			"Could not attest to node eth registration",
			"delegate",
			reg.DelegateWallet,
			"endpoint",
			reg.Endpoint,
			"eth block",
			reg.EthBlock,
		)
		return nil, errors.New("node is not registered on ethereum")
	}

	regBytes, err := proto.Marshal(reg)
	if err != nil {
		c.core.logger.Error("could not marshal registration", "error", err)
		return nil, err
	}
	sig, err := common.EthSign(c.core.config.EthereumKey, regBytes)
	if err != nil {
		c.core.logger.Error("could not sign registration", "error", err)
		return nil, err
	}

	return connect.NewResponse(&v1.GetRegistrationAttestationResponse{
		Signature:    sig,
		Registration: reg,
	}), nil
}

// GetTransaction implements v1connect.CoreServiceHandler.
func (c *CoreService) GetTransaction(ctx context.Context, req *connect.Request[v1.GetTransactionRequest]) (*connect.Response[v1.GetTransactionResponse], error) {
	txhash := req.Msg.TxHash

	c.core.logger.Debug("query", "txhash", txhash)

	tx, err := c.core.db.GetTx(ctx, txhash)
	if err != nil {
		return nil, err
	}

	var transaction v1.SignedTransaction
	err = proto.Unmarshal(tx.Transaction, &transaction)
	if err != nil {
		return nil, err
	}

	block, err := c.core.db.GetBlock(ctx, tx.BlockID)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&v1.GetTransactionResponse{
		Transaction: &v1.Transaction{
			Hash:        txhash,
			BlockHash:   block.Hash,
			ChainId:     c.core.config.GenesisFile.ChainID,
			Height:      block.Height,
			Timestamp:   timestamppb.New(block.CreatedAt.Time),
			Transaction: &transaction,
		},
	}), nil
}

// Ping implements v1connect.CoreServiceHandler.
func (c *CoreService) Ping(context.Context, *connect.Request[v1.PingRequest]) (*connect.Response[v1.PingResponse], error) {
	return connect.NewResponse(&v1.PingResponse{Message: "pong"}), nil
}

// SendTransaction implements v1connect.CoreServiceHandler.
func (c *CoreService) SendTransaction(ctx context.Context, req *connect.Request[v1.SendTransactionRequest]) (*connect.Response[v1.SendTransactionResponse], error) {
	// TODO: do validation check
	txhash, err := common.ToTxHash(req.Msg.Transaction)
	if err != nil {
		return nil, fmt.Errorf("could not get tx hash of signed tx: %v", err)
	}

	// TODO: use data companion to keep this value up to date via channel
	status, err := c.core.rpc.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("chain not healthy: %v", err)
	}

	deadline := status.SyncInfo.LatestBlockHeight + 10
	mempoolTx := &MempoolTransaction{
		Tx:       req.Msg.Transaction,
		Deadline: deadline,
	}

	ps := c.core.txPubsub

	txHashCh := ps.Subscribe(txhash)
	defer ps.Unsubscribe(txhash, txHashCh)

	c.core.logger.Infof("adding tx: %v", req.Msg.Transaction)

	// add transaction to mempool with broadcast set to true
	err = c.core.addMempoolTransaction(txhash, mempoolTx, true)
	if err != nil {
		c.core.logger.Errorf("tx could not be included in mempool %s: %v", txhash, err)
		return nil, fmt.Errorf("could not add tx to mempool %v", err)
	}

	select {
	case <-txHashCh:
		tx, err := c.core.db.GetTx(ctx, txhash)
		if err != nil {
			return nil, err
		}

		block, err := c.core.db.GetBlock(ctx, tx.BlockID)
		if err != nil {
			return nil, err
		}

		return connect.NewResponse(&v1.SendTransactionResponse{
			Transaction: &v1.Transaction{
				Hash:        txhash,
				BlockHash:   block.Hash,
				ChainId:     c.core.config.GenesisFile.ChainID,
				Height:      block.Height,
				Timestamp:   timestamppb.New(block.CreatedAt.Time),
				Transaction: req.Msg.Transaction,
			},
		}), nil
	case <-time.After(30 * time.Second):
		c.core.logger.Errorf("tx timeout waiting to be included %s", txhash)
		return nil, errors.New("tx waiting timeout")
	}
}

// Utilities
func (c *CoreService) getBlockRpcFallback(ctx context.Context, height int64) (*connect.Response[v1.GetBlockResponse], error) {
	block, err := c.core.rpc.Block(ctx, &height)
	if err != nil {
		blockInFutureMsg := "must be less than or equal to the current blockchain height"
		if strings.Contains(err.Error(), blockInFutureMsg) {
			// return block with -1 to indicate it doesn't exist yet
			return connect.NewResponse(&v1.GetBlockResponse{
				Block: &v1.Block{
					ChainId:   c.core.config.GenesisFile.ChainID,
					Height:    -1,
					Timestamp: timestamppb.New(time.Now()),
				},
			}), nil
		}
		c.core.logger.Errorf("error getting block: %v", err)
		return nil, err
	}

	txs := []*v1.Transaction{}
	for _, tx := range block.Block.Txs {
		var transaction v1.SignedTransaction
		err = proto.Unmarshal(tx, &transaction)
		if err != nil {
			return nil, err
		}
		txs = append(txs, &v1.Transaction{
			Hash:        c.core.toTxHash(&transaction),
			BlockHash:   block.BlockID.Hash.String(),
			ChainId:     c.core.config.GenesisFile.ChainID,
			Height:      block.Block.Height,
			Timestamp:   timestamppb.New(block.Block.Time),
			Transaction: &transaction,
		})
	}

	txs = sortTransactionResponse(txs)

	res := &v1.GetBlockResponse{
		Block: &v1.Block{
			Hash:         block.BlockID.Hash.String(),
			ChainId:      c.core.config.GenesisFile.ChainID,
			Proposer:     block.Block.ProposerAddress.String(),
			Height:       block.Block.Height,
			Transactions: txs,
			Timestamp:    timestamppb.New(block.Block.Time),
		},
	}

	return connect.NewResponse(res), nil
}

// GetStoredSnapshots implements v1connect.CoreServiceHandler.
func (c *CoreService) GetStoredSnapshots(context.Context, *connect.Request[v1.GetStoredSnapshotsRequest]) (*connect.Response[v1.GetStoredSnapshotsResponse], error) {
	snapshots, err := c.core.getStoredSnapshots()
	if err != nil {
		c.core.logger.Errorf("error getting stored snapshots: %v", err)
		return connect.NewResponse(&v1.GetStoredSnapshotsResponse{
			Snapshots: []*v1.SnapshotMetadata{},
		}), nil
	}

	snapshotResponses := make([]*v1.SnapshotMetadata, 0, len(snapshots))
	for _, snapshot := range snapshots {
		snapshotResponses = append(snapshotResponses, &v1.SnapshotMetadata{
			Height:     int64(snapshot.Height),
			Hash:       hex.EncodeToString(snapshot.Hash),
			ChunkCount: int64(snapshot.Chunks),
			ChainId:    string(snapshot.Metadata),
		})
	}

	res := &v1.GetStoredSnapshotsResponse{
		Snapshots: snapshotResponses,
	}

	return connect.NewResponse(res), nil
}
