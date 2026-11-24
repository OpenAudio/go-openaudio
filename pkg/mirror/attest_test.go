package mirror

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

// trackWrite is a track create naming audio from an upload.
func trackWrite(user, id int64, uploadID, orig, transcoded string) *corev1.SignedTransaction {
	meta, _ := json.Marshal(map[string]any{"cid": "bafy", "data": map[string]any{
		"audio_upload_id": uploadID, "orig_file_cid": orig, "track_cid": transcoded, "title": "t",
	}})
	tx := manageEntity(id, "Create")
	tx.GetManageEntity().UserId = user
	tx.GetManageEntity().Metadata = string(meta)
	return tx
}

type fakeUploads struct {
	uploads map[string]*UploadRecord
	// listed is what UploadsAfter serves, oldest first, two to a page.
	listed []UploadRecord
	fail   int // transient failures to return before answering
	calls  int
}

func (u *fakeUploads) UploadsAfter(_ context.Context, after time.Time) ([]UploadRecord, error) {
	var page []UploadRecord
	for _, up := range u.listed {
		if up.CreatedAt.After(after) && len(page) < 2 {
			page = append(page, up)
		}
	}
	return page, nil
}

func (u *fakeUploads) Upload(_ context.Context, id string) (*UploadRecord, error) {
	u.calls++
	if u.fail > 0 {
		u.fail--
		return nil, errors.New("connection reset")
	}
	up, ok := u.uploads[id]
	if !ok {
		return nil, errUploadNotFound
	}
	return up, nil
}

// claimsTarget refuses track writes naming audio another user holds a claim
// on, the way content auth does, and records claims from committed
// attestations.
func claimsTarget(t *testing.T, owner map[string]int64) *fakeTarget {
	dst := newFakeTarget()
	dst.refuse = func(tx *corev1.SignedTransaction, committed map[string]int64) error {
		if ca := tx.GetContentAttestation(); ca != nil {
			payload, err := coreServer.ContentAttestationBytes(ca)
			require.NoError(t, err)
			_, signer, err := common.EthRecover(ca.GetValidatorSignature(), payload)
			require.NoError(t, err)
			require.Equal(t, ca.GetValidatorAddress(), signer)
			return nil
		}
		me := tx.GetManageEntity()
		if me == nil {
			return nil
		}
		meta := parseMetadata(me.GetMetadata())
		for _, key := range trackAudioKeys {
			cid := metaString(meta, key)
			holder, claimed := owner[cid]
			if cid == "" || !claimed || holder == me.GetUserId() || attestedFor(dst, committed, cid, me.GetUserId()) {
				continue
			}
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s %q was not uploaded for user %d", key, cid, me.GetUserId()))
		}
		return nil
	}
	return dst
}

func attestedFor(dst *fakeTarget, committed map[string]int64, cid string, user int64) bool {
	for h := range committed {
		ca := dst.txs[h].GetContentAttestation()
		if ca == nil || ca.GetUserId() != user {
			continue
		}
		for _, c := range ca.GetCids() {
			if c == cid {
				return true
			}
		}
	}
	return false
}

func newAttester(t *testing.T, uploads UploadSource) *Attester {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	return NewAttester(key, uploads)
}

// The case the attester exists for: a second account uploads a file someone
// else already holds a claim on. Its own upload vouches for the file, so the
// mirror attests it for that account and the track goes through.
func TestAttestsATracksOwnUploadAndRetries(t *testing.T) {
	track := trackWrite(42, 1, "up1", "orig", "transcoded")
	next := manageEntity(2, "Create")
	src := &fakeSource{chainID: srcChain, tip: 11, blocks: map[int64][][]byte{10: {raw(t, track)}, 11: {raw(t, next)}}}
	dst := claimsTarget(t, map[string]int64{"orig": 7, "transcoded": 7})
	uploads := &fakeUploads{uploads: map[string]*UploadRecord{
		"up1": {ID: "up1", OrigFileCID: "orig", Results: map[string]string{"320": "transcoded"}, UserID: sql.NullInt64{Int64: 42, Valid: true}},
	}}
	m := newMirror(t, src, dst, &memCursor{}, 10)
	m.cfg.Attester = newAttester(t, uploads)

	require.NoError(t, drive(t, m, src, dst))
	require.Len(t, dst.order, 3)
	att := dst.txs[dst.order[0]].GetContentAttestation()
	require.NotNil(t, att, "the attestation commits before the track")
	require.Equal(t, int64(42), att.GetUserId())
	require.ElementsMatch(t, []string{"orig", "transcoded"}, att.GetCids())
	require.Equal(t, m.cfg.Attester.Address(), att.GetValidatorAddress())
	require.Equal(t, []string{hashOf(t, track), hashOf(t, next)}, dst.order[1:])
	require.GreaterOrEqual(t, dst.committed[hashOf(t, track)], dst.committed[dst.order[0]]+2,
		"the track is retried only once a block beyond the attestation's has committed, so every validator has applied it")

	st := m.Status()
	require.Len(t, st.Attested, 1)
	require.Equal(t, hashOf(t, track), st.Attested[0].Hash)
	require.Equal(t, dst.order[0], st.Attested[0].AttestationHash)
}

