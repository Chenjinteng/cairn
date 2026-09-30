# 管理 API 缺陷（`/api/*`）

> 被测版本：cairn `0.6.11`（commit `f66bd4d`）｜环境：`registry.local:10001`（容器 `cairn:0.6.11`，`env=prod`）
> 范围：cairn 自有 HTTP 管理 API，编号 `MA-1`、`MA-3`、`MA-4`、`MA-5`、`MA-6`
> 每条包含：现象 / 根因 / 证据（源码 file:line + 实机实测）/ 修复方向
> 修复状态：**全部未修复**。
> 编号说明：`MA-2` 号位与 MA-1 同源（同一路由缺陷的另一处表现），已并入 MA-1，故缺号。

---

## MA-1 · `/api/repositories/{repo}` 路径参数对含 `/` 的仓库名**全部失效**（High）

**现象**

仓库名含 `/`（如 `3proxy/3proxy`）时，所有走 `/api/repositories/{repo}` 的端点都拿不到数据。四种组合实测（158，`2026-09-30T16:39:15Z`）：

| # | 请求 | 实测响应 | 说明 |
| --- | --- | --- | --- |
| a | `GET /v2/3proxy/3proxy/manifests/1.0.0` | **`200`**，739B | 协议侧正常（对照组） |
| b | `GET /api/repositories/3proxy%2F3proxy/tags/1.0.0/manifest` | **`404`** JSON 128B `{"code":"NOT_FOUND",…,"message":"storage: not found"}` | 路由匹配上了，但仓库名变成了字面量 `3proxy%2F3proxy` |
| c | `GET /api/repositories/3proxy/3proxy/tags/1.0.0/manifest` | **`404`** `text/plain` 19B `404 page not found` | 路径段数超出路由模式，压根没匹配上 |
| d | `DELETE /api/repositories/3proxy%2F3proxy` | **`500`** `{"code":"INTERNAL_ERROR",…,"message":"storage: not found"}` 138B | 同 (b) 的机制，落进 500 分支（另见 [MA-4](./management-api.md)） |

**影响面**：158 实机 `_catalog` 共 **77 个仓库，其中 65 个含 `/`（84%）**。也就是说：

- 前端「删除仓库」按钮（`web/src/api.ts:232-236`）对 84% 的仓库直接报错；
- 前端「按 digest 删除 manifest」（`web/src/api.ts:239-243`）同样不可用；
- `GET /api/repositories/{repo}/tags/{tag}/manifest`（`internal/api/api.go:83`）对这批仓库不可用。

**根因**

两层叠加：

1. **chi 的单段参数跨不过 `/`**。`/{repo}/` 只能吃掉一个路径段；仓库名里的 `/` 必须转义成 `%2F` 才能塞进一段。
2. **`%2F` 没有被解码就进了存储层**。chi 匹配时优先用 `r.URL.RawPath`（`chi@v5.3.2 mux.go:452-460`），因此参数值保持 `%2F` 的**原始转义形态**；而 `internal/api/api.go:20-22` 的 `chiURLParam` 只是 `chi.URLParam(r, key)` 的直通包装，**不做任何 URL 解码**。于是存储层收到仓库名 `"3proxy%2F3proxy"`，在磁盘上自然查不到 → `storage: not found`。

对比：**协议侧（`/v2/*`）用完全不同的机制解决了同一个问题**——`internal/registryd/routes.go:115-123` 注释明确写了「chi's named params cannot span `"/"`」，于是改用 `r.HandleFunc("/*", h.dispatchRepoRoute)` 自行切分路径，再用 `injectRouteParams` 把结果塞回 chi route context。**管理 API 没有采用这套办法**，这就是本条缺陷的根源。

前端调用方用的是 `encodeURIComponent(repo)`（`web/src/api.ts:232-243`），输出的正是 `%2F`——**恰好命中失败路径 (b)，而不是 (c)**，所以用户在 UI 上看到的是「`storage: not found`」这类后端存储错误，而不是「找不到路由」。

