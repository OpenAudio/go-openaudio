package server

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

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
			ErrorCount:              missedTranscodeMaxErrorCount + 1,
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
