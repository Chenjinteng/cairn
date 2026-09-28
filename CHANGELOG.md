# Changelog

cairn 的所有显著变更记录于此。格式遵循 [Keep a Changelog](https://keepachangelog.com/)。

版本规则见 [AGENTS.md §版本号规则](./AGENTS.md#版本号规则)。

---

## [0.5.29] - 2026-09-28

本轮主题:**镜像列表的「删除仓库」入口去掉,改走「删 tag → GC」链路**。

### 变更

- **镜像列表操作列收紧**(`web/src/pages/images-page.tsx:230-247`):

  旧:`操作` 列同时挂 `详情` + `删除仓库` 两个入口;删除仓库调 `DELETE /api/repositories/{repo}`,**前端在 `await deleteRepository(...)` 之后无脑 `message.success(...)`**,不检查 `result.success`。后端 `Store.DeleteRepository` 在 v0.5.24 Pass 3 bug fix 之后对命名空间段路径 (`library/alpine`) 是支持的,但**前端无脑 success toast 掩盖了后端真删与否的状态** —— 体感就是「提示删除没用」。

  新:操作列只剩 `详情` 一个入口;列宽 140 → 100;Popconfirm + `deleteRepository` 整套移除。`DeleteOutlined` / `Popconfirm` / `message` imports **不删**(GC 弹窗 `运行 GC` 按钮仍在用)。

  ```diff
  - {config?.allowDelete ? (
  -   <Popconfirm ... onConfirm={async () => {
  -     try {
  -       await deleteRepository(record.name);
  -       message.success(`已删除仓库 ${record.name}`);  // 无脑 success
  -       ...
  -     }
  -   }}>
  -     <Button ... danger icon={<DeleteOutlined />}>删除</Button>
  -   </Popconfirm>
  - ) : null}
  + // v0.5.29 注释:操作员在仓库列表上「一键删整个仓库」不合理。要清掉一个仓库走
  + // 「详情 → 删完所有 tag → 列表 → 运行 GC + 勾「也清理 0 tag 仓库」」。
  + <Button type="link" size="small" onClick={() => setDetailName(record.name)}>详情</Button>
  ```

### 设计意图

- 一键删仓库是**危险操作**(即便有 Popconfirm 也防不住误操作),且**没用**:删 manifest 只是解除引用,磁盘空间要 GC 才回收。
- 让操作员走「删 tag → GC」链路的好处:
  1. 每个 tag 的删除是独立动作,误删一个不影响其他
  2. GC 是显式动作,操作员**自己决定**何时清理磁盘
  3. `运行 GC` 弹窗的「也清理 0 tag 仓库」勾选框(v0.5.20)是**批量**清空仓库的合法入口,自动 / 手动都覆盖到了

### 保守保留 / 待后续清理

- **`DELETE /api/repositories/{repo}` HTTP 端点保留**(`internal/api/handlers_extra.go:588`):本轮只去 UI 入口,后端 API 不动 —— 防止破坏外部手动 curl / 调试场景;`Storage.DeleteRepository` 也保留(GC Pass 3 还要用)。
- **前端 `web/src/api.ts:225-229` 的 `deleteRepository` export 保留**:无人调用,但删 export 是一次性破坏,留到下一轮统一清理。
- 跟踪项:如果确认不需要外部 API 调用,后续一轮统一删 `DeleteRepository` handler + `deleteRepository` export + `DeleteRepositoryPayload` in `types.ts`。

### 影响范围(升级须知)

- **行为变化**:升级后镜像列表操作列只剩 `详情`,**仓库层级的删除按钮彻底消失**(包含 `allowDelete=false` 时的灰态占位)。
- **API 契约无变化**:`DELETE /api/repositories/{repo}` 端点仍能调通,只是 UI 不再暴露。
- **无回归测试变动**:删除链路本来就没测试覆盖(单元测试重点在 GC Pass 1..4)。

---

## [0.5.28] - 2026-09-28

本轮主题:**为 v0.6.0「http/https 切换」做准备 —— 仓库地址字段语义改成「裸 host:port」,协议字段拆出来单独管理;展示名称上右上角,不再被 host 淹没**。

### 变更

- **仓库地址字段语义调整**(`internal/api/handlers.go` `MutableFieldType["url"]` 校验 + `GetConfig` 的 `displayURL` 拼接):

  | 旧 (≤v0.5.27) | 新 (v0.5.28) |
  | --- | --- |
  | 字段存 `http://registry.example.com:8787`(带协议) | 字段存 `registry.example.com:8787`(裸 host:port) |
  | 校验:必须 `http://` 或 `https://` 开头 | 校验:必须是合法 host 或 host:port,端口 1..65535,**不允许协议前缀** |
  | 渲染 URL:直接用字段值 | 渲染 URL:`"http://" + 字段值`(协议在服务端补) |

  协议现在写死 http。**为 v0.6.0「http/https 切换按钮」铺路**:届时这块从硬编码改成读 https toggle 状态,字段本身不动。

- **`isValidHostPort()` 校验 helper**(`internal/api/handlers.go`):
  - 正则 `^[a-zA-Z0-9](?:[a-zA-Z0-9._-]*[a-zA-Z0-9])?(?::\d{1,5})?$`
  - 端口合法区间 1..65535
  - 拒绝 `http://` / `https://` 前缀,错误信息直说「不要带协议前缀,只填 host:port」

- **设置页 UI 改造**(`web/src/pages/settings-page.tsx`):
  - Input 加 `addonBefore={<Tag color="cyan">http://</Tag>}`,让用户一眼看到当前协议 = http,**v0.6.0 改 Select 即可**
  - placeholder 改成 `registry.example.com:8787`,跟示例对齐
  - extra 文案:去掉 `http://...` / `https://...` 例子,改成裸 host 例子,并加一句「协议 = http(写在前面那个 badge),**这里只填 host:port**」
  - 只读视图也补上 `http://` 前缀,避免用户看到「`http://registry.example.com:8787`」却找不到它从哪儿配出来

- **一次性数据迁移**(前端 `stripUrlProtocol()` helper):
  - 用户已有的 `http://...` 值加载到编辑态时,**自动剥掉协议前缀**显示
  - 提交后存进 SQLite 的就是新格式
  - 老用户首次升级后,进设置页保存一次即完成迁移,无需手工改

- **右上角主显示改为「展示名称」**(`web/src/App.tsx`):
  - 旧:`{config.url}` 单行(展示名称填了没人看到,等于没填)
  - 新:主行 `config.name`(展示名称);副行 `config.url` 灰色小字号(保留 host 入口可见性,避免操作员失去「当前连哪个 registry」的直觉)
  - 鼠标 hover 任意位置看 tooltip 都是 `config.url` 全文
  - 用户原话「展示名称要在右上角显示出来,不然配置就没有用」—— 本轮兑现

- **`buildPullCommand` 内部补协议**(`web/src/utils.ts`):
  - 旧:接收的 `config.host` 是带协议的完整 URL,直接拼 `docker pull ${host}/...`
  - 新:`config.host` 是裸 host:port(从 `hostOf(displayURL)` 抽出 host 部分,不含协议);`buildPullCommand` 内部补 `http://` 前缀
  - 否则 docker pull 拿到裸 host 默认按 https 处理,会失败

### 影响范围(升级须知)

- **存量的 `http://...` 值**:升级后**首次进设置页保存一次**即完成迁移(前端自动 strip,后端按新格式校验)。在此之前显示还是带协议(右上也还是裸 host:port 拼出来的 URL,跟之前一致)。
- **API 契约无破坏**:`/api/config` 的 `mutable.registryUrl` 字段类型未变(string),只是值的语义变了。客户端调用方需要同步去掉 `^https?:\/\/` 才能拼回原值。
- **`buildPullCommand` 兼容性**:接收带或不带协议的 host 都行,内部统一补 `http://`。
- **行为变化**:
  - 设置页仓库地址 Input 前面多一个 `http://` badge(只读视图前缀也跟着补)
  - 右上角现在显示「展示名称」作为主标题,URL 退到副行 + tooltip
  - docker pull 命令现在带 `http://` 前缀(之前是裸 URL,行为其实是 docker 默认按 https 处理;补 http 后语义明确)
- **回归测试**:本轮纯字段语义调整,没引入新单测。

---

## [0.5.27] - 2026-09-28

本轮主题:**修复删除 tag 成功后弹空 Alert 的 UI bug —— 后端 `writeJSON` 注入的 message 是空串,前端直接把空字符串渲进 AntD Alert**。

### 修复

- **删除 tag 改用顶部 toast**(`web/src/components/image-detail-drawer.tsx:78-99` 的 `handleDelete`):

  旧实现:成功也 `setNotice(result)`,drawer 顶部 Alert 的 `message={notice.message}` 直接拿后端注入的空串渲染。AntD Alert 的 message 为空时不会报错,只是渲成「只有对勾图标 + 关闭按钮的空白绿条」,让人误以为系统在发什么公告。

  新实现:成功走 `message.success(\`已删除 tag ${record.tag}\`)` 顶部 toast(沿用 `images-page.tsx:250` 删除仓库的模式,3 秒自动消失),**根本不进** `setNotice`。

- **drawer 顶部 Alert 收紧为「错误反馈专用」**(同文件 L202-218):

  - 加 `!notice.success` 守卫:成功路径完全不画 Alert(代码原本有,但因为当时 setNotice(result) 把成功响应也塞进去,渲染分支被走了)。
  - description 从 `affectedTags.join('、')`(成功路径下读 sibling tags)换成 `错误分类: ${code}` —— 后端拒绝时给用户看 code 是有价值的事实;成功时根本不会进这条分支。
  - type 从 `'success' | 'warning'` 收窄到 `'warning'`,编译期就锁死「这里只画错误」。

### 影响范围(升级须知)

- **行为变化**:升级后删除单个 tag,drawer 顶部不再出现绿色空 Alert,改在页面右上角弹「已删除 tag X」toast(3 秒自动消失)。失败(allowDelete 关闭、tag 不存在、manifest 删除错误等)依旧在 drawer 顶部画橙 Alert + 错误分类。
- **API 契约无变化**:后端 `DELETE /api/tags` 响应字节完全一致,只调整前端渲染。
- **无回归测试变动**:`handleDelete` 是组件内回调,后端逻辑零改动。

---

## [0.5.26] - 2026-09-28

本轮主题:**热度页面去掉「还没收到任何热度事件」空态提示 —— Cairn 只管自带 registry 的热度,不引导用户排查外部 registry**。

### 变更

- **`statsNotice()` 返回类型收窄**(`web/src/pages/stats-page.tsx:813`):
  - 旧:`{type, message, description}` 三种形态(DB 报错 / `allowRegistryEvents=false` / 一切就绪但窗口内没事件 —— 后者返回 `info` 提示 + 长篇延伸「是不是外部 registry 没配好?`REGISTRY_NOTIFY_TOKEN`?Harbor 怎么打开 notifications?」)
  - 新:`{type: 'warning', ...} | null` —— DB 错 / `allowRegistryEvents=false` 才返回 warning,其他场景返回 `null`
  - 写明的设计意图:**Cairn 0.5.23 起定位改成「自带 registry 的镜像基础设施平台」**,事件=0 只表示当前窗口没 pulls,**不再代表操作员配错了什么**;插一条「你是不是没配 REGISTRY_NOTIFY_TOKEN」反而会让人误以为系统没在干活。

- **`empty` 分支彻底不画任何提示**(原行 ~620-647 一整段 `<Alert>` + `<Collapse>` 嵌入 `NotifyConfigSnippet` 已删除):
  - KPI 卡片自带的「事件总数 0 / 拉取次数 0」已经是准确表达
  - 真要排查走下方「最近事件」面板(不受 200 条窗口限制、重启也不丢)与 KPI 自检
  - 当前分支留一行注释说明 v0.5.26 移除原因,免得有人回看 git blame 误以为是漏改

- **`!config.statsEnabled` 分支加 `notice ?` 守卫**(`web/src/pages/stats-page.tsx:553-566`):
  - 旧实现:`statsEnabled=false` 时**无脑**展示 alert + `NotifyConfigSnippet`(外部 registry 配置片段)
  - 新实现:仅当 DB 报错或 `allowRegistryEvents=false` 时才展示告警 + 配置片段;其它情况让页面空白显示 metric 0
  - 与「当且仅当 DB 报错时显示告警 + 配置片段」保持一致

### 保守保留 / 待后续清理

- **`NotifyConfigSnippet` 组件**(整段保留,未删除):本轮只针对用户要求的「没事件」提示做最小改动。`NotifyConfigSnippet`(附 YAML 配置片段 + 「Distribution 没有热重载,改完必须重启 registry」提醒)目前**仍**在 `!config.statsEnabled` 分支被 `notice ?` 守卫渲染。**Cairn 不接外部 registry,这部分组件理论上应该整体下线**,但用户没明确要求删,留到下一轮单独清理。
  - 跟踪项:打开 `web/src/pages/stats-page.tsx`,搜 `NotifyConfigSnippet`,整段应该删掉(连同 `NOTIFY_CONFIG_YAML` 常量、`TextCopyButton` helper 若不再被引用)。
  - 跟踪项:`internal/server/api.go` 的 `notify.token` / `AllowRegistryEvents()` 配置面也一并评估**(本轮不动后端,只删前端)**。

### 影响范围(升级须知)

- **后端零变化**:本轮纯前端 UI 删除,`/api/stats/*` 响应字节、`stats.db` schema 全部不变。
- **行为变化**:升级后即使从来没收到任何事件,「热度」页面也不会再弹提示;打开就是干净的 KPI + Top 榜单(空) + 趋势图 + 最近事件。
- **无回归测试变动**:`statsNotice()` 是纯展示函数,不引入新单测。

---

## [0.5.25] - 2026-09-28

本轮主题:**修「拉取平台过滤不生效」的 UX bug —— 后端其实过滤对了,但 tag 仍指向原始 multi-arch INDEX,UI 因此显示"+13"**。

### 修复

- **tag 指向过滤后的 root**(`internal/pull/executor.go:296` 的 `PutManifest`):

  旧实现 (v0.5.0 ~ v0.5.24) 始终把**原始 source manifest**(`srcManifest.Raw`)写成 destTag:
  ```go
  written, err := o.Dest.PutManifest(ctx, destRepo, destTag, plan.rootMediaType, srcManifest.Raw)
  ```

  对 multi-arch 镜像,这意味着 `tags/3.19` 指向的仍是上游 index(14 个 child 引用)—— 即使本地**只拉了 amd64 的 child + layer**。158 现场复现:磁盘上 `manifests/sha256/<amd64-child>` 与 `blobs/` 都只有 amd64 的字节,但 UI 架构列显示 `linux/amd64 +13`,误导操作员以为过滤没生效。

  新实现按过滤结果分支:

  | 过滤匹配数 | tag 写入 |
  | --- | --- |
  | 0(没配过滤) | 原 INDEX 不变 —— multi-arch 保留 |
  | 1(`linux/amd64`) | 该平台的 child manifest —— single-arch,UI `+0` |
  | ≥2(`linux/amd64,linux/arm64`) | **合成的 filtered index**(只含被选的 child)—— multi-arch 但限定到操作员选的平台 |

  三种 case 都匹配 Docker CLI `docker pull --platform` 的行为语义。

- **`synthesizeFilteredIndex` helper**(`internal/pull/executor.go`):
  - 解析原始 INDEX 为 generic map,**保留所有字段**(annotations / mediaType / platform / size)→ Docker attestation 通过 `vnd.docker.reference.digest` annotation 链 SBOM/signature,**必须保留**否则断链
  - 按 keep 列表过滤 child 条目
  - 重新 marshal,得到 SHA256 不同的新 INDEX;此 digest 作为 destTag 的目标
  - 现有 GC 不会把它当孤儿,因为 tag 还指向它

### 影响范围(升级须知)

⚠️ **已存在的 tag 不会自动修复**:
  - 0.5.24 及之前拉的镜像,`tags/<X>` 指向的还是原始 INDEX digest。**重新 pull 一次**才会被新逻辑覆盖。删除重建也行:`DELETE /api/repositories/<repo>` 再 `POST /api/pull/jobs`。
  - 或者保留旧 tag,但要知道 UI 上看到的 `+N` 是源 INDEX 的 multi-arch 计数,**不是本地实际拉的内容**。

- **对 storage 层零影响**:Pass 1/2/3/4 GC 都没动;blob manifest 的引用关系也没动。
- **API 契约无变化**:`POST /api/pull/jobs` 的 body / 响应字节完全一致。
- **回归测试**:本轮新增 2 个单测:
  - `TestSynthesizeFilteredIndexKeepsOnlyFiltered` —— 验证只保留过滤后的 child,且 mediaType / schemaVersion 不丢
  - `TestSynthesizeFilteredIndexPreservesAnnotations` —— 验证 attestation 的 `vnd.docker.reference.digest` annotation 在合成 INDEX 里仍存在(Docker 的 SBOM/signature 链路依赖)
  - 既有 `TestPlanTransferIndexFiltering` / `TestPlanTransferEmptyAllowListMatchesAll` / `TestPlanTransferNoMatchErrors` / `TestPlanTransferSingleArchManifestUnaffected` 全部通过 —— 本轮没改 planTransfer 的过滤逻辑

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**(本轮**改动文件零新增**,前端无变化)
- **Go 侧门禁**:`gofmt -l internal/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build ./internal/... ./cmd/...` 退出码 0;`go test ./internal/... -count=1` 全 pass(includes 2 new tests)

### 生产现场复测(在 158 跑)

| 检查点 | 步骤 | 预期 |
| --- | --- | --- |
| **A. 之前已拉的多 arch** | 镜像列表点开 `library/alpine:3.19`(0.5.24 拉的) | 「架构」列仍是 `linux/amd64 +13`(旧 tag 指向原始 INDEX,新逻辑没重写已有 tag) |
| **B. 新拉的 amd64** | 「镜像拉取」重新 pull `library/alpine:3.19` → 等完成 | 「架构」列变 `linux/amd64`(无 `+N`,single-arch) |
| **C. 多平台过滤** | 设置里把拉取平台改为 `linux/amd64,linux/arm64` → 重新 pull `library/alpine:3.19` | 「架构」列显示 `linux/amd64 +1`(合成的 filtered INDEX,只有 2 个 child) |
| **D. 字节数** | pull 前后对比 `du -sb /data/cairn/registry` | B 比 0.5.24 拉的同一镜像**小** —— 因为不再有 attests 引用源的 14 个 child manifests,只保留过滤后的子集 |
| **E. attestation 链路** | `docker pull alpine:3.19` 看 SBOM/signature 是否仍可拉到 | 仍可拉(`vnd.docker.reference.digest` annotation 在合成 INDEX 里没丢) |

### 轮次与号位

- 本轮占 **0.5.25**:**defect fix**(UI 与磁盘不一致),按 `AGENTS.md` 判定为**小版本(第 3 位)+1**(既有 pull 流程的语义补正,不引入新功能模块)。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.25 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.24] - 2026-09-28

本轮主题:**Critical bug:勾选 GC「也清理 0 tag 仓库」会**递归删除整条命名空间** —— 凡是命名空间目录下挂着子仓库的,子仓库无论有无 tag 都会被一并清掉**。**已现场确认一次事故:158 上 4 个仓库(其中 2 个有 tag)被一次 GC 清零**,磁盘上 `repos/` 直接空了。**这是上线以来最严重的数据丢失 bug**。

### 修复(CRITICAL)

- **GC Pass 3 命名空间遍历 bug**(`internal/storage/filesystem.go`):

  旧实现:
  ```go
  repoEntries, _ := os.ReadDir(reposRoot)   // 取 repos/ 直接子项
  for _, entry := range repoEntries {
      repo := entry.Name()                  // "library", "webauto-push" ...
      repoDir := filepath.Join(reposRoot, repo)
      if !repoHasNoTags(repoDir) { ... }    // 检查 repos/library/tags/
      os.RemoveAll(repoDir)                 // 删 repos/library/
  }
  ```

  旧代码每一行都有问题:

  | 行 | 错在哪 | 后果 |
  | --- | --- | --- |
  | `os.ReadDir(reposRoot)` | 只取顶层,**没有递归找真仓库** | `library` 当成 repo 名字 |
  | `repo := entry.Name()` | 取的是命名空间段,不是仓库全名 | 缺 `/alpine` |
  | `repoHasNoTags(repos/library)` | 检查的路径根本不存在 tags/ | ENOENT → 当成「无 tag」 |
  | `os.RemoveAll(repos/library)` | 整条目录树递归删 | `library/alpine`(有 1 个 tag)一并清掉 |

  新实现:抽出 `discoverRepos()` helper,与 `Repositories()` 共享同一套 walk 规则 —— 递归遍历 `repos/`,找 `tags/` 或 `manifests/` 的父目录作为仓库,名字带完整命名空间前缀(如 `library/alpine`)。Pass 3 沿用同一个 helper,**只有叶子仓库被删**,命名空间目录里其他有 tag 的仓库不受影响。

- **根因 / 为什么这条 bug 走到生产**:
  - 0.5.20 引入 Pass 3 时**没复用列表接口的发现逻辑**,自己写了一段独立的 `os.ReadDir`,把 namespace 当成 repo。
  - 既有测试 / 场景只覆盖了 Pass 1 + Pass 2(`gc-real.yaml`),Pass 3 在 0.5.20 上线时**没有任何自动化场景**(CHANGELOG 0.5.20 明确写「未补 web-auto 场景,留待后续」)。
  - 158 上恰好有 `library/alpine` / `library/busybox` 这种**多段仓库名**;测试仓里全是单段名(`busybox`、`alpine`),触发不了 bug。
  - 类型层 / `gofmt` / `go vet` / `go build` / `tsc` 全过 —— bug 在逻辑层,静态检查拦不住。

- **修复后的 hard gate**:Pass 3 现在跟 `Repositories()` 共享**同一份 walk 代码**,两边永远看到同一个仓库集合 —— 这条 bug 类不可能再分叉回归。

### 变更

- **GC tooltip UX**(`web/src/pages/images-page.tsx`):长 GC 解释从「? 图标的 Tooltip」(右侧、placement=top、文本被右边裁切)搬到「运行 GC 按钮的 Tooltip」(placement=bottomLeft,出现在按钮**下方偏左**,避开按钮所在页面右上角的右边缘);Popconfirm 的 description 只留可交互的 checkbox(破坏性开关);`?` 图标 + `QuestionCircleOutlined` 整个删除(冗余 affordance)。tooltip 文案不变。

### 影响范围(升级须知)

⚠️ **数据丢失警告**(如果你跑过 0.5.20 ~ 0.5.23 的「也清理 0 tag 仓库」):

  - 0.5.20 ~ 0.5.23 任何一次该勾选都**可能误删**所有挂在命名空间下的仓库。如果你在线上跑过 0.5.20+ 的这个开关,**立刻拉 0.5.24 升级,但被删的镜像需要从上游 registry 重新拉回来才能恢复** —— 这是物理删除,没有回收站。
  - 升级本身不会撤销已发生的删除。**不要在升级前再点 GC**,直接 pull + build + up,先把这个 bug 关掉。

- **本轮 API 行为变化**:
  - `POST /api/gc` 的 body / 响应**字节兼容** 0.5.23。
  - 行为变化:Pass 3 现在**只删真仓库,不再误删命名空间目录**。空仓库检查的逻辑没动(仍是「tags/ 为空 + 无 24h 内 upload session」)。

- **未触动项(明确划线)**:
  - **零兼容策略,no deprecation period**:用户用 0.5.20~0.5.23 已经点了那个勾选就是数据丢失,**0.5.24 修了之后这条路径仍然是「破坏性」**(勾上 = 删 0 tag 仓库,不可恢复),这是设计,不是 bug。
  - **Pass 1/2/4 逻辑未动**:仅 Pass 3 的「发现仓库」从「读顶层」改成「walk 全树」,隔离。
  - **`web-auto` 场景仍缺**:CHANGELOG 0.5.20 标注「未补场景」、本轮仍是「未补场景」。强烈建议下一个 PR 起一个 `gc-empty-repos-real.yaml`,dev 上造一个 `library/<repo>` 形态的真仓库跑 Pass 3,断言**只删 0 tag 那个**,**有 tag 的兄弟仓库必须留下来**。这条场景写出来,这条 bug 类以后不会再有。

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有,本轮**改动文件零新增**。
- **Go 侧门禁**:`gofmt -l internal/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build ./internal/... ./cmd/...` 退出码 0。
- **逻辑一致性**:`Repositories()` 和 Pass 3 都通过 `discoverRepos()` 拿仓库列表 —— 同一份代码,不可能分叉。

### 轮次与号位

- 本轮占 **0.5.24**:**critical bug fix**(数据丢失),按 `AGENTS.md` 判定为**小版本(第 3 位)+1**。需要主版本(0.x → 1.x 切换)的场景是「API 路径大改 / 移除功能 / 数据格式不兼容」,这条 bug 只是修复了一个回归,不动主版本。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.24 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.23] - 2026-09-28

