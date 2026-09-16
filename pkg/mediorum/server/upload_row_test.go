package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/stretchr/testify/require"
)

func newUploadRowTestRow(t *testing.T, ss *MediorumServer) string {
	t.Helper()
	id := fmt.Sprintf("upload-row-%d", time.Now().UnixNano())
	cleanup := func() {
		require.NoError(t, ss.crud.DB.Where("id = ?", id).Delete(&Upload{}).Error)
	}
	cleanup()
	t.Cleanup(cleanup)
	require.NoError(t, ss.crud.DB.Create(&Upload{
		ID:               id,
		Template:         JobTemplateAudio,
		Status:           JobStatusNew,
		CreatedBy:        ss.Config.Self.Host,
		Mirrors:          []string{},
		TranscodeResults: map[string]string{},
	}).Error)
	return id
}

func countUploadOps(t *testing.T, ss *MediorumServer, id string) int64 {
	t.Helper()
	var n int64
	require.NoError(t, ss.crud.DB.Model(&crudr.Op{}).
		Where("\"table\" = ? AND data->0->>'id' = ?", "uploads", id).
		Count(&n).Error)
	return n
}

// Two writers that each own different fields of one row must both survive,
// and every snapshot the row publishes must be consistent with the ones before
// it. This is the shape of the transcode claim and the replication worker's
// mirror write racing on a new upload: each read the row, set its own fields,
// and wrote the whole row back, and whichever wrote second erased the other.
//
// Each writer drives its own counter so a lost write shows up as a wrong
// number rather than as a coincidence in the final state.
func TestUpdateUploadRowKeepsBothWritersFields(t *testing.T) {
	ss := testNetwork[0]
	id := newUploadRowTestRow(t, ss)

	const rounds = 20
	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range rounds {
			if _, err := ss.updateUploadRow(id, func(u *Upload) error {
				u.TranscodedBy = ss.Config.Self.Host
				u.Status = JobStatusBusy
				u.ErrorCount++
				return nil
			}); err != nil {
				errs <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range rounds {
			host := fmt.Sprintf("http://mirror-%d", i)
			if _, err := ss.updateUploadRow(id, func(u *Upload) error {
				u.Mirrors = append(u.Mirrors, host)
				return nil
			}); err != nil {
				errs <- err
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	var got Upload
	require.NoError(t, ss.crud.DB.First(&got, "id = ?", id).Error)
	require.Equal(t, ss.Config.Self.Host, got.TranscodedBy)
	require.Equal(t, JobStatusBusy, got.Status)
	require.Equal(t, rounds, got.ErrorCount)
	require.Len(t, got.Mirrors, rounds)

	// The op stream is what peers replay, in ulid order. Under the lock the
	// ulid is minted and the row committed without another writer in between,
	// so the history must never step backwards on either writer's field.
	var ops []crudr.Op
	require.NoError(t, ss.crud.DB.Order("ulid asc").
		Where("\"table\" = ? AND data->0->>'id' = ?", "uploads", id).
		Find(&ops).Error)
	// The row was seeded with a plain insert, so every op here is one write.
	require.Len(t, ops, 2*rounds)
	claims, mirrors := 0, 0
	for _, op := range ops {
		var rows []Upload
		require.NoError(t, json.Unmarshal(op.Data, &rows))
		require.Len(t, rows, 1)
		require.GreaterOrEqual(t, rows[0].ErrorCount, claims, "op %s retracts a claim", op.ULID)
		require.GreaterOrEqual(t, len(rows[0].Mirrors), mirrors, "op %s retracts a mirror", op.ULID)
		claims, mirrors = rows[0].ErrorCount, len(rows[0].Mirrors)
	}
}

func TestUpdateUploadRowUnchangedEmitsNoOp(t *testing.T) {
	ss := testNetwork[0]
	id := newUploadRowTestRow(t, ss)
	before := countUploadOps(t, ss, id)

	got, err := ss.updateUploadRow(id, func(u *Upload) error {
		return errUploadRowUnchanged
	})
	require.NoError(t, err)
	require.Equal(t, id, got.ID)
	require.Equal(t, before, countUploadOps(t, ss, id))
}

func TestUpdateUploadRowMutateErrorWritesNothing(t *testing.T) {
	ss := testNetwork[0]
	id := newUploadRowTestRow(t, ss)
	before := countUploadOps(t, ss, id)
	boom := errors.New("boom")

	got, err := ss.updateUploadRow(id, func(u *Upload) error {
		u.Status = JobStatusBusy
		return boom
	})
	require.ErrorIs(t, err, boom)
	// The row as read comes back so the caller can name it in its error.
	require.Equal(t, id, got.ID)
	require.Equal(t, before, countUploadOps(t, ss, id))

	var row Upload
	require.NoError(t, ss.crud.DB.First(&row, "id = ?", id).Error)
	require.Equal(t, JobStatusNew, row.Status)
}
