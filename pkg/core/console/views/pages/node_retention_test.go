package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	corev1 "github.com/OpenAudio/go-openaudio/pkg/api/core/v1"
	storagev1 "github.com/OpenAudio/go-openaudio/pkg/api/storage/v1"
)

func TestGetNodeRetentionType(t *testing.T) {
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
			name: "store all",
			status: &corev1.GetStatusResponse{
				PruningInfo: &corev1.GetStatusResponse_PruningInfo{Enabled: true},
				StorageInfo: &corev1.GetStatusResponse_StorageInfo{StoreAll: true},
			},
			want: "Store All",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getNodeRetentionType(tt.status); got != tt.want {
				t.Fatalf("getNodeRetentionType() = %q, want %q", got, tt.want)
			}
		})
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
