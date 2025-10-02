package server

import (
	"testing"

	"github.com/OpenAudio/go-openaudio/pkg/core/config"
)

func TestIsNonRoutableAddress(t *testing.T) {
	tests := []struct {
		name        string
		addr        string
		environment string
		expected    bool
	}{
		// Test cases for invalid address formats (should return true)
		{
			name:     "invalid address format - no port",
			addr:     "192.168.1.1",
			expected: true,
		},
		{
			name:     "invalid address format - empty string",
			addr:     "",
			expected: true,
		},
		{
			name:     "invalid address format - malformed",
			addr:     "not:a:valid:address:format",
			expected: true,
		},

		// Test cases for localhost hostname
		{
			name:     "localhost hostname",
			addr:     "localhost:8080",
			expected: true,
		},

		// Test cases for other hostnames (should return false)
		{
			name:     "example.com hostname",
			addr:     "example.com:80",
			expected: false,
		},
		{
			name:     "google.com hostname",
			addr:     "google.com:443",
			expected: false,
		},

		// Test cases for IPv4 loopback addresses
		{
			name:     "IPv4 loopback 127.0.0.1",
			addr:     "127.0.0.1:8080",
			expected: true,
		},
		{
			name:     "IPv4 loopback 127.1.2.3",
			addr:     "127.1.2.3:9000",
			expected: true,
		},

		// Test cases for IPv6 loopback addresses
		{
			name:     "IPv6 loopback ::1",
			addr:     "[::1]:8080",
			expected: true,
		},

		// Test cases for IPv4 private addresses
		{
			name:     "IPv4 private 10.0.0.1",
			addr:     "10.0.0.1:8080",
			expected: true,
		},
		{
			name:     "IPv4 private 172.16.0.1",
			addr:     "172.16.0.1:8080",
			expected: true,
		},
		{
			name:     "IPv4 private 192.168.1.1",
			addr:     "192.168.1.1:8080",
			expected: true,
		},
		{
			name:     "IPv4 private edge case 10.255.255.255",
			addr:     "10.255.255.255:8080",
			expected: true,
		},
		{
			name:     "IPv4 private edge case 172.31.255.255",
			addr:     "172.31.255.255:8080",
			expected: true,
		},
		{
			name:     "IPv4 private edge case 192.168.255.255",
			addr:     "192.168.255.255:8080",
			expected: true,
		},

		// Test cases for IPv6 private addresses
		{
			name:     "IPv6 private fc00::",
			addr:     "[fc00::]:8080",
			expected: true,
		},
		{
			name:     "IPv6 private fd12:3456:789a::",
			addr:     "[fd12:3456:789a::]:8080",
			expected: true,
		},

		// Test cases for link-local addresses
		{
			name:     "IPv4 link-local 169.254.1.1",
			addr:     "169.254.1.1:8080",
			expected: true,
		},
		{
			name:     "IPv6 link-local fe80::1",
			addr:     "[fe80::1]:8080",
			expected: true,
		},

		// Test cases for unspecified addresses
		{
			name:     "IPv4 unspecified 0.0.0.0",
			addr:     "0.0.0.0:8080",
			expected: true,
		},
		{
			name:     "IPv6 unspecified ::",
			addr:     "[::]:8080",
			expected: true,
		},

		// Test cases for public/routable IPv4 addresses
		{
			name:     "public IPv4 8.8.8.8",
			addr:     "8.8.8.8:53",
			expected: false,
		},
		{
			name:     "public IPv4 1.1.1.1",
			addr:     "1.1.1.1:53",
			expected: false,
		},
		{
			name:     "public IPv4 172.15.255.255", // Just outside private range
			addr:     "172.15.255.255:8080",
			expected: false,
		},
		{
			name:     "public IPv4 172.32.0.1", // Just outside private range
			addr:     "172.32.0.1:8080",
			expected: false,
		},

		// Test cases for public/routable IPv6 addresses
		{
			name:     "public IPv6 2001:4860:4860::8888",
			addr:     "[2001:4860:4860::8888]:53",
			expected: false,
		},
		{
			name:     "public IPv6 2606:4700:4700::1111",
			addr:     "[2606:4700:4700::1111]:53",
			expected: false,
		},

		// Edge cases with different port formats
		{
			name:     "IPv4 with high port number",
			addr:     "8.8.8.8:65535",
			expected: false,
		},
		{
			name:     "IPv6 with brackets and port",
			addr:     "[2001:db8::1]:80",
			expected: false,
		},
		{
			name:     "localhost with port 0",
			addr:     "localhost:0",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{
				config: &config.Config{
					Environment: tt.environment,
				},
			}
			result := s.isNonRoutableAddress(tt.addr)
			if result != tt.expected {
				t.Errorf("Server.isNonRoutableAddress(%q) = %v, expected %v (env: %s)", tt.addr, result, tt.expected, tt.environment)
			}
		})
	}
}

// Additional test to verify specific IP classification behaviors
func TestIsNonRoutableAddress_IPClassifications(t *testing.T) {
	// Test boundary conditions for private IP ranges
	boundaryTests := []struct {
		name     string
		addr     string
		expected bool
		reason   string
	}{
		{"9.255.255.255:8080", "9.255.255.255:8080", false, "just before 10.0.0.0/8"},
		{"11.0.0.1:8080", "11.0.0.1:8080", false, "just after 10.255.255.255"},
		{"172.15.255.255:8080", "172.15.255.255:8080", false, "just before 172.16.0.0/12"},
		{"172.32.0.1:8080", "172.32.0.1:8080", false, "just after 172.31.255.255"},
		{"192.167.255.255:8080", "192.167.255.255:8080", false, "just before 192.168.0.0/16"},
		{"192.169.0.1:8080", "192.169.0.1:8080", false, "just after 192.168.255.255"},
	}

	for _, tt := range boundaryTests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{
				config: &config.Config{
					Environment: "prod", // Use prod environment for boundary tests
				},
			}
			result := s.isNonRoutableAddress(tt.addr)
			if result != tt.expected {
				t.Errorf("Server.isNonRoutableAddress(%q) = %v, expected %v (%s)", tt.addr, result, tt.expected, tt.reason)
			}
		})
	}
}
