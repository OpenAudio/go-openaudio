package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// awaitUploadMirrors drives the confirming pass and waits for the mirrors it
// records.
//
// A handoff records no mirror: the peer answers 202 and the row still says the
// blob is unreplicated until something asks again and is told already_present.
// In production that something is findMissedReplications, on a five minute
// ticker. Asserting mirrors the instant status flips to done, as this used to,
// tests a synchronicity the design gave up -- the blob is on the peers either
// way, and only the record of it arrives later.
//
// It re-queues this upload rather than calling findMissedReplications, which
// would be the closer analogue but is the wrong tool in a test. That sweep
// selects every under-replicated upload in the table, and the table is shared
// across tests and accumulates across runs; calling it repeatedly floods
// replicationWork with unrelated rows, several workers then land on the same
// upload, and the mirror write -- a read-modify-write -- drops an entry. The
// test would manufacture the very race it is trying to observe. Re-queueing one
// upload reaches the same worker code by the same channel, and touches nothing
// else.
func awaitUploadMirrors(t *testing.T, reader, opsFrom, readFrom *MediorumServer, uploadID string, want int) *Upload {
	t.Helper()

	var got *Upload
	require.Eventually(t, func() bool {
		requeueUploadForReplication(t, uploadID)
		// From the creator: applyCoreOpsFrom replays the ops that node authored
		// onto every other, and the mirror update is one of them. Replaying the
		// reader's own ops would propagate nothing.
		applyCoreOpsFrom(t, opsFrom)

		var u Upload
		resp, err := reader.reqClient.R().SetSuccessResult(&u).Get(readFrom.Config.Self.Host + "/uploads/" + uploadID)
		if err != nil || resp.StatusCode != 200 {
			return false
		}
		got = &u
		return u.Status == JobStatusDone && len(u.Mirrors) == want && len(u.TranscodedMirrors) == want
	}, 20*time.Second, 250*time.Millisecond,
		"upload never reached done with %d mirrors recorded", want)

	return got
}

// requeueUploadForReplication hands one upload back to its creator's
// replication workers, reading the row fresh so the worker sees the transcode
// results and mirrors as they stand rather than a stale snapshot.
func requeueUploadForReplication(t *testing.T, uploadID string) {
	t.Helper()

	for _, ss := range testNetwork {
		var u Upload
		if err := ss.crud.DB.Where("id = ? AND created_by = ?", uploadID, ss.Config.Self.Host).First(&u).Error; err != nil {
			continue
		}
		select {
		case ss.replicationWork <- &u:
		default:
		}
		return
	}
}

func TestUploadFile(t *testing.T) {
	ctx := context.Background()
	s1 := testNetwork[0]
	s2 := testNetwork[1]

	var uploads []Upload

	resp := s1.reqClient.R().
		SetFile("files", "testdata/beep.wav").
		SetFormData(map[string]string{"template": "audio"}).
		SetSuccessResult(&uploads).
		MustPost(s1.Config.Self.Host + "/uploads")

	assert.Equal(t, resp.StatusCode, 200)
	uploadId := uploads[0].ID

	// poll for complete, and for the mirrors the handoff records on a
	// confirming pass rather than during the upload
	u2 := awaitUploadMirrors(t, s2, s1, s2, uploadId, s1.Config.ReplicationFactor)

	assert.Equal(t, u2.TranscodeProgress, 1.0)
	assert.Len(t, u2.TranscodedMirrors, s1.Config.ReplicationFactor)
	assert.Equal(t, u2.TranscodedBy, s1.Config.Self.Host)

	// Every completion snapshot must be playable, including intermediate ops
	// a peer may serve before it receives the final transcode update.
	var uploadOps []crudr.Op
	require.NoError(t, s1.crud.DB.Where("\"table\" = ? AND data->0->>'id' = ?", "uploads", uploadId).Find(&uploadOps).Error)
	require.NotEmpty(t, uploadOps)
	for _, op := range uploadOps {
		var snapshots []Upload
		require.NoError(t, json.Unmarshal(op.Data, &snapshots))
		for _, snapshot := range snapshots {
			if snapshot.Status == JobStatusDone {
				require.NotEmpty(t, snapshot.TranscodeResults["320"], "completion op %s has no audio CID", op.ULID)
			}
		}
	}

	// check transcode stats
	{
		s1stats := s1.updateTranscodeStats(ctx)
		assert.Equal(t, 1, s1stats.UploadCount)
		assert.Greater(t, s1stats.MinTranscodeTime, 0.1)
	}

	// test preview

	{
		var audioPreview AudioPreview
		resp := s1.reqClient.R().
			SetSuccessResult(&audioPreview).
			MustPost(s1.Config.Self.Host + "/generate_preview/" + u2.TranscodeResults["320"] + "/1")
		assert.Equal(t, resp.StatusCode, 200)
		assert.Equal(t, "1", audioPreview.PreviewStartSeconds)
	}
}

