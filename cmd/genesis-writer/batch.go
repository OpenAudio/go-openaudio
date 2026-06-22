package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

// processBatched streams rows from srcDB using selectQuery and calls emit for
// each row. Emit is called concurrently from a worker pool of NumCPU goroutines,
// so emit must be safe for concurrent use (addManageEntity is, via blockMu).
//
// It first runs countQuery to log total count for progress. The scan function
// receives pgx.Rows and must call rows.Scan to populate a value, then return it.
// pgx v5 streams results by default, so no LIMIT/OFFSET is needed.
func processBatched[T any](
	ctx context.Context,
	w *Writer,
	name string,
	countQuery string,
	selectQuery string,
	scan func(pgx.Rows) (T, error),
	emit func(context.Context, T) error,
) error {
	var total int64
	if err := w.srcDB.QueryRow(ctx, countQuery).Scan(&total); err != nil {
		return fmt.Errorf("count %s: %w", name, err)
	}
	if total == 0 {
		w.logger.Info("no rows", zap.String("entity", name))
		return nil
	}
	w.logger.Info("processing", zap.String("entity", name), zap.Int64("total", total))

	rows, err := w.srcDB.Query(ctx, selectQuery)
	if err != nil {
		return fmt.Errorf("query %s: %w", name, err)
	}
	defer rows.Close()

	workers := runtime.NumCPU()
	batchSize := w.cfg.BatchSize
	if batchSize <= 0 {
		batchSize = workers * 2
	}

	var processed int64
	batch := make([]T, 0, batchSize)

	// processBatch signs and emits a batch of items concurrently.
	processBatch := func(items []T) error {
		var wg sync.WaitGroup
		errs := make([]error, len(items))

		// Use a semaphore to limit concurrency to NumCPU.
		sem := make(chan struct{}, workers)
		for i, item := range items {
			wg.Add(1)
			sem <- struct{}{}
			go func(idx int, it T) {
				defer wg.Done()
				defer func() { <-sem }()
				errs[idx] = emit(ctx, it)
			}(i, item)
		}
		wg.Wait()

		for i, err := range errs {
			if err != nil {
				return fmt.Errorf("emit %s row %d: %w", name, processed-int64(len(items))+int64(i), err)
			}
		}
		return nil
	}

	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return fmt.Errorf("scan %s row %d: %w", name, processed, err)
		}
		batch = append(batch, item)
		processed++

		if len(batch) >= batchSize {
			if err := processBatch(batch); err != nil {
				return err
			}
			batch = batch[:0]
		}

		if processed%100000 == 0 {
			w.logger.Info("progress", zap.String("entity", name), zap.Int64("processed", processed), zap.Int64("total", total))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("rows %s: %w", name, err)
	}

	// Process remaining items.
	if len(batch) > 0 {
		if err := processBatch(batch); err != nil {
			return err
		}
	}

	w.logger.Info("done", zap.String("entity", name), zap.Int64("processed", processed))
	return nil
}

// preloadMap runs a two-column query and returns a map from the first column to
// a slice of values from the second column. Useful for pre-loading related data
// (e.g. comment threads, email access grants).
func preloadMap[K comparable, V any](ctx context.Context, db *pgxpool.Pool, query string) (map[K][]V, error) {
	rows, err := db.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	m := make(map[K][]V)
	for rows.Next() {
		var k K
		var v V
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = append(m[k], v)
	}
	return m, rows.Err()
}

// deref safely dereferences a string pointer, returning "" if nil.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// derefInt safely dereferences an int pointer, returning 0 if nil.
func derefInt(i *int) int {
	if i == nil {
		return 0
	}
	return *i
}

// unmarshalJSONB unmarshals a JSONB byte slice into an interface{} value.
// Returns nil if the input is empty or invalid JSON.
func unmarshalJSONB(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	return v
}
