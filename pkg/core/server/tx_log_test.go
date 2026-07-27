package server

import (
	"strings"
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/stretchr/testify/assert"
)

func TestTxTypeName(t *testing.T) {
	assert.Equal(t, "none", txTypeName(nil))
	assert.Equal(t, "none", txTypeName(&v1.SignedTransaction{}))

	plays := &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_Plays{Plays: &v1.TrackPlays{}},
	}
	assert.Equal(t, "Plays", txTypeName(plays))

	em := &v1.SignedTransaction{
		Transaction: &v1.SignedTransaction_ManageEntity{ManageEntity: &v1.ManageEntityLegacy{}},
	}
	assert.Equal(t, "ManageEntity", txTypeName(em))
}

func TestTxPayloadPreview(t *testing.T) {
	assert.Equal(t, "", txPayloadPreview(nil))

	// a typed-nil message must not panic
	var typedNil *v1.SignedTransaction
	assert.NotPanics(t, func() { txPayloadPreview(typedNil) })

	small := &v1.SignedTransaction{
		Signature: "abc",
		Transaction: &v1.SignedTransaction_Plays{
			Plays: &v1.TrackPlays{Plays: []*v1.TrackPlay{{TrackId: "track-1"}}},
		},
	}
	preview := txPayloadPreview(small)
	assert.Contains(t, preview, "track-1")
	assert.NotContains(t, preview, "truncated")
}

func TestTxPayloadPreviewTruncatesLargePayloads(t *testing.T) {
	big := &v1.SignedTransaction{
		Signature: "abc",
		Transaction: &v1.SignedTransaction_ManageEntity{
			ManageEntity: &v1.ManageEntityLegacy{Metadata: strings.Repeat("x", 100_000)},
		},
	}
	preview := txPayloadPreview(big)
	assert.LessOrEqual(t, len(preview), txPayloadPreviewCap+len("...(truncated)"))
	assert.True(t, strings.HasSuffix(preview, "...(truncated)"))
}
