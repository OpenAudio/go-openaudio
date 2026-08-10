package main

import (
	"encoding/json"
	"testing"
	"time"
)

// TestBuildTrackMetadataCarriesStateFlags pins the boolean track columns that
// the ETL reads back out of metadata (see pkg/etl/processors/entity_manager/
// track_create.go). A flag the writer never emits indexes as false no matter
// what the source row said, and the loss is silent: the migrated row is
// well-formed, just wrong.
func TestBuildTrackMetadataCarriesStateFlags(t *testing.T) {
	src := sourceTrack{
		TrackID:            2064779,
		OwnerID:            141804191,
		Title:              strPtr("Grand Piano Solo Melody Azure"),
		IsScheduledRelease: true,
		IsPlaylistUpload:   true,
		IsCustomBPM:        true,
		IsCustomMusicalKey: true,
		CommentsDisabled:   true,
		NoAIUse:            true,
		IsUnlisted:         true,
		IsStreamGated:      true,
		IsAvailable:        true,
		CreatedAt:          time.Unix(1700000000, 0).UTC(),
	}

	got := decodeTrackMetadata(t, buildTrackMetadata(src, nil))

	for _, key := range []string{
		"is_scheduled_release",
		"is_playlist_upload",
		"is_custom_bpm",
		"is_custom_musical_key",
		"comments_disabled",
		"no_ai_use",
		"is_unlisted",
		"is_stream_gated",
	} {
		v, ok := got[key]
		if !ok {
			t.Errorf("metadata is missing %q: the source row set it, so the migrated track would index as false", key)
			continue
		}
		if v != true {
			t.Errorf("metadata[%q] = %v, want true", key, v)
		}
	}
}

// TestBuildTrackMetadataOmitsFalseStateFlags documents the serialization
// contract for these six flags. Unlike is_delete/is_available, the ETL reads
// each of them with MetadataBoolOr(..., false), so "absent" and "false" index
// identically and omitempty is safe -- it keeps the flags off the majority of
// tracks that do not set them. is_available must still be present on a false
// value, because its ETL default is true.
func TestBuildTrackMetadataOmitsFalseStateFlags(t *testing.T) {
	got := decodeTrackMetadata(t, buildTrackMetadata(sourceTrack{TrackID: 1}, nil))

	for _, key := range []string{
		"is_scheduled_release",
		"is_playlist_upload",
		"is_custom_bpm",
		"is_custom_musical_key",
		"comments_disabled",
		"no_ai_use",
	} {
		if _, ok := got[key]; ok {
			t.Errorf("metadata[%q] should be omitted when false", key)
		}
	}
	for _, key := range []string{"is_delete", "is_available"} {
		if _, ok := got[key]; !ok {
			t.Errorf("metadata[%q] must always be serialized, even when false", key)
		}
	}
}

func decodeTrackMetadata(t *testing.T, inner trackMetadataInner) map[string]any {
	t.Helper()
	raw, err := json.Marshal(inner)
	if err != nil {
		t.Fatalf("marshal track metadata: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal track metadata: %v", err)
	}
	return got
}

func strPtr(s string) *string { return &s }
