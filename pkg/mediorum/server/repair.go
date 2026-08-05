package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/erni27/imcache"
	"go.uber.org/zap"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/labstack/echo/v4"
	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"
	"golang.org/x/exp/slices"
	"gorm.io/gorm"
)

const (
	abortContextCanceled = "CONTEXT_CANCELED"

	// uploadScanBatchSize is how many uploads runRepair pulls per keyset page.
	uploadScanBatchSize = 1000

	// pullAttemptMargin is how many hosts beyond the replica set repair will
	// try before falling back. A blob is pushed to the top ReplicationFactor
	// hosts at upload time, so those are where it lives; the margin absorbs
	// ring churn and hosts that have since dropped it.
	pullAttemptMargin = 4

	// storeAllFallbackHosts caps how many store-all peers are tried after the
	// replica set. Kept small on purpose: there are only a handful of them on
	// the network, they are other operators' production nodes, and every peer
	// running repair would otherwise converge on the same ones.
	storeAllFallbackHosts = 2
)

// nextUploadBatch returns the page of uploads immediately after cursor.
//
// Keyset pagination requires the sort direction to match the comparison
// operator: with "id > cursor" the rows must come back ASC so that the last
// element is the largest ID in the batch and can serve as the next cursor —
// a true high-water mark. Pairing "id > cursor" with DESC makes the last
// element the *smallest* of the batch, so the cursor only ever moves up into
// rows already read, the candidate window (cursor, max] shrinks every
// iteration, and every row below the first batch is excluded permanently.
// That bug limited the scan to the top uploadScanBatchSize uploads no matter
// how large the table was.
//
// Split out from runRepair so the invariant is directly testable.
func (ss *MediorumServer) nextUploadBatch(cursor string, limit int) ([]Upload, error) {
	var uploads []Upload
	err := ss.crud.DB.Where("id > ?", cursor).Order("id ASC").Limit(limit).Find(&uploads).Error
	return uploads, err
}

// maxPullAttempts bounds how many hosts a single repair pull will try.
//
// preferredHosts is the full rendezvous ranking — every node on the network —
// so without a bound a CID the replica set no longer serves walks all of them.
// In production that produced ~2.8 failed attempts per success. The misses are
// individually cheap (measured around a second each, mostly 404s rather than
// timeouts), but at that ratio they still consume most of the per-blob budget.
func (ss *MediorumServer) maxPullAttempts() int {
	n := ss.Config.ReplicationFactor + pullAttemptMargin
	if n < 1 {
		return 1
	}
	return n
}

// seenKeyResult stores the outcome of a previous Attributes check for the same
// key within one repair cycle, allowing duplicate checks to be skipped.
type seenKeyResult struct {
	alreadyHave bool
	size        int64
}

type RepairTracker struct {
	StartedAt        time.Time `gorm:"primaryKey;not null"`
	UpdatedAt        time.Time `gorm:"not null"`
	FinishedAt       time.Time
	CleanupMode      bool                     `gorm:"not null"`
	CursorI          int                      `gorm:"not null"`
	CursorUploadID   string                   `gorm:"not null"`
	CursorPreviewCID string                   ``
	CursorQmCID      string                   `gorm:"not null"`
	Counters         map[string]int           `gorm:"not null;serializer:json"`
	ContentSize      int64                    `gorm:"not null"`
	Duration         time.Duration            `gorm:"not null"`
	AbortedReason    string                   `gorm:"not null"`
	SeenKeys         map[string]seenKeyResult `gorm:"-" json:"-"`
	// PruneSkips are CIDs the prune job judged not worth chasing. Without it
	// an unrecoverable CID is retried on every cycle, forever.
	PruneSkips map[string]struct{} `gorm:"-" json:"-"`
	mu               *sync.Mutex              `gorm:"-" json:"-"`
}

