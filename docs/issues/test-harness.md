# 测试脚手架缺陷（Makefile / web-auto runner / 场景）

> **被测版本**：cairn `0.6.11`（`internal/version/version.go:18`，commit `f66bd4d`）
> **测试环境**：前端 E2E = `runner.local:8080` test-runner（`web-auto-runner:0.1.0`，Playwright 1.61.0）；被驱动的 cairn = `registry.local:10001`（容器 `cairn:0.6.11`）
> **范围**：本文件全部条目**不涉及 cairn 产品行为**，只针对「用 `make test-*` / web-auto runner 跑 cairn 回归」这条测试链路本身：Makefile 目标、runner 的超时与诊断能力、场景 YAML 的假设。
> **每条包含**：现象 / 根因 / 证据（源码 file:line + 实机实测）/ 修复方向。
> **修复状态**：**仅 TH-3 已在现场就地生效**（53 上 runner 的 dev `baseUrl` 已改），其余全部未修复。

## 前置：测试链路与资产归属

```
Mac(仓库)  --make test-frontend-->  53:8080 test-runner  --Playwright-->  158:10001 cairn
                                          |
                                     /scenarios/go-hub/*.yaml   (20 个)
```

| 资产 | 位置 | 是否进 cairn 仓库 |
| --- | --- | --- |
| Makefile 测试目标 | `Makefile:195-254` | ✅ 在库内 |
| runner 源码（`server.js` / `runner.js`） | 53 `/opt/web-auto-runner/test-runner/src/` | ❌ 不在库内 |
| 场景 YAML | 53 `/opt/web-auto-runner/web-auto/tests/go-hub/*.yaml`（容器内 `/scenarios/go-hub/`） | ❌ 不在库内 |
| runner 环境配置 | 53 `/opt/web-auto-runner/web-auto/config/web-auto.dev.yaml` | ❌ 不在库内 |
| 本机副本目录 | `tests/web-auto/` | 目录在库内但被 `.gitignore:52-53` 忽略，实测**仅剩 `.DS_Store`** |

两条使用事实（后续 debug 必知）：

- **场景名必须与 runner 的目录名完全同名**，只带 `go-hub/` 前缀（`server.js:81` 直接拼 `SCENARIOS_DIR/<scenario>.yaml`）；Makefile 里的 `cairn/` 前缀已过期，见 TH-1。
- 改场景 YAML / runner 配置**不需要重启 runner**（`server.js:81`、`loadEnvConfig` 每请求现读磁盘），修完立即重跑即可。

---

## TH-1 · Makefile 场景前缀 `cairn/` 与实际 `go-hub/` 不符（Medium）

**现象**

`make test-frontend`（14 个场景）与 `make test-frontend-fast` 发出去的 `scenario` 字段全部形如 `cairn/images-page`，runner 返回 `status=errored`、`duration=0`、`steps=[]`，报 `ENOENT ... '/scenarios/cairn/images-page.yaml'`。**整条前端回归用 make 跑必然全挂**，且 `errored` 与「用例真的跑挂」在汇总输出里不易区分。

**根因**

- `Makefile:210` 拼的是 `"cairn/$$sc"`（`:196-197` 注释同样写「53 上 web-auto runner 跑 cairn 全 14 场景」「场景名必须带 cairn/ 前缀」）。
- 53 上 runner 实际加载的目录是 `go-hub/`（历史原因：项目曾叫 `go-hub`，改产品名后 runner 侧目录没跟着改）。

**证据**

- 源码：`Makefile:196-197`（注释）、`Makefile:204-206` + `:210`（实参拼接）、`Makefile:217-222`（fast 目标同样前缀）。
- 实测：`POST /api/run {"scenario":"cairn/_smoke-all-pages","env":"dev","timeout":60000}` → runId `r-20260930165611-8122`、`status=errored`、`duration=0`、`steps=[]`、`diagnosis=null`、`error` 含 `ENOENT ... '/scenarios/cairn/_smoke-all-pages.yaml'`。
- 实测：`GET /api/scenarios` → `count: 23`，全部为 `go-hub/*`（20 个）+ `_adhoc/*`（3 个），**没有一个 `cairn/` 前缀**。

