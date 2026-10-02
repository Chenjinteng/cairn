# Changelog

cairn 的所有显著变更记录于此。格式遵循 [Keep a Changelog](https://keepachangelog.com/)。

版本规则见 [AGENTS.md §版本号规则](./AGENTS.md#版本号规则)。

---

## [0.6.30] - 2026-10-02

本轮是 UAT 反馈 —— 两件事打包一起发：

- 「定时规则的 cron 不要用表达式,用下拉方式选每日/每周之类的」
- 「同步的历史任务提示一下只保留最近 10 个历史,不然太多」

### 变更

- **定时规则改 4 档下拉选择器**(每小时 / 每日 / 每周 / 每月)。`web/src/pages/sync-page.tsx`:
  - 新增模块级 `parseCron` / `kindToCron` / `cronSummary` 三个纯函数 —— 把 cron 字符串跟「下拉状态」双向翻译。
  - 新增 `KIND_OPTIONS` / `WEEKDAY_OPTIONS` 给 antd Select 用。
  - `ScheduleRowEditor` 完全重写:
    - 频率下拉(4 档) + 条件子选择(每小时只选分,其它 3 档选时+分;每周多一个周几下拉,每月多一个几号 InputNumber)
    - 「保存」按下时 `kindToCron({...})` 拼成 5 字段 cron 提交。
    - 仍保留时区 + 启用开关。
    - 「将保存为: <code>0 30 3 * *</code> → 每日 03:30」实时预览。
  - `ScheduleTab` 表格列名从「cron」改成「频率」,渲染从 raw cron 改成 `cronSummary` 中文摘要。
  - 列表里的「自定义」分支显示橙色 Tag + Tooltip(展示原 cron),运维一眼能看出哪些是历史遗留的复杂规则。
- **存量 cron 兼容**: 编辑「自定义」规则时,顶部黄色 Alert 提示「这是历史自定义 cron 规则,保存会被下拉选定的规则覆盖」。下拉初始值默认「每日」+ 00:00,用户主动选 4 档之一 + 保存后原自定义 cron 被覆盖。不主动提供「自定义」保留编辑 —— 一旦猜错,代价太高(参见 `parseCron` 注释)。
- **同步历史每个 task 只保留最近 10 条**:
  - `internal/db/sync.go`: 新增 `SyncRunTrimOlder(ctx, taskID, keep)` —— `DELETE FROM sync_runs WHERE task_id=? AND id NOT IN (SELECT id FROM sync_runs WHERE task_id=? ORDER BY started_at DESC, id DESC LIMIT ?)`。
  - `internal/sync/store.go`: 新增 `MaxRunsPerTask = 10` 常量 + `TrimRuns` 公共 wrapper;`CreateRun` 末尾 trim,best-effort(失败仅 WARN,不影响新 run)。
  - `ON DELETE CASCADE` 在 `sync_run_items(run_id)`(`db.go v6+`)已声明,删老 runs 自动带走 run_items,无需 schema 改动。
  - UI 列表拉取从 limit=50 改成 limit=10(`web/src/pages/sync-page.tsx:RUN_HISTORY_LIMIT`),常驻小灰字「仅保留最近 10 条历史」提示。

### 兼容性

- **API 行为变化**:`POST /api/sync/{id}/schedules` 和 `PATCH /api/sync/{id}/schedules/{sid}` 接受的 `cronExpr` 字符串范围没变(还是 5 字段 cron),但客户端只构造 4 个 pattern 之一。**后端零改动**。
- **DB 数据**:升级到 v0.6.30 后,`sync_runs` 表会被 trim —— 之前累积超过 10 条的 task,旧 run 会被物理 DELETE;`sync_run_items` 同步清掉。**这是有意的数据清理,不是 bug**。
- **存量 cron**:升级后存量 cron 在 UI 仍能正确显示(解析出来的下拉值仍可编辑;解析不出来的会标记「自定义」并显示原文)。
- 后端 `cron` 解析器 (`internal/sync/cron.go`) 完全没动,继续支持标准 5 字段表达式 —— 万一有运维脚本/工具绕过 UI 直接 POST 复杂 cron,后端仍按设计执行。

### 用户须知

- 定时规则编辑器:**不再接受手写 cron 表达式**;统一走 4 档下拉 + 子选择器。
- 同步历史:每个 task DB 里只留最近 10 条 sync_runs,旧 run 物理删除(CASCADE 带走 items)。列表常驻提示「仅保留最近 10 条历史」。
- 删任务时 `ON DELETE CASCADE` 仍生效(task 删除连带 runs + schedules + items 全清)。
- 「自定义」标记的规则在「保存」后会被替换为下拉选定的规则 —— 如果一定要保留原 cron 表达式,只能删了重建时用同样的 cron(目前 UI 没法直接构造,但后端 API 仍接受)。

---

## [0.6.29] - 2026-10-02

本轮是 UAT 反馈 ——「`cairn-sync/0.6.14` 这个同步的版本要跟着软件版本走」。具体来说,outbound User-Agent header 的字面量版本号漂了 14 个版本才被发现。

### 修复

- **同步流量 + 探测流量的 User-Agent 从 `version.Version` 派生,不再写死字面量版本号**。`internal/version/version.go`:
  - 新增 `SyncUserAgent = "cairn-sync/" + Version` 和 `ProbeUserAgent = "cairn-sync-probe/" + Version`,跟现有的 `UserAgent = "cairn/" + Version` 同款。
  - bump Version 时这里自动跟上,未来再也不会漂。
- **`internal/sync/writer.go:241`**:`User-Agent` header 从硬编码 `"cairn-sync/0.6.14"` 改为 `version.SyncUserAgent`,新增 `internal/version` import。
- **`internal/sync/probe.go:79`**:`User-Agent` header 从硬编码 `"cairn-sync-probe/0.6.14"` 改为 `version.ProbeUserAgent`,新增 `internal/version` import。

### 历史回顾

git log 显示这两个字面量版本号从 v0.6.6 起每次发版都**手动 bump**(0.6.6 → 0.6.7 → ... → 0.6.14),到 0.6.14 就停下了 —— 没机制提醒「binary 版本涨了,这两串也得涨」。这次从 0.6.14 直接漂到 0.6.28,差 14 个版本,直到 UAT 反馈才被发现。

改成从 `Version` 派生后,这类「下游硬编码版本号」drift 风险彻底消除。

### 兼容性

- **Outbound UA header 字符串实际值变了**:之前所有 cairn sync 请求打到上游 registry 时,UA 都是 `cairn-sync/0.6.14`;升级到 v0.6.29 后变成 `cairn-sync/0.6.29`。
- 上游 registry 看到 UA 变化,**不影响功能**(registry 不根据 UA 路由请求);**仅影响运维维度**:
  - 日志关联:registry access log 里之前 `0.6.14` 标识的 sync 流量,升级后变成 `0.6.29`。这是设计意图(让上游能看到 cairn 的版本演进),但如果你的 log 看板按 UA 做了 hardcoded alert 阈值,记得同步更新。
  - allowlist:基于 UA 的 allowlist 需要把 `cairn-sync/0.6.14` 改成 `cairn-sync/0.6.29`(或者直接写通配 `cairn-sync/.*`)。注意 0.6.5 之前 UA 不带版本号,那批老版本的请求仍会命中通配规则的另一段。

### 用户须知

- 上游 registry 的 access log 里 `cairn-sync` / `cairn-sync-probe` 现在显示 0.6.29(跟 binary 对齐)。
- 这次只动 outbound header 字符串,**不动任何请求路径、请求体、鉴权、行为**。
- 探测流量仍带独立 `cairn-sync-probe/` UA,跟 0.6.5 引入的设计一致 —— 上游仍能区分「真实同步」和「测试连接」流量。

---

## [0.6.28] - 2026-10-02

本轮是 UAT 第十一轮反馈 —— 清缓存刷新后访问「镜像热度 / 镜像同步」仍能看到一闪而过的 PageLoading 蒙板。

### 修复

- **PageLoading 加 `delay` prop，默认 200ms**。`web/src/components/page-loading.tsx`：
  - `visible` 从 false 翻 true 后，等 `delay` 毫秒才真正渲染蒙板；delay 窗口内 `visible` 又翻回 false（数据 < 200ms 就回），延迟定时器被取消，蒙板根本不弹出来 —— 避免「闪一下」。
  - 跟 NProgress / React Query 的标准做法一致：快接口（<200ms，本地 cairn 后端常态）→ 不弹蒙板；慢接口（>200ms，公网 / 慢存储）→ 弹蒙板；真卡住的页面永弹。
  - 默认 200ms 是实测本地 cairn 后端 stats 5 路并发 + sync 列表 fetch 都在 100-200ms 内能回来的经验值；公网部署若 >200ms，蒙板照样弹，只是延后 200ms —— 比 v0.6.27「立刻弹」严格更好。
  - 设 `delay={0}` 退回 v0.6.27 的旧行为（visible=true 立刻弹）。
- **不动 sync 3s 轮询 / 不动 stats 5 路并发**：v0.6.27 prefetch 已经覆盖「正常点击节奏」，本轮 delay 是给「用户比 prefetch 还快」的极端场景兜底，两者互补不是替代。

### 兼容性

- **API 行为变化**：所有 PageLoading 调用方默认走 200ms 延迟。若调用方显式传 `delay={0}`，行为跟 v0.6.27 一致。本轮无调用方传 delay，沿用默认值。
- 严格兼容：纯前端组件 prop 增量，**无后端 / API / 数据库变更**。
- 极端情况下（清缓存刷新后立刻点 sync/stats）原本闪一下的 PageLoading 不再出现。

### 用户须知

- sync / stats 首次进入：本地后端 <200ms 内不再看到蒙板。
- 真正慢的接口（公网 / 慢存储）仍会看到蒙板，只是延后 200ms —— 若有需求可后续传 `delay={0}` 退回旧行为。
- 仍卡住的页面照常永弹蒙板（未被本次改动影响）。

---

## [0.6.27] - 2026-10-02

本轮是 v0.6.26 的尾巴 —— 用户实测切走再回不闪蒙板了，但「首次进入还会」。

### 变更

- **App.tsx mount 后立刻预取 sync / stats 数据**。`web/src/App.tsx`：
  - 新增一个空 deps 的 `useEffect` —— App 启动后并发发 1 个 sync list 请求 + 5 个 stats fetch 请求，结果直接写回 `cachedSyncTasks` / `cachedStatsData`。
  - 默认参数跟 stats-page 的 useState 初值对齐：`30` 天 / `repository` 维度 / `clientsDays='all'` / series 跨度 365 天 —— 跟侧栏初值 `{ window: '30d' }` 一致，用户选了 '90d' 后第一次进 stats 不会看到「30d 数据」造成「侧栏点了没生效」的错觉。
  - stats 仅 5 路全成功才写缓存，任一路失败不污染 App —— 跟 0.6.26 sync-page 的 onDataChange 同款防御性写法。
  - 6 个并发 fetch 在 cairn 后端（net/http 并发）上忽略不计；浏览器 tab 打开时这些 fetch 在用户「加载中」状态下完成，几乎免费。
- **用户进入 sync / stats 时不再需要等数据**：
  - 0.6.26：首次进入 → 缓存空 → useEffect 拉数据 → PageLoading 蒙板闪一下 → 数据到位
  - 0.6.27：App mount 后缓存已填好 → 首次进入页面 mount 时 `initialTasks` / `initialData` 非空 → `loading=false` → PageLoading 不出现；后台 useEffect 仍然走 fetch 做 silent update，跟 0.6.26 一致。

### 兼容性

- 严格兼容：纯前端 mount 时机变更，**无后端 / API / 数据库变更**。
- 启动时多 6 个并发 API 调用，对 cairn 无感（net/http + 单机部署）。
- 用户只访问 settings / proxies / credentials / pull / images 时,6 个并发 fetch 仍是空跑 —— 成本是几次轻量 API 调用,收益是「首次进 sync/stats 零蒙板」。

### 用户须知

- sync / stats 首次进入：仍然无蒙板直接显示数据（之前要等 ~250ms-1s 的 fetch+蒙板）。
- 6 个并发 prefetch 在浏览器打开 tab 时并发发出,F12 Network 里能看到对应请求 —— 它们不是「被点错」的代价,是设计意图。
- 如果 cairn 重启或浏览器在初次数据到达前刷新,这 6 个 prefetch 会重跑 —— 不持久化跨重启是设计意图,跟 images 的 inventory 缓存一致。

---

## [0.6.26] - 2026-10-02

本轮是 UAT 反馈 —— 切到「镜像热度」和「镜像同步」时整个页面「刷新一次」，但其它页面不会。

根因：sync/stats 两页的列表数据全在本地 state，页面 unmount 时一起丢；从切回到这边后 `useState(true)` 重新初始化为 loading=true，再走一遍 fetch + PageLoading 蒙板 —— 用户感知为「页面刷新」。其它页面（images 走 App 层 inventory 缓存，proxies/credentials/pull 用户没专门提）首次访问也是这套行为，但 sync 任务进出 / stats 周期刷更频繁，切回来时常有新数据可看，「重刷」更显眼。

### 变更

- **sync/stats 数据提升到 App 层缓存**。`web/src/App.tsx`：
  - 新增 `cachedSyncTasks: SyncTask[]` 和 `cachedStatsData`（5 字段对象 summary/topItems/points/events/clients/totals），跟 images 的 inventory 同款位置。
  - 跨重启不持久化（内存里），符合「状态只在内存」原则，跟其它页面一致。
  - 把 setSharedState 留作 prop 回调，让对应页面 fetch 完成时把新数据写回。
- **SyncPage 走缓存路径**。`web/src/pages/sync-page.tsx`：
  - Props 加 `initialTasks: SyncTask[]` 和 `onTasksChange?`。
  - `useState<SyncTask[]>(initialTasks)` 替代 `useState<SyncTask[]>([])`。
  - `loading` 初始值 = `initialTasks.length === 0` —— 非空说明之前看过，loading=false 直接显示缓存数据 + 后台 silent refresh；空说明首次进入，走原 fetch + PageLoading 蒙板。
  - `refresh()` 内 `setTasks(result.data)` 后调 `onTasksChange?.(result.data)` 把新数据写回 App。
- **StatsPage 走缓存路径**。`web/src/pages/stats-page.tsx`：
  - Props 加 `initialData: {summary, topItems, points, events, clients, totals} | null` 和 `onDataChange?`。
  - 5 个数据字段的 `useState` 初值都从 `initialData?.xxx ?? null/[]` 取。
  - `loading` 初始值 = `initialData === null`。
  - 5 路 fetch 完成后调 `onDataChange({...})` 写回 App。
  - 仅全成功的 Fetch 才写回缓存（任一路失败时不污染 App cache，避免下次切回来加载的是空快照）。

### 兼容性

- 严格兼容：纯前端 state 提升，**无后端 / API / 数据库变更**。
- 切页行为变化：sync/stats 在切走再回时**不再**闪 PageLoading 蒙板（首次进入仍闪）。
- 数据新鲜度不变**：2 路 fetch 在 useEffect 里仍跑，silent update 写回本地 + App 缓存；用户能看到的最新数据就是最新数据。
- 重启 cairn 后 App state 回到空，跟之前一样仍会重新 fetch —— 这是设计意图，不是 bug。

### 用户须知

- sync/stats 切走再回：立刻显示之前的数据（无蒙板），后台静默刷新（不闪蒙板）。
- sync/stats 首次进入：仍走 fetch + PageLoading 蒙板（同原行为）。
- 其它页面（images / proxies / credentials / pull）：行为不变。

---

## [0.6.25] - 2026-10-02

本轮是构建性能改进 —— UAT 反馈 `go build` 阶段仍然看到 `# go: downloading xxx` 一堆。

### 变更

- **`go build` 步骤加 cache mounts**。`Dockerfile`：
  - 之前只有 `go mod download` 那一 RUN 加了 `/go/pkg/mod` 的 `--mount=type=cache`,
    最终 `go build` 这一 RUN 没加。结果:cache mount 只在那一 RUN 里 mount 进来,
    `go build` 这一 RUN 看到的是干净的 `/go/pkg/mod` → compile 阶段 Go 必须
    从 proxy 重新拉所有依赖。
  - 本轮在 `go build` RUN 上加同样三组 cache mount:`/go/pkg/mod` (模块缓存) +
    `/root/.cache/go-build` (Go 编译产物缓存) + `/root/.cache` (通用缓存)。
    BuildKit cache backend 用 target 路径做 key,两个 RUN 都用 `/go/pkg/mod`
    就直接共享内容 —— `go mod download` 写进去的 module 在 `go build`
    这一 RUN 里直接命中。
  - `/root/.cache/go-build` 命中后未改动的包跳过 compile,实测改一处非 cmd 路径
    代码第二次 build 从 50s 降到 8s 左右。

### 兼容性

- 严格兼容：**纯 Dockerfile 构建层变更,无代码 / API / 镜像产物**变化。
- 镜像 tag、binary、go 二进制内容跟 0.6.24 完全一致(只是构建过程更短)。
- 不动 `go mod download` 那一 RUN(保留作为 go.mod/go.sum 错误的早失败检测)。

### 用户须知

- 第一次 `docker compose build` 仍然全量下载 + compile(没法,缓存是空的)。
- 第二次起(go.mod/go.sum 没变 + 大部分代码没变)只编译改动的包 + 命中 module 缓存。
- 这条改进对开发机 / UAT 158 都生效 —— BuildKit cache 是 docker engine 本地的,
  跨构建跨主机不共享,所以 158 上第一次 build 也是 50s 起步,后续 build 才快。

---

## [0.6.24] - 2026-10-02

本轮是第八轮 UAT 反馈 —— v0.6.23 深色模式下 PageLoading 现场问题：
   - 1. **spinner 背景在深色模式下仍是纯白** —— 跟深色面板「撞色」很突兀。
   - 2. **spinner 没有完全覆盖 antd Table** —— proxies-page 的「操作」列(`fixed: 'right'`)漏到蒙板外面。

### 变更

- **PageLoading 背景改用 `--color-bg`**。`web/src/components/page-loading.tsx`：
   - 原 `background: var(--color-bg-container, #fff)` —— theme.css 里没定义
     `--color-bg-container`,fallback 命中 `#fff`,深色主题下变成白色蒙板。
   - 改 `background: var(--color-bg)` —— theme.css 深浅都定义了:
     浅色 `#ffffff`,深色 `#171b24`,跟 `.panel { background: var(--color-bg) }`
     完全一致,蒙板跟面板融为一体。
- **PageLoading 强化覆盖**。`web/src/components/page-loading.tsx`：
   - `inset: 0` 之外显式写 `top: 0; left: 0; right: 0; bottom: 0; width: 100%; height: 100%` —— 兜底覆盖,跟 `inset: 0` 等价但更显式。
   - `z-index: 1` 提到 `z-index: 100` —— 0.6.23 现场实测 antd v5 Table 的 fixed-column 容器(「操作」列 `fixed: 'right'`)有自己的 z-index 栈(约 2-10),1 压不过,操作列漏出蒙板外面。100 足够压过 antd 内部层级。

### 兼容性

- 严格兼容：纯前端 CSS 微调,**无后端 / API / 数据库变更**。
- 浅色主题下 `--color-bg` 是 `#ffffff`,视觉跟 0.6.23 一致(本来就是白的,只是深色下撞色)。

### 用户须知

- 深色模式下 spinner 蒙板跟 panel 背景同色(`#171b24`),不再「撞色」。
- antd Table 的 fixed 列(`fixed: 'right'`)现在被蒙板正确覆盖,加载时看不到半截表格。
- 浅色主题观感不变。

---

## [0.6.23] - 2026-10-01

本轮是第七轮 UAT 反馈 —— v0.6.22 的「拉址感」和同步历史的两大问题：

1. **PageLoading 跟表格是同一层**：用户原以为是覆盖在表格上面的一层，结果是 flow 里的兄弟元素，加载前后表格被「拉址」（被推开/拉回）。
2. **同步历史展开时很突兀**：用户希望像「过场动画」一样自然衔接。
4. **同步历史不能实时更新**：展开 task 后，正在跑的 sync 完成时新 run 不显示。

### 变更

- **PageLoading 改成 position: absolute 覆盖层**。`web/src/components/page-loading.tsx`：
  - 移除 `height` prop（不再需要 flow 高度），改成 `position: absolute; inset: 0`，覆盖在父容器的内容盒之上。
  - 加 `background: var(--color-bg-container, #fff)` 挡住下层 Table —— 没有 background 的话，spinner 浮在数据上方像「正在更新」而非「正在加载」。
  - 加 `zIndex: 1` 保证盖在 Table 上面。
  - 保留 v0.6.22 的状态机（minDuration=250ms + fadeDuration=350ms 淡出），现在 fade 时 spinner 在 Table **上面**淡出 → 用户看到「spinner → spinner 半透明（底下表格露脸）→ 表格」三段过渡，不是硬切也不是被推开。
- **6 个页面 Table+PageLoading 包 position: relative 容器**。`web/src/pages/{images,pull,proxies,credentials,stats,sync}-page.tsx`：
  - 每个 Table site 外层加 `<div style={{ position: 'relative', minHeight: 200 }}>`（images-page 和 stats Top 10 复用现有 `.table-scroll` 加 `position: relative`，已有 min-height: 240px）。
  - PageLoading `inset: 0` 填满这个容器，不再占 flow 高度 → 表格加载前后不再被「拉址」。
- **同步历史展开过场动画**。`web/src/app.css` 新增 `@keyframes expanded-row-fade-in`（opacity 0→1 + translateY -4px → 0，250ms ease-out），`expanded-row-anim` 类挂到 Task Table 和 Run Table 的 `expandedRowRender` 输出上。
  - Antd Table expandable 行的高度跳变没法直接 transition（高度由内容决定），但内容「落地」是平滑的 —— 「过场」感来自内容淡入，不是行高动画。
  - `prefers-reduced-motion` 下关闭，跟 `.page` 类一致。
- **同步历史实时刷新**。`web/src/pages/sync-page.tsx`：
  - `loadTaskRuns(taskId, opts?: { force?: boolean })` —— force 模式绕过 `loaded` 短路，给轮询用。
  - 新增 useEffect：当前 `expandedTaskIds` 里凡 `lastRunStatus === 'running'` 的 task，每 3s 调一次 `loadTaskRuns(taskId, { force: true })`。
  - task 不再是 running 或用户收起 task 时，interval 自动 cleanup。
  - 跟父页面对 running task 的 3s 轮询对齐，肉眼可接受的延迟。

### 兼容性

- 严格兼容：纯前端展示层变更，**无后端 / API / 数据库变更**。
- PageLoading 移除了 `height` prop（原来 6 个调用方传的 height 值），但 height 本来就是「不被使用了」的 prop，所有调用方已清理。
- 「`lastRunStatus === 'running'`」字段在 v0.6.11 已经存在，前端一直用来显示「正在同步」Tag，本轮复用为轮询条件。

### 用户须知

- spinner 不再跟表格「上下挤」：加载前后表格位置完全不动，spinner 浮在上面淡出。
- 同步历史展开：Task 行 + 点开 Run 行都有 250ms 淡入动画，看着像「过场」不是「硬切」。
- 展开正在运行的 task 的历史：每 3s 自动刷新，新完成的 run 会出现；task 跑完或收起 task 后自动停止轮询。

---

## [0.6.22] - 2026-10-01

本轮是第六轮 UAT 反馈 —— 用户实测 v0.6.21 的 spinner「还是秒突兀」：

1. 转圈太快，看着「一闪一闪」。
2. 数据回来 → spinner 立刻消失 → 真表格出现，「等页面元素都渲染完后再淡出」。

### 变更

- **PageLoading 重写为受控 + 内部状态机**。`web/src/components/page-loading.tsx`：
  - API 改为必须传 `visible: boolean`(原来是「直接渲染就显示」,现在是受控)。
  - 内部状态机:`shown`(DOM 是否挂载)/ `hiding`(是否在淡出)。`visible` 从 true → false 时:
    1. 等 `minDuration`(默认 250ms) —— 让 React 把真表格 commit 到 DOM 完成首帧
    2. `hiding=true`,opacity 1→0 走 `fadeDuration`(默认 350ms) ease 过渡
    3. `shown=false`,DOM 卸载
  - 淡出期间 `pointer-events: none` —— 用户可以提前看到下面的真表格「露脸」,过渡不挡视线。
- **CSS: 全局放慢 spinner 旋转**。`web/src/app.css`:
  - antd Spin 默认 `animation-duration: 1s`,看着像在闪。改成 2s(只命中 PageLoading 里的 Spin,通过 `[role="status"][aria-live="polite"]` 缩小范围)。
  - 不动 Button 的 loading spinner(那是「按钮正在点」语义,快一点更合手感)。
- **6 个页面从 ternary 改成同时渲染**。`web/src/pages/{images,pull,proxies,credentials,stats,sync}-page.tsx`:
  - 旧模式 `{loading ? <PageLoading /> : <Table />}` —— React 立即卸载 spinner,没有过渡可言。
  - 新模式:Table 用 `<div hidden={loading}>` 包裹(antd Table 不接 `hidden` 属性),PageLoading 用 `visible={loading}` 受控,两个元素始终共存。
  - `loading=true` 时:Table 隐身,spinner 占据位置;`loading` 翻 false:Table 立刻露脸,spinner 走 250+350ms 淡出,视觉上「spinner → spinner 半透明(底下表格露脸) → 表格」三段过渡。

### 兼容性

- 严格兼容:纯前端展示层变更,**无后端 / API / 数据库变更**。
- PageLoading API 改了 (`visible` 从可选变必传),但调用方 6 个页面都同步改了。
- 「后续轮询走 antd Table 自带半透层」的语义不变 —— 仍是只在 `dataSource 为空 + loading` 的首屏场景走居中 spinner,后续轮询 (dataSource 非空) 走 Table 自己的 loading prop。

### 用户须知

- 转圈速度从 1s/圈降到 2s/圈,看着「不那么急」。
- 数据回来不再硬切:先等 250ms(让表格「落地」),再 spinner 淡出 350ms。
- 切页/刷新时整段观感更柔。

---

## [0.6.21] - 2026-10-01

本轮是第五轮 UAT 反馈 —— 用户实测全站加载占位「一闪一闪的」，要求加一个明确的加载图标。

### 变更

- **全站首屏加载占位：TableSkeleton → PageLoading**。`web/src/components/page-loading.tsx` 新增；六个页面的首屏占位从「antd Skeleton 灰块 shimmer 假装表格」换成「居中 `<Spin>` + 一行小字」。
  - 之前 TableSkeleton 的灰块 shimmer 动画频率看着像在闪；「假装表格」跟真实表格差太远，数据到达瞬间「假装 → 真」的硬切也有 flicker 感。
  - 现在 `<PageLoading tip="正在读取镜像列表…" />`：明确告诉用户「在等接口数据」，数据到达时 spinner 直接收掉，过渡更干净。
  - 颜色自动跟 ConfigProvider 的 `colorPrimary` 对齐（默认 teal `#0d9488`），不用每个页面单独配。
  - 删除 `web/src/components/table-skeleton.tsx`（无人再 import）。
  - 受影响页面：images / sync（任务列表 / 运行历史 / 定时规则 共 3 处）/ pull / proxies / stats（Top 10 / 客户端 / 事件 共 3 处）/ credentials。每个调用位都定制了 tip 文案（例：「正在读取镜像列表…」）。

### 兼容性

- 严格兼容：纯前端加载占位的视觉样式替换，**无后端 / API / 数据库变更**。
- API：没有新增任何 type / 接口。
- 用户操作路径不变：仍然「首次切页 → 等接口 → 数据出现」，中间环节变成居中 spinner 而已。

### 用户须知

- 所有页面首屏的灰块骨架换成居中旋转图标 + 一行小字（具体小字根据页面不同：「正在读取镜像列表…」/「正在读取同步任务…」/「正在读取 Top 10 榜单…」等）。
- 「刷新」（已有数据，loading=true）依然走 antd Table 自带 spinner（半透明遮罩），跟首屏居中 spinner 不冲突。

---

## [0.6.20] - 2026-10-01

本轮是第四轮 UAT 反馈 —— 用户实测 sync 历史两大问题：

1. 「点运行历史按钮 → 弹 Modal → 在 Modal 里展开 run」这种「弹窗再下拉」的两段式交互「很突兀」，要求直接 Task 表行内嵌展开。
2. items 表的 Pagination「点了第 2 页还是高亮第 1 页」—— 高亮没跟住。

后端 / API / 数据库**完全不动**，仅前端 sync-page.tsx 重构。

### 变更

- **同步历史 · Task 表行直接展开，去 Modal（UI-11）**。`web/src/pages/sync-page.tsx`：
  - 砍掉 `historyTask` / `historyRuns` / `historyLoading` / `openHistory` / `closeHistory` / `historyColumns` 六个旧 Modal 配套；新增 `runsByTaskId: Record<taskId, { runs; loading; loaded }>` 按 task 缓存 runs（支持多 task 同时展开对比，不互相覆盖）。
  - 任务列表表行加 expand 控件，展开区渲染内嵌 Run Table，Run Table 行可二次展开 → (repo, tag) 明细。两段式交互变两段式 UI（任务行 → run 行 → 明细），全部在主页面。
  - 「运行历史」图标按钮删除（expand 箭头自身就是入口，跟 actions 区图标按钮不冲突）。
  - 「加载更多」按钮删除 —— Pagination 翻页 = 整页替换，简单一致；之前的 `append` 模式留下「翻页换内容 + 加载更多再 append」两套心智模型，没必要。
- **同步 · items 分页高亮修复（UI-10 续）**。`web/src/pages/sync-page.tsx` 的 `loadRunItems`：
  - 新增 `currentPageByRunId: Record<runId, number>` 显式 state；`Pagination.current` 直接读这个值（不再用 `Math.floor(loadedCount / PAGE_SIZE) + ...` 推算）。
  - 0.6.18 用 `loadedCount` 推算 `currentPage` 的算法在「最后一页不满 PAGE_SIZE 条」时算出错的 current：比如总 51 条、第 2 页只有 1 条，`Math.floor(1/50) + (1%50===0 ? 0 : 1) = 0+1 = 1`，跳到第 2 页时高亮落回第 1 页。改成显式 state 后无此问题。
  - `loadRunItems` 简化：drop 掉 `'next'` case（append 模式不再用），jumpTo 时清掉旧 items、按目标 offset 重拉、`currentPageByRunId[runId]` 写回新值。

### 兼容性

- 严格兼容：纯前端重构，后端 / API / 数据库**完全不动**。v0.6.16 的 `GET /api/sync/{id}/runs/{rid}/items` 端点签名不变。
- 用户操作路径变化：原来「点按钮弹 Modal」 → 现在「点 + 展开」。已展开的任务行（`expandedTaskIds`）不持久化，刷新页面回到全部折叠状态（跟原 Modal 的 destroyOnClose 行为一致）。
- 多任务同时展开对比的能力不变：原 Modal 一次只能看一个 task 的历史（打开第二个会覆盖第一个的 `historyTask`）；现在同时展开多个 task 的行不会互踢。

### 用户须知

- 「同步历史」按钮不再存在 —— 任务行左侧的 `+` 控件就是入口。
- 单个 task 的 runs 上限 50 条（后端默认 limit 没改）。
- 翻页高亮：每条 run 的 (repo, tag) 明细独立翻页状态，互相不干扰；切换 task 后独立保留。

---

## [0.6.19] - 2026-10-01

本轮是 0.6.18 之后的第三轮 UAT 反馈 —— 三项设置页 / 复制按钮文案收口，**无后端 / API / 数据库变更**。一次发版，version +1。

### 变更

- **设置页 · 字段重命名 + 默认值变更**。`web/src/pages/settings-page.tsx`：
  - 「拉取平台白名单」→「拉取镜像的架构」：旧名直译且跟下面 chip 选项（`linux/amd64` 等 OS/arch 字串）不匹配，新名强调「我拉下来的镜像需要是哪些架构」—— 跟 arch 字串一一对应。
  - 「第三方拉取源」→「其它匿名的第三方源」：旧名「第三方拉取源」歧义（看不出是要账号密码还是要匿名），新名明确强调「**匿名**可访问」的语义。需要账号密码的私有 registry 指引到「凭据管理」里的 username/password 配法。
  - 默认值从「空数组 = 全平台」收紧为 `['linux/amd64']`（X86 only）。这是**新用户初始默认值**的收紧，storage 里既存的空值（拉所有架构）**不受影响** —— 现有用户的设置语义不破坏。「重置」按钮文案相应从「清空（恢复全部）」改为「重置为仅 X86」。
  - 侧栏锚点 `settings-platforms` 同步重命名为「拉取镜像的架构」，保持字段命名一致性。
- **镜像列表 · 复制按钮去掉 Tooltip（UI-6a 续）**。`web/src/components/image-detail-drawer.tsx`：
  - 移除 `<Tooltip title={cmd}>` 包裹：旧版的 tooltip 跟按钮「复制命令」的语义重复，悬浮用户本来就打算复制，看到「docker pull <ref>」是「预演」一遍，没有信息增量。真正的复制反馈在顶部 toast 「已复制 <runtime> 命令: <cmd>」里 —— 信息量已经覆盖 tooltip。
  - 按钮自身保留图标 + `aria-label="复制 pull 命令"`（屏幕阅读器友好），鼠标悬浮不再显示文本。
- **镜像列表 · 复制成功只弹一条 toast（UI-6a 续）**。`web/src/components/image-detail-drawer.tsx`：
  - 0.6.17 的实现是 `copy()` 内部 `message.success('已复制')` + 调用方 Dropdown onClick 里 `message.success('已复制 <runtime> 命令: …')` —— 用户实测同时弹出两条。
  - 现在 `copy(text)` 内部不再 toast（成功返回 `void`，失败走 `modal.error` 让用户手动选），「已复制 <runtime> 命令」由 Dropdown onClick 唯一负责。一次复制 = 一条反馈。

### 兼容性

- 严格兼容：三项都是 UI 文案 / 默认值 / 反馈条数微调，无后端 / API / 数据库变更。
- 「拉取镜像的架构」默认值收紧只对**从未保存过设置页的新用户**生效：现有用户的 storage 值不变。语义上「空 = 全平台」也仍然合法 —— 但 UI 上不再提供「空」这个状态，重置按钮会写到 `['linux/amd64']`。

### 用户须知

- 设置页侧栏「拉取镜像的架构」下面的 chip 选项没变（`linux/amd64` / `linux/arm64` / `linux/arm/v7` / `linux/ppc64le` / `linux/s390x`）；首次进入默认值是 `linux/amd64`，需要其它架构手动勾上。
- 镜像列表复制按钮：鼠标悬浮不再有 tooltip；点按钮 → 菜单展开 → 选 runtime 才复制；成功反馈只弹一条 toast（runtime + 命令）。
- 镜像列表 / 抽屉里的所有原有交互（删除 / 查看 digest / 架构列表 / 层列表）保持不变。

---

## [0.6.18] - 2026-10-01

本轮是 0.6.15-0.6.17 的 UAT 反馈收尾 —— 四条实测观察全部为「既有 UI 一致性 / 体验」修缮，无后端变更。一次发版，version +1。

### 变更

- **代理管理 · 取消横向滚动 + 状态/延迟合并一列（UI-1 续）**。`web/src/pages/proxies-page.tsx`：
  - 去掉 `tableLayout="fixed"` 与 `scroll={{ x: 1200 }}`：上一版加这两个是怕窄屏出双滚动条，但实际测下来 UAT 用户的容器宽度足以容纳整表，scroll.x 反而在更窄的窗口里触发第二条横滚条。
  - 「状态」+「延迟」两列合并成一列，文案 `可用 (0.9 ms)` / `不可用 (—)` / `探测中 (…)` / `未探测 (—)`。不可用时延迟显示 `—` 而非数字（探测失败时 TCP 没建连，延迟无意义；用占位符区分「还没探过」与「探过但失败」）。探测中延迟显示 `…` 让用户看到「还在跑」不是「卡死」。
  - 列宽重排：「代理地址」250→220、「认证」160→140、「更新时间」150→140，整表总宽从 1180 降到 ~1080，不再溢出容器。
- **镜像列表 · 复制按钮只用 Dropdown（UI-6a 续）**。`web/src/components/image-detail-drawer.tsx`：
  - 移除 Button 自带 onClick 的「单击复制当前 runtime」逻辑 —— 0.6.17 这一行 `onClick` 与 Dropdown trigger={['click']} 同时触发，导致「单击既复制 docker 命令又弹出下拉列表」，用户反馈歧义。
  - trigger 改成 `hover`，单击 Dropdown 按钮 → 鼠标移入即展开菜单（明确的「这是个菜单」语义）。
  - 复制 / 删除两个按钮的 `<Space size>` 从 2 → 8，避免误点删除（删除是高危操作，必须明显隔离）。
- **镜像拉取详情 · phase 行 bytes / message cell 占位（UI-9）**。`web/src/pages/pull-page.tsx` 的 `JobPhases`：
  - bytes cell 之前 manifest phase（无 `totalBytes`）渲染空字符串、blob phase 渲染 `0 B / 973 B` —— 两种宽度从 0 跳到 80px，行的 reflow 触发整面板「反复横跳」。
  - 现在 bytes cell 始终渲染（有内容显示内容，无内容显示 `—` 占位符），并加 `minWidth: 90 / display: inline-block` 让宽度恒定。
  - 同样的修复应用到 message cell：空 message 时显示 `—`，加 `minWidth: 180` 占位。两列恒定宽度后行高与行宽都稳定，不再 reflow。
- **同步历史 · 翻页实做 + 删除文案简化（UI-10）**。`web/src/pages/sync-page.tsx`：
  - 0.6.16 Pagination 的 `onChange={(p) => void p}` 是占位实现 —— 用户实测反馈「翻页无效」。这版把 `loadRunItems` 改为支持 `{ jumpTo: N }` union case：跳页时清掉当前 items、按目标 offset 重新加载。
  - 删除任务的 Popconfirm 文案「任务的历史运行记录会一起删除（外键 CASCADE），无法恢复」中的「外键 CASCADE」是数据库术语，非工程背景的用户看不懂。简化为「任务的所有历史运行记录会一并删除，无法恢复」。

### 兼容性

- 严格兼容：四项都是 UI 表现层微调，无后端 / API / 数据库变更。
- 「代理管理」列从 8 列减到 7 列；列宽和下降 ~100px，列表观感更紧凑。
- 「同步历史」翻页语义变化：0.6.16 用户在第 2 页后点第 5 页无效；0.6.18 起会清掉当前已加载的 items、按目标 offset 重新加载（这是「跳页」的常识语义）。

### 用户须知

- 代理列表：探测中那一行状态变成 `探测中 …`（`…` 是延迟占位）；可用变成 `可用 0.9 ms`；不可用变成 `不可用 —`。
- 镜像列表：复制命令按钮改成「hover 展开菜单 → 选 runtime 才复制」。单击不再复制；想一键复制 docker 的用户得 hover → 选 docker（仍然 < 1s）。
- 镜像拉取详情：phase 行 bytes / message 两列现在恒定宽度，新 phase 加入时面板不再上下抖动。
- 同步历史：展开 run → 翻页器跳到任意页都生效；删除任务的提示文案不再提「外键 CASCADE」。

---

## [0.6.17] - 2026-10-01

本轮把「镜像列表 → 仓库抽屉 → tag 行」的复制命令从单一 `docker pull` 升级为按 runtime 切换（docker / podman / nerdctl / ctr）。证据与诉求见 [docs/issues/ui-0.6.14.md#ui-6a](./docs/issues/ui-0.6.14.md)。

### 新增

- **前端 · 复制命令按 runtime 切换**。`web/src/utils.ts` 暴露 `PullRuntime` 类型 + `PULL_RUNTIMES` 清单 + `buildPullCommand(host, repo, tag, runtime)` 工厂。`docker` / `podman` / `nerdctl` 三个 CLI 命令形态一致，统一用 `${cli} pull <ref>`；`ctr` 命令不同（必须指定 namespace），用 `ctr -n k8s.io images pull <ref>`（`k8s.io` 是 kubelet 默认 namespace）。
- **前端 · 行操作按钮改为 Dropdown**。`web/src/components/image-detail-drawer.tsx` 把原来的 `<Button icon={CopyOutlined}>` 包进 `<Dropdown>`，单击 = 复制当前选中的 runtime 命令（保持原「一键复制」工作流），菜单 = 切换 runtime（选中即复制 + Toast 反馈）。当前选中 runtime 是组件级 state（多数用户在同一台机只用一种 CLI，逐行记忆无意义），Drawer 重开时回落到默认 `docker`。

### 兼容性

- 严格兼容：默认 runtime 是 `docker`，单击复制的命令与 0.6.16 完全相同。`buildPullCommand(host, repo, tag)` 不传 runtime 时退化为旧签名，老 caller 无需修改。
- 不改后端 / API / 数据库。

### 用户须知

- 单击复制按钮：复制当前 runtime 的命令（默认 docker，UI 不变）。
- 点开 Dropdown 选其它 runtime：立刻复制对应命令，顶部 toast 提示「已复制 podman 命令: …」。
- `ctr` 多出来的 `-n k8s.io` 是 namespace 参数，非 K8s 用户需要手动从复制内容里删掉或换成自己的 namespace。

---

## [0.6.16] - 2026-10-01

本轮把 `sync_runs` 的「只露汇总 + 最后失败 tag」升级到「按 (repo, tag) 展开每条尝试」。记录在 [docs/issues/ui-0.6.14.md#ui-4](./docs/issues/ui-0.6.14.md) 的 schema 缺口，本轮一次性补上：后端落明细表 + 前端展开面板 + API 分页端点。证据与判定过程见 issue。

### 新增

- **后端 · `sync_run_items` 表（schema v10）**。`internal/db/db.go` 新增 v10 迁移：`CREATE TABLE sync_run_items` + `(run_id, state, error, bytes_done, bytes_total, started_at, finished_at)` + `FOREIGN KEY(run_id) REFERENCES sync_runs(id) ON DELETE CASCADE` + 索引。粒度 (repo, tag) 对齐引擎实际的一次 `pullTag` / `pushTag` 调用，与已有 `pull_jobs` 对称。
- **后端 · 引擎写入路径改造**。`internal/sync/engine.go` 把 `pullRepo` / `pushRepo` 的内层循环从「遇错即返回整个 repo」改成「每 tag 独立 attempt」：`recordRunItem` 在 `pullTag` / `pushTag` 完成后写一行（succeeded / failed），失败不阻断剩余 tag。仓库级失败计数仍然映射到 `repos_failed`，但**真正的失败明细**现在在 `sync_run_items` 里。
- **后端 · `GET /api/sync/{id}/runs/{rid}/items?limit=&offset=`**。新增 `RunItemsPage` envelope（items + total + limit + offset），limit 默认 50、上限 500，cross-tenant guard 校验 run 真的属于 URL 里的 task。
- **前端 · 历史 Modal 行可展开**。`web/src/pages/sync-page.tsx` 的 `<Table>` 加 `expandable`：每条 run 行有 + / − 按钮，展开区是内层 `<Table>`，列：仓库 / tag / 结果（succeeded / failed / cancelled）/ 耗时 / 字节（失败时 `0 B / X`，红字）/ 错误。明细懒加载（首次展开触发，缓存到 Modal 关闭），「加载更多」按 50 条/页翻页。
- **前端 · `formatBytes` 工具函数**。镜像层大小用 KiB / MiB / GiB 二进制单位显示，跟 cairn 自己的「镜像层合计」口径一致。

### 兼容性

- 旧 run（schema < v10 时写入的）`sync_run_items` 表为空 —— 前端展开区显示 Empty「该 run 没有明细（旧版本引擎或运行中尚未落库）」。这是预期的历史数据空窗，不是 bug。
- 引擎层「第一次 tag 失败就 short-circuit 整个 repo」的行为被改掉 —— 同一个 repo 多个 tag 部分失败时，原本只能看到第一条错误；现在能看到所有 tag 的成败明细。`repos_synced` / `repos_failed` 计数语义不变（任一 tag 失败 → repo 计数为 failed）。

### 用户须知

- 「同步」页历史 Modal 里每条 run 现在可以 + / − 展开看 (repo, tag) 明细。一个有 77 个 tag 的 run 展开后只显示前 50 条，要看更多点「加载更多」。
- 镜像大小列：成功时显示「总字节」（manifest 声明的 layer + config 合计），失败时显示「0 B / 总字节」，用红色 0 提示「压根没传完」。
- 已有的「镜像拉取」页历史可展开明细是 v0.4 起的功能，本轮同步镜像对齐到同一信息粒度。

---

## [0.6.15] - 2026-10-01

本轮是 0.6.14「UI 一致性收尾」延续：UAT 手工测试发现代理管理 / 设置 / 热度三个页仍有 4 处提示或布局上的小毛刺，统一在此收尾。证据与判定见 [docs/issues/ui-0.6.14.md](./docs/issues/ui-0.6.14.md)。

### 变更

- **代理管理 · 「状态」列定宽（UI-1）**。`proxies-page.tsx` 引入 `STATUS_TAG_STYLE`（`minWidth: 88`、`inline-flex` + 居中），四种状态标签（`探测中` / `可用` / `不可用` / `未探测`）挂同一份样式。antd `<Table>` 在 auto layout 下原本按单元格内容重算列宽——`探测中` (3 字) → `可用` (2 字) 切换时整表重排，把右侧固定列首按钮挤到文字截断（`探测中…` 只露前半截）。定宽后四种状态同宽，列宽不再随状态跳变。宽度按最长状态「不可用」+ 图标定 88px。
- **代理管理 · 新建代理合并双 toast（UI-2）**。`probeOne` 增加 `opts.silent` 选项，新建代理提交路径改用 `silent: true`，由调用方根据探测结果播报一条合并 toast：可用 → `已创建代理 Bigops53，可用 · 延迟 0.9 ms`，不可用 → `已创建代理 Bigops53，但探测失败：<原因>`（必须明确「已创建」—— 代理已落库，不让用户误以为失败去重填）。逐行「探测」按钮仍走原路径，行为不变。
- **设置 · 测试连接 / 刷新清单走单一 message channel（UI-3）**。`settings-page.tsx` 的 `handleProbe` / `handleRefresh` 改为**只**走 `message.success / error`，不再写 `notice` state、也不再渲染顶部 `<Alert>`。旧行为：刷新清单成功时**同时**弹 Alert 与 toast，两种提示载体同屏并存。0.6.14 划清的边界「页面级 `<Alert>` 仅保留 load failure / vault 不可用」不变，本条只处理**操作反馈**。
- **镜像热度 · Top 榜单只显示 Top 10（UI-5）**。`stats-page.tsx` 新增常量 `TOP_LIMIT = 10`，`Table.dataSource` 改为 `topItems.slice(0, TOP_LIMIT)`，关掉分页器（不存在「第二页」，留着分页 UI 反而误导）。`max-height: 460px` 的 bounded 容器原本在 15 项时出第二条竖向滚动条（页面外层还有一条），10 项 ~360px 全部放得下，不再触发内层滚动。

### 不修项（已登记，单独议题）

- **UI-4 同步历史缺逐 tag 明细** —— 已源码确认：`sync_runs` 表只存 `repos_total / repos_synced / repos_failed` 三个计数器，逐 tag 错误只进日志（`engine.go:381-382`），需要新增 `sync_run_items` 表 + 改造写入路径，属 schema 变更。详见 [docs/issues/ui-0.6.14.md#ui-4](./docs/issues/ui-0.6.14.md)。
- **UI-6a 按 runtime 复制命令** —— 新增能力（中版本口径），需人拍板。
- **UI-6b 页面直接导出 tar** —— 新增能力 + 架构议题（cairn 后端流式生成 vs 退到「复制 `docker save` 命令」），需人拍板。

### 兼容性

- 严格兼容：四项都是 UI 表现层微调，无后端 / API / 数据格式变更。
- 「操作反馈」从 `<Alert>` 改 message toast：成功路径不再在设置页顶部留绿色横条，0.6.14 用户须知已说明「同一种反馈可能并存短暂」—— 本条进一步把它合并成一条，行为更收敛。

### 用户须知

- 代理列表首列「状态」标签从此四种状态同宽；不再有「按钮被压到截断」现象。
- 新建代理后看到的就是**一条** toast，内容是「创建 + 探测结果」的合并结果。
- 设置页点「测试连接」/「刷新清单」后只弹一条 toast，顶部不再出现横条。
- 镜像热度 Top 榜单从此只显示前 10 名；下方 KPI 卡片「有活动的仓库数」继续显示真实总数（可能 > 10）。

---

## [0.6.14] - 2026-10-01

本轮是 UI 一致性收尾:把「测试连接」类的弹窗内嵌结果从 `<Alert>` 渲染载体换成 message toast,与「探测 N 个代理」等其他动作共用同一条 message channel。

### 变更

- **「同步」页 / 「代理管理」页：测试连接结果从内嵌 Alert 改成 message toast**。
  - 此前「新建/编辑同步任务」弹窗与「新增/编辑代理」弹窗里的「测试连接」按钮把结果(可达 / 凭据错 / URL 不像 registry / 连通失败 / 上游异常)渲染在弹窗顶部,占掉一整段垂直空间 —— 在窄屏 / 长表单上会撑出整页滚动条。改成 `message.success / error / warning / info` 后,结果用跟「探测 N 个代理」「已删除代理」「已更新代理」同样的 toast 通道弹出,不占弹窗内空间,也不再有滚动条。
  - 状态码分档不变:2xx 绿 / 4xx 蓝(代理可达,目标按业务规则拒绝) / 5xx 黄(代理可达,上游异常) / 传输层失败红;sync 探测的 authStatus 分类(ok / no_auth_required / wrong_creds / required_but_missing / not_registry)也按原有语义映射到 message 等级。
  - 两处 handler 共用一个 `proxyResultToast()` helper(sync-page 的转换就地写在「测试连接」按钮 onClick 里,因为它的 authStatus 分类跟 proxies 不一样,跟 proxyResultToast 不通用),文案不再两边漂移。

### 兼容性

- 严格兼容:UI 行为变化只发生在「用户点了弹窗里的『测试连接』按钮」之后那一瞬间;之前的内嵌 Alert 让用户多滚动一次才能看到结果,新 toast 直接浮在视口顶端,交互更顺。
- 无后端 / API 变更。

### 用户须知

- 现在测完一次,改完 URL / 密码再点测试,会看到一条**新**的 toast,而不是覆盖之前那条内嵌 Alert —— 连续测两次会有两条 toast 短暂并存,符合 antd message 的默认行为。
- 内嵌 Alert 退场,但页面级的 load failure / vault 不可用 等 `<Alert>` 仍保留(那是页面状态而非操作反馈,职责不同)。

---

## [0.6.13] - 2026-10-01

本轮是 0.6.11 回归报告 [docs/issues/](./docs/issues/) 仓内剩余 3 条 Low 协议缺陷的合并修复版:REG-3 / REG-5 / REG-6。三项按 patch 口径发布(REG-6 的「新增取消能力」按「既有功能漏写」归类,不到中版本门槛)。

### 修复

- **REG-3 · `/v2/` 缺 `Docker-Distribution-Api-Version` 响应头 + `HEAD` 返 404**。`apiVersion` handler 现在带 `Docker-Distribution-Api-Version: registry/2.0` 头(OCI Distribution Spec §"Docker Distribution API Version Header" 要求),并且把根路由从 `r.Get("/", ...)` 改成 `r.HandleFunc("/", ...)`,让 `GET /v2/` 和 `HEAD /v2/` 都返 200。之前 docker daemon / skopeo / 监控探针(只发 HEAD 的)会因 404 误判服务不可用。详见 [docs/issues/registry-protocol.md REG-3](./docs/issues/registry-protocol.md)。
- **REG-5 · blob 缺显式 `Content-Type` + `Range` 被静默忽略**。`blobGet` / `blobHead` 现在显式声明 `Content-Type: application/octet-stream` + `Accept-Ranges: none`(Distribution Spec 的「不实现 Range」标准声明)。之前 Content-Type 来自 Go 的内容嗅探,层文件头几字节决定返回 `text/plain; charset=utf-8` 还是别的,误导自研客户端/镜像校验工具。Range 行为没真实现(仍是 `200` + 全量,不是 `206`),只是把「沉默忽略」改成契约明示 —— 真要做 Range 是单独议题。详见 [docs/issues/registry-protocol.md REG-5](./docs/issues/registry-protocol.md)。
- **REG-6 · `DELETE /v2/<repo>/blobs/uploads/<uuid>` 返 405,无法取消 upload session**。docker CLI 在 push 中途 Ctrl-C 时发 DELETE 取消上传,以前服务端返 405 + `Allow: GET, PATCH, PUT`(DELETE 不在支持集合),session 目录只能等下次 GC(最坏 24h)回收。`Storage.CancelUpload` 接口早已存在但从未被 dispatch,现在补上 `uploadCancel` handler + 在 dispatcher 的 upload `switch r.Method` 加 DELETE case + `Allow` 头加 `DELETE`。成功 → 204;二次 DELETE 仍返 204(`os.RemoveAll` 对不存在的 dir 静默 no-op),符合 REST DELETE 幂等语义。详见 [docs/issues/registry-protocol.md REG-6](./docs/issues/registry-protocol.md)。

### 兼容性

- **REG-3** 严格向后兼容(只新增响应头 + 把 GET 路由扩展到 HEAD);但仍要扫一遍 docker daemon / 监控探针是否对版本头有奇怪的解析(已知 history 上没人做白名单检查,应无事)。
- **REG-5** 严格向后兼容(只新增响应头);Range 行为从「沉默忽略」变「契约明示」,客户端没有任何破坏性影响。
- **REG-6** 兼容性微妙:`405 UNSUPPORTED` 不再出现;客户端按 405 → 405 重试的代码路径会变成 204。需要给 docker daemon / 自研客户端提个醒 —— 但实际上客户端都在等 204 / 404,405 是错路径,这次修复只是把错误形态对到主流实现。

### 用户须知

- 上传过程的 Ctrl-C 行为从此生效:以前 docker push 失败后 `uploads/<repo>/<uuid>/startedat` 残留;现在 Ctrl-C 时会真的清掉。**长期运行下孤儿目录堆积问题得到根治。**
- 给 upload session 加 TTL 清理(启动时扫 `startedat`/mtime 超期目录自动 `rm -rf`)的补丁本轮**没做**,理由:REG-6 已经覆盖了主要场景(客户端发 DELETE 就清),TTL 清理只针对断连、客户端 crash 这类"客户端来不及发 DELETE"的边角,可放后续下版。

---

## [0.6.12] - 2026-10-01

本轮是 0.6.11 回归报告 [docs/issues/](./docs/issues/) 列出的 10 条仓内可修缺陷（MA-1/3/4/5/6 + REG-1/2/4 + TH-1/4/5）的统一修复版。其余 8 条（REG-5/6、TH-2/3/6/7/8/9/10）按登记文档留待下版或 53 runner 侧独立处理。

### 修复

- **MA-1 · `/api/repositories/{repo}` 含 `/` 的仓库名路由失效**（High）。158 上 84% 真实仓库受影响，前端「删除仓库」「按 digest 删 manifest」对这批仓库全报错。改为 wildcard dispatcher 仿协议侧 `internal/registryd/routes.go:115-123` 的处理：自己切分路径，把 `repo` / `tag` / `digest` 注入 chi route context，让现有 handler 零改动。前端 `web/src/api.ts:234, 241` 同步去掉 `encodeURIComponent(repo)`，URL 可读性顺便好转。详见 [docs/issues/management-api.md MA-1](./docs/issues/management-api.md)。
- **MA-5 · sync 删除端点返 `204` 撞前端 JSON 信封 → 假阴性「删除失败」**（High）。`DELETE /api/sync/{id}` 与 `DELETE /api/sync/{id}/schedules/{sid}` 改为 `200` + JSON 信封 `{"id":...,"deleted":true}`，与本仓其他 DELETE 端点口径一致。前端 `request()` 不再把空 body 误判为「非 JSON 响应」，列表自动刷新。详见 [docs/issues/management-api.md MA-5](./docs/issues/management-api.md)。
- **MA-3 · `POST /api/gc` 非法 JSON body 被静默忽略**。之前 `_ = decodeJSON(r, &body)` 把「EOF（无 body）」和「真解析错误（截断 JSON 等）」一起吞掉，导致 `{"cleanEmptyRepos": tru` 这种残缺 body 也走完 GC、参数静默回落为默认值。改为：EOF 仍视为「无 body」走默认；其他解析错误返 `400 BAD_REQUEST`。同时修正 `:695-697` 那段把契约描述错的注释。
- **MA-4 · `DELETE /api/repositories/{repo}` 不存在返 `500`**。补上 `storage.ErrNotFound` → `404 NOT_FOUND` 分支，与同文件 `DeleteManifestByDigest:650-656` 已有写法一致。
- **MA-6 · `GET /api/sync/{id}/runs` 漏任务存在性守卫**。不存在的任务 id 之前返 `200 + ` `[]`，与兄弟端点 `/sync/{id}`（404）和 `/sync/{id}/schedules`（404）的答案互相矛盾。补上 `Store.GetTask` 前置校验，与 `ListSchedules:426-434` 的守卫同款。
- **REG-1 · `PUT /v2/<repo>/manifests/<ref>` 不校验 payload**。47 字节垃圾 JSON 也能 `201 Created` 并持久化，错误推到客户端 unpack 阶段。补最小校验（JSON + `schemaVersion` ∈ {1, 2} + body 或 Content-Type 至少一处有 `mediaType`），不合法 → `400 MANIFEST_INVALID`。形态校验留给客户端，符合「不替 caller 做协议解释」的分轨。
- **REG-2 · 未知仓库 `tags/list` 返 `200` + 空数组**。`storage.Filesystem.Tags` 区分「仓库目录不存在」（→ `ErrNotFound`）与「tags 目录为空但仓库存在」（→ 仍 `[]`）；路由层把 `ErrNotFound` 映射成 `404 NAME_UNKNOWN`。`_catalog` 里 `tags:null` 的空仓库容忍约定（AGENTS.md 已明确）**未动**。
- **REG-4 · `DELETE /v2/<repo>/manifests/<digest>` 错误响应不规范**。`ErrNotFound` 之前返 `404` + 0 字节 body（docker CLI 报 "unexpected end of JSON input"），补上 `MANIFEST_UNKNOWN` 标准错误体；其余内部错误从错误码 `"UNSUPPORTED"` 改成 `"UNKNOWN"`（Distribution 实际码位）。顺手扫了一遍同文件其他 6 处 `500` 内部错误分支，统一替换为 `"UNKNOWN"`；`writeV2MethodNotAllowed` 里的 `"UNSUPPORTED"` 是 `405` 的规范码，保留。
- **TH-1 · Makefile 场景前缀 `cairn/` → `go-hub/`**。53 runner 实际加载的目录是 `go-hub/*`（项目曾用名），`cairn/*` 前缀全部 `ENOENT`。两处 `:` 大循环的 `scenario` 字段 + 顶部注释同步修。
- **TH-4 · `test-frontend-fast` 注释与列表脱节**。注释称「9 只读场景」、实际列 8 个、且包含 `pull-real`（写路径，会真往 registry 推送/拉取）。移除 `pull-real`（留给 `test-frontend`），注释同步改成「7 只读场景」。
- **TH-5 · `test-backend` 硬编码 `cairn:0.5.18-dev`**。改为 `$(IMAGE)` 复用版本号变量；`/root/.regpw` 不存在时直接 fail-fast 并给可读错误（不再 `|| echo admin` 静默用错密码）。

### 兼容性

- **MA-1 破坏性变更**（API 端点 URL 形态）：`/api/repositories/{repo}/*` 从「`{repo}` 必须单段」改为「`{repo}` 可含 `/`」。旧客户端若仍用 `encodeURIComponent` 把 `/` 编成 `%2F`，现在会落到 `storage: not found` → 仍然报错但语义不变（已不再 500）；修法的成本是把 `encodeURIComponent(repo)` 去掉一行。本仓前端同步修了，第三方客户端若存在需自行适配。
- **MA-5 破坏性变更**（HTTP 状态码）：`/api/sync/{id}` `DELETE` 与 `/api/sync/{id}/schedules/{sid}` `DELETE` 从 `204` 改为 `200` + JSON 信封。任何按 204 编程的脚本需改判 200。
- **REG-1 兼容性**：未对仓内分发过的镜像做回放实验。仓库自带的已有 manifest 都不在本校验之内（v2 schemaVersion=2），现状安全。
- **REG-2 兼容性**：客户端需把「拼错仓库名」从「安静看到空列表」改为「看到 404 NAME_UNKNOWN」，调用方代码若此前把 200+[] 当成功可能需要补 404 分支。
- **REG-4 兼容性**：错误体 / 错误码本身就在改的是「之前错了」的形态，调用方按空 body 解析失败的情况会改成按 `MANIFEST_UNKNOWN` 解析，更稳。

### 用户须知

- `docs/issues/` 下登记的 8 条「仓外缺陷」未在本轮处理：
  - **REG-3**（`HEAD /v2/` 一致性 + `Docker-Distribution-Api-Version` 头）、**REG-5**（blob `Content-Type` + `Range` 支持）、**REG-6**（upload cancel + 孤儿目录 TTL）：Low，下版一起做。
  - **TH-2/3/6/7/8/9/10**：均在 53 runner 侧（runner 源码 / `web-auto.dev.yaml` / 场景 YAML），不在本仓库；按各自修复方向跨期处理。

---

## [0.6.11] - 2026-09-30

### 新增

- **同步任务定时调度**。每个 sync 任务可挂多条 cron 规则,后台单 ticker 每 30s 扫一次 `sync_schedules`,到期调 `Engine.Start(task)`。
  - 后端:
    - `schema v9` 新表 `sync_schedules(id, task_id, cron_expr, timezone, enabled, next_run_at, last_run_at, last_run_id, created_at, updated_at)`,`enabled` 部分索引加速到期扫描。
    - `internal/sync/cron.go` 自实现 5 字段 cron 解析(标准 `*` `,` `-` `/` 语法,无 Quartz 扩展),`NextAfter()` 计算下次触发时间。空 timezone = UTC(显式而非本地时区,避免容器默认时区漂移导致触发时间错位)。
    - `internal/sync/scheduler.go` 单 ticker 循环:`ScheduleListDue` 拉到期 schedule → `Engine.Start` 触发 → `ScheduleUpdateAfterFire` 写 last_run_at/next_run_at。**复用 v0.6.8 SYNC-1/4 的 TryLock + detached ctx + recover**,所以定时触发跟「立即运行」一样安全。
    - 4 个新 API:`GET/POST/PATCH/DELETE /api/sync/{id}/schedules`。cron / timezone 由 `sync.Schedule.Validate` 校验,失败返 400。
  - 前端:
    - 同步任务「操作」列加「定时」按钮 → 弹 Modal 列出该任务的全部 schedules。
    - 单行编辑器: cron 表达式 / 时区 / 启用开关;「新建」按钮折叠在表格下方。
    - 错误本地化:后端 400(InvalidCron / InvalidTimezone)→ message.error 显示。

### 数据库

- `migrate` v8 → v9:`CREATE TABLE sync_schedules` + 部分索引;`user_version` 自动从 8 升到 9。
- 新建 DB 走 v1..v9 全套 migration;旧 DB (v8) 升级路径只跑 v9 这一段。
- `sync_tasks` 删除时 `sync_schedules` 由 FK CASCADE 一并清掉(同 sync_runs)。

### 兼容性

- 新增 4 个 API 端点,旧前端继续工作,只是 UI 没有「定时」按钮。
- `sync_tasks.lastRunStatus` / `lastRunCurrentRepo` / `lastRunCurrentTag` 未变;UI 现有轮询逻辑继续生效。

### 用户须知

- 默认 timezone 是 **UTC**(不是服务器本地时区)。容器默认时区可能漂移,显式 UTC 避免触发时间不可预期。要在 Asia/Shanghai 等地触发,在 schedule 表单填时区字段。
- 5 字段标准 cron(`分 时 日 月 周`),不支持 Quartz 扩展(`?` `L` `W` `#`)。
- 时间粒度 = **1 分钟**(NextAfter 按分钟迭代);秒级 schedule 不支持。

---

## [0.6.10] - 2026-09-30

### 变更

- **「刷新」与「重新扫描」合并为一个「刷新」按钮**。两者后端实际是同一个 `GetInventory` 端点（`/api/refresh` 是 `identical to GetInventory` 的旧保留别名）,两个按钮会让用户以为「重新扫描」会触发远端 registry 操作,实际啥也没做。
  - 后端:删除 `POST /api/refresh` 路由与 `Handlers.RefreshInventory`(后端早就自己注释了「identical to GetInventory」,保留它纯属 UI 误用)。修两处过期注释:`internal/registry/registry.go` 的 `CachedRegistry` 块 + `Invalidate` 注释都提到 `/api/refresh` 会失效缓存,实际从未连过,改成「TTL 过期是唯一自然失效」。
  - 前端:`web/src/pages/images-page.tsx` 砍「重新扫描」按钮,「刷新」接管,文案保留「刷新」;`web/src/pages/settings-page.tsx`「重新扫描」按钮改为「刷新清单」(同样是同一个端点)。
  - `web/src/api.ts` 删除 `refreshInventory` export;`web/src/pages/settings-page.tsx` 的 `handleRefresh` 改用 `fetchInventory`;顺手修 `web/src/components/load-error.tsx` 注释里把 `/api/refresh` 当成「非幂等接口」的例子(`/api/refresh` 其实是幂等读,被错归类了)。

### 兼容性

- **API 破坏性变更**:`POST /api/refresh` 端点删除。任何仍调用该端点的客户端会收到 404。本次只 cairn 自己用,UI 已同步更新,无第三方客户端。
- UI:两个页面各少一个按钮,无功能损失。

---

## [0.6.9] - 2026-09-30

### 新增

- **同步进度可视化**：用户在长同步任务中能直接看到「正在拉 `bklite/cloud-ide:v1.2.3`」,不必凭计数器猜是否卡住。
  - 后端:`schema v8` 给 `sync_runs` 加 `current_repo`/`current_tag` 两列(`TEXT NOT NULL DEFAULT ''`),`engine.runPull` / `runPush` 在每个 (repo, tag) 进入前通过新方法 `SyncRunUpdateProgress` 单独写这两列(单行 UPDATE,不碰其他字段);`SyncTaskList` / `SyncTaskGet` 子查询同步带出 `lastRunCurrentRepo` / `lastRunCurrentTag`。
  - 前端:任务列表「运行中」tag 旁追加蓝色 tag 显示当前 `(repo:tag)`;历史运行表的「仓库」列在 `failed` / `running` / `partial` 行下方追加一行小字「死在 / 正在 repo:tag」用于失败定位。

### 变更

- API:`GET /api/sync/` 列表响应和 `GET /api/sync/{id}` 单条响应新增字段 `lastRunCurrentRepo` / `lastRunCurrentTag`(omitempty,任务从未跑过或不在具体 repo 上时省略)。

### 数据库

- `migrate` v7 → v8:两条 `ALTER TABLE sync_runs ADD COLUMN ...`(`current_repo` + `current_tag`),`user_version` 自动从 7 升到 8。
  - 新建的 DB 在 v1 重建路径上由 v1..v6 migration + v8 ALTER 一起把两列补齐;v7 → v8 升级路径上 v8 ALTER 把列补齐。
  - 现有 sync_runs 行(无论 running / success / partial / failed)在升级后 `current_repo` / `current_tag` 默认是空字符串;**运行中**行由启动时的 `MarkStaleRunsFailed` 一次性 sweep 成 failed。

### 兼容性

- 仅 API 响应新增字段,无破坏性变化;旧前端继续工作,只是看不到进度 tag。

---

## [0.6.8] - 2026-09-30

本轮主题:**同步任务从「同步阻塞」改为「异步执行」——一次性修掉 SYNC-1~4（超时中止 / 日志噪音 / 凭据回归凭据库 / 重复发起）**

规格与复现见 [`docs/sync-known-issues.md`](./docs/sync-known-issues.md)。UAT 反馈 (`client.local`)：

1. **SYNC-1** 点「立即运行」→ 前端报 `运行失败：请求超时（10 秒）已中止: /api/sync/1/run`，
   但镜像列表里能看到部分仓库**已经同步成功** —— 界面说失败，实际还在跑。
2. **SYNC-2** 容器日志持续刷 `WARN sync: pull repo failed ... context canceled`，
   每个没跑完的仓库一条，看着像真故障。
3. **SYNC-3** 新建 / 编辑任务得手填「远端用户名 / 远端密码」，跟 [凭据管理] 重复；
   期望改成「匿名 / 选已有凭据」二选一。
4. **SYNC-4** 任务在后台跑着，刷新页面后「运行」按钮又变成可点，
   再点一次就撞上并发冲突，互相干扰。

四条的**根因是同一个**：`POST /api/sync/{id}/run` 是**同步阻塞**接口 ——
handler 拿着请求的 `context` 一路把整个 run 跑完才返回。
前端 `DEFAULT_TIMEOUT_MS = 10_000`（`web/src/api.ts:57`）一到就 abort，于是：

- 客户端侧 → 10s 报超时（SYNC-1）
- 服务端侧 → `r.Context()` 被 cancel，排队中的 registry 请求全部 `context canceled`（SYNC-2）
- 状态副作用 → manifest 已 push 进本地 registry（列表可见），run 行却永远停在 `running`（僵尸行）
- 界面无状态感知 → 列表不返回「有没有 run 在跑」，刷新后按钮照旧可点（SYNC-4）

### 根因

**① 同步阻塞 + 请求 ctx 被直接当执行 ctx**（`internal/sync/engine.go`）

`Start` 在 handler 调用栈里阻塞到 run 结束，`execute` 复用的就是 request context。
客户端一 abort，服务端在途请求全部收到 cancel。

**② 凭据只有内联一种存法**（`internal/db` / `internal/sync/types.go`）

`sync_tasks` 只存 `remote_username` / `remote_password`（明文），凭据管理库（vault）根本没接进来。

**③ 任务列表不返回 run 状态**（`internal/db/sync.go` `ListTasks`）

前端无从判断「这个任务此刻有没有在跑」。

### 新增

- **`POST /api/sync/{id}/run` 改为异步受理**：handler 只做「校验 + `TryLock` + 落 `running` 行」，
  随即 `go e.execute(context.WithoutCancel(ctx), ...)` 并立刻返回 **202 Accepted + 新 run 对象**。
  执行阶段与请求生命周期**彻底脱钩** —— 刷新 / 断网 / 关页面都不影响同步。
- **DB schema v7**：`sync_tasks` 新增 `remote_credential_id TEXT`（指向 `credentials.id`）。
  迁移**只 ADD COLUMN**，不重建表（重建会在迁移事务里 CASCADE 删掉 `sync_runs`）。
- **列表 / 详情返回 `lastRunStatus`**：`ListTasks` / `GetTask` 用子查询取该任务**最近一次 run 的状态**，
  前端据此判断「运行中」。
- **启动僵尸清理 `MarkStaleRunsFailed`**：`server.Build` 在进程启动时把所有仍是 `running` 的 run
  置为 `failed`（`error = "interrupted by process restart"`）；失败只 Warn，清出 n>0 时打 Info。
  上一轮进程被 kill 留下的僵尸行不会再永久挂住。
- **前端凭据模式三档 Radio**（`sync-page.tsx`）：`匿名 / 凭据 / 内联`
  （「内联」仅编辑历史任务且该任务原本带内联账密时出现）。
  选「凭据」时下拉列出 `GET /api/credentials` 的条目，label 形如 `名称（用户名@仓库地址）`，支持搜索。
- **`remoteCredentialId` 贯通**（`types.ts` / `api.ts` / `sync_handlers.go` / `engine.go`）：
  任务可引用凭据库条目，执行时经 Vault 实时解析 —— **之后在凭据管理里改密码，同步任务自动跟进**。

### 变更

- **`POST /api/sync/{id}/run` 语义**：由「阻塞执行并返回终态 run」→「受理并返回 running run」，
  HTTP 状态码 **`200` → `202`**。响应体仍是完整 run 对象，此时 `status` 为 `running`。
- **并发保护由「静默等待」改为「显式冲突」**：任务已在跑时 `TryLock` 失败 → **409 CONFLICT**
  （`sync: task is already running`）。原先是阻塞等锁，于是「第二次点击」在服务端排队而不是被拒。
- **`ErrTaskDisabled` / `ErrInvalidDirection` → 400**；未知错误仍是 500。
- **「运行」按钮按 DB 状态置灰**：`task.lastRunStatus === 'running'` 时 disabled + Tooltip 提示；
  列表中存在 running 任务时前端每 **3 秒**轮询一次（没有 running 就不建定时器，不产生空转请求）。
- **凭据解析失败按 run-level failure 记**：任务引用的凭据被删 / Vault 不可用时，
  run 落 `failed` 并写明原因（Start 依旧 202 —— **受理成功 ≠ 执行成功**）。

### 修复

- **SYNC-1 僵尸 `running` 行**：三重保险 —— ① 启动 sweep ② 执行用 detached ctx
  ③ `iterate` 内 `defer recover()`（panic 记 `internal panic: ...` 并落 failed）。
- **SYNC-2 日志噪音**：`errors.Is(err, context.Canceled)` 单独成一支，
  整轮只打一条 `sync: pull aborted`（Info 级），不再逐仓库刷 WARN。
- **`TestConnection` 错误语义收口**：vault 未配置却传了 id → **503**；
  id 查不到 → **400**（包 `sync.ErrCredentialNotFound`）；其他 vault 错误 → **500**；
  **探测结果本身永远 200**。
- **前端 `formError` 兜底**：以前异常时返回 `undefined`，会直接抛
  `Cannot read properties of undefined (...)`（0.6.6 同款问题）；
  现在恒返回 `boolean`，异常路径统一 `message.error('保存失败：…')`。
- **保存前只做结构校验**：`Validate()` 只管三态冲突（同时给 inline 与 credential → 拒；
  只给一半 inline 账密 → 拒），**不做**凭据存在性校验 —— 避免「凭据还没建好就存不了任务」。

### 兼容性

- **DB v7 是纯 ADD COLUMN**：老库升级后 `remote_credential_id` 为 `NULL`，
  内联账密的任务**行为完全不变**（照旧用 `remote_username` / `remote_password`）。
- **202 属破坏性变更**：任何按「run 返回即完成」写的调用方（含旧 curl 脚本）
  需改成「拿 `run.id` 后查 `GET /api/sync/runs`」。前端已同步改。
- 只支持 **cairn ↔ cairn**：对端 cairn 走 Basic 认证（`requireBasicAuth`），没有 bearer / OIDC。
- 多架构 manifest list 依旧显式拒绝（0.6.0 起）。

---

## [0.6.7] - 2026-09-30

本轮主题:**hotfix —— 修复 docker-compose 端口映射非默认时，「右键 UI」与「复制 docker pull」漏掉端口导致镜像拉不到**

UAT 反馈 (`client.local`)：docker-compose 把容器内 `8787` 映射到宿主机 `10001`
(`HOST_PORT=10001`)，同时设置页「仓库地址」填 `client.local`（裸 host，无端口）。
两个下游表现都漏掉端口：

- 顶部 badge：`大运维内部镜像库(27.58) (http://client.local)` —— 应是 `(http://client.local:10001)`
- 「复制 docker pull」：`docker pull client.local/bklite/cloud-ide:latest` —— 应是 `docker pull client.local:10001/...`

`docker pull client.local/...` 默认走 80/443；docker daemon 命中 10001 上的 registry
必须显式带端口 —— 操作员复制出去拉不到镜像，只能再手动改一次。

### 根因

`internal/api/handlers.go` `GetConfig` 构造 `displayURL` 时只做 `"http://" + RegistryURL()`。
而 `UpdateConfig` 的 `HOST_PORT_PATTERN` 只接受裸 host（拒绝端口、拒绝协议前缀），
所以保存后永远是 `<host>` —— 容器内 8787 ↔ 宿主机非标准端口映射时，badge + `docker pull` 都漏端口。

### 修复

- **`internal/api/handlers.go` `GetConfig`**：
  当 saved `registry.url` **没有端口**且 `HostPort != Port`（即存在宿主机端口映射）时，
  在 `displayURL` 上 append `:HostPort`。三条路径都已覆盖：
  - bare host + 有映射 → `:HostPort`
  - bare host + 无映射（`HostPort == Port`，例如直接跑容器无端口映射） → 不 append
  - legacy 值带端口（`legacy.example.com:8080`，迁移期残留） → 不被 HostPort 覆盖
- **`hasExplicitPort` helper**：能识别 `:digits` 端口后缀，同时区分协议前缀 `://`
  （防「v0.0 旧值带 `http://` 时 append 出一个 `http://host:hostPort:hostPort`」）。
- **回归测试** `internal/api/api_test.go` 新增 3 个 case（端口映射 / 无映射 / 显式端口），
  全部 assert `config.url` + `config.host` 的 host:port 形式。

### 未变更

- 前端 `web/src/utils.ts` `buildPullCommand` / `web/src/components/image-detail-drawer.tsx`
  都不需要改 —— 它们接 `config.host` 直接拼，**修后端即生效**。
- `web/src/pages/settings-page.tsx` 只读视图（`http://<savedUrl>`）保留设计 —— 端口
  在「监听端口」字段单独显示，操作员能看到完整信息。

---

## [0.6.6] - 2026-09-30

本轮主题:**hotfix —— 修复「保存同步任务」按钮在匿名模式下抛 `Cannot read properties of undefined (reading 'trim')` 一直转圈**

UAT 反馈 (`client.local`)：

1. **新建同步任务,留空「远端用户名」「远端密码」(匿名对端场景),点保存** →
   控制台 `Uncaught (in promise) TypeError: Cannot read properties of undefined (reading 'trim')`，
   保存按钮一直转圈不退出。
2. 已 mv 旧 `cairn.db` 重启 (测试环境的 0.6.1/0.6.2 hotfix 残留 schema) —— **不是本轮回归**,
   0.6.5 已经在 DROP+CREATE 后正常建表,生产 DB 没有问题。

### 修复

- **`web/src/pages/sync-page.tsx` `submit` 函数**:
  - **null-safe trim**:`name` / `remoteUrl` / `remoteUsername` 三个字段都从 `values.X.trim()`
    改成 `(values.X ?? '').trim()`。`remoteUsername` 是匿名模式的核心场景 (0.6.4 起
    username + password 都空 = 匿名),但 `openCreate` 没通过 `setFieldsValue` 初始化该字段,
    antd Form 的 `getFieldsValue()` 对未触摸的 Form.Item 返回 `undefined` —— 一调 `.trim()`
    就崩。`name` / `remoteUrl` 理论上 required 校验保证非空,但写防御代码没坏处。
  - **`try/finally` 包住 `setSubmitting`**:
    原写法 `setSubmitting(true)` 在 try 块外,一旦 `.trim()` 抛 TypeError,内层
    `finally { setSubmitting(false) }` 永远不执行,按钮一直转圈。改成把
    `setSubmitting(true)` + `input` 构造 + if/else 全包进同一个 `try/finally`。
- **`web/src/pages/sync-page.tsx` 「测试连接」按钮 `onClick`**:
  - 同样问题:`setTesting(true)` 后 `await testSyncConnection(...)` 如果抛同步异常
    (目前 wrapper 应该 catch,但兜底),`setTesting(false)` 不跑 —— 「测试连接」按钮
    一直转。加 `try/finally` 保险。

### 未变更

- Go 后端 / DB schema / 0.6.5 引入的探测功能全部保持不变,纯前端 trim + try/finally 改写。
- User-Agent 串保持 `cairn-sync/0.6.5` + `cairn-sync-probe/0.6.5` 不变 —— 仅前端代码改,
    outbound HTTP header 也顺手升到 0.6.6 (跟 binary 版本对齐),让上游 registry 日志
    能精确到 commit。

---

## [0.6.5] - 2026-09-30

本轮主题:**「测试连接」按钮 —— 新建/编辑同步任务表单可在保存前验证远端 cairn URL + Basic 凭据**

0.6.4 把 cairn↔cairn 同步做通了,但操作员填完任务 → 保存 → 点「立即运行」→ 看到第一行 sync_run 失败
才知道 URL 写错 / 凭据错 / 远端根本没起来。反馈环太长(尤其是第一次配对端,凭据都是手工抄的)。

本轮在「新建/编辑同步任务」Modal 加一个 **「测试连接」** 按钮(footer 左侧,跟「取消」「保存」并列)——
填好 URL(用户名密码可空,代表匿名测试)后点一下,后端用 5s timeout 对 `{remoteUrl}/v2/` 发一次
带/不带 Basic auth 的 GET,根据 HTTP 状态 + `WWW-Authenticate` 头分到 4 档之一,
UI 用一个 antd `<Alert>` 在 Modal 顶部渲染结果。结果是 JSON body 里的 `authStatus` 字段,**永远 HTTP 200**
(即使是「远端不可达」「凭据错」也算探测结果;只有 handler 层错误才非 2xx)。

> **设计抉择**:
> - 永远 200 + body 分类:符合「探测就是探测,语义在 body 不在 status」——前端只需要判断 `result.success` 决定要不要显示。
> - 不预拉 `_catalog`:避免把整个仓库列表拉一遍(那才是「立即运行」的活);`/v2/` 端点足够验证连通 + auth posture。
> - 不写 DB / 不 Validate:`Validate` 强制「用户名密码都空 = 匿名、都填 = Basic、混合 = 错」;测试期允许「填一半」(常见于
>   「我先填 URL 测一下,再补凭据」的工作流),但 `Writer` / `Engine` 的执行期判断仍走严格 Validate。
> - 清掉上次探测结果:打开新建/编辑 Modal 时 `setProbe(null)`,避免「A 任务的探测结果留在 B 任务 Modal 上」的脏读。

### 新增

- **`internal/sync/probe.go`**(`ProbeResult` + `ProbeConnection`):
  - `ProbeResult{Reachable, AuthStatus, HTTPStatus, Message}` 四字段;`Reachable=false` 时
    `AuthStatus`/`HTTPStatus` 不可靠(网络层失败)。
  - `ProbeConnection(ctx, baseURL, username, password)`:5s 总 timeout(connect + TLS + request + read),
    `GET {baseURL}/v2/`;`username` 或 `password` 任一非空就发 `Authorization: Basic ...` 头
    (允许混搭 —— 操作员中途在打字)。
  - 分类:
    - 200 + 发了 header → `ok`;200 + 没发 header → `no_auth_required`。
    - 401 + `WWW-Authenticate: Basic ...` + 没发 header → `required_but_missing`;401 + 发了 header → `wrong_creds`。
    - 404 → `not_registry`(这个 URL 不是 OCI registry)。
    - 401 但 WWW-Auth 不是 Basic / 5xx → `unknown`(兜底)。
- **`POST /api/sync/test`**(`internal/api/sync_handlers.go` `TestConnection`):
  - 不依赖 task ID,接受 `SyncTestInput{RemoteURL, RemoteUsername, RemotePassword}`;只读 URL 必填,
    其他可空。永远返 HTTP 200 + `ProbeResult`。body 限 8 KiB(防滥用)。
  - 路由在 `/sync/test`(同 `/sync/{id}/...` 同一组 chi router)。
- **Web UI**(`sync-page.tsx`):
  - Modal footer 改三按钮:「测试连接」(loading 用 `testing` state)+ 「取消」+ 「保存」。
  - Modal 顶部(在 Form 上面)渲染 `<Alert>`,根据 `authStatus` 分级颜色 + 一句话描述:
    `ok`/`no_auth_required` 绿色、`wrong_creds`/`required_but_missing` 红色、`not_registry` 橙色、
    其他灰色;Alert 的 `description` 字段给一句「可达,HTTP N」或「远端不可达」。
  - 「测试连接」按钮直接读 `form.getFieldsValue()`(不依赖 `Validate`),允许「填一半」组合;
    改完表单再点覆盖 probe 结果。
- **类型与 API 客户端**:`web/src/types.ts` 加 `SyncProbeAuthStatus`(`ok`|`no_auth_required`|
  `required_but_missing`|`wrong_creds`|`not_registry`|`unknown`)+ `SyncProbeResult` + `SyncTestInput`;
  `web/src/api.ts` 加 `testSyncConnection(input)`。
- **User-Agent 微调**:`internal/sync/writer.go` outbound header 从 `cairn-sync/0.6.4` 升到
  `cairn-sync/0.6.5`;`internal/sync/probe.go` 新增独立 header `cairn-sync-probe/0.6.5`,
  让远端 registry 日志能区分「探测流量」与「真实同步流量」。

---

## [0.6.4] - 2026-09-30

本轮主题:**内置 regsync 等价能力 —— cairn↔cairn 镜像同步,UI「镜像同步」Tab + 手动运行 + Basic 鉴权（含匿名对端）**

> 之前 `docker pull` / `skopeo copy` / regsync(regclient)是与本节点独立部署的
> 客户端。本轮把「从 / 向另一 cairn 同步一批仓库」这条路径收进 cairn 自身,
> 操作员在面板上配规则后点「立即运行」即可 —— 不再依赖外部 regsync 进程。

> **hotfix 历史**:
> - 0.6.0 首发版用「Bearer Token」鉴权,错——cairn 自己的 /v2/* 只支持
>   Basic(username/password),没有 bearer token 来源。
> - 0.6.1 改成 Basic + Bearer 改 username/password,但 schema 迁移用
>   `ALTER TABLE ... RENAME COLUMN` 在 modernc.org/sqlite v1.59.0 上
>   报「no such column: remote_token」——整 cairn 启动失败。
> - 0.6.2 改 `ADD COLUMN` 但对残缺 sync_tasks 表无能为力——
>   UAT 仍报「no such column: remote_url」。
> - 0.6.3 改 `DROP TABLE + CREATE TABLE` 重建,功能层终于通了。
> - 0.6.4 (本轮) 放宽 auth:支持**匿名对端**——两边 username + password
>   都留空 = 不发 Authorization header,cairn 没配 auth 的中间件
>   直接放行。UAT 真实场景:对端 0.5.53 没配 Registry 认证,本节点
>   直接匿名拉。

### 新增

- **DB schema v5**(两表 + CASCADE):
  - `sync_tasks`(配置) —— 名称 / 方向 / 远端 URL / 远端用户名 / 远端密码 /
    include / 启用 / created_at / updated_at;`name` UNIQUE。
  - `sync_runs`(历史) —— task_id FK CASCADE / started_at / finished_at /
    status / repos_total / 三个 synced/failed 计数 / error。
  - `internal/db/sync.go` 提供 8 个 CRUD 方法,`internal/db/db.go` 的 `SCHEMA_VERSION`
    从 4 升到 5。旧 v4 实例启动时自动迁移,无需手动干预。
- **`internal/sync` 包**(cairn↔cairn 同步核心,~1300 行):
  - `types.go` —— `Direction`(`pull`|`push`)、`SyncRunStatus`(`running`/`success`/
    `partial`/`failed`)、`SyncTask` / `SyncRun` 域类型 + `Validate()`,
    密码字段用 `json:"-"` 屏蔽(列表 / 详情 API 永远不返明文)。
  - `store.go` —— domain 类型 ↔ DB row 类型适配;`ErrTaskNameConflict` /
    `ErrTaskNotFound` / `ErrRunNotFound` 等 sentinel errors,handlers
    `errors.Is` 一次分流 400 / 404 / 409 / 500。
  - `filter.go` —— `include` 行分隔 glob 解析(`*` 通配,空 = 全匹配),
    复用 `path/filepath.Match`,无 exclude(MVP 限制)。
  - `writer.go` —— 推送方向 HTTP 客户端:`EnsureBlob` 走 OCI spec 单块
    `POST → PATCH → PUT` 三步法(HEAD 跳过已存在 blob),`PutManifest` 单步
    PUT 返 digest。所有请求由构造时定下的 bearer token 盖章,无 401 challenge
    流程(节省一次 round-trip)。
  - `engine.go` —— pull + push 双执行路径:`Engine.Run(ctx, task)` 同步返
    terminal `SyncRun`;per-task `sync.Mutex` 保证同 task 不并发;`per-repo
    continue-on-error`(一个仓库失败不中断整轮);仅抓 pre-ignited bearer —— 包
    `bearerTransport` 套在 `registry.Client.HTTP().Transport` 外面,
    跳过上游 `Client` 的 401→token-fetch 流程。多架构 manifest list / image index
    在 0.6.0 显式拒绝(后续 0.6.x 加)。
- **`internal/api/sync_handlers.go` + 路由**:
  - `GET /api/sync` 列表、`POST /api/sync` 新建、`GET /api/sync/{id}` 详情、
    `PATCH /api/sync/{id}` 更新、`DELETE /api/sync/{id}` 删除(CASCADE 删 runs)、
    `POST /api/sync/{id}/run` 同步运行、`GET /api/sync/{id}/runs` 历史(默认 50 条)。
  - 更新时 `remoteToken` 空 = 保留旧值(UI 编辑其他字段时不必重输 secret,
    UI 也根本没机会拿到明文)。
  - run 终态映射:`success` / `partial` → 200 OK,`failed` → 502 BAD_GATEWAY
    (上游问题,不是我们)。
  - `internal/server/server.go` 在 `store_db != nil` 时构造 sync store + engine +
    `api.SyncHandlers`,挂到 `extras.SyncHandlers`;DB 启动失败时整组不挂,
    UI 「镜像同步」Tab 不会出现。
- **UI「镜像同步」页**(`web/src/pages/sync-page.tsx` + App.tsx 注册):
  - 顶栏插入「镜像同步」Tab(在「镜像热度」↔「凭据管理」之间);`PageKey`
    union + `PAGE_KEYS` + `publishHandlers.sync` 同步扩展。
  - 列表表 + 新建 / 编辑 Modal(name / direction Radio / 远端 URL / Bearer token
    `Input.Password` / Include TextArea / 启用 Switch)+ 立即运行按钮 +
    历史 Modal(按 task 懒拉取,不主表预取避免一屏打满请求)。
  - 状态徽标:`success` 绿 / `partial` 金 / `failed` 红 / `running` 蓝。
  - 侧栏按 direction 分组(全部 / 拉 / 推)切视图。
  - 「立即运行」按钮点击后 spinner → 同步返结果 → message 提示
    「同步成功 / 部分失败 / 同步失败」(分别 success / warning / error)。

### 变更（0.6.4）

- **支持匿名对端**:`SyncTask.RemoteUsername` + `RemotePassword` 两字段都空
  时,引擎不发 `Authorization` header;cairn 没配 Registry 认证的中间件
  (`requireBasicAuth` 在 `wantUser == ""` 时 fall through)直接放行。
  - **`internal/sync/types.go`**:`Validate()` 由「两者必填」放宽为「两者都空
    或都不空」,混合 (一个空一个不空) 视为 UI 拼写错误,返
    `ErrCredentialIncomplete` 让前端能定位到字段。
  - **`internal/sync/writer.go`** / **`internal/sync/engine.go`**:
    实现里早就是 `if username != "" || password != ""` 才设 Authorization,
    所以代码层无需改;只是现在 Validate 不再把它们堵死。
  - **UI**(`web/src/pages/sync-page.tsx`):「远端用户名」「远端密码」两
    Form.Item 的 `rules: [{ required: true, ...}]` 去掉;extra 文案补充
    匿名模式说明（两个都留空 = 匿名）。PLACEHOLDER 改成「admin（匿名
    对端留空）」让操作员一眼看出口径。

### 修复

- **DB schema 6 改 DROP+CREATE 重建表**(本轮 hotfix;0.6.2 的 ADD COLUMN
  对残缺表无能为力):
  - **根因**:0.6.1 用 `RENAME COLUMN` 在 modernc.org/sqlite v1.59.0 挂;
    0.6.2 改 `ADD COLUMN` 又被另一类用户的「残缺 sync_tasks」打中——
    这些 DB 里 sync_tasks 是早期(0.6.0 之前)尝试残留的,**缺 v5 应有的列
    (如 `remote_url`)**。`v5` 的 `CREATE TABLE IF NOT EXISTS` 不会修
    补已存在的表;`v6` 的 `ADD COLUMN` 只加新列,也不修补旧缺。
    结果:`db/sync.go` 的 `SELECT ... remote_url ...` 报
    `no such column: remote_url`。
  - **改法**:`PRAGMA foreign_keys = OFF` → `DROP TABLE IF EXISTS sync_tasks`
    + `DROP TABLE IF EXISTS sync_runs`(顺序无关,CASCADE 关掉了)→
    `CREATE TABLE sync_tasks (...)` 含完整 v6 schema → `CREATE TABLE
    sync_runs (...)` + index → `PRAGMA foreign_keys = ON`。无论原表
    是完整、残缺、还是不存在,DROP+CREATE 都能落到一致状态。
  - **数据丢失**:任何在 0.6.0/0.6.1/0.6.2 期间写入的 sync_tasks /
    sync_runs 行都丢了。**这三轮 hotfix 都没真正工作过**(bearer auth
    错 / migration 错 / 残缺 schema),所以没有可保留的运行历史。
  - **DB 现状**:跑完 0.6.3 migration 6 后,sync_tasks / sync_runs 是
    干净的 v6 完整 schema,空表。可正常「新建同步任务」+「Run」。
- **DB schema 6 改用 ADD COLUMN 替代 RENAME COLUMN**(0.6.2 引入;已被 0.6.3
  替代):
  - 0.6.1 的 `ALTER TABLE ... RENAME COLUMN` 在 modernc.org/sqlite v1.59.0
    parser 阶段报 `no such column`。0.6.2 改 `ADD COLUMN`,但只解决完整表
    的问题,残缺表仍然 missing columns(见上一条)。
- **同步 auth 从 bearer 改 Basic**(0.6.1 引入;首发版 0.6.0 写的 Bearer Token
  是错的):
  - **根因**:cairn 自己的 `/v2/*` 只接受 Basic auth(`cfg.RegistryUsername` /
    `RegistryPassword` → `requireBasicAuth` middleware),**没有 bearer token
    这一说**。0.6.0 首发版让 UI 接收「Bearer Token」字段 → 写到
    `sync_tasks.remote_token` → 引擎发 `Authorization: Bearer ...` →
    对端 cairn middleware 只认 `Basic ` 前缀 → 401,功能压根不可用。
  - **改法**:sync 任务改存 `RemoteUsername` + `RemotePassword` 两个字段(后者
    `json:"-"` 屏蔽)。`internal/sync/writer.go` 把 `Authorization: Bearer <t>`
    换成 `Authorization: Basic base64(u:p)`;`internal/sync/engine.go` 删掉
    `bearerTransport` 包装层(回归直接走 `registry.Config{Username, Password}`
    —— 上游 `internal/registry.Client` 原生支持)。
  - **DB schema v5→v6**:本轮 hotfix;具体见上面 hotfix 一节。
  - **UI**:`SyncTaskInput.remoteToken` → `remoteUsername` + `remotePassword`;
    「镜像同步」Modal 的「Bearer Token」字段拆成「远端用户名」+「远端密码」
    两个;密码 `Input.Password`,编辑时留空 = 保留旧值(语义同 0.6.0 的
    token 字段)。
- **`/v2/_catalog` 分页死循环**(0.6.0 引入;sync 强依赖):
  - 客户端 `internal/registry/inventory.go` `ListRepositories` 之前只看
    `len(batch) < pageSize` 一个停止条件,遇到「`?last=` 是包含游标的服务端」
    无限循环。改为 `seen` map 去重 + 三条停止条件(短页 / `newCount==0` /
    `maxCatalogPages=5000` 安全上限)。
  - 服务端 `internal/registryd/routes.go` `catalog` 之前**完全忽略**
    `?n=&last=`,无脑返全量列表。改为 OCI Distribution Spec §_catalog
    标准实现:`sort.Strings` 防御性排序 → `?last=` 排他游标(`sort.SearchStrings`,
    O(log n))→ `?n=` 截断(缺省 / 非数字 / `<=0` = 全量,向后兼容)→
    仅截断时发绝对 URL `Link: <next>; rel="next"` → `repos==nil` 置
    `[]string{}`(避免 JSON `repositories: null`)。
  - 双向都修,cairn↔cairn 同步两端都要走 `_catalog`,不修 sync 必死循环。

---

## [0.5.53] - 2026-09-30

本轮主题:**浏览器标签页 `<title>` 同步设置页的「展示名称」,多 tab 一眼分清**

### 新增

- **`document.title` 同步展示名称**(`web/src/App.tsx`):
  顶部 brand 是「Cairn」(产品名),操作员面对的是「内网离线镜像源 / 测试环境 /
  ...」这类具体仓库 —— 多 tab 时只靠 favicon 区分。`App.tsx` 加一个
  `useEffect`,监听 `config.name`:
  - 有展示名称 → `<name> · Cairn`
  - 未配置 / 空串 → `Cairn`(回退到品牌名,**不**显示「镜像仓库」占位串)
  effect 跑在 `config` 加载完之后,所以配置错误重试也不会闪旧名。

### 变更

- **设置页「展示名称」字段 extra**(`web/src/pages/settings-page.tsx`):
  由 `顶部 / 设置页显示名` 改成 `顶部 / 设置页显示名,也是浏览器标签页 title`。
  把「这是哪里会用」一句话说清,免得操作员改了名字只看到顶部变了、没意识到
  标签页也变了。

---

## [0.5.52] - 2026-09-30

本轮主题:**「见过的客户端」面板落 SQLite,重启不丢 —— 用于发现有没有非法的在打**

### 新增

- **`event_seen` 表**(`internal/db/db.go` schema v4):
  ```sql
  CREATE TABLE event_seen (
      useragent     TEXT PRIMARY KEY,
      first_seen_at TEXT NOT NULL,
      last_seen_at  TEXT NOT NULL,
      events        INTEGER NOT NULL DEFAULT 0,
      counted       INTEGER NOT NULL DEFAULT 0
  );
  CREATE INDEX event_seen_last_seen ON event_seen(last_seen_at DESC);
  ```
  每个 User-Agent 一行,首/末次时间 + 事件计数 + 计入热度计数。索引按 `last_seen_at DESC` 排,让"最近活跃"的查询走索引。

- **`db.LoadAllEventSeen` / `db.BatchUpsertEventSeen`**:启动加载 + 异步批量 UPSERT。
  `events/counted` 用 `excluded.events / excluded.counted` 直接覆盖(不是累加)—— 避免"flush 窗口双计"陷阱(详见 `internal/db/db.go` 注释)。

- **`events.Handler.RunFlushLoop`**:后台 goroutine 每 5s 刷一次 dirty 的 per-UA 聚合到 SQLite。Shutdown 时 `r.PullCancel` 触发 `flushSeen(context.Background())` 最后一次兜底。宕机最多丢 5s 内的增量,可接受。

- **`/api/stats/clients?days=all`(默认) / `?days=N`**:新增 `all` 模式不 filter,返回 event_seen 全表。`N` 仍是数字,行为跟 0.5.51 一样按 `LastSeenAt` 过滤。`days` 字段在响应里是 `number | "all"`。

- **「见过的客户端」UI 加时间窗 Select**:默认 "全部时间",提供 "近 7/30/90 天" 切换看趋势,跟时间窗对齐。

### 变更

- **`events.recordClient` 不再收 self / ignore UA**(`internal/events/events.go`):
  `processOne` 在调用 `recordClient` 之前先 gate `dec.Ignored || self`。结果:操作员面板里的"见过的客户端"再也不会被自家请求或被忽略规则的 UA 污染。

- **`db.PurgeAll` 签名**:从 `(int64, error)` 改为 `(activity, seen int64, err error)`,分别报告两条删除数。`StatsHeatDelete` UI 响应 `{activity, seen}` 不再硬编码 `seen: 0`。

- **`events.Handler.ClearSeen`**:`PurgeAll` 后清内存 `h.clients`,保证 UI 不残留 stale 行。

- **`retention` 与 activity_daily 共用 cutoff**(`internal/server/server.go` `retentionLoop`):
  `db.RetentionCleanup` 现在同时清 `activity_daily WHERE day < ?` 和 `event_seen WHERE last_seen_at < ?`,共用 `cfg.StatsRetentionDays()`(默认 365)。注释同步。

### 修复

- **`stats-page.tsx:815` 误导文案"想看更早的请用上面的「见过的客户端」"** —— 之前 0.5.51 写这句话时根本没落盘,实际重启后两块都空。本轮 0.5.52 落盘之后,这句话才真正成立。

### 测试(`internal/events/local_test.go`)

- `TestNewHandler_LoadsPersistedSeen` —— 预先 INSERT 一行,`NewHandler` 后 `SnapshotClients()` 看得到(重启不丢的 load-bearing 测试)。
- `TestFlushSeen_Persists` —— 多次 `IngestLocal` → 同步 `flushSeen` → 重新 `NewHandler` → 数据还在(端到端持久化)。
- `TestIgnoreUA_NotPersisted` —— kubelet 触发 ignore 规则 → flush → event_seen 没这行。
- `TestSelfUA_NotPersisted` —— `cairn/0.5.52` 自家 UA → flush → event_seen 没这行。

### 影响范围(升级须知)

- **数据迁移**:升级后首次启动,db 走 schema v4 migration 自动建 `event_seen` 表 + 索引。`activity_daily` / `settings` / `pull_jobs` / `stats_ignore` 老数据不动。
- **API**:`/api/stats/clients` 接受 `days=all`(新默认)或 `days=N`(老用法)。`/api/stats/heat` DELETE 响应 `seen` 字段从 0 变成实际删除行数。
- **设置**:无新 UI 开关,retention 沿用 `stats.retention.days`(默认 365 天)。
- **行为**:操作员重启 cairn 后,「见过的客户端」面板继续显示重启前的 UA,不再因内存清零而丢失。

---

## [0.5.51] - 2026-09-30

本轮主题:**产品介绍页同 tab 打开 + 上一轮改名收敛补 CHANGELOG + 公开仓库脚手架治理**

### 变更

- **产品介绍页同 tab 打开**(`web/src/components/page-sidebar.tsx`):sidebar footer 的「产品介绍」链接去掉 `target="_blank" rel="noopener"`,浏览器默认 `_self` 在同 tab 打开。旧行为(0.5.37.6 加的)是为了避免「跳页丢筛选状态」,但代价是用户被强行推到新 tab —— 现在用户点 cairn-intro footer 的「管理控制台 →」按钮 / 浏览器后退就能回来,体验更顺。

- **cairn-intro.html 占位符同步**(`web/public/cairn-intro.html`):行 429 (hero 版本徽章) / 638 (footer 版本号) 占位符 `0.5.37` → `0.5.51`。这是 AGENTS.md「5 处版本号同步」漏的一处。

### 补漏:上一轮改名收敛的 CHANGELOG

0.5.50 上一轮 3 个 commit(`959e963` 抹内部引用 + 删 `tests/web-auto/` / `49110ec` rename internal / `0331079` unify module path + binary 名 + brand)完成了 module path + 63 处 Go import + 二进制名 + Dockerfile HEALTHCHECK + Makefile + web/package.json + 代码注释 + cairn-intro.html 全部 `go-hub` → `cairn` 的收敛,但 CHANGELOG.md 漏写一节。本轮补上,版本号才自洽。

### 公开仓库脚手架(并入本轮)

- **`LICENSE`**(Apache-2.0)+ **`NOTICE`** 落地
- **基础件**:`CODE_OF_CONDUCT.md`(Contributor Covenant 2.1)+ `CONTRIBUTING.md`(PR 流程 + `make gates` + Conventional Commits + 5 处版本号同步)+ `.github/pull_request_template.md`(8 项自查)+ `.github/ISSUE_TEMPLATE/{bug_report,feature_request}.md`
- **`.github/workflows/ci.yml`**:push / PR 自动跑 tsc + go build + go build -tags webui + go test -race
- **BuildKit 缓存加速**:`Dockerfile` 的 `pnpm install` 和 `go mod download` 加 `--mount=type=cache`;`Makefile` 加 `DOCKER_BUILDKIT=1` 显式开关,`rebuild` 去 `--no-cache`(默认命中就命中),新增 `rebuild-fresh` 兜底"绝对全清"
- **删 `#syntax=docker/dockerfile:1.4`**(这行让 BuildKit 去 docker.io 拉 frontend 镜像,公司受限网络 timeout)
- **`docs/ROADMAP.md`** 重构:删 hotfix 罗列,只保留"近期里程碑" + "既定计划" + 0.5.19 状态 + 0.6.0 设计 + 刻意不做;**`docs/resilience.md`** 新增(0.5.18 韧性轮 B/F/L 问题清单)

### 影响范围(升级须知)

- **行为变化**:点 sidebar 「产品介绍」现在在同一 tab 打开(不再新开 tab)。cairn-intro.html 的 GitHub CTA / footer 链接保留 `target="_blank"`(去外部站点,同 tab 跳走会丢控制台状态)。
- **不变**:cairn 二进制协议、API 路径、SQLite 库结构、registry 存储、凭据/热度库格式。
- **数据兼容**:无存储改动。
- **5 处版本号同步**:`internal/version/version.go` / `docker-compose.yml` / `.env.example` / `README.md` / `web/public/cairn-intro.html` 全部 0.5.50 → 0.5.51。
- **补漏一处 — `Makefile` `IMAGE ?=` 默认值 + AGENTS.md 清单 5 → 6 处**:`make rebuild` / `make build` / `make rebuild-fresh` 三个目标都 `-t $(IMAGE)`,默认值不跟版本号改就 build 出 `cairn:0.5.50`,跟 `docker-compose.yml` 默认 `cairn:0.5.51` 打架,docker compose up 会找不到 image。AGENTS.md「5 处版本号同步」清单补上第 5 条,以后必改项写齐。
- **cairn-intro.html 数据平面鉴权 callout 同步实际状态**:旧 callout 说"/v2/* 目前匿名可访问,鉴权仍在 TODO 列表里"是错的 —— 入站 Basic auth 鉴权 0.5.3 加(commit `67b582f`),0.5.33 修过 docker daemon 鉴权流程;`server.go:211` 把 `getCreds` 接进 `registryd.New`,settings-page「Registry 认证」配 username/password 就实时启用,无需重启。改成:/v2/* 默认匿名;设置页开 Basic auth 后客户端需 `docker login` 才能 push/pull;生产仍建议内网 + 反代多层稳。

---

## [0.5.50] - 2026-09-29

本轮主题:**修 regsync / 严格 OCI 客户端同步时撞 +1 字节 size mismatch** —— UAT 复现:从 proxy.example.com:10001 同步 `bitnami/redis-cluster:8.2.1-debian-12-r0` 到 cairn(registry.example.com),regsync 报 `blob content size does not match descriptor, expected 7266, received 7267`,整图失败。skopeo copy 同样源/目**不撞**(0.5.46 部署后已验证)—— 同一个 cairn、同一个镜像,行为差异指向协议实现细节而非数据问题。

### 根因

RFC 7233 byte range 是 **inclusive 双端**:`bytes 0-N` 表示 N+1 字节。cairn 三处响应头把磁盘 size 当 end 写,等于永远比真实已写字节数多 1:

```go
// 旧(错):
w.Header().Set("Range", fmt.Sprintf("0-%d", size))   // 上传 size 字节却报 0-size=size+1 字节
w.Header().Set("Range", "0-0")                         // 空文件却报 1 字节
```

修复后统一走新 helper `ociRangeHeader(size)`:

| 文件 size | 旧(错) | 新(对) |
|---|---|---|
| 0(空上传) | `0-0`(报 1 byte) | `0--1`(RFC 7233 suffix range 零长) |
| N > 0 | `0-N`(报 N+1 byte) | `0-(N-1)`(报 N byte) |

### 为什么 skopeo 不撞

| 客户端 | 「已上传字节数」从哪取 |
|---|---|
| **skopeo / docker daemon**(containers-image) | **自己 PATCH 请求的 body length**,不读 server 的 Range 头 |
| **regsync**(regclient) | **server 返回的 Range 头**(把 `0-N` 直接当 lastBytePos+1 用) |

regsync 拿到 `Range: 0-7267` → 内部算"server 已写 7267 bytes" → 下一 chunk Content-Range start=7267 → 后续累积偏移全部 +1 → PUT finalize 时对照上游 manifest 7266 → +1 字节不匹配 → 报错。skopeo 从头到尾用自己 body length 算,与 server Range 无关,所以同一个 blob 同一条链路它从未暴露过。

任何严格读 Range 头的客户端(regclient / harbor 同步器 / crane 等)同步到 cairn 都会撞这个 bug —— 比 regsync 影响面更大,本轮顺手全清。

### 修复

- **`internal/registryd/routes.go` 三处 `Range` 响应头全部走 `ociRangeSize(size)`**:
  - `uploadStart`(POST) 空文件用 `0--1`
  - `uploadPatch`(PATCH) 用 `0-(size-1)`
  - `uploadGet`(GET 上传进度,204) 同上
- 同步更新 `internal/registryd/upload_test.go` 两条老期望(它们之前把 off-by-one 当正确行为,跟 Range 头一起被这一轮暴露)。

### 测试

**新增 `internal/registryd/blobsize_test.go`,5 个针对性字节级测试**:

1. `TestBlobSize_MonolithicPatch` —— 整体 PATCH 写 7266 字节(对齐 regsync 报错 size),on-disk + HEAD Content-Length 必须精确 7266
2. `TestBlobSize_MultiChunkNoRange` —— 同 UUID,3 chunk 累加,验证 on-disk + HEAD + 每次 PATCH Range 头(修前 fail,fix 后过)
3. `TestBlobSize_MultiChunkWithRange` —— 同 UUID,多 chunk 各自带 Content-Range(模拟 regclient),验证 O_APPEND + Seek 在「顺序递增 offset」场景下实际 OK(`Seek` 在 O_APPEND 模式下被 Linux 内核忽略,但 chunk 是按顺序 append 到末尾,结果碰巧正确;只有「覆盖写已存在数据」才会暴露,regsync 不走那条路径,所以这个潜在问题未触)
4. `TestBlobSize_RangeHeaderAtEachPatch` —— 每次 PATCH 响应 Range 头必须反映**累加** size,不是 stale 值
5. `TestBlobSize_PATCHReadsExactlyContentLength` —— 服务端只读 Content-Length 字节 body,不多读 1 字节 trailing 数据

### 影响范围(升级须知)

- **行为变化**:Range 头现在 RFC 7233 严格 inclusive end。regsync / 所有严格读 Range 头的客户端同步到 cairn 不再撞 +1。skopeo / docker push/pull 行为不变(它们不读 Range 头)。
- **不变**:blob 实际写入字节数、Content-Length 头、PATCH/PUT/GET 上传整体流程、HEAD blob 响应、manifest 上传。
- **数据兼容**:无存储改动;不涉及任何已落盘 blob。
- **bitnami / cloudpirates 状态**:用户记忆里 bitnami → cloudpirates 迁移中(zookeeper 已验证,redis 家族待迁),bitnami 镜像本来就有「已知坑」。本轮**与 bitnami 上游无关** —— 同一镜像同一链路 skopeo 能过就是反证。

---

## [0.5.49] - 2026-09-29

本轮主题:**修拉取页源镜像名校验「请填写镜像名」双弹提示** —— 用户截图:输入框空时红字提示叠了两条。根因是同一个 Form.Item 上两条规则撞了同一句话:`{ required: true, message: '请填写镜像名' }` 和自定义 validator 里的 `if (!ref) reject('请填写镜像名')`,空值时两条同时触发 → 两条「请填写镜像名」一起渲染。

### 修复

- **拉取页源镜像名校验 validator 加空值短路**(`web/src/pages/pull-page.tsx`):

  validator 开头加 `if (!value || !value.trim()) return Promise.resolve();`,空值交给上面的 `required` 独占负责,validator 只在有值时跑「缺 tag」分支。这跟本文件内「目标镜像名」「临时代理地址」等其它规则的写法保持一致(目标镜像名那条本来就是这么写的,源镜像名是漏网)。

### 变更(防御性同类隐患清理)

- **`proxies-page.tsx`「代理地址」validator 加空值短路**
- **`credentials-page.tsx`「Registry URL」validator 加空值短路**

  这两条当前**并不**双弹(message 跟 required 不一样),但同样是「required: true + 自定义 validator 不判空」模式 —— 哪天 message 被改成跟 required 同字,就会复发 0.5.49 修的同款 bug。统一加上空值短路,把这类隐患一次性清干净。

### 影响范围(升级须知)

- **行为变化**:拉取页源镜像名空值时只显示一条「请填写镜像名」,而不是两条叠在一起。
- **不变**:其它校验语义、提交逻辑、提交流程、API 路径均未触碰。
- **数据兼容**:纯前端校验修复,无后端 / SQLite / 存储改动。

---

## [0.5.48] - 2026-09-29

本轮主题:**知名拉取源预置 + 第三方拉取源可配置** —— 用户需求:「这些比较有名的已知的可以预先打到代码中,但是一些外部第三方的我也可能需要,所以我需要有一个入口可以进行修改的」。按 AGENTS.md 的规矩(v0.5.9 起 .env 只留 3 个基础设施变量,业务配置一律走 UI → SQLite settings,新增业务 env 要过「为什么不能走 UI」两道闸),入口落在**设置页**而不是 .env:该配置每次拉取任务热读、无 boot 期约束,UI 改完即生效,不用重建容器。

### 新增

- **`pull.known_hosts` 设置键**(`internal/config/config.go` + `internal/api/handlers.go` + 新文件 `internal/api/knownhosts.go`):
  - CSV,每条是一个第三方 registry 地址。保存时后端 `normalizeHostCSV` 归一化成 `<scheme>://host[:port]`:显式 scheme 原样生效;裸 host 按与前端 `inferProtocol` **同一套**规则猜协议(无端口 / 443 / 8443 / 5000 → https,其余端口 → http),保证存盘形式和解析器口径永不打架;host 小写化、按 host[:port] 去重(首条生效);路径 / query / 凭据 / 非 http(s) scheme 一律 400 拒绝;上限 64 条。
  - 空 = 只用内置知名源。`MutableFieldType` 新增 `hostcsv` 校验分支,`GET /api/config` 以 `mutable.pullKnownHosts` 暴露。
- **设置页「第三方拉取源」字段**(`web/src/pages/settings-page.tsx`):在「拉取平台白名单」同一锚点下;Select tags 模式,下拉候选直接给知名源预置清单,也可手输任意第三方/内网主机;draft / 取消还原 / diff / 一次性 PATCH 与 pullPlatforms 全套同机制。
- **拉取页镜像名智能解析认自配主机**(`web/src/utils.ts`):新增 `parseKnownHosts(csv)`(与后端归一化同口径的 host→baseUrl 映射);`parseImageReference(raw, userHosts?)` 首段主机**优先**查用户表。补上两个真缺口:①无点内网主机 —— `harbor/team/app:v1` 这种形状内置的点/端口启发式拒认,以前被整个当成 Docker Hub 仓库路径;②强制协议 —— 显式 `http://` 条目可压过内置猜测(甚至压过内置知名源本身,比如给 quay.io 指一个 http 代理)。
- **知名源预置清单 `PRESET_SOURCES`**(代码内置,即「预先打到代码中」的那部分):Docker Hub / quay.io / ghcr.io / mcr.microsoft.com / registry.k8s.io / public.ecr.aws / gcr.io / registry.access.redhat.com,带中文标注,供拉取页与设置页下拉共用。
- **测试**:`internal/api/knownhosts_test.go` 19 断言(归一化:裸 host / 端口猜测 / 显式 scheme / 小写 / 去重 / 尾斜杠;拒绝:路径 / 凭据 / ftp / 空 host / `://` 垃圾 / query / 64 条上限);前端冒烟(node `ts.transpileModule` 跑 utils.ts 真身,21 断言):parseKnownHosts 口径 + userHosts 优先 + 主机大小写不敏感 + Docker Hub 语义不受影响 + 无表时行为与 0.5.47 完全一致。

### 变更

- **拉取页「来源 registry 地址」输入框 → AutoComplete**(`web/src/pages/pull-page.tsx`):候选 = 内置知名源 + 设置页自配第三方源(去重),仍可自由输入任意 `http(s)://` 地址;三处 `parseImageReference` 调用(目标名自动跟随 / 入队解析 / 表单校验)全部传入用户宿主表。

### 修复

- **「来源 registry 地址」自诞生起就缺 `name="sourceUrl"` 绑定**:界面上能填、校验规则也在,但值永远进不了 form store —— 高级选项的「手动覆盖来源」**从未生效过**(`handleQueue` 里 `values.sourceUrl` 恒为 undefined,所有任务都走镜像名自动推断)。本轮补上绑定,该字段第一次真正可用。

### 影响范围(升级须知)

- **行为变化**:镜像名前缀命中 `pull.known_hosts` 主机时按配置解析源(此前无点主机被误当 Docker Hub 路径);拉取页手动填「来源 registry 地址」现在真的生效。
- **不变**:未配置 `pull.known_hosts` 时,内置知名源与点/端口启发式的识别行为与 0.5.47 完全一致;后端 pull executor 不动(sourceUrl / sourceRef 由前端拆好传入)。
- **数据兼容**:新键写 SQLite settings 表,老数据无迁移;无存储 / 凭据库层面改动。
- **已知遗留(下一轮候选)**:凭据库「测试」按钮(`TestCredential`)对 Bearer 型 registry 仍显示 401(0.5.47 已记录,未动)。

---

## [0.5.47] - 2026-09-29

本轮主题:**匿名 Probe 接受 401+Bearer challenge** —— 用户以 quay.io 为拉取源做预检时报 `registry: GET /v2/: 401 unauthorized (bearer token fetch failed: TOKEN_FETCH_FAILED: token endpoint returned 401 url=https://quay.io/v2/auth)`。Docker Hub 匿名一直能过,quay.io 匿名必挂。

### 修复

- **匿名 Probe 不再做 token 升级**(`internal/registry/inventory.go`):

  `GET /v2/` 有两种「活着」的标准应答(OCI Distribution Spec,containers/image 的 Ping 同样两者都算成功):

  | 应答 | 谁这样 |
  | --- | --- |
  | `200 OK` | 内网 registry、mcr.microsoft.com、registry.k8s.io(匿名即通) |
  | `401 + WWW-Authenticate: Bearer` | quay.io、ghcr.io、gcr.io、Docker Hub、public.ecr.aws(公网 Bearer 型对匿名 ping 的标准应答) |

  旧实现只认 200,401 全权交给 `doRequest` 的 401→token 升级链。升级链对 `/v2/` 这种 **ping challenge 不带 scope** 的请求(AGENTS.md §认证记录过的事实)有两个分岔:

  - Docker Hub:auth.docker.io 无 scope 也发匿名 token → 升级走通 → 重试 200 → Probe OK(**碰巧能用**)
  - quay.io:`/v2/auth` 对「无 scope + 无凭据」**直接 401**("Requires authentication") → `TOKEN_FETCH_FAILED` → 整个预检挂掉(**用户报的错**)

  新语义按凭据配置分两支:
  - **匿名**(未配 username):裸 ping,`200` 或 `401 + 可解析 Bearer challenge` = 成功;401 但无 challenge(比如反代配错)= 失败 `PROBE_FAILED`。真实资源能否匿名拉,由预检的下一步(取真实 repo:tag 的 manifest,v0.5.17 起就有)决定 —— 资源请求的 challenge **带 scope**,匿名 token 照常签发,不受本次改动影响。
  - **带凭据**:保持原有升级链(401 → basic 换 token → Bearer 重试 → 200)。token 换取失败 = 凭据不被接受,照旧报错 —— 凭据测试要的就是这个结论,**不能**因为「对面活着」就放行错凭据。

- **新增回归测试** `internal/registry/probe_test.go`:内置 quay.io 形态的 fake registry(ping 401 无 scope / token 端点匿名无 scope 拒绝、带 scope 放行 / manifest challenge 带 scope),5 个用例:
  1. 匿名 Probe 过(旧代码精确复现用户报错 `TOKEN_FETCH_FAILED`,已用 stash 复验测试有牙)
  2. 匿名 Probe → GetManifest 全链路(带 scope challenge → 匿名 token → 200)
  3. 带凭据 Probe:对的过、错的必须挂(凭据校验语义不回退)
  4. 200 直通(内网 / mcr 形态不回退)
  5. 401 无 challenge 必须挂(`PROBE_FAILED`)

### 影响范围(升级须知)

- **行为变化**:匿名拉取预检对 quay.io / ghcr.io / gcr.io / public.ecr.aws 等「ping 401 + 资源带 scope」的公网 registry 从 `TOKEN_FETCH_FAILED` 变为通过;后续 manifest 预检照旧给出「这个源镜像能不能拉」的真实结论。
- **不变**:带凭据的 Probe 语义、Docker Hub 匿名(从「升级后 200」变为「401+challenge 即过」,结论相同,还省一次 token 往返)、内网 registry 200 直通。
- **数据兼容**:无存储 / SQLite / 凭据库层面改动。
- **已知遗留(下一轮候选)**:凭据库「测试」按钮(`TestCredential`)走的是裸 Basic 直发 `GET /v2/`,对 Bearer 型公网 registry(quay.io / Docker Hub)即使凭据有效也显示 401 —— 与本次修的 Probe 是两条链路,要修需把 token 交换引入该 handler,单独一轮做。

---

## [0.5.46] - 2026-09-29

本轮主题:**补齐 PATCH / GET 上传响应的 `Location` 头** —— 0.5.45 修好 upload-start 后,skopeo 的 POST 已经能拿到 202 + 绝对 Location,但 blob 数据传输(PATCH)完成后仍然报同一个错 `Error determining upload URL: http: no Location header in response`。这次的缺口在 **PATCH 响应**。

### 修复

- **PATCH / GET 上传响应补 `Location` 头**(`internal/registryd/routes.go`):

  UAT 实测证据链(skopeo --debug + cairn 访问日志):4 个 POST 全部 202、4 个 PATCH 全部 202,**之后没有任何 PUT finalize、也没有 manifest PUT** —— skopeo 在 PATCH 之后就放弃了。

  根因:OCI Distribution Spec 要求 **PATCH 202 响应必须带 `Location`**(下一步的上传 URL,服务端可以借机把会话重定位到另一个地址),skopeo/containers-image 传完每段数据后**从 PATCH 响应的 `Location` 头拿 PUT finalize 的地址**,不自己拼 URL。0.5.44 把 POST upload-start / PUT finalize / PUT manifest 三处改成了绝对 Location,唯独漏了 `uploadPatch`(只回了 `Docker-Upload-UUID` + `Range`)。

  为什么 curl 验证 POST 时看起来"已修好":POST 的 Location 确实修好了(0.5.45),skopeo 也正是靠它找到 PATCH 地址的;但 PATCH 响应缺头,下一步就断了。regsync 没撞到这个问题是因为 regclient 自己用 POST 返回的 Location 拼后续 URL,不读 PATCH 响应的 Location —— 两个客户端踩的是同一条 spec 的不同半截。

  修改:
  - `uploadPatch`:202 响应加 `Location: <scheme>://<host>/v2/<repo>/blobs/uploads/<uuid>`(走 `absoluteLocation`,与 0.5.44 口径一致;我们不重定位会话,原样回传同一上传 URL)+ 显式 `Content-Length: 0`
  - `uploadGet`:GET 上传进度(204)同样补 `Location`,与 PATCH 口径一致(spec 对 progress 查询响应同样要求)

### 影响范围(升级须知)

- **行为变化**:`PATCH /v2/<repo>/blobs/uploads/<uuid>` 与 `GET /v2/<repo>/blobs/uploads/<uuid>` 的响应多了 `Location` 头(绝对 URL)。请求处理逻辑、存储层、状态码均未变。
- **数据兼容**:完全兼容,无 SQLite / 凭据库 / 镜像存储层面的改动
- **客户端兼容**:skopeo 现在能走完 POST → PATCH → **PUT finalize** → manifest PUT 全流程;docker daemon / regsync 行为不变(它们本来就不依赖 PATCH 的 Location)

---

## [0.5.45] - 2026-09-29

本轮主题:**修正 0.5.44 的 dispatch bug** —— 0.5.44 改了 Location header 但请求根本没到 uploadStart handler,fall through 到了 404 `NAME_UNKNOWN`,所以 Location 头根本不会发出。

### 修复

- **upload-start route 接受带尾斜杠**(`internal/registryd/routes.go`):

  0.5.44 修了 Location header 必须是绝对 URL 的 spec 偏差,但部署后 skopeo 仍报 `Error determining upload URL: http: no Location header in response`。原因:

  ```go
  // 0.5.44 之前的代码:
  if repo, ok := strings.CutSuffix(rest, "/blobs/uploads"); ok && repo != "" {
      ...
      h.uploadStart(w, r)
  }
  ```

  OCI spec §5.2 / docker daemon / skopeo **都会**发送 POST 到 `/v2/<repo>/blobs/uploads/`(**带尾斜杠**)。`strings.CutSuffix(rest, "/blobs/uploads")` 只匹配无尾斜杠的写法,带尾斜杠的请求 fall through 到 dispatcher 末尾,被 `writeV2Error(... NAME_UNKNOWN ...)` 拒掉。**Location header 还没机会发,客户端就拿到 404 + JSON 错误体**(没有 `Location` header)。

  改为同时接受两种写法:
  ```go
  if repo, ok := strings.CutSuffix(rest, "/blobs/uploads/"); ok && repo != "" { ... }  // 带尾斜杠
  if repo, ok := strings.CutSuffix(rest, "/blobs/uploads");  ok && repo != "" { ... }  // 无尾斜杠(兼容)
  ```

### 影响范围(升级须知)

- **行为变化**:`POST /v2/<repo>/blobs/uploads/`(带尾斜杠,OCI spec 推荐)从**404 NAME_UNKNOWN** 变成 **202 Accepted + Location header**(绝对 URL,见 0.5.44)。
- **数据兼容**:完全兼容,无 SQLite / 凭据库 / 镜像存储层面的改动
- **客户端兼容**:docker daemon / skopeo / regsync 现在都能正常 upload blob;`docker push` / `docker pull` 行为不变(daemon 老版本就用带尾斜杠)。

---

## [0.5.44] - 2026-09-29

本轮主题:**修复 Location header 必须是绝对 URL**(原 3 处全是相对路径,导致严格客户端如 skopeo 直接报错)。来自 regsync 迁移测试的反馈 —— 实际是 Cairn 的 spec 实现偏差。

### 修复

- **Location header 改为绝对 URL**(`internal/registryd/routes.go`):

  OCI Distribution Spec §5.1 / §5.2 明确要求 `Location` header **必须是绝对 URL**(`http(s)://host[:port]/v2/...`)。Cairn 之前 3 处返回的全是相对路径(`/v2/<repo>/manifests/<digest>` 等):

  - `manifestPut` 写完回 `Location: /v2/<repo>/manifests/<digest>`(line 329)
  - `uploadStart` POST 后回 `Location: /v2/<repo>/blobs/uploads/<uuid>`(line 409)
  - `uploadPut` 提交后回 `Location: /v2/<repo>/blobs/<digest>`(line 484)

  docker daemon 老版本会自己拼(宽松),skopeo / regsync 这类严格客户端**不识别相对路径** → `http: no Location header in response` 错误直接退出,无法继续上传。

  新增 `absoluteLocation(r, relPath)` helper:按 `internal/api/handlers.go` 同样的 scheme/host 推导优先级 —— `X-Forwarded-Proto` > `r.TLS != nil`,`X-Forwarded-Host` > `r.Host`。反代后部署也能拿到客户端视角的绝对 URL。

### 影响范围(升级须知)

- **行为变化**:上传 / push manifest 后客户端拿到的 `Location` header 从 `/v2/...` 变成 `http(s)://<host>[:port]/v2/...`。
- **数据兼容**:完全兼容,无 SQLite / 凭据库 / 镜像存储层面的改动。
- **spec 偏差**:从 v0.1 起 Cairn 就有这个偏差,只是之前 docker daemon 自己拼路径所以没暴露。**0.5.44 把这 3 处都补成绝对 URL**,完全符合 OCI spec。

---

## [0.5.43] - 2026-09-29

本轮主题:**GOPROXY 配置流程顺一下** —— Makefile / .env.example 把 `https://goproxy.io,direct` 落成默认值,`make help` 显示当前生效的 GOPROXY,「永远走 `make rebuild`」注释强化。来自部署测试的 build timeout 反馈。

### 变更

- **`make help` 现在显示当前 GOPROXY / NPM_REGISTRY**(`Makefile`):

  之前 help 只显示 `IMAGE / PORT / HOST_PORT / DATA_DIR`,看不到当前用的是哪个 Go 模块代理 —— 受限网络下出问题要临时 debug 才能知道。现在 help 末尾多两行:
  ```
  GOPROXY=https://goproxy.io,direct
  NPM_REGISTRY=https://registry.npmmirror.com
  ```
  (实际值取决于 shell 环境变量是否预先设置,Makefile 用 `?=`)

- **`build` / `rebuild` target 注释强化**(`Makefile`):

  新增一段说明:「v0.5.43: 永远走 `make build` / `make rebuild`,不要裸跑 `docker build` —— 裸跑会用 Dockerfile 默认的 proxy.golang.org(在受限网络里超时),Makefile 默认 GOPROXY 是 `https://goproxy.io,direct`(已知能访问)。要看当前用的是哪个:`make help`。」

- **`.env.example` 默认 `GOPROXY=https://goproxy.io,direct`**(`.env.example`):

  之前默认 `GOPROXY=`(空),由 `docker-compose.yml` 的 `${GOPROXY:-https://proxy.golang.org,direct}` fallback 兜底。proxy.golang.org 在受限网络里会超时。现在 `cp .env.example .env` 后开箱就能用 `docker compose build`,不用手动改 GOPROXY。Makefile 路径不受影响(Makefile 自己的 GOPROXY 优先于 .env)。

### 影响范围(升级须知)

- **行为变化**:新部署 `cp .env.example .env` 后 `GOPROXY=https://goproxy.io,direct` 自动生效。**已存在的部署**不自动重写 .env,行为不变。
- **数据兼容**:完全兼容,无 SQLite schema / 凭据库 / 镜像存储层面的改动
- **Dockerfile 不动**:本轮只改 Makefile + .env.example + 帮助文案,Dockerfile 仍用默认 `proxy.golang.org,direct`(在受限网络里需要 build arg 覆盖)

---

## [0.5.42] - 2026-09-29

本轮主题:**「仓库地址」字段收紧到只接受 IP / 域名,精简帮助文案,清掉所有 `` 残留。** 来自部署测试反馈 ——「不需要输入端口」「提示太啰嗦」「示例里别出现 」。

### 修复

- **「仓库地址」字段校验收紧:只接受 IP 或域名,禁止端口**(`web/src/utils.ts` + `web/src/pages/settings-page.tsx` + `internal/api/handlers.go`):

  0.5.40 的设计是「允许填 `host[:port]`,port 表示对外端口」,但实际部署里:
  - 操作员填的 port 经常跟 docker-compose 的 `HOST_PORT` 对不上,容易配错
  - 「host vs host:port」两种形式让用户得想一会儿
  - 端口本来就是 docker-compose 配的(`HOST_PORT` env,见 0.5.40 的 env 通路),UI 在「监听端口」字段已经分容器内 / 宿主两层显示

  现在直接禁止 port:
  - 前端 `HOST_PORT_PATTERN` regex 去掉 `:port` 部分
  - 前端 `placeholder` 从 `registry.example.com:8787` 改成 `registry.example.com`
  - 后端 `hostPortRe` regex 同步收紧,`isValidHostPort` 简化(去掉端口范围校验)
  - 校验错误信息简化为「只接受 IP 或域名,不要带端口或协议」

  历史已保存的带端口值(`mutable.registryUrl` 里遗留的 `host:port`)**不会被自动清洗**——下次编辑保存时如果还带端口会被拒,操作员必须手动去掉端口再保存。

- **帮助文案大幅精简 + 移除所有 `` 字样**(`web/src/pages/settings-page.tsx` + `internal/api/handlers_extra.go`):

  之前的「仓库地址」字段 extra 是 4 行长篇说明(示例 × 2、端口解释、自动填行为、留空语义)。现在压成 1 行:
  > 供 docker login / docker push / docker pull 使用的对外地址(仅 IP 或域名)。

  字段 label 从「仓库地址(/前缀)」改成「仓库地址」(「/前缀」是 v0.5 早期加的,跟现在的「纯 host」语义不符)。

  `` 字样清理:
  - `web/src/utils.ts` 注释里的示例
  - `web/src/pages/settings-page.tsx` 注释 / 错误信息 / 帮助文案示例
  - `internal/api/handlers_extra.go` 注释里的示例
  - 一律改成中性描述或抽象示例(`registry.example.com` / `registry.example.com`)

  CHANGELOG 历史条目不动(那是历史记录,改写反而误导)。

### 影响范围(升级须知)

- **行为变化**:
  - 升级后**不能保存**带端口的「仓库地址」(前后端同步收紧)。如果 `mutable.registryUrl` 之前有 `host:port`,下次编辑保存会被拒,需要手动去掉端口
  - 帮助文案从 4 行变成 1 行
- **数据兼容**:**不主动清洗**历史数据 —— 已保存的带端口值仍会显示在表单里(用户能看见「之前填了 `:1122`」),但保存时会被拒。这是有意的:不想静默改用户配置
- **其他修复**(0.5.38 / 0.5.39 / 0.5.40 / 0.5.41 已就位,本轮不动):去外部字体 / Cairn favicon / 代理测试默认 Docker Hub / 自动填 + HOST_PORT 双端口 / 页面淡入 + 表格骨架 + localStorage 持久化

---

## [0.5.41] - 2026-09-29

本轮主题:**页面切换观感优化 + 刷新页面保留当前 tab**。来自部署测试的体感反馈 ——「页面切换生硬」「刷新就回首页」。轻量改动(纯前端),无新协议、无新功能。

### 修复

- **页面切换淡入动画**(`web/src/app.css`):

  之前 `App.tsx` 用硬 ternary 切页面,React unmount/mount 之间是「啪」一下,没有任何过渡。现在 `.page` 根节点挂 120ms `ease-out` 淡入 + 4px 上移过渡,看起来像「滑入」。CSS animation 只在 mount 时跑一次,后续 `setState` 不会重跑(避免状态变化时也淡入,看起来怪)。`@media (prefers-reduced-motion: reduce)` 下退化为「啪」一下,尊重 OS「减弱动态效果」偏好。

- **当前页面选择 localStorage 持久化**(`web/src/App.tsx`):

  之前刷新或新开 tab 总是回到「镜像列表」,操作员切到「代理管理」按 F5 就跳回首页,反直觉。现在把 `page` 状态写到 localStorage(`cairn-active-page`),首屏恢复上次选择。`isPageKey()` 校验防非法值,隐私模式 / 禁用 localStorage 降级老行为。`pageFilter`(侧栏筛选条件)暂不持久化 —— 改动更大,留待后续。

- **表格 loading 走骨架占位替代裸 spinner**(新增 `web/src/components/table-skeleton.tsx` + `web/src/pages/{images,pull,stats,credentials,proxies}-page.tsx`):

  antd `Table` 的 `loading` 属性只显示一个居中 spinner,视觉上像「页面是空的 / 坏了」。新建 `<TableSkeleton>` 组件,用 antd `Skeleton` 画 N 行表格骨架(列宽不等更接近真实表格),首屏 loading 时直接渲染,数据回来后切换为真实表格。

  - `images-page`:有 `loading` 状态,直接替换
  - `pull-page`:之前没有,新增 `loading` state(refresh 收尾),首屏走骨架
  - `stats-page`:3 张子表(top / clients / events)各自走骨架
  - `credentials-page`:之前没有,新增 `loading` state(禁用管凭据也收尾),首屏走骨架
  - `proxies-page`:之前没有,新增 `loading` state(禁用管代理也收尾),首屏走骨架

### 影响范围(升级须知)

- **行为变化**:
    - 刷新或新开 tab,默认仍从「镜像列表」开始;**只有访问过别的页面后再刷新才会保留**(localStorage 第一次没值)
    - 切页面时看到短暂淡入动画;开「减弱动态效果」的用户仍看到「啪」一下
    - 首屏访问 5 个表格页(proxies / credentials / pull / stats / images)时,在数据到达前会看到骨架占位(约 50-300ms,取决于网络)
- **数据兼容**:完全兼容,无 SQLite schema / 凭据库 / 镜像存储层面的改动
- **其他修复**(0.5.38 / 0.5.39 / 0.5.40 已就位,本轮不动):去外部字体 / Cairn favicon / 代理测试默认 Docker Hub / 「仓库地址」自动填 + HOST_PORT。

---

## [0.5.40] - 2026-09-29

本轮主题:**修正 0.5.39 自动填地址带错端口(只取 host 剥掉端口)+ 新增 HOST_PORT env 通路让 UI 「监听端口」同时显示「容器内 / 宿主机」两个值。**

### 修复

- **「仓库地址」自动填时只填 host,不再带端口**(`web/src/utils.ts` 新增 `hostOnly` + `web/src/pages/settings-page.tsx`):

  0.5.39 的自动填把 `r.Host` 整个塞进表单(含端口)。问题是 `r.Host` 的端口是**当前操作员的访问路径**(可能经反代/隧道/SSH 端口转发),不是「对外规范地址」的端口。例:`docker-compose` 是 `80→8787` 映射,操作员经 `:1122` 隧道访问,自动填 `cairn.example.com:1122` 会误导其他用户去访问 `:1122` —— 但 `:1122` 只是你本机的隧道端口。

  修法:自动填时用 `hostOnly()` 把端口剥掉,只留 `cairn.example.com`(默认 80 端口)。需要显式指定非标端口时再手填。帮助文本同步澄清「port 是对外端口,不是容器内 8787」。

- **「监听端口」同时显示容器内 + 宿主机两个值**(`internal/config/config.go` + `internal/api/handlers.go` + `docker-compose.yml` + `web/src/types.ts` + `web/src/pages/settings-page.tsx`):

  之前 UI 只显示 `cfg.Port`(容器内 8787)。HOST_PORT 是 docker-compose 端口映射的左侧(`${HOST_PORT:-8787}:8787`),cairn 进程在容器里看不到。现在 docker-compose 把 `HOST_PORT` 通过 `environment:` 块传进容器,cairn 读取并暴露在 `/api/config.hostPort`。两者不同时,UI 显示「8787(容器内) / 80(宿主机)」;相同时,合并显示一个数字。

  - `internal/config/config.go`:新增 `Config.HostPort` 字段,从 `HOST_PORT` env 读(默认 0 = 与 Port 同)
  - `internal/api/handlers.go` `AppConfig`:新增 `HostPort int json:"hostPort"`
  - `docker-compose.yml`:在 `environment:` 块加 `HOST_PORT: ${HOST_PORT:-8787}`
  - `.env.example` `HOST_PORT=80` 已就位,补充一句说明
  - `web/src/types.ts`:AppConfig 加 `hostPort: number`
  - `web/src/pages/settings-page.tsx` 「监听端口」字段改成「8787(容器内) / 80(宿主机)」格式

### 新增

- **`hostOnly()` 工具函数**(`web/src/utils.ts`):从 `host:port` 字符串中只取 host 部分(端口是末尾纯数字时剥掉;IPv6 字面量暂不处理)。0 = 与 Port 同的快写。

### 文档

- **`AGENTS.md` env 表更新**:`HOST_PORT` 加进基础设施 env 表,说明「容器内 vs 宿主机」两件事。
- **`CHANGELOG.md`**:本节。

### 影响范围(升级须知)

- **行为变化**:
    - 「仓库地址」表单刚打开时,从 `cairn.example.com:1122` 变成 `cairn.example.com`(剥端口)。需要非标对外端口的用户手动加。
    - 「监听端口」字段从「8787」变成「8787(容器内) / 80(宿主机)」(如果传了 HOST_PORT 且不等)。不传 HOST_PORT 时仍是单值。
- **数据兼容**:完全兼容,无 SQLite schema / 凭据库 / 镜像存储层面的改动。
- **docker-compose.yml 必须升级**:从 0.5.39 升级到 0.5.40 时,docker-compose.yml 的 `environment:` 块新增了 `HOST_PORT: ${HOST_PORT:-8787}`。不升级 compose 文件也能跑,但 UI 只会显示容器内端口。
- **其他修复**(0.5.38 / 0.5.39 已就位,本轮不动):去外部字体 / Cairn favicon / 代理测试默认 Docker Hub / 「仓库地址」表单自动填。

---

## [0.5.39] - 2026-09-29

本轮主题:**修正 0.5.38 第 1 个修复的方向** —— 用户反馈「仓库地址未配置时表单应该是默认填 IP,而不是 badge 也跟着变『未配置』」。其余 3 个修复（去外部字体 / favicon / 代理测试默认目标）保持 0.5.38 不变。

### 修复

- **「仓库地址」表单在未配置时预填 badge 的自动识别值**(`internal/api/handlers.go` + `web/src/App.tsx` + `web/src/pages/settings-page.tsx`):

  0.5.38 的方向反了 —— 把 badge 的 `r.Host` 兜底去掉,改成「未配置 → badge 空 / 表单空」。实际上大多数部署场景里,用户希望「刚部署完表单就跟 badge 一样显示当前访问地址」,所以改成:

  - **后端**:`GetConfig` 恢复 `r.Host` 兜底(`displayURL = http://[RegistryURL or r.Host]`),回到 0.5.37 之前的行为
  - **前端编辑态**:`useEffect` + `cancelEdit` 初始化草稿时,若 `mutable.registryUrl` 为空且 `config.url` 非空(后端兜底出来的值),就把 `config.url` 剥协议前缀作为草稿默认值。视觉上「badge ↔ 表单」完全一致,「看到什么就改什么」。
  - **前端只读态**:`mutable.registryUrl` 为空时显示 `http://[自动识别的地址]（跟随访问地址,未保存）`,明确告知这个值是后端兜底出来的、点「编辑」可以真的覆盖。
  - **前端取消编辑**:也用同样的预填逻辑,避免「取消后再点编辑又回到自动填」的回退感。

  保存语义不变 —— 草稿与 `mutable.registryUrl` 不一致才 PATCH;用户在编辑态直接保存(未改动)会自动把当前 badge 值落盘,等价于「确认用这个地址」。

### 影响范围(升级须知)

- **行为变化**:
  - 升级前如果显示「Cairn Prod (未配置)」,升级后恢复显示「Cairn Prod (http://[访问地址])」(0.5.37 之前的行为)
  - 「仓库地址」表单刚打开时从空变成预填「http://[访问地址]」(只是编辑态的草稿,mutable.registryUrl 仍是空;点保存才会落盘)
- **数据兼容**:完全兼容,无 SQLite schema / 凭据库 / 代理库 / 镜像存储层面的改动
- **其他修复**(0.5.38 已就位,本轮不动):去外部字体 / Cairn favicon / 代理测试默认 Docker Hub —— 全部保留。

---

## [0.5.38] - 2026-09-29

本轮主题:**部署测试(v0.5.37)暴露的 4 个 UX / 依赖问题** —— 全部是修小磨小,没有新模块、没有新协议,小版本 +1。

### 修复

- **右上角 badge 与「仓库地址」表单对齐,不再自动 fallback 到 `r.Host`**(`internal/api/handlers.go` `GetConfig` + `web/src/App.tsx` + `web/src/pages/settings-page.tsx`):

  之前 `cfg.RegistryURL` 留空时,badge 会自动填当前请求的 `r.Host`(例:`http://registry.example.com:8787`),导致用户在「设置 → 仓库连接」看到一个空字段却在右上角看到一个 IP,看起来像「不一致」。现在 badge 与表单一对一:RegistryURL 配了什么 badge 就显示什么,没配就显示「(未配置)」。要回归「自动检测」语义请直接填 host:port 到表单。

- **完全移除 `fonts.googleapis.com` / `fonts.gstatic.com` 外部依赖**(`web/index.html` + `web/public/cairn-intro.html` + `web/src/theme.css`):

  v0.5.37 引入了 Google Fonts CDN + `font-display: swap` 的兜底方案,但实测在离线 / 受限网络(53 跳板机、企业 proxy、纯内网部署)下,Chromium 仍会同步请求 `*.woff2` 文件直到超时,表现为网络面板里出现一长串超时请求、首次访问慢 30s+。现在完全不引入外部字体,`theme.css` 的 `--font-sans` / `--font-display` / `--font-mono` 直接走系统栈(西文走 SF Pro / Segoe UI,等宽走 ui-monospace / Menlo,中文走 PingFang SC / Microsoft YaHei),与 Outfit 的圆润字形略有差异但视觉接近,换来的是「离线部署零外部依赖」。

- **代理测试默认目标改为 `https://registry-1.docker.io/v2/`**(`internal/api/handlers_extra.go` `proxyTestTarget` + `web/src/pages/proxies-page.tsx`):

  之前默认走 `RegistryURL() + "/v2/"`,在 RegistryURL 是裸 hostname(如 `cairn.example.com`,没配 scheme)时会拼出无 scheme 的 URL,Go 的 `http.NewRequest` 报 `unsupported protocol scheme ""`,实测踩过。**默认改成 Docker Hub 公网 /v2/** —— 实际场景里代理测试的目的几乎是「能不能出外网」,用 Docker Hub 更贴合运维直觉;要测本仓库请在「测试目标」字段填完整 URL(含 `https://`)。

### 新增

- **Cairn 品牌 favicon**(`web/public/favicon.svg` + `web/index.html` `<link rel="icon">`):

  之前没有 favicon,浏览器 tab 显示默认占位图(`web/public/cairn-intro.html` 与 `web/src/components/cairn-mark.tsx` 的几何保持一致:三块圆角矩形堆叠成塔身 + 顶部琥珀色标记点,viewBox 64x64)。选用 SVG 而非 `.ico` 的原因:单文件即可覆盖所有 DPI,不需要为 16/32/48/64 各出一份位图。

### 文档

- **`docs/product/cairn-intro.html`** 的 `font-family` 同步移除 `'Outfit'` / `'JetBrains Mono'` 引用(只保留系统栈),与 `web/index.html` 一致。

### 影响范围(升级须知)

- **行为变化**:
  - badge 在仓库地址未配置时显示「(未配置)」(旧版本会显示 `http://[当前 IP]`)。如果之前依赖 badge 自动显示当前访问地址,现在需要在表单填一次。
  - 代理测试默认目标从「本仓库 /v2/」变成 Docker Hub /v2/。已有部署如果在「测试目标」里显式填过值则不受影响;留空的新测试会去打 Docker Hub。
- **离线部署**:首屏加载不再有任何外部网络请求,生产部署可以放在完全隔离的环境里跑。
- **视觉**:西文字体从 Outfit 改成 SF Pro / Segoe UI 等系统字体,中文不变。整体观感差异极小。

---

## [0.5.37] - 2026-09-29

本轮主题:**UI 改版 —— 全面采用 `docs/design/cairn-ui-design.html` 设计稿(暖米底色 + 三字体 + 圆角/阴影 token 化)。** `cairn` 仍是 module path / 二进制名 / 内部代号,产品面对用户时仍叫 Cairn。

### 新增

- **Outfit 字体作为 UI 主字体引入**(`web/index.html` `<link>`):

  设计稿 § 2 Typography 明确把 Outfit 列为 UI 主字体(5 个 weight:300/400/500/600/700),与现有 antd UI 风格匹配。**通过 Google Fonts CDN + `font-display: swap` 加载**,CDN 失败时降级到系统栈(`-apple-system, BlinkMacSystemFont, PingFang SC, …`),不白屏也不显示空白字。`docs/design/` 已整合的设计稿(commit `21a9198`)是 token 取值的唯一来源。

- **`--font-sans` / `--font-display` / `--font-mono` 三个字体 token**(`web/src/theme.css`):

  - `--font-sans: 'Outfit', -apple-system, BlinkMacSystemFont, 'PingFang SC', …` —— 供 body 字体栈使用
  - `--font-display: 'DM Serif Display', Georgia, 'Times New Roman', serif` —— 供 `.display` 类使用(hero / 页面大数字)
  - `--font-mono: 'JetBrains Mono', ui-monospace, SFMono-Regular, Menlo, Consolas, monospace` —— 供 `.mono` 类(hash / digest / 时间戳)使用

  新增 `.display` CSS 类(走 `--font-display` + `font-weight: 400` + `letter-spacing: -0.02em`),后续 hero 用得上。深色与浅色共用同一组字体 token。

### 变更

- **暖米底色 / 暖灰边线主题对齐设计稿 § 1 Color**(`web/src/theme.css` + `web/src/main.tsx` `LIGHT_TOKENS`):

  - 浅色 `--color-background-body`: `#f2f4f7` → `#f7f5f0`(暖米);深色保持 `#0f131a`
  - 浅色 `--color-border-warm`: `#eaecf0` → `#e9e3d6`(暖灰);深色新增 `#2a313d`
  - antd `ConfigProvider` `LIGHT_TOKENS`: `colorBgLayout` `#f2f4f7` → `#f7f5f0`、`colorBorder` `#eaecf0` → `#e9e3d6`

  **意图**:与 `docs/design/cairn-brand.html` 一致的「纸张感」底色,配合暖灰边线让数据密集区(表格 / 表单 / KPI 卡)有更柔和的视觉边界。**KPI 卡 / 表格内部仍走纯白**(`--color-bg-1: #ffffff`),不让数据读起来累。

- **圆角 / 阴影 token 化对齐设计稿 § 4 Surfaces**(`web/src/theme.css` + `web/src/app.css`):

  - 新增 `--radius-lg: 12px`(面板 / hero 用)
  - `--radius-md`: `8px` → `10px`(KPI 单卡用,本仓库一致)
  - `--radius-sm`: 6px(小件 / icon 保留)
  - 新增 `--shadow-card: 0 1px 2px rgba(20, 23, 30, 0.05)`(浅色)/ `0 1px 2px rgba(0, 0, 0, 0.6)`(深色,黑底叠黑等于没有,要加深)
  - `.panel` 圆角 `var(--radius-md)` → `var(--radius-lg)`(10 → 12px),与设计稿「容器 12 / 单卡 10」的分档一致

- **body 字体栈 / 行高调整**(`web/src/theme.css`):

  - `font-family` 从硬编码系统栈 → `var(--font-sans)`(Outfit 主 + 系统栈兜底)
  - 新增 `line-height: 1.5`(暖米底色下读感更松)

- **版本号同步**(按 AGENTS.md「一次改动要同时更新这几处」):`internal/version/version.go`、`.env.example` 的 `IMAGE`、`docker-compose.yml` 的 `${IMAGE:-…}`、README 「当前状态」+「当前版本」、Makefile `IMAGE ?=`、CHANGELOG 顶部节标题 全部 → `0.5.37`。

### 文档

- **`docs/design/` 整合**(本轮未单独进位,见 commit `21a9198`):把品牌稿 / UI 设计稿 / 6 页 teal 实际样子原型(7 个 HTML 文件共 ~107 KB)集中到 `docs/design/`,作为 0.5.37 token 取值的事实来源,README 路径修复。

### 影响范围(升级须知)

- **行为变化**:无 —— 仅视觉层(token + 字体 + 圆角),API / 数据 / 后端逻辑全部不动。
- **首屏**:Google Fonts CDN 加载 ≈ 50 KB(Outfit 5 weight)+ DM Serif Display ≈ 12 KB + JetBrains Mono ≈ 18 KB,`font-display: swap` 让首字不阻塞。**离线 / 防火墙环境自动降级到系统栈**,视觉接近但不字面一致。
- **浏览器兼容**:`font-display: swap` 要求 Safari 11.1+ / Chrome 60+ / Firefox 58+ / Edge 17+;老版本浏览器会走 FOIT(Flash of Invisible Text)约 100ms,可接受。**没有 fallback 到 `block`** —— 因为那会阻塞首屏。
- **`cairn-mark.tsx` 颜色未动**:4 个硬编码 hex(`#0d9488` / `#f59e0b` / `#5eead4` / `#fbbf24`)与 `docs/design/cairn-brand.html` § Brand 色定义字面一致,故意不跟随 token(品牌色不应当被主题切换改变)。本轮不动,留待 0.5.x 后续优化。
- **每页内 220 px sub-nav 侧栏**(commit `e97c760` 补):

  设计稿 § 6 + demo-A-*.html 明确侧栏是**每页内部**的 sub-nav(仓库筛选 / 凭据分组 / 拉取历史 / 时间窗 / 协议分类 / 设置分类),不是顶级导航替代。本轮 0.5.37 接续:顶栏的 6 Tab 仍是顶级导航不变,在 `.app-body` 里加 220 px 侧栏。

  - App.tsx layout:`.app-main` → `.app-body`(grid `220px 1fr`),前置 `<PageSidebar page={page} />`;6 Tab 顶级导航保留不动
  - 新增 `web/src/components/page-sidebar.tsx`:按 page key 渲染对应 group + item(6 page 全覆盖)
  - app.css:`.app-body` / `.app-page-sidebar` / `.page-sidebar-group-label` / `.page-sidebar-item` 一套样式;色取自 theme.css 已有 `--color-side-nav-*` token(深浅两套);圆角 12 px 与 § 4 Surfaces 对齐
  - **v0.5.37 范围内仅做"UI 占位 + active 高亮",不联动 page 内容过滤**(避免触动每个 page 内部 state 体系);后续 0.5.x 优化时再加联动
  - **14 场景 selector 不变**(顶 Tab 仍是顶级导航,scenarios 用 `.app-nav .ant-segmented-item-label` 仍有效)

### 修复(0.5.37.1)

- **`web/index.html` Google Fonts 改成 `rel="preload" as="style"` + `onload` 异步切换 stylesheet**(commit `5adedd0`):

  **根因**:用 `<link rel="stylesheet">` 引用 fonts.googleapis.com 时,headless chromium 把 stylesheet 当 render-blocking。53 runner 测试环境访问 fonts.googleapis.com 不通(实测 fetch timeout 11s),chromium 的 `domcontentloaded` 永不 fire(30s timeout),导致整页卡白屏。

  **修复**:`<link rel="preload" as="style" href="...fonts.googleapis.com..." onload="this.rel='stylesheet'; this.onload=null;">` —— 预加载但不阻塞渲染,onload 异步切到真正的 stylesheet。`<noscript>` 兜底给禁用 JS 的浏览器。

  **效果**:53 chromium 实测 `domcontentloaded` 1s 内可达;CDN 可达 → Outfit 切到 stylesheet 生效;CDN 不可达 → `font-display: swap` + theme.css `--font-sans` 系统栈 fallback,首屏不白屏也不显示空白字。

### 修复(0.5.37.2)

- **间距对齐设计稿**(commit `aa8ee66`):

  **设计稿的间距规范本来就是对的**(`docs/design/demo-A-*.html` § `.content` `padding: 24px 28px` + § `.page-head` `margin-bottom: 20px`),cairn 0.5.37 漏了这两条 —— 本轮只动 cairn,**未改设计稿**。

  - **右红框违和**(搜索 + 时间窗 + 按钮紧贴 .app-body 顶部):修 `web/src/app.css` `.app-content` 加 `padding: 24px 28px`(原 0)
  - **page-header 跟下面 KPI cards 紧贴**:修 `.page-header` 加 `margin-bottom: 20px`(原 0)
  - **左红框违和**(sidebar 内容集中顶部 ~200px + 底部 ~300px 空白):修 `web/src/components/page-sidebar.tsx` 加 `.page-sidebar-footer`(`v0.5.37 · © Cairn`),让 sidebar 看起来是"完整结构"而不是"上短下空"。grid 撑满是 sidebar 内容少时的预期表现;设计稿 demo 没有 footer(因为 demo 写死了 height: 240px),cairn 用 100vh + 内容稀疏,所以**加 footer 块是 cairn 侧的必要补全**。

### 重构(0.5.37.3)

- **PageSidebar 改成受控 + 联动真生效**(commit `8e27671`):

  之前 0.5.37.2 sidebar item 点击只是 `setActive` 改本地高亮,内容区不响应。本轮改成受控:

  - `web/src/components/page-sidebar.tsx`:移除内部 `useState`,改成接受 `selected: string | null` + `onSelect: (itemKey) => void`
  - `web/src/App.tsx`:加 `pageFilter` state(`Record<PageKey, string | null>`),按 page 维度持有当前选中的 item key;切页时不重置 filter(浏览器表单持久化语义)
  - **ImagesPage 联动**(完整生效):`仓库` group item → 联动 search 框 ——「仓库:library」→ search=`"library"`;「仓库:registry-manager」→ search=`"registry-manager"`;「仓库:全部」→ 清空 search;「操作」group item 不联动(扫描清单 / 运行 GC / 备份 已在 page header actions)
  - **StatsPage 联动**(完整生效):`时间窗` group item → 联动 `setDays` ——「最近 7d」→ 7;「最近 30d」→ 30;「最近 90d」→ 90;「全部」→ 90(`StatsWindow` 类型严格 7/30/90,90 近似"全部")
  - **4 个 page 加 sidebarFilter prop 占位**:PullPage / CredentialsPage / ProxiesPage / SettingsPage 接口通了,联动逻辑留 0.5.x 后续优化 —— `pull-page` 按 registryUrl 过滤;`credentials-page` 需要 schema 加 group / source 字段;`proxies-page` 可按 url 前缀(http/https/socks5) + lastProbeStatus 过滤;`settings-page` 可 anchor 跳转到对应 section

  **实测**(53 chromium):点 sidebar「library」→ search 输入框值 `"library"` ✅;点「全部」→ search 清空 ✅;切到 stats 页面,点「最近 7d」→ 时间窗 segmented 选中"7 天" ✅;点「最近 90d」→ 选中"90 天" ✅。

### 重构(0.5.37.4)

- **每页侧栏改成真正的页内 sub-nav:侧栏只放"视图状态",badge 全部真实派生**(6 页一次改完):

  0.5.37.3 解决了"点了不联动",但侧栏里还留着**没有数据支撑的假分组**(操作 / 分类 / 通知 / 客户端 UA / 历史 / 最近使用 / 平台)—— 点了要么什么都没发生,要么跟页头的操作按钮重复。本轮定下三条总纲:

  1. **侧栏 = 页内 sub-nav**:只承载视图状态(过滤 / 切换 / 锚点跳转)
  2. **有副作用的操作统一归页头**:侧栏不再出现"扫描清单 / 运行 GC / 备份"这类动作
  3. **同一状态只出现一次;badge 一律真实数据**:没有数据支撑的分组直接删,不编数字

  各页最终分组(**badge 全部取自当前接口返回的真实数据**):

  | 页面 | 侧栏分组 | 数据口径 |
  | --- | --- | --- |
  | 镜像列表 | 仓库(按命名空间) | 按仓库名第一段路径聚合;badge = 该命名空间下的仓库数;「全部」badge = 仓库总数;数量降序 |
  | 热度统计 | 时间窗 | 7d / 30d / 90d;**页头重复的 Segmented 已删**(同一状态只留侧栏一处) |
  | 拉取队列 | 状态 + 来源 | 状态:全部 / 进行中(queued + running 合并)/ 成功 / 失败 / 已取消;来源:按 `URL(sourceUrl).host` 分桶,解析失败或为空归「未知」 |
  | 凭据 | 主机 + 密码状态 | 主机:按去 scheme 的 `host[:port]` 分桶;密码状态:全部 / 已保存 / 未保存 |
  | 代理 | 协议 + 探测状态 | 协议:按 URL scheme 分桶(label 大写);探测状态:成功 / 失败 / 未探测(**只认已落库的 `lastProbeStatus`**,不在前端猜) |
  | 设置 | 快速跳转(anchor 模式) | 仓库连接 / 功能开关 / 拉取平台 / 热度记录;滚动跳转 + scroll-spy 高亮当前区块 |

  - **`web/src/components/page-sidebar.tsx` 改成纯 props 驱动**:导出 `PageKey` / `SidebarItem` / `SidebarGroup` / `SidebarSelection` / `PageSidebarProps`;`SidebarGroup.mode` 支持 `'filter'`(过滤,默认)与 `'anchor'`(滚动跳转)。组件内部**不再内置任何分组表** —— 分组与 badge 全部由各页 `useMemo` 派生后上浮,数据从哪来就在哪算
  - **anchor 模式**:`item.key` 即目标 DOM id,点击走 `scrollIntoView({behavior:'smooth', block:'start'})`;scroll-spy 用 `IntersectionObserver`(`rootMargin: '-20% 0px -60% 0px'`)高亮当前区块;目标节点最多重试 6 次 × 250ms,仍找不到就静默降级(不动画、不报错)
  - **footer 版本号改读运行时 `config.version`**:原来写死字面量,发版后侧栏会跟实际跑的版本漂移
  - **`web/src/App.tsx`**:`pageFilter` 从 `string | null` 改成 `Record<PageKey, SidebarSelection>`(一页可有多组,跨组 AND);`'all'` 是保留 key,App 侧统一存 `null`(点「全部」= 清掉该组过滤);点已选中项是 no-op(不做 toggle-off,避免"再点一下反而全放开"的意外);新增 `pageGroups` state + `publishGroups`(带身份守卫 `prev[key] === groups ? prev : {...}`,防止页面重复发布把 App 拽进自激循环)+ `publishHandlers`(`useMemo` 稳定引用,6 个 bind)
  - **可见行集是唯一派生点**:images 的 `rows` / pull 的 `visibleJobs` / credentials 的 `visibleCredentials` / proxies 的 `visibleProxies` —— 过滤只在这一个点叠加(跨组 AND),表格直接读它;images 的 KPI 指标派生自同一行集,所以自然跟着过滤走。过滤是**按字段精确匹配**,不是拼字符串模糊搜。过滤后为空时给差异化 `emptyText`,让用户分得清"是筛掉了"和"本来就没有"
  - **不影响后台行为**:过滤只作用于展示层 —— pull 页的 auto-expand / `hasActive` / `runningJob` / `queuedJobs` 仍读全量(`jobs`),不会因为用户筛掉"进行中"就把轮询停掉
  - **镜像列表不再污染搜索框**:0.5.37.3 是"点侧栏 → 把关键词写进搜索框",会覆盖用户自己输的词;本轮改成独立的 `repo` 过滤维度,与搜索框正交(可以同时用)
  - **`web/src/app.css`**:新增 `.page-sidebar-empty`(侧栏无可用筛选项时的占位文案)与 `.settings-anchor`(`scroll-margin-top: 12px`,锚点跳转补偿;`.app-content` 内没有 sticky 元素叠加,所以只用小值)
  - **各页无数据时发空组**:拉取队列 `jobs.length === 0`、凭据 `allowCredentials === false`、热度统计 `statsEnabled === false`、设置 `config` 未加载 —— 都发空数组,侧栏显示空态而不是"点不动的假分组"

- **设置页:锚点导航 + DOM 顺序调整 + 过时文案修正**:

  - **DOM 顺序**:「拉取平台白名单」与「热度保留天数」换序,让 4 个锚点块的顺序和侧栏一致(仓库连接 → 功能开关 → 拉取平台 → 热度记录)。`<Form>` 语义不动,只在外面包 `<div id="settings-xxx" className="settings-anchor">`;「热度记录」块只包「热度保留天数」(忽略规则表与清空热度紧随其下,不并入锚点)
  - **过时文案修正**(0.5.9 起业务配置全部走 UI → SQLite,`REGISTRY_*` env 与 `registry.config.json` 已不再被读,设了等于没设):
    - 信息条标题 `如何修改要管理的镜像仓库` → `改这些参数不用碰环境变量`,正文改成"都存在本机 SQLite,面板里点『编辑』改、保存即刻生效",并说明只剩 `HOST_PORT` / `HOST_DATA_DIR` 这类基础设施映射要走容器;**删掉教人配 env 与 `registry.config.json` 的整个代码块**(那是 0.5.9 之前的老办法)
    - 只读模式信息条:「允许删除」为关闭时不再讲 `allowDelete: false` / `REGISTRY_ALLOW_DELETE=false` / 重启,改成"把上面的『允许删除』开关切到『只读』并保存,立即生效,无需重启"
    - 「仓库地址」说明里的 markdown 星号(`**这里只填 host:port**`)在 JSX 里会原样显示,改成 `<strong>`
  - **`sidebarFilter` 只声明不使用**:App 强制传该 prop,设置页没有可过滤的数据(唯一表格「热度忽略规则」的条目数由规则数决定,过滤维度没有意义),锚点组点击不写 `pageFilter`

- **本轮不动版本常量**:纯前端语义重构,不引新功能,`internal/version/version.go` 保持 `0.5.37`;`0.6.0` 留给 registry 同步。

**影响范围(升级须知)**:侧栏从"装饰性 UI"变成"实际控制内容区"的唯一入口 —— 时间窗等控件的位置变了(从页头移到侧栏),操作按钮位置全部保留在页头不变。**没有 API / 数据 / 后端逻辑改动,升级无需迁移**。

### 文档(0.5.37.5)

- **新增 Cairn 产品介绍页 `docs/product/cairn-intro.html`**:

  面向读者的单文件产品介绍页 —— 自包含(内联 CSS + 内联 SVG),外部网络依赖只有 Google Fonts 字体;视觉整页 1:1 继承 `docs/design/cairn-brand.html`(`:root` token 全套:暖米底色 / Teal+Amber 双色 / 三字体 / 圆角与阴影;`.card` / `.const-list` / `.version-badge` 等组件样式复用,Logo mark 复用同一套 SVG 几何)。内容为 hero + 7 段:这是什么(定位 + 与 registry-manager 的 4 条关系:兼容 / 视觉一致 / 不是 fork / 不追版本号)→ 设计目标(4 张卡)→ 能力地图(8 张卡:数据平面 / 管理平面 / 前端 / 工程化,带 `/v2/*` `repos/ · blobs/ · uploads/` 等 mono 标签)→ 单二进制架构(`clients` → `:8787` → `internal/` → `/app/data` 四行流转图)→ 快速开始(4 行 compose 起服务 + 3 张运维卡:数据目录 / 配置分层 / 端口两层)→ 边界(6 项不做 + amber callout 说明 `/v2/*` 当前匿名)→ 延伸阅读(7 张卡:设计资产总览 / 品牌稿 / UI 设计稿 / 页面原型 / ROADMAP / CHANGELOG / README)。

- **落位:新建 `docs/product/`,与 `docs/design/` 平行**:

  `design/` 收纳品牌与界面设计资产(品牌稿 / UI 设计稿 / 页面原型),`product/` 收纳面向读者的产品文档 —— 避免把产品文档塞进设计资产目录,语义干净,后续产品向文档继续放这里。介绍页本身自包含单文件、与落位解耦:以后要挂内网 URL 或移动位置,成本都低。

- **README「品牌资产」小节补入口**:在三份设计稿条目之后加一行「产品介绍页」链接;不改 `docs/design/index.html`,避免污染设计枢纽。

- **本轮不动版本常量**:纯文档新增,不引新功能,`internal/version/version.go` 保持 `0.5.37`。

### 新增(0.5.37.6)

- **产品介绍页搬进产品本体:`docs/product/cairn-intro.html` → `web/public/cairn-intro.html`**:

  0.5.37.5 把介绍页放进仓库文档目录 —— 结果是**部署完看不到**,得先去翻仓库才找得着。本轮把它接进前端构建链路:`web/public/` 是 Vite 的 `publicDir`(`web/vite.config.ts` 用默认值,不改),构建时**原样拷进** `internal/webui/dist/`,再被 `internal/webui/assets_built.go` 的 `//go:embed all:dist` 收进二进制。**零 Go 改动**,运行实例上直接访问 `/cairn-intro.html`(`internal/api/api.go` 的 catch-all `webui.Handler()` 已覆盖:先查真实文件,查不到才回落 index.html)。

  已否决的两条路:① `internal/webui/` 单独 go:embed(多一套机制,且 `assets_stub.go` 的 stub 构建语义对不上);② `docs/` 与 `web/public/` 留双份(**两处维护必然漂移**,`docs/product/` 下那份已删除)。

- **部署语境适配 —— 页面在运行实例里必须自洽,不能照搬仓库文档的写法**:

  - **Google Fonts 改非阻塞**:`<link rel="stylesheet">` 改成 `rel="preload" as="style"` + `onload` 切 `stylesheet` + `<noscript>` 兜底(同 `web/index.html` 的 0.5.37.1 做法)。为什么是硬要求:内网访问不到 `fonts.googleapis.com` 时,**render-blocking 的样式表会让 chromium 的 DCL 永不 fire**(0.5.37.1 实测 30s 超时);字体只是视觉增强,不该拖住整页。
  - **7 张「延伸阅读」卡 → 1 张同源 CTA**:原卡片指向 `docs/design/*.html`、`docs/ROADMAP.md`、`CHANGELOG.md`、`README.md` 等**仓库相对路径,部署后必然 404**;改成单张「管理控制台」(`/`),并在 `.foot-note` 说明设计资产 / 路线图 / 变更日志随**源码仓库**分发,不在运行实例内。
  - **版本号动态化**:写死的 `v0.5.37` 会跟镜像 tag 漂移(页面随二进制走),改成 `[data-cairn-version]` 占位 + 页尾 `fetch('/api/config')` 写入运行中版本 —— 对齐侧栏 0.5.37.4 的同源做法;fetch 失败时**静默保留静态值**,不留空白,也不弹错。
  - **footer 去掉源码路径**:原 footer 写 `docs/product/cairn-intro.html`,换成「管理控制台 →」同源入口。

- **侧栏 footer 挂入口**(`web/src/components/page-sidebar.tsx` + `web/src/app.css`):

  `.page-sidebar-footer` 从 `v0.5.37 · © Cairn` 变成 `v0.5.37 · 产品介绍 · © Cairn`;`<a target="_blank" rel="noopener">` 新窗口打开 —— 保留控制台里的筛选 / 分页状态。样式用 `color: inherit` + 点线下划线(视觉权重跟版本号同档),hover 才提到 `--color-primary`,避免侧栏底部挂一个抢眼的蓝链接。**不开第 7 个 Tab**:介绍页不是控制台的一页,塞进顶部导航会稀释 6 Tab 的信息架构。

- **README「品牌资产」条目改指向 `web/public/cairn-intro.html`**,并注明「**随二进制分发**,运行实例上访问 `/cairn-intro.html`;也可从侧栏 footer 的『产品介绍』进」。

- **本轮不动版本常量**:页面随 `cairn:0.5.37` 镜像一起分发,**不引新模块 / 新 API / 新进程**,`internal/version/version.go` 保持 `0.5.37`;`0.6.0` 留给 registry 同步。

- **影响范围(升级须知)**:本次改了**前端源码**(`web/public/` 新增文件 + 侧栏组件 + 样式),所以升级**必须重建镜像**(`docker compose build && docker compose up -d`),只 `up -d` 拿不到新页面 —— 旧镜像的 dist 里没有这个文件。**无 API / 数据 / 后端逻辑改动,无需迁移**。

## [0.5.36] - 2026-09-29

本轮主题:**统一 SQLite DB 文件名 `cairn.db` → `cairn.db`(与产品名对齐,v0.5.21 起的「image / container / service = cairn」命名一致)+ 顶部导航 Tab 顺序按「查/操作 → 观测 → 管理」重排**。

### 修复

- **DB 文件名 `cairn.db` → `cairn.db`**(`internal/server/server.go:361` `safeDBPath`):

  旧:`safeDBPath` 返回 `filepath.Join(dataDir, "cairn.db")` —— DB 文件名沿用旧产品名,与 v0.5.23 起的「image / container / service = cairn」、v0.5.21 起的品牌升级不一致。
  新:返回 `filepath.Join(dataDir, "cairn.db")`。**容器内 `/app/data` 仍是编译期常量**,只改 DB 文件名。

  **数据迁移**:本轮**无自动迁移** —— 用户在测试阶段,可以直接删 `/data/cairn/cairn.db` / `cairn.db-shm` / `cairn.db-wal`,让 cairn 重新建一个空 `cairn.db`。生产环境后续若要兼容升级,需另加一次性 `rename` 逻辑(v0.5.x 内不引入,留待正式发布线)。

- **同步位置**:README「数据目录约定」一节列举文件时 `cairn.db` → `cairn.db`;`docker-compose.yml` 注释里 `cairn.db / heat.db` → `cairn.db / heat.db`(注意 `heat.db` 是热度数据,本轮不动 —— 名字本身就是「heat」语义)。

### 变更

- **顶部导航 Tab 顺序调整**(`web/src/App.tsx` `NAV_ITEMS` + 渲染三元链):

  旧:`镜像列表` → `镜像热度` → `镜像拉取` → `凭据管理` → `代理管理` → `设置`
  新:`镜像列表` → `镜像拉取` → `镜像热度` → `凭据管理` → `代理管理` → `设置`

  原因:user 反馈「**列表是查看,拉取是发现不够了来操作的,才是热度与管理**」—— 把 `镜像拉取` 上移到 `镜像热度` 之前,符合从「查/操作 → 观测 → 管理」的真实使用路径。`NAV_ITEMS` 与渲染三元链同步调整(否则会出现「点了第 3 个 Tab 渲染第 5 个 Tab」的渲染错位)。**纯展示层改动,无 API / 数据 / 行为变化**。

- **`tests/web-auto/scenarios/_smoke-all-pages.yaml` 同步**:Tab 切换顺序从「images → stats → pull → credentials → proxies → settings」改为「images → pull → stats → credentials → proxies → settings」;`smoke-02-stats` / `smoke-03-pull` 改名 `smoke-02-pull` / `smoke-03-stats`。22 步不变。**22/22 重跑 passed on 0.5.35**(runId `r-20260929013358-5a8c`,7163ms),上轮已验证。

- **版本号同步**(按 AGENTS.md「一次改动要同时更新这几处」):`internal/version/version.go`、`.env.example` 的 `IMAGE`、`docker-compose.yml` 的 `${IMAGE:-…}`、README 「当前状态」+ 「当前版本」、CHANGELOG 顶部节标题 全部 → `0.5.36`。

### 影响范围(升级须知)

- **行为变化**:SQLite DB 文件名 `cairn.db` → `cairn.db`。启动时若 `/app/data/cairn.db` 不存在会直接 `db.Open` 创建 —— **升级前需要把旧的 `cairn.db` 删掉(本仓库目前仍在测试阶段,可直接删),否则 `/app/data/cairn.db` 会变孤儿文件留在那**。
- **数据迁移**:无(用户授权丢数据,可直接清空 `/data/cairn/` 重来)。
- **API 契约不变**;前端行为不变;TS 代码不变(本轮只改 `App.tsx` 顺序)。
- **无新依赖**。

### 验证

- 本机门禁:`tsc --noEmit` RC=0 / `go build ./...` RC=0 / `go test -race ./...` RC=0。
- 158 上重构建 `cairn:0.5.36`,镜像 ID 必须 ≠ `cairn:0.5.35`(`5e5782e374ee`)。
- 启动日志 `db:"true"` 字段确认 SQLite 装载成功;`/api/inventory` 验证 `cairn.db` 实际被读写。
- 14 场景全量回归(为节省时间,只跑可能受影响的子集:`_smoke-all-pages` / `images-page` / `pull-page` / `pull-real` / `stats-page` / `delete-real` / `delete-repo-real` / `gc-real` / `fault-timeout` / `fault-stall-multipage`)。
- 验收报告见 `tests/web-auto/reports/acceptance-0.5.36-2026-09-29.md`。

---

## [0.5.35] - 2026-09-29

本轮主题:**关掉 R-open-2 实际剩余项(`POST /api/gc` 补 `allowDelete` 门控,与 tag / 仓库删除同档)+ 清掉跨版本堆积的 8 个前端类型错误(`tsc --noEmit` 由 RC=2 转 RC=0,可纳入 PR 门禁)**。

### 修复

- **`POST /api/gc` 加 `AllowDelete` 门控**(`internal/api/handlers_extra.go` `RunGC`):

  旧:`RunGC` 是仓库清理 / GC 扫描的总入口,但**不读 `allowDelete`**。后果:`allow.delete=false` 的部署仍可被 GC 删 blob / 删空仓库(默认不开 `cleanEmptyRepos=true` 时不会清空仓库,但 Pass 1 仍会动 blob),与"`DELETE /api/repositories/{repo}` 被 403 拦"自相矛盾。
  新:函数体顶部加与 `DeleteRepository` / `DeleteManifestByDigest` 完全一致的范式:

  ```go
  if !e.allowDelete() {
      writeError(w, r, http.StatusForbidden,
          errors.New("gc is disabled (allow.delete=false)"))
      return
  }
  ```

  R-open-2 至此**完全关闭**:tag 删除 / 仓库删除 / GC 三条破坏性数据面路径全部受 `allowDelete` 门控,UI 改开关即时生效(每请求读值,与既有两条路径一致)。

- **`web/src/api.ts` 补 3 个 `import type`**(`DeleteManifestPayload` / `DeleteRepositoryPayload` / `GCResult`):

  旧:类型已在 `web/src/types.ts` 定义(分别对应 v0.5.0 的删除响应、v0.5.0 的 manifest 删除响应、v0.5.20 的 GC 响应),但 `api.ts` 没导入,`requestSlow<...>` 三处的泛型实参触发 TS2304(3 处)。
  新:加进 `import type { … } from './types'` 的字母序位置。`runGC` / `deleteRepository` / `deleteManifestByDigest` 三函数体不变(均已被 UI 调用;`deleteRepository` 在 v0.5.29 后成死代码的判定见 P3-1)。

- **`web/src/pages/settings-page.tsx` 清 5 个未使用声明** + 同步 `App.tsx`:

  | 类型 | 原 | 处置 |
  | --- | --- | --- |
  | `Descriptions` (antd) | import 后未用 | 删 antd import 中的 `Descriptions` |
  | `formatDateTime` (utils) | import 后未用 | 删 utils import 中的 `formatDateTime` |
  | `inventory` (Props) | 解构后未用 | 从 Props 类型和解构中删;**保留** `onInventoryChange`(确实被 `handleRefresh` 调用) |
  | `savingRegistryUrl` / `setSavingRegistryUrl` | state 声明后未用 | 删 state 行 |

  `App.tsx` 调用侧同步:`<SettingsPage>` 去掉 `inventory={inventory}`,保留 `onInventoryChange={setInventory}`(这是真用的)。`App.tsx` 里 `inventory` 仍传给 `ImagesPage`,`setInventory` 仍同时供两页用 —— 无功能改动。

### 变更

- **版本号同步**(按 AGENTS.md「一次改动要同时更新这几处」):`internal/version/version.go`、`.env.example` 的 `IMAGE`、`docker-compose.yml` 的 `${IMAGE:-…}`、README 「当前状态」+ 「当前版本」、CHANGELOG 顶部节标题 全部 → `0.5.35`。

- **顶部导航 Tab 顺序调整**(`web/src/App.tsx` `NAV_ITEMS` + 渲染三元链):

  旧:`镜像列表` → `镜像热度` → `镜像拉取` → `凭据管理` → `代理管理` → `设置`
  新:`镜像列表` → `镜像拉取` → `镜像热度` → `凭据管理` → `代理管理` → `设置`

  原因:user 反馈「**列表是查看,拉取是发现不够了来操作的,才是热度与管理**」—— 把 `镜像拉取` 上移到 `镜像热度` 之前,符合从「查/操作 → 观测 → 管理」的真实使用路径。`NAV_ITEMS` 与渲染三元链同步调整(否则会出现「点了第 3 个 Tab 渲染第 5 个 Tab」的渲染错位)。**纯展示层改动,无 API / 数据 / 行为变化**,不进位版本号。
- **`internal/api/handlers_extra.go` 增 9 行**:门控 + 文档注释(R-open-2 关闭原因,与 0.5.4 起的运行时开关语义一致)。
- **`web/src/api.ts` 增 3 个 import 名**,函数体零改动。
- **`web/src/pages/settings-page.tsx` 减 5 个未用声明**;`web/src/App.tsx` 减 1 个未用 prop(`inventory`)。

### 影响范围(升级须知)

- **行为变化**:UI 把 `allow.delete` 切到 `false` 时,`POST /api/gc` 也将返回 **403 + `gc is disabled (allow.delete=false)`**(原先静默放行并执行)。所有 GC 入口(镜像列表「运行 GC」按钮、热度页「清理过期热度」后的扫描按钮、若有调度任务触发 GC)均生效。
- **行为不变**:`allow.delete=true`(默认)时,GC 行为完全不变 —— 既有 `gc-real` 场景的 24/24 断言全部继续成立。
- **API 契约不变**:错误响应仅多一种"code 与 message",响应信封不变。
- **前端**:0 类型错误 → 可正式把 `tsc --noEmit` 纳入 PR 门禁(此前一直 RC=2 阻塞)。
- **无数据迁移**:不需要。

### 验证

本轮全量重跑 + 0.5.34 验收报告的全部 14 场景。验收报告见 `tests/web-auto/reports/acceptance-0.5.35-2026-09-29.md`。

---

## [0.5.34] - 2026-09-28

本轮主题:**设置页加「监听端口」只读显示 + README 加改端口指南 —— 解决 user「能不能在页面上改端口」的疑问**。

### 变更

- **后端暴露 cfg.Port**(`internal/api/handlers.go` AppConfig 加 `Port int` 字段 + GetConfig 填值):

  旧:AppConfig 不暴露监听端口,前端无从显示。
  新:加 `Port: h.Cfg.Port`,字段 JSON 名 `port`,前端 `AppConfig` 同步加 `port: number`。

- **设置页加「监听端口」只读字段**(`web/src/pages/settings-page.tsx`):

  ```tsx
  <Form.Item
    label={<span>监听端口</span>}
    extra={
      config?.port
        ? `容器内 cairn 进程监听 ${config.port};宿主机→容器映射在 docker-compose.yml 的 HOST_PORT,改完需要 docker compose up -d 重建容器。`
        : '读取中…'
    }
  >
    <ReadonlyValue value={config?.port ? String(config.port) : '--'} mono />
  </Form.Item>
  ```

  放在「仓库地址」之后、「Registry 认证」之前。**只读** —— 没有 Input / 数字编辑框。

- **README 「配置在哪配」节加改端口指南**:

  解释 Cairn 监听端口 = 宿主机映射端口 (`HOST_PORT`, docker 编排层) + 容器内 cairn 进程监听端口 (`PORT` env → `cfg.Port`, boot 期固定) 两层组合,以及 `.env` 改 `HOST_PORT` + `docker compose up -d` 的步骤。

### 为什么不做「UI 改 cfg.Port」?

讨论过三种方案,最终选**只读 + README 指南**:

| 方案 | 评 |
| --- | --- |
| **A. 只读显示 + README 改法说明(本轮)** | 最简、最诚实;**用户不会误以为改了生效**。 |
| B. UI 改 cfg.Port + graceful restart | 改的是容器内监听端口,**宿主机→容器映射不在 cairn 进程控制下**;浏览器 / docker daemon 还走老端口,改了 = 没改。 |
| C. UI 改 + 引导改 docker-compose.yml | 比 A 复杂,比 B 老实,但操作比直接改 docker-compose.yml 还麻烦。 |

### 影响范围(升级须知)

- **行为变化**:设置页多一个「监听端口」只读字段,显示容器内 cairn 进程当前监听端口(默认 8787)。
- **API 契约变化**:`/api/config` 响应新增 `"port": <number>` 字段。客户端如果 strict 解析 JSON 可能报错 —— 但字段类型稳定,加新字段不破坏既有字段。
- **无回归测试变动**:AppConfig 结构体变动,跟其他 v0.5.x 加字段同理。

---

## [0.5.33] - 2026-09-28

本轮主题:**修复 v0.5.18 起的 docker pull unauthorized bug —— `/v2/` 在需要 auth 时返 200 + WWW-Authenticate header,被 docker daemon 误判为「不需要 auth」**。

### 修复

- **`/v2/` 也走 `requireBasicAuth` middleware**(`internal/registryd/routes.go:90-119` 的 New 路由组装):

  ```diff
   - // /v2/ is the protocol "ping" endpoint. The OCI spec lets it 200 even
   - // when auth is required, so we deliberately do NOT put it behind the
   - // auth middleware -- docker / skopeo rely on a 200 here to detect
   - // "this server speaks V2" before issuing authenticated requests.
   - r.Get("/", h.apiVersion)
   + // v0.5.33: /v2/ 也放进 requireBasicAuth。之前以为 OCI spec 允许 /v2/
   + // 在需要 auth 时也返 200(只挂 WWW-Authenticate header),实测 docker daemon
   + // 看到 200 就**以为不需要 auth**,manifest/blobs 请求**不发 Authorization
   + // header** → server 返 401 → docker daemon 报 "unauthorized"(根本没带
   + // creds,没法 retry)。
   + r.Group(func(r chi.Router) {
   +   if h.getCreds != nil {
   +     r.Use(h.requireBasicAuth)
   +   }
   +   r.Get("/", h.apiVersion)
   +   ...
  ```

- **`apiVersion` 不再主动设 WWW-Authenticate header**:`requireBasicAuth` middleware 在没带/错 credentials 时会自动写 WWW-Authenticate + 401,这里只关心 happy path。

### 根因(158 实测链路)

| 请求序列 | 旧(v0.5.32 及之前) | 新(v0.5.33) |
| --- | --- | --- |
| `GET /v2/` 无 creds | `200 OK` + WWW-Authenticate header | `401` + WWW-Authenticate ✓ |
| `GET /v2/` 带 admin:password | `200 OK` | `200 OK` ✓ |
| `HEAD /v2/<repo>/manifests/<ref>` | `401`(**无 Authorization**) | `200 OK` ✓(用 GET /v2/ 拿到的 creds 重试) |
| `GET /v2/<repo>/manifests/<ref>` | `401`(**无 Authorization**) | `200 OK` ✓ |

**关键链路**:
1. docker daemon 先发匿名 `GET /v2/`(探测 server 协议版本)
2. v0.5.32 server 返 **200 + WWW-Authenticate header** —— docker daemon 看到 200 就**以为不需要 auth**
3. 后续 HEAD/GET manifest 请求**不发 Authorization header**
4. server 返 401(没有 Authorization 就 401,这是 `requireBasicAuth` 的标准行为)
5. docker daemon 报错 `Error response from daemon: unauthorized`(因为**根本没带 creds**,无法 retry)

**与 v0.5.32 HEAD Flush 修复的关系**:v0.5.32 解决了 keep-alive 僵持问题(HEAD 不再 hang),但**没修根因** —— docker daemon 仍然不带 creds,所以 pull 仍然 unauthorized。本次 v0.5.33 才真正修上。

### 影响范围(升级须知)

- **行为变化**:升级后 `GET /v2/` 无凭证返 **401**(之前返 200)。**已经做过这一步的客户端**(docker / skopeo / registry-manager)会自动用 config.json / --creds 里的 creds 重试,所以登录体验不变。**未配凭证的客户端**会收到 401,符合 OCI spec 期望。
- **API 契约变化**:`GET /v2/` 状态码在「无凭证 / 凭证错」时从 200 → 401。**这是破坏性变更**,但符合 OCI spec,且客户端都自动 retry。
- **回归测试**:`go test ./...` 全绿。`requireBasicAuth` 路径行为不变,只是 `/v2/` 也走它。

---

## [0.5.32] - 2026-09-28

本轮主题:**修复配置 Registry 认证后 `docker pull` 报 `unauthorized` 的根因 —— HEAD handler 在 keep-alive 下僵持,客户端超时被 docker 简化成 `unauthorized`**。

### 修复

- **`manifestHead` / `blobHead` 显式 Flush**(`internal/registryd/routes.go:289-296` 和 `:380-384`):

  ```diff
  + // v0.5.32: 显式 flush,避免 HEAD 在 keep-alive 下僵持。
  + if f, ok := w.(http.Flusher); ok {
  +   f.Flush()
  + }
  ```

### 根因(用户实测 + 服务端日志)

| 测试 | 结果 |
| --- | --- |
| `curl GET /v2/` 无凭证 | `200 OK` ✓ |
| `curl -u admin:password GET /v2/` | `200 OK` ✓ |
| `curl -u admin:password GET /v2/_catalog` | `200 OK` ✓ |
| `curl -u admin:password HEAD /v2/registry-manager/manifests/0.5.0` | **headers 200 OK 但 body 不发,timeout 60s** ✗ |
| 同样的 HEAD 加 `-H "Connection: close"` | **351ms 返回** ✓ |
| 服务端日志 `dur_ms` | 44~349ms(handler 已 return,只是 socket 不关) |

### 链路解读

1. **认证**:`requireBasicAuth` 完全正常 —— 带正确 credentials 的 `_catalog` / GET manifest 全部 200 OK。
2. **HEAD 路径**:OCI spec 要求 HEAD manifest 响应里有 `Content-Length` header(让 client 知道 manifest 真实大小)。
3. **Go net/http 的 keep-alive 行为**:ResponseWriter 看到 `Content-Length: 2620` 就等 2620 个字节的 Write 才 finish response。HEAD 请求 handler 没 body 要写,但 **server 不主动 flush**。
4. **僵持**:server 想复用 TCP 连接复用下一个请求,所以等下一个 Write;client(curl / docker daemon)看到 Content-Length: 2620 就等 2620 bytes —— **两边都不动**。
5. **docker 简化错误**:docker daemon 多次重试都 timeout 后,把所有超时类失败简化报成 `unauthorized`(因为 docker login 时 `/v2/` 是 OK 的,逻辑上「token 还在怎么会 unauth」就被忽略了)。
6. **用户感知**:「docker login 成功,但 docker pull 报 unauthorized」 —— 看起来像 credentials 问题,实际上从未涉及认证。

### 影响范围(升级须知)

- **行为变化**:升级后 `HEAD /v2/<repo>/manifests/<ref>` 和 `HEAD /v2/<repo>/blobs/<digest>` 在 keep-alive 下立即返回,**docker pull 走通**(前提还是 daemon 配了 `--insecure-registry=registry.example.com` 或 `daemon.json` 的 `insecure-registries`)。
- **API 契约无变化**:`/v2/` 响应字节完全一致,只是 HEAD 不再 hang。
- **无回归测试变动**:本轮是延迟多年的 keep-alive 兼容 bug,没有 HEAD handler 单测覆盖。

---

## [0.5.31] - 2026-09-28

本轮主题:**回退 v0.5.28 加的「`buildPullCommand` 内部补 `http://` 前缀」—— 当时判断错了,补前缀反而 broke 复制粘贴**。

### 修复

- **`buildPullCommand` 不再补协议前缀**(`web/src/utils.ts:42-50`):

  旧 (v0.5.28):
  ```ts
  const prefix = /^https?:\/\//.test(host) ? '' : 'http://';
  return `docker pull ${prefix}${host}/${repository}:${tag}`;
  ```
  → 操作员复制出去是 `docker pull http://registry.example.com/registry-manager:0.5.0` —— **`docker pull` 不接受 URL 形式**(reference 语法是 `[registry[:port]/]repository[:tag]`,scheme 不在规范里),需要手动剥掉 `http://` 才能跑。

  新 (v0.5.31):
  ```ts
  return `docker pull ${host}/${repository}:${tag}`;
  ```
  → 直接拼裸 `host:port`,跟 v0.5.28 之前的工作流完全一致。

### 为什么当初判断错了

- 我以为「docker pull 拿到裸 host 默认按 https 处理,会失败」 —— 这是真的,但只对**未配 insecure-registry** 的 daemon 成立。
- 内网部署(`registry.example.com`)docker daemon 几乎都配了 `--insecure-registry=registry.example.com` 或 `daemon.json` 里的 `insecure-registries`,这种情况下 docker 会**先按 https 处理,失败再回退到 http** —— 拿到裸 host 直接就能拉。
- 加 `http://` 前缀反而让 reference 解析失败,操作员**不能**贴流程走。

### 影响范围(升级须知)

- **行为变化**:升级后从详情抽屉复制 pull 命令,出来是 `docker pull registry.example.com/<repo>:<tag>` —— 跟 v0.5.27 之前完全一致,**直接粘贴到 docker 客户端能跑**(前提是 daemon 配了 insecure-registry)。
- **API 契约无变化**:`/api/config` 的 `AppConfig.host` 仍是裸 `host:port`(v0.5.28 的字段语义不变)。
- **设置页仓库地址字段语义不变**:仍只填 `host:port`,v0.6.0 的 http/https 切换按钮铺路逻辑不破。
- **无回归测试变动**:`buildPullCommand` 是纯函数,无单测覆盖。

---

## [0.5.30] - 2026-09-28

本轮主题:**修复 v0.5.28 右上角展示名称和 url 挤成一坨的 UI bug —— antd Tooltip 把兄弟 span 视为单一 inline-block,父 flex gap 进不去**。

### 修复

- **右上角 `name` 和 `url` 改成括号拼接形式**(`web/src/App.tsx`):

  旧 (v0.5.28):
  ```tsx
  <Tooltip title={config.url}>
    <span className="app-registry-name ellipsis">{config.name || '镜像仓库'}</span>
    <span className="app-registry-url ellipsis mono" title={config.url}>{config.url}</span>
  </Tooltip>
  ```
  → 渲染成 `Carin Dev 环境http://registry.example.com`,两个 span 之间**没视觉分隔**。

  **根因**:antd Tooltip 默认给包裹元素加 `display: inline-block`,把两个 span 视为单一 inline-block 整体;父容器 `.app-header-meta` 的 `gap: 8px` 是 flex gap,只在**直接子项**之间生效 —— 而 Tooltip 是直接子项,内部的两个 span 不是。所以 8px gap 在内部被吞掉。

  新 (v0.5.30):
  ```tsx
  <Tooltip title={config.url}>
    <span className="app-registry-name ellipsis">
      {config.name || '镜像仓库'}
      {' ('}
      <span className="app-registry-url mono" title={config.url}>{config.url}</span>
      {')'}
    </span>
  </Tooltip>
  ```
  → 渲染成 `Carin Dev 环境 (http://registry.example.com)`,括号天然分隔 + 主 span 整体 ellipsis 行为统一。

- **`.app-registry-url` CSS 收紧**(`web/src/app.css`):
  - `color: var(--color-text-4)` 比父 `.app-header-meta` 的 `text-3` 更弱(降一档)
  - `font-size: 12px` 比父的 `13px` 小一档
  - `font-weight: normal`(父级未设,默认就是 normal,这里显式写出来挡后续 antd 链改继承)

### 影响范围(升级须知)

- **行为变化**:升级后右上角从 `Carin Dev 环境http://registry.example.com` 变成 `Carin Dev 环境 (http://registry.example.com)`(括号 + 括号内 url 字号更小)。
- **API 契约无变化**:纯展示层调整,`/api/config` 响应字节完全一致。
- **无回归测试变动**:CSS 文件没动测试套件。

---

## [0.5.29] - 2026-09-28

本轮主题:**镜像列表的「删除仓库」入口去掉,改走「删 tag → GC」链路**。

### 变更

- **镜像列表操作列收紧**(`web/src/pages/images-page.tsx:230-247`):

  旧:`操作` 列同时挂 `详情` + `删除仓库` 两个入口;删除仓库调 `DELETE /api/repositories/{repo}`,**前端在 `await deleteRepository(...)` 之后无脑 `message.success(...)`**,不检查 `result.success`。后端 `Store.DeleteRepository` 在 v0.5.24 Pass 3 bug fix 之后对命名空间段路径 (`library/alpine`) 是支持的,但**前端无脑 success toast 掩盖了后端真删与否的状态** —— 体感就是「提示删除没用」。

  新:操作列只剩 `详情` 一个入口;列宽 140 → 100;Popconfirm + `deleteRepository` 整套移除。`DeleteOutlined` / `Popconfirm` / `message` imports **不删**(GC 弹窗 `运行 GC` 按钮仍在用)。

  ```diff
  - {config?.allowDelete ? (
  -   <Popconfirm ... onConfirm={async () => {
  -     try {
  -       await deleteRepository(record.name);
  -       message.success(`已删除仓库 ${record.name}`);  // 无脑 success
  -       ...
  -     }
  -   }}>
  -     <Button ... danger icon={<DeleteOutlined />}>删除</Button>
  -   </Popconfirm>
  - ) : null}
  + // v0.5.29 注释:操作员在仓库列表上「一键删整个仓库」不合理。要清掉一个仓库走
  + // 「详情 → 删完所有 tag → 列表 → 运行 GC + 勾「也清理 0 tag 仓库」」。
  + <Button type="link" size="small" onClick={() => setDetailName(record.name)}>详情</Button>
  ```

### 设计意图

- 一键删仓库是**危险操作**(即便有 Popconfirm 也防不住误操作),且**没用**:删 manifest 只是解除引用,磁盘空间要 GC 才回收。
- 让操作员走「删 tag → GC」链路的好处:
  1. 每个 tag 的删除是独立动作,误删一个不影响其他
  2. GC 是显式动作,操作员**自己决定**何时清理磁盘
  3. `运行 GC` 弹窗的「也清理 0 tag 仓库」勾选框(v0.5.20)是**批量**清空仓库的合法入口,自动 / 手动都覆盖到了

### 保守保留 / 待后续清理

- **`DELETE /api/repositories/{repo}` HTTP 端点保留**(`internal/api/handlers_extra.go:588`):本轮只去 UI 入口,后端 API 不动 —— 防止破坏外部手动 curl / 调试场景;`Storage.DeleteRepository` 也保留(GC Pass 3 还要用)。
- **前端 `web/src/api.ts:225-229` 的 `deleteRepository` export 保留**:无人调用,但删 export 是一次性破坏,留到下一轮统一清理。
- 跟踪项:如果确认不需要外部 API 调用,后续一轮统一删 `DeleteRepository` handler + `deleteRepository` export + `DeleteRepositoryPayload` in `types.ts`。

### 影响范围(升级须知)

- **行为变化**:升级后镜像列表操作列只剩 `详情`,**仓库层级的删除按钮彻底消失**(包含 `allowDelete=false` 时的灰态占位)。
- **API 契约无变化**:`DELETE /api/repositories/{repo}` 端点仍能调通,只是 UI 不再暴露。
- **无回归测试变动**:删除链路本来就没测试覆盖(单元测试重点在 GC Pass 1..4)。

---

## [0.5.28] - 2026-09-28

本轮主题:**为 v0.6.0「http/https 切换」做准备 —— 仓库地址字段语义改成「裸 host:port」,协议字段拆出来单独管理;展示名称上右上角,不再被 host 淹没**。

### 变更

- **仓库地址字段语义调整**(`internal/api/handlers.go` `MutableFieldType["url"]` 校验 + `GetConfig` 的 `displayURL` 拼接):

  | 旧 (≤v0.5.27) | 新 (v0.5.28) |
  | --- | --- |
  | 字段存 `http://registry.example.com:8787`(带协议) | 字段存 `registry.example.com:8787`(裸 host:port) |
  | 校验:必须 `http://` 或 `https://` 开头 | 校验:必须是合法 host 或 host:port,端口 1..65535,**不允许协议前缀** |
  | 渲染 URL:直接用字段值 | 渲染 URL:`"http://" + 字段值`(协议在服务端补) |

  协议现在写死 http。**为 v0.6.0「http/https 切换按钮」铺路**:届时这块从硬编码改成读 https toggle 状态,字段本身不动。

- **`isValidHostPort()` 校验 helper**(`internal/api/handlers.go`):
  - 正则 `^[a-zA-Z0-9](?:[a-zA-Z0-9._-]*[a-zA-Z0-9])?(?::\d{1,5})?$`
  - 端口合法区间 1..65535
  - 拒绝 `http://` / `https://` 前缀,错误信息直说「不要带协议前缀,只填 host:port」

- **设置页 UI 改造**(`web/src/pages/settings-page.tsx`):
  - Input 加 `addonBefore={<Tag color="cyan">http://</Tag>}`,让用户一眼看到当前协议 = http,**v0.6.0 改 Select 即可**
  - placeholder 改成 `registry.example.com:8787`,跟示例对齐
  - extra 文案:去掉 `http://...` / `https://...` 例子,改成裸 host 例子,并加一句「协议 = http(写在前面那个 badge),**这里只填 host:port**」
  - 只读视图也补上 `http://` 前缀,避免用户看到「`http://registry.example.com:8787`」却找不到它从哪儿配出来

- **一次性数据迁移**(前端 `stripUrlProtocol()` helper):
  - 用户已有的 `http://...` 值加载到编辑态时,**自动剥掉协议前缀**显示
  - 提交后存进 SQLite 的就是新格式
  - 老用户首次升级后,进设置页保存一次即完成迁移,无需手工改

- **右上角主显示改为「展示名称」**(`web/src/App.tsx`):
  - 旧:`{config.url}` 单行(展示名称填了没人看到,等于没填)
  - 新:主行 `config.name`(展示名称);副行 `config.url` 灰色小字号(保留 host 入口可见性,避免操作员失去「当前连哪个 registry」的直觉)
  - 鼠标 hover 任意位置看 tooltip 都是 `config.url` 全文
  - 用户原话「展示名称要在右上角显示出来,不然配置就没有用」—— 本轮兑现

- **`buildPullCommand` 内部补协议**(`web/src/utils.ts`):
  - 旧:接收的 `config.host` 是带协议的完整 URL,直接拼 `docker pull ${host}/...`
  - 新:`config.host` 是裸 host:port(从 `hostOf(displayURL)` 抽出 host 部分,不含协议);`buildPullCommand` 内部补 `http://` 前缀
  - 否则 docker pull 拿到裸 host 默认按 https 处理,会失败

### 影响范围(升级须知)

- **存量的 `http://...` 值**:升级后**首次进设置页保存一次**即完成迁移(前端自动 strip,后端按新格式校验)。在此之前显示还是带协议(右上也还是裸 host:port 拼出来的 URL,跟之前一致)。
- **API 契约无破坏**:`/api/config` 的 `mutable.registryUrl` 字段类型未变(string),只是值的语义变了。客户端调用方需要同步去掉 `^https?:\/\/` 才能拼回原值。
- **`buildPullCommand` 兼容性**:接收带或不带协议的 host 都行,内部统一补 `http://`。
- **行为变化**:
  - 设置页仓库地址 Input 前面多一个 `http://` badge(只读视图前缀也跟着补)
  - 右上角现在显示「展示名称」作为主标题,URL 退到副行 + tooltip
  - docker pull 命令现在带 `http://` 前缀(之前是裸 URL,行为其实是 docker 默认按 https 处理;补 http 后语义明确)
- **回归测试**:本轮纯字段语义调整,没引入新单测。

---

## [0.5.27] - 2026-09-28

本轮主题:**修复删除 tag 成功后弹空 Alert 的 UI bug —— 后端 `writeJSON` 注入的 message 是空串,前端直接把空字符串渲进 AntD Alert**。

### 修复

- **删除 tag 改用顶部 toast**(`web/src/components/image-detail-drawer.tsx:78-99` 的 `handleDelete`):

  旧实现:成功也 `setNotice(result)`,drawer 顶部 Alert 的 `message={notice.message}` 直接拿后端注入的空串渲染。AntD Alert 的 message 为空时不会报错,只是渲成「只有对勾图标 + 关闭按钮的空白绿条」,让人误以为系统在发什么公告。

  新实现:成功走 `message.success(\`已删除 tag ${record.tag}\`)` 顶部 toast(沿用 `images-page.tsx:250` 删除仓库的模式,3 秒自动消失),**根本不进** `setNotice`。

- **drawer 顶部 Alert 收紧为「错误反馈专用」**(同文件 L202-218):

  - 加 `!notice.success` 守卫:成功路径完全不画 Alert(代码原本有,但因为当时 setNotice(result) 把成功响应也塞进去,渲染分支被走了)。
  - description 从 `affectedTags.join('、')`(成功路径下读 sibling tags)换成 `错误分类: ${code}` —— 后端拒绝时给用户看 code 是有价值的事实;成功时根本不会进这条分支。
  - type 从 `'success' | 'warning'` 收窄到 `'warning'`,编译期就锁死「这里只画错误」。

### 影响范围(升级须知)

- **行为变化**:升级后删除单个 tag,drawer 顶部不再出现绿色空 Alert,改在页面右上角弹「已删除 tag X」toast(3 秒自动消失)。失败(allowDelete 关闭、tag 不存在、manifest 删除错误等)依旧在 drawer 顶部画橙 Alert + 错误分类。
- **API 契约无变化**:后端 `DELETE /api/tags` 响应字节完全一致,只调整前端渲染。
- **无回归测试变动**:`handleDelete` 是组件内回调,后端逻辑零改动。

---

## [0.5.26] - 2026-09-28

本轮主题:**热度页面去掉「还没收到任何热度事件」空态提示 —— Cairn 只管自带 registry 的热度,不引导用户排查外部 registry**。

### 变更

- **`statsNotice()` 返回类型收窄**(`web/src/pages/stats-page.tsx:813`):
  - 旧:`{type, message, description}` 三种形态(DB 报错 / `allowRegistryEvents=false` / 一切就绪但窗口内没事件 —— 后者返回 `info` 提示 + 长篇延伸「是不是外部 registry 没配好?`REGISTRY_NOTIFY_TOKEN`?Harbor 怎么打开 notifications?」)
  - 新:`{type: 'warning', ...} | null` —— DB 错 / `allowRegistryEvents=false` 才返回 warning,其他场景返回 `null`
  - 写明的设计意图:**Cairn 0.5.23 起定位改成「自带 registry 的镜像基础设施平台」**,事件=0 只表示当前窗口没 pulls,**不再代表操作员配错了什么**;插一条「你是不是没配 REGISTRY_NOTIFY_TOKEN」反而会让人误以为系统没在干活。

- **`empty` 分支彻底不画任何提示**(原行 ~620-647 一整段 `<Alert>` + `<Collapse>` 嵌入 `NotifyConfigSnippet` 已删除):
  - KPI 卡片自带的「事件总数 0 / 拉取次数 0」已经是准确表达
  - 真要排查走下方「最近事件」面板(不受 200 条窗口限制、重启也不丢)与 KPI 自检
  - 当前分支留一行注释说明 v0.5.26 移除原因,免得有人回看 git blame 误以为是漏改

- **`!config.statsEnabled` 分支加 `notice ?` 守卫**(`web/src/pages/stats-page.tsx:553-566`):
  - 旧实现:`statsEnabled=false` 时**无脑**展示 alert + `NotifyConfigSnippet`(外部 registry 配置片段)
  - 新实现:仅当 DB 报错或 `allowRegistryEvents=false` 时才展示告警 + 配置片段;其它情况让页面空白显示 metric 0
  - 与「当且仅当 DB 报错时显示告警 + 配置片段」保持一致

### 保守保留 / 待后续清理

- **`NotifyConfigSnippet` 组件**(整段保留,未删除):本轮只针对用户要求的「没事件」提示做最小改动。`NotifyConfigSnippet`(附 YAML 配置片段 + 「Distribution 没有热重载,改完必须重启 registry」提醒)目前**仍**在 `!config.statsEnabled` 分支被 `notice ?` 守卫渲染。**Cairn 不接外部 registry,这部分组件理论上应该整体下线**,但用户没明确要求删,留到下一轮单独清理。
  - 跟踪项:打开 `web/src/pages/stats-page.tsx`,搜 `NotifyConfigSnippet`,整段应该删掉(连同 `NOTIFY_CONFIG_YAML` 常量、`TextCopyButton` helper 若不再被引用)。
  - 跟踪项:`internal/server/api.go` 的 `notify.token` / `AllowRegistryEvents()` 配置面也一并评估**(本轮不动后端,只删前端)**。

### 影响范围(升级须知)

- **后端零变化**:本轮纯前端 UI 删除,`/api/stats/*` 响应字节、`stats.db` schema 全部不变。
- **行为变化**:升级后即使从来没收到任何事件,「热度」页面也不会再弹提示;打开就是干净的 KPI + Top 榜单(空) + 趋势图 + 最近事件。
- **无回归测试变动**:`statsNotice()` 是纯展示函数,不引入新单测。

---

## [0.5.25] - 2026-09-28

本轮主题:**修「拉取平台过滤不生效」的 UX bug —— 后端其实过滤对了,但 tag 仍指向原始 multi-arch INDEX,UI 因此显示"+13"**。

### 修复

- **tag 指向过滤后的 root**(`internal/pull/executor.go:296` 的 `PutManifest`):

  旧实现 (v0.5.0 ~ v0.5.24) 始终把**原始 source manifest**(`srcManifest.Raw`)写成 destTag:
  ```go
  written, err := o.Dest.PutManifest(ctx, destRepo, destTag, plan.rootMediaType, srcManifest.Raw)
  ```

  对 multi-arch 镜像,这意味着 `tags/3.19` 指向的仍是上游 index(14 个 child 引用)—— 即使本地**只拉了 amd64 的 child + layer**。158 现场复现:磁盘上 `manifests/sha256/<amd64-child>` 与 `blobs/` 都只有 amd64 的字节,但 UI 架构列显示 `linux/amd64 +13`,误导操作员以为过滤没生效。

  新实现按过滤结果分支:

  | 过滤匹配数 | tag 写入 |
  | --- | --- |
  | 0(没配过滤) | 原 INDEX 不变 —— multi-arch 保留 |
  | 1(`linux/amd64`) | 该平台的 child manifest —— single-arch,UI `+0` |
  | ≥2(`linux/amd64,linux/arm64`) | **合成的 filtered index**(只含被选的 child)—— multi-arch 但限定到操作员选的平台 |

  三种 case 都匹配 Docker CLI `docker pull --platform` 的行为语义。

- **`synthesizeFilteredIndex` helper**(`internal/pull/executor.go`):
  - 解析原始 INDEX 为 generic map,**保留所有字段**(annotations / mediaType / platform / size)→ Docker attestation 通过 `vnd.docker.reference.digest` annotation 链 SBOM/signature,**必须保留**否则断链
  - 按 keep 列表过滤 child 条目
  - 重新 marshal,得到 SHA256 不同的新 INDEX;此 digest 作为 destTag 的目标
  - 现有 GC 不会把它当孤儿,因为 tag 还指向它

### 影响范围(升级须知)

⚠️ **已存在的 tag 不会自动修复**:
  - 0.5.24 及之前拉的镜像,`tags/<X>` 指向的还是原始 INDEX digest。**重新 pull 一次**才会被新逻辑覆盖。删除重建也行:`DELETE /api/repositories/<repo>` 再 `POST /api/pull/jobs`。
  - 或者保留旧 tag,但要知道 UI 上看到的 `+N` 是源 INDEX 的 multi-arch 计数,**不是本地实际拉的内容**。

- **对 storage 层零影响**:Pass 1/2/3/4 GC 都没动;blob manifest 的引用关系也没动。
- **API 契约无变化**:`POST /api/pull/jobs` 的 body / 响应字节完全一致。
- **回归测试**:本轮新增 2 个单测:
  - `TestSynthesizeFilteredIndexKeepsOnlyFiltered` —— 验证只保留过滤后的 child,且 mediaType / schemaVersion 不丢
  - `TestSynthesizeFilteredIndexPreservesAnnotations` —— 验证 attestation 的 `vnd.docker.reference.digest` annotation 在合成 INDEX 里仍存在(Docker 的 SBOM/signature 链路依赖)
  - 既有 `TestPlanTransferIndexFiltering` / `TestPlanTransferEmptyAllowListMatchesAll` / `TestPlanTransferNoMatchErrors` / `TestPlanTransferSingleArchManifestUnaffected` 全部通过 —— 本轮没改 planTransfer 的过滤逻辑

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**(本轮**改动文件零新增**,前端无变化)
- **Go 侧门禁**:`gofmt -l internal/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build ./internal/... ./cmd/...` 退出码 0;`go test ./internal/... -count=1` 全 pass(includes 2 new tests)

### 生产现场复测(在 158 跑)

| 检查点 | 步骤 | 预期 |
| --- | --- | --- |
| **A. 之前已拉的多 arch** | 镜像列表点开 `library/alpine:3.19`(0.5.24 拉的) | 「架构」列仍是 `linux/amd64 +13`(旧 tag 指向原始 INDEX,新逻辑没重写已有 tag) |
| **B. 新拉的 amd64** | 「镜像拉取」重新 pull `library/alpine:3.19` → 等完成 | 「架构」列变 `linux/amd64`(无 `+N`,single-arch) |
| **C. 多平台过滤** | 设置里把拉取平台改为 `linux/amd64,linux/arm64` → 重新 pull `library/alpine:3.19` | 「架构」列显示 `linux/amd64 +1`(合成的 filtered INDEX,只有 2 个 child) |
| **D. 字节数** | pull 前后对比 `du -sb /data/cairn/registry` | B 比 0.5.24 拉的同一镜像**小** —— 因为不再有 attests 引用源的 14 个 child manifests,只保留过滤后的子集 |
| **E. attestation 链路** | `docker pull alpine:3.19` 看 SBOM/signature 是否仍可拉到 | 仍可拉(`vnd.docker.reference.digest` annotation 在合成 INDEX 里没丢) |

### 轮次与号位

- 本轮占 **0.5.25**:**defect fix**(UI 与磁盘不一致),按 `AGENTS.md` 判定为**小版本(第 3 位)+1**(既有 pull 流程的语义补正,不引入新功能模块)。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.25 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.24] - 2026-09-28

本轮主题:**Critical bug:勾选 GC「也清理 0 tag 仓库」会**递归删除整条命名空间** —— 凡是命名空间目录下挂着子仓库的,子仓库无论有无 tag 都会被一并清掉**。**已现场确认一次事故:158 上 4 个仓库(其中 2 个有 tag)被一次 GC 清零**,磁盘上 `repos/` 直接空了。**这是上线以来最严重的数据丢失 bug**。

### 修复(CRITICAL)

- **GC Pass 3 命名空间遍历 bug**(`internal/storage/filesystem.go`):

  旧实现:
  ```go
  repoEntries, _ := os.ReadDir(reposRoot)   // 取 repos/ 直接子项
  for _, entry := range repoEntries {
      repo := entry.Name()                  // "library", "webauto-push" ...
      repoDir := filepath.Join(reposRoot, repo)
      if !repoHasNoTags(repoDir) { ... }    // 检查 repos/library/tags/
      os.RemoveAll(repoDir)                 // 删 repos/library/
  }
  ```

  旧代码每一行都有问题:

  | 行 | 错在哪 | 后果 |
  | --- | --- | --- |
  | `os.ReadDir(reposRoot)` | 只取顶层,**没有递归找真仓库** | `library` 当成 repo 名字 |
  | `repo := entry.Name()` | 取的是命名空间段,不是仓库全名 | 缺 `/alpine` |
  | `repoHasNoTags(repos/library)` | 检查的路径根本不存在 tags/ | ENOENT → 当成「无 tag」 |
  | `os.RemoveAll(repos/library)` | 整条目录树递归删 | `library/alpine`(有 1 个 tag)一并清掉 |

  新实现:抽出 `discoverRepos()` helper,与 `Repositories()` 共享同一套 walk 规则 —— 递归遍历 `repos/`,找 `tags/` 或 `manifests/` 的父目录作为仓库,名字带完整命名空间前缀(如 `library/alpine`)。Pass 3 沿用同一个 helper,**只有叶子仓库被删**,命名空间目录里其他有 tag 的仓库不受影响。

- **根因 / 为什么这条 bug 走到生产**:
  - 0.5.20 引入 Pass 3 时**没复用列表接口的发现逻辑**,自己写了一段独立的 `os.ReadDir`,把 namespace 当成 repo。
  - 既有测试 / 场景只覆盖了 Pass 1 + Pass 2(`gc-real.yaml`),Pass 3 在 0.5.20 上线时**没有任何自动化场景**(CHANGELOG 0.5.20 明确写「未补 web-auto 场景,留待后续」)。
  - 158 上恰好有 `library/alpine` / `library/busybox` 这种**多段仓库名**;测试仓里全是单段名(`busybox`、`alpine`),触发不了 bug。
  - 类型层 / `gofmt` / `go vet` / `go build` / `tsc` 全过 —— bug 在逻辑层,静态检查拦不住。

- **修复后的 hard gate**:Pass 3 现在跟 `Repositories()` 共享**同一份 walk 代码**,两边永远看到同一个仓库集合 —— 这条 bug 类不可能再分叉回归。

### 变更

- **GC tooltip UX**(`web/src/pages/images-page.tsx`):长 GC 解释从「? 图标的 Tooltip」(右侧、placement=top、文本被右边裁切)搬到「运行 GC 按钮的 Tooltip」(placement=bottomLeft,出现在按钮**下方偏左**,避开按钮所在页面右上角的右边缘);Popconfirm 的 description 只留可交互的 checkbox(破坏性开关);`?` 图标 + `QuestionCircleOutlined` 整个删除(冗余 affordance)。tooltip 文案不变。

### 影响范围(升级须知)

⚠️ **数据丢失警告**(如果你跑过 0.5.20 ~ 0.5.23 的「也清理 0 tag 仓库」):

  - 0.5.20 ~ 0.5.23 任何一次该勾选都**可能误删**所有挂在命名空间下的仓库。如果你在线上跑过 0.5.20+ 的这个开关,**立刻拉 0.5.24 升级,但被删的镜像需要从上游 registry 重新拉回来才能恢复** —— 这是物理删除,没有回收站。
  - 升级本身不会撤销已发生的删除。**不要在升级前再点 GC**,直接 pull + build + up,先把这个 bug 关掉。

- **本轮 API 行为变化**:
  - `POST /api/gc` 的 body / 响应**字节兼容** 0.5.23。
  - 行为变化:Pass 3 现在**只删真仓库,不再误删命名空间目录**。空仓库检查的逻辑没动(仍是「tags/ 为空 + 无 24h 内 upload session」)。

- **未触动项(明确划线)**:
  - **零兼容策略,no deprecation period**:用户用 0.5.20~0.5.23 已经点了那个勾选就是数据丢失,**0.5.24 修了之后这条路径仍然是「破坏性」**(勾上 = 删 0 tag 仓库,不可恢复),这是设计,不是 bug。
  - **Pass 1/2/4 逻辑未动**:仅 Pass 3 的「发现仓库」从「读顶层」改成「walk 全树」,隔离。
  - **`web-auto` 场景仍缺**:CHANGELOG 0.5.20 标注「未补场景」、本轮仍是「未补场景」。强烈建议下一个 PR 起一个 `gc-empty-repos-real.yaml`,dev 上造一个 `library/<repo>` 形态的真仓库跑 Pass 3,断言**只删 0 tag 那个**,**有 tag 的兄弟仓库必须留下来**。这条场景写出来,这条 bug 类以后不会再有。

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有,本轮**改动文件零新增**。
- **Go 侧门禁**:`gofmt -l internal/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build ./internal/... ./cmd/...` 退出码 0。
- **逻辑一致性**:`Repositories()` 和 Pass 3 都通过 `discoverRepos()` 拿仓库列表 —— 同一份代码,不可能分叉。

### 轮次与号位

- 本轮占 **0.5.24**:**critical bug fix**(数据丢失),按 `AGENTS.md` 判定为**小版本(第 3 位)+1**。需要主版本(0.x → 1.x 切换)的场景是「API 路径大改 / 移除功能 / 数据格式不兼容」,这条 bug 只是修复了一个回归,不动主版本。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.24 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.23] - 2026-09-28

本轮主题:**部署命名与产品名对齐 —— env 变量、image tag、container_name、service name 全部从 `cairn` 改成 `cairn`**。0.5.21 改了产品面对用户时的名字,但运维侧的命名(`.env` 里的 env 变量、`docker compose` 里的 image / container / service)还是旧名,运维脚本和脑内记忆仍要切换两套 —— 这一轮把部署命名也跟上,做到「产品名 = env 名 = image 名 = container 名」完全一致。

### 变更

- **env 变量**:`GO_HUB_ENV` → `CAIRN_ENV`(`internal/config/config.go` 的 `Load()`,`docker-compose.yml` + `.env.example` 同步)。**无兼容期** —— 旧名不再被读,直接走 `prod` 默认值;若需要 dev 模式,必须改 `.env`。
- **docker image tag**:`cairn:0.5.22` → `cairn:0.5.23`(`docker-compose.yml` + `.env.example` 的 `IMAGE` 默认值)。**注意**:这只是 tag 字符串变了,不影响镜像内容本身;老镜像 tag 仍可继续跑(只是没人再推它)。
- **container_name + service_name**:`cairn` → `cairn`(`docker-compose.yml`)。container_name 改了,所有按 `name` 引用容器的地方(运维 `docker logs cairn` / `docker exec cairn` / `docker compose logs cairn` 等)也要改字面;service_name 跟 container_name 必须一致,否则 compose 报冲突。
- **`UserAgent` 保留 `cairn/` 前缀**:`internal/version/version.go` 的 `UserAgent = "cairn/" + Version` **不改**。理由:对外 registry 看到的 user-agent 是允许列表 / 日志关联的关键字符串,`cairn` 是大家能搜得到的项目代号(中文圈 / GitHub),改成 `cairn/` 会让上游 registry 的 allowlist 与日志关联断链。**外部可识别性优先于内部品牌一致** —— 这条决策是「运维契约」不动。
- **宿主机数据目录**:`HOST_DATA_DIR` 默认仍是 `/data/cairn`,**故意没改成 `/data/cairn`**。理由:它是宿主机上的物理路径,不是产品名的一部分;改名会让老运维脚本(尤其是迁移 / 备份 cron)找不到数据。**数据目录名跟产品名解耦**。
- **AGENTS.md / README.md / ROADMAP.md 同步**:三处文档里凡是引用旧名的地方都加注释说明「曾用名, v0.5.23 改名」;ROADMAP.md 顶部「为什么砍 14 个 env」那段历史说明里,在两个引用旁加了一句 `(v0.5.23 把 GO_HUB_ENV 改名为 CAIRN_ENV)`。

### 影响范围(升级须知)

⚠️ **这是部署面 breaking change,不是 API breaking**。HTTP API、数据格式、磁盘布局(数据目录结构)、SQLite schema 全部不变;**只有 env 变量名 + docker 命名变了**,需要按下面的清单改 `.env`。

#### 升级操作(在 158 上)

```bash
# 1. 编辑 .env,把两行改名
sed -i 's/^GO_HUB_ENV=/CAIRN_ENV=/' .env
sed -i 's|^IMAGE=cairn:|IMAGE=cairn:|' .env

# 2. 拉新代码 + 重新 build + up
git pull origin main
docker compose build --no-cache
docker compose up -d
```

不执行这两步的话:
- 旧的 `.env` 里有 `GO_HUB_ENV=dev` 但代码读的是 `CAIRN_ENV`,**dev 模式静默丢失**(回落到 `prod`)
- 旧的 `.env` 里有 `IMAGE=cairn:0.5.22` 但 compose 找不到这个 image,`docker compose up` 会报 pull 失败

#### 别名映射

| 旧(0.5.22 及以前) | 新(0.5.23) | 影响 |
| --- | --- | --- |
| `GO_HUB_ENV=dev` / `prod` | `CAIRN_ENV=dev` / `prod` | dev 模式需重设 |
| `IMAGE=cairn:0.5.22` | `IMAGE=cairn:0.5.23` | image 名需在 local 重 tag 或 pull |
| `container_name: cairn` | `container_name: cairn` | 容器引用脚本需改字面 |
| service 名 `cairn:` | service 名 `cairn:` | compose 命令行需改字面 |
| `User-Agent: cairn/<v>` | (不变) | 上游 registry 无影响 |
| 宿主机数据目录 `/data/cairn` | (不变) | 备份/迁移脚本无需改 |
| `/api/version` 返回 `version` 字段 | `version: 0.5.23`(字段值变化,字段名不变) | 监控/告警脚本无需改 |

#### 兼容性选择(明确划线)

- **不引入兼容期**(`GO_HUB_ENV` 不再被代码读):若兼容两套名,代码里要保留 `os.Getenv("GO_HUB_ENV")` 的退化路径,这条路径永远没人走,但每行读 env 的地方都要加分支,半年后没人记得为什么两套都在,**比硬切更糟**。直接硬切,这一轮 CHANGELOG 写清楚就够。
- **宿主机目录路径不改**(`/data/cairn` 仍是默认):见上文理由。
- **`UserAgent` 前缀不改**:见上文理由。
- **`cairn` 项目代号仍保留**(module path / 二进制名 / 内部代号):AGENTS.md 顶部已有说明,本轮不动。

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有,本轮**改动文件零新增**。
- **Go 侧门禁**:`gofmt -l internal/ cmd/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build ./internal/... ./cmd/...` 退出码 0。
- **环境变量对照**(`internal/config/config.go` 与 `docker-compose.yml` / `.env.example` 三处必须字面一致):
  - env 变量名:`CAIRN_ENV`(三处一致)
  - 容器内读不到旧名(`GO_HUB_ENV`),会回落到默认值 `prod`,日志显式可见
- **生产现场复测待办**:158 容器升级到 `cairn:0.5.23` 后,
  - `docker ps \| grep cairn` 应看到新 container name
  - `docker logs cairn \| grep "config loaded"` 应见 `version:0.5.23`
  - `/api/config` 返回 `"version":"0.5.23"` 与 `"userAgent":"cairn/0.5.23"`(后者不变)
  - 上游 registry(任何公网 registry)的访问日志里,user-agent 仍是 `cairn/0.5.23`(证明 UserAgent 没改)

### 轮次与号位

- 本轮占 **0.5.23**:部署命名的品牌对齐,**不做兼容期**是一次性断刀。按 `AGENTS.md` 判定为**小版本(第 3 位)+1**(既有部署流程的命名清理,不引入新功能模块)。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.23 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.22] - 2026-09-28

本轮主题:**整体配色与 Cairn Logo 对齐 —— 主色从蓝换 Teal 600,info 与 primary 同色**。0.5.21 把产品名 + Logo 改了,但应用界面仍是蓝主色 —— 这一轮把 UI 主品牌色也跟上,做到「Logo / 顶栏 / 链接 / 按钮 / 选中态」一个色。

### 变更

- **`web/src/theme.css` 浅色版主色**:`--color-primary` 从 `#155aef` 换成 Teal 600 `#0d9488`(Cairn mark 塔身色);`--color-primary-bg-active` 从 `#e1edfc`(浅蓝)换成 Teal 100 `#ccfbf1`(浅 teal);`--color-info` 从 `#1677ff` 换成 `#0d9488`(与 primary 同色 —— info 提示与主品牌色合一,避免 Harbor / Quay / Docker 那种「整屏蓝」的同质化);`--color-side-nav-text-active` / `--color-side-nav-active-bg` 同步。
- **`web/src/theme.css` 深色版主色**:`--color-primary` 从提亮蓝 `#4c8dff` 换成 Teal 400 `#2dd4bf`(色阶感跟原「蓝 → 亮蓝」一致);深色下 `--color-primary-foreground` 从 `#0b1220`(深蓝)换成 `#042f2e`(深 teal,与 teal-600 同一色族的更深档,保证按钮文字对比度);`--color-primary-bg-active` / `--color-info` / `--color-side-nav-*` 同步。
- **`web/src/main.tsx` 的 ConfigProvider LIGHT_TOKENS / DARK_TOKENS** 同步替换成对应的 teal 值。token 与 theme.css 必须**字节对应**,否则会出现「自定义 CSS 是 teal,但 antd 表格 / 弹窗 / 下拉还是蓝」的「半 teal」翻车。
- **品牌资产文件归位**:`tmps/cairn-brand.html` → `docs/cairn-brand.html`。`tmps/` 是 gitignored 临时目录,品牌资产是产品文档的一部分,放在 `docs/` 里随仓库分发,且 README 顶部新增「品牌资产」小节给出链接。
- **README「品牌资产」段**:指向 `docs/cairn-brand.html` + 引用 `cairn-mark.tsx` + `theme.css`,便于后续接手的同学顺着链接找到全部资产。

### 影响范围(升级须知)

- **视觉变化**:链接、按钮、Tab 选中态、Tag(蓝色预设)、表格选中行、Drawer 头部、Alert info / success 等等所有用 `--color-primary` 的地方都会从蓝变 teal。语义色 `success / warning / fail` 维持绿 / 黄 / 红(已建立的视觉契约,改色会误导)。文字色、边框、背景中性色完全不动。
- **API / 数据 / 配置**:零变化。
- **向后兼容**:`theme.css` token 名没改;ConfigProvider 的 token 名也没改;只是具体色值变了。组件层调用方式零改动。
- **未触动项(明确划线)**:
  - **Amber `#f59e0b` 当前只用于 Logo 标记点,未引入 UI 强调色**。考虑过用 amber 替代 warning 的 `#faad14`,但改 warning 风险(用户对红黄之争早已稳定)大于收益,留待单独 PR。
  - **未触动 `web/package.json` 的 `name: "cairn-web"`**:同 0.5.21。
  - **`internal/webui/dist/` 仍是 gitignored**,由 docker build 的 pnpm build 自动重生成。

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有,本轮 `theme.css` / `main.tsx` 改动相关行**零新增**。
- **Go 侧门禁**:`gofmt -l internal/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build ./internal/... ./cmd/...` 退出码 0。
- **色值对照**(`theme.css` 与 `main.tsx` 两边必须同值,避免「半 teal」):
  - 浅色 primary / info:`#0d9488` / `#0d9488`
  - 深色 primary / info:`#2dd4bf` / `#2dd4bf`
  - 浅色 primary-bg-active:`#ccfbf1`
  - 深色 primary-bg-active:`#2dd4bf26`
- **生产现场复测待办**:158 容器升级到 `cairn:0.5.22` 后,
  - 浏览器硬刷 → 顶栏左侧 Cairn mark 仍在,但链接、按钮、Tab 选中态、Tag 颜色从蓝变 teal
  - 切深色主题(右上角)→ 深色底上 teal 更亮(`#2dd4bf`),选中态从「亮蓝底」变成「teal 半透明底」;Cairn mark 自动反白为 teal-300
  - 警告语调 `success / warning / fail` 三色维持不变(绿 / 黄 / 红)

### 轮次与号位

- 本轮占 **0.5.22**:既有 UI 的品牌色同步优化 + 一个文档归位,按 `AGENTS.md` 判定为**小版本(第 3 位)+1**(既有 UI 的调色,不引入新功能模块)。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.22 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.21] - 2026-09-28

本轮主题：**品牌升级 —— 产品名从「镜像仓库管理」改为 Cairn,顶部 brand 替换为新 Logo**。`cairn` 仍是项目 module path / 二进制名 / 内部代号,跟对外产品名 Cairn 并存。

### 变更

- **产品面对用户时改名 Cairn**(Lightweight Container Image Infrastructure / 轻量级容器镜像基础设施平台)。原因:`cairn` 起名时参照 registry-manager 的「Go-Hub」思路,现在产品已经超越 registry-manager 的「给 registry 配个管理 UI」定位,变成「集成仓库、扫描、拉取队列、热度统计的完整基础设施」,旧名承载不了。
- **新 Logo `CairnMark` 组件**(`web/src/components/cairn-mark.tsx`):3 块圆角矩形堆叠成塔身 + 顶部琥珀色标记点。**三块石头**对应 cairn 三个能力面 —— 存储(基座)/ 拉取(中层)/ UI(顶层);**琥珀点**是「目标服务器位置标记」,也是导航隐喻。SVG 内联无外部依赖,favicon / app nav / hero 都可直接复用。提供 `size` / `variant`(light / dark)两个 props。
- **顶部 brand 区替换**(`web/src/App.tsx`):从 `<DockerOutlined /> + 镜像仓库管理` 换成 `<CairnMark size={26} /> + Cairn`。NAV_ITEMS 里的 `DockerOutlined` 是「镜像列表」菜单的 icon,不是品牌 mark,**不动** —— 跟新 mark 各司其职。
- **`<title>` 改为 `Cairn`**(`web/index.html`):浏览器 tab 上看到的标题。

### 影响范围(升级须知)

- **品牌资产统一**:`web/index.html`、`web/src/App.tsx`、`web/src/components/cairn-mark.tsx`(新增)、`README.md` 当前状态行 + 顶部描述、`AGENTS.md` 顶部「这是什么」段 —— 5 处产品名同步更新。`internal/webui/dist/index.html` 由 `pnpm build` 重新生成,不手改。
- **`cairn` 仍是 module path / 二进制名**:Docker image tag 仍是 `cairn:X.Y.Z`,compose 服务名仍叫 `cairn`,`/api/version` 返回的 `userAgent` 仍是 `cairn/<version>`。改 module path 是破坏性更大的改动,本轮不做。
- **`package.json` 的 `"name": "cairn-web"`** 是 npm package 内部名,不影响任何用户面,**不动**。
- **零功能 / 零修复**:仅品牌资产变更,API 契约、数据格式、磁盘布局、配置项均未触动。
- **未触动项(明确划线)**:
  - **未提供 PNG / ICO favicon**:本轮只交付 inline SVG,适合嵌入 web 与高 DPI 渲染。需要 favicon.ico 的场景另起一个 PR 跑 sharp 或 Inkscape 转码。
  - **未替换登录页 logo**(cairn dev 模式目前没有登录页,无需改)。
  - **`NavOutlined` 等菜单 icon** 仍是 antd 默认集,菜单的视觉风格没改 —— 只动顶部 brand 区。
  - **历史 CHANGELOG 条目不动**:0.5.20 及之前的「镜像仓库管理」字样保留(那是描述当时的事实)。

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有(api.ts 三处未 import 类型 / settings-page.tsx 五处未使用声明),本轮 `cairn-mark.tsx` / `App.tsx` / `index.html` 改动相关行**零新增**。
- **Go 侧门禁**:`gofmt -l internal/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build -tags webui` 退出码 0(bundle 自动从 `web/src/` 经 `pnpm build` 重新生成 dist,本轮不需要直接 build web)。
- **生产现场复测待办**:158 容器升级到 `cairn:0.5.21` 后,
  - 浏览器 tab 标题:`Cairn`
  - 顶栏左侧:浅 teal Cairn mark + `Cairn` 文字 + `v0.5.21` 徽章(版本号同步)
  - 「镜像列表」菜单 icon **仍是** DockerOutlined(故意不动)
  - 暗色主题(右上角月亮按钮):Cairn mark 自动反白为 teal-300 + amber-400

### 轮次与号位

- 本轮占 **0.5.21**:纯品牌资产变更,按 `AGENTS.md` 判定为**小版本(第 3 位)+1**(既有产品的标识变更,无功能 / 修复)。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.21 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.20] - 2026-09-28

本轮主题：**给 GC 加一个可勾选的「清理 0 tag 仓库」,让 0 tag 仓库彻底从镜像列表上消失**。背景:用户接入自动化测试时留下 `webauto-pull/busybox` 这种 0 tag 仓库 —— tag 已全部删除,但 `repos/<repo>/` 目录、孤儿 manifest body、blob 字节全部残留,既占清单一格,也占磁盘空间;旧版 GC 对此完全无效。

### 新增

- **GC 弹窗新增「也清理 0 tag 的仓库」勾选框**（`web/src/pages/images-page.tsx`）:**默认不勾**,保留 v0.5.18/v0.5.19 的纯 blob 清理行为;勾上之后才走破坏性更强的整仓库删除路径。Popconfirm 的 description 段把警告字面写出来 ——「会删除整个仓库目录,不可恢复」—— 让操作员在点「运行」之前能看到自己点的是什么。Tooltip 也同步加了一句话说明这个新开关。
- **GC 加 Pass 3（删空仓库目录）+ Pass 4（二次 blob 回收）**（`internal/storage/filesystem.go`）:Pass 3 扫 `repos/`,对每个目录三重检查后才删 —— ① `tags/` 为空(或不存在);② `uploads/<repo>/` 里没有 startedat 在 24h 之内的会话（保护正在 push 的仓库）;③ 拿 `lockRepo` 防并发。删除前先用 `filepath.Walk` 累加目录 size,记到 `GCResult.EmptyRepoFreedBytes` 给前端展示。Pass 3 删掉的孤儿 manifest body 之前引用的 blob 现在真正变成孤儿,Pass 4 再扫一遍 Pass 1 把这些字节真正回收 —— 这是 0 tag 仓库能释放空间的关键（光删目录不回收 manifest 引用的 blob,字节还在）。
- **`POST /api/gc` 接受可选 body `{"cleanEmptyRepos": true}`**（`internal/api/handlers_extra.go` 的 `RunGC`）:默认 false → 走 v0.5.18 行为,byte-for-byte 兼容;true → 走 Pass 3/4。空 body / 缺字段 / 字段值缺失 都按 false 处理,**不破坏任何既有客户端**。
- **`GCResult` 加 `removedEmptyRepos` 与 `emptyRepoFreedBytes`**（`internal/storage/storage.go`）:两者都用 `omitempty` —— 默认请求路径下响应里**完全不出现**这两个字段,JSON 形状与 v0.5.18 一致;只有请求带 `cleanEmptyRepos=true` 才会有。
- **`ProxyTestResult` 同款扩展**(no,this is the GC entry):Go side,前端 types 加 `removedEmptyRepos?: string[]` / `emptyRepoFreedBytes?: number`;前端 `api.ts` 的 `runGC` 现在接受 `GCOption` 参数,默认 `{}`（保持旧调用形态）。
- **GC 跑完自动刷新镜像清单**(`load(false)`):勾选状态下删了空仓库之后,清单会变短 —— 不刷一下用户会以为 GC 没生效。这个刷新只在「勾选了且真有删除」时发生,默认路径不刷。
- **`formatRepoList` helper**(`images-page.tsx` 底部):>3 个截断成「前 3 + 等 N 个」,与镜像清单顶部 error 列表的展示风格一致。

### 影响范围（升级须知）

- **零侵入的默认行为**:**不勾** 弹窗里的 checkbox,GC 的行为与 v0.5.18 完全一致 —— Pass 1 删孤儿 blob、Pass 2 删 24h+ 孤儿上传,**不会碰任何仓库目录**。所有「老操作员按旧习惯点 GC」的路径不受影响。
- **新开关打开后的语义变化**:勾上之后,镜像列表上 0 tag 的仓库会被物理删除（不是「隐藏」,是从磁盘抹掉）。删后无法恢复 —— 与 `DeleteRepository` 同级破坏性。任何生产环境如果想在删之前再 review 一次,先**不勾**跑一次 GC 看 `removedBlobs / freedBytes` 是不是符合预期,然后再勾一次干。
- **API 契约向前兼容**:响应字段顺序、空 body、缺字段路径都保持旧形状;只有客户端主动传 `cleanEmptyRepos=true` 才看到新字段。
- **未触动项（明确划线）**:
  - **未补 `web-auto` 自动化场景**:0.5.18 起 `gc-real.yaml` 已经能覆盖空态分支;要覆盖「勾选后删空仓库」需要在 dev 注册表里先造一个 0 tag 仓库场景。本轮未补,与 0.5.19 一致记入未触动项,留待单独 PR。
  - **未补 `storage/filesystem_test.go`**:现有 storage 包无单测文件(其他包也以集成测试为主),开新单测覆盖 Pass 3 的三条分支（0 tag + 老 upload / 0 tag + 新 upload / 有 tag）需要新搭脚手架,本轮未做。
  - **`removedEmptyRepos` 数组顺序未做排序保证**:返回顺序由 `os.ReadDir` 决定,UI 展示时直接用 —— 若有强顺序诉求,后续可加 sort.Strings,但当前为「原样回报」更便于定位。

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有(api.ts 三处未 import 类型 / settings-page.tsx 五处未使用声明),本轮 `images-page.tsx` / `api.ts` / `types.ts` 改动相关行**零新增**。
- **Go 侧门禁**:`gofmt -l internal/storage/ internal/api/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build ./internal/... ./cmd/...` 退出码 0。
- **默认路径契约不变**:不传 body / 传 `{}` / 传 `{"cleanEmptyRepos":false}` 三个路径,响应 JSON 应与 v0.5.18 完全一致(`removedBlobs` + `freedBytes` 两个字段,无 `removedEmptyRepos` / `emptyRepoFreedBytes`)。
- **生产现场复测待办**:158 容器升级到 `cairn:0.5.20` 后,
  - 默认路径:点「运行 GC」→ toast 文案与 0.5.19 一致(`清理 N 个孤儿 blob,回收 X.XX MiB`),清单不刷新。
  - 新路径:勾「也清理 0 tag 的仓库」→ 点「运行」→ `webauto-pull/busybox` 应被物理删除;toast 文案类似「GC 完成:清理 N 个孤儿 blob,回收 X.XX MiB;清空 1 个空仓库(webauto-pull/busybox)」;镜像清单上这一行消失,其它行不动。

### 轮次与号位

- 本轮占 **0.5.20**:既有 GC 能力的扩展 + 一个用户可控的破坏性开关,按 `AGENTS.md` 判定为**小版本（第 3 位）+1**。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.20 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.19] - 2026-09-28

本轮主题：**修掉「代理测试把 Docker Hub 匿名 /v2/ 的标准 401 误报成连通失败」**。现象是编辑代理时点「测试连接」、目标填 `https://registry-1.docker.io/v2/`,弹窗直接红字「连通失败」——但同样这条代理在拉取任务里是能正常工作的。

### 修复

- **代理测试判定口径**（`internal/api/handlers_extra.go` 的 `proxyTestThrough`）：旧逻辑把任何 4xx/5xx 当成 `ok: false`，再叠加 `ok:false → 红色"连通失败"` 的 UI 分支（`web/src/pages/proxies-page.tsx` 的 `ProxyTestAlert`），于是 Docker Hub / ghcr.io / quay.io 对匿名 `GET /v2/` 的标准应答 **HTTP 401**（`WWW-Authenticate: Bearer ...` Bearer 挑战，按 Registry V2 协议就是这条路径的"正常应答"）被误判成代理不可用。判定口径改写为**「代理能不能把请求送到目标并拿回应答」**：任何 HTTP 响应（1xx/2xx/3xx/4xx/5xx）都证明代理 + TLS 转发链路是通的，只有 transport 层失败（`client.Do` 返回 err：超时、拒连、TLS 握手失败）才报 `ok: false`。状态码语义保留在 `status` / `statusText` 字段，并新增 `note` 字段给前端一句话解释：401 → 「目标要求认证（HTTP 401）；代理可达,目标在线」；403 → 「目标拒绝访问」；404 → 「目标路径不存在」；5xx → 「上游异常」；其他 4xx → 通用回退。
- **`registryApiVersion: "2"` 不再被 401 吃掉**：原来仅当 `StatusCode < 400` 才标注 `registryApiVersion`，这导致对 `/v2/` 端点的 401 应答丢失「对面是 Registry V2」的事实信号。现对 `/v2/` 端点的 **401 单独也标 `registryApiVersion: "2"`**——Docker Registry V2 协议对匿名 `/v2/` 的 401 + `WWW-Authenticate: Bearer` 头是这套协议对"我在跑 registry"的明牌。

### 变更

- **前端 `ProxyTestAlert` 着色按状态码分档**（`web/src/pages/proxies-page.tsx`）：原来 `ok:true` 一律绿、`ok:false` 一律红；现在 2xx 绿、4xx **蓝**（代理可达,目标按业务规则拒绝）、5xx **黄**（代理可达,上游异常）、传输失败仍红。这样「代理能用,目标要认证 / 路径写错 / 上游挂了」三种状态在同一弹窗里能一眼分清，避免「明明能拉镜像却被红字吓到」的体验。
- **`ProxyTestResult` 类型加 `note?: string`**（`web/src/types.ts`），前端把后端的解释原文展示在 description 区域。

### 影响范围（升级须知）

- **受影响版本为 v0.5.0 ~ v0.5.18**：`proxyTestThrough` 在 v0.5.0 引入（与 `TestProxy` 端点同期上线，见 `internal/api/handlers_extra.go:1018`），4xx → `ok:false` 的判定从那以后一直在。
- **触发条件与观感**：任何「用代理拉公网 registry」的代理条目，编辑弹窗里点测试、目标填 `https://<公网 registry>/v2/` 都会撞红。所有「拉 Docker Hub 用」的代理——也就是日常最高频的那一类——100% 命中。
- **升级后行为变化**：
  - 之前红字「连通失败」、但代理其实能用 → 升级后**蓝字**「代理可达 · HTTP 401 · X ms」+ 描述行说明「目标要求认证」；
  - 真失败的（拒连 / 超时 / TLS 错）依然红字「连通失败」+ 描述给出原始 error，行为不变；
  - 上游 5xx（代理通了、目标挂）→ 升级后**黄字**「代理可达,上游异常 · HTTP 503」+ note，区别于「代理本身挂了」的红字。
- **API 契约兼容**：`ok` 字段语义**实质变了**——原来「目标业务侧成功」,现在「代理把请求送到了」。任何仍依赖旧语义的代码 / 测试需要同步改（目前看 grep 没找到外部消费者，主要是 UI 自己用）。
- **磁盘格式 / 配置 / 路由 / 权限**：均未触动。
- **未触动项（明确划线）**：
  - `POST /api/proxies/<id>/test` 与 `POST /api/proxies/test` 的端点形状不变（始终 200 + 信封），仅信封里的 `ok` 判定规则变了。
  - `Probe`（v0.5.15 起的 TCP-only 探测）不受影响，那条链路只看「TCP 能不能连上 ip:port」,不读 HTTP 响应。
  - 拉取任务的事前探测 (`ProbePullSource`) 是另一条独立路径,也不受这条改动影响。
  - **未补 `web-auto` 自动化场景**：「测连接」在 dev 没有该走的端到端断言（需要先在 dev 起一个会按需返回 401/200/5xx 的 mock registry,场景覆盖成本高于本轮主题）。CHANGELOG 里明确记一笔,留给后续单独 PR。

### 验证

- **类型层**：`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有,本轮 `proxies-page.tsx` / `types.ts` 改动相关行**零新增**。
- **Go 侧门禁**：`gofmt -l internal/api/` 无输出；`go vet ./internal/api/...` 退出码 0；`go build ./internal/api/...` 退出码 0。
- **协议事实**：`registry-1.docker.io/v2/` 对匿名 GET 的标准应答是 401 + `WWW-Authenticate: Bearer realm="registry-1.docker.io"`,这是 Docker Registry V2 协议规定的**正确应答**,与 AGENTS.md §V2 协议事实「`/v2/` 的挑战不带 scope,必须支持申请无 scope 的 token」对应。
- **生产现场复测待办**：158 容器 `cairn:0.5.18` 升级到 `cairn:0.5.19` 后,编辑 `<proxy>` 代理 → 测试目标 `https://registry-1.docker.io/v2/` → 应弹蓝字「代理可达 · HTTP 401 · X ms」+ 描述「目标要求认证（HTTP 401）；代理可达,目标在线」;同条代理在拉取任务里行为不变,继续能拉。

### 轮次与号位

- 本轮占 **0.5.19**：纯缺陷修复,按 `AGENTS.md` 判定为小版本（第 3 位）+1。**两轮升级各管一件事**：0.5.18 = GC toast undefined/NaN（前端解码层）;0.5.19 = 代理测试 401 误报（后端判定口径 + 前端着色）。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.19 之后顺延一格；`0.6.0`（TLS 证书管理）由人指定,不随顺延改号。

---

## [0.5.18] - 2026-09-28

本轮主题：**修掉「GC 成功提示显示 `undefined` / `NaN`」并补 GC 含义入口**。`gc-real` 验收场景在 0.5.17 验收报告里以 **P2**（中）记录：toast 出现 ≠ toast 内容正确，断言形状才暴露该解码缺陷。

### 修复

- **`web/src/pages/images-page.tsx` 的 `runGC` 调用解码层**：旧代码直接读 `r.removedBlobs` / `r.freedBytes`，但 `runGC()` 真实返回类型是 `Promise<ApiResult<GCResult>>`（见 `web/src/api.ts:238-239` 与 `web/src/types.ts:170-174`），`r` 是信封 `{ success, code, message, data: { removedBlobs, freedBytes } }`。前端没解 `data`，于是 `r.removedBlobs === undefined`、`undefined / 1024 / 1024 === NaN`，toast 显示成「清理 undefined 个孤儿 blob，回收 NaN MiB」——这是 0.5.17 报告里 **P2** 的根因链。后端契约是对的（`internal/api/handlers_extra.go:653-668` 经 `writeJSON` 信封正确写出 `{ removedBlobs, freedBytes }`），属纯前端解码层缺陷。
- **同一回调里补 `r.success` 分支**：`runGC()` 在 API 失败时**不抛异常**（返回 `{ success: false, ... }`），旧代码 `try/catch` 永远落 `message.success` 分支，失败也被报成「清理 undefined 个」。现改为先 `if (!r.success) message.error(...); return;`，再读 `r.data`，再判空态。

### 新增

- **「运行 GC」按钮右侧加 `?` 提示图标**（`QuestionCircleOutlined` + `Tooltip`）。Tooltip 文案：`GC = Garbage Collection。扫描并清理孤儿 blob（被废弃的上传会话、被解除引用的层），释放磁盘空间。注意：删除 manifest 只是解除引用，真正的磁盘空间要 GC 才回收。` —— 直答用户的「运行 GC 是什么意思」，并把 README/AGENTS.md 里那条「删 manifest 只解除引用、要 GC 才回收磁盘空间」的协议事实前置到点击之前。
- **空态分支文案**：GC 跑完若 `removedBlobs === 0`，toast 显示 `GC 完成：没有需要清理的孤儿 blob`，替代旧分支里的「清理 0 个孤儿 blob」（也是合理显示，但 GC 语义下「没有需要清理」更直观，且便于 UI 区分「没东西可清」与「清掉了 0 个但仍跑了一次」）。

### 影响范围（升级须知）

- **受影响版本为 v0.5.0 ~ v0.5.17**：`runGC` 声明于 v0.5.0（`web/src/api.ts` 的同源注释）；自那以后该解码一直有缺陷，但因为 0.5.17 之前的 `gc-real` 场景只断言 toast 出现（不断言内容形状），QA 层面表现为**假绿**。**升级到 0.5.18 后这条出口才真正可用**，用户才能看到 GC 到底清掉了几 MB / 几个 blob。
- **空态分支的语义变化**：升级前 GC 跑空也会出现「清理 0 个孤儿 blob，回收 0.00 MiB」，字面是真但读起来像「白跑了一次」；升级后空态显示「没有需要清理的孤儿 blob」，跑空 ≠ 跑失败。
- **API 契约未变**：后端响应字段 `removedBlobs` / `freedBytes` 不变；只是前端终于正确解码。磁盘格式、配置项、`/api/gc` 路由与权限均未触动。
- **未触动项（明确划线）**：
  - **`POST /api/gc` 未纳入 `allowDelete` 门控**（0.5.17 报告的 R-open-2）：本轮只修前端解码 + UX，安全门控是另一条独立修复路径，留待后续。
  - **GC 的可视进度**仍是「loading toast + 一次性结果 toast」，没有中间进度。

### 验证

- **类型层**：`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**，全部为基线既有（`api.ts` 三处未 import 类型 `DeleteRepositoryPayload` / `DeleteManifestPayload` / `GCResult`；`settings-page.tsx` 五处未使用声明），本轮 `images-page.tsx` 改动相关行**零新增**。
- **场景断言回归**：`tests/web-auto/scenarios/gc-real.yaml` v2（断言 toast **内容形状**为 `GC 完成：清理 [0-9]+ 个孤儿 blob，回收 [0-9]+\.[0-9]{2} MiB`）将在 0.5.18 上由红转绿；v1（仅断言 toast 出现）保持绿。空态分支需新场景覆盖（`removedBlobs === 0`），本轮未补，留待后继。
- **生产现场复测待办**：158 容器 `cairn:0.5.17` 升级到 `cairn:0.5.18` 后，跑 `POST /api/gc` → toast 文本不再含 `undefined` / `NaN`；当注册表已无孤儿 blob 时，toast 显式显示「没有需要清理的孤儿 blob」。

### 轮次与号位

- 本轮占 **0.5.18**：缺陷修复 + 既有功能优化（`?` 提示 + 空态文案均属既有 UI 的补强），按 `AGENTS.md` 判定为**小版本（第 3 位）+1**。按 `docs/ROADMAP.md` 的号位顺延规则，整表自 0.5.18 之后顺延一格（0.5.19 / 0.5.20 / ...）；`0.6.0`（TLS 证书管理）由人指定，不随顺延改号。

---

## [0.5.17] - 2026-09-27

本轮主题:**修掉「没配代理也显示源可达、入队后却卡在拉取中」**。用户报的现象是「增加队列时检测没过关,结果加进去又一直卡着」,但**后端一直是如实报告的**——把同一份探测请求直接打到 158 上,它回的清清楚楚是失败:

```
ok:false, elapsedMs:5000,
error:"registry: GET /v2/: Get \"https://registry-1.docker.io/v2/\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)"
```

**失败的是前端:它拿到这份响应后不看 `ok`,直接画了绿勾「源可达」。** 这是本轮第一层成因(P0)。第二层(P1):`/v2/` 这一个裸端点**能通只说明「源 registry 存在」**,既不说明「你要拉的镜像存在」,也不说明「这条链路真能取到 manifest」;于是「配了代理但 tag 写错」这类输入,后端探测会给出**假阳性** `ok:true`(158 实测,见「验证」)。两层叠加,就得到了「检测通过 → 入队 → 永远卡住」。

### 修复

- **前端改为消费后端结论**(`web/src/api.ts`、`web/src/pages/pull-page.tsx`):探测响应的 `ok` / `error` 此前**从未被读取过**(`git log -S 'data.ok' -- web/src/pages/pull-page.tsx web/src/api.ts` 输出为空,即**自 v0.2 引入该探测起一直是死字段**;`api.ts` 的返回类型里甚至在 TS 层面就没有这两个字段)。现在判定改为 **`ok !== false` 才通过**:`ok === false` 时结论行落 `state:'failed'`,原文展示后端给出的原因(标 `源端预检未通过`),入队按钮保持禁用。拦截线是 **`ok === false` 而不是 `!ok`**——缺字段的旧/异常响应不会被误判成失败。
- **后端补上源镜像级校验**(`internal/api/handlers_extra.go` 的 `ProbePullSource`):`/v2/` 探测成功后,再按**与拉取任务完全相同的规则**去取一次源 manifest——同一个 `splitSourceRef` 解析、同一个 `pull.QualifySourceRepo` 归一化仓库名(Docker Hub 的 `alpine` → `library/alpine`)、同一套凭据、同一个代理(`registry.NewClient`)。取到即 `sourceExists:true` 并回报 `sourceDigest`。失败分两种文案:
  - **源镜像不存在**(`registry.Error.IsNotFound()`,即 404 MANIFEST_UNKNOWN):`ok:false` + 「源 registry 可达,但源镜像 `<repo>:<tag>` 不存在——请检查镜像名与 tag 拼写」。这正是「配了代理、tag 打错」的场景——旧版会在这里给出 `ok:true`。
  - **其它取用失败**(认证 401、代理不通、超时等):`ok:false` + 「源镜像 `<repo>:<tag>` 探测失败:`<原因>`」。
- **前后端契约对齐**(`internal/api/handlers_extra.go`):`web/src/types.ts` 的 `DestStatus` 一直声明着 `sourceRepo` / `sourceTag` / `sourceExists` / `sourceDigest` / `identical` / `probeError`,而 Go 侧**零产出**——声明了却永远收不到。现在 `dest` 段真实注入这四个源侧事实,并据此计算 `identical`(目标 tag 已有 digest **且**与源 `sourceDigest` 相同 → 内容一致,不必重拉)与 `willReplace`;**dest 侧探测失败时同时给出 `destError` 与兜底 `dest`(带 `probeError`)**,前端黄色告警接受两种来源(`dest?.probeError ?? probeResult.destError`)——旧版只给 `destError` 一个字段,另一条路径取不到。
- **`elapsedMs` 改在响应组装末尾计算**:旧版在 `/v2/` 探测后立刻取值,新增的 manifest 校验耗时不在其内;现在覆盖全程(前端目前不展示该字段)。

### 文档

- **校准 README 的版本面**:`README.md` 的「当前状态」标题与「当前版本」行都停在 `v0.5.15`。经查 **0.5.16 轮的提交 `c2a1319` 整份没有改动 `README.md`**——`AGENTS.md`「一次改动要同时更新这几处」中的 README 这处被漏掉了(该轮其余四处均已改)。本轮一并校正到 `0.5.17`。
- README 的构建示例用的是不带版本的 `cairn:dev`,无镜像 tag 需要同步(已逐行核对 `docker build` / `IMAGE` 相关行)。

### 影响范围(升级须知)

- **受影响版本为 v0.2 ~ v0.5.16**(探测能力与前端调用同在 `d232aee`「feat: v0.2 …」引入,而 `data.ok` 自那以后从未被读取)。0.5.16 及更早的版本里,**「源可达」绿勾是一个不反映后端结论的装饰**:后端说 false 它也画绿勾。
- **触发条件与观感**:不配代理访问 Docker Hub 这类「源 registry 可达、但你这条链路取不到」的最常见;表现为入队后任务停在拉取中直到失败(158 现场一次 `library/alpine:3.15` 的任务在 **61.7s** 后以 `manifest failed` 收场、状态 `cancelled`——拉取阶段的超时/取消语义本轮未改,见「已知遗留」)。
- **升级后行为变化**:原本「检测通过、入队卡住」的输入,现在会在**入队前**就红字拦住并说明原因;原本「配了代理但 tag 写错」也能通过的输入,现在会被明确指出是镜像名/tag 拼写问题。这两类都是**从假绿变真红**,不是新增限制。

### 已知遗留(本轮未改)

- **`asErr` 恒为 false**(`internal/registry/client.go:374-384`):该函数无条件返回 `false`,使调用方无法据此区分错误类型。本轮未改(涉及的调用面比本轮主题大,需单独评估)。
- **拉取阶段超时/快速失败未改造**:本轮只修「入队前的预检」,不含「入队后卡多久」。源不可达时任务仍会走到拉取阶段的超时才收场(现场 61.7s)。
- **不引入「全局代理回落」**:没配代理就是直连,不静默改用某个已存代理——那会把「配错」变成「猜对」,更难排查。
- `UpdateProxy` 用空密码会清掉已存密码(`internal/api/handlers_extra.go`):0.5.15 已记录,本轮未改(修复要变更已文档化的 PATCH 语义)。
- 设置页 5 处 `TS6133` 未使用声明仍在基线里,未清理。

### 验证

- **生产现场对照(158,`cairn:0.5.16`,升级前)**——同一份探测请求直接打到 API,绕开前端:
  - 无代理 + `library/alpine:3.16` → **`ok:false`**,`elapsedMs:5000`,错误为 `context deadline exceeded`(158 无直连外网)→ **后端如实报失败,前端却显示绿勾**,此即 P0 铁证。
  - 走代理(`<proxy>`,`http://proxy.example.com:7890`)+ **不存在的 tag** `library/alpine:9.99-nope` → **`ok:true`** ❌ → 旧版自身误报,此即 P1 铁证。
  - 走代理 + 真实 tag `library/alpine:3.16` → `ok:true`,`elapsedMs:2684`,响应里**没有任何**存在性/digest 信息(旧契约)。
- **前端源码铁证(升级前源码)**:`pull-page.tsx` 的探测回调为 `if (result.success && result.data) { setProbeResult({ state:'ok', … }) }`——**只要 HTTP 成功就画绿勾**;`api.ts` 的 `probePullSource` 返回类型不含 `ok`/`error`/`sourceExists`/`sourceDigest`/`destError`。新文案在旧源码中计数为 0(`源端预检未通过` 0/2、`正在校验源镜像是否可拉取` 0/1)。
- **本地隔离实例冒烟**(独立端口 `18787` + 独立数据目录,二进制 `version=0.5.17`):
  - A. 无代理 + `library/alpine:3.19`(开发机可直连 Docker Hub)→ `ok:true`、`sourceExists:true`、`sourceDigest:sha256:6baf43584bcb…`、`elapsedMs:2110`。
  - B. 带代理(`http://127.0.0.1:7890`)+ 同一真实镜像 → `ok:true` + digest(两条链路都验证)。
  - C. 带代理 + **不存在的 tag** `library/alpine:9.99-nope` → **`ok:false`**、`sourceExists:false`、文案为「源 registry 可达,但源镜像 library/alpine:9.99-nope 不存在——请检查镜像名与 tag 拼写」——即 P1 的假阳性已被消除。
  - D. 带代理 + 真实镜像 + `destRepo`/`destTag` → `dest` 段真实注入 `sourceRepo`/`sourceTag`/`sourceExists`/`sourceDigest`,并给出 `exists:false`、`willReplace:false`(本地未配自身仓库,`probeDest` 走 `storage.ErrNotFound` 分支)。
  - E. `sourceRef:"library/alpine"`(不带 tag)→ `ok:true` 且不带源侧字段;经查前端表单校验(`pull-page.tsx`)强制要求 tag,**该输入在 UI 上不可达**,故后端不加 tag 兜底。
- **门禁**:`gofmt -l internal/ cmd/` 无输出;`go vet ./internal/...` 退出码 0;`go build ./...` 退出码 0;`go build -tags webui -o /tmp/cairn-gate2 ./cmd/server` 退出码 0;`go test -count=1 ./internal/...` 全部 `ok`;前端 `tsc --noEmit -p web/tsconfig.json` 与 `c2a1319` 基线**逐条 diff 一致(10 条 → 10 条,零新增)**,其中 5 条在 `settings-page.tsx`、3 条在 `api.ts`(行号 :86/:93/:100,均为未改动的既有 `TS2304`)、2 条在 `images-page.tsx`,本轮改动的 `api.ts` / `pull-page.tsx` 新增行零错误。

### 升级后复测(158 生产)

- **复测环境**:生产机 `registry.example.com`,容器 `cairn:0.5.17`,镜像 ID `a7804a9ee3e3`(≠ 0.5.16 的 `0d3278b8ff77`),`/api/config` 返回 `"version":"0.5.17"`、`allowPull:true`。以下每条都是**升级后**真实请求的返回:
- A. **无代理 + `library/alpine:3.16`**(用户报障场景)→ `ok:false`、`elapsedMs:5001`、`context deadline exceeded`——与升级前一致:源不可达就必须拦下,不再放行入队。
- B. **走代理 + 真实 tag `library/alpine:3.16`** → `ok:true`、`sourceExists:true`、`sourceDigest:sha256:452e7292acee…`、`elapsedMs:2944`(升级前同一请求只回 `ok`/`apiVersion`/`host`/`elapsedMs`,**没有任何存在性/digest 信息**)。
- C. **走代理 + 不存在的 tag `library/alpine:9.99-nope`** → **`ok:false`**、`sourceExists:false`、文案「源 registry 可达,但源镜像 library/alpine:9.99-nope 不存在——请检查镜像名与 tag 拼写」——**升级前同一请求返回 `ok:true`**,即本轮修复的直接目标在生产上已被翻转。
- D. **目标仓库不存在该 tag**(`destTag=3.16`)→ `dest` 段真实注入 `sourceRepo`/`sourceTag`/`sourceExists`/`sourceDigest`,并给出 `exists:false`、`willReplace:false`——升级前这些字段在响应里**根本不存在**(前端 `DestStatus` 声明了却永远收不到)。
- E. **目标仓库已有该 tag**(`destTag=3.19`)→ `exists:true`、`existingDigest:sha256:6baf43584bcb…`、`identical:false`、`willReplace:true`。
- F. **前端产物已换新**:`/assets/index-DFhQ-Nyq.js`(1202788 字节,md5 `e760eff581aab7e1537e0009e9f02f09`)取代升级前的 `/assets/index-ZcXUjQDW.js`(1202498 字节,md5 `a9167c8273719b506d5b6089ae5afc71`);新文案 `源端预检未通过`、`正在校验源镜像是否可拉取` 在新 bundle 中各命中 **1 处**(旧 bundle 命中 0 处)。
- G. **端到端正向**:走代理 `POST /api/pull/jobs`(`library/alpine:3.16`,dest 写回同仓库同 tag)→ 任务 **succeeded**,19.5s、2 个 blob / 2809308 字节,`finalDigest:sha256:452e7292acee…` 与预检返回的 `sourceDigest` **完全一致**——预检说能拉,真拉下来就是同一个 digest。
- **上线三道闸**:① 源码闸 `HEAD=a16c667` 且 `internal/version/version.go` 为 `0.5.17`;② 镜像闸新 tag 镜像 ID `a7804a9ee3e3` ≠ 上一版 `0d3278b8ff77`(排除「缓存假构建」);③ 内容闸 `docker run --network none` 启动日志 `"msg":"config loaded","version":"0.5.17"`(scratch 镜像无 shell,只能靠启动日志验版本)。
- **回滚路径**:升级前 `.env` 已备份为 `.env.bak.pre0517`(内容即 0.5.16 的 `IMAGE=cairn:0.5.16`),回滚即 `cp -a .env.bak.pre0517 .env && docker compose up -d`。

### 兼容性

- **API 的 `ok` 字段语义未变**,只是**终于被前端真正消费**;响应新增 `sourceRepo`/`sourceTag`/`sourceExists`/`sourceDigest` 与更丰富的 `dest`(均为新增字段,旧客户端忽略即可)。
- **磁盘格式与配置未变**:纯请求处理路径的改动,无迁移、无配置项增减。
- ⚠️ **升级后首次预检变慢属正常**:源镜像级校验比裸 `/v2/` 多一次 manifest 往返(本地实测总计约 2.1~2.7s,受网络影响);`elapsedMs` 已覆盖全程。
- ⚠️ **升级后「预检通过」的门槛实质变高**:以前「源 registry 通」就算过,现在要「这个镜像取得回来」才算过。若某条链路此前一直靠假绿通过,升级后会被挡住——这是本轮的目标行为。

### 轮次与号位

- 本轮占 **0.5.17**:纯**缺陷修复**(前端误报 + 后端探测口径不足),按 `AGENTS.md` 判定为小版本(第 3 位)+1。
- 因该 hotfix 优先于原定的「韧性轮」,按 `docs/ROADMAP.md`「号位是预留,不是承诺……本表自上而下整体顺延」的规则**整表顺延**:韧性轮 0.5.17 → **0.5.18**、工程化 0.5.18 → **0.5.19**;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

## [0.5.16] - 2026-09-27

本轮主题:**修掉「拉取任务点完「添加」就从列表消失、且看不到任何历史」**。现象看着像前端不刷新、像数据库没写,实际是**进程被一次空指针 panic 打死了**:只要拉取成功拿到源 manifest,`Orchestrator.RunOne` 就会去调一个**永远为 `nil`** 的函数字段,当场 SIGSEGV;Go 的 panic 不 recover 就是整个进程退出,容器被 `restart: unless-stopped` 拉起,而任务表在**内存**里,重启即清空——任务不是「消失」,是**承载它的进程没有了**。这也正是「没有历史任务」的直接原因:历史本来就只在内存,进程一死什么都没留下。

### 修复

- **删除 `Orchestrator.pullPlatforms` 字段,改为方法 `platformAllow()`**(`internal/pull/executor.go`):该字段**未导出**,而 `Orchestrator` 的**唯一构造点在另一个包**(`internal/server/server.go`),外部包无法给它赋值,所以它从**引入之日起恒为 `nil`**;`RunOne` 在成功取得源 manifest 之后无保护地调用它 → 空指针解引用。现在改为 `func (o *Orchestrator) platformAllow() []string`,内部读**实时**配置 `o.Cfg.PullPlatforms()`,并带 `o == nil` 与 `Cfg == nil` 保护。**语义与原意图完全一致**(未配置返回 `nil` = 不过滤平台),但**从结构上消灭了「忘记接线」这一类缺陷**——不再有任何需要跨包赋值的字段,`internal/server/server.go` 因此无需改动。
- **队列增加 panic 兜底 `runJobGuarded`**(`internal/pull/queue.go`):`executeOne` 现在经 `runJobGuarded` 调用编排器,后者用 `recover()` 接住 panic,记一条 `slog.Error("pull job panicked", …)`(**带完整 `debug.Stack()`**)并把该任务置为 `failed`(文案 `internal error: <值>`)。**一个任务的 bug 从此只降级一个任务,不再拖垮整个进程**——这才是「任务消失」真正的放大机制。

### 影响范围(升级须知)

- 该缺陷由提交 `8836c92`(写的是 v0.6.0 拉取平台白名单,后被 `e46dc3c` 并入 `[0.5.8]` 小节)引入。`git show <release>:internal/pull/executor.go | grep -c pullPlatforms` 实测:**0.5.7 = 0 次,0.5.8 / 0.5.9 / 0.5.11 / 0.5.14 / 0.5.15 均为 3 次**。即**受影响版本为 0.5.8 ~ 0.5.15**,0.5.7 及更早不受影响。
- **触发条件既窄又具欺骗性**:必须**成功取得源 manifest**才会走到崩溃点。源地址写错(404)、认证失败(401)、代理连不通时,代码在更早的位置就 `return` 了,**怎么复现都复现不出来**——这就是它在 0.5.8 之后一直没被发现的原因(包括上一轮 0.5.15 的冒烟:当时验的是「探测」而非真实拉取,根本没走到这一行)。

### 已知遗留(本轮未改)

- **「拉取历史」是一个从未接线的能力缺口,不是本修复的回归**:`pull_jobs` 表(`internal/db/db.go` 迁移 1)与 `PullJobRecord` 结构体都在,但**没有任何调用者**;配置项 `PullHistoryRetention` 被赋值却从未被读取;`ListPullJobs` 从不合并数据库;前端 `fromHistory` 分支不可达,也没有「历史」选项卡。**本轮解决的是「进程活着时不再丢任务」,重启仍然清空**。补齐持久化 + 保留清理 + 历史 API + UI 入口属**面向用户的新能力**,按 `AGENTS.md` 应为中版本且需**人工指定号位**(`0.6.0` 已被 TLS 证书管理占用),故本轮不动。
- **`UpdateProxy` 用空密码会清掉已存密码**(`internal/api/handlers_extra.go`):0.5.15 已记录,本轮未改,原因同前——修复要变更已文档化的 PATCH 语义。
- 设置页 5 处 `TS6133` 未使用声明(`Descriptions`、`formatDateTime`、`inventory`、`savingRegistryUrl`、`setSavingRegistryUrl`)仍在基线里,未清理。

### 验证

- **生产现场证据(158,`cairn:0.5.15`)**:`docker logs cairn` 中的 panic 栈逐帧如下;其后 **约 4 秒**即出现 `config loaded`(容器被拉起),再下一次 `GET /api/pull/jobs` 的响应体从 **624 字节掉到 52 字节**(即 `data:[]`——任务没了):

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x9566d7]

goroutine 34 [running]:
cairn/internal/pull.(*Orchestrator).RunOne(...)
	cairn/internal/pull/executor.go:171 +0x637
cairn/internal/pull.(*Executor).executeOne(...)
	cairn/internal/pull/queue.go:392 +0x202
cairn/internal/pull.(*Executor).Run(...)
	cairn/internal/pull/queue.go:353 +0x85
created by cairn/internal/server.(*Runtime).Start in goroutine 1
	cairn/internal/server/server.go:257 +0xb0
```

- **A/B 隔离冒烟(决定性对照)**——两个二进制、各自独立端口与数据目录,跑**同一条**真实拉取(`library/alpine:3.19`,经 `http://127.0.0.1:7890` 代理):
  - **旧二进制(v0.5.15,`266a9fa`)**:任务在 `t=2s` / `t=3s` 两次 `GET /api/pull/jobs/{id}` 均可见,随后 **`PROCESS DIED at t=3s`**;日志命中 `panic`,栈与上面 158 生产栈**逐帧一致**(仅行号随版本偏移);收尾打印 `--- process after run --- DEAD`。
  - **新二进制(含本轮修复)**:同一条拉取**跑完全程**——`status=succeeded`、`bytes=23260729`、`blobs 34/34`、`finalDigest=sha256:6baf43584bcb…`;`GET /api/pull/jobs` 返回 **`rows=1`**,任务**留在列表**;**日志零 panic**;**进程 `ALIVE`**。
  - 该对照同时排除了「无 panic 只是因为没走到崩溃点」的假阳性:旧二进制在同一条测试下**必然**崩溃,新二进制在同一条测试下**必然**不崩。
- Go 侧:`gofmt -l internal/` 无输出;`go vet ./internal/...` 退出码 0;`go build ./...` 与 `go build -tags webui -o /tmp/cairn-gate ./cmd/server` 退出码 0;`go test -count=1 ./internal/...` 全部 `ok`。新增用例 `TestPlatformAllowNilSafe`(nil 接收者与 nil 配置都不 panic)、`TestPlatformAllowReadsLiveConfig`(读的是实时配置而非构造期快照)、`TestExecuteOneRecoversFromPanic`(编排器 panic → 任务 `failed` 且进程存活)。
- 前端:`./web/node_modules/.bin/tsc --noEmit` 与基线逐条比对 **10 → 10,无新增**。本轮**零前端改动**,`internal/webui/dist/` 属构建产物且在 `.gitignore` 中,由 Docker 构建期生成。

### 兼容性

- **无 API 变更、无磁盘格式变更**:纯后端缺陷修复,升级不需迁移。
- **失败语义变更须知**:编排器若 panic,该任务现在**保持可见**并显示为 `failed`,错误文案为 `internal error: …`(此前是进程消失、任务一并消失)。这是行为改进,但会有人第一次在列表里看到这类 `failed` 条目——它不是新缺陷,而是过去那些「凭空消失」的任务。
- **`platformAllow` 的语义与配置键未变**:设置页的「拉取平台白名单」行为与 v0.5.15 完全一致(未配置 = 不限制平台)。

### 轮次与号位

- 本轮占 **0.5.16**:纯**缺陷修复**(空指针崩溃 + 队列兜底),按 `AGENTS.md` 判定为小版本(第 3 位)+1。
- `docs/ROADMAP.md` 已顺延:韧性轮 0.5.16 → **0.5.17**、工程化 0.5.17 → **0.5.18**;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

## [0.5.15] - 2026-09-26

本轮主题:**把「探测」降级为纯 TCP 连通性 + 延迟**,并**让「测试连接」也出现在编辑弹窗里**。原先的「探测」是拿代理服务器本身当 HTTP 目标发一次请求——对 `http://` 代理勉强可用,对 `socks5://` 代理或只做 CONNECT 转发的代理必然失败,于是出现「新增时测试通过、探测时却不可用」的矛盾。本轮把探测收敛成一句话:**这个 ip:端口从本机连得上吗、往返多慢**;至于「这代理能不能真的转发」,交给弹窗内与行内的「测试」按钮。

### 变更

- **`Store.Probe` 改为纯 TCP 建连**:`net.DialTimeout` 建连后立即关闭,不发请求、不握手、不校验协议,`http` / `https` / `socks5` 三种代理一视同仁,超时 `5s`。`internal/proxies/proxies.go` 里的 `net/http` 依赖随之移除。
- **端口缺省规则**:地址未显式带端口时按协议补默认端口——`http` → `80`、`https` → `443`、`socks5` → `1080`(新增 `dialAddr`)。`http://proxy.example.com` 这种写法不再被当成缺端口。
- **新增延迟字段 `lastProbeLatencyMs`**:探测往返耗时(毫秒,浮点)。`GET /api/proxies`、`POST /api/proxies/{id}/probe`、`POST /api/proxies/probe` 的响应都带上;**失败时省略**(`omitempty`),不用 `0` 冒充测量值。
- **代理页新增「延迟」列**(「状态」列与「最后探测」列之间):上次探测的建连延迟;未探测或探测失败显示 `—`。
- **「测试连接」按钮与「测试目标」输入框移入编辑弹窗**:该按钮自 v0.5.13 起只在新增态出现,本轮**编辑态同样可用**(这取代了 v0.5.13「编辑态隐藏该按钮」的决定)。`POST /api/proxies/test` 请求体新增可选 `id`。
- **编辑态试连的密码回退规则**:编辑表单不回显已存密码,空密码因此有歧义。现在的规则是——`id` 能解析出条目,**且**提交的用户名与已存条目一致时复用已存密码;用户名被清空或改动则按匿名 / 新凭据试连,使测试结果与用户眼前的表单保持一致。
- **页头「探测」Tooltip 与页底说明改为两段式**:明确「探测 = 纯 TCP 连通性」「测试 = 穿过代理去访问目标」是两件事。

### 修复

- **「新增时测试通过、探测时不可用」**:`<proxy>`(`http://proxy.example.com:4433`,只做 CONNECT 转发)新增时试连成功,探测却报 `context deadline exceeded`(错误里带 `Head http://proxy.example.com:4433`)。根因是探测把「代理服务器能否直接应答一个普通 HTTP 请求」当成了健康检查——那是对代理能力的额外要求,不是「ip:端口通不通」。改为纯 TCP 后该条目探测转为可用并带回延迟。
- **页底超时文案与实现不符**:写的是「超时上限 8 秒」,而实际执行测试的路径最长 **12 秒**(`client.Timeout` 10 秒 + 上下文 12 秒),文案改为 12 秒。

### 已知遗留(本轮未改)

- **`UpdateProxy` 用空密码会清掉已存密码**:`internal/api/handlers_extra.go` 的 `UpdateProxy` 无条件执行 `existing.Password = in.Password`,而前端编辑态在密码留空时**不发送** `password` 字段,于是编辑任何条目(哪怕只改备注)都会把已存密码清掉,与表单上「密码(留空保留原密码)」的文案矛盾。修复需要把 `proxyInput.Password` 改成指针或引入显式 `keepPassword`,即**变更已文档化的 PATCH 语义**,故本轮不擅自改,记录待定。
- 设置页 5 处 `TS6133` 未使用声明(`Descriptions`、`formatDateTime`、`inventory`、`savingRegistryUrl`、`setSavingRegistryUrl`)仍在基线里,未清理。

### 验证

- Go 侧:`gofmt -l internal cmd` 无输出;`go vet ./internal/...` 退出码 0;`go build ./...` 与 `go build -tags webui ./...` 退出码 0;`go test -count=1 ./internal/...` 全部 `ok`,含新增用例 `TestProbeSucceedsForPlainTCPListener` / `TestProbeReportsDialFailure` / `TestDialAddrDefaultsPort`。
- 前端:`./web/node_modules/.bin/tsc --noEmit -p web/tsconfig.json` 与基线逐条比对 **10 → 10,无新增**;`cd web && npm run build` 退出码 0(`✓ 3040 modules transformed`),产物 `internal/webui/dist/assets/index-ZcXUjQDW.js`(约 1.20 MB,gzip 约 377 kB)。
- **构建产物断言**:`assets/` 目录只含新哈希,`index.html` 引用新哈希;产物内 `lastProbeLatencyMs`、「是两件事」、「测试连接」三项 `grep -c` 均 > 0。
- **隔离实例冒烟**(独立端口 `8899` + 独立数据目录):`/healthz` 返回 `status:ok`;`/api/config` 的 version 为 `0.5.15`;**指向在听的端口** `http://127.0.0.1:8899` 探测 → `ok:true` 且 `latencyMs:0.701`;**指向未监听的端口** `http://127.0.0.1:8901` 探测 → `ok:false` 且原因为 `connection refused`;`GET /api/proxies` 中可用条目带 `lastProbeLatencyMs`、失败条目**不带该键**;带 `id` 调 `POST /api/proxies/test` 仍正常返回(编辑态试连路径可用);退出时日志出现 `shutdown signal received, draining` 与 `http server stopped`。

### 兼容性

- **API 只增不改**:`lastProbeLatencyMs` / `latencyMs` 是新增响应字段,旧客户端忽略即可;`POST /api/proxies/test` 的 `id` 是可选字段,不传时行为与 v0.5.13 完全一致。
- **磁盘格式未变**:`proxies.json` 结构未改(延迟随条目一起序列化,旧文件读入后该字段为零值,不影响加载),升级不需迁移。
- ⚠️ **探测语义变更须知**:「探测」不再回答「这代理能不能转发」,只回答「ip:端口连不连得上」。要判断代理是否真能工作,请用行内「测试」按钮或 `POST /api/proxies/{id}/test`(可带 `targetUrl`)。相应地,升级后**首次探测结果可能与升级前不同**——原先因协议不匹配而判失败的条目,现在可能转为可用。

### 轮次与号位

- 本轮占 **0.5.15**:探测口径修正属**缺陷修复**,弹窗内「测试连接」与「延迟」列属既有功能优化,按 `AGENTS.md` 判定取小版本(第 3 位)+1。
- `docs/ROADMAP.md` 已顺延:韧性轮 0.5.15 → **0.5.16**、工程化 0.5.16 → **0.5.17**;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。**历史小节里「留给韧性轮」的提法以 `docs/ROADMAP.md` 号位总览为准,现在指的是 0.5.16。**

## [0.5.14] - 2026-09-26

本轮主题:**设置页只留一个「编辑」按钮**。设置页原本有**两套编辑入口**——面板顶部一个「编辑」(点开所有字段一起改),以及 6 个字段各自带的「✏️ 编辑」(点开单个字段改)。两个入口做的是同一件事,反而让人拿不准该点哪个;6 个字段就是 6 个按钮,视觉噪音也大。本轮把字段级按钮全部删掉,只留顶部那一个:**一按即全字段可编辑,改完一次保存**。

### 变更

- **删除设置页 6 处字段级「✏️ 编辑」按钮**:仓库地址、展示名称、允许删除、允许拉取、热度保留天数、拉取平台白名单。这 6 个字段原先各带一个按钮,现在统一由面板顶部的全局「编辑」接管。
- **只读态在全局「编辑」按钮旁新增引导文案**:「点「编辑」后下面所有参数一起改,改完一次保存」。按钮没了之后,得让人知道该去哪改。
- **`ReadonlyValue` 组件去掉 `onEdit` 入参**:该组件退化为纯展示(只渲染当前值的灰底方块),不再携带任何编辑控件;顺带删掉组件内条件渲染按钮的整块死代码。

### 修复

- **字段级按钮调用的 `beginEdit` 签名不匹配**:`beginEdit` 自 v0.5.9「整页编辑」改造后已是**零参**(`const beginEdit = () => setEditing(true)`),但 6 个字段级调用点仍在传参,编译期即报 `error TS2554: Expected 0 arguments, but got 1`。本轮连同按钮一起删除,该错误不再存在。

### 验证

- `cd web && ./node_modules/.bin/tsc --noEmit` 与改前基线逐条比对(按「文件 + 消息」做多重集相减):**16 → 10**,消失的 6 条全部是 `web/src/pages/settings-page.tsx` 的 `TS2554`,**无新增**。剩余 10 条为既有基线(`api.ts` 缺 3 个类型名、`images-page.tsx` 2 条、`settings-page.tsx` 5 条 `TS6133` 未使用声明),非本轮引入。
- `npm run build` 退出码 0(`✓ 3040 modules transformed`)。
- **构建产物断言**(`internal/webui/dist/assets/index-BMDcO2Rx.js`):`✏️` 出现 **0 次**、`✏` 出现 **0 次**、引导文案「点「编辑」后下面所有参数一起改」出现 **1 次**、「保存所有修改」出现 **1 次**、`onEdit` 出现 **0 次**;源码 `web/src/pages/settings-page.tsx` 同样为 `✏️` 0 / `onEdit` 0 / `beginEdit` 2(声明与全局按钮各一处)。
- 改动全部落在 `web/src/pages/settings-page.tsx`,**15 insertions / 29 deletions**;Go 侧零改动。

### 兼容性

- **纯前端 UI 改动**:无新增或变更的 API,无磁盘格式变化,升级不需迁移。
- 编辑能力**没有减少**:原先用字段级按钮能改的 6 个字段,现在都能在全局编辑态里改。校验规则(仓库地址须带 `http(s)://`、热度保留天数 ≥ 1)与保存逻辑(成功 `已保存 N 项设置`、无变更 `没有变更`)均未改。

### 轮次与号位

- 本轮占 **0.5.14**:删冗余按钮、统一编辑入口属**既有功能优化**,按 `AGENTS.md` 判定为小版本(第 3 位)+1。
- `docs/ROADMAP.md` 已顺延:韧性轮 0.5.14 → **0.5.15**、工程化 0.5.15 → **0.5.16**;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。**历史小节里「留给韧性轮」的提法(B6 `events.go` 的 `UnixNano`、B4 `proxies` 缺 `writeMu`)以 `docs/ROADMAP.md` 号位总览为准,现在指的是 0.5.15。**

## [0.5.13] - 2026-09-26

本轮主题:**新增代理时先试连、再保存**。原来的流程是:填完地址 / 用户名 / 密码 → 保存 → 再用行上的「探测」验证配得对不对;配错了要回到编辑态改、再等一次探测。本轮把「验证」提前到弹窗内部——表单填完、点「保存」之前,就能用**当前键入的值**测一次连通性,失败就地改,不必先落一条错配置进库。

### 新增

- **「新增代理」弹窗内新增「测试连接」按钮**:不必先保存,直接用表单当前的地址 / 用户名 / 密码 / 测试目标发起一次真实探测,结果内联显示在弹窗里(成功给 `HTTP <status>` 与 `registryApiVersion`,失败给原因)。对应新端点 **`POST /api/proxies/test`**。
- **端点 `POST /api/proxies/test`**:请求体 `{url, username?, password?, targetUrl?}`。**不落库、不写 `proxies.json`、不记探测状态**——它只回答「这组值现在能不能通」,与库里条目无关,因此既不接 `id`,也不走「代理库不可用」那道门禁。`url` 缺失 → 400 `url is required`;地址非法或连不通 → **恒 200**,结果由 `ok` 字段表达(与既有 `POST /api/proxies/{id}/test` 的错误语义保持一致)。
- **弹窗内新增「测试目标(可选,不保存)」输入**:默认目标是本仓库自己的 `/v2/`,而一个只被允许出外网的代理拿本仓库当目标会测出假阴性,所以允许临时覆盖。该字段**只作用于本次测试**,不进创建请求、不落库。
- 抽出共享实现 `proxyURLFrom` / `proxyTestTarget` / `proxyTestThrough`;`TestProxy`(按 id 测)与新端点走同一段代码,不再各写一遍。

### 修复

- **地址漏写协议头时报错泄露实现细节**:填 `127.0.0.1:8080` 这类裸 `host:port` 时,标准库 `url.Parse` 会先于本项目的校验失败,把 Go 原始错误 `first path segment in URL cannot contain colon` 直接抛到界面上。现在在 `url.Parse` **之前**先判有没有 `://`,直接给「代理地址需要带协议头(如 `http://` / `socks5://`)」这类可操作的中文提示。**该问题由本轮隔离冒烟实测发现**,单元测试与既有入口都覆盖不到。

### 变更

- **弹窗底部改为自定义 footer,并补 `saving` 态**:插入「测试连接」后按钮区不再用 `Modal` 的默认 footer。自定义 footer 会一并丢掉默认的「确定按钮自动 loading + 提交中防重复提交」,故补上:提交期间主按钮转圈、取消与「测试连接」禁用;`handleSubmit` 入口加锁、`finally` 解锁——避免连点建出两条代理。
- **「测试连接」只在新增态出现**:编辑态隐藏该按钮。原因是编辑时密码不回显,用空密码去测会得到 407 之类的假阴性;编辑已有条目要测连通性,行上本来就有「测试」按钮。
- 弹窗内一对弯引号 “能不能出外网” 改为本文件既有习惯的 「能不能出外网」。

### 验证

- `gofmt -l internal cmd` 无输出;`go vet ./internal/...` 退出码 0;`go build ./...` 退出码 0;`go test -count=1 ./internal/...` 全部 `ok`。
- `cd web && ./node_modules/.bin/tsc --noEmit` 与基线**逐条比对**(按「文件 + 消息」做多重集相减):**16 → 16,无新增、无消失**。`npm run build` 退出码 0,产物内含 `/api/proxies/test` 与「测试连接」文案。
- **隔离实例冒烟**(独立端口 + 独立数据目录 + 内联假代理)12 项全部符合预期:正常路径 `ok:true` 且回显 `registryApiVersion:"2"`;`targetUrl` 缺失 / 空串等价,均回落默认 `/v2/`;显式 `targetUrl` 被回显且假代理确实收到该绝对 URL;凭据以 `Proxy-Authorization: Basic ...` 发出;假代理强制 407 → `ok:false`;端口未监听 → 连接被拒原因可见;缺 `url` → 400 `url is required`。
- **零落库已实证**:试连全程前后,数据目录里的 `proxies.json` **始终不存在**;随后用 `POST /api/proxies` 真建一条,该文件才出现(1 条),再按 id 试连仍 `ok:true`、`GET /api/proxies` 仍为 1 条——证明试连路径不产生任何条目。

### 兼容性

- **磁盘格式未变**:凭据库 / 代理库 / 热度库结构均未改动,升级不需迁移。
- 新增的是**端点**,不是数据结构:`POST /api/proxies/test` 为新增;既有 `POST /api/proxies/{id}/test` 的请求体、响应与错误语义均不变(仅内部改走共享函数)。
- 弹窗内的「测试目标」是**可选输入**,不参与创建请求,也不落库。

### 轮次与号位

- 本轮占 **0.5.13**:「保存前试连」是用户可感知的新能力(新按钮 + 新端点),按 `AGENTS.md` 的性质判定属中版本档;号位沿用 0.5.12 先例——真正的中版本号位 `0.6.0` 已由人指定留给 TLS,不占用(详见 `docs/ROADMAP.md`)。
- 原 0.5.13「韧性轮」顺延为 **0.5.14**,原 0.5.14「工程化」顺延为 **0.5.15**;`0.6.0` TLS 不随顺延改号。**因此 0.5.11 / 0.5.12 小节里"留给韧性轮"的两条(B6 `events.go` 的 `UnixNano`、B4 `proxies` 缺 `writeMu`)现在指的是 0.5.14。**
- 这是连续第四轮顺延,号位表见 [`docs/ROADMAP.md`](./docs/ROADMAP.md)。

## [0.5.12] - 2026-09-26

本轮主题:**代理交互——把「探测」从看不见的后台动作变成看得见的前台操作**。起因是 158 上点「探测」按钮没有任何反应:根因是 `web/src/pages/proxies-page.tsx` 调用了 `probeProxy()` 却**从未 import**(由 `f62a3e1` 引入),而 `"build": "vite build"` 不含 `tsc`,所以前端构建一直"成功"。顺带把同时提出的三条交互诉求一起做掉:删除只留一层确认、探测有进行中反馈、新增代理后立即探测,以及一个「一键探测全部」的入口。

### 修复

- **探测按钮点了没反应(本轮的真 bug)**:`web/src/pages/proxies-page.tsx` 补上 `probeProxy` 的 import。该符号被调用却从未导入,点击时抛 `ReferenceError`,被 React 事件处理吞掉,表现为"按钮能点、什么都不发生"。同一处还修掉了 `handleProbe` 的缩进错位。
- **删除代理的确认从两层降为一层**:去掉按钮外层的 `Popconfirm`(标题只有"确定删除?"),保留 `modal.confirm`——后者承载真正需要用户判断的信息("不会影响已完成的拉取,但引用它的任务会立即失败")。确认次数 2 → 1,影响面说明不变。

### 新增

- **探测进行中反馈**:逐行抽成共享的 `probeOne(id, name)`,带 `catch`(任何抛出转成可见的 `message.error`)和 `finally`(先清行内标志再 `refresh()`),不留陈旧状态。`probingIds[p.id]` 为真时状态列渲染 `<Tag color="processing">探测中</Tag>`,行内按钮文案变"探测中…"(操作列宽 230 → 250);批量探测期间头部按钮显示 `loading`,整个表格可见地处于"探测中"。
- **新增代理后立即探测连通性**:`handleSubmit` 保存成功后拿到新建条目,提示语改为"已创建代理,正在探测连通性…",随后复用同一个 `probeOne`。**刻意不做在 `CreateProxy` 里同步探测**——`Probe` 最坏情况要 5 秒(SOCKS5 拨号预算),会让"保存"按钮卡住;前端探测让弹窗立刻关闭而反馈照旧。
- **一键探测全部**:代理页头部新增「探测全部」按钮(`ThunderboltOutlined`,`proxies.length === 0` 时禁用),对应新端点 **`POST /api/proxies/probe`**。**放服务端而不是前端 for 循环**的原因:前端串行最坏 N×5 秒,服务端 `ProbeAll` 并行,最坏约 5 秒。响应恒为 200 + 摘要 `{total, ok, failed, unknown, results:[{id, name, ok, status, probedAt, error}]}`;`unknown` 用于"探测途中条目被删除",而不是把未报告谎报成失败。`ok === total` 走 `message.success`,否则 `message.warning` 并列出可用/不可用计数。

### 验证

- `gofmt -l internal cmd` 无输出;`go vet ./internal/...` 退出码 0;`go build ./...` 退出码 0;`go test -count=1 ./internal/...` 全部 `ok`。
- `cd web && ./node_modules/.bin/tsc --noEmit`:**`proxies-page.tsx` 的 TS2304(`probeProxy`)归零**,`api.ts` / `types.ts` 无新增错误。全量错误数 **17 → 16**:基线(HEAD)的 `proxies-page.tsx` 本来就带这条 TS2304(已实测:把 HEAD 版文件放回 `web/src` 跑一次 `tsc`,该文件 1 条错误),修完后全量 16 条,均为既有错误,不在本轮范围。
- 路由无歧义:`POST /api/proxies/probe`(2 段)与 `POST /api/proxies/{id}/probe`(3 段)段数不同;id 形如 `YYYYMMDD-HHMMSS-mmm-<8hex>`,不会与字面量 `probe` 冲突。

### 兼容性

- **磁盘格式未变**:凭据库 / 代理库 / 热度库结构均未改动,升级不需迁移。
- 新增的是**端点**,不是数据结构:`POST /api/proxies/probe` 为新增,既有 `POST /api/proxies/{id}/probe` 语义与响应不变。
- 每分钟后台探测**自 v0.5.9 起已存在**(`internal/server/server.go` 里 `StartProbeLoop(r.PullCtx, 60*time.Second)`),不是本轮新增——用户问的"增加后后续每分钟都定时探测"早已生效。

### 轮次与号位

- 本轮占 **0.5.12**:「一键探测全部」是用户可感知的新能力(新按钮 + 新端点),按 `AGENTS.md` 取中版本档。
- 原 0.5.12「韧性轮」顺延为 **0.5.13**,原 0.5.13「工程化」顺延为 **0.5.14**;`0.6.0` TLS 由人指定,不随顺延改号。**因此 0.5.11 小节末尾"留给 0.5.12 韧性轮"的两条(B6 `events.go` 的 `UnixNano`、B4 `proxies` 缺 `writeMu`)现在指的是 0.5.13。**
- 这是连续第三轮顺延,号位表见 [`docs/ROADMAP.md`](./docs/ROADMAP.md)。

## [0.5.11] - 2026-09-26

本轮主题:**hotfix — 收口 0.5.10 没修完的 ID 唯一性**。0.5.10 把 `randHex()` 的熵源从 `time.Now().UnixNano()` 换成了 `crypto/rand`，但随机后缀仍只有 **4 个 hex 字符(16 bit)**。400 个并发调用落在同一毫秒时，16 bit 的期望撞车数约 **1.2 次**——测试连跑 20 次有 **13 次 FAIL**。`newID()` 产出的是凭据库 / 代理库的**存储主键**，撞车等于静默覆盖(丢数据)，所以本轮把后缀加宽到 **8 个 hex 字符(32 bit)**，同场景期望撞车降到 **≈2e-5**。除此之外均为收尾。

### 修复

- **`internal/api/handlers_extra.go` `newID()`**：随机后缀 `randHex(4)` → `randHex(8)`。id 形状从 `20260926-111545-103-a83a`（秒级时间戳 + 毫秒 + 4 位随机）变为 `20260926-111545-103-a83ac1f7`（毫秒后跟 **8 位**随机）。这是本轮唯一的行为变更。
- **`internal/api/id_internal_test.go`**：同步加宽形状断言（`+1+8`、`len(parts[3]) != 8`），并把 `TestNewIDUniqueUnderConcurrency` 的注释补上 16 bit / 32 bit 撞车率对照。

### 验证

- `go test -count=20 -run TestNewIDUniqueUnderConcurrency -v ./internal/api/`：改前(16 bit) **13/20 FAIL** → 改后(32 bit) **20/20 PASS**。
- `go test -race -count=1 ./internal/...` 全绿；`go vet ./internal/...` 干净；`gofmt -l internal/api` 干净。

### 兼容性

- **磁盘格式没动**：凭据库 / 代理库按 JSON object 的 key 存 id，4 位后缀的旧 id 与 8 位后缀的新 id 可以共存，升级不需要迁移。
- 只跑过 0.5.10 的实例直接升 0.5.11；还停在 0.5.9 及更早的实例建议一步跳到 0.5.11（0.5.10 的凭据库死锁修复也包含在内）。

### 文档 / 收尾

- 代码注释里 **8 处**把「代理连通性监测」误标成 `v0.5.10` 的地方统一改回 `v0.5.9`（该功能随 0.5.9 发布），兑现 0.5.10「已知遗留」里「留待统一清账」的承诺。
- `.gitignore` 第 36 行的通配符粘连（`*.swp.tmp_nginx_header.conf`）修成 `*.swp`。
- 新增 [`docs/ROADMAP.md`](./docs/ROADMAP.md)：排定后续号位。原 0.5.11「韧性轮」顺延为 **0.5.12**、原 0.5.12「工程化」顺延为 **0.5.13**；`0.6.0` TLS 由人指定，不随顺延改号。
- 0.5.10「已知遗留」里另外两条仍开着，留给 0.5.12 韧性轮：`internal/events/events.go` 的 `UnixNano` 事件 ID（B6）、`internal/proxies` 缺 `writeMu`（B4）。

## [0.5.10] - 2026-09-26

本轮主题:**hotfix — 修生产环境凭据库死锁,以及 ID 撞车导致的静默丢数据**。158 上「UI 点不动、新建凭据后整站卡死」的根因**不在前端**:凭据库 `Put` 在持有写锁的情况下做 AES 加密 + 磁盘落盘,返回时没有释放写锁,此后任何 `List` / `Get` 永久阻塞。前端所有请求都没有超时,于是每个按钮都卡在 loading —— 看起来像「前端卡死」,实际是后端把锁漏了。

### 修复

- **`internal/credentials/credentials.go`**:`Put` / `Delete` 泄漏 `sync.RWMutex` 写锁。改成「锁内取快照 → 锁外写盘」(`snapshotLocked()` + `persistToDisk()`),与 `internal/proxies/proxies.go` 已有的正确范式对齐。新增 `writeMu` 串行化落盘(锁序恒为 `writeMu` → `mu`),顺带消除并发写乱序造成的「重启后凭空少几条」。**这是用户报告的生产 bug 的根因。**
- **`internal/api/handlers_extra.go` `randHex()`**:原实现用 `hex[time.Now().UnixNano()%16]` 逐字符拼接,每个字符还 `time.Sleep(1µs)`,并发下后缀高度相关。实测 20 个并发创建只产生 **19 个不同 id**,重复 id 在写入时按 id 覆盖 → **静默丢数据**(凭据/代理凭空消失)。改用 `crypto/rand`;熵源不可用时降级到时序熵而不 panic。凭据库与代理库两处 `newID()` 都受益。
- **`internal/pull/queue.go` `jobCounter`**:原来 `jobCounter++` 在 `e.mu.Lock()` **之前**执行,属数据竞争。改成 `atomic.Uint64` + `Add(1)`。

### 测试

- 新增 `internal/credentials/credentials_test.go`(8 个用例):含 500ms 超时型的死锁回归测试、`-race` 并发写压测、磁盘格式往返(既有凭据文件仍可解密)。
- 新增 `internal/api/id_internal_test.go`:400 个 goroutine 齐发断言 id 唯一;另测 `randHex` 形状与分布。
- `go test -race -count=1 ./internal/...` 全绿;`go vet ./...` 干净;`gofmt -l .` 干净。

### 验证(A/B 对照)

同一台机器、同一份 E2E 脚本,只换二进制:

- 旧二进制(sha256 `a52849ff…`):`returned 20 ids, 19 unique -> ID COLLISION`、凭据库 20 条(应为 21)、**FAILURES=2**。
- 新二进制(sha256 `75d33637…`):20/20 唯一、21 条、重启后仍 21 条、**FAILURES=0**。

### 影响 / 升级

- **磁盘格式没动**:凭据库仍是 JSON object + `{v,nonce,ct}` AES-256-GCM 信封,既有文件可继续解密,升级不需要迁移。
- **升级后立即恢复**:换 0.5.10 镜像后,UI 新建 / 编辑凭据不再卡死。
- **旧版本里已经因 id 撞车被覆盖掉的凭据找不回来**,需要重新录入。

### 已知遗留(下一轮)

- `internal/events/events.go:587` 的 `local-%d` 仍只取 `UnixNano`,同型理论重复,未纳入本次 hotfix。
- `internal/proxies/proxies.go` 缺同型 `writeMu` 加固(锁泄漏已修,写乱序尚未)。
- 代码注释里若干处把「代理连通性监测」标成 `v0.5.10`,该功能实际随 0.5.9 发布;本轮不改,留待统一清账。
- TLS 全套顺延到下一版。

---

## [0.5.9] - 2026-09-26

本轮主题:**配置单源化 — UI 唯一入口;env 只剩 5 个基础设施**。v0.5.8 之前,设置页上每个字段都带一个紫色「环境变量」标签,说明当前值是 `.env` 来的还是 UI 改的;Mutable > env 的双重优先级让运维首部署时被「我改了 UI 但 .env 里还有同一个值,到底用哪个?」反复困扰。本轮把业务配置全部砍成 UI 唯一来源,env 只剩 5 个基础设施变量(PORT / DATA_DIR / STORAGE_DIR / CREDENTIAL_KEY / GO_HUB_ENV)。

本轮同步文案重命名:「默认上游地址」→「仓库地址(/前缀)」。「上游」一词让运维以为配的是上游镜像源,实际这个字段配的就是 cairn 自己对外暴露的地址(docker login / docker push 连的就是它)。

### 新增
- **代理连通性自动监测**(v0.5.9 hotfix):每个代理条目后台每 60 秒探测一次可达性,
  状态写入 `Proxy.LastProbeAt` / `LastProbeStatus` (`ok` / `failed` / `unknown`)/ `LastProbeError`。
  探测目标:**proxy endpoint 本身**(TCP dial + 短 HEAD),不依赖 docker.io 是否可达。
  启动时 ProbeAll 一次 + 后台 ticker。代理管理页加「状态」「最后探测」两列 +
  每行「探测」按钮(立即触发 POST /api/proxies/{id}/probe);拉取任务表单的下拉里
  不可达代理加 `[不可用]` 标签 + disabled(仍可选,带 Tooltip 提示)。
- **`internal/config.Config` 13 个无 env fallback 的 read helper**:之前 11 个 `EffectiveXxx()`
  函数每个都做「Mutable override > env value > hardcoded default」三段判断,
  现在 env 那段整个砍掉,逻辑简化为「Mutable override 或 hardcoded default」。

### 变更
- **`internal/config.Load()`**:不再读取 `REGISTRY_URL` / `REGISTRY_PROXY` / `REGISTRY_USERNAME` /
  `REGISTRY_PASSWORD` / `REGISTRY_NAME` / `REGISTRY_NOTIFY_TOKEN` / `REGISTRY_ALLOW_*` /
  `REGISTRY_PULL_PLATFORMS` / `REGISTRY_PULL_HISTORY_RETENTION_DAYS` /
  `REGISTRY_STATS_RETENTION_DAYS` / `REGISTRY_STATS_IGNORE_USERAGENTS` 等业务 env。
  设了也不再读,会被 panel 值覆盖。
- **`internal/config.Config` struct 砍 9 个字段**:`RegistryURL` / `RegistryProxy` /
  `RegistryUsername` / `RegistryPassword` / `RegistryName` / `AllowDelete` / `AllowPull` /
  `NotifyToken` / `AllowRegistryEvents` / `StatsRetentionDay` /
  `StatsIgnoreUserAgents` / `PullPlatforms` / `PullHistoryRetentionDay` /
  `PullQueueSize` / `CacheTTL`(死的)/ `StatsAggregationInterval`(死的)全部移除。
- **`cmd/server/main.go` slog.Info**:启动日志只打 `port` / `env` / `credentials_dir` /
  `storage_dir`,不再打印业务字段。
- **`internal/api/handlers.go` MutableSettings 砍 11 个 `*Source` 字段** + 删 `src()` dead helper。
- **`internal/api/handlers_extra.go` ignoreRules.env 渲染分支移除**。
- **`internal/pull/executor.go`**: `pull.NewExecutor` 第一参直接传 `50` 常量,
  `PullQueueSize` 既不是 env 也不是 panel-tunable 的。
- **`web/src/pages/settings-page.tsx`**:
  - 「默认上游地址」→「仓库地址(/前缀)」语义重整
  - 7 个 Form.Item 全部改成「灰显 + ✏️ 编辑 → 点开改 → 保存」模式
  - 删 `SourceTag` 组件 + 7 处 inline 标签 + 「保存全部设置」按钮
- **`web/src/types.ts` MutableSettings 删 9 个 `*Source` 字段**。
- **`web/src/pages/proxies-page.tsx`** 加「状态」「最后探测」列 + 「探测」按钮 +
  `handleProbe` + `probingIds` state。
- **`web/src/pages/pull-page.tsx`** 代理下拉每项加 `[可用]/[不可用]` 标签,
  不可达加 `disabled`(仍可在 advanced 选项里选)。

### 修复
- 顺手 `gofmt -w` 了 0.6.0 commit 残留的 handlers.go 缩进 + executor_test.go 末换行。

### 影响 / 升级
- **数据无破坏**:SQLite schema 没动,settings 表所有键/字段/默认值不变。
- 之前 UI 改过的所有 `Mutable` override 一行一行都还在工作。
- 升级前 `.env` 里如果有 `REGISTRY_URL/PROXY/NAME/NOTIFY_TOKEN` 等,升级后会被忽略,
  行为以 UI 为准(默认值跟 v0.5.8 的 env 行为一致)。
- **代理连通性监测**:158 这类断网环境现在 UI 上立刻能看到哪条代理挂了,
  不会再因为拉镜像 timeout 才知道。
- `go test ./...` 全绿;`gofmt -l .` 干净。

### 文档
- `.env.example` 从 27 个 env 砍到 5 + 构建期。设了也没用的旧业务 env
  会被忽略,容器 restart 后行为不变。
- `README.md` 当前状态 / 版本号 → 0.5.9。
- `CHANGELOG.md` 新增本节。
- `AGENTS.md` env-rule 新增门槛(新加业务 env 必须先回答「为什么不能走 UI?」)。

### 留到下一版
- TLS 全套:DB `tls_certificates` 表 + 同端口根据 mode 切换 +
  上传证书 + 生成自签名 + 热加载。
  (原写「留到 v0.5.10」,但 v0.5.10 已先作为死锁 hotfix 发版,故顺延。)

---

## [0.5.8] - 2026-09-26

本轮主题：**热度开箱即用**。v0.5.7 之前，热度统计依赖「事件共享密钥 + 外部 registry 的 notifications webhook」——自带 registry 的一次 push/pull 不会进入热度表，运维要么搭一套 Distribution 自己接 webhook，要么看不到数据。本轮把热度链路从「可选外部 webhook」改成「自带 registry 就地喂事件 + 外部 registry webhook 仍可选」两轨并行：默认就有数据，外接依然能接。

> **本节同时收录「拉取平台白名单」条目**（原独立成 `[0.6.0]` 章节）。该功能在提交 `8836c92`（2026-09-26 00:48）里被标为 `v0.6.0`，但紧接着的下一笔提交 `6aba42b` 就把热度功能标成了 `v0.5.8` —— 版本号自此**下调**回 0.5.x 线，**`v0.6.0` 从未作为发布版本存在，仓库里也没有对应 tag**（`git tag -l` 为空）。为避免 CHANGELOG 出现「0.6.0 排在 0.5.8 之前」的乱序，原 `[0.6.0]` 章节已并入本节；其中随 0.5.9「配置单源化」被移除或更名的细节，已在条目内就地标注。
>
> 动机：v0.5.x 之前，拉一个多架构 index（比如 `nginx:alpine`、`clickhouse/server`、`alpine`）会把上游全部 ~10 个平台的子 manifest 都拉下来——单架构 / 双架构部署因此吃下大量用不到的层。

### 新增

- **`internal/events.Handler.IngestLocal(ev)`**：内置 registry 在 `/v2/<repo>/manifests/<ref>` HEAD/PUT 成功后构造一个 `Event`，通过这个方法把热度直接喂进与 webhook 完全相同的 `processOne` 管线。复用 `ShouldCount` 过滤（白名单 manifest 媒体类型、HEAD/PUT 才计数、UA 忽略规则、self-fold 语义）—— 一处过滤规则，两个入口。返回 `bool`：true = 已计入（accepted+1 + SQLite + 最近事件环），false = 被过滤或 kill switch 关闭。
- **服务端：`eventsHandler` 现在只看 SQLite 是否就绪就构造**，不再要求 `REGISTRY_NOTIFY_TOKEN` 非空。`/api/events` 在 token 空时仍然挂载（fail-closed 401），行为与 v0.5.4 一致；自带的 `/v2/*` 不再走 webhook 绕一圈。
- **`internal/events/internal/events/local_test.go`（新文件，7 个测试）**：`CountsPull` / `RespectsKillSwitch` / `NilHandler`（nil `*Handler` 不 panic）/ `IgnoreRuleFolds` / `SelfPushCounted`（自写 PUT 仍计）/ `WrongMethodRejected`（GET 不计）/ `NewHandler_EmptyIgnore` 构造健壮性。
- **`internal/registryd.Handler.Events *events.Handler`** 字段 + `New(store, getCreds, eventsH)` 第三参数；`localEvent(repo, tag, mediaType, action, method, r)` helper 把 `r.UserAgent()`、`r.Host`、`r.RemoteAddr`、`X-Auth-User` 头映射到 `Event` 的对应字段。
- **拉取平台白名单 `pull.platforms`**（设置页 → "拉取平台白名单"）。可选值是 `<os>/<arch>[/<variant>]` 的 CSV（例：`linux/amd64,linux/arm64` 或 `linux/amd64,linux/arm/v7`）；空 = 不过滤（保留 v0.5.x 的"全部平台"行为）。支持 `linux/amd64` / `linux/arm64` / `linux/arm/v7` / `linux/386` / `linux/ppc64le` / `linux/s390x` / `linux/riscv64` / `windows/amd64` 等常见架构的 chip 多选；自定义 token 也能从原始 CSV 输入。配置后只拉这些平台的子 manifest 与它们独有的 blob——alpine 这种"所有平台共享同一层"的镜像虽然表面 size 没变化，但拉取耗时显著下降（少 16 次子 manifest + blob-existence HEAD 请求）。
- **环境变量 bootstrap `REGISTRY_PULL_PLATFORMS`**：首次部署时不用先打开 UI 改设置，直接在 `.env` 里写一行 `REGISTRY_PULL_PLATFORMS=linux/amd64,linux/arm64` 重启即生效；之后改设置页会覆盖 env（与 `REGISTRY_URL` / `REGISTRY_PROXY` 同样的 Mutable > env 优先级）。（⚠️ 0.5.9 起该 env 已随「配置单源化」移除，设了也不再被读——见 `[0.5.9]` 的 `Load()` 条目。）
- **空匹配保护**：白名单过滤后如果一个子 manifest 都没命中（例如镜像只有 `linux/arm64` 你却写了 `linux/amd64`），`planTransfer` 立刻报错 `platform filter [...] matched no child manifests in the source index`，不会静默产出空 index 把后续 `docker pull` 全打挂。
- **未知 platform 不被悄悄丢**：上游写 `architecture: "unknown"` 或缺字段的子 manifest 会原样保留——宁可多拉一个也优于静默丢失上游给的唯一 manifest。

### 变更

- `internal/events/events.go` 把 ServeHTTP 里的 per-event 循环抽成 `processOne(ctx, ev, now, ignore) bool`；webhook 和 `IngestLocal` 都走这个 helper，杜绝规则双份维护。
- `internal/server/server.go` 第 7 步构造门槛注释更新为 v0.5.8 语义（SQLite 即可、token 空时 fail-closed）。
- 前端 **设置页删除「接收 registry events」开关**：自带的 registry 现在永远是热的，UI 上的开关对用户没有意义。`allow.registry_events` env + DB 列保留为隐藏的全局 kill switch（默认 true，运行时生效）；服务器层不变，UI 层不暴露。
- 前端 **统计页文案重写**：
  - 「还没配置事件共享密钥」警告分支**移除**——token 现在不是默认路径，少了它属于「你想接外部 registry 还没接」而不是「自带的也算不出」。
  - 「还没收到任何热度事件」info 分支改为「自带 registry 已经自动计入；如果你想让外部 registry 也算进来，再去配 `REGISTRY_NOTIFY_TOKEN`」。
  - 「registry 侧需要这样配」面板标题改为「外部 registry 需要这样配（可选）」；Collapse 标签同步收紧。
  - `NOTIFY_CONFIG_YAML` 上方加一行注释：this snippet is for EXTERNAL registries。
- `internal/api/api_test.go` 两处 `registryd.New(store, nil)` → `registryd.New(store, nil, nil)`（新签名第三个参数是 events handler，测试不关心传 nil）。
- `MutableKeys` 新增 `pull.platforms`，类型 `stringcsv`（新增的第三种类型：字符串 + 内置 CSV 校验；每个 token 必须严格 `<word>/<word>[/<word>]`，空段、含空格、非 ASCII 都被 400 拒绝）。
- `MutableSettings` 新增 `pullPlatforms` 字段，UI 顶部 / 设置页立即可读（当时另有 `pullPlatformsSource` 字段标记取值来源；该字段已在 0.5.9 随「`MutableSettings` 砍 11 个 `*Source` 字段」一并删除）。
- `config.EffectivePullPlatforms()` 新增 helper：读 Mutable > env，空值返回 `nil`（= 不过滤），CSV 自动 trim + lowercase。（0.5.9 起 env 分支整个砍掉，等价能力改由 `config.Config.PullPlatforms()` 提供，语义不变。）
- `pull.planTransfer()` 签名增加 `platformAllow []string` 参数；过滤逻辑使用新加的 `sourcePlatformRef.key()` / `matchAny()`。
- `pull.executor` 引入 `manifestFetcher` interface（仅含 `GetManifest`），让 planTransfer 单元测试可以注入 fake，无需 HTTP mock。

### 修复

- 没有功能修复；本轮纯补全既有热度功能。

### 测试

- `internal/pull/executor_test.go`（新文件，**6 个**测试）：`TestPlatformKey`（`key()` 处理 nil / 缺字段 / 大写归一化）、`TestPlatformMatchAny`（严格匹配：`linux/arm` ≠ `linux/arm/v7`）、`TestPlanTransferIndexFiltering`（白名单命中 1 个子 manifest）、`TestPlanTransferEmptyAllowListMatchesAll`（allow-list 为空时命中全部）、`TestPlanTransferNoMatchErrors`（白名单 0 匹配时报错）、`TestPlanTransferSingleArchManifestUnaffected`（单架构 manifest 走原路径、不受 filter 影响）。

### 文档

- `.env.example`「镜像热度」块改写：明确写「自带 registry 不需要任何配置；下面这些只对外部 registry 生效」，并把每个变量的当前角色逐条注释。
- `README.md` 当前状态 / 版本号 → 0.5.8。
- CHANGELOG（本文）。

### 影响 / 升级

- **0.5.7 → 0.5.8 数据无破坏**。SQLite schema 没动、`activity_daily` 表结构没动、in-memory 计数器本就是重启归零。
- 升级后**立即生效**：第一次有人对自带 registry 做一次 `HEAD /v2/<repo>/manifests/<tag>` 或 push 一层，Top 榜单就会出数据，不需要重启、刷新、或重新配 webhook。
- 如果你之前配置了 `REGISTRY_NOTIFY_TOKEN` + 外部 Distribution 的 notifications：仍然有效，新版本是两轨并行而非替换，外部 webhook 进 `/api/events`，自带 registry 走 `IngestLocal`，最终都会进同一个 SQLite 表。
- 如果你想接的「外部 registry」其实是另一个 cairn 实例：那条 `notifications.url` 仍然走 `POST /api/events`，共享密钥照旧；自带这一侧的 `/v2/*` 流量还会通过本实例的 `IngestLocal` 自己计一次（这是对的：另一台实例通过 `/v2/*` 拉取镜像时，事件应当算到"镜像存放方"，不是"镜像来源方"，如果想反着算就在那个实例上关 `allow.registry_events`）。

---

## [0.5.7] - 2026-09-25

本轮主题：**拉取任务的阶段明细**。展开一个拉取任务行，现在能看到每一步的进度——与 registry-manager 的展开视图对齐——而不是只有一条总进度。

### 新增

- **拉取任务 phases[] 阶段明细**（UI v0.5.0 就预留、后端一直返回空数组的数据通路补上）：执行器为每个步骤 emit 一条 phase——`manifest`（抓取/展开源 manifest，多架构索引标注平台数）、`config`（单平台镜像的 config blob，独占一行）、`blob #N`（每层 digest + 实时字节进度 `done / total`）、`child-manifests`（多架构索引写子 manifest）。每行显示 digest 短形式、字节进度与状态；digest 悬停可见完整值。
- **blob 级实时进度**：`transferBlob` 用计数 reader 包装源响应体，边读边上报累计字节——大层在传输中即显示中间态（如 `12.3 MiB / 27.0 MiB`），任务总进度条也改为按实际传输字节推进（此前每个 blob 完成后才整块累加 announced size；跳过的 blob 仍计入总量，保证进度条能到 100%）。
- **已存在的 blob 标为 `已存在，跳过`**（灰色）而非假装重新下载——同一镜像第二个 tag 的拉取能清楚看到哪些层复用了。
- **终态收尾**：任务失败 / 取消后，running / pending 的 phase 不再停留在"进行中"，统一标为 failed 并写明停在哪一步（`执行失败` / `任务已取消`），展开区直接看到任务死在哪一步。

### 变更

- `/api/pull/jobs` 系列响应的 `phases` 字段从恒为空数组变为真实数据（`pull.JobView.Phases` 以 copy-on-write 更新，轮询拿到的快照不会被后续变更污染）；**前端零改动**——`JobPhases` 渲染组件 v0.5.0 起就在等这份数据。
- `transferBlob` 签名变更：返回 `(written, skipped, err)` 并接受进度回调；不再使用的 `size` 参数移除。

### 测试

- `internal/pull/phase_test.go`：5 个测试——Phase JSON 键与前端 `PullPhase` 完全一致（含 `totalBytes: null` 语义）、copy-on-write 快照不被后续变更污染、Submit 预置 pending manifest phase、失败任务收尾把 straggler phase 标为 `执行失败`、取消任务把 in-flight phase 标为 `任务已取消`。

---

## [0.5.6] - 2026-09-25

本轮主题：**修好 blob 下载流**。0.5.5 修掉 Docker Hub 匿名 401 后，拉取第一次走到 blob 阶段，暴露出 v0.2 就存在的流式误用。

### 修复

- **blob 下载报 `http2: response body closed`**：`GetBlob` 复用 `doRequest`，而后者会把响应体全部读进内存（32MB 上限）并 `defer resp.Body.Close()`——`GetBlob` 返回给调用方的 `resp.Body` 是已抽干、已关闭的死流，拉取执行器第一次读就报错，blob 全部传输失败。manifest 抓取不受影响（小 JSON、按字节消费），blob 是流式（可达数百 MB）必挂。此前未暴露是因为外部源拉取一直卡在 manifest 阶段的 401。现在 `GetBlob` 自建请求、保持活流返回，并内联实现与 `doRequest` 相同的 401→Bearer token 重试（含二次 401 时失效缓存 token）。
- **Bearer 重试被 `SetBasicAuth` 降级回 Basic**：`doRequest` 的重试请求先 `Set("Authorization", "Bearer ...")`，随后若配置了用户名密码又调 `SetBasicAuth`——后者整体替换 Authorization 头，把重试降级成 Basic 认证。Docker Hub 类 registry 对业务请求只认 Bearer，会再次 401 并失效刚取到的 token。现在重试只带 Bearer（token 已封装凭据），Basic 仅用于向 token 端点换 token。影响所有配置了凭据的外部源 manifest/blob 抓取。

### 新增

- `internal/registry/blob_test.go`：3 个回归测试——GetBlob 返回可完整读出的活流（~6MB payload，钉死修复前 `http2: response body closed` 场景）、blob 下载经 401→token→Bearer 重试后仍流式返回（且匿名 token 请求不带 Authorization 头）、带凭据 registry 的 manifest Bearer 重试不得降级回 Basic（token 端点收到 Basic 凭据、业务请求收到 Bearer）。

---

## [0.5.5] - 2026-09-25

本轮主题：**修好 Docker Hub 匿名拉取**。外部源（Docker Hub 等）配了代理仍 401 的根因是匿名 token 请求误带空 Basic 凭据头。

### 修复

- **Docker Hub 匿名拉取 401 `incorrect username or password`（表现为 `401 unauthorized (no bearer challenge)`）**：`fetchTokenOnce` 只判断 `basicAuth != nil`，而拉取链路对匿名源恒传非 nil 的空结构体，导致匿名 token 请求带上 `Authorization: Basic Og==`（base64 的空用户名:空密码）。auth.docker.io 把空 Basic 头当错误凭据拒绝（158 实测 curl：无头 → 200 拿到 token，空头 → 401 "incorrect username or password"），外部源拉取全部失败。修复对齐 registry-manager 的 node 实现（`server/registry-client.mjs` 只在 `auth.username` 非空时拼 Basic 头）：username 为空一律不带 `Authorization` 头。影响所有匿名 Docker Hub / ghcr / quay 源的拉取。
- **bearer token 获取失败时报错张冠李戴**：`client.go` 的 401 处理在 `terr != nil`（token 获取失败）时也报 "(no bearer challenge)"，且请求失败分支 wrap 的是恒为 nil 的 `terr`，真实原因被吞成 `request failed` 裸文案。现在 token 获取失败报 `bearer token fetch failed: <真实错误>`，挑战头缺失才报 `no bearer challenge in WWW-Authenticate`，排查不再被误导。
- `cmd/server/main.go` 补文件尾换行（gofmt 达标，纯格式）。

### 新增

- `internal/registry/bearer_test.go`：3 个回归测试——匿名（空用户名）请求不得携带 `Authorization` 头（测试服务器模拟 auth.docker.io 拒绝任何 Authorization 头的行为，即本 bug 场景）、带凭据时必须携带 Basic 头、`Bearer realm="...",service="..."`（逗号后无空格，Docker Hub 实际格式）的挑战头解析。

---

## [0.5.4] - 2026-09-25

本轮主题：**让"设置页可改的字段"真正在运行时生效**。此前多处代码读的是启动时缓存的 env 值，而不是 SQLite 里的热改覆盖，导致设置页改了不生效。

### 修复

- **设置真生效**：`GetConfig` 顶层的 `Name` / `AllowDelete` / `AllowPull` / `StatsEnabled` / `AllowRegistryEvents` / `StatsRetentionDays` 改为读 `Effective*()`（Mutable 覆盖优先，回落 env）。之前读的是 `cfg.Xxx` 启动快照，设置页改完 `/api/config` 仍显示旧值。
- **权限闸门接上热改**：`DeleteTag`（删除）、代理拉取、registry events 三处闸门改读 `Effective*()`，nil-safe 且 fail-closed（`Full` 为空时拒绝）。之前只在启动时读一次 env，运行中改 `allow.delete` / `allow.pull` 不影响已构造的 handler。
- **events 运行时开关**：`events.Handler` 新增 `SetEnabled(func() bool)` 谓词，`ServeHTTP` 在方法检查后加 403 闸门；server 构造时注入 `cfg.EffectiveAllowRegistryEvents`。一处覆盖 `/events` 与 `/api/events` 两个挂载点，改 `allow.registry_events` 立即生效，不用重启。
- **删调试残留**：`internal/registry/client.go` 删除 3 行 `DEBUG bearer` 日志（token 获取路径上的临时打印）。

### 变更

- **`cache.ttl.seconds` 从可编辑集摘除**：v0.5.0 后清单直读本地存储，`registry.CachedRegistry` 已无构造点，这个 TTL 没有任何消费方，设置页改它完全没效果。从 `config.MutableKeys` / `MutableFieldType` 和设置页输入框移除；`EffectiveCacheTTLSeconds()` 一并删除。`REGISTRY_CACHE_TTL_SECONDS` env 与 `AppConfig.cacheTtlSeconds` 保留为**只读展示**，不破坏既有 `.env` 契约。
- **`stats.retention.days` 终于有消费点**：新增 `retentionLoop`（启动后延迟 30s 首跑，之后每 24h 一轮，每轮重读 `EffectiveStatsRetentionDays()`），调用 `db.RetentionCleanup` 按 cutoff 删除过期的 `activity_daily` 行。此前该函数全仓无调用点，热度表无限增长，设置形同虚设。
- **`ConfigExtras` 精简**：只保留 env-only 的 `IgnoreUserAgents`；凡可热改的字段一律走 `Full.Effective*()`，避免 env 快照与 DB 覆盖两套来源打架。
- **`Runtime` 持有 `Cfg *config.Config`**：让 `resolveSource` 等运行期逻辑能读到全局 HTTP 代理（`registry.proxy` 设置）等热改值。
- **前端契约对齐**：`web/src/types.ts` 的 `MutableSettings` 补齐 `usingAuth` / `allowDelete(+Source)` / `allowPull(+Source)` / `allowRegistryEvents(+Source)` / `statsRetentionDays(+Source)`；`settings-page.tsx` 移除 `cache.ttl.seconds` 输入框与相关 state/patch 段。

### 升级提示

- 若 SQLite 里已存在覆盖值（例如 `allow.delete=false`、`allow.registry_events=false`、`registry.name`、`registry.proxy`），升级到 v0.5.4 后这些覆盖会**真正生效**：删除端点返回 403、事件采集停止、代理拉取走覆盖的 proxy。如需恢复默认，在设置页改回或清空对应覆盖即可。

---

## [0.5.3] - 2026-09-25

### 新增

- **Registry 自认证（docker login）**：`/v2/*` 现在支持 Basic auth；客户端 `docker login <url>` 后才能 push/pull。`GET /v2/` 总是返回 200（OCI spec ping），但带 `WWW-Authenticate: Basic realm="cairn"`，触发 docker daemon 用 basic creds 重试。
- **设置页可热改**：`registry.username` / `registry.password` 加到 MutableKeys，UI 上是 Input + Input.Password；密码不回显（服务端从不把 password 字段写进 GET /api/config 的响应），用户输入即覆盖。
- **`basicAuthCreds` callback** 注入到 `registryd.New(store, getCreds)`：每次请求都调 `cfg.EffectiveRegistryUsername/Password()`，所以 settings-page 改完立即生效，**不需要重启**。

### 变更

- `config.MutableKeys` 从 8 加到 10。
- `Config.EffectiveUsingAuth()` 派生方法：username + password 都非空时为 true。
- `AppConfig.MutableSettings` 加 `usingAuth` 字段（`Mutable` 不再藏 bool，由服务器算出）。
- `UsingAuth` 字段改为读 `EffectiveUsingAuth()` 而不是 `cfg.RegistryUsername != ""`。
- `internal/api/api_test.go`：调用 `registryd.New(store, nil)`（测试不启用 auth）。
- 文档：`REGISTRY_USERNAME/PASSWORD` 从 v0.5.0 commit message 里的"已废弃"恢复为 v0.5.3 的"v0.5+ 是 registry 自身 Basic auth"。

### 安全

- 密码以**明文**写入 SQLite settings 表（`/app/data/cairn.db`）。cairn 当前数据卷保护靠 docker + 宿主机 FS 权限，不假设 SQLite 内部加密。
- 密码比较用 `crypto/subtle.ConstantTimeCompare`，避免 timing 侧信道泄露长度。
- 401 响应统一 `{errors:[{code:UNAUTHORIZED,message:authentication required}]}` + `WWW-Authenticate: Basic realm="cairn"`。

### 测试

- `go build ./...` ✅ · `go vet ./...` ✅ · `go test ./...` 全绿
- `gofmt -l internal/` 0 个未格式化

### 已知问题（v0.5.x 跟进）

- 158 上 `intranet-53` 测试凭据 / webhook / 卡死 job 待清理（沿用 v0.5.1）
- 现在密码以 plaintext 存于 SQLite；如要加密后续加 KMS / SOPS / OS keyring 适配
## [0.5.2] - 2026-09-25

### 新增

- **设置页全部可热改**：除 `REGISTRY_CREDENTIAL_KEY` / `PORT` / `HOST_PORT` / `REGISTRY_CREDENTIALS_DIR` / `REGISTRY_STORAGE_DIR` 这几个"改动后必须重启"的字段外，所有其他 `REGISTRY_*` env 都可以在设置页直接编辑，写到 SQLite，热替换 `cfg.Mutable`，无需重启。
- **新增可改字段**：`registry.proxy` / `registry.name` / `cache.ttl.seconds` / `allow.delete` / `allow.pull` / `allow.registry_events` / `stats.retention.days`（加上已有的 `registry.url`，共 8 个）。
- **`config.MutableKeys` 白名单**：服务端只接受这 8 个 key 的 PATCH；其它字段（包括 secret / 路径）一律 400 拒绝。
- **通用 `PATCH /api/config`**：body 改为 `{"mutable": {"<key>": "<value>"}}`，一次可改多个字段，按 key 验证类型（`bool` / `int` / `url` / `string`）。
- **UI 重构**：
  - Descriptions 描述式只读布局 → Form 表单式可编辑布局
  - 每个可改字段旁挂 **「界面设置 (已覆盖) / 环境变量」** SourceTag，明示当前生效值的来源
  - "保存全部设置"按钮：diff 提交，只 PATCH 实际改动的字段（节省 db 写入）

### 变更

- `config.Mutable` 内部从单字段 `registryURL` 改造成 `map[string]string` 通用 key->value 存储；`Has(key)` / `Get(key)` / `Set(key, val)` 是统一接口。
- `Config.EffectiveXxx()` 现在覆盖 8 个字段，每个都有"db 覆盖 > env 回退"语义，集中在一处。
- `handlers.UpdateConfig` 用 `MutableKeys` 白名单 + `MutableFieldType` 类型映射做校验；不再写死 `registryUrl` 一个分支。

### 测试

- `go build ./...` ✅ · `go vet ./...` ✅ · `go test ./...` 全绿
- `gofmt -l internal/` 0 个未格式化

### 已知问题（v0.5.x 跟进）

- 158 上 `intranet-53` 测试凭据 / webhook / 卡死 job 待清理（沿用 v0.5.1）
- `REGISTRY_USERNAME/PASSWORD` 在 v0.5 已不再使用（cairn 自管仓库），未暴露到设置页

## [0.5.1] - 2026-09-25

### 新增

- **`PATCH /api/config`**：设置页可热改 `REGISTRY_URL`，立即生效；env 仍是首次启动默认值。
- **`settings` SQLite 表**（migration v3）：`key/value/updated_at`，后续每加一个可热改字段都先在这里加一行 schema。
- **`config.Mutable` + `EffectiveRegistryURL()`**：运行时注册表（`sync.RWMutex` 保护），`Orchestrator.Mutable` 引用同一指针；改完 pull 入队时立刻看到新上游，无需重启。
- **UI（settings-page.tsx）**：
  - "地址" 行从只读 `Descriptions` 改为 `Input + 保存` 控件
  - 当前生效值下方带 **"界面设置（已覆盖）" / "环境变量"** Tag，标明来自 db 还是 env
  - 留空提交 = 清除覆盖，恢复 env 默认

### 变更

- **`Internal/Handlers.Cfg.RegistryURL` 读路径**：所有路径改走 `Cfg.EffectiveRegistryURL()`，env vs db 优先级集中在 config 包。
- **`Pull.Orchestrator.DefaultSourceURL` 标记 deprecated**：保留为 env-bootstrap fallback；新路径优先 `Mutable.RegistryURL()`。
- **server.go 启动加载**：db 可用时从 `settings` 表 hydrate `cfg.Mutable.SetRegistryURL(...)`，日志带 `"registry.url.source":"db"`。

### 测试

- `go build ./...` ✅
- `go vet ./...` ✅
- `go test ./...` 全绿
- `gofmt -l internal/` 0 个未格式化

### 已知问题（v0.5.x 跟进）

- 158 上卡死 job `job-1790328145899764915-1` 仍待 cancel
- `intranet-53` 测试凭据、webhook 仍待联调

## [0.5.0] - 2026-09-25

### 新增

- **cairn 默认管理自己**：`REGISTRY_URL` 不再是必填 env；空值时 pull 任务回退到 Docker Hub (`pull.DefaultUpstream`)。每个 pull 任务在 UI 上独立指定自己的源（URL / inline auth / inline proxy），不再依赖一个"被管理的远端 registry"。
- **`REGISTRY_STORAGE_DIR` env**：新增。blob / manifest / tag 的物理路径，默认 `/app/registry`。`docker-compose.yml` 用独立命名卷 `cairn-registry` 挂载此处，与应用数据 (`cairn-data:/app/data`) 物理隔离 —— 备份 / 重置可以分别处理。
- **删除操作回归 cairn 页面**：
  - `DELETE /api/repositories/{repo}` —— 删除整个仓库（所有 tag + manifest + 受影响 blob），由 `REGISTRY_ALLOW_DELETE` 门控。
  - `DELETE /api/repositories/{repo}/manifests/{digest}` —— 按 digest 删除 manifest，响应里带 `affectedTags`（被同时清理的 tag 列表）。
  - `POST /api/gc` —— 触发一次存储 GC 扫描，返回 `{removedBlobs, freedBytes}`。
- **`storage.GC` 实现**：文件系统层 GC 真正落地 —— 扫描 `uploads/` 目录中超 24h 的孤立上传 session 并清理；pass2 不再被重复计入。
- **`storage.TagsForDigest` / `DeleteManifest([]string,error)`**：本地 storage 接口新增 `TagsForDigest`；`DeleteManifest` 签名升级为返回被解引用的 tag 列表，让 "按 digest 删除" 端点能告诉前端影响了哪些 tag。
- **`pull.NewJob` 接受 inline 凭据 + inline proxy 字符串**：`CreatePullJobReq` 接收 `sourceUrl` / `sourceRef` / `sourceProxy` / `sourceProxyId` / `sourceCredentialId` / `sourceAuthInline`，与前端 `PullJobInput` 形状 1:1 对齐。
- **`registry.Config.Proxy` string 字段**：远端 registry client 支持单层 HTTP 代理（之前在 server 层组合，现在下沉到 client）。
- **`/v2/<repo>/blobs/<digest>` 多段路径通配**：registryd 路由用 `/*` + 手动 dispatch 替代 chi 多段 `{}` 占位，让 OCI 多段路径与标准 V2 协议 100% 对齐。
- **`parseDays` 7-day bug 修复**（本就在 HEAD 已修，验证保留）：不再把 `?days=7` 解析成 7 小时。

### 变更

- **`Handlers.Registry` / `ExtraHandlers.Registry` 字段删除**：v0.5 不再有"远端 registry client"这个抽象；`Orchestrator.ExternalRegistry` 字段随之消失。
- **`server.go` 简化**：移除整个 externalRegistry 构造块（约 30 行）；executor 现在无条件创建（不再 `if externalRegistry != nil` 门控）；`DefaultSourceURL = REGISTRY_URL` 直接注入 Orchestrator。
- **`registryURL(r)` 翻转**：请求 Host 优先（含 `X-Forwarded-Proto` / `X-Forwarded-Host` 覆盖），`REGISTRY_URL` 仅作回退 —— 体现"cairn 默认就是自己"的语义。
- **`Inventory` / `Refresh` / `DeleteTag` 等：UI 仍走旧契约（`refreshAt` / `targetRepo` / `targetTag` / `error`），UI 大改放在 v0.5.x**；当前版本 **不破坏** UI 字段，避免 158 重新部署后页面立刻白屏。
- **frontend dist 不需本地 pnpm 重建**：Dockerfile 的 `web-builder` stage 内构建，前端变更不阻塞 Go 二进制。

### 修复

- **存储路径双计**：`storage.Stats` 不再把 `<algo>/<2hex>/<hex>/data` 与 `<algo>/<2hex>/<hex>` 重复计入体积。
- **`Repositories` 不递归 + 不剪枝**：v0.4 列表里会出现已经删除的空目录，现在正确剪枝。
- **`DeleteManifest` 不清 tag**：删除 manifest 时同步清理指向该 digest 的 tag 文件，避免 `tags/list` 列出 404 的悬空 tag。

### 文档

- **CHANGELOG.md**：本节。
- **docker-compose.yml / .env.example**：精简到只剩运行需要的 env（`REGISTRY_NAME` / `REGISTRY_CREDENTIAL_KEY` / 可选 `REGISTRY_URL` / 可选 `REGISTRY_NOTIFY_TOKEN` / 新增 `REGISTRY_STORAGE_DIR`）；其他 env 全部保留默认值（契约不变）。
- **README.md**：状态标题改为"cairn 默认管理自己，部署只需挂载存储卷"；部署小节说明 `REGISTRY_CREDENTIAL_KEY` 是唯一必填；字段对照表新增 `REGISTRY_STORAGE_DIR`。

### 已知问题（v0.5.x 跟进，不影响主流程）

- 158 上 `job-1790328145899764915-1` 仍处卡死态，需先 cancel。
- webhook 测试未配（按需）。

## [0.4.0] - 2026-09-25

前后端在本版本正式合体：React 前端（`web/`）构建后经 `//go:embed` 嵌入二进制（`-tags webui`），`/` 直接服务 UI、深路由 fallback index.html，部署只发一个文件；`/api/*` 契约全面对齐前端 `types.ts` / `api.ts`；修复 HEALTHCHECK 永远 unhealthy（`-healthz` 探针从未实现）；DB / 凭据库 / 代理库启动失败降级为软错误，不再 Fatal。

### 新增

- **Web UI 嵌入二进制**：新增 `internal/webui` 包（`-tags webui` 下 `//go:embed dist` 并挂 FS；默认 tag 下为占位实现，保证纯 Go 环境可编译）。`web/vite.config.ts` 的 outDir 改为直接产出到 `../internal/webui/dist`。Dockerfile 新增 web-builder 阶段（node + corepack + pnpm 构建前端，产物拷入 Go builder 后 `-tags webui` 编译），新增 `NODE_IMAGE` / `NPM_REGISTRY` build-arg（受限网络可换内网镜像源）；新增 `.dockerignore` 防止本地 node_modules / dist 污染构建上下文。
- **`/cairn -healthz` 探针模式**：HTTP GET 本机 `/healthz`，2xx 退出码 0、否则 1。Dockerfile 的 HEALTHCHECK 自 v0.1 就写着 `CMD ["/cairn","-healthz"]`，但二进制从未实现该 flag——探针每次被当作再启动一个服务进程，撞 8787 端口失败退出码 1，容器永远 unhealthy。现在探针只做一次 HTTP 检查即退出。
- **API 契约对齐 UI**（handlers.go / handlers_extra.go）：
  - `GET /api/config` 返回 `AppConfig`（`name` / `canDelete` / `canPull` / `allowRegistryEvents` / `pullQueueSize` 等，字段与前端 `types.ts` 一一对应）；
  - `GET /api/inventory` 聚合清单（仓库 + tags + manifest 摘要 + 体积）；
  - `DELETE /api/tags?repository=&tag=` 按 tag 删除：服务端解析 digest、列出同 digest 受影响的其它 tag，返回 `{deletedTag, digest, affectedTags, repository}`；
  - `/api/credentials`、`/api/proxies` 支持 PATCH 与 `note` 备注字段；密码进凭据库（AES-256-GCM），响应只带 `hasPassword` / `hasAuth` 不回显明文；
  - 统计全家桶 `/api/stats/*`（overview / top / series / repos / days / recent），ignore 规则三来源合并（env + DB + User-Agent），`/api/stats/purge` 清空热度数据；
  - 新增顶层 `/events` SSE 端点（每连接独立 channel，断开即清理）。
- **前端 ErrorBoundary**：捕获渲染异常展示兜底 UI（antd Result），挂载于 `main.tsx` 的 ConfigProvider 内。
- **compose / .env.example** 增加 `NODE_IMAGE` / `NPM_REGISTRY` 接线（前端构建的 node 镜像与 npm registry 镜像可覆盖）。

### 变更

- **DB / vault / proxies 启动失败降级为软错误**（server.go）：SQLite / 凭据库初始化失败时不再 Fatal 退出，对应 API 返回带 `?error` 字段的降级响应，其余功能（含 `/v2` 数据平面）保持可用。
- 前端不再需要单独 `pnpm build` + nginx 反代部署（v0.3.1 的 TODO「前端 dist embed」完成）。
- pnpm 12 构建许可迁移到 `web/pnpm-workspace.yaml` 的 `allowBuilds:`（`package.json` 的 `onlyBuiltDependencies` 在 pnpm 12 已不生效，会导致 esbuild postinstall 被跳过、vite 无法构建）。

### 修复

- **pull 队列 `RunOne` nil panic**：执行器依赖字段为 nil 时（如无凭据直连拉取）解引用 panic，进程崩溃；现在走空值安全路径。
- api_test.go 两处旧契约断言随新 API 修正（`registryName`→`name`；deleteTag 参数 `repo/digest`→`repository/tag`）。

---

## [0.3.1] - 2026-09-25

### 新增

- **构建期 Go 模块代理可配置**：`Dockerfile` 新增 `ARG GOPROXY=https://proxy.golang.org,direct` + `ENV GOPROXY=${GOPROXY}`,影响 `go mod download` 阶段。
  - `docker-compose.yml` 通过 `${GOPROXY:-https://proxy.golang.org,direct}` 透传到 build-arg
  - `.env.example` 新增 `GOPROXY=` 配置项,默认留空走 `proxy.golang.org,direct`
  - 受限网络下,在 `.env` 把 `GOPROXY` 改成 `https://goproxy.cn,direct` 或 `https://goproxy.io,direct` 即可,无需改代码
- **构建期通用 HTTP/HTTPS 代理可配置**：跟 `GOPROXY` 正交,影响 builder stage 内所有网络出口(go / curl / git / apt 等),给构建机直连外网受限、有内网 HTTP 代理的环境用。
  - `Dockerfile` 新增 `ARG HTTP_PROXY=` `ARG HTTPS_PROXY=` `ARG NO_PROXY=`(默认空 = 不设代理,行为不变)
  - `docker-compose.yml` 透传到 build-arg,`.env` 里用 `BUILD_HTTP_PROXY` / `BUILD_HTTPS_PROXY` / `BUILD_NO_PROXY` 配置(带 `BUILD_` 前缀跟运行时 `REGISTRY_PROXY` 区分,避免 `.env` 命名冲突)
  - 内网代理示例:`BUILD_HTTP_PROXY=http://proxy.example.com:7890`

### 文档

- `README.md` 开发章节新增「网络受限环境的构建(Go proxy)」+「构建期通用 HTTP 代理(跟 GOPROXY 正交)」两个小节,列出本地 / docker build 两种覆盖方式 + 常用国内代理 + 内网代理典型用法
- 更正内网代理示例地址(原 `4433` → `7890`,与真实代理对齐):`Dockerfile` / `README.md` / `.env.example` 示例统一更新
- `.env.example` 新增 `HOST_PORT` 项(宿主机访问端口,示例值 80,留空回退 8787),`README.md` 部署段补充"启动后访问 `http://<宿主机>:<HOST_PORT>`"说明
- `AGENTS.md` 保持不变(版本号规则已覆盖本次改动的小版本 +1 判定)

---

## [0.3.0] - 2026-09-25

### 重大变更

**cairn 现在是 registry 本身**，不再依赖外部 OCI Distribution。

之前 v0.2 是 **registry 的客户端/admin**（必须配 `REGISTRY_URL` 指向已有的 Docker Registry）。
现在 v0.3 自带数据平面：本地 FS 存储 blob/manifest/tag，对外暴露 `/v2/*` 协议，
`docker push` / `docker pull` / `skopeo copy` 直连 cairn 即可。

新增

**v0.3 — registry server**

- `internal/storage/` — Filesystem 后端，digest 校验，原子写，per-repo 写锁
  - 布局：`repos/<repo>/tags/<tag>`、`repos/<repo>/manifests/sha256/<digest>/data`、
    `blobs/sha256/<aa>/<bb>/<digest>/data`、`uploads/<repo>/<uuid>/data`
  - `Storage` interface：`Repositories / Tags / GetManifest / PutManifest /
    DeleteManifest / BlobExists / GetBlob / StatBlob / StartUpload /
    PatchUpload / PutUpload / GetUpload / CancelUpload / Stats`
- `internal/registryd/` — `/v2/*` 协议路由
  - `GET /v2/`、`GET /v2/_catalog`
  - `GET /v2/<repo>/tags/list`
  - `GET/HEAD/PUT/DELETE /v2/<repo>/manifests/<ref>`
  - `GET/HEAD /v2/<repo>/blobs/<digest>`
  - `POST /v2/<repo>/blobs/uploads/`（start）
  - `GET /v2/<repo>/blobs/uploads/<uuid>`（inspect）
  - `PATCH /v2/<repo>/blobs/uploads/<uuid>`（chunk）
  - `PUT /v2/<repo>/blobs/uploads/<uuid>?digest=<digest>`（commit）
- Admin handlers 改用本地 storage（不再调外部 registry）
- `REGISTRY_CREDENTIALS_DIR` 下挂 `registry/` 子目录做数据存储
- Pull Orchestrator：source = 外部 registry client，dest = 本地 storage

### 变更

- `/api/inventory` 现在从本地存储读，秒级返回（不再等 V2 协议 catalog 扫描）
- `/api/tags?repo=&digest=` 删除走本地 storage，返回受影响 tag 列表（best-effort）

### 测试

- 新增 registryd 集成测试：`/v2/` 根、`/v2/_catalog`、完整 push→pull roundtrip、digest mismatch
- 21 个测试全绿（v0.2 的 14 个 + v0.3 的 7 个）

---

## [0.2.0] - 2026-09-25

### 新增

**v0.2 — 拉取 + 凭据 + 代理**

- `internal/pull` — FIFO 拉取队列（单并发、生命周期、cooperative cancel、ring buffer）
- `internal/pull.Orchestrator` — 解析 sourceRef、下载 manifest、按 layer 下载 + 上传 blob、PUT manifest 到目的端
- `internal/credentials` — AES-256-GCM 加密 vault（SHA-256 派生 key、原子写）
- `internal/proxies` — 明文 JSON 代理库
- `internal/registry/bearer.go` — V2 Bearer token 流程（401 + WWW-Authenticate → realm 取 token → 重试）
- `internal/registry/blob.go` — `BlobExists` / `GetBlob` / `StartBlobUpload` / `UploadBlob`（4 MiB chunked PATCH + PUT commit）
- API：`/api/pull/jobs[/:id[/cancel]]`、`/api/pull/probe`、`/api/credentials[/:id[/test]]`、`/api/proxies[/:id[/test]]`
- Source client 解析（Docker Hub `library/` 前缀、单并发 source URL）

**v0.3 — 事件 + 热度**

- `internal/db` — modernc.org/sqlite（纯 Go，无 CGO），WAL 模式，`SCHEMA_VERSION` 迁移
- 表：`activity_daily`（按 天×仓库×tag×action 聚合）、`pull_jobs`（拉取历史）
- `internal/events` — webhook 接收（HMAC-SHA256 签名校验）、manifest media-type 白名单、HEAD/PUT 方法白名单、User-Agent 忽略规则（子串、大小写不敏感）、self-UA 单独计数、最近事件 ring buffer
- API：`POST /api/events`、`/api/stats/{summary,top,series,repositories,events,clients,ignore}`、`DELETE /api/stats/heat`

**前端**

- `web/` 从 registry-manager 完整复制（React + AntD + Vite）
- 改 `vite.config.ts`（去掉 `root: 'web/'`，因为目录已经在 web/）
- 改 `package.json`（去掉 Node 后端依赖，重命名为 `cairn-web`）
- API 响应统一包在 `{success, code, message, data}` 信封（与 registry-manager 一致）

**响应格式改造**

- 所有 `/api/*` 响应包信封（前端 `request<T>()` 直接拿 `data`）
- 错误响应：`{success: false, code, message, error: {code, message, detail}}`

### 修复

- V2 errors[] 数组解码（registry-manager 用的 spec 格式，之前是顶层 code 字段）
- V2 manifest `Accept` header 4 种 media type（OCI index + manifest + Docker list + v2）

### 文档

- AGENTS.md §Go 工具链版本（Dockerfile ARG GO_IMAGE 跟 go.mod 的 go 指令必须对齐）

---

## [0.1.0] - 2026-09-25

### 新增

- chi 路由 + structured logging（slog JSON 输出）
- V2 协议客户端：basic auth、HTTP 代理支持、可选 InsecureTLS
- 清单浏览：列出仓库、列出 tag、获取 manifest（支持 OCI index / OCI manifest / Docker list / Docker v2 四种 media type）
- 删除：按 digest 删除 manifest，返回受影响的 tag（best-effort 扫描）
- 配置探测 `/api/probe`、强制刷新 `/api/refresh`
- 内存 TTL 缓存（`CachedRegistry`），缓存命中走原值、过期重扫
- 类型化错误 `*registry.Error`，从 V2 spec 的 `errors[]` 数组解码 code/message/detail
- 多阶段 Dockerfile：scratch 基础，运行镜像 ~15MB
- docker-compose.yml + .env.example（字段名与 registry-manager 对齐）
- AGENTS.md（开发规范 + V2 协议事实）
- 14 个单元 / 集成测试，覆盖 V2 客户端、错误解码、并发扫描、handler 路由