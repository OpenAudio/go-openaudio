package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	v1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1beta1"
	ddexv1beta1 "github.com/OpenAudio/go-openaudio/pkg/api/ddex/v1beta1"
	storagev1 "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	storagev1connect "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type CoreService struct {
	core           *Server
	storageService storagev1connect.StorageServiceHandler
}

func NewCoreService() *CoreService {
	return &CoreService{}
}

func (c *CoreService) SetCore(core *Server) {
	c.core = core
	c.core.setSelf(c)
}

func (c *CoreService) SetStorageService(storageService storagev1connect.StorageServiceHandler) {
	c.storageService = storageService
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
	// Check feature flag for programmable distribution features
	if !c.core.config.ProgrammableDistributionEnabled {
		// Check if transaction uses programmable distribution features
		if req.Msg != nil && req.Msg.Transaction != nil && req.Msg.Transaction.GetFileUpload() != nil {
			return nil, connect.NewError(connect.CodeUnimplemented, errors.New("programmable distribution is not enabled in this environment"))
		}
		if req.Msg != nil && req.Msg.Transactionv2 != nil && req.Msg.Transactionv2.Envelope != nil {
			for _, msg := range req.Msg.Transactionv2.Envelope.Messages {
				if msg != nil && msg.GetErn() != nil {
					return nil, connect.NewError(connect.CodeUnimplemented, errors.New("programmable distribution is not enabled in this environment"))
				}
			}
		}
	}

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
		tx := req.Msg.Transaction
		em := tx.GetManageEntity()
		if em != nil {
			err := InjectSigner(c.core.config, em)
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.Join(errors.New("signer not recoverable"), err))
			}
		}
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
	// Check feature flag for programmable distribution features
	if !c.core.config.ProgrammableDistributionEnabled {
		// Check if transaction uses programmable distribution features
		if req.Msg != nil && req.Msg.Transaction != nil && req.Msg.Transaction.GetFileUpload() != nil {
			return nil, connect.NewError(connect.CodeUnimplemented, errors.New("programmable distribution is not enabled in this environment"))
		}
		if req.Msg != nil && req.Msg.Transactionv2 != nil && req.Msg.Transactionv2.Envelope != nil {
			for _, msg := range req.Msg.Transactionv2.Envelope.Messages {
				if msg != nil && msg.GetErn() != nil {
					return nil, connect.NewError(connect.CodeUnimplemented, errors.New("programmable distribution is not enabled in this environment"))
				}
			}
		}
	}

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
		tx := req.Msg.Transaction
		em := tx.GetManageEntity()
		if em != nil {
			err := InjectSigner(c.core.config, em)
			if err != nil {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.Join(errors.New("signer not recoverable"), err))
			}
		}
		// Use consistent hashing by marshaling to bytes first, matching abci.go behavior
		txBytes, marshalErr := proto.Marshal(req.Msg.Transaction)
		if marshalErr != nil {
			return nil, fmt.Errorf("could not marshal transaction: %v", marshalErr)
		}
		txhash = common.ToTxHashFromBytes(txBytes)

		// Validate v1 transactions
		err = c.core.validateV1Transaction(ctx, c.core.cache.currentHeight.Load(), req.Msg.Transaction)
		if err != nil {
			return nil, fmt.Errorf("transaction validation failed: %v", err)
		}
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

	// Only support programmatic rewards via reward_address
	rewardAddress := req.Msg.RewardAddress
	if rewardAddress == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("reward_address is required"))
	}

	specifier := req.Msg.Specifier
	if specifier == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("specifier is required"))
	}
	claimAuthority := req.Msg.ClaimAuthority
	if claimAuthority == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("claim_authority is required"))
	}
	signature := req.Msg.Signature
	if signature == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("signature is required"))
	}
	amount := req.Msg.Amount
	if amount == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("amount is required"))
	}

	// Get programmatic reward by deployed address
	reward, err := c.core.db.GetReward(ctx, rewardAddress)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("programmatic reward not found"))
	}

	sigData := &v1.RewardAttestationSignature{
		EthRecipientAddress: req.Msg.EthRecipientAddress,
		Amount:              req.Msg.Amount,
		RewardAddress:       req.Msg.RewardAddress,
		RewardId:            req.Msg.RewardId,
		Specifier:           req.Msg.Specifier,
		ClaimAuthority:      req.Msg.ClaimAuthority,
	}

	signer, err := common.ProtoRecover(sigData, req.Msg.Signature)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("signature recovery failure"))
	}

	// Verify claim authority is authorized to sign for this reward
	authorized := false
	for _, ca := range reward.ClaimAuthorities {
		if strings.EqualFold(ca, signer) {
			authorized = true
			break
		}
	}
	if !authorized {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("claim authority not authorized for this programmatic reward"))
	}

	if !strings.EqualFold(req.Msg.GetClaimAuthority(), signer) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("claim authority and signer mismatch"))
	}

	attestation, err := common.ProtoSign(c.core.config.EthereumKey, sigData)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("error signing attestation"))
	}

	res := &v1.GetRewardAttestationResponse{
		Owner:       c.core.rewards.EthereumAddress,
		Attestation: attestation,
	}

	return connect.NewResponse(res), nil
}

