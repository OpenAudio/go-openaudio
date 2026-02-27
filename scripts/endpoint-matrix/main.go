// Command endpoint-matrix fetches GetRegisteredEndpoints from a source node,
// then from each endpoint in that list, and prints a matrix showing which
// nodes are missing which endpoints.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

const ethGetRegisteredEndpointsPath = "/eth.v1.EthService/GetRegisteredEndpoints"

var (
	sourceURL = flag.String("source", "https://creatornode.audius.co", "Source node URL for the reference endpoint list")
	timeout   = flag.Duration("timeout", 15*time.Second, "Request timeout per node")
	verbose   = flag.Bool("verbose", false, "Print progress and errors")
	format    = flag.String("format", "text", "Output format: text or html")
	output    = flag.String("output", "endpoint-matrix.html", "Output file for html format (ignored for text)")
)

type ServiceEndpoint struct {
	ID             string `json:"id"`
	Owner          string `json:"owner"`
	Endpoint       string `json:"endpoint"`
	BlockNumber    string `json:"blockNumber"`
	DelegateWallet string `json:"delegateWallet"`
	RegisteredAt   string `json:"registeredAt"`
	ServiceType    string `json:"serviceType"`
}

type GetRegisteredEndpointsResponse struct {
	Endpoints []ServiceEndpoint `json:"endpoints"`
}

type nodeResult struct {
	baseURL string
	resp    *GetRegisteredEndpointsResponse
	err     error
}

func main() {
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	sourceEp := strings.TrimSuffix(*sourceURL, "/")
	ref, err := fetchEndpoints(ctx, sourceEp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to fetch reference from %s: %v\n", sourceEp, err)
		os.Exit(1)
	}

	refSet := make(map[string]ServiceEndpoint)
	for _, ep := range ref.Endpoints {
		refSet[normalizeEndpoint(ep.Endpoint)] = ep
	}

	refList := make([]string, 0, len(refSet))
	for k := range refSet {
		refList = append(refList, k)
	}
	sort.Strings(refList)

	fmt.Fprintf(os.Stderr, "Reference has %d endpoints from %s\n", len(refList), sourceEp)
	fmt.Fprintf(os.Stderr, "Querying %d nodes...\n", len(refList))

	// Fetch from each endpoint
	results := make([]nodeResult, 0, len(refList))
	for _, ep := range refList {
		baseURL := ep
		resp, err := fetchEndpoints(ctx, baseURL)
		results = append(results, nodeResult{baseURL: baseURL, resp: resp, err: err})
		if *verbose && err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", baseURL, err)
		}
	}

	if *format == "html" {
		writeHTML(results, refList, sourceEp, *output)
	} else {
		writeText(results, refList)
	}
}

