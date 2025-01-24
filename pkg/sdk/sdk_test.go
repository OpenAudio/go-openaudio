package sdk

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSDKHealth(t *testing.T) {
	storageSDK := NewStorageSDK("https://creatornode.audius.co")
	_, err := storageSDK.GetHealth()
	require.Nil(t, err)
}
