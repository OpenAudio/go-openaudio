package eth

import (
	"context"
	"errors"
	"math/big"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/eth/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/eth/contracts"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ v1connect.EthServiceHandler = (*EthService)(nil)

func (e *EthService) GetStatus(context.Context, *connect.Request[v1.GetStatusRequest]) (*connect.Response[v1.GetStatusResponse], error) {
	res := &v1.GetStatusResponse{}
	if e.isReady.Load() {
		res.Ready = true
	}
	return connect.NewResponse(res), nil
}

func (e *EthService) GetRegisteredEndpoints(ctx context.Context, _ *connect.Request[v1.GetRegisteredEndpointsRequest]) (*connect.Response[v1.GetRegisteredEndpointsResponse], error) {
	eps, err := e.db.GetRegisteredEndpoints(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		e.logger.Debug("could not get registered endpoints", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not get registered endpoints"))
	}
	peps := make([]*v1.ServiceEndpoint, 0, len(eps))
	for _, ep := range eps {
		peps = append(peps, &v1.ServiceEndpoint{
			Id:             int64(ep.ID),
			RegisteredAt:   timestamppb.New(ep.RegisteredAt.Time),
			ServiceType:    ep.ServiceType,
			BlockNumber:    ep.Blocknumber,
			Owner:          ep.Owner,
			Endpoint:       ep.Endpoint,
			DelegateWallet: ep.DelegateWallet,
		})
	}
	res := &v1.GetRegisteredEndpointsResponse{Endpoints: peps}
	return connect.NewResponse(res), nil
}

func (e *EthService) GetRegisteredEndpointsForServiceProvider(ctx context.Context, req *connect.Request[v1.GetRegisteredEndpointsForServiceProviderRequest]) (*connect.Response[v1.GetRegisteredEndpointsForServiceProviderResponse], error) {
	eps, err := e.db.GetRegisteredEndpointsForServiceProvider(ctx, req.Msg.Owner)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		e.logger.Debug("could not get registered endpoints for service provider", zap.String("service_provider", req.Msg.Owner), zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not get registered endpoints for service provider"))
	}
	peps := make([]*v1.ServiceEndpoint, 0, len(eps))
	for _, ep := range eps {
		peps = append(peps, &v1.ServiceEndpoint{
			Id:             int64(ep.ID),
			RegisteredAt:   timestamppb.New(ep.RegisteredAt.Time),
			ServiceType:    ep.ServiceType,
			BlockNumber:    ep.Blocknumber,
			Owner:          ep.Owner,
			Endpoint:       ep.Endpoint,
			DelegateWallet: ep.DelegateWallet,
		})
	}
	res := &v1.GetRegisteredEndpointsForServiceProviderResponse{Endpoints: peps}
	return connect.NewResponse(res), nil
}

func (e *EthService) GetRegisteredEndpointInfo(ctx context.Context, req *connect.Request[v1.GetRegisteredEndpointInfoRequest]) (*connect.Response[v1.GetRegisteredEndpointInfoResponse], error) {
	ep, err := e.db.GetRegisteredEndpoint(ctx, req.Msg.Endpoint)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		e.logger.Debug("could not get registered endpoint", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not get registered endpoint"))
	} else if errors.Is(err, pgx.ErrNoRows) {
		e.logger.Debug("registered endpoint not found", zap.Error(err))
		return nil, connect.NewError(connect.CodeNotFound, errors.New("could not get registered endpoint"))
	}
	res := &v1.GetRegisteredEndpointInfoResponse{
		Se: &v1.ServiceEndpoint{
			Id:             int64(ep.ID),
			ServiceType:    ep.ServiceType,
			Owner:          ep.Owner,
			Endpoint:       ep.Endpoint,
			BlockNumber:    ep.Blocknumber,
			DelegateWallet: ep.DelegateWallet,
			RegisteredAt:   timestamppb.New(ep.RegisteredAt.Time),
		},
	}
	return connect.NewResponse(res), nil
}

func (e *EthService) GetServiceProvider(ctx context.Context, req *connect.Request[v1.GetServiceProviderRequest]) (*connect.Response[v1.GetServiceProviderResponse], error) {
	serviceProvider, err := e.db.GetServiceProvider(ctx, req.Msg.Address)
	if err != nil {
		e.logger.Debug("could not get service provider", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not get service provider"))
	}

	res := &v1.GetServiceProviderResponse{
		ServiceProvider: &v1.ServiceProvider{
			Wallet:            serviceProvider.Address,
			DeployerStake:     serviceProvider.DeployerStake,
			DeployerCut:       serviceProvider.DeployerCut,
			ValidBounds:       serviceProvider.ValidBounds,
			NumberOfEndpoints: serviceProvider.NumberOfEndpoints,
			MinAccountStake:   serviceProvider.MinAccountStake,
			MaxAccountStake:   serviceProvider.MaxAccountStake,
		},
	}
	return connect.NewResponse(res), nil
}

func (e *EthService) GetServiceProviders(ctx context.Context, _ *connect.Request[v1.GetServiceProvidersRequest]) (*connect.Response[v1.GetServiceProvidersResponse], error) {
	sps, err := e.db.GetServiceProviders(ctx)
	if err != nil {
		e.logger.Debug("could not get service providers", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not get service providers"))
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
		e.logger.Debug("could not get latest funding round", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not get latest funding round"))
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
		e.logger.Debug("could not check for duplicate wallet", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not check for duplicate wallet"))
	}
	res := &v1.IsDuplicateDelegateWalletResponse{IsDuplicate: count > 1}
	return connect.NewResponse(res), nil
}

// For development purposes only
func (e *EthService) Register(ctx context.Context, req *connect.Request[v1.RegisterRequest]) (*connect.Response[v1.RegisterResponse], error) {
	if e.env != "dev" && e.env != "test" {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))
	}

	ethKey, err := common.EthToEthKey(req.Msg.DelegateKey)
	if err != nil {
		e.logger.Debug("failed to create eth key", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not register endpoint"))
	}

	chainID, err := e.rpc.ChainID(ctx)
	if err != nil {
		e.logger.Debug("could not get chain id", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not register endpoint"))
	}

	opts, err := bind.NewKeyedTransactorWithChainID(ethKey, chainID)
	if err != nil {
		e.logger.Debug("could not create keyed transactor", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not register endpoint"))
	}

	token, err := e.c.GetAudioTokenContract()
	if err != nil {
		e.logger.Debug("could not get token contract", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not register endpoint"))
	}

	spf, err := e.c.GetServiceProviderFactoryContract()
	if err != nil {
		e.logger.Debug("could not get service provider factory contract", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not register endpoint"))
	}

	stakingAddress, err := spf.GetStakingAddress(nil)
	if err != nil {
		e.logger.Debug("could not get staking address", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not register endpoint"))
	}

	decimals := 18
	stake := new(big.Int).Mul(big.NewInt(200000), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))

	_, err = token.Approve(opts, stakingAddress, stake)
	if err != nil {
		e.logger.Debug("could not approve tokens", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not register endpoint"))
	}

	delegateOwnerWallet := crypto.PubkeyToAddress(ethKey.PublicKey)
	st, err := contracts.StringToServiceType(req.Msg.ServiceType)
	if err != nil {
		e.logger.Debug("invalid service type", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not register endpoint"))
	}

	_, err = spf.Register(opts, st, req.Msg.Endpoint, stake, delegateOwnerWallet)
	if err != nil {
		e.logger.Debug("couldn't register node", zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not register endpoint"))
	}

	e.logger.Info("node registered on eth", zap.String("endpoint", req.Msg.Endpoint))

	return connect.NewResponse(&v1.RegisterResponse{}), nil
}

func (e *EthService) Subscribe(ctx context.Context, req *connect.Request[v1.SubscriptionRequest], stream *connect.ServerStream[v1.SubscriptionResponse]) error {
	e.logger.Info("subscription started")

	deregCh := e.deregPubsub.Subscribe(DeregistrationTopic, 10)

	for {
		select {
		case <-ctx.Done():
			// Cleanup subscriptions when context is cancelled
			if deregCh != nil {
				e.deregPubsub.Unsubscribe(DeregistrationTopic, deregCh)
				e.logger.Info("unsubscribed from deregistration events")
			}
			e.logger.Debug("subscription canceled by context", zap.Error(ctx.Err()))
			return connect.NewError(connect.CodeInternal, errors.New("subscription canceled"))

		case dereg := <-deregCh:
			if dereg != nil {
				deregCopy := *dereg
				err := stream.Send(&v1.SubscriptionResponse{
					Event: &v1.SubscriptionResponse_Deregistration{
						Deregistration: &v1.SubscriptionResponse_DeregistrationEvent{
							ServiceEndpoint: &deregCopy,
						},
					},
				})
				if err != nil {
					e.logger.Error("failed to send deregistration event", zap.Error(err))
				}
			}
		}
	}
}

func (e *EthService) GetStakingMetadataForServiceProvider(ctx context.Context, req *connect.Request[v1.GetStakingMetadataForServiceProviderRequest]) (*connect.Response[v1.GetStakingMetadataForServiceProviderResponse], error) {
	staked, err := e.db.GetStakedAmountForServiceProvider(ctx, req.Msg.Address)
	if err != nil {
		e.logger.Debug("could not get staked amount for service provider at address", zap.String("address", req.Msg.Address), zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("Could not get staking metadata"))
	}
	rewardsPerRound := int64(float64(staked) * float64(e.fundingRound.fundingAmountPerRound) / float64(e.fundingRound.totalStakedAmount))
	return connect.NewResponse(
		&v1.GetStakingMetadataForServiceProviderResponse{
			TotalStaked:     e.fundingRound.totalStakedAmount,
			RewardsPerRound: rewardsPerRound,
		},
	), nil
}

func (e *EthService) GetActiveSlashProposalForAddress(ctx context.Context, req *connect.Request[v1.GetActiveSlashProposalForAddressRequest]) (*connect.Response[v1.GetActiveSlashProposalForAddressResponse], error) {
	proposal, err := e.getSlashProposalForAddress(ctx, req.Msg.Address)
	if err != nil {
		e.logger.Debug("could not get active slash proposal for address", zap.String("address", req.Msg.Address), zap.Error(err))
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not get active slash proposal"))
	} else if proposal == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no active slash proposal for address"))
	}
	return connect.NewResponse(
		&v1.GetActiveSlashProposalForAddressResponse{
			ProposalId:                proposal.ID,
			Proposer:                  proposal.Proposer,
			SubmissionBlockNumber:     proposal.SubmissionBlockNumber,
			TargetContractRegistryKey: proposal.TargetContractRegistryKey,
			TargetContractAddress:     proposal.TargetContractAddress,
			CallValue:                 proposal.CallValue,
			FunctionSignature:         proposal.FunctionSignature,
			CallData:                  proposal.CallData,
		},
	), nil
}