**修复方向**

1. Makefile 里 14 处 `cairn/$$sc` → `go-hub/$$sc`，并同步 `:196-197` 注释；或者
2. 在 53 上把 `/scenarios/go-hub` 改名为 `/scenarios/cairn`（二选一即可，但**必须同时只留一种口径**）。

顺带修一并列出的两个偏差（细节见 TH-4）：

- 实际场景共 **20 个**（`go-hub/*`），Makefile 只列了 14 个，漏 `login`、`registries-list`、`probe-gc-popover`、`probe-images-dom`、`probe-settings-edit-stats-tag`、`probe-settings-stats-dom` 六个。
- 建议在 `test-frontend` 里先打一次 `GET /api/scenarios` 做名字校验，避免以后再出现「前缀漂移 → 全挂」这种一次性报废整轮的失败模式。

---

## TH-2 · `RUNNER_BASE` 默认值是脱敏占位域名（Medium）

**现象**

直接 `make test-frontend` 而不显式传 `RUNNER_BASE`，请求全部打向 `http://proxy.example.com:8080`（不存在的域名），curl/python 直接连接失败——14 个场景一个都跑不到，报错形态是网络错误而不是用例失败。

**根因**

`Makefile:199` 的默认值是脱敏后的占位值：

```make
RUNNER_BASE ?= http://proxy.example.com:8080
```

与本仓库其他地方「内网地址已实际出现」的现状（`CHANGELOG.md`、`docs/sync-known-issues.md`、`internal/api/api_test.go`、`internal/db/migrate_test.go` 均含 `10.11.27.x`）不一致：**这份默认值脱了敏，但同一个命令行的其它部分没有**，结果是真实可用的默认值被牺牲、脱敏收益为零。

**证据**

- 源码：`Makefile:199`（`RUNNER_BASE`）、`Makefile:200`（`RUNNER_ENV ?= dev`）、`:210` / `:221`（`$$RUNNER_BASE/api/run` 调用点）。
- 实测：默认值域名 `proxy.example.com` 不可解析；改用 `RUNNER_BASE=http://runner.local:8080` 后链路正常（10 场景 passed）。

**修复方向**

把默认值改为内网真实地址 `http://runner.local:8080`（与仓库既有内网地址口径一致），或反过来：既然要脱敏就把 `CHANGELOG.md` 等处的地址一并处理——但**不要维持「半脱敏」现状**。若坚持占位域名，至少让 Makefile 在默认值未被覆盖时 fail-fast（如 `$(if $(filter proxy.example.com,$(RUNNER_BASE)),$(error set RUNNER_BASE))`），别让 14 个场景无提示全挂。

---

## TH-3 · runner dev `baseUrl` 曾指向 `:80`（Medium，已在现场修复）

**现象**

本轮开始前，53 上 runner 的 `web-auto.dev.yaml` 里 `baseUrl` 是 `http://registry.local:80`，而 cairn 实际发布在 `registry.local:10001`。用 dev 环境跑任何 `go-hub/*` 场景，页面根本打不开（`:80` 上没有 cairn），第一批场景集体失败。

**根因**

测试环境配置漂移，**不在 cairn 仓库内**——这份配置只存在 53 上，没有任何单向同步机制，cairn 侧部署端口变了之后 runner 侧不会跟着变。

**证据**

