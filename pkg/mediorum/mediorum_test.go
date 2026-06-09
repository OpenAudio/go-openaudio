package mediorum

import (
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/server"
	"github.com/stretchr/testify/assert"
)

func TestParseStoreRecentTTL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want time.Duration
	}{
		{name: "unset defaults to one year", raw: "", want: server.DefaultStoreRecentTTL},
		{name: "go duration still works", raw: "48h", want: 48 * time.Hour},
		{name: "day suffix works", raw: "365d", want: 365 * 24 * time.Hour},
		{name: "fractional day suffix works", raw: "1.5d", want: 36 * time.Hour},
		{name: "invalid defaults to one year", raw: "nope", want: server.DefaultStoreRecentTTL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseStoreRecentTTL(tt.raw))
		})
	}
}
