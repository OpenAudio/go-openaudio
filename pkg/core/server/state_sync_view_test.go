package server

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/core/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func setupAppStateTestDB(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()

	dbURL := os.Getenv("TEST_DB_URL")
	if dbURL == "" {
		t.Skip("TEST_DB_URL not set, skipping database tests")
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	require.NoError(t, err)

	_, err = pool.Exec(context.Background(), `
		CREATE TABLE IF NOT EXISTS core_app_state(
			block_height bigint not null,
			app_hash bytea not null,
			created_at timestamp default current_timestamp,
			primary key (block_height, app_hash)
		);
		TRUNCATE core_app_state;
	`)
	require.NoError(t, err)

	t.Cleanup(func() {
		pool.Exec(context.Background(), "DROP TABLE IF EXISTS core_app_state")
		pool.Close()
	})

	return pool, dbURL
}

func insertAppState(t *testing.T, pool *pgxpool.Pool, height int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		"INSERT INTO core_app_state (block_height, app_hash) VALUES ($1, $2)",
		height, []byte{byte(height)})
	require.NoError(t, err)
}

// dumpedAppStateHeights restores core_app_state's data from a pg_dump file as
// text and returns the block heights it contains.
func dumpedAppStateHeights(t *testing.T, dumpPath string) []string {
	t.Helper()
	out, err := exec.Command("pg_restore", "--data-only", "-t", "core_app_state", "-f", "-", dumpPath).CombinedOutput()
	require.NoError(t, err, string(out))

	var heights []string
	inCopy := false
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "COPY public.core_app_state"):
			inCopy = true
		case inCopy && line == `\.`:
			inCopy = false
		case inCopy:
			heights = append(heights, strings.SplitN(line, "\t", 2)[0])
		}
	}
	return heights
}

// A block that commits after the view is opened must not appear in the dump:
// that is what made a snapshot labeled 33,000,000 restore to 33,014,102.
func TestSnapshotViewPinsDumpToLabeledHeight(t *testing.T) {
	pool, dbURL := setupAppStateTestDB(t)
	insertAppState(t, pool, 99)
	insertAppState(t, pool, 100)

	view, err := openSnapshotView(context.Background(), dbURL)
	require.NoError(t, err)
	defer view.Close()

	require.Equal(t, int64(100), view.Height)
	require.Equal(t, []byte{100}, view.AppHash)
	require.NotEmpty(t, view.SnapshotID)

	// The next block commits while the snapshot is being taken.
	insertAppState(t, pool, 101)

	s := &Server{config: &config.Config{PSQLConn: dbURL}}
	dir := t.TempDir()
	require.NoError(t, s.createPgDump(zap.NewNop(), dir, view.SnapshotID))
	view.Close()

	require.Equal(t, []string{"99", "100"}, dumpedAppStateHeights(t, getPgDumpPath(dir)))
}

func TestSnapshotViewRequiresAppState(t *testing.T) {
	_, dbURL := setupAppStateTestDB(t)

	_, err := openSnapshotView(context.Background(), dbURL)
	require.ErrorContains(t, err, "read latest app state")
}

func TestSnapshotViewCloseIsIdempotent(t *testing.T) {
	pool, dbURL := setupAppStateTestDB(t)
	insertAppState(t, pool, 1)

	view, err := openSnapshotView(context.Background(), dbURL)
	require.NoError(t, err)

	view.Close()
	view.Close()
}
