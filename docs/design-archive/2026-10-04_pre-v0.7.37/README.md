# docs/design 快照 · 2026-10-04 pre-v0.7.37

## 这是什么

`docs/design/` 上一版（自报 cairn web 0.5.36）的内容快照。
保留原因：马上要把它刷新到 v0.7.37。刷新前先 freeze 一份,方便对照新旧版做复盘。

| 字段 | 值 |
|---|---|
| 备份时间 | 2026-10-04 |
| 备份者 | Mavis |
| 触发动作 | 把 `docs/design/` 刷新到 cairn web 0.7.37 |
| 原版自报版本 | "与 cairn 0.5.36 web 同步"(自述) |
| 原版实际对应 | cai web 0.5.36(自述对应)→ 实际 current era 版本是 0.7.37,**期间跨度 22 个中版本** |
| 当前 cairn web 版本 | 0.7.37(`internal/version/version.go`) |
| 备份目录 | `docs/design-archive/2026-10-04_pre-v0.7.37/` |
| 文件清单 | 10 个 html(brand / ui-design / overview / 6 个 demo-A) |

## 复盘索引

下面每个差异点都标了"原版在哪 + 实际实现位置",刷新后两边都能对照看。

### 字体策略（v0.7.37 设计稿恢复 Google Fonts）

刷新时走方案 A —— **设计稿和 demo / 实际 web 字体栈刻意不同**:

| 资产 | 字体策略 | 理由 |
|---|---|---|
| 设计稿（cairn-brand / cairn-ui-design / index）| Google Fonts CDN · Outfit / DM Serif / JetBrains Mono | 设计理想态,视觉好看 |
| demo-A-*.html（8 个）| system stack | 跟实际 cairn web 一致 |
| 实际 cairn web（web/src/theme.css + main.tsx）| system stack（v0.5.38 起）| 离线部署零外部依赖,无 FOUT |

原版（v0.5.36 时期）所有 10 个设计稿 + 8 个 demo 都 link Google Fonts——本快照完整保留了那时的状态。

v0.7.37 刷新时一开始把所有 html 都改成 system stack,后用户觉得老字体（Google Fonts）好看一些,改回分工模式。三份设计稿顶部都有注释明确写「设计理想态 vs 实际 web」,不会让人误以为字体栈应该一致。

如果未来需要让设计稿字体跟实际 web 完全对齐,有两条路:
- (i) 接受 system stack 在设计稿里的视觉,所有 html 统一走 system stack
- (ii) 给实际 cairn web 加回 Google Fonts,放弃 v0.5.38 立的"离线部署零外部依赖"原则

### 元数据过期

| 原版位置 | 写的是什么 | 实际是什么 |
|---|---|---|
| `index.html` L105 | "与 cairn 0.5.36 web 同步" | 0.7.37 |
| `cairn-ui-design.html` L306-309 | "Cairn 0.5.36 / 与 web/src/theme.css + main.tsx 同步" | 同上 |
| `cairn-ui-design.html` footer | "2026-09-29" | 2026-10-04 |
| 每个 `demo-A-*.html` `.brand-version` | `v0.5.36` | v0.7.37 |

### 设计稿 vs 实际前端差异

差异项按等级排序:**🔴 必须修的过期声明 / 🟠 大块改动 / 🟡 数值漂移 / 🟢 设计稿小坑**

#### 🔴 元数据(必须修)

- `index.html` 自报版本号:`0.5.36` → 应改 `0.7.37`
- `cairn-ui-design.html` 自报版本号 + "与 ... 同步"声明
- 各 demo 的 `.brand-version: v0.5.36`

#### 🟠 大块缺漏 / 设计路线已变

- **缺 sync 页 demo**:`App.tsx` 第 4 个 Tab(NAV_ITEMS `sync`,`SwapOutlined` 图标)是 v0.5.40+ 加的核心功能,设计稿 6 页 demo 中完全没有 sync,也没有 `demo-A-sync.html`
- **侧栏"操作"组已废止**:`demo-A-images.html` 侧栏写"扫描清单 / 运行 GC / 备份"三个有副作用的按钮。但 `page-sidebar.tsx` L13-15 注释明确写 v0.5.37.4 起侧栏只放视图状态,操作归页头 `.page-actions`。设计稿代表已被推翻的设计路线。
- **anchor scroll-spy 实际做了,demo 没体现**:settings 页用 `IntersectionObserver`(`mode: 'anchor'`),`demo-A-settings.html` 还在用 `.subtabs` 容器 + 5 个 sub-tab 按钮的老设计。实际锚点是 4 项:仓库连接 / 功能开关 / 拉取镜像的架构 / 热度记录;demo 的 5 项 sub-tab(拉取配置 / 忽略规则 / 高级)都不准。
- **字体 CDN 已下线**:`cairn-brand.html` / `cairn-ui-design.html` link `fonts.googleapis.com`(Outfit / DM Serif Display / JetBrains Mono)。v0.5.38 改成 system stack(`-apple-system / Georgia / ui-monospace`),注释说"离线部署零外部依赖"。设计稿没标注这次回退。

