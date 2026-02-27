package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func main() {
	ctx := context.Background()

	validatorEndpoint := flag.String("validator", "node3.oap.devnet", "Validator endpoint URL")
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

	// Upload track via DDEX
	addresses, err := uploadTrackExample(ctx, auds)
	if err != nil {
		log.Fatalf("upload failed: %v", err)
	}

	// Prime the stream URL by calling GetStreamURLs once
	streamURL, streamURLNoSig := getStreamURL(ctx, auds, addresses)
	if streamURL == "" {
		log.Fatalf("failed to get stream URL")
	}

	handler := &StreamHandler{
		auds:                auds,
		addresses:           addresses,
		streamURLNoSigMutex: &sync.RWMutex{},
	}
	handler.setStreamURLNoSig(streamURLNoSig)

	log.Printf("ERN: %s | Stream at http://localhost:%s/stream (no-signature at /stream-no-signature)", addresses[0], *serverPort)
	log.Printf("Running local server, Ctrl-C to close.")

	mux := http.NewServeMux()
	mux.Handle("/stream", handler)
	mux.Handle("/stream-no-signature", &StreamNoSignatureHandler{handler: handler})

	if err := http.ListenAndServe(":"+*serverPort, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

type StreamHandler struct {
	auds                *sdk.OpenAudioSDK
	addresses           []string
	streamURLNoSig      string
	streamURLNoSigMutex *sync.RWMutex
}

func (h *StreamHandler) setStreamURLNoSig(u string) {
	h.streamURLNoSigMutex.Lock()
	defer h.streamURLNoSigMutex.Unlock()
	h.streamURLNoSig = u
}

func (h *StreamHandler) getStreamURLNoSig() string {
	h.streamURLNoSigMutex.RLock()
	defer h.streamURLNoSigMutex.RUnlock()
	return h.streamURLNoSig
}

func (h *StreamHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	streamURL, streamURLNoSig := getStreamURL(r.Context(), h.auds, h.addresses)
	if streamURL == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode("no stream URLs available")
		return
	}

	h.setStreamURLNoSig(streamURLNoSig)
	http.Redirect(w, r, streamURL, http.StatusFound)
}

type StreamNoSignatureHandler struct {
	handler *StreamHandler
}

func (h *StreamNoSignatureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	u := h.handler.getStreamURLNoSig()
	if u == "" {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode("no stream URL yet - use /stream first")
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}

func getStreamURL(ctx context.Context, auds *sdk.OpenAudioSDK, addresses []string) (streamURL, streamURLNoSig string) {
	if len(addresses) == 0 {
		return "", ""
	}

	expiry := time.Now().Add(1 * time.Hour)
	sig := &corev1.GetStreamURLsSignature{
		Addresses: addresses,
		ExpiresAt: timestamppb.New(expiry),
	}
	sigData, err := proto.Marshal(sig)
	if err != nil {
		return "", ""
	}
	streamSignature, err := common.EthSign(auds.PrivKey(), sigData)
	if err != nil {
		return "", ""
	}

	res, err := auds.Core.GetStreamURLs(ctx, connect.NewRequest(&corev1.GetStreamURLsRequest{
		Signature: streamSignature,
		Addresses: addresses,
		ExpiresAt: timestamppb.New(expiry),
	}))
	if err != nil {
		return "", ""
	}

	for _, entityUrls := range res.Msg.GetEntityStreamUrls() {
		if len(entityUrls.Urls) > 0 {
			u := entityUrls.Urls[0]
			parsed, err := url.Parse(u)
			if err != nil {
				return u, ""
			}
			q := parsed.Query()
			q.Del("signature")
			parsed.RawQuery = q.Encode()
			return u, parsed.String()
		}
	}
	return "", ""
}
