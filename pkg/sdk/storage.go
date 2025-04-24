package sdk

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/AudiusProject/audiusd/pkg/common"
	"github.com/AudiusProject/audiusd/pkg/mediorum/server"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gowebpki/jcs"
)

type StorageSDK struct {
	nodeURL    string
	httpClient *http.Client
	privKey    *ecdsa.PrivateKey
}

func NewStorageSDK(nodeURL string) *StorageSDK {
	return &StorageSDK{
		nodeURL: nodeURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (s *StorageSDK) LoadPrivateKey(path string) error {
	key, err := common.LoadPrivateKey(path)
	if err != nil {
		return err
	}
	s.privKey = key
	return nil
}

func (s *StorageSDK) GetNodeURL() string {
	return s.nodeURL
}

func (s *StorageSDK) SetNodeURL(nodeURL string) {
	s.nodeURL = nodeURL
}

func (s *StorageSDK) GetHealth() (*server.HealthCheckResponse, error) {
	req, err := http.NewRequest("GET", s.nodeURL+"/health_check", nil)
	if err != nil {
		return nil, err
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var healthCheckResponse server.HealthCheckResponse
	err = json.Unmarshal(body, &healthCheckResponse)
	if err != nil {
		return nil, err
	}

	return &healthCheckResponse, nil
}

func (s *StorageSDK) UploadAudio(filePath string) ([]*server.Upload, error) {
	// Open the audio file
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Create a buffer to hold the multipart form data
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Create the file field in the multipart form
	part, err := writer.CreateFormFile("files", filepath.Base(filePath))
	if err != nil {
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}

	// Copy the file data into the form
	_, err = io.Copy(part, file)
	if err != nil {
		return nil, fmt.Errorf("failed to copy file data: %w", err)
	}

	// Add additional fields (if any)
	err = writer.WriteField("template", "audio")
	if err != nil {
		return nil, fmt.Errorf("failed to write field: %w", err)
	}

	// Close the writer to finalize the multipart form
	err = writer.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to close writer: %w", err)
	}

	// Create the HTTP POST request
	req, err := http.NewRequest("POST", s.nodeURL+"/uploads", &body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set the Content-Type header to the multipart boundary
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// Execute the request
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Check the response status
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse the response body
	var upload []*server.Upload
	err = json.NewDecoder(resp.Body).Decode(&upload)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return upload, nil
}

func (s *StorageSDK) DownloadTrack(trackId, outputPath string) error {
	if s.privKey == nil {
		return errors.New("No private key set, cannot download track")
	}

	dataObj := map[string]interface{}{
		"upload_id":   trackId,
		"cid":         "",
		"shouldCache": 0,
		"timestamp":   time.Now().Unix(),
		"trackId":     0,
		"userId":      0,
	}

	jsonBytes, err := json.Marshal(dataObj)
	if err != nil {
		return fmt.Errorf("failed to marshal: %w", err)
	}
	canonicalJSON, err := jcs.Transform(jsonBytes)
	if err != nil {
		return fmt.Errorf("failed to canonicalize: %w", err)
	}

	// Hash and sign
	hash := crypto.Keccak256Hash(canonicalJSON)
	sig, err := crypto.Sign(hash[:], s.privKey)
	if err != nil {
		return fmt.Errorf("failed to sign: %w", err)
	}
	sigHex := "0x" + hex.EncodeToString(sig)

	// Wrap in envelope
	envelope := map[string]string{
		"Data":      string(jsonBytes),
		"Signature": sigHex,
	}
	envelopeBytes, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("failed to marshal envelope: %w", err)
	}
	query := url.Values{}

	query.Set("signature", string(envelopeBytes))

	fullURL := fmt.Sprintf("%s/tracks/stream/%s?%s", s.nodeURL, trackId, query.Encode())
	fmt.Printf("Downloading from: %s\n", fullURL)

	resp, err := http.Get(fullURL)
	if err != nil {
		return fmt.Errorf("failed to make GET request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download failed: status %d - %s", resp.StatusCode, string(body))
	}

	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer outFile.Close()

	_, err = io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write to file: %w", err)
	}

	return nil
}
