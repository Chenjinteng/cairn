# cairn 缺陷登记

> **整理日期**：2026-09-30（UTC；本地为 2026-10-01 CST）
> **被测版本**：cairn `0.6.11`（`internal/version/version.go:18`，commit `f66bd4d`）
> **测试环境**：registry = `registry.local:10001`（容器 `cairn:0.6.11`，`env=prod`）；前端 E2E = `runner.local:8080` test-runner（`go-hub/*` 场景 26 个，其中 Sync 页 6 个为本轮新增）
> **取证口径**：每条缺陷必须同时有「源码 file:line」与「实机实测」两类证据；只有源码推断、或只有一次偶发失败的项一律降级为观察项（见下文「明确不记为缺陷的观察项」）。
> **编号分区**：`REG-*` 镜像协议（`/v2/*`）｜`MA-*` 管理 API（`/api/*`）｜`TH-*` 测试脚手架（Makefile / web-auto runner / 场景）
> **修复状态**：
> - 0.6.12 (commit `a5577ea`, 2026-10-01) **修复了 10 条**：MA-1/3/4/5/6、REG-1/2/4、TH-1/4/5。
> - 0.6.13 (后续, 2026-10-01) **修复了 3 条**：REG-3 / REG-5 / REG-6。
>
> 其余 11 条按各文件末尾"修复落地"段标记未修复或部分修复;TH-3 此前已在 53 runner 侧就地修复（baseUrl 改 `10001`）。

## 总览

| 编号 | 严重度 | 一句话 | 详情 |
| --- | --- | --- | --- |
| **MA-1** | **High** | `/api/repositories/{repo}` 路径参数对含 `/` 的仓库名全失效（158 上 84% 仓库受影响；前端「删除仓库」「按 digest 删 manifest」对这批仓库不可用） | [management-api.md](./management-api.md) |
| **MA-5** | **High** | sync 删除端点返 `204` 撞前端 JSON 信封 → **删除已生效却弹「删除失败」**，且列表不刷新（UI 状态与后端分裂） | [management-api.md](./management-api.md) |
| REG-1 | Medium | `PUT` manifest 不校验 payload，任意 JSON 也 `201` 并持久化 | [registry-protocol.md](./registry-protocol.md) |
| REG-2 | Medium | 未知仓库 `tags/list` 返回 `200` + 空数组（应为 `404 NAME_UNKNOWN`） | [registry-protocol.md](./registry-protocol.md) |
| REG-4 | Medium | `DELETE` manifest 未命中返回 `404` 但 body 为 0 字节；其余错误 `500` 误用 `UNSUPPORTED` | [registry-protocol.md](./registry-protocol.md) |
| MA-4 | Medium | `DELETE /api/repositories/{repo}` 未命中返回 `500` 而非 `404` | [management-api.md](./management-api.md) |
| TH-1 | Medium | Makefile 场景前缀 `cairn/` 与实际 `go-hub/` 不符 → `make test-frontend` 必然全挂 | [test-harness.md](./test-harness.md) |
| TH-2 | Medium | `RUNNER_BASE` 默认值是脱敏占位域名，用默认值必然连不上 | [test-harness.md](./test-harness.md) |
| TH-3 | Medium | runner dev `baseUrl` 曾指向 `:80`（已在 53 就地修复，建议回写上游） | [test-harness.md](./test-harness.md) |
| TH-6 | Medium | 20 个前端场景零覆盖 Sync 页面（0.6.x 新功能无 UI 回归保护） | [test-harness.md](./test-harness.md) |
| REG-3 | Low | `/v2/` 缺 `Docker-Distribution-Api-Version` 响应头；`GET 200` / `HEAD 404` | [registry-protocol.md](./registry-protocol.md) |
| REG-5 | Low | blob `GET` 无显式 `Content-Type`（靠 Go 内容嗅探）；不支持 `Range` | [registry-protocol.md](./registry-protocol.md) |
| REG-6 | Low | 无法取消 upload session（`DELETE` → `405`），遗留孤儿目录只能人工清理 | [registry-protocol.md](./registry-protocol.md) |
| MA-3 | Low | `POST /api/gc` 非法 JSON body 被静默忽略仍 `200` 并执行 GC | [management-api.md](./management-api.md) |
| MA-6 | Low | `GET /api/sync/{id}/runs` 对不存在的任务返 `200` + `[]` 而非 `404`（兄弟端点 `/schedules` 已有正确守卫） | [management-api.md](./management-api.md) |
| TH-4 | Low | `test-frontend-fast` 注释称 9 个场景实际列 8 个，且含写路径 `pull-real` | [test-harness.md](./test-harness.md) |
| TH-5 | Low | `test-backend` 的期望与 dev 部署不符（写死 `cairn:0.5.18-dev`） | [test-harness.md](./test-harness.md) |
| TH-7 | Low | runner 超时/诊断一组缺口（超时步不入 `steps[]`、`error` 不含步名、单步预算只用全局 `timeout`…） | [test-harness.md](./test-harness.md) |
| TH-8 | Low | 6 个场景的选择器/断言与实际 UI（刻意变更）或数据前置不符 | [test-harness.md](./test-harness.md) |
| TH-9 | Low | `delete-repo-real` / `registries-list` 硬编码 fixture 且假设目标在第 1 页 | [test-harness.md](./test-harness.md) |
| TH-10 | Low | `gc-real` 第 2 轮 toast 断言竞态 | [test-harness.md](./test-harness.md) |

