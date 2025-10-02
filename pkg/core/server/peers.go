// Peers that core is aware of and uses. This is different than the lower level p2p list that cometbft manages.
// This is where we store sdk clients for other validators for the purposes of forwarding transactions, querying health checks, and
// anything else.
package server

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/eth/contracts"
	"github.com/OpenAudio/go-openaudio/pkg/safemap"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/cometbft/cometbft/p2p"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

const (
	connectRPCInterval  = 15 * time.Second
	cometRPCInterval    = 15 * time.Second
	healthcheckInterval = 30 * time.Second
	p2pcheckInterval    = 15 * time.Second
	peerInfoInterval    = 15 * time.Second
)

var legacyDiscoveryProviderProfile = []string{".audius.co", ".creatorseed.com", "dn1.monophonic.digital", ".figment.io", ".tikilabs.com"}

type RegisteredNodeVerboseResponse struct {
	Owner               string `json:"owner"`
	Endpoint            string `json:"endpoint"`
	SpID                uint64 `json:"spID"`
	NodeType            string `json:"type"`
	BlockNumber         uint64 `json:"blockNumber"`
	DelegateOwnerWallet string `json:"delegateOwnerWallet"`
	CometAddress        string `json:"cometAddress"`
}

type RegisteredNodesVerboseResponse struct {
	RegisteredNodes []*RegisteredNodeVerboseResponse `json:"data"`
}

type RegisteredNodesEndpointResponse struct {
	RegisteredNodes []string `json:"data"`
}

func (s *Server) getRegisteredNodes(c echo.Context) error {
	ctx := c.Request().Context()
	queries := s.db

	path := c.Path()

	discoveryQuery := strings.Contains(path, "discovery")
	contentQuery := strings.Contains(path, "content")
	allQuery := !discoveryQuery && !contentQuery

	verbose := strings.Contains(path, "verbose")

	nodes := []*RegisteredNodeVerboseResponse{}

	if allQuery {
		res, err := queries.GetAllRegisteredNodes(ctx)
		if err != nil {
			return fmt.Errorf("could not get all nodes: %v", err)
		}
		for _, node := range res {
			spID, err := strconv.ParseUint(node.SpID, 10, 32)
			if err != nil {
				return fmt.Errorf("could not convert spid to int: %v", err)
			}

			ethBlock, err := strconv.ParseUint(node.EthBlock, 10, 32)
			if err != nil {
				return fmt.Errorf("could not convert ethblock to int: %v", err)
			}

			nodes = append(nodes, &RegisteredNodeVerboseResponse{
				// TODO: fix this
				Owner:               node.EthAddress,
				Endpoint:            node.Endpoint,
				SpID:                spID,
				NodeType:            node.NodeType,
				BlockNumber:         ethBlock,
				DelegateOwnerWallet: node.EthAddress,
				CometAddress:        node.CometAddress,
			})
		}
	}

	if discoveryQuery {
		res, err := queries.GetRegisteredNodesByType(ctx, common.HexToUtf8(contracts.DiscoveryNode))
		if err != nil {
			return fmt.Errorf("could not get discovery nodes: %v", err)
		}
		for _, node := range res {
			isProd := s.config.Environment == "prod"
			if isProd {
				nodeFound := false
				for _, nodeType := range legacyDiscoveryProviderProfile {
					if nodeFound {
						break
					}
					if strings.Contains(node.Endpoint, nodeType) {
						nodeFound = true
						break
					}
				}
				if !nodeFound {
					continue
				}
			}

			spID, err := strconv.ParseUint(node.SpID, 10, 32)
			if err != nil {
				return fmt.Errorf("could not convert spid to int: %v", err)
			}

			ethBlock, err := strconv.ParseUint(node.EthBlock, 10, 32)
			if err != nil {
				return fmt.Errorf("could not convert ethblock to int: %v", err)
			}

			nodeResponse := &RegisteredNodeVerboseResponse{
				Owner:               node.EthAddress,
				Endpoint:            node.Endpoint,
				SpID:                spID,
				NodeType:            node.NodeType,
				BlockNumber:         ethBlock,
				DelegateOwnerWallet: node.EthAddress,
				CometAddress:        node.CometAddress,
			}

			nodes = append(nodes, nodeResponse)
		}
	}

	if contentQuery {
		res, err := queries.GetRegisteredNodesByType(ctx, common.HexToUtf8(contracts.ContentNode))
		if err != nil {
			return fmt.Errorf("could not get discovery nodes: %v", err)
		}
		for _, node := range res {
			spID, err := strconv.ParseUint(node.SpID, 10, 32)
			if err != nil {
				return fmt.Errorf("could not convert spid to int: %v", err)
			}

			ethBlock, err := strconv.ParseUint(node.EthBlock, 10, 32)
			if err != nil {
				return fmt.Errorf("could not convert ethblock to int: %v", err)
			}

			nodes = append(nodes, &RegisteredNodeVerboseResponse{
				// TODO: fix this
				Owner:               node.EthAddress,
				Endpoint:            node.Endpoint,
				SpID:                spID,
				NodeType:            node.NodeType,
				BlockNumber:         ethBlock,
				DelegateOwnerWallet: node.EthAddress,
				CometAddress:        node.CometAddress,
			})
		}
	}

	if verbose {
		res := RegisteredNodesVerboseResponse{
			RegisteredNodes: nodes,
		}
		return c.JSON(200, res)
	}

	endpoint := []string{}

	for _, node := range nodes {
		endpoint = append(endpoint, node.Endpoint)
	}

	res := RegisteredNodesEndpointResponse{
		RegisteredNodes: endpoint,
	}

	return c.JSON(200, res)
}

