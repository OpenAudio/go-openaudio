package etl

import (
	"testing"
	"time"
)

func TestMigrationBlockTime(t *testing.T) {
	fb := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, meta string
		want       string // "" => fallback
	}{
		{"nested data envelope (track/user/playlist)",
			`{"cid":"x","data":{"title":"t","created_at":"2021-05-04T10:11:12Z"}}`, "2021-05-04T10:11:12Z"},
		{"flat social metadata",
			`{"created_at":"2020-01-02T03:04:05Z","is_delete":false}`, "2020-01-02T03:04:05Z"},
		{"postgres-style timestamp", `{"created_at":"2022-11-28 07:08:09"}`, "2022-11-28T07:08:09Z"},
		{"absent -> fallback", `{"data":{"title":"t"}}`, ""},
		{"malformed json -> fallback", `{oops`, ""},
		{"empty -> fallback", ``, ""},
		{"non-string created_at -> fallback", `{"created_at":12345}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := migrationBlockTime(c.meta, fb)
			if c.want == "" {
				if !got.Equal(fb) {
					t.Fatalf("expected fallback %v, got %v", fb, got)
				}
				return
			}
			want, _ := time.Parse(time.RFC3339, c.want)
			if !got.Equal(want) {
				t.Fatalf("expected %v, got %v", want, got)
			}
		})
	}
}
