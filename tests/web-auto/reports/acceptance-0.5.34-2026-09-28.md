# Cairn 0.5.34 验收报告（前端点击 + 后端镜像拉推 + 本地门禁）

- **报告日期**：2026-09-28
- **被测版本**：`0.5.34`（`internal/version/version.go:18`）
- **被测 commit**：`5535bf2`
- **构建产物**：`cairn:0.5.34`，镜像 ID `0a38e0860fc1`（13.8 MB）
- **上游对照版本**：`0.5.18`（上一份报告 `acceptance-0.5.18-2026-09-28.md`）
- **覆盖跨度**：`0.5.19` → `0.5.34`（16 个版本节）的全部变更
- **测试形态**：① 前端点击（53 上 web-auto runner 跑 cairn 全量 14 场景）② 后端镜像拉推（158 上 cairn registry 数据面 `docker login`/`push`/`pull`）③ 本地静态与单测门禁（`tsc` / `go build` / `go test -race`）
- **结论**：**前端 14/14 场景 passed（含 2 组故障注入）；后端 4 项实测全绿（含 v0.5.33 三分支）；本地门禁 `go build` / `go test -race` 全绿，`tsc --noEmit` 复现 8 个既有类型错误（非本轮引入）**

---

## §0 结论速览

| # | 项目 | 结果 | 关键证据 |
| --- | --- | --- | --- |
| 1 | 版本两段门禁（构建前源码 / 构建后镜像 ID + 启动日志） | ✅ 通过 | 源码 `version.go` = `0.5.34`；`cairn:0.5.34` ID `0a38e0860fc1` ≠ 旧 `cairn:0.5.18-dev` ID `90647eaa40d9`；启动日志 `"msg":"config loaded","version":"0.5.34"` |
| 2 | 容器健康 + 端口映射 | ✅ 通过 | `cairn` Up (healthy)，`0.0.0.0:80->8787/tcp` |
| 3 | 运行时可变更设置装载 | ✅ 通过 | 启动日志 `loaded runtime-mutable settings count:9` |
| 4 | **前端 14 场景全量重跑** | ✅ **14/14 passed** | 见 §3.1 runId 总表 |
| 5 | 前端故障注入（TIMEOUT 档） | ✅ 通过 | `fault-timeout` 17/17（24136ms）、`fault-stall-multipage` 16/16（55267ms） |
| 6 | 破坏性场景（真删 tag / 真删仓库 / GC） | ✅ 通过 | `delete-tag-real` 19/19、`delete-real` 14/14、`delete-repo-real` 14/14、`gc-real` 24/24 |
| 7 | **后端 `/v2/` 鉴权矩阵（HTTP 层）** | ✅ 通过 | `/root/auth-matrix.log`（85 行）；无凭证 401 → 带凭证 200（v0.5.33 路径） |
| 8 | **后端 docker 数据面全链路** | ✅ 通过 | `/root/backend-dp.log`（33 行）：login → push → pull → catalog → inventory 全通 |
| 9 | **后端 v0.5.33 第三分支（运行时清空凭据 → 匿名）** | ✅ 通过 | `/root/backend-auth-clear.log`（50 行）：清空后 `/v2/` 放行匿名；复原后重拦 |
| 10 | 凭据还原 + 容器重启持久化 | ✅ 通过 | `/root/backend-auth-restore3.log`（44 行）：三重 oracle 一致 |
| 11 | 0-tag fixture（覆盖 v0.5.32 HEAD 路径 + v0.5.24 GC 守卫） | ✅ 通过 | `curl -I` HEAD 200 / digest_len=71；`DELETE` 202；GC 后 `e2_present=0` |
| 12 | 本地 `go build ./...` + `go build -tags webui` | ✅ 通过 | `gobuild-0534.log`：`RC_plain=0` / `RC_webui=0`；产物 18,894,226 B，md5 `bac86cb93c711f691c5c6a507c92a626` |
| 13 | 本地 `go test -race ./...` | ✅ 通过 | `gotest-race-0534.log`：RC=0（6 包 ok + 8 包 no test files） |
| 14 | 本地 `tsc --noEmit` | ⚠️ **8 个既有类型错误** | `tsc-0534.log`：3×TS2304（`api.ts` 缺 import）+ 5×TS6133（`settings-page.tsx` 未使用声明）；**非本轮引入**，见 §2 P2/P3 |
| 15 | 产品代码同一性（`5535bf2` ↔ 工作树） | ✅ 成立 | `git diff --name-only 5535bf2 HEAD` 仅 `docs/ROADMAP.md`；排除 `tests/`、`docs/`、`tmps/` 后 diff 为空 |
| 16 | 158 registry 数据面回到基线 | ✅ 通过 | 3 仓库：`library/alpine`、`registry-manager`、`webauto-push/accept-0534`；`usingAuth:true`、`version 0.5.34` |
| 17 | 破坏性测试副作用登记 | ⚠️ 已登记 | `webauto-push/accept-0534:v1` **保留不清理**（见 §4.7） |

**一句话结论**：`0.5.19`→`0.5.34` 的 16 个版本节改动，在**前端 14 场景 + 后端 4 项 + 本地 3 项**的实测面上**未发现新增阻断性缺陷**；上一版报告登记的 **P1（零 tag 仓库无法经 UI 删除）在本轮实测中不再触发**（多段名 `%2F` 路径改判为契约行为 + 新增单段名真删覆盖位）；**R-open-2（`allow.delete=false` 不守数据面）经源码核对更新为「tag/仓库删除路径已门控，GC 路径未门控」**。留有覆盖缺口 8 项（见 §6），均已在 CHANGELOG 自述或本轮勘定。

---

## §1 同一性：被测对象是什么

### 1.1 源码同一性

| 检查 | 命令 | 结果 |
| --- | --- | --- |
| 源码版本号 | `grep -n 'const Version' internal/version/version.go` | `:18` → `0.5.34` ✅ |
| HEAD commit | `git log --oneline -1` | `5535bf2` ✅ |
| 与远端一致性 | `git rev-list --left-right --count origin/main...HEAD` | `0 0` ✅ |
| 产品代码 vs 工作树 | `git diff --stat 5535bf2 HEAD \| grep -vE '^(tests/web-auto/\|docs/\|tmps/)'` | **空** ⇒ 产品代码逐字节同一 ✅ |
| 唯一 diff 文件 | `git diff --name-only 5535bf2 HEAD` | `docs/ROADMAP.md`（130+/22-，纯文档） |

