package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"golang.org/x/exp/slices"
	"golang.org/x/exp/slog"
	"gorm.io/gorm"
)

type StorageAndDbSize struct {
	LoggedAt           time.Time `gorm:"primaryKey;not null"`
	Host               string    `gorm:"primaryKey;not null"`
	StorageBackend     string    `gorm:"not null"`
	DbUsed             uint64    `gorm:"not null"`
	MediorumDiskUsed   uint64    `gorm:"not null"`
	MediorumDiskSize   uint64    `gorm:"not null"`
	StorageExpectation uint64    `gorm:"not null;default:0"`
	LastRepairSize     int64     `gorm:"not null"`
	LastCleanupSize    int64     `gorm:"not null"`
}

func (ss *MediorumServer) recordStorageAndDbSize(ctx context.Context) error {
	record := func(ctx context.Context) {
		// only do this once every 6 hours, even if the server restarts
		var lastStatus StorageAndDbSize
		err := ss.crud.DB.WithContext(ctx).
			Where("host = ?", ss.Config.Self.Host).
			Order("logged_at desc").
			First(&lastStatus).
			Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Error("Error getting last storage and db size", "err", err)
			return
		}
		if lastStatus.LoggedAt.After(time.Now().Add(-6 * time.Hour)) {
			return
		}

		blobStorePrefix, _, foundBlobStore := strings.Cut(ss.Config.BlobStoreDSN, "://")
		if !foundBlobStore {
			blobStorePrefix = ""
		}
		status := StorageAndDbSize{
			LoggedAt:           time.Now(),
			Host:               ss.Config.Self.Host,
			StorageBackend:     blobStorePrefix,
			DbUsed:             ss.databaseSize,
			MediorumDiskUsed:   ss.mediorumPathUsed,
			MediorumDiskSize:   ss.mediorumPathSize,
			StorageExpectation: ss.storageExpectation,
			LastRepairSize:     ss.lastSuccessfulRepair.ContentSize,
			LastCleanupSize:    ss.lastSuccessfulCleanup.ContentSize,
		}

		err = ss.crud.Create(&status)
		if err != nil {
			slog.Error("Error recording storage and db sizes", "err", err)
		}
	}

	record(ctx)
	ticker := time.NewTicker(6*time.Hour + time.Minute)
	for {
		select {
		case <-ticker.C:
			record(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) monitorMetrics(ctx context.Context) error {
	ticker := time.NewTicker(1 * time.Second)
	// retry a few times to get initial status on startup
	for i := 0; i < 3; i++ {
		select {
		case <-ticker.C:
			ticker.Reset(1 * time.Minute) // set longer interval after first attempt
			ss.updateDiskAndDbStatus(ctx)
			ss.updateTranscodeStats(ctx)
			ss.runBucketWriteCanary(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// persist the sizes in the db and let Core relay them to other nodes
	ss.lc.AddManagedRoutine("storage and db size recorder", ss.recordStorageAndDbSize)

	ticker.Reset(10 * time.Minute)
	for {
		select {
		case <-ticker.C:
			ss.updateDiskAndDbStatus(ctx)
			ss.updateTranscodeStats(ctx)
			ss.runBucketWriteCanary(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Probes the bucket so health-check catches backends that accept connections
// but reject writes (e.g. provider quota hit).
func (ss *MediorumServer) runBucketWriteCanary(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := ss.bucket.WriteAll(ctx, "__healthcheck__/canary", []byte("ok"), nil); err != nil {
		ss.bucketWriteErr = err.Error()
		slog.Error("bucket write canary failed", "err", err)
		return
	}
	ss.bucketWriteErr = ""
}

func (ss *MediorumServer) monitorPeerReachability(ctx context.Context) error {
	ticker := time.NewTicker(1 * time.Minute)
	for {
		select {
		case <-ticker.C:
			// find unreachable nodes in the last 2 minutes
			var unreachablePeers []string
			for _, peer := range ss.Config.Peers {
				if peer.Host == ss.Config.Self.Host {
					continue
				}
				if peerHealth, ok := ss.peerHealths[peer.Host]; ok {
					if peerHealth.LastReachable.Before(time.Now().Add(-2 * time.Minute)) {
						unreachablePeers = append(unreachablePeers, peer.Host)
					}
				} else {
					unreachablePeers = append(unreachablePeers, peer.Host)
				}
			}

			// check if each unreachable node was also unreachable last time we checked (so we ignore temporary downtime from restarts/updates)
			failsPeerReachability := false
			for _, unreachable := range unreachablePeers {
				if slices.Contains(ss.unreachablePeers, unreachable) {
					// we can't reach this peer. self-mark unhealthy if >50% of other nodes can
					if ss.canMajorityReachHost(unreachable) {
						// TODO: we can self-mark unhealthy if we want to enforce peer reachability
						failsPeerReachability = true
						break
					}
				}
			}

			ss.peerHealthsMutex.Lock()
			ss.unreachablePeers = unreachablePeers
			ss.failsPeerReachability = failsPeerReachability
			ss.peerHealthsMutex.Unlock()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (ss *MediorumServer) canMajorityReachHost(host string) bool {
	ss.peerHealthsMutex.RLock()
	defer ss.peerHealthsMutex.RUnlock()

	twoMinAgo := time.Now().Add(-2 * time.Minute)
	numCanReach, numTotal := 0, 0
	for _, peer := range ss.peerHealths {
		if peer.LastReachable.After(twoMinAgo) {
			numTotal++
			if lastReachable, ok := peer.ReachablePeers[host]; ok && lastReachable.After(twoMinAgo) {
				numCanReach++
			}
		}
	}
	return numTotal < 5 || numCanReach > numTotal/2
}

func (ss *MediorumServer) updateDiskAndDbStatus(ctx context.Context) {
	dbSize, errStr := getDatabaseSize(ctx, ss.pgPool)
	ss.databaseSize = dbSize
	ss.dbSizeErr = errStr

	uploadsCount, errStr := getUploadsCount(ctx, ss.crud.DB)
	ss.uploadsCount = uploadsCount
	ss.uploadsCountErr = errStr

	// Determine which path to check for disk status
	// If using file storage, check the actual blob storage path, not Config.Dir
	diskPath := ss.Config.Dir
	if strings.HasPrefix(ss.Config.BlobStoreDSN, "file://") {
		// Extract the path from file:// URL (e.g., "file:///data/blobs" -> "/data/blobs")
		_, uri, found := strings.Cut(ss.Config.BlobStoreDSN, "://")
		if found {
			// Remove query parameters if present (e.g., "?no_tmp_dir=true")
			blobPath := strings.Split(uri, "?")[0]
			diskPath = blobPath
		}
	}

	mediorumTotal, mediorumFree, err := getDiskStatus(diskPath)
	if err == nil {
		ss.mediorumPathFree = mediorumFree
		ss.mediorumPathUsed = mediorumTotal - mediorumFree
		ss.mediorumPathSize = mediorumTotal
	} else {
		slog.Error("Error getting mediorum disk status", "err", err, "path", diskPath)
	}

	// Archive bucket disk status (file:// only — mirrors primary's behavior).
	if ss.archiveBucket != nil && strings.HasPrefix(ss.Config.ArchiveBlobStoreDSN, "file://") {
		_, uri, found := strings.Cut(ss.Config.ArchiveBlobStoreDSN, "://")
		if found {
			archivePath := strings.Split(uri, "?")[0]
			archiveTotal, archiveFree, archiveErr := getDiskStatus(archivePath)
			if archiveErr == nil {
				ss.archivePathFree = archiveFree
				ss.archivePathUsed = archiveTotal - archiveFree
				ss.archivePathSize = archiveTotal
			} else {
				slog.Error("Error getting archive disk status", "err", archiveErr, "path", archivePath)
			}
		}
	}

	// The legacy term is derived from qm_cids, which a one-time migration fills
	// and nothing appends to, so this only has to succeed once.
	if !ss.legacyCorpusComputed {
		if legacy, legacyErr := getLegacyCorpusBytes(ctx, ss.pgPool); legacyErr != nil {
			slog.Error("Error getting legacy corpus size", "err", legacyErr.Error())
		} else {
			ss.legacyCorpusBytes = legacy
			ss.legacyCorpusComputed = true
			slog.Info("Legacy corpus size", "bytes", legacy)
		}
	}

	ss.storageExpectation, err = getStorageExpectation(ctx, ss.pgPool, ss.Config.ReplicationFactor, ss.legacyCorpusBytes)
	slog.Info("Storage expectation", "size", ss.storageExpectation)
	slog.Info("Replication factor", "replicationFactor", ss.Config.ReplicationFactor)
	if err != nil {
		slog.Error("Error getting storage expectation", "err", err.Error())
	}
}

func getDiskStatus(path string) (total uint64, free uint64, err error) {
	s := syscall.Statfs_t{}
	err = syscall.Statfs(path, &s)
	if err != nil {
		return
	}

	total = uint64(s.Bsize) * s.Blocks
	free = uint64(s.Bsize) * s.Bfree
	return
}

// Per-artifact costs used to size the corpus. Each is the on-disk consequence
// of a decision made elsewhere in this package, not a fitted parameter.
const (
	// transcodeBytesPerSecond is one second of the 320 kbps CBR mp3 every audio
	// upload is transcoded to (transcode.go's ffmpeg "-b:a 320k"):
	// 320_000 bits / 8.
	transcodeBytesPerSecond = 40_000

	// audioPreviewBytes is one preview clip: audioPreviewDuration (30s) at that
	// same bitrate.
	audioPreviewBytes = 30 * transcodeBytesPerSecond

	// legacyBareBlobBytes and legacyOriginalJpgBytes are mean sizes for the two
	// addressable classes of pre-mediorum Qm content. They exist because the
	// legacy corpus has no size recorded anywhere in Postgres -- qm_cids holds
	// keys only -- so it is invisible to any query over `uploads`, despite
	// being roughly a third of what a node stores.
	//
	// Measured 2026-09-03 by sampling 20,000 of the 2,445,544 addressable
	// qm_cids keys against a storeAll node's /internal/blobs/info endpoint,
	// at a 99.9% hit rate on both classes:
	//
	//	bare Qm CIDs (audio)        8,498 sampled, mean 14.654 MB
	//	.../original.jpg (artwork) 11,502 sampled, mean  0.685 MB
	//
	// Resized variants (.../150x150.jpg and friends) are deliberately excluded:
	// repair deletes them on sight because they are regenerated on demand, and
	// 0 of 1,221 sampled were present on any node. They hold no bytes anywhere.
	//
	// Cross-check: these put legacy at 33.0% of the corpus, against 34.2%
	// measured by a filesystem walk of a different node's blob tree.
	legacyBareBlobBytes    = 14_654_000
	legacyOriginalJpgBytes = 685_000
)

// getLegacyCorpusBytes estimates the size of the pre-mediorum Qm corpus from
// the key shapes in qm_cids.
//
// Deriving it from row counts rather than hardcoding a total keeps it honest on
// networks that have no legacy content: dev and stage have an empty qm_cids and
// correctly get zero, where a flat constant would inflate their expectation by
// the whole of mainnet's history.
//
// The counts are stable -- qm_cids is populated once from the old blobs table
// (ddl/drop_blobs.sql) and only ever synced between peers, never appended to by
// the upload path -- so callers compute this once and keep it.
func getLegacyCorpusBytes(ctx context.Context, p *pgxpool.Pool) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var bare, originalJpg int64
	err := p.QueryRow(ctx, `
SELECT
  COUNT(*) FILTER (WHERE key NOT LIKE '%/%'),
  COUNT(*) FILTER (WHERE key LIKE '%/original.jpg')
FROM qm_cids
`).Scan(&bare, &originalJpg)
	if err != nil {
		return 0, err
	}

	return bare*legacyBareBlobBytes + originalJpg*legacyOriginalJpgBytes, nil
}

// storageExpectationInputs are the corpus components read from the database.
// Split out so the arithmetic is testable without one.
type storageExpectationInputs struct {
	// OriginalBytes is the summed size of every uploaded file, audio and image
	// alike, exactly as ffprobe measured it.
	OriginalBytes int64
	// AudioSeconds is the total duration of audio uploads, which is what the
	// 320 kbps transcode costs are derived from.
	AudioSeconds float64
	// PreviewCount is rows in audio_previews. Previews also appear in an
	// upload's transcode_results, so counting both would double them.
	PreviewCount int64
	// NodeCount is registered endpoints that store content.
	NodeCount int64
}

// computeStorageExpectation returns this node's share of the corpus.
//
// Every node stores replicationFactor/nodeCount of the network's content, so
// the expectation is the whole corpus scaled by that fraction. The corpus is
// the sum of what actually lands on disk:
//
//	originals + 320 kbps transcodes + preview clips + the legacy Qm corpus
//
// This replaced `originals * 2`, where the doubling stood in for "we store a
// transcode as well as the original". That was wrong in both directions at
// once. It over-counted transcodes, since most originals are lossless or
// high-bitrate and downsample to well under their own size (measured across
// mainnet: 0.66 TB of transcodes against 1.06 TB of originals, a ratio of 0.63,
// and it doubled image uploads, which have no transcode at all). And it
// omitted previews and the entire legacy corpus, which together are larger
// than the over-count -- leaving the published figure ~16% below what nodes
// actually hold.
//
// Note this remains a fair-share model: it describes a node storing its
// rendezvous share and says nothing about one running StoreAll, StoreRecent, or
// an archive tier, whose footprint is set by its retention config instead.
func computeStorageExpectation(in storageExpectationInputs, replicationFactor int, legacyCorpusBytes int64) uint64 {
	if in.NodeCount <= 0 || replicationFactor <= 0 {
		return 0
	}

	corpus := in.OriginalBytes +
		int64(in.AudioSeconds*transcodeBytesPerSecond) +
		in.PreviewCount*audioPreviewBytes +
		legacyCorpusBytes
	if corpus <= 0 {
		return 0
	}

	return uint64(corpus * int64(replicationFactor) / in.NodeCount)
}

func getStorageExpectation(ctx context.Context, p *pgxpool.Pool, replicationFactor int, legacyCorpusBytes int64) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var in storageExpectationInputs
	err := p.QueryRow(ctx, `
WITH uploads_size AS (
  SELECT
    COALESCE(SUM(NULLIF((ff_probe::jsonb)->'format'->>'size', '')::bigint), 0) AS original_bytes,
    COALESCE(SUM(NULLIF((ff_probe::jsonb)->'format'->>'duration', '')::float8)
             FILTER (WHERE template = 'audio'), 0) AS audio_seconds
  FROM uploads
),
preview_count AS (
  SELECT COUNT(*) AS n FROM audio_previews
),
node_count AS (
  SELECT COUNT(*) AS n
  FROM eth_registered_endpoints
  WHERE service_type IN ('content-node', 'validator')
)
SELECT original_bytes, audio_seconds, preview_count.n, node_count.n
FROM uploads_size, preview_count, node_count
`).Scan(&in.OriginalBytes, &in.AudioSeconds, &in.PreviewCount, &in.NodeCount)
	if err != nil {
		return 0, err
	}

	return computeStorageExpectation(in, replicationFactor, legacyCorpusBytes), nil
}

func getDatabaseSize(ctx context.Context, p *pgxpool.Pool) (size uint64, errStr string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := p.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&size); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			errStr = "timeout getting database size within 10s: " + err.Error()
		} else {
			errStr = "error getting database size: " + err.Error()
		}
	}

	return
}

func getUploadsCount(ctx context.Context, db *gorm.DB) (count int64, errStr string) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	if err := db.WithContext(ctx).Model(&Upload{}).Count(&count).Error; err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			errStr = "timeout getting uploads count within 60s: " + err.Error()
		} else {
			errStr = "error getting uploads count: " + err.Error()
		}
	}

	return
}

func (ss *MediorumServer) serveStorageAndDbLogs(c echo.Context) error {
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

	loggedBeforeStr := c.QueryParam("before")
	var loggedBefore time.Time
	if loggedBeforeStr != "" {
		loggedBefore, err = time.Parse(time.RFC3339Nano, loggedBeforeStr)
		if err != nil {
			return c.String(http.StatusBadRequest, "Invalid time format. Use RFC3339Nano or leave blank.")
		}
	}
	dbQuery := ss.crud.DB.Order("logged_at desc").Limit(limit)
	if !loggedBefore.IsZero() {
		dbQuery = dbQuery.Where("logged_at < ?", loggedBefore)
	}

	host := c.QueryParam("host")
	if host == "" {
		host = ss.Config.Self.Host
	}
	dbQuery = dbQuery.Where("host = ?", host)

	var logs []StorageAndDbSize
	if err := dbQuery.Find(&logs).Error; err != nil {
		return c.String(http.StatusInternalServerError, "DB query failed: "+err.Error())
	}

	return c.JSON(http.StatusOK, logs)
}
