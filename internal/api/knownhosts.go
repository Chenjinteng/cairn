package api

import (
	"fmt"
	"net/url"
	"strings"
)

// maxKnownHosts caps pull.known_hosts. The list feeds a UI autocomplete and
// an image-reference lookup map — dozens of entries already cover any realistic
// deployment, and a hard cap keeps a fat-fingered paste from bloating the
// settings row.
const maxKnownHosts = 64

// normalizeHostCSV validates and canonicalises the pull.known_hosts value.
//
// Input is a comma-separated list where each entry is one of:
//
//   - "https://registry.example.com"  — explicit scheme wins verbatim;
//   - "http://harbor.local:8080"      — the only way to force plain http
//     on a port that would otherwise guess https;
//   - "registry.example.com"          — bare host: guessed https;
//   - "harbor.local:8080"             — bare host:port: guessed http, unless
//     the port is 443/8443/5000 (same heuristic as the frontend's
//     inferProtocol, so parser and stored form never disagree).
//
// Output entries are "<scheme>://host[:port]" with a lowercased host, deduped
// by host[:port] (first wins), joined by ",". Empty/whitespace tokens are
// dropped; an all-empty input yields "" which the caller persists as "clear
// the override".
func normalizeHostCSV(raw string) (string, error) {
	parts := strings.Split(raw, ",")
	if len(parts) > maxKnownHosts {
		return "", fmt.Errorf("too many entries (max %d)", maxKnownHosts)
	}
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		host, base, err := normalizeKnownHost(p)
		if err != nil {
			return "", err
		}
		if _, dup := seen[host]; dup {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, base)
	}
	return strings.Join(out, ","), nil
}

// normalizeKnownHost canonicalises a single entry and returns its dedup key
// (lowercased host[:port]) plus the stored base URL.
func normalizeKnownHost(entry string) (host, base string, err error) {
	s := strings.TrimSpace(entry)
	explicit := strings.Contains(s, "://")
	candidate := s
	if !explicit {
		candidate = "https://" + s
	}
	u, parseErr := url.Parse(candidate)
	if parseErr != nil {
		return "", "", fmt.Errorf("%q is not a valid registry host: %v", s, parseErr)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", fmt.Errorf("%q: scheme must be http or https", s)
	}
	if u.Host == "" {
		return "", "", fmt.Errorf("%q: missing host", s)
	}
	if u.User != nil {
		return "", "", fmt.Errorf("%q: credentials are not part of a registry host entry", s)
	}
	if u.Path != "" && u.Path != "/" {
		return "", "", fmt.Errorf("%q: paths are not allowed — one registry host per entry", s)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", "", fmt.Errorf("%q: query/fragment are not allowed", s)
	}

	host = strings.ToLower(u.Host)
	scheme := u.Scheme
	if !explicit {
		// Bare entry: mirror the frontend's inferProtocol heuristic so the
		// stored form and what the pull page would have guessed agree.
		scheme = "https"
		if port := u.Port(); port != "" && port != "443" && port != "8443" && port != "5000" {
			scheme = "http"
		}
	}
	return host, scheme + "://" + host, nil
}