**证据**

- 源码：
  - `internal/api/handlers_extra.go:139-145`（`/repositories` 子路由注册：`:142` `DeleteRepository`、`:143` `DeleteManifestByDigest`、`:145` `r.Post("/gc", e.RunGC)`）
  - `internal/api/api.go:83`（`r.Get("/repositories/{repo}/tags/{tag}/manifest", h.GetManifest)`）
  - `internal/api/api.go:20-22`（`chiURLParam` = `chi.URLParam` 直通，无解码）
  - `internal/registryd/routes.go:115-123` + `:696-697`（协议侧的自切分 dispatcher，可对照借鉴）
  - `chi@v5.3.2 mux.go:452-460`（RawPath 优先 → 参数保持 `%2F` 原样）
  - `web/src/api.ts:232-236`（`deleteRepository`）、`:239-243`（`deleteManifestByDigest`）—— 均用 `encodeURIComponent`
- 实测：见表中 (a)–(d)；仓库总数与含 `/` 数量取自 `GET /v2/_catalog`（`?n=&last=` 翻页统计，77 / 65）。

**修复方向**

两条路，推荐 (a)：

- **(a) 复用协议侧的做法**：给 `/api/repositories/*` 挂一个 wildcard dispatcher（照 `routes.go:115-123` 的样子切分路径），自行把 `repo` 解出来；这样仓库名里的 `/` 可以**字面出现**（不再需要 `%2F`），前端也可以去掉 `encodeURIComponent`，URL 可读性与可调试性都更好。若仍想支持 `%2F` 形态，需在 `chiURLParam` 里补一次 `url.PathUnescape`（注意先 Unescape 再判空，且要拒绝解出 `..` 的路径）。
- **(b) 改成查询参数**：`DELETE /api/repositories?repo=<name>`，与 `/api/tags`（前端 `web/src/api.ts:225-229` 已在用查询参数风格）保持一致；改动面小，但会破坏现有 URL 形态。

无论选哪条，都要**同时修 `internal/api/api.go:83` 的 `GetManifest`**，并补一组含 `/` 仓库名的接口测试（当前 84% 的真实仓库名都落在未覆盖区间里）。

---

## MA-3 · `POST /api/gc` 非法 JSON body 被静默忽略，仍 `200` 并执行 GC

**现象**

请求体是**截断的非法 JSON**（`{"cleanEmptyRepos": tru`）时：

```
200 {"code":"OK","data":{"freedBytes":0,"removedBlobs":0},"message":"","success":true}
```

服务端不但没报错，还**照常执行了一次 GC**，`cleanEmptyRepos` 静默回落为默认 `false`。调用方无法得知自己传的参数没生效——是典型的「静默语义漂移」：前端/脚本以为开了 `cleanEmptyRepos`，实际没开；反之若默认值将来变成 `true`，一次残缺请求就可能触发清理空仓库这种破坏性操作。

**根因**

- `internal/api/handlers_extra.go:695-698`：注释自称 *"decodeJSON tolerates EOF and surfaces real parse errors"*，但代码是 `_ = decodeJSON(r, &body)`——**错误被显式丢弃**，与注释直接矛盾（注释描述的是期望行为，实现从未落地）。
- `internal/api/handlers_extra.go:1736-1738`：`decodeJSON` 只是 `json.NewDecoder(r.Body).Decode(dst)`，本身不区分「EOF（空 body）」和「真解析错误」——它把分类责任留给了调用方，而调用方（`:698`）没接。

**证据**

- 源码：`internal/api/handlers_extra.go:682-718`（`RunGC`，尤 `:692-694`、`:695-698`）、`:1736-1738`（`decodeJSON`）。
- 实测：`POST /api/gc`，body `{"cleanEmptyRepos": tru`（截断非法 JSON）→ `200`，响应体 `{"code":"OK","data":{"freedBytes":0,"removedBlobs":0},"message":"","success":true}`（注意 `cleanEmptyRepos`/`cleanEmptyRepos` 相关字段未出现在响应里，正是因为走了 `false` 分支）。

