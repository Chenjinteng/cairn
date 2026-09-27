# cairn 路线图

本文件记录**已排定的号位**与选题范围。版本号规则见 [`AGENTS.md`](../AGENTS.md)：
新增模块 / 用户可感知的新能力 → 中版本 +1；缺陷修复与既有功能优化（含韧性、工程化）→ 小版本 +1；
主版本必须由人指定。当前版本以 `internal/version/version.go` 为准。

## 号位总览

| 号位 | 主题 | 改动性质 | 状态 |
| --- | --- | --- | --- |
| 0.5.11 | ID 唯一性收口：`newID` 随机后缀 16 → 32 bit | 缺陷修复 | **已发布**（2026-09-26） |
| 0.5.12 | 代理交互轮：探测反馈 · 新增即测 · 一键探测全部 | 新能力（中版本） | **已发布**（2026-09-26） |
| 0.5.13 | 代理管理：新增弹窗内「测试连接」（保存前试连） | 新能力（中版本） | **已发布**（2026-09-26） |
| 0.5.14 | 设置页改版：删除 6 处字段级「✏️ 编辑」，只留全局「编辑」 | 优化（小版本） | **已发布**（2026-09-26） |
| 0.5.15 | 代理可达性：探测改为 ip:端口连通性 + 延迟；「测试连接」搬进编辑弹窗 | 优化（小版本） | **已发布**（2026-09-26） |
| 0.5.16 | 拉取任务崩溃修复：`platformAllow` 读实时配置 · 队列 panic 兜底 | 缺陷修复 | **已发布**（2026-09-27） |
| 0.5.17 | 拉取预检误报修复：前端消费探测结论 · 后端校验源镜像存在性 | 缺陷修复 | **待发布**（本轮） |
| 0.5.18 | 韧性轮：前端不再假死 · 后端不再阻塞 | 缺陷修复 / 优化 | 待开工 |
| 0.5.19 | 工程化：最小 CI · `-race` 守门 | 工程化 | 待开工 |
| 0.6.0 | TLS 证书管理 | 新模块（中版本） | 号位已定，待开工 |

> **号位是预留，不是承诺。** 中途插入更高优先级的 hotfix 时，它占用顺位号位，本表自上而下整体顺延；
> `0.6.0` 由人指定，不随顺延改号。

## 0.5.18 · 韧性轮

### 背景

v0.5.10 修掉的是**服务端凭据库写锁泄漏**（`internal/credentials/credentials.go` 的 `Put`
持锁写盘后漏解锁）——该锁不再释放，之后任何取锁的请求（读或写）都永久排队，直到整站挂死。
这解释了「前端操作卡死」的一条通路，但只解释了一条。

系统性 review 后确认还有**第二条通路**（后端存储层的锁与取消机制），以及一批「不阻塞进程、
但让页面永久假死」的前端缺陷。两类叠加会产生同样的用户观感，因此合并在同一轮收敛。

### B 组 · 后端抗阻塞

| # | 位置 | 问题 | 影响 |
| --- | --- | --- | --- |
| B1 | `internal/storage/filesystem.go:546-551` `lockRepo` | 裸阻塞等锁，不接 `context.Context` | **最高危**：单个慢操作让同仓库请求永久排队 |
| B2 | `internal/storage/filesystem.go:670` `DeleteRepository` / `:699` `GC` | 全树遍历，且收到的 ctx 被 `_` 丢弃 | 与 B1 叠加构成第二条「整站卡死」通路 |
| B3 | `internal/storage/filesystem.go` 19 个方法 | 签名只收 `_ context.Context` | 取消 / 超时全链路失效 |
| B4 | `internal/proxies/proxies.go` | 缺 `writeMu` 写者串行化（`internal/credentials/credentials.go:94` 已有范式） | 并发写各自快照先后落盘 → last-writer-wins，静默丢更新 |
| B5 | `internal/pull/queue.go:114` `IsTerminal()` | 无锁读（调用点 `:298` / `:319`） | 数据竞争，`-race` 下可复现 |
| B6 | `internal/events/events.go:586-587` | 事件 ID 用 `UnixNano` | 与 v0.5.10 同类的「拿时钟当唯一性」缺陷 |
| B7 | `internal/api/handlers_extra.go:1314` `newID()` | 毫秒时间戳 + 仅 16 bit 随机后缀 | 同毫秒并发撞车 → 凭据 / 代理按 id 覆盖，静默丢数据 → **0.5.11 已修**（后缀加宽到 32 bit，回归测试由 **13/20 FAIL** 转为 20/20 PASS），不再占用本轮验收 |

