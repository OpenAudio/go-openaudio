package server

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/api/core/v1/v1connect"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_proto"
	"google.golang.org/protobuf/proto"
)

type CoreService struct {
	core *Server
}

func NewCoreService() *CoreService {
	return &CoreService{}
}

func (c *CoreService) SetCore(core *Server) {
	c.core = core
}

// ForwardTransaction implements v1connect.CoreServiceHandler.
func (c *CoreService) ForwardTransaction(ctx context.Context, req *connect.Request[v1.ForwardTransactionRequest]) (*connect.Response[v1.ForwardTransactionResponse], error) {
	tx, err := convertV1TransactionToSignedTransaction(req.Msg.Transaction)
	if err != nil {
		return nil, fmt.Errorf("failed to convert transaction: %w", err)
	}
	_, err = c.core.ForwardTransaction(ctx, &core_proto.ForwardTransactionRequest{
		Transaction: tx,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to forward transaction: %w", err)
	}
	return connect.NewResponse(&v1.ForwardTransactionResponse{}), nil
}

// GetBlock implements v1connect.CoreServiceHandler.
func (c *CoreService) GetBlock(ctx context.Context, req *connect.Request[v1.GetBlockRequest]) (*connect.Response[v1.GetBlockResponse], error) {
	block, err := c.core.GetBlock(ctx, &core_proto.GetBlockRequest{Height: req.Msg.Height})
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %w", err)
	}

	transactions := make([]*v1.Transaction, len(block.TransactionResponses))
	for i, tx := range block.TransactionResponses {
		signedTx, err := convertSignedTransactionToV1Transaction(tx.Transaction)
		if err != nil {
			return nil, fmt.Errorf("failed to convert transaction: %w", err)
		}

		transactions[i] = &v1.Transaction{
			Hash:        tx.Txhash,
			BlockHash:   tx.BlockHash,
			ChainId:     block.Chainid,
			Height:      block.Height,
			Timestamp:   block.Timestamp,
			Transaction: signedTx,
		}
	}

	return connect.NewResponse(&v1.GetBlockResponse{Block: &v1.Block{
		Height:       block.Height,
		Hash:         block.Blockhash,
		ChainId:      block.Chainid,
		Proposer:     block.Proposer,
		Timestamp:    block.Timestamp,
		Transactions: transactions,
	}}), nil
}

