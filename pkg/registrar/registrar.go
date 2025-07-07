package registrar

type Peer struct {
	Host   string `json:"host"`
	Wallet string `json:"wallet"`
}

type PeerProvider interface {
	Peers() ([]Peer, error)
	Signers() ([]Peer, error)
}
