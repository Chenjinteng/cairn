package sync

import (
	"path/filepath"
	"strings"
)

// Filter decides which repositories a task should process.
//
// Semantics:
//   include = ""        → match every repo (no filter)
//   include = "lib/*"   → match repos starting with "lib/"
//   include = "a\nb"    → match either pattern (newline-separated, OR)
//
// Patterns are filepath.Match globs against the full repo name
// ("library/nginx", not just "nginx"). `*` does NOT cross the slash
// because filepath.Match treats '/' as a separator by default — so
// "lib/*" matches "lib/nginx" but NOT "lib/sub/nginx". If operators
// want multi-segment globs they can use "lib/**" pattern but that's
// not currently interpreted specially).
//
// Bad patterns (e.g. unclosed `[`) are silently skipped rather than
// failing the whole run — the engine logs them and continues. Operators
// see the include string via the task edit modal so they can fix typos.
type Filter struct {
	patterns []string // pre-parsed; nil/empty == match all
}

// NewFilter parses an include string into a Filter.
//
// Whitespace-only lines are dropped (so trailing newlines don't create
// spurious empty patterns). Empty input returns a filter that matches
// everything.
func NewFilter(include string) *Filter {
	if strings.TrimSpace(include) == "" {
		return &Filter{}
	}
	var patterns []string
	for _, line := range strings.Split(include, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		patterns = append(patterns, line)
	}
	return &Filter{patterns: patterns}
}

// Match reports whether repo passes this filter (true = include).
func (f *Filter) Match(repo string) bool {
	if len(f.patterns) == 0 {
		return true
	}
	for _, p := range f.patterns {
		ok, err := filepath.Match(p, repo)
		if err != nil {
			// Malformed pattern — skip rather than fail. See Filter doc.
			continue
		}
		if ok {
			return true
		}
	}
	return false
}

// Apply returns the subset of repos that pass this filter, preserving
// input order. A nil/empty filter returns the input slice unchanged.
//
// Implementation note: we reuse the input backing array via in-place
// reslicing when at least one item is dropped. This is safe because the
// caller is iterating sync results and doesn't hold separate references
// to the original slice after this call.
func (f *Filter) Apply(repos []string) []string {
	if len(f.patterns) == 0 {
		return repos
	}
	out := repos[:0]
	for _, r := range repos {
		if f.Match(r) {
			out = append(out, r)
		}
	}
	return out
}