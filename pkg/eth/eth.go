package eth

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync/atomic"
	"time"

	v1 "github.com/AudiusProject/audiusd/pkg/api/eth/v1"
	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/eth/contracts"
	"github.com/AudiusProject/audiusd/pkg/eth/contracts/gen"
	"github.com/AudiusProject/audiusd/pkg/eth/db"
	"github.com/AudiusProject/audiusd/pkg/pubsub"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	DeregistrationTopic = "deregistration-subscriber"
)

type DeregistrationPubsub = pubsub.Pubsub[*v1.ServiceEndpoint]

type EthService struct {
	rpcURL          string
	dbURL           string
	registryAddress string
	env             string

	rpc          *ethclient.Client
	db           *db.Queries
	pool         *pgxpool.Pool
	logger       *zap.Logger
	c            *contracts.AudiusContracts
	deregPubsub  *DeregistrationPubsub
	fundingRound *fundingRoundMetadata

	isReady atomic.Bool
}

type fundingRoundMetadata struct {
	initialized           bool
	fundingAmountPerRound int64
	totalStakedAmount     int64
}

func NewEthService(dbURL, rpcURL, registryAddress string, logger *zap.Logger, environment string) *EthService {
	return &EthService{
		logger:          logger.With(zap.String("service", "eth")),
		rpcURL:          rpcURL,
		dbURL:           dbURL,
		registryAddress: registryAddress,
		env:             environment,
		fundingRound:    &fundingRoundMetadata{},
	}
}

