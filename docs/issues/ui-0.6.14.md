# cairn UI 缺陷登记 · v0.6.14 UAT 轮

> **登记日期**：2026-10-01（CST）
> **被测版本**：cairn `0.6.14`（`internal/version/version.go`，commit `3598f23`）
> **测试环境**：registry = `http://registry.local:10001`（`env=prod`）；浏览器手工测试
> **来源**：UAT 手工测试记录《Cairn v0.6.14 UI 测试》，7 张实机截图
> **编号分区**：`UI-*` 纯前端表现/交互；沿用既有 `MA-*` 管理 API、`REG-*` 协议分区（本轮无新增协议缺陷）
> **取证口径**：每条至少 1 张实机截图；源码定位以「文件:行」标注到具体 handler / 组件

## 编号与范围约定

| 编号 | 严重度 | 一句话 | 本轮处置 |
| --- | --- | --- | --- |
| **UI-1** | Low | 代理「状态」列在「探测中」↔「可用」间切换时宽度抖动，把「操作」列首按钮文字挤到截断（「探测中…」后半截看不见） | 0.6.15 修 |
| **UI-2** | Low | 新建代理点提交后连弹两条 toast（「已创建代理，正在探测连通性…」+「Bigops53 可用 · 延迟 0.9 ms」），同一动作产生两次反馈 | 0.6.15 修 |
| **UI-3** | Low | 设置页「刷新清单」走右上 toast、「连接成功 (API 2)」走页面内 `<Alert>`，同页两种提示载体并存 | 0.6.15 修 |
| **UI-4** | Medium | 镜像同步「历史」弹窗只有汇总（成功 76 / 失败 1 / 死在 `zookeeper:3.8`），无法展开逐 tag 推送明细（对比「镜像拉取」历史可 `+ / -` 展开）。**已确认是后端未落明细**（`sync_runs` 只有计数器），非 UI 遗漏 | 0.6.15 不修，需 schema 变更 → 独立立项（中版本） |
| **UI-5** | Low | 镜像热度「Top 榜单」渲染全部 15 项，表格内部出第二条竖向滚动条，与页面外层滚动条嵌套 | 0.6.15 修（限 Top 10） |
| **UI-6a** | — | 复制命令只有 `docker pull`，希望可按 runtime（docker / containerd / nerdctl / podman）切换 | 新增能力，待拍板进位 |
| **UI-6b** | — | 希望在页面直接把某 tag 镜像下载为 tar 格式 | **暂缓（2026-10-01）**：触发场景未明、tar writer 长期维护成本待评估，UI 退到「复制 `docker save` 命令」先 |

> UI-6a / UI-6b 是**新增能力**（不是既有功能的优化），按 `AGENTS.md §版本号规则` 属中版本进位口径，AI 不得自行进位，需人拍板。

## UI-1 · 代理状态列宽度抖动

**现象**（截图 1 / 截图 2 连续两帧）

- 探测进行中：状态列 = 旋转图标 + 「探测中」（3 字，warning 色 tag）；操作列首按钮文字被压到只剩「探测中…」前半截。
- 探测完成：状态列 = 对勾图标 + 「可用」（2 字，success 色 tag）；操作列首按钮恢复完整为「探测」。

**根因**

- 状态 tag 的文本长度在两个状态间不等（3 字 → 2 字），tag 自身宽度随之变化。
- 表格列宽按内容自适应，未给「状态」列设固定/最小宽度，内容变窄时整表重排，把「操作」列首按钮的可用宽度压缩，按钮内文本溢出截断。

**影响**：纯视觉，同一表格在探测完成的瞬间出现一次可见的列宽抖动；无数据/逻辑影响。

**判定**：真缺陷（UI 一致性）。归属 0.6.14「UI 一致性收尾」同一条线的延续。

**修复方向**：给「状态」列设固定/最小宽度（使 2 字与 3 字状态同宽），或「操作」列按钮改为固定宽度 + 文字不换行。择一即可，不必两者都改。

---

## UI-2 · 新建代理双 toast

**现象**（截图 3）

创建代理点提交后，屏幕右上角**先后**出现两条 message：

1. 「已创建代理，正在探测连通性…」
2. 「Bigops53 可用 · 延迟 0.9 ms」

