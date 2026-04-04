package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// compareTable defines how to compare a domain table across two databases.
type compareTable struct {
	Name       string
	IDCols     []string          // primary key column(s)
	Columns    []string          // columns to compare
	Where      string            // filter for rows to compare (applied to ETL db)
	ProdWhere  string            // filter for prod lookup (defaults to "is_current = true" if empty)
	KnownDiffs []string          // columns with known Python/Go divergence (reported separately)
	CastCols   map[string]string // column -> cast expression for SELECT (e.g. "save_type" -> "save_type::text")
}

var compareTables = []compareTable{
	{
		Name:   "users",
		IDCols: []string{"user_id"},
		Columns: []string{
			"handle", "name", "bio", "location",
			"profile_picture_sizes",
			"cover_photo_sizes",
			"is_verified", "is_deactivated",
			"wallet", "allow_ai_attribution",
		},
		Where: "is_current = true",
		// profile_picture and cover_photo are immutable in Python but updatable in Go
		KnownDiffs: []string{"profile_picture", "cover_photo"},
	},
	{
		Name:   "tracks",
		IDCols: []string{"track_id"},
		Columns: []string{
			"owner_id", "title", "genre", "mood", "tags", "description",
			"cover_art", "cover_art_sizes",
			"is_unlisted", "is_delete",
			"track_cid", "preview_cid", "orig_file_cid",
			"duration", "is_downloadable", "is_available",
			"is_stream_gated", "is_download_gated",
			"is_scheduled_release", "is_playlist_upload",
		},
		Where: "is_current = true AND is_delete = false",
	},
	{
		Name:   "playlists",
		IDCols: []string{"playlist_id"},
		Columns: []string{
			"playlist_owner_id", "playlist_name", "description",
			"is_album", "is_private",
			"playlist_image_sizes_multihash",
			"is_stream_gated", "is_scheduled_release",
		},
		Where: "is_current = true AND is_delete = false",
		// Python bug: playlist_image_multihash gets sizes_multihash value during create
		KnownDiffs: []string{"playlist_image_multihash"},
	},
	{
		Name:      "follows",
		IDCols:    []string{"follower_user_id", "followee_user_id"},
		Columns:   []string{"is_delete"},
		Where:     "is_current = true",
		ProdWhere: "is_current = true",
	},
	{
		Name:      "saves",
		IDCols:    []string{"user_id", "save_item_id", "save_type"},
		Columns:   []string{"is_delete"},
		Where:     "is_current = true",
		ProdWhere: "is_current = true",
		CastCols:  map[string]string{"save_type": "save_type::text"},
	},
	{
		Name:      "reposts",
		IDCols:    []string{"user_id", "repost_item_id", "repost_type"},
		Columns:   []string{"is_delete"},
		Where:     "is_current = true",
		ProdWhere: "is_current = true",
		CastCols:  map[string]string{"repost_type": "repost_type::text"},
	},
	{
		Name:      "subscriptions",
		IDCols:    []string{"subscriber_id", "user_id"},
		Columns:   []string{"is_delete"},
		Where:     "is_current = true",
		ProdWhere: "is_current = true",
	},
	{
		Name:    "comments",
		IDCols:  []string{"comment_id"},
		Columns: []string{"user_id", "entity_id", "entity_type", "text", "is_delete"},
		Where:   "is_delete = false",
	},
	{
		Name:    "grants",
		IDCols:  []string{"user_id", "grantee_address"},
		Columns: []string{"is_approved", "is_revoked"},
		Where:   "is_current = true",
	},
	{
		Name:    "developer_apps",
		IDCols:  []string{"address"},
		Columns: []string{"user_id", "name", "description", "is_delete"},
		Where:   "is_current = true",
	},
	{
		Name:    "muted_users",
		IDCols:  []string{"user_id", "muted_user_id"},
		Columns: []string{"is_delete"},
		Where:   "is_delete = false",
	},
	{
		Name:      "associated_wallets",
		IDCols:    []string{"user_id", "wallet"},
		Columns:   []string{"chain", "is_delete"},
		Where:     "is_current = true AND is_delete = false",
		ProdWhere: "is_current = true AND is_delete = false",
	},
	{
		Name:    "dashboard_wallet_users",
		IDCols:  []string{"user_id", "wallet"},
		Columns: []string{"is_delete"},
		Where:   "is_delete = false",
	},
}

