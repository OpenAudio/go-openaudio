package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/zap"
)

const (
	// DefaultAsyncPullWorkers bounds how many transfers this node runs at once.
	// Exported because mediorum.go reads it as the env fallback, the way
	// DefaultOpsRetention already is.
	//
	// This is the backpressure that a synchronous pull used to provide by
	// accident: the sender held one of its own workers for the duration, so no
	// node could have more transfers in flight than the sender had workers. A
	// sender that returns immediately can queue as fast as it enumerates, so the
	// limit has to live here, on the side actually moving the bytes.
	//
	// Six rather than the sender's three. Mirroring the sender's worker count
	// was the wrong reference once the limit moved: the receiver is the
	// bottleneck now, and the synchronous handler it replaced ran one goroutine
	// per request with no ceiling at all, so a number chosen to match the
	// sender is a much sharper cut than it looks. Operators tune it with
	// OPENAUDIO_ASYNC_PULL_WORKERS; see MediorumConfig.AsyncPullWorkers.
	DefaultAsyncPullWorkers = 6

	// maxAsyncPullWorkers caps what an operator can ask for. Each worker holds
	// a whole blob in staging, so an unbounded value would demand staging
	// headroom no disk can satisfy and refuse every pull -- and it would
	// overflow the multiplication in pullStagingMinFree.
	maxAsyncPullWorkers = 64

	// asyncPullQueueDepth is deliberately shallow. A deep queue would accept
	// work this node cannot start for a long time, and the sender would have
	// stopped waiting for it -- 503 tells the sender to come back on its next
	// sweep instead, when the picture may have changed.
	asyncPullQueueDepth = 32

	// DefaultAsyncPullTimeout bounds one queued transfer. The request context
	// cannot be used: it is cancelled the moment the handler returns 202.
	//
	// It also bounds how long the source must keep serving the blob. A
	// synchronous pull enforced that structurally -- the sender held the
	// connection, so it knew when the peer was finished reading its bucket.
	// Answering 202 gives that up, so the source's retention is now an
	// invariant nothing checks.
	//
	// It holds today with room to spare. The two paths that delete a blob are
	// guarded by wall clock, not by replication: repair only drops an
	// over-replicated blob whose ModTime is older than a week
	// (wasReplicatedThisWeek), and prune only drops an unpublished upload older
	// than unpublishedUploadAge, 30 days. Either is orders of magnitude beyond
	// this timeout. Tightening one of them below it would break replication
	// silently, since the puller would simply see the object vanish mid
	// transfer.
	DefaultAsyncPullTimeout = 60 * time.Minute
)

// asyncPullWorkers is the configured worker count, clamped to something a
// staging disk can actually back.
func (ss *MediorumServer) asyncPullWorkers() int {
	n := ss.Config.AsyncPullWorkers
	if n <= 0 {
		return DefaultAsyncPullWorkers
	}
	return min(n, maxAsyncPullWorkers)
}

func (ss *MediorumServer) asyncPullTimeout() time.Duration {
	if ss.Config.AsyncPullTimeout <= 0 {
		return DefaultAsyncPullTimeout
	}
	return ss.Config.AsyncPullTimeout
}

// errAsyncPullQueueFull is answered with 503, which senders treat as a plain
// failure. It must not look like "pull unsupported", or the sender falls back
// to pushing the bytes at a node that just said it was busy.
var errAsyncPullQueueFull = errors.New("pull queue is full")

// The two things a 202 can mean. The distinction is the sender's only way to
// tell a transfer that is still running from one that ended without producing
// the blob: a peer that already holds it answers 200 already_present, and a
// peer that is still working answers in_progress, so a second accepted for a
// cid we handed off earlier says the earlier attempt is over and the blob is
// not there. That is a failure signal derived from the peer's own state, which
// is the same evidence already_present rests on -- no callback, and nothing the
// peer has to promise.
const (
	asyncPullStatusAccepted   = "accepted"
	asyncPullStatusInProgress = "in_progress"
)

// asyncPullAdmission says which of those two an enqueue produced.
type asyncPullAdmission int

const (
	// asyncPullQueued: this node took the transfer on just now.
	asyncPullQueued asyncPullAdmission = iota
	// asyncPullRunning: a transfer for this cid was already under way, so the
	// request was folded into it rather than queued again.
	asyncPullRunning
)

func (a asyncPullAdmission) status() string {
	if a == asyncPullRunning {
		return asyncPullStatusInProgress
	}
	return asyncPullStatusAccepted
}

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
//
// The marker is published only for a job that actually reached the queue, and
// the lock is held across the send to keep those two the same event. Marking
// first and unmarking on a full queue leaves a window where a second caller
// reads the marker, is told the transfer is under way, and goes on waiting for
// one that was never queued -- and 202 is the only thing that answer can be, so
// a caller has no way to find out otherwise until its next sweep.
//
// Holding the lock across the send is safe because the send cannot block: the
// default arm makes it a constant-time try, so a worker calling
// releaseAsyncPull can always take the lock.
func (ss *MediorumServer) enqueueAsyncPull(job asyncPullJob) (asyncPullAdmission, error) {
	ss.asyncPullMu.Lock()
	defer ss.asyncPullMu.Unlock()

	if _, running := ss.asyncPullInFlight[job.cid]; running {
		return asyncPullRunning, nil
	}

	select {
	case ss.asyncPullQueue <- job:
		ss.asyncPullInFlight[job.cid] = struct{}{}
		return asyncPullQueued, nil
	default:
		return asyncPullQueued, errAsyncPullQueueFull
	}
}

func (ss *MediorumServer) releaseAsyncPull(cid string) {
	ss.asyncPullMu.Lock()
	delete(ss.asyncPullInFlight, cid)
	ss.asyncPullMu.Unlock()
}

func (ss *MediorumServer) startAsyncPullWorkers(ctx context.Context) error {
	workers := ss.asyncPullWorkers()
	ss.logger.Info("starting async blob pull workers", zap.Int("count", workers))

	var wg sync.WaitGroup
	for range workers {
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
	ctx, cancel := context.WithTimeout(parent, ss.asyncPullTimeout())
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
