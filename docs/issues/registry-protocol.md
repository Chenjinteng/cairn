# 镜像协议缺陷（`/v2/*`）

> 被测版本：cairn `0.6.11`（commit `f66bd4d`）｜环境：`registry.local:10001`（容器 `cairn:0.6.11`，`env=prod`）
> 范围：CNCF Distribution（Docker Registry HTTP API V2）协议行为，编号 `REG-1` … `REG-6`
> 每条包含：现象 / 根因 / 证据（源码 file:line + 实机实测）/ 修复方向
> 修复状态：**全部未修复**。

## 前置：两条路由机制（理解 MA-1 与 REG 系列的前提）

registry 协议路径里的仓库名**天然含 `/`**（如 `3proxy/3proxy`），而 chi 的具名参数 `{repo}` 跨不过 `/`。cairn 在协议侧用了一个**自切分 dispatcher** 绕开这个限制：

- `internal/registryd/routes.go:115-123` —— 注释原文解释「chi's named params cannot span `"/"`」，因此把 `/v2/*` 整体交给 `r.HandleFunc("/*", h.dispatchRepoRoute)`；
- dispatcher 自己按 `/` 切分路径、推断出 repo / 动作 / 引用，再用 `injectRouteParams` 把结果塞回 chi 的 route context（`routes.go:696-697` 是一例），于是下层 handler 里照常 `chi.URLParam(r, "repo")` 就能拿到**完整**的 `3proxy/3proxy`。

这套机制在协议侧工作正常（实测 `GET /v2/3proxy/3proxy/manifests/1.0.0` → `200`）。**管理 API 没有采用同样的办法**（直接用了 `{repo}` 单段参数），这就是 MA-1 的机制根源，详见 [management-api.md](./management-api.md)。

---

## REG-1 · `PUT` manifest 不校验 payload，任意 JSON 也 `201` 并持久化

**现象**

向一个不存在任何内容的仓库 `PUT /v2/<repo>/manifests/<tag>`，body 是 47 字节的**垃圾 JSON**（无 `schemaVersion`、无 `mediaType`、无 `layers`），服务端：

1. 返回 `201 Created` + `Docker-Content-Digest` + `Location`；
2. 随后 `GET /v2/<repo>/manifests/<digest>` 返回 `200`，**原样回显垃圾内容**，`Content-Type` 被标成 `application/vnd.oci.image.manifest.v1+json`；
3. 该 tag 出现在 `tags/list`，该仓库出现在 `_catalog`。

即：任意字节都能把一个仓库「造」出来并挂上 tag；docker 客户端拉取时才会在解包阶段失败（`manifest invalid`），错误被推迟到客户端侧。

**根因**

- `internal/registryd/routes.go:366-394`（`manifestPut`）只做了「body 非空」检查（`:369-372`），**没有任何 schema / mediaType 校验**；`:377` 用 32MB `LimitReader` 限长后直接调 `PutManifest`；`:383-385` 把任何错误都映射成 `400 MANIFEST_INVALID`，成功路径直接 `201`（`:390-393`）。
- `internal/storage/filesystem.go:216-250`（`PutManifest`）只在 `ref` 形如 digest 时做比对（`:220-222`），否则 `atomicWrite` 盲写内容（`:232`），再写 mediaType（`:235-237`）并建 tag（`:239-247`）。
- 结果是「校验责任在谁」没有落点：路由层认为存储层会校验，存储层认为路由层校验过了。

**证据**

- 源码：`internal/registryd/routes.go:366-394`（尤 `:382`、`:383-385`）、`internal/storage/filesystem.go:216-250`（尤 `:220-222`、`:232`、`:239-247`）。
- 实测（158，`2026-09-30T16:39:15Z`）：`PUT /v2/qa-probe-x/manifests/garbage-tag`（47B 垃圾）→ `201 Created`、`Docker-Content-Digest: sha256:1f2f7ebf…2859`、`Location: http://127.0.0.1:10001/v2/qa-probe-x/manifests/sha256:1f2f7ebf…2859`；`GET` 同 digest → `200` / 47B / `Content-Type: application/vnd.oci.image.manifest.v1+json`；`tags/list` → `{"name":"qa-probe-x","tags":["garbage-tag"]}`；`_catalog` 含 `qa-probe-x`。

**修复方向**

在 `manifestPut` 里补一层最小校验（对齐 Distribution）：body 必须是合法 JSON、`schemaVersion == 2`、`mediaType` 在允许集合内、`config`/`layers` 结构合法；不合法 → `400 MANIFEST_INVALID`。校验通过后再交给存储层。若担心兼容自建/实验性 manifest，可退一步：至少要求 body 是 JSON 且带 `schemaVersion`/`mediaType` 两个字段。

---

## REG-2 · 未知仓库的 `tags/list` 返回 `200` + 空数组

**现象**

对一个**从未存在过**的仓库请求 `GET /v2/<repo>/tags/list`：

```
200 {"name":"qa-probe-norepo","tags":[]}
```

同一仓库的 `GET /v2/<repo>/manifests/latest` 则是 `404 MANIFEST_UNKNOWN`。