本轮主题:**部署命名与产品名对齐 —— env 变量、image tag、container_name、service name 全部从 `cairn` 改成 `cairn`**。0.5.21 改了产品面对用户时的名字,但运维侧的命名(`.env` 里的 env 变量、`docker compose` 里的 image / container / service)还是旧名,运维脚本和脑内记忆仍要切换两套 —— 这一轮把部署命名也跟上,做到「产品名 = env 名 = image 名 = container 名」完全一致。

### 变更

- **env 变量**:`GO_HUB_ENV` → `CAIRN_ENV`(`internal/config/config.go` 的 `Load()`,`docker-compose.yml` + `.env.example` 同步)。**无兼容期** —— 旧名不再被读,直接走 `prod` 默认值;若需要 dev 模式,必须改 `.env`。
- **docker image tag**:`cairn:0.5.22` → `cairn:0.5.23`(`docker-compose.yml` + `.env.example` 的 `IMAGE` 默认值)。**注意**:这只是 tag 字符串变了,不影响镜像内容本身;老镜像 tag 仍可继续跑(只是没人再推它)。
- **container_name + service_name**:`cairn` → `cairn`(`docker-compose.yml`)。container_name 改了,所有按 `name` 引用容器的地方(运维 `docker logs cairn` / `docker exec cairn` / `docker compose logs cairn` 等)也要改字面;service_name 跟 container_name 必须一致,否则 compose 报冲突。
- **`UserAgent` 保留 `cairn/` 前缀**:`internal/version/version.go` 的 `UserAgent = "cairn/" + Version` **不改**。理由:对外 registry 看到的 user-agent 是允许列表 / 日志关联的关键字符串,`cairn` 是大家能搜得到的项目代号(中文圈 / GitHub),改成 `cairn/` 会让上游 registry 的 allowlist 与日志关联断链。**外部可识别性优先于内部品牌一致** —— 这条决策是「运维契约」不动。
- **宿主机数据目录**:`HOST_DATA_DIR` 默认仍是 `/data/cairn`,**故意没改成 `/data/cairn`**。理由:它是宿主机上的物理路径,不是产品名的一部分;改名会让老运维脚本(尤其是迁移 / 备份 cron)找不到数据。**数据目录名跟产品名解耦**。
- **AGENTS.md / README.md / ROADMAP.md 同步**:三处文档里凡是引用旧名的地方都加注释说明「曾用名, v0.5.23 改名」;ROADMAP.md 顶部「为什么砍 14 个 env」那段历史说明里,在两个引用旁加了一句 `(v0.5.23 把 GO_HUB_ENV 改名为 CAIRN_ENV)`。

