# cairn

> 用 Go 重写的镜像仓库管理工具（registry-manager 的 Go 版），前后端同源单进程部署。

## 这是什么

一个独立的 CNCF Distribution（Docker Registry HTTP API V2）镜像仓库管理 Web 工具。

**与 registry-manager 的关系**：

- ✅ 行为 / API 兼容（registry-manager 是参考实现）
- ✅ UI 视觉风格一致（前端 copy 自 registry-manager）
- ❌ **不是 fork** —— Go 是从零写的，不绑定上游 Node 实现
- ❌ 不追版本号同步 —— cairn 自己有版本号规则

## 设计目标

1. **单进程单二进制**：编译后 ~10MB 静态二进制，运行时无外部依赖（SQLite 用 modernc 纯 Go 驱动，规划中）
2. **前后端同源**：前端 dist 通过 `//go:embed` 嵌入二进制（v0.2+），部署只发一个文件
3. **库式后端**：HTTP handler 通过 `cmd/server` 装配，业务逻辑在 `internal/` 下，方便后续挂到别人的服务里
4. **可观测性**：自带 `/healthz` `/readyz`

## 当前状态：v0.5.8（2026-09-26）—— **热度开箱即用：自带 registry 自动计入**

✅ 已实现（v0.1 – v0.4）：

**数据平面**（v0.3 新增）

- `/v2/*` OCI Distribution 协议完整路由：catalog / tags/list / manifests / blobs / uploads
- 本地 FS 存储：repos/ + blobs/ + uploads/，digest 校验，原子写
- 支持完整 push→pull roundtrip（docker / skopeo 可直连）

**管理平面**（v0.1 + v0.2）

- chi 路由 + structured logging (slog)
- 清单浏览、删除（走本地 storage）
- 拉取队列（FIFO + 单并发 + cooperative cancel + ring buffer），从外部 registry 拉到本地
- Blob 传输：4 MiB chunked PATCH + PUT commit
- 凭据库（AES-256-GCM）+ 代理库
- SQLite 热度库（modernc.org/sqlite 纯 Go）
- webhook 接收（HMAC-SHA256 签名、manifest 白名单、HEAD/PUT 方法白名单、User-Agent 忽略）
- API 响应包 `{success, code, message, data}` 信封

**前端**

- React + AntD + Vite，从 registry-manager/web 复制（视觉 / 交互 100% 一致）
- 前端 dist 通过 `//go:embed` 嵌入二进制（-tags webui），`/` 直接服务 UI，深路由 fallback index.html（v0.4 新增）

**工程化**

- 单版本号源（`internal/version/version.go` 5 处同步）
- AGENTS.md 开发规范 + CHANGELOG.md（Keep a Changelog）
- Dockerfile（多阶段 scratch 基础，运行镜像 ~15MB）+ docker-compose
- `go test ./...` 全绿

⏳ 后续 TODO：

- 鉴权（basic / Bearer token）—— 当前 `/v2/*` 匿名，靠 reverse proxy 守住

- 多架构 index 拉取（registry-manager 也跳过，cairn 沿用）
- v0.2 / v0.3 单元测试（v0.3 已有集成测试覆盖）

## 版本管理

