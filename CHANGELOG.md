# Changelog

cairn 的所有显著变更记录于此。格式遵循 [Keep a Changelog](https://keepachangelog.com/)。

版本规则见 [AGENTS.md §版本号规则](./AGENTS.md#版本号规则)。

---

## [0.5.15] - 2026-09-26

本轮主题:**把「探测」降级为纯 TCP 连通性 + 延迟**,并**让「测试连接」也出现在编辑弹窗里**。原先的「探测」是拿代理服务器本身当 HTTP 目标发一次请求——对 `http://` 代理勉强可用,对 `socks5://` 代理或只做 CONNECT 转发的代理必然失败,于是出现「新增时测试通过、探测时却不可用」的矛盾。本轮把探测收敛成一句话:**这个 ip:端口从本机连得上吗、往返多慢**;至于「这代理能不能真的转发」,交给弹窗内与行内的「测试」按钮。

### 变更

- **`Store.Probe` 改为纯 TCP 建连**:`net.DialTimeout` 建连后立即关闭,不发请求、不握手、不校验协议,`http` / `https` / `socks5` 三种代理一视同仁,超时 `5s`。`internal/proxies/proxies.go` 里的 `net/http` 依赖随之移除。
- **端口缺省规则**:地址未显式带端口时按协议补默认端口——`http` → `80`、`https` → `443`、`socks5` → `1080`(新增 `dialAddr`)。`http://proxy.example.com` 这种写法不再被当成缺端口。
- **新增延迟字段 `lastProbeLatencyMs`**:探测往返耗时(毫秒,浮点)。`GET /api/proxies`、`POST /api/proxies/{id}/probe`、`POST /api/proxies/probe` 的响应都带上;**失败时省略**(`omitempty`),不用 `0` 冒充测量值。
- **代理页新增「延迟」列**(「状态」列与「最后探测」列之间):上次探测的建连延迟;未探测或探测失败显示 `—`。
- **「测试连接」按钮与「测试目标」输入框移入编辑弹窗**:该按钮自 v0.5.13 起只在新增态出现,本轮**编辑态同样可用**(这取代了 v0.5.13「编辑态隐藏该按钮」的决定)。`POST /api/proxies/test` 请求体新增可选 `id`。
- **编辑态试连的密码回退规则**:编辑表单不回显已存密码,空密码因此有歧义。现在的规则是——`id` 能解析出条目,**且**提交的用户名与已存条目一致时复用已存密码;用户名被清空或改动则按匿名 / 新凭据试连,使测试结果与用户眼前的表单保持一致。
- **页头「探测」Tooltip 与页底说明改为两段式**:明确「探测 = 纯 TCP 连通性」「测试 = 穿过代理去访问目标」是两件事。

### 修复

- **「新增时测试通过、探测时不可用」**:`<proxy>`(`http://proxy.example.com:4433`,只做 CONNECT 转发)新增时试连成功,探测却报 `context deadline exceeded`(错误里带 `Head http://proxy.example.com:4433`)。根因是探测把「代理服务器能否直接应答一个普通 HTTP 请求」当成了健康检查——那是对代理能力的额外要求,不是「ip:端口通不通」。改为纯 TCP 后该条目探测转为可用并带回延迟。
- **页底超时文案与实现不符**:写的是「超时上限 8 秒」,而实际执行测试的路径最长 **12 秒**(`client.Timeout` 10 秒 + 上下文 12 秒),文案改为 12 秒。

### 已知遗留(本轮未改)

- **`UpdateProxy` 用空密码会清掉已存密码**:`internal/api/handlers_extra.go` 的 `UpdateProxy` 无条件执行 `existing.Password = in.Password`,而前端编辑态在密码留空时**不发送** `password` 字段,于是编辑任何条目(哪怕只改备注)都会把已存密码清掉,与表单上「密码(留空保留原密码)」的文案矛盾。修复需要把 `proxyInput.Password` 改成指针或引入显式 `keepPassword`,即**变更已文档化的 PATCH 语义**,故本轮不擅自改,记录待定。
- 设置页 5 处 `TS6133` 未使用声明(`Descriptions`、`formatDateTime`、`inventory`、`savingRegistryUrl`、`setSavingRegistryUrl`)仍在基线里,未清理。

### 验证

- Go 侧:`gofmt -l internal cmd` 无输出;`go vet ./internal/...` 退出码 0;`go build ./...` 与 `go build -tags webui ./...` 退出码 0;`go test -count=1 ./internal/...` 全部 `ok`,含新增用例 `TestProbeSucceedsForPlainTCPListener` / `TestProbeReportsDialFailure` / `TestDialAddrDefaultsPort`。
- 前端:`./web/node_modules/.bin/tsc --noEmit -p web/tsconfig.json` 与基线逐条比对 **10 → 10,无新增**;`cd web && npm run build` 退出码 0(`✓ 3040 modules transformed`),产物 `internal/webui/dist/assets/index-ZcXUjQDW.js`(约 1.20 MB,gzip 约 377 kB)。
- **构建产物断言**:`assets/` 目录只含新哈希,`index.html` 引用新哈希;产物内 `lastProbeLatencyMs`、「是两件事」、「测试连接」三项 `grep -c` 均 > 0。
- **隔离实例冒烟**(独立端口 `8899` + 独立数据目录):`/healthz` 返回 `status:ok`;`/api/config` 的 version 为 `0.5.15`;**指向在听的端口** `http://127.0.0.1:8899` 探测 → `ok:true` 且 `latencyMs:0.701`;**指向未监听的端口** `http://127.0.0.1:8901` 探测 → `ok:false` 且原因为 `connection refused`;`GET /api/proxies` 中可用条目带 `lastProbeLatencyMs`、失败条目**不带该键**;带 `id` 调 `POST /api/proxies/test` 仍正常返回(编辑态试连路径可用);退出时日志出现 `shutdown signal received, draining` 与 `http server stopped`。

### 兼容性

- **API 只增不改**:`lastProbeLatencyMs` / `latencyMs` 是新增响应字段,旧客户端忽略即可;`POST /api/proxies/test` 的 `id` 是可选字段,不传时行为与 v0.5.13 完全一致。
- **磁盘格式未变**:`proxies.json` 结构未改(延迟随条目一起序列化,旧文件读入后该字段为零值,不影响加载),升级不需迁移。
- ⚠️ **探测语义变更须知**:「探测」不再回答「这代理能不能转发」,只回答「ip:端口连不连得上」。要判断代理是否真能工作,请用行内「测试」按钮或 `POST /api/proxies/{id}/test`(可带 `targetUrl`)。相应地,升级后**首次探测结果可能与升级前不同**——原先因协议不匹配而判失败的条目,现在可能转为可用。

### 轮次与号位

- 本轮占 **0.5.15**:探测口径修正属**缺陷修复**,弹窗内「测试连接」与「延迟」列属既有功能优化,按 `AGENTS.md` 判定取小版本(第 3 位)+1。
- `docs/ROADMAP.md` 已顺延:韧性轮 0.5.15 → **0.5.16**、工程化 0.5.16 → **0.5.17**;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。**历史小节里「留给韧性轮」的提法以 `docs/ROADMAP.md` 号位总览为准,现在指的是 0.5.16。**

## [0.5.14] - 2026-09-26

本轮主题:**设置页只留一个「编辑」按钮**。设置页原本有**两套编辑入口**——面板顶部一个「编辑」(点开所有字段一起改),以及 6 个字段各自带的「✏️ 编辑」(点开单个字段改)。两个入口做的是同一件事,反而让人拿不准该点哪个;6 个字段就是 6 个按钮,视觉噪音也大。本轮把字段级按钮全部删掉,只留顶部那一个:**一按即全字段可编辑,改完一次保存**。

### 变更

- **删除设置页 6 处字段级「✏️ 编辑」按钮**:仓库地址、展示名称、允许删除、允许拉取、热度保留天数、拉取平台白名单。这 6 个字段原先各带一个按钮,现在统一由面板顶部的全局「编辑」接管。
- **只读态在全局「编辑」按钮旁新增引导文案**:「点「编辑」后下面所有参数一起改,改完一次保存」。按钮没了之后,得让人知道该去哪改。
- **`ReadonlyValue` 组件去掉 `onEdit` 入参**:该组件退化为纯展示(只渲染当前值的灰底方块),不再携带任何编辑控件;顺带删掉组件内条件渲染按钮的整块死代码。

### 修复

- **字段级按钮调用的 `beginEdit` 签名不匹配**:`beginEdit` 自 v0.5.9「整页编辑」改造后已是**零参**(`const beginEdit = () => setEditing(true)`),但 6 个字段级调用点仍在传参,编译期即报 `error TS2554: Expected 0 arguments, but got 1`。本轮连同按钮一起删除,该错误不再存在。

### 验证

- `cd web && ./node_modules/.bin/tsc --noEmit` 与改前基线逐条比对(按「文件 + 消息」做多重集相减):**16 → 10**,消失的 6 条全部是 `web/src/pages/settings-page.tsx` 的 `TS2554`,**无新增**。剩余 10 条为既有基线(`api.ts` 缺 3 个类型名、`images-page.tsx` 2 条、`settings-page.tsx` 5 条 `TS6133` 未使用声明),非本轮引入。
- `npm run build` 退出码 0(`✓ 3040 modules transformed`)。
- **构建产物断言**(`internal/webui/dist/assets/index-BMDcO2Rx.js`):`✏️` 出现 **0 次**、`✏` 出现 **0 次**、引导文案「点「编辑」后下面所有参数一起改」出现 **1 次**、「保存所有修改」出现 **1 次**、`onEdit` 出现 **0 次**;源码 `web/src/pages/settings-page.tsx` 同样为 `✏️` 0 / `onEdit` 0 / `beginEdit` 2(声明与全局按钮各一处)。
- 改动全部落在 `web/src/pages/settings-page.tsx`,**15 insertions / 29 deletions**;Go 侧零改动。

### 兼容性

- **纯前端 UI 改动**:无新增或变更的 API,无磁盘格式变化,升级不需迁移。
- 编辑能力**没有减少**:原先用字段级按钮能改的 6 个字段,现在都能在全局编辑态里改。校验规则(仓库地址须带 `http(s)://`、热度保留天数 ≥ 1)与保存逻辑(成功 `已保存 N 项设置`、无变更 `没有变更`)均未改。

### 轮次与号位

- 本轮占 **0.5.14**:删冗余按钮、统一编辑入口属**既有功能优化**,按 `AGENTS.md` 判定为小版本(第 3 位)+1。
- `docs/ROADMAP.md` 已顺延:韧性轮 0.5.14 → **0.5.15**、工程化 0.5.15 → **0.5.16**;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。**历史小节里「留给韧性轮」的提法(B6 `events.go` 的 `UnixNano`、B4 `proxies` 缺 `writeMu`)以 `docs/ROADMAP.md` 号位总览为准,现在指的是 0.5.15。**

## [0.5.13] - 2026-09-26

本轮主题:**新增代理时先试连、再保存**。原来的流程是:填完地址 / 用户名 / 密码 → 保存 → 再用行上的「探测」验证配得对不对;配错了要回到编辑态改、再等一次探测。本轮把「验证」提前到弹窗内部——表单填完、点「保存」之前,就能用**当前键入的值**测一次连通性,失败就地改,不必先落一条错配置进库。

### 新增

- **「新增代理」弹窗内新增「测试连接」按钮**:不必先保存,直接用表单当前的地址 / 用户名 / 密码 / 测试目标发起一次真实探测,结果内联显示在弹窗里(成功给 `HTTP <status>` 与 `registryApiVersion`,失败给原因)。对应新端点 **`POST /api/proxies/test`**。
- **端点 `POST /api/proxies/test`**:请求体 `{url, username?, password?, targetUrl?}`。**不落库、不写 `proxies.json`、不记探测状态**——它只回答「这组值现在能不能通」,与库里条目无关,因此既不接 `id`,也不走「代理库不可用」那道门禁。`url` 缺失 → 400 `url is required`;地址非法或连不通 → **恒 200**,结果由 `ok` 字段表达(与既有 `POST /api/proxies/{id}/test` 的错误语义保持一致)。
- **弹窗内新增「测试目标(可选,不保存)」输入**:默认目标是本仓库自己的 `/v2/`,而一个只被允许出外网的代理拿本仓库当目标会测出假阴性,所以允许临时覆盖。该字段**只作用于本次测试**,不进创建请求、不落库。
- 抽出共享实现 `proxyURLFrom` / `proxyTestTarget` / `proxyTestThrough`;`TestProxy`(按 id 测)与新端点走同一段代码,不再各写一遍。

### 修复

- **地址漏写协议头时报错泄露实现细节**:填 `127.0.0.1:8080` 这类裸 `host:port` 时,标准库 `url.Parse` 会先于本项目的校验失败,把 Go 原始错误 `first path segment in URL cannot contain colon` 直接抛到界面上。现在在 `url.Parse` **之前**先判有没有 `://`,直接给「代理地址需要带协议头(如 `http://` / `socks5://`)」这类可操作的中文提示。**该问题由本轮隔离冒烟实测发现**,单元测试与既有入口都覆盖不到。

### 变更

- **弹窗底部改为自定义 footer,并补 `saving` 态**:插入「测试连接」后按钮区不再用 `Modal` 的默认 footer。自定义 footer 会一并丢掉默认的「确定按钮自动 loading + 提交中防重复提交」,故补上:提交期间主按钮转圈、取消与「测试连接」禁用;`handleSubmit` 入口加锁、`finally` 解锁——避免连点建出两条代理。
- **「测试连接」只在新增态出现**:编辑态隐藏该按钮。原因是编辑时密码不回显,用空密码去测会得到 407 之类的假阴性;编辑已有条目要测连通性,行上本来就有「测试」按钮。
- 弹窗内一对弯引号 “能不能出外网” 改为本文件既有习惯的 「能不能出外网」。

### 验证

- `gofmt -l internal cmd` 无输出;`go vet ./internal/...` 退出码 0;`go build ./...` 退出码 0;`go test -count=1 ./internal/...` 全部 `ok`。
- `cd web && ./node_modules/.bin/tsc --noEmit` 与基线**逐条比对**(按「文件 + 消息」做多重集相减):**16 → 16,无新增、无消失**。`npm run build` 退出码 0,产物内含 `/api/proxies/test` 与「测试连接」文案。
- **隔离实例冒烟**(独立端口 + 独立数据目录 + 内联假代理)12 项全部符合预期:正常路径 `ok:true` 且回显 `registryApiVersion:"2"`;`targetUrl` 缺失 / 空串等价,均回落默认 `/v2/`;显式 `targetUrl` 被回显且假代理确实收到该绝对 URL;凭据以 `Proxy-Authorization: Basic ...` 发出;假代理强制 407 → `ok:false`;端口未监听 → 连接被拒原因可见;缺 `url` → 400 `url is required`。
- **零落库已实证**:试连全程前后,数据目录里的 `proxies.json` **始终不存在**;随后用 `POST /api/proxies` 真建一条,该文件才出现(1 条),再按 id 试连仍 `ok:true`、`GET /api/proxies` 仍为 1 条——证明试连路径不产生任何条目。

### 兼容性

- **磁盘格式未变**:凭据库 / 代理库 / 热度库结构均未改动,升级不需迁移。
- 新增的是**端点**,不是数据结构:`POST /api/proxies/test` 为新增;既有 `POST /api/proxies/{id}/test` 的请求体、响应与错误语义均不变(仅内部改走共享函数)。
- 弹窗内的「测试目标」是**可选输入**,不参与创建请求,也不落库。

### 轮次与号位

- 本轮占 **0.5.13**:「保存前试连」是用户可感知的新能力(新按钮 + 新端点),按 `AGENTS.md` 的性质判定属中版本档;号位沿用 0.5.12 先例——真正的中版本号位 `0.6.0` 已由人指定留给 TLS,不占用(详见 `docs/ROADMAP.md`)。
- 原 0.5.13「韧性轮」顺延为 **0.5.14**,原 0.5.14「工程化」顺延为 **0.5.15**;`0.6.0` TLS 不随顺延改号。**因此 0.5.11 / 0.5.12 小节里"留给韧性轮"的两条(B6 `events.go` 的 `UnixNano`、B4 `proxies` 缺 `writeMu`)现在指的是 0.5.14。**
- 这是连续第四轮顺延,号位表见 [`docs/ROADMAP.md`](./docs/ROADMAP.md)。

## [0.5.12] - 2026-09-26

本轮主题:**代理交互——把「探测」从看不见的后台动作变成看得见的前台操作**。起因是 158 上点「探测」按钮没有任何反应:根因是 `web/src/pages/proxies-page.tsx` 调用了 `probeProxy()` 却**从未 import**(由 `f62a3e1` 引入),而 `"build": "vite build"` 不含 `tsc`,所以前端构建一直"成功"。顺带把同时提出的三条交互诉求一起做掉:删除只留一层确认、探测有进行中反馈、新增代理后立即探测,以及一个「一键探测全部」的入口。

### 修复

- **探测按钮点了没反应(本轮的真 bug)**:`web/src/pages/proxies-page.tsx` 补上 `probeProxy` 的 import。该符号被调用却从未导入,点击时抛 `ReferenceError`,被 React 事件处理吞掉,表现为"按钮能点、什么都不发生"。同一处还修掉了 `handleProbe` 的缩进错位。
- **删除代理的确认从两层降为一层**:去掉按钮外层的 `Popconfirm`(标题只有"确定删除?"),保留 `modal.confirm`——后者承载真正需要用户判断的信息("不会影响已完成的拉取,但引用它的任务会立即失败")。确认次数 2 → 1,影响面说明不变。

### 新增

- **探测进行中反馈**:逐行抽成共享的 `probeOne(id, name)`,带 `catch`(任何抛出转成可见的 `message.error`)和 `finally`(先清行内标志再 `refresh()`),不留陈旧状态。`probingIds[p.id]` 为真时状态列渲染 `<Tag color="processing">探测中</Tag>`,行内按钮文案变"探测中…"(操作列宽 230 → 250);批量探测期间头部按钮显示 `loading`,整个表格可见地处于"探测中"。
- **新增代理后立即探测连通性**:`handleSubmit` 保存成功后拿到新建条目,提示语改为"已创建代理,正在探测连通性…",随后复用同一个 `probeOne`。**刻意不做在 `CreateProxy` 里同步探测**——`Probe` 最坏情况要 5 秒(SOCKS5 拨号预算),会让"保存"按钮卡住;前端探测让弹窗立刻关闭而反馈照旧。
- **一键探测全部**:代理页头部新增「探测全部」按钮(`ThunderboltOutlined`,`proxies.length === 0` 时禁用),对应新端点 **`POST /api/proxies/probe`**。**放服务端而不是前端 for 循环**的原因:前端串行最坏 N×5 秒,服务端 `ProbeAll` 并行,最坏约 5 秒。响应恒为 200 + 摘要 `{total, ok, failed, unknown, results:[{id, name, ok, status, probedAt, error}]}`;`unknown` 用于"探测途中条目被删除",而不是把未报告谎报成失败。`ok === total` 走 `message.success`,否则 `message.warning` 并列出可用/不可用计数。

### 验证

- `gofmt -l internal cmd` 无输出;`go vet ./internal/...` 退出码 0;`go build ./...` 退出码 0;`go test -count=1 ./internal/...` 全部 `ok`。
- `cd web && ./node_modules/.bin/tsc --noEmit`:**`proxies-page.tsx` 的 TS2304(`probeProxy`)归零**,`api.ts` / `types.ts` 无新增错误。全量错误数 **17 → 16**:基线(HEAD)的 `proxies-page.tsx` 本来就带这条 TS2304(已实测:把 HEAD 版文件放回 `web/src` 跑一次 `tsc`,该文件 1 条错误),修完后全量 16 条,均为既有错误,不在本轮范围。
- 路由无歧义:`POST /api/proxies/probe`(2 段)与 `POST /api/proxies/{id}/probe`(3 段)段数不同;id 形如 `YYYYMMDD-HHMMSS-mmm-<8hex>`,不会与字面量 `probe` 冲突。

### 兼容性

- **磁盘格式未变**:凭据库 / 代理库 / 热度库结构均未改动,升级不需迁移。
- 新增的是**端点**,不是数据结构:`POST /api/proxies/probe` 为新增,既有 `POST /api/proxies/{id}/probe` 语义与响应不变。
- 每分钟后台探测**自 v0.5.9 起已存在**(`internal/server/server.go` 里 `StartProbeLoop(r.PullCtx, 60*time.Second)`),不是本轮新增——用户问的"增加后后续每分钟都定时探测"早已生效。

### 轮次与号位

- 本轮占 **0.5.12**:「一键探测全部」是用户可感知的新能力(新按钮 + 新端点),按 `AGENTS.md` 取中版本档。
- 原 0.5.12「韧性轮」顺延为 **0.5.13**,原 0.5.13「工程化」顺延为 **0.5.14**;`0.6.0` TLS 由人指定,不随顺延改号。**因此 0.5.11 小节末尾"留给 0.5.12 韧性轮"的两条(B6 `events.go` 的 `UnixNano`、B4 `proxies` 缺 `writeMu`)现在指的是 0.5.13。**
- 这是连续第三轮顺延,号位表见 [`docs/ROADMAP.md`](./docs/ROADMAP.md)。

## [0.5.11] - 2026-09-26

本轮主题:**hotfix — 收口 0.5.10 没修完的 ID 唯一性**。0.5.10 把 `randHex()` 的熵源从 `time.Now().UnixNano()` 换成了 `crypto/rand`，但随机后缀仍只有 **4 个 hex 字符(16 bit)**。400 个并发调用落在同一毫秒时，16 bit 的期望撞车数约 **1.2 次**——测试连跑 20 次有 **13 次 FAIL**。`newID()` 产出的是凭据库 / 代理库的**存储主键**，撞车等于静默覆盖(丢数据)，所以本轮把后缀加宽到 **8 个 hex 字符(32 bit)**，同场景期望撞车降到 **≈2e-5**。除此之外均为收尾。

### 修复

- **`internal/api/handlers_extra.go` `newID()`**：随机后缀 `randHex(4)` → `randHex(8)`。id 形状从 `20260926-111545-103-a83a`（秒级时间戳 + 毫秒 + 4 位随机）变为 `20260926-111545-103-a83ac1f7`（毫秒后跟 **8 位**随机）。这是本轮唯一的行为变更。
- **`internal/api/id_internal_test.go`**：同步加宽形状断言（`+1+8`、`len(parts[3]) != 8`），并把 `TestNewIDUniqueUnderConcurrency` 的注释补上 16 bit / 32 bit 撞车率对照。

### 验证

- `go test -count=20 -run TestNewIDUniqueUnderConcurrency -v ./internal/api/`：改前(16 bit) **13/20 FAIL** → 改后(32 bit) **20/20 PASS**。
- `go test -race -count=1 ./internal/...` 全绿；`go vet ./internal/...` 干净；`gofmt -l internal/api` 干净。

### 兼容性

- **磁盘格式没动**：凭据库 / 代理库按 JSON object 的 key 存 id，4 位后缀的旧 id 与 8 位后缀的新 id 可以共存，升级不需要迁移。
- 只跑过 0.5.10 的实例直接升 0.5.11；还停在 0.5.9 及更早的实例建议一步跳到 0.5.11（0.5.10 的凭据库死锁修复也包含在内）。

### 文档 / 收尾

- 代码注释里 **8 处**把「代理连通性监测」误标成 `v0.5.10` 的地方统一改回 `v0.5.9`（该功能随 0.5.9 发布），兑现 0.5.10「已知遗留」里「留待统一清账」的承诺。
- `.gitignore` 第 36 行的通配符粘连（`*.swp.tmp_nginx_header.conf`）修成 `*.swp`。
- 新增 [`docs/ROADMAP.md`](./docs/ROADMAP.md)：排定后续号位。原 0.5.11「韧性轮」顺延为 **0.5.12**、原 0.5.12「工程化」顺延为 **0.5.13**；`0.6.0` TLS 由人指定，不随顺延改号。
- 0.5.10「已知遗留」里另外两条仍开着，留给 0.5.12 韧性轮：`internal/events/events.go` 的 `UnixNano` 事件 ID（B6）、`internal/proxies` 缺 `writeMu`（B4）。

## [0.5.10] - 2026-09-26

本轮主题:**hotfix — 修生产环境凭据库死锁,以及 ID 撞车导致的静默丢数据**。158 上「UI 点不动、新建凭据后整站卡死」的根因**不在前端**:凭据库 `Put` 在持有写锁的情况下做 AES 加密 + 磁盘落盘,返回时没有释放写锁,此后任何 `List` / `Get` 永久阻塞。前端所有请求都没有超时,于是每个按钮都卡在 loading —— 看起来像「前端卡死」,实际是后端把锁漏了。

### 修复

- **`internal/credentials/credentials.go`**:`Put` / `Delete` 泄漏 `sync.RWMutex` 写锁。改成「锁内取快照 → 锁外写盘」(`snapshotLocked()` + `persistToDisk()`),与 `internal/proxies/proxies.go` 已有的正确范式对齐。新增 `writeMu` 串行化落盘(锁序恒为 `writeMu` → `mu`),顺带消除并发写乱序造成的「重启后凭空少几条」。**这是用户报告的生产 bug 的根因。**
- **`internal/api/handlers_extra.go` `randHex()`**:原实现用 `hex[time.Now().UnixNano()%16]` 逐字符拼接,每个字符还 `time.Sleep(1µs)`,并发下后缀高度相关。实测 20 个并发创建只产生 **19 个不同 id**,重复 id 在写入时按 id 覆盖 → **静默丢数据**(凭据/代理凭空消失)。改用 `crypto/rand`;熵源不可用时降级到时序熵而不 panic。凭据库与代理库两处 `newID()` 都受益。
- **`internal/pull/queue.go` `jobCounter`**:原来 `jobCounter++` 在 `e.mu.Lock()` **之前**执行,属数据竞争。改成 `atomic.Uint64` + `Add(1)`。

### 测试

- 新增 `internal/credentials/credentials_test.go`(8 个用例):含 500ms 超时型的死锁回归测试、`-race` 并发写压测、磁盘格式往返(既有凭据文件仍可解密)。
- 新增 `internal/api/id_internal_test.go`:400 个 goroutine 齐发断言 id 唯一;另测 `randHex` 形状与分布。
- `go test -race -count=1 ./internal/...` 全绿;`go vet ./...` 干净;`gofmt -l .` 干净。

### 验证(A/B 对照)

同一台机器、同一份 E2E 脚本,只换二进制:

- 旧二进制(sha256 `a52849ff…`):`returned 20 ids, 19 unique -> ID COLLISION`、凭据库 20 条(应为 21)、**FAILURES=2**。
- 新二进制(sha256 `75d33637…`):20/20 唯一、21 条、重启后仍 21 条、**FAILURES=0**。

### 影响 / 升级

- **磁盘格式没动**:凭据库仍是 JSON object + `{v,nonce,ct}` AES-256-GCM 信封,既有文件可继续解密,升级不需要迁移。
- **升级后立即恢复**:换 0.5.10 镜像后,UI 新建 / 编辑凭据不再卡死。
- **旧版本里已经因 id 撞车被覆盖掉的凭据找不回来**,需要重新录入。

### 已知遗留(下一轮)

- `internal/events/events.go:587` 的 `local-%d` 仍只取 `UnixNano`,同型理论重复,未纳入本次 hotfix。
- `internal/proxies/proxies.go` 缺同型 `writeMu` 加固(锁泄漏已修,写乱序尚未)。
- 代码注释里若干处把「代理连通性监测」标成 `v0.5.10`,该功能实际随 0.5.9 发布;本轮不改,留待统一清账。
- TLS 全套顺延到下一版。

---

## [0.5.9] - 2026-09-26

本轮主题:**配置单源化 — UI 唯一入口;env 只剩 5 个基础设施**。v0.5.8 之前,设置页上每个字段都带一个紫色「环境变量」标签,说明当前值是 `.env` 来的还是 UI 改的;Mutable > env 的双重优先级让运维首部署时被「我改了 UI 但 .env 里还有同一个值,到底用哪个?」反复困扰。本轮把业务配置全部砍成 UI 唯一来源,env 只剩 5 个基础设施变量(PORT / DATA_DIR / STORAGE_DIR / CREDENTIAL_KEY / GO_HUB_ENV)。

本轮同步文案重命名:「默认上游地址」→「仓库地址(/前缀)」。「上游」一词让运维以为配的是上游镜像源,实际这个字段配的就是 cairn 自己对外暴露的地址(docker login / docker push 连的就是它)。

### 新增
- **代理连通性自动监测**(v0.5.9 hotfix):每个代理条目后台每 60 秒探测一次可达性,
  状态写入 `Proxy.LastProbeAt` / `LastProbeStatus` (`ok` / `failed` / `unknown`)/ `LastProbeError`。
  探测目标:**proxy endpoint 本身**(TCP dial + 短 HEAD),不依赖 docker.io 是否可达。
  启动时 ProbeAll 一次 + 后台 ticker。代理管理页加「状态」「最后探测」两列 +
  每行「探测」按钮(立即触发 POST /api/proxies/{id}/probe);拉取任务表单的下拉里
  不可达代理加 `[不可用]` 标签 + disabled(仍可选,带 Tooltip 提示)。
- **`internal/config.Config` 13 个无 env fallback 的 read helper**:之前 11 个 `EffectiveXxx()`
  函数每个都做「Mutable override > env value > hardcoded default」三段判断,
  现在 env 那段整个砍掉,逻辑简化为「Mutable override 或 hardcoded default」。

### 变更
- **`internal/config.Load()`**:不再读取 `REGISTRY_URL` / `REGISTRY_PROXY` / `REGISTRY_USERNAME` /
  `REGISTRY_PASSWORD` / `REGISTRY_NAME` / `REGISTRY_NOTIFY_TOKEN` / `REGISTRY_ALLOW_*` /
  `REGISTRY_PULL_PLATFORMS` / `REGISTRY_PULL_HISTORY_RETENTION_DAYS` /
  `REGISTRY_STATS_RETENTION_DAYS` / `REGISTRY_STATS_IGNORE_USERAGENTS` 等业务 env。
  设了也不再读,会被 panel 值覆盖。
- **`internal/config.Config` struct 砍 9 个字段**:`RegistryURL` / `RegistryProxy` /
  `RegistryUsername` / `RegistryPassword` / `RegistryName` / `AllowDelete` / `AllowPull` /
  `NotifyToken` / `AllowRegistryEvents` / `StatsRetentionDay` /
  `StatsIgnoreUserAgents` / `PullPlatforms` / `PullHistoryRetentionDay` /
  `PullQueueSize` / `CacheTTL`(死的)/ `StatsAggregationInterval`(死的)全部移除。
- **`cmd/server/main.go` slog.Info**:启动日志只打 `port` / `env` / `credentials_dir` /
  `storage_dir`,不再打印业务字段。
- **`internal/api/handlers.go` MutableSettings 砍 11 个 `*Source` 字段** + 删 `src()` dead helper。
- **`internal/api/handlers_extra.go` ignoreRules.env 渲染分支移除**。
- **`internal/pull/executor.go`**: `pull.NewExecutor` 第一参直接传 `50` 常量,
  `PullQueueSize` 既不是 env 也不是 panel-tunable 的。
- **`web/src/pages/settings-page.tsx`**:
  - 「默认上游地址」→「仓库地址(/前缀)」语义重整
  - 7 个 Form.Item 全部改成「灰显 + ✏️ 编辑 → 点开改 → 保存」模式
  - 删 `SourceTag` 组件 + 7 处 inline 标签 + 「保存全部设置」按钮
- **`web/src/types.ts` MutableSettings 删 9 个 `*Source` 字段**。
- **`web/src/pages/proxies-page.tsx`** 加「状态」「最后探测」列 + 「探测」按钮 +
  `handleProbe` + `probingIds` state。
- **`web/src/pages/pull-page.tsx`** 代理下拉每项加 `[可用]/[不可用]` 标签,
  不可达加 `disabled`(仍可在 advanced 选项里选)。

### 修复
- 顺手 `gofmt -w` 了 0.6.0 commit 残留的 handlers.go 缩进 + executor_test.go 末换行。

### 影响 / 升级
- **数据无破坏**:SQLite schema 没动,settings 表所有键/字段/默认值不变。
- 之前 UI 改过的所有 `Mutable` override 一行一行都还在工作。
- 升级前 `.env` 里如果有 `REGISTRY_URL/PROXY/NAME/NOTIFY_TOKEN` 等,升级后会被忽略,
  行为以 UI 为准(默认值跟 v0.5.8 的 env 行为一致)。
- **代理连通性监测**:158 这类断网环境现在 UI 上立刻能看到哪条代理挂了,
  不会再因为拉镜像 timeout 才知道。
- `go test ./...` 全绿;`gofmt -l .` 干净。

### 文档
- `.env.example` 从 27 个 env 砍到 5 + 构建期。设了也没用的旧业务 env
  会被忽略,容器 restart 后行为不变。
- `README.md` 当前状态 / 版本号 → 0.5.9。
- `CHANGELOG.md` 新增本节。
- `AGENTS.md` env-rule 新增门槛(新加业务 env 必须先回答「为什么不能走 UI?」)。

### 留到下一版
- TLS 全套:DB `tls_certificates` 表 + 同端口根据 mode 切换 +
  上传证书 + 生成自签名 + 热加载。
  (原写「留到 v0.5.10」,但 v0.5.10 已先作为死锁 hotfix 发版,故顺延。)

---

## [0.5.8] - 2026-09-26

本轮主题：**热度开箱即用**。v0.5.7 之前，热度统计依赖「事件共享密钥 + 外部 registry 的 notifications webhook」——自带 registry 的一次 push/pull 不会进入热度表，运维要么搭一套 Distribution 自己接 webhook，要么看不到数据。本轮把热度链路从「可选外部 webhook」改成「自带 registry 就地喂事件 + 外部 registry webhook 仍可选」两轨并行：默认就有数据，外接依然能接。

> **本节同时收录「拉取平台白名单」条目**（原独立成 `[0.6.0]` 章节）。该功能在提交 `8836c92`（2026-09-26 00:48）里被标为 `v0.6.0`，但紧接着的下一笔提交 `6aba42b` 就把热度功能标成了 `v0.5.8` —— 版本号自此**下调**回 0.5.x 线，**`v0.6.0` 从未作为发布版本存在，仓库里也没有对应 tag**（`git tag -l` 为空）。为避免 CHANGELOG 出现「0.6.0 排在 0.5.8 之前」的乱序，原 `[0.6.0]` 章节已并入本节；其中随 0.5.9「配置单源化」被移除或更名的细节，已在条目内就地标注。
>
> 动机：v0.5.x 之前，拉一个多架构 index（比如 `nginx:alpine`、`clickhouse/server`、`alpine`）会把上游全部 ~10 个平台的子 manifest 都拉下来——单架构 / 双架构部署因此吃下大量用不到的层。

### 新增

- **`internal/events.Handler.IngestLocal(ev)`**：内置 registry 在 `/v2/<repo>/manifests/<ref>` HEAD/PUT 成功后构造一个 `Event`，通过这个方法把热度直接喂进与 webhook 完全相同的 `processOne` 管线。复用 `ShouldCount` 过滤（白名单 manifest 媒体类型、HEAD/PUT 才计数、UA 忽略规则、self-fold 语义）—— 一处过滤规则，两个入口。返回 `bool`：true = 已计入（accepted+1 + SQLite + 最近事件环），false = 被过滤或 kill switch 关闭。
- **服务端：`eventsHandler` 现在只看 SQLite 是否就绪就构造**，不再要求 `REGISTRY_NOTIFY_TOKEN` 非空。`/api/events` 在 token 空时仍然挂载（fail-closed 401），行为与 v0.5.4 一致；自带的 `/v2/*` 不再走 webhook 绕一圈。
- **`internal/events/internal/events/local_test.go`（新文件，7 个测试）**：`CountsPull` / `RespectsKillSwitch` / `NilHandler`（nil `*Handler` 不 panic）/ `IgnoreRuleFolds` / `SelfPushCounted`（自写 PUT 仍计）/ `WrongMethodRejected`（GET 不计）/ `NewHandler_EmptyIgnore` 构造健壮性。
- **`internal/registryd.Handler.Events *events.Handler`** 字段 + `New(store, getCreds, eventsH)` 第三参数；`localEvent(repo, tag, mediaType, action, method, r)` helper 把 `r.UserAgent()`、`r.Host`、`r.RemoteAddr`、`X-Auth-User` 头映射到 `Event` 的对应字段。
- **拉取平台白名单 `pull.platforms`**（设置页 → "拉取平台白名单"）。可选值是 `<os>/<arch>[/<variant>]` 的 CSV（例：`linux/amd64,linux/arm64` 或 `linux/amd64,linux/arm/v7`）；空 = 不过滤（保留 v0.5.x 的"全部平台"行为）。支持 `linux/amd64` / `linux/arm64` / `linux/arm/v7` / `linux/386` / `linux/ppc64le` / `linux/s390x` / `linux/riscv64` / `windows/amd64` 等常见架构的 chip 多选；自定义 token 也能从原始 CSV 输入。配置后只拉这些平台的子 manifest 与它们独有的 blob——alpine 这种"所有平台共享同一层"的镜像虽然表面 size 没变化，但拉取耗时显著下降（少 16 次子 manifest + blob-existence HEAD 请求）。
- **环境变量 bootstrap `REGISTRY_PULL_PLATFORMS`**：首次部署时不用先打开 UI 改设置，直接在 `.env` 里写一行 `REGISTRY_PULL_PLATFORMS=linux/amd64,linux/arm64` 重启即生效；之后改设置页会覆盖 env（与 `REGISTRY_URL` / `REGISTRY_PROXY` 同样的 Mutable > env 优先级）。（⚠️ 0.5.9 起该 env 已随「配置单源化」移除，设了也不再被读——见 `[0.5.9]` 的 `Load()` 条目。）
- **空匹配保护**：白名单过滤后如果一个子 manifest 都没命中（例如镜像只有 `linux/arm64` 你却写了 `linux/amd64`），`planTransfer` 立刻报错 `platform filter [...] matched no child manifests in the source index`，不会静默产出空 index 把后续 `docker pull` 全打挂。
- **未知 platform 不被悄悄丢**：上游写 `architecture: "unknown"` 或缺字段的子 manifest 会原样保留——宁可多拉一个也优于静默丢失上游给的唯一 manifest。

### 变更

- `internal/events/events.go` 把 ServeHTTP 里的 per-event 循环抽成 `processOne(ctx, ev, now, ignore) bool`；webhook 和 `IngestLocal` 都走这个 helper，杜绝规则双份维护。
- `internal/server/server.go` 第 7 步构造门槛注释更新为 v0.5.8 语义（SQLite 即可、token 空时 fail-closed）。
- 前端 **设置页删除「接收 registry events」开关**：自带的 registry 现在永远是热的，UI 上的开关对用户没有意义。`allow.registry_events` env + DB 列保留为隐藏的全局 kill switch（默认 true，运行时生效）；服务器层不变，UI 层不暴露。
- 前端 **统计页文案重写**：
  - 「还没配置事件共享密钥」警告分支**移除**——token 现在不是默认路径，少了它属于「你想接外部 registry 还没接」而不是「自带的也算不出」。
  - 「还没收到任何热度事件」info 分支改为「自带 registry 已经自动计入；如果你想让外部 registry 也算进来，再去配 `REGISTRY_NOTIFY_TOKEN`」。
  - 「registry 侧需要这样配」面板标题改为「外部 registry 需要这样配（可选）」；Collapse 标签同步收紧。
  - `NOTIFY_CONFIG_YAML` 上方加一行注释：this snippet is for EXTERNAL registries。
- `internal/api/api_test.go` 两处 `registryd.New(store, nil)` → `registryd.New(store, nil, nil)`（新签名第三个参数是 events handler，测试不关心传 nil）。
- `MutableKeys` 新增 `pull.platforms`，类型 `stringcsv`（新增的第三种类型：字符串 + 内置 CSV 校验；每个 token 必须严格 `<word>/<word>[/<word>]`，空段、含空格、非 ASCII 都被 400 拒绝）。
- `MutableSettings` 新增 `pullPlatforms` 字段，UI 顶部 / 设置页立即可读（当时另有 `pullPlatformsSource` 字段标记取值来源；该字段已在 0.5.9 随「`MutableSettings` 砍 11 个 `*Source` 字段」一并删除）。
- `config.EffectivePullPlatforms()` 新增 helper：读 Mutable > env，空值返回 `nil`（= 不过滤），CSV 自动 trim + lowercase。（0.5.9 起 env 分支整个砍掉，等价能力改由 `config.Config.PullPlatforms()` 提供，语义不变。）
- `pull.planTransfer()` 签名增加 `platformAllow []string` 参数；过滤逻辑使用新加的 `sourcePlatformRef.key()` / `matchAny()`。
- `pull.executor` 引入 `manifestFetcher` interface（仅含 `GetManifest`），让 planTransfer 单元测试可以注入 fake，无需 HTTP mock。

### 修复

- 没有功能修复；本轮纯补全既有热度功能。

### 测试

- `internal/pull/executor_test.go`（新文件，**6 个**测试）：`TestPlatformKey`（`key()` 处理 nil / 缺字段 / 大写归一化）、`TestPlatformMatchAny`（严格匹配：`linux/arm` ≠ `linux/arm/v7`）、`TestPlanTransferIndexFiltering`（白名单命中 1 个子 manifest）、`TestPlanTransferEmptyAllowListMatchesAll`（allow-list 为空时命中全部）、`TestPlanTransferNoMatchErrors`（白名单 0 匹配时报错）、`TestPlanTransferSingleArchManifestUnaffected`（单架构 manifest 走原路径、不受 filter 影响）。

### 文档

- `.env.example`「镜像热度」块改写：明确写「自带 registry 不需要任何配置；下面这些只对外部 registry 生效」，并把每个变量的当前角色逐条注释。
- `README.md` 当前状态 / 版本号 → 0.5.8。
- CHANGELOG（本文）。

### 影响 / 升级

- **0.5.7 → 0.5.8 数据无破坏**。SQLite schema 没动、`activity_daily` 表结构没动、in-memory 计数器本就是重启归零。
- 升级后**立即生效**：第一次有人对自带 registry 做一次 `HEAD /v2/<repo>/manifests/<tag>` 或 push 一层，Top 榜单就会出数据，不需要重启、刷新、或重新配 webhook。
- 如果你之前配置了 `REGISTRY_NOTIFY_TOKEN` + 外部 Distribution 的 notifications：仍然有效，新版本是两轨并行而非替换，外部 webhook 进 `/api/events`，自带 registry 走 `IngestLocal`，最终都会进同一个 SQLite 表。
- 如果你想接的「外部 registry」其实是另一个 cairn 实例：那条 `notifications.url` 仍然走 `POST /api/events`，共享密钥照旧；自带这一侧的 `/v2/*` 流量还会通过本实例的 `IngestLocal` 自己计一次（这是对的：另一台实例通过 `/v2/*` 拉取镜像时，事件应当算到"镜像存放方"，不是"镜像来源方"，如果想反着算就在那个实例上关 `allow.registry_events`）。

---

## [0.5.7] - 2026-09-25

本轮主题：**拉取任务的阶段明细**。展开一个拉取任务行，现在能看到每一步的进度——与 registry-manager 的展开视图对齐——而不是只有一条总进度。

### 新增

- **拉取任务 phases[] 阶段明细**（UI v0.5.0 就预留、后端一直返回空数组的数据通路补上）：执行器为每个步骤 emit 一条 phase——`manifest`（抓取/展开源 manifest，多架构索引标注平台数）、`config`（单平台镜像的 config blob，独占一行）、`blob #N`（每层 digest + 实时字节进度 `done / total`）、`child-manifests`（多架构索引写子 manifest）。每行显示 digest 短形式、字节进度与状态；digest 悬停可见完整值。
- **blob 级实时进度**：`transferBlob` 用计数 reader 包装源响应体，边读边上报累计字节——大层在传输中即显示中间态（如 `12.3 MiB / 27.0 MiB`），任务总进度条也改为按实际传输字节推进（此前每个 blob 完成后才整块累加 announced size；跳过的 blob 仍计入总量，保证进度条能到 100%）。
- **已存在的 blob 标为 `已存在，跳过`**（灰色）而非假装重新下载——同一镜像第二个 tag 的拉取能清楚看到哪些层复用了。
- **终态收尾**：任务失败 / 取消后，running / pending 的 phase 不再停留在"进行中"，统一标为 failed 并写明停在哪一步（`执行失败` / `任务已取消`），展开区直接看到任务死在哪一步。

### 变更

- `/api/pull/jobs` 系列响应的 `phases` 字段从恒为空数组变为真实数据（`pull.JobView.Phases` 以 copy-on-write 更新，轮询拿到的快照不会被后续变更污染）；**前端零改动**——`JobPhases` 渲染组件 v0.5.0 起就在等这份数据。
- `transferBlob` 签名变更：返回 `(written, skipped, err)` 并接受进度回调；不再使用的 `size` 参数移除。

### 测试

- `internal/pull/phase_test.go`：5 个测试——Phase JSON 键与前端 `PullPhase` 完全一致（含 `totalBytes: null` 语义）、copy-on-write 快照不被后续变更污染、Submit 预置 pending manifest phase、失败任务收尾把 straggler phase 标为 `执行失败`、取消任务把 in-flight phase 标为 `任务已取消`。

---

## [0.5.6] - 2026-09-25

本轮主题：**修好 blob 下载流**。0.5.5 修掉 Docker Hub 匿名 401 后，拉取第一次走到 blob 阶段，暴露出 v0.2 就存在的流式误用。

### 修复

- **blob 下载报 `http2: response body closed`**：`GetBlob` 复用 `doRequest`，而后者会把响应体全部读进内存（32MB 上限）并 `defer resp.Body.Close()`——`GetBlob` 返回给调用方的 `resp.Body` 是已抽干、已关闭的死流，拉取执行器第一次读就报错，blob 全部传输失败。manifest 抓取不受影响（小 JSON、按字节消费），blob 是流式（可达数百 MB）必挂。此前未暴露是因为外部源拉取一直卡在 manifest 阶段的 401。现在 `GetBlob` 自建请求、保持活流返回，并内联实现与 `doRequest` 相同的 401→Bearer token 重试（含二次 401 时失效缓存 token）。
- **Bearer 重试被 `SetBasicAuth` 降级回 Basic**：`doRequest` 的重试请求先 `Set("Authorization", "Bearer ...")`，随后若配置了用户名密码又调 `SetBasicAuth`——后者整体替换 Authorization 头，把重试降级成 Basic 认证。Docker Hub 类 registry 对业务请求只认 Bearer，会再次 401 并失效刚取到的 token。现在重试只带 Bearer（token 已封装凭据），Basic 仅用于向 token 端点换 token。影响所有配置了凭据的外部源 manifest/blob 抓取。

### 新增

- `internal/registry/blob_test.go`：3 个回归测试——GetBlob 返回可完整读出的活流（~6MB payload，钉死修复前 `http2: response body closed` 场景）、blob 下载经 401→token→Bearer 重试后仍流式返回（且匿名 token 请求不带 Authorization 头）、带凭据 registry 的 manifest Bearer 重试不得降级回 Basic（token 端点收到 Basic 凭据、业务请求收到 Bearer）。

---

## [0.5.5] - 2026-09-25

本轮主题：**修好 Docker Hub 匿名拉取**。外部源（Docker Hub 等）配了代理仍 401 的根因是匿名 token 请求误带空 Basic 凭据头。

### 修复

- **Docker Hub 匿名拉取 401 `incorrect username or password`（表现为 `401 unauthorized (no bearer challenge)`）**：`fetchTokenOnce` 只判断 `basicAuth != nil`，而拉取链路对匿名源恒传非 nil 的空结构体，导致匿名 token 请求带上 `Authorization: Basic Og==`（base64 的空用户名:空密码）。auth.docker.io 把空 Basic 头当错误凭据拒绝（158 实测 curl：无头 → 200 拿到 token，空头 → 401 "incorrect username or password"），外部源拉取全部失败。修复对齐 registry-manager 的 node 实现（`server/registry-client.mjs` 只在 `auth.username` 非空时拼 Basic 头）：username 为空一律不带 `Authorization` 头。影响所有匿名 Docker Hub / ghcr / quay 源的拉取。
- **bearer token 获取失败时报错张冠李戴**：`client.go` 的 401 处理在 `terr != nil`（token 获取失败）时也报 "(no bearer challenge)"，且请求失败分支 wrap 的是恒为 nil 的 `terr`，真实原因被吞成 `request failed` 裸文案。现在 token 获取失败报 `bearer token fetch failed: <真实错误>`，挑战头缺失才报 `no bearer challenge in WWW-Authenticate`，排查不再被误导。
- `cmd/server/main.go` 补文件尾换行（gofmt 达标，纯格式）。

### 新增

- `internal/registry/bearer_test.go`：3 个回归测试——匿名（空用户名）请求不得携带 `Authorization` 头（测试服务器模拟 auth.docker.io 拒绝任何 Authorization 头的行为，即本 bug 场景）、带凭据时必须携带 Basic 头、`Bearer realm="...",service="..."`（逗号后无空格，Docker Hub 实际格式）的挑战头解析。

---

## [0.5.4] - 2026-09-25

本轮主题：**让"设置页可改的字段"真正在运行时生效**。此前多处代码读的是启动时缓存的 env 值，而不是 SQLite 里的热改覆盖，导致设置页改了不生效。

### 修复

- **设置真生效**：`GetConfig` 顶层的 `Name` / `AllowDelete` / `AllowPull` / `StatsEnabled` / `AllowRegistryEvents` / `StatsRetentionDays` 改为读 `Effective*()`（Mutable 覆盖优先，回落 env）。之前读的是 `cfg.Xxx` 启动快照，设置页改完 `/api/config` 仍显示旧值。
- **权限闸门接上热改**：`DeleteTag`（删除）、代理拉取、registry events 三处闸门改读 `Effective*()`，nil-safe 且 fail-closed（`Full` 为空时拒绝）。之前只在启动时读一次 env，运行中改 `allow.delete` / `allow.pull` 不影响已构造的 handler。
- **events 运行时开关**：`events.Handler` 新增 `SetEnabled(func() bool)` 谓词，`ServeHTTP` 在方法检查后加 403 闸门；server 构造时注入 `cfg.EffectiveAllowRegistryEvents`。一处覆盖 `/events` 与 `/api/events` 两个挂载点，改 `allow.registry_events` 立即生效，不用重启。
- **删调试残留**：`internal/registry/client.go` 删除 3 行 `DEBUG bearer` 日志（token 获取路径上的临时打印）。

### 变更

- **`cache.ttl.seconds` 从可编辑集摘除**：v0.5.0 后清单直读本地存储，`registry.CachedRegistry` 已无构造点，这个 TTL 没有任何消费方，设置页改它完全没效果。从 `config.MutableKeys` / `MutableFieldType` 和设置页输入框移除；`EffectiveCacheTTLSeconds()` 一并删除。`REGISTRY_CACHE_TTL_SECONDS` env 与 `AppConfig.cacheTtlSeconds` 保留为**只读展示**，不破坏既有 `.env` 契约。
- **`stats.retention.days` 终于有消费点**：新增 `retentionLoop`（启动后延迟 30s 首跑，之后每 24h 一轮，每轮重读 `EffectiveStatsRetentionDays()`），调用 `db.RetentionCleanup` 按 cutoff 删除过期的 `activity_daily` 行。此前该函数全仓无调用点，热度表无限增长，设置形同虚设。
- **`ConfigExtras` 精简**：只保留 env-only 的 `IgnoreUserAgents`；凡可热改的字段一律走 `Full.Effective*()`，避免 env 快照与 DB 覆盖两套来源打架。
- **`Runtime` 持有 `Cfg *config.Config`**：让 `resolveSource` 等运行期逻辑能读到全局 HTTP 代理（`registry.proxy` 设置）等热改值。
- **前端契约对齐**：`web/src/types.ts` 的 `MutableSettings` 补齐 `usingAuth` / `allowDelete(+Source)` / `allowPull(+Source)` / `allowRegistryEvents(+Source)` / `statsRetentionDays(+Source)`；`settings-page.tsx` 移除 `cache.ttl.seconds` 输入框与相关 state/patch 段。

### 升级提示

- 若 SQLite 里已存在覆盖值（例如 `allow.delete=false`、`allow.registry_events=false`、`registry.name`、`registry.proxy`），升级到 v0.5.4 后这些覆盖会**真正生效**：删除端点返回 403、事件采集停止、代理拉取走覆盖的 proxy。如需恢复默认，在设置页改回或清空对应覆盖即可。

---

## [0.5.3] - 2026-09-25

### 新增

- **Registry 自认证（docker login）**：`/v2/*` 现在支持 Basic auth；客户端 `docker login <url>` 后才能 push/pull。`GET /v2/` 总是返回 200（OCI spec ping），但带 `WWW-Authenticate: Basic realm="cairn"`，触发 docker daemon 用 basic creds 重试。
- **设置页可热改**：`registry.username` / `registry.password` 加到 MutableKeys，UI 上是 Input + Input.Password；密码不回显（服务端从不把 password 字段写进 GET /api/config 的响应），用户输入即覆盖。
- **`basicAuthCreds` callback** 注入到 `registryd.New(store, getCreds)`：每次请求都调 `cfg.EffectiveRegistryUsername/Password()`，所以 settings-page 改完立即生效，**不需要重启**。

### 变更

- `config.MutableKeys` 从 8 加到 10。
- `Config.EffectiveUsingAuth()` 派生方法：username + password 都非空时为 true。
- `AppConfig.MutableSettings` 加 `usingAuth` 字段（`Mutable` 不再藏 bool，由服务器算出）。
- `UsingAuth` 字段改为读 `EffectiveUsingAuth()` 而不是 `cfg.RegistryUsername != ""`。
- `internal/api/api_test.go`：调用 `registryd.New(store, nil)`（测试不启用 auth）。
- 文档：`REGISTRY_USERNAME/PASSWORD` 从 v0.5.0 commit message 里的"已废弃"恢复为 v0.5.3 的"v0.5+ 是 registry 自身 Basic auth"。

### 安全

- 密码以**明文**写入 SQLite settings 表（`/app/data/cairn.db`）。cairn 当前数据卷保护靠 docker + 宿主机 FS 权限，不假设 SQLite 内部加密。
- 密码比较用 `crypto/subtle.ConstantTimeCompare`，避免 timing 侧信道泄露长度。
- 401 响应统一 `{errors:[{code:UNAUTHORIZED,message:authentication required}]}` + `WWW-Authenticate: Basic realm="cairn"`。

### 测试

- `go build ./...` ✅ · `go vet ./...` ✅ · `go test ./...` 全绿
- `gofmt -l internal/` 0 个未格式化

### 已知问题（v0.5.x 跟进）

- 158 上 `intranet-53` 测试凭据 / webhook / 卡死 job 待清理（沿用 v0.5.1）
- 现在密码以 plaintext 存于 SQLite；如要加密后续加 KMS / SOPS / OS keyring 适配
## [0.5.2] - 2026-09-25

### 新增

- **设置页全部可热改**：除 `REGISTRY_CREDENTIAL_KEY` / `PORT` / `HOST_PORT` / `REGISTRY_CREDENTIALS_DIR` / `REGISTRY_STORAGE_DIR` 这几个"改动后必须重启"的字段外，所有其他 `REGISTRY_*` env 都可以在设置页直接编辑，写到 SQLite，热替换 `cfg.Mutable`，无需重启。
- **新增可改字段**：`registry.proxy` / `registry.name` / `cache.ttl.seconds` / `allow.delete` / `allow.pull` / `allow.registry_events` / `stats.retention.days`（加上已有的 `registry.url`，共 8 个）。
- **`config.MutableKeys` 白名单**：服务端只接受这 8 个 key 的 PATCH；其它字段（包括 secret / 路径）一律 400 拒绝。
- **通用 `PATCH /api/config`**：body 改为 `{"mutable": {"<key>": "<value>"}}`，一次可改多个字段，按 key 验证类型（`bool` / `int` / `url` / `string`）。
- **UI 重构**：
  - Descriptions 描述式只读布局 → Form 表单式可编辑布局
  - 每个可改字段旁挂 **「界面设置 (已覆盖) / 环境变量」** SourceTag，明示当前生效值的来源
  - "保存全部设置"按钮：diff 提交，只 PATCH 实际改动的字段（节省 db 写入）

### 变更

- `config.Mutable` 内部从单字段 `registryURL` 改造成 `map[string]string` 通用 key->value 存储；`Has(key)` / `Get(key)` / `Set(key, val)` 是统一接口。
- `Config.EffectiveXxx()` 现在覆盖 8 个字段，每个都有"db 覆盖 > env 回退"语义，集中在一处。
- `handlers.UpdateConfig` 用 `MutableKeys` 白名单 + `MutableFieldType` 类型映射做校验；不再写死 `registryUrl` 一个分支。

### 测试

- `go build ./...` ✅ · `go vet ./...` ✅ · `go test ./...` 全绿
- `gofmt -l internal/` 0 个未格式化

### 已知问题（v0.5.x 跟进）

- 158 上 `intranet-53` 测试凭据 / webhook / 卡死 job 待清理（沿用 v0.5.1）
- `REGISTRY_USERNAME/PASSWORD` 在 v0.5 已不再使用（cairn 自管仓库），未暴露到设置页

## [0.5.1] - 2026-09-25

### 新增

- **`PATCH /api/config`**：设置页可热改 `REGISTRY_URL`，立即生效；env 仍是首次启动默认值。
- **`settings` SQLite 表**（migration v3）：`key/value/updated_at`，后续每加一个可热改字段都先在这里加一行 schema。
- **`config.Mutable` + `EffectiveRegistryURL()`**：运行时注册表（`sync.RWMutex` 保护），`Orchestrator.Mutable` 引用同一指针；改完 pull 入队时立刻看到新上游，无需重启。
- **UI（settings-page.tsx）**：
  - "地址" 行从只读 `Descriptions` 改为 `Input + 保存` 控件
  - 当前生效值下方带 **"界面设置（已覆盖）" / "环境变量"** Tag，标明来自 db 还是 env
  - 留空提交 = 清除覆盖，恢复 env 默认

### 变更

- **`Internal/Handlers.Cfg.RegistryURL` 读路径**：所有路径改走 `Cfg.EffectiveRegistryURL()`，env vs db 优先级集中在 config 包。
- **`Pull.Orchestrator.DefaultSourceURL` 标记 deprecated**：保留为 env-bootstrap fallback；新路径优先 `Mutable.RegistryURL()`。
- **server.go 启动加载**：db 可用时从 `settings` 表 hydrate `cfg.Mutable.SetRegistryURL(...)`，日志带 `"registry.url.source":"db"`。

### 测试

- `go build ./...` ✅
- `go vet ./...` ✅
- `go test ./...` 全绿
- `gofmt -l internal/` 0 个未格式化

### 已知问题（v0.5.x 跟进）

- 158 上卡死 job `job-1790328145899764915-1` 仍待 cancel
- `intranet-53` 测试凭据、webhook 仍待联调

## [0.5.0] - 2026-09-25

### 新增

- **cairn 默认管理自己**：`REGISTRY_URL` 不再是必填 env；空值时 pull 任务回退到 Docker Hub (`pull.DefaultUpstream`)。每个 pull 任务在 UI 上独立指定自己的源（URL / inline auth / inline proxy），不再依赖一个"被管理的远端 registry"。
- **`REGISTRY_STORAGE_DIR` env**：新增。blob / manifest / tag 的物理路径，默认 `/app/registry`。`docker-compose.yml` 用独立命名卷 `cairn-registry` 挂载此处，与应用数据 (`cairn-data:/app/data`) 物理隔离 —— 备份 / 重置可以分别处理。
- **删除操作回归 cairn 页面**：
  - `DELETE /api/repositories/{repo}` —— 删除整个仓库（所有 tag + manifest + 受影响 blob），由 `REGISTRY_ALLOW_DELETE` 门控。
  - `DELETE /api/repositories/{repo}/manifests/{digest}` —— 按 digest 删除 manifest，响应里带 `affectedTags`（被同时清理的 tag 列表）。
  - `POST /api/gc` —— 触发一次存储 GC 扫描，返回 `{removedBlobs, freedBytes}`。
- **`storage.GC` 实现**：文件系统层 GC 真正落地 —— 扫描 `uploads/` 目录中超 24h 的孤立上传 session 并清理；pass2 不再被重复计入。
- **`storage.TagsForDigest` / `DeleteManifest([]string,error)`**：本地 storage 接口新增 `TagsForDigest`；`DeleteManifest` 签名升级为返回被解引用的 tag 列表，让 "按 digest 删除" 端点能告诉前端影响了哪些 tag。
- **`pull.NewJob` 接受 inline 凭据 + inline proxy 字符串**：`CreatePullJobReq` 接收 `sourceUrl` / `sourceRef` / `sourceProxy` / `sourceProxyId` / `sourceCredentialId` / `sourceAuthInline`，与前端 `PullJobInput` 形状 1:1 对齐。
- **`registry.Config.Proxy` string 字段**：远端 registry client 支持单层 HTTP 代理（之前在 server 层组合，现在下沉到 client）。
- **`/v2/<repo>/blobs/<digest>` 多段路径通配**：registryd 路由用 `/*` + 手动 dispatch 替代 chi 多段 `{}` 占位，让 OCI 多段路径与标准 V2 协议 100% 对齐。
- **`parseDays` 7-day bug 修复**（本就在 HEAD 已修，验证保留）：不再把 `?days=7` 解析成 7 小时。

### 变更

- **`Handlers.Registry` / `ExtraHandlers.Registry` 字段删除**：v0.5 不再有"远端 registry client"这个抽象；`Orchestrator.ExternalRegistry` 字段随之消失。
- **`server.go` 简化**：移除整个 externalRegistry 构造块（约 30 行）；executor 现在无条件创建（不再 `if externalRegistry != nil` 门控）；`DefaultSourceURL = REGISTRY_URL` 直接注入 Orchestrator。
- **`registryURL(r)` 翻转**：请求 Host 优先（含 `X-Forwarded-Proto` / `X-Forwarded-Host` 覆盖），`REGISTRY_URL` 仅作回退 —— 体现"cairn 默认就是自己"的语义。
- **`Inventory` / `Refresh` / `DeleteTag` 等：UI 仍走旧契约（`refreshAt` / `targetRepo` / `targetTag` / `error`），UI 大改放在 v0.5.x**；当前版本 **不破坏** UI 字段，避免 158 重新部署后页面立刻白屏。
- **frontend dist 不需本地 pnpm 重建**：Dockerfile 的 `web-builder` stage 内构建，前端变更不阻塞 Go 二进制。

### 修复

- **存储路径双计**：`storage.Stats` 不再把 `<algo>/<2hex>/<hex>/data` 与 `<algo>/<2hex>/<hex>` 重复计入体积。
- **`Repositories` 不递归 + 不剪枝**：v0.4 列表里会出现已经删除的空目录，现在正确剪枝。
- **`DeleteManifest` 不清 tag**：删除 manifest 时同步清理指向该 digest 的 tag 文件，避免 `tags/list` 列出 404 的悬空 tag。

### 文档

- **CHANGELOG.md**：本节。
- **docker-compose.yml / .env.example**：精简到只剩运行需要的 env（`REGISTRY_NAME` / `REGISTRY_CREDENTIAL_KEY` / 可选 `REGISTRY_URL` / 可选 `REGISTRY_NOTIFY_TOKEN` / 新增 `REGISTRY_STORAGE_DIR`）；其他 env 全部保留默认值（契约不变）。
- **README.md**：状态标题改为"cairn 默认管理自己，部署只需挂载存储卷"；部署小节说明 `REGISTRY_CREDENTIAL_KEY` 是唯一必填；字段对照表新增 `REGISTRY_STORAGE_DIR`。

### 已知问题（v0.5.x 跟进，不影响主流程）

- 158 上 `job-1790328145899764915-1` 仍处卡死态，需先 cancel。
- webhook 测试未配（按需）。

## [0.4.0] - 2026-09-25

前后端在本版本正式合体：React 前端（`web/`）构建后经 `//go:embed` 嵌入二进制（`-tags webui`），`/` 直接服务 UI、深路由 fallback index.html，部署只发一个文件；`/api/*` 契约全面对齐前端 `types.ts` / `api.ts`；修复 HEALTHCHECK 永远 unhealthy（`-healthz` 探针从未实现）；DB / 凭据库 / 代理库启动失败降级为软错误，不再 Fatal。

### 新增

- **Web UI 嵌入二进制**：新增 `internal/webui` 包（`-tags webui` 下 `//go:embed dist` 并挂 FS；默认 tag 下为占位实现，保证纯 Go 环境可编译）。`web/vite.config.ts` 的 outDir 改为直接产出到 `../internal/webui/dist`。Dockerfile 新增 web-builder 阶段（node + corepack + pnpm 构建前端，产物拷入 Go builder 后 `-tags webui` 编译），新增 `NODE_IMAGE` / `NPM_REGISTRY` build-arg（受限网络可换内网镜像源）；新增 `.dockerignore` 防止本地 node_modules / dist 污染构建上下文。
- **`/cairn -healthz` 探针模式**：HTTP GET 本机 `/healthz`，2xx 退出码 0、否则 1。Dockerfile 的 HEALTHCHECK 自 v0.1 就写着 `CMD ["/cairn","-healthz"]`，但二进制从未实现该 flag——探针每次被当作再启动一个服务进程，撞 8787 端口失败退出码 1，容器永远 unhealthy。现在探针只做一次 HTTP 检查即退出。
- **API 契约对齐 UI**（handlers.go / handlers_extra.go）：
  - `GET /api/config` 返回 `AppConfig`（`name` / `canDelete` / `canPull` / `allowRegistryEvents` / `pullQueueSize` 等，字段与前端 `types.ts` 一一对应）；
  - `GET /api/inventory` 聚合清单（仓库 + tags + manifest 摘要 + 体积）；
  - `DELETE /api/tags?repository=&tag=` 按 tag 删除：服务端解析 digest、列出同 digest 受影响的其它 tag，返回 `{deletedTag, digest, affectedTags, repository}`；
  - `/api/credentials`、`/api/proxies` 支持 PATCH 与 `note` 备注字段；密码进凭据库（AES-256-GCM），响应只带 `hasPassword` / `hasAuth` 不回显明文；
  - 统计全家桶 `/api/stats/*`（overview / top / series / repos / days / recent），ignore 规则三来源合并（env + DB + User-Agent），`/api/stats/purge` 清空热度数据；
  - 新增顶层 `/events` SSE 端点（每连接独立 channel，断开即清理）。
- **前端 ErrorBoundary**：捕获渲染异常展示兜底 UI（antd Result），挂载于 `main.tsx` 的 ConfigProvider 内。
- **compose / .env.example** 增加 `NODE_IMAGE` / `NPM_REGISTRY` 接线（前端构建的 node 镜像与 npm registry 镜像可覆盖）。

### 变更

- **DB / vault / proxies 启动失败降级为软错误**（server.go）：SQLite / 凭据库初始化失败时不再 Fatal 退出，对应 API 返回带 `?error` 字段的降级响应，其余功能（含 `/v2` 数据平面）保持可用。
- 前端不再需要单独 `pnpm build` + nginx 反代部署（v0.3.1 的 TODO「前端 dist embed」完成）。
- pnpm 12 构建许可迁移到 `web/pnpm-workspace.yaml` 的 `allowBuilds:`（`package.json` 的 `onlyBuiltDependencies` 在 pnpm 12 已不生效，会导致 esbuild postinstall 被跳过、vite 无法构建）。

### 修复

- **pull 队列 `RunOne` nil panic**：执行器依赖字段为 nil 时（如无凭据直连拉取）解引用 panic，进程崩溃；现在走空值安全路径。
- api_test.go 两处旧契约断言随新 API 修正（`registryName`→`name`；deleteTag 参数 `repo/digest`→`repository/tag`）。

---

## [0.3.1] - 2026-09-25

### 新增

- **构建期 Go 模块代理可配置**：`Dockerfile` 新增 `ARG GOPROXY=https://proxy.golang.org,direct` + `ENV GOPROXY=${GOPROXY}`,影响 `go mod download` 阶段。
  - `docker-compose.yml` 通过 `${GOPROXY:-https://proxy.golang.org,direct}` 透传到 build-arg
  - `.env.example` 新增 `GOPROXY=` 配置项,默认留空走 `proxy.golang.org,direct`
  - 受限网络下,在 `.env` 把 `GOPROXY` 改成 `https://goproxy.cn,direct` 或 `https://goproxy.io,direct` 即可,无需改代码
- **构建期通用 HTTP/HTTPS 代理可配置**：跟 `GOPROXY` 正交,影响 builder stage 内所有网络出口(go / curl / git / apt 等),给构建机直连外网受限、有内网 HTTP 代理的环境用。
  - `Dockerfile` 新增 `ARG HTTP_PROXY=` `ARG HTTPS_PROXY=` `ARG NO_PROXY=`(默认空 = 不设代理,行为不变)
  - `docker-compose.yml` 透传到 build-arg,`.env` 里用 `BUILD_HTTP_PROXY` / `BUILD_HTTPS_PROXY` / `BUILD_NO_PROXY` 配置(带 `BUILD_` 前缀跟运行时 `REGISTRY_PROXY` 区分,避免 `.env` 命名冲突)
  - 内网代理示例:`BUILD_HTTP_PROXY=http://proxy.example.com:7890`

### 文档

- `README.md` 开发章节新增「网络受限环境的构建(Go proxy)」+「构建期通用 HTTP 代理(跟 GOPROXY 正交)」两个小节,列出本地 / docker build 两种覆盖方式 + 常用国内代理 + 内网代理典型用法
- 更正内网代理示例地址(原 `4433` → `7890`,与真实代理对齐):`Dockerfile` / `README.md` / `.env.example` 示例统一更新
- `.env.example` 新增 `HOST_PORT` 项(宿主机访问端口,示例值 80,留空回退 8787),`README.md` 部署段补充"启动后访问 `http://<宿主机>:<HOST_PORT>`"说明
- `AGENTS.md` 保持不变(版本号规则已覆盖本次改动的小版本 +1 判定)

---

## [0.3.0] - 2026-09-25

### 重大变更

**cairn 现在是 registry 本身**，不再依赖外部 OCI Distribution。

之前 v0.2 是 **registry 的客户端/admin**（必须配 `REGISTRY_URL` 指向已有的 Docker Registry）。
现在 v0.3 自带数据平面：本地 FS 存储 blob/manifest/tag，对外暴露 `/v2/*` 协议，
`docker push` / `docker pull` / `skopeo copy` 直连 cairn 即可。

新增

**v0.3 — registry server**

- `internal/storage/` — Filesystem 后端，digest 校验，原子写，per-repo 写锁
  - 布局：`repos/<repo>/tags/<tag>`、`repos/<repo>/manifests/sha256/<digest>/data`、
    `blobs/sha256/<aa>/<bb>/<digest>/data`、`uploads/<repo>/<uuid>/data`
  - `Storage` interface：`Repositories / Tags / GetManifest / PutManifest /
    DeleteManifest / BlobExists / GetBlob / StatBlob / StartUpload /
    PatchUpload / PutUpload / GetUpload / CancelUpload / Stats`
- `internal/registryd/` — `/v2/*` 协议路由
  - `GET /v2/`、`GET /v2/_catalog`
  - `GET /v2/<repo>/tags/list`
  - `GET/HEAD/PUT/DELETE /v2/<repo>/manifests/<ref>`
  - `GET/HEAD /v2/<repo>/blobs/<digest>`
  - `POST /v2/<repo>/blobs/uploads/`（start）
  - `GET /v2/<repo>/blobs/uploads/<uuid>`（inspect）
  - `PATCH /v2/<repo>/blobs/uploads/<uuid>`（chunk）
  - `PUT /v2/<repo>/blobs/uploads/<uuid>?digest=<digest>`（commit）
- Admin handlers 改用本地 storage（不再调外部 registry）
- `REGISTRY_CREDENTIALS_DIR` 下挂 `registry/` 子目录做数据存储
- Pull Orchestrator：source = 外部 registry client，dest = 本地 storage

### 变更

- `/api/inventory` 现在从本地存储读，秒级返回（不再等 V2 协议 catalog 扫描）
- `/api/tags?repo=&digest=` 删除走本地 storage，返回受影响 tag 列表（best-effort）

### 测试

- 新增 registryd 集成测试：`/v2/` 根、`/v2/_catalog`、完整 push→pull roundtrip、digest mismatch
- 21 个测试全绿（v0.2 的 14 个 + v0.3 的 7 个）

---

## [0.2.0] - 2026-09-25

### 新增

**v0.2 — 拉取 + 凭据 + 代理**

- `internal/pull` — FIFO 拉取队列（单并发、生命周期、cooperative cancel、ring buffer）
- `internal/pull.Orchestrator` — 解析 sourceRef、下载 manifest、按 layer 下载 + 上传 blob、PUT manifest 到目的端
- `internal/credentials` — AES-256-GCM 加密 vault（SHA-256 派生 key、原子写）
- `internal/proxies` — 明文 JSON 代理库
- `internal/registry/bearer.go` — V2 Bearer token 流程（401 + WWW-Authenticate → realm 取 token → 重试）
- `internal/registry/blob.go` — `BlobExists` / `GetBlob` / `StartBlobUpload` / `UploadBlob`（4 MiB chunked PATCH + PUT commit）
- API：`/api/pull/jobs[/:id[/cancel]]`、`/api/pull/probe`、`/api/credentials[/:id[/test]]`、`/api/proxies[/:id[/test]]`
- Source client 解析（Docker Hub `library/` 前缀、单并发 source URL）

**v0.3 — 事件 + 热度**

- `internal/db` — modernc.org/sqlite（纯 Go，无 CGO），WAL 模式，`SCHEMA_VERSION` 迁移
- 表：`activity_daily`（按 天×仓库×tag×action 聚合）、`pull_jobs`（拉取历史）
- `internal/events` — webhook 接收（HMAC-SHA256 签名校验）、manifest media-type 白名单、HEAD/PUT 方法白名单、User-Agent 忽略规则（子串、大小写不敏感）、self-UA 单独计数、最近事件 ring buffer
- API：`POST /api/events`、`/api/stats/{summary,top,series,repositories,events,clients,ignore}`、`DELETE /api/stats/heat`

**前端**

- `web/` 从 registry-manager 完整复制（React + AntD + Vite）
- 改 `vite.config.ts`（去掉 `root: 'web/'`，因为目录已经在 web/）
- 改 `package.json`（去掉 Node 后端依赖，重命名为 `cairn-web`）
- API 响应统一包在 `{success, code, message, data}` 信封（与 registry-manager 一致）

**响应格式改造**

- 所有 `/api/*` 响应包信封（前端 `request<T>()` 直接拿 `data`）
- 错误响应：`{success: false, code, message, error: {code, message, detail}}`

### 修复

- V2 errors[] 数组解码（registry-manager 用的 spec 格式，之前是顶层 code 字段）
- V2 manifest `Accept` header 4 种 media type（OCI index + manifest + Docker list + v2）

### 文档

- AGENTS.md §Go 工具链版本（Dockerfile ARG GO_IMAGE 跟 go.mod 的 go 指令必须对齐）

---

## [0.1.0] - 2026-09-25

### 新增

- chi 路由 + structured logging（slog JSON 输出）
- V2 协议客户端：basic auth、HTTP 代理支持、可选 InsecureTLS
- 清单浏览：列出仓库、列出 tag、获取 manifest（支持 OCI index / OCI manifest / Docker list / Docker v2 四种 media type）
- 删除：按 digest 删除 manifest，返回受影响的 tag（best-effort 扫描）
- 配置探测 `/api/probe`、强制刷新 `/api/refresh`
- 内存 TTL 缓存（`CachedRegistry`），缓存命中走原值、过期重扫
- 类型化错误 `*registry.Error`，从 V2 spec 的 `errors[]` 数组解码 code/message/detail
- 多阶段 Dockerfile：scratch 基础，运行镜像 ~15MB
- docker-compose.yml + .env.example（字段名与 registry-manager 对齐）
- AGENTS.md（开发规范 + V2 协议事实）
- 14 个单元 / 集成测试，覆盖 V2 客户端、错误解码、并发扫描、handler 路由