func (s *Server) managePeers(ctx context.Context) error {
	s.StartProcess(ProcessStatePeerManager)

	logger := s.logger.With(zap.String("service", "peer_manager"))

	select {
	case <-ctx.Done():
		s.CompleteProcess(ProcessStatePeerManager)
		return ctx.Err()
	case <-s.awaitRpcReady:
	}

	connectRPCTicker := time.NewTicker(connectRPCInterval)
	defer connectRPCTicker.Stop()

	cometRPCTicker := time.NewTicker(cometRPCInterval)
	defer cometRPCTicker.Stop()

	healthcheckTicker := time.NewTicker(healthcheckInterval)
	defer healthcheckTicker.Stop()

	p2pcheckTicker := time.NewTicker(p2pcheckInterval)
	defer p2pcheckTicker.Stop()

	peerInfoTicker := time.NewTicker(peerInfoInterval)
	defer peerInfoTicker.Stop()

	for {
		select {
		case <-connectRPCTicker.C:
			s.RunningProcessWithMetadata(ProcessStatePeerManager, "Refreshing Connect RPC clients")
			if err := s.refreshConnectRPCPeers(ctx, logger); err != nil {
				logger.Error("could not refresh connectrpcs", zap.Error(err))
			}
			s.SleepingProcessWithMetadata(ProcessStatePeerManager, "Waiting for next cycle")
		case <-cometRPCTicker.C:
			s.RunningProcessWithMetadata(ProcessStatePeerManager, "Refreshing Comet RPC clients")
			if err := s.refreshCometRPCPeers(ctx, logger); err != nil {
				logger.Error("could not refresh cometbft rpcs", zap.Error(err))
			}
			s.SleepingProcessWithMetadata(ProcessStatePeerManager, "Waiting for next cycle")
		case <-healthcheckTicker.C:
			s.RunningProcessWithMetadata(ProcessStatePeerManager, "Health checking peers")
			if err := s.refreshPeerHealth(ctx, logger); err != nil {
				logger.Error("could not check health", zap.Error(err))
			}
			s.SleepingProcessWithMetadata(ProcessStatePeerManager, "Waiting for next cycle")
		case <-p2pcheckTicker.C:
			s.RunningProcessWithMetadata(ProcessStatePeerManager, "Checking P2P connectivity")
			if err := s.checkPeerP2PAddr(ctx, logger); err != nil {
				logger.Error("could not check p2p connectivity", zap.Error(err))
			}
			s.SleepingProcessWithMetadata(ProcessStatePeerManager, "Waiting for next cycle")
		case <-peerInfoTicker.C:
			s.RunningProcessWithMetadata(ProcessStatePeerManager, "Refreshing peer data")
			if err := s.refreshPeerData(ctx, logger); err != nil {
				logger.Error("could not refresh peer data", zap.Error(err))
			}
			s.SleepingProcessWithMetadata(ProcessStatePeerManager, "Waiting for next cycle")
		case <-ctx.Done():
			logger.Info("shutting down")
			s.CompleteProcess(ProcessStatePeerManager)
			return ctx.Err()
		}
	}
}

