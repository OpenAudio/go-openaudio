package entity_manager

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractRedirectURIs(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		want      []string
		isPresent bool
		isNull    bool
	}{
		{name: "happy path", raw: `{"redirect_uris":["https://a","https://b"]}`, want: []string{"https://a", "https://b"}, isPresent: true},
		{name: "missing", raw: `{}`, isPresent: false},
		{name: "explicit null", raw: `{"redirect_uris":null}`, isPresent: true, isNull: true},
		{name: "non-list dropped", raw: `{"redirect_uris":"oops"}`, isPresent: false},
		{name: "non-string entry skipped", raw: `{"redirect_uris":["ok",1]}`, isPresent: false},
		{name: "over-length URI rejects whole list", raw: `{"redirect_uris":["` + strings.Repeat("x", MaxRedirectURILength+1) + `"]}`, isPresent: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var meta map[string]any
			_ = json.Unmarshal([]byte(tt.raw), &meta)
			uris, present, isNull := extractRedirectURIs(meta)
			if present != tt.isPresent {
				t.Errorf("present = %v, want %v", present, tt.isPresent)
			}
			if isNull != tt.isNull {
				t.Errorf("isNull = %v, want %v", isNull, tt.isNull)
			}
			if len(uris) != len(tt.want) {
				t.Fatalf("uris = %v, want %v", uris, tt.want)
			}
			for i := range uris {
				if uris[i] != tt.want[i] {
					t.Errorf("[%d] = %q, want %q", i, uris[i], tt.want[i])
				}
			}
		})
	}
}

func TestDeveloperAppCreate_IndexesRedirectURIs(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	uid := int64(UserIDOffset + 1200)
	seedUser(t, pool, uid, "0xappOwner", "appownr")

	addr := "0xdeadbeef00000000000000000000000000000001"
	meta := `{"name":"My App","address":"` + addr + `","redirect_uris":["https://a.example","https://b.example"]}`
	mustHandle(t, DeveloperAppCreate(),
		buildParams(t, pool, EntityTypeDeveloperApp, ActionCreate, uid, 0, "0xappOwner", meta))

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM oauth_redirect_uris WHERE client_id = $1",
		addr).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 redirect URI rows, got %d", count)
	}
}

func TestReplaceRedirectURIs_DeletesAndInserts(t *testing.T) {
	pool := setupTestDB(t)
	seedBlock(t, pool)
	addr := "0xdeadbeef00000000000000000000000000000002"

	if err := replaceRedirectURIs(context.Background(), pool, addr, []string{"https://old1", "https://old2", "https://old3"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := replaceRedirectURIs(context.Background(), pool, addr, []string{"https://new"}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	var count int
	if err := pool.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM oauth_redirect_uris WHERE client_id = $1",
		addr).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row after replace, got %d", count)
	}

	var uri string
	if err := pool.QueryRow(context.Background(),
		"SELECT redirect_uri FROM oauth_redirect_uris WHERE client_id = $1",
		addr).Scan(&uri); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if uri != "https://new" {
		t.Errorf("uri = %q, want https://new", uri)
	}
}
