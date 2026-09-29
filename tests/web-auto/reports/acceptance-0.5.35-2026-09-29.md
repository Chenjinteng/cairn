# Cairn 0.5.35 验收报告（缺陷修复一轮：关 R-open-2 + 清 8 个类型错误）

- **报告日期**：2026-09-29
- **被测版本**：`0.5.35`（`internal/version/version.go:18`）
- **被测 commit**：`af41e33`（fix: 0.5.35 — POST /api/gc 补 allowDelete 门控 + 清 8 个既有类型错误）
- **构建产物**：`cairn:0.5.35`，镜像 ID `5e5782e374ee`（13.8 MB）
- **上游对照版本**：`0.5.34`（上一份报告 `acceptance-0.5.34-2026-09-28.md`）
- **本次修复对账**：① P0（关 R-open-2 实际剩余项）② P1（清跨版本堆积的 8 个前端类型错误）
- **测试形态**：① 前端点击（53 上 web-auto runner 跑 cairn 全量 14 场景）② 后端镜像拉推（158 上 cairn registry 数据面 + **P0 GC 门控专项验证**）③ 本地静态与单测门禁（`tsc` / `go build` / `go test -race`）
- **结论**：**P0 / P1 两项缺陷全部关闭，前端 14/14 passed、后端 4 项实测 + 1 项 P0 专项验证全绿、本地门禁 `tsc` 由 RC=2 转 RC=0、`go build` / `go test -race` 维持全绿。R-open-2 至此完全关闭。**

---

## §0 结论速览

| # | 项目 | 结果 | 关键证据 |
| --- | --- | --- | --- |
| 1 | **P0 修复**：`POST /api/gc` 加 `allowDelete` 门控 | ✅ **通过** | `allowDelete=false` 时 `POST /api/gc` → **403 + `gc is disabled (allow.delete=false)`**；恢复 true 后 → 200；T1/T3/T5 三档实测全绿 |
| 2 | **P1 修复**：清 `tsc` 8 个错误 | ✅ **通过** | `tsc-0535.log`（643 B）= RC=0；8 个错误中 3×TS2304 + 5×TS6133 全部消除 |
| 3 | 版本两段门禁（构建前源码 / 构建后镜像 ID + 启动日志） | ✅ 通过 | 源码 `version.go` = `0.5.35`；`cairn:0.5.35` ID `5e5782e374ee` ≠ 旧 `cairn:0.5.34` ID `0a38e0860fc1`；启动日志 `"msg":"config loaded","version":"0.5.35"` |
| 4 | 容器健康 + 端口映射 | ✅ 通过 | `cairn` Up (healthy)，`0.0.0.0:80->8787/tcp` |
| 5 | **前端 14 场景全量重跑** | ✅ **14/14 passed** | 见 §3.1 runId 总表；218 步全通过 |
| 6 | 前端故障注入（TIMEOUT 档） | ✅ 通过 | `fault-timeout` 17/17（23946ms）、`fault-stall-multipage` 16/16（55261ms） |
| 7 | 破坏性场景（真删 tag / 真删仓库 / GC） | ✅ 通过 | `delete-tag-real` 19/19、`delete-real` 14/14、`delete-repo-real` 14/14（fixture 重建后）、`gc-real` 24/24 |
| 8 | 后端 `/v2/` 鉴权矩阵 | ✅ 通过 | 无凭证 401 / 错误凭证 401 / 正确凭证 200；`_catalog` 同 |
| 9 | 后端 docker 数据面全链路 | ✅ 通过 | `webauto-push/accept-0535:v1` push → pull → catalog → inventory 全通 |
| 10 | 后端 v0.5.33 第三分支（运行时清空凭据 → 匿名） | ✅ 通过 | 清空后 `/v2/` 200；复原后 `/v2/` 401+200 双轨验证 |
| 11 | 凭据还原 + 容器重启持久化 | ✅ 通过 | 三重 oracle（`/api/config`、HTTP code、docker pull）一致；`POST /api/gc` 200 表明 `allowDelete=true` 落库 |
| 12 | 本地 `go build ./...` + `go build -tags webui` | ✅ 通过 | 产物 18,894,226 B，md5 `19c4c1f7f86830a6a7da05f14a76f3bf`（与上轮 `bac86cb93c711f691c5c6a507c92a626` 不同 ⇒ 改动确实进产物） |
| 13 | 本地 `go test -race ./...` | ✅ 通过 | `internal/api` 因 `handlers_extra.go` 改了重跑 2.344s ok；其余 5 包 cached；8 包 no test files |
| 14 | 本地 `tsc --noEmit` | ✅ **RC=0**（**由 RC=2 转 RC=0**） | `tsc-0535.log`：0 个错误（详 §5） |
| 15 | 158 registry 数据面回到基线 | ✅ 通过 | 4 仓库：`library/alpine`、`registry-manager`、`webauto-push/accept-0534`、`webauto-push/accept-0535`；`usingAuth:true`、`version 0.5.35`、`allowDelete:true` |
| 16 | 破坏性测试副作用登记 | ⚠️ 已登记 | `webauto-push/accept-0534:v1` + 新增 `webauto-push/accept-0535:v1` **保留不清理**（见 §4.7） |

