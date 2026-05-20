package entity_manager

import (
	"context"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
)

// MaxRedirectURIs caps how many redirect URIs a single developer app may register.
const MaxRedirectURIs = 50

// MaxRedirectURILength caps each individual URI string length.
const MaxRedirectURILength = 2000

// extractRedirectURIs reads the metadata's redirect_uris field, dropping
// invalid entries (non-string, over-length, or non-list inputs) and capping
// the list at MaxRedirectURIs. Returns isNull=true for explicit null in
// metadata; present=false when the key is missing entirely.
func extractRedirectURIs(metadata map[string]any) (uris []string, present bool, isNull bool) {
	raw, ok := metadata["redirect_uris"]
	if !ok {
		return nil, false, false
	}
	if raw == nil {
		return nil, true, true
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, false, false
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			return nil, false, false
		}
		if len(s) > MaxRedirectURILength {
			return nil, false, false
		}
		out = append(out, s)
	}
	if len(out) > MaxRedirectURIs {
		out = out[:MaxRedirectURIs]
	}
	return out, true, false
}

// replaceRedirectURIs sets the redirect URI list for a developer app:
// clears existing rows for the client_id and inserts the new set.
func replaceRedirectURIs(ctx context.Context, dbtx db.DBTX, clientID string, uris []string) error {
	if _, err := dbtx.Exec(ctx, "DELETE FROM oauth_redirect_uris WHERE client_id = $1", clientID); err != nil {
		return err
	}
	for _, uri := range uris {
		if _, err := dbtx.Exec(ctx,
			"INSERT INTO oauth_redirect_uris (client_id, redirect_uri) VALUES ($1, $2)",
			clientID, uri,
		); err != nil {
			return err
		}
	}
	return nil
}
