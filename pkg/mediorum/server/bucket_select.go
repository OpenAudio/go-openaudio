package server

import (
	"gocloud.dev/blob"
	"golang.org/x/exp/slices"
)

// bucketForCID returns the bucket that should hold this CID on this node.
//
// A CID is routed to archiveBucket only when all of:
//   - archiveBucket is configured
//   - StoreAll is enabled (otherwise the node never holds archive content)
//   - the CID is not in an explicit placementHosts list (placement always
//     uses the primary bucket; placement implies the CID is required, not archive)
//   - this node's rendezvous rank for the CID is >= ReplicationFactor, i.e. the
//     only reason this node holds the CID is StoreAll
//
// Otherwise the primary bucket is returned. When archiveBucket is unset, this
// always returns the primary bucket — preserving current behavior.
func (ss *MediorumServer) bucketForCID(cid string, placementHosts []string) *blob.Bucket {
	if ss.archiveBucket == nil {
		return ss.bucket
	}
	if !ss.Config.StoreAll {
		return ss.bucket
	}
	if len(placementHosts) > 0 {
		// explicit placement: never archive
		return ss.bucket
	}
	orderedHosts := ss.rendezvousHasher.Rank(cid)
	myRank := slices.Index(orderedHosts, ss.Config.Self.Host)
	if myRank >= 0 && myRank < ss.Config.ReplicationFactor {
		return ss.bucket
	}
	return ss.archiveBucket
}

// isArchiveCID reports whether the given CID would be routed to the archive bucket
// on this node. Used by callers that need to branch on archive-ness without
// taking the bucket itself (e.g. repair counters, disk-space gating).
func (ss *MediorumServer) isArchiveCID(cid string, placementHosts []string) bool {
	return ss.archiveBucket != nil && ss.bucketForCID(cid, placementHosts) == ss.archiveBucket
}
