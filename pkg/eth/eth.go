package eth

import (
	"context"
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

var audConversion = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

type DeregistrationPubsub = pubsub.Pubsub[*v1.ServiceEndpoint]

type EthService struct {
	rpcURL          string
	dbURL           string
	registryAddress string
	env             string

	rpc          *ethclient.Client
	db           *db.Queries
	pool         *pgxpool.Pool
	logger       *common.Logger
	z            *zap.Logger
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

func NewEthService(dbURL, rpcURL, registryAddress string, logger *common.Logger, environment string) *EthService {
	return &EthService{
		logger:          logger.Child("eth"),
		rpcURL:          rpcURL,
		dbURL:           dbURL,
		z:               zap.NewNop(),
		registryAddress: registryAddress,
		env:             environment,
		fundingRound:    &fundingRoundMetadata{},
	}
}

func (eth *EthService) SetZapLogger(z *zap.Logger) {
	eth.z = z
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
		eth.z.Error("eth client dial err", zap.Error(err))
		return fmt.Errorf("eth client dial err: %v", err)
	}
	eth.rpc = ethrpc
	defer ethrpc.Close()

	eth.z.Info("eth service is connected")

	// Init contracts
	eth.z.Info("creating new audius contracts instance")
	c, err := contracts.NewAudiusContracts(eth.rpc, eth.registryAddress)
	if err != nil {
		eth.z.Info("failed to make audius contracts")
		return fmt.Errorf("failed to initialize eth contracts: %v", err)
	}
	eth.c = c

	eth.z.Info("starting eth data manager")

	if err := eth.startEthDataManager(ctx); err != nil {
		eth.z.Error("error starting eth data manager", zap.Error(err))
		return fmt.Errorf("error running endpoint manager: %w", err)
	}

	return nil
}

func (eth *EthService) startEthDataManager(ctx context.Context) error {
	// hydrate eth data at startup
	delay := 2 * time.Second
	ticker := time.NewTicker(delay)
initial:
	for {
		select {
		case <-ticker.C:
			if err := eth.hydrateEthData(ctx); err != nil {
				eth.z.Error("error gathering registered eth endpoints", zap.Error(err))
				eth.logger.Errorf("error gathering registered eth endpoints: %v", err)
				delay *= 2
				eth.z.Error("retrying", zap.Int("delay", int(delay)))
				eth.logger.Infof("retrying in %s seconds", delay)
				ticker.Reset(delay)
			} else {
				break initial
			}
		case <-ctx.Done():
			eth.z.Info("eth context canceled")
			return errors.New("context canceled")
		}
	}

	eth.logger.Info("eth service is ready")
	eth.z.Info("eth service is ready")
	eth.isReady.Store(true)

	// Instantiate the contracts
	serviceProviderFactory, err := eth.c.GetServiceProviderFactoryContract()
	if err != nil {
		eth.z.Error("eth failed to bind service provider factory contract", zap.Error(err))
		return fmt.Errorf("failed to bind service provider factory contract: %v", err)
	}
	staking, err := eth.c.GetStakingContract()
	if err != nil {
		eth.z.Error("eth failed to bind staking contract", zap.Error(err))
		return fmt.Errorf("failed to bind staking contract: %v", err)
	}

	watchOpts := &bind.WatchOpts{Context: ctx}

	registerChan := make(chan *gen.ServiceProviderFactoryRegisteredServiceProvider)
	deregisterChan := make(chan *gen.ServiceProviderFactoryDeregisteredServiceProvider)
	updateChan := make(chan *gen.ServiceProviderFactoryEndpointUpdated)

	stakedChan := make(chan *gen.StakingStaked)
	unstakedChan := make(chan *gen.StakingUnstaked)
	slashedChan := make(chan *gen.StakingSlashed)

	registerSub, err := serviceProviderFactory.WatchRegisteredServiceProvider(watchOpts, registerChan, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to endpoint registration events: %v", err)
	}
	deregisterSub, err := serviceProviderFactory.WatchDeregisteredServiceProvider(watchOpts, deregisterChan, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to endpoint deregistration events: %v", err)
	}
	updateSub, err := serviceProviderFactory.WatchEndpointUpdated(watchOpts, updateChan, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to endpoint update events: %v", err)
	}

	stakedSub, err := staking.WatchStaked(watchOpts, stakedChan, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to staking events: %v", err)
	}
	unstakedSub, err := staking.WatchUnstaked(watchOpts, unstakedChan, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to unstaking events: %v", err)
	}
	slashedSub, err := staking.WatchSlashed(watchOpts, slashedChan, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to slashing events: %v", err)
	}

	// interval to clean refresh all indexed data
	ticker = time.NewTicker(1 * time.Hour)

	for {
		select {
		case err := <-registerSub.Err():
			return fmt.Errorf("register event subscription error: %v", err)
		case err := <-deregisterSub.Err():
			return fmt.Errorf("deregister event subscription error: %v", err)
		case err := <-updateSub.Err():
			return fmt.Errorf("update event subscription error: %v", err)
		case err := <-stakedSub.Err():
			return fmt.Errorf("staked event subscription error: %v", err)
		case err := <-unstakedSub.Err():
			return fmt.Errorf("unstaked event subscription error: %v", err)
		case err := <-slashedSub.Err():
			return fmt.Errorf("slashed event subscription error: %v", err)
		case reg := <-registerChan:
			if err := eth.addRegisteredEndpoint(ctx, reg.SpID, reg.ServiceType, reg.Endpoint, reg.Owner); err != nil {
				eth.logger.Error("could not handle registration event: %v", err)
				continue
			}
			if err := eth.updateServiceProvider(ctx, reg.Owner); err != nil {
				eth.logger.Error("could not update service provider from registration event: %v", err)
				continue
			}
		case dereg := <-deregisterChan:
			if err := eth.deleteAndDeregisterEndpoint(ctx, dereg.SpID, dereg.ServiceType, dereg.Endpoint, dereg.Owner); err != nil {
				eth.logger.Error("could not handle deregistration event: %v", err)
				continue
			}
			if err := eth.updateServiceProvider(ctx, dereg.Owner); err != nil {
				eth.logger.Error("could not update service provider from deregistration event: %v", err)
				continue
			}
		case update := <-updateChan:
			if err := eth.deleteAndDeregisterEndpoint(ctx, update.SpID, update.ServiceType, update.OldEndpoint, update.Owner); err != nil {
				eth.logger.Error("could not handle deregistration phase of update event: %v", err)
				continue
			}
			if err := eth.addRegisteredEndpoint(ctx, update.SpID, update.ServiceType, update.NewEndpoint, update.Owner); err != nil {
				eth.logger.Error("could not handle registration phase of update event: %v", err)
				continue
			}
			if err := eth.updateServiceProvider(ctx, update.Owner); err != nil {
				eth.logger.Error("could not update service provider from update event: %v", err)
				continue
			}
		case staked := <-stakedChan:
			if err := eth.updateStakedAmountForServiceProvider(ctx, staked.User, staked.Total); err != nil {
				eth.logger.Error("could not update service staked amount from staked event: %v", err)
				continue
			}
		case unstaked := <-unstakedChan:
			if err := eth.updateStakedAmountForServiceProvider(ctx, unstaked.User, unstaked.Total); err != nil {
				eth.logger.Error("could not update service staked amount from unstaked event: %v", err)
				continue
			}
		case slashed := <-slashedChan:
			if err := eth.updateStakedAmountForServiceProvider(ctx, slashed.User, slashed.Total); err != nil {
				eth.logger.Error("could not update service staked amount from slashed event: %v", err)
				continue
			}
		case <-ticker.C:
			if err := eth.hydrateEthData(ctx); err != nil {
				// crash if periodic updates fail - it may be necessary to reestablish connections
				return fmt.Errorf("error gathering eth endpoints: %v", err)
			}
		case <-ctx.Done():
			return errors.New("context canceled")
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
		eth.z.Error("eth could not fetch endpoint from db", zap.String("endpoint", endpoint), zap.Error(err))
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
		eth.z.Error("eth could not delete registered endpoint", zap.Error(err))
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
		db.UpsertStakedParams{Address: address.Hex(), TotalStaked: new(big.Int).Div(totalStaked, audConversion).Int64()},
	); err != nil {
		eth.z.Error("eth could not update service staked amount", zap.Error(err))
		return fmt.Errorf("could not update service staked amount: %v", err)
	}
	if err := eth.updateTotalStakedAmount(ctx); err != nil {
		eth.z.Error("eth could not update total staked amount", zap.Error(err))
		return fmt.Errorf("cound not update total staked amount: %v", err)
	}
	return nil
}

func (eth *EthService) updateTotalStakedAmount(ctx context.Context) error {
	staking, err := eth.c.GetStakingContract()
	if err != nil {
		eth.z.Error("eth failed to bind staking contract", zap.Error(err))
		return fmt.Errorf("failed to bind staking contract: %v", err)
	}
	opts := &bind.CallOpts{Context: ctx}
	totalStaked, err := staking.TotalStaked(opts)
	if err != nil {
		eth.z.Error("eth could not get total staked across all delegators", zap.Error(err))
		return fmt.Errorf("could not get total staked across all delegators: %v", err)
	}
	eth.fundingRound.totalStakedAmount = new(big.Int).Div(totalStaked, audConversion).Int64()
	return nil
}

func (eth *EthService) addRegisteredEndpoint(ctx context.Context, spID *big.Int, serviceType [32]byte, endpoint string, owner ethcommon.Address) error {
	st, err := contracts.ServiceTypeToString(serviceType)
	if err != nil {
		return err
	}
	node, err := eth.c.GetRegisteredNode(ctx, spID, serviceType)
	if err != nil {
		eth.z.Error("eth could not get registered node", zap.Error(err))
		return err
	}

	// Grab timestamp from block when this endpoint was registered
	registeredBlock, err := eth.rpc.HeaderByNumber(ctx, node.BlockNumber)
	if err != nil {
		eth.z.Error("eth failed to get block to check registration date", zap.Error(err))
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
		eth.z.Error("eth failed to bind service provider factory contract while updating service provider", zap.Error(err))
		return fmt.Errorf("failed to bind service provider factory contract while updating service provider: %v", err)
	}
	opts := &bind.CallOpts{Context: ctx}

	spDetails, err := serviceProviderFactory.GetServiceProviderDetails(opts, serviceProviderAddress)
	if err != nil {
		eth.z.Error("eth failed to get service provider details for address", zap.String("address", serviceProviderAddress.Hex()), zap.Error(err))
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
		eth.z.Error("eth could not upsert service provider into eth service db", zap.Error(err))
		return fmt.Errorf("could not upsert service provider into eth service db: %v", err)
	}
	return nil
}

func (eth *EthService) hydrateEthData(ctx context.Context) error {
	eth.z.Info("refreshing eth data")

	nodes, err := eth.c.GetAllRegisteredNodes(ctx)
	if err != nil {
		eth.z.Error("eth could not get registered nodes", zap.Error(err))
		return fmt.Errorf("could not get registered nodes from contracts: %w", err)
	}

	tx, err := eth.pool.Begin(ctx)
	if err != nil {
		eth.z.Error("eth could not begin db tx", zap.Error(err))
		return fmt.Errorf("could not begin db tx: %w", err)
	}
	defer tx.Rollback(context.Background())

	txq := eth.db.WithTx(tx)

	if err := txq.ClearRegisteredEndpoints(ctx); err != nil {
		eth.z.Error("eth could not clear registered endpoints", zap.Error(err))
		return fmt.Errorf("could not clear registered endpoints: %w", err)
	}

	if err := txq.ClearRegisteredEndpoints(ctx); err != nil {
		return fmt.Errorf("could not clear registered endpoints: %w", err)
	}
	if err := txq.ClearServiceProviders(ctx); err != nil {
		eth.z.Error("eth could not clear service providers", zap.Error(err))
		return fmt.Errorf("could not clear service providers: %w", err)
	}

	allServiceProviders := make(map[string]*db.EthServiceProvider, len(nodes))
	serviceProviderFactory, err := eth.c.GetServiceProviderFactoryContract()
	if err != nil {
		eth.z.Error("eth failed to bind service provider factory contract", zap.Error(err))
		return fmt.Errorf("failed to bind service provider factory contract: %v", err)
	}
	staking, err := eth.c.GetStakingContract()
	if err != nil {
		eth.z.Error("eth failed to bind staking contract", zap.Error(err))
		return fmt.Errorf("failed to bind staking contract: %v", err)
	}
	claimsManager, err := eth.c.GetClaimsManagerContract()
	if err != nil {
		eth.z.Error("eth failed to bind claims manager contract", zap.Error(err))
		return fmt.Errorf("failed to bind claims manager contract: %v", err)
	}
	opts := &bind.CallOpts{Context: ctx}

	for _, node := range nodes {
		st, err := contracts.ServiceTypeToString(node.Type)
		if err != nil {
			eth.z.Error("eth could not resolve service type for node", zap.Error(err))
			return fmt.Errorf("could resolve service type for node: %v", err)
		}

		// Grab timestamp from block when this endpoint was registered
		registeredBlock, err := eth.rpc.HeaderByNumber(ctx, node.BlockNumber)
		if err != nil {
			eth.z.Error("eth failed to get block to check registration date", zap.Error(err))
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
			eth.z.Error("eth could not insert registered endpoint into eth indexer db", zap.Error(err))
			return fmt.Errorf("could not insert registered endpoint into eth indexer db: %v", err)
		}

		if _, ok := allServiceProviders[node.Owner.Hex()]; !ok {
			spDetails, err := serviceProviderFactory.GetServiceProviderDetails(opts, node.Owner)
			if err != nil {
				eth.z.Error("eth failed to get service provider details", zap.String("address", node.Owner.Hex()), zap.Error(err))
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
			eth.z.Error("eth could not insert service provider into eth indexer db", zap.Error(err))
			return fmt.Errorf("could not insert service provider into eth indexer db: %v", err)
		}

		totalStakedForSp, err := staking.TotalStakedFor(opts, ethcommon.HexToAddress(sp.Address))
		if err != nil {
			eth.z.Error("eth could not get total staked amount for address", zap.String("address", sp.Address), zap.Error(err))
			return fmt.Errorf("could not get total staked amount for address %s: %v", sp.Address, err)
		}
		if err = txq.UpsertStaked(
			ctx,
			db.UpsertStakedParams{
				Address:     sp.Address,
				TotalStaked: new(big.Int).Div(totalStakedForSp, audConversion).Int64(),
			},
		); err != nil {
			eth.z.Error("eth could not insert staked amount into eth indexer db", zap.Error(err))
			return fmt.Errorf("could not insert staked amount into eth indexer db: %v", err)
		}
	}

	if err := eth.updateTotalStakedAmount(ctx); err != nil {
		eth.z.Error("eth could not update total staked amount", zap.Error(err))
		return fmt.Errorf("could not update total staked amount: %v", err)
	}

	fundingAmountPerRound, err := claimsManager.GetFundsPerRound(opts)
	if err != nil {
		eth.z.Error("eth could not get funding amount per round", zap.Error(err))
		return fmt.Errorf("could not get funding amount per round: %v", err)
	}

	eth.fundingRound.fundingAmountPerRound = new(big.Int).Div(fundingAmountPerRound, audConversion).Int64()
	eth.fundingRound.initialized = true

	return tx.Commit(ctx)
}
