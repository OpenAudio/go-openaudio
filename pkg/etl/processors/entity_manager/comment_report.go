package entity_manager

import (
	"context"
)

type commentReportHandler struct{}

func (h *commentReportHandler) EntityType() string { return EntityTypeComment }
func (h *commentReportHandler) Action() string     { return ActionReport }

func (h *commentReportHandler) Handle(ctx context.Context, params *Params) error {
	if err := ValidateSigner(ctx, params); err != nil {
		return err
	}
	// Check for duplicate report
	dup, err := commentReportExists(ctx, params.DBTX, params.UserID, params.EntityID)
	if err != nil {
		return err
	}
	if dup {
		return NewValidationError("user %d already reported comment %d", params.UserID, params.EntityID)
	}

	_, err = params.DBTX.Exec(ctx, `
		INSERT INTO comment_reports (
			comment_id, user_id, created_at, updated_at, is_delete,
			txhash, blockhash, blocknumber
		) VALUES ($1, $2, $3, $3, false, $4, $5, $6)
	`, params.EntityID, params.UserID, params.BlockTime, params.TxHash, params.BlockHash, params.BlockNumber)
	return err
}

// CommentReport returns the Comment Report handler.
func CommentReport() Handler { return &commentReportHandler{} }