**B3 明细**（19 处签名，全部只收 `_ context.Context`）：
`:59` `Repositories` · `:98` `Tags` · `:118` `TagDigest` · `:136` `GetManifest` · `:179` `PutManifest` ·
`:215` `DeleteManifest` · `:268` `TagsForDigest` · `:303` `BlobExists` · `:317` `GetBlob` · `:337` `StatBlob` ·
`:353` `StartUpload` · `:370` `PatchUpload` · `:402` `PutUpload` · `:457` `GetUpload` · `:483` `CancelUpload` ·
`:496` `Stats` · `:644` `ManifestDigests` · `:670` `DeleteRepository` · `:699` `GC`

### 复核结论：`internal/registry`（不阻塞）

`CachedRegistry.Inventory` 的锁点 `:67`/`:70`/`:73`（快路径）与 `:79`/`:82`（回填缓存）**均成对解锁，无泄漏**。
但 `:61-64` 的注释声称「并发调用共享同一次在途扫描（singleflight-style）」，实现只是 TTL 缓存、
没有 in-flight 去重：缓存过期的瞬间，每个并发调用都会各自跑一次 `:75` `ScanInventory`。
实际羊群规模被调用方 errgroup 的 worker 上限约束，故列为**观察项**，不在本轮修；
若后续出现扫描风暴再把注释或实现补齐。

### F 组 · 前端不再假死

| # | 位置 | 问题 | 影响 |
| --- | --- | --- | --- |
| F1 | `web/src/api.ts:34`（全仓唯一 `fetch(`） | 无 `AbortController` / `signal` / 超时 | **根因**：后端 hang 则请求永不结束 |
| F2 | `web/src/api.ts` `request()` | 所有异常被吞，统一返回 `{success:false}` | **根因**：调用方分不清「网络挂 / 超时」与「业务失败」，无法触发重试或错误态 |
| F3 | `web/src/pages/stats-page.tsx:510-519` | `if (!config)` 只渲染「正在读取服务配置…」，无超时 / 错误 / 重试 | **用户可见的永久假死**（本轮工单现场） |
| F4 | `web/src/pages/stats-page.tsx:160-162` / `:177-179` / `:202` | `setLoading(false)` 不在 `finally`，`!statsEnabled` 与 `cancelled` 两条提前 return 都会跳过它 | loading 卡住，按钮永久转圈 |
| F5 | `web/src/pages/pull-page.tsx:317-321` | `setInterval` 轮询无在途守卫 | 慢后端下请求堆积，放大 F1 |
| F6 | `/api/config` 被 5 个页面各自拉取（`credentials-page.tsx:79` / `images-page.tsx:88` / `proxies-page.tsx:81` / `pull-page.tsx:309` / `stats-page.tsx:152`） | 同一份配置重复请求，失败各自处理 | 收敛为单一入口 + 缓存 |
| F7 | `web/src/pages/settings-page.tsx:118` / `:134` / `:237` / `:403` | 三处 `if (!config) return;`，保存按钮 `disabled={!config}` | 配置拉不到时按钮永久禁用且无提示 |
| F8 | `web/src/pages/images-page.tsx:57` / `:288` / `:292` / `:387` | `useState(!inventory)` 与 `loading && Boolean(inventory)` 语义混用 | 首屏 loading 判断不一致（低危，随 F1/F2 收敛） |

### 同源观察

F3 / F4 / F7 / F8 是同一模式的四个变体：**等一个可能永不完成的请求，且没有兜底状态**。
所以真正的修复点在 F1 / F2（`api.ts` 加超时 + 让错误可被识别）；页面层只需把 `setLoading(false)`
收进 `finally`、并在错误分支渲染可重试的错误态，不必逐页各写一套超时逻辑。

### 验收标准

1. 后端不可达或 `kill -STOP` 时，5 个页面均在 ≤10s 内显示错误态，**不再出现永久 loading**。
2. `web/src/api.ts` 内所有请求都带超时，且超时/网络错误与业务错误可区分。
3. `go test -race ./...` 稳定全绿；B5 相关测试连续 20 次运行无 FAIL（B7 已于 0.5.11 收口）。
4. `DeleteRepository` / `GC` 收到的 ctx 生效（可取消、可被请求超时打断），`lockRepo` 具备超时或排队语义。
5. 6 条主链路手工回归无退化：镜像列表 · tag 详情 · 复制 `docker pull` · 按 digest 删除 · 拉取任务 · 代理探测与热度统计。

