package rewards

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/mr-tron/base58/base58"
)

const AddSenderMessagePrefix = "add"

type CreateSenderAttestationParams struct {
	// must be existing validator
	NewSenderAddress string

	// base58 encoded pubkey
	RewardsManagerAccountPubKey string
}

func GetCreateSenderAttestation(signer *ecdsa.PrivateKey, params *CreateSenderAttestationParams) (ownerWallet string, signedAttestation string, err error) {
	newSenderAddress := strings.TrimPrefix(params.NewSenderAddress, "0x")
	programPubKey := params.RewardsManagerAccountPubKey

	programBytes, err := base58.Decode(programPubKey)
	if err != nil {
		return "", "", fmt.Errorf("invalid program pubkey: %w", err)
	}

	// concatenate bytes: "add" + rewardsManagerPubkey + newSenderAddress (as bytes)
	addrBytes, err := hex.DecodeString(newSenderAddress)
	if err != nil {
		return "", "", fmt.Errorf("invalid sender address: %w", err)
	}

	var attestation bytes.Buffer
	attestation.WriteString(AddSenderMessagePrefix)
	attestation.Write(programBytes)
	attestation.Write(addrBytes)

	sig, err := common.EthSignKeccak(signer, attestation.Bytes())
	if err != nil {
		return "", "", err
	}

	_, address := common.EthPublicKeyAndAddress(signer)

	return address.Hex(), sig, nil
}