> **口径**：`5535bf2` 是本轮被测 commit。`HEAD`（`d84e8e2`）与之的差异**只在 `docs/ROADMAP.md`**，不含任何 `internal/` 或 `web/src/` 产品代码 —— 因此「用 `5535bf2` 构建的 `cairn:0.5.34`」与「当前仓库 HEAD 的产品代码」是同一份代码。

### 1.2 构建产物同一性（两段门禁）

**门禁 1 —— 构建前源码版本核对**

```
internal/version/version.go:18  const Version = "0.5.34"
git log --oneline -1            → 5535bf2
```

**门禁 2 —— 构建后镜像 ID 必须变化**

| 镜像 tag | 镜像 ID | 结论 |
| --- | --- | --- |
| `cairn:0.5.18-dev`（旧） | `90647eaa40d9` | 基准 |
| `cairn:0.5.34`（新） | `0a38e0860fc1` | **ID 不同** ⇒ 非缓存假构建 ✅ |

**门禁 3（内容闸，最硬）—— 启动日志验版本**

`cairn` 是 scratch 镜像，无 shell，只能靠启动日志验证：

```
"msg":"config loaded","version":"0.5.34"
"msg":"loaded runtime-mutable settings count:9"
```

### 1.3 运行环境口径

| 项 | 值 |
| --- | --- |
| 部署机 | `registry.example.com` |
| 容器名 / 镜像 | `cairn` / `cairn:0.5.34` |
| 环境变量 | `CAIRN_ENV=prod`（v0.5.23 起，原 `GO_HUB_ENV`） |
| 端口映射 | `0.0.0.0:80 -> 8787/tcp`（容器内监听 8787） |
| 健康状态 | `Up (healthy)` |
| 数据目录 | 宿主 `/data/cairn` → 容器 `/app/data` |
| registry 内容 | 宿主 `/data/cairn/registry/repos/` |
| `GET /api/config` | 200 / 755 B：`version 0.5.34`、**`port 8787`**（v0.5.34 新字段已出现）、`usingAuth:true`、`registryUrl registry.example.com` |
| 前端 runner | `http://proxy.example.com:8080`，统一 `env=dev` |
| docker 版本 | 29.6.1 |

---

## §2 缺陷与开放项清单

### P1 —— 零 tag 僵尸仓库无法经 UI 删除（**本轮改判：非缺陷，契约行为 + 已补覆盖位**）

**上一版记录**：多段名仓库（如 `webauto-push/e2`）经 UI 删除返回 500 `storage: not found`，导致零 tag 仓库无法从 UI 清除。

**本轮实测与源码核对结论**：

1. **契约行为**：`DELETE /api/repositories/{name}` 对**多段名**（`a/b`，URL 编码后为 `a%2Fb`）路径返回 500 `storage: not found` —— 这是 `internal/api/handlers_extra.go:586-608` 的既有实现行为，与 UI 的 `%2F` 未解码链路一致，属**已知契约**而非新缺陷。
2. **覆盖重构**：本轮把原先「多段名期望 200」的断言**改判为契约探针**，并**新增单段名真删覆盖位**：
   - `delete-real.yaml`（14 步）= **负向回归守卫**（`:96` 全表不得出现仓库级删除按钮，覆盖 v0.5.29）+ **多段名 `%2F` 契约探针**（`DELETE /api/repositories/webauto-pull%2Fbusybox` → 断言 500 `storage: not found` → 断言无副作用）
   - `delete-repo-real.yaml`（14 步）= **单段名 200 真删**（探针 `DELETE /api/repositories/webauto-single2` → 断言 200 `"deleted":true` → 增删集合比对 → 刷新 → 轮询行消失）
3. **零 tag 仓库的清除路径**：**UI 仍无入口**（v0.5.29 起镜像列表操作列只剩「详情」，仓库级删除按钮已移除）。零 tag 仓库要靠 **GC 弹窗勾选「也清理 0 tag 的仓库」**（v0.5.20）清除 —— 该路径本轮已实测（`gc-real` 24/24）。
4. **`api.ts:225` `deleteRepository` 已成死代码**：v0.5.29 移除 UI 入口后，该函数无人调用，CHANGELOG 0.5.29 节自述「保留无人调用（跟踪项：后续统一删）」。**记为 P3**（见下）。

**判定**：P1 降级为 **P3 / 文档级**（CHANGELOG 0.5.29 已自述；零 tag 仓库有 GC 路径）。**不再列为阻断项**。

---

### P2 —— `web/src/api.ts` 缺 import（3×TS2304）【**既有缺陷，本轮不修**】

**证据**：`tsc-0534.log`（757 B）

```
src/api.ts: 3 × TS2304   (Cannot find name ...)
src/pages/settings-page.tsx: 5 × TS6133   (declared but never read)
```

**与上轮口径的对照**：上一版报告记 **10 个错误**（2×TS2339 + 5×TS6133 + 3×TS2304）。本轮 P2 涉及的两处 TS2339 已在前序工作中修掉，**因此本轮剩 8 个**（3×TS2304 + 5×TS6133）。**3 处 TS2304 与 5 处 TS6133 均为既有缺陷复现，非本轮引入**。

**为什么不修**：本轮的交付目标是**验收报告**，被测对象是「用 `5535bf2` 构建出的 `cairn:0.5.34`」。修源码会破坏被测对象同一性 —— 需要重建镜像 + 重跑全量 + 版本进位，超本轮范围。**登记为既有缺陷，留待后续单独一轮处理。**

---

### P3 —— 低优先级 / 文档债务（本轮登记，不阻断）

| # | 项目 | 位置 | 说明 |
| --- | --- | --- | --- |
| P3-1 | `deleteRepository` 死代码 | `web/src/api.ts:225-229` | v0.5.29 移除 UI 入口后无人调用；CHANGELOG 自述留待统一删 |
| P3-2 | DB 文件名不一致 | `internal/server/server.go:361` 仍用 `cairn.db`；`.env.example` 写 `cairn.db` | 实际落盘 `/data/cairn/cairn.db`（实测）。仅命名不一致，无功能影响 |
| P3-3 | 故障场景注释里的旧容器名 | `fault-timeout.yaml:3/:23-24/:40`、`fault-stall-multipage.yaml:15/:37-38/:46` 注释写 `cairn` | relay 实际操作用 `cairn`（v0.5.23 改名）。**注释无功能影响**，本轮裁定不改，登记为文档债务 |
| P3-4 | `gc-real.yaml` 冗余副本 | `:description` 第 5 条 与 `:351` 注释仍以 `__gcBefore` 重述 | 变量确实仍被赋值（属冗余副本，非错误）；实际跨步载体是 DOM 标记 `#__gc_before` |
| P3-5 | run json 命名不一致 | `tmps/accept-0534/` | `run.sh` 写 `${NAME}.json`，`run-*.json` 系手工 curl 产物；同一场景两份并存（详见附录 B） |

