package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The scroller is the only way a node acquires uploads created before it
// started syncing Core, so cover the three things that matter: it pulls rows
// it is missing, it does not clobber newer local state, and its cursor
// advances so the next pass doesn't re-read the same page.
func TestScrollUploadsFromPeerBackfillsAndAdvancesCursor(t *testing.T) {
	ctx := context.Background()
	source := testNetwork[0]
	target := testNetwork[1]
	peerHost := source.Config.Self.Host

	now := time.Now().UTC().Truncate(time.Second)
	prefix := fmt.Sprintf("scroll-%d-", now.UnixNano())
	missingID := prefix + "missing"
	staleID := prefix + "stale"
	newerID := prefix + "newer"

	cleanup := func() {
		require.NoError(t, source.crud.DB.Where("id LIKE ?", prefix+"%").Delete(&Upload{}).Error)
		require.NoError(t, target.crud.DB.Where("id LIKE ?", prefix+"%").Delete(&Upload{}).Error)
		require.NoError(t, target.crud.DB.Where("host = ?", peerHost).Delete(&UploadCursor{}).Error)
	}
	cleanup()
	t.Cleanup(cleanup)

	// The peer holds all three rows; two of them are newer than the target's.
	require.NoError(t, source.crud.DB.Create(&[]Upload{
		{ID: missingID, Template: JobTemplateAudio, OrigFileCID: "cid-missing", CreatedAt: now.Add(-3 * time.Hour), TranscodedAt: now.Add(-3 * time.Hour)},
		{ID: staleID, Template: JobTemplateAudio, OrigFileCID: "cid-stale", CreatedAt: now.Add(-2 * time.Hour), TranscodedAt: now.Add(-1 * time.Hour)},
		{ID: newerID, Template: JobTemplateAudio, OrigFileCID: "cid-newer", CreatedAt: now.Add(-1 * time.Hour), TranscodedAt: now.Add(-2 * time.Hour)},
	}).Error)

	// The target is missing one row, has an older copy of another, and a
	// strictly newer copy of the third that must survive.
	require.NoError(t, target.crud.DB.Create(&[]Upload{
		{ID: staleID, Template: JobTemplateAudio, OrigFileCID: "cid-stale-old", CreatedAt: now.Add(-2 * time.Hour), TranscodedAt: now.Add(-90 * time.Minute)},
		{ID: newerID, Template: JobTemplateAudio, OrigFileCID: "cid-newer-local", CreatedAt: now.Add(-1 * time.Hour), TranscodedAt: now.Add(-time.Minute)},
	}).Error)

	target.scrollUploadsFromPeer(ctx, peerHost)

	// A fresh destination per lookup: GORM folds a populated primary key on the
	// destination into the next query's conditions.
	origCID := func(id string) string {
		var got Upload
		require.NoError(t, target.crud.DB.First(&got, "id = ?", id).Error)
		return got.OrigFileCID
	}

	require.Equal(t, "cid-missing", origCID(missingID), "missing upload should be backfilled")
	require.Equal(t, "cid-stale", origCID(staleID), "stale local row should be refreshed from the peer")
	require.Equal(t, "cid-newer-local", origCID(newerID), "newer local row must not be clobbered")

	var cursor UploadCursor
	require.NoError(t, target.crud.DB.First(&cursor, "host = ?", peerHost).Error)
	require.False(t, cursor.After.IsZero(), "cursor should advance so the next pass skips this page")
}
