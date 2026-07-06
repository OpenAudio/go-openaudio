package eth

import "testing"

func TestEthRPCURLHelpers(t *testing.T) {
	tests := []struct {
		name      string
		rawURL    string
		wantRead  string
		wantWatch string
	}{
		{
			name:      "https provider",
			rawURL:    "https://eth-validator.audius.co",
			wantRead:  "https://eth-validator.audius.co",
			wantWatch: "wss://eth-validator.audius.co",
		},
		{
			name:      "http provider",
			rawURL:    "http://eth-ganache:8545",
			wantRead:  "http://eth-ganache:8545",
			wantWatch: "ws://eth-ganache:8545",
		},
		{
			name:      "wss provider",
			rawURL:    "wss://eth-validator.audius.co",
			wantRead:  "https://eth-validator.audius.co",
			wantWatch: "wss://eth-validator.audius.co",
		},
		{
			name:      "ws provider",
			rawURL:    "ws://eth-ganache:8545",
			wantRead:  "http://eth-ganache:8545",
			wantWatch: "ws://eth-ganache:8545",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ethReadRPCURL(tt.rawURL); got != tt.wantRead {
				t.Fatalf("ethReadRPCURL(%q) = %q, want %q", tt.rawURL, got, tt.wantRead)
			}
			if got := ethWatchRPCURL(tt.rawURL); got != tt.wantWatch {
				t.Fatalf("ethWatchRPCURL(%q) = %q, want %q", tt.rawURL, got, tt.wantWatch)
			}
		})
	}
}
