package server

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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

	// The peer holds all three rows. TranscodedAt is identical for the existing
	// pairs, so freshness depends on updates made after transcoding.
	require.NoError(t, source.crud.DB.Create(&[]Upload{
		{ID: missingID, Template: JobTemplateAudio, OrigFileCID: "cid-missing", CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour), TranscodedAt: now.Add(-3 * time.Hour)},
		{ID: staleID, Template: JobTemplateAudio, OrigFileCID: "cid-stale", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-15 * time.Minute), TranscodedAt: now.Add(-1 * time.Hour)},
		{ID: newerID, Template: JobTemplateAudio, OrigFileCID: "cid-newer", CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-30 * time.Minute), TranscodedAt: now.Add(-2 * time.Hour)},
	}).Error)

	// The target is missing one row, has an older copy of another, and a
	// strictly newer copy of the third that must survive.
	require.NoError(t, target.crud.DB.Create(&[]Upload{
		{ID: staleID, Template: JobTemplateAudio, OrigFileCID: "cid-stale-old", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-90 * time.Minute), TranscodedAt: now.Add(-1 * time.Hour)},
		{ID: newerID, Template: JobTemplateAudio, OrigFileCID: "cid-newer-local", CreatedAt: now.Add(-1 * time.Hour), UpdatedAt: now.Add(-time.Minute), TranscodedAt: now.Add(-2 * time.Hour)},
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

func TestScrollUploadsFromPeerDoesNotAdvanceCursorAfterUpsertFailure(t *testing.T) {
	ctx := context.Background()
	source := testNetwork[0]
	target := testNetwork[1]
	peerHost := source.Config.Self.Host

	now := time.Now().UTC().Truncate(time.Second)
	id := fmt.Sprintf("scroll-failure-%d", now.UnixNano())
	require.NoError(t, source.crud.DB.Create(&Upload{
		ID: id, Template: JobTemplateAudio, CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour),
	}).Error)
	t.Cleanup(func() {
		require.NoError(t, source.crud.DB.Delete(&Upload{}, "id = ?", id).Error)
		require.NoError(t, target.crud.DB.Delete(&Upload{}, "id = ?", id).Error)
		require.NoError(t, target.crud.DB.Delete(&UploadCursor{}, "host = ?", peerHost).Error)
	})
	require.NoError(t, target.crud.DB.Delete(&UploadCursor{}, "host = ?", peerHost).Error)

	callbackName := "test:fail_upload_scroll_upsert"
	require.NoError(t, target.crud.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(db *gorm.DB) {
		if db.Statement.Schema != nil && db.Statement.Schema.Table == "uploads" {
			db.AddError(errors.New("forced upload upsert failure"))
		}
	}))
	t.Cleanup(func() {
		require.NoError(t, target.crud.DB.Callback().Create().Remove(callbackName))
	})

	require.False(t, target.scrollUploadsFromPeer(ctx, peerHost))

	var cursorCount int64
	require.NoError(t, target.crud.DB.Model(&UploadCursor{}).Where("host = ?", peerHost).Count(&cursorCount).Error)
	require.Zero(t, cursorCount, "cursor must remain in place so the failed page is retried")
}
