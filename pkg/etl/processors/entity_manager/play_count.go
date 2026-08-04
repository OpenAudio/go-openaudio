package entity_manager

import (
	"context"
	"sync"
)

// playCountReconcileHandler applies a play-count delta to aggregate_plays.
//
// aggregate_plays is owned by the consumer, not by pkg/etl: migration 0017 is a
// no-op stub precisely because those derived tables are maintained downstream
// (via triggers) rather than here. The table is therefore present in some
// databases and absent in others, and a Reconcile transaction must not be fatal
// where it is absent — an undefined relation is not a ValidationError, so it
// would roll back and fail the surrounding block rather than being skipped,
// stalling the indexer on every retry.
type playCountReconcileHandler struct {
	once   sync.Once
	exists bool
}

func (h *playCountReconcileHandler) EntityType() string { return EntityTypePlayCount }
func (h *playCountReconcileHandler) Action() string     { return ActionReconcile }

func (h *playCountReconcileHandler) Handle(ctx context.Context, params *Params) error {
	delta, ok := params.MetadataInt64("delta")
	if !ok {
		return NewValidationError("delta is required for PlayCount/Reconcile")
	}

	if !h.aggregateTableExists(ctx, params) {
		// Nothing owns the rollup in this database. The play rows themselves are
		// still indexed, so drop the delta rather than failing the block.
		return nil
	}

	// Upsert the delta into aggregate_plays. During genesis migration this
	// sets the base count before individual plays are replayed.
	_, err := params.DBTX.Exec(ctx, `
		INSERT INTO aggregate_plays (play_item_id, count)
		VALUES ($1, $2)
		ON CONFLICT (play_item_id) DO UPDATE
		SET count = aggregate_plays.count + $2
	`, params.EntityID, delta)
	return err
}

// aggregateTableExists resolves once per process. A catalog lookup per
// transaction would be wasteful across the ~1.4M Reconcile transactions a
// genesis migration emits, and the table is not expected to appear or disappear
// mid-run. to_regclass returns NULL rather than raising when the relation is
// absent, so this is safe to run inside the caller's transaction — a raising
// probe would poison it.
func (h *playCountReconcileHandler) aggregateTableExists(ctx context.Context, params *Params) bool {
	h.once.Do(func() {
		var name *string
		if err := params.DBTX.QueryRow(ctx,
			"SELECT to_regclass('aggregate_plays')::text").Scan(&name); err != nil {
			// Treat an unreadable catalog as absent: skipping a rollup update is
			// recoverable, aborting the block is not.
			h.exists = false
			return
		}
		h.exists = name != nil
		if !h.exists && params.Logger != nil {
			params.Logger.Warn("aggregate_plays not present; PlayCount/Reconcile deltas will be skipped")
		}
	})
	return h.exists
}

func PlayCountReconcile() Handler { return &playCountReconcileHandler{} }