### 影响范围(升级须知)

⚠️ **这是部署面 breaking change,不是 API breaking**。HTTP API、数据格式、磁盘布局(数据目录结构)、SQLite schema 全部不变;**只有 env 变量名 + docker 命名变了**,需要按下面的清单改 `.env`。

#### 升级操作(在 158 上)

```bash
# 1. 编辑 .env,把两行改名
sed -i 's/^GO_HUB_ENV=/CAIRN_ENV=/' .env
sed -i 's|^IMAGE=cairn:|IMAGE=cairn:|' .env

# 2. 拉新代码 + 重新 build + up
git pull origin main
docker compose build --no-cache
docker compose up -d
```

不执行这两步的话:
- 旧的 `.env` 里有 `GO_HUB_ENV=dev` 但代码读的是 `CAIRN_ENV`,**dev 模式静默丢失**(回落到 `prod`)
- 旧的 `.env` 里有 `IMAGE=cairn:0.5.22` 但 compose 找不到这个 image,`docker compose up` 会报 pull 失败

#### 别名映射

| 旧(0.5.22 及以前) | 新(0.5.23) | 影响 |
| --- | --- | --- |
| `GO_HUB_ENV=dev` / `prod` | `CAIRN_ENV=dev` / `prod` | dev 模式需重设 |
| `IMAGE=cairn:0.5.22` | `IMAGE=cairn:0.5.23` | image 名需在 local 重 tag 或 pull |
| `container_name: cairn` | `container_name: cairn` | 容器引用脚本需改字面 |
| service 名 `cairn:` | service 名 `cairn:` | compose 命令行需改字面 |
| `User-Agent: cairn/<v>` | (不变) | 上游 registry 无影响 |
| 宿主机数据目录 `/data/cairn` | (不变) | 备份/迁移脚本无需改 |
| `/api/version` 返回 `version` 字段 | `version: 0.5.23`(字段值变化,字段名不变) | 监控/告警脚本无需改 |

#### 兼容性选择(明确划线)

- **不引入兼容期**(`GO_HUB_ENV` 不再被代码读):若兼容两套名,代码里要保留 `os.Getenv("GO_HUB_ENV")` 的退化路径,这条路径永远没人走,但每行读 env 的地方都要加分支,半年后没人记得为什么两套都在,**比硬切更糟**。直接硬切,这一轮 CHANGELOG 写清楚就够。
- **宿主机目录路径不改**(`/data/cairn` 仍是默认):见上文理由。
- **`UserAgent` 前缀不改**:见上文理由。
- **`cairn` 项目代号仍保留**(module path / 二进制名 / 内部代号):AGENTS.md 顶部已有说明,本轮不动。

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有,本轮**改动文件零新增**。
- **Go 侧门禁**:`gofmt -l internal/ cmd/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build ./internal/... ./cmd/...` 退出码 0。
- **环境变量对照**(`internal/config/config.go` 与 `docker-compose.yml` / `.env.example` 三处必须字面一致):
  - env 变量名:`CAIRN_ENV`(三处一致)
  - 容器内读不到旧名(`GO_HUB_ENV`),会回落到默认值 `prod`,日志显式可见
