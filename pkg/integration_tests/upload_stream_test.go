package integrationtests

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"testing"

	"github.com/AudiusProject/audiusd/pkg/sdk"
	"github.com/stretchr/testify/require"
)

func TestTrackReleaseWorkflow(t *testing.T) {
	ctx := context.Background()

	serverAddr := "node3.audiusd.devnet"
	privKeyPath := "./assets/demo_key.txt"
	privKeyPath2 := "./assets/demo_key2.txt"
	title := "Anxiety Upgrade"
	genre := "Electronic"
	downloadPath := fmt.Sprintf("%s/test_audio_download.mp3", os.TempDir())

	// configure storage sdk
	storageSdk := sdk.NewStorageSDK(fmt.Sprintf("https://%s", serverAddr))
	err := storageSdk.LoadPrivateKey(privKeyPath)
	require.Nil(t, err, "failed to set privKey on storage sdk")

	// ensure storage health
	health, err := storageSdk.GetHealth()
	require.Nil(t, err)
	require.Equal(t, health.Data.Healthy, true)

	// upload audio file
	uploadRes, err := storageSdk.UploadAudio("./assets/anxiety-upgrade.mp3")
	require.Nil(t, err, "failed to upload file")
	require.EqualValues(t, 1, len(uploadRes), "failed to upload file")
	upload := uploadRes[0]
	require.Equal(t, upload.OrigFileName, "anxiety-upgrade.mp3")

	// release the track
	sdk := sdk.NewAudiusdSDK(serverAddr)
	if err := sdk.ReadPrivKey(privKeyPath); err != nil {
		require.Nil(t, err, "failed to read private key: %w", err)
	}
	res, err := sdk.ReleaseTrack(ctx, upload.OrigFileCID, title, genre)
	require.Nil(t, err, "failed to release track")

	// Now try to access the file
	err = storageSdk.DownloadTrack(res.TrackID, downloadPath)
	require.Nil(t, err, "failed to download track")

	// Try to access the file with a different key
	err = storageSdk.LoadPrivateKey(privKeyPath2)
	require.Nil(t, err, "failed to set privKey2 on storage sdk")
	err = storageSdk.DownloadTrack(res.TrackID, downloadPath)
	require.NotNil(t, err, "expected error when downloading track with wrong key")
	require.ErrorContains(t, err, "signer not authorized")
}
