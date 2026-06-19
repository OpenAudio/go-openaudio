package fuzz

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultScheme         = "https"
	defaultRequestTimeout = 5 * time.Second
)

// Client reads node status through public HTTP surfaces.
type Client struct {
	HTTPClient     *http.Client
	DefaultScheme  string
	RequestTimeout time.Duration
}

type ClientOption func(*Client)

func NewClient(opts ...ClientOption) *Client {
	c := &Client{
		HTTPClient:     &http.Client{Timeout: defaultRequestTimeout},
		DefaultScheme:  defaultScheme,
		RequestTimeout: defaultRequestTimeout,
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.DefaultScheme == "" {
		c.DefaultScheme = defaultScheme
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = defaultRequestTimeout
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: c.RequestTimeout}
	}
	return c
}

func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		c.HTTPClient = client
	}
}

func WithDefaultScheme(scheme string) ClientOption {
	return func(c *Client) {
		c.DefaultScheme = strings.TrimSuffix(scheme, "://")
	}
}

func WithRequestTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		c.RequestTimeout = timeout
		if c.HTTPClient != nil {
			c.HTTPClient.Timeout = timeout
		}
	}
}

func WithInsecureTLS() ClientOption {
	return func(c *Client) {
		c.HTTPClient = &http.Client{
			Timeout: c.RequestTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // test harness option for local self-signed nodes
			},
		}
	}
}

func (c *Client) GetNodeStatus(ctx context.Context, node NodeSpec) (NodeStatus, error) {
	status := NodeStatus{
		ID:         node.ID,
		Endpoint:   node.Endpoint,
		ObservedAt: time.Now().UTC(),
	}
	baseURL, err := c.baseURL(node.Endpoint)
	if err != nil {
		status.ObservationError = err.Error()
		return status, err
	}

	var errs []error
	if body, err := c.getJSON(ctx, baseURL, "/health-check"); err == nil {
		status.Reachable = true
		mergeHealthStatus(&status, body)
	} else {
		errs = append(errs, fmt.Errorf("health-check: %w", err))
	}

	if body, err := c.getJSON(ctx, baseURL, "/core.v1.CoreService/GetStatus"); err == nil {
		status.Reachable = true
		mergeCoreStatus(&status, body)
	} else {
		errs = append(errs, fmt.Errorf("core status: %w", err))
	}

	if body, err := c.getJSON(ctx, baseURL, "/core/crpc/status"); err == nil {
		status.Reachable = true
		mergeCometStatus(&status, body)
	} else {
		errs = append(errs, fmt.Errorf("comet status: %w", err))
	}

	if status.Reachable {
		if len(errs) > 0 {
			status.ObservationError = errorsText(errs)
		}
		return status, nil
	}

	err = fmt.Errorf("%s unreachable: %s", node.ID, errorsText(errs))
	status.ObservationError = err.Error()
	return status, err
}

func (c *Client) DiscoverValidatorEndpoints(ctx context.Context, endpoint string) ([]string, error) {
	baseURL, err := c.baseURL(endpoint)
	if err != nil {
		return nil, err
	}
	body, err := c.getJSON(ctx, baseURL, "/console/api/core-validators-endpoints")
	if err != nil {
		return nil, err
	}
	raw, ok := body["endpoints"].([]any)
	if !ok {
		return nil, fmt.Errorf("validator endpoint response missing endpoints array")
	}

	endpoints := make([]string, 0, len(raw))
	for _, item := range raw {
		endpoint, ok := item.(string)
		if !ok || strings.TrimSpace(endpoint) == "" {
			continue
		}
		endpoints = append(endpoints, endpoint)
	}
	if len(endpoints) == 0 {
		return nil, fmt.Errorf("validator endpoint response contained no usable endpoints")
	}
	return endpoints, nil
}

func (c *Client) baseURL(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", fmt.Errorf("empty endpoint")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = c.DefaultScheme + "://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid endpoint %q", endpoint)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/"), nil
}

func (c *Client) getJSON(ctx context.Context, baseURL, path string) (map[string]any, error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out map[string]any
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func mergeHealthStatus(status *NodeStatus, body map[string]any) {
	status.Git = stringAt(body, "git")
	status.Version = firstNonEmpty(status.Version, stringAt(body, "data", "version"), stringAt(body, "version", "version"))
	core := mapAt(body, "core")
	if core == nil {
		return
	}
	mergeCoreStatus(status, core)
}

func mergeCoreStatus(status *NodeStatus, body map[string]any) {
	status.Ready = status.Ready || boolAt(body, "ready")
	status.Live = status.Live || boolAt(body, "live")
	status.Synced = status.Synced || boolAt(body, "sync_info", "synced") || boolAt(body, "syncInfo", "synced")
	status.Version = firstNonEmpty(status.Version, stringAt(body, "version"), stringAt(body, "data", "version"))
	status.Git = firstNonEmpty(status.Git, stringAt(body, "git"))

	height := firstInt64(
		int64At(body, "chain_info", "current_height"),
		int64At(body, "chainInfo", "currentHeight"),
		int64At(body, "sync_info", "latest_block_height"),
		int64At(body, "syncInfo", "latestBlockHeight"),
	)
	if height > 0 {
		status.Height = height
	}

	status.BlockHash = firstNonEmpty(
		status.BlockHash,
		stringAt(body, "chain_info", "current_block_hash"),
		stringAt(body, "chainInfo", "currentBlockHash"),
	)
	status.ProcessState = firstNonEmpty(
		status.ProcessState,
		stringAt(body, "process_info", "abci", "state"),
		stringAt(body, "processInfo", "abci", "state"),
	)
	status.ProcessError = firstNonEmpty(
		status.ProcessError,
		stringAt(body, "process_info", "abci", "error"),
		stringAt(body, "processInfo", "abci", "error"),
	)
}

func mergeCometStatus(status *NodeStatus, body map[string]any) {
	status.Live = true
	height := int64At(body, "result", "sync_info", "latest_block_height")
	if height > status.Height {
		status.Height = height
	}
	status.ValidatorPower = firstInt64(
		status.ValidatorPower,
		int64At(body, "result", "validator_info", "voting_power"),
	)
}

func mapAt(root map[string]any, path ...string) map[string]any {
	var cur any = root
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	m, _ := cur.(map[string]any)
	return m
}

func valueAt(root map[string]any, path ...string) any {
	var cur any = root
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[key]
	}
	return cur
}

func stringAt(root map[string]any, path ...string) string {
	switch v := valueAt(root, path...).(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

func boolAt(root map[string]any, path ...string) bool {
	v, ok := valueAt(root, path...).(bool)
	return ok && v
}

func int64At(root map[string]any, path ...string) int64 {
	switch v := valueAt(root, path...).(type) {
	case json.Number:
		i, _ := v.Int64()
		return i
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		i, _ := strconv.ParseInt(v, 10, 64)
		return i
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstInt64(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

func errorsText(errs []error) string {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	return strings.Join(parts, "; ")
}