func TestUploadPlacement(t *testing.T) {
	s1 := testNetwork[0]
	s2 := testNetwork[1]
	s3 := testNetwork[2]
	s5 := testNetwork[4]

	examplePlacement := []string{
		s3.Config.Self.Host,
		s5.Config.Self.Host,
	}

	var uploads []Upload

	resp := s1.reqClient.R().
		SetFile("files", "testdata/tom.wav").
		SetFormData(map[string]string{
			"template":        "audio",
			"placement_hosts": strings.Join(examplePlacement, ","),
		}).
		SetSuccessResult(&uploads).
		MustPost(s3.Config.Self.Host + "/uploads")

	assert.Equal(t, resp.StatusCode, 200)
	assert.Equal(t, examplePlacement, uploads[0].PlacementHosts)
	assert.Equal(t, []string{s3.Config.Self.Host}, uploads[0].Mirrors)
	uploadId := uploads[0].ID

	// poll for complete, driving the sweep that records mirrors after a handoff
	u2 := awaitUploadMirrors(t, s2, s3, s3, uploadId, len(examplePlacement))

	assert.Equal(t, u2.TranscodeProgress, 1.0)

	assert.ElementsMatch(t, u2.Mirrors, examplePlacement)
	assert.ElementsMatch(t, u2.TranscodedMirrors, examplePlacement)

	// verify correct blob locations
	{
		locations := testNetworkLocateBlob(u2.OrigFileCID)
		assert.ElementsMatch(t, locations, examplePlacement)

		locations = testNetworkLocateBlob(u2.TranscodeResults["320"])
		assert.ElementsMatch(t, locations, examplePlacement)
	}

	// drop from s5
	s5.dropFromMyBucket(u2.OrigFileCID)

	// run repair
	testNetworkRunRepair(true)

	// verify correct blob locations
	{
		locations := testNetworkLocateBlob(u2.OrigFileCID)
		assert.ElementsMatch(t, locations, examplePlacement)

		locations = testNetworkLocateBlob(u2.TranscodeResults["320"])
		assert.ElementsMatch(t, locations, examplePlacement)
	}

}

