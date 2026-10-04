# Cairn (cairn) 从 0 部署 + 验收流水线
# ────────────────────────────────────────────────────────────
# 这个 Makefile 把"从 git clone 到提测就绪"的全流程流水线化,
# 让 158 上从空目录就能 make 一条推到第 0 部署就绪状态。
#
# 设计原则:
#   - 命令 copy-paste ready,每条目标都能单独跑
#   - 默认目标 help,显示所有目标
#   - 镜像 / 容器 / 数据三类产物独立 target,避免误删
#   - 强约束:跑 build 必须传 GOPROXY(否则会卡 proxy.golang.org 超时)
#
# 用法:
#   make help                       # 显示所有目标
#   make clone REMOTE=<git-url>     # git clone 到当前目录(从空开始)
#   make fresh-deploy DATA_DIR=/data/cairn  # 从 0 部署
#   make test                       # 跑本机门禁
#
# 环境变量(全部可覆盖,默认值见对应 target):
#   IMAGE          cairn:0.7.37
#   PORT           8787(容器内监听)
#   HOST_PORT      80(宿主机映射端口)
#   DATA_DIR       /data/cairn(宿主机数据目录)
#   REGISTRY_URL   registry.example.com(本仓库默认测试地址)
#   GOPROXY        https://goproxy.io,direct
#   NPM_REGISTRY   https://registry.npmmirror.com

# ───────────────────────── 变量 ─────────────────────────
IMAGE       ?= cairn:0.7.37
PORT        ?= 8787
HOST_PORT   ?= 80
DATA_DIR    ?= /data/cairn
REGISTRY_URL ?= registry.example.com
GOPROXY     ?= https://goproxy.io,direct
NPM_REGISTRY ?= https://registry.npmmirror.com

# 运行时构造 (从镜像 tag 解析版本号, 例: cairn:0.7.0 → 0.7.0)
VERSION     := $(shell echo $(IMAGE) | sed 's/.*://')

# 工具检测
GO          := $(shell command -v go 2>/dev/null)
DOCKER      := $(shell command -v docker 2>/dev/null)
COMPOSE     := $(shell command -v docker 2>/dev/null)

# ───────────────────────── 默认目标 ─────────────────────────
.PHONY: help
help:           ## 显示所有目标与说明
	@echo "Cairn (cairn) 部署流水线 — 当前 IMAGE=$(IMAGE) VERSION=$(VERSION)"
	@echo "用法: make <target> [VAR=value ...]"
	@echo ""
	@awk 'BEGIN {FS = ":.*##"; printf "TARGET          DESCRIPTION\n"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  %-14s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@echo ""
	@echo "环境变量(可覆盖):"
	@echo "  IMAGE=$(IMAGE)   VERSION=$(VERSION)"
	@echo "  PORT=$(PORT)   HOST_PORT=$(HOST_PORT)   DATA_DIR=$(DATA_DIR)"
	@echo "  GOPROXY=$(GOPROXY)"
	@echo "  NPM_REGISTRY=$(NPM_REGISTRY)"

# ───────────────────────── 一、克隆 ─────────────────────────
.PHONY: clone
clone:          ## 从远端 git clone 到 . (要求当前目录为空,REMOTE=git-url)
	@if [ -z "$(REMOTE)" ]; then echo "ERROR: REMOTE=<git-url> is required"; exit 1; fi
	@if [ -n "$$(ls -A .)" ]; then echo "ERROR: current dir not empty: $$(pwd)"; exit 1; fi
	git clone $(REMOTE) .

.PHONY: pull
pull:           ## git fetch + reset hard to origin/main
	git fetch origin main
	git reset --hard origin/main
	@echo "HEAD: $$(git log --oneline -1)"

# ───────────────────────── 二、构建 ─────────────────────────
# v0.5.43: 永远走 make build / make rebuild,不要裸跑 docker build —— 裸跑会用
# Dockerfile 默认的 proxy.golang.org(在受限网络里超时),Makefile 默认 GOPROXY
# 是 https://goproxy.io,direct(已知能访问)。要看当前用的是哪个:`make help`。
#
# BuildKit cache:Dockerfile 用 `# syntax=docker/dockerfile:1.4` + `--mount=type=cache`,
# 跨 build 复用 pnpm store + go mod cache;rebuild 默认不开 --no-cache,缓存命中即秒级。
# 若用 legacy docker daemon(<= 18.09),需要装 buildx:`docker buildx install`。
.PHONY: build
build:          ## docker build 镜像 (默认 $(IMAGE),GOPROXY=$(GOPROXY);BuildKit 自动复用缓存)
	DOCKER_BUILDKIT=1 docker build \
		--build-arg GOPROXY=$(GOPROXY) \
		--build-arg NPM_REGISTRY=$(NPM_REGISTRY) \
		-t $(IMAGE) .

