// Keeps the validators updated in cometbft and core up to date with what is present on the ethereum node registry.
package server

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/AudiusProject/audiusd/pkg/core/common"
	"github.com/AudiusProject/audiusd/pkg/core/contracts"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_openapi/protocol"
	"github.com/AudiusProject/audiusd/pkg/core/gen/core_proto"
	"github.com/AudiusProject/audiusd/pkg/logger"
	"github.com/cometbft/cometbft/crypto/ed25519"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	geth "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
)

func (s *Server) startRegistryBridge() error {
	if s.isDevEnvironment() {
		s.logger.Info("running in dev, registering on ethereum")
		if err := s.registerSelfOnEth(); err != nil {
			return fmt.Errorf("error registering onto eth: %v", err)
		}
		s.gatherEthNodes()
	}

	<-s.awaitEthNodesReady
	<-s.awaitRpcReady
	s.logger.Info("starting registry bridge")

	// check eth status
	_, err := s.eth.ChainID(context.Background())
	if err != nil {
		return fmt.Errorf("init registry bridge failed eth chain id: %v", err)
	}

	// check comet status
	_, err = s.rpc.Status(context.Background())
	if err != nil {
		return fmt.Errorf("init registry bridge failed comet rpc status: %v", err)
	}

	if err := s.awaitNodeCatchup(context.Background()); err != nil {
		return err
	}

	delay := 2 * time.Second
	maxTime := 120 * time.Minute
	startTime := time.Now()

	for time.Since(startTime) < maxTime {
		if err := s.RegisterSelf(); err != nil {
			s.logger.Errorf("node registration failed, will try again: %v", err)
			s.logger.Infof("Retrying registration in %s", delay)
			time.Sleep(delay)
			delay *= 2
		} else {
			return nil
		}
	}

	s.logger.Warn("exhausted registration retries after 120 minutes")
	return nil
}

// checks mainnet eth for itself, if registered and not
// already in the comet state will register itself on comet
func (s *Server) RegisterSelf() error {
	ctx := context.Background()

	if s.isSelfAlreadyRegistered(ctx) {
		s.logger.Info("Skipping registration, we are already registered.")
		return nil
	}

	nodeAddress := crypto.PubkeyToAddress(s.config.EthereumKey.PublicKey)
	nodeEndpoint := s.config.NodeEndpoint

	isRegistered, err := s.isSelfRegisteredOnEth()
	if err != nil {
		return fmt.Errorf("could not check ethereum registration status: %v", err)
	}
	if !isRegistered {
		s.logger.Infof("node %s : %s not registered on Ethereum", nodeAddress.Hex(), nodeEndpoint)
		logger.Info("continuing unregistered")
		return nil
	}

	spf, err := s.contracts.GetServiceProviderFactoryContract()
	if err != nil {
		return fmt.Errorf("could not get service provider factory: %v", err)
	}

	spID, err := spf.GetServiceProviderIdFromEndpoint(nil, nodeEndpoint)
	if err != nil {
		return fmt.Errorf("issue getting sp data: %v", err)
	}

	serviceType, err := contracts.ServiceType(s.config.NodeType)
	if err != nil {
		return fmt.Errorf("invalid node type: %v", err)
	}

	info, err := spf.GetServiceEndpointInfo(nil, serviceType, spID)
	if err != nil {
		return fmt.Errorf("could not get service endpoint info: %v", err)
	}

	if info.DelegateOwnerWallet != nodeAddress {
		return fmt.Errorf("node %s is claiming to be %s but that endpoint is owned by %s", nodeAddress.Hex(), nodeEndpoint, info.DelegateOwnerWallet.Hex())
	}

	nodeRecord, err := s.db.GetNodeByEndpoint(ctx, nodeEndpoint)
	if errors.Is(err, pgx.ErrNoRows) {
		s.logger.Infof("node %s not found on comet but found on eth, registering", nodeEndpoint)
		if err := s.registerSelfOnComet(ctx, info.DelegateOwnerWallet, info.BlockNumber, spID.String()); err != nil {
			return fmt.Errorf("could not register on comet: %v", err)
		}
		return nil
	} else if err != nil {
		return err
	}

	s.logger.Infof("node %s : %s registered on network %s", nodeRecord.EthAddress, nodeRecord.Endpoint, s.config.Environment)
	return nil
}

func (s *Server) isDevEnvironment() bool {
	return s.config.Environment == "dev" || s.config.Environment == "sandbox"
}

