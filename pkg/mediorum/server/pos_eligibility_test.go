package server

import (
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/stretchr/testify/require"
)

// seedProofCandidate writes an uploads row whose orig_file_cid sorts immediately
// after fauxCid.
//
// Every raw CID has the same length, so a real CID greater than fauxCid differs
// from it somewhere inside that length and is therefore also greater than
// fauxCid+suffix. That makes these rows the nearest candidates above fauxCid
// whatever else the shared test database holds, without deleting anyone's data.
func seedProofCandidate(t *testing.T, ss *MediorumServer, idPrefix, fauxCid, suffix, status string, age time.Duration) string {
	t.Helper()
	cid := fauxCid + suffix
	upload := &Upload{
		ID:          idPrefix + suffix,
		OrigFileCID: cid,
		Status:      status,
		CreatedAt:   time.Now().UTC().Add(-age),
	}
	require.NoError(t, ss.crud.DB.Create(upload).Error)
	return cid
}

func TestStorageProofSkipsUploadsTooNewToHaveReplicated(t *testing.T) {
	ss := testNetwork[0]
	blockhash := []byte("pos-eligibility-age-fixture")
	fauxCid, err := cidutil.ComputeRawDataCID(blockhash)
	require.NoError(t, err)

	prefix := "postest-age-"
	t.Cleanup(func() {
		ss.crud.DB.Where("id LIKE ?", prefix+"%").Delete(&Upload{})
	})

	// "0" sorts before "1", so without the age filter the nearest candidate
	// above fauxCid is the upload created seconds ago.
	tooNew := seedProofCandidate(t, ss, prefix, fauxCid, "0", JobStatusDone, time.Minute)
	settled := seedProofCandidate(t, ss, prefix, fauxCid, "1", JobStatusDone, 2*time.Hour)

	got, err := ss.getStorageProofCIDFromBlockhash(blockhash)
	require.NoError(t, err)
	require.NotEqual(t, tooNew, got, "challenged an upload that has had no time to replicate")
	require.Equal(t, settled, got)
}

func TestStorageProofSkipsErroredUploads(t *testing.T) {
	ss := testNetwork[0]
	blockhash := []byte("pos-eligibility-status-fixture")
	fauxCid, err := cidutil.ComputeRawDataCID(blockhash)
	require.NoError(t, err)

	prefix := "postest-status-"
	t.Cleanup(func() {
		ss.crud.DB.Where("id LIKE ?", prefix+"%").Delete(&Upload{})
	})

	// An errored upload can carry an orig_file_cid that was assigned before the
	// failure stopped it being stored, so no node ever held that blob.
	errored := seedProofCandidate(t, ss, prefix, fauxCid, "0", JobStatusError, 2*time.Hour)
	stored := seedProofCandidate(t, ss, prefix, fauxCid, "1", JobStatusDone, 2*time.Hour)

	got, err := ss.getStorageProofCIDFromBlockhash(blockhash)
	require.NoError(t, err)
	require.NotEqual(t, errored, got, "challenged a cid from an upload that failed before storage")
	require.Equal(t, stored, got)
}

// The wrap-around branch must carry the same predicates as the primary query,
// or it hands back exactly what the primary excluded.
func TestStorageProofWrapAroundAppliesTheSameFilters(t *testing.T) {
	ss := testNetwork[0]
	blockhash := []byte("pos-eligibility-wrap-fixture")
	fauxCid, err := cidutil.ComputeRawDataCID(blockhash)
	require.NoError(t, err)

	prefix := "postest-wrap-"
	t.Cleanup(func() {
		ss.crud.DB.Where("id LIKE ?", prefix+"%").Delete(&Upload{})
	})

	ineligible := []string{
		seedProofCandidate(t, ss, prefix, fauxCid, "0", JobStatusDone, time.Minute),
		seedProofCandidate(t, ss, prefix, fauxCid, "1", JobStatusError, 2*time.Hour),
	}

	got, err := ss.getStorageProofCIDFromBlockhash(blockhash)
	if err != nil {
		// Acceptable: nothing in the corpus qualified. It must fail cleanly --
		// startPoSHandler logs and continues rather than crashing the loop.
		return
	}
	require.NotContains(t, ineligible, got)
}
