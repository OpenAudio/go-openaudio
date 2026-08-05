package server

import (
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

// Migration transactions are written straight into the genesis block range and
// must never be accepted from the mempool: the indexer routes them through a
// relaxed handler set and takes created_at from their metadata, so an accepted
// one would let a peer write entity state under replay rules with a timestamp
// of its choosing.
func TestCheckTxRejectsManageEntityMigration(t *testing.T) {
	tx := &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_ManageEntityMigration{
			ManageEntityMigration: &v1.ManageEntityLegacyMigration{
				UserId: 1, EntityType: "Track", Action: "Create",
				Metadata: `{"data":{"created_at":"2019-01-01T00:00:00Z"}}`,
			},
		},
	}
	if err := validateSignedTransactionForCheckTx(tx); err == nil {
		t.Fatal("expected CheckTx to reject a submitted migration transaction, got nil")
	}
}

// The non-migration entity path must stay acceptable.
func TestCheckTxAcceptsManageEntity(t *testing.T) {
	tx := &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_ManageEntity{
			ManageEntity: &v1.ManageEntityLegacy{
				UserId: 1, EntityType: "Track", Action: "Create",
			},
		},
	}
	if err := validateSignedTransactionForCheckTx(tx); err != nil {
		t.Fatalf("expected normal manage entity to pass CheckTx, got %v", err)
	}
}
