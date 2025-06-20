package server

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"time"

	"github.com/AudiusProject/audiusd/pkg/core/contracts/gen"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/crypto"
)

func (s *Server) startProportionalRewards() error {
	if !s.isDevEnvironment() {
		return nil
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// retry deployment until success
	for range ticker.C {
		if err := s.deployProportionalRewards(); err != nil {
			s.logger.Error("failed to deploy proportional rewards", "error", err)
		} else {
			return nil
		}
	}

	return nil
}

func (s *Server) deployProportionalRewards() error {
	logger := s.logger
	client := s.eth
	privateKey := s.config.EthereumKey
	logger.Info("starting deployment of proportional rewards")

	publicKey := privateKey.Public()
	publicKeyECDSA, _ := publicKey.(*ecdsa.PublicKey)
	fromAddress := crypto.PubkeyToAddress(*publicKeyECDSA)

	nonce, err := client.PendingNonceAt(context.Background(), fromAddress)
	if err != nil {
		return fmt.Errorf("failed to get nonce: %v", err)
	}

	logger.Info("got nonce: %d", nonce)

	gasPrice, err := client.SuggestGasPrice(context.Background())
	if err != nil {
		return fmt.Errorf("failed to get gas price: %v", err)
	}

	logger.Info("got gas price: %d", gasPrice)
	auth, err := bind.NewKeyedTransactorWithChainID(privateKey, big.NewInt(1337)) // geth --dev default
	if err != nil {
		return fmt.Errorf("failed to create auth: %v", err)
	}
	auth.Nonce = big.NewInt(int64(nonce))
	auth.Value = big.NewInt(0)
	auth.GasLimit = uint64(300000)
	auth.GasPrice = gasPrice

	logger.Info("deploying proportional rewards")
	address, tx, _, err := gen.DeployProportionalRewards(auth, client)
	if err != nil {
		return fmt.Errorf("failed to deploy proportional rewards: %v", err)
	}

	logger.Infof("ProportionalRewards deployed to: %s", address.Hex())
	logger.Infof("Transaction hash: %s", tx.Hash().Hex())

	return nil
}
