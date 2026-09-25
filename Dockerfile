# 多阶段构建：
#   builder   编译 Go 二进制
#   runtime   最终镜像，scratch + 二进制
#
# 运行镜像只 ~15MB（10MB 二进制 + 5MB ca-certs + /etc/passwd），
# 比 registry-manager 的 Node + antd 几十MB 还要小一个数量级。
#
# 基础镜像可覆盖：构建机拉不到 Docker Hub 时换成内网镜像。
#   docker build --build-arg GO_IMAGE=proxy.example.com:10001/golang:1.26-alpine .

ARG GO_IMAGE=golang:1.26-alpine

# ---------------------------------------------------------------------------
# 1) 构建二进制
# ---------------------------------------------------------------------------
FROM ${GO_IMAGE} AS builder

WORKDIR /src

# 依赖先 COPY：源码不变时这层缓存命中，构建从秒级起步
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO=0：编译成纯静态二进制，scratch 也能跑
# -trimpath：去掉本地路径信息，二进制可重现
# -ldflags="-s -w"：去符号表，缩 ~30%
RUN CGO_ENABLED=0 go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/cairn \
    ./cmd/server

# ---------------------------------------------------------------------------
# 2) 运行
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

# 健康检查：用 Go 二进制自己的 /healthz
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/cairn", "-healthz"]

ENTRYPOINT ["/cairn"]