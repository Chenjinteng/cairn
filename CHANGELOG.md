# Changelog

cairn 的所有显著变更记录于此。格式遵循 [Keep a Changelog](https://keepachangelog.com/)。

版本规则见 [AGENTS.md §版本号规则](./AGENTS.md#版本号规则)。

---

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