// GetRewards implements v1connect.CoreServiceHandler.
func (c *CoreService) GetRewards(ctx context.Context, req *connect.Request[v1.GetRewardsRequest]) (*connect.Response[v1.GetRewardsResponse], error) {
	claimAuthority := req.Msg.ClaimAuthority
	if claimAuthority == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("claim_authority required"))
	}

	rewards, err := c.core.db.GetRewardsByClaimAuthority(ctx, claimAuthority)
	if err != nil {
		return nil, err
	}

	responseRewards := make([]*v1.GetRewardResponse, 0, len(rewards))
	for _, reward := range rewards {
		responseRewards = append(responseRewards, &v1.GetRewardResponse{
			Address:          reward.Address,
			RewardId:         reward.RewardID,
			Name:             reward.Name,
			Amount:           uint64(reward.Amount),
			ClaimAuthorities: reward.ClaimAuthorities,
			Sender:           reward.Sender,
			BlockHeight:      reward.BlockHeight,
		})
	}

	return connect.NewResponse(&v1.GetRewardsResponse{
		Rewards: responseRewards,
	}), nil
}

// GetReward implements v1connect.CoreServiceHandler.
func (c *CoreService) GetReward(ctx context.Context, req *connect.Request[v1.GetRewardRequest]) (*connect.Response[v1.GetRewardResponse], error) {
	address := req.Msg.Address
	txhash := req.Msg.Txhash
	if address == "" && txhash == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("address or txhash is required"))
	}

	if address != "" {
		reward, err := c.core.db.GetReward(ctx, address)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("reward not found for address: %s", address))
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get reward: %w", err))
		}

		res := &v1.GetRewardResponse{
			Address:          reward.Address,
			RewardId:         reward.RewardID,
			Name:             reward.Name,
			Amount:           uint64(reward.Amount),
			ClaimAuthorities: reward.ClaimAuthorities,
			Sender:           reward.Sender,
			BlockHeight:      reward.BlockHeight,
		}

		return connect.NewResponse(res), nil
	}

	if txhash != "" {
		reward, err := c.core.db.GetRewardByTxHash(ctx, txhash)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("reward not found for address: %s", address))
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to get reward: %w", err))
		}

		res := &v1.GetRewardResponse{
			Address:          reward.Address,
			RewardId:         reward.RewardID,
			Name:             reward.Name,
			Amount:           uint64(reward.Amount),
			ClaimAuthorities: reward.ClaimAuthorities,
			Sender:           reward.Sender,
			BlockHeight:      reward.BlockHeight,
		}

		return connect.NewResponse(res), nil
	}

	return nil, connect.NewError(connect.CodeNotFound, nil)
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

