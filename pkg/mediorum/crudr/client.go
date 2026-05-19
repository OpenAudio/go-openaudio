package crudr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/httputil"
	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server/signature"
	"github.com/oklog/ulid/v2"
	"golang.org/x/exp/slog"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PeerClient struct {
	Host     string
	Seeded   bool
	outbox   chan []byte
	crudr    *Crudr
	logger   *slog.Logger
	selfHost string
	cancel   context.CancelFunc
}

func NewPeerClient(host string, crudr *Crudr, selfHost string) *PeerClient {
	// buffer up to N outgoing messages
	// if full, Send will drop outgoing message
	// which is okay because of sweep
	outboxBufferSize := 8

	return &PeerClient{
		Host:     httputil.RemoveTrailingSlash(strings.ToLower(host)),
		outbox:   make(chan []byte, outboxBufferSize),
		crudr:    crudr,
		logger:   slog.With("crudr_client", httputil.RemoveTrailingSlash(strings.ToLower(host))),
		selfHost: selfHost,
	}
}

func (p *PeerClient) Start(lc *lifecycle.Lifecycle) {
	lc.AddManagedRoutine(fmt.Sprintf("crudr peer %s", p.Host), func(ctx context.Context) error {
		ctx, cancel := context.WithCancel(ctx)
		p.cancel = cancel
		go p.startSweeper(ctx)
		return p.startSender(ctx)
	})
}

func (p *PeerClient) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
}

func (p *PeerClient) Send(data []byte) bool {
	select {
	case p.outbox <- data:
		return true
	default:
		p.logger.Debug("outbox full, dropping message", "msg", string(data), "len", len(p.outbox), "cap", cap(p.outbox))
		return false
	}
}

