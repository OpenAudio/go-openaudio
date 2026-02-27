package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/OpenAudio/go-openaudio/pkg/hashes"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/OpenAudio/go-openaudio/pkg/sdk/mediorum"
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func main() {
	ctx := context.Background()
	validatorEndpoint := flag.String("validator", "node1.oap.devnet", "Validator endpoint URL")
	serverPort := flag.String("port", "8800", "Server port")
	flag.Parse()

	// Worker key - in production this would be in Cloudflare Worker env
	workerKey, err := crypto.GenerateKey()
	if err != nil {
		log.Fatalf("Failed to generate worker key: %v", err)
	}

	auds := sdk.NewOpenAudioSDK(*validatorEndpoint)
	if err := auds.Init(ctx); err != nil {
		log.Fatalf("failed to init SDK: %v", err)
	}
	auds.SetPrivKey(workerKey)

	// Upload track with worker as signer (worker can grant stream access)
	trackID, err := uploadTrackExample(ctx, auds)
	if err != nil {
		log.Fatalf("upload failed: %v", err)
	}

	handler := &StreamHandler{
		privateKey:   workerKey,
		trackID:     trackID,
		nodeBaseURL: fmt.Sprintf("https://%s", *validatorEndpoint),
	}

	mux := http.NewServeMux()
	mux.Handle("/stream", handler)

	log.Printf("Server starting on :%s", *serverPort)
	log.Printf("Track ID: %d - Stream at http://localhost:%s/stream?track_id=%d", trackID, *serverPort, trackID)
	if err := http.ListenAndServe(":"+*serverPort, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

type StreamHandler struct {
	privateKey   *ecdsa.PrivateKey
	trackID     int64
	nodeBaseURL string
}

func (h *StreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	trackIDStr := r.URL.Query().Get("track_id")
	if trackIDStr == "" {
		http.Error(w, "track_id required", http.StatusBadRequest)
		return
	}
	trackID, err := strconv.ParseInt(trackIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid track_id", http.StatusBadRequest)
		return
	}

	// Generate signature for this track
	sigData := &signature.SignatureData{
		TrackId:     trackID,
		Timestamp:   time.Now().UnixMilli(),
		UploadID:    trackIDStr,
		Cid:         "",
		ShouldCache: 0,
		UserID:      0,
	}
	sigStr, err := signature.GenerateQueryStringFromSignatureData(sigData, h.privateKey)
	if err != nil {
		http.Error(w, "failed to sign", http.StatusInternalServerError)
		return
	}

	streamURL := fmt.Sprintf("%s/tracks/stream/%d?signature=%s", h.nodeBaseURL, trackID, url.QueryEscape(sigStr))
	http.Redirect(w, r, streamURL, http.StatusFound)
}

func uploadTrackExample(ctx context.Context, auds *sdk.OpenAudioSDK) (int64, error) {
	audioPath := "../../pkg/integration_tests/assets/anxiety-upgrade.mp3"
	audioFile, err := os.Open(audioPath)
	if err != nil {
		return 0, fmt.Errorf("open audio: %w", err)
	}
	defer audioFile.Close()

	fileCID, err := hashes.ComputeFileCID(audioFile)
	if err != nil {
		return 0, fmt.Errorf("compute CID: %w", err)
	}
	audioFile.Seek(0, 0)

	uploadSigData := &corev1.UploadSignature{Cid: fileCID}
	uploadSigBytes, err := proto.Marshal(uploadSigData)
	if err != nil {
		return 0, fmt.Errorf("marshal upload sig: %w", err)
	}
	uploadSignature, err := common.EthSign(auds.PrivKey(), uploadSigBytes)
	if err != nil {
		return 0, fmt.Errorf("sign upload: %w", err)
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
		return 0, fmt.Errorf("upload file: %w", err)
	}
	if len(uploads) == 0 {
		return 0, fmt.Errorf("no uploads returned")
	}
	upload := uploads[0]
	if upload.Status != "done" {
		return 0, fmt.Errorf("upload failed: %s", upload.Error)
	}

	transcodedCID := upload.GetTranscodedCID()
	workerAddress := auds.Address()

	entityID := time.Now().UnixNano() % 1000000
	if entityID < 0 {
		entityID = -entityID
	}

	metadata := map[string]interface{}{
		"title":         "Programmable Distribution Demo",
		"genre":         "Electronic",
		"release_date":  time.Now().Format("2006-01-02"),
		"cid":           transcodedCID,
		"stream_conditions": map[string]interface{}{
			"signers": []string{workerAddress},
		},
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return 0, fmt.Errorf("marshal metadata: %w", err)
	}

	manageEntity := &corev1.ManageEntityLegacy{
		UserId:     1,
		EntityType: "Track",
		EntityId:   entityID,
		Action:     "Create",
		Metadata:   string(metadataJSON),
		Nonce:      fmt.Sprintf("0x%064x", entityID),
		Signer:     "",
	}
	mockConfig := &config.Config{
		AcdcEntityManagerAddress: config.DevAcdcAddress,
		AcdcChainID:             config.DevAcdcChainID,
	}
	if err := server.SignManageEntity(mockConfig, manageEntity, auds.PrivKey()); err != nil {
		return 0, fmt.Errorf("sign ManageEntity: %w", err)
	}

	stx := &corev1.SignedTransaction{
		RequestId: uuid.NewString(),
		Transaction: &corev1.SignedTransaction_ManageEntity{
			ManageEntity: manageEntity,
		},
	}
	_, err = auds.Core.SendTransaction(ctx, connect.NewRequest(&corev1.SendTransactionRequest{
		Transaction: stx,
	}))
	if err != nil {
		return 0, fmt.Errorf("send ManageEntity: %w", err)
	}

	return entityID, nil
}