// GetStreamURLs implements v1connect.CoreServiceHandler.
func (c *CoreService) GetStreamURLs(ctx context.Context, req *connect.Request[v1.GetStreamURLsRequest]) (*connect.Response[v1.GetStreamURLsResponse], error) {
	// Check feature flag
	if !c.core.config.ProgrammableDistributionEnabled {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("programmable distribution is not enabled in this environment"))
	}

	// Validate request
	if req.Msg.Signature == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("signature is required"))
	}
	if len(req.Msg.Addresses) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("at least one address is required"))
	}
	if req.Msg.ExpiresAt == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("expires_at is required"))
	}

	// Verify signature hasn't expired
	expiryTime := req.Msg.ExpiresAt.AsTime()
	if time.Now().After(expiryTime) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("signature has expired"))
	}

	// Construct signature data to verify
	sigData := &v1.GetStreamURLsSignature{
		Addresses: req.Msg.Addresses,
		ExpiresAt: req.Msg.ExpiresAt,
	}
	sigDataBytes, err := proto.Marshal(sigData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal signature data: %w", err)
	}

	// Recover signer address from signature
	_, signerAddress, err := common.EthRecover(req.Msg.Signature, sigDataBytes)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("invalid signature: %w", err))
	}

	// Process each requested address
	entityStreamURLs := make(map[string]*v1.GetStreamURLsResponse_EntityStreamURLs)

	for _, address := range req.Msg.Addresses {
		// First try to get it as an ERN directly
		dbErn, err := c.core.db.GetERN(ctx, address)

		if err == nil {
			// This is an ERN address - verify ownership
			if !strings.EqualFold(dbErn.Sender, signerAddress) {
				return nil, connect.NewError(connect.CodePermissionDenied,
					fmt.Errorf("signer %s does not own ERN at address %s", signerAddress, address))
			}

			// Unmarshal ERN to get resource details
			var ern ddexv1beta1.NewReleaseMessage
			if err := proto.Unmarshal(dbErn.RawMessage, &ern); err != nil {
				c.core.logger.Error("failed to unmarshal ERN", zap.Error(err))
				continue
			}

			// Get all streamable resources from the ERN
			streamURLs := c.extractStreamURLsFromERN(&ern)
			if len(streamURLs) > 0 {
				entityStreamURLs[address] = &v1.GetStreamURLsResponse_EntityStreamURLs{
					EntityType:      "ern",
					EntityReference: "",
					Urls:            streamURLs,
					ErnAddress:      address,
				}
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			// Not an ERN address - check if it's contained in an ERN
			result, err := c.core.db.GetERNContainingAddress(ctx, address)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					c.core.logger.Warn("address not found in any ERN", zap.String("address", address))
					continue
				}
				return nil, fmt.Errorf("failed to query ERN containing address: %w", err)
			}

			// Verify ownership of parent ERN
			if !strings.EqualFold(result.Sender, signerAddress) {
				return nil, connect.NewError(connect.CodePermissionDenied,
					fmt.Errorf("signer %s does not own ERN containing address %s", signerAddress, address))
			}

			// Unmarshal ERN to get specific entity
			var ern ddexv1beta1.NewReleaseMessage
			if err := proto.Unmarshal(result.RawMessage, &ern); err != nil {
				c.core.logger.Error("failed to unmarshal ERN", zap.Error(err))
				continue
			}

			// Get entity reference based on index and type
			entityRef := c.getEntityReference(&ern, result.EntityType, int(result.EntityIndex))
			streamURLs := c.getEntityStreamURLs(&ern, result.EntityType, entityRef)

			if len(streamURLs) > 0 {
				entityStreamURLs[address] = &v1.GetStreamURLsResponse_EntityStreamURLs{
					EntityType:      result.EntityType,
					EntityReference: entityRef,
					Urls:            streamURLs,
					ErnAddress:      result.ErnAddress,
				}
			}
		} else {
			return nil, fmt.Errorf("failed to get ERN: %w", err)
		}
	}

	return connect.NewResponse(&v1.GetStreamURLsResponse{
		EntityStreamUrls: entityStreamURLs,
	}), nil
}

// GetUploadByCID implements v1connect.CoreServiceHandler.
func (c *CoreService) GetUploadByCID(ctx context.Context, req *connect.Request[v1.GetUploadByCIDRequest]) (*connect.Response[v1.GetUploadByCIDResponse], error) {
	// Check feature flag
	if !c.core.config.ProgrammableDistributionEnabled {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("programmable distribution is not enabled in this environment"))
	}

	upload, err := c.core.db.GetCoreUpload(ctx, req.Msg.Cid)
	if err != nil {
		// Return exists=false if not found instead of error
		return connect.NewResponse(&v1.GetUploadByCIDResponse{
			Exists: false,
		}), nil
	}

	return connect.NewResponse(&v1.GetUploadByCIDResponse{
		Exists:          true,
		UploaderAddress: upload.UploaderAddress,
		OriginalCid:     upload.Cid,
		TranscodedCid:   upload.TranscodedCid,
	}), nil
}

// Helper function to get entity reference by index
func (c *CoreService) getEntityReference(ern *ddexv1beta1.NewReleaseMessage, entityType string, index int) string {
	// Arrays are 1-indexed in PostgreSQL, adjust to 0-indexed
	idx := index - 1

	switch entityType {
	case "resource":
		if idx >= 0 && idx < len(ern.ResourceList) {
			if sr := ern.ResourceList[idx].GetSoundRecording(); sr != nil {
				return sr.ResourceReference
			}
			if img := ern.ResourceList[idx].GetImage(); img != nil {
				return img.ResourceReference
			}
		}
	case "release":
		if idx >= 0 && idx < len(ern.ReleaseList) {
			if mr := ern.ReleaseList[idx].GetMainRelease(); mr != nil {
				return mr.ReleaseReference
			}
			if tr := ern.ReleaseList[idx].GetTrackRelease(); tr != nil {
				return tr.ReleaseReference
			}
		}
	case "party":
		if idx >= 0 && idx < len(ern.PartyList) {
			return ern.PartyList[idx].PartyReference
		}
	}
	return ""
}

