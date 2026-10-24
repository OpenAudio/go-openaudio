package mirror

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
)

const usage = `Usage: openaudio mirror -source <old-chain node> -target <new-chain node>[,<node>...] [flags]

Replays the old chain's ManageEntity and Plays transactions onto the new chain,
in block order, exactly once. Run it in its own container next to a node:

  docker run -d --name mirror -v /path/to/mirror:/data/mirror <image> mirror \
    -source https://creatornode.audius.co \
    -target https://audius.rickyrombo.com,https://v.audius.rickyrombo.com \
    -start-height 34654625

The source must serve /core/crpc (a state sync RPC node). List every new-chain
validator as a target: each gets every transaction, in the same order.

On a refused write the mirror halts and keeps serving its status. Investigate,
then restart the container to retry from the saved cursor.

Flags:
`

// Main runs the mirror command and returns the process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("mirror", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}
	source := fs.String("source", "", "old-chain node URL; its CometBFT RPC is read at <url>/core/crpc")
	targets := fs.String("target", "", "comma-separated new-chain validator URLs; the first answers status and lookups")
	sourceChain := fs.String("source-chain-id", "audius-mainnet-alpha-beta", "chain ID the source must serve")
	targetChain := fs.String("target-chain-id", "audius-mainnet-beta", "chain ID the target must serve, stamped on every forward")
	startHeight := fs.Int64("start-height", 0, "first source block to mirror, used only when no cursor is saved")
	stateFile := fs.String("state-file", "/data/mirror/cursor.json", "where the cursor is saved")
	statusAddr := fs.String("status-addr", ":8091", "address for the GET /status endpoint; empty disables it")
	maxInflight := fs.Int("max-inflight", 500, "transactions submitted but not yet committed")
	blocksInFlight := fs.Int("blocks-in-flight", 1, "source blocks that may await commit at once; 1 follows the source block by block, higher packs several into each target block to work through a backlog")
	commitTimeout := fs.Int64("commit-timeout-blocks", 12, "target blocks to wait for a commit before resubmitting once")
	startupWait := fs.Int64("startup-wait-blocks", 12, "target blocks to wait on startup so a previous run's transactions settle")
	poll := fs.Duration("poll-interval", time.Second, "wait between rounds when there is nothing to do")
	dryRun := fs.Int64("dry-run-blocks", 0, "read this many source blocks from -start-height, report what would be mirrored, submit nothing, and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *source == "" || *targets == "" {
		fs.Usage()
		return 2
	}

	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: %v\n", err)
		return 1
	}
	defer logger.Sync() //nolint:errcheck

	httpClient := &http.Client{Timeout: 30 * time.Second}
	target, err := NewConnectTarget(*targetChain, splitList(*targets), httpClient)
	if err != nil {
		logger.Error("target", zap.Error(err))
		return 2
	}

	m := New(Config{
		SourceChainID:       *sourceChain,
		TargetChainID:       *targetChain,
		StartHeight:         *startHeight,
		MaxInflight:         *maxInflight,
		BlocksInFlight:      *blocksInFlight,
		CommitTimeoutBlocks: *commitTimeout,
		StartupWaitBlocks:   *startupWait,
		PollInterval:        *poll,
	}, NewCometSource(*source, httpClient), target, FileCursor{Path: *stateFile}, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if *dryRun > 0 {
		if *startHeight <= 0 {
			logger.Error("-dry-run-blocks needs -start-height")
			return 2
		}
		r, err := DryRun(ctx, NewCometSource(*source, httpClient), target, *sourceChain, *startHeight, *dryRun)
		if r != nil {
			out, _ := json.MarshalIndent(r, "", "  ")
			fmt.Println(string(out))
		}
		if err != nil {
			logger.Error("dry run failed", zap.Error(err))
			return 1
		}
		if len(r.HashMismatches) > 0 {
			return 1
		}
		return 0
	}

	if *statusAddr != "" {
		srv := &http.Server{Addr: *statusAddr, Handler: statusHandler(m), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("status server", zap.Error(err))
			}
		}()
		defer srv.Close()
	}

	if err := m.Init(ctx); err != nil {
		logger.Error("mirror init failed", zap.Error(err))
		return 1
	}
	logger.Info("mirror starting",
		zap.String("source", *source), zap.Strings("targets", splitList(*targets)),
		zap.String("source_chain_id", *sourceChain), zap.String("target_chain_id", *targetChain))

	err = m.Run(ctx)
	var halt *HaltError
	if errors.As(err, &halt) {
		// Stay up so the status endpoint keeps reporting why, until the
		// operator stops the container.
		<-ctx.Done()
		return 1
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("mirror stopped", zap.Error(err))
		return 1
	}
	return 0
}

func statusHandler(m *Mirror) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		st := m.Status()
		w.Header().Set("Content-Type", "application/json")
		if st.Halted {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		_ = json.NewEncoder(w).Encode(st)
	})
	return mux
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
