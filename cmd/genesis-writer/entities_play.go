package main

import (
	"context"
	"fmt"
	"time"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const playsPerTransaction = 20

type sourcePlay struct {
	UserID    *string
	TrackID   string
	CreatedAt time.Time
	City      *string
	Region    *string
	Country   *string
}

func (w *Writer) writePlays(ctx context.Context) error {
	var total int64
	if err := w.srcDB.QueryRow(ctx, `SELECT count(*) FROM plays`).Scan(&total); err != nil {
		return fmt.Errorf("count plays: %w", err)
	}
	if total == 0 {
		w.logger.Info("no rows", zap.String("entity", "plays"))
		return nil
	}
	w.logger.Info("processing", zap.String("entity", "plays"), zap.Int64("total", total))

	rows, err := w.srcDB.Query(ctx,
		`SELECT user_id::text, play_item_id::text, created_at, city, region, country
		FROM plays
		ORDER BY created_at, play_item_id`)
	if err != nil {
		return fmt.Errorf("query plays: %w", err)
	}
	defer rows.Close()

	var batch []*corev1.TrackPlay
	var processed int64

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := w.addTrackPlays(ctx, &corev1.TrackPlays{Plays: batch}); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for rows.Next() {
		var p sourcePlay
		if err := rows.Scan(&p.UserID, &p.TrackID, &p.CreatedAt, &p.City, &p.Region, &p.Country); err != nil {
			return fmt.Errorf("scan play row %d: %w", processed, err)
		}

		tp := &corev1.TrackPlay{
			TrackId:   p.TrackID,
			Timestamp: timestamppb.New(p.CreatedAt),
		}
		if p.UserID != nil {
			tp.UserId = *p.UserID
		}
		if p.City != nil {
			tp.City = *p.City
		}
		if p.Region != nil {
			tp.Region = *p.Region
		}
		if p.Country != nil {
			tp.Country = *p.Country
		}

		batch = append(batch, tp)
		if len(batch) >= playsPerTransaction {
			if err := flush(); err != nil {
				return fmt.Errorf("flush plays at row %d: %w", processed, err)
			}
		}

		processed++
		if processed%1000000 == 0 {
			w.logger.Info("progress", zap.String("entity", "plays"), zap.Int64("processed", processed), zap.Int64("total", total))
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("plays rows: %w", err)
	}

	if err := flush(); err != nil {
		return fmt.Errorf("flush remaining plays: %w", err)
	}

	w.logger.Info("done", zap.String("entity", "plays"), zap.Int64("processed", processed))
	return nil
}
