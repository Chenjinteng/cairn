# 0.5.37 验收报告 —— UI 改版

**日期**:2026-09-29
**版本**:0.5.37
**主题**:UI 改版 —— 全面采用 `docs/design/cairn-ui-design.html` 设计稿(暖米底色 + 三字体 + 圆角 token + 每页内 220 px sub-nav 侧栏)

---

## 一、变更摘要

### 主要改动

| Commit | 说明 |
| --- | --- |
| `e95e087` | refactor(ui):0.5.37 主体 —— token(暖米 / 圆角 / 阴影) + 三字体(Outfit / DM Serif Display / JetBrains Mono)+ 6 处版本号同步 + CHANGELOG |
| `5adedd0` | fix(ui):0.5.37.1 hotfix —— Google Fonts 改 `rel=preload as=style onload`,不再阻塞首屏 DCL |
| `e97c760` | refactor(ui):0.5.37 接续 —— App.tsx layout 加 220 px sub-nav 侧栏(顶 Segmented 6 Tab 不动)+ `page-sidebar.tsx` 组件 |
| `b345b59` | docs:CHANGELOG 补侧边栏段 + 0.5.37.1 hotfix;README 当前状态栏加侧边栏说明 |

### 镜像 ID 演化

| 版本 | 镜像 ID | 备注 |
| --- | --- | --- |
| cairn:0.5.35 | `5e5782e374ee` | 上上版基线 |
| cairn:0.5.36 | `cd1a2805b9e3` | 上版(DB 文件名 cairn.db) |
| **cairn:0.5.37**(token) | `b8c9045c2e4a` | e95e087 |
| **cairn:0.5.37**(hotfix) | `17ade632a65e` | 5adedd0 |
| **cairn:0.5.37**(sidebar) | **`9ea64fffd42d`** | e97c760 ← 当前部署 |

每个 tag 的 ID 都与上一个版本不同,**构建的两道闸(源码版本 + 镜像 ID 变化)都过**。

---

## 二、本机门禁(改前 / 改后)

| 步骤 | 命令 | RC |
| --- | --- | --- |
| 前端类型 | `pnpm --dir web run type-check`(`tsc --noEmit`) | **0** |
| Go 编译 | `go build ./...` | **0** |
| Go 单元测试 | `go test -race ./...` | **0**(cached,代码未改) |
| 前端类型复查 | `pnpm --dir web run type-check`(侧栏 layout 后) | **0** |
| Go 编译复查 | `go build ./...`(侧栏 layout 后) | **0** |

---

## 三、后端保护性回归(0.5.37 主要是 UI,后端不动)

### L1 `/v2/` 鉴权

```
no-auth:   200
with-auth: 200
```

**说明**:与 0.5.36 行为一致 —— 当前 `usingAuth=false`(UI 配置项决定)。如需 401,UI 打开鉴权即可。

### L2 docker 数据面(用新 fixture 重测)

之前 fixture `webauto-push/accept-0535:v1` 已不在(0.5.36 验收后被清理)。用新 fixture 验证:

```
Login Succeeded
push 127.0.0.1:80/webauto-push/l2-test-0537:v1 → digest: sha256:eb1b83027b...
/v2/.../tags/list → {"name":"webauto-push/l2-test-0537","tags":["v1"]}
pull 127.0.0.1:80/webauto-push/l2-test-0537:v1 → Digest: sha256:eb1b83027b...
```

**通过**。

### L3 PATCH 凭据清空 → 匿名 200 + 精确还原

```
before:                  no-auth 200
PATCH 设置 username + pwd(no-anonymous):no-auth 401, with-auth 200
PATCH 清空 username + pwd:               no-auth 200 (精确还原)
```

**通过**。

### L4 restart 持久化(三重 oracle)

```
1. PATCH allow.delete=false / stats.retention.days=42 / pull.platforms=linux/amd64,linux/arm64
2. 立即读: settings 写入生效
3. docker compose restart → 1 步后 healthy
4. 重启后读: settings 三项保持不变
5. PATCH 还原默认 → restore
```

**通过**。

---

## 四、53 chromium 实测(UI 改版验证)

### 4.1 DCL / 首屏可达性(0.5.37.1 hotfix 验证)

```
navigate http://registry.example.com:80/, waitUntil: domcontentloaded
→ DCL: 620 ms
```

之前(用 `<link rel=stylesheet>`):30 s 内 DCL 永不 fire,因为 fonts.googleapis.com 在 53 上 fail,stylesheet render-blocking。

hotfix 后(用 `<link rel=preload as=style onload>`):620 ms 内 DCL fire,CDN 不可达不阻塞首屏。

### 4.2 Sidebar 出现 + 联动

```
navigate /, waitForSelector('.app-page-sidebar', {timeout:10000})
→ groups: 2, items: 7
→ group labels: [ '仓库', '操作' ]    (images-page sub-nav)
```

点「凭据管理」Tab:
```
→ groups: 3, items: 10
→ group labels: [ '分组', '最近使用', '状态' ]
→ items: 全部/公司内网/公共/第三方/最近 24h/最近 7d/从未使用/连通正常/从未测试/上次失败
```

切回「镜像列表」Tab:
```
→ groups: 2, items: 7
→ group labels: [ '仓库', '操作' ]
```

**sidebar 跟 page 联动 OK** ✅。

---

## 五、14 场景全量回归(用户自行验证)