- 实测：修复前 `POST /api/run {"scenario":"go-hub/login","env":"dev",...}` 打开 `http://registry.local:80` 失败；`registry.local:10001` 上 `GET /v2/` → `200 {}`（cairn 只监听 10001）。
- 实测：把 53 上 `/opt/web-auto-runner/web-auto/config/web-auto.dev.yaml` 的 `baseUrl` 改为 `http://registry.local:10001` 后（备份为 `web-auto.dev.yaml.bak-0.6.11`，`loadEnvConfig` 每请求现读，无需重启），`go-hub/login` 等 10 个场景 passed。
- 说明：本轮**发现后即就地修复，未保留修复前的失败现场**（失败发生在修复动作之前的探索阶段，没有留下可引用的 runId）。

**修复方向**

- 53 侧：保留本次修改；后续把 `web-auto.dev.yaml` 也纳入版本管理（或至少在改动时留备份）。
- 通用防御：runner 在 `/api/run` 入口加一次 `baseUrl` 可达性前置（比如对 `/v2/` 发 `HEAD`），不可达时直接返回 `errored` + 明确诊断，而不是让每个场景各自 Playwright 超时。
- 长期：把 `tests/web-auto/`（已在 `.gitignore:52-53` 中预留）真正用于存放本机场景/配置副本，避免「唯一副本只存在于某台机器的容器里」。

---

## TH-4 · `test-frontend-fast` 注释称 9 个场景实际列 8 个，且含写路径（Low）

**现象**

`Makefile:216` 注释写「只跑 9 只读场景(~1 分钟)」，`:217-218` 实际只列了 8 个，且其中包含**写路径**场景 `pull-real`（会向 registry 推送/拉取镜像，不是只读）。

**根因**

注释与列表在后续增删场景时脱节；且「只读 / 写路径」的划分标准没有落进场景元数据（YAML 里的 `tags: [destructive, pull, go-hub]` 已有此信息，Makefile 没用）。

**证据**

- 源码：`Makefile:216`（注释「9 只读场景」）、`Makefile:217-218`（8 个场景名，含 `pull-real`）、`:222`（`timeout:120000`）。
- 实测：`pull-real` 场景在 batch3（破坏性批次）中单独跑过并 passed，其 `tags` 含 `destructive`；runner 侧场景清单 20 个里并没有「只读」这一维度。

**修复方向**

把注释放到场景清单旁边（改注释时一起数一遍），并让「只读/写」由 YAML `tags` 决定而非人工列表：`test-frontend-fast` 只取 `!destructive` 的场景。场景总数同时更新为实际值（见 TH-1）。

---

## TH-5 · `test-backend` 的期望与 dev 部署不符（Low）

**现象**

`make test-backend` 里写死了 `cairn:0.5.18-dev` 这一镜像 tag（`:239`），本仓库当前版本已是 `0.6.11`；同时 `docker tag` 的目标仓库名写死为 `webauto-push/accept-$(VERSION):v1`。任何非「0.5.18-dev 恰好存在本地」的机器上，这条链路第一步就挂。

**根因**

版本号硬编码在测试目标里，没有复用 `Makefile:19-37` 已有的变量块（`IMAGE`、`VERSION` 等）。

**证据**

- 源码：`Makefile:227-241`（`test-backend`），尤 `:239`（`cairn:0.5.18-dev`）、`:235`（`-u admin:$$(cat /root/.regpw 2>/dev/null || echo admin)`）、`:232`（expect 401）。
- 实测：本机 `cairn:0.6.11` 镜像存在，`cairn:0.5.18-dev` 不存在（`docker images` 无该 tag）；后端实测使用的是 `cairn:0.6.11` 与容器 `cairn`。

**修复方向**

`:239` 改用 `$(IMAGE)`（其 `?=` 默认值已随版本维护）；仓库路径 `webauto-push/accept-$(VERSION):v1` 用变量拼装并在注释里说明它是测试专用命名空间；`/root/.regpw` 这类宿主机专属路径建议在目标开头显式检查存在性并给出可读错误。

---

## TH-6 · 前端场景曾零覆盖 Sync 页面 → **本轮部分闭环**（Medium）