// Compare connects to both the ETL clone and production DB and compares
// rows created after the snapshot boundary.
//
// The boundary is determined automatically: the first em_block written by
// the Go ETL is the lowest em_block in core_indexed_blocks that was NOT
// present in the original snapshot (i.e., the first non-NULL em_block
// written by us). Everything at or above that boundary is Go-written data.
func Compare(ctx context.Context, etlPool *pgxpool.Pool, prodPool *pgxpool.Pool) error {
	// Find the em_block boundary using etl_blocks, which only the Go ETL writes to.
	// The first etl_blocks row marks where Go started indexing. We then find the
	// minimum em_block assigned by Go in core_indexed_blocks for that height range.
	// Everything below that em_block is Python-written; everything at or above is Go-written.
	var minGoHeight, maxGoHeight int64
	err := etlPool.QueryRow(ctx,
		`SELECT MIN(block_height), MAX(block_height) FROM etl_blocks`).Scan(&minGoHeight, &maxGoHeight)
	if err != nil {
		return fmt.Errorf("no etl_blocks data — has the Go ETL run? %w", err)
	}

	// The boundary is the em_block just before the first Go-written em_block.
	var emBlockBoundary int64
	err = etlPool.QueryRow(ctx,
		`SELECT COALESCE(MIN(em_block) - 1, 0)
		 FROM core_indexed_blocks
		 WHERE em_block IS NOT NULL
		   AND height >= $1`, minGoHeight).Scan(&emBlockBoundary)
	if err != nil {
		return fmt.Errorf("determining em_block boundary: %w", err)
	}

	// Find the max em_block in prod that corresponds to the Go ETL's max chain height.
	// Any prod entity with blocknumber > this was modified after our comparison window.
	var prodMaxEmBlock int64
	err = prodPool.QueryRow(ctx,
		`SELECT COALESCE(MAX(em_block), 0)
		 FROM core_indexed_blocks
		 WHERE height <= $1 AND em_block IS NOT NULL`, maxGoHeight).Scan(&prodMaxEmBlock)
	if err != nil {
		return fmt.Errorf("determining prod em_block cutoff: %w", err)
	}

	fmt.Printf("=== ETL vs Production Comparison ===\n")
	fmt.Printf("em_block boundary:     %d (rows with blocknumber > this are Go-written)\n", emBlockBoundary)
	fmt.Printf("Go ETL chain heights:  %d .. %d\n", minGoHeight, maxGoHeight)
	fmt.Printf("Prod em_block cutoff:  %d (prod rows above this are beyond our window)\n", prodMaxEmBlock)
	fmt.Println()

	var totals struct {
		compared, matched, mismatched, missing, skippedAhead int
	}

	for _, ct := range compareTables {
		r, err := compareOneTable(ctx, etlPool, prodPool, ct, emBlockBoundary, prodMaxEmBlock)
		if err != nil {
			fmt.Printf("ERROR comparing %s: %v\n\n", ct.Name, err)
			continue
		}
		totals.compared += r.compared
		totals.matched += r.matched
		totals.mismatched += r.mismatched
		totals.missing += r.missing
		totals.skippedAhead += r.skippedAhead
	}

	fmt.Printf("=== Summary ===\n")
	fmt.Printf("Total compared: %d  matched: %d  mismatched: %d  missing_in_prod: %d  skipped(prod_ahead): %d\n",
		totals.compared, totals.matched, totals.mismatched, totals.missing, totals.skippedAhead)
	if totals.compared > 0 {
		fmt.Printf("Match rate: %.1f%%\n", float64(totals.matched)/float64(totals.compared)*100)
	}
	fmt.Println("=== Done ===")
	return nil
}

