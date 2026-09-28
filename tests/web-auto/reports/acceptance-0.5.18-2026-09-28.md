# cairn 0.5.18-dev 双面验收报告

| 项 | 值 |
| --- | --- |
| 验收对象 | `cairn:0.5.18-dev`（镜像 ID `bafe9701b615`） |
| 被测环境 | dev，`http://registry.example.com`，容器 `Up (healthy)`、`0.0.0.0:80->8787/tcp`、`Restarts=0` |
| 执行日期 | 2026-09-28（部分前端证据采集自 2026-09-27） |
| 前端执行方式 | 53 侧自建 web-auto runner（Playwright，`http://proxy.example.com:8080`，`web-auto-runner:0.1.0`） |
| 后端执行方式 | 158 容器内 `curl` + `docker` 客户端一手取证（docker client/server 均 29.6.1） |
| 关联报告 | [f-group-0.5.18-2026-09-27.md](./f-group-0.5.18-2026-09-27.md)（F 组逐页 UI 功能回归） |
| **总判定** | **不通过** —— 前端 2 个真缺陷（P1 / P2）+ 后端 1 个门禁绕过缺陷（R-open-2）；B 组性能健壮性 6 项全部未落地 |

> **源码引用约定（便于查证）**：源码锚点首次出现用完整路径；其后短名一律按此展开 —— `ROADMAP` → `docs/ROADMAP.md`，`api.ts` / `types.ts` → `web/src/`，`X-page.tsx` → `web/src/pages/`，`handlers_extra.go` → `internal/api/`，破坏性/故障注入场景 `*.yaml` → `tests/web-auto/scenarios/`；裸行号 `:NNN` 指同段落中最近一次点名的文件。

---

## 0. 结论速览

| # | 面 | 口径 | 判定 | 关键证据 |
| --- | --- | --- | --- | --- |
| 1 | 前端 UI 功能 | F 组 8 行矩阵 + 22/22 全页冒烟 + 6 页深度场景 | 通过（功能可用） | F 组报告；`r-2026092711xxxx` 8 条 |
| 2 | 前端破坏性路径 | 真拉取 / 真删 tag / 真删仓库 / 真删错误路径 | 3 通过 + 1 红（坐实 P1） | `r-...-e2ed` / `-f898` / `-c6af` / `-909b` |
| 3 | 前端 GC 量化 | 真 GC + 量化回收值 toast | **不通过（P2）** | `r-20260927153442-2622` 步 10 红 |
| 4 | 前端故障态（单页） | `docker pause` → 页面 ≤10s 错误态 | 通过 17/17 | `r-20260928022812-447a`（23526ms） |
| 5 | 前端故障态（多页） | 同上，5 页口径 | **4/5 达标**（images 页未达标） | `r-20260928032154-e0d2`（55598ms，16/16） |
| 6 | 后端数据面 | `docker login` / `push` / `pull` 匿名回环 | 通过（匿名） | push→rmi→pull RepoDigests 一致 |
| 7 | 后端鉴权面 | 开鉴权后的 login / push / pull | **不通过**（login 假阳性 + push/pull 恒 401） | 服务端日志每层两发 401 |
| 8 | 后端门禁一致性 | `allow.delete=false` 是否守住数据面 | **不通过（R-open-2）** | 管理面 403 / 数据面 202 / GC 200 |
| 9 | 后端协议一致性 | Distribution spec 常见偏差 | 7 项偏差（见 §5.3） | 逐项 curl 实测 |
| 10 | 后端 B 组落地 | ROADMAP `:36-52` B1~B6 性能/健壮性 | **全部未落地**（与 `:18` 状态列一致） | 源码逐点锚定 |
| 11 | 主标准 | `go test -race ./...` | 绿（但 storage 等核心包零测试） | `tmps/go-test-race2.log` RC=0 |
| 12 | 前端类型门禁 | `tsc --noEmit` 是否在构建链路上 | **不通过（门禁缺失 + 10 个错误）** | `web/package.json` / `Dockerfile` |
| 13 | L 组追加 | L1 重启后拉取历史仍在 | 未实现（与 ROADMAP 一致） | 重启后 `/api/pull/jobs` 空 |

---

## 1. 被测对象与同一性证明