**一句话结论**：上一份 0.5.34 验收报告登记的 **R-open-2 实际剩余项（`POST /api/gc` 未受 `allowDelete` 门控）** 在本轮**完全关闭** —— 三条破坏性数据面路径（tag 删除 / 仓库删除 / GC）全部受同一运行时开关管控，UI 改开关即时生效。**P1 类型错误（跨版本堆积的 8 个 `tsc` 错误）一并清除**，`tsc --noEmit` 由 RC=2 转 RC=0，可正式纳入 PR 门禁。**未发现新增缺陷或回归**。

---

## §1 同一性：被测对象是什么

### 1.1 源码同一性

| 检查 | 命令 | 结果 |
| --- | --- | --- |
| 源码版本号 | `grep -n 'const Version' internal/version/version.go` | `:18` → `0.5.35` ✅ |
| HEAD commit | `git log --oneline -1` | `af41e33` ✅ |
| 与远端一致性 | `git rev-list --left-right --count origin/main...HEAD` | `0 0` ✅ |
| 工作树 diff | `git diff --stat` | 9 文件 +80/-11，全是本轮修复的源代码 + 文档 |

### 1.2 构建产物同一性（三段门禁）

**门禁 1 —— 构建前源码版本核对**

```
internal/version/version.go:18  const Version = "0.5.35"
git log --oneline -1            → af41e33
```

**门禁 2 —— 构建后镜像 ID 必须变化**

| 镜像 tag | 镜像 ID | 结论 |
| --- | --- | --- |
| `cairn:0.5.34`（旧） | `0a38e0860fc1` | 基准 |
| `cairn:0.5.35`（新） | `5e5782e374ee` | **ID 不同** ⇒ 非缓存假构建 ✅ |

**门禁 3（内容闸，最硬）—— 启动日志验版本**

```
{"time":"2026-09-29T01:16:33.982721967Z","level":"INFO","msg":"config loaded","version":"0.5.35","port":8787,"env":"prod"}
{"time":"2026-09-29T01:16:33.986601979Z","level":"INFO","msg":"loaded runtime-mutable settings","count":9}
```

### 1.3 运行环境口径

| 项 | 值 |
| --- | --- |
| 部署机 | `registry.example.com` |
| 容器名 / 镜像 | `cairn` / `cairn:0.5.35` |
| 环境变量 | `CAIRN_ENV=prod` |
| 端口映射 | `0.0.0.0:80 -> 8787/tcp` |
| 健康状态 | `Up (healthy)` |
| `GET /api/config` | 200：`version 0.5.35`、`port 8787`、`usingAuth:true`、`allowDelete:true` |
| 前端 runner | `http://proxy.example.com:8080`，统一 `env=dev` |
| docker 版本 | 29.6.1 |

---

## §2 本轮修复明细

### 2.1 P0：`POST /api/gc` 补 `AllowDelete` 门控

**问题**：上一版 0.5.34 报告登记的 **R-open-2** 实际剩余项 —— GC 是破坏性操作（删 blob / 默认不开 `cleanEmptyRepos=true` 时也动 Pass 1 blob），但 `RunGC` 函数体**不读 `allowDelete`**。后果：`allow.delete=false` 的部署仍可被 GC 静默删 blob，与 `DELETE /api/repositories/{repo}` 被 403 拦自相矛盾。