func (s *Server) refreshPeerData(ctx context.Context, _ *zap.Logger) error {
	validators, err := s.db.GetAllRegisteredNodes(ctx)
	if err != nil {
		return fmt.Errorf("could not get validators from db: %v", err)
	}

	for _, validator := range validators {
		self := s.config.WalletAddress
		if validator.EthAddress == self {
			continue
		}
		exists := s.peerStatus.Has(validator.EthAddress)
		if exists {
			continue
		}
		s.peerStatus.Set(validator.EthAddress, &v1.GetStatusResponse_PeerInfo_Peer{
			Endpoint:     validator.Endpoint,
			CometAddress: validator.CometAddress,
			EthAddress:   validator.EthAddress,
			NodeType:     validator.NodeType,
		})
	}

	return nil
}

// refreshes the clients in the server struct for connectrpc, does not test connectivity.
func (s *Server) refreshConnectRPCPeers(ctx context.Context, _ *zap.Logger) error {
	validators, err := s.db.GetAllRegisteredNodes(ctx)
	if err != nil {
		return fmt.Errorf("could not get validators from db: %v", err)
	}

	for _, validator := range validators {
		ethAddress := validator.EthAddress
		self := s.config.WalletAddress
		if ethAddress == self {
			continue
		}

		status, exists := s.peerStatus.Get(ethAddress)
		if s.connectRPCPeers.Has(ethAddress) {
			// Client exists, make sure status reflects reality
			if exists && !status.ConnectrpcClient {
				status.ConnectrpcClient = true
				s.peerStatus.Set(ethAddress, status)
			}
			continue
		}

		endpoint := validator.Endpoint
		oap := sdk.NewOpenAudioSDK(endpoint)
		connectRPC := oap.Core
		s.connectRPCPeers.Set(ethAddress, connectRPC)

		if exists {
			status.ConnectrpcClient = true
			s.peerStatus.Set(ethAddress, status)
		}
	}

	return nil
}

// refreshes the cometbft rpc clients in the server struct, does not test connectivity.
func (s *Server) refreshCometRPCPeers(ctx context.Context, logger *zap.Logger) error {
	validators, err := s.db.GetAllRegisteredNodes(ctx)
	if err != nil {
		return fmt.Errorf("could not get validators from db: %v", err)
	}

	for _, validator := range validators {
		ethAddress := validator.EthAddress
		self := s.config.WalletAddress
		if ethAddress == self {
			continue
		}

		status, exists := s.peerStatus.Get(ethAddress)
		if s.cometRPCPeers.Has(ethAddress) {
			if exists && !status.CometrpcClient {
				status.CometrpcClient = true
				s.peerStatus.Set(ethAddress, status)
			}
			continue
		}

		endpoint := validator.Endpoint + "/core/crpc"
		cometRPC, err := rpchttp.New(endpoint)
		if err != nil {
			logger.Error("could not create cometrpc", zap.String("peer_endpoint", endpoint), zap.Error(err))
			continue
		}
		s.cometRPCPeers.Set(ethAddress, cometRPC)

		if exists {
			status.CometrpcClient = true
			s.peerStatus.Set(ethAddress, status)
		}
	}

	return nil
}

