package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/api/core/v1/v1connect"
	v1beta1 "github.com/AudiusProject/audiusd/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/AudiusProject/audiusd/pkg/api/ddex/v1beta1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/rewards"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
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
	status, err := c.GetStatus(ctx, &connect.Request[v1.GetStatusRequest]{})
	if err != nil {
		return nil, err
	}

	res := &v1.GetNodeInfoResponse{
		Chainid:       c.core.config.GenesisFile.ChainID,
		Synced:        status.Msg.SyncInfo.Synced,
		CometAddress:  c.core.config.ProposerAddress,
		EthAddress:    c.core.config.WalletAddress,
		CurrentHeight: status.Msg.ChainInfo.CurrentHeight,
	}
	return connect.NewResponse(res), nil
}

// ForwardTransaction implements v1connect.CoreServiceHandler.
func (c *CoreService) ForwardTransaction(ctx context.Context, req *connect.Request[v1.ForwardTransactionRequest]) (*connect.Response[v1.ForwardTransactionResponse], error) {
	// TODO: check signature from known node

	// TODO: validate transaction in same way as send transaction

	var mempoolKey common.TxHash
	var err error
	// Use consistent hashing by marshaling to bytes first, matching abci.go behavior
	if req.Msg.Transactionv2 != nil {
		txBytes, marshalErr := proto.Marshal(req.Msg.Transactionv2)
		if marshalErr != nil {
			return nil, fmt.Errorf("could not marshal transaction: %v", marshalErr)
		}
		mempoolKey = common.ToTxHashFromBytes(txBytes)
	} else {
		txBytes, marshalErr := proto.Marshal(req.Msg.Transaction)
		if marshalErr != nil {
			return nil, fmt.Errorf("could not marshal transaction: %v", marshalErr)
		}
		mempoolKey = common.ToTxHashFromBytes(txBytes)
	}

	if req.Msg.Transactionv2 != nil {
		c.core.logger.Debug("received forwarded v2 tx", zap.Any("tx", req.Msg.Transactionv2))
		if c.core.config.Environment != "dev" {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("received forwarded v2 tx outside of dev"))
		}
	} else {
		c.core.logger.Debug("received forwarded tx", zap.Any("tx", req.Msg.Transaction))
	}

	// TODO: intake block deadline from request
	status, err := c.core.rpc.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("chain not healthy: %v", err)
	}

	deadline := status.SyncInfo.LatestBlockHeight + 10
	var mempoolTx *MempoolTransaction
	if req.Msg.Transaction != nil {
		mempoolTx = &MempoolTransaction{
			Tx:       req.Msg.Transaction,
			Deadline: deadline,
		}
	} else if req.Msg.Transactionv2 != nil {
		mempoolTx = &MempoolTransaction{
			Txv2:     req.Msg.Transactionv2,
			Deadline: deadline,
		}
	} else {
		return nil, fmt.Errorf("no transaction provided")
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
		c.core.logger.Error("error getting block", zap.Error(err))
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

// GetBlocks implements v1connect.CoreServiceHandler.
func (c *CoreService) GetBlocks(ctx context.Context, req *connect.Request[v1.GetBlocksRequest]) (*connect.Response[v1.GetBlocksResponse], error) {
	heights := req.Msg.Height
	if len(heights) == 0 {
		return connect.NewResponse(&v1.GetBlocksResponse{
			Blocks:        map[int64]*v1.Block{},
			CurrentHeight: c.core.cache.currentHeight.Load(),
		}), nil
	}

	// Apply server-side limit of 500 blocks
	if len(heights) > 500 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("too many blocks requested: %d (max 500)", len(heights)))
	}

	currentHeight := c.core.cache.currentHeight.Load()

	// Get blocks with transactions in one efficient query
	rows, err := c.core.db.GetBlocksWithTransactions(ctx, heights)
	if err != nil {
		return nil, fmt.Errorf("error getting blocks with transactions: %v", err)
	}

	// Group results by block height
	blockMap := make(map[int64]*v1.Block)

	for _, row := range rows {
		// Initialize block if not already created
		if _, exists := blockMap[row.Height]; !exists {
			blockMap[row.Height] = &v1.Block{
				Hash:         row.BlockHash,
				ChainId:      c.core.config.GenesisFile.ChainID,
				Proposer:     row.Proposer,
				Height:       row.Height,
				Transactions: []*v1.Transaction{},
				Timestamp:    timestamppb.New(row.BlockCreatedAt.Time),
			}
		}

		// Add transaction if it exists (pgtype.Text.Valid checks for NULL)
		if row.TxHash.Valid && len(row.Transaction) > 0 {
			var transaction v1.SignedTransaction
			err = proto.Unmarshal(row.Transaction, &transaction)
			if err != nil {
				return nil, fmt.Errorf("error unmarshaling transaction: %v", err)
			}

			txResponse := &v1.Transaction{
				Hash:        row.TxHash.String,
				BlockHash:   row.BlockHash,
				ChainId:     c.core.config.GenesisFile.ChainID,
				Height:      row.Height,
				Timestamp:   timestamppb.New(row.BlockCreatedAt.Time),
				Transaction: &transaction,
			}

			blockMap[row.Height].Transactions = append(blockMap[row.Height].Transactions, txResponse)
		}
	}

	// Sort transactions within each block
	for _, block := range blockMap {
		block.Transactions = sortTransactionResponse(block.Transactions)
	}

	return connect.NewResponse(&v1.GetBlocksResponse{
		Blocks:        blockMap,
		CurrentHeight: currentHeight,
	}), nil
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

	registered, err := c.core.IsNodeRegisteredOnEthereum(
		ctx,
		node.Endpoint,
		node.EthAddress,
		ethBlock.Int64(),
	)
	if err != nil {
		c.core.logger.Error("Could not attest to node eth deregistration: error checking eth registration status",
			zap.String("cometAddress", dereg.CometAddress),
			zap.String("ethAddress", node.EthAddress),
			zap.String("endpoint", node.Endpoint),
			zap.Error(err),
		)
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not attest to node deregistration"))
	}

	shouldPurge, err := c.core.ShouldPurgeValidatorForUnderperformance(ctx, dereg.CometAddress)
	if err != nil {
		c.core.logger.Error("Could not attest to node eth deregistration: could not check uptime SLA history",
			zap.String("cometAddress", dereg.CometAddress),
			zap.String("ethAddress", node.EthAddress),
			zap.String("endpoint", node.Endpoint),
			zap.Error(err),
		)
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not attest to node deregistration"))
	}

	if registered && !shouldPurge {
		c.core.logger.Error("Could not attest to node eth deregistration: node is still registered and not underperforming",
			zap.String("cometAddress", dereg.CometAddress),
			zap.String("ethAddress", node.EthAddress),
			zap.String("endpoint", node.Endpoint),
		)
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not attest to node deregistration"))
	}

	c.core.logger.Info("Attesting to deregister a validator because it is down", zap.String("validatorAddress", dereg.CometAddress))

	deregBytes, err := proto.Marshal(dereg)
	if err != nil {
		c.core.logger.Error("could not marshal deregistration", zap.Error(err))
		return nil, err
	}
	sig, err := common.EthSign(c.core.config.EthereumKey, deregBytes)
	if err != nil {
		c.core.logger.Error("could not sign deregistration", zap.Error(err))
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

	if registered, err := c.core.IsNodeRegisteredOnEthereum(
		ctx,
		reg.Endpoint,
		reg.DelegateWallet,
		reg.EthBlock,
	); !registered || err != nil {
		c.core.logger.Error(
			"Could not attest to node registration, failed to find endpoint on ethereum",
			zap.String("delegate", reg.DelegateWallet),
			zap.String("endpoint", reg.Endpoint),
			zap.Int64("eth block", reg.EthBlock),
			zap.Error(err),
		)
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node is not registered on ethereum"))
	}

	if shouldPurge, err := c.core.ShouldPurgeValidatorForUnderperformance(ctx, reg.CometAddress); shouldPurge || err != nil {
		c.core.logger.Error(
			"Could not attest to node eth registration, validator should stay purged",
			zap.String("delegate", reg.DelegateWallet),
			zap.String("endpoint", reg.Endpoint),
			zap.Int64("eth block", reg.EthBlock),
			zap.Error(err),
		)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node is temporarily blacklisted"))
	}

	regBytes, err := proto.Marshal(reg)
	if err != nil {
		c.core.logger.Error("could not marshal registration", zap.Error(err))
		return nil, err
	}
	sig, err := common.EthSign(c.core.config.EthereumKey, regBytes)
	if err != nil {
		c.core.logger.Error("could not sign registration", zap.Error(err))
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

	c.core.logger.Debug("query", zap.String("txhash", txhash))

	tx, err := c.core.db.GetTx(ctx, txhash)
	if err != nil {
		return nil, err
	}

	block, err := c.core.db.GetBlock(ctx, tx.BlockID)
	if err != nil {
		return nil, err
	}

	// Try to unmarshal as v1 transaction first
	var v1Transaction v1.SignedTransaction
	err = proto.Unmarshal(tx.Transaction, &v1Transaction)
	if err == nil {
		// Successfully unmarshaled as v1 transaction
		return connect.NewResponse(&v1.GetTransactionResponse{
			Transaction: &v1.Transaction{
				Hash:        txhash,
				BlockHash:   block.Hash,
				ChainId:     c.core.config.GenesisFile.ChainID,
				Height:      block.Height,
				Timestamp:   timestamppb.New(block.CreatedAt.Time),
				Transaction: &v1Transaction,
			},
		}), nil
	}

	// Try to unmarshal as v2 transaction
	var v2Transaction v1beta1.Transaction
	err = proto.Unmarshal(tx.Transaction, &v2Transaction)
	if err == nil {
		// Successfully unmarshaled as v2 transaction
		// For now, return the v2 transaction in the response - the API might need to be extended
		// to properly handle v2 transactions, but this allows retrieval without error
		return connect.NewResponse(&v1.GetTransactionResponse{
			Transaction: &v1.Transaction{
				Hash:          txhash,
				BlockHash:     block.Hash,
				ChainId:       c.core.config.GenesisFile.ChainID,
				Height:        block.Height,
				Timestamp:     timestamppb.New(block.CreatedAt.Time),
				Transaction:   &v1Transaction,
				Transactionv2: &v2Transaction,
			},
		}), nil
	}

	// If neither worked, return the original error
	return nil, fmt.Errorf("could not unmarshal transaction as v1 or v2: %v", err)
}

// Ping implements v1connect.CoreServiceHandler.
func (c *CoreService) Ping(context.Context, *connect.Request[v1.PingRequest]) (*connect.Response[v1.PingResponse], error) {
	return connect.NewResponse(&v1.PingResponse{Message: "pong"}), nil
}

// SendTransaction implements v1connect.CoreServiceHandler.
func (c *CoreService) SendTransaction(ctx context.Context, req *connect.Request[v1.SendTransactionRequest]) (*connect.Response[v1.SendTransactionResponse], error) {
	// TODO: do validation check
	var txhash common.TxHash
	var err error
	if req.Msg.Transactionv2 != nil {
		// add gate just for dev
		if c.core.config.Environment != "dev" {
			return nil, connect.NewError(connect.CodeUnimplemented, errors.New("tx v2 in development"))
		}

		// Use consistent hashing by marshaling to bytes first, matching abci.go behavior
		txBytes, marshalErr := proto.Marshal(req.Msg.Transactionv2)
		if marshalErr != nil {
			return nil, fmt.Errorf("could not marshal transaction: %v", marshalErr)
		}
		txhash = common.ToTxHashFromBytes(txBytes)

		err = c.core.validateV2Transaction(ctx, c.core.cache.currentHeight.Load(), req.Msg.Transactionv2)
		if err != nil {
			return nil, fmt.Errorf("transactionv2 validation failed: %v", err)
		}
	} else {
		// Use consistent hashing by marshaling to bytes first, matching abci.go behavior
		txBytes, marshalErr := proto.Marshal(req.Msg.Transaction)
		if marshalErr != nil {
			return nil, fmt.Errorf("could not marshal transaction: %v", marshalErr)
		}
		txhash = common.ToTxHashFromBytes(txBytes)
	}

	// create mempool transaction for both v1 and v2
	var mempoolTx *MempoolTransaction
	deadline := c.core.cache.currentHeight.Load() + 10
	if req.Msg.Transaction != nil {
		mempoolTx = &MempoolTransaction{
			Tx:       req.Msg.Transaction,
			Deadline: deadline,
		}
	} else if req.Msg.Transactionv2 != nil {
		mempoolTx = &MempoolTransaction{
			Txv2:     req.Msg.Transactionv2,
			Deadline: deadline,
		}
	}

	ps := c.core.txPubsub

	txHashCh := ps.Subscribe(txhash)
	defer ps.Unsubscribe(txhash, txHashCh)

	// add transaction to mempool with broadcast set to true
	if mempoolTx != nil {
		err = c.core.addMempoolTransaction(txhash, mempoolTx, true)
		if err != nil {
			c.core.logger.Error("tx could not be included in mempool", zap.String("tx", txhash), zap.Error(err))
			return nil, fmt.Errorf("could not add tx to mempool %v", err)
		}
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

		// only build receipt for v2 transactions
		var receipt *v1beta1.TransactionReceipt
		if req.Msg.Transactionv2 != nil {
			receipt = &v1beta1.TransactionReceipt{
				EnvelopeInfo: &v1beta1.EnvelopeReceiptInfo{
					ChainId:      c.core.config.GenesisFile.ChainID,
					Expiration:   req.Msg.Transactionv2.Envelope.Header.Expiration,
					Nonce:        req.Msg.Transactionv2.Envelope.Header.Nonce,
					MessageCount: int32(len(req.Msg.Transactionv2.Envelope.Messages)),
				},
				TxHash:          txhash,
				Height:          block.Height,
				Timestamp:       block.CreatedAt.Time.Unix(),
				Sender:          "", // TODO: get sender from transaction signature
				Responder:       c.core.config.ProposerAddress,
				Proposer:        block.Proposer,
				MessageReceipts: make([]*v1beta1.MessageReceipt, len(req.Msg.Transactionv2.Envelope.Messages)),
			}
			// get all receipts by tx hash and use index to map to the correct message

			// get ERNs, MEADs, and PIES by tx hash and use index to map to the correct message
			ernReceipts, err := c.core.db.GetERNReceipts(ctx, txhash)
			if err != nil {
				c.core.logger.Error("error getting ERN receipts", zap.Error(err))
			} else {
				for _, ernReceipt := range ernReceipts {
					ernAck := &ddexv1beta1.NewReleaseMessageAck{}
					err = proto.Unmarshal(ernReceipt.RawAcknowledgment, ernAck)
					if err != nil {
						c.core.logger.Error("error unmarshalling ERN receipt", zap.Error(err))
					}
					receipt.MessageReceipts[ernReceipt.Index] = &v1beta1.MessageReceipt{
						MessageIndex: int32(ernReceipt.Index),
						Result: &v1beta1.MessageReceipt_ErnAck{
							ErnAck: ernAck,
						},
					}
				}
			}

			meadReceipts, err := c.core.db.GetMEADReceipts(ctx, txhash)
			if err != nil {
				c.core.logger.Error("error getting MEAD receipts", zap.Error(err))
			} else {
				for _, meadReceipt := range meadReceipts {
					meadAck := &ddexv1beta1.MeadMessageAck{}
					err = proto.Unmarshal(meadReceipt.RawAcknowledgment, meadAck)
					if err != nil {
						c.core.logger.Error("error unmarshalling MEAD receipt", zap.Error(err))
					}
					receipt.MessageReceipts[meadReceipt.Index] = &v1beta1.MessageReceipt{
						MessageIndex: int32(meadReceipt.Index),
						Result: &v1beta1.MessageReceipt_MeadAck{
							MeadAck: meadAck,
						},
					}
				}
			}

			pieReceipts, err := c.core.db.GetPIEReceipts(ctx, txhash)
			if err != nil {
				c.core.logger.Error("error getting PIE receipts", zap.Error(err))
			} else {
				for _, pieReceipt := range pieReceipts {
					pieAck := &ddexv1beta1.PieMessageAck{}
					err = proto.Unmarshal(pieReceipt.RawAcknowledgment, pieAck)
					if err != nil {
						c.core.logger.Error("error unmarshalling PIE receipt", zap.Error(err))
					}
					receipt.MessageReceipts[pieReceipt.Index] = &v1beta1.MessageReceipt{
						MessageIndex: int32(pieReceipt.Index),
						Result: &v1beta1.MessageReceipt_PieAck{
							PieAck: pieAck,
						},
					}
				}
			}
		}

		return connect.NewResponse(&v1.SendTransactionResponse{
			Transaction: &v1.Transaction{
				Hash:          txhash,
				BlockHash:     block.Hash,
				ChainId:       c.core.config.GenesisFile.ChainID,
				Height:        block.Height,
				Timestamp:     timestamppb.New(block.CreatedAt.Time),
				Transaction:   req.Msg.Transaction,
				Transactionv2: req.Msg.Transactionv2,
			},
			TransactionReceipt: receipt,
		}), nil
	case <-time.After(30 * time.Second):
		c.core.logger.Error("tx timeout waiting to be included", zap.String("tx", txhash))
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
		c.core.logger.Error("error getting block", zap.Error(err))
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
			Hash:        common.ToTxHashFromBytes(tx),
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
		c.core.logger.Error("error getting stored snapshots", zap.Error(err))
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

// GetStatus implements v1connect.CoreServiceHandler.
func (c *CoreService) GetStatus(ctx context.Context, _ *connect.Request[v1.GetStatusRequest]) (*connect.Response[v1.GetStatusResponse], error) {
	live := true
	ready := false

	res := &v1.GetStatusResponse{
		Live:  live,
		Ready: ready,
	}

	if c.core == nil {
		return connect.NewResponse(res), nil
	}

	peerStatuses := c.core.peerStatus.Values()
	sort.Slice(peerStatuses, func(i, j int) bool {
		return peerStatuses[i].CometAddress < peerStatuses[j].CometAddress
	})

	nodeInfo, _ := c.core.cache.nodeInfo.Get(NodeInfoKey)
	peers := &v1.GetStatusResponse_PeerInfo{Peers: peerStatuses}
	chainInfo, _ := c.core.cache.chainInfo.Get(ChainInfoKey)
	syncInfo, _ := c.core.cache.syncInfo.Get(SyncInfoKey)
	pruningInfo := &v1.GetStatusResponse_PruningInfo{}
	resourceInfo, _ := c.core.cache.resourceInfo.Get(ResourceInfoKey)
	mempoolInfo, _ := c.core.cache.mempoolInfo.Get(MempoolInfoKey)
	snapshotInfo, _ := c.core.cache.snapshotInfo.Get(SnapshotInfoKey)

	chainInfo.TotalTxCount = c.core.cache.currentTxCount.Load()

	// Retrieve process states from cache
	abciState, _ := c.core.cache.abciState.Get(ProcessStateABCI)
	registryBridgeState, _ := c.core.cache.registryBridgeState.Get(ProcessStateRegistryBridge)
	echoServerState, _ := c.core.cache.echoServerState.Get(ProcessStateEchoServer)
	syncTasksState, _ := c.core.cache.syncTasksState.Get(ProcessStateSyncTasks)
	peerManagerState, _ := c.core.cache.peerManagerState.Get(ProcessStatePeerManager)
	dataCompanionState, _ := c.core.cache.dataCompanionState.Get(ProcessStateDataCompanion)
	cacheState, _ := c.core.cache.cacheState.Get(ProcessStateCache)
	logSyncState, _ := c.core.cache.logSyncState.Get(ProcessStateLogSync)
	stateSyncState, _ := c.core.cache.stateSyncState.Get(ProcessStateStateSync)
	mempoolCacheState, _ := c.core.cache.mempoolCacheState.Get(ProcessStateMempoolCache)

	// data companion state
	if c.core != nil && c.core.rpc != nil {
		status, err := c.core.rpc.Status(ctx)
		if err == nil {
			pruningInfo.EarliestHeight = status.SyncInfo.EarliestBlockHeight
			pruningInfo.Enabled = status.SyncInfo.EarliestBlockHeight != 1
			pruningInfo.RetainBlocks = c.core.config.RetainHeight
		}
	}

	processInfo := &v1.GetStatusResponse_ProcessInfo{
		Abci:           abciState,
		RegistryBridge: registryBridgeState,
		EchoServer:     echoServerState,
		SyncTasks:      syncTasksState,
		PeerManager:    peerManagerState,
		DataCompanion:  dataCompanionState,
		Cache:          cacheState,
		LogSync:        logSyncState,
		StateSync:      stateSyncState,
		MempoolCache:   mempoolCacheState,
	}

	peersOk := len(peers.Peers) > 0
	syncInfoOk := syncInfo.Synced
	diskOk := resourceInfo.DiskFree > 0
	memOk := resourceInfo.MemUsage < resourceInfo.MemSize
	cpuOk := resourceInfo.CpuUsage < 100
	ready = peersOk && syncInfoOk && diskOk && memOk && cpuOk

	res.Ready = ready
	res.NodeInfo = nodeInfo
	res.Peers = peers
	res.ChainInfo = chainInfo
	res.SyncInfo = syncInfo
	res.PruningInfo = pruningInfo
	res.ResourceInfo = resourceInfo
	res.MempoolInfo = mempoolInfo
	res.SnapshotInfo = snapshotInfo
	res.ProcessInfo = processInfo

	return connect.NewResponse(res), nil
}

// GetRewardAttestation implements v1connect.CoreServiceHandler.
func (c *CoreService) GetRewardAttestation(ctx context.Context, req *connect.Request[v1.GetRewardAttestationRequest]) (*connect.Response[v1.GetRewardAttestationResponse], error) {
	ethRecipientAddress := req.Msg.EthRecipientAddress
	if ethRecipientAddress == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("eth_recipient_address is required"))
	}
	rewardID := req.Msg.RewardId
	if rewardID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("reward_id is required"))
	}
	specifier := req.Msg.Specifier
	if specifier == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("specifier is required"))
	}
	oracleAddress := req.Msg.OracleAddress
	if oracleAddress == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("oracle_address is required"))
	}
	signature := req.Msg.Signature
	if signature == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("signature is required"))
	}
	amount := req.Msg.Amount
	if amount == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("amount is required"))
	}

	claim := rewards.RewardClaim{
		RecipientEthAddress:       ethRecipientAddress,
		Amount:                    amount,
		RewardID:                  rewardID,
		Specifier:                 specifier,
		AntiAbuseOracleEthAddress: oracleAddress,
	}

	err := c.core.rewards.Validate(claim)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	err = c.core.rewards.Authenticate(claim, signature)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	_, attestation, err := c.core.rewards.Attest(claim)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	res := &v1.GetRewardAttestationResponse{
		Owner:       c.core.rewards.EthereumAddress,
		Attestation: attestation,
	}

	return connect.NewResponse(res), nil
}