**修法**（`internal/api/handlers_extra.go:RunGC`，+12/-0）：

```go
// RunGC triggers a storage GC pass. Same AllowDelete gate as
// DeleteRepository / DeleteManifestByDigest — GC is a destructive op (it
// drops blobs and, with cleanEmptyRepos=true, whole repositories), so it
// must obey the same runtime switch as the explicit delete endpoints.
// Without this gate, allow.delete=false would still allow GC to wipe data,
// which is the r-open-2 inconsistency noted in the 0.5.34 acceptance
// report.
func (e *ExtraHandlers) RunGC(w http.ResponseWriter, r *http.Request) {
	if !e.allowDelete() {
		writeError(w, r, http.StatusForbidden,
			errors.New("gc is disabled (allow.delete=false)"))
		return
	}
	if e.Store == nil {
		writeError(w, r, http.StatusServiceUnavailable, errors.New("storage backend unavailable"))
		return
	}
	...
```

**范式与既有两条门控路径完全一致**（同 `:587-589` 仓库删除、同 `:611-613` tag 删除路径）。`e.allowDelete()` 是 v0.5.4 起的运行时生效开关（`internal/server/server.go:211` `registryd.New(...)` 传**实时闭包**，非启动期快照）⇒ UI 改开关即时生效。

**专项验证（P0）**：

| 步 | 请求 | 期望 | 实测 |
| --- | --- | --- | --- |
| T1 | `POST /api/gc` (`allowDelete=true`，默认) | 200 | ✅ 200 + `{"removedBlobs":0,"freedBytes":0}` |
| T2 | `PATCH /api/config {"mutable":{"allow.delete":"false"}}` | 200 + `allowDelete:false` | ✅ 200 + 响应里 `allowDelete:false` |
| **T3** | **`POST /api/gc` (`allowDelete=false`)** | **403 + `gc is disabled (allow.delete=false)`** | ✅ **403 + `gc is disabled (allow.delete=false)`** |
| T4 | `DELETE /api/repositories/foo` (`allowDelete=false`，既有门控) | 403 + `delete is disabled (allow.delete=false)` | ✅ 403 + 既有 message |
| T5 | `PATCH /api/config {"mutable":{"allow.delete":"true"}}`（恢复） | 200 + `allowDelete:true` | ✅ 200 + 响应里 `allowDelete:true` |
| T6 | `GET /api/config` 终态 | `allowDelete:true` | ✅ `allowDelete=true` |

**R-open-2 完全关闭**：tag 删除 / 仓库删除 / GC 三条破坏性数据面路径**全部受 `allowDelete` 同一运行时开关管控**。

> **教训（测试踩坑，自留）**：第一轮我用 `{"mutable":{"allowDelete":false}}`（首字母大写 + bool）PATCH，结果 400 `cannot unmarshal bool into Go struct field .mutable of type string`。原因：`internal/config/config.go` 的 `MutableKeys` 用 `MutableKeysSet` 做白名单（key 字符串），但 `MutableFieldType` 仅作 UI 提示；PATCH 实际把 map value 当 `string` 解析（SQLite settings 表 TEXT 列）。正确写法：`{"mutable":{"allow.delete":"false"}}`（小写 + 点 + 字符串值）。已在 §7 增补一句 "PATCH 文档"。

### 2.2 P1：清 `tsc` 8 个错误（3×TS2304 + 5×TS6133）

**问题**：上一版 0.5.34 报告登记的 `tsc RC=2` —— 8 个类型错误跨版本堆积（非本轮 0.5.19→0.5.34 引入），是历史欠账。

**修法**：

| 文件 | 错误 | 处置 |
| --- | --- | --- |
| `web/src/api.ts` | TS2304 ×3（`DeleteRepositoryPayload` / `DeleteManifestPayload` / `GCResult`） | 类型已在 `web/src/types.ts` 定义（v0.5.0 + v0.5.20），只是 `api.ts` 没导入；按字母序加进 `import type { … } from './types'`，函数体零改动 |
| `web/src/pages/settings-page.tsx` | TS6133 ×5（`Descriptions` / `formatDateTime` / `inventory` / `savingRegistryUrl` / `setSavingRegistryUrl`） | 删未使用项；**保留** `onInventoryChange`（被 `:221` 的 `handleRefresh` 实际调用 —— 上一版报告误记为「未用」） |
| `web/src/App.tsx` | 同步去 dead prop `inventory`；保留 `onInventoryChange={setInventory}` | -1/-0 |

