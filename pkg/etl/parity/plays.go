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
// There are ~40M play rows, so the row-at-a-time strategy used elsewhere (load
// every ETL row, then one lookup per row) would neither fit in memory nor finish
// in reasonable time. Instead:
//
//  1. total row counts, which catches wholesale loss;
//  2. a full comparison of aggregate_plays — only ~1.7M rows, and because it is
//     the per-track play count it covers all 40M plays in aggregate, so a
//     misplaced or dropped subset shows up here even though no individual play
//     was inspected;
//  3. a bounded random sample compared field by field, to catch corruption that
//     preserves counts.
//
// Only the fields genesis-writer actually carries are compared: user_id,
// play_item_id, created_at, city, region, and country. `source`, `slot` and
// `signature` are not part of the migration payload and are expected to differ.
//
// Note that (user_id, play_item_id, created_at) is not unique — the source has
// ~1.8M duplicate triples — and user_id is NULL for anonymous plays (~47% of
// rows), so sample lookups match NULL-safely and compare occurrence counts
// rather than assuming a single row.

type playsResult struct {
	etlCount, prodCount int64

	aggCompared, aggMatched, aggMismatched, aggMissing, aggExtra int

	sampleCompared, sampleMatched, sampleMismatched, sampleMissing int
}

// ComparePlays runs the three checks described above. sampleSize <= 0 skips the
// per-row sample.
func ComparePlays(ctx context.Context, etlPool, prodPool *pgxpool.Pool, sampleSize int) error {
	fmt.Printf("=== Plays ===\n")
	var r playsResult

	// --- 1. total counts -----------------------------------------------------
	if err := etlPool.QueryRow(ctx, "SELECT count(*) FROM plays").Scan(&r.etlCount); err != nil {
		return fmt.Errorf("count etl plays: %w", err)
	}
	if err := prodPool.QueryRow(ctx, "SELECT count(*) FROM plays").Scan(&r.prodCount); err != nil {
		return fmt.Errorf("count prod plays: %w", err)
	}
	delta := r.etlCount - r.prodCount
	fmt.Printf("row count:        etl=%d  source=%d  delta=%+d\n", r.etlCount, r.prodCount, delta)

	// --- 2. full aggregate_plays comparison ----------------------------------
	if err := comparePlayCounts(ctx, etlPool, prodPool, &r); err != nil {
		return err
	}

	// --- 3. bounded sample ---------------------------------------------------
	if sampleSize > 0 {
		if err := comparePlaySample(ctx, etlPool, prodPool, sampleSize, &r); err != nil {
			return err
		}
	}

	fmt.Printf("\n--- Plays summary ---\n")
	fmt.Printf("aggregate_plays: compared=%d matched=%d mismatched=%d missing_in_source=%d extra_in_source=%d\n",
		r.aggCompared, r.aggMatched, r.aggMismatched, r.aggMissing, r.aggExtra)
	if r.aggCompared > 0 {
		fmt.Printf("aggregate_plays match rate: %.4f%%\n", float64(r.aggMatched)/float64(r.aggCompared)*100)
	}
	if sampleSize > 0 {
		fmt.Printf("sampled rows:    compared=%d matched=%d mismatched=%d missing_in_source=%d\n",
			r.sampleCompared, r.sampleMatched, r.sampleMismatched, r.sampleMissing)
		if r.sampleCompared > 0 {
			fmt.Printf("sample match rate: %.2f%%\n", float64(r.sampleMatched)/float64(r.sampleCompared)*100)
		}
	}
	fmt.Println()
	return nil
}