type compareResult struct {
	compared, matched, mismatched, missing, skippedAhead int
}

func compareOneTable(ctx context.Context, etlPool, prodPool *pgxpool.Pool, ct compareTable, emBlockBoundary, prodMaxEmBlock int64) (compareResult, error) {
	var r compareResult

	// castCol returns the SELECT expression for a column, applying casts if configured.
	castCol := func(col string) string {
		if ct.CastCols != nil {
			if expr, ok := ct.CastCols[col]; ok {
				return expr
			}
		}
		return col
	}

	// Build column list: IDs + blocknumber + compare columns + known-diff columns
	// allCols holds bare column names for indexing; selectCols holds SELECT expressions with casts.
	allCols := make([]string, 0, len(ct.IDCols)+1+len(ct.Columns)+len(ct.KnownDiffs))
	allCols = append(allCols, ct.IDCols...)
	allCols = append(allCols, "blocknumber")
	allCols = append(allCols, ct.Columns...)
	allCols = append(allCols, ct.KnownDiffs...)

	selectCols := make([]string, len(allCols))
	for i, col := range allCols {
		selectCols[i] = castCol(col)
	}
	colList := strings.Join(selectCols, ", ")

	idCount := len(ct.IDCols)
	bnIdx := idCount // blocknumber is right after IDs
	colStartIdx := idCount + 1
	knownDiffStartIdx := colStartIdx + len(ct.Columns)

	// Query ETL db for new rows
	where := fmt.Sprintf("blocknumber > %d", emBlockBoundary)
	if ct.Where != "" {
		where += " AND " + ct.Where
	}
	orderBy := strings.Join(ct.IDCols, ", ")
	etlQuery := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s", colList, ct.Name, where, orderBy)

	etlRows, err := etlPool.Query(ctx, etlQuery)
	if err != nil {
		return r, fmt.Errorf("query etl %s: %w", ct.Name, err)
	}
	defer etlRows.Close()

	type rowData struct {
		ids        []any
		blocknum   int64
		values     map[string]any
		knownDiffs map[string]any
	}
	var etlEntities []rowData

	for etlRows.Next() {
		vals := make([]any, len(allCols))
		ptrs := make([]any, len(allCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := etlRows.Scan(ptrs...); err != nil {
			return r, fmt.Errorf("scan etl %s: %w", ct.Name, err)
		}

		ids := make([]any, idCount)
		copy(ids, vals[:idCount])

		bn := toInt64(vals[bnIdx])

		m := make(map[string]any, len(ct.Columns))
		for i, col := range ct.Columns {
			m[col] = vals[colStartIdx+i]
		}

		kd := make(map[string]any, len(ct.KnownDiffs))
		for i, col := range ct.KnownDiffs {
			kd[col] = vals[knownDiffStartIdx+i]
		}

		etlEntities = append(etlEntities, rowData{ids: ids, blocknum: bn, values: m, knownDiffs: kd})
	}

	if len(etlEntities) == 0 {
		fmt.Printf("--- %s: no new rows to compare ---\n\n", ct.Name)
		return r, nil
	}

	fmt.Printf("--- %s: %d new rows ---\n", ct.Name, len(etlEntities))

	// Build prod lookup query
	prodWhere := "is_current = true"
	if ct.ProdWhere != "" {
		prodWhere = ct.ProdWhere
	}
	var idPredicates []string
	for i, col := range ct.IDCols {
		idPredicates = append(idPredicates, fmt.Sprintf("%s = $%d", col, i+1))
	}
	prodQuery := fmt.Sprintf("SELECT %s FROM %s WHERE %s AND %s LIMIT 1",
		colList, ct.Name, strings.Join(idPredicates, " AND "), prodWhere)

	var diffs []string
	var knownDiffCount int
	maxDiffsShown := 20

	for _, entity := range etlEntities {
		prodRow := prodPool.QueryRow(ctx, prodQuery, entity.ids...)
		prodVals := make([]any, len(allCols))
		prodPtrs := make([]any, len(allCols))
		for i := range prodVals {
			prodPtrs[i] = &prodVals[i]
		}

		if err := prodRow.Scan(prodPtrs...); err != nil {
			if err == pgx.ErrNoRows {
				r.missing++
				r.compared++
				if len(diffs) < maxDiffsShown {
					diffs = append(diffs, fmt.Sprintf("  %s: MISSING in production", fmtIDs(ct.IDCols, entity.ids)))
				}
			}
			continue
		}

		// Skip if prod modified this entity after our comparison window
		// (i.e., prod's blocknumber for this entity is beyond what the Go ETL's
		// max chain height maps to in prod's numbering).
		prodBN := toInt64(prodVals[bnIdx])
		if prodBN > prodMaxEmBlock {
			r.skippedAhead++
			continue
		}

		r.compared++

		// Compare standard columns
		prodMap := make(map[string]any, len(ct.Columns))
		for i, col := range ct.Columns {
			prodMap[col] = prodVals[colStartIdx+i]
		}

		rowMatch := true
		var rowDiffs []string
		for _, col := range ct.Columns {
			etlVal := entity.values[col]
			prodVal := prodMap[col]
			if !valuesEqual(etlVal, prodVal) {
				rowMatch = false
				rowDiffs = append(rowDiffs, fmt.Sprintf("    %s: etl=%v prod=%v", col, fmtVal(etlVal), fmtVal(prodVal)))
			}
		}

		// Check known-diff columns (report separately, don't count as mismatch)
		for i, col := range ct.KnownDiffs {
			etlVal := entity.knownDiffs[col]
			prodVal := prodVals[knownDiffStartIdx+i]
			if !valuesEqual(etlVal, prodVal) {
				knownDiffCount++
			}
		}

		if rowMatch {
			r.matched++
		} else {
			r.mismatched++
			if len(diffs) < maxDiffsShown {
				diffs = append(diffs, fmt.Sprintf("  %s:", fmtIDs(ct.IDCols, entity.ids)))
				diffs = append(diffs, rowDiffs...)
			}
		}
	}

	// Print results
	fmt.Printf("  Compared: %d  Matched: %d  Mismatched: %d  Missing: %d  Skipped(prod ahead): %d\n",
		r.compared, r.matched, r.mismatched, r.missing, r.skippedAhead)
	if r.compared > 0 {
		fmt.Printf("  Match rate: %.1f%%\n", float64(r.matched)/float64(r.compared)*100)
	}
	if knownDiffCount > 0 {
		fmt.Printf("  Known divergences (Python immutable/bug): %d rows\n", knownDiffCount)
	}
	if len(diffs) > 0 {
		fmt.Println("  Differences (first 20):")
		for _, d := range diffs {
			fmt.Println(d)
		}
		if r.mismatched+r.missing > maxDiffsShown {
			fmt.Printf("  ... and %d more\n", r.mismatched+r.missing-maxDiffsShown)
		}
	}
	fmt.Println()

	return r, nil
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func fmtIDs(cols []string, vals []any) string {
	parts := make([]string, len(cols))
	for i, col := range cols {
		parts[i] = fmt.Sprintf("%s=%v", col, vals[i])
	}
	return strings.Join(parts, ", ")
}

// valuesEqual compares two values from pgx scans, handling nil and type differences.
func valuesEqual(a, b any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		// Treat empty string as equivalent to nil for nullable text columns
		if a == nil {
			if s, ok := b.(string); ok && s == "" {
				return true
			}
			return false
		}
		if s, ok := a.(string); ok && s == "" {
			return true
		}
		return false
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func fmtVal(v any) string {
	if v == nil {
		return "<nil>"
	}
	s := fmt.Sprintf("%v", v)
	if len(s) > 80 {
		return s[:77] + "..."
	}
	return s
}