## 0.5.19 · 工程化

本轮不加功能，只把「能提前发现上面那类缺陷」的闸门建起来。三个死锁/阻塞 bug（凭据库写锁泄漏、
代理库持锁写盘、前端假死）都逃过了现有验证，靠的是线上现象而非测试。

| # | 项 | 现状 | 目标 |
| --- | --- | --- | --- |
| E1 | 最小 CI | 无 `.github/workflows`、无 Makefile，靠人工跑 | 至少把 `gofmt -l` / `go vet ./internal/...` / `go test ./...` / 前端 `tsc` + `build` 串成一条可复现命令 |
| E2 | `-race` 守门 | 无 | B5 / B7 这类数据竞争只有 `-race` 能抓到，纳入固定流程 |
| E3 | 构建产物一致性 | 版本号需手工同步 5 处，靠自觉 | 用脚本/测试校验 5 处一致，漏改即失败 |
| E4 | go 指令对齐 | `go.mod` 的 `go` 指令与 `Dockerfile` 的 `GO_IMAGE` 需人工保持同步 | 加入一致性检查（`go mod tidy` 升 `go` 指令时同步 `ARG GO_IMAGE`） |
| E5 | 冒烟脚本固化 | 本轮验证用的隔离实例（独立端口 + 独立数据目录）脚本是一次性的，跑完即弃 | 固化为仓库内脚本，任何版本都能一键起隔离实例并从外部打 HTTP 验证 |

### 为什么值得单独立一轮

韧性轮的回归测试（例如「`List` 在 500ms 内必须返回」这类超时型断言）如果不进固定流程，
下次改动照样能悄悄退化。E5 是本轮临时验证点的固化——**验证方式本身要被版本化**。

## 0.6.0 · TLS 证书管理（新模块，中版本）

号位 0.6.0 由人指定，不随顺延改号。本路线图里按「中版本」办理的改动有三处：**0.5.12 代理交互轮**（新增「一键探测全部」这一用户可感知的新能力，按性质自动判定）、**0.5.13 新增弹窗内「测试连接」**（保存前试连，同为用户可感知的新能力，按性质自动判定），以及本轮 **0.6.0**（号位由人指定）。

- 现状（开工起点，已实测）：
  - `internal/server/server.go:213-222` 已预留 `TLSConfig`，但只设了 `MinVersion: tls.VersionTLS12`；
    而 `:282` 实际调用的是 `ListenAndServe()` —— **没有证书装载路径，该字段当前不生效**。
  - `internal/config` **没有任何证书相关配置项**（不存在 `tls_*`）：证书路径 / 开关 / SAN 均需新增。
  - 出站方向另有 `internal/registry/client.go:97` 的 `InsecureTLS`（按 registry 跳过证书校验），
    属于上游连接选项，与本模块无关，勿混为一谈。
- 规划要点：
  - 数据目录（`REGISTRY_CREDENTIALS_DIR`，默认 `/app/data`）新增证书存储
  - 上传自有证书 / 一键生成自签名证书
  - 证书与私钥落盘，容器重建后不丢失（挂卷约定见 `AGENTS.md`）
  - 服务启动路径从 `ListenAndServe()` 切到带证书的监听，让预留的 `TLSConfig` 真正生效
- 待确认（开工前需先回答）：
  - 证书轮换与热加载的边界：是否需要 `SIGHUP` 之外的重载入口
  - 与单二进制 / scratch 镜像约束的兼容性（不引入外部依赖）
  - 自签名证书的默认有效期与 SAN 默认值
  - 同端口按 mode 切换（HTTP / HTTPS）如何与单个 `http.Server` 监听器共存

## 刻意不做

以下项明确不进路线图，理由见 `AGENTS.md`（`cairn` 是单进程单二进制、单 registry 的镜像浏览与
管理工具，不做多租户与安全扫描）：

- ❌ 登录 / 用户体系 / RBAC
- ❌ 镜像扫描 / CVE 检测
- ❌ 镜像签名 / cosign 集成
- ❌ 多 registry 聚合
- ❌ 配额 / 速率限制
- ❌ Helm chart / OCI artifact 浏览（只管 Docker 镜像）

要加先问。
