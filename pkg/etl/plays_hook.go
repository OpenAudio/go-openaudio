package etl

import (
	"context"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
	"go.uber.org/zap"
)

// PlaysParams holds the context for a single Plays transaction, passed to
// every registered PlaysHook after the play processor has written the
// etl_plays rows.
//
// Plays do not flow through the entity-manager dispatcher (they have no
// ManageEntity envelope and no metadata), so they cannot use the
// em.PostHook mechanism. PlaysHook is the parallel extension point for the
// Plays tx type: it hands consumers the decoded TrackPlay slice plus the
// same DB transaction (DBTX) the processor wrote on, so a consumer can
// write a derived row in the same savepoint atomically with etl_plays.
type PlaysParams struct {
	// Plays is the decoded list of plays in this transaction. May be empty
	// for a plays tx that carried no entries (the processor no-ops those,
	// and so should hooks).
	Plays []*corev1.TrackPlay

	// BlockHeight is the Core block height the plays were indexed at.
	BlockHeight int64

	// BlockTime is the block timestamp.
	BlockTime time.Time

	// BlockHash is the Core block hash.
	BlockHash string

	// TxHash is the hash of the Plays transaction.
	TxHash string

	// DBTX is the savepoint-scoped transaction the play processor wrote
	// etl_plays on. A hook writing via this handle commits or rolls back
	// atomically with the rest of the tx.
	DBTX db.DBTX

	// Logger is the indexer logger.
	Logger *zap.Logger
}

// Queries returns a sqlc Queries handle bound to the hook's DBTX, for
// consumers that want to use generated queries rather than raw SQL.
func (p *PlaysParams) Queries() *db.Queries {
	return db.New(p.DBTX)
}

// PlaysHook fires after the play processor has successfully written the
// etl_plays rows for a Plays transaction. It receives the decoded plays and
// the same DB transaction the processor used (Params.DBTX), so a consumer
// can write a derived row in the same savepoint.
//
// Errors returned from a hook are logged but do NOT roll back the play
// processor's etl_plays write or fail the surrounding block — this matches
// the em.PostHook contract and prevents a buggy consumer-side hook (or a
// deterministic bad-data error) from halting the indexer. Transient infra
// failures that must be retried should instead surface through the block
// transaction, which is reprocessed on the next pass.
//
// Multiple hooks may be registered; they run in registration order.
type PlaysHook func(ctx context.Context, params *PlaysParams) error
