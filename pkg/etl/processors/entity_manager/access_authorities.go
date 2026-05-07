package entity_manager

import (
	"context"
	"strings"

	"github.com/OpenAudio/go-openaudio/etl/db"
)

// applyAccessNormalization writes any present allowed_api_keys /
// access_authorities values onto the current tracks row. Called after
// the main INSERT/UPDATE so it can use UPDATE statements without bloating
// the giant track INSERT with two more optional columns.
func applyAccessNormalization(ctx context.Context, dbtx db.DBTX, trackID int64, metadata map[string]any) error {
	if metadata == nil {
		return nil
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
	if vals, present, isNull := normalizeAccessAuthorities(metadata); present {
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
