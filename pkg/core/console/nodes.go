package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	ethv1 "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/console/views/pages"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/OpenAudio/go-openaudio/pkg/version"
	"github.com/labstack/echo/v4"
	"golang.org/x/mod/semver"
)

func (con *Console) nodePage(c echo.Context) error {
	ctx := c.Request().Context()

	nodeID := strings.ToUpper(c.Param("node"))
	node := db.CoreValidator{}

	if strings.HasPrefix(nodeID, "0x") {
		record, err := con.db.GetRegisteredNodeByEthAddress(ctx, nodeID)
		if err != nil {
			return err
		}
		node = record
	} else {
		record, err := con.db.GetRegisteredNodeByCometAddress(ctx, nodeID)
		if err != nil {
			return err
		}
		node = record
	}

	view := &pages.NodePageView{
		Endpoint:     node.Endpoint,
		EthAddress:   node.EthAddress,
		CometAddress: node.CometAddress,
	}

	return con.views.RenderNodeView(c, view)
}

func (con *Console) nodesPage(c echo.Context) error {
	ctx := c.Request().Context()

	// Fetch consensus validators from CometBFT
	var consensusNodes []pages.ConsensusNode
	if con.rpc != nil {
		validators, err := con.rpc.Validators(ctx, nil, nil, nil)
		if err == nil && validators != nil {
			// Build a map of comet address -> core_validator for cross-referencing
			allNodes, _ := con.db.GetAllRegisteredNodesIncludingJailed(ctx)
			cometMap := make(map[string]db.CoreValidator)
			for _, n := range allNodes {
				cometMap[strings.ToUpper(n.CometAddress)] = n
			}

			for _, val := range validators.Validators {
				addr := strings.ToUpper(val.Address.String())
				cn := pages.ConsensusNode{
					CometAddress: addr,
					VotingPower:  val.VotingPower,
				}
				if node, ok := cometMap[addr]; ok {
					cn.Endpoint = node.Endpoint
					cn.EthAddress = node.EthAddress
					cn.NodeType = node.NodeType
				}
				consensusNodes = append(consensusNodes, cn)
			}
		}
	}

	// Fetch all core_validators (including jailed)
	nodes, err := con.db.GetAllRegisteredNodesIncludingJailed(ctx)
	if err != nil {
		return err
	}

	// Fetch eth registry endpoints
	var ethEndpoints []pages.EthEndpoint
	if resp, err := con.eth.GetRegisteredEndpoints(ctx, connect.NewRequest(&ethv1.GetRegisteredEndpointsRequest{})); err == nil && resp.Msg != nil {
		for _, ep := range resp.Msg.Endpoints {
			ethEndpoints = append(ethEndpoints, pages.EthEndpoint{
				Endpoint:       ep.Endpoint,
				ServiceType:    ep.ServiceType,
				Owner:          ep.Owner,
				DelegateWallet: ep.DelegateWallet,
				BlockNumber:    ep.BlockNumber,
			})
		}
	}

	return con.views.RenderNodesView(c, &pages.NodesView{
		ConsensusNodes:      consensusNodes,
		ConsensusNodesCount: len(consensusNodes),
		Nodes:               nodes,
		ValidatorNodesCount: len(nodes),
		EthEndpoints:        ethEndpoints,
		EthEndpointsCount:   len(ethEndpoints),
	})
}

// coreValidatorsEndpointsAPI returns this node's core_validators endpoints as JSON.
// Used by the Matrix View to query each node's view of the network.
func (con *Console) coreValidatorsEndpointsAPI(c echo.Context) error {
	ctx := c.Request().Context()
	nodes, err := con.db.GetAllRegisteredNodesIncludingJailed(ctx)
	if err != nil {
		return err
	}
	endpoints := make([]string, len(nodes))
	for i, n := range nodes {
		endpoints[i] = n.Endpoint
	}
	return c.JSON(200, map[string]interface{}{"endpoints": endpoints})
}

