package server

import (
	"fmt"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func storeAllIntakeTestServer(t *testing.T, storeAll bool) *MediorumServer {
	t.Helper()
	ss := asyncPullTestServer(t, 16)
	ss.Config.StoreAll = storeAll
	return ss
}

func drainIntakeQueue(ss *MediorumServer) []asyncPullJob {
	jobs := []asyncPullJob{}
	for {
		select {
		case job := <-ss.asyncPullQueue:
			jobs = append(jobs, job)
		default:
			return jobs
		}
	}
}

func drainIntakeCIDs(ss *MediorumServer) []string {
	cids := []string{}
	for _, job := range drainIntakeQueue(ss) {
		// Release too, so the in-flight set cannot stand in for the predicate
		// and make a repeat-offer assertion pass on any implementation.
		ss.releaseAsyncPull(job.cid)
		cids = append(cids, job.cid)
	}
	return cids
}

// intakeUpload is the row as its creator first published it: the original
// exists, no transcoder has claimed it yet. This is the op that announces the
// original.
func intakeUpload() *Upload {
	return &Upload{
		ID:               "up1",
		OrigFileCID:      "cid-orig",
		TranscodeResults: map[string]string{},
		CreatedBy:        "http://peer1",
		Mirrors:          []string{"http://peer1"},
	}
}

// intakeTranscodedUpload is the same row once transcode has published the 320.
// This is the op that announces the 320; the original's cid is still on it, but
// it is a repeat by then.
func intakeTranscodedUpload() *Upload {
	u := intakeUpload()
	u.TranscodeResults = map[string]string{"320": "cid-320"}
	u.TranscodedBy = "http://peer2"
	u.TranscodedMirrors = []string{"http://peer2"}
	return u
}

func uploadsOp(action string, uploads ...*Upload) (*crudr.Op, interface{}) {
	list := uploads
	return &crudr.Op{Table: "uploads", Action: action}, &list
}

func TestStoreAllIntakeUnmirrored(t *testing.T) {
	const producer = "http://producer"
	cases := []struct {
		name    string
		mirrors []string
		want    bool
		why     string
	}{
		{"nil", nil, true,
			"a producer outside the placement set for its own blob records no mirror at all"},
		{"empty", []string{}, true, ""},
		{"exactly the producer", []string{producer}, true, ""},
		{"someone else", []string{"http://other"}, false,
			"another node holds a copy, so this is not the announcing op"},
		{"producer plus one", []string{producer, "http://other"}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, storeAllIntakeUnmirrored(tc.mirrors, producer), tc.why)
		})
	}
}

func TestStoreAllIntakeCIDs(t *testing.T) {
	audio := func(mut func(*Upload)) *Upload {
		u := intakeTranscodedUpload()
		mut(u)
		return u
	}

	t.Run("row as its creator first published it offers the original", func(t *testing.T) {
		assert.Equal(t, []string{"cid-orig"}, storeAllIntakeCIDs(intakeUpload()))
	})

	// Once a transcoder has claimed the row, every op still carries the
	// original's cid but none of them is announcing it any more.
	t.Run("post-transcode row offers only the 320", func(t *testing.T) {
		assert.Equal(t, []string{"cid-320"}, storeAllIntakeCIDs(intakeTranscodedUpload()))
	})

	t.Run("original already mirrored elsewhere is not re-offered", func(t *testing.T) {
		u := intakeUpload()
		u.Mirrors = []string{"http://peer1", "http://peer9"}
		assert.Empty(t, storeAllIntakeCIDs(u))
	})

	t.Run("320 already mirrored elsewhere offers nothing", func(t *testing.T) {
		u := audio(func(u *Upload) { u.TranscodedMirrors = []string{"http://peer2", "http://peer9"} })
		assert.Empty(t, storeAllIntakeCIDs(u))
	})

	t.Run("both mirrored offers nothing", func(t *testing.T) {
		u := audio(func(u *Upload) {
			u.Mirrors = []string{"http://peer1", "http://peer9"}
			u.TranscodedMirrors = []string{"http://peer2", "http://peer9"}
		})
		assert.Empty(t, storeAllIntakeCIDs(u),
			"this is the repeat op; offering again is the noise the predicate exists to remove")
	})

	// The case that rules out keying on "exactly one mirror". A producer only
	// adds itself when it is a placement target for the cid it produced, which
	// depends on rendezvous rank -- so an empty list is a normal announcement,
	// not a broken row.
	t.Run("producer that is not a placement target still announces", func(t *testing.T) {
		u := audio(func(u *Upload) { u.TranscodedMirrors = []string{} })
		assert.Equal(t, []string{"cid-320"}, storeAllIntakeCIDs(u),
			"an empty mirror list is a normal announcement, not a broken row")

		pre := intakeUpload()
		pre.Mirrors = []string{}
		assert.Equal(t, []string{"cid-orig"}, storeAllIntakeCIDs(pre))
	})

	// Images never populate transcoded_mirrors, so the transcode arm is
	// permanently true for them -- they have to be covered by the original arm,
	// and their sole transcode result is the original's own cid.
	t.Run("image offers its one blob once", func(t *testing.T) {
		u := &Upload{
			ID:               "img1",
			OrigFileCID:      "cid-img",
			TranscodeResults: map[string]string{"original.jpg": "cid-img"},
			CreatedBy:        "http://peer1",
			Mirrors:          []string{"http://peer1"},
		}
		assert.Equal(t, []string{"cid-img"}, storeAllIntakeCIDs(u))
	})

	t.Run("image already mirrored offers nothing", func(t *testing.T) {
		u := &Upload{
			OrigFileCID:      "cid-img",
			TranscodeResults: map[string]string{"original.jpg": "cid-img"},
			CreatedBy:        "http://peer1",
			Mirrors:          []string{"http://peer1", "http://peer9"},
		}
		assert.Empty(t, storeAllIntakeCIDs(u),
			"an empty transcoded_mirrors must not keep an image live forever")
	})

	t.Run("previews ride with the transcode arm", func(t *testing.T) {
		u := audio(func(u *Upload) { u.TranscodeResults["preview"] = "cid-prev" })
		assert.ElementsMatch(t, []string{"cid-320", "cid-prev"}, storeAllIntakeCIDs(u))
	})
}