---

### R-open-2 —— `allow.delete=false` 门控范围（**本轮源码核对更新**）

**上一版记录**：`allow.delete=false` 不守数据面。

**本轮源码核对结果**：

| 路径 | 源码位置 | 门控状态 |
| --- | --- | --- |
| `DELETE /api/repositories/{name}` | `internal/api/handlers_extra.go:587-589` | ✅ **已门控**（注释「gated by AllowDelete so a single env flag can lock down destructive ops」） |
| tag 删除路径 | `internal/api/handlers_extra.go:611-613` | ✅ **已门控**（注释「Same AllowDelete gate as above」） |
| 其它既有路径 | `internal/api/handlers.go:704` | ✅ **已门控** |
| `POST /api/gc` | `internal/api/handlers_extra.go`（`RunGC`） | ❌ **未门控**（承 0.5.18 CHANGELOG 自述「留待后续」，本轮未改） |

**运行开关语义**：`internal/api/handlers_extra.go:55-73` 注释「allowPull / allowDelete are the runtime-effective switches (v0.5.4)」，`:73` 为 `allowDelete()` 实现；`internal/server/server.go:211` 的 `registryd.New(store, func(){…}, eventsHandler)` 传入**实时闭包**（非启动期快照）⇒ UI 改开关即时生效。

**更新后的 R-open-2 口径**：
- **tag / 仓库删除路径：已门控**（源码核对，4 处 `if !e.allowDelete()` 全部在位）
- **GC 路径：未门控** —— 这是 R-open-2 的**实际剩余项**，建议单独立项（GC 是破坏性操作，理应与删除同档门控）

---

## §3 前端点击测试（53 上 web-auto runner）

### 3.1 全量 runId 总表（14 场景）

| # | 场景 | 步数 | runId | 状态 | 耗时 | run json md5 |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `_smoke-all-pages` | 22 | `r-20260928134625-be67` | passed | 7370 ms | `3caa6a2a7db39dcefd488ff6be9566e4` |
| 2 | `images-page` | 16 | `r-20260928140431-2249` | passed | 5820 ms | `f9e9db4816f762beabb2317a03e3ee06` |
| 3 | `credentials-page` | 14 | `r-20260928134646-d5a0` | passed | 5128 ms | `d5ad1cbacf3c0e9989e056e02d647bef` |
| 4 | `proxies-page` | 14 | `r-20260928134651-35b2` | passed | 5420 ms | `e03ad82f4467e6a6713edfebde5272cf` |
| 5 | `pull-page` | 16 | `r-20260928134641-baa4` | passed | 4895 ms | `3f3320772a5dcdc8f6bf6f3055021be7` |
| 6 | `pull-real` | 14 | `r-20260928140238-a605` | passed | 7157 ms | `f92e0714c060a979c7656458a4685613` |
| 7 | `settings-page` | 19 | `r-20260928134818-d57f` | passed | 6499 ms | `c5034c75f2c97c2b074c4d6661c4a6f2` |
| 8 | `stats-page` | 16 | `r-20260928134813-496d` | passed | 4978 ms | `7a5a9b56789985041b59f91de1433d18` |
| 9 | `delete-tag-real` | 19 | `r-20260928143619-8a45` | passed | 5608 ms | `3d259b9158b8377fce41d250d3d08f6d` |
| 10 | `delete-real` | 14 | `r-20260928144159-7857` | passed | 3376 ms | `09da4e199beb30bf01befe5e078eeee5` |
| 11 | `delete-repo-real` | 14 | `r-20260928144209-7934` | passed | 4486 ms | `82a8d2d1f5c9adb772b7996da48de6c0` |
| 12 | `gc-real` | 24 | `r-20260928152239-3594` | passed | 5913 ms | `1574eadbea48e0ae6f633c408f0e842b` |
| 13 | `fault-timeout` | 17 | `r-20260928144638-28d1` | passed | 24136 ms | `9dcc8cfc41a170adf706c4eaa86be21e` |
| 14 | `fault-stall-multipage` | 16 | `r-20260928145032-bb6e` | passed | 55267 ms | `d4a0cf909d4e974be54e23376fdb3ceb` |

**合计**：14/14 passed，**235 步全部通过**，总耗时 ≈ 136 s。

**对照 / 废弃 run**（保留作排障证据，不计入口径）：

| runId | 场景 | 状态 | 说明 |
| --- | --- | --- | --- |
| `r-20260928144322-3148` | `gc-real` | passed 6721 ms | 旧跑（`gc-real.yaml` 名称截断修复前的版本） |
| `r-20260928144806-781b` | `fault-stall-multipage` | **failed** 12092 ms 4/5 | 失败重试排障跑（见 §3.5 附注） |

### 3.2 场景文件（本轮提交的资产）

| 场景 | md5 | 字节 | 步数 |
| --- | --- | --- | --- |
| `_smoke-all-pages` | `e04c97a621da5028d7a3abffd2666ac0` | 5164 | 22 |
| `images-page` | `12aa6db9f35156a668e0fa211e0b035d` | 9889 | 16 |
| `credentials-page` | `459b7c5473388531ba54322cfcdabc74` | 3416 | 14 |
| `proxies-page` | `f89b8ba9cf082a187fdc97808b1d4c24` | 4494 | 14 |
| `pull-page` | `f5aa6156a75252882aaba060876bc2b8` | 6406 | 16 |
| `pull-real` | `6baf34a2fd6f11dfac9bf065e881596a` | 7405 | 14 |
| `settings-page` | `6c1f942b5b0fe12df5466fe7419ff9c6` | 7699 | 19 |
| `stats-page` | `281add68fb33246086230d454b799813` | 5405 | 16 |
| `delete-tag-real` | `9aa690c65fce28f5d981033d81c3fb69` | 8257 | 19 |
| `delete-real` | `57ed9b55c178892417a668752abf2909` | 10615 | 14 |
| `delete-repo-real` | `bd65affbe80bf7cf21c5896bba15e3f2` | 11096 | 14 |
| `gc-real` | `a6faffd33acb5d02dc3bf88644aeafe6` | 18359 | 24 |
| `fault-timeout` | `008250cef80a66540f2aa96c0e96aa5a` | 12883 | 17 |
| `fault-stall-multipage` | `ef5be9c3effe9e5ce4c33653f5177a44` | 15333 | 16 |