> `MA-2` 号位与 MA-1 同源（同一路由缺陷的另一处表现），已并入 MA-1，故缺号。

## 后续测试轮次

| 轮次 | 被测版本 | 文件 | 处置 |
| --- | --- | --- | --- |
| UAT 手工轮 | 0.6.14 | [ui-0.6.14.md](./ui-0.6.14.md) | UI-1 / UI-2 / UI-3 / UI-5 在 0.6.15 修；UI-4 需 schema 变更、UI-6a / UI-6b 属新增能力，均待中版本进位拍板 |

## 明确不记为缺陷的观察项

这些现象在本轮测试中被观察到，但经源码/协议核对后判定为**有意设计或协议允许**，不作为缺陷上报：

| 观察 | 依据 |
| --- | --- |
| 删除仓库后其 blobs 仍被其他仓库引用/可见（blobs 全局共享） | `internal/storage/filesystem.go:704-706` 注释明说 blobs 是 content-addressed 全局共享、删除仓库不动 blobs —— 设计如此 |
| 上传会话响应头 `Range: 0--1`（而非 `0-0`） | 与 Docker Distribution 的既有行为一致，非缺陷 |
| Pull 作业「取消」后终态接口仍返回 `200` | `internal/pull/queue.go:288-289` 刻意幂等，避免重复取消报错 |
| manifest 请求的 `Accept` 内容协商被忽略（按存储时的 mediaType 回显） | Docker/OCI 语义中该协商为 MAY，客户端按 digest 校验不受影响 |
| 未匹配的 `/api/*` 返回 `404 page not found`（`text/plain`）而非 SPA HTML | 对 API 路径是合理行为；只有非 API 路径才走 SPA fallback（`internal/webui/webui.go:43-51`） |

## 复现环境速查

```bash
# ── 产品端（158，registry + 管理 API 同一端口）────────────────
curl --noproxy '*' -sS -i http://registry.local:10001/v2/                                    # REG-3
curl --noproxy '*' -sS    http://registry.local:10001/v2/qa-norepo/tags/list                 # REG-2
curl --noproxy '*' -sS -i 'http://registry.local:10001/api/repositories/3proxy%2F3proxy/tags/1.0.0/manifest'   # MA-1（对照 /v2/3proxy/3proxy/manifests/1.0.0 → 200）
curl --noproxy '*' -sS -i -X DELETE 'http://registry.local:10001/api/repositories/qa-norepo' # MA-4
curl --noproxy '*' -sS -i http://registry.local:10001/api/sync/999/runs                     # MA-6（200 + []，对照 GET /api/sync/999 → 404）
# MA-5：需先建一个临时任务（POST /api/sync），再
#   curl --noproxy '*' -sS -i -X DELETE 'http://registry.local:10001/api/sync/<新建的id>'    # → 204 空 body（前端据此弹「删除失败」，实际已删）
#   curl --noproxy '*' -sS -i 'http://registry.local:10001/api/sync/<新建的id>'              # → 404，证明删除确实生效
# ⚠️ 勿对 id=1「Sync 58」执行 DELETE（它是唯一的真实同步任务）

# ── 前端 E2E（53 runner）─────────────────────────────────────
curl --noproxy '*' -sS -X POST http://runner.local:8080/api/run \
  -H 'Content-Type: application/json' \
  -d '{"scenario":"go-hub/login","env":"dev","timeout":300000}'
curl --noproxy '*' -sS http://runner.local:8080/api/scenarios    # 实际加载的场景清单（前缀是 go-hub/，见 TH-1）
```

环境事实（写 issue 时按此口径）：

- 158 容器是 **scratch 镜像**，`docker exec` 不可用；数据只能从宿主机 bind mount `/data/cairn` 检查（仓库目录 `/data/cairn/registry/repos/<repo>`，上传目录 `/data/cairn/registry/uploads/<repo>`）。
- 158 无 `python3` / `jq` / `nc` / `sqlite3`（只有 py2、`perl`、`timeout`、`setsid`、`nohup`）。
- `_catalog` 实测 **77 个仓库**，其中 **65 个含 `/`**（MA-1 的影响面口径；77 是测量时点数，含本轮临时夹具 `qa-probe-x`，清理后为 76，不影响「多数仓库含 `/`」这一结论）。
- registry 的协议路由（`/v2/*`）与管理 API（`/api/*`）**走两套不同的路径参数机制**，MA-1 正是两套机制不一致导致的后果，机制说明见 [registry-protocol.md](./registry-protocol.md) 开头。