- **生产现场复测待办**:158 容器升级到 `cairn:0.5.23` 后,
  - `docker ps \| grep cairn` 应看到新 container name
  - `docker logs cairn \| grep "config loaded"` 应见 `version:0.5.23`
  - `/api/config` 返回 `"version":"0.5.23"` 与 `"userAgent":"cairn/0.5.23"`(后者不变)
  - 上游 registry(任何公网 registry)的访问日志里,user-agent 仍是 `cairn/0.5.23`(证明 UserAgent 没改)

### 轮次与号位

- 本轮占 **0.5.23**:部署命名的品牌对齐,**不做兼容期**是一次性断刀。按 `AGENTS.md` 判定为**小版本(第 3 位)+1**(既有部署流程的命名清理,不引入新功能模块)。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.23 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.22] - 2026-09-28

本轮主题:**整体配色与 Cairn Logo 对齐 —— 主色从蓝换 Teal 600,info 与 primary 同色**。0.5.21 把产品名 + Logo 改了,但应用界面仍是蓝主色 —— 这一轮把 UI 主品牌色也跟上,做到「Logo / 顶栏 / 链接 / 按钮 / 选中态」一个色。

### 变更

- **`web/src/theme.css` 浅色版主色**:`--color-primary` 从 `#155aef` 换成 Teal 600 `#0d9488`(Cairn mark 塔身色);`--color-primary-bg-active` 从 `#e1edfc`(浅蓝)换成 Teal 100 `#ccfbf1`(浅 teal);`--color-info` 从 `#1677ff` 换成 `#0d9488`(与 primary 同色 —— info 提示与主品牌色合一,避免 Harbor / Quay / Docker 那种「整屏蓝」的同质化);`--color-side-nav-text-active` / `--color-side-nav-active-bg` 同步。
- **`web/src/theme.css` 深色版主色**:`--color-primary` 从提亮蓝 `#4c8dff` 换成 Teal 400 `#2dd4bf`(色阶感跟原「蓝 → 亮蓝」一致);深色下 `--color-primary-foreground` 从 `#0b1220`(深蓝)换成 `#042f2e`(深 teal,与 teal-600 同一色族的更深档,保证按钮文字对比度);`--color-primary-bg-active` / `--color-info` / `--color-side-nav-*` 同步。
- **`web/src/main.tsx` 的 ConfigProvider LIGHT_TOKENS / DARK_TOKENS** 同步替换成对应的 teal 值。token 与 theme.css 必须**字节对应**,否则会出现「自定义 CSS 是 teal,但 antd 表格 / 弹窗 / 下拉还是蓝」的「半 teal」翻车。
- **品牌资产文件归位**:`tmps/cairn-brand.html` → `docs/cairn-brand.html`。`tmps/` 是 gitignored 临时目录,品牌资产是产品文档的一部分,放在 `docs/` 里随仓库分发,且 README 顶部新增「品牌资产」小节给出链接。
- **README「品牌资产」段**:指向 `docs/cairn-brand.html` + 引用 `cairn-mark.tsx` + `theme.css`,便于后续接手的同学顺着链接找到全部资产。

### 影响范围(升级须知)

- **视觉变化**:链接、按钮、Tab 选中态、Tag(蓝色预设)、表格选中行、Drawer 头部、Alert info / success 等等所有用 `--color-primary` 的地方都会从蓝变 teal。语义色 `success / warning / fail` 维持绿 / 黄 / 红(已建立的视觉契约,改色会误导)。文字色、边框、背景中性色完全不动。
- **API / 数据 / 配置**:零变化。
- **向后兼容**:`theme.css` token 名没改;ConfigProvider 的 token 名也没改;只是具体色值变了。组件层调用方式零改动。
- **未触动项(明确划线)**:
  - **Amber `#f59e0b` 当前只用于 Logo 标记点,未引入 UI 强调色**。考虑过用 amber 替代 warning 的 `#faad14`,但改 warning 风险(用户对红黄之争早已稳定)大于收益,留待单独 PR。
  - **未触动 `web/package.json` 的 `name: "cairn-web"`**:同 0.5.21。
  - **`internal/webui/dist/` 仍是 gitignored**,由 docker build 的 pnpm build 自动重生成。

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有,本轮 `theme.css` / `main.tsx` 改动相关行**零新增**。
- **Go 侧门禁**:`gofmt -l internal/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build ./internal/... ./cmd/...` 退出码 0。
- **色值对照**(`theme.css` 与 `main.tsx` 两边必须同值,避免「半 teal」):
  - 浅色 primary / info:`#0d9488` / `#0d9488`
  - 深色 primary / info:`#2dd4bf` / `#2dd4bf`
  - 浅色 primary-bg-active:`#ccfbf1`
  - 深色 primary-bg-active:`#2dd4bf26`
- **生产现场复测待办**:158 容器升级到 `cairn:0.5.22` 后,
  - 浏览器硬刷 → 顶栏左侧 Cairn mark 仍在,但链接、按钮、Tab 选中态、Tag 颜色从蓝变 teal
  - 切深色主题(右上角)→ 深色底上 teal 更亮(`#2dd4bf`),选中态从「亮蓝底」变成「teal 半透明底」;Cairn mark 自动反白为 teal-300
  - 警告语调 `success / warning / fail` 三色维持不变(绿 / 黄 / 红)

### 轮次与号位

- 本轮占 **0.5.22**:既有 UI 的品牌色同步优化 + 一个文档归位,按 `AGENTS.md` 判定为**小版本(第 3 位)+1**(既有 UI 的调色,不引入新功能模块)。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.22 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.21] - 2026-09-28

本轮主题：**品牌升级 —— 产品名从「镜像仓库管理」改为 Cairn,顶部 brand 替换为新 Logo**。`cairn` 仍是项目 module path / 二进制名 / 内部代号,跟对外产品名 Cairn 并存。

### 变更

- **产品面对用户时改名 Cairn**(Lightweight Container Image Infrastructure / 轻量级容器镜像基础设施平台)。原因:`cairn` 起名时参照 registry-manager 的「Go-Hub」思路,现在产品已经超越 registry-manager 的「给 registry 配个管理 UI」定位,变成「集成仓库、扫描、拉取队列、热度统计的完整基础设施」,旧名承载不了。
- **新 Logo `CairnMark` 组件**(`web/src/components/cairn-mark.tsx`):3 块圆角矩形堆叠成塔身 + 顶部琥珀色标记点。**三块石头**对应 cairn 三个能力面 —— 存储(基座)/ 拉取(中层)/ UI(顶层);**琥珀点**是「目标服务器位置标记」,也是导航隐喻。SVG 内联无外部依赖,favicon / app nav / hero 都可直接复用。提供 `size` / `variant`(light / dark)两个 props。
- **顶部 brand 区替换**(`web/src/App.tsx`):从 `<DockerOutlined /> + 镜像仓库管理` 换成 `<CairnMark size={26} /> + Cairn`。NAV_ITEMS 里的 `DockerOutlined` 是「镜像列表」菜单的 icon,不是品牌 mark,**不动** —— 跟新 mark 各司其职。
- **`<title>` 改为 `Cairn`**(`web/index.html`):浏览器 tab 上看到的标题。

### 影响范围(升级须知)

- **品牌资产统一**:`web/index.html`、`web/src/App.tsx`、`web/src/components/cairn-mark.tsx`(新增)、`README.md` 当前状态行 + 顶部描述、`AGENTS.md` 顶部「这是什么」段 —— 5 处产品名同步更新。`internal/webui/dist/index.html` 由 `pnpm build` 重新生成,不手改。
- **`cairn` 仍是 module path / 二进制名**:Docker image tag 仍是 `cairn:X.Y.Z`,compose 服务名仍叫 `cairn`,`/api/version` 返回的 `userAgent` 仍是 `cairn/<version>`。改 module path 是破坏性更大的改动,本轮不做。
- **`package.json` 的 `"name": "cairn-web"`** 是 npm package 内部名,不影响任何用户面,**不动**。
- **零功能 / 零修复**:仅品牌资产变更,API 契约、数据格式、磁盘布局、配置项均未触动。
- **未触动项(明确划线)**:
  - **未提供 PNG / ICO favicon**:本轮只交付 inline SVG,适合嵌入 web 与高 DPI 渲染。需要 favicon.ico 的场景另起一个 PR 跑 sharp 或 Inkscape 转码。
  - **未替换登录页 logo**(cairn dev 模式目前没有登录页,无需改)。
  - **`NavOutlined` 等菜单 icon** 仍是 antd 默认集,菜单的视觉风格没改 —— 只动顶部 brand 区。
  - **历史 CHANGELOG 条目不动**:0.5.20 及之前的「镜像仓库管理」字样保留(那是描述当时的事实)。

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有(api.ts 三处未 import 类型 / settings-page.tsx 五处未使用声明),本轮 `cairn-mark.tsx` / `App.tsx` / `index.html` 改动相关行**零新增**。
- **Go 侧门禁**:`gofmt -l internal/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build -tags webui` 退出码 0(bundle 自动从 `web/src/` 经 `pnpm build` 重新生成 dist,本轮不需要直接 build web)。
- **生产现场复测待办**:158 容器升级到 `cairn:0.5.21` 后,
  - 浏览器 tab 标题:`Cairn`
  - 顶栏左侧:浅 teal Cairn mark + `Cairn` 文字 + `v0.5.21` 徽章(版本号同步)
  - 「镜像列表」菜单 icon **仍是** DockerOutlined(故意不动)
  - 暗色主题(右上角月亮按钮):Cairn mark 自动反白为 teal-300 + amber-400

### 轮次与号位

- 本轮占 **0.5.21**:纯品牌资产变更,按 `AGENTS.md` 判定为**小版本(第 3 位)+1**(既有产品的标识变更,无功能 / 修复)。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.21 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.20] - 2026-09-28

