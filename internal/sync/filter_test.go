package sync

import (
	"reflect"
	"testing"
)

// TestParseTagsFilter covers the v0.7.21 catalog-bypass parser. The
// function deliberately swallows bad input (mirrors Filter's tolerance
// for bad globs) so the engine doesn't abort a healthy run on a typo;
// these tests pin down what "swallowed" means so future changes don't
// silently regress.
func TestParseTagsFilter(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []TagSpec
	}{
		{
			name: "empty input → nil (caller falls through to catalog path)",
			in:   "",
			want: nil,
		},
		{
			name: "whitespace-only input → nil",
			in:   "   \n  \n",
			want: nil,
		},
		{
			name: "single spec",
			in:   "bklite/alpine/openssl:3.5.4",
			want: []TagSpec{{Repository: "bklite/alpine/openssl", Tag: "3.5.4"}},
		},
		{
			name: "multiple specs, newline-separated",
			in: "library/nginx:1.27\nlibrary/redis:7.4-alpine",
			want: []TagSpec{
				{Repository: "library/nginx", Tag: "1.27"},
				{Repository: "library/redis", Tag: "7.4-alpine"},
			},
		},
		{
			name: "comment lines skipped (# prefix, with surrounding whitespace)",
			in: "# this is a comment\nlibrary/nginx:1.27\n   # indented comment\n",
			want: []TagSpec{{Repository: "library/nginx", Tag: "1.27"}},
		},
		{
			name: "empty lines between specs skipped",
			in: "library/nginx:1.27\n\n\nlibrary/redis:7.4\n",
			want: []TagSpec{
				{Repository: "library/nginx", Tag: "1.27"},
				{Repository: "library/redis", Tag: "7.4"},
			},
		},
		{
			name: "trimmed whitespace around repo and tag",
			in: "  library/nginx  :  1.27  ",
			want: []TagSpec{{Repository: "library/nginx", Tag: "1.27"}},
		},
		{
			name: "bad line (no colon) dropped, others kept",
			in: "library/nginx:1.27\nlibrary/redis-without-tag\nlibrary/postgres:16",
			want: []TagSpec{
				{Repository: "library/nginx", Tag: "1.27"},
				{Repository: "library/postgres", Tag: "16"},
			},
		},
		{
			name: "bad line (empty repo after split) dropped",
			in: ":justatag\nlibrary/nginx:1.27",
			want: []TagSpec{{Repository: "library/nginx", Tag: "1.27"}},
		},
		{
			name: "bad line (empty tag) dropped",
			in: "library/nginx:\nlibrary/redis:7.4",
			want: []TagSpec{{Repository: "library/redis", Tag: "7.4"}},
		},
		{
			name: "all-bad input → nil (caller still falls through to catalog path)",
			in:   "not-a-spec\n:just-a-colon\nstill-not-a-spec:",
			want: nil,
		},
		{
			name: "tag with embedded colons? — first colon wins (strings.Cut semantics)",
			in:   "library/nginx:1.27:extra",
			want: []TagSpec{{Repository: "library/nginx", Tag: "1.27:extra"}},
		},
		{
			name: "all-comment input → nil",
			in:   "# nothing\n# here\n   # at all",
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTagsFilter(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseTagsFilter(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseTagsFilter_DoesNotMutate verifies the parser is read-only
// against the input. (Trivial now — strings.Split on a copy doesn't
// mutate — but pinning it down makes a future "TrimSpace in place"
// refactor visibly fail here instead of at engine-run time.)
func TestParseTagsFilter_DoesNotMutate(t *testing.T) {
	in := "  library/nginx:1.27  \n"
	snapshot := in
	_ = ParseTagsFilter(in)
	if in != snapshot {
		t.Errorf("input mutated: %q → %q", snapshot, in)
	}
}
