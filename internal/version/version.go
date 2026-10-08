// Package version is the single source of truth for the cairn release version.
//
// Bumping the version is a coordinated change — see AGENTS.md §"版本号规则".
// The same value MUST be updated in lockstep at:
//
//  1. this file's Version constant
//  2. docker-compose.yml's ${IMAGE:-cairn:X.Y.Z}      (v0.5.23: cairn → cairn)
//  3. .env.example's IMAGE=cairn:X.Y.Z                  (v0.5.23: cairn → cairn)
//  4. README.md's docker build/tag/push examples
//  5. CHANGELOG.md (new section at the top)
//
// Missing any one causes version drift — the running binary disagrees with
// the image tag, the UI shows a stale version, or the CHANGELOG has no entry.
package version

// Version is the semantic version of the running binary.
// Format: 主.中.小 (Major.Minor.Patch). See AGENTS.md for the bump rules.
const Version = "0.7.53"

// UserAgent is the value sent on outbound registry requests. Useful for
// allowlists / log filtering on the upstream registry.
//
// Includes the version so operators can correlate a registry's access log
// entry with the cairn release that produced it.
//
// v0.5.23: 保留 "cairn/" 前缀而不是改成 "cairn/" —— 对外 registry
// 看到 user-agent 时,cairn 是大家能搜得到的项目代号,改前缀会让
// 上游 registry 的 allowlist / log 关联断开。这条决策是「外部可识别性」
// 优先于「内部品牌一致」。
const UserAgent = "cairn/" + Version

// v0.6.29: 同步流量 + 探测流量也走版本派生,不再写死字面量版本号。
//
// 历史教训:SyncUserAgent / ProbeUserAgent 之前是 writer.go / probe.go 里
// `"cairn-sync/0.6.14"`、`"cairn-sync-probe/0.6.14"` 这样的硬编码字面量,
// 每次发版手动 bump 一次 —— 到 0.6.14 就停了,直接漂到 0.6.28 才被 UAT
// 关注起来("cairn-sync/0.6.14 这个同步的版本要跟着软件版本走")。
//
// 修法:跟 UserAgent 同款,从 Version 派生。bump Version 时这里自动跟上,
// 未来再也不会漂。
//
// 前缀保留 "cairn-sync/" 和 "cairn-sync-probe/" —— 让上游 registry 能从
// UA 区分流量类型:SyncUserAgent 是真实同步任务,ProbeUserAgent 是 "测试
// 连接" 按钮的探测请求(0.6.5 引入独立 UA 的初衷,详见 CHANGELOG 该条)。
const (
	SyncUserAgent  = "cairn-sync/" + Version
	ProbeUserAgent = "cairn-sync-probe/" + Version
)
