package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/stretchr/testify/require"
)

func TestSaveAudioAnalysisPreservesUploadCompletion(t *testing.T) {
	ss := testNetwork[0]
	for _, status := range []string{JobStatusBusy, JobStatusDone, JobStatusError} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", status, fail), func(t *testing.T) {
				id := fmt.Sprintf("analysis-completion-%d", time.Now().UnixNano())
				upload := Upload{
					ID: id, Template: JobTemplateAudio, Status: status,
					TranscodedBy: "transcoder", TranscodeProgress: 0.9,
					Mirrors: []string{"original-mirror"}, TranscodedMirrors: []string{"audio-mirror"},
					Error: "existing transcode error", ErrorCount: 2,
					AudioAnalysisError: "previous analysis error", AudioAnalysisErrorCount: 1,
				}
				if status == JobStatusDone {
					upload.TranscodeResults = map[string]string{"320": "persisted-cid"}
				}
				require.NoError(t, ss.crud.DB.Create(&upload).Error)
				t.Cleanup(func() {
					ss.crud.DB.Delete(&Upload{}, "id = ?", id)
					ss.crud.DB.Where("\"table\" = ? AND data->0->>'id' = ?", "uploads", id).Delete(&crudr.Op{})
				})

				var analysisErr error
				result := &AudioAnalysisResult{BPM: 120, Key: "C major"}
				if fail {
					analysisErr = errors.New("analysis failed")
					result = nil
				}
				require.NoError(t, ss.saveAudioAnalysis(id, result, analysisErr))

				var saved Upload
				require.NoError(t, ss.crud.DB.First(&saved, "id = ?", id).Error)
				var ops []crudr.Op
				require.NoError(t, ss.crud.DB.Where("\"table\" = ? AND data->0->>'id' = ?", "uploads", id).Find(&ops).Error)
				require.Len(t, ops, 1)
				var replicated []Upload
				require.NoError(t, json.Unmarshal(ops[0].Data, &replicated))
				require.Len(t, replicated, 1)

				for _, got := range []Upload{saved, replicated[0]} {
					// A new transcode's CID is still only in memory. Analysis must
					// leave it busy until transcode publishes the attested result.
					require.Equal(t, upload.Status, got.Status)
					require.Equal(t, upload.TranscodeResults, got.TranscodeResults)
					require.Equal(t, upload.TranscodedBy, got.TranscodedBy)
					require.Equal(t, upload.TranscodeProgress, got.TranscodeProgress)
					require.Equal(t, upload.Mirrors, got.Mirrors)
					require.Equal(t, upload.TranscodedMirrors, got.TranscodedMirrors)
					require.Equal(t, upload.Error, got.Error)
					require.Equal(t, upload.ErrorCount, got.ErrorCount)
					require.Equal(t, ss.Config.Self.Host, got.AudioAnalyzedBy)
					require.False(t, got.AudioAnalyzedAt.IsZero())
					if fail {
						require.Equal(t, JobStatusError, got.AudioAnalysisStatus)
						require.Equal(t, analysisErr.Error(), got.AudioAnalysisError)
						require.Equal(t, 2, got.AudioAnalysisErrorCount)
					} else {
						require.Equal(t, JobStatusDone, got.AudioAnalysisStatus)
						require.Equal(t, result, got.AudioAnalysisResults)
						require.Empty(t, got.AudioAnalysisError)
						require.Equal(t, 1, got.AudioAnalysisErrorCount)
					}
				}
			})
		}
	}
}