**修复方向**

在 `RunGC` 里区分两种 EOF：

```go
err := decodeJSON(r, &body)
if err != nil && !errors.Is(err, io.EOF) {   // 空 body 合法；真解析错误 → 400
    writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON body: "+err.Error())
    return
}
```

或者把容忍 EOF 的逻辑收进 `decodeJSON` 并改名（如 `decodeJSONOptional`），让「容忍契约」成为函数名的一部分。同时修正 `:695-697` 的注释，别让它继续误导下一个读者。

---

## MA-4 · `DELETE /api/repositories/{repo}` 未命中时返回 `500` 而非 `404`

**现象**

删除一个**不存在**的仓库：

```
DELETE /api/repositories/qa-probe-norepo
→ 500 {"code":"INTERNAL_ERROR","message":"storage: not found"}   (138B)
```

「仓库不存在」是**客户端可预期的正常结果**，应为 `404`；返回 `500` 会让前端把它当成服务端故障（弹「服务器内部错误」、触发重试、污染错误率指标），也让运维误判。

**根因**

- `internal/api/handlers_extra.go:601-621`（`DeleteRepository`）：错误处理把 `storage.ErrNotFound` 和其他错误**一起**映射成 `500`（尤 `:616-618`）。
- 存储层侧语义是清楚的：`internal/storage/filesystem.go:707-727`（`DeleteRepository`）在目录不存在时明确返回 `ErrNotFound`（`:715-718`），只是调用方没有分支处理。
- **同文件里已有正确对照**：`internal/api/handlers_extra.go:650-656`（`DeleteManifestByDigest`）正确地把 `ErrNotFound` → `404`，其余 → `500`。说明这是漏改，而非设计选择。

**证据**

- 源码：`internal/api/handlers_extra.go:601-621`（尤 `:616-618`）、`:650-656`（正确对照）、`internal/storage/filesystem.go:707-727`（尤 `:715-718`）、`internal/storage/storage.go:30`（`ErrNotFound` 定义）。
- 实测：`DELETE /api/repositories/qa-probe-norepo` → `500 INTERNAL_ERROR storage: not found`；另经 MA-1(d)（`DELETE /api/repositories/3proxy%2F3proxy`）二次复现同一 500 分支。仓库确实不存在时 `_catalog` 中 `qa-probe-*` 命中 0。

**修复方向**

按 `:650-656` 的写法补分支：

```go
if errors.Is(err, storage.ErrNotFound) {
    writeError(w, http.StatusNotFound, "NOT_FOUND", "repository not found")
    return
}
```

顺带扫一遍同文件其他删除/查询端点，确认没有同类漏改。

---

## MA-5 · sync 删除端点返回 `204 No Content`，前端 JSON 信封解析失败 → **删除成功却弹「删除失败」（假阴性）**（High）

**现象**

UI 上删除同步任务/同步定时规则，**后端已经删掉了**，前端却弹红色错误：

```
删除失败：服务返回了非 JSON 响应（HTTP 204）: /api/sync/2
删除失败：服务返回了非 JSON 响应（HTTP 204）: /api/sync/2/schedules/2
```

两条 toast 均在本轮 E2E 场景中**实机截获**。随后以服务端为权威核对：任务确已删除（`GET /api/sync/2` → `404`，`GET /api/sync` 里已无该条），规则确已删除（`GET /api/sync/2/schedules` 随任务级联消失）——**即「失败」提示是假阴性**。

反过来若用户因为这条错误提示而**重试删除**，会得到 `404`，体验上等于「删两次都报错」，无法确认自己到底有没有删成功。

**影响面**

