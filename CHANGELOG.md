# Changelog

cairn 的所有显著变更记录于此。格式遵循 [Keep a Changelog](https://keepachangelog.com/)。

版本规则见 [AGENTS.md §版本号规则](./AGENTS.md#版本号规则)。

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