func TestFindMissedAudioAnalysisCandidatesBoundsRetriesAndBackoff(t *testing.T) {
	ctx := context.Background()
	ss := testNetwork[0]
	now := time.Now().UTC().Truncate(time.Second)
	prefix := fmt.Sprintf("audio-analysis-candidates-%d-", now.UnixNano())

	cleanup := func() {
		require.NoError(t, ss.crud.DB.Where("id LIKE ?", prefix+"%").Delete(&Upload{}).Error)
	}
	cleanup()
	t.Cleanup(cleanup)

	uploads := []Upload{
		{
			ID:                      prefix + "done",
			Template:                JobTemplateAudio,
			AudioAnalysisStatus:     JobStatusDone,
			AudioAnalysisErrorCount: 0,
			AudioAnalyzedAt:         now.Add(-48 * time.Hour),
		},
		{
			ID:                      prefix + "too-many-errors",
			Template:                JobTemplateAudio,
			AudioAnalysisStatus:     JobStatusError,
			AudioAnalysisErrorCount: MAX_TRIES,
			AudioAnalyzedAt:         now.Add(-48 * time.Hour),
		},
		{
			ID:                      prefix + "recent-error",
			Template:                JobTemplateAudio,
			AudioAnalysisStatus:     JobStatusError,
			AudioAnalysisErrorCount: 1,
			AudioAnalyzedAt:         now.Add(-time.Hour),
		},
		{
			ID:                      prefix + "image",
			Template:                JobTemplateImgSquare,
			AudioAnalysisStatus:     JobStatusError,
			AudioAnalysisErrorCount: 1,
			AudioAnalyzedAt:         now.Add(-48 * time.Hour),
		},
		{
			ID:                      prefix + "old-error",
			Template:                JobTemplateAudio,
			AudioAnalysisStatus:     JobStatusError,
			AudioAnalysisErrorCount: 1,
			AudioAnalyzedAt:         now.Add(-48 * time.Hour),
		},
		{
			// Boundary: error_count == MAX_TRIES-1 must still be retryable.
			ID:                      prefix + "max-minus-one",
			Template:                JobTemplateAudio,
			AudioAnalysisStatus:     JobStatusError,
			AudioAnalysisErrorCount: MAX_TRIES - 1,
			AudioAnalyzedAt:         now.Add(-48 * time.Hour),
		},
		{
			ID:                  prefix + "never-tried",
			Template:            JobTemplateAudio,
			AudioAnalysisStatus: "",
			AudioAnalyzedAt:     time.Time{},
		},
		{
			ID:                      prefix + "transcode-terminal-no-result",
			Template:                JobTemplateAudio,
			Status:                  JobStatusError,
			ErrorCount:              missedTranscodeMaxErrorCount,
			TranscodeResults:        map[string]string{},
			AudioAnalysisStatus:     "",
			AudioAnalysisErrorCount: 0,
			AudioAnalyzedAt:         time.Time{},
		},
		{
			ID:                      prefix + "transcode-terminal-empty-result",
			Template:                JobTemplateAudio,
			Status:                  JobStatusError,
			ErrorCount:              missedTranscodeMaxErrorCount + 1,
			TranscodeResults:        map[string]string{"320": ""},
			AudioAnalysisStatus:     "",
			AudioAnalysisErrorCount: 0,
			AudioAnalyzedAt:         time.Time{},
		},
		{
			ID:                      prefix + "transcode-terminal-with-result",
			Template:                JobTemplateAudio,
			Status:                  JobStatusError,
			ErrorCount:              missedTranscodeMaxErrorCount + 1,
			TranscodeResults:        map[string]string{"320": "cid-320"},
			AudioAnalysisStatus:     "",
			AudioAnalysisErrorCount: 0,
			AudioAnalyzedAt:         time.Time{},
		},
	}
	for i := range uploads {
		require.NoError(t, ss.crud.DB.Create(&uploads[i]).Error)
	}

	candidates, err := ss.findMissedAudioAnalysisCandidates(ctx, now, 100)
	require.NoError(t, err)

	got := map[string]bool{}
	for _, upload := range candidates {
		if strings.HasPrefix(upload.ID, prefix) {
			got[upload.ID] = true
		}
	}

	require.Equal(t, map[string]bool{
		prefix + "never-tried":                    true,
		prefix + "old-error":                      true,
		prefix + "max-minus-one":                  true,
		prefix + "transcode-terminal-with-result": true,
	}, got)
}