func (eth *EthService) Run(ctx context.Context) error {
	// Init db
	if eth.dbURL == "" {
		return fmt.Errorf("dbUrl environment variable not set")
	}

	if err := db.RunMigrations(eth.logger, eth.dbURL, false); err != nil {
		return fmt.Errorf("error running migrations: %v", err)
	}

	pgConfig, err := pgxpool.ParseConfig(eth.dbURL)
	if err != nil {
		return fmt.Errorf("error parsing database config: %v", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, pgConfig)
	if err != nil {
		return fmt.Errorf("error creating database pool: %v", err)
	}
	eth.pool = pool
	eth.db = db.New(pool)

	// Init pubsub
	eth.deregPubsub = pubsub.NewPubsub[*v1.ServiceEndpoint]()

	// Init eth rpc
	wsRpcUrl := eth.rpcURL
	if strings.HasPrefix(eth.rpcURL, "https") {
		wsRpcUrl = "wss" + strings.TrimPrefix(eth.rpcURL, "https")
	} else if strings.HasPrefix(eth.rpcURL, "http:") { // local devnet
		wsRpcUrl = "ws" + strings.TrimPrefix(eth.rpcURL, "http")
	}
	ethrpc, err := ethclient.Dial(wsRpcUrl)
	if err != nil {
		eth.logger.Error("eth client dial err", zap.Error(err))
		return fmt.Errorf("eth client dial err: %v", err)
	}
	eth.rpc = ethrpc
	defer ethrpc.Close()

	eth.logger.Info("eth service is connected")

	// Init contracts
	eth.logger.Info("creating new audius contracts instance")
	c, err := contracts.NewAudiusContracts(eth.rpc, eth.registryAddress)
	if err != nil {
		eth.logger.Info("failed to make audius contracts")
		return fmt.Errorf("failed to initialize eth contracts: %v", err)
	}
	eth.c = c

	delay := 1 * time.Second
	ticker := time.NewTicker(delay)
	for {
		select {
		case <-ticker.C:
			eth.logger.Info("starting eth data manager")
			if err := eth.startEthDataManager(ctx); err != nil {
				eth.logger.Error("error running eth data manager", zap.Error(err))
				delay *= 2
				if delay > 30*time.Minute {
					return errors.New("eth service shutting down, too many retries")
				}
				eth.logger.Info("retrying eth data manager after delay", zap.Int("delay", int(delay.Seconds())))
				ticker.Reset(delay)
			} else {
				return nil
			}
		case <-ctx.Done():
			eth.logger.Info("eth context canceled")
			return ctx.Err()
		}
	}

	return nil
}

func (eth *EthService) startEthDataManager(ctx context.Context) error {
	// hydrate eth data at startup
	if err := eth.hydrateEthData(ctx); err != nil {
		return fmt.Errorf("error hydrating eth data: %v", err)
	}

	eth.logger.Info("eth service is ready")
	eth.isReady.Store(true)

	// Instantiate the contracts
	serviceProviderFactory, err := eth.c.GetServiceProviderFactoryContract()
	if err != nil {
		eth.logger.Error("eth failed to bind service provider factory contract", zap.Error(err))
		return fmt.Errorf("failed to bind service provider factory contract: %v", err)
	}
	staking, err := eth.c.GetStakingContract()
	if err != nil {
		eth.logger.Error("eth failed to bind staking contract", zap.Error(err))
		return fmt.Errorf("failed to bind staking contract: %v", err)
	}
	governance, err := eth.c.GetGovernanceContract()
	if err != nil {
		eth.logger.Error("eth could not get governance contract", zap.Error(err))
		return fmt.Errorf("eth could not get governance contract: %v", err)
	}

	watchOpts := &bind.WatchOpts{Context: ctx}

	registerChan := make(chan *gen.ServiceProviderFactoryRegisteredServiceProvider)
	deregisterChan := make(chan *gen.ServiceProviderFactoryDeregisteredServiceProvider)
	updateChan := make(chan *gen.ServiceProviderFactoryEndpointUpdated)

	stakedChan := make(chan *gen.StakingStaked)
	unstakedChan := make(chan *gen.StakingUnstaked)
	slashedChan := make(chan *gen.StakingSlashed)

	proposalSubmittedChan := make(chan *gen.GovernanceProposalSubmitted)
	proposalOutcomeChan := make(chan *gen.GovernanceProposalOutcomeEvaluated)

	registerSub, err := serviceProviderFactory.WatchRegisteredServiceProvider(watchOpts, registerChan, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to endpoint registration events: %v", err)
	}
	defer registerSub.Unsubscribe()
	deregisterSub, err := serviceProviderFactory.WatchDeregisteredServiceProvider(watchOpts, deregisterChan, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to endpoint deregistration events: %v", err)
	}
	updateSub, err := serviceProviderFactory.WatchEndpointUpdated(watchOpts, updateChan, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to endpoint update events: %v", err)
	}
	defer updateSub.Unsubscribe()

	stakedSub, err := staking.WatchStaked(watchOpts, stakedChan, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to staking events: %v", err)
	}
	defer stakedSub.Unsubscribe()
	unstakedSub, err := staking.WatchUnstaked(watchOpts, unstakedChan, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to unstaking events: %v", err)
	}
	defer unstakedSub.Unsubscribe()
	slashedSub, err := staking.WatchSlashed(watchOpts, slashedChan, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to slashing events: %v", err)
	}
	defer slashedSub.Unsubscribe()

	proposalSubmittedSub, err := governance.WatchProposalSubmitted(watchOpts, proposalSubmittedChan, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to proposal submission events: %v", err)
	}
	defer proposalSubmittedSub.Unsubscribe()
	proposalOutcomeSub, err := governance.WatchProposalOutcomeEvaluated(watchOpts, proposalOutcomeChan, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to proposal outcome events: %v", err)
	}
	defer proposalOutcomeSub.Unsubscribe()

	// interval to clean refresh all indexed data
	ticker := time.NewTicker(1 * time.Hour)

	for {
		select {
		case err := <-registerSub.Err():
			eth.logger.Error("register event subscription error", zap.Error(err))
			registerSub.Unsubscribe()
			registerSub, err = serviceProviderFactory.WatchRegisteredServiceProvider(watchOpts, registerChan, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to subscribe to endpoint registration events: %v", err)
			}
			defer registerSub.Unsubscribe()
		case err := <-deregisterSub.Err():
			eth.logger.Error("deregister event subscription error", zap.Error(err))
			deregisterSub.Unsubscribe()
			deregisterSub, err = serviceProviderFactory.WatchDeregisteredServiceProvider(watchOpts, deregisterChan, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to subscribe to endpoint deregistration events: %v", err)
			}
			defer deregisterSub.Unsubscribe()
		case err := <-updateSub.Err():
			eth.logger.Error("update event subscription error", zap.Error(err))
			updateSub.Unsubscribe()
			updateSub, err = serviceProviderFactory.WatchEndpointUpdated(watchOpts, updateChan, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to subscribe to endpoint update events: %v", err)
			}
			defer updateSub.Unsubscribe()
		case err := <-stakedSub.Err():
			eth.logger.Error("staked event subscription error", zap.Error(err))
			stakedSub.Unsubscribe()
			stakedSub, err = staking.WatchStaked(watchOpts, stakedChan, nil)
			if err != nil {
				return fmt.Errorf("failed to subscribe to staking events: %v", err)
			}
			defer stakedSub.Unsubscribe()
		case err := <-unstakedSub.Err():
			eth.logger.Error("unstaked event subscription error", zap.Error(err))
			unstakedSub.Unsubscribe()
			unstakedSub, err = staking.WatchUnstaked(watchOpts, unstakedChan, nil)
			if err != nil {
				return fmt.Errorf("failed to subscribe to unstaking events: %v", err)
			}
			defer unstakedSub.Unsubscribe()
		case err := <-slashedSub.Err():
			eth.logger.Error("slashed event subscription error", zap.Error(err))
			slashedSub.Unsubscribe()
			slashedSub, err = staking.WatchSlashed(watchOpts, slashedChan, nil)
			if err != nil {
				return fmt.Errorf("failed to subscribe to slashing events: %v", err)
			}
			defer slashedSub.Unsubscribe()
		case err := <-proposalSubmittedSub.Err():
			eth.logger.Error("proposal submission event subscription error", zap.Error(err))
			proposalSubmittedSub.Unsubscribe()
			proposalSubmittedSub, err = governance.WatchProposalSubmitted(watchOpts, proposalSubmittedChan, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to subscribe to proposal submission events: %v", err)
			}
			defer proposalSubmittedSub.Unsubscribe()
		case err := <-proposalOutcomeSub.Err():
			eth.logger.Error("proposal outcome event subscription error", zap.Error(err))
			proposalOutcomeSub.Unsubscribe()
			proposalOutcomeSub, err = governance.WatchProposalOutcomeEvaluated(watchOpts, proposalOutcomeChan, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to subscribe to proposal outcome events: %v", err)
			}
			defer proposalOutcomeSub.Unsubscribe()

		case reg := <-registerChan:
			if err := eth.addRegisteredEndpoint(ctx, reg.SpID, reg.ServiceType, reg.Endpoint, reg.Owner); err != nil {
				eth.logger.Error("could not handle registration event", zap.Error(err))
				continue
			}
			if err := eth.updateServiceProvider(ctx, reg.Owner); err != nil {
				eth.logger.Error("could not update service provider from registration event", zap.Error(err))
				continue
			}
		case dereg := <-deregisterChan:
			if err := eth.deleteAndDeregisterEndpoint(ctx, dereg.SpID, dereg.ServiceType, dereg.Endpoint, dereg.Owner); err != nil {
				eth.logger.Error("could not handle deregistration event", zap.Error(err))
				continue
			}
			if err := eth.updateServiceProvider(ctx, dereg.Owner); err != nil {
				eth.logger.Error("could not update service provider from deregistration event", zap.Error(err))
				continue
			}
		case update := <-updateChan:
			if err := eth.deleteAndDeregisterEndpoint(ctx, update.SpID, update.ServiceType, update.OldEndpoint, update.Owner); err != nil {
				eth.logger.Error("could not handle deregistration phase of update event", zap.Error(err))
				continue
			}
			if err := eth.addRegisteredEndpoint(ctx, update.SpID, update.ServiceType, update.NewEndpoint, update.Owner); err != nil {
				eth.logger.Error("could not handle registration phase of update event", zap.Error(err))
				continue
			}
			if err := eth.updateServiceProvider(ctx, update.Owner); err != nil {
				eth.logger.Error("could not update service provider from update event", zap.Error(err))
				continue
			}
		case staked := <-stakedChan:
			if err := eth.updateStakedAmountForServiceProvider(ctx, staked.User, staked.Total); err != nil {
				eth.logger.Error("could not update service staked amount from staked event", zap.Error(err))
				continue
			}
		case unstaked := <-unstakedChan:
			if err := eth.updateStakedAmountForServiceProvider(ctx, unstaked.User, unstaked.Total); err != nil {
				eth.logger.Error("could not update service staked amount from unstaked event", zap.Error(err))
				continue
			}
		case slashed := <-slashedChan:
			if err := eth.updateStakedAmountForServiceProvider(ctx, slashed.User, slashed.Total); err != nil {
				eth.logger.Error("could not update service staked amount from slashed event", zap.Error(err))
				continue
			}
		case submission := <-proposalSubmittedChan:
			if err := eth.addActiveProposal(ctx, governance, submission.ProposalId); err != nil {
				eth.logger.Error("could not add new proposal", zap.Int64("proposal_id", submission.ProposalId.Int64()), zap.Error(err))
				continue
			}
		case outcome := <-proposalOutcomeChan:
			if err := eth.db.DeleteActiveProposal(ctx, outcome.ProposalId.Int64()); err != nil {
				eth.logger.Error("could not remove proposal", zap.Int64("proposal_id", outcome.ProposalId.Int64()), zap.Error(err))
				continue
			}
		case <-ticker.C:
			if err := eth.hydrateEthData(ctx); err != nil {
				// crash if periodic updates fail - it may be necessary to reestablish connections
				return fmt.Errorf("error gathering eth endpoints: %v", err)
			}
		case <-ctx.Done():
			eth.logger.Info("eth context canceled")
			return ctx.Err()
		}
	}
}

func (eth *EthService) SubscribeToDeregistrationEvents() chan *v1.ServiceEndpoint {
	return eth.deregPubsub.Subscribe(DeregistrationTopic, 10)
}

func (eth *EthService) UnsubscribeFromDeregistrationEvents(ch chan *v1.ServiceEndpoint) {
	eth.deregPubsub.Unsubscribe(DeregistrationTopic, ch)
}

func (eth *EthService) deleteAndDeregisterEndpoint(ctx context.Context, spID *big.Int, serviceType [32]byte, endpoint string, owner ethcommon.Address) error {
	st, err := contracts.ServiceTypeToString(serviceType)
	if err != nil {
		return err
	}
	ep, err := eth.db.GetRegisteredEndpoint(ctx, endpoint)
	if err != nil {
		eth.logger.Error("eth could not fetch endpoint from db", zap.String("endpoint", endpoint), zap.Error(err))
		return fmt.Errorf("could not fetch endpoint %s from db: %v", endpoint, err)
	}
	if err := eth.db.DeleteRegisteredEndpoint(
		ctx,
		db.DeleteRegisteredEndpointParams{
			ID:          int32(spID.Int64()),
			Endpoint:    endpoint,
			Owner:       owner.Hex(),
			ServiceType: st,
		},
	); err != nil {
		eth.logger.Error("eth could not delete registered endpoint", zap.Error(err))
		return err
	}
	eth.deregPubsub.Publish(
		ctx,
		DeregistrationTopic,
		&v1.ServiceEndpoint{
			Id:             spID.Int64(),
			ServiceType:    st,
			RegisteredAt:   timestamppb.New(ep.RegisteredAt.Time),
			Owner:          owner.Hex(),
			Endpoint:       endpoint,
			DelegateWallet: ep.DelegateWallet,
		},
	)
	return nil
}

func (eth *EthService) updateStakedAmountForServiceProvider(ctx context.Context, address ethcommon.Address, totalStaked *big.Int) error {
	if err := eth.db.UpsertStaked(
		ctx,
		db.UpsertStakedParams{Address: address.Hex(), TotalStaked: contracts.WeiToAudio(totalStaked).Int64()},
	); err != nil {
		eth.logger.Error("eth could not update service staked amount", zap.Error(err))
		return fmt.Errorf("could not update service staked amount: %v", err)
	}
	if err := eth.updateTotalStakedAmount(ctx); err != nil {
		eth.logger.Error("eth could not update total staked amount", zap.Error(err))
		return fmt.Errorf("cound not update total staked amount: %v", err)
	}
	return nil
}

func (eth *EthService) updateTotalStakedAmount(ctx context.Context) error {
	staking, err := eth.c.GetStakingContract()
	if err != nil {
		eth.logger.Error("eth failed to bind staking contract", zap.Error(err))
		return fmt.Errorf("failed to bind staking contract: %v", err)
	}
	opts := &bind.CallOpts{Context: ctx}
	totalStaked, err := staking.TotalStaked(opts)
	if err != nil {
		eth.logger.Error("eth could not get total staked across all delegators", zap.Error(err))
		return fmt.Errorf("could not get total staked across all delegators: %v", err)
	}
	eth.fundingRound.totalStakedAmount = contracts.WeiToAudio(totalStaked).Int64()
	return nil
}

func (eth *EthService) addRegisteredEndpoint(ctx context.Context, spID *big.Int, serviceType [32]byte, endpoint string, owner ethcommon.Address) error {
	st, err := contracts.ServiceTypeToString(serviceType)
	if err != nil {
		return err
	}
	node, err := eth.c.GetRegisteredNode(ctx, spID, serviceType)
	if err != nil {
		eth.logger.Error("eth could not get registered node", zap.Error(err))
		return err
	}

	// Grab timestamp from block when this endpoint was registered
	registeredBlock, err := eth.rpc.HeaderByNumber(ctx, node.BlockNumber)
	if err != nil {
		eth.logger.Error("eth failed to get block to check registration date", zap.Error(err))
		return fmt.Errorf("failed to get block to check registration date: %v", err)
	}
	registrationTimestamp := time.Unix(int64(registeredBlock.Time), 0)

	return eth.db.InsertRegisteredEndpoint(
		ctx,
		db.InsertRegisteredEndpointParams{
			ID:             int32(spID.Int64()),
			ServiceType:    st,
			Owner:          owner.Hex(),
			DelegateWallet: node.DelegateOwnerWallet.Hex(),
			Endpoint:       endpoint,
			Blocknumber:    node.BlockNumber.Int64(),
			RegisteredAt: pgtype.Timestamp{
				Time:  registrationTimestamp,
				Valid: true,
			},
		},
	)
}

func (eth *EthService) updateServiceProvider(ctx context.Context, serviceProviderAddress ethcommon.Address) error {
	serviceProviderFactory, err := eth.c.GetServiceProviderFactoryContract()
	if err != nil {
		eth.logger.Error("eth failed to bind service provider factory contract while updating service provider", zap.Error(err))
		return fmt.Errorf("failed to bind service provider factory contract while updating service provider: %v", err)
	}
	opts := &bind.CallOpts{Context: ctx}

	spDetails, err := serviceProviderFactory.GetServiceProviderDetails(opts, serviceProviderAddress)
	if err != nil {
		eth.logger.Error("eth failed to get service provider details for address", zap.String("address", serviceProviderAddress.Hex()), zap.Error(err))
		return fmt.Errorf("failed get service provider details for address %s: %v", serviceProviderAddress.Hex(), err)
	}
	if err := eth.db.UpsertServiceProvider(
		ctx,
		db.UpsertServiceProviderParams{
			Address:           serviceProviderAddress.Hex(),
			DeployerStake:     spDetails.DeployerStake.Int64(),
			DeployerCut:       spDetails.DeployerCut.Int64(),
			ValidBounds:       spDetails.ValidBounds,
			NumberOfEndpoints: int32(spDetails.NumberOfEndpoints.Int64()),
			MinAccountStake:   spDetails.MinAccountStake.Int64(),
			MaxAccountStake:   spDetails.MaxAccountStake.Int64(),
		},
	); err != nil {
		eth.logger.Error("eth could not upsert service provider into eth service db", zap.Error(err))
		return fmt.Errorf("could not upsert service provider into eth service db: %v", err)
	}
	return nil
}

func (eth *EthService) hydrateEthData(ctx context.Context) error {
	eth.logger.Info("refreshing eth data")

	// refresh proposals asynchronously, ignoring failures until data becomes mission critical
	go func() {
		if err := eth.refreshInProgressProposals(ctx); err != nil {
			eth.logger.Error("eth failed to refresh in progress proposals", zap.Error(err))
		}
	}()

	nodes, err := eth.c.GetAllRegisteredNodes(ctx)
	if err != nil {
		eth.logger.Error("eth could not get registered nodes", zap.Error(err))
		return fmt.Errorf("could not get registered nodes from contracts: %w", err)
	}

	tx, err := eth.pool.Begin(ctx)
	if err != nil {
		eth.logger.Error("eth could not begin db tx", zap.Error(err))
		return fmt.Errorf("could not begin db tx: %w", err)
	}
	defer tx.Rollback(context.Background())

	txq := eth.db.WithTx(tx)

	if err := txq.ClearRegisteredEndpoints(ctx); err != nil {
		eth.logger.Error("eth could not clear registered endpoints", zap.Error(err))
		return fmt.Errorf("could not clear registered endpoints: %w", err)
	}

	if err := txq.ClearRegisteredEndpoints(ctx); err != nil {
		return fmt.Errorf("could not clear registered endpoints: %w", err)
	}
	if err := txq.ClearServiceProviders(ctx); err != nil {
		eth.logger.Error("eth could not clear service providers", zap.Error(err))
		return fmt.Errorf("could not clear service providers: %w", err)
	}

	allServiceProviders := make(map[string]*db.EthServiceProvider, len(nodes))
	serviceProviderFactory, err := eth.c.GetServiceProviderFactoryContract()
	if err != nil {
		eth.logger.Error("eth failed to bind service provider factory contract", zap.Error(err))
		return fmt.Errorf("failed to bind service provider factory contract: %v", err)
	}
	staking, err := eth.c.GetStakingContract()
	if err != nil {
		eth.logger.Error("eth failed to bind staking contract", zap.Error(err))
		return fmt.Errorf("failed to bind staking contract: %v", err)
	}
	claimsManager, err := eth.c.GetClaimsManagerContract()
	if err != nil {
		eth.logger.Error("eth failed to bind claims manager contract", zap.Error(err))
		return fmt.Errorf("failed to bind claims manager contract: %v", err)
	}
	opts := &bind.CallOpts{Context: ctx}

	for _, node := range nodes {
		st, err := contracts.ServiceTypeToString(node.Type)
		if err != nil {
			eth.logger.Error("eth could not resolve service type for node", zap.Error(err))
			return fmt.Errorf("could resolve service type for node: %v", err)
		}

		// Grab timestamp from block when this endpoint was registered
		registeredBlock, err := eth.rpc.HeaderByNumber(ctx, node.BlockNumber)
		if err != nil {
			eth.logger.Error("eth failed to get block to check registration date", zap.Error(err))
			return fmt.Errorf("failed to get block to check registration date: %v", err)
		}
		registrationTimestamp := time.Unix(int64(registeredBlock.Time), 0)

		if err := txq.InsertRegisteredEndpoint(
			ctx,
			db.InsertRegisteredEndpointParams{
				ID:             int32(node.Id.Int64()),
				ServiceType:    st,
				Owner:          node.Owner.Hex(),
				DelegateWallet: node.DelegateOwnerWallet.Hex(),
				Endpoint:       node.Endpoint,
				Blocknumber:    node.BlockNumber.Int64(),
				RegisteredAt: pgtype.Timestamp{
					Time:  registrationTimestamp,
					Valid: true,
				},
			},
		); err != nil {
			eth.logger.Error("eth could not insert registered endpoint into eth indexer db", zap.Error(err))
			return fmt.Errorf("could not insert registered endpoint into eth indexer db: %v", err)
		}

		if _, ok := allServiceProviders[node.Owner.Hex()]; !ok {
			spDetails, err := serviceProviderFactory.GetServiceProviderDetails(opts, node.Owner)
			if err != nil {
				eth.logger.Error("eth failed to get service provider details", zap.String("address", node.Owner.Hex()), zap.Error(err))
				return fmt.Errorf("failed get service provider details for address %s: %v", node.Owner.Hex(), err)
			}
			allServiceProviders[node.Owner.Hex()] = &db.EthServiceProvider{
				Address:           node.Owner.Hex(),
				DeployerStake:     spDetails.DeployerStake.Int64(),
				DeployerCut:       spDetails.DeployerCut.Int64(),
				ValidBounds:       spDetails.ValidBounds,
				NumberOfEndpoints: int32(spDetails.NumberOfEndpoints.Int64()),
				MinAccountStake:   spDetails.MinAccountStake.Int64(),
				MaxAccountStake:   spDetails.MaxAccountStake.Int64(),
			}
		}
	}

	for _, sp := range allServiceProviders {
		if err := txq.InsertServiceProvider(
			ctx,
			db.InsertServiceProviderParams{
				Address:           sp.Address,
				DeployerStake:     sp.DeployerStake,
				DeployerCut:       sp.DeployerCut,
				ValidBounds:       sp.ValidBounds,
				NumberOfEndpoints: sp.NumberOfEndpoints,
				MinAccountStake:   sp.MinAccountStake,
				MaxAccountStake:   sp.MaxAccountStake,
			},
		); err != nil {
			eth.logger.Error("eth could not insert service provider into eth indexer db", zap.Error(err))
			return fmt.Errorf("could not insert service provider into eth indexer db: %v", err)
		}

		totalStakedForSp, err := staking.TotalStakedFor(opts, ethcommon.HexToAddress(sp.Address))
		if err != nil {
			eth.logger.Error("eth could not get total staked amount for address", zap.String("address", sp.Address), zap.Error(err))
			return fmt.Errorf("could not get total staked amount for address %s: %v", sp.Address, err)
		}
		if err = txq.UpsertStaked(
			ctx,
			db.UpsertStakedParams{
				Address:     sp.Address,
				TotalStaked: contracts.WeiToAudio(totalStakedForSp).Int64(),
			},
		); err != nil {
			eth.logger.Error("eth could not insert staked amount into eth indexer db", zap.Error(err))
			return fmt.Errorf("could not insert staked amount into eth indexer db: %v", err)
		}
	}

	if err := eth.updateTotalStakedAmount(ctx); err != nil {
		eth.logger.Error("eth could not update total staked amount", zap.Error(err))
		return fmt.Errorf("could not update total staked amount: %v", err)
	}

	fundingAmountPerRound, err := claimsManager.GetFundsPerRound(opts)
	if err != nil {
		eth.logger.Error("eth could not get funding amount per round", zap.Error(err))
		return fmt.Errorf("could not get funding amount per round: %v", err)
	}

	eth.fundingRound.fundingAmountPerRound = contracts.WeiToAudio(fundingAmountPerRound).Int64()
	eth.fundingRound.initialized = true

	return tx.Commit(ctx)
}

func (eth *EthService) refreshInProgressProposals(ctx context.Context) error {
	eth.db.ClearActiveProposals(ctx)
	governance, err := eth.c.GetGovernanceContract()
	if err != nil {
		eth.logger.Error("eth could not get governance contract", zap.Error(err))
		return fmt.Errorf("eth could not get governance contract: %v", err)
	}
	opts := &bind.CallOpts{Context: ctx}
	proposalIds, err := governance.GetInProgressProposals(opts)
	if err != nil {
		eth.logger.Error("eth could not get in progress proposals", zap.Error(err))
		return fmt.Errorf("eth could not get in progress proposals: %v", err)
	}

	for _, id := range proposalIds {
		if err := eth.addActiveProposal(ctx, governance, id); err != nil {
			eth.logger.Error("could not add active proposal", zap.Int64("proposal_id", id.Int64()), zap.Error(err))
		}
	}

	return nil
}

func (eth *EthService) addActiveProposal(ctx context.Context, governance *gen.Governance, proposalId *big.Int) error {
	opts := &bind.CallOpts{Context: ctx}
	proposal, err := governance.GetProposalById(opts, proposalId)
	if err != nil {
		return fmt.Errorf("eth could not get proposal by id: %v", err)
	}
	return eth.db.InsertActiveProposal(
		ctx,
		db.InsertActiveProposalParams{
			ID:                        proposal.ProposalId.Int64(),
			Proposer:                  proposal.Proposer.Hex(),
			SubmissionBlockNumber:     proposal.SubmissionBlockNumber.Int64(),
			TargetContractRegistryKey: common.HexToUtf8(proposal.TargetContractRegistryKey),
			TargetContractAddress:     proposal.TargetContractAddress.Hex(),
			CallValue:                 proposal.CallValue.Int64(),
			FunctionSignature:         proposal.FunctionSignature,
			CallData:                  hex.EncodeToString(proposal.CallData),
		},
	)
}

// Get an active slash proposal against a given address. Returns nil if there are none.
// In the case of multiple active slash proposals, returns the proposal with the highest slash amount
func (eth *EthService) getSlashProposalForAddress(ctx context.Context, address string) (*db.EthActiveProposal, error) {
	var foundProposal *db.EthActiveProposal
	var foundProposalAmount *big.Int

	activeProposals, err := eth.db.GetActiveProposals(ctx)
	if err != nil {
		eth.logger.Error("could not get active proposals from db", zap.Error(err))
		return foundProposal, fmt.Errorf("could not get slash proposals from db: %v", err)
	}

	// ensure delegate manager contract address is initialized
	if _, err := eth.c.GetDelegateManagerContract(); err != nil {
		eth.logger.Error("eth failed to bind delegate manager contract", zap.Error(err))
		return foundProposal, fmt.Errorf("failed to bind delegate manager contract: %v", err)
	}

	for _, prop := range activeProposals {
		// Ignore proposals that aren't slash method from staking contract
		if !strings.HasPrefix(prop.FunctionSignature, "slash(") || prop.TargetContractAddress != eth.c.DelegateManagerAddress.String() {
			continue
		}

		slashAddr, slashAmount, err := DecodeSlashProposalArguments(prop.CallData)
		if err != nil {
			eth.logger.Error("failed to decode arguments from proposal call data", zap.String("call_data", prop.CallData), zap.Error(err))
			continue
		}

		// Ignore proposals not affecting target address
		if slashAddr.String() != address {
			continue
		}

		// Found a matching active proposal that slashes the target address
		if foundProposal == nil {
			foundProposal = &prop
			foundProposalAmount = slashAmount
			continue
		}

		// If more than one slash proposal exists for this address,
		// select the proposal with the highest slash amount
		if foundProposalAmount.Cmp(slashAmount) == -1 {
			foundProposal = &prop
			foundProposalAmount = slashAmount
			continue
		}
	}

	return foundProposal, nil
}

func DecodeSlashProposalArguments(callData string) (address ethcommon.Address, amount *big.Int, err error) {
	// Get bound ABI
	parsedABI, err := gen.DelegateManagerMetaData.GetAbi()
	if err != nil {
		return address, amount, fmt.Errorf("failed to parse staking contract abi: %v", err)
	}

	// Get bound method
	data := ethcommon.FromHex(callData)
	method, ok := parsedABI.Methods["slash"]
	if !ok {
		return address, amount, errors.New("failed to get slash method from DelegateManager contract")
	}

	// Decode arguments
	args := map[string]interface{}{}
	err = method.Inputs.UnpackIntoMap(args, data)
	if err != nil {
		return address, amount, fmt.Errorf("failed unpack arguments: %v", err)
	}

	// Extract account address and slash amount from unpacked values
	slashAddrRaw, ok := args["_slashAddress"]
	if !ok {
		return address, amount, fmt.Errorf("failed get slash address from unpacked call data: %v", err)
	}
	address, ok = slashAddrRaw.(ethcommon.Address)
	if !ok {
		return address, amount, fmt.Errorf("incompatible type for slash address from unpacked call data: %v", err)
	}

	slashAmountRaw, ok := args["_amount"]
	if !ok {
		return address, amount, fmt.Errorf("failed get slash amount from unpacked call data: %v", err)
	}
	amount, ok = slashAmountRaw.(*big.Int)
	if !ok {
		return address, amount, fmt.Errorf("incompatible type for slash amount from unpacked call data: %v", err)
	}

	return address, amount, nil
}
