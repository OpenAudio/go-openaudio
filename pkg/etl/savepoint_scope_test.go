package etl

import (
	"context"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

func migrationTx() *corev1.Transaction {
	return &corev1.Transaction{Transaction: &corev1.SignedTransaction{
		Transaction: &corev1.SignedTransaction_ManageEntityMigration{
			ManageEntityMigration: &corev1.ManageEntityLegacyMigration{},
		},
	}}
}

func liveTx() *corev1.Transaction {
	return &corev1.Transaction{Transaction: &corev1.SignedTransaction{
		Transaction: &corev1.SignedTransaction_ManageEntity{
			ManageEntity: &corev1.ManageEntityLegacy{},
		},
	}}
}

// Only a block made entirely of migration transactions may skip savepoints. A
// live or mixed block keeps per-tx isolation, because there one bad handler
// must not discard its neighbours' writes.
func TestMigrationOnlyBlock(t *testing.T) {
	for _, tt := range []struct {
		name string
		txs  []*corev1.Transaction
		want bool
	}{
		{"all migration", []*corev1.Transaction{migrationTx(), migrationTx()}, true},
		{"all live", []*corev1.Transaction{liveTx()}, false},
		{"mixed", []*corev1.Transaction{migrationTx(), liveTx()}, false},
		{"empty", nil, false},
		{"nil inner tx", []*corev1.Transaction{{}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := migrationOnlyBlock(&corev1.Block{Transactions: tt.txs})
			if got != tt.want {
				t.Errorf("migrationOnlyBlock = %v, want %v", got, tt.want)
			}
		})
	}
}

// Without a savepoint there is nothing to roll back to, so a rollback request
// must mark the block for a savepointed retry rather than silently continuing
// on a poisoned transaction.
func TestTxScopeRollbackMarksPoisoned(t *testing.T) {
	s := &txScope{nested: false}
	s.Rollback(context.Background())
	if !s.poisoned {
		t.Error("savepoint-free rollback must poison the block so it is retried")
	}
}
