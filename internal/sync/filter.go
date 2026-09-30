package sync

import "strings"

// DenyFilter decides whether a given repository name should be excluded
// from a sync run. It is a closed-set match (exact, case-sensitive,
// no regex) per docs/ROADMAP.md "不做正则" — the user base asks for
// "deny these repos" not "deny repos matching a pattern".
//
// The empty deny list matches every repo. Order doesn't matter; we
// build a map on construction so lookup is O(1) per repo.
type DenyFilter struct {
	denied map[string]struct{}
}

// NewDenyFilter builds the filter from a slice of repo names. Whitespace
// is trimmed and empties are dropped so callers don't have to sanitise.
// Nil input is treated as no filter (every repo allowed).
func NewDenyFilter(deny []string) *DenyFilter {
	f := &DenyFilter{denied: map[string]struct{}{}}
	if len(deny) == 0 {
		return f
	}
	for _, raw := range deny {
		r := strings.TrimSpace(raw)
		if r == "" {
			continue
		}
		f.denied[r] = struct{}{}
	}
	return f
}

// Allows reports whether the given repo name is permitted. Repos that
// exactly match a deny entry are rejected; everything else passes.
// An empty filter allows everything.
func (f *DenyFilter) Allows(repo string) bool {
	if f == nil || len(f.denied) == 0 {
		return true
	}
	_, hit := f.denied[strings.TrimSpace(repo)]
	return !hit
}

// Len returns the number of deny entries. Useful for the UI ("3 repos
// denied") and for tests asserting construction.
func (f *DenyFilter) Len() int {
	if f == nil {
		return 0
	}
	return len(f.denied)
}

// PrefixFilter narrows the source catalog to a single subtree (e.g. only
// repos under "library/" on Docker Hub, or under a team's namespace on
// a private registry). Empty prefix = match everything.
//
// The prefix matches by leading-segment, not substring: a prefix of
// "team-a/" matches "team-a/web" but NOT "team-a-extra/web". The
// trailing slash is the segment delimiter and is required when the
// caller wants subtree behaviour.
type PrefixFilter struct {
	prefix string
}

// NewPrefixFilter stores the prefix verbatim. Empty is allowed (means
// no filter) and is matched as "everything".
func NewPrefixFilter(prefix string) *PrefixFilter {
	return &PrefixFilter{prefix: prefix}
}

// Allows reports whether the repo name starts with the configured prefix.
// Empty prefix → true.
func (p *PrefixFilter) Allows(repo string) bool {
	if p == nil || p.prefix == "" {
		return true
	}
	return strings.HasPrefix(repo, p.prefix)
}

// String returns the configured prefix. Empty string when no filter.
func (p *PrefixFilter) String() string {
	if p == nil {
		return ""
	}
	return p.prefix
}