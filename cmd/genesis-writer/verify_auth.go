package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/urfave/cli/v2"
)

// verify-auth compares the consensus auth state the writer projected
// (core_auth_* in the chain DB) with what the source snapshot says it should
// hold. ETL parity only covers the indexer's tables; the projection is a
// separate code path in pkg/core/server and can drift from the source without
// the ETL noticing.
//
// Each check is a pair of queries returning (k, col1, col2, ...) as text.
// runAuthParityCheck orders both by k under the "C" collation, so the two
// sides merge in one pass without holding either in memory. Expected rows
// follow the writer's own source queries (entities_*.go): the same is_current
// filters and the same grantor/owner wallet joins.

type authParityCheck struct {
	table   string
	columns []string
	src     string
	dst     string
}

// authParityChecks takes the writer's fallback signer: a source user with no
// wallet is created with it as the signer, which the projection records as
// the user's wallet.
func authParityChecks(fallbackSigner string) []authParityCheck {
	return []authParityCheck{
		{
			table:   "core_auth_users",
			columns: []string{"wallet", "handle_lc", "is_deactivated"},
			src: fmt.Sprintf(`SELECT user_id::text AS k,
					COALESCE(NULLIF(LOWER(wallet), ''), %s),
					COALESCE(LOWER(handle), ''),
					COALESCE(is_deactivated, false)::text
				FROM users WHERE is_current = true`, pgQuote(strings.ToLower(fallbackSigner))),
			dst: `SELECT user_id::text AS k, wallet, COALESCE(handle_lc, ''), is_deactivated::text
				FROM core_auth_users`,
		},
		{
			table:   "core_auth_grants",
			columns: []string{"is_approved", "is_revoked"},
			// The writer migrates the newest is_current row per pair.
			src: `SELECT k, COALESCE(is_approved::text, 'null'), is_revoked::text FROM (
					SELECT DISTINCT ON (g.user_id, LOWER(g.grantee_address))
						LOWER(g.grantee_address) || '/' || g.user_id::text AS k,
						g.is_approved, g.is_revoked
					FROM grants g
					JOIN users u ON u.user_id = g.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
					WHERE g.is_current = true
					ORDER BY g.user_id, LOWER(g.grantee_address), g.created_at DESC
				) g`,
			dst: `SELECT grantee_address || '/' || user_id::text AS k,
					COALESCE(is_approved::text, 'null'), is_revoked::text
				FROM core_auth_grants`,
		},
		{
			table:   "core_auth_developer_apps",
			columns: []string{"user_id", "is_deleted"},
			src: `SELECT LOWER(d.address) AS k, d.user_id::text, d.is_delete::text
				FROM developer_apps d
				JOIN users u ON u.user_id = d.user_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
				WHERE d.is_current = true`,
			dst: `SELECT address AS k, user_id::text, is_deleted::text
				FROM core_auth_developer_apps`,
		},
		{
			table:   "core_auth_entities",
			columns: []string{"owner_user_id", "is_deleted"},
			src: `SELECT k, owner_id, is_delete FROM (
					SELECT 'Track/' || t.track_id::text AS k, t.owner_id::text AS owner_id, t.is_delete::text AS is_delete
					FROM tracks t
					JOIN users u ON u.user_id = t.owner_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
					WHERE t.is_current = true
					UNION ALL
					SELECT 'Playlist/' || p.playlist_id::text, p.playlist_owner_id::text, p.is_delete::text
					FROM playlists p
					JOIN users u ON u.user_id = p.playlist_owner_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
					WHERE p.is_current = true
				) e`,
			dst: `SELECT entity_type || '/' || entity_id::text AS k, owner_user_id::text, is_deleted::text
				FROM core_auth_entities`,
		},
		{
			table:   "core_auth_cids",
			columns: []string{"tx_hash"},
			// Migrated claims carry no tx hash (pkg/core/server/content_auth_state.go).
			src: `SELECT DISTINCT c.cid || '/' || t.owner_id::text AS k, ''
				FROM tracks t
				JOIN users u ON u.user_id = t.owner_id AND u.is_current = true AND u.wallet IS NOT NULL AND u.wallet <> ''
				CROSS JOIN LATERAL (VALUES (t.track_cid), (t.preview_cid), (t.orig_file_cid)) AS c(cid)
				WHERE t.is_current = true AND c.cid IS NOT NULL AND c.cid <> ''`,
			dst: `SELECT cid || '/' || user_id::text AS k, tx_hash
				FROM core_auth_cids`,
		},
	}
}

// sortedByKey orders a check query by k bytewise, the order the merge compares in.
func sortedByKey(q string) string {
	return `SELECT * FROM (` + q + `) q ORDER BY q.k COLLATE "C"`
}

func pgQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

const authParitySamples = 10

type authParityResult struct {
	table      string
	columns    []string
	srcRows    int64
	dstRows    int64
	missing    int64 // in source, not projected
	extra      int64 // projected, not in source
	missingEx  []string
	extraEx    []string
	mismatches []map[string]int64 // per column: "src -> dst" -> count
	mismatchEx [][]string         // per column: sample keys
}

func (r *authParityResult) clean() bool {
	if r.missing != 0 || r.extra != 0 {
		return false
	}
	for _, m := range r.mismatches {
		if len(m) != 0 {
			return false
		}
	}
	return true
}

// verifyAuthState runs every check and returns the results; it fails only on
// query errors, leaving the verdict to the caller.
func verifyAuthState(ctx context.Context, src, dst *pgxpool.Pool, fallbackSigner string) ([]*authParityResult, error) {
	var out []*authParityResult
	for _, c := range authParityChecks(fallbackSigner) {
		r, err := runAuthParityCheck(ctx, src, dst, c)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", c.table, err)
		}
		out = append(out, r)
	}
	return out, nil
}

