package main

import (
	"encoding/json"
	"testing"
	"time"
)

// allowed_api_keys restricts which API keys may access a track. The writer
// never selected the column, so it was dropped on all 185 tracks in the
// snapshot that carry one, and a migrated catalog would report an empty list
// for every one of them.
//
// It belongs in the data payload: nothing in core reads it, so it is purely a
// field the indexer projects into a column, alongside the track's other
// descriptive attributes.
func TestAllowedAPIKeysRideInTheDataPayload(t *testing.T) {
	apiKeys := []string{"8acf5eb7436ea403ee536a7334faa5e9ada4b50f"}
	src := sourceTrack{
		TrackID:        42,
		OwnerID:        7,
		CreatedAt:      time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		AllowedAPIKeys: apiKeys,
	}

	blob, err := json.Marshal(trackMetadataWrapper{CID: "bagaaierackxyz", Data: buildTrackMetadata(src, nil)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var env map[string]any
	if err := json.Unmarshal(blob, &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	inner, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data envelope in %s", blob)
	}

	got, ok := inner["allowed_api_keys"].([]any)
	if !ok || len(got) != 1 || got[0] != apiKeys[0] {
		t.Errorf("data.allowed_api_keys = %#v, want %#v", inner["allowed_api_keys"], apiKeys)
	}
	if _, atRoot := env["allowed_api_keys"]; atRoot {
		t.Error("allowed_api_keys emitted at the envelope root; the indexer reads it from the payload")
	}

	// Empty stays omitted: the indexer treats a present key as an instruction
	// to write, including NULL, so emitting one would clobber the column.
	bare, err := json.Marshal(trackMetadataWrapper{
		CID:  "bagaaierackxyz",
		Data: buildTrackMetadata(sourceTrack{TrackID: 43, OwnerID: 7, CreatedAt: src.CreatedAt}, nil),
	})
	if err != nil {
		t.Fatalf("marshal bare: %v", err)
	}
	var bareEnv map[string]any
	if err := json.Unmarshal(bare, &bareEnv); err != nil {
		t.Fatalf("unmarshal bare: %v", err)
	}
	if bi, ok := bareEnv["data"].(map[string]any); ok {
		if _, present := bi["allowed_api_keys"]; present {
			t.Error("empty allowed_api_keys was emitted; omitempty must drop it")
		}
	}
}