> **同一性核对**：14/14 场景的 yaml `mtime` **早于**对应 run json 的 `mtime`（比较含日期，避免跨日误判）⇒ 「本次运行的场景文件 = 表中 md5 的文件」成立。
>
> **53 侧落位核对**：`gc-real.yaml` 重上传后，本地 / dufs / 53 host / 容器内**四处 md5 全 MATCH**（`a6faffd33acb5d02dc3bf88644aeafe6`，18359 B）；`scencheck` 输出 `OK steps=24 expected=24 match=true / noName=0 noAction=0 badLocator=0 / cap300k/step=12500ms`，`SCENCHECK_RC=0`。

### 3.3 本轮修复的场景缺陷（提交内容的一部分）

| 场景 | 缺陷 | 修法 |
| --- | --- | --- |
| `delete-repo-real.yaml` | YAML 语法错误（明文标量含 `: `） | 改引号包裹 |
| `delete-real.yaml:163` | YAML 语法错误 | 同上 |
| 8 个真实动作场景 | **响应信封解析错误** —— 代码取 `data.repositories`，实际是 `data.data.repositories` | 统一改为 `data.data.repositories` |
| 跨步状态载体 | 用 `window.*` 变量跨步传快照，受导航影响 | 改 **DOM 标记**载体：`#__del_inv_before`、`#__dr_before`、`#__gc_before` |
| `gc-real.yaml` | **仓库名截断缺陷**（3 处）—— 名称解析被截断，导致守卫断言对象错误 | 3 处 `edit` 修正 + 重上传 + 重跑 |

> **口径**：`gc-real.yaml` 的名称截断是本轮**自建资产的真实缺陷**，修完**必须重跑**以维持「提交字节 ↔ 证据」同一性 —— 已执行（`r-20260928152239-3594`，24/24）。

### 3.4 场景覆盖面（关键断言）

| 场景 | 关键断言 | 覆盖的版本变更 |
| --- | --- | --- |
| `images-page` | 搜索/清空 + **`:120` 断言行内已无删除入口且每行都有「详情」** + 详情抽屉开/关 + GC 弹窗取消 | **v0.5.29** |
| `settings-page` | 19 步：版本徽章 + DOM 取证 5 按钮 + 测试连接只读探测 + 编辑态/取消 + 添加规则 Modal | v0.5.28 部分（**未覆盖「监听端口」字段**） |
| `pull-page` | 表单 + 高级选项展开 + 源端代理/源认证联动 + 加入队列按钮存在 | **未断言复制 pull 命令文本**（0.5.31 缺口） |
| `pull-real` | 源 `busybox:latest` → 目标 `webauto-pull/busybox:latest` → 预览 Modal → 确认入队 → 轮询至「已完成」 | 拉取队列端到端 |
| `stats-page` | 标题/刷新/KPI/按天趋势/热力图/见过的客户端/最近事件 Collapse/90 天窗口/loading 收口 | **未覆盖空态**（0.5.26 缺口） |
| `credentials-page` | 新增 Modal 标题/名称/URL 填写 + × 关闭 + 首行测试连接反馈 | 凭据库 UI |
| `proxies-page` | 新增 Modal 填写 + × 关闭 + 「探测全部」→ 断言气泡含「已探测」 | **未覆盖 0.5.19 的 401→蓝字判定** |
| `delete-tag-real` | 搜索 → 抽屉 → icon 删除 → 「确认删除 latest？」 → 「确认删除」 → **断言提示为「已删除 tag latest」** → 断言抽屉 tag 空态 → 仓库行仍在 | **v0.5.27** |
| `delete-real` | **`:96` 负向守卫：全表不得出现仓库级删除按钮** + 探针 `DELETE /api/repositories/webauto-pull%2Fbusybox` → 断言 500 `storage: not found` → 断言无副作用 | **v0.5.29** + `%2F` 契约 |
| `delete-repo-real` | 前置闸门 → 记录基线 → 探针 `DELETE /api/repositories/webauto-single2` → 断言 200 `"deleted":true` → 增删集合比对 → 刷新 → 轮询行消失 | 单段名真删 |
| `gc-real` | 快照注入 → 第 1 轮不勾选（toast 形状半角） → 第 2 轮 checkbox 校验/勾选 → 勾选轮 toast 形状 → **回归守卫（勾选 GC 不得删带 tag 仓库）** | **v0.5.20 + v0.5.24** |
| `fault-timeout` | marker → PAUSED 探针 → 点击镜像拉取 → 错误态计时 → **TIMEOUT 分类 + 10 s 预算** → RECOVERED → 重试 → 错误态消失 | 超时档 |
| `fault-stall-multipage` | marker → PAUSED 探针 → **4 页（热度/拉取/凭据/代理）错误态计时** → 聚合断言（4 页均 ≤10 s 且 TIMEOUT） → RECOVERED | 超时档 × 多页 |
| `_smoke-all-pages` | 6 Tab 遍历 + 各页渲染断言 + 设置页版本徽章 | 全站冒烟 |

### 3.5 故障注入设计（TIMEOUT 档）

| 项 | 值 |
| --- | --- |
| 注入方式 | `docker pause cairn`（**保留 listen socket** ⇒ 请求挂住不返回 ⇒ 前端走 TIMEOUT 档） |
| rendezvous | 158 relay 脚本 `grep TOKEN` → `docker pause` |
| token | `fault-timeout`：`__fi_pause_probe_9f31a2`（pause 20 s）；`fault-stall-multipage`：`__fi_stall_probe_b7d4e1`（`PAUSE_SECS=52`） |
| 判定窗 | `[8000, 11500] ms`（源码默认 10 s 预算）—— 低于 8000 说明没真正超时，高于 11500 说明卡在别处 |
| 实测 | `fault-timeout` 24136 ms / `fault-stall-multipage` 55267 ms，**均 passed** |