// GetDeregistrationAttestation implements v1connect.CoreServiceHandler.
func (c *CoreService) GetDeregistrationAttestation(ctx context.Context, req *connect.Request[v1.GetDeregistrationAttestationRequest]) (*connect.Response[v1.GetDeregistrationAttestationResponse], error) {
	resp, err := c.core.GetDeregistrationAttestation(ctx, &core_proto.DeregistrationAttestationRequest{
		Deregistration: &core_proto.ValidatorDeregistration{
			CometAddress: req.Msg.Deregistration.CometAddress,
			PubKey:       req.Msg.Deregistration.PubKey,
			Deadline:     req.Msg.Deregistration.Deadline,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get deregistration attestation: %w", err)
	}
	return connect.NewResponse(&v1.GetDeregistrationAttestationResponse{
		Signature: resp.Signature,
		Deregistration: &v1.ValidatorDeregistration{
			CometAddress: resp.Deregistration.CometAddress,
			PubKey:       resp.Deregistration.PubKey,
			Deadline:     resp.Deregistration.Deadline,
		},
	}), nil
}

// GetHealth implements v1connect.CoreServiceHandler.
func (c *CoreService) GetHealth(context.Context, *connect.Request[v1.GetHealthRequest]) (*connect.Response[v1.GetHealthResponse], error) {
	return connect.NewResponse(&v1.GetHealthResponse{}), nil
}

// GetRegistrationAttestation implements v1connect.CoreServiceHandler.
func (c *CoreService) GetRegistrationAttestation(ctx context.Context, req *connect.Request[v1.GetRegistrationAttestationRequest]) (*connect.Response[v1.GetRegistrationAttestationResponse], error) {
	resp, err := c.core.GetRegistrationAttestation(ctx, &core_proto.RegistrationAttestationRequest{
		Registration: &core_proto.ValidatorRegistration{
			CometAddress:   req.Msg.Registration.CometAddress,
			PubKey:         req.Msg.Registration.PubKey,
			Power:          req.Msg.Registration.Power,
			DelegateWallet: req.Msg.Registration.DelegateWallet,
			Endpoint:       req.Msg.Registration.Endpoint,
			NodeType:       req.Msg.Registration.NodeType,
			EthBlock:       req.Msg.Registration.EthBlock,
			SpId:           req.Msg.Registration.SpId,
			Deadline:       req.Msg.Registration.Deadline,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get registration attestation: %w", err)
	}
	return connect.NewResponse(&v1.GetRegistrationAttestationResponse{
		Signature: resp.Signature,
		Registration: &v1.ValidatorRegistration{
			CometAddress:   resp.Registration.CometAddress,
			PubKey:         resp.Registration.PubKey,
			Power:          resp.Registration.Power,
			DelegateWallet: resp.Registration.DelegateWallet,
			Endpoint:       resp.Registration.Endpoint,
			NodeType:       resp.Registration.NodeType,
			EthBlock:       resp.Registration.EthBlock,
			SpId:           resp.Registration.SpId,
			Deadline:       resp.Registration.Deadline,
		},
	}), nil
}

// GetTransaction implements v1connect.CoreServiceHandler.
func (c *CoreService) GetTransaction(ctx context.Context, req *connect.Request[v1.GetTransactionRequest]) (*connect.Response[v1.GetTransactionResponse], error) {
	tx, err := c.core.GetTransaction(ctx, &core_proto.GetTransactionRequest{Txhash: req.Msg.TxHash})
	if err != nil {
		return nil, fmt.Errorf("failed to get transaction: %w", err)
	}

	block, err := c.core.GetBlock(ctx, &core_proto.GetBlockRequest{Height: tx.BlockHeight})
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %w", err)
	}

	signedTx, err := convertSignedTransactionToV1Transaction(tx.Transaction)
	if err != nil {
		return nil, fmt.Errorf("failed to convert transaction: %w", err)
	}
	return connect.NewResponse(&v1.GetTransactionResponse{Transaction: &v1.Transaction{
		Hash:        tx.Txhash,
		BlockHash:   tx.BlockHash,
		ChainId:     block.Chainid,
		Height:      block.Height,
		Timestamp:   block.Timestamp,
		Transaction: signedTx,
	}}), nil
}

// Ping implements v1connect.CoreServiceHandler.
func (c *CoreService) Ping(context.Context, *connect.Request[v1.PingRequest]) (*connect.Response[v1.PingResponse], error) {
	return connect.NewResponse(&v1.PingResponse{Message: "pong"}), nil
}

// SendTransaction implements v1connect.CoreServiceHandler.
func (c *CoreService) SendTransaction(ctx context.Context, req *connect.Request[v1.SendTransactionRequest]) (*connect.Response[v1.SendTransactionResponse], error) {
	tx, err := convertV1TransactionToSignedTransaction(req.Msg.Transaction)
	if err != nil {
		return nil, fmt.Errorf("failed to convert transaction: %w", err)
	}
	resp, err := c.core.SendTransaction(ctx, &core_proto.SendTransactionRequest{
		Transaction: tx,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to send transaction: %w", err)
	}
	signedTx, err := convertSignedTransactionToV1Transaction(resp.Transaction)
	if err != nil {
		return nil, fmt.Errorf("failed to convert transaction: %w", err)
	}

	block, err := c.GetBlock(ctx, connect.NewRequest(&v1.GetBlockRequest{Height: resp.BlockHeight}))
	if err != nil {
		return nil, fmt.Errorf("failed to get block: %w", err)
	}

	return connect.NewResponse(&v1.SendTransactionResponse{
		Transaction: &v1.Transaction{
			Transaction: signedTx,
			Hash:        resp.Txhash,
			BlockHash:   resp.BlockHash,
			ChainId:     c.core.config.GenesisFile.ChainID,
			Height:      resp.BlockHeight,
			Timestamp:   block.Msg.Block.Timestamp,
		},
	}), nil
}

func convertSignedTransactionToV1Transaction(tx *core_proto.SignedTransaction) (*v1.SignedTransaction, error) {
	txBytes, err := proto.Marshal(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transaction: %w", err)
	}
	signedTx := &v1.SignedTransaction{}
	err = proto.Unmarshal(txBytes, signedTx)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
	}
	return signedTx, nil
}

func convertV1TransactionToSignedTransaction(tx *v1.SignedTransaction) (*core_proto.SignedTransaction, error) {
	txBytes, err := proto.Marshal(tx)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal transaction: %w", err)
	}
	signedTx := &core_proto.SignedTransaction{}
	err = proto.Unmarshal(txBytes, signedTx)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
	}
	return signedTx, nil
}

var _ v1connect.CoreServiceHandler = (*CoreService)(nil)
