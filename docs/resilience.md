# 韧性轮 · 0.5.18 计划文档

> 本文档是 2026-09-27 排定、最终未作为单独一轮发布的 0.5.18 「韧性轮 + 遗留收口」计划。其中的问题拆分(B / F / L 组)分别合并进了 0.5.46–0.5.50 的具体工作中。保留此文档是为后人查 v0.5.x 中段最严重的并发 / 进程死锁 / 前端永久假死问题的全貌。

## 0.5.18 · 韧性轮 + 遗留收口

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

### 并入本轮的历史遗留（2026-09-27 排定）

v0.5.17 交付时记录的「已知遗留」共 5 条，经逐条取证后全部并入本轮，不另开号位。
表中「现状」均为实测结论（文件行号取自 `0.5.18` 开工前的代码）。

| # | 位置 | 现状（已实测） | 目标 |
| --- | --- | --- | --- |
| L1 | `internal/db/db.go:129`（表）/ `:142`（索引）/ `:184` `PullJobRecord`（唯一 INSERT，仅被 `internal/pull/executor.go:319` 调用） | **只写不读**：`internal/db` 全文无任何 `SELECT`；`GET /api/pull/jobs`（`internal/api/handlers_extra.go:286` `ListPullJobs`）只读内存 `Executor.List()`；`Executor` 结构体（`internal/pull/queue.go:180-188`）不含 DB 句柄；`Executor.Delete`（`queue.go:313-331`）只删内存；保留期配置 `PullHistoryRetention`（`executor.go:101` 声明 · `internal/server/server.go:143` 赋值 · `internal/config/config.go:255` 访问器 · 键 `pull.history.retention.days` 默认 90 见 `config.go:78`）**零读取**；前端 `fromHistory`（`web/src/types.ts:196` · `pull-page.tsx:1280-1281`）后端零产出。`queue.go:11-12` 注释仍写着已删除的 env `REGISTRY_PULL_HISTORY_RETENTION_DAYS` | 拉取历史闭环：写进去的能读出来（进程重启后仍在）、能从历史中移除、到期自动清理且留日志；注释里的废弃 env 一并清掉 |
| L2 | `internal/registry/client.go:347-374` | `IsRetryable`（`:352` 定义）**全仓零调用点**（导出但无人用）；唯一解链它的 `asErr`（`:374`）解链后从不写 `target`，**无条件 `return false`**；真正的重试判定退化成字符串匹配 `:362-368`（`connection refused` / `EOF` / `no such host` / `i/o timeout`） | 二选一并定稿：让 `asErr` 具备真正的 `errors.As` 语义、`IsRetryable` 成为唯一判据；或连同 `IsRetryable` 一起删除死代码。无论选哪条，判定必须有单测覆盖（当前 15 分钟超时下的失败重试完全依赖字符串） |
| L3 | `internal/api/handlers_extra.go:974-1003` `UpdateProxy` | `:995-997` 无条件覆盖 `Username` / `Password` / `Note`，`:996` 注释自认「empty = clear (anonymous proxy)」；而同类 `UpdateCredential`（`:764`）`:786` 写作 **`if in.Password != ""` 才写**（empty = 保持）。**两个 Update 语义相反**。`toProxyView`（`:883-898`）不回传密码只给 `HasAuth`，故前端 `proxies-page.tsx:244-245` 空密码不发字段（线上不会误清），但 UI 也**无法显式清空**代理密码 | 统一语义：用指针字段区分「未提供 = 保持」与「显式清空 = 清空」，并明确它与 `UpdateCredential` 谁向谁对齐 |
| L4 | 预检 `handlers_extra.go:381` `Timeout: 5 * time.Second` ↔ 拉取 `internal/pull/executor.go:68` `Timeout: 15 * time.Minute` | 两处超时相差 **180 倍**：预检 5s 绿灯只证明「5s 内可达」，慢源仍会在入队后长时间停在「拉取中」（158 现场实测一次 `library/alpine:3.15` 61.7s 后 `manifest failed` → `cancelled`） | 二选一并落地：入队后的早期失败更快浮出（不再干等到 15 分钟）；或在 UI 明示「慢源可能等待数分钟」的预期 |
| L5 | `web/src/pages/settings-page.tsx` | 5 处 `TS6133`（未使用声明）：`:6,3 Descriptions` · `:31,1 formatDateTime` · `:64,64 inventory` · `:77,10 savingRegistryUrl` · `:77,29 setSavingRegistryUrl` | 清零，且不新增任何 TS 错误（与 `tsc --noEmit` 基线逐字比对） |

**号位说明（L1 含 1 项新能力）**

L1（拉取历史闭环）按 `AGENTS.md` 属**用户可感知的新能力** → 严格按规则应进中版本；而 `0.6.0` 已由人指定、不随顺延改号（2026-09-28 由同步议题替换原 TLS 议题）。
故按人指定把 L1~L5 并入 `0.5.18` 这一交付批次；**发版时需再确认号位**（若坚持按性质判定，L1 应拆到 `0.6.x`，其余 4 条留在 `0.5.18`）。

**追加验收（随 L1~L5）**

1. L1：进程重启后 `GET /api/pull/jobs` 仍能列出历史记录，且条目带 `fromHistory` 标记；「从历史中移除」后 SQLite 中对应行同步消失。
2. L1：保留期到期清理可查（`pull.history.retention.days`，默认 90），执行时有日志。
3. L2：`asErr` / `IsRetryable` 的判定有单测覆盖，或对应死代码已删除（二选一，实现时定稿）。
4. L5：`web/` 下 5 处 `TS6133` 清零，且不新增任何 TS 错误（与基线逐字比对）。

