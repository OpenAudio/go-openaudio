package entity_manager

import (
	"bytes"
	"testing"
)

// The DDEX processor sets these on sdk.tracks.uploadTrack and the indexer
// dropped every one of them. The playlist path already read the same fields.
func TestTrackDDEXFieldsReadFromMetadata(t *testing.T) {
	base := &trackRow{}
	p := &Params{Metadata: map[string]any{
		"artists":                        []any{map[string]any{"name": "A"}},
		"resource_contributors":          []any{map[string]any{"name": "B"}},
		"indirect_resource_contributors": []any{map[string]any{"name": "C"}},
		"rights_controller":              map[string]any{"name": "D"},
		"copyright_line":                 map[string]any{"text": "E"},
		"producer_copyright_line":        map[string]any{"text": "F"},
		"parental_warning_type":          "Explicit",
		"is_original_available":          true,
	}}

	out := mergeTrackFromMetadata(p, base)

	for name, got := range map[string][]byte{
		"artists":                        out.Artists,
		"resource_contributors":          out.ResourceContributors,
		"indirect_resource_contributors": out.IndirectContributors,
		"rights_controller":              out.RightsController,
		"copyright_line":                 out.CopyrightLine,
		"producer_copyright_line":        out.ProducerCopyrightLine,
	} {
		if len(got) == 0 || bytes.Equal(got, []byte("null")) {
			t.Errorf("%s = %s, want the metadata value", name, got)
		}
	}
	if out.ParentalWarningType == nil || *out.ParentalWarningType != "Explicit" {
		t.Errorf("parental_warning_type = %v, want Explicit", out.ParentalWarningType)
	}
	if !out.IsOriginalAvailable {
		t.Error("is_original_available = false, want true")
	}
}

// Absent metadata must leave the existing values alone, matching how every
// other field on this row merges.
func TestTrackDDEXFieldsPreservedWhenAbsent(t *testing.T) {
	base := &trackRow{
		Artists:             []byte(`[{"name":"kept"}]`),
		IsOriginalAvailable: true,
	}
	out := mergeTrackFromMetadata(&Params{Metadata: map[string]any{}}, base)
	if !bytes.Contains(out.Artists, []byte("kept")) {
		t.Errorf("artists = %s, want the existing value preserved", out.Artists)
	}
	if !out.IsOriginalAvailable {
		t.Error("is_original_available was cleared by an absent key")
	}
}
