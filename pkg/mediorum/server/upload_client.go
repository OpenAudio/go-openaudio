package server

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm/clause"
)

const (
	// Backfill is never urgent, and the first pass would otherwise contend with
	// node startup, so hold off before the first one.
	uploadScrollStartDelay = 5 * time.Minute
	// Poll rate while backfilling history, and once the cursor has caught up.
	uploadScrollBackfillInterval = 5 * time.Minute
	uploadScrollCaughtUpInterval = time.Hour
	// A cursor within this of now has nothing left to backfill; Core carries
	// everything newer.
	uploadScrollCaughtUpWindow = 2 * time.Hour
)

// startUploadScroller backfills the uploads table from peers.
//
// Core is the transport for live operations, but it only carries operations
// committed since this node started syncing, and the chain retains a bounded
// window of blocks. Rows created before that window still have to come from
// somewhere: this scroller walks each peer's GET /uploads?after=<created_at>
// and upserts rows it is missing or that are staler than the peer's copy.
//
// It is ordered by created_at, so it never re-reads a row once the cursor
// passes it — updates to already-seen rows arrive over Core instead. That
// makes this a bootstrap path, not a replication path: it polls quickly while
// there is history left to fetch, then backs off to an idle heartbeat.
//
// Rows are written straight to the table rather than through the op log; a
// backfill must not manufacture new operations for other nodes to apply.
func (ss *MediorumServer) startUploadScroller(ctx context.Context) error {
	ticker := time.NewTicker(uploadScrollStartDelay)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if ss.scrollUploadsFromPeers(ctx) {
				ticker.Reset(uploadScrollCaughtUpInterval)
			} else {
				ticker.Reset(uploadScrollBackfillInterval)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// scrollUploadsFromPeers runs one pass over every peer and reports whether all
// cursors are close enough to now that there is no history left to backfill.
func (ss *MediorumServer) scrollUploadsFromPeers(ctx context.Context) bool {
	caughtUp := true
	for _, peer := range ss.Config.Peers {
		if peer.Host == ss.Config.Self.Host {
			continue
		}
		if ctx.Err() != nil {
			return false
		}
		if !ss.scrollUploadsFromPeer(ctx, peer.Host) {
			caughtUp = false
		}
	}
	return caughtUp
}

// scrollUploadsFromPeer advances one peer's cursor by a page and reports
// whether that cursor is now current.
func (ss *MediorumServer) scrollUploadsFromPeer(ctx context.Context, host string) bool {
	var uploadCursor *UploadCursor
	if ss.crud.DB.First(&uploadCursor, "host = ?", host).Error != nil {
		uploadCursor = &UploadCursor{Host: host}
	}
	logger := ss.logger.With(zap.String("task", "upload_scroll"), zap.String("host", host), zap.Time("after", uploadCursor.After))

	var uploads []*Upload
	u := apiPath(host, "uploads") + "?after=" + uploadCursor.After.Format(time.RFC3339Nano)
	resp, err := ss.reqClient.R().
		SetContext(ctx).
		SetSuccessResult(&uploads).
		Get(u)
	if err != nil {
		logger.Error("list uploads failed", zap.Error(err))
		return false
	}
	if resp.StatusCode != 200 {
		logger.Error("list uploads failed", zap.Error(fmt.Errorf("%s: %s %s", resp.Request.RawURL, resp.Status, string(resp.Bytes()))))
		return false
	}

	if len(uploads) == 0 {
		// Nothing newer than the cursor on this peer.
		return true
	}

	// One lookup for the whole page rather than one per row: a backfill page is
	// up to 2000 uploads, and this runs against every peer.
	ids := make([]string, 0, len(uploads))
	for _, upload := range uploads {
		ids = append(ids, upload.ID)
	}
	var existing []Upload
	if err := ss.crud.DB.Select("id", "transcoded_at").Where("id IN ?", ids).Find(&existing).Error; err != nil {
		logger.Warn("lookup existing uploads failed", zap.Error(err))
		return false
	}
	transcodedAt := make(map[string]time.Time, len(existing))
	for _, e := range existing {
		transcodedAt[e.ID] = e.TranscodedAt
	}

	var overwrites []*Upload
	for _, upload := range uploads {
		if prev, ok := transcodedAt[upload.ID]; !ok || prev.Before(upload.TranscodedAt) {
			overwrites = append(overwrites, upload)
		}
		uploadCursor.After = upload.CreatedAt
	}

	if len(overwrites) > 0 {
		if err := ss.crud.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(overwrites).Error; err != nil {
			logger.Warn("overwrite upload failed", zap.Error(err))
		}
	}

	// Always save the cursor after processing a page so the next pass doesn't
	// re-fetch the same uploads.
	if err := ss.crud.DB.Clauses(clause.OnConflict{UpdateAll: true}).Create(uploadCursor).Error; err != nil {
		logger.Error("save upload cursor failed", zap.Error(err))
		return false
	}
	logger.Debug("upload scroll page done", zap.Int("uploads", len(uploads)), zap.Int("overwrites", len(overwrites)))

	return time.Since(uploadCursor.After) < uploadScrollCaughtUpWindow
}
