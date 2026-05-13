package server

import (
	"context"
	"net/http"
	"strings"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
	"golang.org/x/exp/slices"
)

// placementHostsHeader carries upload placement context across peer-to-peer
// /internal/blobs requests. Without it, a receiver on a StoreAll node with an
// archive bucket configured would route purely by rendezvous rank and could
// place a placement-required CID into archive while readers with placement
// context expect it in primary.
//
// The header is unsigned (no auth value to manipulating it; effect is local
// routing only) and absent on the wire means "no placement context — route by
// rank," matching pre-header behavior for backwards compatibility.
const placementHostsHeader = "X-Placement-Hosts"

func encodePlacementHosts(hosts []string) string {
	return strings.Join(hosts, ",")
}

func decodePlacementHosts(h http.Header) []string {
	v := h.Get(placementHostsHeader)
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

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
	// myRank < 0 means self isn't in the hash ring (misconfigured or
	// mid-registration); route to primary so we don't silently divert
	// everything to cold storage during a transient bad state.
	if myRank < 0 || myRank < ss.Config.ReplicationFactor {
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

// presenceCacheKey scopes a storage-key cache entry to a specific bucket.
// attrCache and knownPresent must use this — without bucket scoping, a
// presence record from one bucket can mask a true miss in the other after
// a rank flip or via placement-aware lookups.
func (ss *MediorumServer) presenceCacheKey(key string, bucket *blob.Bucket) string {
	if bucket == ss.archiveBucket {
		return "a:" + key
	}
	return "p:" + key
}

// blobAttrs reads attributes from primary first, falling back to archive on
// NotFound when archive is configured. Returns the bucket the blob was found
// in. Reads should always go through this (or readBlob/blobExists) rather
// than calling bucketForCID directly — bucketForCID is for picking write
// destinations; on reads we want hot first, archive as a tier.
//
// Error semantics: a transient/permission error from archive surfaces to the
// caller rather than being masked by the primary's NotFound, so upstream code
// distinguishes "blob doesn't exist anywhere" from "archive is unhealthy."
// Only when both buckets report NotFound do we return the primary NotFound.
func (ss *MediorumServer) blobAttrs(ctx context.Context, key string) (*blob.Attributes, *blob.Bucket, error) {
	attrs, err := ss.bucket.Attributes(ctx, key)
	if err == nil {
		return attrs, ss.bucket, nil
	}
	if ss.archiveBucket == nil || gcerrors.Code(err) != gcerrors.NotFound {
		return nil, nil, err
	}
	a2, err2 := ss.archiveBucket.Attributes(ctx, key)
	if err2 == nil {
		return a2, ss.archiveBucket, nil
	}
	if gcerrors.Code(err2) != gcerrors.NotFound {
		// Real archive failure (permission, transient I/O). Surface it so
		// callers see the actual problem instead of a misleading 404.
		return nil, nil, err2
	}
	return nil, nil, err
}

// readBlob opens a reader from primary first, falling back to archive on
// NotFound. Returns the bucket the blob was found in. Same error semantics
// as blobAttrs: archive failures surface; only both-NotFound returns
// primary's NotFound.
func (ss *MediorumServer) readBlob(ctx context.Context, key string) (*blob.Reader, *blob.Bucket, error) {
	r, err := ss.bucket.NewReader(ctx, key, nil)
	if err == nil {
		return r, ss.bucket, nil
	}
	if ss.archiveBucket == nil || gcerrors.Code(err) != gcerrors.NotFound {
		return nil, nil, err
	}
	r2, err2 := ss.archiveBucket.NewReader(ctx, key, nil)
	if err2 == nil {
		return r2, ss.archiveBucket, nil
	}
	if gcerrors.Code(err2) != gcerrors.NotFound {
		return nil, nil, err2
	}
	return nil, nil, err
}

// blobExists reports whether the blob is in either bucket.
func (ss *MediorumServer) blobExists(ctx context.Context, key string) bool {
	if ok, _ := ss.bucket.Exists(ctx, key); ok {
		return true
	}
	if ss.archiveBucket != nil {
		if ok, _ := ss.archiveBucket.Exists(ctx, key); ok {
			return true
		}
	}
	return false
}
