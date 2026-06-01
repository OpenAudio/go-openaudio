package crudr

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/lifecycle"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestDoSweepAdvancesCursorToLastScannedHeader(t *testing.T) {
	db := SetupTestDB()
	z := zap.NewNop()
	c := New("https://self.example", nil, nil, db, lifecycle.NewLifecycle(context.Background(), "crudr client test", z), z, nil)
	require.NoError(t, db.Exec("TRUNCATE ops, cursors").Error)

	lastScannedULID := "01KT26A1XRE7JYJ3FQBTT4C3CX"
	peerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/crud/sweep", r.URL.Path)
		w.Header().Set(SweepLastScannedULIDHeader, lastScannedULID)
		w.Header().Set(SweepLimitedHeader, "true")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "[]")
	}))
	t.Cleanup(peerServer.Close)

	peer := NewPeerClient(peerServer.URL, c, "https://self.example")
	require.NoError(t, peer.doSweep(context.Background()))
	require.False(t, peer.Seeded)

	var cursor Cursor
	require.NoError(t, db.Where("host = ?", peer.Host).First(&cursor).Error)
	require.Equal(t, lastScannedULID, cursor.LastULID)
}