func writeText(results []nodeResult, refList []string) {
	colWidth := 25
	fmt.Println()
	fmt.Println("## Matrix: ✓ = node includes this endpoint, ✗ = node is missing this endpoint")
	fmt.Println("## Rows = nodes we queried. Cols = endpoints from reference list.")
	fmt.Println()

	fmt.Print(strings.Repeat(" ", colWidth+2))
	for _, ep := range refList {
		short := shortenEndpoint(ep, colWidth-1)
		fmt.Printf(" %*s", colWidth, short)
	}
	fmt.Println()

	fmt.Print(strings.Repeat("-", colWidth+2))
	for range refList {
		fmt.Print(strings.Repeat("-", colWidth+1))
	}
	fmt.Println()

	for _, r := range results {
		if r.err != nil {
			fmt.Printf("%-*s", colWidth+2, shortenEndpoint(r.baseURL, colWidth)+" (err)")
			for range refList {
				fmt.Printf(" %*s", colWidth, "?")
			}
			fmt.Println()
			continue
		}

		nodeSet := make(map[string]bool)
		for _, ep := range r.resp.Endpoints {
			nodeSet[normalizeEndpoint(ep.Endpoint)] = true
		}

		fmt.Printf("%-*s", colWidth+2, shortenEndpoint(r.baseURL, colWidth))
		for _, refEp := range refList {
			if nodeSet[refEp] {
				fmt.Printf(" %*s", colWidth, "✓")
			} else {
				fmt.Printf(" %*s", colWidth, "✗")
			}
		}
		fmt.Println()
	}

	fmt.Println()
	fmt.Println("## Summary: endpoints missing from each node's view")
	fmt.Println()
	for _, r := range results {
		if r.err != nil {
			fmt.Printf("%s: ERROR %v\n", r.baseURL, r.err)
			continue
		}

		nodeSet := make(map[string]bool)
		for _, ep := range r.resp.Endpoints {
			nodeSet[normalizeEndpoint(ep.Endpoint)] = true
		}

		var missing []string
		for _, refEp := range refList {
			if !nodeSet[refEp] {
				missing = append(missing, refEp)
			}
		}

		if len(missing) == 0 {
			fmt.Printf("%s: (none - matches reference)\n", r.baseURL)
		} else {
			fmt.Printf("%s: missing %d endpoints:\n", r.baseURL, len(missing))
			for _, m := range missing {
				fmt.Printf("  - %s\n", m)
			}
		}
		fmt.Println()
	}
}

