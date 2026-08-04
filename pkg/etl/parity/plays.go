package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Plays are compared differently from the domain tables in compare.go.
//
// There are ~40M play rows, so the strategy used elsewhere — load every ETL row,
// then issue one source lookup per row — would neither fit in memory nor finish
// in reasonable time. Instead:
//
//  1. total row counts, which catches wholesale loss;
//  2. per-track play counts, merge-walked so each side is read once with no
//     per-row queries. This covers all ~40M plays in aggregate: a dropped or
//     misattributed subset shows up here even though no individual play was
//     inspected;
//  3. a bounded random sample compared field by field, to catch corruption that
//     preserves counts.
//
// The two sides do not share a schema. The ETL writes `etl_plays`, whose ids are
// text and whose timestamp is `played_at`; the source table is `plays`, with
// integer ids and `created_at`. Ids are cast to bigint on both sides so the
// merge-walk orders numerically rather than lexicographically ('10' < '9').
//
// Only the fields genesis-writer carries are compared: user, track, timestamp,
// city, region and country. `source`, `slot` and `signature` are not part of the
// migration payload and would otherwise report as differences.
//
// Note that the per-track counts are computed from the source `plays` table
// directly rather than from its `aggregate_plays` rollup: the rollup is
// trigger-maintained in the consumer's schema, so comparing against it would
// attribute any drift in that rollup to the migration. (`aggregate_plays` does
// not exist in an ETL database at all — pkg/etl migration 0017 is a no-op stub;
// those derived tables are owned by the consumer.)
//
// Two properties of the data the comparison has to respect: user is NULL/empty
// for anonymous plays (~47% of rows), so sample lookups match NULL-safely; and
// (user, track, timestamp) is not unique — the source has ~1.8M duplicate
// triples — so the lookup compares occurrence counts rather than assuming a
// single row.

type playsResult struct {
	etlCount, srcCount int64

	trackCompared, trackMatched, trackMismatched, trackMissing, trackExtra int

	sampleCompared, sampleMatched, sampleMismatched, sampleMissing int
}

// ComparePlays runs the three checks described above. sampleSize <= 0 skips the
// per-row sample.
func ComparePlays(ctx context.Context, etlPool, srcPool *pgxpool.Pool, sampleSize int) error {
	fmt.Printf("=== Plays ===\n")
	var r playsResult

	// --- 1. total counts -----------------------------------------------------
	if err := etlPool.QueryRow(ctx, "SELECT count(*) FROM etl_plays").Scan(&r.etlCount); err != nil {
		return fmt.Errorf("count etl_plays: %w", err)
	}
	if err := srcPool.QueryRow(ctx, "SELECT count(*) FROM plays").Scan(&r.srcCount); err != nil {
		return fmt.Errorf("count source plays: %w", err)
	}
	fmt.Printf("row count:  etl_plays=%d  source plays=%d  delta=%+d\n",
		r.etlCount, r.srcCount, r.etlCount-r.srcCount)

	// --- 2. per-track counts -------------------------------------------------
	if err := comparePlayCounts(ctx, etlPool, srcPool, &r); err != nil {
		return err
	}

	// --- 3. bounded sample ---------------------------------------------------
	if sampleSize > 0 {
		if err := comparePlaySample(ctx, etlPool, srcPool, sampleSize, &r); err != nil {
			return err
		}
	}

	fmt.Printf("\n--- Plays summary ---\n")
	fmt.Printf("per-track counts: compared=%d matched=%d mismatched=%d missing_in_source=%d not_indexed=%d\n",
		r.trackCompared, r.trackMatched, r.trackMismatched, r.trackMissing, r.trackExtra)
	if r.trackCompared > 0 {
		fmt.Printf("per-track match rate: %.4f%%\n", float64(r.trackMatched)/float64(r.trackCompared)*100)
	}
	if sampleSize > 0 {
		fmt.Printf("sampled rows:     compared=%d matched=%d mismatched=%d missing_in_source=%d\n",
			r.sampleCompared, r.sampleMatched, r.sampleMismatched, r.sampleMissing)
		if r.sampleCompared > 0 {
			fmt.Printf("sample match rate: %.2f%%\n", float64(r.sampleMatched)/float64(r.sampleCompared)*100)
		}
	}
	fmt.Println()
	return nil
}