### 1.1 版本面（按事实陈述，不当缺陷）

| 来源 | 值 |
| --- | --- |
| 容器 tag | `cairn:0.5.18-dev`（镜像 `bafe9701b615`） |
| `/api/config` 自报 | `"version":"0.5.17"` |
| `internal/version/version.go:18` | `0.5.17` |
| `docker-compose.yml:28` | `cairn:0.5.17` |
| `.env.example:35` | `0.5.17` |
| `CHANGELOG.md` 最新节 | `## [0.5.17]` |

结论：**0.5.18 是轮次标签，不是已发布的版本号**。四处版本事实仍为 0.5.17，未见漂移或矛盾（不进位是既定事实，本报告不代为判定是否应当进位）。

### 1.2 产品代码同一性

| 项 | 值 |
| --- | --- |
| 158 源码树 HEAD | `38858c3` |
| 本地 HEAD | `4c27ee7` |
| `git diff --name-only 38858c3 HEAD \| grep -vE '^(tests/web-auto/\|docs/\|tmps/)'` | **空输出** |

⇒ 被测镜像所构建的**产品代码**与本地仓库 HEAD 逐字节相同，差异仅在测试资产与文档。

### 1.3 运行态 SPA 资产核验

| 项 | 值 |
| --- | --- |
| `GET /` | 200 / 1614 B |
| 入口 bundle | `/assets/index-rBCZMpkz.js` |
| bundle 大小 / md5 | 1207110 B / `e6ecc2f9b141b0fffd85898654004c24` |
| 标记计数 | 错误分类=7、已中止=1、`AbortController`=1、`NETWORK_ERROR`=2、`TIMEOUT`=1 |

⇒ 运行中的容器确实携带 0.5.18 的错误态代码（与 F 组报告记录一致），故障注入结论针对的是**真实运行产物**而非源码推测。

---

## 2. 缺陷清单

### P1 — 零 tag 僵尸仓库无法经 UI 删除（前端真缺陷，中高）

| 项 | 内容 |
| --- | --- |
| 现象 | `webauto-pull/busybox` 在拉取失败后留下 0 tag 的仓库记录，UI 点删除 → 提示失败，仓库无法清除 |
| 触发 run | `r-20260927150136-909b`（delete-real，failed / 19096ms / 21-23） |
| 根因链 | UI 发 `DELETE /api/repositories/webauto-pull%2Fbusybox` → `internal/api/handlers_extra.go:588-608`：`:594` 用 `chi.URLParam` 取 `{repo}`，chi 按 `RawPath` 匹配 ⇒ 参数**未 URL 解码** ⇒ `os.Stat` 收到含 `%2F` 的名字 → `ENOENT` → `ErrNotFound` → `:603-605` 对**任何** err 一律回 `writeError(500, "INTERNAL_ERROR")` |
| 影响 | 用户可见的「删不掉的僵尸仓库」；错误语义也失真（真实原因是不存在，对外却报 500 内部错误） |
| 定性 | 前后端契约缺陷（前端编码路径段、后端未解码 + 错误分类粗糙），非测试口径问题 |

### P2 — GC 成功提示显示 `undefined` / `NaN`（前端真缺陷，中）

| 项 | 内容 |
| --- | --- |
| 现象 | GC 成功后 toast：`GC 完成：清理 undefined 个孤儿 blob，回收 NaN MiB` |
| 触发 run | `r-20260927153442-2622`（gc-real v2，failed / 4729ms，步 1~9 passed，**步 10 toast 形状断言 failed**） |
| 对比证据 | v1 场景（`r-20260927151653-bdaf`，passed / 4474ms / 10-10）**是假绿**——它只断言 toast 出现、不断言内容形状 |
| 根因链 | `web/src/pages/images-page.tsx:305-317`（`:308-311` 直读 `r.removedBlobs` / `r.freedBytes`）← `web/src/api.ts:238-239 runGC` ← `api.ts:98-182 request<T>` 返回 `Promise<ApiResult<T>>`，而 `:168` 是 `return payload;`（**不解包 `data`**） |
| 判定依据 | HTTP 响应字段名正确 ⇒ **纯前端解码层 bug**，后端数据面无辜 |
| 影响 | 唯一一个量化 GC 收益的用户可见出口失效，用户无法判断 GC 是否真的回收了空间 |