版本号 `主.中.小` 三位规则见 [AGENTS.md §版本号规则](./AGENTS.md#版本号规则)。

版本号 5 处同步（漏一处就漂移）：

1. `internal/version/version.go` 的 `Version` 常量
2. `docker-compose.yml` 的 `image: ${IMAGE:-cairn:X.Y.Z}`
3. `.env.example` 的 `IMAGE=`
4. `README.md` 里所有 `docker build/tag/push` 示例
5. `CHANGELOG.md` 新增一节

当前版本：`0.5.8`（来自 `internal/version.Version`，运行时日志和 `/api/config` 都暴露）。

## 项目结构

```
.
├── cmd/server/                 # 主入口（main + 装配）
├── internal/
│   ├── config/                 # env 配置加载
│   ├── registry/               # V2 协议客户端（浏览 / 删除 / 拉取）
│   │   ├── client.go             # HTTP 客户端 + basic auth + 代理
│   │   ├── inventory.go          # 浏览：repos / tags / manifest
│   │   ├── delete.go             # 删除：按 digest
│   │   ├── registry.go           # Registry interface + TTL 缓存
│   │   ├── types.go              # Manifest / Layer / Repository / TagInfo
│   │   └── errors.go             # typed V2 errors
│   ├── api/                    # HTTP handlers
│   │   ├── api.go                # chi 路由装配 + middleware
│   │   ├── handlers.go           # /api/* handler 实现
│   │   └── errors.go             # ErrorBody shape（与 registry-manager 对齐）
│   └── server/                 # Build(cfg) → *http.Server
├── go.mod
├── go.sum
├── Dockerfile                  # 多阶段构建，scratch 基础
├── docker-compose.yml
├── .env.example
└── README.md
```

## 开发

```bash
# 拉依赖（首次 / go.mod 改后）
go mod download

# 跑（需要 .env 或环境变量）
go run ./cmd/server

# 编译
go build -o cairn ./cmd/server

# 跑测试
go test ./...

# 容器化
docker build -t cairn:dev .
```

### 网络受限环境的构建（Go proxy）

`go build` / `go mod download` 默认走 `proxy.golang.org`。在受限网络下会 timeout，两种方式覆盖：

```bash
# 1) 本地 go 命令直接覆盖(只影响当前 shell)
GOPROXY=https://goproxy.cn,direct go mod download
GOPROXY=https://goproxy.cn,direct go build -o cairn ./cmd/server

# 2) docker build 时通过 build-arg 覆盖,影响 Dockerfile 里的 go mod download
docker build --build-arg GOPROXY=https://goproxy.cn,direct -t cairn:dev .
# 或:在 .env 里设 GOPROXY=https://goproxy.cn,direct 再 docker compose build --no-cache
```

常用代理：`https://goproxy.cn,direct`（国内七牛）、`https://goproxy.io,direct`（国内官方推荐）、`https://mirrors.aliyun.com/goproxy/,direct`（阿里云）。

### 构建期通用 HTTP 代理（跟 GOPROXY 正交）

如果构建机**直连外网受限**（不只是 Go module，go / curl / git / apt 都不通），但内网有可用的 HTTP 代理（如 `proxy.example.com:7890`），可以给 web-builder / builder stage 配 HTTP 代理。`Dockerfile` 透传 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` build-arg：

```bash
# docker build 直接覆盖
docker build \
  --build-arg HTTP_PROXY=http://proxy.example.com:7890 \
  --build-arg HTTPS_PROXY=http://proxy.example.com:7890 \
  --build-arg NO_PROXY=localhost,127.0.0.1,.local \
  -t cairn:dev .

# docker compose:在 .env 里设 BUILD_HTTP_PROXY 等(避免跟运行时 REGISTRY_PROXY 混淆)
cat >> .env <<'EOF'
BUILD_HTTP_PROXY=http://proxy.example.com:7890
BUILD_HTTPS_PROXY=http://proxy.example.com:7890
BUILD_NO_PROXY=localhost,127.0.0.1,.local
# 前端依赖走国内/内网 npm 镜像(可选)
NPM_REGISTRY=https://registry.npmmirror.com
EOF
docker compose build --no-cache
```

**跟 GOPROXY 的关系**：GOPROXY 控制 Go module 协议本身（决定去哪个 module proxy 拉），HTTP_PROXY 控制底层 HTTP 客户端出口。两者独立可叠加 — 内网代理环境下一般两个都要配（Go module 协议层 + 实际网络层）。

**留空 = 不设代理**：默认行为跟之前完全一致，普通 build 不需要任何额外配置。

## 部署

```bash
cp .env.example .env       # 必填: REGISTRY_CREDENTIAL_KEY(随机 32 字节)
                           # 受限网络:把 GOPROXY 改成 https://goproxy.cn,direct
                           # 内网代理:把 BUILD_HTTP_PROXY/HTTPS_PROXY 设成 http://proxy.example.com:7890
                           # 前端依赖镜像:NPM_REGISTRY=https://registry.npmmirror.com(受限网络)
                           # 访问端口:改 HOST_PORT(示例 80 = http://<宿主机>:80;不设回退 8787)
                           # 自定义 registry 内容路径:改 REGISTRY_STORAGE_DIR
docker compose up -d --build
```

启动后浏览器访问 `http://<宿主机>:<HOST_PORT>`（容器内固定监听 8787，`HOST_PORT` 只决定宿主机侧的映射端口）。

`.env` 字段含义与 registry-manager 保持一致，方便复用。

## 与 registry-manager 的字段对照

| registry-manager | cairn | 说明 |
|---|---|---|
| `REGISTRY_URL` | ✅ | 要管理的 OCI registry 地址 |
| `REGISTRY_USERNAME/PASSWORD` | ✅ | 本 registry 自身 basic auth |
| `REGISTRY_NAME` | ✅ | 展示名称 |
| `REGISTRY_CACHE_TTL_SECONDS` | ✅ | 清单缓存 TTL |
| `REGISTRY_ALLOW_DELETE/PULL` | ✅ | 能力开关 |
| `REGISTRY_NOTIFY_TOKEN` | ✅（v0.3） | webhook 共享密钥 |
| `REGISTRY_CREDENTIAL_KEY` | ✅（v0.2） | 凭据库加密密钥 |
| `REGISTRY_CREDENTIALS_DIR` | ✅ | 应用数据目录（凭据 / SQLite） |
| `REGISTRY_STORAGE_DIR` | ✅（v0.5） | registry 内容目录（blobs / manifests） |
| `REGISTRY_PROXY` | ✅ | 访问本 registry 的 HTTP 代理 |

---

详细的模块设计见 `docs/design.md`（待补）。