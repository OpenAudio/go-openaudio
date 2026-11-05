package config

// Nodes gossip their mempool to every node registered on their own chain
// through ForwardTransaction, and transactions carry no chain of their own. A
// node that moves to another chain while still registered on the old one (as
// every node does during the genesis rollover, until it is deregistered) is
// therefore fed the old chain's traffic, and its new-chain mempool admits it.
// Forwarders stamp their chain ID on the request so receivers can refuse it.
//
// A mismatched chain ID is refused everywhere. An empty one is what a node too
// old to stamp sends, so refusing it is only safe where every node on the
// chain is known to stamp: networks that start on a release that already
// does, or ones whose whole fleet has since upgraded. Elsewhere refusing it
// would throttle gossip during a rolling upgrade, as upgraded nodes turned
// away forwards from peers that had not upgraded yet.
//
// This is mempool admission, not consensus, so it is keyed by chain rather
// than by height and can change between releases without coordination.
var forwardChainIDRequired = map[string]bool{
	// dev and sandbox: every node runs the same image.
	"openaudio-devnet": true,
	"audius-devnet":    true,
	// prod, audius-mainnet-beta: every validator joins on a release that
	// stamps, and the nodes still on audius-mainnet-alpha-beta do not, which
	// is exactly the traffic to refuse.
	"audius-mainnet-beta": true,
}

// ForwardChainIDRequired reports whether ForwardTransaction on this chain
// refuses requests that carry no chain ID.
func ForwardChainIDRequired(chainID string) bool {
	return forwardChainIDRequired[chainID]
}