// Helper function to extract all streamable URLs from an ERN
func (c *CoreService) extractStreamURLsFromERN(ern *ddexv1beta1.NewReleaseMessage) []string {
	var urls []string

	for _, resource := range ern.ResourceList {
		if sr := resource.GetSoundRecording(); sr != nil {
			if sre := sr.GetSoundRecordingEdition(); sre != nil {
				if td := sre.GetTechnicalDetails(); td != nil {
					if df := td.GetDeliveryFile(); df != nil {
						if f := df.GetFile(); f != nil && f.Uri != "" {
							// Generate signed streaming URLs for the CID from multiple hosts
							streamURLs := c.generateStreamURLs(f.Uri)
							urls = append(urls, streamURLs...)
						}
					}
				}
			}
		}
	}

	return urls
}

// Helper function to get stream URLs for a specific entity
func (c *CoreService) getEntityStreamURLs(ern *ddexv1beta1.NewReleaseMessage, entityType, entityRef string) []string {
	var urls []string

	switch entityType {
	case "resource":
		// Find the specific resource and return its URL
		for _, resource := range ern.ResourceList {
			if sr := resource.GetSoundRecording(); sr != nil && sr.ResourceReference == entityRef {
				if sre := sr.GetSoundRecordingEdition(); sre != nil {
					if td := sre.GetTechnicalDetails(); td != nil {
						if df := td.GetDeliveryFile(); df != nil {
							if f := df.GetFile(); f != nil && f.Uri != "" {
								streamURLs := c.generateStreamURLs(f.Uri)
								urls = append(urls, streamURLs...)
							}
						}
					}
				}
			}
		}
	case "release":
		// Find all resources associated with this release
		for _, release := range ern.ReleaseList {
			var isTargetRelease bool
			if mr := release.GetMainRelease(); mr != nil && mr.ReleaseReference == entityRef {
				isTargetRelease = true
			} else if tr := release.GetTrackRelease(); tr != nil && tr.ReleaseReference == entityRef {
				isTargetRelease = true
			}

			if isTargetRelease {
				// Get resource references from the release
				if mr := release.GetMainRelease(); mr != nil {
					urls = append(urls, c.getResourceURLsFromRelease(ern, mr)...)
				} else if tr := release.GetTrackRelease(); tr != nil {
					urls = append(urls, c.getResourceURLsFromReleaseTrack(ern, tr)...)
				}
			}
		}
	case "ern":
		// Return all streamable resources
		urls = c.extractStreamURLsFromERN(ern)
	}

	return urls
}

// Helper to get resource URLs from a release
func (c *CoreService) getResourceURLsFromRelease(ern *ddexv1beta1.NewReleaseMessage, release *ddexv1beta1.Release_Release) []string {
	var urls []string

	if release.ResourceGroup != nil {
		for _, rg := range release.ResourceGroup.ResourceGroup {
			for _, item := range rg.ResourceGroupContentItem {
				// Find the resource with this reference
				for _, resource := range ern.ResourceList {
					if sr := resource.GetSoundRecording(); sr != nil && sr.ResourceReference == item.ResourceGroupContentItemText {
						if sre := sr.GetSoundRecordingEdition(); sre != nil {
							if td := sre.GetTechnicalDetails(); td != nil {
								if df := td.GetDeliveryFile(); df != nil {
									if f := df.GetFile(); f != nil && f.Uri != "" {
										streamURLs := c.generateStreamURLs(f.Uri)
										urls = append(urls, streamURLs...)
									}
								}
							}
						}
					}
				}
			}
		}
	}

	return urls
}

// Helper to get resource URLs from a track release
func (c *CoreService) getResourceURLsFromReleaseTrack(ern *ddexv1beta1.NewReleaseMessage, release *ddexv1beta1.Release_TrackRelease) []string {
	var urls []string

	// TrackRelease only has a direct reference to a resource
	if release.ReleaseResourceReference != "" {
		// Find the resource with this reference
		for _, resource := range ern.ResourceList {
			if sr := resource.GetSoundRecording(); sr != nil && sr.ResourceReference == release.ReleaseResourceReference {
				if sre := sr.GetSoundRecordingEdition(); sre != nil {
					if td := sre.GetTechnicalDetails(); td != nil {
						if df := td.GetDeliveryFile(); df != nil {
							if f := df.GetFile(); f != nil && f.Uri != "" {
								streamURLs := c.generateStreamURLs(f.Uri)
								urls = append(urls, streamURLs...)
							}
						}
					}
				}
			}
		}
	}

	return urls
}

