package processors

import (
	"context"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/etl/db"
	"github.com/jackc/pgx/v5/pgtype"
)

// Transaction type constants. Correspond to proto SignedTransaction union members.
const (
	TxTypePlay                       = "play"
	TxTypeManageEntity               = "manage_entity"
	TxTypeValidatorRegistration      = "validator_registration"
	TxTypeValidatorDeregistration    = "validator_deregistration"
	TxTypeValidatorRegistrationLegacy = "validator_registration_legacy"
	TxTypeSlaRollup                  = "sla_rollup"
	TxTypeValidatorMisbehaviorDereg  = "validator_misbehavior_deregistration"
	TxTypeStorageProof               = "storage_proof"
	TxTypeStorageProofVerification   = "storage_proof_verification"
	TxTypeRelease                    = "release"
)

// TxContext holds block and transaction metadata for processing.
type TxContext struct {
	Block      *corev1.Block
	TxHash     string
	TxIndex    int
	BlockTime  pgtype.Timestamp
	InsertTx   db.InsertTransactionParams
}

// Result is returned by processors. InsertTx is always set; the processor
// may perform additional entity inserts via the db.
type Result struct {
	InsertTx db.InsertTransactionParams
}

// Processor processes a specific transaction type.
type Processor interface {
	// TxType returns the transaction type string (e.g. "play", "manage_entity").
	TxType() string
	// Process handles the transaction and returns InsertTransactionParams for etl_transactions.
	Process(ctx context.Context, tx *corev1.SignedTransaction, txCtx *TxContext, q *db.Queries) (*Result, error)
}

// DefaultRegistry returns the default set of processors.
func DefaultRegistry() []Processor {
	return []Processor{
		Play(),
		ManageEntity(),
	}
}