本轮主题：**给 GC 加一个可勾选的「清理 0 tag 仓库」,让 0 tag 仓库彻底从镜像列表上消失**。背景:用户接入自动化测试时留下 `webauto-pull/busybox` 这种 0 tag 仓库 —— tag 已全部删除,但 `repos/<repo>/` 目录、孤儿 manifest body、blob 字节全部残留,既占清单一格,也占磁盘空间;旧版 GC 对此完全无效。

### 新增

- **GC 弹窗新增「也清理 0 tag 的仓库」勾选框**（`web/src/pages/images-page.tsx`）:**默认不勾**,保留 v0.5.18/v0.5.19 的纯 blob 清理行为;勾上之后才走破坏性更强的整仓库删除路径。Popconfirm 的 description 段把警告字面写出来 ——「会删除整个仓库目录,不可恢复」—— 让操作员在点「运行」之前能看到自己点的是什么。Tooltip 也同步加了一句话说明这个新开关。
- **GC 加 Pass 3（删空仓库目录）+ Pass 4（二次 blob 回收）**（`internal/storage/filesystem.go`）:Pass 3 扫 `repos/`,对每个目录三重检查后才删 —— ① `tags/` 为空(或不存在);② `uploads/<repo>/` 里没有 startedat 在 24h 之内的会话（保护正在 push 的仓库）;③ 拿 `lockRepo` 防并发。删除前先用 `filepath.Walk` 累加目录 size,记到 `GCResult.EmptyRepoFreedBytes` 给前端展示。Pass 3 删掉的孤儿 manifest body 之前引用的 blob 现在真正变成孤儿,Pass 4 再扫一遍 Pass 1 把这些字节真正回收 —— 这是 0 tag 仓库能释放空间的关键（光删目录不回收 manifest 引用的 blob,字节还在）。
- **`POST /api/gc` 接受可选 body `{"cleanEmptyRepos": true}`**（`internal/api/handlers_extra.go` 的 `RunGC`）:默认 false → 走 v0.5.18 行为,byte-for-byte 兼容;true → 走 Pass 3/4。空 body / 缺字段 / 字段值缺失 都按 false 处理,**不破坏任何既有客户端**。
- **`GCResult` 加 `removedEmptyRepos` 与 `emptyRepoFreedBytes`**（`internal/storage/storage.go`）:两者都用 `omitempty` —— 默认请求路径下响应里**完全不出现**这两个字段,JSON 形状与 v0.5.18 一致;只有请求带 `cleanEmptyRepos=true` 才会有。
- **`ProxyTestResult` 同款扩展**(no,this is the GC entry):Go side,前端 types 加 `removedEmptyRepos?: string[]` / `emptyRepoFreedBytes?: number`;前端 `api.ts` 的 `runGC` 现在接受 `GCOption` 参数,默认 `{}`（保持旧调用形态）。
- **GC 跑完自动刷新镜像清单**(`load(false)`):勾选状态下删了空仓库之后,清单会变短 —— 不刷一下用户会以为 GC 没生效。这个刷新只在「勾选了且真有删除」时发生,默认路径不刷。
- **`formatRepoList` helper**(`images-page.tsx` 底部):>3 个截断成「前 3 + 等 N 个」,与镜像清单顶部 error 列表的展示风格一致。

### 影响范围（升级须知）

- **零侵入的默认行为**:**不勾** 弹窗里的 checkbox,GC 的行为与 v0.5.18 完全一致 —— Pass 1 删孤儿 blob、Pass 2 删 24h+ 孤儿上传,**不会碰任何仓库目录**。所有「老操作员按旧习惯点 GC」的路径不受影响。
- **新开关打开后的语义变化**:勾上之后,镜像列表上 0 tag 的仓库会被物理删除（不是「隐藏」,是从磁盘抹掉）。删后无法恢复 —— 与 `DeleteRepository` 同级破坏性。任何生产环境如果想在删之前再 review 一次,先**不勾**跑一次 GC 看 `removedBlobs / freedBytes` 是不是符合预期,然后再勾一次干。
- **API 契约向前兼容**:响应字段顺序、空 body、缺字段路径都保持旧形状;只有客户端主动传 `cleanEmptyRepos=true` 才看到新字段。
- **未触动项（明确划线）**:
  - **未补 `web-auto` 自动化场景**:0.5.18 起 `gc-real.yaml` 已经能覆盖空态分支;要覆盖「勾选后删空仓库」需要在 dev 注册表里先造一个 0 tag 仓库场景。本轮未补,与 0.5.19 一致记入未触动项,留待单独 PR。
  - **未补 `storage/filesystem_test.go`**:现有 storage 包无单测文件(其他包也以集成测试为主),开新单测覆盖 Pass 3 的三条分支（0 tag + 老 upload / 0 tag + 新 upload / 有 tag）需要新搭脚手架,本轮未做。
  - **`removedEmptyRepos` 数组顺序未做排序保证**:返回顺序由 `os.ReadDir` 决定,UI 展示时直接用 —— 若有强顺序诉求,后续可加 sort.Strings,但当前为「原样回报」更便于定位。

### 验证

- **类型层**:`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有(api.ts 三处未 import 类型 / settings-page.tsx 五处未使用声明),本轮 `images-page.tsx` / `api.ts` / `types.ts` 改动相关行**零新增**。
- **Go 侧门禁**:`gofmt -l internal/storage/ internal/api/` 无输出;`go vet ./internal/... ./cmd/...` 退出码 0;`go build ./internal/... ./cmd/...` 退出码 0。
- **默认路径契约不变**:不传 body / 传 `{}` / 传 `{"cleanEmptyRepos":false}` 三个路径,响应 JSON 应与 v0.5.18 完全一致(`removedBlobs` + `freedBytes` 两个字段,无 `removedEmptyRepos` / `emptyRepoFreedBytes`)。
- **生产现场复测待办**:158 容器升级到 `cairn:0.5.20` 后,
  - 默认路径:点「运行 GC」→ toast 文案与 0.5.19 一致(`清理 N 个孤儿 blob,回收 X.XX MiB`),清单不刷新。
  - 新路径:勾「也清理 0 tag 的仓库」→ 点「运行」→ `webauto-pull/busybox` 应被物理删除;toast 文案类似「GC 完成:清理 N 个孤儿 blob,回收 X.XX MiB;清空 1 个空仓库(webauto-pull/busybox)」;镜像清单上这一行消失,其它行不动。

### 轮次与号位

- 本轮占 **0.5.20**:既有 GC 能力的扩展 + 一个用户可控的破坏性开关,按 `AGENTS.md` 判定为**小版本（第 3 位）+1**。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.20 之后顺延一格;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

---

## [0.5.19] - 2026-09-28

本轮主题：**修掉「代理测试把 Docker Hub 匿名 /v2/ 的标准 401 误报成连通失败」**。现象是编辑代理时点「测试连接」、目标填 `https://registry-1.docker.io/v2/`,弹窗直接红字「连通失败」——但同样这条代理在拉取任务里是能正常工作的。

### 修复

- **代理测试判定口径**（`internal/api/handlers_extra.go` 的 `proxyTestThrough`）：旧逻辑把任何 4xx/5xx 当成 `ok: false`，再叠加 `ok:false → 红色"连通失败"` 的 UI 分支（`web/src/pages/proxies-page.tsx` 的 `ProxyTestAlert`），于是 Docker Hub / ghcr.io / quay.io 对匿名 `GET /v2/` 的标准应答 **HTTP 401**（`WWW-Authenticate: Bearer ...` Bearer 挑战，按 Registry V2 协议就是这条路径的"正常应答"）被误判成代理不可用。判定口径改写为**「代理能不能把请求送到目标并拿回应答」**：任何 HTTP 响应（1xx/2xx/3xx/4xx/5xx）都证明代理 + TLS 转发链路是通的，只有 transport 层失败（`client.Do` 返回 err：超时、拒连、TLS 握手失败）才报 `ok: false`。状态码语义保留在 `status` / `statusText` 字段，并新增 `note` 字段给前端一句话解释：401 → 「目标要求认证（HTTP 401）；代理可达,目标在线」；403 → 「目标拒绝访问」；404 → 「目标路径不存在」；5xx → 「上游异常」；其他 4xx → 通用回退。
- **`registryApiVersion: "2"` 不再被 401 吃掉**：原来仅当 `StatusCode < 400` 才标注 `registryApiVersion`，这导致对 `/v2/` 端点的 401 应答丢失「对面是 Registry V2」的事实信号。现对 `/v2/` 端点的 **401 单独也标 `registryApiVersion: "2"`**——Docker Registry V2 协议对匿名 `/v2/` 的 401 + `WWW-Authenticate: Bearer` 头是这套协议对"我在跑 registry"的明牌。

### 变更

- **前端 `ProxyTestAlert` 着色按状态码分档**（`web/src/pages/proxies-page.tsx`）：原来 `ok:true` 一律绿、`ok:false` 一律红；现在 2xx 绿、4xx **蓝**（代理可达,目标按业务规则拒绝）、5xx **黄**（代理可达,上游异常）、传输失败仍红。这样「代理能用,目标要认证 / 路径写错 / 上游挂了」三种状态在同一弹窗里能一眼分清，避免「明明能拉镜像却被红字吓到」的体验。
- **`ProxyTestResult` 类型加 `note?: string`**（`web/src/types.ts`），前端把后端的解释原文展示在 description 区域。

### 影响范围（升级须知）

- **受影响版本为 v0.5.0 ~ v0.5.18**：`proxyTestThrough` 在 v0.5.0 引入（与 `TestProxy` 端点同期上线，见 `internal/api/handlers_extra.go:1018`），4xx → `ok:false` 的判定从那以后一直在。
- **触发条件与观感**：任何「用代理拉公网 registry」的代理条目，编辑弹窗里点测试、目标填 `https://<公网 registry>/v2/` 都会撞红。所有「拉 Docker Hub 用」的代理——也就是日常最高频的那一类——100% 命中。
- **升级后行为变化**：
  - 之前红字「连通失败」、但代理其实能用 → 升级后**蓝字**「代理可达 · HTTP 401 · X ms」+ 描述行说明「目标要求认证」；
  - 真失败的（拒连 / 超时 / TLS 错）依然红字「连通失败」+ 描述给出原始 error，行为不变；
  - 上游 5xx（代理通了、目标挂）→ 升级后**黄字**「代理可达,上游异常 · HTTP 503」+ note，区别于「代理本身挂了」的红字。
