# Cairn

> 轻量级容器镜像管理平台 —— 单进程单二进制,自带管理控制台,实现 CNCF Distribution(Docker Registry HTTP API V2)。

<p align="left"><img src="./docs/logo.svg" alt="Cairn logo" width="64" /></p>

**[中文](./README_ZH.md)** · [English](./README.md)

[![Docker Hub](https://img.shields.io/badge/Docker%20Hub-chenjinteng%2Fcairn-2496ED?logo=docker&logoColor=white)](https://hub.docker.com/r/chenjinteng/cairn) · `docker pull chenjinteng/cairn`

Cairn 是一个**可独立部署的容器镜像仓库**:完整实现 Docker Registry V2 协议,自带管理控制台。它只有**一个进程、一个二进制、一个 registry** —— 起在哪儿,镜像就存在哪儿;浏览器打开同一个端口,就是管理界面。

> 📘 **想看完整产品介绍?** [README 下方「这是什么」段](#这是什么)给出"这是什么 / 设计目标 / 能力地图 / 单二进制架构 / 快速开始 / 配置 / 边界 / 从这里开始"8 节;运行实例上还自带一份[`/cairn-intro.html`](./web/public/cairn-intro.html)(随二进制分发,UI 上点 footer「产品介绍」可访问)。

## 这是什么

Cairn 用 Go 从零实现 —— 单二进制、自带控制台、零外部依赖(数据库 / 消息队列 / Redis 全不要)。覆盖 docker push / pull / skopeo copy 的全 roundtrip,日常运维走 6 个内置页面(镜像列表 / 拉取 / 热度 / 凭据 / 代理 / 设置)即可。

## 设计目标

四条硬约束:

1. **单进程单二进制**
   运行时不依赖数据库、消息队列、Redis 这类需要运维的外部服务;只依赖 Go 标准库 + chi + `golang.org/x/sync`,最终二进制 ~10 MB。
2. **前后端同源**
   React 前端构建产物经 `//go:embed` 打进二进制;同一个端口同时提供 `/v2/*`、`/api/*` 与页面 —— 没有跨域,也没有第二套部署。
3. **库式后端**
   `cmd/server` 只做装配;能力都住在 `internal/` 各包(registry / pull / stats / credentials / proxies / webhook),行为修改有唯一落点。
4. **可观测**
   `/healthz` 存活、`/readyz` 就绪,日志走结构化 `slog`;compose 与 k8s 需要的探针端点开箱即用。

## 能力地图

| 模块 | 状态 | 实现要点 |
|---|---|---|
| `/v2/*` Docker Registry V2 协议 | ✅ | catalog、tags/list、manifests(4 种 Accept)、blobs 与 uploads(4 MiB 分块 PATCH / PUT);docker push / pull 与 skopeo 直连均已 roundtrip 验证 |
| 本地 FS 存储 | ✅ | registry 内容落文件系统;digest 校验 + 原子写入;容器内固定 `/app/data/registry`,加一条 bind mount 即可让内容独占大盘 |
| 清单浏览与删除 | ✅ | 按仓库 / tag 浏览 digest、架构、层数、体积与构建时间;删除按 digest 执行,执行前先列出同一 digest 的全部 tag 影响面 |
| 拉取队列 | ✅ | 从上游 registry 拉取镜像:FIFO 队列、单并发执行、可协作式取消;进度用 ring buffer 限制内存占用 |
| 凭据库(AES-256-GCM) | ✅ | 凭据加密落盘(密钥来自 `REGISTRY_CREDENTIAL_KEY`);支持 Docker Hub / ghcr.io / quay.io 等的 Bearer 令牌流程(含无 scope token) |
| 代理库 | ✅ | 独立表,代理可达性纯 TCP 探测 + 延迟 |
| 热度库(SQLite) | ✅ | `modernc.org/sqlite` 纯 Go,无 cgo;按镜像与时间窗聚合;数据同样在 `/app/data` 下 |
| Webhook 通知 | ✅ | 事件出站 POST 带 HMAC-SHA256 签名,接收端走白名单校验 |
| 控制台(React + AntD) | ✅ | 镜像列表 / 镜像拉取 / 镜像热度 / 凭据管理 / 代理管理 / 设置 —— 6 个页面覆盖日常运维 |

## 单二进制架构

```
                ┌───────────────────────────────────────────────┐
  docker /      │                  :8787                       │
  skopeo /      │  chi 路由 + slog 结构化日志                    │
  curl / 浏览器 │  /v2/* 数据平面 + /api/* 管理平面 + Web UI    │
                │  (//go:embed 嵌入二进制)                      │
                └───────────────┬───────────────────────────────┘
                                │
                ┌───────────────▼───────────────────────────────┐
                │  internal/                                    │
                │  registry(协议与存储) · pull(拉取队列) ·      │
                │  stats(热度) · credentials · proxies · webhook│
                └───────────────┬───────────────────────────────┘
                                │
                ┌───────────────▼───────────────────────────────┐
                │  /app/data                                    │
                │  credentials.json · proxies.json · cairn.db · │
                │  registry/(repos + blobs + uploads)           │
                └───────────────────────────────────────────────┘
```

容器内监听端口是编译期常量 8787;对外端口由 compose 的 `HOST_PORT` 决定(默认 8787)。

## 快速开始

```bash
# 1. 准备宿主机数据目录(存放全部状态 —— 凭据 / SQLite / registry 内容)
mkdir -p /data/cairn

# 2. 复制 .env.example 并填必填项
cp .env.example .env
# 然后编辑 .env,至少改这两项:
#   REGISTRY_CREDENTIAL_KEY=<openssl rand -hex 32 输出>
#   HOST_PORT=80           # 宿主机对外端口;不设回退 8787

# 3. 启动
docker compose up -d --build

# 4. 验证
curl -s http://127.0.0.1:80/healthz
# 期望:HTTP 200 + "OK"

# 5. 浏览器打开 http://<宿主机>:<HOST_PORT>
```

启动后容器内监听端口固定 8787;`HOST_PORT` 只决定宿主机侧的映射端口。

## 配置在哪配

**基础设施走 `.env`,业务配置走设置页。** 两边不重叠,`docker inspect` 里看不到业务 env(本来就不读)。

| 配置项 | 在哪配 | 说明 |
|---|---|---|
| `REGISTRY_CREDENTIAL_KEY` | `.env` | **必填**。凭据库 AES-256-GCM 主密钥,**丢失则已存凭据永久不可恢复** |
| `HOST_DATA_DIR` | `.env` | 宿主机数据目录,默认 `/data/cairn` |
| `HOST_PORT` | `.env` | 宿主机映射端口,默认 8787 |
| `CAIRN_ENV` | `.env` | `prod`(默认)/ `dev`,只影响日志详细度 |
| `IMAGE` | `.env` | 运行镜像 tag,默认 `cairn:X.Y.Z` |
| `NODE_IMAGE` / `NPM_REGISTRY` / `GOPROXY` / `BUILD_HTTP_PROXY` / `BUILD_HTTPS_PROXY` / `BUILD_NO_PROXY` | `.env` | 只在 `docker compose build` 时生效,运行时不读 |
| 仓库地址 / 代理 / 认证 / 展示名 | 设置页 | 落 SQLite,热生效 |
| 能力开关(`allow.delete` / `allow.pull` / `allow.registry_events`) | 设置页 | 落 SQLite,热生效 |
| 通知 token / 各类保留天数 / 忽略 UA | 设置页 | 落 SQLite,热生效 |

### 改监听端口(HOST_PORT)

Cairn 监听端口 = **宿主机映射端口** + **容器内 cairn 进程监听端口** 两层的组合。

- **宿主机 → 容器映射**:`HOST_PORT`(默认 8787)。这是 docker 编排层的事,cairn 进程管不到。
- **容器内 cairn 进程监听**:`PORT` 环境变量 → `cfg.Port`(默认 8787,`Dockerfile` `EXPOSE 8787`)。boot 期固定,改需要重启进程 + 改 env。

**改端口步骤**:

1. 编辑 `.env`:`HOST_PORT=8888`(改 8787 为你想要的宿主机端口)
2. `docker compose up -d` 重建容器(仅改 HOST_PORT 时 cairn 进程内部监听端口不变,仍是 8787;如果同时改了容器内监听端口需要重新构建镜像)
3. 浏览器访问 `http://<host>:8888`,`docker pull <host>:8888/...`

**为什么不暴露到设置页?** UI 改 `cfg.Port` 只能改容器内监听端口,**没法改 docker 端口映射** —— 浏览器 / docker daemon 还在走 80/8787,改了等于没改。设置页只读显示当前端口,改法在 `.env` + `docker-compose.yml`。

## 边界

以下功能**刻意不做**,而不是"还没做":

- ❌ 登录 / 用户体系 / RBAC
- ❌ 镜像扫描 / CVE 检测
- ❌ 镜像签名 / cosign 集成
- ❌ 多 registry 聚合
- ❌ 配额 / 速率限制
- ❌ Helm chart / OCI artifact 浏览(只管 Docker 镜像)

数据平面(`/v2/*`)的鉴权现状:`/v2/*` 目前匿名可访问,鉴权仍在 TODO 列表里 —— 生产部署请把它放在内网或反向代理之后,不要裸暴露到公网。

## 从这里开始

1. 浏览器打开 `http://<宿主机>:<HOST_PORT>` —— 默认进「镜像列表」
2. 如果镜像库是空的,先去「镜像拉取」配一条上游源(Docker Hub / ghcr.io / quay.io 等),拉一个镜像下来
3. 凭据 / 代理 / 保留天数 / 通知 token 等业务配置在设置页保存即生效,不用改 env

仓库里的 `docs/design/*.html` 是设计与品牌稿、`docs/ROADMAP.md` 是排期号位、`CHANGELOG.md` 是所有版本变更 —— 都在源里,不在运行实例内。

## 项目结构

```
.
├── cmd/server/                 # 主入口(main + 装配)
├── internal/
│   ├── config/                 # env 配置加载
│   ├── registry/               # V2 协议客户端(浏览 / 删除 / 拉取)
│   ├── registryd/              # 内置 registry server(/v2/* 路由 + uploads)
│   ├── pull/                   # 拉取队列
│   ├── credentials/            # AES-256-GCM 凭据库
│   ├── proxies/                # 代理库
│   ├── events/                 # webhook + 热度统计
│   ├── api/                    # HTTP handlers(/api/*)
│   ├── storage/                # 文件系统原子写 + digest 校验
│   ├── db/                     # SQLite 封装(modernc 纯 Go)
│   ├── server/                 # Build(cfg) → *http.Server
│   └── webui/                  # //go:embed 前端 dist
├── web/                        # React + AntD + Vite 前端源码
├── docs/
│   ├── design/                 # 品牌 / UI / 页面原型
│   └── ROADMAP.md              # 排期号位
├── Dockerfile                  # 多阶段构建,scratch 基础
├── docker-compose.yml
├── Makefile                    # 从 0 部署 + 验收流水线
└── .env.example
```

## 开发

```bash
# 拉依赖(首次 / go.mod 改后)
go mod download

# 跑(需要 .env 或环境变量)
go run ./cmd/server

# 编译
go build -o cairn ./cmd/server

# 跑测试
go test ./...

# 容器化
docker build -t cairn:dev .
```

### 网络受限环境的构建(Go proxy)

`go build` / `go mod download` 默认走 `proxy.golang.org`。在受限网络下会 timeout,两种方式覆盖:

```bash
# 1) 本地 go 命令直接覆盖(只影响当前 shell)
GOPROXY=https://goproxy.cn,direct go mod download
GOPROXY=https://goproxy.cn,direct go build -o cairn ./cmd/server

# 2) docker build 时通过 build-arg 覆盖,影响 Dockerfile 里的 go mod download
docker build --build-arg GOPROXY=https://goproxy.cn,direct -t cairn:dev .
# 或:在 .env 里设 GOPROXY=https://goproxy.cn,direct 再 docker compose build --no-cache
```

常用代理:`https://goproxy.cn,direct`(国内七牛)、`https://goproxy.io,direct`(国内官方推荐)、`https://mirrors.aliyun.com/goproxy/,direct`(阿里云)。

## 让 registry 内容独占一块盘(可选)

默认不拆。想让 blobs/manifests 落在大盘上,在 `docker-compose.yml` 的 `volumes:` 加第二条 bind(**不新增变量**):

```yaml
volumes:
  - ${HOST_DATA_DIR:-/data/cairn}:/app/data
  - /data2/cairn-registry:/app/data/registry
```

## 版本管理

版本号 `主.中.小` 三位规则见 [AGENTS.md §版本号规则](./AGENTS.md#版本号规则)。

版本号 5 处同步(漏一处就漂移):

1. `internal/version/version.go` 的 `Version` 常量
2. `docker-compose.yml` 的 `image: ${IMAGE:-cairn:X.Y.Z}`
3. `.env.example` 的 `IMAGE=`
4. `README.md` 里所有 `docker build/tag/push` 示例
5. `CHANGELOG.md` 新增一节

当前版本:`0.7.53`(来自 `internal/version.Version`,运行时日志和 `/api/config` 都暴露)。

## 文档索引

- [`CHANGELOG.md`](./CHANGELOG.md) —— 所有版本的显著变更
- [`AGENTS.md`](./AGENTS.md) —— 项目开发规范(版本规则 / env 约定 / 数据目录 / V2 协议事实)
- [`CONTRIBUTING.md`](./CONTRIBUTING.md) —— 贡献指南(PR 流程 / `make gates` / commit message 规范)
- [`CODE_OF_CONDUCT.md`](./CODE_OF_CONDUCT.md) —— 行为准则(Contributor Covenant 2.1)
- [`docs/ROADMAP.md`](./docs/ROADMAP.md) —— 排期号位
- [`docs/resilience.md`](./docs/resilience.md) —— v0.5.x 中段的并发 / 死锁 / 前端假死问题全貌(0.5.18 韧性轮计划文档)
- [`.github/workflows/ci.yml`](./.github/workflows/ci.yml) —— GitHub Actions:push / PR 跑 `make gates`
- [`.github/pull_request_template.md`](./.github/pull_request_template.md) —— PR 描述模板
- [`.github/ISSUE_TEMPLATE/`](./.github/ISSUE_TEMPLATE/) —— bug 报告 / feature 提案模板
- [`docs/design/cairn-brand.html`](./docs/design/cairn-brand.html) —— 品牌稿(Logo / 调色板)
- [`docs/design/cairn-ui-design.html`](./docs/design/cairn-ui-design.html) —— UI 设计稿(token / 组件 / 状态)
- [`docs/design/demo-A-*.html`](./docs/design/demo-A-overview.html) —— 6 个页面原型
- [`web/public/cairn-intro.html`](./web/public/cairn-intro.html) —— 产品介绍页(随二进制分发,运行实例 `/cairn-intro.html` 可见)

## 许可证

本项目采用 [Apache License 2.0](https://www.apache.org/licenses/LICENSE-2.0) —— 完整条款见 [`LICENSE`](./LICENSE)、第三方依赖归属见 [`NOTICE`](./NOTICE)。

![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)

主要含义:
- ✅ 可商用、可修改、可分发
- ✅ 公司可以把 Cairn 二进制闭源嵌入自家产品
- ✅ 含 explicit patent grant,降低专利诉讼风险
- ❌ 不提供商标授权(「Cairn」名字需单独授权)
- ❌ 不提供质量担保(自行评估)