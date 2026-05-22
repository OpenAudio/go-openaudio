package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergeReplicationMirrors_AddsNewHostsInStableOrder(t *testing.T) {
	upload := &Upload{Mirrors: []string{"a", "b"}}
	merged, changed := mergeReplicationMirrors(false, upload, []string{"c", "d"})
	assert.True(t, changed)
	assert.Equal(t, []string{"a", "b", "c", "d"}, merged)
}

func TestMergeReplicationMirrors_TranscodedSelectsRightField(t *testing.T) {
	upload := &Upload{
		Mirrors:           []string{"orig-a"},
		TranscodedMirrors: []string{"t-a"},
	}
	merged, changed := mergeReplicationMirrors(true, upload, []string{"t-b"})
	assert.True(t, changed)
	assert.Equal(t, []string{"t-a", "t-b"}, merged)
}

func TestMergeReplicationMirrors_NoNewHostsReturnsUnchanged(t *testing.T) {
	upload := &Upload{Mirrors: []string{"a", "b"}}
	merged, changed := mergeReplicationMirrors(false, upload, nil)
	assert.False(t, changed, "empty newSuccessHosts must report unchanged")
	assert.Equal(t, []string{"a", "b"}, merged)
}

func TestMergeReplicationMirrors_AllDuplicatesReturnsUnchanged(t *testing.T) {
	upload := &Upload{Mirrors: []string{"a", "b", "c"}}
	merged, changed := mergeReplicationMirrors(false, upload, []string{"a", "c"})
	assert.False(t, changed,
		"every newSuccessHost already in existing must report unchanged "+
			"(this is the suppression path that prevents uploads-update churn)")
	assert.Equal(t, []string{"a", "b", "c"}, merged)
}

func TestMergeReplicationMirrors_PartialOverlapReportsChanged(t *testing.T) {
	upload := &Upload{Mirrors: []string{"a", "b"}}
	merged, changed := mergeReplicationMirrors(false, upload, []string{"b", "c"})
	assert.True(t, changed, "at least one new host means changed=true")
	assert.Equal(t, []string{"a", "b", "c"}, merged)
}

func TestMergeReplicationMirrors_EmptyExistingAddsAll(t *testing.T) {
	upload := &Upload{Mirrors: nil}
	merged, changed := mergeReplicationMirrors(false, upload, []string{"a", "b"})
	assert.True(t, changed)
	assert.Equal(t, []string{"a", "b"}, merged)
}

func TestMergeReplicationMirrors_DoesNotMutateUploadFields(t *testing.T) {
	upload := &Upload{Mirrors: []string{"a"}}
	originalLen := len(upload.Mirrors)
	merged, _ := mergeReplicationMirrors(false, upload, []string{"b"})
	merged[0] = "MUTATED"
	assert.Equal(t, originalLen, len(upload.Mirrors),
		"merge result must not alias upload.Mirrors (callers may inspect or "+
			"reassign it independently)")
	assert.Equal(t, "a", upload.Mirrors[0])
}

func TestMergeReplicationMirrors_DedupsWithinNewHostsList(t *testing.T) {
	upload := &Upload{Mirrors: []string{"a"}}
	// A buggy caller could pass the same host twice in newSuccessHosts;
	// the merge must not produce duplicates.
	merged, changed := mergeReplicationMirrors(false, upload, []string{"b", "b"})
	assert.True(t, changed)
	assert.Equal(t, []string{"a", "b"}, merged)
}
