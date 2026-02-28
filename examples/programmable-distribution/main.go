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
	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/OpenAudio/go-openaudio/pkg/sdk/mediorum"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

func main() {
	ctx := context.Background()
	validatorEndpoint := flag.String("validator", "node1.oap.devnet", "Validator endpoint URL")
	serverPort := flag.String("port", "8800", "Server port")
	flag.Parse()

	signerKey, err := crypto.GenerateKey()
	if err != nil {
		log.Fatalf("Failed to generate signer key: %v", err)
	}

	auds := sdk.NewOpenAudioSDK(*validatorEndpoint)
	if err := auds.Init(ctx); err != nil {
		log.Fatalf("failed to init SDK: %v", err)
	}
	auds.SetPrivKey(signerKey)

	signerAddress := auds.Address()

	fmt.Printf("\n\nYour uploaded track is only accessible with a signature from %s. This local server signs for you. Modify its logic to control who can stream the file back.\n\n", signerAddress)

	// Upload track with signer (signer can grant stream access)
	cid, trackID, err := uploadTrackExample(ctx, auds)
	if err != nil {
		log.Fatalf("upload failed: %v", err)
	}

	nodeBaseURL := fmt.Sprintf("https://%s", *validatorEndpoint)

	log.Printf("Track ID: %d | Stream at http://localhost:%s/stream (no-signature at /stream-no-signature)", trackID, *serverPort)
	log.Printf("Running local server, Ctrl-C to close.")

	handler := &StreamHandler{
		privateKey:  signerKey,
		trackID:     trackID,
		cid:         cid,
		nodeBaseURL: nodeBaseURL,
	}

	mux := http.NewServeMux()
	mux.Handle("/stream", handler)
	mux.Handle("/stream-no-signature", &StreamNoSignatureHandler{trackID: trackID, nodeBaseURL: nodeBaseURL})

	if err := http.ListenAndServe(":"+*serverPort, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

type StreamNoSignatureHandler struct {
	trackID     int64
	nodeBaseURL string
}

func (h *StreamNoSignatureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	streamURL := fmt.Sprintf("%s/tracks/stream/%d", h.nodeBaseURL, h.trackID)
	http.Redirect(w, r, streamURL, http.StatusFound)
}

type StreamHandler struct {
	privateKey  *ecdsa.PrivateKey
	trackID     int64
	cid         string
	nodeBaseURL string
}

func (h *StreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	trackID := h.trackID
	if trackIDParam := r.URL.Query().Get("track_id"); trackIDParam != "" {
		if tid, err := strconv.ParseInt(trackIDParam, 10, 64); err == nil {
			trackID = tid
		}
	}

	// Generate signature for this track ID
	sigData := &signature.SignatureData{
		Cid:         h.cid,
		Timestamp:   time.Now().UnixMilli(),
		UploadID:    "",
		ShouldCache: 0,
		TrackId:     trackID,
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

func uploadTrackExample(ctx context.Context, auds *sdk.OpenAudioSDK) (string, int64, error) {
	audioPath := "../../pkg/integration_tests/assets/anxiety-upgrade.mp3"
	audioFile, err := os.Open(audioPath)
	if err != nil {
		return "", 0, fmt.Errorf("open audio: %w", err)
	}
	defer audioFile.Close()

	fileCID, err := hashes.ComputeFileCID(audioFile)
	if err != nil {
		return "", 0, fmt.Errorf("compute CID: %w", err)
	}
	audioFile.Seek(0, 0)

	uploadSigData := &corev1.UploadSignature{Cid: fileCID}
	uploadSigBytes, err := proto.Marshal(uploadSigData)
	if err != nil {
		return "", 0, fmt.Errorf("marshal upload sig: %w", err)
	}
	uploadSignature, err := common.EthSign(auds.PrivKey(), uploadSigBytes)
	if err != nil {
		return "", 0, fmt.Errorf("sign upload: %w", err)
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
		return "", 0, fmt.Errorf("upload file: %w", err)
	}
	if len(uploads) == 0 {
		return "", 0, fmt.Errorf("no uploads returned")
	}
	upload := uploads[0]
	if upload.Status != "done" {
		return "", 0, fmt.Errorf("upload failed: %s", upload.Error)
	}

	transcodedCID := upload.GetTranscodedCID()
	signerAddress := auds.Address()

	entityID := time.Now().UnixNano() % 1000000
	if entityID < 0 {
		entityID = -entityID
	}

	metadata := map[string]interface{}{
		"cid":                "",
		"access_authorities": []string{signerAddress},
		"data": map[string]interface{}{
			"title":        "Programmable Distribution Demo",
			"genre":        "Electronic",
			"release_date": time.Now().Format("2006-01-02"),
			"track_cid":    transcodedCID,
			"owner_id":     1,
		},
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return "", 0, fmt.Errorf("marshal metadata: %w", err)
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
		AcdcChainID:              config.DevAcdcChainID,
	}
	if err := server.SignManageEntity(mockConfig, manageEntity, auds.PrivKey()); err != nil {
		return "", 0, fmt.Errorf("sign ManageEntity: %w", err)
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
		return "", 0, fmt.Errorf("send ManageEntity: %w", err)
	}

	return transcodedCID, entityID, nil
}