### 追加项 · 部署方案收敛（2026-09-28 排定，破坏性）

**这是一条破坏性部署变更**（按 `AGENTS.md` 的规则，号位需人指定）→ 本轮**不自行进位**：只改代码 / compose /
`.env.example` / 文档，**不动 `version.go`、不动 `CHANGELOG.md`**；发版时补写 CHANGELOG（条目要点见本节末尾）。

起点是三条实测问题：

1. **变量太多，且把容器内的实现细节暴露成旋钮**：`REGISTRY_CREDENTIALS_DIR` / `REGISTRY_STORAGE_DIR` 决定的是
   "文件在容器里放哪儿" —— 运维不需要、也不应该动它。容器内路径由我们自己定义，只有**需要挂到宿主机的路径**才该可配。
2. **`.env` 里大部分变量早就不生效**：v0.5.9 起业务配置单源于 SQLite（`settings` 表，设置页写），
   `internal/config.Load()` 只读 `PORT` / `REGISTRY_CREDENTIAL_KEY` / `GO_HUB_ENV`；而 compose 仍把 14 个业务
   `REGISTRY_*` 注进容器。`.env.example` 与 158 线上 `.env` 因此长期分叉，运维改了半天以为生效、其实以页面为准。
   (v0.5.23 把 `GO_HUB_ENV` 改名为 `CAIRN_ENV`,compose `environment` 仍是 **2 项**,只是 env 名字变了。)
3. **数据用两个 named volume**（`cairn-data` → `/app/data`、`cairn-registry` → `/app/registry`），
   数据藏在 docker 卷目录里，备份 / 迁盘要绕 `docker info` 的 `DockerRootDir` 内部路径。

**定稿（实施口径）**

| 项 | 改前 | 改后 |
| --- | --- | --- |
| 容器内数据目录 | env `REGISTRY_CREDENTIALS_DIR`（默认 `/app/data`） | 常量 `config.DataDirPath` = `/app/data` |
| 容器内 registry 内容 | env `REGISTRY_STORAGE_DIR`（compose 默认 `/app/registry`） | 常量 `config.StorageDirPath` = `/app/data/registry` |
| 宿主机数据目录 | 无（named volume） | `HOST_DATA_DIR`（默认 `/data/cairn`），单条 bind mount |
| compose `environment` | 16 项（14 个业务 `REGISTRY_*` + 密钥 + 两个目录） | **2 项**（`REGISTRY_CREDENTIAL_KEY` + `CAIRN_ENV`；曾用名 `GO_HUB_ENV`，v0.5.23 改名） |
| 顶层 `volumes:` | `cairn-data` / `cairn-registry` | **删除**（全部 bind mount） |
| `.env` 生效项 | 与 158 线上分叉 | 运行时 5 项 + 构建期 6 项（见 `.env.example`） |

**为什么 registry 内容取 `/app/data/registry`（嵌套）而不是 `/app/registry`（同级）** —— 158 实测：线上
`REGISTRY_STORAGE_DIR=/app/data/registry`（该 env 确实生效过）、107M 真实镜像数据**已经在 data 卷的 `registry/` 子目录里**，
而 `cairn_cairn-registry` 卷**完全空**。选嵌套 = 与线上现值逐字一致 → **迁移零成本**（整卷内容复制即可）、发布零行为变化。
（旧 compose 的默认值 `/app/registry` 与本条无关：只有没覆盖过它的部署才会用那个默认值，迁移时须一并搬。）

**只暴露宿主机侧变量**：宿主机侧只留 `HOST_DATA_DIR` 一个。不给 registry 内容单独开变量 —— 要让 blobs 独占大盘，
自行加第二条 bind mount（`/data2/cairn-registry:/app/data/registry`，README 有配方）。这条守住"容器内路径是实现细节"。

**`PORT` 保持可读但不进 `.env.example` 生效行**：`PORT` 不是路径，且已被 `Dockerfile` 的 `ENV PORT=8787` 固定、
compose 从不传递（旧 compose 也从未列 `PORT` ⇒ `.env` 里的 `PORT` 一直是空操作）。改宿主机端口一律用 `HOST_PORT`。

**验收**

1. `gofmt` / `go vet` / `go build ./...` / `go test ./...` 全绿（已通过，2026-09-28）。
2. 全仓 `grep -rn 'REGISTRY_CREDENTIALS_DIR\|REGISTRY_STORAGE_DIR'` 只剩历史 `CHANGELOG.md`（不改写历史）与说明性文字。
3. `docker compose config` 里 `environment` 只剩 2 项、无顶层 `volumes`、`volumes` 只有一条 bind（本机无 docker CLI，由 158 验）。
4. 158 按 README「从命名卷迁移」搬完数据后：`config loaded` 日志正常、设置页配置还在、镜像列表能看到原仓库与 tag、
   容器内 `/app/data/registry` 有内容。

**发版时必须补的 CHANGELOG 条目（本轮不写）**：破坏性部署变更 —— 移除 `REGISTRY_CREDENTIALS_DIR` /
`REGISTRY_STORAGE_DIR`（容器内路径改为编译期常量）、compose 业务 env 收敛到 2 项、named volume 改 bind mount；
升级须按 README「从命名卷迁移」先搬数据再 `up`。