// GetRewards implements v1connect.CoreServiceHandler.
func (c *CoreService) GetRewards(context.Context, *connect.Request[v1.GetRewardsRequest]) (*connect.Response[v1.GetRewardsResponse], error) {
	rewards := c.core.rewards.Rewards
	rewardResponses := make([]*v1.Reward, 0, len(rewards))
	for _, reward := range rewards {
		claimAuthorities := make([]*v1.ClaimAuthority, 0, len(reward.ClaimAuthorities))
		for _, claimAuthority := range reward.ClaimAuthorities {
			claimAuthorities = append(claimAuthorities, &v1.ClaimAuthority{
				Address: claimAuthority.Address,
			})
		}
		rewardResponses = append(rewardResponses, &v1.Reward{
			RewardId:         reward.RewardId,
			Amount:           reward.Amount,
			Name:             reward.Name,
			ClaimAuthorities: claimAuthorities,
		})
	}

	res := &v1.GetRewardsResponse{
		Rewards: rewardResponses,
	}

	return connect.NewResponse(res), nil
}

// GetERN implements v1connect.CoreServiceHandler.
func (c *CoreService) GetERN(ctx context.Context, req *connect.Request[v1.GetERNRequest]) (*connect.Response[v1.GetERNResponse], error) {
	address := req.Msg.Address
	if address == "" {
		return nil, fmt.Errorf("address is required")
	}

	dbErn, err := c.core.db.GetERN(ctx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("ERN not found for address: %s", address)
		}
		return nil, fmt.Errorf("failed to get ERN: %w", err)
	}

	var ern ddexv1beta1.NewReleaseMessage
	if err := proto.Unmarshal(dbErn.RawMessage, &ern); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ERN message: %w", err)
	}

	return connect.NewResponse(&v1.GetERNResponse{
		Ern: &ern,
	}), nil
}

