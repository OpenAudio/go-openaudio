package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/OpenAudio/go-openaudio/pkg/hashes"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/OpenAudio/go-openaudio/pkg/sdk/mediorum"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func main() {
	ctx := context.Background()
	serverAddr := "node1.oap.devnet"
	privKeyPath := "../../pkg/integration_tests/assets/demo_key.txt"
	audioPath := "../../pkg/integration_tests/assets/anxiety-upgrade.mp3"

	auds := sdk.NewOpenAudioSDK(serverAddr)
	if err := auds.Init(ctx); err != nil {
		log.Fatalf("failed to init SDK: %v", err)
	}
	if err := auds.ReadPrivKey(privKeyPath); err != nil {
		log.Fatalf("failed to read private key: %v", err)
	}

	audioFile, err := os.Open(audioPath)
	if err != nil {
		log.Fatalf("failed to open audio file: %v", err)
	}
	defer audioFile.Close()

	fileCID, err := hashes.ComputeFileCID(audioFile)
	if err != nil {
		log.Fatalf("failed to compute file CID: %v", err)
	}
	audioFile.Seek(0, 0)

	uploadSigData := &corev1.UploadSignature{Cid: fileCID}
	uploadSigBytes, err := proto.Marshal(uploadSigData)
	if err != nil {
		log.Fatalf("failed to marshal upload signature: %v", err)
	}
	uploadSignature, err := common.EthSign(auds.PrivKey(), uploadSigBytes)
	if err != nil {
		log.Fatalf("failed to sign upload: %v", err)
	}

	uploadOpts := &mediorum.UploadOptions{
		Template:          "audio",
		Signature:         uploadSignature,
		WaitForTranscode:  true,
		WaitForFileUpload: true,
		OriginalCID:       fileCID,
	}

	uploads, err := auds.Mediorum.UploadFile(ctx, audioFile, "anxiety-upgrade.mp3", uploadOpts)
	if err != nil {
		log.Fatalf("failed to upload file: %v", err)
	}
	if len(uploads) == 0 {
		log.Fatalf("no uploads returned")
	}
	upload := uploads[0]
	if upload.Status != "done" {
		log.Fatalf("upload failed: %s", upload.Error)
	}

	transcodedCID := upload.GetTranscodedCID()

	// Build ManageEntity Track Create
	entityID := time.Now().UnixNano() % 1000000 // deterministic-ish for demo
	if entityID < 0 {
		entityID = -entityID
	}

	metadata := map[string]interface{}{
		"title":        "Anxiety Upgrade",
		"genre":        "Electronic",
		"release_date": time.Now().Format("2006-01-02"),
		"cid":          transcodedCID,
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		log.Fatalf("failed to marshal metadata: %v", err)
	}

	manageEntity := &corev1.ManageEntityLegacy{
		UserId:     1, // placeholder; devnet may use different schemes
		EntityType: "Track",
		EntityId:   entityID,
		Action:     "Create",
		Metadata:   string(metadataJSON),
		Nonce:      fmt.Sprintf("0x%064x", entityID), // 32-byte hex for EIP712
		Signer:     "",                               // filled by SignManageEntity
	}

	mockConfig := &config.Config{
		AcdcEntityManagerAddress: config.DevAcdcAddress,
		AcdcChainID:              config.DevAcdcChainID,
	}
	if err := server.SignManageEntity(mockConfig, manageEntity, auds.PrivKey()); err != nil {
		log.Fatalf("failed to sign ManageEntity: %v", err)
	}

	stx := &corev1.SignedTransaction{
		RequestId: uuid.NewString(),
		Transaction: &corev1.SignedTransaction_ManageEntity{
			ManageEntity: manageEntity,
		},
	}

	submitRes, err := auds.Core.SendTransaction(ctx, connect.NewRequest(&corev1.SendTransactionRequest{
		Transaction: stx,
	}))
	if err != nil {
		log.Fatalf("failed to send ManageEntity tx: %v", err)
	}

	fmt.Printf("uploaded cid: %s\n", transcodedCID)
	fmt.Printf("tx receipt: %s\n", submitRes.Msg.Transaction.Hash)
}