// The upload vouches only for what it holds. A track naming a different file
// than its upload, or an upload made for someone else, still halts, and
// nothing is attested.
func TestDoesNotAttestWhatTheUploadDoesNotHold(t *testing.T) {
	for name, up := range map[string]*UploadRecord{
		"different file": {ID: "up1", OrigFileCID: "other", UserID: sql.NullInt64{Int64: 42, Valid: true}},
		"other user":     {ID: "up1", OrigFileCID: "orig", UserID: sql.NullInt64{Int64: 9, Valid: true}},
		"missing upload": nil,
	} {
		t.Run(name, func(t *testing.T) {
			track := trackWrite(42, 1, "up1", "orig", "")
			src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, track)}}}
			dst := claimsTarget(t, map[string]int64{"orig": 7})
			uploads := &fakeUploads{uploads: map[string]*UploadRecord{}}
			if up != nil {
				uploads.uploads["up1"] = up
			}
			m := newMirror(t, src, dst, &memCursor{}, 10)
			m.cfg.Attester = newAttester(t, uploads)

			err := drive(t, m, src, dst)
			var halt *HaltError
			require.ErrorAs(t, err, &halt)
			require.Equal(t, hashOf(t, track), halt.Hash)
			require.Contains(t, halt.Reason, "not attestable")
			require.Empty(t, dst.order)
			require.Empty(t, m.Status().Attested)
		})
	}
}

// trackUpdate is a track update naming audio, as an audio replacement sends it.
func trackUpdate(user, id int64, uploadID, orig, transcoded string) *corev1.SignedTransaction {
	tx := trackWrite(user, id, uploadID, orig, transcoded)
	tx.GetManageEntity().Action = "Update"
	return tx
}

// Replacing a track's audio keeps the track's first audio_upload_id. The
// upload that holds the new file is a later one for the same user, and that
// is the one the mirror attests.
func TestAttestsAReplacedTracksNewUpload(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 7, 0, m, 0, 0, time.UTC) }
	mine := sql.NullInt64{Int64: 42, Valid: true}
	uploads := &fakeUploads{
		uploads: map[string]*UploadRecord{"up1": {ID: "up1", OrigFileCID: "first", UserID: mine, CreatedAt: at(0)}},
		listed: []UploadRecord{
			{ID: "theirs", OrigFileCID: "new", Results: map[string]string{"320": "tc"}, UserID: sql.NullInt64{Int64: 7, Valid: true}, CreatedAt: at(1)},
			{ID: "other-transcode", OrigFileCID: "new", Results: map[string]string{"320": "tc-other"}, UserID: mine, CreatedAt: at(2)},
			{ID: "unrelated", OrigFileCID: "x", UserID: mine, CreatedAt: at(3)},
			{ID: "up2", OrigFileCID: "new", Results: map[string]string{"320": "tc"}, UserID: mine, CreatedAt: at(4)},
		},
	}
	track := trackUpdate(42, 1, "up1", "new", "tc")
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, track)}}}
	dst := claimsTarget(t, map[string]int64{"new": 7, "tc": 7})
	m := newMirror(t, src, dst, &memCursor{}, 10)
	m.cfg.Attester = newAttester(t, uploads)

	require.NoError(t, drive(t, m, src, dst))
	require.Len(t, dst.order, 2)
	att := dst.txs[dst.order[0]].GetContentAttestation()
	require.NotNil(t, att)
	require.Equal(t, int64(42), att.GetUserId())
	require.ElementsMatch(t, []string{"new", "tc"}, att.GetCids())
	require.Equal(t, hashOf(t, track), dst.order[1])
}