// matrixAPI fetches core_validators from this node and each peer, returns the full matrix.
// Server-side aggregation avoids CORS when querying remote nodes.
func (con *Console) matrixAPI(c echo.Context) error {
	ctx := c.Request().Context()

	nodes, err := con.db.GetAllRegisteredNodesIncludingJailed(ctx)
	if err != nil {
		return c.JSON(500, map[string]string{"error": err.Error()})
	}
	refList := make([]string, len(nodes))
	for i, n := range nodes {
		refList[i] = strings.TrimSuffix(strings.ToLower(n.Endpoint), "/")
	}

	type row struct {
		Base      string   `json:"base"`
		Endpoints []string `json:"endpoints"`
		Err       string   `json:"err,omitempty"`
	}
	results := make([]row, 0, len(refList))

	client := &http.Client{Timeout: 15 * time.Second}
	for _, ep := range refList {
		// Normalize to URL for fetching
		baseURL := ep
		if !strings.HasPrefix(baseURL, "http") {
			baseURL = "https://" + baseURL
		}
		u, _ := url.Parse(baseURL)
		u.Path = "/console/api/core-validators-endpoints"
		u.RawQuery = ""

		req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if err != nil {
			results = append(results, row{Base: ep, Err: err.Error()})
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			results = append(results, row{Base: ep, Err: err.Error()})
			continue
		}
		var data struct {
			Endpoints []string `json:"endpoints"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			resp.Body.Close()
			results = append(results, row{Base: ep, Err: err.Error()})
			continue
		}
		resp.Body.Close()
		normalized := make([]string, len(data.Endpoints))
		for i, e := range data.Endpoints {
			normalized[i] = strings.TrimSuffix(strings.ToLower(e), "/")
		}
		results = append(results, row{Base: ep, Endpoints: normalized})
	}

	return c.JSON(200, map[string]interface{}{
		"referenceList": refList,
		"rows":          results,
	})
}

// versionAdoptionAPI fetches version info for each consensus validator.
// Uses peer health data first, falls back to direct HTTP requests.
func (con *Console) versionAdoptionAPI(c echo.Context) error {
	ctx := c.Request().Context()

	// Get consensus validators from CometBFT for the denominator
	var consensusEndpoints []string
	consensusTotal := 0
	selfEndpoint := normalizeEndpoint(con.config.NodeEndpoint)
	includedSelf := false

	if con.rpc != nil {
		validators, err := con.rpc.Validators(ctx, nil, nil, nil)
		if err == nil && validators != nil {
			consensusTotal = len(validators.Validators)

			// Cross-reference with core_validators to get endpoints
			allNodes, _ := con.db.GetAllRegisteredNodesIncludingJailed(ctx)
			cometMap := make(map[string]db.CoreValidator)
			for _, n := range allNodes {
				cometMap[strings.ToUpper(n.CometAddress)] = n
			}

			for _, val := range validators.Validators {
				addr := strings.ToUpper(val.Address.String())
				if node, ok := cometMap[addr]; ok {
					ep := normalizeEndpoint(node.Endpoint)
					if selfEndpoint != "" && ep == selfEndpoint {
						includedSelf = true
						continue // will count ourselves separately
					}
					consensusEndpoints = append(consensusEndpoints, node.Endpoint)
				}
			}
		}
	}

	// If we couldn't get consensus validators, fall back to core_validators
	if consensusTotal == 0 {
		nodes, err := con.db.GetAllRegisteredNodesIncludingJailed(ctx)
		if err != nil {
			return c.JSON(500, map[string]string{"error": err.Error()})
		}
		consensusTotal = len(nodes)
		for _, n := range nodes {
			ep := normalizeEndpoint(n.Endpoint)
			if selfEndpoint != "" && ep == selfEndpoint {
				includedSelf = true
				continue
			}
			consensusEndpoints = append(consensusEndpoints, n.Endpoint)
		}
	}

	selfVersion := version.Version.Version
	versionCounts := make(map[string]int)
	var versions []string
	versionSet := make(map[string]bool)

	// Always include ourselves if we're in the consensus set
	if includedSelf || selfEndpoint == "" {
		versionCounts[selfVersion]++
		versionSet[selfVersion] = true
		versions = append(versions, selfVersion)
	}

	// Get peer health from our own GetStatus to check which peers are healthy
	peerHealthVersions := make(map[string]string) // normalized endpoint -> version
	if con.core != nil {
		statusResp, err := con.core.GetStatus(ctx, &connect.Request[v1.GetStatusRequest]{})
		if err == nil && statusResp.Msg != nil && statusResp.Msg.Peers != nil {
			// Build map of healthy peers with their endpoints
			for _, peer := range statusResp.Msg.Peers.Peers {
				if peer.ConnectrpcHealthy && peer.Endpoint != "" {
					// Peer is healthy via connectRPC - we can try getting version from it
					peerHealthVersions[normalizeEndpoint(peer.Endpoint)] = ""
				}
			}
		}
	}

	// Fetch versions concurrently
	type versionResult struct {
		version string
	}
	results := make([]versionResult, len(consensusEndpoints))
	var wg sync.WaitGroup
	client := &http.Client{Timeout: 5 * time.Second}

	for i, ep := range consensusEndpoints {
		wg.Add(1)
		go func(idx int, endpoint string) {
			defer wg.Done()
			results[idx] = versionResult{version: fetchNodeVersion(ctx, client, endpoint)}
		}(i, ep)
	}
	wg.Wait()

	for _, r := range results {
		v := r.version
		if v == "" {
			v = "unknown"
		}
		versionCounts[v]++
		if !versionSet[v] {
			versionSet[v] = true
			versions = append(versions, v)
		}
	}

	// Determine latest version via semver
	sort.Slice(versions, func(i, j int) bool {
		vi, vj := versions[i], versions[j]
		if vi == "unknown" {
			return false
		}
		if vj == "unknown" {
			return true
		}
		if !strings.HasPrefix(vi, "v") {
			vi = "v" + vi
		}
		if !strings.HasPrefix(vj, "v") {
			vj = "v" + vj
		}
		return semver.Compare(vi, vj) > 0
	})

	latestVersion := ""
	if len(versions) > 0 && versions[0] != "unknown" {
		latestVersion = versions[0]
	} else if len(versions) > 1 {
		latestVersion = versions[1]
	}

	total := consensusTotal
	onLatest := 0
	if latestVersion != "" {
		onLatest = versionCounts[latestVersion]
	}
	percentOnLatest := 0
	if total > 0 {
		percentOnLatest = (onLatest * 100) / total
	}

	segments := make([]map[string]interface{}, 0, len(versions))
	for _, v := range versions {
		segments = append(segments, map[string]interface{}{
			"version": v,
			"count":   versionCounts[v],
			"latest":  v == latestVersion,
		})
	}

	return c.JSON(200, map[string]interface{}{
		"selfVersion":     selfVersion,
		"latestVersion":   latestVersion,
		"totalNodes":      total,
		"onLatest":        onLatest,
		"percentOnLatest": percentOnLatest,
		"versionCounts":   versionCounts,
		"segments":        segments,
	})
}

func normalizeEndpoint(ep string) string {
	ep = strings.TrimSuffix(strings.ToLower(ep), "/")
	if ep != "" && !strings.HasPrefix(ep, "http") {
		ep = "https://" + ep
	}
	return strings.TrimSuffix(ep, "/")
}

func fetchNodeVersion(ctx context.Context, client *http.Client, endpoint string) string {
	baseURL := endpoint
	if !strings.HasPrefix(baseURL, "http") {
		baseURL = "https://" + baseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}

	// Try /health-check first
	u.Path = "/health-check"
	u.RawQuery = ""

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	var data map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return ""
	}

	// Try storage.version
	if storage, ok := data["storage"].(map[string]interface{}); ok {
		if s, ok := storage["version"].(string); ok && s != "" {
			return s
		}
	}
	// Try data.version
	if d, ok := data["data"].(map[string]interface{}); ok {
		if s, ok := d["version"].(string); ok && s != "" {
			return s
		}
	}
	// Try version directly
	if s, ok := data["version"].(string); ok && s != "" {
		return s
	}
	// Try git field as fallback
	if s, ok := data["git"].(string); ok && s != "" {
		return s
	}

	return ""
}
