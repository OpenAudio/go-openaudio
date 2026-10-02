package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// restoreSessionOptions are passed to pg_restore through PGOPTIONS, so they
// apply to its own session and end with it.
//
// Earlier restores tuned postgres with ALTER SYSTEM instead, on the belief that
// the settings reset when postgres restarted. ALTER SYSTEM writes
// postgresql.auto.conf, which persists, so every node that state synced kept
// running with synchronous_commit=off, max_wal_size=8GB and
// checkpoint_timeout=1h. max_wal_size and checkpoint_timeout cannot be set per
// session, so restores no longer change them.
const restoreSessionOptions = "-c synchronous_commit=off"

// pgRestoreEnv returns env with restoreSessionOptions added to PGOPTIONS,
// keeping any options the operator already set there.
func pgRestoreEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	options := restoreSessionOptions
	for _, kv := range env {
		if existing, ok := strings.CutPrefix(kv, "PGOPTIONS="); ok {
			if existing != "" {
				options = existing + " " + restoreSessionOptions
			}
			continue
		}
		out = append(out, kv)
	}
	return append(out, "PGOPTIONS="+options)
}

type persistedSetting struct {
	Name  string
	Value string
}

// leftoverRestoreSettings reports the server-wide settings earlier restores
// persisted, when they still hold exactly the values those restores wrote.
func leftoverRestoreSettings(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) ([]persistedSetting, error) {
	// max_wal_size is reported in MB and checkpoint_timeout in seconds.
	rows, err := q.Query(ctx, `
		SELECT name, setting
		FROM pg_settings
		WHERE source = 'configuration file'
		  AND (sourcefile IS NULL OR sourcefile LIKE '%postgresql.auto.conf')
		  AND (   (name = 'synchronous_commit' AND setting = 'off')
		       OR (name = 'max_wal_size'       AND setting = '8192')
		       OR (name = 'checkpoint_timeout' AND setting = '3600'))
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	defer rows.Close()

	var settings []persistedSetting
	for rows.Next() {
		var st persistedSetting
		if err := rows.Scan(&st.Name, &st.Value); err != nil {
			return nil, err
		}
		settings = append(settings, st)
	}
	return settings, rows.Err()
}

// warnLeftoverRestoreSettings logs settings an earlier restore left behind.
// It does not change them: an operator may have chosen the same values, and
// postgres configuration is theirs to change.
func (s *Server) warnLeftoverRestoreSettings(ctx context.Context) {
	if s.pool == nil {
		return
	}

	settings, err := leftoverRestoreSettings(ctx, s.pool)
	if err != nil {
		s.logger.Warn("could not check for postgres settings left by an earlier state sync restore", zap.Error(err))
		return
	}
	if len(settings) == 0 {
		return
	}

	names := make([]string, 0, len(settings))
	var fix strings.Builder
	for _, st := range settings {
		names = append(names, st.Name+"="+st.Value)
		fmt.Fprintf(&fix, "ALTER SYSTEM RESET %s; ", st.Name)
	}
	fix.WriteString("SELECT pg_reload_conf();")

	s.logger.Warn("postgres is running with settings an earlier state sync restore persisted by mistake; "+
		"synchronous_commit=off can lose recently committed writes if postgres stops uncleanly. "+
		"Unless you set these on purpose, reset them",
		zap.Strings("settings", names),
		zap.String("fix", fix.String()))
}