- **API 契约兼容**：`ok` 字段语义**实质变了**——原来「目标业务侧成功」,现在「代理把请求送到了」。任何仍依赖旧语义的代码 / 测试需要同步改（目前看 grep 没找到外部消费者，主要是 UI 自己用）。
- **磁盘格式 / 配置 / 路由 / 权限**：均未触动。
- **未触动项（明确划线）**：
  - `POST /api/proxies/<id>/test` 与 `POST /api/proxies/test` 的端点形状不变（始终 200 + 信封），仅信封里的 `ok` 判定规则变了。
  - `Probe`（v0.5.15 起的 TCP-only 探测）不受影响，那条链路只看「TCP 能不能连上 ip:port」,不读 HTTP 响应。
  - 拉取任务的事前探测 (`ProbePullSource`) 是另一条独立路径,也不受这条改动影响。
  - **未补 `web-auto` 自动化场景**：「测连接」在 dev 没有该走的端到端断言（需要先在 dev 起一个会按需返回 401/200/5xx 的 mock registry,场景覆盖成本高于本轮主题）。CHANGELOG 里明确记一笔,留给后续单独 PR。

### 验证

- **类型层**：`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**,全部为基线既有,本轮 `proxies-page.tsx` / `types.ts` 改动相关行**零新增**。
- **Go 侧门禁**：`gofmt -l internal/api/` 无输出；`go vet ./internal/api/...` 退出码 0；`go build ./internal/api/...` 退出码 0。
- **协议事实**：`registry-1.docker.io/v2/` 对匿名 GET 的标准应答是 401 + `WWW-Authenticate: Bearer realm="registry-1.docker.io"`,这是 Docker Registry V2 协议规定的**正确应答**,与 AGENTS.md §V2 协议事实「`/v2/` 的挑战不带 scope,必须支持申请无 scope 的 token」对应。
- **生产现场复测待办**：158 容器 `cairn:0.5.18` 升级到 `cairn:0.5.19` 后,编辑 `<proxy>` 代理 → 测试目标 `https://registry-1.docker.io/v2/` → 应弹蓝字「代理可达 · HTTP 401 · X ms」+ 描述「目标要求认证（HTTP 401）；代理可达,目标在线」;同条代理在拉取任务里行为不变,继续能拉。

### 轮次与号位

- 本轮占 **0.5.19**：纯缺陷修复,按 `AGENTS.md` 判定为小版本（第 3 位）+1。**两轮升级各管一件事**：0.5.18 = GC toast undefined/NaN（前端解码层）;0.5.19 = 代理测试 401 误报（后端判定口径 + 前端着色）。按 `docs/ROADMAP.md`「号位是预留」的规则,整表自 0.5.19 之后顺延一格；`0.6.0`（TLS 证书管理）由人指定,不随顺延改号。

---

## [0.5.18] - 2026-09-28

本轮主题：**修掉「GC 成功提示显示 `undefined` / `NaN`」并补 GC 含义入口**。`gc-real` 验收场景在 0.5.17 验收报告里以 **P2**（中）记录：toast 出现 ≠ toast 内容正确，断言形状才暴露该解码缺陷。

### 修复

- **`web/src/pages/images-page.tsx` 的 `runGC` 调用解码层**：旧代码直接读 `r.removedBlobs` / `r.freedBytes`，但 `runGC()` 真实返回类型是 `Promise<ApiResult<GCResult>>`（见 `web/src/api.ts:238-239` 与 `web/src/types.ts:170-174`），`r` 是信封 `{ success, code, message, data: { removedBlobs, freedBytes } }`。前端没解 `data`，于是 `r.removedBlobs === undefined`、`undefined / 1024 / 1024 === NaN`，toast 显示成「清理 undefined 个孤儿 blob，回收 NaN MiB」——这是 0.5.17 报告里 **P2** 的根因链。后端契约是对的（`internal/api/handlers_extra.go:653-668` 经 `writeJSON` 信封正确写出 `{ removedBlobs, freedBytes }`），属纯前端解码层缺陷。
- **同一回调里补 `r.success` 分支**：`runGC()` 在 API 失败时**不抛异常**（返回 `{ success: false, ... }`），旧代码 `try/catch` 永远落 `message.success` 分支，失败也被报成「清理 undefined 个」。现改为先 `if (!r.success) message.error(...); return;`，再读 `r.data`，再判空态。

### 新增

- **「运行 GC」按钮右侧加 `?` 提示图标**（`QuestionCircleOutlined` + `Tooltip`）。Tooltip 文案：`GC = Garbage Collection。扫描并清理孤儿 blob（被废弃的上传会话、被解除引用的层），释放磁盘空间。注意：删除 manifest 只是解除引用，真正的磁盘空间要 GC 才回收。` —— 直答用户的「运行 GC 是什么意思」，并把 README/AGENTS.md 里那条「删 manifest 只解除引用、要 GC 才回收磁盘空间」的协议事实前置到点击之前。
- **空态分支文案**：GC 跑完若 `removedBlobs === 0`，toast 显示 `GC 完成：没有需要清理的孤儿 blob`，替代旧分支里的「清理 0 个孤儿 blob」（也是合理显示，但 GC 语义下「没有需要清理」更直观，且便于 UI 区分「没东西可清」与「清掉了 0 个但仍跑了一次」）。

### 影响范围（升级须知）

- **受影响版本为 v0.5.0 ~ v0.5.17**：`runGC` 声明于 v0.5.0（`web/src/api.ts` 的同源注释）；自那以后该解码一直有缺陷，但因为 0.5.17 之前的 `gc-real` 场景只断言 toast 出现（不断言内容形状），QA 层面表现为**假绿**。**升级到 0.5.18 后这条出口才真正可用**，用户才能看到 GC 到底清掉了几 MB / 几个 blob。
- **空态分支的语义变化**：升级前 GC 跑空也会出现「清理 0 个孤儿 blob，回收 0.00 MiB」，字面是真但读起来像「白跑了一次」；升级后空态显示「没有需要清理的孤儿 blob」，跑空 ≠ 跑失败。
- **API 契约未变**：后端响应字段 `removedBlobs` / `freedBytes` 不变；只是前端终于正确解码。磁盘格式、配置项、`/api/gc` 路由与权限均未触动。
- **未触动项（明确划线）**：
  - **`POST /api/gc` 未纳入 `allowDelete` 门控**（0.5.17 报告的 R-open-2）：本轮只修前端解码 + UX，安全门控是另一条独立修复路径，留待后续。
  - **GC 的可视进度**仍是「loading toast + 一次性结果 toast」，没有中间进度。

### 验证

- **类型层**：`web/tsconfig.json` 下 `tsc --noEmit` 仍为 **8 条错误**，全部为基线既有（`api.ts` 三处未 import 类型 `DeleteRepositoryPayload` / `DeleteManifestPayload` / `GCResult`；`settings-page.tsx` 五处未使用声明），本轮 `images-page.tsx` 改动相关行**零新增**。
- **场景断言回归**：`tests/web-auto/scenarios/gc-real.yaml` v2（断言 toast **内容形状**为 `GC 完成：清理 [0-9]+ 个孤儿 blob，回收 [0-9]+\.[0-9]{2} MiB`）将在 0.5.18 上由红转绿；v1（仅断言 toast 出现）保持绿。空态分支需新场景覆盖（`removedBlobs === 0`），本轮未补，留待后继。
- **生产现场复测待办**：158 容器 `cairn:0.5.17` 升级到 `cairn:0.5.18` 后，跑 `POST /api/gc` → toast 文本不再含 `undefined` / `NaN`；当注册表已无孤儿 blob 时，toast 显式显示「没有需要清理的孤儿 blob」。

### 轮次与号位

- 本轮占 **0.5.18**：缺陷修复 + 既有功能优化（`?` 提示 + 空态文案均属既有 UI 的补强），按 `AGENTS.md` 判定为**小版本（第 3 位）+1**。按 `docs/ROADMAP.md` 的号位顺延规则，整表自 0.5.18 之后顺延一格（0.5.19 / 0.5.20 / ...）；`0.6.0`（TLS 证书管理）由人指定，不随顺延改号。

---

## [0.5.17] - 2026-09-27

本轮主题:**修掉「没配代理也显示源可达、入队后却卡在拉取中」**。用户报的现象是「增加队列时检测没过关,结果加进去又一直卡着」,但**后端一直是如实报告的**——把同一份探测请求直接打到 158 上,它回的清清楚楚是失败:

```
ok:false, elapsedMs:5000,
error:"registry: GET /v2/: Get \"https://registry-1.docker.io/v2/\": context deadline exceeded (Client.Timeout exceeded while awaiting headers)"
```

**失败的是前端:它拿到这份响应后不看 `ok`,直接画了绿勾「源可达」。** 这是本轮第一层成因(P0)。第二层(P1):`/v2/` 这一个裸端点**能通只说明「源 registry 存在」**,既不说明「你要拉的镜像存在」,也不说明「这条链路真能取到 manifest」;于是「配了代理但 tag 写错」这类输入,后端探测会给出**假阳性** `ok:true`(158 实测,见「验证」)。两层叠加,就得到了「检测通过 → 入队 → 永远卡住」。

### 修复

