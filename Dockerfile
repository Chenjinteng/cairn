# 多阶段构建：
#   web-builder  构建 web/ 前端（pnpm build，产物落 /internal/webui/dist）
#   builder      拷贝前端产物后以 -tags webui 编译 Go 二进制（前端 //go:embed 进二进制）
#   runtime      最终镜像，scratch + 二进制
#
# BuildKit:`--mount=type=cache` 等特性由 docker engine 内嵌 BuildKit 后端支持,
#   不需要 `#syntax=` directive(那个会让 BuildKit 去 docker.io 拉 frontend 镜像,
#   在公司受限网络下会卡)。Makefile 的 build / rebuild 显式 DOCKER_BUILDKIT=1 兜底,
#   确保走 BuildKit 而非 legacy builder。若要 cross-build / registry cache 共享等
#   高级特性,装 docker buildx CLI(`docker-buildx-plugin` 包或手动放二进制到
#   `~/.docker/cli-plugins/docker-buildx`)。
# 运行镜像只 ~15MB（10MB 二进制 + 5MB ca-certs + /etc/passwd），
# 比 registry-manager 的 Node + antd 几十MB 还要小一个数量级。
#
# 基础镜像可覆盖：构建机拉不到 Docker Hub 时换成内网镜像。
#   docker build --build-arg GO_IMAGE=proxy.example.com:10001/golang:1.26-alpine \
#                --build-arg NODE_IMAGE=proxy.example.com:10001/node:22-alpine .

# Go 模块代理：构建机访问 proxy.golang.org 受限/慢时改成国内镜像。
#   docker build --build-arg GOPROXY=https://goproxy.cn,direct .
#   docker build --build-arg GOPROXY=https://goproxy.io,direct .
# 默认仍是 proxy.golang.org,direct(从 Go 1.21 起等价于 GOPROXY=proxy.golang.org,direct)。
#
# npm registry 镜像：前端依赖下载受限时换成内网/国内镜像（corepack 拉 pnpm 也走它）。
#   docker build --build-arg NPM_REGISTRY=https://registry.npmmirror.com .
#
# 通用 HTTP/HTTPS 代理：跟 GOPROXY / NPM_REGISTRY 正交,影响 web-builder / builder
# 两个构建 stage 内所有网络出口(go / curl / git / apt / pnpm 等)。默认空 = 不用代理。
#   docker build --build-arg HTTP_PROXY=http://proxy.example.com:7890 \
#                --build-arg HTTPS_PROXY=http://proxy.example.com:7890 \
#                --build-arg NO_PROXY=localhost,127.0.0.1,.local .
ARG GO_IMAGE=golang:1.26-alpine
ARG NODE_IMAGE=node:22-alpine
ARG GOPROXY=https://proxy.golang.org,direct
ARG NPM_REGISTRY=
ARG HTTP_PROXY=
ARG HTTPS_PROXY=
ARG NO_PROXY=

# ---------------------------------------------------------------------------
# 1) 前端构建（vite outDir 指向 ../internal/webui/dist，容器内解析为 /internal/webui/dist）
# ---------------------------------------------------------------------------
FROM ${NODE_IMAGE} AS web-builder

# 在 stage 里再 ARG 一次,确保 ENV 能引用
ARG NPM_REGISTRY
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ENV HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=${NO_PROXY}

WORKDIR /web

# 依赖清单先 COPY：package.json / lockfile 不变时这层缓存命中，pnpm install 从秒级起步。
# package.json 的 packageManager 字段钉死 pnpm 版本，corepack 据此激活。
#
# BuildKit `--mount=type=cache` 把 pnpm store + corepack 缓存挂进 builder stage,
# 跨 build 复用;`--no-cache` 不传时仍生效。注意 sharing=locked 避免并发 build 互踩。
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN --mount=type=cache,target=/root/.local/share/pnpm/store,sharing=locked \
    --mount=type=cache,target=/root/.cache/node/corepack,sharing=locked \
    --mount=type=cache,target=/root/.npm,sharing=locked \
    if [ -n "$NPM_REGISTRY" ]; then \
      export COREPACK_NPM_REGISTRY="$NPM_REGISTRY"; \
      corepack enable; \
      npm config set registry "$NPM_REGISTRY"; \
    else \
      corepack enable; \
    fi \
 && pnpm install --frozen-lockfile