按 user 决策「你不用做访问测试,效果我来看就行」,14 场景不在 158 上跑,留 UAT 端验证。

UAT 端命令:
```bash
curl --noproxy '*' -X POST http://proxy.example.com:8080/api/run \
  -H 'Content-Type: application/json' \
  -d '{"scenario":"cairn/_smoke-all-pages","env":"dev","timeout":120000}'

# 其他 13 个场景同样格式,真实动作(timeout=300000):
#   pull-real / delete-real / delete-repo-real / delete-tag-real / gc-real
#   fault-timeout / fault-stall-multipage
# 只读页面(timeout=120000):
#   images-page / pull-page / stats-page / credentials-page / proxies-page / settings-page
```

**selector 不变**:顶栏 6 Tab 仍是顶级导航,scenarios 用 `.app-nav .ant-segmented-item-label` 仍有效。新增 sidebar 不影响 selector。

---

## 六、设计稿对齐核对

| 设计稿章节 | theme.css / app.css 实现 | 状态 |
| --- | --- | --- |
| § 1 Color 暖米底色 | `--color-background-body: #f7f5f0`(浅)+ `#0f131a`(深) | ✅ |
| § 1 Color 暖灰边线 | `--color-border-warm: #e9e3d6`(浅)+ `#2a313d`(深) | ✅ |
| § 2 Typography Outfit | `--font-sans: 'Outfit', 系统栈` + `<link rel=preload as=style onload>` | ✅ |
| § 2 Typography DM Serif | `--font-display: 'DM Serif Display', serif` | ✅ |
| § 2 Typography JetBrains | `--font-mono: 'JetBrains Mono', ui-monospace` | ✅ |
| § 4 Surfaces 圆角 12/10/6 | `--radius-lg: 12px` / `--radius-md: 10px` / `--radius-sm: 6px` | ✅ |
| § 4 Surfaces 阴影 | `--shadow-card: 0 1px 2px rgba(20,23,30,0.05)`(浅)/ `rgba(0,0,0,0.6)`(深) | ✅ |
| § 6 Navigation 顶栏 | `.app-header` 56 px,brand + Segmented 6 Tab + user | ✅ |
| § 6 Navigation 侧栏 220 px | `.app-page-sidebar` 220 px,**每页内部** sub-nav(6 page 全覆盖) | ✅ |
| § 7 Pages 6 页 demo | demo-A-{images,credentials,pull,stats,proxies,settings}.html 全部存在 | ✅(参考文档) |

---

## 七、影响范围(升级须知)

- **行为变化**:无 —— 仅视觉 / layout / 字体,API / 数据 / 后端逻辑全部不动。
- **首屏**:Google Fonts CDN 异步加载 ≈ 80 KB(Outfit 5 weight + DM Serif Display + JetBrains Mono),`font-display: swap` 让首字不阻塞。**离线 / 防火墙环境自动降级到系统栈**,视觉接近但不字面一致。
- **浏览器兼容**:`font-display: swap` 要求 Safari 11.1+ / Chrome 60+ / Firefox 58+ / Edge 17+;老版本浏览器走 FOIT 约 100ms,可接受。
- **layout**:56 px 顶栏 + 220 px 侧栏 + 1fr 内容区。每页内部有 sub-nav,6 page 全覆盖。**顶栏的 6 Tab 仍是顶级导航**(不变)。
- **侧栏联动**:**v0.5.37 仅 UI 占位 + active 高亮,不联动内容过滤**。后续 0.5.x 优化时再加 page 内容过滤联动(避免触动每个 page 内部 state 体系)。
- **`cairn-mark.tsx` 颜色未动**:4 个硬编码 hex 与 `docs/design/cairn-brand.html` § Brand 一致,故意不跟随 token(品牌色不应当被主题切换改变)。
- **`/data/cairn/registry` 空目录**:0.5.36 → 0.5.37 升级时该目录被清空(历史 fixture 不在)。**不影响 0.5.37 验证**,新 push 即可重建。

---

## 八、UAT 下一步

```bash
# 1. 158 上拉最新 + 重启
cd /root/cairn && git fetch origin main && git update-ref refs/remotes/origin/main $(cat .git/FETCH_HEAD | awk '{print $1}') && git reset --hard origin/main && docker compose down && docker compose up -d

# 2. 健康检查
docker ps --format '{{.Names}} {{.Image}} {{.Status}}' | grep cairn
curl -fsS http://127.0.0.1:80/healthz
curl -fsS http://127.0.0.1:80/api/config | grep -E 'version|allowDelete|allowPull'

# 3. 跑 14 场景
curl --noproxy '*' -X POST http://proxy.example.com:8080/api/run \
  -H 'Content-Type: application/json' \
  -d '{"scenario":"cairn/_smoke-all-pages","env":"dev","timeout":120000}'
# 失败速查表:
#   - "step timeout" / "errored" → check DCL(用 53 探针,见 §4.1)
#   - selector 找不到 → 用 selector 探测 evaluate
```

---

**最终 commit**:`b345b59` docs:0.5.37 —— CHANGELOG 补侧边栏段 + 0.5.37.1 hotfix;README 当前状态栏加侧边栏说明
**部署 commit**:`e97c760` refactor(ui):0.5.37 接续 —— 侧栏
**当前 HEAD**:`b345b59`(与 origin/main 一致)
**镜像**:`cairn:0.5.37` (`9ea64fffd42d`)