两者间隔极短（探测是一次「不发请求的 TCP 建连」，本机回环同网段通常 < 5ms），实际观感是同一件事被播报两次。

**根因**

- 提交 handler 把「创建成功」与「随后触发的一次连通性探测」当成两个独立成功事件，各自 `message.success(...)` 一次。
- 新建/编辑弹窗里的「测试连接」按钮在 0.6.14 改成 toast 之后，提交路径的「创建 + 探测」两段反馈路径没有一并合并。

**影响**：纯交互冗余，无数据影响。

**判定**：真缺陷（同一动作双反馈）。

**修复方向**：合并为一条 toast，按探测结果分流：

- 可用 → 「已创建代理，可用 · 延迟 0.9 ms」
- 不可用 → 「已创建代理，但探测失败 · <原因>」（**注意：代理已落库，这条必须是 warning/error 级但文案要明确「已创建」**，不能让用户以为创建失败而去重填表单）

探测中使用 `message.loading` 占位再转 success/error，避免探测慢时出现「空窗期」。

---

## UI-3 · 设置页两种提示载体并存

**现象**（截图 4）

「设置 → 仓库连接」页点「刷新清单」后，页面上同时出现：

- 右上 toast：「清单刷新完成，用时 40 ms」（success 级）
- 页面内绿色 `<Alert>`：「连接成功 (API 2)」

同页两种提示形态，视觉上互相竞争。

**根因**

- 「刷新清单」是**用户主动操作**的即时反馈 → 走 message toast（符合 0.6.14 确立的原则）。
- 「连接成功 (API 2)」是**页面状态**（进入本页时探测出的 registry 联通状态），用页面内 `<Alert>` 承载。
- 两者在同屏出现，控件样式与位置都不同，用户读到的是「出了两件事」，实际是「一件事 + 一个状态」。

**关于用户提出的两个疑问的澄清**（源码核对结论）

1. **「测试连接」能不能不要？** —— 不能。页面能打开只证明 cairn 进程在 listen 并返回 SPA shell，**不证明 registry 在响应**。cairn 与 registry 进程可以一个活着一个已挂，此时 UI 照常渲染，只有「测试连接」能暴露真实 registry 状态。保留该按钮。
2. **「刷新清单」为什么需要？」** —— 需要。`GET /v2/_catalog` 在 cairn 侧有短 TTL 缓存，手动按钮的作用是**主动失效缓存**并重新抓取，用于「刚在别的客户端 push 了新仓库、列表还没出现」这类场景。不是冗余功能。

**判定**：真缺陷（提示载体不一致），但两条按钮的功能都保留。

**修复方向**：让「连接状态」有固定的、可预期的呈现位置，而不是和操作 toast 抢同一视觉层。可选做法：

- 把 registry 连接状态做成卡片顶部一个**常驻**状态行（复用既有状态 tag 样式），进入页面时静默探测并回填，不再用 `<Alert>` 也不再用 toast；
- 操作类反馈（刷新清单 / 测试连接 / 保存）继续统一走 message toast（0.6.14 已建立，本条不推翻它）。

**注意**：不要顺手把 0.6.14 保留的页面级 `<Alert>`（load failure / vault 不可用）也改成 toast —— CHANGELOG 已明确那类是页面状态而非操作反馈，职责不同。本条只处理「连接成功 (API 2)」这一处。

---

## UI-4 · 同步历史缺逐 tag 明细

**现象**（截图 5）

「镜像同步 → Push 53 → 历史」弹窗，77 次推送只给一行汇总：

```
成功 76 / 失败 1   [部分失败]
死在 zookeeper:3.8
耗时 14.0s    40 分钟前
```

失败只暴露**一个** tag 名（`zookeeper:3.8`），看不到是哪个 registry 返回了什么错误；而「镜像拉取」页的历史是能 `+ / -` 逐条展开的，两个页面的信息粒度不一致。

**根因（已源码确认：情况 B）**

对比两侧的存储形态：

| 能力 | 拉取（`pull_jobs`） | 同步（`sync_runs`） |
| --- | --- | --- |
| 粒度 | **一行 = 一次 tag 拉取** | **一行 = 一次任务执行** |
| 表结构 | `source_ref` / `dest_repo` / `dest_tag` / `state` / `error` / `bytes_done` / `bytes_total` 逐字段落库 | 只有 `repos_total` / `repos_synced` / `repos_failed` 三个**计数器** + 一个 `error` 文本 |
| 能否还原逐 tag 明细 | 能（每行自带 repo/tag/error） | **不能** |