// A later upload of the file by someone else, or one that did not produce the
// transcode the track names, does not vouch for it.
func TestDoesNotAttestAReplacementTheUserDidNotUpload(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 7, 0, m, 0, 0, time.UTC) }
	mine := sql.NullInt64{Int64: 42, Valid: true}
	uploads := &fakeUploads{
		uploads: map[string]*UploadRecord{"up1": {ID: "up1", OrigFileCID: "first", UserID: mine, CreatedAt: at(1)}},
		listed: []UploadRecord{
			{ID: "before", OrigFileCID: "new", Results: map[string]string{"320": "tc"}, UserID: mine, CreatedAt: at(0)},
			{ID: "theirs", OrigFileCID: "new", Results: map[string]string{"320": "tc"}, UserID: sql.NullInt64{Int64: 7, Valid: true}, CreatedAt: at(2)},
			{ID: "other-transcode", OrigFileCID: "new", Results: map[string]string{"320": "tc-other"}, UserID: mine, CreatedAt: at(3)},
		},
	}
	track := trackUpdate(42, 1, "up1", "new", "tc")
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, track)}}}
	dst := claimsTarget(t, map[string]int64{"new": 7})
	m := newMirror(t, src, dst, &memCursor{}, 10)
	m.cfg.Attester = newAttester(t, uploads)

	var halt *HaltError
	require.ErrorAs(t, drive(t, m, src, dst), &halt)
	require.Contains(t, halt.Reason, "no later upload for user 42")
	require.Empty(t, dst.order)
}

// A track written through the SDK often names no upload. The upload behind it
// is the one made for its user shortly before its block, holding its audio.
func TestAttestsAnUnnamedUploadFromBeforeTheBlock(t *testing.T) {
	block := time.Date(2026, 11, 7, 9, 49, 21, 0, time.UTC)
	mine := sql.NullInt64{Int64: 42, Valid: true}
	uploads := &fakeUploads{listed: []UploadRecord{
		{ID: "too-early", OrigFileCID: "orig", Results: map[string]string{"320": "tc"}, UserID: mine, CreatedAt: block.Add(-2 * time.Hour)},
		{ID: "theirs", OrigFileCID: "orig", Results: map[string]string{"320": "tc"}, UserID: sql.NullInt64{Int64: 7, Valid: true}, CreatedAt: block.Add(-2 * time.Minute)},
		{ID: "up1", OrigFileCID: "orig", Results: map[string]string{"320": "tc"}, UserID: mine, CreatedAt: block.Add(-22 * time.Second)},
	}}
	track := trackWrite(42, 1, "", "orig", "tc")
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, track)}}, times: map[int64]time.Time{10: block}}
	dst := claimsTarget(t, map[string]int64{"orig": 7, "tc": 7})
	m := newMirror(t, src, dst, &memCursor{}, 10)
	m.cfg.Attester = newAttester(t, uploads)

	require.NoError(t, drive(t, m, src, dst))
	require.Len(t, dst.order, 2)
	att := dst.txs[dst.order[0]].GetContentAttestation()
	require.NotNil(t, att)
	require.Equal(t, int64(42), att.GetUserId())
	require.ElementsMatch(t, []string{"orig", "tc"}, att.GetCids())
	require.Equal(t, hashOf(t, track), dst.order[1])
}

// For a track naming no upload, only an upload for its user from the hour
// before its block counts, and without the block's time nothing does.
func TestDoesNotAttestAnUnnamedUploadOutsideTheWindow(t *testing.T) {
	block := time.Date(2026, 11, 7, 9, 49, 21, 0, time.UTC)
	mine := sql.NullInt64{Int64: 42, Valid: true}
	for name, tc := range map[string]struct {
		at     time.Time
		reason string
	}{
		"uploads outside the hour": {block, "no upload for user 42 in the hour before it"},
		"no block time":            {time.Time{}, "track names no audio_upload_id"},
	} {
		t.Run(name, func(t *testing.T) {
			uploads := &fakeUploads{listed: []UploadRecord{
				{ID: "too-early", OrigFileCID: "orig", Results: map[string]string{"320": "tc"}, UserID: mine, CreatedAt: block.Add(-2 * time.Hour)},
				{ID: "after", OrigFileCID: "orig", Results: map[string]string{"320": "tc"}, UserID: mine, CreatedAt: block.Add(5 * time.Minute)},
			}}
			track := trackWrite(42, 1, "", "orig", "tc")
			src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, track)}}, times: map[int64]time.Time{10: tc.at}}
			dst := claimsTarget(t, map[string]int64{"orig": 7})
			m := newMirror(t, src, dst, &memCursor{}, 10)
			m.cfg.Attester = newAttester(t, uploads)

			var halt *HaltError
			require.ErrorAs(t, drive(t, m, src, dst), &halt)
			require.Contains(t, halt.Reason, tc.reason)
			require.Empty(t, dst.order)
		})
	}
}