// Helper function to generate a signed streaming URL for a CID
func (c *CoreService) generateStreamURL(cid string) string {
	// Generate a time-limited signed URL for streaming
	// Using mediorum's streaming endpoint
	baseURL := fmt.Sprintf("%s/tracks/cidstream/%s", c.core.config.NodeEndpoint, cid)

	// Create signature data matching production format exactly
	sigData := &signature.SignatureData{
		Cid:       cid,
		Timestamp: time.Now().UnixMilli(), // mediorum expects milliseconds
		// Don't set ShouldCache, UploadID - let them be zero values to match production
		// TrackId and UserId will be 0 for ERN streaming
	}

	// Generate the signature query string using mediorum's helper
	sigQueryString, err := signature.GenerateQueryStringFromSignatureData(sigData, c.core.config.EthereumKey)
	if err != nil {
		c.core.logger.Error("failed to generate stream signature", zap.Error(err))
		return ""
	}

	// URL encode the signature since it's a JSON string with special characters
	encodedSig := url.QueryEscape(sigQueryString)

	return fmt.Sprintf("%s?signature=%s", baseURL, encodedSig)
}

// generateStreamURLs generates signed streaming URLs for a CID from multiple hosts using rendezvous hashing
func (c *CoreService) generateStreamURLs(cid string) []string {
	ctx := context.Background()

	// If storage service is available, use it to get rendezvous nodes
	if c.storageService != nil {
		req := &storagev1.GetRendezvousNodesRequest{
			Cid:               cid,
			ReplicationFactor: 3, // Default replication factor
		}

		resp, err := c.storageService.GetRendezvousNodes(ctx, connect.NewRequest(req))
		if err == nil && len(resp.Msg.Nodes) > 0 {
			// Generate signed URLs for each node
			urls := make([]string, 0, len(resp.Msg.Nodes))
			for _, endpoint := range resp.Msg.Nodes {
				// Generate signed URL for this host
				baseURL := fmt.Sprintf("%s/tracks/cidstream/%s", endpoint, cid)

				sigData := &signature.SignatureData{
					Cid:       cid,
					Timestamp: time.Now().UnixMilli(),
					// Don't set ShouldCache - match production format
				}

				sigQueryString, err := signature.GenerateQueryStringFromSignatureData(sigData, c.core.config.EthereumKey)
				if err != nil {
					c.core.logger.Error("failed to generate stream signature for endpoint",
						zap.String("endpoint", endpoint),
						zap.Error(err))
					continue
				}

				encodedSig := url.QueryEscape(sigQueryString)
				streamURL := fmt.Sprintf("%s?signature=%s", baseURL, encodedSig)
				urls = append(urls, streamURL)
			}

			if len(urls) > 0 {
				return urls
			}
		} else if err != nil {
			c.core.logger.Debug("could not get rendezvous nodes from storage service",
				zap.String("cid", cid),
				zap.Error(err))
		}
	}

	// Fall back to single URL from current node
	if url := c.generateStreamURL(cid); url != "" {
		return []string{url}
	}
	return []string{}
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

// authenticateProgrammaticRewardClaim validates signatures for programmatic rewards
// using the structured protobuf signature format to prevent cross-reward attacks
func (c *CoreService) authenticateProgrammaticRewardClaim(
	ethRecipientAddress string,
	amount uint64,
	rewardAddress string,
	rewardID string,
	specifier string,
	claimAuthority string,
	signature string,
) error {
	// Create the structured signature payload that claim authorities must sign
	sigPayload := &v1.RewardAttestationSignature{
		EthRecipientAddress: ethRecipientAddress,
		Amount:              amount,
		RewardAddress:       rewardAddress, // This prevents cross-reward signature reuse
		RewardId:            rewardID,
		Specifier:           specifier,
		ClaimAuthority:      claimAuthority,
	}

	// Marshal the protobuf message to get the bytes that should be signed
	sigBytes, err := proto.Marshal(sigPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal signature payload: %w", err)
	}

	// Verify the signature using the common signature verification
	_, sender, err := common.EthRecover(signature, sigBytes)
	if err != nil {
		return fmt.Errorf("failed to recover signature: %w", err)
	}

	// Ensure the recovered address matches the claim authority address
	if !strings.EqualFold(sender, claimAuthority) {
		return fmt.Errorf("signature sender %s does not match claim authority %s", sender, claimAuthority)
	}

	return nil
}
