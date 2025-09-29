package server

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"strings"

	v1 "github.com/AudiusProject/audiusd/pkg/api/core/v1"
	"github.com/AudiusProject/audiusd/pkg/core/config"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

func InjectSigner(config *config.Config, em *v1.ManageEntityLegacy) error {
	address, _, err := RecoverPubkeyFromCoreTx(config, em)
	if err != nil {
		return err
	}

	em.Signer = address
	return nil
}

func RecoverPubkeyFromCoreTx(config *config.Config, em *v1.ManageEntityLegacy) (string, *ecdsa.PublicKey, error) {
	contractAddress := config.AcdcEntityManagerAddress
	chainId := config.AcdcChainID

	var nonce [32]byte
	copy(nonce[:], toBytes(em.Nonce))

	var typedData = apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": []apitypes.Type{
				{
					Name: "name",
					Type: "string",
				},
				{
					Name: "version",
					Type: "string",
				},
				{
					Name: "chainId",
					Type: "uint256",
				},
				{
					Name: "verifyingContract",
					Type: "address",
				},
			},
			"ManageEntity": []apitypes.Type{
				{
					Name: "userId",
					Type: "uint",
				},
				{
					Name: "entityType",
					Type: "string",
				},
				{
					Name: "entityId",
					Type: "uint",
				},
				{
					Name: "action",
					Type: "string",
				},
				{
					Name: "metadata",
					Type: "string",
				},
				{
					Name: "nonce",
					Type: "bytes32",
				},
			},
		},
		Domain: apitypes.TypedDataDomain{
			Name:              "Entity Manager",
			Version:           "1",
			ChainId:           math.NewHexOrDecimal256(int64(chainId)),
			VerifyingContract: contractAddress,
		},
		PrimaryType: "ManageEntity",
		Message: map[string]interface{}{
			"userId":     fmt.Sprintf("%d", em.UserId),
			"entityType": em.EntityType,
			"entityId":   fmt.Sprintf("%d", em.EntityId),
			"action":     em.Action,
			"metadata":   em.Metadata,
			"nonce":      nonce,
		},
	}

	pubkeyBytes, err := recoverPublicKey(toBytes(em.Signature), typedData)
	if err != nil {
		return "", nil, err
	}

	pubkey, err := crypto.UnmarshalPubkey(pubkeyBytes)
	if err != nil {
		return "", nil, err
	}

	address := crypto.PubkeyToAddress(*pubkey).String()
	return address, pubkey, nil
}

func toBytes(str string) []byte {
	v, _ := hex.DecodeString(strings.TrimPrefix(str, "0x"))
	return v
}

// taken from:
// https://gist.github.com/APTy/f2a6864a97889793c587635b562c7d72#file-main-go
func recoverPublicKey(signature []byte, typedData apitypes.TypedData) ([]byte, error) {

	domainSeparator, err := typedData.HashStruct("EIP712Domain", typedData.Domain.Map())
	if err != nil {
		return nil, fmt.Errorf("eip712domain hash struct: %w", err)
	}

	typedDataHash, err := typedData.HashStruct(typedData.PrimaryType, typedData.Message)
	if err != nil {
		return nil, fmt.Errorf("primary type hash struct: %w", err)
	}

	// add magic string prefix
	rawData := []byte(fmt.Sprintf("\x19\x01%s%s", string(domainSeparator), string(typedDataHash)))
	sighash := crypto.Keccak256(rawData)

	// update the recovery id
	// https://github.com/ethereum/go-ethereum/blob/55599ee95d4151a2502465e0afc7c47bd1acba77/internal/ethapi/api.go#L442
	if len(signature) > 64 {
		signature[64] -= 27
	}

	return crypto.Ecrecover(sighash, signature)

}

// SignManageEntity creates an EIP712 signature for a ManageEntityLegacy message and sets it on the message
func SignManageEntity(config *config.Config, em *v1.ManageEntityLegacy, privateKey *ecdsa.PrivateKey) error {
	contractAddress := config.AcdcEntityManagerAddress
	chainId := config.AcdcChainID

	var nonce [32]byte
	copy(nonce[:], toBytes(em.Nonce))

	var typedData = apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain": []apitypes.Type{
				{
					Name: "name",
					Type: "string",
				},
				{
					Name: "version",
					Type: "string",
				},
				{
					Name: "chainId",
					Type: "uint256",
				},
				{
					Name: "verifyingContract",
					Type: "address",
				},
			},
			"ManageEntity": []apitypes.Type{
				{
					Name: "userId",
					Type: "uint",
				},
				{
					Name: "entityType",
					Type: "string",
				},
				{
					Name: "entityId",
					Type: "uint",
				},
				{
					Name: "action",
					Type: "string",
				},
				{
					Name: "metadata",
					Type: "string",
				},
				{
					Name: "nonce",
					Type: "bytes32",
				},
			},
		},
		Domain: apitypes.TypedDataDomain{
			Name:              "Entity Manager",
			Version:           "1",
			ChainId:           math.NewHexOrDecimal256(int64(chainId)),
			VerifyingContract: contractAddress,
		},
		PrimaryType: "ManageEntity",
		Message: map[string]interface{}{
			"userId":     fmt.Sprintf("%d", em.UserId),
			"entityType": em.EntityType,
			"entityId":   fmt.Sprintf("%d", em.EntityId),
			"action":     em.Action,
			"metadata":   em.Metadata,
			"nonce":      nonce,
		},
	}

	domainSeparator, err := typedData.HashStruct("EIP712Domain", typedData.Domain.Map())
	if err != nil {
		return fmt.Errorf("eip712domain hash struct: %w", err)
	}

	typedDataHash, err := typedData.HashStruct(typedData.PrimaryType, typedData.Message)
	if err != nil {
		return fmt.Errorf("primary type hash struct: %w", err)
	}

	// add magic string prefix
	rawData := []byte(fmt.Sprintf("\x19\x01%s%s", string(domainSeparator), string(typedDataHash)))
	sighash := crypto.Keccak256(rawData)

	// Sign the hash
	signature, err := crypto.Sign(sighash, privateKey)
	if err != nil {
		return fmt.Errorf("failed to sign: %w", err)
	}

	// Adjust recovery id for Ethereum
	signature[64] += 27

	em.Signature = "0x" + hex.EncodeToString(signature)
	return nil
}