### R-open-2 — `allow.delete=false` 不守数据面，GC 也未门控（后端真缺陷，高）

| 动作 | 结果 |
| --- | --- |
| 配置 `allow.delete = "false"` | — |
| 管理面 `DELETE /api/repositories/rcprobe` | **403**（门禁生效） |
| 数据面 `DELETE /v2/rcprobe/manifests/sha256:73aaf090…` | **202**，随后 `GET` → **404**（**门禁被绕过，删除真实发生**） |
| `POST /api/gc` | **200**（GC 未纳入该开关） |

⇒ 「禁止删除」策略只在管理面成立，任何能连上 `/v2` 的客户端仍可删 manifest；GC 亦不受该开关约束。安全语义不一致。

### 版本面 / 非缺陷澄清

- L1（重启后 `/api/pull/jobs` 历史）实测**重启即清空**，与 `docs/ROADMAP.md:18` 状态列「待开工」一致 ⇒ **未开工，非新缺陷**。
- `pull ...:latest` 返回 `manifest unknown`：仓库真实 tag 列表为 `library/busybox:["1.36"]`、`library/alpine:["3.16"]`、`webauto-pull/busybox:[]`，**确无 `latest`** ⇒ **符合 spec**，非缺陷。
- 容器 tag 与 `/api/config` 版本号不一致：见 §1.1，轮次标签≠发布版本号。

---

## 3. 前端验收详述

### 3.1 F 组 UI 功能回归（能力面）

覆盖：`_smoke-all-pages` 22/22 步、6 个 Tab 深度场景（镜像列表 / 镜像热度 / 镜像拉取 / 凭据管理 / 代理管理 / 设置）、三轮复跑稳定性、images-page 4 处弹窗作用域修复（antd 关闭后弹窗残留在 DOM，须加 `:not(.ant-popover-hidden)` 作用域，修复后断言从 **5277ms → 83ms**）。

逐条结果、选择器策略与 8 行矩阵见 [F 组报告](./f-group-0.5.18-2026-09-27.md)。本节结论：**功能面可用**，未发现功能性回归；缺陷集中在破坏性与故障态路径（见下）。

### 3.2 破坏性路径四格矩阵

| 场景 | runId | 结果 | 时长 | 步 | 说明 |
| --- | --- | --- | --- | --- | --- |
| 真拉取 | `r-20260927151035-e2ed` | passed | 6574ms | 14/14 | 真触发 pull job 并观测终态 |
| 真删 tag | `r-20260927151558-f898` | passed | 5598ms | 18/18 | tag 级删除 + 影响面确认件 |
| 真删仓库 | `r-20260927152556-c6af` | passed | 4988ms | 16/16 | 仓库级删除正常路径 |
| 真删错误路径 | `r-20260927150136-909b` | **failed** | 19096ms | 21/23 | 打中 **P1**（零 tag 僵尸仓库删不掉） |

⇒ 删除的**正常路径可用**，但**边界路径（仓库存在、tag 数为 0）不可用**，且错误信息误导（500 INTERNAL_ERROR）。

### 3.3 GC 量化（打中 P2）

| 场景版本 | runId | 结果 | 时长 | 步 |
| --- | --- | --- | --- | --- |
| gc-real v1（仅断言 toast 出现） | `r-20260927151653-bdaf` | passed | 4474ms | 10/10（**假绿**） |
| gc-real v2（断言 toast **内容形状**） | `r-20260927153442-2622` | **failed** | 4729ms | 步 1~9 passed，步 10 红 |

v2 相对 v1 的**唯一增强**就是把「toast 出现」提升为「toast 内容必须含数字且不含 `undefined`/`NaN`」—— 正是这条断言暴露了 P2。这同时是一条方法论证据：**只断言元素可见的用例会漏掉数据绑定缺陷**。

### 3.4 故障注入（两档）

注入机制：`docker pause cairn`（保留 listen 端口 ⇒ 客户端感知为**超时**而非拒连；`docker stop` 会产生 RST/拒连，落到 `NETWORK_ERROR` 档，故不用）。`docs/ROADMAP.md:83` 原文口径为「后端不可达**或 `kill -STOP`** 时，5 个页面均在 ≤10s 内显示错误态」⇒ `docker pause` 正是 **`kill -STOP` 分支**。

