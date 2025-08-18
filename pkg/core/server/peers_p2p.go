package server

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/api/core/v1/v1connect"
	"github.com/AudiusProject/audiusd/pkg/safemap"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
)

func (s *Server) startP2PPeers(ctx context.Context) error {
	<-s.awaitRpcReady
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.refreshP2PPeers(ctx); err != nil {
				s.logger.Errorf("error connecting to cometbft rpc peers: %v", err)
			}
		case <-ctx.Done():
			// shut down rpc clients
			return ctx.Err()
		}
	}
}

func (s *Server) refreshP2PPeers(ctx context.Context) error {
	// get existing set of connected peers by nodeid (lowercase comet key)
	nodeInfo, err := s.rpc.NetInfo(ctx)
	if err != nil {
		return fmt.Errorf("cannot get self netinfo: %v", err)
	}

	peers := nodeInfo.Peers
	existingPeers := safemap.New[string, CometBFTAddress]()
	otherPeers := safemap.New[string, CometBFTAddress]()

	// gather all listen addrs from peers (ip:26656 format) into set
	for _, peer := range peers {
		existingPeers.Set(peer.NodeInfo.ListenAddr, CometBFTAddress(peer.NodeInfo.ID()))
	}

	rpcClients := s.cometRPCPeers.Values()
	var wg sync.WaitGroup
	wg.Add(len(rpcClients))

	// get listen addr from all peers we're aware of
	for _, rpc := range rpcClients {
		go func(rpc *CometBFTRPC) {
			defer wg.Done()

			status, err := rpc.Status(ctx)
			if err != nil {
				return
			}

			listenAddr := status.NodeInfo.ListenAddr
			if existingPeers.Has(listenAddr) {
				return
			}

			// port open test for listen addr, don't attempt peers who
			// don't have the port open
			conn, err := net.DialTimeout("tcp", listenAddr, 3*time.Second)
			if err != nil {
				// TODO: report 26656 status to core status endpoint
				return
			}
			_ = conn.Close()

			otherPeers.Set(listenAddr, CometBFTAddress(status.NodeInfo.ID()))
		}(rpc)
	}

	wg.Wait()

	// connect to ones available that aren't in existing peer set and have port open
	dialPeers := safemap.ToSlice(otherPeers, func(listenAddr string, nodeid CometBFTAddress) string {
		return fmt.Sprintf("%s@%s", strings.ToLower(nodeid), listenAddr)
	})

	if len(dialPeers) == 0 {
		return nil
	}

	result, err := s.rpc.DialPeers(ctx, dialPeers, true, false, false)
	if err != nil {
		return err
	}

	s.logger.Infof("dialed peers: %s", result.Log)

	return nil
}

// reads the existing connectrpc peers in the state and retains
// cometbft rpc clients for each node as well
func (s *Server) startCometRPCPeers(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := s.refreshCometBFTRPCPeers(ctx); err != nil {
				s.logger.Errorf("error connecting to cometbft rpc peers: %v", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Server) refreshCometBFTRPCPeers(ctx context.Context) error {
	// gather existing connectrpc peers
	peers := s.GetPeers()

	// already all peered up
	if len(peers) == s.cometRPCPeers.Len() {
		return nil
	}

	// iterate through connectrpc peers and
	// if cometbft rpc doesn't exist, create and connect
	var wg sync.WaitGroup
	wg.Add(len(peers))

	for peerEthAddr, peer := range peers {
		go func(peerEthAddr EthAddress, peer v1connect.CoreServiceClient) {
			defer wg.Done()

			// if we have a client we don't need to recreate
			ok := s.cometRPCPeers.Has(peerEthAddr)
			if ok {
				return
			}

			status, err := peer.GetStatus(ctx, connect.NewRequest(&v1.GetStatusRequest{}))
			if err != nil {
				return
			}

			endpoint := status.Msg.NodeInfo.Endpoint

			rpc, err := rpchttp.New(endpoint + "/core/crpc")
			if err != nil {
				return
			}

			s.cometRPCPeers.Set(peerEthAddr, rpc)

		}(peerEthAddr, peer)
	}

	wg.Wait()

	return nil
}
