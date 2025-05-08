package signature

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	ethcommon "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gowebpki/jcs"
	"google.golang.org/protobuf/proto"
	protob "google.golang.org/protobuf/proto"
)

type SignatureEnvelope struct {
	Data      string
	Signature string
}

type SignatureData struct {
	UploadID    string `json:"upload_id"`
	Cid         string `json:"cid"`
	ShouldCache int    `json:"shouldCache"`
	Timestamp   int64  `json:"timestamp"`
	TrackId     int64  `json:"trackId"`
	UserID      int    `json:"userId"`
}

type RecoveredSignature struct {
	DataHash     ethcommon.Hash
	Data         SignatureData
	SignerWallet string
	SignerPubkey []byte
}

type ListenTSSignature struct {
	Signature string
	Timestamp string
}

func (r *RecoveredSignature) String() string {
	j, _ := json.Marshal(r)
	return string(j)
}

func ParseFromQueryString(queryStringValue string) (*RecoveredSignature, error) {
	var envelope *SignatureEnvelope

	err := json.Unmarshal([]byte(queryStringValue), &envelope)
	if err != nil {
		return nil, err
	}

	// ensure json keys are sorted
	inner, err := jcs.Transform([]byte(envelope.Data))
	if err != nil {
		return nil, err
	}

	hash := crypto.Keccak256Hash(inner)
	// TextHash will prepend Ethereum signed message prefix to the hash
	// and hash that again
	hash2 := accounts.TextHash(hash.Bytes())

	signatureBytes, err := hex.DecodeString(envelope.Signature[2:])
	if err != nil {
		return nil, err
	}

	// Normalize v
	if len(signatureBytes) < 65 {
		return nil, errors.New("Invalid signature length")
	}
	if signatureBytes[64] >= 27 {
		signatureBytes[64] -= 27
	}

	recoveredPubkey, err := crypto.SigToPub(hash2, signatureBytes)
	if err != nil {
		return nil, err
	}
	recoveredAddress := crypto.PubkeyToAddress(*recoveredPubkey)
	pubkeyBytes := crypto.CompressPubkey(recoveredPubkey)

	var data SignatureData
	err = json.Unmarshal([]byte(envelope.Data), &data)
	if err != nil {
		return nil, err
	}

	recovered := &RecoveredSignature{
		DataHash:     hash,
		Data:         data,
		SignerWallet: recoveredAddress.String(),
		SignerPubkey: pubkeyBytes,
	}

	return recovered, nil
}

func GenerateQueryStringFromSignatureData(data *SignatureData, privKey *ecdsa.PrivateKey) (string, error) {
	// Step 1: Marshal SignatureData to JSON
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshal SignatureData: %w", err)
	}

	// Step 2: Apply JCS transformation for canonical JSON
	canonicalJSON, err := jcs.Transform(dataBytes)
	if err != nil {
		return "", fmt.Errorf("jcs.Transform: %w", err)
	}

	// Step 3: Hash the canonical JSON
	hash := crypto.Keccak256Hash(canonicalJSON)

	// Step 4: Ethereum-style text hash
	hash2 := accounts.TextHash(hash.Bytes())

	// Step 5: Sign the hash
	sigBytes, err := crypto.Sign(hash2, privKey)
	if err != nil {
		return "", fmt.Errorf("crypto.Sign: %w", err)
	}

	// Step 6: Normalize v to Ethereum style (i.e. +27)
	if len(sigBytes) == 65 {
		sigBytes[64] += 27
	}

	// Step 7: Encode to hex with 0x prefix
	signatureHex := "0x" + hex.EncodeToString(sigBytes)

	// Step 8: Construct the envelope
	envelope := &SignatureEnvelope{
		Data:      string(dataBytes), // original, not canonical
		Signature: signatureHex,
	}

	// Step 9: Marshal the envelope to JSON string
	envelopeBytes, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("marshal envelope: %w", err)
	}

	return string(envelopeBytes), nil
}

func GenerateListenTimestampAndSignature(privateKey *ecdsa.PrivateKey) (*ListenTSSignature, error) {
	// based on: https://github.com/AudiusProject/audius-protocol/blob/main/creator-node/src/apiSigning.ts
	// '{"data":"listen","timestamp":"2023-05-24T15:37:57.051Z"}'
	timestamp := time.Now().UTC().Format(time.RFC3339)
	data := fmt.Sprintf("{\"data\":\"listen\",\"timestamp\":\"%s\"}", timestamp)

	signature, err := Sign(data, privateKey)
	if err != nil {
		fmt.Println("Error signing message:", err)
		return nil, err
	}
	signatureHex := fmt.Sprintf("0x%s", hex.EncodeToString(signature))

	return &ListenTSSignature{
		Signature: signatureHex,
		Timestamp: timestamp,
	}, nil
}

// From https://github.com/AudiusProject/sig/blob/main/go/index.go
func Sign(input string, privateKey *ecdsa.PrivateKey) ([]byte, error) {
	return SignBytes([]byte(input), privateKey)
}

func SignBytes(input []byte, privateKey *ecdsa.PrivateKey) ([]byte, error) {
	// hash the input
	hash := crypto.Keccak256Hash(input)
	// TextHash will prepend Ethereum signed message prefix to the hash
	// and hash that again
	hash2 := accounts.TextHash(hash.Bytes())

	signature, err := crypto.Sign(hash2, privateKey)
	if err != nil {
		return nil, err
	}
	return signature, nil
}

func SignCoreBytes(input proto.Message, privateKey *ecdsa.PrivateKey) (string, error) {
	eventBodyBytes, err := protob.Marshal(input)
	if err != nil {
		return "", err
	}

	signedBody, err := SignBytes(eventBodyBytes, privateKey)
	if err != nil {
		return "", err
	}

	return hex.EncodeToString(signedBody), nil
}