跨机 rendezvous：158 侧 `slogLogger`（`internal/api/api.go:54`，挂在根 router）对每请求打一行 `{"msg":"http","path":…}` ⇒ 场景先请求**唯一 TOKEN 路径** `__fi_stall_probe_b7d4e1`，158 relay `docker logs -f | grep -q -m1 <TOKEN>` 命中即注入。TOKEN 在容器日志中出现 **1 次**（唯一性成立）。

**单页档**（`fault-timeout.yaml`，17 步）：

| 步 | 观测 | 值 |
| --- | --- | --- |
| 5 | 冻结确认 | `{"state":"PAUSED","probes":3,"elapsedMs":1047}` |
| 8 | 页面错误态 | `{"found":true,"elapsedMs":10022,"within10s":true}` |
| 11 | 解冻恢复 | `{"state":"RECOVERED","probes":13,"elapsedMs":8652}` |
| 14 | 恢复后复核 | `{"pre":"MISSING","recovered":true}` |

runId `r-20260928022812-447a`，**passed**，23526ms，17/17。

**多页档**（`fault-stall-multipage.yaml`，16 步）：

| 页 | 错误态出现 | 耗时 | 错误分类 | ≤10s |
| --- | --- | --- | --- | --- |
| 镜像热度 stats | found | **9814ms** | `TIMEOUT` | ✅ |
| 镜像拉取 pull | found | **10016ms** | `TIMEOUT` | ✅ |
| 凭据管理 credentials | found | **10006ms** | `TIMEOUT` | ✅ |
| 代理管理 proxies | found | **10009ms** | `TIMEOUT` | ✅ |
| 聚合断言（4 页齐备） | — | 23ms | 一致 | ✅ |

runId `r-20260928032154-e0d2`，**passed**，55598ms，16/16。冻结窗口与场景逐点交叉验证：relay 日志 `HIT 11:21:57` / `PAUSED 11:21:57` → `UNPAUSE-BEGIN 11:22:49` / `UNPAUSED 11:22:49`；场景步 1~14 累计 45657ms ⇒ 步 15 起于 11:22:39.7，+9375ms = **11:22:49.1**，与解冻时刻相差 ~100ms。冻结后自愈复核：`Paused=false Status=running Health=healthy Restarts=0`，`/healthz 200` ×4，探针请求重新可达。

阈值口径：`fault-timeout.yaml:161,187` `LIMIT=14000`；判定窗 `elapsed ∈ [8000, 11500] ms`（下限排 `NETWORK_ERROR`，上限排「没走预算」）。

**覆盖面缺口（如实声明，不计入通过）**：ROADMAP `:83` 的 F6 五页口径（credentials / images / proxies / pull / stats）下为 **4/5 达标**，缺口为**镜像列表页**：

- `web/src/pages/images-page.tsx:109-115`：inventory 已跨页缓存（`web/src/App.tsx:52`）⇒ 冻结流中 remount **不发请求**，页面不进入错误态；
- 该页唯一的数据错误源 `:92-107 load()`（`:100 setError(result)`）走 `/api/inventory`，而它在 `web/src/api.ts` 中是**慢档 120s**（`SLOW_TIMEOUT_MS=120_000`）⇒ 即使冷路径也**天然不可能 ≤10s**；
- `:85-90 loadHeat` 失败静默丢弃；`:325-347` 有 `error` Alert 渲染分支，但触发条件受上述两点限制。

另：**`后端不可达`（NETWORK_ERROR）冷启动分支未测**——index.html 与 bundle 由同一进程提供，harness 无法在进程冻结/停止的情况下冷启动 SPA 并区分「静态资源不可达」与「API 不可达」。

`LoadError` 使用面（`web/src/components/load-error.tsx:29`）经 grep 确权：import 者仅 4 页 = `credentials-page.tsx:33`、`stats-page.tsx:24`、`settings-page.tsx:30`、`proxies-page.tsx:38`，且**全部只在 `configFailure` 分支**（`credentials:337-344`、`stats:519-525`、`settings:399-404`、`proxies:616-623`）。`pull-page` 未 import（走自有内联 `错误分类：` Alert，`:571`）；`images-page` **既未 import 也无整页 configFailure 态**。设置页数据失败静默丢弃（`settings-page.tsx:257-272`）。