同步执行时只累加计数器，不写明细行：

- `internal/sync/engine.go:272` / `engine.go:275`（pull 方向）—— 失败 `run.ReposFailed++`，成功 `run.ReposSynced++`
- `internal/sync/engine.go:383` / `engine.go:386`（push 方向）—— 同上
- `internal/db/sync.go:254` —— run 结束时只 `UPDATE sync_runs SET repos_total=?, repos_synced=?, repos_failed=?`

而 UI 上「死在 `zookeeper:3.8`」那一行来自 `current_repo` / `current_tag` 两个字段（`internal/sync/engine.go:279` 附近的 progress 写入路径，`internal/db/sync.go:279` 的 `UPDATE sync_runs SET current_repo=?, current_tag=?`），它们记录的是**执行引擎最后停在哪一个**（v0.6.9 加的「正在拉 repo:tag」进度用），**不是失败清单**。所以：

- 77 次推送里 76 成功 1 失败，`current_repo/current_tag` 恰好停在那个失败的 `zookeeper:3.8` → 看起来像「失败清单只有一条」
- 实际上**同一 run 内有多个失败时，UI 只会显示最后一个**，前面的全丢
- 成功/失败的具体 tag 名与错误文本（`e.log.Warn("sync: push repo failed", "repo", repoName, "err", err)`，`engine.go:381-382`）**只进了日志，没有任何持久化**

**判定**：**情况 B 确认**。UI 无法凭现有数据还原明细 —— 需要新增明细表 + 改造写入路径，属**新增能力**（中版本口径），不进 0.6.15。

**修复方向（独立立项）**

1. 新增 `sync_run_items` 表（`run_id` / `repo` / `tag` / `state` / `error` / `started_at` / `ended_at`），粒度对齐 `pull_jobs`；
2. 改造 `engine.go` 的两个循环，把每次 repo 推送/拉取的成败写成一行（现在是 `continue` 直接跳过）；
3. 历史表行加可展开子行，读明细接口按 run 分页（单 run 可达数百行，不能一次全吐，否则重演 UI-5 的双滚动条）；
4. `current_repo/current_tag` 的语义要与之区隔 —— 它是「进度指针」不是「失败清单」，UI 上不该继续用它表达失败。

**另有一处已存在的语义混淆**（本条根因的伴生问题）：`current_repo` / `current_tag` 复用了「进度」与「失败定位」两种职责（v0.6.11 的注释写「失败 / 运行中 run 显示最后/当前位置——避免『卡住了?』+ 失败定位」）。即使本条不修，现有文案也会在多次失败时误导用户以为只有一个失败项。

---

## UI-5 · Top 榜单双滚动条

**现象**（截图 6）

「镜像热度 → Top 榜单」实测共 **15** 项，表格区可视高度只显示约 8 项，第 9 项起被内部竖向滚动条遮住，需在表格内再滚一次。页面外层本身已有一条竖向滚动条，形成嵌套双滚动。

**根因**

- 榜单未做条数上限，按活跃度全量渲染（当前 15 项 = 「有活动的仓库数」卡片显示的同一个数）。
- 表格容器设了固定/最大高度并 `overflow: auto`，条目数一多就内层出滚动条。

**判定**：真缺陷（布局嵌套）。属 UI 一致性收尾同一条线。

**修复方向**：榜单条数限 **Top 10**，超出部分不再渲染（与用户诉求一致）。需确认「按仓库」与「按 tag」两个 tab 都受限。若后续发现 Top 10 信息量不够，再评估是否加「Top 10 / Top 20 / 全部」切换——**本轮不加**，避免范围蔓延。

---

## UI-6a · 复制命令按 runtime 切换（新增能力 · 待拍板）

**现象**（截图 7）

「镜像列表 → 仓库抽屉 → tag 行」hover 复制按钮，tooltip / 剪贴板只给 `docker pull registry.local:10001/library/postgres:15.18`。实际环境里除 docker 外，containerd（`ctr` / `nerdctl`）与 podman 也常用同一份镜像。