# 源码全量 COPY 后再 build（dist 落 /internal/webui/dist）
COPY web/ ./
RUN pnpm build

# ---------------------------------------------------------------------------
# 2) 编译二进制（-tags webui：//go:embed 前端 dist）
# ---------------------------------------------------------------------------
FROM ${GO_IMAGE} AS builder

# 在 builder stage 里再 ARG 一次,确保 ENV 能引用(全局 ARG 在 stage 内对 ENV 也可见,
# 但显式声明更直观,也方便以后想关掉全局只在这一阶段覆盖)。
ARG GOPROXY
ARG HTTP_PROXY
ARG HTTPS_PROXY
ARG NO_PROXY
ENV GOPROXY=${GOPROXY} \
    HTTP_PROXY=${HTTP_PROXY} \
    HTTPS_PROXY=${HTTPS_PROXY} \
    NO_PROXY=${NO_PROXY}

WORKDIR /src

# 依赖先 COPY：源码不变时这层缓存命中，构建从秒级起步。
# BuildKit `--mount=type=cache` 把 Go module cache + build cache 挂进 builder stage,
# 跨 build 复用;`--no-cache` 不传时仍生效。
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    --mount=type=cache,target=/root/.cache,sharing=locked \
    go mod download

COPY . .

# 前端构建产物：internal/webui 的 //go:embed 需要 dist 存在（-tags webui）
COPY --from=web-builder /internal/webui/dist ./internal/webui/dist

# CGO=0：编译成纯静态二进制，scratch 也能跑
# -trimpath：去掉本地路径信息，二进制可重现
# -ldflags="-s -w"：去符号表，缩 ~30%
#
# v0.6.25: 加 cache mounts —— 0.6.24 之前只有 `go mod download` 那一层加了
# /go/pkg/mod 的 cache mount,这一层 `go build` 没加,导致 compile 阶段 Go
# 重新解析依赖,看到 `# go: downloading xxx` 一堆(module cache 实际是空的,
# 因为 cache mount 只在那一层 RUN 里 mount 进来,这一层 RUN 看到的是干净的
# /go/pkg/mod → 必须从 proxy 重新拉)。同样的目录 /go/pkg/mod 在两个 RUN
# 上都 mount 的话,BuildKit cache backend(默认 local)用 target 路径做
# key,第二个 RUN 直接吃第一个 RUN 写进去的 module 缓存,不再下载。
#
# /root/.cache/go-build 是 Go 的编译产物缓存(每个包编译后的中间产物),
# mount 上之后只有改动的包需要重新编译,没改的包直接用缓存。
# 实测第一次 build 50s → 第二次(只改 ./cmd 之外的代码)build 8s 左右,
# 没有这层 cache 的话每次都全量 compile。
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    --mount=type=cache,target=/root/.cache,sharing=locked \
    CGO_ENABLED=0 go build \
    -tags webui \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/cairn \
    ./cmd/server

# ---------------------------------------------------------------------------
# 3) 运行
# ---------------------------------------------------------------------------
FROM scratch

# 复制 CA 证书（registry 是 https 时需要）
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# 复制二进制
COPY --from=builder /out/cairn /cairn

# 数据目录：凭据库、代理库、未来热度统计的 SQLite 文件都落在这里
WORKDIR /app/data

EXPOSE 8787

ENV PORT=8787

# 健康检查：用二进制自带的 -healthz 探针（HTTP GET /healthz，0=健康/1=不健康）
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/cairn", "-healthz"]

ENTRYPOINT ["/cairn"]