- **前端改为消费后端结论**(`web/src/api.ts`、`web/src/pages/pull-page.tsx`):探测响应的 `ok` / `error` 此前**从未被读取过**(`git log -S 'data.ok' -- web/src/pages/pull-page.tsx web/src/api.ts` 输出为空,即**自 v0.2 引入该探测起一直是死字段**;`api.ts` 的返回类型里甚至在 TS 层面就没有这两个字段)。现在判定改为 **`ok !== false` 才通过**:`ok === false` 时结论行落 `state:'failed'`,原文展示后端给出的原因(标 `源端预检未通过`),入队按钮保持禁用。拦截线是 **`ok === false` 而不是 `!ok`**——缺字段的旧/异常响应不会被误判成失败。
- **后端补上源镜像级校验**(`internal/api/handlers_extra.go` 的 `ProbePullSource`):`/v2/` 探测成功后,再按**与拉取任务完全相同的规则**去取一次源 manifest——同一个 `splitSourceRef` 解析、同一个 `pull.QualifySourceRepo` 归一化仓库名(Docker Hub 的 `alpine` → `library/alpine`)、同一套凭据、同一个代理(`registry.NewClient`)。取到即 `sourceExists:true` 并回报 `sourceDigest`。失败分两种文案:
  - **源镜像不存在**(`registry.Error.IsNotFound()`,即 404 MANIFEST_UNKNOWN):`ok:false` + 「源 registry 可达,但源镜像 `<repo>:<tag>` 不存在——请检查镜像名与 tag 拼写」。这正是「配了代理、tag 打错」的场景——旧版会在这里给出 `ok:true`。
  - **其它取用失败**(认证 401、代理不通、超时等):`ok:false` + 「源镜像 `<repo>:<tag>` 探测失败:`<原因>`」。
- **前后端契约对齐**(`internal/api/handlers_extra.go`):`web/src/types.ts` 的 `DestStatus` 一直声明着 `sourceRepo` / `sourceTag` / `sourceExists` / `sourceDigest` / `identical` / `probeError`,而 Go 侧**零产出**——声明了却永远收不到。现在 `dest` 段真实注入这四个源侧事实,并据此计算 `identical`(目标 tag 已有 digest **且**与源 `sourceDigest` 相同 → 内容一致,不必重拉)与 `willReplace`;**dest 侧探测失败时同时给出 `destError` 与兜底 `dest`(带 `probeError`)**,前端黄色告警接受两种来源(`dest?.probeError ?? probeResult.destError`)——旧版只给 `destError` 一个字段,另一条路径取不到。
- **`elapsedMs` 改在响应组装末尾计算**:旧版在 `/v2/` 探测后立刻取值,新增的 manifest 校验耗时不在其内;现在覆盖全程(前端目前不展示该字段)。

### 文档

- **校准 README 的版本面**:`README.md` 的「当前状态」标题与「当前版本」行都停在 `v0.5.15`。经查 **0.5.16 轮的提交 `c2a1319` 整份没有改动 `README.md`**——`AGENTS.md`「一次改动要同时更新这几处」中的 README 这处被漏掉了(该轮其余四处均已改)。本轮一并校正到 `0.5.17`。
- README 的构建示例用的是不带版本的 `cairn:dev`,无镜像 tag 需要同步(已逐行核对 `docker build` / `IMAGE` 相关行)。

### 影响范围(升级须知)

- **受影响版本为 v0.2 ~ v0.5.16**(探测能力与前端调用同在 `d232aee`「feat: v0.2 …」引入,而 `data.ok` 自那以后从未被读取)。0.5.16 及更早的版本里,**「源可达」绿勾是一个不反映后端结论的装饰**:后端说 false 它也画绿勾。
- **触发条件与观感**:不配代理访问 Docker Hub 这类「源 registry 可达、但你这条链路取不到」的最常见;表现为入队后任务停在拉取中直到失败(158 现场一次 `library/alpine:3.15` 的任务在 **61.7s** 后以 `manifest failed` 收场、状态 `cancelled`——拉取阶段的超时/取消语义本轮未改,见「已知遗留」)。
- **升级后行为变化**:原本「检测通过、入队卡住」的输入,现在会在**入队前**就红字拦住并说明原因;原本「配了代理但 tag 写错」也能通过的输入,现在会被明确指出是镜像名/tag 拼写问题。这两类都是**从假绿变真红**,不是新增限制。

### 已知遗留(本轮未改)

- **`asErr` 恒为 false**(`internal/registry/client.go:374-384`):该函数无条件返回 `false`,使调用方无法据此区分错误类型。本轮未改(涉及的调用面比本轮主题大,需单独评估)。
- **拉取阶段超时/快速失败未改造**:本轮只修「入队前的预检」,不含「入队后卡多久」。源不可达时任务仍会走到拉取阶段的超时才收场(现场 61.7s)。
- **不引入「全局代理回落」**:没配代理就是直连,不静默改用某个已存代理——那会把「配错」变成「猜对」,更难排查。
- `UpdateProxy` 用空密码会清掉已存密码(`internal/api/handlers_extra.go`):0.5.15 已记录,本轮未改(修复要变更已文档化的 PATCH 语义)。
- 设置页 5 处 `TS6133` 未使用声明仍在基线里,未清理。

### 验证

- **生产现场对照(158,`cairn:0.5.16`,升级前)**——同一份探测请求直接打到 API,绕开前端:
  - 无代理 + `library/alpine:3.16` → **`ok:false`**,`elapsedMs:5000`,错误为 `context deadline exceeded`(158 无直连外网)→ **后端如实报失败,前端却显示绿勾**,此即 P0 铁证。
  - 走代理(`<proxy>`,`http://proxy.example.com:7890`)+ **不存在的 tag** `library/alpine:9.99-nope` → **`ok:true`** ❌ → 旧版自身误报,此即 P1 铁证。
  - 走代理 + 真实 tag `library/alpine:3.16` → `ok:true`,`elapsedMs:2684`,响应里**没有任何**存在性/digest 信息(旧契约)。
- **前端源码铁证(升级前源码)**:`pull-page.tsx` 的探测回调为 `if (result.success && result.data) { setProbeResult({ state:'ok', … }) }`——**只要 HTTP 成功就画绿勾**;`api.ts` 的 `probePullSource` 返回类型不含 `ok`/`error`/`sourceExists`/`sourceDigest`/`destError`。新文案在旧源码中计数为 0(`源端预检未通过` 0/2、`正在校验源镜像是否可拉取` 0/1)。
- **本地隔离实例冒烟**(独立端口 `18787` + 独立数据目录,二进制 `version=0.5.17`):
  - A. 无代理 + `library/alpine:3.19`(开发机可直连 Docker Hub)→ `ok:true`、`sourceExists:true`、`sourceDigest:sha256:6baf43584bcb…`、`elapsedMs:2110`。
  - B. 带代理(`http://127.0.0.1:7890`)+ 同一真实镜像 → `ok:true` + digest(两条链路都验证)。
  - C. 带代理 + **不存在的 tag** `library/alpine:9.99-nope` → **`ok:false`**、`sourceExists:false`、文案为「源 registry 可达,但源镜像 library/alpine:9.99-nope 不存在——请检查镜像名与 tag 拼写」——即 P1 的假阳性已被消除。
  - D. 带代理 + 真实镜像 + `destRepo`/`destTag` → `dest` 段真实注入 `sourceRepo`/`sourceTag`/`sourceExists`/`sourceDigest`,并给出 `exists:false`、`willReplace:false`(本地未配自身仓库,`probeDest` 走 `storage.ErrNotFound` 分支)。
  - E. `sourceRef:"library/alpine"`(不带 tag)→ `ok:true` 且不带源侧字段;经查前端表单校验(`pull-page.tsx`)强制要求 tag,**该输入在 UI 上不可达**,故后端不加 tag 兜底。
- **门禁**:`gofmt -l internal/ cmd/` 无输出;`go vet ./internal/...` 退出码 0;`go build ./...` 退出码 0;`go build -tags webui -o /tmp/cairn-gate2 ./cmd/server` 退出码 0;`go test -count=1 ./internal/...` 全部 `ok`;前端 `tsc --noEmit -p web/tsconfig.json` 与 `c2a1319` 基线**逐条 diff 一致(10 条 → 10 条,零新增)**,其中 5 条在 `settings-page.tsx`、3 条在 `api.ts`(行号 :86/:93/:100,均为未改动的既有 `TS2304`)、2 条在 `images-page.tsx`,本轮改动的 `api.ts` / `pull-page.tsx` 新增行零错误。

### 升级后复测(158 生产)

- **复测环境**:生产机 `registry.example.com`,容器 `cairn:0.5.17`,镜像 ID `a7804a9ee3e3`(≠ 0.5.16 的 `0d3278b8ff77`),`/api/config` 返回 `"version":"0.5.17"`、`allowPull:true`。以下每条都是**升级后**真实请求的返回:
- A. **无代理 + `library/alpine:3.16`**(用户报障场景)→ `ok:false`、`elapsedMs:5001`、`context deadline exceeded`——与升级前一致:源不可达就必须拦下,不再放行入队。
- B. **走代理 + 真实 tag `library/alpine:3.16`** → `ok:true`、`sourceExists:true`、`sourceDigest:sha256:452e7292acee…`、`elapsedMs:2944`(升级前同一请求只回 `ok`/`apiVersion`/`host`/`elapsedMs`,**没有任何存在性/digest 信息**)。
- C. **走代理 + 不存在的 tag `library/alpine:9.99-nope`** → **`ok:false`**、`sourceExists:false`、文案「源 registry 可达,但源镜像 library/alpine:9.99-nope 不存在——请检查镜像名与 tag 拼写」——**升级前同一请求返回 `ok:true`**,即本轮修复的直接目标在生产上已被翻转。
- D. **目标仓库不存在该 tag**(`destTag=3.16`)→ `dest` 段真实注入 `sourceRepo`/`sourceTag`/`sourceExists`/`sourceDigest`,并给出 `exists:false`、`willReplace:false`——升级前这些字段在响应里**根本不存在**(前端 `DestStatus` 声明了却永远收不到)。
- E. **目标仓库已有该 tag**(`destTag=3.19`)→ `exists:true`、`existingDigest:sha256:6baf43584bcb…`、`identical:false`、`willReplace:true`。
- F. **前端产物已换新**:`/assets/index-DFhQ-Nyq.js`(1202788 字节,md5 `e760eff581aab7e1537e0009e9f02f09`)取代升级前的 `/assets/index-ZcXUjQDW.js`(1202498 字节,md5 `a9167c8273719b506d5b6089ae5afc71`);新文案 `源端预检未通过`、`正在校验源镜像是否可拉取` 在新 bundle 中各命中 **1 处**(旧 bundle 命中 0 处)。
- G. **端到端正向**:走代理 `POST /api/pull/jobs`(`library/alpine:3.16`,dest 写回同仓库同 tag)→ 任务 **succeeded**,19.5s、2 个 blob / 2809308 字节,`finalDigest:sha256:452e7292acee…` 与预检返回的 `sourceDigest` **完全一致**——预检说能拉,真拉下来就是同一个 digest。
- **上线三道闸**:① 源码闸 `HEAD=a16c667` 且 `internal/version/version.go` 为 `0.5.17`;② 镜像闸新 tag 镜像 ID `a7804a9ee3e3` ≠ 上一版 `0d3278b8ff77`(排除「缓存假构建」);③ 内容闸 `docker run --network none` 启动日志 `"msg":"config loaded","version":"0.5.17"`(scratch 镜像无 shell,只能靠启动日志验版本)。
- **回滚路径**:升级前 `.env` 已备份为 `.env.bak.pre0517`(内容即 0.5.16 的 `IMAGE=cairn:0.5.16`),回滚即 `cp -a .env.bak.pre0517 .env && docker compose up -d`。

