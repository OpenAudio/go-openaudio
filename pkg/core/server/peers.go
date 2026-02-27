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
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

const (
	connectRPCInterval  = 15 * time.Second
	cometRPCInterval    = 15 * time.Second
	healthcheckInterval = 30 * time.Second
	peerInfoInterval    = 15 * time.Second
)

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

	verbose := strings.Contains(path, "verbose")

	nodes := []*RegisteredNodeVerboseResponse{}
	registeredNodes, err := queries.GetAllRegisteredNodes(ctx)
	if err != nil {
		return fmt.Errorf("could not get all nodes: %v", err)
	}
	for _, node := range registeredNodes {
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

func (s *Server) debugP2PConnections(c echo.Context) error {
	ctx := c.Request().Context()

	type DebugResponse struct {
		NetInfoPeers    []map[string]interface{} `json:"netinfo_peers"`
		RegisteredPeers []map[string]interface{} `json:"registered_peers"`
		PeerStatuses    []map[string]interface{} `json:"peer_statuses"`
		MatchingInfo    map[string]interface{}   `json:"matching_info"`
	}

	resp := DebugResponse{
		NetInfoPeers:    []map[string]interface{}{},
		RegisteredPeers: []map[string]interface{}{},
		PeerStatuses:    []map[string]interface{}{},
		MatchingInfo:    map[string]interface{}{},
	}

	if s.rpc != nil {
		netInfo, err := s.rpc.NetInfo(ctx)
		if err == nil {
			for _, peer := range netInfo.Peers {
				resp.NetInfoPeers = append(resp.NetInfoPeers, map[string]interface{}{
					"node_id": string(peer.NodeInfo.ID()),
					"moniker": peer.NodeInfo.Moniker,
				})
			}
			resp.MatchingInfo["connected_p2p_peers_count"] = len(netInfo.Peers)
		} else {
			resp.MatchingInfo["netinfo_error"] = err.Error()
		}
	} else {
		resp.MatchingInfo["rpc_not_ready"] = true
	}

	validators, err := s.db.GetAllRegisteredNodes(ctx)
	if err == nil {
		for _, validator := range validators {
			resp.RegisteredPeers = append(resp.RegisteredPeers, map[string]interface{}{
				"endpoint":      validator.Endpoint,
				"comet_address": validator.CometAddress,
				"eth_address":   validator.EthAddress,
			})
		}
		resp.MatchingInfo["registered_nodes_count"] = len(validators)
	} else {
		resp.MatchingInfo["db_error"] = err.Error()
	}

	for _, peerStatus := range s.peerStatus.Values() {
		resp.PeerStatuses = append(resp.PeerStatuses, map[string]interface{}{
			"endpoint":           peerStatus.Endpoint,
			"comet_address":      peerStatus.CometAddress,
			"eth_address":        peerStatus.EthAddress,
			"p2p_connected":      peerStatus.P2PConnected,
			"connectrpc_healthy": peerStatus.ConnectrpcHealthy,
		})
	}
	resp.MatchingInfo["peer_statuses_count"] = len(resp.PeerStatuses)

	return c.JSON(200, resp)
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
	// Include jailed nodes so UI can show status
	validators, err := s.db.GetAllRegisteredNodesIncludingJailed(ctx)
	if err != nil {
		return fmt.Errorf("could not get validators from db: %v", err)
	}

	currentAddrs := make(map[EthAddress]bool)
	for _, validator := range validators {
		self := s.config.WalletAddress
		if validator.EthAddress == self {
			continue
		}
		addr := EthAddress(validator.EthAddress)
		currentAddrs[addr] = true

		existing, exists := s.peerStatus.Get(addr)
		peer := &v1.GetStatusResponse_PeerInfo_Peer{
			Endpoint:         validator.Endpoint,
			CometAddress:     validator.CometAddress,
			EthAddress:       validator.EthAddress,
			NodeType:         validator.NodeType,
			Jailed:           validator.Jailed,
			ConnectrpcClient:  exists && existing.ConnectrpcClient,
			ConnectrpcHealthy: exists && existing.ConnectrpcHealthy,
			CometrpcClient:   exists && existing.CometrpcClient,
			P2PConnected:      exists && existing.P2PConnected,
		}
		s.peerStatus.Set(addr, peer)
	}

	// Remove peers no longer in core_validators
	for _, addr := range s.peerStatus.Keys() {
		if !currentAddrs[addr] {
			s.peerStatus.Delete(addr)
		}
	}

	return nil
}

// refreshes the clients in the server struct for connectrpc, does not test connectivity.
func (s *Server) refreshConnectRPCPeers(ctx context.Context, _ *zap.Logger) error {
	validators, err := s.db.GetAllRegisteredNodesIncludingJailed(ctx)
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
	validators, err := s.db.GetAllRegisteredNodesIncludingJailed(ctx)
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

	for ethaddress, rpc := range connectPeers {
		wg.Add(1)
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

	wg.Wait()

	// Refresh P2P connection status
	if err := s.refreshP2PConnections(ctx, logger); err != nil {
		logger.Error("could not refresh P2P connections", zap.Error(err))
	}

	return nil
}

func (s *Server) refreshP2PConnections(ctx context.Context, logger *zap.Logger) error {
	if s.rpc == nil {
		return nil
	}

	netInfo, err := s.rpc.NetInfo(ctx)
	if err != nil {
		return fmt.Errorf("could not get netinfo: %v", err)
	}

	connectedNodeIDs := make(map[string]bool)
	for _, peer := range netInfo.Peers {
		nodeID := string(peer.NodeInfo.ID())
		if nodeID != "" {
			connectedNodeIDs[nodeID] = true
		}
	}

	connectedCometAddresses := make(map[string]bool)
	cometPeers := s.cometRPCPeers.ToMap()
	for _, cometRPC := range cometPeers {
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		validators, err := cometRPC.Validators(queryCtx, nil, nil, nil)
		cancel()

		if err == nil && validators != nil {
			for _, val := range validators.Validators {
				if val != nil {
					validatorAddr := strings.ToLower(val.Address.String())
					connectedCometAddresses[validatorAddr] = true
				}
			}
		}

		statusCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		status, err := cometRPC.Status(statusCtx)
		cancel()

		if err == nil && status != nil {
			validatorAddr := strings.ToLower(status.ValidatorInfo.Address.String())
			if validatorAddr != "" {
				connectedCometAddresses[validatorAddr] = true
			}
		}
	}

	for nodeID := range connectedNodeIDs {
		for _, peerStatus := range s.peerStatus.Values() {
			if strings.HasPrefix(peerStatus.CometAddress, nodeID+"@") {
				connectedCometAddresses[peerStatus.CometAddress] = true
			}
		}
	}

	for _, peerStatus := range s.peerStatus.Values() {
		if peerStatus.CometAddress == "" {
			continue
		}

		cometAddrLower := strings.ToLower(peerStatus.CometAddress)
		isConnected := connectedCometAddresses[cometAddrLower]

		if !isConnected && strings.Contains(peerStatus.CometAddress, "@") {
			parts := strings.Split(peerStatus.CometAddress, "@")
			if len(parts) > 0 {
				nodeID := strings.TrimSpace(parts[0])
				if connectedNodeIDs[nodeID] {
					isConnected = true
				}
			}
		}

		peerStatus.P2PConnected = isConnected
		if ethAddr := peerStatus.EthAddress; ethAddr != "" {
			s.peerStatus.Set(EthAddress(ethAddr), peerStatus)
		}
	}

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
