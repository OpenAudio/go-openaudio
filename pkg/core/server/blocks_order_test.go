package server

import (
	"slices"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/proto"
)

func manageEntityTx(action, entityType string, entityID int64, sig string) *v1.SignedTransaction {
	return &v1.SignedTransaction{
		Signature: sig,
		Transaction: &v1.SignedTransaction_ManageEntity{ManageEntity: &v1.ManageEntityLegacy{
			UserId: 1, EntityType: entityType, EntityId: entityID, Action: action, Signature: sig,
		}},
	}
}

func playTx(sig string) *v1.SignedTransaction {
	return &v1.SignedTransaction{
		Signature: sig,
		Transaction: &v1.SignedTransaction_Plays{Plays: &v1.TrackPlays{
			Plays: []*v1.TrackPlay{{UserId: "1", TrackId: "1", Signature: sig}},
		}},
	}
}

// blockRows builds GetBlocksWithTransactions rows for one block, in index order.
func blockRows(t *testing.T, height int64, txs []*v1.SignedTransaction) []db.GetBlocksWithTransactionsRow {
	t.Helper()
	if len(txs) == 0 {
		return []db.GetBlocksWithTransactionsRow{{Height: height, BlockHash: "block"}}
	}
	rows := make([]db.GetBlocksWithTransactionsRow, 0, len(txs))
	for i, tx := range txs {
		raw, err := proto.Marshal(tx)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, db.GetBlocksWithTransactionsRow{
			Height:      height,
			BlockHash:   "block",
			TxIndex:     pgtype.Int4{Int32: int32(i), Valid: true},
			TxHash:      pgtype.Text{String: tx.GetSignature(), Valid: true},
			Transaction: raw,
		})
	}
	return rows
}

func txSignatures(txs []*v1.Transaction) []string {
	sigs := make([]string, len(txs))
	for i, tx := range txs {
		sigs[i] = tx.GetTransaction().GetSignature()
	}
	return sigs
}

// A block whose executed order is not the display order: an Update before
// the Create of a different entity, plays between them whose signatures the
// display sort would swap, and a Delete before a Create of the same entity.
func outOfDisplayOrderBlock() []*v1.SignedTransaction {
	return []*v1.SignedTransaction{
		manageEntityTx("Update", "User", 2, "update-user-2"),
		playTx("play-b"),
		playTx("play-a"),
		manageEntityTx("Create", "User", 3, "create-user-3"),
		manageEntityTx("Delete", "Track", 7, "delete-track-7"),
		manageEntityTx("Create", "Track", 7, "create-track-7"),
	}
}

func TestBlocksFromRowsCanonKeepsIndexOrder(t *testing.T) {
	txs := outOfDisplayOrderBlock()
	want := make([]string, len(txs))
	for i, tx := range txs {
		want[i] = tx.GetSignature()
	}

	var rows []db.GetBlocksWithTransactionsRow
	rows = append(rows, blockRows(t, 10, txs)...)
	rows = append(rows, blockRows(t, 11, nil)...)
	rows = append(rows, blockRows(t, 12, []*v1.SignedTransaction{playTx("play-c")})...)

	blocks, err := blocksFromRows(rows, "test-chain", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}
	if got := txSignatures(blocks[10].Transactions); !slices.Equal(got, want) {
		t.Fatalf("block 10 order = %v, want index order %v", got, want)
	}
	if n := len(blocks[11].Transactions); n != 0 {
		t.Fatalf("empty block 11 has %d transactions", n)
	}
	if got := txSignatures(blocks[12].Transactions); !slices.Equal(got, []string{"play-c"}) {
		t.Fatalf("block 12 = %v", got)
	}
}

// Without canon, the display sort reorders the same block. This guards the
// canon test above from passing on a fixture the sort happens to leave alone.
func TestBlocksFromRowsDisplayOrderDiffers(t *testing.T) {
	txs := outOfDisplayOrderBlock()
	want := make([]string, len(txs))
	for i, tx := range txs {
		want[i] = tx.GetSignature()
	}

	blocks, err := blocksFromRows(blockRows(t, 10, txs), "test-chain", false)
	if err != nil {
		t.Fatal(err)
	}
	got := txSignatures(blocks[10].Transactions)
	if slices.Equal(got, want) {
		t.Fatalf("display sort left index order %v unchanged", got)
	}
	if got[0] != "create-user-3" && got[0] != "create-track-7" {
		t.Fatalf("display sort should put a Create first, got %v", got)
	}
	if slices.Index(got, "play-a") > slices.Index(got, "play-b") {
		t.Fatalf("display sort should reorder the plays by signature hash, got %v", got)
	}
}