#### 🟡 数值漂移

**颜色 token**(design 用了 google palette 接近值,实际用的是 v0.5.22 后的调色板):

| token | 原版 demo | 实际 theme.css / main.tsx |
|---|---|---|
| success 浅色 | `#16a34a` | `#27c274` |
| success 深色 | `#4ade80` | `#3ddc84` |
| ink / text-1 浅色 | `#1f2937` | `#1e252e` |
| ink-soft / text-3 浅色 | `#6b7280` | `#7588a3` |
| line 浅色 | `#e5e7eb` | `#eaecf0` |
| primary-deep | `#0f766e` | 没这个 token |
| accent / accent-deep | 有 | 没这两个 token(amber 已无业务用途) |

**布局尺寸**:

| 项 | 原版 demo | 实际 |
|---|---|---|
| Header 高 | 56 px | 58 px |
| Page title 字号 | 22 px / weight 600 | 16 px / weight 500 |
| KPI value 字号 | 24 px | 22 px |
| 表格 | 手工 grid(`.row` / `.cred-row` / `.proxy-row` / `.job-row`) | AntD `<Table>` |

**品牌 mark**:
- `cairn-brand.html` / `cairn-ui-design.html` hero 用了正确的 SVG(3 stacked rect + amber dot)—— 跟 `web/src/components/cairn-mark.tsx` 一致 ✅
- **6 个 `demo-A-*.html` 用的是 26×26 圆角方块 + 大写"C"字**——跟 cairn mark 不一致,也不是 brand 稿里的版本

#### 🟢 设计稿小坑

- demo 内部分裂:`cairn-ui-design.html` 写 `--line: #e9e3d6`(暖灰)、`page-title 28 px`;各 `demo-A-*.html` 写 `--line: #e5e7eb`(cool gray)、`page-title 22 px`——设计稿自己内部不一致
- ignore-rule 位置变了:demo-A-settings 把"忽略的 User-Agent"放在 settings 的"高级 / 危险区";v0.5.18 后迁到 stats 页(`stats-page.tsx` `IgnoreRules` 弹框)
- sidebar footer 链接:实际有 `/cairn-intro.html` + `/api/docs`(v0.7.19 swagger),设计稿没体现
- 各页侧栏分组(详见下表)

**侧栏分组对照表**:

| 页 | 原版设计稿侧栏 | 实际侧栏 |
|---|---|---|
| images | 仓库 / 操作(2 组) | 无(grep `sidebarGroups` 0 命中) |
| pull | 拉取源 / 平台 | 状态 / 来源(按 host 分桶) |
| stats | 时间窗 / 维度 / 过滤器(3 组) | 时间窗 7d/30d/90d(1 组) |
| credentials | 分组 / 最近使用 / 状态 | 主机 / 密码状态 |
| proxies | 协议 / 用途 / 状态 | 协议 / 状态 |
| settings | 设置分组(5 anchor)/ 运行时(端口、env)| 快速跳转(4 anchor:仓库连接 / 功能开关 / 拉取镜像的架构 / 热度记录)|
| sync | (无设计稿) | 方向(pull / push / 全部) |

> 原版 demo 里写的都是**写死的假 bucket**(library / registry-manager / webauto-push),实际是 v0.5.37.4 的 `onPublishGroups` 协议 —— 页面按真实数据分组后**上浮**给 App,App 下发给 PageSidebar。

## 修改记录

| 版本 | 作者 | 描述 |
|---|---|---|
| v1.0 | Mavis | 把 `docs/design/` 上一版冻结到本目录,作为 v0.7.37 刷新前的快照。共 10 个 html + 1 份本说明。 |
| v1.1 | Mavis | 追加字体策略一节,讲清 v0.7.37 设计稿为什么恢复 Google Fonts、demo 和实际 web 为什么不恢复,以及未来若要对齐的两个方向。 |

## 后续动作

刷新工作完成后,本目录**保留**,不进版本库列表也不删除。
如果哪天又要回看"0.5.36 当时的设计稿是什么样子",直接看本目录即可。