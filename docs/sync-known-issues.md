# 镜像同步 · UAT 已知问题记录（0.6.x）

> 2026-09-30 UAT（client.local ↔ runner.local，cairn 0.6.7）实测反馈的 4 个问题。
> **本文档只记录，不改代码**——每条含：现象 / 根因 / 证据（file:line）/ 修复方向。
> 修复时按 AGENTS.md 版本规则进位（1、2、4 是缺陷 → 小版本；3 是新能力 → 中版本或并入下一号位）。
> 编号 SYNC-1 ~ SYNC-4，后续修复的 commit message 引用编号。

---

## SYNC-1 · 「立即运行」10 秒超时中止，但部分镜像实际已同步成功

**现象**

点任务行的「运行」→ 前端报 `运行失败：请求超时（10 秒）已中止: /api/sync/1/run`，
但去看镜像列表，**一部分仓库已经同步过来了**。用户视角「失败了却有数据」，状态自相矛盾。

**根因（与 SYNC-2 同一条链路）**

`runSyncTask` 走的是 `request()` 默认预算 `DEFAULT_TIMEOUT_MS = 10_000`，而同步是
**阻塞式长任务**（逐仓库 × 逐 tag × 逐 layer 复制 blob，cloud-ide 这类 GB 级镜像远超 10s）。
10s 到点 → 前端 `controller.abort()` → TCP 连接断开 → Go `net/http` 取消 `r.Context()` →
`Engine.Run(r.Context(), ...)` 的 ctx 被连带取消 → 在途的每个仓库 HTTP 调用立即
`context canceled`。

「部分成功」的解释：per-repo continue-on-error 之前**已完成**的仓库，其 blob / manifest
已经写进本地 storage（写入即持久），abort 只杀掉在途的，不回滚已完成的。

**证据**

- `web/src/api.ts:57` `DEFAULT_TIMEOUT_MS = 10_000`；`:519-520` `runSyncTask` 用 `request()`（没有传 `timeoutMs`，也没走 `requestSlow`）
- `web/src/api.ts:515-517` 注释自己都承认：「v0.6.0 是阻塞调用……v0.6.1+ 加 cron 后会改为立即返 202 + poll URL」——设计时就知道这是临时形态
- `internal/api/sync_handlers.go:243` `run, _ := s.Engine.Run(r.Context(), task)` —— engine 生命周期绑死在 HTTP 请求上
- `internal/sync/engine.go:95` `runErr = e.runPull(ctx, task, &run)` —— ctx 一路透传到每个 blob GET

**连带伤害（修复时必须一起考虑）**

`engine.go:88` `CreateRun(ctx, &run)` 先落一行 `status='running'`；`:112` `UpdateRun(ctx, run)`
收尾时**用的还是同一个已被取消的 ctx** → SQLite 写失败（database/sql 尊重 ctx）→
只打一条 WARN（`:114`）→ **sync_runs 里留下永远 `running` 的僵尸行**。
这直接影响 SYNC-4 的修复方案：如果「运行中禁用按钮」靠查 DB 的 running 行，
僵尸行会让按钮**永久**禁用。两个 bug 必须同轮修，或 SYNC-4 的判定要能容忍/清理僵尸行
（例如：`running` 且 `started_at` 超过 N 分钟视为陈旧）。

**修复方向**

1. **短期止血**：`runSyncTask` 改走 `requestSlow`（120s）。只是把窗口放大，>120s 的同步照样被杀，不算真修。
2. **真修（推荐）**：改异步模型——`POST /api/sync/{id}/run` 立即返 202 + run id，
   engine 用 **detached context**（`context.WithoutCancel(r.Context())`，Go 1.21+，当前 1.25 可用；
   或干脆 `context.Background()` + 自带合理超时）跑，UI 轮询 `GET /api/sync/{id}/runs` 或按 run id 查。
   这正是 ROADMAP 0.6.0「已知不做」里押后的「同步实时进度推送」的前半段，也是 SSE 候选的铺垫。
3. 无论选哪条：`UpdateRun` 的收尾写库必须用**不会随请求取消**的 ctx（否则僵尸 running 行照旧产生）。

---

## SYNC-2 · 日志大量 `sync: pull repo failed ... context canceled`

**现象**

```
WARN sync: pull repo failed task_id=1 repo=bklite/cloud-ide
  err="tag \"latest\": layer sha256:a2245b60...: get blob: ... context canceled"
WARN sync: pull repo failed task_id=1 repo=bklite/kube-state-metrics
  err="list tags: ... context canceled"
```

**根因**

**不是独立 bug——是 SYNC-1 的日志侧影**。前端 10s abort → ctx 取消 → 在途仓库的
blob GET / tags list 全部 `context canceled` → per-repo continue-on-error 把每个在途
仓库记一条 WARN。`cloud-ide`（大镜像，正在传 layer）和 `kube-state-metrics`（还在 list tags）
恰好是 abort 瞬间在途的两个。

**证据**

- 时间戳 `10:28:23.774847` / `10:28:23.774932`——两条 WARN 相差 85µs，是同一个 ctx
  取消事件同时打断多个在途调用的特征，不是各自独立的网络故障
- `internal/sync/engine.go` runPull 的 per-repo 错误处理：单 repo 失败记 WARN 不中断整轮

**修复方向**

随 SYNC-1 一起消失。另外可做一个观感优化：runPull 里对 `errors.Is(err, context.Canceled)`
单独归类（记 `run.Error = "已中止"` 而不是逐仓库刷 WARN），避免日志噪音误导排查。