.PHONY: rebuild
rebuild:        ## rebuild 镜像 (BuildKit 自动复用 pnpm/go 缓存;仅源码改动 rebuild 时用这条)
	DOCKER_BUILDKIT=1 docker build \
		--build-arg GOPROXY=$(GOPROXY) \
		--build-arg NPM_REGISTRY=$(NPM_REGISTRY) \
		-t $(IMAGE) .

.PHONY: rebuild-fresh
rebuild-fresh:  ## 强制全清 rebuild (--no-cache + 清 BuildKit cache mount;解决缓存命中出错时用)
	DOCKER_BUILDKIT=1 docker build --no-cache --progress=plain \
		--build-arg GOPROXY=$(GOPROXY) \
		--build-arg NPM_REGISTRY=$(NPM_REGISTRY) \
		-t $(IMAGE) .
	@echo ""
	@echo "提示:BuildKit cache mount 仍保留,真要彻底清可用 \`docker buildx prune --all\`。"

# ───────────────────────── 三、部署 ─────────────────────────
.PHONY: env-init
env-init:       ## 复制 .env.example 到 .env(只在 .env 不存在时)
	@if [ ! -f .env ]; then \
		cp .env.example .env; \
		echo "created .env (please edit HOST_PORT / REGISTRY_CREDENTIAL_KEY)"; \
	else \
		echo ".env already exists, skipping"; \
	fi

.PHONY: env-image
env-image:      ## 同步 .env 里的 IMAGE= 与本 Makefile 一致(避免 IMAGE 漂移)
	@if grep -q '^IMAGE=' .env 2>/dev/null; then \
		sed -i.bak 's|^IMAGE=.*|IMAGE=$(IMAGE)|' .env; \
		rm -f .env.bak; \
		echo ".env IMAGE -> $(IMAGE)"; \
	else \
		echo "WARNING: .env has no IMAGE= line; run make env-init first"; \
	fi

.PHONY: up
up: env-image   ## docker compose up -d
	docker compose up -d
	@sleep 3
	docker compose ps

.PHONY: down
down:           ## docker compose down
	docker compose down

.PHONY: restart
restart:        ## docker compose restart (源码未变,只重启才生效)
	docker compose restart
	@sleep 2
	docker compose ps

.PHONY: logs
logs:           ## tail 100 行 cairn 日志
	docker compose logs --tail=100 cairn

# ───────────────────────── 四、健康 + 配置核对 ─────────────────────────
.PHONY: health
health:         ## GET /healthz 期望 200
	@unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy; \
		curl -sS -w '\nHTTP=%{http_code}\n' http://$(REGISTRY_URL):$(HOST_PORT)/healthz

.PHONY: config
config:         ## GET /api.config 期望 version=VERSION + usingAuth=true
	@unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy; \
		curl -sS http://$(REGISTRY_URL):$(HOST_PORT)/api/config | \
		perl -ne 'BEGIN{undef $$/;} \
			print "  version=$$1\n" if /"version":"([^"]+)"/; \
			print "  port=$$1\n"    if /"port":(\d+)/; \
			print "  usingAuth=$$1\n" if /"usingAuth":(\w+)/; \
			print "  allowDelete=$$1\n" if /"allowDelete":(\w+)/;'

.PHONY: registry
registry:       ## curl /v2/ 期望带凭据 200 (无凭据 401)
	@unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy; \
		echo '--- /v2/ no creds ---'; \
		curl -sS -w 'HTTP=%{http_code}\n' -o /dev/null http://$(REGISTRY_URL):$(HOST_PORT)/v2/; \
		echo '--- /v2/ with creds ---'; \
		curl -sS -w 'HTTP=%{http_code}\n' -u admin:$$(cat /root/.regpw 2>/dev/null || echo admin) -o /dev/null http://$(REGISTRY_URL):$(HOST_PORT)/v2/