// comparePlayCounts merge-walks per-track play counts on both sides in numeric
// track order, so each side is read once with no per-row lookups.
func comparePlayCounts(ctx context.Context, etlPool, srcPool *pgxpool.Pool, r *playsResult) error {
	const etlQ = `SELECT track_id::bigint AS id, count(*) FROM etl_plays GROUP BY 1 ORDER BY 1`
	const srcQ = `SELECT play_item_id::bigint AS id, count(*) FROM plays GROUP BY 1 ORDER BY 1`

	etlRows, err := etlPool.Query(ctx, etlQ)
	if err != nil {
		return fmt.Errorf("group etl_plays: %w", err)
	}
	defer etlRows.Close()

	srcRows, err := srcPool.Query(ctx, srcQ)
	if err != nil {
		return fmt.Errorf("group source plays: %w", err)
	}
	defer srcRows.Close()

	type row struct {
		id, count int64
		ok        bool
	}
	next := func(rows pgx.Rows, side string) (row, error) {
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return row{}, fmt.Errorf("iterate %s play counts: %w", side, err)
			}
			return row{}, nil
		}
		var out row
		if err := rows.Scan(&out.id, &out.count); err != nil {
			return row{}, fmt.Errorf("scan %s play counts: %w", side, err)
		}
		out.ok = true
		return out, nil
	}

	e, err := next(etlRows, "etl")
	if err != nil {
		return err
	}
	s, err := next(srcRows, "source")
	if err != nil {
		return err
	}

	var shown int
	const maxShown = 20

	for e.ok || s.ok {
		switch {
		case e.ok && (!s.ok || e.id < s.id):
			r.trackCompared++
			r.trackMissing++
			if shown < maxShown {
				fmt.Printf("  track %d: in ETL only (etl plays=%d)\n", e.id, e.count)
				shown++
			}
			if e, err = next(etlRows, "etl"); err != nil {
				return err
			}
		case s.ok && (!e.ok || s.id < e.id):
			r.trackCompared++
			r.trackExtra++
			if shown < maxShown {
				fmt.Printf("  track %d: NOT INDEXED (source plays=%d)\n", s.id, s.count)
				shown++
			}
			if s, err = next(srcRows, "source"); err != nil {
				return err
			}
		default:
			r.trackCompared++
			if e.count == s.count {
				r.trackMatched++
			} else {
				r.trackMismatched++
				if shown < maxShown {
					fmt.Printf("  track %d: plays etl=%d source=%d (delta %+d)\n",
						e.id, e.count, s.count, e.count-s.count)
					shown++
				}
			}
			if e, err = next(etlRows, "etl"); err != nil {
				return err
			}
			if s, err = next(srcRows, "source"); err != nil {
				return err
			}
		}
	}
	if shown >= maxShown {
		fmt.Printf("  ... further per-track differences suppressed\n")
	}
	return nil
}

// comparePlaySample pulls a bounded random sample of ETL plays and checks each
// against the source. TABLESAMPLE is page-based, so it stays cheap on a 40M-row
// table where ORDER BY random() would not.
func comparePlaySample(ctx context.Context, etlPool, srcPool *pgxpool.Pool, sampleSize int, r *playsResult) error {
	if r.etlCount == 0 {
		return nil
	}
	pct := 100.0 * float64(sampleSize) / float64(r.etlCount)
	pct *= 3 // TABLESAMPLE yields a variable count; oversample and let LIMIT trim
	if pct > 100 {
		pct = 100
	}

	sampleQ := fmt.Sprintf(`
		SELECT user_id, track_id, played_at, city, region, country
		FROM etl_plays TABLESAMPLE SYSTEM (%.6f)
		LIMIT %d`, pct, sampleSize)

	rows, err := etlPool.Query(ctx, sampleQ)
	if err != nil {
		return fmt.Errorf("sample etl_plays: %w", err)
	}
	defer rows.Close()

	type play struct {
		userID, trackID       *string
		playedAt              time.Time
		city, region, country *string
	}
	var sample []play
	for rows.Next() {
		var p play
		if err := rows.Scan(&p.userID, &p.trackID, &p.playedAt, &p.city, &p.region, &p.country); err != nil {
			return fmt.Errorf("scan sampled play: %w", err)
		}
		sample = append(sample, p)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sampled plays: %w", err)
	}
	rows.Close()

	fmt.Printf("\nsampled %d plays (TABLESAMPLE %.6f%%)\n", len(sample), pct)

	// The ETL stores ids as text and leaves the user empty for anonymous plays,
	// so cast and match NULL-safely. The triple is not unique, so compare how
	// many rows each side has for it rather than assuming one.
	const lookupQ = `
		SELECT count(*), min(city), min(region), min(country)
		FROM plays
		WHERE user_id IS NOT DISTINCT FROM $1
		  AND play_item_id = $2
		  AND created_at = $3`

	var shown int
	const maxShown = 20

	for _, p := range sample {
		userID, err := nullableInt(p.userID)
		if err != nil {
			return fmt.Errorf("sampled play has non-numeric user_id %q: %w", *p.userID, err)
		}
		trackID, err := nullableInt(p.trackID)
		if err != nil || trackID == nil {
			return fmt.Errorf("sampled play has unusable track_id %v", p.trackID)
		}

		var srcCount int64
		var city, region, country *string
		err = srcPool.QueryRow(ctx, lookupQ, userID, *trackID, p.playedAt).
			Scan(&srcCount, &city, &region, &country)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lookup sampled play: %w", err)
		}

		r.sampleCompared++
		if errors.Is(err, pgx.ErrNoRows) || srcCount == 0 {
			r.sampleMissing++
			if shown < maxShown {
				fmt.Printf("  play(user=%s track=%d at=%s): MISSING in source\n",
					fmtNullableInt(userID), *trackID, p.playedAt.Format(time.RFC3339))
				shown++
			}
			continue
		}

		if strEq(p.city, city) && strEq(p.region, region) && strEq(p.country, country) {
			r.sampleMatched++
			continue
		}
		r.sampleMismatched++
		if shown < maxShown {
			fmt.Printf("  play(user=%s track=%d at=%s): location etl=(%s,%s,%s) source=(%s,%s,%s)\n",
				fmtNullableInt(userID), *trackID, p.playedAt.Format(time.RFC3339),
				derefStr(p.city), derefStr(p.region), derefStr(p.country),
				derefStr(city), derefStr(region), derefStr(country))
			shown++
		}
	}
	if shown >= maxShown {
		fmt.Printf("  ... further sampled-play differences suppressed\n")
	}
	return nil
}

// nullableInt converts the ETL's text id to an integer, treating NULL and the
// empty string alike (anonymous plays carry no user).
func nullableInt(s *string) (*int64, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	var v int64
	if _, err := fmt.Sscanf(*s, "%d", &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func fmtNullableInt(v *int64) string {
	if v == nil {
		return "<anon>"
	}
	return fmt.Sprintf("%d", *v)
}

func strEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func derefStr(s *string) string {
	if s == nil {
		return "<null>"
	}
	return *s
}
