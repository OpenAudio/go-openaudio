package server

import (
	"golang.org/x/exp/slices"
)

func (ss *MediorumServer) rendezvousAllHosts(key string) ([]string, bool) {
	orderedHosts := ss.rendezvousHasher.Rank(key)

	myRank := slices.Index(orderedHosts, ss.Config.Self.Host)
	isMine := myRank >= 0 && myRank < ss.Config.ReplicationFactor

	if ss.Config.StoreAll {
		isMine = true
	}
	return orderedHosts, isMine
}
