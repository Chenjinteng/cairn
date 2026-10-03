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

// TagSpec is one parsed "repo:tag" line from a task's TagsFilter (v0.7.21).
// Repository carries the leading path-style prefix (e.g. "library/nginx");
// Tag is the literal tag string (e.g. "1.27-alpine") with no normalization.
type TagSpec struct {
	Repository string
	Tag        string
}

// ParseTagsFilter decodes a task's TagsFilter string into a list of
// (repo, tag) specs. Format and error handling mirror NewFilter above:
//
//   - "" or whitespace-only input → empty result (caller falls through
//     to the catalog path; same as a task without TagsFilter at all)
//   - newline-separated; empty lines dropped
//   - '#' prefix marks a comment (whole line skipped)
//   - bad rows (no colon, empty repo, empty tag) are silently dropped —
//     the same way malformed globs are in Filter — rather than failing
//     the whole run. Operators see the raw text via the edit modal.
//
// The engine dispatches on the result: a non-empty list switches
// runPull into the "skip /v2/_catalog and /v2/<name>/tags/list, fetch
// each spec's manifest directly" branch; an empty result falls through
// to the original ListRepositories → Filter → ListTags path.
//
// Symmetry note: NewFilter trims the input first; we only TrimSpace
// each line, NOT the whole input — the whole string is meant to be the
// raw textarea value from the UI.
func ParseTagsFilter(spec string) []TagSpec {
	if strings.TrimSpace(spec) == "" {
		return nil
	}
	var out []TagSpec
	for _, line := range strings.Split(spec, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// strings.Cut is the Go 1.18+ idiom for "split on first occurrence".
		// Tags never contain ':' (per OCI / docker conventions), so first
		// colon is unambiguous. Use raw strings.Cut — not SplitN — so a
		// hypothetical future "digest@sha256:..." stays a single token.
		repo, tag, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		repo = strings.TrimSpace(repo)
		tag = strings.TrimSpace(tag)
		if repo == "" || tag == "" {
			continue
		}
		out = append(out, TagSpec{Repository: repo, Tag: tag})
	}
	return out
}