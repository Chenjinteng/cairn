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

## 当前状态：v0.1（2026-09-25）

✅ 已实现：

- chi 路由 + structured logging (slog)
- V2 协议客户端（basic auth + 代理支持）
- 清单浏览：列出仓库、列出 tag、获取 manifest
- 删除：按 digest 删除 manifest，返回受影响的 tag
- 配置探测、强制刷新
- 内存缓存（TTL 可配）
- 在制品：Dockerfile（多阶段，scratch 基础，运行镜像 ~15MB）+ docker-compose
- 14 个单元 / 集成测试，全绿

⏳ 待办：

- v0.2：拉取队列（FIFO + 流式 PATCH）+ 凭据库 + 代理库
- v0.3：webhook 接收 + SQLite 热度聚合 + 忽略规则
- 前端：copy 自 registry-manager/web，改 vite.config.ts 代理 `/api` 到 :8787

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

## 部署

```bash
cp .env.example .env       # 填好 REGISTRY_URL + REGISTRY_CREDENTIAL_KEY
docker compose up -d --build
```

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
| `REGISTRY_CREDENTIALS_DIR` | ✅ | 数据目录 |
| `REGISTRY_PROXY` | ✅ | 访问本 registry 的 HTTP 代理 |

---

详细的模块设计见 `docs/design.md`（待补）。