func (s *Server) registerSelfOnComet(ctx context.Context, delegateOwnerWallet geth.Address, ethBlock *big.Int, spID string) error {
	if err := s.isDuplicateDelegateOwnerWallet(s.config.WalletAddress); err != nil {
		s.logger.Errorf("node is a duplicate, not registering on comet: %s", s.config.WalletAddress)
		return nil
	}

	if s.cache.catchingUp.Load() {
		return errors.New("Aborting comet registration because node is still syncing.")
	}

	genValidators := s.config.GenesisFile.Validators
	isGenValidator := false
	for _, validator := range genValidators {
		if validator.Address.String() == s.config.ProposerAddress {
			isGenValidator = true
			break
		}
	}

	peers := s.GetPeers()
	noPeers := len(peers) == 0

	if !isGenValidator && noPeers {
		return fmt.Errorf("not in genesis and no peers, retrying to register on comet later")
	}

	serviceType, err := contracts.ServiceType(s.config.NodeType)
	if err != nil {
		return fmt.Errorf("invalid node type: %v", err)
	}

	addrs, err := s.db.GetAllEthAddressesOfRegisteredNodes(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("failed to get all registered nodes: %v", err)
	}
	keyBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(keyBytes, ethBlock.Uint64())
	rendezvous := common.GetAttestorRendezvous(addrs, keyBytes, s.config.AttRegistrationRSize)

	attestations := make([]string, 0, s.config.AttRegistrationRSize)
	reg := &core_proto.ValidatorRegistration{
		CometAddress:   s.config.ProposerAddress,
		PubKey:         s.config.CometKey.PubKey().Bytes(),
		Power:          int64(s.config.ValidatorVotingPower),
		DelegateWallet: delegateOwnerWallet.Hex(),
		Endpoint:       s.config.NodeEndpoint,
		NodeType:       common.HexToUtf8(serviceType),
		EthBlock:       ethBlock.Int64(),
		SpId:           spID,
		Deadline:       s.cache.currentHeight.Load() + 120,
	}
	params := protocol.NewProtocolGetRegistrationAttestationParams()
	params.SetRegistration(common.ValidatorRegistrationIntoOapi(reg))
	for addr, _ := range rendezvous {
		if peer, ok := peers[addr]; ok {
			resp, err := peer.ProtocolGetRegistrationAttestation(params)
			if err != nil {
				s.logger.Error("failed to get registration attestation from %s: %v", peer.OAPIEndpoint, err)
				continue
			}
			attestations = append(attestations, resp.Payload.Signature)
		}
	}

	registrationAtt := &core_proto.Attestation{
		Signatures: attestations,
		Body:       &core_proto.Attestation_ValidatorRegistration{reg},
	}

	txBytes, err := proto.Marshal(registrationAtt)
	if err != nil {
		return fmt.Errorf("failure to marshal register tx: %v", err)
	}

	sig, err := common.EthSign(s.config.EthereumKey, txBytes)
	if err != nil {
		return fmt.Errorf("could not sign register tx: %v", err)
	}

	tx := &core_proto.SignedTransaction{
		Signature: sig,
		RequestId: uuid.NewString(),
		Transaction: &core_proto.SignedTransaction_Attestation{
			Attestation: registrationAtt,
		},
	}

	txreq := &core_proto.SendTransactionRequest{
		Transaction: tx,
	}

	txhash, err := s.SendTransaction(context.Background(), txreq)
	if err != nil {
		return fmt.Errorf("send register tx failed: %v", err)
	}

	s.logger.Infof("registered node %s in tx %s", s.config.NodeEndpoint, txhash)

	return nil
}

func (s *Server) awaitNodeCatchup(ctx context.Context) error {
	retries := 60
	for tries := retries; tries >= 0; tries-- {
		res, err := s.rpc.Status(ctx)
		if err != nil {
			s.logger.Errorf("error getting comet health: %v", err)
			time.Sleep(10 * time.Second)
			continue
		}

		if res.SyncInfo.CatchingUp {
			s.logger.Infof("comet catching up: latest seen block %d", res.SyncInfo.LatestBlockHeight)
			time.Sleep(10 * time.Second)
			continue
		}

		// no health error nor catching up
		return nil
	}
	return errors.New("timeout waiting for comet to catch up")
}

func (s *Server) isSelfAlreadyRegistered(ctx context.Context) bool {
	res, err := s.db.GetNodeByEndpoint(ctx, s.config.NodeEndpoint)

	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}

	if err != nil {
		s.logger.Errorf("error getting registered nodes: %v", err)
		return false
	}

	// return if owner wallets match
	return res.EthAddress == s.config.WalletAddress
}

