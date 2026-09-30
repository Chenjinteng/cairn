# 管理 API 缺陷（`/api/*`）

> 被测版本：cairn `0.6.11`（commit `f66bd4d`）｜环境：`registry.local:10001`（容器 `cairn:0.6.11`，`env=prod`）
> 范围：cairn 自有 HTTP 管理 API，编号 `MA-1`、`MA-3`、`MA-4`
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