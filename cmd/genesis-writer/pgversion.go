package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"
)

// nodePostgresMajor is the Postgres major version the node image bundles
// (cmd/openaudio/Dockerfile installs postgresql-15). The chain dump is restored
// there, so it is the default target for everything the writer produces.
//
// Two compatibility rules pin the whole pipeline to it:
//
//   - pg_restore cannot read an archive written by a newer pg_dump: each major
//     bumps the archive format (1.14 through 15, 1.15 in 16, 1.16 in 17), and
//     an older pg_restore refuses with "unsupported version in file header".
//   - pg_dump refuses to dump a server newer than itself.
//
// So server <= pg_dump <= node. On 2026-08-25 the artifact sat in a PG17
// cluster on macOS, which no PG15 pg_dump can read and whose PG17 dump no PG15
// pg_restore can load; moving it to the node took a hand-built workaround.
const nodePostgresMajor = 15

// pgDumpTool is a pg_dump binary and the version it reports.
type pgDumpTool struct {
	Path    string
	Version string // first line of `pg_dump --version`
	Major   int
}

// pgTarget describes the destination database's server and the pg_dump that
// will archive it.
type pgTarget struct {
	ServerVersion string // SHOW server_version
	ServerMajor   int
	TargetMajor   int         // the node's Postgres major, where the dump is restored
	Dump          *pgDumpTool // nil when dumping is disabled
}

var pgVersionRe = regexp.MustCompile(`\(PostgreSQL\)\s+(\d+)`)

// parsePgToolMajor extracts the major version from a Postgres client tool's
// --version output, e.g. "pg_dump (PostgreSQL) 15.8 (Debian 15.8-1.pgdg120+1)"
// or "pg_dump (PostgreSQL) 17beta1".
func parsePgToolMajor(out string) (int, error) {
	m := pgVersionRe.FindStringSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("unrecognized version output %q", strings.TrimSpace(out))
	}
	return strconv.Atoi(m[1])
}

// serverMajorFromNum converts server_version_num (e.g. 150008) to its major
// version. Every supported server is >= 10, where the major is the leading
// component.
func serverMajorFromNum(num int) int {
	return num / 10000
}

// checkServerMajor enforces server <= target. A newer server produces a
// database no node can restore, so it is refused unless explicitly allowed.
func checkServerMajor(server, target int, allowNewer bool) error {
	if server <= target {
		return nil
	}
	if allowNewer {
		return nil
	}
	return fmt.Errorf("destination postgres is major %d but the node restores into postgres %d: "+
		"pg_dump cannot dump a newer server with an older client, and a pg_dump %d archive "+
		"is unreadable by pg_restore %d. Write into a postgres %d server (omit --dst-dsn to "+
		"use the managed one), or pass --allow-newer-postgres if you will move the data some other way",
		server, target, server, target, target)
}

// pgDumpFits reports whether a pg_dump of the given major can dump the server
// and produce an archive the target's pg_restore reads.
func pgDumpFits(dumpMajor, server, target int, allowNewer bool) bool {
	if dumpMajor < server {
		return false
	}
	return dumpMajor <= target || allowNewer
}

// pgBinCandidates lists directories that conventionally hold the client tools
// for one Postgres major, most specific first.
func pgBinCandidates(major int) []string {
	m := strconv.Itoa(major)
	return []string{
		// macOS Homebrew
		"/opt/homebrew/opt/postgresql@" + m + "/bin",
		"/usr/local/opt/postgresql@" + m + "/bin",
		// macOS Postgres.app
		"/Applications/Postgres.app/Contents/Versions/" + m + "/bin",
		// Debian / Ubuntu
		"/usr/lib/postgresql/" + m + "/bin",
		// RHEL / Fedora PGDG
		"/usr/pgsql-" + m + "/bin",
	}
}

func probePgTool(path string) (version string, major int, err error) {
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		return "", 0, err
	}
	version = strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	major, err = parsePgToolMajor(version)
	return version, major, err
}

