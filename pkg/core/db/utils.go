package db

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func (q *Queries) ToPgxTimestamp(t time.Time) pgtype.Timestamp {
	return pgtype.Timestamp{
		Time:  t,
		Valid: true,
	}
}
