package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"testing"
	"time"

	"github.com/OpenAudio/go-openaudio/pkg/mediorum/crudr"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

func TestPruneOps(t *testing.T) {
	ss := testNetwork[0]
	host := fmt.Sprintf("prune-test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		require.NoError(t, ss.crud.DB.Where("host = ?", host).Delete(&crudr.Op{}).Error)
	})

	mkOp := func(at time.Time) *crudr.Op {
		id, err := ulid.New(ulid.Timestamp(at), rand.Reader)
		require.NoError(t, err)
		return &crudr.Op{ULID: id.String(), Host: host, Action: "update", Table: "uploads", Data: []byte("[]")}
	}

	now := time.Now()
	for _, age := range []time.Duration{-2 * time.Hour, -24 * time.Hour, -90 * 24 * time.Hour, -time.Minute, 0} {
		require.NoError(t, ss.crud.DB.Create(mkOp(now.Add(age))).Error)
	}

	// retention of 1h prunes the three older ops, keeps the two within the window
	deleted, err := ss.pruneOps(context.Background(), time.Hour)
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, int64(3))

	var remaining int64
	require.NoError(t, ss.crud.DB.Model(&crudr.Op{}).Where("host = ?", host).Count(&remaining).Error)
	require.Equal(t, int64(2), remaining)
}