// findPgDump picks a pg_dump that satisfies server <= pg_dump <= target. It
// prefers the target major, then the managed cluster's bin dir, then PATH.
func findPgDump(binDirHint string, server, target int, allowNewer bool) (*pgDumpTool, error) {
	var dirs []string
	dirs = append(dirs, pgBinCandidates(target)...)
	if binDirHint != "" {
		dirs = append([]string{binDirHint}, dirs...)
	}
	for m := target - 1; m >= server; m-- {
		dirs = append(dirs, pgBinCandidates(m)...)
	}

	var paths []string
	for _, d := range dirs {
		paths = append(paths, filepath.Join(d, "pg_dump"))
	}
	if p, err := exec.LookPath("pg_dump"); err == nil {
		paths = append(paths, p)
	}

	seen := map[string]bool{}
	var rejected []string
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		if _, err := os.Stat(p); err != nil {
			continue
		}
		version, major, err := probePgTool(p)
		if err != nil {
			rejected = append(rejected, fmt.Sprintf("%s (%v)", p, err))
			continue
		}
		if pgDumpFits(major, server, target, allowNewer) {
			return &pgDumpTool{Path: p, Version: version, Major: major}, nil
		}
		rejected = append(rejected, fmt.Sprintf("%s (major %d)", p, major))
	}

	msg := fmt.Sprintf("no pg_dump with server(%d) <= major <= node(%d) found; install postgres %d client tools (e.g. `brew install postgresql@%d`)",
		server, target, target, target)
	if len(rejected) > 0 {
		msg += "; rejected: " + strings.Join(rejected, ", ")
	}
	return nil, fmt.Errorf("%s, or pass --no-dump", msg)
}

// checkDestinationPostgres reads the destination server's version, enforces
// that the node can restore it, and resolves the pg_dump to archive it with.
// It runs before any writing, so a mismatch costs seconds rather than a
// multi-hour run that cannot be moved.
func checkDestinationPostgres(ctx context.Context, dsn string, target int, allowNewer, dump bool, binDirHint string, logger *zap.Logger) (*pgTarget, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect dst db for version check: %w", err)
	}
	defer conn.Close(ctx)

	var numStr, version string
	if err := conn.QueryRow(ctx, "SHOW server_version_num").Scan(&numStr); err != nil {
		return nil, fmt.Errorf("read server_version_num: %w", err)
	}
	if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&version); err != nil {
		return nil, fmt.Errorf("read server_version: %w", err)
	}
	num, err := strconv.Atoi(numStr)
	if err != nil {
		return nil, fmt.Errorf("parse server_version_num %q: %w", numStr, err)
	}

	t := &pgTarget{ServerVersion: version, ServerMajor: serverMajorFromNum(num), TargetMajor: target}
	if err := checkServerMajor(t.ServerMajor, target, allowNewer); err != nil {
		return nil, err
	}
	if t.ServerMajor > target {
		logger.Error("!!! DESTINATION POSTGRES IS NEWER THAN THE NODE'S — the output cannot be restored on a node as-is !!!",
			zap.Int("server_major", t.ServerMajor),
			zap.Int("node_major", target),
			zap.String("override", "--allow-newer-postgres"))
	}

	if dump {
		tool, err := findPgDump(binDirHint, t.ServerMajor, target, allowNewer)
		if err != nil {
			return nil, err
		}
		t.Dump = tool
		if tool.Major > target {
			logger.Error("!!! pg_dump IS NEWER THAN THE NODE'S postgres — its archive will not load with the node's pg_restore !!!",
				zap.String("pg_dump", tool.Version), zap.Int("node_major", target))
		}
	}

	logger.Info("destination postgres version checked",
		zap.String("server_version", t.ServerVersion),
		zap.Int("node_major", target),
		zap.Any("pg_dump", t.Dump),
	)
	return t, nil
}
