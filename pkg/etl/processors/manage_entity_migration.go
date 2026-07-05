package processors

import (
	"context"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"github.com/jackc/pgx/v5/pgtype"
)

type manageEntityMigrationProcessor struct{}

func (p *manageEntityMigrationProcessor) TxType() string { return TxTypeManageEntityMigration }

func (p *manageEntityMigrationProcessor) Process(ctx context.Context, tx *corev1.SignedTransaction, txCtx *TxContext, q *db.Queries) (*Result, error) {
	me := tx.GetManageEntityMigration()
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

	txCtx.InsertTx.TxType = TxTypeManageEntityMigration
	txCtx.InsertTx.Address = pgtype.Text{String: me.GetSigner(), Valid: true}
	return &Result{InsertTx: txCtx.InsertTx}, nil
}

// ManageEntityMigration returns the manage_entity_migration processor.
func ManageEntityMigration() Processor { return &manageEntityMigrationProcessor{} }
