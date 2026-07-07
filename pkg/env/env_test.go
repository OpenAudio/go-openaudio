package env

import "testing"

func TestGetInt64(t *testing.T) {
	t.Setenv("TEST_INT64_VALUE", "107374182400")
	if got := GetInt64(1, "TEST_INT64_VALUE"); got != 107374182400 {
		t.Fatalf("GetInt64() = %d, want 107374182400", got)
	}
}

func TestGetInt64InvalidUsesDefault(t *testing.T) {
	t.Setenv("TEST_INT64_VALUE", "not-an-int")
	if got := GetInt64(42, "TEST_INT64_VALUE"); got != 42 {
		t.Fatalf("GetInt64() = %d, want 42", got)
	}
}
