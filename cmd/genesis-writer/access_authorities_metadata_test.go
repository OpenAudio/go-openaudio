package main

import (
	"encoding/json"
	"testing"
	"time"
)

// The two fields sit at different levels on purpose.
//
// access_authorities is protocol state: core parses the envelope in
// finalizeManageEntity, turns these wallets into management_keys and
// invalidates the track's stream-access cache. It belongs beside cid, where
// the protocol can read it without interpreting the opaque user payload, and
// in exactly one place so a client cannot submit two values that disagree.
//
// allowed_api_keys is not enforced by core at all -- it is only ever projected
// into a column by the indexer -- so it rides in the data payload with the
// rest of the track's descriptive fields.
func TestTrackMetadataPutsAccessFieldsAtTheRightLevel(t *testing.T) {
	authorities := []string{"0x8E6a0C5e8c93775D1F70Ac5591514A5E00BaC7f5"}
	apiKeys := []string{"8acf5eb7436ea403ee536a7334faa5e9ada4b50f"}
	src := sourceTrack{
		TrackID:           42,
		OwnerID:           7,
		CreatedAt:         time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		AccessAuthorities: authorities,
		AllowedAPIKeys:    apiKeys,
	}

	blob, err := json.Marshal(trackMetadataWrapper{
		CID:               "bagaaierackxyz",
		AccessAuthorities: src.AccessAuthorities,
		Data:              buildTrackMetadata(src, nil),
	})
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

	// Root: what core reads.
	rootAuth, ok := env["access_authorities"].([]any)
	if !ok || len(rootAuth) != 1 || rootAuth[0] != authorities[0] {
		t.Errorf("root access_authorities = %#v, want %#v", env["access_authorities"], authorities)
	}
	// One home only: a second copy invites core and the indexer to disagree.
	if _, dup := inner["access_authorities"]; dup {
		t.Error("access_authorities also emitted inside data; it must have a single source of truth")
	}

	// Payload: what the indexer projects.
	innerKeys, ok := inner["allowed_api_keys"].([]any)
	if !ok || len(innerKeys) != 1 || innerKeys[0] != apiKeys[0] {
		t.Errorf("data.allowed_api_keys = %#v, want %#v", inner["allowed_api_keys"], apiKeys)
	}
	if _, atRoot := env["allowed_api_keys"]; atRoot {
		t.Error("allowed_api_keys emitted at the root; core does not read it")
	}

	// Empty values stay omitted: the indexer treats a present key as an
	// instruction to write, including NULL, so emitting one would clobber.
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
	if _, present := bareEnv["access_authorities"]; present {
		t.Error("empty access_authorities was emitted; omitempty must drop it")
	}
	if bi, ok := bareEnv["data"].(map[string]any); ok {
		if _, present := bi["allowed_api_keys"]; present {
			t.Error("empty allowed_api_keys was emitted; omitempty must drop it")
		}
	}
}
