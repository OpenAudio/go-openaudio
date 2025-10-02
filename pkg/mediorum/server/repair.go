package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"go.uber.org/zap"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/labstack/echo/v4"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
	"golang.org/x/exp/slices"
	"gorm.io/gorm"
)

type RepairTracker struct {
	StartedAt        time.Time `gorm:"primaryKey;not null"`
	UpdatedAt        time.Time `gorm:"not null"`
	FinishedAt       time.Time
	CleanupMode      bool           `gorm:"not null"`
	CursorI          int            `gorm:"not null"`
	CursorUploadID   string         `gorm:"not null"`
	CursorPreviewCID string         ``
	CursorQmCID      string         `gorm:"not null"`
	Counters         map[string]int `gorm:"not null;serializer:json"`
	ContentSize      int64          `gorm:"not null"`
	Duration         time.Duration  `gorm:"not null"`
	AbortedReason    string         `gorm:"not null"`
}

func (ss *MediorumServer) startRepairer(ctx context.Context) error {
	logger := ss.logger.With(zap.String("task", "repair"))

	// wait a minute on startup to determine healthy peers
	ticker := time.NewTicker(1 * time.Minute)
	for {
		select {
		case <-ticker.C:
			// Wait 1 hour for next interval unless otherwise specified
			ticker.Reset(1 * time.Hour)

			// pick up where we left off from the last repair.go run, including if the server restarted in the middle of a run
			tracker := RepairTracker{
				StartedAt:   time.Now(),
				CleanupMode: true,
				CursorI:     1,
				Counters:    map[string]int{},
			}
			var lastRun RepairTracker
			if err := ss.crud.DB.Order("started_at desc").First(&lastRun).Error; err == nil {
				if lastRun.FinishedAt.IsZero() {
					// resume previously interrupted job
					tracker = lastRun
				} else {
					// run the next job
					tracker.CursorI = lastRun.CursorI + 1

					// every few runs, run cleanup mode
					if tracker.CursorI > 4 {
						tracker.CursorI = 1
					}
					tracker.CleanupMode = tracker.CursorI == 1
				}
			} else {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					logger.Error("failed to get last repair.go run", zap.Error(err))
				}
			}
			logger := logger.With(zap.Int("run", tracker.CursorI), zap.Bool("cleanupMode", tracker.CleanupMode))

			saveTracker := func() {
				tracker.UpdatedAt = time.Now()
				if err := ss.crud.DB.Save(tracker).Error; err != nil {
					logger.Error("failed to save repair tracker", zap.Error(err))
				}
			}

			// check that network is valid (should have more peers than replication factor)
			if healthyPeers := ss.findHealthyPeers(time.Hour); len(healthyPeers) < ss.Config.ReplicationFactor {
				logger.Warn("not enough healthy peers to run repair",
					zap.Int("R", ss.Config.ReplicationFactor),
					zap.Int("peers", len(healthyPeers)))
				tracker.AbortedReason = "NOT_ENOUGH_PEERS"
				tracker.FinishedAt = time.Now()
				saveTracker()
				// wait 1 minute before running again
				ticker.Reset(time.Minute * 1)
				continue
			}

			// check that disk has space
			if !ss.diskHasSpace() && !tracker.CleanupMode {
				logger.Warn("disk has <200GB remaining and is not in cleanup mode. skipping repair")
				tracker.AbortedReason = "DISK_FULL"
				tracker.FinishedAt = time.Now()
				saveTracker()
				// wait 1 minute before running again
				ticker.Reset(time.Minute * 1)
				continue
			}

			logger.Info("repair starting")
			err := ss.runRepair(ctx, &tracker)
			tracker.FinishedAt = time.Now()
			if err != nil {
				logger.Error("repair failed", zap.Error(err), zap.Duration("took", tracker.Duration))
				tracker.AbortedReason = err.Error()
			} else {
				logger.Info("repair OK", zap.Duration("took", tracker.Duration))
				ss.lastSuccessfulRepair = tracker
				if tracker.CleanupMode {
					ss.lastSuccessfulCleanup = tracker
				}
			}
			saveTracker()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) runRepair(ctx context.Context, tracker *RepairTracker) error {
	saveTracker := func() {
		tracker.UpdatedAt = time.Now()
		if err := ss.crud.DB.Save(tracker).Error; err != nil {
			ss.logger.Error("failed to save tracker", zap.Error(err))
		}
	}

	// scroll uploads and repair CIDs
	// (later this can clean up "derivative" images if we make image resizing dynamic)
	for {
		// abort if disk is filling up
		if !ss.diskHasSpace() && !tracker.CleanupMode {
			tracker.AbortedReason = "DISK_FULL"
			saveTracker()
			break
		}

		startIter := time.Now()

		var uploads []Upload
		if err := ss.crud.DB.Where("id > ?", tracker.CursorUploadID).Order("id").Limit(1000).Find(&uploads).Error; err != nil {
			return err
		}
		if len(uploads) == 0 {
			break
		}
		for _, u := range uploads {
			tracker.CursorUploadID = u.ID
			ss.repairCid(ctx, u.OrigFileCID, u.PlacementHosts, tracker)
			// images are resized dynamically
			// so only consider audio TranscodeResults for repair
			if u.Template != JobTemplateAudio {
				continue
			}
			for _, cid := range u.TranscodeResults {
				ss.repairCid(ctx, cid, u.PlacementHosts, tracker)
			}
		}

		tracker.Duration += time.Since(startIter)
		saveTracker()
	}

	// scroll audio_previews for repair
	for {
		// abort if disk is filling up
		if !ss.diskHasSpace() && !tracker.CleanupMode {
			tracker.AbortedReason = "DISK_FULL"
			saveTracker()
			break
		}

		startIter := time.Now()

		var previews []AudioPreview
		if err := ss.crud.DB.Where("cid > ?", tracker.CursorPreviewCID).Order("cid").Limit(1000).Find(&previews).Error; err != nil {
			return err
		}
		if len(previews) == 0 {
			break
		}
		for _, u := range previews {
			tracker.CursorPreviewCID = u.CID
			ss.repairCid(ctx, u.CID, nil, tracker)
		}

		tracker.Duration += time.Since(startIter)
		saveTracker()
	}

	// scroll older qm_cids table and repair
	for {
		// abort if disk is filling up
		if !ss.diskHasSpace() && !tracker.CleanupMode {
			tracker.AbortedReason = "DISK_FULL"
			saveTracker()
			break
		}

		startIter := time.Now()

		var cidBatch []string
		err := pgxscan.Select(ctx, ss.pgPool, &cidBatch,
			`select key
			 from qm_cids
			 where key > $1
			 order by key
			 limit 1000`, tracker.CursorQmCID)

		if err != nil {
			return err
		}
		if len(cidBatch) == 0 {
			break
		}
		for _, cid := range cidBatch {
			tracker.CursorQmCID = cid
			ss.repairCid(ctx, cid, nil, tracker)
		}

		tracker.Duration += time.Since(startIter)
		saveTracker()
	}

	return nil
}

func (ss *MediorumServer) repairCid(ctx context.Context, cid string, placementHosts []string, tracker *RepairTracker) error {
	logger := ss.logger.With(zap.String("task", "repair"), zap.String("cid", cid), zap.Bool("cleanup", tracker.CleanupMode))

	preferredHosts, isMine := ss.rendezvousAllHosts(cid)

	// if placementHosts is specified
	isPlaced := len(placementHosts) > 0
	if isPlaced {
		// we're not a preferred host
		if !slices.Contains(placementHosts, ss.Config.Self.Host) {
			return nil
		}

		// we are a preffered host
		preferredHosts = placementHosts
		isMine = true
	}

	// fast path: do zero bucket ops if we know we don't care about this cid
	if !tracker.CleanupMode && !isMine {
		return nil
	}

	// wait 100ms to avoid too many IOPS if using disk or bandwidth if using cloud storage
	time.Sleep(time.Millisecond * 100)

	tracker.Counters["total_checked"]++

	myRank := slices.Index(preferredHosts, ss.Config.Self.Host)

	key := cidutil.ShardCID(cid)
	alreadyHave := true
	attrs := &blob.Attributes{}
	attrs, err := ss.bucket.Attributes(ctx, key)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			attrs = &blob.Attributes{}
			alreadyHave = false
		} else {
			tracker.Counters["read_attrs_fail"]++
			logger.Error("exist check failed", zap.Error(err))
			return err
		}
	}

	// in cleanup mode do some extra checks:
	// - validate CID, delete if invalid (doesn't apply to Qm keys because their hash is not the CID)
	if tracker.CleanupMode && alreadyHave && !cidutil.IsLegacyCID(cid) {
		if r, errRead := ss.bucket.NewReader(ctx, key, nil); errRead == nil {
			errVal := cidutil.ValidateCID(cid, r)
			errClose := r.Close()
			if err != nil {
				tracker.Counters["delete_invalid_needed"]++
				logger.Error("deleting invalid CID", zap.Error(errVal))
				if errDel := ss.bucket.Delete(ctx, key); errDel == nil {
					tracker.Counters["delete_invalid_success"]++
				} else {
					tracker.Counters["delete_invalid_fail"]++
					logger.Error("failed to delete invalid CID", zap.Error(errDel))
				}
				return err
			}

			if errClose != nil {
				logger.Error("failed to close blob reader", zap.Error(errClose))
			}
		} else {
			tracker.Counters["read_blob_fail"]++
			logger.Error("failed to read blob", zap.Error(errRead))
			return errRead
		}
	}

	// delete derived image variants since they'll be dynamically resized
	if strings.HasSuffix(cid, ".jpg") && !strings.HasSuffix(cid, "original.jpg") {
		if tracker.CleanupMode && alreadyHave {
			err := ss.dropFromMyBucket(cid)
			if err != nil {
				logger.Error("delete_resized_image_failed", zap.Error(err))
				tracker.Counters["delete_resized_image_failed"]++
			} else {
				tracker.Counters["delete_resized_image_ok"]++
			}
		}
		return nil
	}

	if alreadyHave {
		tracker.Counters["already_have"]++
		tracker.ContentSize += attrs.Size
	}

	// get blobs that I should have (regardless of health of other nodes)
	if isMine && !alreadyHave && ss.diskHasSpace() {
		tracker.Counters["pull_mine_needed"]++
		success := false
		// loop preferredHosts (not preferredHealthyHosts) because pullFileFromHost can still give us a file even if we thought the host was unhealthy
		for _, host := range preferredHosts {
			if host == ss.Config.Self.Host {
				continue
			}
			err := ss.pullFileFromHost(ctx, host, cid)
			if err != nil {
				tracker.Counters["pull_mine_fail"]++
				logger.Error("pull failed (blob I should have)", zap.Error(err), zap.String("host", host))
			} else {
				tracker.Counters["pull_mine_success"]++
				logger.Debug("pull OK (blob I should have)", zap.String("host", host))
				success = true

				pulledAttrs, errAttrs := ss.bucket.Attributes(ctx, key)
				if errAttrs != nil {
					tracker.ContentSize += pulledAttrs.Size
				}
				return nil
			}
		}
		if !success {
			logger.Warn("failed to pull from any host", zap.Strings("hosts", preferredHosts))
			return errors.New("failed to pull from any host")
		}
	}

	// delete over-replicated blobs:
	// check all healthy nodes ahead of me in the preferred order to ensure they have it.
	// if R+1 healthy nodes in front of me have it, I can safely delete.
	// don't delete if we replicated the blob within the past week
	wasReplicatedThisWeek := attrs.ModTime.After(time.Now().Add(-24 * 7 * time.Hour))

	// by default retain blob if our rank < ReplicationFactor+2
	// but nodes with more free disk space will use a higher threshold
	// to accomidate "spill over" from nodes that might be full or down.
	diskPercentFree := float64(ss.mediorumPathFree) / float64(ss.mediorumPathSize)
	rankThreshold := ss.Config.ReplicationFactor + 2
	if !ss.diskHasSpace() {
		rankThreshold = ss.Config.ReplicationFactor
	} else if diskPercentFree > 0.4 {
		rankThreshold = ss.Config.ReplicationFactor * 3
	} else if diskPercentFree > 0.2 {
		rankThreshold = ss.Config.ReplicationFactor * 2
	}

	if !isPlaced && !ss.Config.StoreAll && tracker.CleanupMode && alreadyHave && myRank > rankThreshold && !wasReplicatedThisWeek {
		// if i'm the first node that over-replicated, keep the file for a week as a buffer since a node ahead of me in the preferred order will likely be down temporarily at some point
		tracker.Counters["delete_over_replicated_needed"]++
		err = ss.dropFromMyBucket(cid)
		if err != nil {
			tracker.Counters["delete_over_replicated_fail"]++
			logger.Error("delete failed", zap.Error(err))
			return err
		} else {
			tracker.Counters["delete_over_replicated_success"]++
			logger.Debug("delete OK")
			tracker.ContentSize -= attrs.Size
			return nil
		}
	}

	return nil
}

func (ss *MediorumServer) serveRepairLog(c echo.Context) error {
	limitStr := c.QueryParam("limit")
	if limitStr == "" {
		limitStr = "1000"
	}

	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		return c.String(http.StatusBadRequest, "Invalid limit value")
	}

	if limit > 1000 {
		limit = 1000
	}

	var logs []RepairTracker
	if err := ss.crud.DB.Order("started_at desc").Limit(limit).Find(&logs).Error; err != nil {
		return c.String(http.StatusInternalServerError, "DB query failed")
	}

	return c.JSON(http.StatusOK, logs)
}