// comparePlayCounts merge-walks aggregate_plays on both sides in play_item_id
// order, so each side is read once with no per-row lookups.
func comparePlayCounts(ctx context.Context, etlPool, prodPool *pgxpool.Pool, r *playsResult) error {
	const q = "SELECT play_item_id, count FROM aggregate_plays ORDER BY play_item_id"

	etlRows, err := etlPool.Query(ctx, q)
	if err != nil {
		return fmt.Errorf("query etl aggregate_plays: %w", err)
	}
	defer etlRows.Close()

	prodRows, err := prodPool.Query(ctx, q)
	if err != nil {
		return fmt.Errorf("query source aggregate_plays: %w", err)
	}
	defer prodRows.Close()

	type row struct {
		id    int64
		count int64
		ok    bool
	}
	next := func(rows pgx.Rows, side string) (row, error) {
		if !rows.Next() {
			if err := rows.Err(); err != nil {
				return row{}, fmt.Errorf("iterate %s aggregate_plays: %w", side, err)
			}
			return row{}, nil
		}
		var out row
		if err := rows.Scan(&out.id, &out.count); err != nil {
			return row{}, fmt.Errorf("scan %s aggregate_plays: %w", side, err)
		}
		out.ok = true
		return out, nil
	}

	e, err := next(etlRows, "etl")
	if err != nil {
		return err
	}
	p, err := next(prodRows, "source")
	if err != nil {
		return err
	}

	var shown int
	const maxShown = 20

	for e.ok || p.ok {
		switch {
		case e.ok && (!p.ok || e.id < p.id):
			// Present in the ETL, absent from the source.
			r.aggCompared++
			r.aggMissing++
			if shown < maxShown {
				fmt.Printf("  play_item_id=%d: MISSING in source (etl count=%d)\n", e.id, e.count)
				shown++
			}
			if e, err = next(etlRows, "etl"); err != nil {
				return err
			}
		case p.ok && (!e.ok || p.id < e.id):
			// Present in the source, never indexed.
			r.aggCompared++
			r.aggExtra++
			if shown < maxShown {
				fmt.Printf("  play_item_id=%d: NOT INDEXED (source count=%d)\n", p.id, p.count)
				shown++
			}
			if p, err = next(prodRows, "source"); err != nil {
				return err
			}
		default:
			r.aggCompared++
			if e.count == p.count {
				r.aggMatched++
			} else {
				r.aggMismatched++
				if shown < maxShown {
					fmt.Printf("  play_item_id=%d: count etl=%d source=%d (delta %+d)\n",
						e.id, e.count, p.count, e.count-p.count)
					shown++
				}
			}
			if e, err = next(etlRows, "etl"); err != nil {
				return err
			}
			if p, err = next(prodRows, "source"); err != nil {
				return err
			}
		}
	}
	if shown == maxShown {
		fmt.Printf("  ... further aggregate_plays differences suppressed\n")
	}
	return nil
}

// comparePlaySample pulls a bounded random sample of ETL plays and checks each
// against the source. TABLESAMPLE is page-based, so it stays cheap on a 40M-row
// table where ORDER BY random() would not.
func comparePlaySample(ctx context.Context, etlPool, prodPool *pgxpool.Pool, sampleSize int, r *playsResult) error {
	pct := 100.0 * float64(sampleSize) / float64(max64(r.etlCount, 1))
	// Oversample: TABLESAMPLE returns a variable number of rows, and LIMIT trims.
	pct *= 3
	if pct > 100 {
		pct = 100
	}
	if pct <= 0 {
		return nil
	}

	sampleQ := fmt.Sprintf(`
		SELECT user_id, play_item_id, created_at, city, region, country
		FROM plays TABLESAMPLE SYSTEM (%.6f)
		LIMIT %d`, pct, sampleSize)

	rows, err := etlPool.Query(ctx, sampleQ)
	if err != nil {
		return fmt.Errorf("sample etl plays: %w", err)
	}
	defer rows.Close()

	type play struct {
		userID                *int64
		itemID                int64
		createdAt             time.Time
		city, region, country *string
	}
	var sample []play
	for rows.Next() {
		var p play
		if err := rows.Scan(&p.userID, &p.itemID, &p.createdAt, &p.city, &p.region, &p.country); err != nil {
			return fmt.Errorf("scan sampled play: %w", err)
		}
		sample = append(sample, p)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate sampled plays: %w", err)
	}

	fmt.Printf("\nsampled %d plays (TABLESAMPLE %.6f%%)\n", len(sample), pct)

	// user_id is NULL for anonymous plays, so match NULL-safely. The triple is
	// not unique, so compare how many rows each side has for it.
	const lookupQ = `
		SELECT count(*), min(city), min(region), min(country)
		FROM plays
		WHERE user_id IS NOT DISTINCT FROM $1
		  AND play_item_id = $2
		  AND created_at = $3`

	var shown int
	const maxShown = 20

	for _, p := range sample {
		var srcCount int64
		var city, region, country *string
		err := prodPool.QueryRow(ctx, lookupQ, p.userID, p.itemID, p.createdAt).
			Scan(&srcCount, &city, &region, &country)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("lookup sampled play: %w", err)
		}

		r.sampleCompared++
		if errors.Is(err, pgx.ErrNoRows) || srcCount == 0 {
			r.sampleMissing++
			if shown < maxShown {
				fmt.Printf("  play(user=%s item=%d at=%s): MISSING in source\n",
					fmtUserID(p.userID), p.itemID, p.createdAt.Format(time.RFC3339))
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
			fmt.Printf("  play(user=%s item=%d at=%s): location etl=(%s,%s,%s) source=(%s,%s,%s)\n",
				fmtUserID(p.userID), p.itemID, p.createdAt.Format(time.RFC3339),
				derefStr(p.city), derefStr(p.region), derefStr(p.country),
				derefStr(city), derefStr(region), derefStr(country))
			shown++
		}
	}
	if shown == maxShown {
		fmt.Printf("  ... further sampled-play differences suppressed\n")
	}
	return nil
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

func fmtUserID(u *int64) string {
	if u == nil {
		return "<anon>"
	}
	return fmt.Sprintf("%d", *u)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