⇒ 结论：错误态覆盖是**按页分别实现**的（4 页 + pull 内联 + images 局部分支），并非统一机制；`≤10s` 在新数据路径上不成立（慢档 120s 与快档 10s 混用）。

---

## 4. 后端 registry 数据面验收详述

用户口径：「看是不是真的支持 docker login、push 和 pull 等常用的场景」。以下全部为 158 容器内一手取证。

### 4.1 匿名回环（可用）

| 项 | 结果 |
| --- | --- |
| `GET /v2/` | 200，响应头带 `Www-Authenticate: Basic realm="cairn"`（**无** `Docker-Distribution-Api-Version`） |
| catalog | 4 个仓库 |
| tag 真相 | `library/busybox:["1.36"]`、`library/alpine:["3.16"]`、`webauto-pull/busybox:[]`、**无 `latest`** |
| `docker pull` | RC=0 |
| `docker push` | RC=0，manifest digest `sha256:b6e67c97…`，945 B，3 层 |
| `push → rmi → pull` 回环 | **RepoDigests 一致**（数据面读写闭环成立） |
| 共享存储验证 | `/v2` 推送的镜像**与 `/api/inventory` 在同一秒内互相可见** ⇒ 数据面与管理面共享同一 `Storage` |

⇒ 结论：**匿名场景下 push/pull 真实可用**，且与管理面同源，无「两套存储」问题。

### 4.2 鉴权矩阵（不通过）

| 场景 | 结果 |
| --- | --- |
| 开鉴权后匿名 `_catalog` | **401** ✅ |
| `curl -u <user>:<pass>` 对照 | 200 / 202 ✅ |
| `docker login` | **恒 `Login Succeeded`**（假阳性）❌ |
| 鉴权后 `docker pull` | rc=1 `unauthorized` ❌ |
| 鉴权后 `docker push` | rc=1 `unauthorized` ❌ |
| 显式注入正确 `X-Registry-Auth` 后 push | 仍 **401**（每层 `Retrying` ×3）❌ |
| 服务端日志 | `POST /v2/webauto-push/cairn/blobs/uploads/ status=401 bytes=72`，**每次恰好两发 401** |

⇒ 定性：**失败点在客户端/守护进程的鉴权装配环节，不在服务端校验**（服务端对正确凭据的 `curl -u` 是放行的）。用户可见表现是「login 说成功、push/pull 说未授权」——这是最容易被误判为「服务端坏」的一类假阳性。

**⚠️ 鉴权已由用户回滚**：`"registryUsername":""`、`"usingAuth":false`；回滚后匿名 catalog/manifest 均 200。注：`GET /v2/` **仍带** `Www-Authenticate` —— 来自 `registryd.New` 的 `getCreds` 闭包恒非 nil，而 `requireBasicAuth` 在 `wantUser=="" || wantPass==""` 时放行。即：**响应头会误导客户端以为需要凭据**（与 docker 客户端交互时的体验噪声），实际放行。

### 4.3 Distribution spec 偏差（7 项）

| # | 偏差 | 实测 |
| --- | --- | --- |
| 1 | manifest `GET`/`HEAD` 与 `/v2/` 缺 `Docker-Distribution-Api-Version` 头 | 缺失 |
| 2 | 不做 `Accept` 内容协商 | 忽略 Accept |
| 3 | blob `Range` 请求不支持 | 不支持 |
| 4 | blob `Content-Type` 为嗅探值（非 `application/octet-stream`） | 嗅探 |
| 5 | 未知仓库 `tags/list` 返回 **200** `{"name":"nosuchrepo","tags":[]}`（spec 期望 **404 NAME_UNKNOWN**） | 200 |
| 6 | `_catalog?n=2` **忽略 `n`** | 全量返回 |
| 7 | 跨仓库 blob mount 直接 202（未按 spec 表达 mount 语义） | 202 |

补充（**符合** spec，作为对照项）：tag 形式 `DELETE /v2/<name>/manifests/<tag>` → **400 `DIGEST_INVALID`**（正确的「只能按 digest 删」）；重复删同一 digest → **404 MANIFEST_UNKNOWN**；bogus digest → 404；plain `uploadStart` → 202 + `Docker-Upload-Uuid` + `Location` + `Range: 0-0`。

