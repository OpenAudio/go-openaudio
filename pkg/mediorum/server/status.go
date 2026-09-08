package server

// serverStatus is a copy of the node status the background pollers publish:
// disk, database, bucket, and the last completed repair runs.
//
// Handlers take the whole block at once instead of reading the fields off
// MediorumServer one at a time, which races with the pollers: monitorMetrics
// rewrites these on every tick, and the write canary and repairer write their
// own, while health checks, blob reads and the diagnostics RPC read them.
type serverStatus struct {
	mediorumPathUsed   uint64
	mediorumPathSize   uint64
	mediorumPathFree   uint64
	storageExpectation uint64

	archivePathUsed uint64
	archivePathSize uint64
	archivePathFree uint64

	databaseSize    uint64
	dbSizeErr       string
	uploadsCount    int64
	uploadsCountErr string
	bucketWriteErr  string

	lastSuccessfulRepair  RepairTracker
	lastSuccessfulCleanup RepairTracker
}

// status returns the currently published status block.
func (ss *MediorumServer) status() serverStatus {
	ss.statusMutex.RLock()
	defer ss.statusMutex.RUnlock()
	return serverStatus{
		mediorumPathUsed:      ss.mediorumPathUsed,
		mediorumPathSize:      ss.mediorumPathSize,
		mediorumPathFree:      ss.mediorumPathFree,
		storageExpectation:    ss.storageExpectation,
		archivePathUsed:       ss.archivePathUsed,
		archivePathSize:       ss.archivePathSize,
		archivePathFree:       ss.archivePathFree,
		databaseSize:          ss.databaseSize,
		dbSizeErr:             ss.dbSizeErr,
		uploadsCount:          ss.uploadsCount,
		uploadsCountErr:       ss.uploadsCountErr,
		bucketWriteErr:        ss.bucketWriteErr,
		lastSuccessfulRepair:  ss.lastSuccessfulRepair,
		lastSuccessfulCleanup: ss.lastSuccessfulCleanup,
	}
}

// dbHealthy reports whether the last poll reached the database. Callers that
// only gate on the database use this instead of copying the whole block.
func (ss *MediorumServer) dbHealthy() bool {
	ss.statusMutex.RLock()
	defer ss.statusMutex.RUnlock()
	return ss.databaseSize > 0 && ss.dbSizeErr == "" && ss.uploadsCountErr == ""
}

// diskFree returns the free bytes last measured for the primary and archive
// blob store paths. dsnHasSpace only falls back to these when it cannot statfs
// the path itself, so the two are always read together.
func (ss *MediorumServer) diskFree() (primary, archive uint64) {
	ss.statusMutex.RLock()
	defer ss.statusMutex.RUnlock()
	return ss.mediorumPathFree, ss.archivePathFree
}
