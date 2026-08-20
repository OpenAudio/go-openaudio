// Command waveform serves a small page that renders a validator node's
// precomputed waveform peaks in wavesurfer.
//
// The waveform JSON is proxied through this process so the browser sees a
// single origin, which keeps the self-signed devnet certificate and any
// cross-origin rules out of the way.
//
// Audio is deliberately not proxied. Serving it requires a signed request
// (/content and /tracks/cidstream both refuse an unsigned GET), whereas the
// waveform does not -- which is rather the point: a client can draw the
// waveform long before it is in a position to stream anything. To compare
// against ground truth, the page takes the source file from local disk.
//
//	go run ./examples/waveform
//	go run ./examples/waveform --node https://node2.oap.devnet --addr :9000
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	_ "embed"
)

//go:embed index.html
var indexHTML []byte

func main() {
	node := flag.String("node", "https://node1.oap.devnet", "validator node to read waveforms and audio from")
	addr := flag.String("addr", ":8777", "address to listen on")
	insecure := flag.Bool("insecure", true, "skip TLS verification (devnet uses a self-signed cert)")
	flag.Parse()

	base := strings.TrimRight(*node, "/")
	if _, err := url.Parse(base); err != nil {
		log.Fatalf("invalid --node %q: %v", *node, err)
	}

	client := &http.Client{
		Timeout: 5 * time.Minute, // audio can be tens of MB
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: *insecure},
		},
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	// Upstream status codes are meaningful to the page -- 202 means the node
	// queued analysis, 403 means the cid is delisted, 404 means no waveform
	// here -- so pass them through rather than collapsing them into an error.
	proxy := func(prefix, upstream string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			cid := strings.TrimPrefix(r.URL.Path, prefix)
			if cid == "" || strings.Contains(cid, "/") {
				http.Error(w, "expected a single cid path segment", http.StatusBadRequest)
				return
			}

			req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, base+upstream+url.PathEscape(cid), nil)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			// Forward Range so seeking in the audio element works.
			if rng := r.Header.Get("Range"); rng != "" {
				req.Header.Set("Range", rng)
			}

			resp, err := client.Do(req)
			if err != nil {
				http.Error(w, fmt.Sprintf("upstream %s: %v", base, err), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()

			for _, h := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Cache-Control"} {
				if v := resp.Header.Get(h); v != "" {
					w.Header().Set(h, v)
				}
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
		}
	}

	mux.HandleFunc("/api/waveform/", proxy("/api/waveform/", "/waveform/"))
	mux.HandleFunc("/api/upload/", proxy("/api/upload/", "/uploads/"))

	// Uploading from the page is what makes the comparison trustworthy: both
	// panels then come from one file, instead of whatever the operator happened
	// to have on disk.
	//
	// The multipart body is forwarded verbatim -- re-encoding it here would mean
	// parsing and rebuilding the boundary for no gain.
	mux.HandleFunc("/api/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base+"/uploads", r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
		req.ContentLength = r.ContentLength

		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, fmt.Sprintf("upstream %s: %v", base, err), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	})

	log.Printf("waveform viewer on http://localhost%s (node %s)", *addr, base)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
