# cairn 前端

`cairn` 的前端，从 [registry-manager](http://github.com/Chenjinteng/registry-manager.git) 的 `web/` 直接复制而来。

UI 风格、交互、状态机与 registry-manager **保持完全一致**。后端 API 由 Go 重写，前端不感知。

## 开发

```bash
# 拉依赖（需要 node 22+ 和 pnpm）
pnpm install

# 开发态：Vite 提供前端 + HMR；/api 代理到本地 cairn 服务（默认 :8787）
pnpm dev
# 浏览器开 http://localhost:5273
```

## 构建

```bash
pnpm build
# 产出 dist/，由 cairn 服务（或 nginx）同源托管
```

## 与 registry-manager 的差异

代码层面只有这几处：

| 文件 | 改动 | 原因 |
|---|---|---|
| `vite.config.ts` | 去掉 `root: 'web/'`（cairn 已经在 `web/` 里） | 目录结构差异 |
| `package.json` | 重命名 `cairn-web`，去掉 Node 后端依赖 | cairn 后端是 Go |
| 其它 | 原样 copy | 行为、视觉一致 |

## API 约定

所有 `/api/*` 响应都包在 `{success, code, message, data}` 信封里（与 registry-manager 一致）。

- 成功：`{success: true, code: 'OK', message: '', data: <payload>}`
- 失败：`{success: false, code: '...', message: '...', error: {code, message, detail}}`

后端真实响应在浏览器 Network 面板里能看到，但 `data` 字段才是组件真正用的。