### 4.4 B 组（性能/健壮性）落地核验：**全部未落地**

| 项 | 结论 | 源码锚点 |
| --- | --- | --- |
| B1 | 未落地 | `internal/storage/filesystem.go:546-551 lockRepo` 裸阻塞（无 ctx、无超时） |
| B2 | 未落地 | `:670`、`:699` ctx 被丢弃；19 处 `_ context.Context`（`:59/98/118/136/179/215/268/303/317/337/353/370/402/457/483/496/644/670/699`） |
| B3 | 未落地 | 同上（19 处签名即 B3 明细） |
| B4 | 未落地 | `internal/proxies/proxies.go:57-62` `Store` 结构体无 `writeMu` 字段（写路径 `Put:100` → `mu.Lock():112` → `os.WriteFile:169` 落盘，无跨写者串行化；范式见 `internal/credentials/credentials.go:94`） |
| B5 | 未落地 | `internal/pull/queue.go:116-122` `IsTerminal()` 无锁读（`:117` 直读 `j.view.State`；对比 `View()` `:125-130` 持 `j.mu`） |
| B6 | 未落地 | `internal/events/events.go:586-587` |

⇒ 与 `docs/ROADMAP.md:18` 状态列「待开工」一致 ⇒ **0.5.18 是部分落地批次**：前端错误态（F 组）落地、后端 B 组未开工。

### 4.5 主标准 `go test -race ./...`

命令：`GOTOOLCHAIN=auto GOPROXY=https://goproxy.io,direct go test -race ./...`（`go.mod` 要求 ≥ go 1.26.0，本机工具链 1.25.5 ⇒ 自动拉取 `go1.26.0 darwin/arm64`）。

结果：**全绿**（RC=0，`tmps/go-test-race2.log`）

| 包 | 结果 |
| --- | --- |
| `internal/api` | ok 2.389s |
| `internal/credentials` | ok 1.874s |
| `internal/events` | ok 2.145s |
| `internal/proxies` | ok 1.876s |
| `internal/pull` | ok 1.940s |
| `internal/registry` | ok 1.942s |
| `internal/storage` / `registryd` / `server` / `webui` / `db` / `config` / `version` / `cmd/server` | **[no test files]** |

**双写结论（重要）**：绿 ≠ B 组风险已消除。B1/B2/B3 所在的 **`internal/storage` 包零测试覆盖**；`registryd` / `server` / `webui` 同样零覆盖 ⇒ 这条主标准对 B 组**不构成有效门禁**。

补充：`go test -race -count=20 ./internal/pull/` 通过（`tmps/go-test-pull20.log`，`ok cairn/internal/pull 1.322s`）⇒ **B5 的 `IsTerminal()` 缺锁未被现有用例捕获**（无并发调用该路径）。首次 `GOTOOLCHAIN=local` 运行失败（RC=1，`go.mod requires go >= 1.26.0`，`tmps/go-test-race.log`），属工具链版本噪声，非产品缺陷。

### 4.6 L 组追加验收：L1

| 时点 | `GET /api/pull/jobs` |
| --- | --- |
| 重启前 | 200 / 5549 B / 5 条 |
| `docker restart cairn`（rc=0）后 | 200 / **空** |

⇒ 拉取历史重启即清空，与 ROADMAP L1「只写不读」的待开工状态一致 ⇒ **未实现，非新缺陷**。

---

## 5. 构建链路门禁核验（tsc）

| 项 | 事实 |
| --- | --- |
| `web/package.json` `build` | `vite build`（**不含 tsc**） |
| `web/package.json` `type-check` | `tsc --noEmit`（存在，但无人调用） |
| `Dockerfile:2-3/24/35/64` | 只 `RUN pnpm build` |
| 实际 `tsc --noEmit` | **10 个 error TS**，RC=2（`tmps/tsc-out.txt`） |

错误明细：