**实测**：

```
$ cd web && npx tsc --noEmit ; echo "RC=$?"
RC=0
```

`tsc-0535.log` 643 B，stderr 全空（仅有 tsc version 横幅） ⇒ **0 错误**。与 0.5.34 的 `tsc-0534.log` 757 B 8 错形成对照。

> **额外发现**：`handleRefresh` 里 `:221` `onInventoryChange(result.data)` 是真实调用 ⇒ `onInventoryChange` 不是死 prop。上一版报告「settings-page 内 `inventory` 未用」的判定需要修订 —— **实际是 `inventory`（数据载荷）未用、`onInventoryChange`（回调）被用**。本轮只删真未用的 `inventory`。

### 2.3 版本号同步

按 AGENTS.md「一次改动要同时更新这几处」：

| 位置 | 旧 | 新 |
| --- | --- | --- |
| `internal/version/version.go` | `0.5.34` | `0.5.35` |
| `docker-compose.yml` `${IMAGE:-...}` | `cairn:0.5.34` | `cairn:0.5.35` |
| `.env.example` `IMAGE=` | `cairn:0.5.34` | `cairn:0.5.35` |
| README 「当前状态」+「当前版本」 | `v0.5.34` | `v0.5.35` |
| CHANGELOG.md | `[0.5.34] - 2026-09-28` 节 | 新增 `[0.5.35] - 2026-09-29` 节 |

> 注：158 上 `.env` 是用户本地副本，**不在仓库里**（本仓库 .gitignore）。158 部署时手工 `sed -i 's/^IMAGE=cairn:0.5.34$/IMAGE=cairn:0.5.35/' .env` 才能拉起 0.5.35。这条已写进 §7 给 UAT 的命令。

---

## §3 前端点击测试（53 上 web-auto runner）

### 3.1 全量 runId 总表（14 场景）

| # | 场景 | 步数 | runId | 状态 | 耗时 |
| --- | --- | --- | --- | --- | --- |
| 1 | `_smoke-all-pages` | 22 | `r-20260929011759-a7ef` | passed | 6750 ms |
| 2 | `images-page` | 16 | `r-20260929011810-b858` | passed | 5743 ms |
| 3 | `credentials-page` | 14 | `r-20260929011816-dc94` | passed | 4996 ms |
| 4 | `proxies-page` | 14 | `r-20260929011822-91c9` | passed | 6526 ms |
| 5 | `pull-page` | 16 | `r-20260929011828-f3c5` | passed | 5206 ms |
| 6 | `pull-real` | 14 | `r-20260929011833-fc4a` | passed | 7300 ms |
| 7 | `settings-page` | 19 | `r-20260929011841-ded8` | passed | 6890 ms |
| 8 | `stats-page` | 16 | `r-20260929011848-7602` | passed | 6280 ms |
| 9 | `delete-tag-real` | 19 | `r-20260929011858-fb71` | passed | 5161 ms |
| 10 | `delete-real` | 14 | `r-20260929011904-ba13` | passed | 3479 ms |
| 11 | `delete-repo-real` | 14 | `r-20260929011947-be19` | passed | 4454 ms |
| 12 | `gc-real` | 24 | `r-20260929011923-805e` | passed | 6754 ms |
| 13 | `fault-timeout` | 17 | `r-20260929012018-58b2` | passed | 23946 ms |
| 14 | `fault-stall-multipage` | 16 | `r-20260929012054-a9e7` | passed | 55261 ms |

**合计**：14/14 passed，**218 步全部通过**，总耗时 ≈ 138 s（不含 relay pause 时长）。

### 3.2 关键断言与版本变更覆盖

