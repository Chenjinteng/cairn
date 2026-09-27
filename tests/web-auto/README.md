# web-auto 测试场景（cairn）

本目录是 [`web-auto`](../../../../../.minimax/agents/web-auto/agent.md) agent 的测试定义，与 cairn 主二进制同 repo 维护。
**触发方**：[`web-auto`](../../../../../.minimax/agents/web-auto/agent.md) agent 通过 `web-auto:run-test` skill
调用独立部署的 test-runner。

## 拓扑（2026-09-27 更新）

```
Mac (开发机)                              UAT 内网
   │                                          │
   │ curl POST /api/run                       │
   ▼                                          │
┌─────────────────────┐    内网    ┌──────────────────────┐
│ test-runner         │───────────▶│ cairn               │
│ proxy.example.com:8080    │  ~3ms     │ registry.example.com:80      │
│ 容器 web-auto-runner│            │ 版本 v0.5.17         │
│ :0.1.0              │            │                      │
└─────────────────────┘            └──────────────────────┘
```

- **Mac → 158:80 不通**（防火墙限制），所以浏览器测试必须在 53 上的 runner 里跑
- **runner → 158:80 通**（3ms 实测），baseUrl 直接写 `http://registry.example.com:80`
- runner 容器 mount：
  - `/opt/web-auto-runner/web-auto/tests` → `/scenarios` (ro)
  - `/opt/web-auto-runner/web-auto/config` → `/config` (ro)
  - `/opt/web-auto-runner/web-auto/test-runner/artifacts` → `/artifacts` (rw)

## 文件结构

```
tests/web-auto/
├── README.md                       ← 你正在看
├── config/
│   ├── web-auto.dev.yaml           ← dev env 配置（已 mount 进 runner）
│   └── web-auto.uat.yaml           ← UAT env 配置
└── scenarios/
    ├── _smoke-all-pages.yaml       ← 6 Tab 顺序点一遍，无副作用
    ├── images-page.yaml            ← 镜像列表：搜索 + 详情 Drawer + 删除/GC 确认
    ├── stats-page.yaml             ← 镜像热度：heatmap + 子 Tab + 忽略规则
    ├── pull-page.yaml              ← 镜像拉取：表单 + 平台下拉 + 任务列表
    ├── credentials-page.yaml       ← 凭据管理：列表 + 新增 Modal + 测试连接
    ├── proxies-page.yaml           ← 代理管理：列表 + 新增 Modal + 批量探测
    └── settings-page.yaml          ← 设置：版本徽章 + 探测/刷新 + 编辑态 + 忽略规则
```

## cairn 6 个 Tab

| 顺序 | key          | 中文标签     | 主要组件 / 入口                              |
|----|--------------|----------|----------------------------------------|
| 1  | `images`     | 镜像列表   | 搜索框、表格、详情 Drawer、删除、GC          |
| 2  | `stats`      | 镜像热度   | 贡献图、Summary、Top、事件 ring、忽略规则 Modal |
| 3  | `pull`       | 镜像拉取   | 拉取表单（源/目标/平台/凭据/代理）、任务列表       |
| 4  | `credentials`| 凭据管理   | 凭据列表、测试连接、新增/编辑/删除 Modal          |
| 5  | `proxies`    | 代理管理   | 代理列表、批量探测、新增/编辑/探测 Modal          |
| 6  | `settings`   | 设置      | 版本号、刷新/探测、编辑/保存、忽略规则 Modal     |

> cairn **没有登录页、没有多实例、没有 RBAC**——所有 Tab 默认直接可达。

## 在 Mac 跑场景（推荐路径）

### 1. 直跑 runner API

```bash
# 冒烟（6 Tab）
curl -sS -X POST -H 'Content-Type: application/json' \
  -d '{"scenario":"cairn/_smoke-all-pages","env":"dev","timeout":120000,"artifacts":{"screenshot":"always","trace":"on-failure","video":"never","har":"never"}}' \
  http://proxy.example.com:8080/api/run | python3 -m json.tool
```

### 2. 通过 web-auto skill

```bash
WEB_AUTO_RUNNER_URL=http://proxy.example.com:8080 \
bash /Users/snow/.minimax/agents/web-auto/skills/run-test/scripts/run.sh \
  cairn/_smoke-all-pages dev 120000
```

