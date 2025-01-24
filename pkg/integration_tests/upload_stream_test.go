package integrationtests

import (
	_ "embed"
	"testing"

	"github.com/AudiusProject/audiusd/pkg/sdk"
	"github.com/stretchr/testify/require"
)

func TestStorageUpload(t *testing.T) {
	sdk := sdk.NewAudiusdSDK(nil, sdk.NewStorageSDK("https://node2.audiusd.devnet"))
	
	health, err := sdk.Storage().GetHealth()
	require.Nil(t, err)

	require.Equal(t, health.Data.Healthy, true)

	uploadRes, err := sdk.Storage().UploadAudio("./assets/anxiety-upgrade.mp3")
	require.Equal(t, len(uploadRes), 1)
	require.Nil(t, err)

	upload := uploadRes[0]

	require.Equal(t, upload.OrigFileName, "anxiety-upgrade.mp3")
}