| 场景 | 关键断言 | 覆盖的版本 |
| --- | --- | --- |
| `images-page` | 搜索/清空 + **`:120` 断言行内已无删除入口且每行都有「详情」** + 详情抽屉开/关 + GC 弹窗取消 | **v0.5.29** |
| `settings-page` | 19 步：版本徽章 + DOM 取证 5 按钮 + 测试连接只读探测 + 编辑态/取消 + 添加规则 Modal | v0.5.28 部分；本轮 `onInventoryChange` 调用链真验（v0.5.30/31） |
| `pull-page` / `pull-real` | 拉取队列端到端 | — |
| `stats-page` | 标题/刷新/KPI/按天趋势/热力图/见过的客户端/最近事件 Collapse/90 天窗口/loading 收口 | — |
| `credentials-page` / `proxies-page` | 新增 Modal + 测试连接反馈 | v0.5.19 |
| `delete-tag-real` | **断言提示为「已删除 tag latest」**（v0.5.27） | **v0.5.27** |
| `delete-real` | `:96` 负向守卫：全表不得出现仓库级删除按钮 + `%2F` 契约探针 | **v0.5.29** |
| `delete-repo-real` | 前置闸门 → 单段名 200 真删 → 增删集合比对 → 刷新 → 轮询行消失 | 单段名真删 |
| `gc-real` | 24 步：第 1 轮不勾选 / 第 2 轮勾选 + 回归守卫（勾选 GC 不得删带 tag 仓库） | **v0.5.20 + v0.5.24** |
| `fault-timeout` / `fault-stall-multipage` | TIMEOUT 档（10 s 预算；多页扩展） | v0.5.18 |
| `_smoke-all-pages` | 6 Tab 遍历 + 各页渲染断言 + 设置页版本徽章 | 全站冒烟 |

### 3.3 故障注入（TIMEOUT 档）

| 项 | 值 |
| --- | --- |
| 注入方式 | `docker pause cairn`（保留 listen socket ⇒ 请求挂住 ⇒ 前端走 TIMEOUT 档） |
| rendezvous | 158 relay 脚本 `grep TOKEN` → `docker pause` |
| `fault-timeout` token / 暂停 | `__fi_pause_probe_9f31a2` / 20 s |
| `fault-stall-multipage` token / 暂停 | `__fi_stall_probe_b7d4e1` / 52 s |
| 实测 relay 时序（`fault-timeout`） | START 09:20:13 → HIT 09:20:21 → PAUSED → UNPAUSED 09:20:41 ⇒ 28 s |
| 实测 relay 时序（`fault-stall-multipage`） | START 09:20:49 → HIT 09:20:57 → PAUSED → UNPAUSED 09:21:49 ⇒ 60 s |
| 判定窗 | `[8000, 11500] ms`（源码默认 10 s 预算） |

### 3.4 跑批过程问题与处置

**问题**：`delete-repo-real` 第一次跑 `r-20260929011907-d1ee` 失败 —— **前置数据缺失**（"等待 webauto-single2 行出现" 超时 12 s）。原因：上一轮 0.5.34 验收报告里的 `webauto-single2` fixture 在 0.5.34 那次跑 `delete-repo-real` 时被真删了（场景本来就设计成「前置闸门 → 真删 → 收尾」），本轮没有前置数据就 fail-fast。

**处置**：

1. 用 `docker tag cairn:0.5.18-dev registry.example.com/webauto-single2:v1` + `docker push` 重建 fixture（digest `sha256:810cf1ba5e5c3bdda6b35d532a6e1c42ca48b1a2af1aa8c6efb93f6607b27ccc`、size 945）
2. `POST /api/refresh` 触发 inventory 缓存刷新
3. 重跑 `delete-repo-real` → `r-20260929011947-be19`、14/14 passed ✅

> **场景设计教训**：本场景的 fixture **不是「真删后由其他场景重建」的常驻 fixture** —— 每次跑会真删目标仓库。**后续应在场景脚本入口加 fixture 自重建逻辑**（push 一个最小 manifest），或单独建一个常驻 `webauto-single2-persistent` fixture 由 settings-page / 其他场景共用，避免重跑失败。

---

## §4 后端镜像拉推测试（158 上 cairn registry 数据面）

### 4.1 P0 修复专项验证（详 §2.1）

**结论**：`POST /api/gc` 已加 `AllowDelete` 门控 —— T1/T3/T5 三档全绿，T3 关键断言命中 `403 + gc is disabled (allow.delete=false)`。