| 文件:行 | 码 | 说明 |
| --- | --- | --- |
| `web/src/api.ts(225,15)` / `(232,15)` / `(239,15)` | TS2304 | `DeleteRepositoryPayload` / `DeleteManifestPayload` / `GCResult` 三个类型名未 import（`web/src/api.ts:1-27` 的 `import type` 缺这三个；`web/src/types.ts:157/163/171` 均已定义） |
| `web/src/pages/images-page.tsx(310,35)` / `(310,66)` | TS2339 | **即 P2 本身**（读不存在的属性） |
| `web/src/pages/settings-page.tsx` ×5 | TS6133 | 未使用变量 |

⇒ **类型门禁缺失是 P2 能进生产的直接通道**：如果构建链路跑 `tsc --noEmit`，`images-page.tsx:310` 的两处 TS2339 会在构建期就挡住这个缺陷。ROADMAP `:112` 的 L5（TS6133 清零）也对应此处的 5 处。

---

## 6. 覆盖缺口与未测项（总表）

| # | 缺口 | 原因 | 影响 |
| --- | --- | --- | --- |
| 1 | 镜像列表页在冻结流中无 ≤10s 错误态 | inventory 跨页缓存 ⇒ remount 不发请求；唯一数据源走慢档 120s | ROADMAP `:83` 五页口径下 **4/5 达标** |
| 2 | 设置页无 ≤10s 错误态 | 数据失败静默丢弃（`settings-page.tsx:257-272`），仅 `configFailure` 有整页 LoadError | 同上，冷启动未必覆盖 |
| 3 | `NETWORK_ERROR` 冷启动分支未测 | index.html/bundle 与 API 同进程提供，冻结时无法冷启 SPA | 「后端不可达」字面分支缺实测，仅有源码级推断 |
| 4 | B 组无有效门禁 | `internal/storage` / `registryd` / `server` / `webui` **零测试文件** | `go test -race` 全绿不构成 B 组结论 |
| 5 | B5 竞态未复现 | `-count=20` 未触发并发调用 `IsTerminal()` | 缺锁存在但未被用例捕获（非「无问题」证据） |
| 6 | 鉴权面未定位到守护进程内部 | 158 无桌面环境，无法读 docker daemon 客户端装配细节 | 只能定性「客户端/守护进程侧」，未定位到具体代码行 |

---

## 7. 建议与后续（按优先级）

1. **P2（最易修、收益直接）**：修 `web/src/api.ts:168` 的解包语义（或让 `runGC` 读 `data` 层）；**同时把 `tsc --noEmit` 接入 CI/Dockerfile 构建链**——门禁会把 `web/src/pages/images-page.tsx(310,35)/(310,66)` 的两处 TS2339（P2 的类型层投影）挡在构建期，并连带要求补上 `web/src/api.ts` 的 3 处漏 import（TS2304）。
2. **P1**：后端 `handlers_extra.go:594` 对 `chi.URLParam` 结果做 `url.PathUnescape`，并把 `:603-605` 的「任何 err → 500」细化为 `ErrNotFound → 404`。
3. **R-open-2（安全语义）**：让 `allow.delete` 同时约束 `/v2/*` 的 manifest 删除与 `POST /api/gc`，或在 UI 上明确标注「该开关仅影响管理面」。
4. **鉴权面**：修 `docker login` 假阳性（非 2xx 不应报 `Login Succeeded` 的体验落差）与客户端凭据装配路径；回滚后 `GET /v2/` 仍带 `Www-Authenticate` 也建议一并收敛。
5. **错误态收敛**：把逐页实现改为统一机制（4 页 LoadError + pull 内联 + images 局部分支现在是 3 套写法），并对慢档页面明确「用户可见的最坏等待是 120s」这一事实。
6. **spec 偏差 7 项**：按需处理；其中「未知仓库 `tags/list` 返回 200」与「缺 `Docker-Distribution-Api-Version`」最容易被第三方客户端探测到。
7. **B 组**：开工前先给 `internal/storage` 补基础用例，否则改了也无法验证。

---

## 附录 A. 本轮新增场景资产（7 份，含 md5）