func TestUploadPlacementTus(t *testing.T) {
	s1 := testNetwork[0]
	s2 := testNetwork[1]
	s3 := testNetwork[2]
	s5 := testNetwork[4]

	examplePlacement := []string{
		s3.Config.Self.Host,
		s5.Config.Self.Host,
	}

	// Read file for upload
	fileData, err := os.ReadFile("testdata/tom.wav")
	assert.NoError(t, err)

	// Create TUS upload
	createResp, err := s1.reqClient.R().
		SetHeader("Upload-Length", fmt.Sprintf("%d", len(fileData))).
		SetHeader("Upload-Metadata", fmt.Sprintf("filename dG9tLndhdg==,template YXVkaW8=,placementHosts %s",
			base64.StdEncoding.EncodeToString([]byte(strings.Join(examplePlacement, ","))))).
		SetHeader("Tus-Resumable", "1.0.0").
		Post(s3.Config.Self.Host + "/files/")

	assert.NoError(t, err)
	assert.Equal(t, 201, createResp.StatusCode)

	uploadLocation := createResp.Header.Get("Location")
	assert.NotEmpty(t, uploadLocation)

	// Extract upload ID from location (Location is absolute URL like http://127.0.0.1:1984/files/xyz)
	uploadId := uploadLocation[strings.LastIndex(uploadLocation, "/")+1:]

	// Upload file content (use absolute URL from Location header)
	patchResp, err := s1.reqClient.R().
		SetHeader("Content-Type", "application/offset+octet-stream").
		SetHeader("Upload-Offset", "0").
		SetHeader("Tus-Resumable", "1.0.0").
		SetBody(fileData).
		Patch(uploadLocation)

	assert.NoError(t, err)
	assert.Equal(t, 204, patchResp.StatusCode)

	// poll for complete
	var u2 *Upload
	for i := 0; i < 30; i++ {
		applyCoreOpsFrom(t, s3)
		var uploadResp Upload
		resp, err := s2.reqClient.R().SetSuccessResult(&uploadResp).Get(s3.Config.Self.Host + "/uploads/" + uploadId)
		assert.NoError(t, err)
		if resp.StatusCode == 404 {
			time.Sleep(time.Second)
			continue
		}
		assert.Equal(t, 200, resp.StatusCode)
		u2 = &uploadResp
		require.NotEqual(t, u2.Status, JobStatusError, "upload failed with error: "+u2.Error)
		if u2.Status == JobStatusDone {
			break
		}
		time.Sleep(time.Second)
	}

	require.NotNil(t, u2)
	require.Equal(t, JobStatusDone, u2.Status)

	// Mirrors land on the confirming pass after the handoff, not during the
	// upload, so drive that pass rather than reading the row that just said
	// done.
	u2 = awaitUploadMirrors(t, s2, s3, s3, uploadId, len(examplePlacement))

	assert.Equal(t, u2.TranscodeProgress, 1.0)

	assert.ElementsMatch(t, u2.Mirrors, examplePlacement)
	assert.ElementsMatch(t, u2.TranscodedMirrors, examplePlacement)

	// verify correct blob locations
	{
		locations := testNetworkLocateBlob(u2.OrigFileCID)
		assert.ElementsMatch(t, locations, examplePlacement)

		locations = testNetworkLocateBlob(u2.TranscodeResults["320"])
		assert.ElementsMatch(t, locations, examplePlacement)
	}

	// drop from s5
	s5.dropFromMyBucket(u2.OrigFileCID)

	// run repair
	testNetworkRunRepair(true)

	// verify correct blob locations
	{
		locations := testNetworkLocateBlob(u2.OrigFileCID)
		assert.ElementsMatch(t, locations, examplePlacement)

		locations = testNetworkLocateBlob(u2.TranscodeResults["320"])
		assert.ElementsMatch(t, locations, examplePlacement)
	}
}

func TestUploadWithInvalidPlacementHosts(t *testing.T) {
	s1 := testNetwork[0]

	// Create placement hosts array with invalid host
	invalidPlacementHosts := []string{
		s1.Config.Self.Host,
		"http://invalid-host:1991", // This host is not in config.Peers
	}

	var uploads []Upload

	resp, err := s1.reqClient.R().
		SetFile("files", "testdata/tom.wav").
		SetFormData(map[string]string{
			"template":        "audio",
			"placement_hosts": strings.Join(invalidPlacementHosts, ","),
		}).
		SetSuccessResult(&uploads).
		Post(s1.Config.Self.Host + "/uploads")

	assert.NoError(t, err)
	assert.Equal(t, 400, resp.StatusCode)
	assert.Contains(t, string(resp.Bytes()), "all placement_hosts must be registered")
}

func TestUploadWithInvalidPlacementHostsTus(t *testing.T) {
	s1 := testNetwork[0]

	// Create placement hosts array with invalid host
	invalidPlacementHosts := []string{
		s1.Config.Self.Host,
		"http://invalid-host:1991", // This host is not in config.Peers
	}

	// Read file for upload
	fileData, err := os.ReadFile("testdata/tom.wav")
	assert.NoError(t, err)

	// Attempt to create TUS upload with invalid placement hosts - should fail validation
	createResp, err := s1.reqClient.R().
		SetHeader("Upload-Length", fmt.Sprintf("%d", len(fileData))).
		SetHeader("Upload-Metadata", fmt.Sprintf("filename dG9tLndhdg==,template YXVkaW8=,placementHosts %s",
			base64.StdEncoding.EncodeToString([]byte(strings.Join(invalidPlacementHosts, ","))))).
		SetHeader("Tus-Resumable", "1.0.0").
		Post(s1.Config.Self.Host + "/files/")

	assert.NoError(t, err)
	assert.Equal(t, 400, createResp.StatusCode)
}
