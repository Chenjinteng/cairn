// Package version is the single source of truth for the cairn release version.
//
// Bumping the version is a coordinated change — see AGENTS.md §"版本号规则".
// The same value MUST be updated in lockstep at:
//
//  1. this file's Version constant
//  2. docker-compose.yml's ${IMAGE:-cairn:X.Y.Z}
//  3. .env.example's IMAGE=
//  4. README.md's docker build/tag/push examples
//  5. CHANGELOG.md (new section at the top)
//
// Missing any one causes version drift — the running binary disagrees with
// the image tag, the UI shows a stale version, or the CHANGELOG has no entry.
package version

// Version is the semantic version of the running binary.
// Format: 主.中.小 (Major.Minor.Patch). See AGENTS.md for the bump rules.
const Version = "0.3.0"

// UserAgent is the value sent on outbound registry requests. Useful for
// allowlists / log filtering on the upstream registry.
//
// Includes the version so operators can correlate a registry's access log
// entry with the cairn release that produced it.
const UserAgent = "cairn/" + Version