**现象（原始）**

首轮 20 个场景全部跑完，**没有任何一个场景打开/操作「同步」页面**。0.6.x 期间新增的 Sync 功能（UI + 后端同步作业）没有 UI 侧回归保护：改坏了不会有人发现。

**本轮进展（2026-09-30）**

补了 6 个 sync 场景（落盘 `/opt/web-auto-runner/web-auto/tests/go-hub/`），其中 5 个已实跑全绿：

| 场景 | runId | 结果 | 覆盖点 |
| --- | --- | --- | --- |
| `sync-open` | `r-20260930232233-c936` | passed 4536ms | 打开站点 → 切「镜像同步」页 → 断言页面就绪（被其余 4 个场景 `include` 复用） |
| `sync-page` | `r-20260930232838-c566` | passed 9783ms，23/23 步 | 「新建同步任务」表单（名称 / `include` / 自动同步）→ 列表行回显 → 行内编辑（改名 + 开自动同步）→ 回显 |
| `sync-page-schedule` | `r-20260930233238-fab4` | passed 7518ms，20/20 步 | 行内「定时」弹窗：新建 cron 规则（`*/2 * * * *`）→ 规则行回显 → 改 cron → 停用/启用 |
| `sync-page-delrule` | `r-20260930234427-b614` | passed 7039ms，13/13 步 | 删定时规则（`button:has-text("删")` → Popconfirm → primary），取证 toast 文案 + 表格陈旧行 |
| `sync-page-deltask` | `r-20260930234533-6896` | passed 6115ms，13/13 步 | 删任务双层确认：Popconfirm → CASCADE `modal.confirm`（断言含「外键 CASCADE」）→ 取证 toast |
| `sync-page-delete`（**已废弃**） | — | 17/21 步 failed | 早期「删除」合并版；因列表不刷新导致断言无法收敛，已拆成上面 delrule + deltask 两个场景 |

**剩余缺口（故仍记「部分闭环」，不标已闭环）**

- Sync 「历史 / 运行记录」面板（`/api/sync/{id}/runs`）的 UI 入口（本轮刻意不点「运行」「历史」按钮）；
- `include` 的**排除性负例 / 零匹配**过滤在 UI 侧的可见行为（仅 API 层测过，见 management-api 相关实测）；
- 同步失败面（源不可达、凭据错误）的 toast 与重试 UI；
- `_smoke-all-pages` 仍是标题级断言，不含 Sync 关键交互。

**证据**

- 实测（本轮）：上表 runId 全部取自 53 `POST /api/run` 的**同步返回**（含完整 `steps[].status/duration`，无需轮询 `GET /api/run/:id`）。
- 实测（原始）：`GET /api/scenarios` → 当时 20 个 `go-hub/*` 场景，名字与内容都不涉及 sync。
- 实测（本轮收尾复核）：`GET /api/scenarios` → `{"count":35}`，其中 **6 项是 macOS AppleDouble 噪声**（`go-hub/._sync-*.yaml`，BSD tar 打包上传的副产物，runner 未过滤）；真实 `go-hub/*` 26 个、`_adhoc/*` 3 个。
- 源码：`web/src/pages/sync-page.tsx`（0.6.x 新功能页面，被导航引用）。
- **顺带产出的产品缺陷**：本轮 UI 场景直接截获 **MA-5**（删除已生效却弹「删除失败」）与其副作用「列表不刷新」（场景内取证 `SPDELRULESTALE:rows=1`），详见 [management-api.md](./management-api.md)。

**修复方向**

按「剩余缺口」逐项补场景（历史面板 / `include` 负例 / 失败面）；清掉 `._sync-*.yaml` 噪声后把场景总数写进 Makefile（与 TH-1 同批改）。

---

## TH-7 · runner 超时/诊断一组缺口（Low）

**现象**