- 同步任务列表、同步定时规则两处删除走同一路由家族（`DELETE /api/sync/{id}`、`DELETE /api/sync/{id}/schedules/{sid}`），**两处都命中**；
- 前端失败分支**不刷新列表**（`web/src/pages/sync-page.tsx:401-418`、`:529-538`），于是 UI 上被删的行**仍然留在表格里**，与后端的真实状态分裂——用户看到「删不掉」，实际库里已经没了。这一条比 toast 文案本身危害更大：它是**可见状态与真实状态不一致**。
- CASCADE 语义也被这条缺陷掩蔽：删除任务时 `modal.confirm` 提示「外键 CASCADE」，实测级联清空确实发生了，但用户只看到失败提示。

**根因**

后端与前端对「删除成功」的**响应形态**约定不一致：

1. 后端（sync 两处）`w.WriteHeader(http.StatusNoContent)` —— 返回 **204 空 body**：
   - `internal/api/sync_handlers.go:248`（`DeleteTask`，函数体 `:235-250`）
   - `internal/api/sync_handlers.go:574`（`DeleteSchedule`，函数体 `:552-576`）
2. 前端 `request()` 把**所有**响应都当 JSON 信封解析（`web/src/api.ts:109-179`）：`:140-152` 先 `await response.text()`，对空串执行 `JSON.parse('')` 抛 `SyntaxError`，被兜底成 `INVALID_RESPONSE`，消息模板为 ``服务返回了非 JSON 响应（HTTP ${status}）: ${path}``；`:153-161` 是「非信封」分支，**没有为 204/空 body 预留成功通道**。
3. 调用方：`deleteSyncTask`（`web/src/api.ts:507-508`）、`deleteSyncSchedule`（`web/src/api.ts:555-558`）直接 `await request(...)`，于是 reject 传到了页面的失败分支。

**这是漏改，不是设计选择**：全仓 `StatusNoContent` 只有 4 处，其中 `internal/api/api.go:132` 是 dev CORS 的 `OPTIONS` 预检（合法），`internal/registryd/routes.go:496` 属于 `/v2/*` 协议侧（不经前端 `request()`）；剩下的就是这 2 处 sync 端点。**其余 6 个删除/写端点全部返回 `200` + JSON 信封**（`internal/api/handlers_extra.go:601`、`:625`、`:848`、`:1056`、`:345`，`internal/api/handlers.go:752`），前端对它们工作正常。同一份前端代码，只有 sync 这两个端点炸——对照关系明确。

**证据**

- 源码（后端）：`internal/api/sync_handlers.go:235-250`、`:552-576`；对照 `internal/api/handlers_extra.go:601/625/848/1056/345`、`internal/api/handlers.go:752`（均 `writeJSON(http.StatusOK, …)`）；`internal/api/api.go:132`（CORS 预检）、`internal/registryd/routes.go:496`（协议侧）为合法 204。
- 源码（前端）：`web/src/api.ts:109-179`（尤 `:140-152`）、`:507-508`、`:555-558`；`web/src/pages/sync-page.tsx:401-418`（`handleDelete`，失败分支无 `refresh()`）、`:529-538`（`handleDeleteSchedule`，失败分支无 `refreshSchedules()`）。
- 实测（UI，53 runner，`2026-09-30`）：
  - `go-hub/sync-page-deltask` → `r-20260930234533-6896` passed，取证 DOM 注入 `SPDELTASKMSG:删除失败：服务返回了非 JSON 响应（HTTP 204）: /api/sync/2`
  - `go-hub/sync-page-delrule` → `r-20260930234427-b614` passed，取证 `SPDELRULEMSG:删除失败：服务返回了非 JSON 响应（HTTP 204）: /api/sync/2/schedules/2`；同场景另取证 `SPDELRULESTALE:rows=1`（列表未刷新，被删规则仍在表格里）
- 实测（服务端权威核对，158，`2026-09-30T23:46:06Z`）：`GET /api/sync/2` → **`404`**；`GET /api/sync` 仅剩 `id=1 "Sync 58"`。即后端删除是成功的。

