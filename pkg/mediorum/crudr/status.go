package crudr

import (
	"context"
)

type TableStatus struct {
	EstimatedRows int64 `json:"estimated_rows"`
	HeapBytes     int64 `json:"heap_bytes"`
	IndexBytes    int64 `json:"index_bytes"`
	TotalBytes    int64 `json:"total_bytes"`
}

type CursorStatus struct {
	Count            int64  `json:"count"`
	MinLastULID      string `json:"min_last_ulid"`
	MaxLastULID      string `json:"max_last_ulid"`
	NullOrEmptyCount int64  `json:"null_or_empty_count"`
}

type Status struct {
	MinAvailableULID string                 `json:"min_available_ulid"`
	MaxULID          string                 `json:"max_ulid"`
	Tables           map[string]TableStatus `json:"tables"`
	Cursors          CursorStatus           `json:"cursors"`
}

type tableStatusRow struct {
	Name          string `gorm:"column:name"`
	EstimatedRows int64  `gorm:"column:estimated_rows"`
	HeapBytes     int64  `gorm:"column:heap_bytes"`
	IndexBytes    int64  `gorm:"column:index_bytes"`
	TotalBytes    int64  `gorm:"column:total_bytes"`
}

type opBoundsRow struct {
	MinULID string `gorm:"column:min_ulid"`
	MaxULID string `gorm:"column:max_ulid"`
}

type cursorStatusRow struct {
	Count            int64  `gorm:"column:count"`
	MinLastULID      string `gorm:"column:min_last_ulid"`
	MaxLastULID      string `gorm:"column:max_last_ulid"`
	NullOrEmptyCount int64  `gorm:"column:null_or_empty_count"`
}

func (c *Crudr) Status(ctx context.Context, tableNames []string) (*Status, error) {
	status := &Status{
		Tables: map[string]TableStatus{},
	}

	var bounds opBoundsRow
	if err := c.DB.WithContext(ctx).Raw(`
		SELECT
			coalesce((SELECT ulid FROM ops ORDER BY ulid ASC LIMIT 1), '') AS min_ulid,
			coalesce((SELECT ulid FROM ops ORDER BY ulid DESC LIMIT 1), '') AS max_ulid
	`).Scan(&bounds).Error; err != nil {
		return nil, err
	}
	status.MinAvailableULID = bounds.MinULID
	status.MaxULID = bounds.MaxULID

	var cursor cursorStatusRow
	if err := c.DB.WithContext(ctx).Raw(`
		SELECT
			count(*)::bigint AS count,
			coalesce(min(nullif(last_ulid, '')), '') AS min_last_ulid,
			coalesce(max(nullif(last_ulid, '')), '') AS max_last_ulid,
			count(*) FILTER (WHERE last_ulid IS NULL OR last_ulid = '')::bigint AS null_or_empty_count
		FROM cursors
	`).Scan(&cursor).Error; err != nil {
		return nil, err
	}
	status.Cursors = CursorStatus{
		Count:            cursor.Count,
		MinLastULID:      cursor.MinLastULID,
		MaxLastULID:      cursor.MaxLastULID,
		NullOrEmptyCount: cursor.NullOrEmptyCount,
	}

	if len(tableNames) == 0 {
		return status, nil
	}

	var rows []tableStatusRow
	if err := c.DB.WithContext(ctx).Raw(`
		SELECT c.relname AS name,
		       GREATEST(c.reltuples::bigint, 0) AS estimated_rows,
		       pg_relation_size(c.oid) AS heap_bytes,
		       pg_indexes_size(c.oid) AS index_bytes,
		       pg_total_relation_size(c.oid) AS total_bytes
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public'
		  AND c.relkind IN ('r', 'p')
		  AND c.relname IN ?
		ORDER BY c.relname
	`, tableNames).Scan(&rows).Error; err != nil {
		return nil, err
	}

	for _, row := range rows {
		status.Tables[row.Name] = TableStatus{
			EstimatedRows: row.EstimatedRows,
			HeapBytes:     row.HeapBytes,
			IndexBytes:    row.IndexBytes,
			TotalBytes:    row.TotalBytes,
		}
	}

	return status, nil
}