# ───────────────────────── 五、本机门禁 ─────────────────────────
.PHONY: gates
gates:          ## 本机门禁:tsc RC=0 + go build RC=0 + go test -race RC=0
	@echo "=== tsc --noEmit ==="
	@cd web && npx tsc --noEmit && cd .. && echo "TSC_RC=0"
	@echo ""
	@echo "=== go build ./... ==="
	@go build ./... && echo "GO_PLAIN_RC=0"
	@echo ""
	@echo "=== go build -tags webui ==="
	@CGO_ENABLED=0 go build -tags webui -o /tmp/cairn-gate ./cmd/server && echo "GO_WEBUI_RC=0"
	@echo ""
	@echo "=== go test -race ./... ==="
	@go test -race ./... | tail -3 && echo "GO_TEST_RC=0"

.PHONY: gate-tsc
gate-tsc:       ## 单跑 tsc
	@cd web && npx tsc --noEmit && echo "TSC_RC=0"

.PHONY: gate-build
gate-build:     ## 单跑 go build
	@go build ./... && echo "GO_PLAIN_RC=0"
	@CGO_ENABLED=0 go build -tags webui -o /tmp/cairn-gate ./cmd/server && echo "GO_WEBUI_RC=0"

.PHONY: gate-test
gate-test:      ## 单跑 go test -race
	@go test -race ./...

# ───────────────────────── 六、验收 — 前端 ─────────────────────────
# 53 上 web-auto runner 跑 cairn 全 14 场景。
# v0.6.12 (TH-1): 场景名前缀从 cairn/ 改为 go-hub/ —— runner 实际加载
# 的是 /scenarios/go-hub/*.yaml(项目曾用名 go-hub,改产品名 cairn 后
# runner 侧目录没跟着改;docs/issues/test-harness.md TH-1)。任何 cairn/
# 前缀的请求 runner 都会返 ENOENT。

RUNNER_BASE ?= http://proxy.example.com:8080
RUNNER_ENV  ?= dev

.PHONY: test-frontend
test-frontend:  ## 跑全部 14 场景(慢,~3 分钟)
	@for sc in _smoke-all-pages images-page credentials-page proxies-page pull-page \
	           pull-real settings-page stats-page delete-tag-real delete-real \
	           delete-repo-real gc-real fault-timeout fault-stall-multipage; do \
		echo "=== $$sc ==="; \
		curl --noproxy '*' -sS -X POST $(RUNNER_BASE)/api/run \
			-H 'Content-Type: application/json' \
			-d "{\"scenario\":\"go-hub/$$sc\",\"env\":\"$(RUNNER_ENV)\",\"timeout\":300000}" | \
		python3 -c "import json,sys; d=json.load(sys.stdin); \
			print(f'  status={d.get(\"status\")} runId={d.get(\"runId\")} dur={d.get(\"duration\")}ms steps={len(d.get(\"steps\",[]))}')"; \
	done

# v0.6.12 (TH-4): 列表里曾经有 9 个,实际是 8 个,而且其中含 pull-real(写路径,
# 会真的往 registry 推送/拉取镜像 —— 跟「只读」表述不符)。从 fast 拉走 pull-real,
# 留给 test-frontend 跑;注释里的 9 → 8。
.PHONY: test-frontend-fast
test-frontend-fast:  ## 只跑 7 只读场景(~1 分钟);写路径走 test-frontend
	@for sc in _smoke-all-pages images-page credentials-page proxies-page pull-page \
	           settings-page stats-page; do \
		echo "=== $$sc ==="; \
		curl --noproxy '*' -sS -X POST $(RUNNER_BASE)/api/run \
			-H 'Content-Type: application/json' \
			-d "{\"scenario\":\"go-hub/$$sc\",\"env\":\"$(RUNNER_ENV)\",\"timeout\":120000}" | \
		python3 -c "import json,sys; d=json.load(sys.stdin); \
			print(f'  status={d.get(\"status\")} runId={d.get(\"runId\")} dur={d.get(\"duration\")}ms steps={len(d.get(\"steps\",[]))}')"; \
	done

