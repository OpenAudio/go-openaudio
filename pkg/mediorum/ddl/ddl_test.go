package ddl

import (
	"database/sql"
	"os"
	"testing"

	_ "github.com/lib/pq"
)

func TestMarkCoreBacklogLegacyMigrationRunsOnce(t *testing.T) {
	migration := `
	update ops
	set core_tx_status = 'legacy'
	where core_tx_status in ('pending', 'error')
	`

	dsn := os.Getenv("dbUrl")
	if dsn == "" {
		dsn = "postgres://postgres:example@localhost:5454/mediorum_test?sslmode=disable"
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		t.Skipf("Postgres unavailable: %v", err)
	}

	for _, ddl := range []string{
		`create temporary table mediorum_migrations ("hash" text primary key, "ts" timestamp)`,
		`create temporary table ops (ulid text primary key, core_tx_status text, core_tx_error text)`,
		`insert into ops values
			('pending', 'pending', ''),
			('error', 'error', 'historical error'),
			('local', 'local', ''),
			('confirmed', 'confirmed', '')`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}

	runMigration(db, migration)

	wantStatuses := map[string]string{
		"pending":   "legacy",
		"error":     "legacy",
		"local":     "local",
		"confirmed": "confirmed",
	}
	for ulid, want := range wantStatuses {
		var got string
		if err := db.QueryRow(`select core_tx_status from ops where ulid = $1`, ulid).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("status for %s = %q, want %q", ulid, got, want)
		}
	}

	var coreError string
	if err := db.QueryRow(`select core_tx_error from ops where ulid = 'error'`).Scan(&coreError); err != nil {
		t.Fatal(err)
	}
	if coreError != "historical error" {
		t.Errorf("core error = %q, want historical error", coreError)
	}

	if _, err := db.Exec(`insert into ops values ('new-pending', 'pending', '')`); err != nil {
		t.Fatal(err)
	}
	runMigration(db, migration)

	var newStatus string
	if err := db.QueryRow(`select core_tx_status from ops where ulid = 'new-pending'`).Scan(&newStatus); err != nil {
		t.Fatal(err)
	}
	if newStatus != "pending" {
		t.Errorf("new status after rerun = %q, want pending", newStatus)
	}
}

func TestRunVacuumFullDefaultOffDoesNotTouchDatabase(t *testing.T) {
	t.Setenv("OPENAUDIO_MEDIORUM_VACUUM_FULL", "")

	runVacuumFull(nil)
}

func TestVacuumFullEnabledDefaultsOff(t *testing.T) {
	t.Setenv("OPENAUDIO_MEDIORUM_VACUUM_FULL", "")

	if vacuumFullEnabled() {
		t.Fatal("expected vacuum full to default off")
	}
}

func TestVacuumFullEnabledParsesBool(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "true", value: "true", want: true},
		{name: "one", value: "1", want: true},
		{name: "false", value: "false", want: false},
		{name: "zero", value: "0", want: false},
		{name: "invalid", value: "yes", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("OPENAUDIO_MEDIORUM_VACUUM_FULL", test.value)

			if got := vacuumFullEnabled(); got != test.want {
				t.Fatalf("vacuumFullEnabled() = %v, want %v", got, test.want)
			}
		})
	}
}
