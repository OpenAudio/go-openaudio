package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const tb = 1e12

func TestComputeStorageExpectationShare(t *testing.T) {
	// 100 TB corpus, rf 4, 50 nodes -> each holds 4/50 = 8 TB.
	got := computeStorageExpectation(storageExpectationInputs{
		OriginalBytes: 100 * tb,
		NodeCount:     50,
	}, 4, 0)
	assert.Equal(t, uint64(8*tb), got)
}

func TestComputeStorageExpectationDegenerateInputs(t *testing.T) {
	full := storageExpectationInputs{OriginalBytes: 100 * tb, NodeCount: 50}

	assert.Zero(t, computeStorageExpectation(storageExpectationInputs{
		OriginalBytes: 100 * tb, NodeCount: 0,
	}, 4, 0), "no registered nodes must not divide by zero")

	assert.Zero(t, computeStorageExpectation(full, 0, 0),
		"replication factor of zero yields no expectation")

	assert.Zero(t, computeStorageExpectation(storageExpectationInputs{NodeCount: 50}, 4, 0),
		"an empty network expects nothing")
}

// Each component has to land in the corpus. Adding one and only one at a time
// makes it obvious which term is missing if this breaks.
func TestComputeStorageExpectationCountsEveryComponent(t *testing.T) {
	base := storageExpectationInputs{OriginalBytes: 10 * tb, NodeCount: 10}
	// rf == nodeCount so the share is 1:1 and the assertions read as raw corpus.
	expect := func(in storageExpectationInputs, legacy int64) uint64 {
		return computeStorageExpectation(in, 10, legacy)
	}

	assert.Equal(t, uint64(10*tb), expect(base, 0))

	withAudio := base
	withAudio.AudioSeconds = 1_000_000 // 1e6 s * 40_000 B/s = 40 GB
	assert.Equal(t, uint64(10*tb+40e9), expect(withAudio, 0),
		"320 kbps transcodes must be sized from duration")

	withPreviews := base
	withPreviews.PreviewCount = 1000 // 1000 * 1.2 MB
	assert.Equal(t, uint64(10*tb+1_200_000_000), expect(withPreviews, 0),
		"preview clips must be counted")

	assert.Equal(t, uint64(10*tb+5*tb), expect(base, 5*tb),
		"the legacy corpus must be counted")
}

// Image uploads have no transcode. The old formula doubled every original
// regardless of template, which over-counted them; sizing transcodes from
// audio duration means an image-only corpus is counted exactly once.
func TestComputeStorageExpectationDoesNotDoubleImages(t *testing.T) {
	imagesOnly := storageExpectationInputs{
		OriginalBytes: 2 * tb,
		AudioSeconds:  0,
		NodeCount:     4,
	}
	assert.Equal(t, uint64(2*tb), computeStorageExpectation(imagesOnly, 4, 0))
}

// Mainnet as measured on 2026-09-03, documenting what this formula corrects.
// The old `originals * 2 * rf / n` published ~2.15 TB while nodes were really
// holding ~2.57 TB of blobs -- about 16% low, because it over-counted
// transcodes and omitted previews and the whole legacy corpus.
func TestComputeStorageExpectationMatchesMeasuredMainnet(t *testing.T) {
	const (
		originalBytes = 20.376 * tb // audio + image originals, network-wide
		audioSeconds  = 313_750_000 // yields ~12.55 TB of 320 kbps transcodes
		previewCount  = 47_167      // ~0.057 TB of clips
		legacyBytes   = 16.26 * tb  // sampled at 99.9% hit rate
		nodeCount     = 76
		rf            = 4
		measured      = 2.571 * tb // content-8's repair ContentSize
	)

	got := computeStorageExpectation(storageExpectationInputs{
		OriginalBytes: originalBytes,
		AudioSeconds:  audioSeconds,
		PreviewCount:  previewCount,
		NodeCount:     nodeCount,
	}, rf, legacyBytes)

	assert.InEpsilon(t, measured, float64(got), 0.05,
		"expectation should land within 5%% of what a node actually holds")

	// The formula this replaces, for contrast.
	old := computeStorageExpectation(storageExpectationInputs{
		OriginalBytes: originalBytes * 2,
		NodeCount:     nodeCount,
	}, rf, 0)
	assert.Less(t, float64(old), measured*0.90,
		"the old formula should be demonstrably more than 10%% low")
	assert.Greater(t, got, old, "the corrected formula must be the larger of the two")
}