即「仓库不存在」与「仓库存在但没有任何 tag」两种语义被合并成同一种响应。调用方（含 cairn 自己的清单刷新逻辑）无法区分，容易把拼错的仓库名当成「空仓库」而保留在列表里。

**根因**

- `internal/storage/filesystem.go:135-153`（`Tags`）把 `os.ErrNotExist` 吞成 `nil, nil`（`:137-141`）——「目录不存在」和「目录存在但为空」在这里合流。
- `internal/registryd/routes.go:266-281`（`tagsList`）拿到 nil 后渲染成 `[]`（`:277-279`），并固定 `200`。

**证据**

- 源码：`internal/registryd/routes.go:266-281`、`internal/storage/filesystem.go:135-153`（尤 `:137-141`）。
- 实测：`GET /v2/qa-probe-norepo/tags/list` → `200 {"name":"qa-probe-norepo","tags":[]}`；对照 `GET /v2/qa-probe-norepo/manifests/latest` → `404 MANIFEST_UNKNOWN`。

**修复方向**

在 `Tags`（或路由层）区分两种状态：仓库目录不存在 → 返回 `storage.ErrNotFound`，路由层映射为 `404` + `{"errors":[{"code":"NAME_UNKNOWN",…}]}`；目录存在但无 tag → 维持现状 `200 {"tags":[]}`。注意 `_catalog` 里 `tags: null` 的空仓库是**另一条**既有约定（AGENTS.md 明确要求容忍且不得剔除），本次修复不要波及它。

---

## REG-3 · `/v2/` 缺 `Docker-Distribution-Api-Version` 响应头，且 `GET` / `HEAD` 不一致

**现象**

```
GET  /v2/  → 200，响应头只有 Content-Type: application/json，body {}
HEAD /v2/  → 404
```

Docker 客户端与各类 registry 探测脚本普遍依赖 `/v2/` 的 `200` 与 `Docker-Distribution-Api-Version: registry/2.0` 头来判断「这是一个 V2 registry」（`docker login` 的 ping 阶段、Helm/skopeo 的探测、监控探针都会用到）。缺头不一定阻断 docker pull，但会让探测方降级到猜测逻辑；`HEAD` 直接 `404` 会让只发 HEAD 的健康检查误判服务不可用。

**根因**

- `internal/registryd/routes.go:130-137`（`apiVersion` handler）只设 `Content-Type: application/json` 并写 `{}`，没有设版本头。
- `internal/registryd/routes.go:111` 只注册了 `r.Get("/", h.apiVersion)`，没有注册 `HEAD`（chi 不做 GET→HEAD 隐式映射），于是 `HEAD /v2/` 落到默认 `404`。

**证据**

- 源码：`internal/registryd/routes.go:130-137`、`:111`。
- 实测：`GET /v2/` → `200`，头集合仅 `Content-Type: application/json`；`HEAD /v2/` → `404`（`Content-Length: 74`，即默认错误页）。

**修复方向**

`apiVersion` 里补 `Docker-Distribution-Api-Version: registry/2.0`（一行）；把路由注册改为 `r.Handle("/", h.apiVersion)` 或额外注册 `r.Head("/", h.apiVersion)`，让 `HEAD` 也返回 `200`。

---

## REG-4 · `DELETE` manifest 未命中时 `404` body 为 0 字节；其余错误 `500` 误用 `UNSUPPORTED`

**现象**

| 请求 | 实测 | 评价 |
| --- | --- | --- |
| `DELETE /v2/<repo>/manifests/<tag>` | `400 DIGEST_INVALID`（93B JSON） | ✅ 符合协议（只能按 digest 删） |
| `DELETE /v2/<repo>/manifests/<不存在的 digest>` | `404`，**body 0 字节、无 `Content-Type`** | ❌ 缺错误体 |
| 其他错误（如内部异常） | `500`，错误码写成 `UNSUPPORTED` | ❌ 码不对 |

`404` 空 body 的问题在于：docker CLI 与各家 SDK 都按 `{"errors":[{code,…}]}` 解析失败原因，空 body 会让客户端报出「unexpected end of JSON input」这类无信息量的错误，运维排查时看不到 `MANIFEST_UNKNOWN`。

**根因**

- `internal/registryd/routes.go:396-412`（`manifestDelete`）：`:399-401` tag 形式 → `400 DIGEST_INVALID`（正确）；`:404-406` 命中 `ErrNotFound` 后**直接 `return`，一个字节都没写**，靠 net/http 补上 `404` 状态码；`:408` 其余错误 → `500` 但复用了 `UNSUPPORTED` 这个码。
- 同类误用还有一处：`routes.go:274`（`tagsList`）在内部错误分支也用了 `UNSUPPORTED`。

**证据**

- 源码：`internal/registryd/routes.go:396-412`（尤 `:404-406`、`:408`）、`:274`；`internal/storage/storage.go:30`（`ErrNotFound` 定义）。
- 实测：仓库存在（`3proxy/3proxy`）与不存在（`qa-probe-norepo`）两种情况下，`DELETE .../manifests/sha256:0000…0000` **均**返回 `404`、`body` 0 字节、无 `Content-Type`。

