package entity_manager

import "context"

type playCountReconcileHandler struct{}

func (h *playCountReconcileHandler) EntityType() string { return EntityTypePlayCount }
func (h *playCountReconcileHandler) Action() string     { return ActionReconcile }

func (h *playCountReconcileHandler) Handle(ctx context.Context, params *Params) error {
	delta, ok := params.MetadataInt64("delta")
	if !ok {
		return NewValidationError("delta is required for PlayCount/Reconcile")
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

func PlayCountReconcile() Handler { return &playCountReconcileHandler{} }
