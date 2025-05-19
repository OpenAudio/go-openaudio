package registrar

import (
	"os"
	"strings"

	"github.com/AudiusProject/audiusd/pkg/httputil"
	"github.com/AudiusProject/audiusd/pkg/mediorum/ethcontracts"

	"github.com/AudiusProject/audiusd/pkg/mediorum/server"
)

func NewEthChainProvider() PeerProvider {
	return &ethChainProvider{}
}

type ethChainProvider struct {
}

func (p *ethChainProvider) Peers() ([]server.Peer, error) {
	serviceProviders, err := ethcontracts.GetServiceProviderList("content-node")
	if err != nil {
		return nil, err
	}
	peers := make([]server.Peer, len(serviceProviders))
	for i, sp := range serviceProviders {
		peers[i] = server.Peer{
			Host:   httputil.RemoveTrailingSlash(strings.ToLower(sp.Endpoint)),
			Wallet: strings.ToLower(sp.DelegateOwnerWallet),
		}
	}
	return peers, nil
}

func (p *ethChainProvider) Signers() ([]server.Peer, error) {
	serviceProviders, err := ethcontracts.GetServiceProviderList("discovery-node")
	if err != nil {
		return nil, err
	}
	if os.Getenv("MEDIORUM_ENV") == "dev" {
		additionalServiceProviders, err := ethcontracts.GetServiceProviderList("content-node")
		if err != nil {
			return nil, err
		}
		serviceProviders = append(serviceProviders, additionalServiceProviders...)
	}
	signers := make([]server.Peer, len(serviceProviders))
	for i, sp := range serviceProviders {
		signers[i] = server.Peer{
			Host:   httputil.RemoveTrailingSlash(strings.ToLower(sp.Endpoint)),
			Wallet: strings.ToLower(sp.DelegateOwnerWallet),
		}
	}
	return signers, nil
}
