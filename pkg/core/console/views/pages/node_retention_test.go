package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	storagev1 "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
)

func TestGetArchiveRetentionType(t *testing.T) {
	tests := []struct {
		name   string
		status *corev1.GetStatusResponse
		want   string
	}{
		{
			name: "full node",
			status: &corev1.GetStatusResponse{
				PruningInfo: &corev1.GetStatusResponse_PruningInfo{Enabled: true},
				StorageInfo: &corev1.GetStatusResponse_StorageInfo{},
			},
			want: "Full node",
		},
		{
			name: "archive",
			status: &corev1.GetStatusResponse{
				PruningInfo: &corev1.GetStatusResponse_PruningInfo{Enabled: false},
				StorageInfo: &corev1.GetStatusResponse_StorageInfo{},
			},
			want: "Archive",
		},
		{
			name: "store all does not override full node",
			status: &corev1.GetStatusResponse{
				PruningInfo: &corev1.GetStatusResponse_PruningInfo{Enabled: true},
				StorageInfo: &corev1.GetStatusResponse_StorageInfo{StoreAll: true},
			},
			want: "Full node",
		},
		{
			name: "archive and store all",
			status: &corev1.GetStatusResponse{
				PruningInfo: &corev1.GetStatusResponse_PruningInfo{Enabled: false},
				StorageInfo: &corev1.GetStatusResponse_StorageInfo{StoreAll: true},
			},
			want: "Archive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getArchiveRetentionType(tt.status); got != tt.want {
				t.Fatalf("getArchiveRetentionType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetStoreAllEnabled(t *testing.T) {
	if getStoreAllEnabled(&corev1.GetStatusResponse{}) {
		t.Fatal("getStoreAllEnabled() = true, want false")
	}

	status := &corev1.GetStatusResponse{
		PruningInfo: &corev1.GetStatusResponse_PruningInfo{Enabled: false},
		StorageInfo: &corev1.GetStatusResponse_StorageInfo{StoreAll: true},
	}
	if !getStoreAllEnabled(status) {
		t.Fatal("getStoreAllEnabled() = false, want true")
	}
	if got := getArchiveRetentionType(status); got != "Archive" {
		t.Fatalf("getArchiveRetentionType() = %q, want %q", got, "Archive")
	}
}

func TestStorageOverviewSectionShowsStoreAllEnabled(t *testing.T) {
	var buf bytes.Buffer
	err := storageOverviewSection(&storagev1.GetStorageDiagnosticsResponse{StoreAll: true}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatal(err)
	}

	html := buf.String()
	if !strings.Contains(html, "STORE_ALL") {
		t.Fatalf("rendered storage overview missing STORE_ALL label: %s", html)
	}
	if !strings.Contains(html, "Enabled") {
		t.Fatalf("rendered storage overview missing enabled state: %s", html)
	}
}

func TestStorageOverviewSectionHidesStoreAllWhenDisabled(t *testing.T) {
	var buf bytes.Buffer
	err := storageOverviewSection(&storagev1.GetStorageDiagnosticsResponse{}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatal(err)
	}

	html := buf.String()
	if strings.Contains(html, "STORE_ALL") {
		t.Fatalf("rendered storage overview should hide STORE_ALL when disabled: %s", html)
	}
}
