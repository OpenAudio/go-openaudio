package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/cidutil"
	"github.com/stretchr/testify/require"
)

func TestCheckAudioFormat(t *testing.T) {
	probe := func(name string) *FFProbeResult {
		p := &FFProbeResult{}
		p.Format.FormatName = name
		return p
	}
	for _, name := range []string{"wav", "mp3", "flac", "aiff", "aac", "ogg", "mov,mp4,m4a,3gp,3g2,mj2", "mpc"} {
		require.NoError(t, checkAudioFormat(probe(name)), name)
	}
	for _, name := range []string{"hls", "png_pipe", "jpeg_pipe", "lrc", "avi", "concat", "", "matroska,webm"} {
		require.ErrorAs(t, checkAudioFormat(probe(name)), &errUnsupportedAudioFormat{}, name)
	}
	require.Error(t, checkAudioFormat(nil))
}

// decodeWithAudioInputArgs runs ffmpeg the way the transcode does, discarding
// the output.
func decodeWithAudioInputArgs(path string) error {
	args := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error"}, ffmpegAudioInputArgs()...)
	args = append(args, "-i", path, "-f", "null", "-")
	out, err := exec.Command("ffmpeg", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func TestFFmpegAudioInputArgs(t *testing.T) {
	requireFFmpeg(t)

	t.Run("audio decodes", func(t *testing.T) {
		p, err := ffprobe("testdata/beep.wav")
		require.NoError(t, err)
		require.NoError(t, checkAudioFormat(p))
		require.NoError(t, decodeWithAudioInputArgs("testdata/beep.wav"))
	})

	t.Run("an image is refused", func(t *testing.T) {
		p, err := ffprobe("testdata/claudius.jpg")
		require.NoError(t, err)
		require.Error(t, checkAudioFormat(p))
		require.Error(t, decodeWithAudioInputArgs("testdata/claudius.jpg"))
	})

	// A playlist names other inputs for ffmpeg to open. Even one pointing at
	// a perfectly good local audio file is refused: ffmpeg must only ever read
	// the bytes it was handed.
	t.Run("a playlist is refused", func(t *testing.T) {
		beep, err := filepath.Abs("testdata/beep.wav")
		require.NoError(t, err)
		playlist := filepath.Join(t.TempDir(), "list.m3u8")
		require.NoError(t, os.WriteFile(playlist, []byte(
			"#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\n"+beep+"\n#EXT-X-ENDLIST\n"), 0o644))

		p, err := ffprobe(playlist)
		if err == nil {
			require.Error(t, checkAudioFormat(p))
		}
		require.Error(t, decodeWithAudioInputArgs(playlist))
	})
}

func TestUploadRejectsNonAudio(t *testing.T) {
	requireFFmpeg(t)
	s1 := testNetwork[0]

	resp, err := s1.reqClient.R().
		SetFile("files", "testdata/claudius.jpg").
		SetFormData(map[string]string{"template": "audio"}).
		Post(s1.Config.Self.Host + "/uploads")
	require.NoError(t, err)
	require.Equal(t, 400, resp.StatusCode)
	require.Contains(t, string(resp.Bytes()), "unsupported audio format")
}

func TestUploadRejectsNonAudioTus(t *testing.T) {
	requireFFmpeg(t)
	s1 := testNetwork[0]

	fileData, err := os.ReadFile("testdata/claudius.jpg")
	require.NoError(t, err)

	createResp, err := s1.reqClient.R().
		SetHeader("Upload-Length", fmt.Sprintf("%d", len(fileData))).
		SetHeader("Upload-Metadata", fmt.Sprintf("filename %s,template %s",
			base64.StdEncoding.EncodeToString([]byte("song.wav")),
			base64.StdEncoding.EncodeToString([]byte("audio")))).
		SetHeader("Tus-Resumable", "1.0.0").
		Post(s1.Config.Self.Host + "/files/")
	require.NoError(t, err)
	require.Equal(t, 201, createResp.StatusCode)
	location := createResp.Header.Get("Location")
	uploadID := location[strings.LastIndex(location, "/")+1:]
	// The refused row stays behind as an error; other tests count rows in the
	// shared uploads table, so take it out.
	t.Cleanup(func() {
		for _, ss := range testNetwork {
			ss.crud.DB.Where("id = ?", uploadID).Delete(&Upload{})
		}
	})

	patchResp, err := s1.reqClient.R().
		SetHeader("Content-Type", "application/offset+octet-stream").
		SetHeader("Upload-Offset", "0").
		SetHeader("Tus-Resumable", "1.0.0").
		SetBody(fileData).
		Patch(location)
	require.NoError(t, err)
	require.Equal(t, 204, patchResp.StatusCode)

	require.Eventually(t, func() bool {
		var u Upload
		if err := s1.crud.DB.First(&u, "id = ?", uploadID).Error; err != nil {
			return false
		}
		return u.Status == JobStatusError && strings.Contains(u.Error, "unsupported audio format")
	}, 30*time.Second, 250*time.Millisecond)
}

// An upload row can come from a node that never checked the format. The
// transcode must check the bytes itself rather than trust the row's probe.
func TestTranscodeChecksFormatOfTheBlob(t *testing.T) {
	requireFFmpeg(t)
	ctx := context.Background()
	ss := testNetwork[0]

	id := fmt.Sprintf("!!!!format-check-%d", time.Now().UnixNano())

	// An image with a unique trailer: still probes as an image, but under a
	// cid no other test uses (TestServeImage expects claudius.jpg's own cid to
	// be absent from this node).
	data, err := os.ReadFile("testdata/claudius.jpg")
	require.NoError(t, err)
	data = append(data, []byte(id)...)
	cid, err := cidutil.ComputeFileCID(bytes.NewReader(data))
	require.NoError(t, err)
	require.NoError(t, ss.replicateToMyBucket(ctx, cid, bytes.NewReader(data), nil))
	t.Cleanup(func() { ss.crud.DB.Where("id = ?", id).Delete(&Upload{}) })
	upload := &Upload{
		ID:               id,
		Template:         JobTemplateAudio,
		OrigFileName:     "song.wav",
		OrigFileCID:      cid,
		Status:           JobStatusNew,
		TranscodeResults: map[string]string{},
		FFProbe:          &FFProbeResult{},
	}
	upload.FFProbe.Format.FormatName = "wav" // what the other node claimed
	require.NoError(t, ss.crud.DB.Create(upload).Error)

	err = ss.transcode(ctx, upload)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported audio format")

	var row Upload
	require.NoError(t, ss.crud.DB.First(&row, "id = ?", id).Error)
	require.Equal(t, JobStatusError, row.Status)
	require.Empty(t, row.TranscodeResults["320"])
}
