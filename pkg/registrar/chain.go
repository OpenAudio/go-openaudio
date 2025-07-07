package registrar

import (
	"os"
	"strings"

	"github.com/AudiusProject/audiusd/pkg/httputil"
	"github.com/AudiusProject/audiusd/pkg/mediorum/ethcontracts"
)

func NewEthChainProvider() PeerProvider {
	return &ethChainProvider{}
}

type ethChainProvider struct {
}

func (p *ethChainProvider) Peers() ([]Peer, error) {
	serviceProviders, err := ethcontracts.GetServiceProviderList("content-node")
	if err != nil {
		return nil, err
	}
	peers := make([]Peer, len(serviceProviders))
	for i, sp := range serviceProviders {
		peers[i] = Peer{
			Host:   httputil.RemoveTrailingSlash(strings.ToLower(sp.Endpoint)),
			Wallet: strings.ToLower(sp.DelegateOwnerWallet),
		}
	}
	return peers, nil
}

func (p *ethChainProvider) Signers() ([]Peer, error) {
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
	signers := make([]Peer, len(serviceProviders))
	for i, sp := range serviceProviders {
		signers[i] = Peer{
			Host:   httputil.RemoveTrailingSlash(strings.ToLower(sp.Endpoint)),
			Wallet: strings.ToLower(sp.DelegateOwnerWallet),
		}
	}
	return signers, nil
}
