package processors

import (
	"context"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/jackc/pgx/v5/pgtype"
)

type manageEntityProcessor struct{}

func (p *manageEntityProcessor) TxType() string { return TxTypeManageEntity }

func (p *manageEntityProcessor) Process(ctx context.Context, tx *corev1.SignedTransaction, txCtx *TxContext, q *db.Queries) (*Result, error) {
	me := tx.GetManageEntity()
	if err := q.InsertAddress(ctx, db.InsertAddressParams{
		Address:              me.GetSigner(),
		PubKey:               nil,
		FirstSeenBlockHeight: pgtype.Int8{Int64: txCtx.Block.Height, Valid: true},
		CreatedAt:            txCtx.BlockTime,
	}); err != nil {
		return nil, err
	}

	if err := q.InsertManageEntity(ctx, db.InsertManageEntityParams{
		Address:     me.GetSigner(),
		EntityType:  me.GetEntityType(),
		EntityID:    me.GetEntityId(),
		Action:      me.GetAction(),
		Metadata:    pgtype.Text{String: me.GetMetadata(), Valid: me.GetMetadata() != ""},
		Signature:   me.GetSignature(),
		Signer:      me.GetSigner(),
		Nonce:       me.GetNonce(),
		BlockHeight: txCtx.Block.Height,
		TxHash:      txCtx.TxHash,
		CreatedAt:   txCtx.BlockTime,
	}); err != nil {
		return nil, err
	}

	txCtx.InsertTx.TxType = TxTypeManageEntity
	txCtx.InsertTx.Address = pgtype.Text{String: me.GetSigner(), Valid: true}
	return &Result{InsertTx: txCtx.InsertTx}, nil
}

// ManageEntity returns the manage_entity processor.
func ManageEntity() Processor { return &manageEntityProcessor{} }