// grabs the cometbft rpc and connectrpc clients from the server struct and tests their
// connectivity and health. reports health status to status check.
func (s *Server) refreshPeerHealth(ctx context.Context, logger *zap.Logger) error {
	var wg sync.WaitGroup

	connectPeers := s.connectRPCPeers.ToMap()
	cometPeers := s.cometRPCPeers.ToMap()
	wg.Add(len(connectPeers) + len(cometPeers))

	for ethaddress, rpc := range connectPeers {
		go func(ethaddress EthAddress, rpc v1connect.CoreServiceClient) {
			defer wg.Done()

			self := s.config.WalletAddress
			if ethaddress == self {
				return
			}

			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			_, err := rpc.Ping(pingCtx, connect.NewRequest(&v1.PingRequest{}))
			if err != nil {
				logger.Error("connect rpc unreachable", zap.String("eth_address", ethaddress), zap.Error(err))
			}

			status, exists := s.peerStatus.Get(ethaddress)
			if exists {
				status.ConnectrpcHealthy = (err == nil)
				s.peerStatus.Set(ethaddress, status)
			}
		}(ethaddress, rpc)
	}

	for ethaddress, rpc := range cometPeers {
		go func(ethaddress EthAddress, rpc *CometBFTRPC) {
			defer wg.Done()

			self := s.config.WalletAddress
			if ethaddress == self {
				return
			}

			healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			_, err := rpc.Health(healthCtx)
			if err != nil {
				logger.Error("connect rpc unreachable", zap.String("eth_address", ethaddress), zap.Error(err))
			}

			status, exists := s.peerStatus.Get(ethaddress)
			if exists {
				status.CometrpcHealthy = (err == nil)
				s.peerStatus.Set(ethaddress, status)
			}
		}(ethaddress, rpc)
	}

	wg.Wait()

	return nil
}

// grabs the cometbft rpc clients in the server struct, uses the status endpoints to test
// p2p connectivity. if not already peered will dial peers for comet. reports p2p status
// to status check.
func (s *Server) checkPeerP2PAddr(ctx context.Context, logger *zap.Logger) error {
	var wg sync.WaitGroup

	netInfoCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	netInfo, err := s.rpc.NetInfo(netInfoCtx)
	if err != nil {
		return fmt.Errorf("could not get self net info: %v", err)
	}

	isPeered := func(nodeid p2p.ID) bool {
		for _, peer := range netInfo.Peers {
			if peer.NodeInfo.DefaultNodeID == nodeid {
				return true
			}
		}
		return false
	}

	peersToDial := safemap.New[CometP2PConnectionString, struct{}]()

	cometPeers := s.cometRPCPeers.ToMap()
	wg.Add(len(cometPeers))
	for ethaddress, rpc := range cometPeers {
		go func(ethaddress EthAddress, rpc *CometBFTRPC) {
			defer wg.Done()

			self := s.config.WalletAddress
			if ethaddress == self {
				return
			}

			status, ok := s.peerStatus.Get(ethaddress)
			if ok && status.P2PAccessible && status.P2PConnected {
				return
			}

			statusCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			cometStatus, err := rpc.Status(statusCtx)
			if err != nil {
				logger.Error("could not get status", zap.String("eth_address", ethaddress), zap.Error(err))
				return
			}

			nodeid := cometStatus.NodeInfo.DefaultNodeID
			listenAddr := cometStatus.NodeInfo.ListenAddr

			var p2pAccessible bool

			conn, err := net.DialTimeout("tcp", listenAddr, 3*time.Second)
			if err != nil {
				p2pAccessible = false
			} else {
				p2pAccessible = true
				_ = conn.Close()
			}

			status, exists := s.peerStatus.Get(ethaddress)
			if exists {
				status.P2PAccessible = p2pAccessible
				s.peerStatus.Set(ethaddress, status)

				if status.P2PConnected {
					return
				}
			}

			if !isPeered(nodeid) {
				connectionString := fmt.Sprintf("%s@%s", nodeid, listenAddr)
				peersToDial.Set(connectionString, struct{}{})
			}
		}(ethaddress, rpc)
	}

	wg.Wait()

	res, err := s.rpc.DialPeers(ctx, peersToDial.Keys(), true, true, false)
	if err != nil {
		return fmt.Errorf("failed to dial peers: %v", err)
	}

	logger.Info("dialed peers", zap.String("result", res.Log))

	return nil
}

func (s *Server) isNonRoutableAddress(listenAddr string) bool {
	host, _, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return true // If we can't parse it, treat as non-routable
	}

	ip := net.ParseIP(host)
	if ip == nil {
		// Could be a hostname, but check for localhost
		if host == "localhost" {
			return true
		}
		// Allow container names and other hostnames in Docker/k8s environments
		return false
	}

	// Always block truly non-routable addresses
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return true
	}

	// In production, also block private IPs - in dev/test, allow them for Docker
	if s.config.Environment != "dev" && ip.IsPrivate() {
		return true
	}

	return false
}
