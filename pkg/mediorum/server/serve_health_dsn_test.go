package server

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearPGEnv unsets the libpq environment variables so DSN parsing doesn't pick
// up the developer's ambient Postgres settings. pgconn consults these when a
// DSN omits the host, which would otherwise make these tests machine-dependent.
func clearPGEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"PGHOST", "PGHOSTADDR", "PGPORT", "PGUSER",
		"PGDATABASE", "PGSERVICE", "PGSERVICEFILE",
	} {
		if old, ok := os.LookupEnv(key); ok {
			require.NoError(t, os.Unsetenv(key))
			t.Cleanup(func() { os.Setenv(key, old) })
		}
	}
}

// TestIsLocalHost covers the classifier directly. The host it receives comes
// from the pgx connection config, so these are the values pgx actually resolves
// a DSN to, including socket directories.
func TestIsLocalHost(t *testing.T) {
	tests := []struct {
		name string
		host string
		want bool
	}{
		{"compose alias", "db", true},
		{"localhost", "localhost", true},
		{"localhost uppercase", "LOCALHOST", true},
		{"localhost trailing dot", "localhost.", true},
		{"loopback v4", "127.0.0.1", true},
		{"loopback v4 elsewhere in the block", "127.0.1.1", true},
		{"loopback v6", "::1", true},
		{"empty host", "", true},
		{"socket dir", "/var/run/postgresql", true},
		{"socket dir macos", "/private/tmp", true},
		{"abstract socket", "@/tmp/.s.PGSQL", true},

		{"rds endpoint", "mydb.abc123.us-east-1.rds.amazonaws.com", false},
		{"private ip", "10.0.1.5", false},
		{"private ip other block", "192.168.1.20", false},
		{"public v6", "2001:db8::1", false},
		{"internal hostname", "db.internal.example.com", false},
		{"hostname merely starting with db", "db.example.com", false},
		{"hostname merely containing localhost", "localhost.evil.example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isLocalHost(tt.host), "host: %q", tt.host)
		})
	}
}

// TestIsLocalHostFromDSN runs real DSNs through the same path New() uses:
// pgxpool.ParseConfig, then isLocalHost on the resolved host. This is what
// actually regressed -- the previous implementation matched whole DSN strings
// against a five-entry allowlist, so everything below except the bundled
// defaults reported remote even with Postgres on the same machine.
func TestIsLocalHostFromDSN(t *testing.T) {
	clearPGEnv(t)

	tests := []struct {
		name string
		dsn  string
		want bool
	}{
		// Defaults the old allowlist covered; kept as regressions.
		{"compose default creator node", "postgres://postgres:postgres@db:5432/audius_creator_node", true},
		{"compose default creator node postgresql scheme", "postgresql://postgres:postgres@db:5432/audius_creator_node", true},
		{"compose default openaudio", "postgres://postgres:postgres@db:5432/openaudio", true},
		{"compose default openaudio postgresql scheme", "postgresql://postgres:postgres@db:5432/openaudio", true},

		// The core default DSN, which the old allowlist never included.
		{"core default dsn", "postgresql://postgres:postgres@localhost:5432/openaudio", true},
		{"core default dsn with sslmode", "postgresql://postgres:postgres@localhost:5432/openaudio?sslmode=disable", true},

		// Local, but not matching any literal default.
		{"non default password", "postgres://postgres:hunter2@localhost:5432/openaudio", true},
		{"non default port", "postgres://postgres:postgres@localhost:6543/openaudio", true},
		{"non default database name", "postgres://postgres:postgres@localhost:5432/mydb", true},
		{"appended sslmode", "postgres://postgres:postgres@db:5432/openaudio?sslmode=disable", true},
		{"multiple query params", "postgres://postgres:postgres@127.0.0.1:5432/openaudio?sslmode=disable&pool_max_conns=10", true},
		{"loopback ip", "postgres://postgres:postgres@127.0.0.1:5432/openaudio", true},
		{"ipv6 loopback", "postgres://postgres:postgres@[::1]:5432/openaudio", true},
		{"no port", "postgres://postgres:postgres@localhost/openaudio", true},
		{"uppercase host", "postgres://postgres:postgres@LOCALHOST:5432/openaudio", true},
		{"percent encoded password", "postgres://postgres:p%40ssw%3Ard@localhost:5432/openaudio", true},
		{"no credentials", "postgres://localhost:5432/openaudio", true},

		// Unix sockets are necessarily local.
		{"host omitted falls back to socket", "postgres://postgres:postgres@/openaudio", true},
		{"socket dir in host param", "postgres:///openaudio?host=/var/run/postgresql", true},

		// libpq keyword/value form, including quoting and spacing.
		{"kv localhost", "host=localhost port=5432 user=postgres dbname=openaudio", true},
		{"kv loopback", "host=127.0.0.1 port=5432 dbname=openaudio sslmode=disable", true},
		{"kv socket path", "host=/var/run/postgresql dbname=openaudio", true},
		{"kv quoted value", "host='localhost' password='pass word' dbname=openaudio", true},
		{"kv spaces around equals", "host = localhost dbname = openaudio", true},
		{"kv host omitted", "user=postgres dbname=openaudio", true},

		// pgx resolves a host param over the authority, so it decides both ways.
		{"socket param overrides remote authority", "postgres://remote.example.com:5432/openaudio?host=/var/run/postgresql", true},
		{"remote host param overrides local authority", "postgres://localhost:5432/openaudio?host=remote.example.com", false},

		// Genuinely remote.
		{"rds endpoint", "postgres://postgres:postgres@mydb.abc123.us-east-1.rds.amazonaws.com:5432/openaudio", false},
		{"private ip", "postgres://postgres:postgres@10.0.1.5:5432/openaudio", false},
		{"private ip with sslmode", "postgres://postgres:postgres@192.168.1.20:5432/openaudio?sslmode=require", false},
		{"ipv6 remote", "postgres://postgres:postgres@[2001:db8::1]:5432/openaudio", false},
		{"remote hostname", "postgres://postgres:postgres@db.internal.example.com:5432/openaudio", false},
		{"kv remote host", "host=db.example.com port=5432 dbname=openaudio", false},
		{"host prefixed with db", "postgres://postgres:postgres@db.example.com:5432/openaudio", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig(tt.dsn)
			require.NoError(t, err, "dsn: %q", tt.dsn)
			assert.Equal(t, tt.want, isLocalHost(cfg.ConnConfig.Host),
				"dsn: %q resolved to host %q", tt.dsn, cfg.ConnConfig.Host)
		})
	}
}

// TestUnbootableDSNsAreRejectedBeforeHealthCheck pins the reason the classifier
// doesn't need to be tolerant of malformed input. New() parses the DSN with
// pgxpool.ParseConfig and returns an error when it fails, so a node configured
// this way never starts and never serves a health check. This includes the bare
// "localhost" sentinel the old allowlist carried, which is why dropping it
// changes nothing observable.
func TestUnbootableDSNsAreRejectedBeforeHealthCheck(t *testing.T) {
	clearPGEnv(t)

	unbootable := []string{
		"localhost",
		"db.example.com",
		// libpq accepts a percent-encoded socket path in the authority; pgx does not.
		"postgres://postgres:postgres@%2Fvar%2Frun%2Fpostgresql/openaudio",
		"postgres://postgres:pass with space@localhost/openaudio",
		"host='unterminated",
		"=",
	}

	for _, dsn := range unbootable {
		t.Run(dsn, func(t *testing.T) {
			_, err := pgxpool.ParseConfig(dsn)
			assert.Error(t, err, "expected %q to fail startup", dsn)
		})
	}
}
