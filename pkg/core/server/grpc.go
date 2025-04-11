// implementation of the grpc service definition found in the audius protocol.proto spec
package server

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net"
	"reflect"
	"strings"
	"time"

	"github.com/AudiusProject/audiusd/pkg/core/common"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_proto"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/iancoleman/strcase"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	TrackPlaysProtoName     string
	ManageEntitiesProtoName string
	SlaRollupProtoName      string
	SlaNodeReportProtoName  string
)

func init() {
	TrackPlaysProtoName = GetProtoTypeName(&core_proto.TrackPlays{})
	ManageEntitiesProtoName = GetProtoTypeName(&core_proto.ManageEntityLegacy{})
	SlaRollupProtoName = GetProtoTypeName(&core_proto.SlaRollup{})
	SlaNodeReportProtoName = GetProtoTypeName(&core_proto.SlaNodeReport{})
}

func GetProtoTypeName(msg proto.Message) string {
	return strcase.ToSnake(reflect.TypeOf(msg).Elem().Name())
}

func (s *Server) SendTransaction(ctx context.Context, req *core_proto.SendTransactionRequest) (*core_proto.TransactionResponse, error) {
	// TODO: do validation check
	txhash, err := common.ToTxHash(req.GetTransaction())
	if err != nil {
		return nil, fmt.Errorf("could not get tx hash of signed tx: %v", err)
	}

	// TODO: use data companion to keep this value up to date via channel
	status, err := s.rpc.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("chain not healthy: %v", err)
	}

	deadline := status.SyncInfo.LatestBlockHeight + 10
	mempoolTx := &MempoolTransaction{
		Tx:       req.GetTransaction(),
		Deadline: deadline,
	}

	ps := s.txPubsub

	txHashCh := ps.Subscribe(txhash)
	defer ps.Unsubscribe(txhash, txHashCh)

	s.logger.Infof("adding tx: %v", req.Transaction)

	// add transaction to mempool with broadcast set to true
	err = s.addMempoolTransaction(txhash, mempoolTx, true)
	if err != nil {
		s.logger.Errorf("tx could not be included in mempool %s: %v", txhash, err)
		return nil, fmt.Errorf("could not add tx to mempool %v", err)
	}

	select {
	case <-txHashCh:
		tx, err := s.db.GetTx(ctx, txhash)
		if err != nil {
			return nil, err
		}

		block, err := s.db.GetBlock(ctx, tx.BlockID)
		if err != nil {
			return nil, err
		}

		return &core_proto.TransactionResponse{
			Txhash:      txhash,
			Transaction: req.GetTransaction(),
			BlockHeight: block.Height,
			BlockHash:   block.Hash,
		}, nil
	case <-time.After(30 * time.Second):
		s.logger.Errorf("tx timeout waiting to be included %s", txhash)
		return nil, errors.New("tx waiting timeout")
	}
}

func (s *Server) ForwardTransaction(ctx context.Context, req *core_proto.ForwardTransactionRequest) (*core_proto.ForwardTransactionResponse, error) {
	// TODO: check signature from known node

	// TODO: validate transaction in same way as send transaction

	mempoolKey, err := common.ToTxHash(req.GetTransaction())
	if err != nil {
		return nil, fmt.Errorf("could not get tx hash of signed tx: %v", err)
	}

	s.logger.Infof("received forwarded tx: %v", req.Transaction)

	// TODO: intake block deadline from request
	status, err := s.rpc.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("chain not healthy: %v", err)
	}

	deadline := status.SyncInfo.LatestBlockHeight + 10
	mempoolTx := &MempoolTransaction{
		Tx:       req.GetTransaction(),
		Deadline: deadline,
	}

	err = s.addMempoolTransaction(mempoolKey, mempoolTx, false)
	if err != nil {
		return nil, fmt.Errorf("could not add tx to mempool %v", err)
	}

	return &core_proto.ForwardTransactionResponse{}, nil
}

