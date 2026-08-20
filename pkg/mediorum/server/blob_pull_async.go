package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	// asyncPullWorkers bounds how many transfers this node runs at once.
	//
	// This is the backpressure that a synchronous pull used to provide by
	// accident: the sender held one of its own workers for the duration, so no
	// node could have more transfers in flight than the sender had workers. A
	// sender that returns immediately can queue as fast as it enumerates, so the
	// limit has to live here, on the side actually moving the bytes.
	asyncPullWorkers = 3

	// asyncPullQueueDepth is deliberately shallow. A deep queue would accept
	// work this node cannot start for a long time, and the sender would have
	// stopped waiting for it -- 503 tells the sender to come back on its next
	// sweep instead, when the picture may have changed.
	asyncPullQueueDepth = 32

	// asyncPullTimeout bounds one queued transfer. The request context cannot be
	// used: it is cancelled the moment the handler returns 202.
	asyncPullTimeout = 60 * time.Minute
)

// errAsyncPullQueueFull is answered with 503, which senders treat as a plain
// failure. It must not look like "pull unsupported", or the sender falls back
// to pushing the bytes at a node that just said it was busy.
var errAsyncPullQueueFull = errors.New("pull queue is full")

type asyncPullJob struct {
	sourceHost     string
	cid            string
	placementHosts []string
	uploadID       string
	transcoded     bool
}

// enqueueAsyncPull accepts a transfer to run in the background, or reports why
// it will not.
//
// Deduplication matters more here than it did synchronously. The sender's sweep,
// other senders, and repair can all ask for the same cid, and previously the
// combination of haveInMyBucket and a blocked caller kept that to one transfer
// at a time. Nothing blocks now, so the in-flight set is what prevents the same
// blob being fetched several times over.
func (ss *MediorumServer) enqueueAsyncPull(job asyncPullJob) error {
	ss.asyncPullMu.Lock()
	if _, running := ss.asyncPullInFlight[job.cid]; running {
		ss.asyncPullMu.Unlock()
		return nil
	}
	ss.asyncPullInFlight[job.cid] = struct{}{}
	ss.asyncPullMu.Unlock()

	select {
	case ss.asyncPullQueue <- job:
		return nil
	default:
		ss.releaseAsyncPull(job.cid)
		return errAsyncPullQueueFull
	}
}

func (ss *MediorumServer) releaseAsyncPull(cid string) {
	ss.asyncPullMu.Lock()
	delete(ss.asyncPullInFlight, cid)
	ss.asyncPullMu.Unlock()
}

func (ss *MediorumServer) startAsyncPullWorkers(ctx context.Context) error {
	ss.logger.Info("starting async blob pull workers", zap.Int("count", asyncPullWorkers))

	var wg sync.WaitGroup
	for range asyncPullWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ss.asyncPullWorker(ctx)
		}()
	}
	wg.Wait()
	return nil
}

func (ss *MediorumServer) asyncPullWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-ss.asyncPullQueue:
			ss.runAsyncPull(ctx, job)
		}
	}
}

func (ss *MediorumServer) runAsyncPull(parent context.Context, job asyncPullJob) {
	defer ss.releaseAsyncPull(job.cid)

	// Deliberately not the request context, which died with the 202 response.
	// Parented to the server's lifecycle so shutdown still cancels in-flight
	// transfers rather than leaking them.
	ctx, cancel := context.WithTimeout(parent, asyncPullTimeout)
	defer cancel()

	err := ss.pullFileFromHostValidated(ctx, job.sourceHost, job.cid, job.placementHosts, job.uploadID, job.transcoded)
	if err != nil {
		// Nothing is waiting on this, so a log is the only report. The sender
		// finds out on its next sweep, when the peer answers something other
		// than already_present.
		ss.logger.Warn("async blob pull failed",
			zap.String("sourceHost", job.sourceHost),
			zap.String("cid", job.cid),
			zap.Error(err),
		)
		return
	}
	ss.logger.Debug("async blob pull complete", zap.String("cid", job.cid))
}
