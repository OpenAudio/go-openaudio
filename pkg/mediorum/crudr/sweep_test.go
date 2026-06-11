package crudr

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func mkULID(t *testing.T, at time.Time) string {
	id, err := ulid.New(ulid.Timestamp(at), rand.Reader)
	require.NoError(t, err)
	return id.String()
}

func newCrudrWithPeers(t *testing.T, db *gorm.DB, peers ...string) *Crudr {
	z := zap.NewNop()
	return New("https://self.example", nil, peers, db,
		lifecycle.NewLifecycle(context.Background(), "crudr floor test", z), z, nil)
}

// The prune floor is the slowest *active* peer's cursor. Self, decommissioned
// (no-longer-active) peers, and non-peer worker rows (e.g. qm_fix_truncated,
// which stores a CID under its own host) must not pin or lower it.
func TestSlowestActivePeerCursor(t *testing.T) {
	db := SetupTestDB()
	c := newCrudrWithPeers(t, db, "https://peer-a.example", "https://peer-b.example")
	require.NoError(t, db.Exec("TRUNCATE ops, cursors").Error)

	now := time.Now()
	low := mkULID(t, now.Add(-2*time.Hour))     // peer-a: slowest active
	high := mkULID(t, now.Add(-1*time.Hour))    // peer-b
	veryLow := mkULID(t, now.Add(-9*time.Hour)) // peer-c: decommissioned (inactive)

	require.NoError(t, db.Create(&[]Cursor{
		{Host: "https://peer-a.example", LastULID: low},
		{Host: "https://peer-b.example", LastULID: high},
		{Host: "https://peer-c.example", LastULID: veryLow},
		{Host: "https://self.example", LastULID: mkULID(t, now)},
		{Host: "qm_fix_truncated", LastULID: "QmSomeContentIdNotAUlid"},
	}).Error)

	require.Equal(t, low, c.SlowestActivePeerCursor(context.Background()))
}

func TestSlowestActivePeerCursor_NoActivePeers(t *testing.T) {
	db := SetupTestDB()
	c := newCrudrWithPeers(t, db)
	require.NoError(t, db.Exec("TRUNCATE ops, cursors").Error)
	require.NoError(t, db.Create(&Cursor{Host: "https://peer-a.example", LastULID: mkULID(t, time.Now())}).Error)

	require.Equal(t, "", c.SlowestActivePeerCursor(context.Background()))
}

func TestSlowestActivePeerCursor_IgnoresImplausibleActiveCursor(t *testing.T) {
	db := SetupTestDB()
	c := newCrudrWithPeers(t, db, "https://peer-a.example", "https://peer-b.example")
	require.NoError(t, db.Exec("TRUNCATE ops, cursors").Error)

	now := time.Now()
	good := mkULID(t, now.Add(-1*time.Hour))
	farFuture := mkULID(t, now.Add(72*time.Hour)) // outside the clock-skew window

	require.NoError(t, db.Create(&[]Cursor{
		{Host: "https://peer-a.example", LastULID: good},
		{Host: "https://peer-b.example", LastULID: farFuture},
	}).Error)

	require.Equal(t, good, c.SlowestActivePeerCursor(context.Background()))
}