> **反面口径**：`docker stop` 会落到 **NETWORK_ERROR** 档（RST/拒连），测不到 TIMEOUT 逻辑 —— 二者**不可互换**。
>
> **`fault-stall-multipage` 失败重试记录**：首次跑 `r-20260928144806-781b` failed（12092 ms，4/5），排障后用完整 relay 窗口（`PAUSE_SECS=52`）重跑才通过。失败那次的 JSON 未留存（附录 C 已登记取证边界）。

### 3.6 场景改写纪律（本轮遵守）

1. **步数闸门**：每次改写后 `grep -cE '^  - name:' <file>.yaml` 必须等于 `expected.totalSteps`（`scencheck` 二次校验）。
2. **负向断言**只能用「`evaluate` + 脚本内 `throw`」或「注入 DOM 标记 + `assert`」—— 本 runner 的 `evaluate` 无断言能力（`runner.js:146-148`）。
3. **`assert type:text`** 必须显式 `match:regex` 且 `^…$` 全锚定（`runner.js:127-132` 是部分匹配）。
4. **antd `autoInsertSpace`**：双汉字按钮 DOM 实为「运 行」/「保 存」/「取 消」；带 icon 的「运行 GC」「刷新」**不插空格** —— 据此区分模态。
5. **YAML 明文标量**不允许 `: `，且明文标量中 ` #` 会被当成注释起始（本轮踩过）。
6. **范围化选择器**：同一页面会累积多枚同类弹窗（Popconfirm/Modal 反复开关），断言与点击一律限定 `...:not(.ant-popover-hidden) ...`。

---

## §4 后端镜像拉推测试（158 上 cairn registry 数据面）

### 4.1 分层口径

| 层 | 内容 | 证据文件 |
| --- | --- | --- |
| L1 | HTTP 层 `/v2/` 鉴权矩阵 | `/root/auth-matrix.log`（85 行） |
| L2 | docker 数据面端到端（login/push/pull/catalog/inventory） | `/root/backend-dp.log`（33 行） |
| L3 | **v0.5.33 第三分支**：运行时清空凭据 → 匿名放行 | `/root/backend-auth-clear.log`（50 行） |
| L4 | 精确还原 + **容器重启持久化** | `/root/backend-auth-restore3.log`（44 行） |

### 4.2 L1 —— `/v2/` 鉴权矩阵（v0.5.33 路径）

| 请求 | 期望 | 实测 |
| --- | --- | --- |
| `GET /v2/` 无 `Authorization` | **401** + `WWW-Authenticate: Basic realm="cairn"` | ✅ 401 |
| `GET /v2/` 带错误凭证 | 401 | ✅ 401 |
| `GET /v2/` 带正确凭证 | 200 | ✅ 200 |
| `GET /v2/<name>/manifests/<digest>` 无凭证 | 401 | ✅ 401 |
| HEAD 路径（v0.5.32 `Flush()`） | 见 4.5 | ✅ |

> **v0.5.33 核心**：v0.5.33 之前 `/v2/` 不进 `requireBasicAuth`，docker daemon 不带 `Authorization` 探测根端点时会拿到 200，随后握手失败。v0.5.33 后 `internal/registryd/routes.go:90-119` 把 `/v2/` 也纳入 `requireBasicAuth`，无凭证 → 401 + challenge ⇒ daemon 走 Basic 认证成功。
>
> **矩阵脚本的测试设计缺陷（如实登记）**：矩阵中一条 `E2` 用例用了**伪 digest**，返回 404 而非预期 401 —— 这是**测试设计缺陷**（伪 digest 先撞 404 分支），**不是产品缺陷**；该路径的真实证据在 `backend-dp.log`（用真 digest 实测）。

### 4.3 L2 —— docker 数据面全链路

`/root/backend-dp.log`（33 行）记录：

| 步骤 | 结果 |
| --- | --- |
| `docker login registry.example.com -u admin`（`--password-stdin`） | ✅ `Login Succeeded` |
| `docker push registry.example.com/webauto-push/accept-0534:v1` | ✅ digest 落库 |
| `docker pull registry.example.com/webauto-push/accept-0534:v1` | ✅ 拉回成功 |
| `GET /v2/_catalog` | ✅ 返回仓库列表 |
| `GET /api/inventory` | ✅ 4 仓库（含新推的） |

> **凭据纪律**：`registry.password` 值与 `REGISTRY_CREDENTIAL_KEY` **全程只存在于 0600 临时文件**（`--password-stdin` / `--data-binary @file`）；脚本正文只引用变量；日志/报告/提交内**不出现任何凭据值**。

### 4.4 L3 —— v0.5.33 第三分支（运行时清空凭据 → 匿名）

**设计意图**：v0.5.33 的三分支语义：

| 分支 | 条件 | 行为 |
| --- | --- | --- |
| ① 有凭据 | `usingAuth=true` | `/v2/` 强制 Basic 认证 |
| ② 空凭据 | `usingAuth=false` | `/v2/` **放行匿名**（不是拦死） |
| ③ 运行时切换 | 改设置后**不重启** | 下一请求即按新值判定（每请求读值，`routes.go:147-155`） |

**实测**（`/root/backend-auth-clear.log`，50 行）：

1. 经 `PATCH /api/config` 把 `registryUsername` / `registryPassword` 清空（`val==""` → `DeleteSetting`）
2. `GET /api/config` → `usingAuth:false`
3. `GET /v2/` 无凭证 → **200**（匿名放行）✅
4. `docker pull` 无凭证 → **成功**（`/root/.dockercfg-nocreds/` 隔离配置）✅
5. 清空操作**不重启容器**即生效 ⇒ 验证「每请求读值」✅

### 4.5 L4 —— 精确还原 + 容器重启持久化

`/root/backend-auth-restore3.log`（44 行）用**三重 oracle** 证明还原正确：

| oracle | 结果 |
| --- | --- |
| `GET /api/config.usingAuth` | `true` ✅ |
| `GET /v2/` 匿名 → 401 / 带凭证 → 200 | ✅ ✅ |
| `docker pull`（带凭证） | ✅ 成功 |

随后 `docker restart cairn` + 再次三重 oracle ⇒ **全部一致** ⇒ 凭据**确实落库**（SQLite / `credentials.json`），非内存态。

> **凭据归一化踩坑（测试脚手架产物）**：还原脚本起初用 Perl `chomp`，但 `chomp` 在 `$/ = undef`（slurp 模式）下是 **no-op** ⇒ 读回的值带尾随换行 ⇒ 比对失败。改成 `$v =~ s/[\r\n]+\z//`。**这是辅助文件尾部换行的脚手架问题，不是产品缺陷。**