func (s *Server) GetTransaction(ctx context.Context, req *core_proto.GetTransactionRequest) (*core_proto.TransactionResponse, error) {
	txhash := req.GetTxhash()

	s.logger.Debug("query", "txhash", txhash)

	tx, err := s.db.GetTx(ctx, txhash)
	if err != nil {
		return nil, err
	}

	var transaction core_proto.SignedTransaction
	err = proto.Unmarshal(tx.Transaction, &transaction)
	if err != nil {
		return nil, err
	}

	block, err := s.db.GetBlock(ctx, tx.BlockID)
	if err != nil {
		return nil, err
	}

	res := &core_proto.TransactionResponse{
		Txhash:      txhash,
		Transaction: &transaction,
		BlockHeight: block.Height,
		BlockHash:   block.Hash,
	}

	return res, nil
}

func (s *Server) GetBlock(ctx context.Context, req *core_proto.GetBlockRequest) (*core_proto.BlockResponse, error) {
	currentHeight := s.cache.currentHeight.Load()
	if req.Height > currentHeight {
		return &core_proto.BlockResponse{
			Chainid:       s.config.GenesisFile.ChainID,
			Height:        -1,
			CurrentHeight: currentHeight,
		}, nil
	}

	block, err := s.db.GetBlock(ctx, req.Height)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// fallback to rpc for now, remove after mainnet-alpha
			return s.getBlockRpcFallback(ctx, req.Height)
		}
		s.logger.Errorf("error getting block: %v", err)
		return nil, err
	}

	blockTxs, err := s.db.GetBlockTransactions(ctx, req.Height)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	tx_responses := []*core_proto.TransactionResponse{}
	for _, tx := range blockTxs {
		var transaction core_proto.SignedTransaction
		err = proto.Unmarshal(tx.Transaction, &transaction)
		if err != nil {
			return nil, err
		}
		res := &core_proto.TransactionResponse{
			Txhash:      transaction.TxHash(),
			Transaction: &transaction,
		}
		tx_responses = append(tx_responses, res)
	}

	res := &core_proto.BlockResponse{
		Blockhash:            block.Hash,
		Chainid:              s.config.GenesisFile.ChainID,
		Proposer:             block.Proposer,
		Height:               block.Height,
		Transactions:         []*core_proto.SignedTransaction{},
		CurrentHeight:        currentHeight,
		Timestamp:            timestamppb.New(block.CreatedAt.Time),
		TransactionResponses: sortTransactionResponse(tx_responses),
	}

	return res, nil
}

func (s *Server) GetNodeInfo(ctx context.Context, req *core_proto.GetNodeInfoRequest) (*core_proto.NodeInfoResponse, error) {
	status, err := s.rpc.Status(ctx)
	if err != nil {
		return nil, err
	}

	res := &core_proto.NodeInfoResponse{
		Chainid:       s.config.GenesisFile.ChainID,
		Synced:        !status.SyncInfo.CatchingUp,
		CometAddress:  s.config.ProposerAddress,
		EthAddress:    s.config.WalletAddress,
		CurrentHeight: status.SyncInfo.LatestBlockHeight,
	}
	return res, nil
}

func (s *Server) Ping(ctx context.Context, req *core_proto.PingRequest) (*core_proto.PingResponse, error) {
	return &core_proto.PingResponse{Message: "pong"}, nil
}

func (s *Server) startGRPC() error {
	s.logger.Info("core gRPC server starting")

	gs := s.grpcServer

	grpcLis, err := net.Listen("tcp", s.config.GRPCladdr)
	if err != nil {
		return fmt.Errorf("grpc listener not created: %v", err)
	}

	core_proto.RegisterProtocolServer(gs, s)

	close(s.awaitGrpcServerReady)
	s.logger.Info("core gRPC server ready")

	if err := gs.Serve(grpcLis); err != nil {
		s.logger.Errorf("grpc failed to start: %v", err)
		return err
	}
	return nil
}

