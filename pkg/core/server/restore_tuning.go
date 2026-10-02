package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// Restores tune postgres for the bulk load (#253). Earlier restores did all of
// it with ALTER SYSTEM, on the belief that the settings reset when postgres
// restarted. ALTER SYSTEM writes postgresql.auto.conf, which persists, so every
// node that state synced kept running with synchronous_commit=off,
// max_wal_size=8GB and checkpoint_timeout=1h.
//
// synchronous_commit can be set per session, so pg_restore gets it through
// PGOPTIONS and it ends with pg_restore. max_wal_size and checkpoint_timeout
// can only be set server-wide, so the restore sets them and puts back what was
// there before when it finishes.

// restoreSessionOptions are passed to pg_restore through PGOPTIONS.
const restoreSessionOptions = "-c synchronous_commit=off"

// restoreServerSettings are set server-wide for the duration of a restore.
// setting is how pg_settings reports value, used to recognize it later.
var restoreServerSettings = []struct {
	name, value, setting string
}{
	{name: "max_wal_size", value: "8GB", setting: "8192"},
	{name: "checkpoint_timeout", value: "1h", setting: "3600"},
}

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

// leftoverRestoreSettings reports the settings earlier restores persisted, when
// they still hold exactly the values those restores wrote. A restore that is
// killed before it finishes leaves its server-wide settings behind the same way.
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

// applyRestoreServerSettings sets restoreServerSettings server-wide and returns
// a function that puts back what was there before: the operator's own ALTER
// SYSTEM value if there was one, and otherwise nothing, so the configuration
// files apply again. A value equal to the restore's own is treated as left by
// an earlier restore and removed rather than put back.
func applyRestoreServerSettings(ctx context.Context, q pgExecQuerier) (func(context.Context) error, error) {
	type previous struct {
		name, value  string
		fromAutoConf bool
	}
	var prev []previous
	for _, st := range restoreServerSettings {
		var p previous
		var setting string
		if err := q.QueryRow(ctx, `
			SELECT current_setting(name), setting, coalesce(sourcefile LIKE '%postgresql.auto.conf', false)
			FROM pg_settings WHERE name = $1`, st.name).Scan(&p.value, &setting, &p.fromAutoConf); err != nil {
			return nil, fmt.Errorf("read %s: %w", st.name, err)
		}
		p.name = st.name
		if setting == st.setting {
			p.fromAutoConf = false
		}
		prev = append(prev, p)
	}

	undo := func(ctx context.Context) error {
		var errs []string
		for _, p := range prev {
			stmt := "ALTER SYSTEM RESET " + p.name
			if p.fromAutoConf {
				stmt = "ALTER SYSTEM SET " + p.name + " = " + quoteLiteral(p.value)
			}
			if _, err := q.Exec(ctx, stmt); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", stmt, err))
			}
		}
		if _, err := q.Exec(ctx, "SELECT pg_reload_conf()"); err != nil {
			errs = append(errs, fmt.Sprintf("reload: %v", err))
		}
		if len(errs) > 0 {
			return fmt.Errorf("restore postgres settings: %s", strings.Join(errs, "; "))
		}
		return nil
	}

	for _, st := range restoreServerSettings {
		if _, err := q.Exec(ctx, "ALTER SYSTEM SET "+st.name+" = "+quoteLiteral(st.value)); err != nil {
			return nil, errors.Join(fmt.Errorf("set %s: %w", st.name, err), undo(ctx))
		}
	}
	if _, err := q.Exec(ctx, "SELECT pg_reload_conf()"); err != nil {
		return nil, errors.Join(fmt.Errorf("reload: %w", err), undo(ctx))
	}
	return undo, nil
}

// quoteLiteral quotes s as a SQL string literal. ALTER SYSTEM does not accept
// bind parameters.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
