package entity_manager

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/OpenAudio/go-openaudio/pkg/etl/db"
)

// applyAccessNormalization writes any present allowed_api_keys /
// access_authorities values onto the current tracks row. Called after
// the main INSERT/UPDATE so it can use UPDATE statements without bloating
// the giant track INSERT with two more optional columns.
func applyAccessNormalization(ctx context.Context, dbtx db.DBTX, trackID int64, metadata map[string]any, rawMetadata string) error {
	if metadata == nil {
		return nil
	}
	// access_authorities is read from the root of the envelope, not from the
	// data payload this function otherwise works on, because that is where
	// core reads it: finalizeManageEntity parses the envelope and turns these
	// wallets into management_keys, then invalidates the track's stream-access
	// cache. Projecting the column from a different field than the one the
	// protocol enforces lets the two disagree -- a client can put one list at
	// the root and another in the payload, and core would register the first
	// while this column showed the second.
	//
	// The payload is still honoured when the root carries nothing, for clients
	// that predate root support. That mirrors the reference indexer, which
	// promotes the root value into the payload when the payload lacks it
	// (apps#13810); the difference is which side wins when both are present,
	// and it has to be the side core acts on.
	authoritySource := metadata
	if root, ok := parseEnvelopeRoot(rawMetadata); ok {
		if _, present := root["access_authorities"]; present {
			authoritySource = root
		}
	}
	if vals, present, isNull := normalizeAllowedAPIKeys(metadata); present {
		var arg any
		if isNull {
			arg = nil
		} else {
			arg = vals
		}
		if _, err := dbtx.Exec(ctx, `
			UPDATE tracks SET allowed_api_keys = $2 WHERE track_id = $1 AND is_current = true
		`, trackID, arg); err != nil {
			return err
		}
	}
	if vals, present, isNull := normalizeAccessAuthorities(authoritySource); present {
		var arg any
		if isNull {
			arg = nil
		} else {
			arg = vals
		}
		if _, err := dbtx.Exec(ctx, `
			UPDATE tracks SET access_authorities = $2 WHERE track_id = $1 AND is_current = true
		`, trackID, arg); err != nil {
			return err
		}
	}
	return nil
}

// normalizeAllowedAPIKeys returns a lowercased copy of metadata.allowed_api_keys.
// (present=true, isNull=true) → set the column to NULL.
// (present=false) → key missing, leave the column unchanged.
func normalizeAllowedAPIKeys(metadata map[string]any) (value []string, present bool, isNull bool) {
	raw, ok := metadata["allowed_api_keys"]
	if !ok {
		return nil, false, false
	}
	if raw == nil {
		return nil, true, true
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, true, false
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		s, ok := v.(string)
		if !ok {
			continue
		}
		out = append(out, strings.ToLower(s))
	}
	return out, true, false
}

// normalizeAccessAuthorities returns a trimmed-string copy of
// metadata.access_authorities. Non-list inputs and non-string entries
// are silently filtered out.
func normalizeAccessAuthorities(metadata map[string]any) (value []string, present bool, isNull bool) {
	raw, ok := metadata["access_authorities"]
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
			continue
		}
		out = append(out, strings.TrimSpace(s))
	}
	return out, true, false
}

// parseEnvelopeRoot returns the top level of the transaction's metadata
// envelope -- {"cid": ..., "access_authorities": ..., "data": {...}} -- which
// Params.Metadata has already discarded in favour of the inner payload.
func parseEnvelopeRoot(rawMetadata string) (map[string]any, bool) {
	if rawMetadata == "" {
		return nil, false
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(rawMetadata), &root); err != nil {
		return nil, false
	}
	return root, true
}
