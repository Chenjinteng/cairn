# AGENTS.md

本文件只记录本仓库特有、且会改变实现方式的约束。通用编码风格不在此重复。

## 这是什么

**cairn** 是一个**独立**的 CNCF Distribution（Docker Registry HTTP API V2）镜像仓库管理 Web 工具，**用 Go 从零写的**，**不 fork** [registry-manager](http://github.com/Chenjinteng/registry-manager.git)。

行为 / API 与 registry-manager 对齐：

- 浏览镜像、查看每个 tag 的 digest/架构/层数/体积/构建时间、复制 `docker pull`、按 digest 删除 manifest。
- registry-manager 是**参考实现**（提供协议事实、UI 来源），cairn 是**独立仓库**。

## 项目约定

### 单进程单二进制

运行时只依赖 Go 标准库 + chi + golang.org/x/sync。最终二进制 ~10MB，scratch 基础镜像下运行镜像 ~15MB。

**不允许引入**：

- ORM（SQLite 操作集中在 `internal/db/`，直接拼 SQL — 待 v0.3 落地后追加）
- 任何需要运维的外部服务（数据库、消息队列、Redis 等）
- 任何 Node / npm 运行时依赖

### 没有登录，没有多实例

registry-manager 明确"一次管理一个 registry"，cairn 沿用。**单进程、单二进制、单 registry**。

### 数据目录约定

**容器内路径写死为 Go 常量**（`internal/config/config.go` 的 `DataDirPath` / `StorageDirPath`），**不提供 env**。容器里的文件放哪儿是实现细节；要换位置改 bind mount 的宿主机侧，不动容器内路径。

`DataDirPath`（`/app/data`）下放：

- 凭据库（v0.2 AES-256-GCM 加密 JSON）
- 代理库（v0.2 明文 JSON）
- SQLite 热度库（v0.3）
- registry 内容（`StorageDirPath` = `/app/data/registry`）—— blobs / manifests / upload sessions

**容器化必须挂卷**（`docker-compose.yml` 里 bind mount `${HOST_DATA_DIR:-/data/cairn}:/app/data`），否则容器重建数据全丢（凭据永久不可恢复）。

**只暴露宿主机侧变量**：`HOST_DATA_DIR`（默认 `/data/cairn`）。不给 registry 内容单独开变量 —— 要让 blobs 独占大盘就加第二条 bind mount（见 README「让 registry 内容独占一块盘」）。

## 版本号规则

形如 `主.中.小` 三位，按下面的规则维护。本规则参考 [registry-manager/AGENTS.md#版本号规则](http://github.com/Chenjinteng/registry-manager)，结合 Go 生态做小幅适配。

| 位 | 何时 +1 | 谁来定 |
| --- | --- | --- |
| **主**（第 1 位） | 有重大/破坏性变化时（API 路径大改、移除功能、数据格式不兼容） | **必须由人明确指定**；AI 不得自行进位 |
| **中**（第 2 位） | 每新增一个**功能或模块** | 按改动性质自动判断 |
| **小**（第 3 位） | **缺陷修复**与**现有功能优化** | 按改动性质自动判断 |

判定要点：

- 新增一整个模块（例如拉取队列、凭据库、代理库、热度统计）→ 中版本 +1
- 新增一项用户可感知的能力（例如支持新的 registry 操作）→ 中版本 +1
- 只是让既有功能更好用/更准确（例如更好的错误信息、并发优化）→ 小版本 +1
- 一轮里既有新功能又有修复 → 取**最高**的那一档（即中版本 +1），具体条目写进 `CHANGELOG.md` 即可
- 纯文档、注释、脱敏等不影响行为的改动 → 通常随该轮一起发布，不单独进位

### 一次改动要同时更新这几处

漏一处就会出现版本漂移（运行中的 binary 跟 image tag 不一致、UI 显示旧版本、CHANGELOG 没有条目）：

1. `internal/version/version.go` 的 `Version` 常量
2. `docker-compose.yml` 的 `image: ${IMAGE:-cairn:X.Y.Z}`
3. `.env.example` 的 `IMAGE=`
4. `README.md` 里所有 `docker build/tag/push` 示例（含"镜像推到内网"、"部署"等段落里的 tag 引用）
5. `CHANGELOG.md` 新增一节

`CHANGELOG.md` 按 [Keep a Changelog](https://keepachangelog.com/) 的分组写（新增 / 变更 / 修复 / 文档），**新版本写在最上面**，条目要写"改了什么、为什么、表现是什么"，不要只写"修复 bug"。

### Go 工具链版本

`Dockerfile` 里的 `GO_IMAGE` 和 `go.mod` 的 `go` 指令必须对齐（构建镜像和最终依赖的 Go 标准库版本要一致）。**`go mod tidy` 升级 `go` 指令时同时更新 Dockerfile 的 `ARG GO_IMAGE`**。

---

## V2 协议事实（跟 registry-manager 保持一致）

以下都是实测踩过的坑，**不要在代码里改"修正"**：

**删除**

- 只能按 digest 删。`DELETE /v2/<name>/manifests/<tag>` → `400 DIGEST_INVALID`。
- 删不存在的 digest → `404 MANIFEST_UNKNOWN`。
- 删除 manifest 只是解除引用，**磁盘空间要运行 `registry garbage-collect` 才回收**。
- 一个 digest 可能被同仓库多个 tag 指向，删除前必须列出影响面（已通过 `tagsForDigest` 实现）。

**清单浏览**

- `tags/list` 的分页**版本相关**：v2.8.3 上不生效（传 `n` 也只返回全量）、v3.1.0+ 生效。调用方**刻意不传 `n`**，行为统一。
- `_catalog` 的分页在两个版本都生效（`?n=&last=`）；代码按返回条数判断是否继续。
- `_catalog` 里可能出现 `tags: null` 的空仓库；必须容忍，且**不要从列表里剔除**（避免"删除后消失、重新扫描又回来"的矛盾）。
- 拿 manifest 必须带 `Accept`，否则 registry 回 **404**（不是 400 也不是 406）。`client.go` 的 `manifestAccept` 覆盖 4 种类型，所有 manifest 请求都必须带上。

**认证**

- Docker Hub / ghcr.io / quay.io 等走 **Bearer 令牌**（v0.2 落地）。
- `/v2/` 的挑战不带 scope；必须支持申请**无 scope 的 token**。

## 注册表协议外的事

cairn 刻意**不做**：

- ❌ 登录 / 用户体系 / RBAC
- ❌ 镜像扫描 / CVE 检测
- ❌ 镜像签名 / cosign 集成
- ❌ 多 registry 聚合
- ❌ 配额 / 速率限制
- ❌ Helm chart / OCI artifact 浏览（只管 Docker 镜像）

要加先问。


## v0.5.9 起:env 只剩基础设施

业务配置(仓库地址 / 代理 / 认证 / 各开关 / 保留天数等)全部走 UI → SQLite settings 表。`.env` 里的 `REGISTRY_URL` / `REGISTRY_PROXY` / `REGISTRY_USERNAME` / `REGISTRY_PASSWORD` / `REGISTRY_NAME` / `REGISTRY_NOTIFY_TOKEN` / `REGISTRY_ALLOW_*` / `REGISTRY_PULL_PLATFORMS` / `REGISTRY_PULL_HISTORY_RETENTION_DAYS` / `REGISTRY_STATS_RETENTION_DAYS` / `REGISTRY_STATS_IGNORE_USERAGENTS` 等设置后**不会再被读**(代码里 `os.Getenv("REGISTRY_*")` 全部删除)。设了等于没设。

**基础设施 env 只有这 3 个**(路径类的一律不进来,容器内路径见上「数据目录约定」):

| env | 用途 |
| --- | --- |
| `PORT` | 容器内监听端口(默认 8787);对外端口走 compose 的 `HOST_PORT` |
| `REGISTRY_CREDENTIAL_KEY` | 凭据库 AES-256-GCM 密钥 |
| `GO_HUB_ENV` | `dev` / `prod` |

要挪数据只改宿主机侧的 `HOST_DATA_DIR`(bind mount 左侧);`/app/data` 与 `/app/data/registry` 是编译期常量。

**新增 env 的门槛**:以后任何 PR 想新加业务 env,需要在 PR description 里说明:
1. 为什么不能走 UI?(例如 boot 期读取、容器编排约束等)
2. 如果是 boot 期读取,为什么改 UI 不够?(必须重启才生效的 vs. 不重启可以热生效的)

走不通这两个问题的不接受。
