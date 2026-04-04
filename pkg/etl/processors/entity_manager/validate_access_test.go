package entity_manager

import (
	"encoding/json"
	"strings"
	"testing"
)

func paramsWithMeta(t *testing.T, meta string) *Params {
	t.Helper()
	p := &Params{}
	if meta != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(meta), &m); err != nil {
			t.Fatalf("invalid test metadata: %v", err)
		}
		p.Metadata = m
	}
	return p
}

func TestValidateAccessConditions_NoGatingFields(t *testing.T) {
	p := paramsWithMeta(t, `{"title":"hello"}`)
	if err := ValidateAccessConditions(p); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateAccessConditions_ValidStreamGated(t *testing.T) {
	meta := `{
		"is_stream_gated": true,
		"is_download_gated": true,
		"stream_conditions": {"tip_user_id": 1},
		"download_conditions": {"tip_user_id": 1}
	}`
	p := paramsWithMeta(t, meta)
	if err := ValidateAccessConditions(p); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateAccessConditions_ValidDownloadOnlyGated(t *testing.T) {
	meta := `{
		"is_stream_gated": false,
		"is_download_gated": true,
		"download_conditions": {"follow_user_id": 1}
	}`
	p := paramsWithMeta(t, meta)
	if err := ValidateAccessConditions(p); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateAccessConditions_ValidUSDCPurchase(t *testing.T) {
	meta := `{
		"is_stream_gated": true,
		"is_download_gated": true,
		"stream_conditions": {"usdc_purchase": {"price": 100, "splits": [{"user_id": 1, "percentage": 100}]}},
		"download_conditions": {"usdc_purchase": {"price": 100, "splits": [{"user_id": 1, "percentage": 100}]}}
	}`
	p := paramsWithMeta(t, meta)
	if err := ValidateAccessConditions(p); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateAccessConditions_StreamGatedNoConditions(t *testing.T) {
	meta := `{"is_stream_gated": true, "is_download_gated": true}`
	p := paramsWithMeta(t, meta)
	err := ValidateAccessConditions(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "must have stream_conditions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccessConditions_StreamGatedMultipleConditions(t *testing.T) {
	meta := `{
		"is_stream_gated": true,
		"is_download_gated": true,
		"stream_conditions": {"tip_user_id": 1, "follow_user_id": 2},
		"download_conditions": {"tip_user_id": 1, "follow_user_id": 2}
	}`
	p := paramsWithMeta(t, meta)
	err := ValidateAccessConditions(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "exactly one condition") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccessConditions_StreamGatedNotDownloadGated(t *testing.T) {
	meta := `{
		"is_stream_gated": true,
		"is_download_gated": false,
		"stream_conditions": {"tip_user_id": 1}
	}`
	p := paramsWithMeta(t, meta)
	err := ValidateAccessConditions(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "must also be download gated") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccessConditions_StreamDownloadConditionsMismatch(t *testing.T) {
	meta := `{
		"is_stream_gated": true,
		"is_download_gated": true,
		"stream_conditions": {"tip_user_id": 1},
		"download_conditions": {"follow_user_id": 2}
	}`
	p := paramsWithMeta(t, meta)
	err := ValidateAccessConditions(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "must match") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccessConditions_DownloadGatedNoConditions(t *testing.T) {
	meta := `{"is_stream_gated": false, "is_download_gated": true}`
	p := paramsWithMeta(t, meta)
	err := ValidateAccessConditions(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "must have download_conditions") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccessConditions_DownloadGatedMultipleConditions(t *testing.T) {
	meta := `{
		"is_stream_gated": false,
		"is_download_gated": true,
		"download_conditions": {"tip_user_id": 1, "follow_user_id": 2}
	}`
	p := paramsWithMeta(t, meta)
	err := ValidateAccessConditions(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "exactly one condition") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccessConditions_StemCannotBeGated(t *testing.T) {
	meta := `{
		"is_stream_gated": true,
		"is_download_gated": true,
		"stream_conditions": {"tip_user_id": 1},
		"download_conditions": {"tip_user_id": 1},
		"stem_of": {"parent_track_id": 123}
	}`
	p := paramsWithMeta(t, meta)
	err := ValidateAccessConditions(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "stem tracks cannot") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccessConditions_USDCMissingSplits(t *testing.T) {
	meta := `{
		"is_stream_gated": true,
		"is_download_gated": true,
		"stream_conditions": {"usdc_purchase": {"price": 100}},
		"download_conditions": {"usdc_purchase": {"price": 100}}
	}`
	p := paramsWithMeta(t, meta)
	err := ValidateAccessConditions(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "splits") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAccessConditions_USDCEmptySplits(t *testing.T) {
	meta := `{
		"is_stream_gated": true,
		"is_download_gated": true,
		"stream_conditions": {"usdc_purchase": {"price": 100, "splits": []}},
		"download_conditions": {"usdc_purchase": {"price": 100, "splits": []}}
	}`
	p := paramsWithMeta(t, meta)
	err := ValidateAccessConditions(p)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "splits cannot be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}