func (ss *MediorumServer) startRepairer(ctx context.Context) error {
	logger := ss.logger.With(zap.String("task", "repair"))

	if !ss.Config.RepairEnabled {
		logger.Info("repair is disabled via OPENAUDIO_REPAIR_ENABLED=false")
		<-ctx.Done()
		return ctx.Err()
	}

	repairInterval := ss.Config.RepairInterval
	logger.Info("repair configured", zap.Duration("interval", repairInterval))

	// wait a minute on startup to determine healthy peers
	ticker := time.NewTicker(1 * time.Minute)
	for {
		select {
		case <-ticker.C:
			ticker.Reset(repairInterval)

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
			// Cleanup cycles do full verification, so clear the cross-cycle
			// presence cache to force fresh Attributes calls.
			if tracker.CleanupMode {
				ss.knownPresent.RemoveAll()
			}

			logger := logger.With(zap.Int("run", tracker.CursorI), zap.Bool("cleanupMode", tracker.CleanupMode))

			saveTracker := func() {
				tracker.UpdatedAt = time.Now()
				if err := ss.crud.DB.Save(tracker).Error; err != nil {
					logger.Error("failed to save repair tracker", zap.Error(err))
				}
			}

			healthyPeers := ss.findHealthyPeers(time.Hour)
			if len(healthyPeers) < 1 {
				logger.Warn("not enough healthy peers to run repair",
					zap.Int("healthyPeers", len(healthyPeers)))
				tracker.AbortedReason = "NOT_ENOUGH_PEERS"
				tracker.FinishedAt = time.Now()
				saveTracker()
				// wait 1 minute before running again
				ticker.Reset(time.Minute * 1)
				continue
			}

			// check that disk has space
			if !ss.diskHasSpace() && !tracker.CleanupMode {
				logger.Warn("disk has <10GB remaining and is not in cleanup mode. skipping repair")
				tracker.AbortedReason = "DISK_FULL"
				tracker.FinishedAt = time.Now()
				saveTracker()
				// wait 1 minute before running again
				ticker.Reset(time.Minute * 1)
				continue
			}

			logger.Info("repair starting")
			err := ss.runRepair(ctx, &tracker)
			if err != nil && !errors.Is(err, context.Canceled) {
				tracker.FinishedAt = time.Now()
				logger.Error("repair failed", zap.Error(err), zap.Duration("took", tracker.Duration))
				tracker.AbortedReason = err.Error()
			} else if errors.Is(err, context.Canceled) {
				logger.Warn("repair interrupted", zap.Error(err), zap.Duration("took", tracker.Duration))
			} else {
				tracker.FinishedAt = time.Now()
				logger.Info("repair OK", zap.Duration("took", tracker.Duration), zap.Int("known_present_size", ss.knownPresent.Len()))
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

	repairConcurrency := ss.Config.RepairConcurrency
	if repairConcurrency < 1 {
		repairConcurrency = 1
	}
	tracker.mu = &sync.Mutex{}
	retentionPolicy := newRepairRetentionPolicy(ss.Config, time.Now())

	// Build a presence index from bucket listing to avoid per-key HeadObject.
	// Replaces hundreds of thousands of HeadObject calls with one ListObjects pagination.
	if skips, err := ss.loadPruneSkips(ctx); err != nil {
		ss.logger.Warn("failed to load prune skips; repair will retry skipped CIDs", zap.Error(err))
	} else {
		tracker.PruneSkips = skips
		if len(skips) > 0 {
			tracker.Counters["prune_skips_loaded"] = len(skips)
			ss.logger.Info("loaded prune skip list", zap.Int("cids", len(skips)))
		}
	}

	var presenceIndex *repairPresenceIndex
	ss.logger.Info("building repair presence index from bucket listing")
	indexStart := time.Now()
	idx, err := ss.buildRepairPresenceIndex(ctx)
	if err != nil {
		ss.logger.Warn("failed to build presence index; falling back to per-key attrs", zap.Error(err))
		tracker.Counters["qm_cids_list_index_build_fail"]++
	} else {
		presenceIndex = idx
		tracker.Counters["qm_cids_list_index_entries"] = len(idx.entries)
		ss.logger.Info("presence index built",
			zap.Int("entries", len(idx.entries)),
			zap.Duration("took", time.Since(indexStart)))
	}

	// scroll uploads and repair CIDs
	// (later this can clean up "derivative" images if we make image resizing dynamic)
	for {
		// abort if context is canceled
		if ctx.Err() != nil {
			tracker.AbortedReason = abortContextCanceled
			saveTracker()
			return ctx.Err()
		}

		// abort if disk is filling up
		if !ss.diskHasSpace() && !tracker.CleanupMode {
			tracker.AbortedReason = "DISK_FULL"
			saveTracker()
			break
		}

		startIter := time.Now()

		uploads, err := ss.nextUploadBatch(tracker.CursorUploadID, uploadScanBatchSize)
		if err != nil {
			return err
		}
		if len(uploads) == 0 {
			break
		}
		sem := make(chan struct{}, repairConcurrency)
		var wg sync.WaitGroup
		for _, u := range uploads {
			if ctx.Err() != nil {
				break
			}
			u := u
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				ss.repairCidWithPolicy(ctx, u.OrigFileCID, u.PlacementHosts, tracker, presenceIndex, retentionPolicy, u.CreatedAt)
				if u.Template != JobTemplateAudio {
					return
				}
				for _, cid := range u.TranscodeResults {
					if ctx.Err() != nil {
						return
					}
					ss.repairCidWithPolicy(ctx, cid, u.PlacementHosts, tracker, presenceIndex, retentionPolicy, u.CreatedAt)
				}
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			tracker.AbortedReason = abortContextCanceled
		} else {
			tracker.CursorUploadID = uploads[len(uploads)-1].ID
		}

		tracker.Duration += time.Since(startIter)
		saveTracker()
	}

	// scroll audio_previews for repair
	for {
		// abort if context is canceled
		if ctx.Err() != nil {
			tracker.AbortedReason = abortContextCanceled
			saveTracker()
			return ctx.Err()
		}

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
		sem := make(chan struct{}, repairConcurrency)
		var wg sync.WaitGroup
		for _, u := range previews {
			if ctx.Err() != nil {
				break
			}
			u := u
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				ss.repairCidWithPolicy(ctx, u.CID, nil, tracker, presenceIndex, retentionPolicy, u.CreatedAt)
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			tracker.AbortedReason = abortContextCanceled
		} else {
			tracker.CursorPreviewCID = previews[len(previews)-1].CID
		}

		tracker.Duration += time.Since(startIter)
		saveTracker()
	}

	// scroll older qm_cids table and repair
	for {
		// abort if context is canceled
		if ctx.Err() != nil {
			tracker.AbortedReason = abortContextCanceled
			saveTracker()
			return ctx.Err()
		}

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
		sem := make(chan struct{}, repairConcurrency)
		var wg sync.WaitGroup
		for _, cid := range cidBatch {
			if ctx.Err() != nil {
				break
			}
			cid := cid
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				ss.repairCidWithPolicy(ctx, cid, nil, tracker, presenceIndex, retentionPolicy, time.Time{})
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			tracker.AbortedReason = abortContextCanceled
		} else {
			tracker.CursorQmCID = cidBatch[len(cidBatch)-1]
		}

		tracker.Duration += time.Since(startIter)
		saveTracker()
	}

	return ctx.Err()
}

func (ss *MediorumServer) repairCid(ctx context.Context, cid string, placementHosts []string, tracker *RepairTracker, presenceIndex *repairPresenceIndex) error {
	return ss.repairCidWithPolicy(ctx, cid, placementHosts, tracker, presenceIndex, newRepairRetentionPolicy(ss.Config, time.Now()), time.Time{})
}

func (ss *MediorumServer) repairCidWithPolicy(ctx context.Context, cid string, placementHosts []string, tracker *RepairTracker, presenceIndex *repairPresenceIndex, retentionPolicy repairRetentionPolicy, createdAt time.Time) error {
	if cid == "" {
		return nil
	}

	// Safety net for direct callers (tests). runRepair initializes mu before
	// spawning workers, so production calls never hit this branch racing.
	if tracker.mu == nil {
		tracker.mu = &sync.Mutex{}
	}

	logger := ss.logger.With(zap.String("task", "repair"), zap.String("cid", cid), zap.Bool("cleanup", tracker.CleanupMode))

	preferredHosts, isMine := ss.rendezvousAllHosts(cid)
	storeRecent := retentionPolicy.shouldStoreRecent(createdAt)
	if storeRecent {
		isMine = true
	}

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

	// A prune already decided this CID is not worth chasing -- unpublished, or
	// unavailable from any reachable peer. Skip before spending a presence
	// lookup or a pull on it.
	tracker.mu.Lock()
	_, pruneSkipped := tracker.PruneSkips[cid]
	if pruneSkipped {
		tracker.Counters["prune_skipped"]++
	}
	tracker.mu.Unlock()
	if pruneSkipped {
		return nil
	}

	tracker.mu.Lock()
	tracker.Counters["total_checked"]++
	tracker.mu.Unlock()

	myRank := slices.Index(preferredHosts, ss.Config.Self.Host)

	key := cidutil.ShardCID(cid)
	bucket := ss.bucketForCID(cid, placementHosts)
	isArchive := ss.archiveBucket != nil && bucket == ss.archiveBucket
	presenceKey := ss.presenceCacheKey(key, bucket)

	// Per-cycle dedupe: repair iterates uploads, audio_previews, and qm_cids,
	// and the same CID can appear across those tables. Skip the duplicate
	// Attributes check when we already resolved this key earlier in the cycle.
	tracker.mu.Lock()
	if tracker.SeenKeys == nil {
		tracker.SeenKeys = map[string]seenKeyResult{}
	}
	if prev, seen := tracker.SeenKeys[key]; seen && !storeRecent {
		tracker.Counters["repair_deduped"]++
		if prev.alreadyHave {
			tracker.Counters["already_have"]++
			tracker.ContentSize += prev.size
		}
		tracker.mu.Unlock()
		return nil
	}
	tracker.mu.Unlock()

	// Cross-cycle cache: skip Attributes for CIDs confirmed present in a
	// previous cycle. Cleanup cycles bypass the cache because they need
	// ModTime for over-replication decisions and run full blob validation.
	if !tracker.CleanupMode {
		if size, ok := ss.knownPresent.Get(presenceKey); ok {
			tracker.mu.Lock()
			tracker.SeenKeys[key] = seenKeyResult{alreadyHave: true, size: size}
			tracker.Counters["already_have"]++
			tracker.Counters["repair_known_present"]++
			tracker.ContentSize += size
			tracker.mu.Unlock()
			return nil
		}
	}

	// Resolve blob presence: use the presence index (from bucket.List) if
	// available, otherwise fall back to per-key HeadObject. The lookup is
	// bucket-scoped — if the key only exists in the *other* bucket
	// (rank-flip orphan), treat as missing so the repair pull below writes
	// it into the bucket the routing decision selected.
	alreadyHave := false
	attrs := &blob.Attributes{}
	if presenceIndex != nil {
		if entry, ok := presenceIndex.Lookup(key, bucket); ok {
			alreadyHave = true
			attrs.Size = entry.Size
			attrs.ModTime = entry.ModTime
			tracker.mu.Lock()
			tracker.Counters["qm_cids_list_index_hit"]++
			if isArchive {
				tracker.Counters["archive_blob_present"]++
			}
			tracker.mu.Unlock()
		} else {
			tracker.mu.Lock()
			tracker.Counters["qm_cids_list_index_miss"]++
			tracker.mu.Unlock()
		}
	} else {
		var err error
		attrs, err = bucket.Attributes(ctx, key)
		if err != nil {
			if gcerrors.Code(err) == gcerrors.NotFound || strings.Contains(err.Error(), "notFound") {
				attrs = &blob.Attributes{}
			} else {
				tracker.mu.Lock()
				tracker.Counters["read_attrs_fail"]++
				if isArchive {
					tracker.Counters["archive_blob_attrs_fail"]++
				}
				tracker.mu.Unlock()
				logger.Error("exist check failed", zap.Error(err))
				attrs = &blob.Attributes{}
			}
		} else {
			alreadyHave = true
			if isArchive {
				tracker.mu.Lock()
				tracker.Counters["archive_blob_present"]++
				tracker.mu.Unlock()
			}
		}
	}

	// Store result for future duplicate checks within this cycle.
	tracker.mu.Lock()
	tracker.SeenKeys[key] = seenKeyResult{alreadyHave: alreadyHave, size: attrs.Size}
	tracker.mu.Unlock()

	// in cleanup mode do some extra checks:
	// - validate CID, delete if invalid (doesn't apply to Qm keys because their hash is not the CID)
	if tracker.CleanupMode && alreadyHave && !cidutil.IsLegacyCID(cid) {
		if r, errRead := bucket.NewReader(ctx, key, nil); errRead == nil {
			computed, errCompute := cidutil.ComputeFileCID(r)
			errClose := r.Close()
			if errCompute != nil {
				// Read/hash error — blob may be fine, skip rather than delete
				tracker.mu.Lock()
				tracker.Counters["validate_read_fail"]++
				tracker.mu.Unlock()
				logger.Warn("CID validation skipped (read error)", zap.Error(errCompute))
			} else if computed != cid {
				tracker.mu.Lock()
				tracker.Counters["delete_invalid_needed"]++
				tracker.mu.Unlock()
				logger.Error("deleting invalid CID", zap.String("expected", cid), zap.String("computed", computed))
				if errDel := bucket.Delete(ctx, key); errDel == nil {
					tracker.mu.Lock()
					tracker.Counters["delete_invalid_success"]++
					tracker.SeenKeys[key] = seenKeyResult{alreadyHave: false, size: 0}
					tracker.mu.Unlock()
					ss.knownPresent.Remove(presenceKey)
				} else {
					tracker.mu.Lock()
					tracker.Counters["delete_invalid_fail"]++
					tracker.mu.Unlock()
					logger.Error("failed to delete invalid CID", zap.Error(errDel))
				}
				return nil
			}

			if errClose != nil {
				logger.Error("failed to close blob reader", zap.Error(errClose))
			}
		} else {
			tracker.mu.Lock()
			tracker.Counters["read_blob_fail"]++
			tracker.mu.Unlock()
			logger.Error("failed to read blob", zap.Error(errRead))
			return errRead
		}
	}

	// Delete derived image variants since they'll be dynamically resized.
	// Variants are pure cache — generated on-demand by serveImage and
	// always written to the primary bucket regardless of where the
	// original lives — so cleanup only needs to touch primary.
	if strings.HasSuffix(cid, ".jpg") && !strings.HasSuffix(cid, "original.jpg") {
		if tracker.CleanupMode && alreadyHave {
			err := ss.bucket.Delete(ctx, key)
			if err != nil && gcerrors.Code(err) != gcerrors.NotFound {
				logger.Error("delete_resized_image_failed", zap.Error(err))
				tracker.mu.Lock()
				tracker.Counters["delete_resized_image_failed"]++
				tracker.mu.Unlock()
			} else {
				tracker.mu.Lock()
				tracker.Counters["delete_resized_image_ok"]++
				tracker.mu.Unlock()
				// Variants always live in primary; only the primary cache key matters.
				ss.knownPresent.Remove(ss.presenceCacheKey(key, ss.bucket))
			}
		}
		return nil
	}

	if alreadyHave {
		// Populate cross-cycle cache after all validation and cleanup has passed,
		// so that corrupt or about-to-be-deleted blobs are never cached.
		ss.knownPresent.Set(presenceKey, attrs.Size, imcache.WithNoExpiration())
		tracker.mu.Lock()
		tracker.Counters["already_have"]++
		tracker.ContentSize += attrs.Size
		tracker.mu.Unlock()
	}

	// get blobs that I should have (regardless of health of other nodes)
	if isMine && !alreadyHave && ss.diskHasSpaceForCID(cid, placementHosts) {
		tracker.mu.Lock()
		tracker.Counters["pull_mine_needed"]++
		tracker.mu.Unlock()

		// Two tiers of source.
		//
		// Tier 1 is the head of the rendezvous ranking — where the blob was
		// pushed at upload time, so where it should be. It is bounded: the full
		// ranking is every node on the network, and walking all of them for a
		// CID the replica set no longer serves burns a request per host.
		//
		// Tier 2 is a small, per-CID-rotated set of store-all peers. Past the
		// replica set the ranking is uncorrelated with who actually holds a
		// blob, so continuing down it is guessing; a store-all peer holds the
		// whole corpus and is a near-certain hit. This also covers the case the
		// tier 1 bound would otherwise lose: a blob that drifted deep in the
		// ranking after ring churn.
		//
		// Note tier 1 uses preferredHosts rather than only healthy ones,
		// because pullFileFromHost can still succeed against a host we believe
		// unhealthy.
		candidates := preferredHosts
		if len(candidates) > ss.maxPullAttempts() {
			candidates = candidates[:ss.maxPullAttempts()]
		}
		tier1 := len(candidates)
		for _, host := range ss.findStoreAllPeers(cid, time.Hour, storeAllFallbackHosts) {
			if !slices.Contains(candidates, host) {
				candidates = append(candidates, host)
			}
		}
		if len(candidates) > tier1 {
			tracker.mu.Lock()
			tracker.Counters["pull_store_all_fallback_offered"]++
			tracker.mu.Unlock()
		}

		success := false
		attempted := 0
		for i, host := range candidates {
			if host == ss.Config.Self.Host {
				continue
			}
			if i == tier1 && attempted > 0 {
				logger.Debug("replica set exhausted; falling back to store-all peers",
					zap.Int("tried", attempted))
			}
			attempted++

			err := ss.pullFileFromHost(ctx, host, cid, placementHosts)
			if err != nil {
				tracker.mu.Lock()
				tracker.Counters["pull_mine_fail"]++
				tracker.mu.Unlock()
				logger.Error("pull failed (blob I should have)", zap.Error(err), zap.String("host", host))
			} else {
				tracker.mu.Lock()
				tracker.Counters["pull_mine_success"]++
				if isArchive {
					tracker.Counters["archive_blob_pulled"]++
				}
				tracker.mu.Unlock()
				logger.Debug("pull OK (blob I should have)", zap.String("host", host))
				success = true

				pulledAttrs, errAttrs := bucket.Attributes(ctx, key)
				if errAttrs == nil {
					tracker.mu.Lock()
					tracker.ContentSize += pulledAttrs.Size
					tracker.mu.Unlock()
				}
				return nil
			}
		}
		if !success {
			tracker.mu.Lock()
			tracker.Counters["pull_mine_gave_up"]++
			tracker.mu.Unlock()
			logger.Warn("failed to pull from any host",
				zap.Int("attempted", attempted), zap.Int("replicaSetTried", tier1),
				zap.Int("candidates", len(preferredHosts)))
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

	if !isPlaced && !ss.Config.StoreAll && !storeRecent && tracker.CleanupMode && alreadyHave && myRank > rankThreshold && !wasReplicatedThisWeek {
		// if i'm the first node that over-replicated, keep the file for a week as a buffer since a node ahead of me in the preferred order will likely be down temporarily at some point
		tracker.mu.Lock()
		tracker.Counters["delete_over_replicated_needed"]++
		tracker.mu.Unlock()
		err := ss.dropFromMyBucket(cid)
		if err != nil {
			tracker.mu.Lock()
			tracker.Counters["delete_over_replicated_fail"]++
			tracker.mu.Unlock()
			logger.Error("delete failed", zap.Error(err))
			return err
		} else {
			tracker.mu.Lock()
			tracker.Counters["delete_over_replicated_success"]++
			tracker.ContentSize -= attrs.Size
			tracker.mu.Unlock()
			logger.Debug("delete OK")
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
