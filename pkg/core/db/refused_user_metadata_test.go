package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestRefusedUserMetadataMigration(t *testing.T) {
	url := os.Getenv("TEST_DB_URL")
	if url == "" {
		t.Skip("TEST_DB_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	defer conn.Close(ctx)
	schema := fmt.Sprintf("refused_user_metadata_%d", time.Now().UnixNano())
	_, err = conn.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	defer conn.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
	_, err = conn.Exec(ctx, "SET search_path TO "+schema)
	require.NoError(t, err)
	execSQL := func(sql string) { t.Helper(); _, err := conn.Exec(ctx, sql); require.NoError(t, err) }

	execSQL(`CREATE TABLE core_auth_users (user_id bigint PRIMARY KEY, wallet text NOT NULL, handle_lc text, is_deactivated boolean NOT NULL DEFAULT false)`)
	migration, err := migrationsFS.ReadFile("sql/migrations/00041_refused_user_metadata.sql")
	require.NoError(t, err)
	up, _, _ := strings.Cut(string(migration), "-- +migrate Down")

	handles := func() map[int64]string {
		t.Helper()
		rows, err := conn.Query(ctx, "SELECT user_id, coalesce(handle_lc, '') FROM core_auth_users")
		require.NoError(t, err)
		defer rows.Close()
		out := map[int64]string{}
		for rows.Next() {
			var id int64
			var h string
			require.NoError(t, rows.Scan(&id, &h))
			out[id] = h
		}
		require.NoError(t, rows.Err())
		return out
	}

	t.Run("repairs the two rows", func(t *testing.T) {
		execSQL(`TRUNCATE core_auth_users`)
		execSQL(`INSERT INTO core_auth_users VALUES
			(243004782, '0x01674a90568b9df4ea42cb8030801fb45db47986', 'not a handle@x', false),
			(990000010, '0xabc', 'i64probe-update', false),
			(28184188, '0xdef', 'someone', false)`)
		execSQL("BEGIN;" + up + "COMMIT;")
		require.Equal(t, map[int64]string{990000010: "i64probemtorsuilx", 28184188: "someone"}, handles())
	})

	t.Run("leaves rows that only partly match", func(t *testing.T) {
		execSQL(`TRUNCATE core_auth_users`)
		execSQL(`INSERT INTO core_auth_users VALUES
			(243004782, '0x01674a90568b9df4ea42cb8030801fb45db47986', 'valid_handle', false),
			(990000010, '0xabc', 'something_else', false)`)
		execSQL("BEGIN;" + up + "COMMIT;")
		require.Equal(t, map[int64]string{243004782: "valid_handle", 990000010: "something_else"}, handles())
	})
}