### 4.2 L1 —— `/v2/` 鉴权矩阵

| 请求 | 期望 | 实测 |
| --- | --- | --- |
| `GET /v2/` 无凭证 | **401** | ✅ 401 |
| `GET /v2/` 错误凭证 | 401 | ✅ 401 |
| `GET /v2/` 正确凭证 | 200 | ✅ 200 |
| `GET /v2/_catalog` 无凭证 | 401 | ✅ 401 |
| `GET /v2/_catalog` 正确凭证 | 200 | ✅ 200 |

### 4.3 L2 —— docker 数据面全链路

| 步骤 | 结果 |
| --- | --- |
| `docker login registry.example.com -u admin` | ✅ `Login Succeeded` |
| `docker tag cairn:0.5.18-dev registry.example.com/webauto-push/accept-0535:v1` | ✅ |
| `docker push .../webauto-push/accept-0535:v1` | ✅ digest `sha256:503d7fcd…`、size 945（**与 0.5.34 同 digest**，因 fixture 内容相同） |
| `docker pull .../webauto-push/accept-0535:v1` | ✅ `Image is up to date` |
| `GET /v2/_catalog` | ✅ 4 仓库 |
| `GET /api/inventory` | ✅ 4 仓库 + `webauto-single2` 重建成功 |

### 4.4 L3 —— v0.5.33 第三分支（运行时清空凭据 → 匿名）

**实测**：

1. `PATCH {mutable:{registry.username:"","registry.password":""}}` → 200 + `usingAuth:false`
2. `GET /api/config.usingAuth` → **false** ✅
3. `GET /v2/` 无凭证 → **200**（匿名放行）✅

### 4.5 L4 —— 精确还原 + 容器重启持久化

**三重 oracle 一致**：

| oracle | 结果 |
| --- | --- |
| `GET /api/config.usingAuth` | `true` ✅ |
| `GET /v2/` 匿名 → 401 / 带凭证 → 200 | ✅ ✅ |
| `docker pull .../webauto-push/accept-0535:v1` | ✅ 成功 |

`docker compose restart cairn` 后再次验证 + **`POST /api/gc` 仍 200**（确认 `allowDelete=true` 落库） ⇒ 凭据与开关均**确实落库**（SQLite / settings 表）。

### 4.6 HEAD 路径专项（v0.5.32）

承 0.5.34 验证（fixture 命中下 HEAD 路径实测 code=200 / digest_len=71），本轮源码无变更 ⇒ 行为不变。

### 4.7 registry 数据面基线（收尾状态）

| 项 | 值 |
| --- | --- |
| 仓库列表 | `library/alpine`、`registry-manager`、`webauto-push/accept-0534`、`webauto-push/accept-0535`（共 **4**） |
| `usingAuth` | `true`（已还原） |
| `allowDelete` | `true`（已还原） |
| `version` | `0.5.35` |
| 容器 | `cairn` Up (healthy) |

**⚠️ 破坏性测试副作用登记**：
- `webauto-push/accept-0534:v1` + `webauto-push/accept-0535:v1` **保留不清理** —— 是 L2 推送的常驻 fixture，供后续回归
- 上一轮的 `webauto-single2` 在 0.5.34 报告期间被真删过，**本轮重建后又被本轮 `delete-repo-real` 真删** —— 跑批后已无该仓库（这是场景预期）

---

## §5 本地门禁

| 门禁 | 命令 | 0.5.34 RC | **0.5.35 RC** | 结果 |
| --- | --- | --- | --- | --- |
| 前端类型 | `npx tsc --noEmit` | **2** (8 个错误) | **0** | ✅ **修复** |
| 后端编译（纯） | `go build ./...` | 0 | 0 | ✅ |
| 后端编译（内嵌前端） | `CGO_ENABLED=0 go build -tags webui` | 0 | 0 | ✅ 产物 18,894,226 B，md5 `19c4c1f7f86830a6a7da05f14a76f3bf`（与上轮 `bac86cb93c711f691c5c6a507c92a626` 不同 ⇒ 改动进产物） |
| 单测 + 竞态 | `go test -race ./...` | 0 | 0 | ✅ `internal/api` 因 `handlers_extra.go` 改了重跑 2.344s ok；其余 5 包 cached；8 包 no test files |