func writeHTML(results []nodeResult, refList []string, sourceEp, outPath string) {
	f := os.Stdout
	if outPath != "" && outPath != "-" {
		var err error
		f, err = os.Create(outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to create %s: %v\n", outPath, err)
			os.Exit(1)
		}
		defer f.Close()
	}

	genTime := time.Now().Format(time.RFC3339)
	sourceEsc := html.EscapeString(sourceEp)

	fmt.Fprintf(f, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Endpoint Matrix – %s</title>
<style>
:root { --bg: #0f0f12; --surface: #1a1a1f; --border: #2a2a32; --text: #e4e4e7; --text-muted: #a1a1aa; --accent: #6366f1; --ok: #22c55e; --missing: #ef4444; --error: #f59e0b; }
* { box-sizing: border-box; }
body { font-family: "JetBrains Mono", "SF Mono", ui-monospace, monospace; background: var(--bg); color: var(--text); margin: 0; padding: 1.5rem; line-height: 1.5; }
h1 { font-size: 1.25rem; font-weight: 600; margin: 0 0 0.5rem; }
.meta { color: var(--text-muted); font-size: 0.8rem; margin-bottom: 1.5rem; }
.wrapper { overflow-x: auto; margin-bottom: 2rem; }
table { border-collapse: collapse; font-size: 0.75rem; min-width: fit-content; }
th, td { padding: 0.35rem 0.5rem; border: 1px solid var(--border); text-align: center; white-space: nowrap; }
th { background: var(--surface); position: sticky; top: 0; z-index: 2; }
th.row-header { left: 0; z-index: 3; text-align: left; min-width: 200px; }
td.row-header { position: sticky; left: 0; background: var(--surface); text-align: left; font-weight: 500; z-index: 1; }
.cell-ok { background: rgba(34,197,94,0.2); color: var(--ok); }
.cell-missing { background: rgba(239,68,68,0.2); color: var(--missing); }
.cell-error { background: rgba(245,158,11,0.2); color: var(--error); }
.summary h2 { font-size: 1rem; margin: 1.5rem 0 0.5rem; }
.summary .node { margin-bottom: 1rem; }
.summary .node-name { font-weight: 600; margin-bottom: 0.25rem; }
.summary .missing-list { margin-left: 1rem; color: var(--text-muted); font-size: 0.85rem; }
</style>
</head>
<body>
<h1>Endpoint matrix</h1>
<div class="meta">Source: %s &middot; %d endpoints &middot; Generated %s</div>
<div class="wrapper">
<table>
<thead><tr><th class="row-header">Node</th>`, sourceEsc, len(refList), genTime)

	for _, ep := range refList {
		short := stripScheme(ep)
		if len(short) > 28 {
			short = short[:25] + "..."
		}
		fmt.Fprintf(f, "<th title=%q>%s</th>", html.EscapeString(ep), html.EscapeString(short))
	}
	fmt.Fprint(f, "</tr></thead><tbody>")

	for _, r := range results {
		nodeName := stripScheme(r.baseURL)
		if len(nodeName) > 45 {
			nodeName = nodeName[:42] + "..."
		}
		if r.err != nil {
			nodeName += " (err)"
		}
		fmt.Fprintf(f, "<tr><td class=\"row-header\" title=%q>%s</td>", html.EscapeString(r.baseURL), html.EscapeString(nodeName))

		if r.err != nil {
			for range refList {
				fmt.Fprint(f, `<td class="cell-error">?</td>`)
			}
		} else {
			nodeSet := make(map[string]bool)
			for _, ep := range r.resp.Endpoints {
				nodeSet[normalizeEndpoint(ep.Endpoint)] = true
			}
			for _, refEp := range refList {
				if nodeSet[refEp] {
					fmt.Fprint(f, `<td class="cell-ok">✓</td>`)
				} else {
					fmt.Fprint(f, `<td class="cell-missing">✗</td>`)
				}
			}
		}
		fmt.Fprint(f, "</tr>")
	}

	fmt.Fprint(f, "</tbody></table></div>")
	fmt.Fprint(f, `<div class="summary"><h2>Summary: endpoints missing per node</h2>`)

	for _, r := range results {
		fmt.Fprintf(f, `<div class="node"><div class="node-name">%s</div>`, html.EscapeString(r.baseURL))
		if r.err != nil {
			fmt.Fprintf(f, `<div class="missing-list">Error: %s</div>`, html.EscapeString(r.err.Error()))
		} else {
			nodeSet := make(map[string]bool)
			for _, ep := range r.resp.Endpoints {
				nodeSet[normalizeEndpoint(ep.Endpoint)] = true
			}
			var missing []string
			for _, refEp := range refList {
				if !nodeSet[refEp] {
					missing = append(missing, refEp)
				}
			}
			if len(missing) == 0 {
				fmt.Fprint(f, `<div class="missing-list">(none – matches reference)</div>`)
			} else {
				fmt.Fprintf(f, `<div class="missing-list">missing %d:`, len(missing))
				for _, m := range missing {
					fmt.Fprintf(f, "<br>• %s", html.EscapeString(m))
				}
				fmt.Fprint(f, "</div>")
			}
		}
		fmt.Fprint(f, "</div>")
	}
	fmt.Fprint(f, "</div></body></html>")
	if outPath != "" && outPath != "-" {
		fmt.Fprintf(os.Stderr, "Wrote %s\n", outPath)
	}
}

func stripScheme(ep string) string {
	ep = strings.TrimPrefix(ep, "https://")
	ep = strings.TrimPrefix(ep, "http://")
	return ep
}

func fetchEndpoints(ctx context.Context, baseURL string) (*GetRegisteredEndpointsResponse, error) {
	baseURL = strings.TrimSuffix(baseURL, "/")
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	// Support both /path and full URL
	u.Path = ethGetRegisteredEndpointsPath
	u.RawQuery = ""

	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: *timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var out GetRegisteredEndpointsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

func normalizeEndpoint(ep string) string {
	ep = strings.TrimSpace(ep)
	ep = strings.TrimSuffix(ep, "/")
	return strings.ToLower(ep)
}

func shortenEndpoint(ep string, maxLen int) string {
	ep = strings.TrimPrefix(ep, "https://")
	ep = strings.TrimPrefix(ep, "http://")
	if len(ep) <= maxLen {
		return ep
	}
	return ep[:maxLen-1] + "…"
}