// Utilities
func (s *Server) getBlockRpcFallback(ctx context.Context, height int64) (*core_proto.BlockResponse, error) {
	currentHeight := s.cache.currentHeight.Load()
	block, err := s.rpc.Block(ctx, &height)
	if err != nil {
		blockInFutureMsg := "must be less than or equal to the current blockchain height"
		if strings.Contains(err.Error(), blockInFutureMsg) {
			// return block with -1 to indicate it doesn't exist yet
			return &core_proto.BlockResponse{
				Chainid:       s.config.GenesisFile.ChainID,
				Height:        -1,
				CurrentHeight: currentHeight,
			}, nil
		}
		s.logger.Errorf("error getting block: %v", err)
		return nil, err
	}

	txs := []*core_proto.SignedTransaction{}
	for _, tx := range block.Block.Txs {
		var transaction core_proto.SignedTransaction
		err = proto.Unmarshal(tx, &transaction)
		if err != nil {
			return nil, err
		}
		txs = append(txs, &transaction)
	}

	res := &core_proto.BlockResponse{
		Blockhash:     block.BlockID.Hash.String(),
		Chainid:       s.config.GenesisFile.ChainID,
		Proposer:      block.Block.ProposerAddress.String(),
		Height:        block.Block.Height,
		Transactions:  txs,
		CurrentHeight: currentHeight,
		Timestamp:     timestamppb.New(block.Block.Time),
	}

	return res, nil
}

func (s *Server) GetRegistrationAttestation(ctx context.Context, req *core_proto.RegistrationAttestationRequest) (*core_proto.RegistrationAttestationResponse, error) {
	reg := req.GetRegistration()
	if reg == nil {
		return nil, errors.New("empty registration attestation")
	}

	if reg.Deadline < s.cache.currentHeight.Load() || reg.Deadline > s.cache.currentHeight.Load()+maxRegistrationAttestationValidity {
		return nil, fmt.Errorf("cannot sign registration request with deadline %d (current height is %d)", reg.Deadline, s.cache.currentHeight.Load())
	}

	if !s.isNodeRegisteredOnEthereum(
		ethcommon.HexToAddress(reg.DelegateWallet),
		reg.Endpoint,
		big.NewInt(reg.EthBlock),
	) {
		s.logger.Error(
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
		s.logger.Error("could not marshal registration", "error", err)
		return nil, err
	}
	sig, err := common.EthSign(s.config.EthereumKey, regBytes)
	if err != nil {
		s.logger.Error("could not sign registration", "error", err)
		return nil, err
	}

	return &core_proto.RegistrationAttestationResponse{
		Signature:    sig,
		Registration: reg,
	}, nil
}

func (s *Server) GetDeregistrationAttestation(ctx context.Context, req *core_proto.DeregistrationAttestationRequest) (*core_proto.DeregistrationAttestationResponse, error) {
	dereg := req.GetDeregistration()
	if dereg == nil {
		return nil, errors.New("empty deregistration attestation")
	}

	node, err := s.db.GetRegisteredNodeByCometAddress(ctx, dereg.CometAddress)
	if err != nil {
		return nil, fmt.Errorf("could not attest deregistration for '%s': %v", dereg.CometAddress, err)
	}

	ethBlock := new(big.Int)
	ethBlock, ok := ethBlock.SetString(node.EthBlock, 10)
	if !ok {
		return nil, fmt.Errorf("could not format eth block '%s' for node '%s'", node.EthBlock, node.Endpoint)
	}

	if s.isNodeRegisteredOnEthereum(
		ethcommon.HexToAddress(node.EthAddress),
		node.Endpoint,
		ethBlock,
	) {
		s.logger.Error("Could not attest to node eth deregistration: node is still registered",
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
		s.logger.Error("could not marshal deregistration", "error", err)
		return nil, err
	}
	sig, err := common.EthSign(s.config.EthereumKey, deregBytes)
	if err != nil {
		s.logger.Error("could not sign deregistration", "error", err)
		return nil, err
	}

	return &core_proto.DeregistrationAttestationResponse{
		Signature:      sig,
		Deregistration: dereg,
	}, nil
}
