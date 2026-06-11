package crudr

import (
	"context"
	"time"

	"github.com/oklog/ulid/v2"
	"go.uber.org/zap"
)

const (
	SweepLastScannedULIDHeader = "X-Crud-Sweep-Last-Scanned-Ulid"
	SweepLimitedHeader         = "X-Crud-Sweep-Limited"

	// Retention-gap signal: when a caller sweeps from a cursor below this
	// node's oldest available op (because older ops have been pruned),
	// ServeCrudSweep advertises the lowest ULID still available so the peer
	// can advance its cursor across the gap explicitly instead of silently
	// skipping it. Clients that don't understand these headers ignore them.
	HeaderRetentionGap = "X-Mediorum-Retention-Gap"
	HeaderAvailableMin = "X-Mediorum-Available-Min-Ulid"
)

// Reject gap/cursor ULIDs outside a plausible wall-clock window: a far-future
// or epoch value from a buggy or hostile peer must not move our cursor or pin
// the prune floor.
var (
	gapULIDEarliestPlausibleTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	gapULIDClockSkewWindow       = 24 * time.Hour
)

func isValidPeerSuppliedULID(candidate string) bool {
	id, err := ulid.Parse(candidate)
	if err != nil {
		return false
	}
	t := ulid.Time(id.Time())
	if t.Before(gapULIDEarliestPlausibleTime) {
		return false
	}
	return t.Before(time.Now().Add(gapULIDClockSkewWindow))
}

// SlowestActivePeerCursor returns the lowest sweep-cursor ULID among currently
// active peers, or "" when no safe floor can be established. The ops pruner
// caps its delete cutoff at this value so it never removes ops a still-active
// peer has not yet swept.
//
// The cursors table is shared with non-peer workers (e.g. qm_fix_truncated
// records a CID under its own host) and decommissioned peers leave stale rows;
// either would otherwise pin the floor. We consider only hosts in the active
// peer set and ignore unparseable or implausible cursors. A peer that has no
// cursor yet is handled by the retention-gap signal on its next sweep, not by
// blocking pruning here.
func (c *Crudr) SlowestActivePeerCursor(ctx context.Context) string {
	c.mu.Lock()
	active := make(map[string]bool, len(c.peerClients))
	for _, p := range c.peerClients {
		active[p.Host] = true
	}
	c.mu.Unlock()

	var cursors []Cursor
	if err := c.DB.WithContext(ctx).Find(&cursors).Error; err != nil {
		c.logger.Warn("retention floor: failed to load cursors", zap.Error(err))
		return ""
	}

	floor := ""
	for _, cur := range cursors {
		if cur.Host == c.host || !active[cur.Host] {
			continue
		}
		if !isValidPeerSuppliedULID(cur.LastULID) {
			continue
		}
		if floor == "" || cur.LastULID < floor {
			floor = cur.LastULID
		}
	}
	return floor
}
