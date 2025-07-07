package registrar

type staticProvider struct {
	peers   []Peer
	signers []Peer
}

func NewStatic(peers, signers []Peer) PeerProvider {
	return &staticProvider{peers, signers}
}

func (p *staticProvider) Peers() ([]Peer, error) {
	return p.peers, nil
}

func (p *staticProvider) Signers() ([]Peer, error) {
	return p.signers, nil
}
