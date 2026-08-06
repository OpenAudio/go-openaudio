package server

import (
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

// The CheckTx-level rejection of migration transactions guards only
// CometBFT's mempool, which PrepareProposal never builds blocks from. The
// custom mempool's chokepoint must refuse them too, or a migration
// transaction submitted through SendTransaction/ForwardTransaction reaches
// live blocks and executes under the relaxed replay handlers with a
// submitter-chosen created_at.
func TestMempoolRejectsManageEntityMigration(t *testing.T) {
	tx := &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_ManageEntityMigration{
			ManageEntityMigration: &v1.ManageEntityLegacyMigration{
				UserId: 1, EntityType: "Track", Action: "Create",
				Metadata: `{"data":{"created_at":"2019-01-01T00:00:00Z"}}`,
			},
		},
	}
	if err := mempoolAdmissible(tx); err == nil {
		t.Fatal("expected the mempool to reject a migration transaction, got nil")
	}
}

// Ordinary transactions (and the v2 path, which carries no v1 payload) must
// stay admissible.
func TestMempoolAdmitsOrdinaryTransactions(t *testing.T) {
	em := &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_ManageEntity{
			ManageEntity: &v1.ManageEntityLegacy{UserId: 1, EntityType: "Track", Action: "Create"},
		},
	}
	if err := mempoolAdmissible(em); err != nil {
		t.Fatalf("expected ordinary manage entity to be admissible, got %v", err)
	}
	if err := mempoolAdmissible(nil); err != nil {
		t.Fatalf("expected nil v1 payload (v2 tx) to be admissible, got %v", err)
	}
}