func TestStoreAllIntakeEnqueuesWithSourceAndFlags(t *testing.T) {
	ss := storeAllIntakeTestServer(t, true)

	byCID := map[string]asyncPullJob{}
	for _, u := range []*Upload{intakeUpload(), intakeTranscodedUpload()} {
		op, records := uploadsOp(crudr.ActionUpdate, u)
		ss.storeAllIntakeFromOp(op, records)
		for _, job := range drainIntakeQueue(ss) {
			ss.releaseAsyncPull(job.cid)
			byCID[job.cid] = job
		}
	}
	require.Len(t, byCID, 2)

	assert.Equal(t, "http://peer1", byCID["cid-orig"].sourceHost)
	assert.False(t, byCID["cid-orig"].transcoded)
	assert.Equal(t, "up1", byCID["cid-orig"].uploadID)

	assert.Equal(t, "http://peer2", byCID["cid-320"].sourceHost, "the 320 sources from a transcoded mirror")
	assert.True(t, byCID["cid-320"].transcoded,
		"the 320 must be flagged; it is what lets the puller skip resolveWaveformUploadID")
}

// The point of the predicate: an upload row is rewritten many times and every
// op repeats the same cids. Only the op that announces a blob should act.
func TestStoreAllIntakeIgnoresRepeatOps(t *testing.T) {
	ss := storeAllIntakeTestServer(t, true)

	for _, u := range []*Upload{intakeUpload(), intakeTranscodedUpload()} {
		op, records := uploadsOp(crudr.ActionCreate, u)
		ss.storeAllIntakeFromOp(op, records)
		require.NotEmpty(t, drainIntakeCIDs(ss))
	}

	// The same upload once replication has recorded copies elsewhere, which is
	// what every subsequent op for it looks like.
	replicated := intakeTranscodedUpload()
	replicated.Mirrors = []string{"http://peer1", "http://peer9"}
	replicated.TranscodedMirrors = []string{"http://peer2", "http://peer9"}
	repeat, repeatRecords := uploadsOp(crudr.ActionUpdate, replicated)

	for range 5 {
		ss.storeAllIntakeFromOp(repeat, repeatRecords)
	}
	assert.Empty(t, drainIntakeCIDs(ss), "repeat ops must not re-offer blobs")
}

// The callback fires on every node that applies the op, including nodes that
// hold nothing they are not ranked for.
func TestStoreAllIntakeIgnoredWhenNotStoreAll(t *testing.T) {
	ss := storeAllIntakeTestServer(t, false)
	op, records := uploadsOp(crudr.ActionUpdate, intakeUpload())

	ss.storeAllIntakeFromOp(op, records)

	assert.Empty(t, drainIntakeQueue(ss))
}

