package mirror

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"github.com/OpenAudio/go-openaudio/pkg/api/core/v1/v1connect"
)

// CometSource reads blocks through a node's CometBFT RPC, served at
// <node>/core/crpc. It uses the CometBFT `block` call rather than core's
// GetBlock because GetBlock re-sorts a block's transactions (writes before
// plays, creates first) for display, and the mirror needs the order the chain
// actually applied.
//
// /core/crpc is only served by nodes with OPENAUDIO_STATE_SYNC_SERVE_SNAPSHOTS,
// and only for an allowlist of methods that includes status and block.
type CometSource struct {
	base   string
	client *http.Client
}

func NewCometSource(nodeURL string, client *http.Client) *CometSource {
	return &CometSource{base: strings.TrimSuffix(nodeURL, "/") + "/core/crpc", client: client}
}

type cometStatus struct {
	NodeInfo struct {
		Network string `json:"network"`
	} `json:"node_info"`
	SyncInfo struct {
		LatestBlockHeight string `json:"latest_block_height"`
	} `json:"sync_info"`
}

type cometBlock struct {
	Block struct {
		Header struct {
			ChainID string `json:"chain_id"`
			Height  string `json:"height"`
		} `json:"header"`
		Data struct {
			Txs []string `json:"txs"`
		} `json:"data"`
	} `json:"block"`
}

func (s *CometSource) call(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s: %s", path, resp.Status, truncate(body))
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
			Data    string `json:"data"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("%s: decode: %w", path, err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("%s: %s %s", path, envelope.Error.Message, envelope.Error.Data)
	}
	return json.Unmarshal(envelope.Result, out)
}

func (s *CometSource) ChainID(ctx context.Context) (string, error) {
	var st cometStatus
	if err := s.call(ctx, "/status", &st); err != nil {
		return "", err
	}
	return st.NodeInfo.Network, nil
}

func (s *CometSource) Tip(ctx context.Context) (int64, error) {
	var st cometStatus
	if err := s.call(ctx, "/status", &st); err != nil {
		return 0, err
	}
	return strconv.ParseInt(st.SyncInfo.LatestBlockHeight, 10, 64)
}

func (s *CometSource) Block(ctx context.Context, height int64) (string, [][]byte, error) {
	var b cometBlock
	if err := s.call(ctx, "/block?height="+strconv.FormatInt(height, 10), &b); err != nil {
		return "", nil, err
	}
	if got := b.Block.Header.Height; got != strconv.FormatInt(height, 10) {
		return "", nil, fmt.Errorf("asked for block %d, got %q", height, got)
	}
	txs := make([][]byte, 0, len(b.Block.Data.Txs))
	for i, enc := range b.Block.Data.Txs {
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return "", nil, fmt.Errorf("block %d tx %d: %w", height, i, err)
		}
		txs = append(txs, raw)
	}
	return b.Block.Header.ChainID, txs, nil
}

// ConnectTarget submits to the new chain's validators over ConnectRPC.
//
// Every transaction goes to every validator, in the same order. A node's
// mempool is FIFO and proposals take from its front, but the gossip between
// validators is asynchronous, so a transaction submitted to one node alone can
// reach the next proposer out of order. Submitting to each validator directly
// gives every proposer the same order. Duplicates from gossip are dropped by
// the mempool, which keys on the hash.
type ConnectTarget struct {
	chainID string
	// primary answers status and committed lookups; it is the first node.
	primary v1connect.CoreServiceClient
	nodes   []v1connect.CoreServiceClient
}

func NewConnectTarget(chainID string, nodeURLs []string, client *http.Client) (*ConnectTarget, error) {
	if len(nodeURLs) == 0 {
		return nil, errors.New("no target nodes")
	}
	t := &ConnectTarget{chainID: chainID}
	for _, u := range nodeURLs {
		t.nodes = append(t.nodes, v1connect.NewCoreServiceClient(client, strings.TrimSuffix(u, "/")))
	}
	t.primary = t.nodes[0]
	return t, nil
}

func (t *ConnectTarget) Status(ctx context.Context) (string, int64, error) {
	res, err := t.primary.GetStatus(ctx, connect.NewRequest(&corev1.GetStatusRequest{}))
	if err != nil {
		return "", 0, err
	}
	ci := res.Msg.GetChainInfo()
	return ci.GetChainId(), ci.GetCurrentHeight(), nil
}

func (t *ConnectTarget) Forward(ctx context.Context, tx *corev1.SignedTransaction) error {
	for _, n := range t.nodes {
		_, err := n.ForwardTransaction(ctx, connect.NewRequest(&corev1.ForwardTransactionRequest{
			Transaction: tx,
			ChainId:     t.chainID,
		}))
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *ConnectTarget) Committed(ctx context.Context, hash string) (int64, bool, error) {
	res, err := t.primary.GetTransaction(ctx, connect.NewRequest(&corev1.GetTransactionRequest{TxHash: hash}))
	if err == nil {
		return res.Msg.GetTransaction().GetHeight(), true, nil
	}
	// Core returns its database error verbatim for an unknown hash rather
	// than a NotFound code.
	if connect.CodeOf(err) == connect.CodeNotFound || strings.Contains(err.Error(), "no rows in result set") {
		return 0, false, nil
	}
	return 0, false, err
}

// classifyForwardErr sorts a ForwardTransaction failure:
//   - ErrRefused: core's admission validation said no (InvalidArgument). It may
//     only be waiting on a transaction still in flight.
//   - ErrFatal: the request itself is wrong, e.g. a chain ID mismatch
//     (FailedPrecondition) or a transaction type the target refuses outright.
//   - anything else is transient (mempool full, node not ready, network) and
//     the mirror retries it with backoff.
func classifyForwardErr(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		return ErrRefused
	case connect.CodeFailedPrecondition, connect.CodeUnimplemented:
		return ErrFatal
	}
	return nil
}

// FileCursor keeps the cursor in a JSON file, replaced atomically.
type FileCursor struct{ Path string }

func (f FileCursor) Load() (Position, bool, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Position{}, false, nil
	}
	if err != nil {
		return Position{}, false, err
	}
	var p Position
	if err := json.Unmarshal(b, &p); err != nil {
		return Position{}, false, fmt.Errorf("parse %s: %w", f.Path, err)
	}
	if p.Height <= 0 || p.Index < 0 {
		return Position{}, false, fmt.Errorf("invalid cursor in %s: %+v", f.Path, p)
	}
	return p, true, nil
}

func (f FileCursor) Save(p Position) error {
	b, err := json.Marshal(struct {
		Position
		SavedAt time.Time `json:"saved_at"`
	}{p, time.Now().UTC()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

func truncate(b []byte) string {
	const n = 300
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