// Without an attester the refusal is a halt, as before.
func TestContentAuthRefusalHaltsWithoutAnAttester(t *testing.T) {
	track := trackWrite(42, 1, "up1", "orig", "")
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, track)}}}
	dst := claimsTarget(t, map[string]int64{"orig": 7})
	m := newMirror(t, src, dst, &memCursor{}, 10)

	var halt *HaltError
	require.ErrorAs(t, drive(t, m, src, dst), &halt)
	require.Equal(t, "refused by target", halt.Reason)
}

// A failed upload lookup is retried, and the track is neither lost nor
// skipped meanwhile.
func TestUploadLookupFailureIsRetried(t *testing.T) {
	track := trackWrite(42, 1, "up1", "orig", "")
	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, track)}}}
	dst := claimsTarget(t, map[string]int64{"orig": 7})
	uploads := &fakeUploads{fail: 2, uploads: map[string]*UploadRecord{"up1": {ID: "up1", OrigFileCID: "orig"}}}
	m := newMirror(t, src, dst, &memCursor{}, 10)
	m.cfg.Attester = newAttester(t, uploads)

	ctx := context.Background()
	for range 2 {
		_, err := m.Step(ctx)
		require.Error(t, err)
		var halt *HaltError
		require.False(t, errors.As(err, &halt), "a lookup failure is transient")
	}
	require.NoError(t, drive(t, m, src, dst))
	require.Len(t, dst.order, 2)
	require.Equal(t, hashOf(t, track), dst.order[1])
}

// A rerun rebuilds the identical attestation, finds it committed, and does not
// send it again.
func TestAttestationIsDeterministicAcrossRuns(t *testing.T) {
	uploads := &fakeUploads{uploads: map[string]*UploadRecord{"up1": {ID: "up1", OrigFileCID: "orig"}}}
	a := newAttester(t, uploads)
	track := trackWrite(42, 1, "up1", "orig", "")
	first, err := a.attestationFor(context.Background(), track, time.Time{})
	require.NoError(t, err)
	second, err := a.attestationFor(context.Background(), track, time.Time{})
	require.NoError(t, err)
	h1, _ := txHash(first)
	h2, _ := txHash(second)
	require.Equal(t, h1, h2)

	src := &fakeSource{chainID: srcChain, tip: 10, blocks: map[int64][][]byte{10: {raw(t, track)}}}
	dst := claimsTarget(t, map[string]int64{"orig": 7})
	dst.txs[h1], dst.committed[h1] = first, 99
	m := newMirror(t, src, dst, &memCursor{}, 10)
	m.cfg.Attester = a

	require.NoError(t, drive(t, m, src, dst))
	require.Zero(t, dst.forwards[h1], "already committed, not resent")
	require.Equal(t, []string{hashOf(t, track)}, dst.order)
}

func TestHTTPUploads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/uploads":
			require.Equal(t, "2026-11-07T00:00:00Z", r.URL.Query().Get("after"))
			_, _ = w.Write([]byte(`[{"id":"up2","orig_file_cid":"o","created_at":"2026-11-07T00:39:32Z"}]`))
		case "/uploads/up1":
			_, _ = w.Write([]byte(`{"id":"up1","orig_file_cid":"orig","results":{"320":"tc"},"user_id":{"Int64":42,"Valid":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u := NewHTTPUploads(srv.URL, srv.Client())

	up, err := u.Upload(context.Background(), "up1")
	require.NoError(t, err)
	require.Equal(t, "orig", up.OrigFileCID)
	require.Equal(t, "tc", up.Results["320"])
	require.Equal(t, sql.NullInt64{Int64: 42, Valid: true}, up.UserID)

	_, err = u.Upload(context.Background(), "nope")
	require.ErrorIs(t, err, errUploadNotFound)

	ups, err := u.UploadsAfter(context.Background(), time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Len(t, ups, 1)
	require.Equal(t, "up2", ups[0].ID)
	require.Equal(t, time.Date(2026, 11, 7, 0, 39, 32, 0, time.UTC), ups[0].CreatedAt)
}