一个场景超时（超出 runner 总预算）时，得到的诊断信息**几乎无法定位**：不知道是哪一步卡的、为什么卡、当前页面长什么样。本轮 `proxies-page` 场景 `errored`（`step timeout after 21428.571428571428ms`），`steps[]` 只有 11/14 步、`diagnosis: null`、artifacts 全空，只能靠人工推断「大概卡在第 12 步点击『探测全部』」。

**根因（8 项缺口，均为 `runner.js` 实现层面）**

| # | 缺口 | 位置 | 后果 |
| --- | --- | --- | --- |
| 1 | 超时步不写入 `steps[]` | `runner.js:283`、`:331-361` | 看不到卡住的那一步 |
| 2 | 超时错误文本不含步名/步号 | `runner.js:285-289` | 只能数步数反推 |
| 3 | `diagnosis` 在超时路径为空 | `runner.js:310-312` | 没有结构化归因 |
| 4 | artifacts（截图/trace）在总超时路径不产出 | `runner.js:293-295` | 无现场可看 |
| 5 | **单步声明的 `timeout` 不参与单步预算** | `runner.js:274`（`stepTimeoutMs = timeout / max(steps,5)`） | 步级 `timeout` 是**装饰**，长耗时步骤必被总预算截断 |
| 6 | `text` 策略短路（`waitFor` 后直接 `text`，不重试） | `runner.js:92`、`:119-134` | `assert type:text` 可能静默假通过 |
| 7 | `step timeout` 诊断分支不可达（死代码） | `runner.js:351-356` | 该分支永远不会执行 |
| 8 | `assert type:text` 无等待重试 | `runner.js:127-132` | 文案稍晚渲染即误判失败 |

**证据**

- 源码：上表行号（`/opt/web-auto-runner/test-runner/src/runner.js`，53 上；`server.js:78-86` 为入口，`:81` 拼场景路径、`:83` 硬顶 300000）。
- 实测：`go-hub/proxies-page` → runId `r-20260930151528-9e04`，`status=errored`、`error: step timeout after 21428.571428571428ms`（= 300000/14 ≈ 21428.57，正是 `runner.js:274` 的公式，14 = 该场景步数）、`steps[]` 仅 11 项、`diagnosis: null`。
- 实测（缺口 5 的直接证据）：该场景第 12 步「点击『探测全部』」自身声明 `timeout: 8000`，但预算仍按 21428ms 计算——**声明未生效**。该步卡住的产品侧原因另有其解：`web/src/pages/proxies-page.tsx:740` 让按钮在无选中行时 `disabled`，Playwright 点击永久等不到可点状态（属场景设计问题，详见 TH-8）。

**修复方向**

按缺口逐项修补，优先级从高到低：④（超时也产出截图/trace）→ ①+②（把超时步写进 `steps[]` 并带步名）→ ③（补 `diagnosis`）→ ⑤（单步 `timeout` 取 `min(声明值, 剩余预算)` 或直接以声明值为主）。⑤ 修完后，长耗时场景不必靠提高全局 `timeout` 绕过（本轮为此把所有真实动作场景统一提到 300000）。

---

## TH-8 · 6 个场景的选择器/断言与实际 UI 或数据前置不符（Low）

**现象**

本轮 9 个 failed 场景中，有 6 个的失败根因是**场景自身**（选择器过期 / 断言文案过期 / 缺数据前置 / 超时机制），而产品侧行为经源码核对是正确或刻意如此的。逐个对照：

