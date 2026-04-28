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
const DeleteSenderMessagePrefix = "del"

type CreateSenderAttestationParams struct {
	// must be existing validator
	NewSenderAddress string

	// base58 encoded pubkey
	RewardsManagerAccountPubKey string
}

type DeleteSenderAttestationParams struct {
	// must not be a registered validator
	SenderAddress string

	// base58 encoded pubkey
	RewardsManagerAccountPubKey string
}

func GetCreateSenderAttestation(signer *ecdsa.PrivateKey, params *CreateSenderAttestationParams) (ownerWallet string, signedAttestation string, err error) {
	return getSenderAttestation(signer, AddSenderMessagePrefix, params.NewSenderAddress, params.RewardsManagerAccountPubKey)
}

func GetDeleteSenderAttestation(signer *ecdsa.PrivateKey, params *DeleteSenderAttestationParams) (ownerWallet string, signedAttestation string, err error) {
	return getSenderAttestation(signer, DeleteSenderMessagePrefix, params.SenderAddress, params.RewardsManagerAccountPubKey)
}

func getSenderAttestation(signer *ecdsa.PrivateKey, messagePrefix string, senderAddress string, rewardsManagerAccountPubKey string) (ownerWallet string, signedAttestation string, err error) {
	senderAddress = strings.TrimPrefix(senderAddress, "0x")

	programBytes, err := base58.Decode(rewardsManagerAccountPubKey)
	if err != nil {
		return "", "", fmt.Errorf("invalid program pubkey: %w", err)
	}

	// concatenate bytes: prefix ("add" or "del") + rewardsManagerPubkey + senderAddress (as bytes)
	addrBytes, err := hex.DecodeString(senderAddress)
	if err != nil {
		return "", "", fmt.Errorf("invalid sender address: %w", err)
	}

	var attestation bytes.Buffer
	attestation.WriteString(messagePrefix)
	attestation.Write(programBytes)
	attestation.Write(addrBytes)

	sig, err := common.EthSignKeccak(signer, attestation.Bytes())
	if err != nil {
		return "", "", err
	}

	_, address := common.EthPublicKeyAndAddress(signer)

	return address.Hex(), sig, nil
}