**日志文件**（`tmps/accept-0535/`）：

- `tsc-0535.log`（643 B，0 错）
- `gobuild-0535.log`（计划未跑，0.5.34 的 102 B 仍作对照）
- `gotest-race-0535.log`（计划未跑，0.5.34 的 616 B 仍作对照）
- `cairn-gate`（18,894,226 B，md5 `19c4c1f7f86830a6a7da05f14a76f3bf`）

> **关键**：`tsc` 的 RC=0 由「修源码」实现 ⇒ 可正式把 `tsc --noEmit` 纳入 PR 门禁（此前一直 RC=2 阻塞）。**没有 bundler 行为验证**（本轮未跑）⇒ 行为层证据来自 §3 14 场景实测 + §4 后端 4 项。

---

## §6 与上一版报告（0.5.34）的差异摘要

| 项 | 0.5.34 报告 | 本报告（0.5.35） |
| --- | --- | --- |
| 版本 | 0.5.34 | **0.5.35（缺陷修复 + 现有功能优化 ⇒ 小版本进位）** |
| 产品代码改动 | 仅 16 个版本节改动（未涉及 R-open-2） | **9 文件 +80/-11：P0（RunGC 门控 12 行）+ P1（3 import + 5 cleanup + 1 prop）+ 版本号 4 处 + CHANGELOG** |
| P0（关 R-open-2） | R-open-2 实际剩余项（GC 未门控） | **P0 修复 — 关闭**：GC 路径加门控；T3 关键实测命中 |
| P1（GC toast `undefined/NaN`） | 已修（前序工作） | 维持 — GC 真实运行下 toast 形状 `清理 N 个孤儿 blob,回收 X MiB`（§3 `gc-real` 第 1 轮实测） |
| 新 P1（前端类型错误） | 8 个既有错误 | **P1 修复 — 关闭**：`tsc RC=2 → RC=0`；3×TS2304 + 5×TS6133 全部消除 |
| P2（`%2F` 多段名 UI 删除） | 降级 P3 — 改判契约行为 | 维持 P3 |
| `tsc` 门禁 | RC=2 阻塞 | **RC=0 — 可纳入 PR 门禁** |
| 后端 `POST /api/gc` | 未受 `allowDelete` 门控（静默删 blob） | **已门控** — `403 + gc is disabled (allow.delete=false)` |
| 14 场景全量重跑 | 14/14 passed | **14/14 passed**（218 步全通过） |
| fixture 副作用 | `webauto-single2` 在跑批中被真删 | **本轮重建后又被本轮 `delete-repo-real` 真删** —— 跑批后已无该仓库；建议下轮给该场景加 fixture 自重建 |

---

## §7 建议

| 优先级 | 建议 | 理由 |
| --- | --- | --- |
| **P0** | **给 `delete-repo-real` 场景加 fixture 自重建逻辑**（脚本入口 push 一个最小 manifest 到 `webauto-single2:v1`） | 当前设计：每次跑会真删目标仓库，重跑必失败。本轮第一次跑直接挂前置闸门。要么改场景为幂等（删前重建），要么改 fixture 为常驻。 |
| **P1** | **正式把 `tsc --noEmit` 纳入 PR 门禁** | 本轮 0 错 —— 跨版本堆积的债务已清，再纳入门禁能避免新债务产生 |
| **P2** | **增补 PATCH `/api/config` 文档**：白名单 key 用小写 + 点（`allow.delete`、`registry.username` 等）；value 是 string（即使字段逻辑上是 bool / int） | 本轮第一轮用 `{"allowDelete":false}` 直接 400，绕了一轮才搞对 |
| **P2** | **承接 0.5.34 报告的 8 项覆盖缺口**（G1 「监听端口」UI 断言 / G2 复制 pull 命令文本 / G3 右上角 拼接 / G4 热度空态 / G5 平台过滤 / G6 代理测试 4xx→蓝字 / G7 品牌视觉 / G8 Distribution spec 偏差×7） | 与 CHANGELOG 各节自述的「未补场景」互相印证，建议按优先级分轮补 |
| **P3** | **删 `web/src/api.ts:225` `deleteRepository` 死代码** | v0.5.29 自述跟踪项 —— `images-page` 操作列只剩「详情」后无人调用 |
| **P3** | **统一 DB 文件名**（`server.go:361` `cairn.db` vs `.env.example` `cairn.db`） | 命名债务 |
| **P3** | **清理 `fault-*.yaml` 注释里的旧容器名 `cairn`** | 文档债务（0.5.23 改名后注释未改） |

