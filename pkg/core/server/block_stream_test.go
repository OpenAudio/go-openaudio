package server

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	v1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
)

// drives runIndexerStream with fake emit callbacks that record the emitted
// sequence, feeds the given live blocks, then cancels to end the stream.
func collectIndexerStream(t *testing.T, startHeight, head int64, liveHeights []int64) []string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())

	var got []string
	emitHeight := func(h int64) error { got = append(got, fmt.Sprintf("db:%d", h)); return nil }
	emitLive := func(b *v1.Block) error { got = append(got, fmt.Sprintf("live:%d", b.Height)); return nil }

	live := make(chan *v1.Block, len(liveHeights))
	for _, h := range liveHeights {
		live <- &v1.Block{Height: h}
	}

	done := make(chan error, 1)
	go func() { done <- runIndexerStream(ctx, startHeight, head, live, emitHeight, emitLive) }()

	// Let catch-up + buffered live blocks drain, then cancel to return.
	time.Sleep(50 * time.Millisecond)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("runIndexerStream returned %v, want context.Canceled", err)
	}
	return got
}

func TestRunIndexerStream_CatchUpOnly(t *testing.T) {
	// head ahead of start, no live blocks: replay 5..8 from the store.
	got := collectIndexerStream(t, 5, 8, nil)
	want := []string{"db:5", "db:6", "db:7", "db:8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRunIndexerStream_LiveContiguous(t *testing.T) {
	// Already caught up (head < start): live blocks forward as-is, no gap-fill.
	got := collectIndexerStream(t, 5, 4, []int64{5, 6, 7})
	want := []string{"live:5", "live:6", "live:7"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRunIndexerStream_GapFill(t *testing.T) {
	// Catch up 5, then a live block jumps to 8 (6,7 dropped by pubsub):
	// gap-fill 6,7 from the store before forwarding live 8.
	got := collectIndexerStream(t, 5, 5, []int64{8})
	want := []string{"db:5", "db:6", "db:7", "live:8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRunIndexerStream_Dedup(t *testing.T) {
	// Catch up 5..7, then a live block for 6 (already sent) is skipped; 8 forwards.
	got := collectIndexerStream(t, 5, 7, []int64{6, 8})
	want := []string{"db:5", "db:6", "db:7", "live:8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRunIndexerStream_EmitError(t *testing.T) {
	// A store read failure during catch-up aborts the stream with that error.
	ctx := context.Background()
	boom := fmt.Errorf("boom")
	emitHeight := func(h int64) error {
		if h == 6 {
			return boom
		}
		return nil
	}
	emitLive := func(b *v1.Block) error { return nil }
	err := runIndexerStream(ctx, 5, 8, nil, emitHeight, emitLive)
	if err != boom {
		t.Fatalf("got %v, want boom", err)
	}
}