**为什么算新增能力**：现状是「一条命令」，改成「按 runtime 选命令」是新增一个选择维度与一组命令模板，不是把既有功能做得更好。属中版本进位口径。

**改动范围（评估）**：纯前端字符串拼接，不需要后端改动。命令模板：

| runtime | 命令 |
| --- | --- |
| docker | `docker pull <registry>/<repo>:<tag>` |
| podman | `podman pull <registry>/<repo>:<tag>` |
| nerdctl | `nerdctl pull <registry>/<repo>:<tag>` |
| ctr | `ctr -n k8s.io images pull <registry>/<repo>:<tag>` |

**实现注意**：用 antd `Dropdown` 挂在现有复制按钮上，不要给每行塞一个 `<Select>` —— 长列表里会让表格行高抖动、渲染开销上一个量级。

---

## UI-6b · 页面直接导出镜像 tar（新增能力 + 架构议题 · 待拍板）

**诉求**：在页面上直接把某个 tag 的镜像下载为 tar 格式，省去「docker pull + docker save」两步。

**方案评估**

| 方案 | 做法 | 代价 | 结论 |
| --- | --- | --- | --- |
| A · cairn 后端流式生成 tar | 新增 `GET /api/repositories/{repo}/tags/{tag}/export`：读 manifest → 逐 layer 取 blob → `archive/tar` 流式写出 `docker save` 兼容布局 | 需自己实现 tar 头拼装、512 字节补齐、manifest.json / config json 生成；大镜像要处理流中断与超时 | 依赖上不违规（`archive/tar` 是标准库），但**改动量与测试面都大** |
| B · 服务端调 skopeo | 后端 `exec skopeo copy docker://... docker-archive:...` | 引入外部二进制依赖，违反 `AGENTS.md`「不允许引入需要运维的外部服务 / 单二进制」原则 | **否决** |
| C · 复制 `docker save` 命令，不做导出 | 页面照旧给命令，用户自己 pull + save | 架构零负担，但不满足「页面直接下载」的诉求 | 退路方案 |

**未决问题**（需人判断，不属代码层能定的事）

- 这个能力的真实触发场景是什么？（离线环境 / 无法访问 registry 的机器 / 单纯图省事）
- 触发频次是否高到值得为一个 tar writer 承担长期维护成本？
- 与「按 digest 删 manifest」这类已实现能力相比，导出属于**读多写少的运维辅助**，还是核心浏览能力的一部分？

**版本影响**：明确是新增功能（中版本口径），且伴随架构决策，需单独拍板是否启动。

---

## 处置汇总（本轮 0.6.15 范围）

**本轮修（v0.6.15，小版本 · 均为既有 UI 的一致性/体验优化）**

- UI-1 状态列宽度抖动
- UI-2 新建代理双 toast 合并
- UI-3 设置页提示载体统一（保留「测试连接」与「刷新清单」两个按钮）
- UI-5 Top 榜单限 Top 10

**本轮不做（留待后续）**