| 场景文件 | 字节 | 步数 | md5 |
| --- | --- | --- | --- |
| `tests/web-auto/scenarios/fault-stall-multipage.yaml` | 15333 | 16 | `ef5be9c3effe9e5ce4c33653f5177a44` |
| `tests/web-auto/scenarios/fault-timeout.yaml` | 12883 | 17 | `008250cef80a66540f2aa96c0e96aa5a` |
| `tests/web-auto/scenarios/delete-repo-real.yaml` | 8328 | 16 | `1deee7e699c22117666efa80226c540e` |
| `tests/web-auto/scenarios/pull-real.yaml` | 7405 | 14 | `6baf34a2fd6f11dfac9bf065e881596a` |
| `tests/web-auto/scenarios/delete-real.yaml` | 6750 | 23 | `7107b9d861507084f0dccec333406bdc` |
| `tests/web-auto/scenarios/gc-real.yaml` | 6292 | 11 | `e71d7a96c27ac1a875f2a2cfe0390f60` |
| `tests/web-auto/scenarios/delete-tag-real.yaml` | 6020 | 18 | `4732ae9a1bcc76928a12d419dda3dcc8` |

本地与 53 侧（`/opt/web-auto-runner/web-auto/tests/cairn/`）逐一 `md5sum` 对齐，**无漂移**。

## 附录 B. runId 索引

| runId | 场景 | 结果 | 时长 / 步 |
| --- | --- | --- | --- |
| `r-20260928032154-e0d2` | fault-stall-multipage（多页冻结） | passed | 55598ms / 16-16 |
| `r-20260928022812-447a` | fault-timeout（单页冻结） | passed | 23526ms / 17-17 |
| `r-20260928021920-503f` | fault-timeout（早一轮） | errored | 步 13 后 `step timeout after 17647ms` |
| `r-20260928032137-29e2` | 场景名缺前缀 | errored | 0ms / ENOENT |
| `r-20260927153442-2622` | gc-real v2 | **failed** | 4729ms / 打中 P2 |
| `r-20260927151653-bdaf` | gc-real v1 | passed（假绿） | 4474ms / 10-10 |
| `r-20260927152556-c6af` | delete-repo-real | passed | 4988ms / 16-16 |
| `r-20260927151558-f898` | delete-tag-real | passed | 5598ms / 18-18 |
| `r-20260927151035-e2ed` | pull-real v3 | passed | 6574ms / 14-14 |
| `r-20260927150136-909b` | delete-real | **failed** | 19096ms / 21-23（P1） |

## 附录 C. 复现方式

前端：本地场景经 dufs `PUT http://proxy.example.com:80/<name>` 上传 → 53 上 `cp -f` 到 `/opt/web-auto-runner/web-auto/tests/cairn/`（场景文件新增/覆盖**无需重启 runner**，每请求读盘）→ `POST http://proxy.example.com:8080/api/run`，body：`{"scenario":"cairn/<name>","env":"dev","timeout":300000}`。**注意场景名必须带 `cairn/` 前缀**（`SCENARIOS_DIR=/scenarios`，缺前缀 → `ENOENT`，duration 0）。

故障注入：158 上 `setsid bash /root/fi-relay-stall.sh`（`docker logs -f --tail 5 cairn | grep -q -m1 '<TOKEN>'` → `docker pause` → `sleep 52` → `docker unpause`，全程记 `/root/fi-relay-stall.log`），随后跑对应场景。

后端：158 容器内 `env -u HTTP_PROXY -u HTTPS_PROXY -u http_proxy -u https_proxy curl ...`（158 侧 curl 必须脱代理）。

## 附录 D. 本轮遗留的本地未跟踪产物

`tmps/`（已被 `.gitignore` 覆盖）：`tsc-out.txt`、`go-test-race.log`、`go-test-race2.log`、`go-test-pull20.log`、`run-*.json`、`run-*.err`、`bundle-verify.js`、`mk-*.py`、`patch-*.py` 等中间产物；均可随手清理，不随本报告提交。

---

**报告结论**：0.5.18-dev 在**功能面可用**（F 组全绿、匿名 push/pull 回环成立、破坏性正常路径可用），但存在 **3 个需修的真缺陷**（P1 僵尸仓库删不掉、P2 GC 提示 `undefined/NaN`、R-open-2 删除门禁不守数据面）、**鉴权面不可用**（login 假阳性 + push/pull 恒 401）、**错误态覆盖 4/5 页**、以及 **B 组 6 项全部未开工且核心包零测试**。类型门禁（`tsc --noEmit`）未接入构建链路，是 P2 流入生产的直接通道。
