# cairn 路线图

本文件记录**已排定的号位**与选题范围。版本号规则见 [`AGENTS.md`](../AGENTS.md)：
新增模块 / 用户可感知的新能力 → 中版本 +1；缺陷修复与既有功能优化（含韧性、工程化）→ 小版本 +1；
主版本必须由人指定。当前版本以 `internal/version/version.go` 为准。

## 近期里程碑

只列中版本 +1 的新能力 / 新模块 / 破坏性变更;小版本 hotfix 看 `CHANGELOG.md`。

| 号位 | 主题 | 类型 |
| --- | --- | --- |
| 0.5.12 | 代理交互轮:探测反馈 · 新增即测 · 一键探测全部 | 新能力 |
| 0.5.13 | 代理管理:新增弹窗测试连接(保存前试连) | 新能力 |

> 0.5.18 韧性轮 + 部署方案收敛**未作为单独一轮发布** —— 韧性轮的问题清单(B / F / L 组)合并进了后续 hotfix,详见 [`docs/resilience.md`](./resilience.md);部署方案收敛(数据目录常量 / bind mount 替代 named volume / 业务 env 收敛到 2 项)合入了 0.5.41–0.5.50 的具体工作。

## 既定计划(待开工)

| 号位 | 主题 | 类型 | 状态 |
| --- | --- | --- | --- |
| 0.5.19 | 工程化:最小 CI / `-race` 守门 / 版本号一致性 / 冒烟脚本固化 | 工程化 | 部分完成(见下) |
| 0.6.0 | registry 同步(regsync 内建):拉 / 推 双向可配 | 新模块 | 号位已定,待开工 |

> **roadmap 不记录 hotfix**,每个小版本修复去 `CHANGELOG.md` 查;**也不列"未来做了哪些小修"**,因为结构性列表没意义 —— 只标"已发布 / 在做 / 待开工"三态。
>
> **`0.6.0` 由人指定**,不随中版本顺延改号(惯例)。

## 0.5.19 · 工程化

**状态**：部分完成（v0.5.50 时的进度）—— `make gates`（`Makefile` 第 156–167 行）已固化 `tsc --noEmit` / `go build ./...` / `go build -tags webui` / `go test -race ./...` 一条命令；`-race` 守门**已落地**（B5 / B7 的并发问题已在此前各轮修掉）；**未完成**：E1 GitHub Actions 自动跑 CI / E3 版本号 5 处一致性校验脚本 / E4 `go.mod` 与 `Dockerfile` `GO_IMAGE` 自动同步 / E5 隔离冒烟脚本固化。

**骨架级别不变**：E1 / E3 / E4 / E5 全部仍在 backlog 里,见 [`docs/resilience.md`](./resilience.md) 了解上下文。

## 0.6.0 · registry 同步（regsync 内建）（新模块，中版本）

号位 0.6.0 由人指定，不随顺延改号（2026-09-28 由同步议题替换原 TLS 议题）。
**TLS 证书管理** 不在本轮做；其触发条件与决策归档仍见 `AGENTS.md`「HTTPS / TLS 证书管理」节，待真正需求落地再排下一号位。

### 背景

