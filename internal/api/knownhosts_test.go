package api

import (
	"strings"
	"testing"
)

// normalizeHostCSV is the write-time gate for pull.known_hosts (v0.5.48).
// These cases pin the contract the frontend parser mirrors: explicit scheme
// wins, bare entries guess https (no port / 443 / 8443 / 5000) or http
// (any other port), host is lowercased, dedup by host[:port] first-wins.
func TestNormalizeHostCSV(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty clears", "", ""},
		{"whitespace only clears", " , ,", ""},
		{
			"bare host guesses https",
			"nvcr.io",
			"https://nvcr.io",
		},
		{
			"bare host:port guesses http",
			"harbor.local:8080",
			"http://harbor.local:8080",
		},
		{
			"bare well-known secure ports stay https",
			"a.example:443,b.example:8443,c.example:5000",
			"https://a.example:443,https://b.example:8443,https://c.example:5000",
		},
		{
			"explicit scheme wins verbatim",
			"http://proxy.example.com:10001,https://reg.internal:9000",
			"http://proxy.example.com:10001,https://reg.internal:9000",
		},
		{
			"host lowercased",
			"HARBOR.LOCAL:8080,HTTPS://Reg.Example",
			"http://harbor.local:8080,https://reg.example",
		},
		{
			"dedup by host, first wins",
			"https://quay.io,http://quay.io,quay.io",
			"https://quay.io",
		},
		{
			"trailing slash trimmed via url.Host",
			"https://quay.io/",
			"https://quay.io",
		},
		{
			"spaces around entries tolerated",
			"  quay.io , https://ghcr.io  ",
			"https://quay.io,https://ghcr.io",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeHostCSV(tc.in)
			if err != nil {
				t.Fatalf("normalizeHostCSV(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeHostCSV(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeHostCSVRejects(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantSub string // substring the error must carry, for a useful failure message
	}{
		{"path rejected", "https://quay.io/v2", "paths are not allowed"},
		{"credentials rejected", "https://user:pass@quay.io", "credentials"},
		{"bare credentials rejected", "user:pass@quay.io", "credentials"},
		{"foreign scheme rejected", "ftp://quay.io", "scheme must be http or https"},
		{"empty host rejected", "https://", "missing host"},
		{"garbage rejected", "://quay.io", "not a valid registry host"},
		{"query rejected", "https://quay.io?x=1", "query/fragment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeHostCSV(tc.in)
			if err == nil {
				t.Fatalf("normalizeHostCSV(%q) accepted, want rejection containing %q", tc.in, tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("normalizeHostCSV(%q) error = %v, want substring %q", tc.in, err, tc.wantSub)
			}
		})
	}
}

func TestNormalizeHostCSVCap(t *testing.T) {
	in := make([]string, maxKnownHosts+1)
	for i := range in {
		in[i] = "h" + strings.Repeat("x", 1) + string(rune('a'+i%26)) + ".example"
	}
	if _, err := normalizeHostCSV(strings.Join(in, ",")); err == nil {
		t.Fatalf("accepted %d entries, want cap error at %d", len(in), maxKnownHosts)
	}
	// Exactly at the cap is fine.
	if _, err := normalizeHostCSV(strings.Join(in[:maxKnownHosts], ",")); err != nil {
		t.Fatalf("rejected %d entries: %v", maxKnownHosts, err)
	}
}
