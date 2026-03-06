package console

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
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

	nodes, err := con.db.GetAllRegisteredNodesIncludingJailed(ctx)
	if err != nil {
		return err
	}

	validatorCount := 0
	for _, n := range nodes {
		if !n.Jailed {
			validatorCount++
		}
	}

	ethCount := 0
	if resp, err := con.eth.GetRegisteredEndpoints(ctx, connect.NewRequest(&ethv1.GetRegisteredEndpointsRequest{})); err == nil && resp.Msg != nil {
		ethCount = len(resp.Msg.Endpoints)
	}

	return con.views.RenderNodesView(c, &pages.NodesView{
		Nodes:               nodes,
		CoreValidatorsCount: len(nodes),
		ValidatorNodesCount: validatorCount,
		EthEndpointsCount:   ethCount,
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

// versionAdoptionAPI fetches /health-check from each validator and returns version adoption stats.
func (con *Console) versionAdoptionAPI(c echo.Context) error {
	ctx := c.Request().Context()

	nodes, err := con.db.GetAllRegisteredNodesIncludingJailed(ctx)
	if err != nil {
		return c.JSON(500, map[string]string{"error": err.Error()})
	}

	selfVersion := version.Version.Version
	versionCounts := make(map[string]int)
	versionCounts[selfVersion]++ // Always include ourselves
	var versions []string
	versionSet := make(map[string]bool)
	versionSet[selfVersion] = true
	versions = append(versions, selfVersion)

	selfEndpoint := strings.TrimSuffix(strings.ToLower(con.config.NodeEndpoint), "/")
	if selfEndpoint != "" && !strings.HasPrefix(selfEndpoint, "http") {
		selfEndpoint = "https://" + selfEndpoint
	}
	selfEndpoint = strings.TrimSuffix(selfEndpoint, "/")

	client := &http.Client{Timeout: 5 * time.Second}
	for _, n := range nodes {
		ep := strings.TrimSuffix(strings.ToLower(n.Endpoint), "/")
		epNorm := ep
		if !strings.HasPrefix(epNorm, "http") {
			epNorm = "https://" + epNorm
		}
		epNorm = strings.TrimSuffix(epNorm, "/")
		if selfEndpoint != "" && epNorm == selfEndpoint {
			continue // Already counted ourselves
		}
		baseURL := ep
		if !strings.HasPrefix(baseURL, "http") {
			baseURL = "https://" + baseURL
		}
		u, _ := url.Parse(baseURL)
		u.Path = "/health-check"
		u.RawQuery = ""

		req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		var data map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		var v string
		if storage, ok := data["storage"].(map[string]interface{}); ok {
			if s, ok := storage["version"].(string); ok && s != "" {
				v = s
			}
		}
		if v == "" {
			if d, ok := data["data"].(map[string]interface{}); ok {
				if s, ok := d["version"].(string); ok && s != "" {
					v = s
				}
			}
		}
		if v == "" {
			v = "unknown"
		}

		versionCounts[v]++
		if !versionSet[v] {
			versionSet[v] = true
			versions = append(versions, v)
		}
	}

	// Determine latest version via semver (canonicalize with "v" prefix for comparison)
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

	total := 0
	for _, c := range versionCounts {
		total += c
	}
	onLatest := 0
	if latestVersion != "" {
		onLatest = versionCounts[latestVersion]
	}
	percentOnLatest := 0
	if total > 0 {
		percentOnLatest = (onLatest * 100) / total
	}

	// Build segments for stacked bar: version -> count, sorted with latest first
	segments := make([]map[string]interface{}, 0, len(versions))
	for _, v := range versions {
		segments = append(segments, map[string]interface{}{
			"version": v,
			"count":   versionCounts[v],
			"latest":  v == latestVersion,
		})
	}

	return c.JSON(200, map[string]interface{}{
		"selfVersion":      selfVersion,
		"latestVersion":    latestVersion,
		"totalNodes":       total,
		"onLatest":         onLatest,
		"percentOnLatest":  percentOnLatest,
		"versionCounts":    versionCounts,
		"segments":         segments,
	})
}
