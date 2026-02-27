package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/common"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (h *GeolocationHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Simulate Cloudflare geolocation header (in real implementation this would come from CF)
	city := r.Header.Get("CF-IPCity")
	if city == "" {
		// For demo purposes, check a custom header or query param
		city = r.URL.Query().Get("city")
		if city == "" {
			city = "Unknown"
		}
	}

	log.Printf("Request from city: %s", city)

	// Check if request is from allowed city
	if !strings.EqualFold(city, h.allowedCity) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode("the treasure you seek is in bozeman")
		return
	}

	// Build addresses list from stored ERN data
	addresses := []string{}
	if h.ernAddress != "" {
		addresses = append(addresses, h.ernAddress)
	}
	addresses = append(addresses, h.resourceAddresses...)
	addresses = append(addresses, h.releaseAddresses...)

	if len(addresses) == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode("no tracks uploaded yet")
		return
	}

	// Create signature with 1 hour expiration
	expiry := time.Now().Add(1 * time.Hour)
	sig := &v1.GetStreamURLsSignature{
		Addresses: addresses,
		ExpiresAt: timestamppb.New(expiry),
	}

	sigData, err := proto.Marshal(sig)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode("oops")
		return
	}

	streamSignature, err := common.EthSign(h.auds.PrivKey(), sigData)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode("kablam")
		return
	}

	res, err := h.auds.Core.GetStreamURLs(r.Context(), connect.NewRequest(&v1.GetStreamURLsRequest{
		Signature: streamSignature,
		Addresses: addresses,
		ExpiresAt: timestamppb.New(expiry),
	}))

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode("couldn't get urls")
		return
	}

	urls := res.Msg.GetEntityStreamUrls()
	// only need first one here
	for _, entityUrls := range urls {
		if len(entityUrls.Urls) > 0 {
			// Redirect to the first stream URL
			http.Redirect(w, r, entityUrls.Urls[0], http.StatusFound)
			return
		}
	}

	// If no URLs found, return error
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode("no stream URLs available")
}
