# web-auto 测试场景（cairn）

本目录是 [web-auto](https://内部) agent 的测试定义，与 cairn 主二进制同 repo 维护。
**触发方**：[`web-auto`](../../../../../.minimax/agents/web-auto/agent.md) agent 通过 `web-auto:run-test` skill
调用独立部署的 test-runner（默认 `http://runner.local:8080`，**MVP 阶段主机未到位**——见末尾"前置条件"）。

## 文件结构

```
tests/web-auto/
├── README.md                       ← 你正在看
├── config/
│   ├── web-auto.dev.yaml           ← dev 环境 baseUrl + 凭据占位
│   └── web-auto.uat.yaml           ← UAT 环境 baseUrl + 凭据占位
└── scenarios/
    ├── _smoke-all-pages.yaml       ← 6 个 Tab 全部点一遍，无副作用
    ├── images-page.yaml            ← 镜像列表：搜索 + 详情 Drawer + 删除确认
    ├── stats-page.yaml             ← 镜像热度：heatmap + 卡片 + 忽略规则
    ├── pull-page.yaml              ← 镜像拉取：表单填写 + 取消任务
    ├── credentials-page.yaml       ← 凭据管理：列表 + 新增/编辑/删除/测试
    ├── proxies-page.yaml           ← 代理管理：列表 + 新增/编辑/探测/测试
    └── settings-page.yaml          ← 设置：刷新 + 探测 + 编辑 + 保存
```

## cairn 6 个 Tab（必须真实存在的导航项）

| 顺序 | key        | 中文标签     | 主要组件 / 入口                                |
|----|------------|----------|------------------------------------------|
| 1  | `images`     | 镜像列表   | 搜索框、表格、详情 Drawer、删除、GC                |
| 2  | `stats`      | 镜像热度   | 贡献图、Summary、Top、事件 ring、忽略规则 Modal |
| 3  | `pull`       | 镜像拉取   | 拉取表单（源/目标/平台/凭据/代理）、任务列表、取消按钮      |
| 4  | `credentials`| 凭据管理   | 凭据列表、测试连接、新增/编辑/删除 Modal                |
| 5  | `proxies`    | 代理管理   | 代理列表、批量探测、新增/编辑/单独探测 Modal              |
| 6  | `settings`   | 设置      | 视图切换、保存全部、忽略规则编辑、版本号 + 探测/刷新按钮   |

> cairn **没有登录页、没有多实例、没有 RBAC**——所有 Tab 默认直接可达。

## 在 UAT 端跑起来

### 0. 前置：test-runner 必须先起来

> ⚠️ **MVP 阶段 runner.local 这台 test-runner 主机还没部署。**
> 在它跑起来之前，`web-auto:run-test` 会一直 `status: errored`，错误信息是 "runner unreachable"。
> 这不是 YAML 的问题，是基础设施问题。部署指引见 `test-runner/README.md` §3。

确认 runner 在跑：

```bash
ssh user@runner.local 'curl -sS http://localhost:8080/api/health'
# 期望返回 {"status":"ok",...}；或者通过 web-auto skill 的 health.sh
bash /Users/snow/.minimax/agents/web-auto/skills/run-test/scripts/health.sh
```

### 1. UAT 端拉代码

```bash
ssh root@registry.example.com
cd /root/cairn   # 或 cairn 实际部署目录
git fetch origin main
git reset --hard origin/main      # 必须有——否则会拿到旧版 web-auto YAML
# 验证：grep -rn '镜像列表' tests/web-auto/scenarios/ | head -3
```

### 2. 跑单 Tab 场景

通过 web-auto agent（推荐——它会汇总报告 + 诊断失败）：

```
"用 web-auto agent 跑一下 cairn 的 credentials-page，env=uat"
```

或直接调 runner：

```bash
WEB_AUTO_RUNNER_URL=http://runner.local:8080 \
bash /Users/snow/.minimax/agents/web-auto/skills/run-test/scripts/run.sh \
  cairn/credentials-page uat 90000
```

### 3. 跑全量冒烟（先打头阵）

```bash
# 全 6 Tab 顺序点一遍，~30~60 秒
WEB_AUTO_RUNNER_URL=http://runner.local:8080 \
bash /Users/snow/.minimax/agents/web-auto/skills/run-test/scripts/run.sh \
  cairn/_smoke-all-pages uat 120000
```

## 编写约定

- **locator 必须给 ≥2 个 strategy**——失败时 web-auto 会按顺序尝试，找到第一个能命中的并提示"把成功策略提到第一位"。
- **断言用 `type: text` + 中文文案**——cairn 是中文 UI，断言英文容易误报。
- **不写副作用测试**——删除凭据、运行 GC、删除 manifest 这种"会改状态的"用 `scenario: xxx` 单独拆场景，由用户在 UAT 主动触发。
- **超时上限 300000ms**——超过会被 runner 截断；本仓库场景统一 90s ~ 120s。
- **截图策略默认 `on-failure`**——只有冒烟场景开 `always`（要留底）。

## 已知坑

- cairn 启动时 `app-header` 会先显示「加载中…」直到 `/api/config` 返回，scenario 的 `waitFor app-shell` 必须给 15s 预算。
- 反代后部署时 `/api/inventory` 第一次会带慢操作（120s 超时），scenario 默认 timeout = 120000ms。
- v0.5.17 之前 settings 页可能没有"保存全部"按钮，本目录 scenario 是按当前 main 分支写的；UI 大改后请同步。