func (s *Server) isSelfRegisteredOnEth() (bool, error) {
	_, err := s.getRegisteredNode(s.config.NodeEndpoint)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) registerSelfOnEth() error {
	s.logger.Info("Attempting to register self on ethereum")
	chainID, err := s.contracts.Rpc.ChainID(context.Background())
	if err != nil {
		return fmt.Errorf("could not get chain id: %v", err)
	}

	opts, err := bind.NewKeyedTransactorWithChainID(s.config.EthereumKey, chainID)
	if err != nil {
		return fmt.Errorf("could not create keyed transactor: %v", err)
	}

	token, err := s.contracts.GetAudioTokenContract()
	if err != nil {
		return fmt.Errorf("could not get token contract: %v", err)
	}

	spf, err := s.contracts.GetServiceProviderFactoryContract()
	if err != nil {
		return fmt.Errorf("could not get service provider factory contract: %v", err)
	}

	alreadyRegistered := false
	addr := geth.HexToAddress(s.config.WalletAddress)
	spIDs, err := spf.GetServiceProviderIdsFromAddress(nil, addr, contracts.DiscoveryNode)
	if err != nil {
		return fmt.Errorf("could not get spIDs from address: %v", err)
	}
	serviceType, err := contracts.ServiceType(s.config.NodeType)
	if err != nil {
		return fmt.Errorf("invalid node type: %v", err)
	}

	for _, spID := range spIDs {
		info, err := spf.GetServiceEndpointInfo(nil, serviceType, spID)
		if err != nil {
			return fmt.Errorf("could not get service endpoint info: %v", err)
		}
		if info.Endpoint == s.config.NodeEndpoint {
			alreadyRegistered = true
		}
	}

	if alreadyRegistered {
		s.logger.Info("node already registered on eth!")
		return nil
	}

	stakingAddress, err := spf.GetStakingAddress(nil)
	if err != nil {
		return fmt.Errorf("could not get staking address: %v", err)
	}

	decimals := 18
	stake := new(big.Int).Mul(big.NewInt(200000), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))

	_, err = token.Approve(opts, stakingAddress, stake)
	if err != nil {
		return fmt.Errorf("could not approve tokens: %v", err)
	}

	endpoint := s.config.NodeEndpoint
	delegateOwnerWallet := crypto.PubkeyToAddress(s.config.EthereumKey.PublicKey)

	_, err = spf.Register(opts, serviceType, endpoint, stake, delegateOwnerWallet)
	if err != nil {
		return fmt.Errorf("couldn't register node: %v", err)
	}

	s.logger.Infof("node %s registered on eth", endpoint)

	return nil
}

func (s *Server) getRegisteredNode(endpoint string) (*contracts.Node, error) {
	s.ethNodeMU.RLock()
	defer s.ethNodeMU.RUnlock()
	for _, node := range s.ethNodes {
		if node.Endpoint == endpoint {
			return node, nil
		}
	}
	return nil, fmt.Errorf("node not found: %s", endpoint)
}

func (s *Server) isDuplicateDelegateOwnerWallet(delegateOwnerWallet string) error {
	s.ethNodeMU.RLock()
	defer s.ethNodeMU.RUnlock()

	for _, node := range s.duplicateEthNodes {
		if node.DelegateOwnerWallet.Hex() == delegateOwnerWallet {
			return fmt.Errorf("delegateOwnerWallet %s duplicated, invalid registration", delegateOwnerWallet)
		}
	}

	return nil
}

func (s *Server) deregisterMissingNode(ctx context.Context, ethAddress string) {
	node, err := s.db.GetRegisteredNodeByEthAddress(ctx, ethAddress)
	if err != nil {
		s.logger.Error("could not deregister missing node", "address", ethAddress, "error", err)
		return
	}

	addrs, err := s.db.GetAllEthAddressesOfRegisteredNodes(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Error("could not deregister node: failed to get currently registered nodes", "address", ethAddress, "error", err)
		return
	}
	pubKey := ed25519.PubKey(node.CometPubKey)
	rendezvous := common.GetAttestorRendezvous(addrs, pubKey.Bytes(), s.config.AttDeregistrationRSize)
	attestations := make([]string, 0, s.config.AttRegistrationRSize)
	dereg := &core_proto.ValidatorDeregistration{
		CometAddress: s.config.ProposerAddress,
		PubKey:       s.config.CometKey.PubKey().Bytes(),
		Deadline:     s.cache.currentHeight.Load() + 120,
	}

	peers := s.GetPeers()
	params := protocol.NewProtocolGetDeregistrationAttestationParams()
	params.SetDeregistration(common.ValidatorDeregistrationIntoOapi(dereg))
	for addr, _ := range rendezvous {
		if peer, ok := peers[addr]; ok {
			resp, err := peer.ProtocolGetDeregistrationAttestation(params)
			if err != nil {
				s.logger.Error("failed to get deregistration attestation from %s: %v", peer.OAPIEndpoint, err)
				continue
			}
			attestations = append(attestations, resp.Payload.Signature)
		}
	}

	deregistrationAtt := &core_proto.Attestation{
		Signatures: attestations,
		Body:       &core_proto.Attestation_ValidatorDeregistration{dereg},
	}

	txBytes, err := proto.Marshal(deregistrationAtt)
	if err != nil {
		s.logger.Error("failure to marshal deregister tx", "error", err)
		return
	}

	sig, err := common.EthSign(s.config.EthereumKey, txBytes)
	if err != nil {
		s.logger.Error("could not sign deregister tx", "error", err)
		return
	}

	tx := &core_proto.SignedTransaction{
		Signature: sig,
		RequestId: uuid.NewString(),
		Transaction: &core_proto.SignedTransaction_Attestation{
			Attestation: deregistrationAtt,
		},
	}

	txreq := &core_proto.SendTransactionRequest{
		Transaction: tx,
	}

	txhash, err := s.SendTransaction(context.Background(), txreq)
	if err != nil {
		s.logger.Error("send deregister tx failed", "error", err)
		return
	}

	s.logger.Infof("deregistered node %s in tx %s", s.config.NodeEndpoint, txhash)
}