### 兼容性

- **API 的 `ok` 字段语义未变**,只是**终于被前端真正消费**;响应新增 `sourceRepo`/`sourceTag`/`sourceExists`/`sourceDigest` 与更丰富的 `dest`(均为新增字段,旧客户端忽略即可)。
- **磁盘格式与配置未变**:纯请求处理路径的改动,无迁移、无配置项增减。
- ⚠️ **升级后首次预检变慢属正常**:源镜像级校验比裸 `/v2/` 多一次 manifest 往返(本地实测总计约 2.1~2.7s,受网络影响);`elapsedMs` 已覆盖全程。
- ⚠️ **升级后「预检通过」的门槛实质变高**:以前「源 registry 通」就算过,现在要「这个镜像取得回来」才算过。若某条链路此前一直靠假绿通过,升级后会被挡住——这是本轮的目标行为。

### 轮次与号位

- 本轮占 **0.5.17**:纯**缺陷修复**(前端误报 + 后端探测口径不足),按 `AGENTS.md` 判定为小版本(第 3 位)+1。
- 因该 hotfix 优先于原定的「韧性轮」,按 `docs/ROADMAP.md`「号位是预留,不是承诺……本表自上而下整体顺延」的规则**整表顺延**:韧性轮 0.5.17 → **0.5.18**、工程化 0.5.18 → **0.5.19**;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

## [0.5.16] - 2026-09-27

本轮主题:**修掉「拉取任务点完「添加」就从列表消失、且看不到任何历史」**。现象看着像前端不刷新、像数据库没写,实际是**进程被一次空指针 panic 打死了**:只要拉取成功拿到源 manifest,`Orchestrator.RunOne` 就会去调一个**永远为 `nil`** 的函数字段,当场 SIGSEGV;Go 的 panic 不 recover 就是整个进程退出,容器被 `restart: unless-stopped` 拉起,而任务表在**内存**里,重启即清空——任务不是「消失」,是**承载它的进程没有了**。这也正是「没有历史任务」的直接原因:历史本来就只在内存,进程一死什么都没留下。

### 修复

- **删除 `Orchestrator.pullPlatforms` 字段,改为方法 `platformAllow()`**(`internal/pull/executor.go`):该字段**未导出**,而 `Orchestrator` 的**唯一构造点在另一个包**(`internal/server/server.go`),外部包无法给它赋值,所以它从**引入之日起恒为 `nil`**;`RunOne` 在成功取得源 manifest 之后无保护地调用它 → 空指针解引用。现在改为 `func (o *Orchestrator) platformAllow() []string`,内部读**实时**配置 `o.Cfg.PullPlatforms()`,并带 `o == nil` 与 `Cfg == nil` 保护。**语义与原意图完全一致**(未配置返回 `nil` = 不过滤平台),但**从结构上消灭了「忘记接线」这一类缺陷**——不再有任何需要跨包赋值的字段,`internal/server/server.go` 因此无需改动。
- **队列增加 panic 兜底 `runJobGuarded`**(`internal/pull/queue.go`):`executeOne` 现在经 `runJobGuarded` 调用编排器,后者用 `recover()` 接住 panic,记一条 `slog.Error("pull job panicked", …)`(**带完整 `debug.Stack()`**)并把该任务置为 `failed`(文案 `internal error: <值>`)。**一个任务的 bug 从此只降级一个任务,不再拖垮整个进程**——这才是「任务消失」真正的放大机制。

### 影响范围(升级须知)

- 该缺陷由提交 `8836c92`(写的是 v0.6.0 拉取平台白名单,后被 `e46dc3c` 并入 `[0.5.8]` 小节)引入。`git show <release>:internal/pull/executor.go | grep -c pullPlatforms` 实测:**0.5.7 = 0 次,0.5.8 / 0.5.9 / 0.5.11 / 0.5.14 / 0.5.15 均为 3 次**。即**受影响版本为 0.5.8 ~ 0.5.15**,0.5.7 及更早不受影响。
- **触发条件既窄又具欺骗性**:必须**成功取得源 manifest**才会走到崩溃点。源地址写错(404)、认证失败(401)、代理连不通时,代码在更早的位置就 `return` 了,**怎么复现都复现不出来**——这就是它在 0.5.8 之后一直没被发现的原因(包括上一轮 0.5.15 的冒烟:当时验的是「探测」而非真实拉取,根本没走到这一行)。

### 已知遗留(本轮未改)

- **「拉取历史」是一个从未接线的能力缺口,不是本修复的回归**:`pull_jobs` 表(`internal/db/db.go` 迁移 1)与 `PullJobRecord` 结构体都在,但**没有任何调用者**;配置项 `PullHistoryRetention` 被赋值却从未被读取;`ListPullJobs` 从不合并数据库;前端 `fromHistory` 分支不可达,也没有「历史」选项卡。**本轮解决的是「进程活着时不再丢任务」,重启仍然清空**。补齐持久化 + 保留清理 + 历史 API + UI 入口属**面向用户的新能力**,按 `AGENTS.md` 应为中版本且需**人工指定号位**(`0.6.0` 已被 TLS 证书管理占用),故本轮不动。
- **`UpdateProxy` 用空密码会清掉已存密码**(`internal/api/handlers_extra.go`):0.5.15 已记录,本轮未改,原因同前——修复要变更已文档化的 PATCH 语义。
- 设置页 5 处 `TS6133` 未使用声明(`Descriptions`、`formatDateTime`、`inventory`、`savingRegistryUrl`、`setSavingRegistryUrl`)仍在基线里,未清理。

### 验证

- **生产现场证据(158,`cairn:0.5.15`)**:`docker logs cairn` 中的 panic 栈逐帧如下;其后 **约 4 秒**即出现 `config loaded`(容器被拉起),再下一次 `GET /api/pull/jobs` 的响应体从 **624 字节掉到 52 字节**(即 `data:[]`——任务没了):

```
panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x9566d7]

goroutine 34 [running]:
cairn/internal/pull.(*Orchestrator).RunOne(...)
	cairn/internal/pull/executor.go:171 +0x637
cairn/internal/pull.(*Executor).executeOne(...)
	cairn/internal/pull/queue.go:392 +0x202
cairn/internal/pull.(*Executor).Run(...)
	cairn/internal/pull/queue.go:353 +0x85
created by cairn/internal/server.(*Runtime).Start in goroutine 1
	cairn/internal/server/server.go:257 +0xb0
```

- **A/B 隔离冒烟(决定性对照)**——两个二进制、各自独立端口与数据目录,跑**同一条**真实拉取(`library/alpine:3.19`,经 `http://127.0.0.1:7890` 代理):
  - **旧二进制(v0.5.15,`266a9fa`)**:任务在 `t=2s` / `t=3s` 两次 `GET /api/pull/jobs/{id}` 均可见,随后 **`PROCESS DIED at t=3s`**;日志命中 `panic`,栈与上面 158 生产栈**逐帧一致**(仅行号随版本偏移);收尾打印 `--- process after run --- DEAD`。
  - **新二进制(含本轮修复)**:同一条拉取**跑完全程**——`status=succeeded`、`bytes=23260729`、`blobs 34/34`、`finalDigest=sha256:6baf43584bcb…`;`GET /api/pull/jobs` 返回 **`rows=1`**,任务**留在列表**;**日志零 panic**;**进程 `ALIVE`**。
  - 该对照同时排除了「无 panic 只是因为没走到崩溃点」的假阳性:旧二进制在同一条测试下**必然**崩溃,新二进制在同一条测试下**必然**不崩。
- Go 侧:`gofmt -l internal/` 无输出;`go vet ./internal/...` 退出码 0;`go build ./...` 与 `go build -tags webui -o /tmp/cairn-gate ./cmd/server` 退出码 0;`go test -count=1 ./internal/...` 全部 `ok`。新增用例 `TestPlatformAllowNilSafe`(nil 接收者与 nil 配置都不 panic)、`TestPlatformAllowReadsLiveConfig`(读的是实时配置而非构造期快照)、`TestExecuteOneRecoversFromPanic`(编排器 panic → 任务 `failed` 且进程存活)。
- 前端:`./web/node_modules/.bin/tsc --noEmit` 与基线逐条比对 **10 → 10,无新增**。本轮**零前端改动**,`internal/webui/dist/` 属构建产物且在 `.gitignore` 中,由 Docker 构建期生成。

### 兼容性

- **无 API 变更、无磁盘格式变更**:纯后端缺陷修复,升级不需迁移。
- **失败语义变更须知**:编排器若 panic,该任务现在**保持可见**并显示为 `failed`,错误文案为 `internal error: …`(此前是进程消失、任务一并消失)。这是行为改进,但会有人第一次在列表里看到这类 `failed` 条目——它不是新缺陷,而是过去那些「凭空消失」的任务。
- **`platformAllow` 的语义与配置键未变**:设置页的「拉取平台白名单」行为与 v0.5.15 完全一致(未配置 = 不限制平台)。

### 轮次与号位

- 本轮占 **0.5.16**:纯**缺陷修复**(空指针崩溃 + 队列兜底),按 `AGENTS.md` 判定为小版本(第 3 位)+1。
- `docs/ROADMAP.md` 已顺延:韧性轮 0.5.16 → **0.5.17**、工程化 0.5.17 → **0.5.18**;`0.6.0`(TLS 证书管理)由人指定,不随顺延改号。

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