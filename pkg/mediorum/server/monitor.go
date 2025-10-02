package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
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
	StorageExpectation uint64    `gorm:"not null"`
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

		err = ss.crud.Create(&status, crudr.WithSkipBroadcast())
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
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// persist the sizes in the db and let crudr share them with other nodes
	ss.lc.AddManagedRoutine("storage and db size recorder", ss.recordStorageAndDbSize)

	ticker.Reset(10 * time.Minute)
	for {
		select {
		case <-ticker.C:
			ss.updateDiskAndDbStatus(ctx)
			ss.updateTranscodeStats(ctx)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
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

	mediorumTotal, mediorumFree, err := getDiskStatus(ss.Config.Dir)
	if err == nil {
		ss.mediorumPathFree = mediorumFree
		ss.mediorumPathUsed = mediorumTotal - mediorumFree
		ss.mediorumPathSize = mediorumTotal
	} else {
		slog.Error("Error getting mediorum disk status", "err", err)
	}
	ss.storageExpectation, err = getStorageExpectation(ctx, ss.pgPool, ss.Config.ReplicationFactor)
	slog.Info("Storage expectation", "size", ss.storageExpectation)
	slog.Info("Replication factor", "replicationFactor", ss.Config.ReplicationFactor)
	slog.Info("running job")
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

func getStorageExpectation(ctx context.Context, p *pgxpool.Pool, replicationFactor int) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var size uint64
	// Calculate storage expectation as
	//   (total_size * 2 * replication_factor) / validator_count
	// * 2 because we store transcode files in addition to original files
	query := fmt.Sprintf(`
WITH total_size AS (
  SELECT
    COALESCE(SUM((ff_probe::jsonb->'format'->>'size')::bigint), 0) AS s
  FROM uploads
),
validator_count AS (
  SELECT COUNT(*) AS n
  FROM core_validators
  WHERE node_type = 'content-node' OR node_type = 'validator'
)
SELECT
	CASE
		WHEN n > 0 THEN ((s * 2) * %d) / n
		ELSE 0
	END
FROM total_size, validator_count
`, replicationFactor)
	var result int64
	if err := p.QueryRow(ctx, query).Scan(&result); err != nil {
		return 0, err
	}
	size = uint64(result)

	return size, nil
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
