package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/AudiusProject/audiusd/pkg/core/contracts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
)

func (s *Server) startEthNodeManager() error {
	// Initial query with retries
	maxRetries := 10
	retryDelay := 2 * time.Second

	for i := 0; i < maxRetries; i++ {
		if err := s.gatherEthNodes(); err != nil {
			s.logger.Errorf("error gathering registered eth nodes (attempt %d/%d): %v", i+1, maxRetries, err)
			time.Sleep(retryDelay)
			retryDelay *= 2
		} else {
			break
		}
		if i == maxRetries-1 {
			return fmt.Errorf("failed to gather registered eth nodes after %d retries", maxRetries)
		}
	}

	close(s.awaitEthNodesReady)
	s.logger.Info("said eth nodes ready")

	ticker := time.NewTicker(6 * time.Hour)
	if s.isDevEnvironment() {
		// query eth chain more aggressively on dev
		ticker = time.NewTicker(5 * time.Second)
	}

	defer ticker.Stop()

	for range ticker.C {
		if err := s.gatherEthNodes(); err != nil {
			s.logger.Errorf("error gathering eth nodes: %v", err)
		}
	}
	return nil
}

func (s *Server) gatherEthNodes() error {
	s.logger.Info("gathering ethereum nodes")

	ctx := context.Background()
	nodes, err := s.contracts.GetAllRegisteredNodes(ctx)
	if err != nil {
		return err
	}

	if len(nodes) == 0 {
		return fmt.Errorf("got 0 registered nodes: %v", nodes)
	}

	ethNodeMap := make(map[string]*contracts.Node, len(nodes))
	duplicateEthNodeSet := make(map[string]*contracts.Node)

	// identify duplicates
	for _, node := range nodes {
		ethaddr := node.DelegateOwnerWallet.String()
		if existingNode, ok := ethNodeMap[ethaddr]; ok {
			duplicateEthNodeSet[node.Endpoint] = node
			duplicateEthNodeSet[existingNode.Endpoint] = existingNode
		} else {
			ethNodeMap[ethaddr] = node
		}
	}

	// identify missing
	missingEthNodeSet := make(map[string]bool)
	existingValidators, err := s.db.GetAllRegisteredNodes(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("could not get existing validators: %v", err)
	}
	for _, ev := range existingValidators {
		if _, ok := ethNodeMap[ev.EthAddress]; !ok {
			missingEthNodeSet[ev.EthAddress] = true
		}
	}

	s.ethNodeMU.Lock()
	defer s.ethNodeMU.Unlock()

	s.ethNodes = nodes
	duplicateEthNodes := make([]*contracts.Node, 0, len(duplicateEthNodeSet))
	for _, node := range duplicateEthNodeSet {
		duplicateEthNodes = append(duplicateEthNodes, node)
	}
	s.duplicateEthNodes = duplicateEthNodes

	// handle deregistering nodes that have been missing for two cycles.
	// (waiting for a second cycle ensures attestors will have noticed it missing too)
	if !s.cache.catchingUp.Load() {
		for _, addr := range s.missingEthNodes {
			if missingEthNodeSet[addr] {
				go s.deregisterMissingNode(ctx, addr)
			}
		}
	} else {
		s.logger.Info("skipping peer auto-deregistration while syncing")
	}

	missingEthNodes := make([]string, 0, len(missingEthNodeSet))
	for addr, _ := range missingEthNodeSet {
		missingEthNodes = append(missingEthNodes, addr)
	}
	s.missingEthNodes = missingEthNodes

	return nil
}

func (s *Server) getEthNodesHandler(c echo.Context) error {
	s.ethNodeMU.RLock()
	defer s.ethNodeMU.RUnlock()
	res := struct {
		Nodes          []*contracts.Node `json:"nodes"`
		DuplicateNodes []*contracts.Node `json:"duplicateNodes"`
		MissingNodes   []string          `json:"missingNodes"`
	}{
		Nodes:          s.ethNodes,
		DuplicateNodes: s.duplicateEthNodes,
		MissingNodes:   s.missingEthNodes,
	}
	return c.JSON(200, res)
}

func (s *Server) isNodeRegisteredOnEthereum(delegateWallet common.Address, endpoint string, ethBlock *big.Int) bool {
	s.ethNodeMU.RLock()
	defer s.ethNodeMU.RUnlock()
	for _, node := range s.ethNodes {
		if !bytes.EqualFold(delegateWallet.Bytes(), node.DelegateOwnerWallet.Bytes()) {
			continue
		}
		if endpoint != node.Endpoint {
			continue
		}
		if node.BlockNumber.Cmp(ethBlock) != 0 {
			continue
		}
		return true
	}
	return false
}
