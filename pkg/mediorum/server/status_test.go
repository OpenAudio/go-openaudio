package server

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// getHealth runs on every /health-check while the background pollers rewrite
// the status block underneath it, and its result is marshalled by the handler
// after any lock getHealth took is long gone. Everything it reads therefore has
// to be copied out under a lock: reading a status field or the live peerHealths
// map directly fails this test under -race.
func TestGetHealthRacesWithPollers(t *testing.T) {
	const peer = "http://peer.test"

	ss := &MediorumServer{
		bucket:      openMemBucket(t),
		logger:      zap.NewNop(),
		peerHealths: map[string]*PeerHealth{peer: {ReachablePeers: map[string]time.Time{}}},
	}

	stop := make(chan struct{})
	var writers sync.WaitGroup

	// Stands in for monitorMetrics and the repairer.
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			ss.statusMutex.Lock()
			ss.databaseSize = uint64(i + 1)
			ss.dbSizeErr = ""
			ss.uploadsCount = int64(i)
			ss.uploadsCountErr = ""
			ss.mediorumPathFree = uint64(i)
			ss.mediorumPathUsed = uint64(i)
			ss.mediorumPathSize = uint64(i)
			ss.storageExpectation = uint64(i)
			ss.lastSuccessfulRepair = RepairTracker{Counters: map[string]int{"repaired": i}}
			ss.statusMutex.Unlock()
		}
	}()

	// The real bucket write canary, writing bucketWriteErr.
	writers.Add(1)
	go func() {
		defer writers.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			ss.runBucketWriteCanary(context.Background())
		}
	}()

	// Stands in for the health poller, which mutates PeerHealth in place.
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			ss.peerHealthsMutex.Lock()
			peerHealth := ss.peerHealths[peer]
			peerHealth.LastReachable = time.Now()
			peerHealth.LastHealthy = time.Now()
			peerHealth.Version = strconv.Itoa(i)
			peerHealth.ReachablePeers[peer] = time.Now()
			ss.peerHealthsMutex.Unlock()
		}
	}()

	// Stands in for the health-check handlers: read, then marshal outside any
	// lock, exactly as serveMediorumHealthCheck does.
	for i := 0; i < 500; i++ {
		_, err := json.Marshal(ss.getHealth())
		require.NoError(t, err)
		ss.dbHealthy()
		ss.diskHasSpace()
	}

	close(stop)
	writers.Wait()
}
