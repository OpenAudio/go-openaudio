package entity_manager

// immutableFields lists metadata keys that cannot be modified by an Update
// action. On Update, mergeTrackFromMetadata / mergePlaylistFromMetadata
// strip these keys from the metadata map before merging so they can't
// override the stored values.

var immutableFields = map[string]struct{}{
	"blocknumber":        {},
	"blockhash":          {},
	"txhash":             {},
	"created_at":         {},
	"updated_at":         {},
	"slot":               {},
	"metadata_multihash": {},
	"is_current":         {},
	"is_delete":          {},
}

var immutableTrackFields = unionFields(immutableFields, map[string]struct{}{
	"track_id":     {},
	"owner_id":     {},
	"is_available": {},
	"ddex_app":     {},
})

var immutablePlaylistFields = unionFields(immutableFields, map[string]struct{}{
	"playlist_id":       {},
	"playlist_owner_id": {},
	"is_album":          {},
	"ddex_app":          {},
})

func unionFields(a, b map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		out[k] = struct{}{}
	}
	for k := range b {
		out[k] = struct{}{}
	}
	return out
}

// stripImmutableFields removes keys in `immutable` from the metadata map.
// Returns the same map (mutated in place) for fluent use. Safe on nil.
func stripImmutableFields(metadata map[string]any, immutable map[string]struct{}) map[string]any {
	if metadata == nil {
		return nil
	}
	for k := range immutable {
		delete(metadata, k)
	}
	return metadata
}
