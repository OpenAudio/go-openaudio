package console

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	ethv1 "github.com/OpenAudio/go-openaudio/pkg/api/eth/v1"
	"github.com/OpenAudio/go-openaudio/pkg/core/console/views/pages"
	"github.com/OpenAudio/go-openaudio/pkg/core/db"
	"github.com/labstack/echo/v4"
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