- UI-4 同步历史逐 tag 明细 —— **已确认为后端缺明细表**（`sync_runs` 只有 `repos_total/repos_synced/repos_failed` 三个计数器，逐 tag 错误只进日志），需 schema 变更 → 独立立项，中版本进位待拍板
- UI-6a 按 runtime 复制命令 —— 中版本进位，需人拍板
- UI-6b 镜像 tar 导出 —— **2026-10-01 拍板暂缓**，详见 [## 拍板记录](#拍板记录-2026-10-01)

**非本轮登记的观察项**

代理列表「更新时间 12:10」与「最后探测 12:44」两列语义不同但文案相近，用户容易误读为同一件事。不作为缺陷上报（属文案可读性），在实现 UI-1 时顺带确认列头措辞是否需要更明确，但不单独进版本。

---

## 拍板记录（2026-10-01）

### 版本号口径

本轮三个待办项（UI-4 / UI-6a / UI-6b）按 AGENTS.md §版本号规则本都属中版本进位口径（新增功能 / 新增表 + 写入路径）。**用户明确拍板不走中版本，每完成一项 +1 走小版本 0.6.x**：

- UI-4 → **0.6.16**（独立项，先做）
- UI-6a → **0.6.17**（独立项，后做）
- 第二轮 UAT 反馈（代理横滚/状态延迟合并 + 复制 Dropdown + 拉取详情横跳 + 同步翻页无效/删除文案）→ **0.6.18**
- 第三轮 UAT 反馈（设置页两个字段名 + 默认值收紧 + 复制 Tooltip 去掉 + 复制双 toast 合并）→ **0.6.19**
- 第四轮 UAT 反馈（同步历史去 Modal + items 分页高亮 bug）→ **0.6.20**
- 第五轮 UAT 反馈（首屏加载占位换成居中 spinner + 文案，去掉一闪一闪的灰块骨架）→ **0.6.21**
- 第六轮 UAT 反馈（v0.6.21 还是突兀：转圈太快 + 硬切；要求放慢 spinner + 等元素渲染完再淡出）→ **0.6.22**
- 第七轮 UAT 反馈（PageLoading 跟表格同一层拉址感 + 同步历史展开突兀 + 不能实时更新）→ **0.6.23**
- 第八轮 UAT 反馈（深色模式 spinner 撞色 + fixed-right 列漏出蒙板）→ **0.6.24**
- 第九轮 UAT 反馈（切到 sync/stats 整页刷新一次）→ **0.6.26**
- 第十轮 UAT 反馈（首次进入 sync/stats 仍闪蒙板 → App mount 后台预取）→ **0.6.27**
- 第十一轮 UAT 反馈（清缓存刷新 sync/stats 仍闪 → PageLoading 加 delay prop，延迟窗口内数据回来则不弹蒙板）→ **0.6.28**
- 第十二轮 UAT 反馈（同步 UA 版本号 `cairn-sync/0.6.14` 漂了 14 个版本没跟 binary 走 → 让 User-Agent 从 `version.Version` 派生）→ **0.6.29**
- 第十三轮 UAT 反馈（定时规则改下拉选择 + 同步历史只保留最近 10 条）→ **0.6.30**
- UI-6b → **暂缓**（详见下文）

> **不进位的原因**：用户认为这两项是「既有 UI 一致性收尾」的延续，不构成 AGENTS.md 所说的"新增模块"或"破坏性变化"——后者才必须进中版本。AI 尊重用户拍板，但本拍板记录在文档里留作 context，下次类似口径分歧时翻这段作为判例。

### UI-6b 暂缓说明

不做的明确原因（拍板时列举）：

- 触发场景未明：cairn 现行部署在 `registry.local:10001`（公网/内网混合），"无镜像拉取能力 + 需要 tar"的真实用例在 UAT 没出现过；
- tar writer（`archive/tar` + manifest 拼装 + 流式响应）属于持续维护成本，本仓目前没人专门 review 这条路径；
- 退路方案已经在 UI 上存在：「镜像列表 → 仓库抽屉 → tag 行」复制 `docker pull` 命令后，用户 `docker pull` + `docker save -o x.tar` 两步即可，架构零负担。

**重启条件**（任一）：
- 用户明确给出"为什么必须 cairn 端流式生成 tar"的场景；
- 至少出现 1 次真实 UAT 用户用 `docker pull` + `docker save` 流程碰到障碍。

### 拍板记录（2026-10-02 · 第十一轮 UAT 反馈）

**症状**：清缓存刷新后访问「镜像热度 / 镜像同步」页面，仍能看到一闪而过的 PageLoading 蒙板。其他页面（凭据 / 代理 / 拉取 / 镜像列表）单 fetch 路径，刷不出来蒙板的现象不明显。

**结构差异分析**（AI 给出的两条根因）：

1. **同步页有 3s 轮询**（`refresh()` 每 3s 跑一次把 running 追成 success/failed）—— 但 `refresh()` 内部只 `setLoading(false)` 不 `setLoading(true)`，所以轮询本身不会让 `visible` 翻转蒙板。**不是根因**。
2. **热度页有 5 路并发 fetch**（summary/top/series/events/clients）—— 比单 fetch 路径重，比 prefetch 同步路径 `cachedStatsData` 触发的更新慢一拍。但 5 路并发在同一后端上也只多花一倍带宽开销，<200ms 能完。

**真正根因**：v0.6.27 把 sync/stats 的数据提升到 App 层缓存并加了 `useEffect(..., [])` 后台预取，但用户刷新后点页面的速度**比 6 路 prefetch 完成还要快**（点击的瞬间 prefetch 仍在飞），于是：

- page mount 时 `initialTasks=[]` / `initialData=null`
- `loading=true` → PageLoading `visible=true` → 立刻弹蒙板
- 页面 useEffect 跑自己的 fetch（如果 prefetch 没抢到，回来时间会偏慢）
- 即便 prefetch 抢先填了 App 缓存，page 已经 mount 完，props 不会再更新

**拍板修法**（用户 2026-10-02「好」）：v0.6.28 给 PageLoading 加 `delay` prop（默认 200ms），`visible` 翻 true 后等 delay 才渲染蒙板；delay 窗口内 visible 又翻回 false（数据 < 200ms 就回），延迟定时器被取消，蒙板根本不弹。**NProgress / React Query 的标准做法**：

- 快接口（<200ms，本地 cairn 后端常态）→ 不弹蒙板
- 慢接口（>200ms，公网 / 慢存储）→ 弹蒙板
- 真卡住 → 永远弹

**为什么不动 prefetch / 不动 page fetch**：prefetch 已经覆盖了「正常点击节奏」（用户点开页面比 prefetch 完成稍晚一拍），sync/stats 多数情况已经走 initialTasks/initialData=非空分支不走蒙板。delay 是给「用户比 prefetch 还快」的极端场景兜底，两者互补不是替代。

**为什么默认 200ms**：实测本地 cairn 后端 stats 5 路并发 + sync 列表 fetch 都在 100-200ms 内能回来。公网部署若 >200ms，蒙板照样弹只是延后 200ms，比 v0.6.27「立刻弹」严格更好。

**API 兼容性**：默认 200ms 行为变化，所有调用方不用改。若需退回旧行为，传 `delay={0}`。

**进一步可能**：将来某页业务真的「永远快」（比如 SSG 缓存命中），可传 `delay={500}`。本轮不动，按需扩展。

### 拍板记录（2026-10-02 · 第十二轮 UAT 反馈）

**症状**：用户反馈「`cairn-sync/0.6.14` 这个同步的版本要跟着软件版本走」—— outbound User-Agent header 里写死了 `0.6.14`，binary 已经到 0.6.28，差 14 个版本。

**根因**：git log 显示从 v0.6.6 起每次发版手动 bump 这两个 UA 字面量（writer.go / probe.go 各一个），到 0.6.14 就停下了 —— 没有机制提醒「binary 涨了，这两个串也得涨」。

- `internal/sync/writer.go:241`：`req.Header.Set("User-Agent", "cairn-sync/0.6.14")`
- `internal/sync/probe.go:79`：`req.Header.Set("User-Agent", "cairn-sync-probe/0.6.14")`

注：registry client 流量走 `internal/registry/client.go`，用的是 `version.UserAgent = "cairn/" + Version` 派生，本身没漂 —— 漂的是 sync 模块这两条。

**拍板修法**（用户 2026-10-02 拍板）：**从 `version.Version` 派生，再写死就还会漂**：

- `internal/version/version.go` 新增两个常量：
  - `SyncUserAgent = "cairn-sync/" + Version`
  - `ProbeUserAgent = "cairn-sync-probe/" + Version`
- writer.go / probe.go 各 import `internal/version`，改用对应常量。
- bump Version 时这两条自动跟上。

**为什么不动版本节奏**：用户拍板独立 v0.6.29，不 amend 进 v0.6.28。理由：UA 是 outbound 行为变化（上游 registry 看到的字符串实际变了），不是 v0.6.28 的 PageLoading delay 那一类「前端 prop 增量」。给一个独立 version 让 UAT log / allowlist 关联更清晰。

**为什么不延后**：「sync UA 漂 14 个版本」这件事本身已经是问题，越早修越好 —— 拖到下个版本期间上游 registry 又会累积一段 `0.6.14` 标识的错误 sync 流量，反而更难清理。

**对上游 registry 的影响**：

- 日志：`cairn-sync/0.6.14` → `cairn-sync/0.6.29`，`cairn-sync-probe/0.6.14` → `cairn-sync-probe/0.6.29`。
- allowlist：基于 UA 的 allowlist 需要同步更新（建议改成 `cairn-sync/.*` 通配，避免下次再漂）。

### 拍板记录（2026-10-02 · 第十三轮 UAT 反馈）

**症状 1**：用户反馈「定时规则的 cron 不要用表达式,使用可以下拉的方式进行选择每日/每周之类的」。

**症状 2**：用户反馈「同步的历史任务提示一下只保留最近 10 个历史,不然太多」。

**两件事打包决策**：用户拍板 v0.6.30 一起装,理由：

- 都是「既有 UI 一致性收尾」的延续,跟 v0.6.16 (UI-4 同步历史)、v0.6.20 (去 Modal)、v0.6.26 (App 缓存) 同档
- 后端几乎零改动(只 DB 多一条 DELETE),UI 是大头
- 一起发便于 UAT 一次验证

---

#### A 部分 · 定时下拉

**拍板修法**(用户「每小时/每日/每周/每月 4 个」)：

- 4 档下拉,每档配条件子选择:
  - 每小时:分钟 (0-59)
  - 每日:时 (0-23) + 分 (0-59)
  - 每周:时 + 分 + 周几 (一-日)
  - 每月:时 + 分 + 几号 (1-31)
- **后端 schema 不动**:`Schedule.CronExpr` 仍存 5 字段 cron 字符串,前端 `kindToCron` 拼好后提交。
- **存量 cron 兼容**: `parseCron` 能反解出 4 档之一 → 回填下拉值;反解不出 → 顶部 Alert + 编辑时下拉覆盖(原自定义 cron 丢失)。
- 表格列名「cron」→「频率」,渲染从 raw cron → `cronSummary` 中文摘要。

**为什么不提供「自定义」保留编辑**：用户拍板「不接受手写 cron」(原话「cron 不要用表达式」)。一旦猜错覆盖了用户的复杂 cron,代价太高(例如「0 0 * * 1-5」工作日凌晨 cron,被覆盖成「每日 00:00」周末也跑)。直接走下拉,损失的是复杂 cron 的可编辑性,收益是「任何人都能看懂规则」。

**为什么不引入 React TimePicker**: hourly / monthly 等选择粒度用 InputNumber 就够,引入 dayjs 包体不值得(antd 内部可能已经 dayjs,但直接用 InputNumber 比 TimePicker 透明度高)。

---

#### B 部分 · 历史保留 10 条

**拍板修法**(用户「DB 物理删 10 条 + UI 文字提示」)：

- `internal/db/sync.go` 新增 `SyncRunTrimOlder(ctx, taskID, keep)`:DELETE 老的 runs。
- `internal/sync/store.go` 新增 `MaxRunsPerTask = 10` 常量 + `TrimRuns` wrapper;`CreateRun` 末尾 trim(best-effort,失败仅 WARN,不影响新 run)。
- `ON DELETE CASCADE` 在 `sync_run_items(run_id)` 已声明(`db.go v6+`),无需 schema 改动。
- UI 列表拉取从 limit=50 改 limit=10,常驻小灰字「仅保留最近 10 条历史」提示。

**为什么是 10 而不是更少**: 10 是「够看最近趋势但不至于无限累积」的折中 —— 跑得频繁的 task (例如 1 小时跑一次),10 条只覆盖 ~10 小时历史;跑得稀疏的(每天一次),10 条覆盖 ~10 天。运维想看更老的历史,直接看 DB(SQLite 文件在 `$HOST_DATA_DIR/cairn.db`)。

**为什么不放在定时任务里清理**: 跟「每次 CreateRun 后 trim」比,定时清理需要额外 goroutine / scheduler,得不偿失。每次新 run 触发清理,自然绑在「新鲜数据来了」的语义上,无需额外调度。

**为什么不设「保留天数」而不是「保留条数」**: 用户原话「10 个」,按条数最直观(UI 也好提示)。保留天数要算时间窗口,逻辑更复杂,且「跑得频繁」和「跑得稀疏」的 task 语义不同 —— 条数方案一刀切,简单且行为可预测。

---

#### 节奏

- v0.6.30 一次装两件事,跟用户「一起走 0.6.30」的拍板一致。
- 中版本进位?两件事都不算「AGENTS.md 意义上的新模块/破坏性变化」,沿用「每轮 UAT 反馈走 0.6.x」节奏。