- 用户当前用 [regsync](https://github.com/regsync/regsync) 在两个独立 registry 间做同步，希望把这一能力内建进 cairn，作为产品能力而不是外部依赖。
- 0.6.0 只做「registry ↔ registry」的同步，**不做多 registry 聚合**（后者违反 `AGENTS.md`「单进程单二进制、单 registry」原则）。
- 双向同步不在本轮做"互相同步"特殊处理：cairn-A 有「A→B」任务 + cairn-B 有「B→A」任务即视为双向，由各端独立配置组合，cairn 不感知「对端也在同步」。

### 设计要点

#### 同步任务模型

每条同步任务 = (源, 目标, 调度, repo 过滤, 可选目标前缀, 凭据引用)。

- 源 / 目标 二选一是「本机」，另一边是「远端 registry URL」。
- 源 ≠ 目标（不能配成自己跟自己同步）。
- 不做「远端 ↔ 远端」中转模式 —— cairn 不是 sync hub。
- 数据落地在 SQLite，跟现有 `pull_*` / `settings_*` 表同库。

#### 方向（拉 / 推）

- **拉取任务**：远端 → 本机。本机作为目标，源远端可只读。
- **推送任务**：本机 → 远端。出于网络拓扑原因（DMZ、单向可达、上游只能拉不能暴露），需要让本机主动把镜像推到对端。
- **双向同步** = A 上有「A→B」任务 + B 上有「B→A」任务，互不感知。

#### repo 范围匹配（iv 混合）

- 默认同步源 registry 全 catalog。
- 用户可填「拒绝列表」（deny list）按 repo 名精确排除；空 = 全量。
- **不做正则**（regsync 也提供 regex，但 cairn 用户群体对正则门槛偏高，需要时再补）。
- 跟 regsync 默认行为对齐，迁移成本最低。

#### 凭据

- 打通 v0.2 凭据库（`internal/credentials/`，AES-256-GCM 加密 JSON），同步任务**只引用凭据 ID**，不在任务里塞明文。
- 拉方向需要源远端的**读权限**凭据（Docker Hub / ghcr 走 Bearer Token，私有 registry 走 Basic Auth，cairn 当作 Basic Auth 远端）。
- 推方向需要目标远端的**写权限**凭据（cairn 远端用 Basic Auth，外部 registry 按其 scheme）。
- 出站方向证书校验沿用现有 `internal/registry/client.go:97` 的 `InsecureTLS` 字段，不在本轮为同步专门做证书开关。

#### 调度

- 内嵌 cron，cairn 进程内跑 ticker，跟现有 `internal/events` 模块复用 ticker 模式。
- 每条任务可单独关调度（仅手工触发）；开调度则按 cron 表达式到点跑。
- 单次执行时长可超过调度间隔时，**跳过本次**而非排队（避免积压）。

#### repo 映射

- 默认 M1（1:1 保留）：源 `team-a/web:v1` → 目标 `team-a/web:v1`。
- UI 可选「目标前缀」输入框，留空 = M1，填了 = M2（`mirrored/team-a/web:v1`）。
- 不做去前缀（M3）。

### 定稿决策（2026-09-30）

开工前 3 个边界决策已拍板,记录在此防止后续讨论反复:

1. **cron 表达式解析手写**(不引入 `github.com/robfig/cron/v3`):严守 `AGENTS.md`「仅 stdlib + chi + x/sync」原则;5 字段标准 cron 手写约 180 行,功能等价。
2. **不发 0.5.53 / 0.5.54 / 0.5.55 过渡号位**:Phase 1-3 跑通后直接发 0.6.0。中间不阻塞开发,UAT 跟 git main 走(每次 commit + push 后给 UAT 下一步命令)。
4. **凭据库 ID 引用在 Phase 1 打通**:否则私有 Basic Auth 源端到端跑不通。`sync_tasks.credential_id` 字段从一开始就位,API 字段一次定型。

### 代码结构

```
internal/sync/
  types.go        // SyncTask, TaskRun, Direction 枚举
  engine.go       // copy(repo, src, dst) — 复用 pull executor 的 blob 推送路径
                   // 拉取 = (远端 client, 本地 push);推送 = (本地 client, 远端 push)
                   // 两侧都用现有 internal/registry/client.go，无新出栈
  scheduler.go    // cron 解析 + ticker(复用 events 模块模式)
  filter.go       // repo deny list
  store.go        // SQLite: sync_tasks + sync_runs 表
internal/api/
  sync_handlers.go // GET/POST/PUT/DELETE /api/sync, POST /api/sync/:id/run,
                   //              GET /api/sync/:id/runs
web/src/pages/
  Sync.tsx         // 镜像同步页面(独立顶部 tab,跟"拉取队列"并列)
```

预计 **~1500 行 Go + ~400 行 TS**。

### 复用现状（开工前要敲定的代码接缝）

- 现有 `internal/registry/blob.go` 已有完整 BlobExists / StartBlobUpload / UploadBlob 链路 —— 同步引擎直接复用，不重写。
- 现有 `internal/pull/executor.go` 的推送路径（拉方向）—— 抽出「读 src + 写 dst」通用化即可覆盖推方向，无需新增两套。
- 现有 `internal/db/` 的 SQLite 表迁移机制（v0.3 起）—— 新增 `sync_tasks` / `sync_runs` 表走同一套迁移。
- 现有 `internal/credentials` 凭据库 —— 同步任务表加 `credential_id` 外键，不复制凭据存储。

### 阶段交付（Phase 拆解）

> 阶段间不发过渡号位(决策 2):Phase 1-3 跑通后直接发 0.6.0。中间每次 commit + push 后给 UAT 下一步命令,UAT 跟 git main 走。

#### Phase 1 · 拉方向骨架（先跑通端到端）

- `internal/sync/{types,store,filter,engine}.go` 写完
- 拉方向 API：`GET/POST/PUT/DELETE /api/sync`、`POST /api/sync/:id/run`
- 端到端：手工触发一条「远端 → 本机」任务跑通，blob / manifest 全部走通
- 单测：`engine` 复制语义、`filter` deny list
- **凭据库 ID 引用**(决策 3):`sync_tasks.credential_id` 字段从一开始就位;私有 Basic Auth 源端到端跑通

#### Phase 2 · 推方向

- engine 抽象「src → dst」通用化（同一份 `copy` 函数，src 与 dst 互换）
- 推方向端到端跑通（拿 Phase 1 的镜像反向再推一次）
- 单测：双向对称

#### Phase 3 · 调度

- `scheduler.go` **手写**(决策 1)5 字段 cron 解析;秒级与 timezone 后续再说
- ticker 与 events 模块对齐
- 「跳过本次不排队」逻辑

#### Phase 4 · UI

- `Sync.tsx` 任务列表 + 新建 / 编辑 / 删除 / 立即运行
- 同步历史（每次 run 的状态、起止时间、复制了多少 manifest / blob）
- 顶部 tab 加「同步」入口，跟「拉取队列」并列
- 设置页「凭据」tab**不变**，仅同步任务页引用凭据 ID 下拉

### 验收标准

- 跑通端到端：起两个 cairn 实例（A、B），B 上配「从 A 拉 `library/nginx:1.25`」任务，A 上 push 一个 `library/nginx:1.25`，B 上手工触发任务，B 内出现该镜像，digest 一致。
- 跑通推方向：B 上配「推到 A」任务，B 内 push 一个镜像，A 内出现。
- 跑通调度：cron 表达式配置为 `*/5 * * * *`，等一个周期，任务自动执行且 UI 显示上次成功时间。
- 跑通 deny list：源有 10 个 repo，deny 1 个，同步后本地只有 9 个。
- 跑通凭据：源远端启用 Basic Auth，同步任务引用 v0.2 凭据库里的凭据 ID，能拉到。
- 跑通双向：A 上「A→B」任务 + B 上「B→A」任务各跑一次，两端都有双方的镜像（验证无回路 bug）。
- 同步历史表里每条 run 都有 `status`（success/failed/skipped）、`started_at`、`finished_at`、`manifests_copied`、`blobs_copied`、`error` 字段。

### 已知不做（0.6.x 或后续再说）

- ❌ 同步循环检测告警（A→B 同时 B→A 互拉的 UI 提示）。本期不挡功能，靠配置自律。
- ❌ 多 image tar 导入（用户 2026-09-28 讨论后否决，非本期议题）。
- ❌ TLS / HTTPS 证书管理（用户 2026-09-28 决定不做，原 0.6.0 议题已撤；触发条件归档在 `AGENTS.md`）。
- ❌ 远端 ↔ 远端 中转模式（违反单 registry 原则）。
- ❌ 凭据库新 scheme（Bearer Token 之外的 S3 / GCP 之类，不在本轮范围；现有 cairn 凭据库只管 Basic Auth）。
- ❌ 同步实时进度推送（web UI 显示当前正在复制哪个 repo）。当前先做「执行完一次刷新结果」，实时进度跟 events 模块联动后续再说。
- ❌ 同步 dry-run / 预览（regsync 支持 `--dry-run`，本轮不做，UI 上有「立即运行」按钮可点击看效果即可）。

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
