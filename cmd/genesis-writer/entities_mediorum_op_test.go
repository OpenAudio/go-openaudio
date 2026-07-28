package main

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/opvalidation"
	mediorumserver "github.com/OpenAudio/go-openaudio/pkg/mediorum/server"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

// The seeded op is built from the live mediorum model but validated on-chain
// against opvalidation's mirror types. These tests pin that agreement: if the
// two ever drift, seeded ops would be silently dropped by every node at apply
// time rather than failing here.
func TestSeededMediorumOpsPassChainValidation(t *testing.T) {
	genesisTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	w := &Writer{cfg: &WriterConfig{GenesisTime: genesisTime}}

	upload := mediorumserver.Upload{
		ID:                      "01HRAAAAAAAAAAAAAAAAAAAAAA",
		UserWallet:              sql.NullString{String: "0xabc", Valid: true},
		Template:                "audio",
		OrigFileName:            "song.wav",
		OrigFileCID:             "baeaaaiqse",
		SelectedPreview:         sql.NullString{String: "preview", Valid: true},
		FFProbe:                 &mediorumserver.FFProbeResult{},
		Mirrors:                 []string{"https://node1.example.com"},
		TranscodedMirrors:       []string{"https://node2.example.com"},
		Status:                  "done",
		PlacementHosts:          []string{"https://node1.example.com"},
		CreatedBy:               "https://node1.example.com",
		CreatedAt:               genesisTime,
		UpdatedAt:               genesisTime,
		TranscodedBy:            "https://node1.example.com",
		TranscodeProgress:       1,
		TranscodedAt:            genesisTime,
		TranscodeResults:        map[string]string{"320": "baeaaaiqseresult"},
		AudioAnalysisStatus:     "done",
		AudioAnalysisErrorCount: 0,
		AudioAnalyzedBy:         "https://node1.example.com",
		AudioAnalyzedAt:         genesisTime,
		AudioAnalysisResults:    &mediorumserver.AudioAnalysisResult{BPM: 120, Key: "C"},
	}

	analysis := mediorumserver.QmAudioAnalysis{
		CID:        "QmAnalysis",
		Mirrors:    []string{"https://node1.example.com"},
		Status:     "done",
		ErrorCount: 0,
		AnalyzedBy: "https://node1.example.com",
		AnalyzedAt: genesisTime,
		Results:    &mediorumserver.AudioAnalysisResult{BPM: 90, Key: "Am"},
	}

	preview := mediorumserver.AudioPreview{
		CID:                 "QmPreview",
		SourceCID:           "QmSource",
		PreviewStartSeconds: "30",
		CreatedBy:           "https://node1.example.com",
		CreatedAt:           genesisTime,
	}

	cases := []struct {
		name  string
		table string
		key   string
		data  []byte
	}{
		{"uploads", mediorumTableName(upload), upload.ID, mustMarshalOne(t, upload)},
		{"qm_audio_analyses", mediorumTableName(analysis), analysis.CID, mustMarshalOne(t, analysis)},
		{"audio_previews", mediorumTableName(preview), preview.CID, mustMarshalOne(t, preview)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.name, tc.table, "table name must match the key opvalidation registers")

			id := w.seedOpULID(tc.table, tc.key)
			require.NoError(t, opvalidation.ValidateOperation(id, "https://node1.example.com", crudr.ActionCreate, tc.table, tc.data))
			require.NoError(t, opvalidation.ValidateCorePayloadSize(tc.data))
		})
	}
}

func TestSeedOpULIDIsStableAndValid(t *testing.T) {
	w := &Writer{cfg: &WriterConfig{GenesisTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}}

	first := w.seedOpULID("uploads", "abc")

	// Stable across calls: a resumed or re-run migration must not mint fresh
	// ULIDs, which would append duplicate rows to every node's ops table.
	require.Equal(t, first, w.seedOpULID("uploads", "abc"))

	// Distinct per record, and per table for a shared key.
	require.NotEqual(t, first, w.seedOpULID("uploads", "abd"))
	require.NotEqual(t, first, w.seedOpULID("audio_previews", "abc"))

	// The chain parses op ULIDs strictly.
	_, err := ulid.ParseStrict(first)
	require.NoError(t, err)
}

func mustMarshalOne[T any](t *testing.T, row T) []byte {
	t.Helper()
	// Mirrors crudr.jsonArrayMarshal: the op payload is always a JSON array.
	b, err := json.Marshal([]T{row})
	require.NoError(t, err)
	return b
}