| 场景 | 失败步（实测） | 场景假设 | 实际 | 归属 |
| --- | --- | --- | --- | --- |
| `credentials-page` | 步 12「点击第一行的『测试连接』」5620ms，`table tbody tr.ant-table-row:first-child button` | 凭据列表至少有 1 行 | 实测 `GET /api/credentials` → `data: []`，表为空、无任何行 | 场景缺数据前置（直接根因）；`:first-child` 是次生隐患 |
| `probe-gc-popover` | 步 6「点击第一行『删除』按钮（如有数据）」5216ms，`button.ant-btn-dangerous` 零命中 | 存在可删除的 manifest 行 | 实测无任何 `button.ant-btn-dangerous` 行（无孤儿 blob / 无行） | 步名自带「（如有数据）」但实现是硬要求 |
| `probe-settings-edit-stats-tag` | 步 5「点击『按 tag』维度」8229ms，`.app-content .ant-segmented-item:has-text("按 tag")` | 统计页维度切换在 `.app-content` 内 | 实测控件在侧栏（`stats-page.tsx:60`），不在 `.app-content` 作用域 | 选择器作用域过期 |
| `pull-page` | 步 9「断言展开后『来源 registry 地址』出现」5255ms，`input[placeholder="自动推断"]` | 展开后该输入框 placeholder 为「自动推断」 | `pull-page.tsx:244-245` 当前实现与之不符（文案/占位已变更） | 断言文案过期 |
| `settings-page` | 步 6「DOM 取证：设置页核心按钮齐全」273ms，missing `重新扫描` | 设置页含「重新扫描」按钮 | 实测 have=`测试连接\|刷新清单\|编辑\|添加规则\|清空热度数据`；「刷新」与「重新扫描」已在 `images-page.tsx:106-107` 合并为一个按钮（刻意变更） | 断言清单过期（产品侧刻意变更） |
| `stats-page` | 步 13「切到『90 天』窗口」16220ms，`.page-actions .ant-segmented-item:has-text("90 天")` | 时间窗切换在页头 `.page-actions` | 实测时间窗在侧栏（`stats-page.tsx:60`）；页头 Segmented 已删除（`stats-page.tsx:69`） | 选择器作用域过期（同 `probe-…tag`） |

**根因（两类）**

1. **UI 刻意变更后场景未跟**：`settings-page`（「刷新/重新扫描」合并）、`stats-page` 与 `probe-settings-edit-stats-tag`（时间窗/维度移入侧栏）、`pull-page`（占位文案变更）——产品侧是有意调整，场景停在旧版 UI。
2. **场景缺数据前置**：`credentials-page`（空表时 `:first-child` 结构性耦合）、`probe-gc-popover`（空态下硬要求行存在）——场景没造前置数据，也没写空态分支。

**证据**

- 源码：`web/src/pages/images-page.tsx:106-107`（「刷新」=「重新扫描」合并）；`stats-page.tsx:60`（窗口在侧栏）、`:69`（页头 Segmented 已删除）、`:93`、`:177-179`；`pull-page.tsx:82-83`、`:244-245`；`proxies-page.tsx:740`（无选中行时按钮 disabled）。
- 实测：6 个场景的失败步与耗时见上表；`GET /api/credentials` → `{"data":[]}`；`proxies-page` 场景 `errored`（超时步 = 第 12 步，根因 disabled，机制见 TH-7）。

**修复方向**

- 把场景选择器从「作用域假设」（`.page-actions` / `.app-content`）改为语义定位（`has-text` 不加父级作用域），避免 UI 布局调整即失效。
- 断言清单类步骤（`settings-page` 步 6）改为「已知变更后的新清单」，或改成识别集合而非精确列表。
- 需要数据的场景（`credentials-page` / `probe-gc-popover`）先走「造数据 → 断言 → 清理」，并在空态时给出明确的 `skip` 语义而不是硬等超时。
- `:first-child` 这类结构性耦合一律替换为 `:has-text(...)`（本轮已确认它是次生隐患：即使有数据，行序变化也会误点）。

---

## TH-9 · `delete-repo-real` / `registries-list` 硬编码 fixture 且假设目标在第 1 页（Low）

**现象**