---

## SYNC-3 · 远端凭据应引用「凭据管理」库，而不是表单里手填用户名密码

**现象（用户原话）**

> 新建或编辑任务时填的远端用户名密码不要填，而是在 [凭据管理] 中先新建，再选择就好了。
> 可以选择 [匿名/凭据]，不用再输入。

**定性**

不是缺陷，是**体验/架构改进**——而且 ROADMAP 0.6.0 设计稿本来就写了「凭据引用」
（`docs/ROADMAP.md` §同步任务模型：「每条同步任务 = (源, 目标, 调度, repo 过滤,
可选目标前缀, **凭据引用**)」；§验收标准：「跑通凭据：……同步任务引用 v0.2 凭据库里的
凭据 ID」）。0.6.0 MVP 实际落地时用了内联 username/password 字段，属于**计划内未实现项**，
现在用户明确要求补上。

**现状盘点（复用面很大）**

- 凭据库已有完整 CRUD + 测试连接：`internal/credentials/credentials.go:64` `Credential{ID, Name, URL, Username, Password, ...}`（AES-256-GCM 落盘）；`web/src/api.ts:314` `listCredentials()`
- `Credential` 自带 `URL` 字段——甚至可以讨论「远端地址」也一并从凭据带出（选了凭据自动填 URL），进一步减少表单字段
- sync 侧要动的：
  - schema：`sync_tasks` 加 `remote_credential_id TEXT`（v7 migration；参考 v6 的 DROP+CREATE 教训，评估直接重建表）
  - `SyncTask` 域类型：加 `RemoteCredentialID`；`Validate()` 三态互斥——匿名（都空）/ 内联（username+password）/ 引用（credential_id），引用态下内联字段必须为空
  - engine/writer：run 时从 Vault 解析出 username/password 再构造 Writer / registry.Client（**执行时解析**，不是保存时拷贝——凭据库改密码后任务自动跟进，这也是「引用」相对「拷贝」的核心价值）
  - UI：表单加 Radio「匿名 / 凭据」；选凭据 → Select 下拉（`listCredentials()` 数据源，显示 `name（url）`）；删掉两个手填输入框（或保留为高级选项——建议直接删，用户原话「不用再输入」）
  - 「测试连接」按钮同样改为按选中凭据解析后再探测
- 兼容：已有内联凭据的任务保留可用（三态里的「内联」档），编辑时引导迁移到引用

**修复方向**

按上面盘点做。属于**新能力**（用户可感知），按版本规则中版本 +1 或并入下一号位；
如果跟 SYNC-1/2/4 同轮修，取最高档（中版本）。

---

## SYNC-4 · 运行中刷新页面后「运行」按钮又可点，会重复发起同步

**现象**

点「立即运行」→ 任务未完成时刷新页面 → 按钮恢复可点 → 再点一次 → 两个运行冲突。

**根因（前后端各一半）**

前端：`runningId` 只是组件内存 state（`web/src/pages/sync-page.tsx:165`），刷新即丢；
页面加载时也没有任何「查一下这个任务是否有在途 run」的逻辑。

后端：per-task mutex 用的是**阻塞 `Lock()`**（`internal/sync/engine.go:78-80`），
第二个请求不会被拒绝，而是**排队**——第一个跑完后第二个接着再跑一整轮。
不报错、不告知，用户完全无感知地多同步了一次（大仓库 = 双倍时间 + 双倍远端流量）。
叠加 SYNC-1：第一次 run 被 abort 后锁很快释放，排队的第二次 run 用**新的完整 ctx**
接着跑——「超时失败」的表象下后台其实还在同步，观感更加混乱。

**证据**

- `web/src/pages/sync-page.tsx:165` `const [runningId, setRunningId] = useState<number | null>(null);`（纯内存）
- `web/src/pages/sync-page.tsx:443-444` 按钮 `loading={runningId === task.id}`——唯一禁用条件
- `internal/sync/engine.go:63-64` 注释明说：「Concurrent Run calls on the same task.ID serialize. The second caller blocks until the first finishes」
- `internal/sync/engine.go:88` run 行**开始时**就落库且 `status='running'`——DB 里其实有在途状态可查，只是没人查

**修复方向**

1. 后端：`lock.TryLock()`（Go 1.18+），拿不到锁立即返 **409 CONFLICT**「任务正在运行中」；
   handler 把 409 映射成前端可识别的 code。
2. 前端：页面加载 / refresh 时对每个 task 查最近 run（现有 `GET /api/sync/{id}/runs?limit=1`
   即可，或列表接口直接带 `lastRunStatus`），`status==='running'` 的任务按钮置灰 +
   Tooltip「后台运行中」。
3. **必须处理僵尸 running 行**（SYNC-1 连带伤害）：判定加陈旧阈值（如 running 且
   started_at 距今 > 30min 视为已死，允许重新发起 + 顺手把僵尸行改成 failed），
   或 engine 收尾写库用 detached ctx 从源头杜绝。
4. 长期：SYNC-1 的异步模型落地后，「运行中」状态天然以 DB 为准，本条的前端部分自动成立。

---

## 修复优先级建议

| 顺序 | 条目 | 理由 |
| --- | --- | --- |
| 1 | SYNC-1 + SYNC-2 + SYNC-4（同轮） | 三者共享「阻塞 run + 请求 ctx + 僵尸 running 行」一条根因链，拆开修会互相打架；异步化一次到位 |
| 2 | SYNC-3 | 独立的新能力，schema + UI 改动面大，单独一轮（中版本） |