### 3. 通过 web-auto agent

在 chat 里说"用 web-auto agent 跑一下 cairn 的 credentials-page，env=dev"。

## 在 Mac 同步新场景到 runner 容器

`/opt/web-auto-runner/web-auto/tests` 是 bind mount 的源目录，**ro** 没法往容器内写；要更新场景：

```bash
# 53 上没 SSH，文件传输走 dufs（53:80 的 nginx 反代到 10002 dufs 容器）
# Mac 端上传新场景
curl -X PUT --data-binary @tests/web-auto/scenarios/_smoke-all-pages.yaml \
  http://proxy.example.com:80/data/dufsStorage/web-auto/scenarios/_smoke-all-pages.yaml

# 53 上 tar 展开到 bind mount 源目录
ssh root@proxy.example.com 'cd /data/dufsStorage/web-auto && tar czf - scenarios/ | tar xzf - -C /opt/web-auto-runner/web-auto/'

# 验证：runner API 重新列场景
curl -sS http://proxy.example.com:8080/api/scenarios | python3 -m json.tool
```

> ⚠️ runner 是以非 root UID 501:games 跑（按 mount 属主推断），文件落到
> `/opt/web-auto-runner/web-auto/tests/cairn/` 后注意权限保持 0644。

## 编写约定

按 2026-09-27 实测跑通的 5 个老场景（`cairn/login` / `cairn/registries-list` + `_adhoc/*`）的风格：

- **`assert` 用 flat 语法**：
  ```yaml
  - name: 断言 H1
    action: assert
    type: text              # text | visible | hidden | value
    selector: 'h1'
    value: "镜像列表"
    match: contains         # exact | contains（仅 text 类型生效）
  ```
- **`click` / `fill` / `waitFor` 用 strategies 列表**：
  ```yaml
  - name: 点击「镜像列表」
    action: click
    locator:
      strategies:
        - { type: text, value: "镜像列表", match: exact }
        - { type: css, value: '.app-nav .ant-segmented-item:has-text("镜像列表")' }
    timeout: 8000
  ```
- **表格行必须 `table tbody tr.ant-table-row`**——antd 内部有 `<tr class="ant-table-measure-row" aria-hidden>` 测量行干扰
- **`include` 复用其他场景**：`{ action: include, scenario: <相对路径>, until: <step-name> }`
- **副作用一律二次确认 + 取消**：删除、GC、拉取提交都包 confirm 弹窗 → 取消
- **超时上限 300000ms**（runner 上限）；本仓库场景统一 90s ~ 120s

## 已知坑

- cairn 启动时 `app-header` 会先显示「加载中…」直到 `/api/config` 返回；`waitFor .app-shell` 给 15s 预算
- `/api/inventory` 第一次是慢操作（120s 超时），smoke 场景 timeout 设 120000ms
- **H1 / 中文文案断言**：当前 App.tsx 顶部没有真 `<h1>`，H1 在 .app-content 各页面里也可能不存；老场景 `cairn/login.yaml` 的兜底是 `'h1, .page-title, [data-testid="page-title"]'`。如果断言 H1 失败，去掉或改 selector
- antd `Segmented` 内部文案是 span（`.ant-segmented-item-label`），`text: "镜像列表"` 能命中；`text: "镜像列表"` + `match: exact` 更稳
- runner 当前是 MVP，`optional:` 字段、`press_key` 等 action **未实现**——场景里不要用
- 版本 v0.5.17 dev 没有登录鉴权，所以 **不要**写登录用例——直接打开 `/` 就是 dashboard

## 实测状态（2026-09-27）

| 场景 | 用时 | 状态 |
|------|-----|------|
| `cairn/login`           | 3.1s | ✅ passed |
| `cairn/registries-list` | 4.0s | ✅ passed |
| `_adhoc/screenshot`      | 2.6s | ✅ passed |
| `_adhoc/click`           | —    | 已加载待实测 |
| `_adhoc/fill`            | —    | 已加载待实测 |

本目录的 7 个新场景刚按真实 runner 语法重写完成，待 Mac → 53 → 158 链路跑通后实测。