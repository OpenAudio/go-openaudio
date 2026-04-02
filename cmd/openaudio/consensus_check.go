package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	coreServer "github.com/OpenAudio/go-openaudio/pkg/core/server"
	"github.com/labstack/echo/v4"
)

type nodeResult struct {
	Endpoint string `json:"endpoint"`
	Healthy  bool   `json:"healthy"`
	Error    string `json:"error,omitempty"`
}

type consensusCheckResponse struct {
	HealthyNodes   int          `json:"healthy_nodes"`
	UnhealthyNodes int          `json:"unhealthy_nodes"`
	TotalNodes     int          `json:"total_nodes"`
	HealthyPercent int          `json:"healthy_percent"`
	Threshold      int          `json:"threshold"`
	Alert          bool         `json:"alert"`
	Nodes          []nodeResult `json:"nodes"`
}

func handleConsensusCheck(c echo.Context, coreService *coreServer.CoreService) error {
	if !coreService.IsReady() {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{"error": "core service not ready"})
	}

	threshold := 66
	if t := c.QueryParam("threshold"); t != "" {
		parsed, err := strconv.Atoi(t)
		if err != nil || parsed < 0 || parsed > 100 {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "threshold must be an integer between 0 and 100"})
		}
		threshold = parsed
	}

	ctx := c.Request().Context()
	endpoints, err := coreService.GetConsensusNodeEndpoints(ctx)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to get consensus nodes: %v", err)})
	}

	totalNodes := len(endpoints)
	if totalNodes == 0 {
		return c.JSON(http.StatusOK, consensusCheckResponse{Threshold: threshold, Nodes: []nodeResult{}})
	}

	results := checkNodeHealth(ctx, endpoints)

	healthyCount := 0
	for _, r := range results {
		if r.Healthy {
			healthyCount++
		}
	}

	healthyPercent := (healthyCount * 100) / totalNodes
	alert := healthyPercent < threshold

	status := http.StatusOK
	if alert {
		status = http.StatusServiceUnavailable
	}

	return c.JSON(status, consensusCheckResponse{
		HealthyNodes:   healthyCount,
		UnhealthyNodes: totalNodes - healthyCount,
		TotalNodes:     totalNodes,
		HealthyPercent: healthyPercent,
		Threshold:      threshold,
		Alert:          alert,
		Nodes:          results,
	})
}

func checkNodeHealth(ctx context.Context, endpoints []string) []nodeResult {
	results := make([]nodeResult, len(endpoints))
	var wg sync.WaitGroup
	client := &http.Client{Timeout: 3 * time.Second}

	for i, ep := range endpoints {
		wg.Add(1)
		go func(idx int, ep string) {
			defer wg.Done()
			results[idx] = checkSingleNode(ctx, client, ep)
		}(i, ep)
	}
	wg.Wait()
	return results
}

func checkSingleNode(ctx context.Context, client *http.Client, endpoint string) nodeResult {
	result := nodeResult{Endpoint: endpoint}

	reqURL := strings.TrimRight(endpoint, "/") + "/health-check"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		result.Error = fmt.Sprintf("failed to create request: %v", err)
		return result
	}

	resp, err := client.Do(req)
	if err != nil {
		result.Error = fmt.Sprintf("request failed: %v", err)
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("non-200 status: %d", resp.StatusCode)
		return result
	}

	var body struct {
		Core struct {
			Live bool `json:"live"`
		} `json:"core"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		result.Error = fmt.Sprintf("failed to decode response: %v", err)
		return result
	}

	result.Healthy = body.Core.Live
	if !result.Healthy {
		result.Error = "live is false"
	}
	return result
}