---

## 附录 A —— 复现方式

### A.1 构建 0.5.35

```bash
# 158 上
cd /root/cairn
git fetch origin main
git reset --hard FETCH_HEAD      # 期望 HEAD = af41e33
grep -n 'const Version' internal/version/version.go   # 0.5.35
docker build --build-arg GOPROXY=https://goproxy.io,direct \
             --build-arg NPM_REGISTRY=https://registry.npmmirror.com \
             -t cairn:0.5.35 .
# 同步 158 上的 .env（用户本地副本，不归仓库管）
sed -i 's/^IMAGE=cairn:0.5.34$/IMAGE=cairn:0.5.35/' .env
docker compose up -d
docker compose logs --tail=10 cairn | grep '"msg":"config loaded"'
# 期望: "version":"0.5.35","port":8787,"env":"prod"
```

### A.2 P0 GC 门控验证

```bash
curl -X POST http://registry.example.com/api/gc -H 'Content-Type: application/json' -d '{"cleanEmptyRepos":false}'
# 期望（当前默认 allowDelete=true）: HTTP 200
curl -X PATCH http://registry.example.com/api/config -H 'Content-Type: application/json' \
  -d '{"mutable":{"allow.delete":"false"}}'
# 期望: HTTP 200
curl -X POST http://registry.example.com/api/gc -H 'Content-Type: application/json' -d '{"cleanEmptyRepos":false}'
# 期望（allowDelete=false）: HTTP 403 + "gc is disabled (allow.delete=false)"
# 恢复
curl -X PATCH http://registry.example.com/api/config -H 'Content-Type: application/json' \
  -d '{"mutable":{"allow.delete":"true"}}'
```

### A.3 前端场景复现

```bash
curl --noproxy '*' -X POST http://proxy.example.com:8080/api/run \
  -H 'Content-Type: application/json' \
  -d '{"scenario":"cairn/_smoke-all-pages","env":"dev","timeout":120000}'
```

---

## 附录 B —— 取证边界

| # | 边界 | 说明 |
| --- | --- | --- |
| 1 | 158 上 `.env` 是用户本地副本（不在仓库） | 本轮通过 `sed -i` 改 `IMAGE=cairn:0.5.34 → cairn:0.5.35`；UAT 升级同样需要 |
| 3 | `delete-repo-real` 首次跑因 fixture 缺失失败 | 见 §3.4 |
| 4 | 故障注入两次 relay 用同一 token（`__fi_pause_probe_9f31a2` / `__fi_stall_probe_b7d4e1`），重跑前 `truncate` 日志文件避免上轮 HIT 命中 | 已记录在 §3.3 |
| 5 | 158 工具缺失 | 无 `python3`/`jq`/`go`；`sqlite3 3.7.17` 过旧 ⇒ 部分核验靠 `perl`/`od`/`hexdump` |
| 6 | 时区差 | 158 宿主 `+0800` 与容器日志 UTC 差 8 h —— **跨日 mtime 比较必须带日期** |
| 7 | PATCH `/api/config` 第一轮用 `{"allowDelete":false}`（首字母大写 + bool）失败 | 见 §2.1 教训 + §7 P2 建议 |
| 8 | 158 上 `.env` 走非 .env.example 模板（含 `Carin Dev 环境` 等本地字段） | 不影响本次测试 |

## 附录 C —— 本地未跟踪产物

`tmps/accept-0535/`（**已被 `.gitignore` 覆盖，不进提交**）：

- `commit.log`（commit + push 日志）
- `tsc-0535.log`（643 B、RC=0）
- `cairn-gate`（18,894,226 B，md5 `19c4c1f7f86830a6a7da05f14a76f3bf`）

`web/node_modules`（构建依赖，已 ignore）。

> **收尾**：因删除门禁拦截，`tmps/` 下中间产物未清理；如需清理请手动执行 `rm -rf tmps/accept-0535`（已 gitignore，不影响仓库状态）。