**修复方向**

`:404-406` 分支写出标准错误体 `{"errors":[{"code":"MANIFEST_UNKNOWN","message":"manifest unknown"}]}` 再返回；`:408`（及 `:274`）把错误码从 `UNSUPPORTED` 换成 `UNKNOWN`（Distribution 的实际码位）。可顺带把「写错误体」抽成一个 helper，避免以后再漏。

---

## REG-5 · blob `GET` 无显式 `Content-Type`，且不支持 `Range`

**现象**

- `GET /v2/<repo>/blobs/<digest>` → `200`，头里有 `Content-Length` 与 `Docker-Content-Digest`，但**没有 `Content-Type`**；实际发出的 `Content-Type: text/plain; charset=utf-8` 是 Go `net/http` 的内容嗅探结果（取决于层文件的头几个字节）。
- 带 `Range: bytes=0-9` 请求同一个 blob → 仍返回 `200` + **完整 1725 字节**，无 `Content-Range`、无 `206`——`Range` 被完全忽略。
- blob `HEAD` → `200` + `Content-Length`（无 `Content-Type`）。

实际影响有限：docker 拉层时按 digest 校验，不依赖 `Content-Type`；`Range` 在 OCI/Distribution 语义里属于可选能力（MAY）。但两点都偏离主流 registry 的返回值（Distribution 返回 `application/octet-stream` 并声明 `Accept-Ranges`），对自研客户端/镜像校验工具有潜在误导。

**根因**

- `internal/registryd/routes.go:416-437`（`blobGet`）只设了 `Content-Length` 与 `Docker-Content-Digest`（`:433-434`），既不设 `Content-Type`，也不读 `Range`；`:439-441`（`blobHead`）同样。

**证据**

- 源码：`internal/registryd/routes.go:416-437`（尤 `:433-436`）、`:439-441`。
- 实测：`GET` blob → `200 Content-Length: 1725` + `Docker-Content-Digest`，`Content-Type` 为嗅探值 `text/plain; charset=utf-8`；`Range: bytes=0-9` → `200` + 全文 1725 字节；`HEAD` → `200 Content-Length: 1725`。

**修复方向**

最省事且不引入行为风险：显式 `Content-Type: application/octet-stream`，并显式声明 `Accept-Ranges: none`（把「不支持 Range」变成契约而非沉默）。若要做 Range，则需在 GET 分支解析 `Range` 并返回 `206` + `Content-Range`，注意与 `Docker-Content-Digest` 语义一致（digest 仍指整个 blob）。

---

## REG-6 · 无法取消 upload session（`DELETE` → `405`），遗留孤儿目录只能人工清理

**现象**

```
POST /v2/<repo>/blobs/uploads/   → 202 + Docker-Upload-Uuid: <uuid> + Location + Range: 0--1
DELETE <upload Location>          → 405, Allow: GET, PATCH, PUT
  body: {"errors":[{"code":"UNSUPPORTED","message":"method not allowed"}]}
```

服务端**没有任何接口可以取消一个已开启的 upload session**。docker 客户端在 push 中途失败/取消时会发 `DELETE` 取消上传，此时只会拿到 `405`；服务端侧则会永久留下一个 session 目录。

**实测遗留物**：`/data/cairn/registry/uploads/qa-probe-up/83c4733be7aaeeaf2a80df3d72f8c111/`，内含唯一文件 `startedat`（20 字节）。该目录**无法通过任何 API 回收**（对应的 `DELETE /api/repositories/qa-probe-up` 也返回 `500`，见 [MA-4](./management-api.md)），本轮由测试人员手动 `rm -rf` 清除。长期运行下这些孤儿目录会随失败的 push 缓慢累积。

**根因**

- `internal/registryd/routes.go:694-708`（upload 分发）只分发 `GET` / `PATCH` / `PUT`（`:698-704`），`default` 分支写死 `writeV2MethodNotAllowed(GET, PATCH, PUT)`（`:706`）——`DELETE` 从一开始就不在支持集合里。

**证据**

- 源码：`internal/registryd/routes.go:694-708`（尤 `:706`）。
- 实测：`POST .../blobs/uploads/` → `202` + uuid `83c4733be7aaeeaf2a80df3d72f8c111`；`DELETE` 该 Location → `405` + `Allow: GET, PATCH, PUT` + 67B 错误体；宿主机实查 `uploads/qa-probe-up/83c4733be7aaeeaf2a80df3d72f8c111/startedat` 存在（20B），`rm -rf` 后确认 `GONE`，`uploads/` 目录数回到 33。

**修复方向**

补 `DELETE` 分支：删除对应 session 目录并返回 `204`（Distribution 的语义），同时把 `Allow` 头清单加上 `DELETE`。配套考虑：给 upload session 加 TTL 清理（启动时或定时扫描 `startedat`/mtime 超期的目录），否则即便有了取消接口，客户端直接断连的场景仍会留孤儿。