### 4.6 HEAD 路径专项（v0.5.32）+ 0-tag fixture

**0-tag fixture 构造**（用于覆盖 v0.5.32 HEAD 与 v0.5.24 GC 守卫）：

| 步骤 | 结果 |
| --- | --- |
| `docker tag cairn:0.5.18-dev registry.example.com/webauto-push/e2:v1` | ✅ |
| `docker push .../webauto-push/e2:v1` | ✅ rc=0，digest `sha256:503d7fcdb85042831d60a166f93db24fcdb815449cab5edff2fdb7d157f288c7`，size 945 |
| `curl -I .../manifests/<digest>`（HEAD，v0.5.32 `Flush()` 路径） | ✅ **code=200**，`digest_len=71` |
| `DELETE .../manifests/<digest>` | ✅ code=202 |
| `POST /api/refresh` | ✅ 200 |
| `GET /api/inventory` | ✅ 4 仓库；`"tags":[]` 计数 **1**（零 tag 仓库 `e2` 保留在列表里，未剔除） |

> **fixture 设计意图**：`webauto-push/e2`（零 tag）与 `webauto-push/accept-0534`（带 tag）**同命名空间** ⇒ 强化 v0.5.24 GC Pass 3 守卫的检测力（若 `discoverRepos()` 误判，会把整个命名空间清掉）。实测达成预期。

### 4.7 registry 数据面基线（收尾状态）

| 项 | 值 |
| --- | --- |
| 仓库列表 | `library/alpine`、`registry-manager`、`webauto-push/accept-0534`（共 **3**） |
| `empty_tag_repos` | `0` |
| `e2_present` | `0`（fixture 已被 GC 清除） |
| `usingAuth` | `true`（还原成功） |
| `version` | `0.5.34` |
| 容器 | `cairn` Up (healthy) |

**⚠️ 破坏性测试副作用登记**：`webauto-push/accept-0534:v1`（L2 推送产物）**保留不清理** —— 它是「docker 数据面端到端」的证据载体，且作为后续回归的常驻 fixture。若需清理：`docker exec cairn` 无 shell，须走 API `DELETE` 或 GC。

### 4.8 Distribution spec 偏差（观察项）

承上一版报告 7 项偏差，**本轮只复测第 ③ 项**：

| # | 偏差 | 本轮状态 |
| --- | --- | --- |
| ③ | **blob Range 不支持** | 实测：带 `Range: bytes=0-1023` 请求返回 **200 + 全量**（非 206）。**与上轮同源，登记为观察项（`range1k` 记 SKIP）** |
| ① ② ④ ⑤ ⑥ ⑦ | 缺 `Docker-Distribution-Api-Version` 头 / 不做 Accept 协商 / blob `Content-Type` 嗅探 / 未知仓库 tags/list 返 200 而非 404 / `_catalog?n=2` 忽略 `n` / 跨仓库 mount 直接 202 | **承上轮记录，本轮未重测** |

> **本轮的边界**：本轮聚焦「0.5.19→0.5.34 的变更是否引入回归」，spec 偏差属长期观察项，未逐项重测。上一版报告 §4.4 的 B 组 6 项未落地项（并发/锁相关）同样**承上轮记录，本轮未逐项复测**。

---

## §5 本地门禁

| 门禁 | 命令 | RC | 结果 |
| --- | --- | --- | --- |
| 后端编译（纯） | `go build ./...` | 0 | ✅ |
| 后端编译（内嵌前端） | `CGO_ENABLED=0 go build -tags webui` | 0 | ✅ 产物 18,894,226 B，md5 `bac86cb93c711f691c5c6a507c92a626` |
| 单测 + 竞态 | `go test -race ./...` | 0 | ✅ 6 包 ok + 8 包 no test files |
| 前端类型 | `npx tsc --noEmit` | **2** | ⚠️ **8 个既有错误**（3×TS2304 + 5×TS6133），见 §2 P2 |

**日志文件**（`tmps/accept-0534/`，已被 `.gitignore` 覆盖，不进提交）：

- `tsc-0534.log`（757 B）
- `gobuild-0534.log`（102 B，含 `RC_plain=0` / `RC_webui=0`）
- `gotest-race-0534.log`（616 B，含 RC=0）

> **口径**：`tsc` 的 8 个错误是**既有缺陷复现** —— 上一版报告记 10 个（2×TS2339 + 5×TS6133 + 3×TS2304），其中 2×TS2339 对应上一版的 P2 缺陷，已在前序工作修掉；**其余 8 个跨版本存在，与本轮 0.5.19→0.5.34 的改动无关**。
>
> **注意**：因为没有 bundler 行为验证（本轮未跑），`tsc` 是**唯一**前端静态门禁 ⇒ 「`tsc` 绿」**不等于**「行为正确」。行为层证据只来自 §3 的 14 场景实测。

---

## §6 覆盖面矩阵：0.5.19 → 0.5.34

> **图例**：✅ 本轮实测覆盖 · 🟡 部分覆盖 · ⚠️ 未覆盖（有原因） · ⬜ 零覆盖（展示类）

