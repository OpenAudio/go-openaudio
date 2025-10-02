package common

import (
	"testing"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSignAndRecover(t *testing.T) {
	// development key, do not use in production
	// generated from eth vanity generator
	privKeyHex := "6bc52a1494870c9329324261dbb457db34c8c1369bc9eb336b25965f46f43cc8"
	expectedAddress := "0xfAf20A7cAed2Ed9054DcADb09778Ce59bEc3A6AD"

	privKey, err := EthToEthKey(privKeyHex)
	require.Nil(t, err)

	registerEvent := &v1.ValidatorRegistration{
		CometAddress:   "12345",
		DelegateWallet: "0xabcdef",
		EthBlock:       20202,
		Power:          10,
	}

	eventBytes, err := proto.Marshal(registerEvent)
	require.Nil(t, err)

	sig, err := EthSign(privKey, eventBytes)
	require.Nil(t, err)

	pubKey, address, err := EthRecover(sig, eventBytes)
	require.Nil(t, err)
	require.EqualValues(t, expectedAddress, address)
	require.EqualValues(t, privKey.PublicKey, *pubKey)
}
