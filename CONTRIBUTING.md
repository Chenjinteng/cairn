# Contributing to Cairn

感谢愿意贡献。本指南说清楚本仓库的协作约定,**先读再提 PR** 会显著减少来回。

## 一、提 PR 前必做(本机门禁)

```bash
# 把所有"格式 / 静态检查 / 单元测试 / webui 编译"一条命令跑完
make gates
```

包含:

| 步骤 | 命令 | 通过条件 |
|---|---|---|
| TypeScript 类型检查 | `cd web && npx tsc --noEmit` | RC=0 |
| Go 普通 build | `go build ./...` | RC=0 |
| Go webui tag build | `go build -tags webui -o /tmp/cairn-gate ./cmd/server` | RC=0 |
| Go race test | `go test -race ./...` | 全部 `ok` |

**任何一步失败都不接受 PR。** 修本地复现后再 push,不要把"在我机器能跑"扔给 reviewer。

## 二、Commit message 规范

仓库遵循 [Conventional Commits](https://www.conventionalcommits.org/),前缀在 git log 里一致使用:

| 前缀 | 何时用 | 版本号影响 |
|---|---|---|
| `feat:` | 新能力 / 新模块 / 用户可感知的功能 | 中版本 +1(0.x.**y** → 0.x+1.0) |
| `fix:` | bug 修复 | 小版本 +1(0.x.y → 0.x.y+1) |
| `refactor:` | 重构(无功能变化、无修复) | 小版本 +1 |
| `perf:` | 性能优化 | 小版本 +1 |
| `docs:` | 仅文档 | 通常不发版本,跟随已合入轮 |
| `chore:` | 构建 / 工具 / 杂项 | 通常不发版本 |
| `test:` | 仅测试 | 通常不发版本 |
| `style:` | 格式 / 命名(无逻辑变化) | 通常不发版本 |

格式:

```
<prefix>(scope): <一句话主题 —— 为什么改 + 改了什么>

可选的 body(说"做了什么 / 为什么这么做 / 用户可见的变化")。

可选的 footer(Breaking change / Closes #issue)。
```

**真实例子**(从 git log):

```
feat(pull): 0.5.48 — 知名拉取源预置 + 第三方拉取源可配置
fix(registry): 0.5.50 — PATCH/GET upload 响应 Range 头多 1 字节 end,致 regsync 撞 size mismatch
chore(release): unify module path, binary name, and brand under cairn
```

> **小版本 vs 中版本** 边界:同一轮里既有新能力又有修复 → 取**最高**那一档(即中版本 +1),具体条目写进 CHANGELOG.md 即可。

## 三、版本号规则(见 [`AGENTS.md`](./AGENTS.md) §版本号规则)

**主版本**(`x.0.0`)必须由人指定,AI / 自动化不得自行进位。  
**中版本**(`0.x.0`):新模块或用户可感知的新能力。  
**小版本**(`0.0.x`):缺陷修复与既有功能优化。

每次发版要在 5 处同步(漏一处会漂移,导致运行时版本号跟 UI / 镜像 tag 不一致):

1. `internal/version/version.go` 的 `Version` 常量
2. `docker-compose.yml` 的 `image: ${IMAGE:-cairn:X.Y.Z}`
3. `.env.example` 的 `IMAGE=`
5. `README.md` 里所有 `docker build/tag/push` 示例
4. `CHANGELOG.md` 新增一节(在顶部)

## 四、目录结构速查

```
.
├── cmd/server/                 # 主入口(main + 装配)
├── internal/
│   ├── api/                    # HTTP handlers(/api/*)
│   ├── config/                 # env 配置加载
│   ├── credentials/            # AES-256-GCM 凭据库
│   ├── db/                     # SQLite 封装
│   ├── events/                 # webhook + 热度统计
│   ├── pull/                   # 拉取队列
│   ├── proxies/                # 代理库
│   ├── registry/               # V2 协议客户端(浏览 / 删除 / 拉取)
│   ├── registryd/              # 内置 registry server(/v2/* 路由)
│   ├── server/                 # Build(cfg) → *http.Server
│   ├── storage/                # 文件系统原子写 + digest 校验
│   ├── version/                # 运行时版本号
│   └── webui/                  # //go:embed 前端 dist
├── web/                        # React + AntD + Vite 前端
├── docs/
│   ├── design/                 # 品牌 / UI / 页面原型
│   ├── resilience.md           # v0.5.x 韧性轮问题清单(历史)
│   └── ROADMAP.md              # 排期号位
├── Dockerfile
├── docker-compose.yml
├── Makefile                    # 从 0 部署 + 验收流水线
├── .env.example
├── LICENSE                     # Apache-2.0
└── NOTICE                      # 三方依赖归属
```

## 五、刻意不做(请确认你的 PR 不在以下范围)

`AGENTS.md` 顶层和 `docs/ROADMAP.md`「刻意不做」段列了 cairn 明确**不**做的事。提交前先看一眼:

- ❌ 登录 / 用户体系 / RBAC
- ❌ 镜像扫描 / CVE 检测
- ❌ 镜像签名 / cosign 集成
- ❌ 多 registry 聚合
- ❌ 配额 / 速率限制
- ❌ Helm chart / OCI artifact 浏览(只管 Docker 镜像)

**如果你的 PR 命中以上任一条**,先开 issue 讨论,不要直接提。

## 六、PR 流程

1. Fork 仓库 → 新建分支(命名建议 `<prefix>/<short-desc>`,例如 `fix/registry-range-header`)
2. 本地跑 `make gates` 全绿
3. 写 commit message(遵循上面的 conventional commits 规范)
4. push → 开 PR
5. **PR 描述**用 [`.github/pull_request_template.md`](./.github/pull_request_template.md) —— 它会列出要确认的清单

Review 关注:
- 改动是否最小(无关文件不要改)
- 单元测试是否覆盖改动
- 文档(README / AGENTS / CHANGELOG / docs)是否需要同步更新
- 是否引入新依赖(看 `go.mod` / `web/package.json` 变化)

## 七、报告 bug

开 issue 用 [`.github/ISSUE_TEMPLATE/bug_report.md`](./.github/ISSUE_TEMPLATE/bug_report.md)。

**报告 bug 时附**:

- 镜像 tag / commit hash
- `docker compose logs cairn` 输出
- 复现步骤(`docker pull` / `curl` 命令、UI 操作路径)
- 预期 vs 实际
- /api/config 输出(注意脱敏 `registry.url` / 凭据)

## 八、提功能建议

开 issue 用 [`.github/ISSUE_TEMPLATE/feature_request.md`](./.github/ISSUE_TEMPLATE/feature_request.md)。

先描述**问题**(为什么要这个能力、当前痛点),再写你想要的方案。提案前先查 `docs/ROADMAP.md`「刻意不做」段 —— 如果命中,大概率不接。

## 九、CLA / DCO

本项目目前不强制签署 CLA,贡献者通过 PR 默认同意按 Apache-2.0 许可本贡献。如果未来加 CLA / DCO 流程,会单独公告。

## 十、行为准则

详见 [`CODE_OF_CONDUCT.md`](./CODE_OF_CONDUCT.md)(Contributor Covenant 2.1)。违反准则的评论 / commit / PR 会被移走甚至封禁。