**修复方向**

二选一，**推荐 (a)**（与本仓既有约定一致）：

- **(a) 后端改成 `200` + JSON 信封**：照兄弟端点的写法 `writeJSON(w, http.StatusOK, map[string]any{"deleted": id})`，前端零改动，且响应里能带回被删 id，便于前端乐观更新。同步更新任何文档化的状态码（`204` → `200`）。
- **(b) 前端为 204 开成功通道**：在 `request()` 的 `:140-152` 里对 `status === 204 || text === ''` 提前返回 `undefined`（并把返回类型放宽为 `T | undefined`），后端不动。改动面小，但会让 `request()` 承担更多分支，且其余 6 个端点永远走不到这条路。

无论选哪条，都要**顺手修列表不刷新**：删除成功（或判定为成功）后必须 `refresh()` / `refreshSchedules()`，否则失败分支的 early-return 会继续把陈旧行留在屏幕上。同时给前端 `request()` 补一条针对「2xx + 空 body」的单测。

---

## MA-6 · `GET /api/sync/{id}/runs` 对**不存在的任务**返回 `200` + `[]` 而非 `404`（Low）

**现象**

任务 id **不存在**时，两个兄弟端点给出**互相矛盾**的答案（158 实测，`2026-09-30T23:47:08Z`，一次命令内取全）：

| 请求 | 实测响应 |
| --- | --- |
| `GET /api/sync/2`（已删除的任务） | `404` |
| `GET /api/sync/2/runs` | **`200` + `[]`** |
| `GET /api/sync/2/schedules` | `404` |
| `GET /api/sync/999`（从未存在） | `404` |
| `GET /api/sync/999/runs` | **`200` + `[]`** |
| `GET /api/sync/999/schedules` | `404` |

也就是说「任务是否存在」这个问题，`/runs` 说「存在但没跑过」，`/schedules` 说「不存在」。

**影响面**

低，但会**掩蔽 URL 拼写错误**：调用方（前端轮询、脚本、外部集成）把任务 id 打错时拿不到任何错误信号，只会安静地看到空列表，从而误判为「这个任务从未运行过」。调试成本被推给下一次现场排查。前端当前只对已存在的任务拉 `/runs`，所以**用户侧暂未观测到**。

**根因**

`ListRuns` 漏了「先查任务」的守卫，而**兄弟端点 `ListSchedules` 有**，且代码注释已经把理由写清楚了：

- `internal/api/sync_handlers.go:306-325`（`ListRuns`）：直接 `store.ListRuns(taskID)` 返回，**没有 `GetTask` 前置校验**；
- `internal/api/sync_handlers.go:426-434`（`ListSchedules`）：先 `GetTask`，不存在则 `404`。其注释原文：

  > `// 404 when the task itself doesn't exist (the store would happily return [] otherwise, which masks typos in the URL).`

  —— 痛点描述与 MA-6 现象**逐字对应**，说明作者知情，只是没回头补 `/runs`。典型的漏改。

**证据**

- 源码：`internal/api/sync_handlers.go:306-325`（`ListRuns`，无守卫）对比 `:426-434`（`ListSchedules`，有守卫 + 注释）。
- 实测：见上表 6 行矩阵（`/api/sync/2*` 与 `/api/sync/999*`），单条命令内取全，避免时序干扰。

**修复方向**

把 `ListSchedules` 的守卫原样复制到 `ListRuns` 开头：

```go
if _, err := store.GetTask(taskID); err != nil {
    if errors.Is(err, storage.ErrNotFound) {
        writeError(w, http.StatusNotFound, "NOT_FOUND", "sync task not found")
        return
    }
    writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", err.Error())
    return
}
```

顺带扫一遍 sync 家族其余子路由（`/{id}/run`、`/{id}/test`、`/{id}/logs` 之类），确认没有第三处同类漏改，并把守卫抽成一个 helper（如 `requireTask(w, r, store)`）让漏改无处可藏。