// GetMEAD implements v1connect.CoreServiceHandler.
func (c *CoreService) GetMEAD(ctx context.Context, req *connect.Request[v1.GetMEADRequest]) (*connect.Response[v1.GetMEADResponse], error) {
	address := req.Msg.Address
	if address == "" {
		return nil, fmt.Errorf("address is required")
	}

	dbMead, err := c.core.db.GetMEAD(ctx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("MEAD not found for address: %s", address)
		}
		return nil, fmt.Errorf("failed to get MEAD: %w", err)
	}

	var mead ddexv1beta1.MeadMessage
	if err := proto.Unmarshal(dbMead.RawMessage, &mead); err != nil {
		return nil, fmt.Errorf("failed to unmarshal MEAD message: %w", err)
	}

	return connect.NewResponse(&v1.GetMEADResponse{
		Mead: &mead,
	}), nil
}

// GetPIE implements v1connect.CoreServiceHandler.
func (c *CoreService) GetPIE(ctx context.Context, req *connect.Request[v1.GetPIERequest]) (*connect.Response[v1.GetPIEResponse], error) {
	address := req.Msg.Address
	if address == "" {
		return nil, fmt.Errorf("address is required")
	}

	dbPie, err := c.core.db.GetPIE(ctx, address)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("PIE not found for address: %s", address)
		}
		return nil, fmt.Errorf("failed to get PIE: %w", err)
	}

	var pie ddexv1beta1.PieMessage
	if err := proto.Unmarshal(dbPie.RawMessage, &pie); err != nil {
		return nil, fmt.Errorf("failed to unmarshal PIE message: %w", err)
	}

	return connect.NewResponse(&v1.GetPIEResponse{
		Pie: &pie,
	}), nil
}

func (c *CoreService) GetSlashAttestation(ctx context.Context, req *connect.Request[v1.GetSlashAttestationRequest]) (*connect.Response[v1.GetSlashAttestationResponse], error) {
	signature, err := c.core.getSlashAttestation(ctx, req.Msg.Data)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.GetSlashAttestationResponse{
		Signature: signature,
		Endpoint:  c.core.config.NodeEndpoint,
	}), nil
}

func (c *CoreService) GetSlashAttestations(ctx context.Context, req *connect.Request[v1.GetSlashAttestationsRequest]) (*connect.Response[v1.GetSlashAttestationsResponse], error) {
	attestations, err := c.core.gatherSlashAttestations(ctx, req.Msg.Request.Data)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	attestationResponses := make([]*v1.GetSlashAttestationResponse, 0, len(attestations))
	for endpoint, signature := range attestations {
		attestationResponses = append(
			attestationResponses,
			&v1.GetSlashAttestationResponse{Signature: signature, Endpoint: endpoint},
		)
	}
	return connect.NewResponse(&v1.GetSlashAttestationsResponse{
		Attestations: attestationResponses,
	}), nil
}
