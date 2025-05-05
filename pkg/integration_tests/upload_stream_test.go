package integrationtests

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1storage "github.com/AudiusProject/audiusd/pkg/api/storage/v1"
	"github.com/AudiusProject/audiusd/pkg/common"
	auds "github.com/AudiusProject/audiusd/pkg/sdk"
	"github.com/stretchr/testify/require"
)

func TestTrackReleaseWorkflow(t *testing.T) {
	ctx := context.Background()

	serverAddr := "node3.audiusd.devnet"
	privKeyPath := "./assets/demo_key.txt"
	privKeyPath2 := "./assets/demo_key2.txt"

	sdk := auds.NewAudiusdSDK(serverAddr)
	if err := sdk.ReadPrivKey(privKeyPath); err != nil {
		require.Nil(t, err, "failed to read private key: %w", err)
	}

	audioFile, err := os.Open("./assets/anxiety-upgrade.mp3")
	require.Nil(t, err, "failed to open file")
	defer audioFile.Close()
	audioFileBytes, err := io.ReadAll(audioFile)
	require.Nil(t, err, "failed to read file")

	// upload the track
	uploadFileRes, err := sdk.Storage.UploadFiles(ctx, &connect.Request[v1storage.UploadFilesRequest]{
		Msg: &v1storage.UploadFilesRequest{
			UserWallet: sdk.Address(),
			Template:   "audio",
			Files: []*v1storage.File{
				{
					Filename: "anxiety-upgrade.mp3",
					Data:     audioFileBytes,
				},
			},
		},
	})
	require.Nil(t, err, "failed to upload file")
	require.EqualValues(t, 1, len(uploadFileRes.Msg.Uploads), "failed to upload file")

	upload := uploadFileRes.Msg.Uploads[0]

	// get the upload info
	uploadRes, err := sdk.Storage.GetUpload(ctx, &connect.Request[v1storage.GetUploadRequest]{
		Msg: &v1storage.GetUploadRequest{
			Id: upload.Id,
		},
	})
	require.Nil(t, err, "failed to get upload")
	require.EqualValues(t, upload.Id, uploadRes.Msg.Upload.Id, "failed to get upload")
	require.EqualValues(t, upload.UserWallet, uploadRes.Msg.Upload.UserWallet, "failed to get upload")
	require.EqualValues(t, upload.OrigFileCid, uploadRes.Msg.Upload.OrigFileCid, "failed to get upload")
	require.EqualValues(t, upload.OrigFilename, uploadRes.Msg.Upload.OrigFilename, "failed to get upload")

	// release the track
	title := "Anxiety Upgrade"
	genre := "Electronic"

	releaseRes, err := sdk.ReleaseTrack(ctx, upload.OrigFileCid, title, genre)
	require.Nil(t, err, "failed to release track")

	trackID := releaseRes.TrackID

	// create stream signature
	data := &v1storage.StreamTrackSignatureData{
		TrackId:   trackID,
		Timestamp: time.Now().Unix(),
	}
	sig, sigData, err := common.GeneratePlaySignature(sdk.PrivKey(), data)
	require.Nil(t, err, "failed to generate stream signature")

	// stream the file
	stream, err := sdk.Storage.StreamTrack(ctx, &connect.Request[v1storage.StreamTrackRequest]{
		Msg: &v1storage.StreamTrackRequest{
			Signature: &v1storage.StreamTrackSignature{
				Signature: sig,
				DataHash:  sigData,
				Data:      data,
			},
		},
	})
	require.Nil(t, err, "failed to stream file")

	var fileData bytes.Buffer
	for stream.Receive() {
		res := stream.Msg()
		if len(res.Data) > 0 {
			fileData.Write(res.Data)
		}
	}
	if err := stream.Err(); err != nil {
		log.Fatalf("stream error: %v", err)
	}

	// verify another user can't stream the file
	sdk2 := auds.NewAudiusdSDK(serverAddr)
	if err := sdk2.ReadPrivKey(privKeyPath2); err != nil {
		require.Nil(t, err, "failed to read private key: %w", err)
	}

	// use same data as first user
	sig2, sigData2, err := common.GeneratePlaySignature(sdk2.PrivKey(), data)
	require.Nil(t, err, "failed to generate stream signature")

	stream2, err := sdk2.Storage.StreamTrack(ctx, &connect.Request[v1storage.StreamTrackRequest]{
		Msg: &v1storage.StreamTrackRequest{
			Signature: &v1storage.StreamTrackSignature{
				Signature: sig2,
				DataHash:  sigData2,
				Data:      data,
			},
		},
	})

	// consume the stream
	for stream2.Receive() {
	}
	require.Nil(t, err)
	require.NotNil(t, stream2.Err(), "could not stream file")
}