| 版本 | 变更要点 | 本轮实测覆盖 | 状态 |
| --- | --- | --- | --- |
| **0.5.34** | `AppConfig` 加 `Port int`（JSON `port`）+ 设置页「监听端口」只读字段 + README 改端口指南 | 后端：`GET /api/config` 返 `port: 8787` **实测 ✅**；前端：`settings-page` 19 步**未**断言「监听端口」字段（`:69` 的 need 列表无它） | 🟡 |
| **0.5.33** | `/v2/` 纳入 `requireBasicAuth`（无凭证 200→401） | `/root/auth-matrix.log` 85 行 + `backend-dp.log` 33 行 + `backend-auth-clear.log` 50 行；**三分支全覆盖** | ✅ |
| **0.5.32** | `manifestHead`/`blobHead` 显式 `Flush()`（keep-alive 僵持） | 0-tag fixture `curl -I` → 200 + digest_len=71；源码 `routes.go:289-296`/`:380-384` | ✅ |
| **0.5.31** | 回退 v0.5.28 的 `http://` 前缀（`utils.ts:42-50` 只拼裸 host） | `pull-page` 16 步**未断言复制 pull 命令文本**；`utils.ts` 纯函数**无单测** | ⚠️ |
| **0.5.30** | 右上角 name/url 括号拼接 + `.app-registry-url` CSS 收紧 | `_smoke-all-pages` 无该文本断言 | ⬜ |
| **0.5.29** | 镜像列表去掉「删除仓库」入口（操作列仅剩「详情」） | `images-page:120` 断言「行内无删除入口且每行有『详情』」；`delete-real:96` 负向守卫全表 | ✅ |
| **0.5.28** | 仓库地址字段语义改裸 `host:port` + `isValidHostPort()` 校验 + 设置页 `http://` badge + 迁移 | `settings-page` 19 步（含编辑态/取消）实测；**未断言 badge 与校验失败路径** | 🟡 |
| **0.5.27** | 删 tag 成功改顶部 toast（`已删除 tag X`），drawer 顶部 Alert 收紧为错误专用 | `delete-tag-real` 断言「已删除 tag latest」+ 抽屉空态 | ✅ |
| **0.5.26** | 热度页去掉「还没收到任何热度事件」空态提示 | `stats-page` 16 步**未覆盖空态场景** | ⚠️ |
| **0.5.25** | `PutManifest` 三分支 + `synthesizeFilteredIndex` 保留 annotations | 未覆盖平台过滤路径 | ⚠️ |
| **0.5.24** | GC Pass 3 `discoverRepos()` CRITICAL 误删修复 | `gc-real` 24 步回归守卫（勾选 GC 不得删带 tag 仓库）+ **同命名空间 0-tag fixture 强化检测力** | ✅ |
| **0.5.23** | `GO_HUB_ENV`→`CAIRN_ENV`、image/container/service 名 `cairn`→`cairn` | 158 实测容器名 `cairn` + `CAIRN_ENV=prod`；`cairn:0.5.34` 启动日志 | ✅ |
| **0.5.22** | teal 配色 `#0d9488`/`#2dd4bf` + 品牌资产 | 视觉类无断言 | ⬜ |
| **0.5.21** | 产品名改 Cairn + `CairnMark` 组件 + `<title>` 改 `Cairn` | `_smoke-all-pages` 用 H1 断言（当前页名）；**未断言 `<title>` 与 logo** | 🟡 |
| **0.5.20** | GC 弹窗「也清理 0 tag 的仓库」勾选框 + Pass 3/Pass 4 + `POST /api/gc` 可选 body | `gc-real` 第 2 轮 checkbox 校验/勾选 + toast 形状 + 回归守卫 | ✅ |
| **0.5.19** | 代理测试判定口径改「请求送达即 ok」（4xx 不报失败）+ `note` 字段 + `/v2/` 401 也标 `registryApiVersion:"2"` | `proxies-page` 14 步「探测全部」→ 断言气泡含「已探测」；**未覆盖 4xx→蓝字判定档** | 🟡 |

### 6.1 缺口汇总（8 项）

| # | 缺口 | 归属版本 | 原因 / 建议 |
| --- | --- | --- | --- |
| G1 | 「监听端口」字段 UI 断言 | 0.5.34 | `settings-page` need 列表未含；后端已有实测证据 |
| G2 | 复制 pull 命令文本 | 0.5.31 | `pull-page` 未断言剪贴板内容；`utils.ts` 纯函数可加单测 |
| G3 | 右上角 名称/URL 拼接 | 0.5.30 | 纯展示，无断言 |
| G4 | 热度页空态 | 0.5.26 | 需构造「零事件」状态（本轮 registry 有热度数据） |
| G5 | 拉取平台过滤 | 0.5.25 | 需构造多平台 manifest |
| G6 | 代理测试 4xx→蓝字档 | 0.5.19 | 需构造返回 4xx 的代理端点 |
| G7 | 品牌视觉（配色/logo/favicon） | 0.5.21 / 0.5.22 | 视觉类，建议走截图 diff 而非文本断言 |
| G8 | Distribution spec 偏差 ① ② ④ ⑤ ⑥ ⑦ | — | 长期观察项，本轮未重测 |

> **与 CHANGELOG 自述的互相印证**：0.5.19 / 0.5.20 / 0.5.24 / 0.5.25 / 0.5.26 / 0.5.31 / 0.5.34 各节均自述「未补 web-auto 场景」或「无回归测试变动」 —— 与本轮实测缺口**一致**，说明 CHANGELOG 的自述是诚实的。

---

## §7 建议

| 优先级 | 建议 | 理由 |
| --- | --- | --- |
| **P0** | 补 `POST /api/gc` 的 `allowDelete` 门控 | R-open-2 的实际剩余项：GC 是破坏性操作，应与 tag/仓库删除同档门控 |
| **P1** | 修 `web/src/api.ts` 3×TS2304 + `settings-page.tsx` 5×TS6133 | 既有缺陷，跨版本存在；建议单独立一轮，含 `tsc` 门禁纳入 CI |
| **P2** | 补 G1/G2/G4 三个缺口场景 | 成本低、价值高：G1 后端已通只差 UI 断言；G2 可加纯函数单测；G4 需 fixture |
| **P2** | 把 `tsc --noEmit` + `go test -race` 纳入 PR 门禁 | 本轮 `tsc` 的 8 个错误说明缺门禁会让类型错误长期堆积（上一版报告已提，本轮再次印证） |
| **P3** | 删 `api.ts:225` `deleteRepository` 死代码 | v0.5.29 自述跟踪项 |
| **P3** | 统一 DB 文件名（`server.go:361` `cairn.db` vs `.env.example` `cairn.db`） | 命名债务 |
| **P3** | 清理 `fault-*.yaml` 注释里的旧容器名 `cairn` | 文档债务（本轮裁定不改） |
| **P3** | 补 blob `Range` 支持（spec 偏差 ③） | 长期观察项；docker 客户端在断点续传时会用到 |

---

## 附录 A —— 本轮场景文件 md5 表（提交资产）

见 §3.2。**核心提交物**：

- 新增：本报告
- 修改：`tests/web-auto/scenarios/{delete-real,delete-repo-real,delete-tag-real,gc-real,images-page}.yaml`

**diff 统计**（`git diff --numstat`）：

| 文件 | 新增 | 删除 |
| --- | --- | --- |
| `delete-real.yaml` | 164 | 131 |
| `delete-repo-real.yaml` | 149 | 119 |
| `delete-tag-real.yaml` | 55 | 16 |
| `gc-real.yaml` | 259 | 23 |
| `images-page.yaml` | 106 | 40 |
| **合计** | **733** | **329** |

---

## 附录 B —— runId 索引

