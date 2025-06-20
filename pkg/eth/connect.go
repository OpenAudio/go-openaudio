package eth

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"connectrpc.com/connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/eth/v1"
	"github.com/AudiusProject/audiusd/pkg/api/eth/v1/v1connect"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/eth/contracts"
	"github.com/AudiusProject/audiusd/pkg/eth/db"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ v1connect.EthServiceHandler = (*EthService)(nil)

func (e *EthService) IsReady(context.Context, *connect.Request[v1.IsReadyRequest]) (*connect.Response[v1.IsReadyResponse], error) {
	res := &v1.IsReadyResponse{}
	if e.isReady.Load() {
		res.Ready = true
	}
	return connect.NewResponse(res), nil
}

func (e *EthService) GetRegisteredEndpoints(ctx context.Context, _ *connect.Request[v1.GetRegisteredEndpointsRequest]) (*connect.Response[v1.GetRegisteredEndpointsResponse], error) {
	eps, err := e.db.GetRegisteredEndpoints(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not get registered endpoints: %w", err))
	}
	peps := make([]*v1.ServiceEndpoint, 0, len(eps))
	for _, ep := range eps {
		peps = append(peps, &v1.ServiceEndpoint{
			Id:             int64(ep.ID),
			BlockNumber:    ep.Blocknumber,
			Owner:          ep.Owner,
			Endpoint:       ep.Endpoint,
			DelegateWallet: ep.DelegateWallet,
		})
	}
	res := &v1.GetRegisteredEndpointsResponse{Endpoints: peps}
	return connect.NewResponse(res), nil
}

func (e *EthService) GetRegisteredEndpointInfo(ctx context.Context, req *connect.Request[v1.GetRegisteredEndpointInfoRequest]) (*connect.Response[v1.GetRegisteredEndpointInfoResponse], error) {
	ep, err := e.db.GetRegisteredEndpoint(
		ctx,
		db.GetRegisteredEndpointParams{
			Endpoint:       req.Msg.Endpoint,
			DelegateWallet: req.Msg.DelegateWallet,
		},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not get registered endpoint: %w", err))
	} else if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("endpoint not found: %w", err))
	}
	res := &v1.GetRegisteredEndpointInfoResponse{
		Se: &v1.ServiceEndpoint{
			Id:             int64(ep.ID),
			Owner:          ep.Owner,
			Endpoint:       ep.Endpoint,
			BlockNumber:    ep.Blocknumber,
			DelegateWallet: ep.DelegateWallet,
		},
	}
	return connect.NewResponse(res), nil
}

func (e *EthService) GetServiceProviders(ctx context.Context, _ *connect.Request[v1.GetServiceProvidersRequest]) (*connect.Response[v1.GetServiceProvidersResponse], error) {
	sps, err := e.db.GetServiceProviders(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not get service providers: %w", err))
	}
	psps := make([]*v1.ServiceProvider, 0, len(sps))
	for _, sp := range sps {
		psps = append(psps, &v1.ServiceProvider{
			Wallet:            sp.Address,
			DeployerStake:     sp.DeployerStake,
			DeployerCut:       sp.DeployerCut,
			ValidBounds:       sp.ValidBounds,
			NumberOfEndpoints: sp.NumberOfEndpoints,
			MinAccountStake:   sp.MinAccountStake,
			MaxAccountStake:   sp.MaxAccountStake,
		})
	}
	res := &v1.GetServiceProvidersResponse{
		ServiceProviders: psps,
	}
	return connect.NewResponse(res), nil
}

func (e *EthService) GetLatestFundingRound(ctx context.Context, _ *connect.Request[v1.GetLatestFundingRoundRequest]) (*connect.Response[v1.GetLatestFundingRoundResponse], error) {
	round, err := e.db.GetLatestFundingRound(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not get latest funding round: %w", err)
	}
	res := &v1.GetLatestFundingRoundResponse{
		Round:     int64(round.RoundNum),
		EthBlock:  round.Blocknumber,
		Timestamp: timestamppb.New(round.CreationTime.Time),
	}
	return connect.NewResponse(res), nil
}

func (e *EthService) IsDuplicateDelegateWallet(ctx context.Context, req *connect.Request[v1.IsDuplicateDelegateWalletRequest]) (*connect.Response[v1.IsDuplicateDelegateWalletResponse], error) {
	count, err := e.db.GetCountOfEndpointsWithDelegateWallet(ctx, req.Msg.Wallet)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not get registered endpoints: %w", err))
	}
	res := &v1.IsDuplicateDelegateWalletResponse{IsDuplicate: count > 1}
	return connect.NewResponse(res), nil
}

// For development purposes only
func (e *EthService) RegisterOnEthereum(ctx context.Context, req *connect.Request[v1.RegisterOnEthereumRequest]) (*connect.Response[v1.RegisterOnEthereumResponse], error) {
	ethKey, err := common.EthToEthKey(req.Msg.DelegateKey)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create eth key %v", err))
	}

	chainID, err := e.rpc.ChainID(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not get chain id: %v", err))
	}

	opts, err := bind.NewKeyedTransactorWithChainID(ethKey, chainID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not create keyed transactor: %v", err))
	}

	token, err := e.c.GetAudioTokenContract()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not get token contract: %v", err))
	}

	spf, err := e.c.GetServiceProviderFactoryContract()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not get service provider factory contract: %v", err))
	}

	stakingAddress, err := spf.GetStakingAddress(nil)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not get staking address: %v", err))
	}

	decimals := 18
	stake := new(big.Int).Mul(big.NewInt(200000), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))

	_, err = token.Approve(opts, stakingAddress, stake)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("could not approve tokens: %v", err))
	}

	delegateOwnerWallet := crypto.PubkeyToAddress(ethKey.PublicKey)
	st, err := contracts.StringToServiceType(req.Msg.ServiceType)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("invalid service type: %v", err))
	}

	_, err = spf.Register(opts, st, req.Msg.Endpoint, stake, delegateOwnerWallet)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("couldn't register node: %v", err))
	}

	e.logger.Infof("node %s registered on eth", req.Msg.Endpoint)

	return connect.NewResponse(&v1.RegisterOnEthereumResponse{}), nil
}
