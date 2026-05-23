package sing

import (
	"strings"
	"testing"
)

func TestNormalizeLegacyDNSAddress(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"1.1.1.1", "1.1.1.1"},
		{"tcp:1.1.1.1", "tcp://1.1.1.1"},
		{"tcp://1.1.1.1", "tcp://1.1.1.1"},
		{"tcp:1.1.1.1:853", "tcp://1.1.1.1:853"},
		{"udp:8.8.8.8", "udp://8.8.8.8"},
		{"udp://8.8.8.8:53", "udp://8.8.8.8:53"},
		{"udp:8.8.8.8:53", "udp://8.8.8.8:53"},
	}
	for _, tt := range tests {
		if got := normalizeLegacyDNSAddress(tt.in); got != tt.want {
			t.Fatalf("normalizeLegacyDNSAddress(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWithNormalizedSingOriginDNS(t *testing.T) {
	raw := []byte(`{
  "dns": {
    "servers": [
      {"tag": "cf-udp", "address": "udp:1.1.1.1"},
      {"tag": "cf-tcp", "address": "tcp:1.1.1.1:853"}
    ]
  }
}`)
	out := withNormalizedSingOriginDNS(raw)
	if !strings.Contains(string(out), `"address":"udp://1.1.1.1"`) {
		t.Fatalf("udp address not normalized: %s", out)
	}
	if !strings.Contains(string(out), `"address":"tcp://1.1.1.1:853"`) {
		t.Fatalf("tcp address not normalized: %s", out)
	}
}