func runAuthParityCheck(ctx context.Context, src, dst *pgxpool.Pool, c authParityCheck) (*authParityResult, error) {
	r := &authParityResult{
		table:      c.table,
		columns:    c.columns,
		mismatches: make([]map[string]int64, len(c.columns)),
		mismatchEx: make([][]string, len(c.columns)),
	}
	for i := range r.mismatches {
		r.mismatches[i] = map[string]int64{}
	}

	srcRows, err := src.Query(ctx, sortedByKey(c.src))
	if err != nil {
		return nil, fmt.Errorf("source query: %w", err)
	}
	defer srcRows.Close()
	dstRows, err := dst.Query(ctx, sortedByKey(c.dst))
	if err != nil {
		return nil, fmt.Errorf("chain query: %w", err)
	}
	defer dstRows.Close()

	width := len(c.columns) + 1
	next := func(rows pgx.Rows, n *int64) ([]string, error) {
		if !rows.Next() {
			return nil, rows.Err()
		}
		vals := make([]string, width)
		ptrs := make([]any, width)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		*n++
		return vals, nil
	}

	s, err := next(srcRows, &r.srcRows)
	if err != nil {
		return nil, err
	}
	d, err := next(dstRows, &r.dstRows)
	if err != nil {
		return nil, err
	}
	for s != nil || d != nil {
		switch {
		case d == nil || (s != nil && s[0] < d[0]):
			r.missing++
			if len(r.missingEx) < authParitySamples {
				r.missingEx = append(r.missingEx, s[0])
			}
			if s, err = next(srcRows, &r.srcRows); err != nil {
				return nil, err
			}
		case s == nil || d[0] < s[0]:
			r.extra++
			if len(r.extraEx) < authParitySamples {
				r.extraEx = append(r.extraEx, d[0])
			}
			if d, err = next(dstRows, &r.dstRows); err != nil {
				return nil, err
			}
		default:
			for i := range c.columns {
				if s[i+1] != d[i+1] {
					r.mismatches[i][s[i+1]+" -> "+d[i+1]]++
					if len(r.mismatchEx[i]) < authParitySamples {
						r.mismatchEx[i] = append(r.mismatchEx[i], s[0])
					}
				}
			}
			if s, err = next(srcRows, &r.srcRows); err != nil {
				return nil, err
			}
			if d, err = next(dstRows, &r.dstRows); err != nil {
				return nil, err
			}
		}
	}
	return r, nil
}

func printAuthParity(w io.Writer, results []*authParityResult) {
	for _, r := range results {
		verdict := "ok"
		if !r.clean() {
			verdict = "DIFFERS"
		}
		fmt.Fprintf(w, "%s: %s (source %d, chain %d)\n", r.table, verdict, r.srcRows, r.dstRows)
		if r.missing > 0 {
			fmt.Fprintf(w, "  missing from chain: %d  e.g. %s\n", r.missing, strings.Join(r.missingEx, ", "))
		}
		if r.extra > 0 {
			fmt.Fprintf(w, "  not in source: %d  e.g. %s\n", r.extra, strings.Join(r.extraEx, ", "))
		}
		for i, col := range r.columns {
			m := r.mismatches[i]
			if len(m) == 0 {
				continue
			}
			pairs := make([]string, 0, len(m))
			for p := range m {
				pairs = append(pairs, p)
			}
			sort.Slice(pairs, func(a, b int) bool { return m[pairs[a]] > m[pairs[b]] })
			const maxPairs = 10
			var total int64
			for _, p := range pairs {
				total += m[p]
			}
			fmt.Fprintf(w, "  %s differs on %d rows (source -> chain):\n", col, total)
			for j, p := range pairs {
				if j == maxPairs {
					fmt.Fprintf(w, "    ... %d more value pairs\n", len(pairs)-maxPairs)
					break
				}
				fmt.Fprintf(w, "    %s: %d\n", p, m[p])
			}
			fmt.Fprintf(w, "    e.g. %s\n", strings.Join(r.mismatchEx[i], ", "))
		}
	}
}

func verifyAuthCmd() *cli.Command {
	return &cli.Command{
		Name:  "verify-auth",
		Usage: "Compare the chain DB's consensus auth state (core_auth_*) with what the source snapshot implies",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "src-dsn",
				Usage:    "Source snapshot PostgreSQL DSN the chain was written from",
				EnvVars:  []string{"GENESIS_SRC_DSN"},
				Required: true,
			},
			&cli.StringFlag{
				Name:     "dst-dsn",
				Usage:    "Chain PostgreSQL DSN holding the projected core_auth_* tables",
				EnvVars:  []string{"GENESIS_DST_DSN"},
				Required: true,
			},
			&cli.StringFlag{
				Name:  "migration-address",
				Usage: "Genesis migration signer address the writer used; wallet-less source users are expected to carry it",
			},
		},
		Action: func(c *cli.Context) error {
			ctx := c.Context
			src, err := pgxpool.New(ctx, c.String("src-dsn"))
			if err != nil {
				return fmt.Errorf("connect source: %w", err)
			}
			defer src.Close()
			dst, err := pgxpool.New(ctx, c.String("dst-dsn"))
			if err != nil {
				return fmt.Errorf("connect chain: %w", err)
			}
			defer dst.Close()

			results, err := verifyAuthState(ctx, src, dst, c.String("migration-address"))
			if err != nil {
				return err
			}
			printAuthParity(c.App.Writer, results)
			for _, r := range results {
				if !r.clean() {
					return cli.Exit("auth state differs from the source", 1)
				}
			}
			return nil
		},
	}
}