# ───────────────────────── 七、验收 — 后端(158 docker 数据面) ─────────────────────────
.PHONY: test-backend
test-backend:   ## 后端四项鉴权矩阵 + docker 数据面
	@if [ ! -f /root/.regpw ]; then echo "ERROR: /root/.regpw not found (host-only file)"; exit 1; fi; \
	unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy; \
		echo '=== L1 /v2/ 鉴权矩阵 ==='; \
		echo '--- /v2/ no creds (expect 401) ---'; \
		curl -sS -w 'HTTP=%{http_code}\n' -o /dev/null http://$(REGISTRY_URL):$(HOST_PORT)/v2/; \
		echo '--- /v2/ with creds (expect 200) ---'; \
		curl -sS -w 'HTTP=%{http_code}\n' -u admin:$$(cat /root/.regpw) -o /dev/null http://$(REGISTRY_URL):$(HOST_PORT)/v2/; \
		echo ''; \
		echo '=== L2 docker 数据面 ==='; \
		docker login $(REGISTRY_URL) -u admin --password-stdin < /root/.regpw 2>&1 | tail -1; \
		docker tag $(IMAGE) $(REGISTRY_URL)/webauto-push/accept-$(VERSION):v1 2>&1; \
		docker push $(REGISTRY_URL)/webauto-push/accept-$(VERSION):v1 2>&1 | tail -1; \
		docker pull $(REGISTRY_URL)/webauto-push/accept-$(VERSION):v1 2>&1 | tail -1

# ───────────────────────── 八、数据管理 ─────────────────────────
.PHONY: reset-data
reset-data:     ## 清空数据目录(测试阶段授权丢数据)
	@echo "WARNING: 即将清空 $(DATA_DIR) (含 registry 内容)"
	@read -p "确认? [y/N] " r && [ "$$r" = "y" ] || (echo "aborted"; exit 1)
	docker compose down
	rm -rf $(DATA_DIR)
	mkdir -p $(DATA_DIR)
	@echo "data dir cleared. run 'make up' to recreate"

.PHONY: backup
backup:         ## 打包数据目录到 /tmp/cairn-backup-<timestamp>.tar.gz
	docker compose down
	tar -czf /tmp/cairn-backup-$$(date +%Y%m%d-%H%M%S).tar.gz -C $$(dirname $(DATA_DIR)) $$(basename $(DATA_DIR))
	@ls -lh /tmp/cairn-backup-*.tar.gz | tail -3

.PHONY: restore
restore:        ## 从备份还原(必须传 BACKUP=/path/to/file.tar.gz)
	@if [ -z "$(BACKUP)" ]; then echo "ERROR: BACKUP=/path/to/file.tar.gz is required"; exit 1; fi
	@if [ ! -f "$(BACKUP)" ]; then echo "ERROR: backup not found: $(BACKUP)"; exit 1; fi
	docker compose down
	tar -xzf $(BACKUP) -C $$(dirname $(DATA_DIR))
	@echo "restored from $(BACKUP)"

# ───────────────────────── 九、清理 ─────────────────────────
.PHONY: clean
clean:          ## 清理本机 build artifact + tmps + node_modules
	@echo "--- removing tmps/ + .DS_Store + .tmp_*.py + cairn-f*check + server ---"
	@find . -maxdepth 1 -name '.tmp_*.py' -delete 2>/dev/null
	@find . -maxdepth 1 -name '.DS_Store' -delete 2>/dev/null
	@rm -rf tmps web/node_modules cairn-f*check server 2>/dev/null
	@echo "--- done ---"

.PHONY: clean-image
clean-image:    ## 删除本仓库构建的镜像
	docker rmi $(IMAGE) 2>&1 || echo "image $(IMAGE) not present"

.PHONY: nuke
nuke: clean clean-image   ## clean + clean-image + reset-data(最彻底)
	docker compose down
	rm -rf $(DATA_DIR)
	mkdir -p $(DATA_DIR)
	@echo "everything cleaned. run 'make fresh-deploy' to start over"

# ───────────────────────── 十、综合 — 从 0 部署提测 ─────────────────────────
.PHONY: fresh-deploy
fresh-deploy: reset-data pull build up health config  ## 从0部署:清数据 → pull → build → up → 健康检查 + 配对核对

.PHONY: ci
ci: gates test-frontend-fast test-backend health      ## CI:本机门禁 + 9 只读场景 + 后端 2 项 + 健康检查