- `delete-repo-real` 步 4「等待 `webauto-single2` 行出现（前置数据闸门）」实测 12317ms 超时（`Timeout 12000ms`）：场景依赖一个**由上一次运行留下的**仓库 `webauto-single2`。
- `registries-list` 步 8「断言 `library/alpine` 可见」实测 10176ms 失败：`_catalog` 里仓库很多（实测 76 个），目标仓库不在当前页/当前筛选视图内。

**根因**

两个场景都把「环境里恰好存在某数据」当作前提，而没有自己制造数据或翻页查找：

- `delete-repo-real.yaml:16` 注释明说 `webauto-single2` 是该场景的**前置数据**、「由 `POST /api/pull/jobs` 现造」——但实现上依赖既有残留，不是每次现造。
- `registries-list` 的 `library/alpine` 假设与当前 catalog（76 个仓库，其中 65 个含 `/`）不符；目标可能不在默认分页第一页。

**证据**

- 源码：场景 `delete-repo-real.yaml:16`（前置说明）、`:85-88`（步 4 闸门）、`:104-105`（fallback `throw`）；`registries-list.yaml` 断言步（实测步 8）。
- 实测：`delete-repo-real` → runId `r-20260930152309-3782`，步 4 12317ms `Timeout 12000ms`；`registries-list` → runId `r-20260930151446-1c58`，步 8 10176ms；`_catalog` 实测 76 个仓库（清理临时夹具后）。
- 组件说明：`webauto-single2` 实测存在于 158（`tags/latest`，磁盘 3 manifest + 7 文件），本轮**保留未删**：它正是 `delete-repo-real` 下次重跑的前置数据；删除命令见报告「清理与残留」。

**修复方向**

- `delete-repo-real`：把「前置数据」从注释变成**步内动作**——场景开头用 API（`POST /api/pull/jobs`）现造一个 `webauto-*` 仓库，跑完再删；或至少在步 4 失败时输出「请先运行本场景的前置脚本」而不是让闸门超时。
- `registries-list`：断言前先搜索/翻页到目标仓库（cairn 有筛选能力），或改用「列表加载完成 + 任意行可见」这类与数据无关的断言；把 `library/alpine` 这种具体仓库改成从当前 catalog 取第一个。

---

## TH-10 · `gc-real` 第 2 轮 toast 断言竞态（Low）

**现象**

`gc-real` 场景 22 步全跑完，但第 22 步「断言第 2 轮 toast 形状（勾选轮，半角口径）」实测 516ms 失败：`actual = GC 完成:没有需要清理的孤儿 blob`（第 1 轮的**未勾选**文案），期望是勾选轮文案。

**根因**

场景的 evaluate 探针在采样时，第 1 轮 toast **还没过期**（3s TTL），而探针取的是 `picks[picks.length-1]`（最后一个 toast 元素）——此时它仍是第 1 轮那条。即：**第 2 轮动作正确、产品文案正确，采样时机抢在旧 toast 消失之前**。

决定性证据：紧邻的独立步「断言 checkbox 已勾选」**通过**——第 2 轮的勾选动作确实生效；`images-page.tsx:394-395` 两个分支（有孤儿/无孤儿）的文案实现都正确。因此这是**场景时序问题，不是产品缺陷**。

**证据**

- 源码：`gc-real.yaml:285-332`（notice 采集：取 `picks[picks.length-1]`）；产品侧 `web/src/pages/images-page.tsx:385-396`（`:394-395` 两分支文案均正确）。
- 实测：`gc-real` → runId `r-20260930152325-dbef`，步 22 失败 516ms，`actual = GC 完成:没有需要清理的孤儿 blob`；相邻步「断言 checkbox 已勾选」passed；toast TTL 3s。

**修复方向**

采样前先等旧 toast 消失（等待 `notice` 数量归零或文本不含「没有需要清理」），或按内容筛选而非按下标取最后一个；更稳的做法：第 2 轮断言同时校验「checkbox 勾选 + toast 文案在允许集合内」，避免对文案精确匹配。