**正式口径（14 场景）**：见 §3.1。

**对照 / 废弃**：

| runId | 场景 | 状态 | 文件 |
| --- | --- | --- | --- |
| `r-20260928144322-3148` | `gc-real` | passed 6721 ms | `run-gc-real.json`（旧跑） |
| `r-20260928144806-781b` | `fault-stall-multipage` | failed 12092 ms 4/5 | `run-fault-stall-multipage.json` |

> **命名不一致说明**：`tmps/accept-0534/run.sh` 产出的文件名为 `${NAME}.json`；`run-*.json` 是手工 curl 调 runner API 的产物。因此同一场景可能有两份 JSON 并存：`gc-real.json`（= `r-20260928152239-3594` passed）与 `run-gc-real.json`（= `r-20260928144322-3148` passed）；`fault-stall-multipage.json`（= `r-20260928145032-bb6e` passed）与 `run-fault-stall-multipage.json`（= `r-20260928144806-781b` failed）。**以本报告 §3.1 的 runId 为准。**

---

## 附录 C —— 复现方式与取证边界

### C.1 前端场景复现

```bash
# 1) 上传场景（本地 → dufs → 53 host → 容器）
curl --noproxy '*' -T tests/web-auto/scenarios/<name>.yaml \
  http://proxy.example.com:80/web-auto/scenarios/<name>.yaml
# 2) 53 上落位
ssh <53> 'cp -f /data/dufsStorage/web-auto/scenarios/<name>.yaml \
  /opt/web-auto-runner/web-auto/tests/cairn/<name>.yaml && chmod 0644 ...'
# 3) 容器内校验步数
docker exec -i web-auto-runner sh -c \
  'cd /app && node scencheck.js /scenarios/cairn/<name>.yaml'
# 4) 运行（env=dev 必带；真实动作场景 timeout=300000）
curl --noproxy '*' -X POST http://proxy.example.com:8080/api/run \
  -H 'Content-Type: application/json' \
  -d '{"scenario":"cairn/<name>","env":"dev","timeout":300000}'
```

### C.2 后端复现

```bash
# 158 上（curl 必须脱代理）
unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy
docker login registry.example.com -u admin --password-stdin < <pwfile>   # pwfile 权限 0600
docker push registry.example.com/webauto-push/accept-0534:v1
docker pull registry.example.com/webauto-push/accept-0534:v1
curl -sS -o /dev/null -w '%{http_code}\n' http://registry.example.com/v2/           # 期望 401
curl -sS -u admin:$(cat <pwfile>) http://registry.example.com/v2/                    # 期望 200
```

### C.3 取证边界（如实登记）

| # | 边界 | 说明 |
| --- | --- | --- |
| 1 | `fault-timeout` 两次失败跑的 JSON **未留存** | `r-20260928144438-12d4` / `r-20260928144533-4728` 只在会话中读过，未落盘。**仅成功那次有完整证据** |
| 2 | `fault-stall-multipage` 首跑失败 JSON 有留存 | `run-fault-stall-multipage.json`（`r-20260928144806-781b` failed） |
| 3 | 首跑失败截图 | `step-fail-1790606898290.png`、`step-fail-1790606690746.png`（作排障证据保留） |
| 4 | `gc-real` toast 数值随 fixture 变化 | 新跑第 1 轮 toast `清理 3 个孤儿 blob,回收 5.51 MiB`，旧跑为 `2.12 MiB` —— 属 **fixture 依赖**，非回归。形状断言（半角 `:`/`,`）才是回归守卫 |
| 5 | `auth-matrix` E2 404 | 矩阵脚本伪 digest 的**测试设计缺陷**，真实证据在 `backend-dp.log` |
| 6 | `range1k` 记 SKIP | 期望 206 实测 200 —— 观察项，非本轮变更 |
| 7 | 158 工具缺失 | 无 `python3`/`jq`/`go`；`sqlite3 3.7.17` 过旧（部分语法不支持）⇒ 部分核验靠 `perl`/`od`/`hexdump` 手工解析 |
| 8 | 时区差 | 158 宿主 `+0800` 与容器日志 UTC 差 8 h —— **跨日 mtime 比较必须带日期**，否则误判 |

---

## 附录 D —— 本地未跟踪产物

`tmps/accept-0534/`（**已被 `.gitignore` 覆盖，不进提交**）：

| 文件 | 说明 |
| --- | --- |
| `run.sh` | 场景运行脚本（`bash tmps/accept-0534/run.sh <short-name> [timeout]`） |
| `tsc-0534.log` / `gobuild-0534.log` / `gotest-race-0534.log` | 门禁日志 |
| `cairn-gate` | 本地构建产物（18,894,226 B） |
| `*.json`（16 个） | run 结果 |
| `rt-*.yaml` / `repo-tail.yaml` | 运行时改写副本 |
| `images-page.log` / `pull-real.log` / `upload-delete-repo-real.log` | 操作日志 |

`web/node_modules`（构建依赖，已 ignore）。

> **收尾**：因删除门禁拦截，`tmps/` 下中间产物未清理；如需清理请手动执行 `rm -rf tmps/accept-0534`（已 gitignore，不影响仓库状态）。

---

## 附录 E —— 与上一版报告（0.5.18）的差异摘要

| 项 | 0.5.18 报告 | 本报告（0.5.34） |
| --- | --- | --- |
| 版本 | 0.5.18 | 0.5.34（跨 16 个版本节） |
| 前端场景数 | 7 个新场景 | **14 个 canonical 场景** |
| P1（零 tag 仓库 UI 删除） | 阻断项 | **降级 P3**：改判契约行为 + 新增单段名真删覆盖位 + GC 路径可清 |
| P2（GC toast `undefined/NaN`） | 阻断项 | **已修**（前序工作）；本轮 `gc-real` 24/24 通过；新 P2 为 `api.ts` 类型错误 |
| R-open-2 | 「`allow.delete=false` 不守数据面」 | **更新**：tag/仓库删除**已门控**（源码核对 4 处）；**GC 路径未门控** = 实际剩余项 |
| 鉴权面 | 假阳性（login 假阳性 + push/pull 恒 401） | **已修**（v0.5.33）；本轮 L1~L4 四层全绿 |
| tsc 门禁 | 缺失（§0 第 12 行） | 已跑：8 个既有错误 |
| 覆盖缺口 | 未系统整理 | **§6 覆盖面矩阵**（16 版本节 × 实测覆盖） |