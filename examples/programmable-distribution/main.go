package main

import (
	"context"
	"crypto/ecdsa"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/OpenAudio/go-openaudio/pkg/sdk"
	"github.com/ethereum/go-ethereum/crypto"
)

type GeolocationHandler struct {
	privateKey        *ecdsa.PrivateKey
	allowedCity       string
	auds              *sdk.AudiusdSDK
	ernAddress        string
	resourceAddresses []string
	releaseAddresses  []string
}

func NewGeolocationHandler(privateKey *ecdsa.PrivateKey, allowedCity string, auds *sdk.AudiusdSDK) *GeolocationHandler {
	return &GeolocationHandler{
		privateKey:        privateKey,
		allowedCity:       allowedCity,
		auds:              auds,
		ernAddress:        "",
		resourceAddresses: []string{},
		releaseAddresses:  []string{},
	}
}

func main() {
	ctx := context.Background()

	validatorEndpoint := flag.String("validator", "node3.audiusd.devnet", "Validator endpoint URL")
	serverPort := flag.String("port", "8800", "Server port")
	flag.Parse()

	privateKey, err := crypto.GenerateKey()
	if err != nil {
		log.Fatalf("Failed to generate private key: %v", err)
	}

	auds := sdk.NewAudiusdSDK(*validatorEndpoint)
	auds.Init(ctx)
	auds.SetPrivKey(privateKey)

	handler := NewGeolocationHandler(privateKey, "Bozeman", auds)

	// Upload a demo track in the background
	go func() {
		if err := uploadTrackExample(ctx, auds, handler); err != nil {
			fmt.Printf("track upload failed: %v\n", err)
		}
	}()

	// Start HTTP server for the filtering service (main thread)
	mux := http.NewServeMux()
	mux.Handle("/stream-access", handler)

	fmt.Println("server starting")

	if err := http.ListenAndServe(":"+*serverPort, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