func (p *PeerClient) startSender(ctx context.Context) error {
	for {
		select {
		case data, ok := <-p.outbox:
			if !ok {
				return nil // channel closed
			}
			endpoint := p.Host + "/internal/crud/push" // hardcoded
			req, err := signature.SignedPost(
				ctx,
				endpoint,
				"application/json",
				bytes.NewReader(data),
				p.crudr.myPrivateKey,
				p.selfHost,
			)
			if err != nil {
				p.logger.Debug("could not create req client", "host", p.Host, "err", err)
				continue
			}

			resp, err := p.crudr.httpClient.Do(req)
			if err != nil {
				p.logger.Debug("push failed", "host", p.Host, "err", err)
				continue
			}

			if resp.StatusCode != 200 {
				p.logger.Debug("push bad status", "host", p.Host, "status", resp.StatusCode)
			}

			resp.Body.Close()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *PeerClient) startSweeper(ctx context.Context) error {
	ticker := time.NewTicker(1 * time.Second) // do first sweep immediately
	for {
		select {
		case <-ticker.C:
			ticker.Reset(10 * time.Minute) // do subsequent sweeps every 10 min
			err := p.doSweep(ctx)
			if err != nil {
				p.logger.Warn("sweep failed", "err", err)
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *PeerClient) doSweep(ctx context.Context) error {

	host := p.Host
	bulkEndpoint := "/internal/crud/sweep" // hardcoded

	// get cursor
	lastUlid := ""
	{
		var cursor Cursor
		err := p.crudr.DB.Where("host = ?", host).First(&cursor).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				p.logger.Warn("failed to get cursor", "err", err)
			}
		} else {
			lastUlid = cursor.LastULID
		}
	}

	endpoint := host + bulkEndpoint + "?after=" + lastUlid

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("User-Agent", "mediorum "+p.selfHost)

	resp, err := p.crudr.httpClient.Do(req)
	if err != nil {
		p.Seeded = true // we can't reach this peer, so we're not able to seed any further
		return fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		p.Seeded = true // we can't reach this peer, so we're not able to seed any further
		return fmt.Errorf("bad status: %d", resp.StatusCode)
	}

	// Retention-gap signal: the peer has dropped ops below our cursor and
	// is advertising the lowest ulid it still has. Surface the gap, then
	// explicitly advance our cursor to that floor. Without this branch,
	// the old code path would silently skip the gap (Topic 7) because the
	// "ulid > after" query already returns ops above the floor and the
	// client would treat the response as a normal sweep result.
	//
	// Validate the advertised ulid before trusting it: a hostile or
	// misconfigured peer that emits a forged future ulid could otherwise
	// permanently silence this sweep stream by jumping our cursor past
	// every legitimate op. Require the ulid to parse cleanly and decode
	// to a time at or before the local wall clock (with a generous skew
	// window).
	gapMinULID := resp.Header.Get(HeaderAvailableMin)
	if resp.Header.Get(HeaderRetentionGap) == "true" && gapMinULID != "" && gapMinULID > lastUlid && isValidGapULID(gapMinULID) {
		p.logger.Warn("retention gap detected: peer cursor below peer's available history; advancing cursor across gap",
			"peer", host,
			"local_cursor", lastUlid,
			"peer_available_min_ulid", gapMinULID)
		// Persist the advanced cursor before applying any ops so a
		// crash mid-apply doesn't leave us stuck below the gap.
		// Only count the gap-advance toward the operator metric if the
		// cursor actually got persisted; otherwise the counter would
		// over-report relative to durable state.
		upsertClause := clause.OnConflict{UpdateAll: true}
		if dbErr := p.crudr.DB.Clauses(upsertClause).Create(&Cursor{Host: host, LastULID: gapMinULID}).Error; dbErr != nil {
			p.logger.Error("failed to advance cursor across retention gap", "err", dbErr)
		} else {
			lastUlid = gapMinULID
			MarkSweepGapAdvance()
		}
	} else if resp.Header.Get(HeaderRetentionGap) == "true" && gapMinULID != "" && !isValidGapULID(gapMinULID) {
		p.logger.Warn("ignoring retention-gap header with invalid ulid",
			"peer", host,
			"advertised_ulid", gapMinULID)
	}

	var ops []*Op
	dec := json.NewDecoder(resp.Body)
	err = dec.Decode(&ops)
	if err != nil {
		return err
	}

	for _, op := range ops {
		// ignore old blobs ops
		if op.Table == "blobs" {
			lastUlid = op.ULID
			continue
		}

		// cancel early from context
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := p.crudr.ApplyOp(op)
		if err != nil {
			p.logger.Error("failed to apply op", "op", op, "err", err)
		} else {
			lastUlid = op.ULID
		}
	}

	// seeding is complete once there are no more ulids to sweep (or very few left)
	if !p.Seeded && len(ops) < 10 {
		p.logger.Info("seeding complete (no more ulids to sweep)")
		p.Seeded = true
	}

	// set cursor
	{
		upsertClause := clause.OnConflict{UpdateAll: true}
		err := p.crudr.DB.Clauses(upsertClause).Create(&Cursor{Host: host, LastULID: lastUlid}).Error
		if err != nil {
			p.logger.Error("failed to set cursor", "err", err)
		}
	}

	p.logger.Debug("backfill done", "host", host, "count", len(ops), "last_ulid", lastUlid)

	// seeding is complete if the last ulid is within the last hour
	if !p.Seeded {
		parsedULID, err := ulid.Parse(lastUlid)
		if err == nil {
			t := ulid.Time(parsedULID.Time())
			since := time.Since(t)
			if since < time.Hour {
				p.logger.Debug("seeding complete (timestamp <1hr)", "last_ulid", lastUlid, "since_minutes", since.Minutes())
				p.Seeded = true
			} else {
				p.logger.Debug("seeding not complete (last ulid is too old)", "last_ulid", lastUlid, "since_minutes", since.Minutes())
			}
		} else {
			p.logger.Warn(fmt.Sprintf("failed to parse last ulid: '%s'", lastUlid), "err", err)
		}
	}

	return nil
}
