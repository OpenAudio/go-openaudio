package sdk

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/AudiusProject/audiusd/pkg/mediorum/server"
)

type StorageSDK struct {
	nodeURL string
	httpClient *http.Client
}

func NewStorageSDK(nodeURL string) *StorageSDK {
	return &StorageSDK{
		nodeURL: nodeURL,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
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


func (s *StorageSDK) GetAudio() (*server.Upload, error) {
	return nil, nil
}
