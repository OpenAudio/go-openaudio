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
	fail    int // transient failures to return before answering
	calls   int
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
	first, err := a.attestationFor(context.Background(), track)
	require.NoError(t, err)
	second, err := a.attestationFor(context.Background(), track)
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
}
