package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNextWatchdogInterval(t *testing.T) {
	cases := []struct {
		name string
		prev time.Duration
		want time.Duration
	}{
		{"first growth", 1 * time.Hour, 2 * time.Hour},
		{"mid growth", 5 * time.Hour, 6 * time.Hour},
		{"one before cap", 23 * time.Hour, 24 * time.Hour},
		{"at cap stays at cap", 24 * time.Hour, 24 * time.Hour},
		{"above cap stays at cap", 48 * time.Hour, 24 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, nextWatchdogInterval(tc.prev))
		})
	}
}
