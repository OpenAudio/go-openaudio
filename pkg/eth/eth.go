package eth

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/jackc/pgx/v5/pgxpool"
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

	rpc         *ethclient.Client
	db          *db.Queries
	pool        *pgxpool.Pool
	logger      *common.Logger
	c           *contracts.AudiusContracts
	deregPubsub *DeregistrationPubsub

	isReady atomic.Bool
}

func NewEthService(dbURL, rpcURL, registryAddress string, logger *common.Logger, environment string) *EthService {
	return &EthService{
		logger:          logger.Child("eth"),
		rpcURL:          rpcURL,
		dbURL:           dbURL,
		registryAddress: registryAddress,
		env:             environment,
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
		return fmt.Errorf("eth client dial err: %v", err)
	}
	eth.rpc = ethrpc
	defer ethrpc.Close()

	// Init contracts
	c, err := contracts.NewAudiusContracts(eth.rpc, eth.registryAddress)
	if err != nil {
		return fmt.Errorf("failed to initialize eth contracts: %v", err)
	}
	eth.c = c

	eth.logger.Infof("starting eth service")

	if err := eth.startEthEndpointManager(ctx); err != nil {
		return fmt.Errorf("Error running endpoint manager: %w", err)
	}

	return nil
}

func (eth *EthService) startEthEndpointManager(ctx context.Context) error {
	// query all endpoints at startup
	delay := 2 * time.Second
	ticker := time.NewTicker(delay)
initial:
	for {
		select {
		case <-ticker.C:
			if err := eth.refreshEndpoints(ctx); err != nil {
				eth.logger.Errorf("error gathering registered eth endpoints: %v", err)
				delay *= 2
				eth.logger.Infof("retrying in %s seconds", delay)
				ticker.Reset(delay)
			} else {
				break initial
			}
		case <-ctx.Done():
			return errors.New("context canceled")
		}
	}

	eth.isReady.Store(true)

	// Instantiate the contract
	spf, err := eth.c.GetServiceProviderFactoryContract()
	if err != nil {
		return fmt.Errorf("failed to bind service provider factory contract: %v", err)
	}

	watchOpts := &bind.WatchOpts{Context: ctx}

	registerChan := make(chan *gen.ServiceProviderFactoryRegisteredServiceProvider)
	deregisterChan := make(chan *gen.ServiceProviderFactoryDeregisteredServiceProvider)
	updateChan := make(chan *gen.ServiceProviderFactoryEndpointUpdated)

	registerSub, err := spf.WatchRegisteredServiceProvider(watchOpts, registerChan, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to endpoint registration events: %v", err)
	}

	deregisterSub, err := spf.WatchDeregisteredServiceProvider(watchOpts, deregisterChan, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to endpoint deregistration events: %v", err)
	}

	updateSub, err := spf.WatchEndpointUpdated(watchOpts, updateChan, nil, nil, nil)
	if err != nil {
		return fmt.Errorf("failed to subscribe to endpoint update events: %v", err)
	}

	ticker = time.NewTicker(1 * time.Hour)

	for {
		select {
		case err := <-registerSub.Err():
			return fmt.Errorf("register event subscription error: %v", err)
		case err := <-deregisterSub.Err():
			return fmt.Errorf("deregister event subscription error: %v", err)
		case err := <-updateSub.Err():
			return fmt.Errorf("update event subscription error: %v", err)
		case reg := <-registerChan:
			st, err := contracts.ServiceTypeToString(reg.ServiceType)
			if err != nil {
				eth.logger.Error("could not handle registration event: %v", err)
				continue
			}
			node, err := eth.c.GetRegisteredNode(ctx, reg.SpID, reg.ServiceType)
			if err != nil {
				eth.logger.Error("could not handle registration event: %v", err)
				continue
			}
			if err := eth.db.InsertRegisteredEndpoint(
				ctx,
				db.InsertRegisteredEndpointParams{
					ID:             int32(reg.SpID.Int64()),
					ServiceType:    st,
					Owner:          reg.Owner.Hex(),
					DelegateWallet: node.DelegateOwnerWallet.Hex(),
					Endpoint:       reg.Endpoint,
					Blocknumber:    node.BlockNumber.Int64(),
				},
			); err != nil {
				eth.logger.Error("could not handle registration event: %v", err)
				continue
			}
		case dereg := <-deregisterChan:
			st, err := contracts.ServiceTypeToString(dereg.ServiceType)
			if err != nil {
				eth.logger.Error("could not handle deregistration event: %v", err)
				continue
			}
			ep, err := eth.db.GetRegisteredEndpoint(ctx, dereg.Endpoint)
			if err != nil {
				eth.logger.Error("could not fetch endpoint %s from db: %v", dereg.Endpoint, err)
				continue
			}
			if err := eth.db.DeleteRegisteredEndpoint(
				ctx,
				db.DeleteRegisteredEndpointParams{
					ID:          int32(dereg.SpID.Int64()),
					Endpoint:    dereg.Endpoint,
					Owner:       dereg.Owner.Hex(),
					ServiceType: st,
				},
			); err != nil {
				eth.logger.Error("could not handle deregistration event: %v", err)
				continue
			}
			eth.deregPubsub.Publish(
				ctx,
				DeregistrationTopic,
				&v1.ServiceEndpoint{
					Id:             dereg.SpID.Int64(),
					Owner:          dereg.Owner.Hex(),
					Endpoint:       dereg.Endpoint,
					DelegateWallet: ep.DelegateWallet,
				},
			)
		case <-ticker.C:
			if err := eth.refreshEndpoints(ctx); err != nil {
				// crash if periodic updates fail - it may be necessary to reestablish connections
				return fmt.Errorf("error gathering eth endpoints: %v", err)
			}
		case <-ctx.Done():
			return errors.New("context canceled")
		}
	}

	return nil
}

func (eth *EthService) SubscribeToDeregistrationEvents() chan *v1.ServiceEndpoint {
	return eth.deregPubsub.Subscribe(DeregistrationTopic, 10)
}

func (eth *EthService) UnsubscribeFromDeregistrationEvents(ch chan *v1.ServiceEndpoint) {
	eth.deregPubsub.Unsubscribe(DeregistrationTopic, ch)
}

func (eth *EthService) refreshEndpoints(ctx context.Context) error {
	eth.logger.Info("refreshing registered endpoints")

	nodes, err := eth.c.GetAllRegisteredNodes(ctx)
	if err != nil {
		return fmt.Errorf("could not get registered nodes from contracts: %w", err)
	}

	tx, err := eth.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("could not begin db tx: %w", err)
	}
	defer tx.Rollback(context.Background())

	txq := eth.db.WithTx(tx)

	if err := txq.ClearRegisteredEndpoints(ctx); err != nil {
		return fmt.Errorf("could not clear registered endpoints: %w", err)
	}
	for _, node := range nodes {
		st, err := contracts.ServiceTypeToString(node.Type)
		if err != nil {
			return fmt.Errorf("could resolve service type for node: %w", err)
		}
		if err := txq.InsertRegisteredEndpoint(
			ctx,
			db.InsertRegisteredEndpointParams{
				ID:             int32(node.Id.Int64()),
				ServiceType:    st,
				Owner:          node.Owner.Hex(),
				DelegateWallet: node.DelegateOwnerWallet.Hex(),
				Endpoint:       node.Endpoint,
				Blocknumber:    node.BlockNumber.Int64(),
			},
		); err != nil {
			return fmt.Errorf("could not insert registered endpoint into eth indexer db: %w", err)
		}
	}

	return tx.Commit(ctx)
}