func TestStoreAllIntakeIgnoresIrrelevantOps(t *testing.T) {
	upload := intakeUpload()
	cases := []struct {
		name    string
		op      *crudr.Op
		records interface{}
	}{
		{"other table", &crudr.Op{Table: "audio_previews", Action: crudr.ActionCreate}, &[]*Upload{upload}},
		{"delete", &crudr.Op{Table: "uploads", Action: crudr.ActionDelete}, &[]*Upload{upload}},
		{"nil op", nil, &[]*Upload{upload}},
		{"records of another type", &crudr.Op{Table: "uploads", Action: crudr.ActionUpdate}, &[]*AudioPreview{{}}},
		{"nil records", &crudr.Op{Table: "uploads", Action: crudr.ActionUpdate}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ss := storeAllIntakeTestServer(t, true)
			// A panic here would take down chain op sync, not just intake.
			require.NotPanics(t, func() { ss.storeAllIntakeFromOp(tc.op, tc.records) })
			assert.Empty(t, drainIntakeQueue(ss))
		})
	}
}

func TestStoreAllIntakeSourceSkipsSelf(t *testing.T) {
	ss := storeAllIntakeTestServer(t, true)
	self := ss.Config.Self.Host

	t.Run("prefers a mirror over the producer", func(t *testing.T) {
		upload := &Upload{Mirrors: []string{"http://peer1"}, CreatedBy: "http://peer9"}
		assert.Equal(t, "http://peer1", ss.storeAllIntakeSource(upload, false))
	})

	t.Run("falls past self to the next host", func(t *testing.T) {
		upload := &Upload{Mirrors: []string{self, "http://peer1"}}
		assert.Equal(t, "http://peer1", ss.storeAllIntakeSource(upload, false))
	})

	t.Run("falls back to the producer when no mirror is recorded", func(t *testing.T) {
		upload := &Upload{TranscodedBy: "http://peer3"}
		assert.Equal(t, "http://peer3", ss.storeAllIntakeSource(upload, true))
	})

	t.Run("empty when only self holds it", func(t *testing.T) {
		upload := &Upload{Mirrors: []string{self}, CreatedBy: self}
		assert.Empty(t, ss.storeAllIntakeSource(upload, false),
			"pulling from self is the one source that can never work")
	})
}

// Our own op comes back around through the chain; there is nobody else to ask
// yet, and a job with an empty source would burn a pull worker on a request
// that cannot be addressed.
func TestStoreAllIntakeSkipsWhenOnlySelfHoldsTheBlob(t *testing.T) {
	ss := storeAllIntakeTestServer(t, true)
	upload := intakeTranscodedUpload()
	upload.CreatedBy = ss.Config.Self.Host
	upload.Mirrors = []string{ss.Config.Self.Host}
	upload.TranscodedBy = ss.Config.Self.Host
	upload.TranscodedMirrors = []string{ss.Config.Self.Host}
	op, records := uploadsOp(crudr.ActionCreate, upload)

	ss.storeAllIntakeFromOp(op, records)

	assert.Empty(t, drainIntakeQueue(ss))
}

// The queue is shared with peer-driven pulls, which have a sender waiting.
func TestStoreAllIntakeYieldsToAFullQueue(t *testing.T) {
	ss := storeAllIntakeTestServer(t, true)
	ss.asyncPullQueue = make(chan asyncPullJob, 1)
	u := intakeTranscodedUpload()
	u.TranscodeResults["preview"] = "cid-prev"
	op, records := uploadsOp(crudr.ActionUpdate, u)

	require.NotPanics(t, func() { ss.storeAllIntakeFromOp(op, records) })

	assert.Len(t, drainIntakeQueue(ss), 1, "the second blob is dropped, not blocked on")
}

// Everything above hands the callback a *[]*Upload built by hand. If crudr
// actually produces some other shape the type assertion fails silently, the
// feature does nothing, and every one of those tests still passes. This drives
// a real Crudr so the assertion is checked against the thing that will call it.
func TestStoreAllIntakeReceivesTheShapeCrudrActuallySends(t *testing.T) {
	ss := storeAllIntakeTestServer(t, true)

	// A Crudr of its own, so registering a callback cannot leak into the
	// servers the rest of the package shares.
	crud := crudr.New("http://intake-shape-test", testNetwork[0].crud.DB, zap.NewNop())
	crud.RegisterModels(&Upload{})
	crud.AddOpCallback(ss.storeAllIntakeFromOp)

	uploadID := fmt.Sprintf("intake-shape-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		crud.DB.Exec(`delete from uploads where id = ?`, uploadID)
		crud.DB.Exec(`delete from ops where data::text like ?`, "%"+uploadID+"%")
	})

	upload := intakeUpload()
	upload.ID = uploadID
	upload.Template = JobTemplateAudio
	upload.Status = JobStatusNew
	upload.OrigFileCID = "cid-orig-" + uploadID
	require.NoError(t, crud.Create(upload))

	require.Equal(t,
		[]string{"cid-orig-" + uploadID},
		drainIntakeCIDs(ss),
		"the callback did not enqueue; the records type assertion is probably wrong")
}
