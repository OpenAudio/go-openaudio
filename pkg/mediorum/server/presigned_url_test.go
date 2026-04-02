package server

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestPresignedURLExpiry(t *testing.T) {
	t.Run("unknown duration returns default 2h", func(t *testing.T) {
		assert.Equal(t, 2*time.Hour, presignedURLExpiry(0))
	})

	t.Run("negative duration returns default 2h", func(t *testing.T) {
		assert.Equal(t, 2*time.Hour, presignedURLExpiry(-1))
	})

	t.Run("very short track uses minimum 5m floor", func(t *testing.T) {
		assert.Equal(t, 5*time.Minute, presignedURLExpiry(30))
	})

	t.Run("3 minute track hits minimum floor", func(t *testing.T) {
		result := presignedURLExpiry(180)
		// 180s * 1.1 = 198s = 3m18s, which is below 5m floor
		assert.Equal(t, 5*time.Minute, result)
	})

	t.Run("10 minute track gets 10% buffer", func(t *testing.T) {
		result := presignedURLExpiry(600)
		// 600s * 1.1 = 660s = 11m
		assert.Equal(t, 660*time.Second, result)
	})

	t.Run("1 hour track gets 10% buffer", func(t *testing.T) {
		result := presignedURLExpiry(3600)
		// 3600s * 1.1 = 3960s = 1h6m
		assert.Equal(t, 3960*time.Second, result)
	})

	t.Run("3 hour DJ mix gets 10% buffer", func(t *testing.T) {
		result := presignedURLExpiry(10800)
		// 10800s * 1.1 = 11880s = 3h18m
		assert.Equal(t, 11880*time.Second, result)
	})
}

func TestPresignedURLExpiryFloor(t *testing.T) {
	// Any track under ~272 seconds (5 min / 1.1) should be clamped to the floor
	result := presignedURLExpiry(100) // 100s * 1.1 = 110s = 1m50s < 5m floor
	assert.Equal(t, 5*time.Minute, result)

	// Just above the floor threshold
	result = presignedURLExpiry(300) // 300s * 1.1 = 330s = 5m30s > 5m floor
	assert.Greater(t, result, 5*time.Minute)
}

