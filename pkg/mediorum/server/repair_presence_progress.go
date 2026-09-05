package server

import (
	"context"
	"sync/atomic"
	"time"

	storagev1 "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// presenceWalkLogInterval is how often an in-flight presence walk reports.
//
// The walk can run for hours on a large file:// bucket and produces no other
// output until it finishes, because runRepair's first saveTracker comes after
// it. One line a minute is enough to tell a running walk from a hung one
// without burying the log on nodes where it takes seconds.
const presenceWalkLogInterval = time.Minute

// presenceWalkCounter accumulates progress for one bucket's walk. Workers
// increment it concurrently; the reporter and the health endpoint read it.
type presenceWalkCounter struct {
	bucket    string
	dir       string
	startedAt time.Time
	files     atomic.Int64
	shards    atomic.Int64
}

func newPresenceWalkCounter(bucket, dir string) *presenceWalkCounter {
	return &presenceWalkCounter{bucket: bucket, dir: dir, startedAt: time.Now()}
}

func (p *presenceWalkCounter) addFile() {
	if p != nil {
		p.files.Add(1)
	}
}

func (p *presenceWalkCounter) addShard() {
	if p != nil {
		p.shards.Add(1)
	}
}

// PresenceWalkProgress is the health-endpoint view of an in-flight walk. It is
// null when no walk is running.
//
// This exists because the walk is otherwise invisible: it runs before repair's
// first checkpoint, so the repair log shows the *previous* run as the latest
// and there is no tracker row for the current one at all. Without this the only
// way to distinguish a walking node from a wedged one is a goroutine dump.
type PresenceWalkProgress struct {
	Bucket       string    `json:"bucket"` // "primary" or "archive"
	Dir          string    `json:"dir"`
	Files        int64     `json:"files"`
	Shards       int64     `json:"shards"`
	StartedAt    time.Time `json:"startedAt"`
	ElapsedHuman string    `json:"elapsedHuman"`
	FilesPerSec  float64   `json:"filesPerSec"`
}

func (p *presenceWalkCounter) snapshot() *PresenceWalkProgress {
	elapsed := time.Since(p.startedAt)
	files := p.files.Load()
	rate := 0.0
	if secs := elapsed.Seconds(); secs > 0 {
		rate = float64(files) / secs
	}
	return &PresenceWalkProgress{
		Bucket:       p.bucket,
		Dir:          p.dir,
		Files:        files,
		Shards:       p.shards.Load(),
		StartedAt:    p.startedAt,
		ElapsedHuman: elapsed.Truncate(time.Second).String(),
		FilesPerSec:  rate,
	}
}

// presenceWalkProgress returns the in-flight walk, or nil if none is running.
func (ss *MediorumServer) presenceWalkProgress() *PresenceWalkProgress {
	p := ss.presenceWalk.Load()
	if p == nil {
		return nil
	}
	return p.snapshot()
}

// trackPresenceWalk publishes p for the health endpoint and starts a periodic
// log line. The returned func stops the reporter and clears the published
// progress; callers should defer it.
func (ss *MediorumServer) trackPresenceWalk(ctx context.Context, p *presenceWalkCounter) func() {
	ss.presenceWalk.Store(p)
	ss.logger.Info("building presence index",
		zap.String("bucket", p.bucket), zap.String("dir", p.dir))

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(presenceWalkLogInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				s := p.snapshot()
				ss.logger.Info("building presence index (in progress)",
					zap.String("bucket", s.Bucket),
					zap.Int64("files", s.Files),
					zap.Int64("shards", s.Shards),
					zap.String("elapsed", s.ElapsedHuman),
					zap.Float64("filesPerSec", s.FilesPerSec))
			}
		}
	}()

	return func() {
		close(done)
		ss.presenceWalk.Store(nil)
		s := p.snapshot()
		ss.logger.Info("presence index walk finished",
			zap.String("bucket", s.Bucket),
			zap.Int64("files", s.Files),
			zap.Int64("shards", s.Shards),
			zap.String("took", s.ElapsedHuman),
			zap.Float64("filesPerSec", s.FilesPerSec))
	}
}

// presenceWalkProto exposes an in-flight walk to the console. Nil when no walk
// is running, which is how the storage page decides whether to describe the
// current cycle from its tracker row or from the walk.
func (ss *MediorumServer) presenceWalkProto() *storagev1.PresenceWalk {
	p := ss.presenceWalk.Load()
	if p == nil {
		return nil
	}
	s := p.snapshot()
	return &storagev1.PresenceWalk{
		Bucket:      s.Bucket,
		Dir:         s.Dir,
		Files:       s.Files,
		Shards:      s.Shards,
		StartedAt:   timestamppb.New(s.StartedAt),
		FilesPerSec: s.FilesPerSec,
	}
}
