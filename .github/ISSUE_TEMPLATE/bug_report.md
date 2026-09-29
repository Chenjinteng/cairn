---
name: Bug report
about: 报告 bug —— 请提供复现步骤与日志
title: '[bug] '
labels: bug
assignees: ''
---

## 现象 / Expected vs Actual

**Expected**: <p align="left"></p>

**Actual**: <p align="left"></p>

## 复现步骤

<!-- 一步步写,让 reviewer 能本地复现 -->

1. <p align="left"></p>
2. <p align="left"></p>
3. <p align="left"></p>

## 环境信息

- **镜像 tag / commit hash**:`<p align="left"></p>`
- **Gitea 部署方式**:`<p align="left"></p>`(docker compose / k8s / binary / dev)
- **操作系统**:<p align="left"></p>
- **架构**:<p align="left"></p>(amd64 / arm64 / ...)
- **数据库目录**:`<p align="left"></p>`(`/data/cairn` 还是其它)

## 日志 / 截图

<!-- `docker compose logs cairn --tail=200` / 浏览器 devtools / 截图 -->

```text
<paste logs here>
```

## `/api/config` 输出

<!-- 帮助诊断;注意脱敏 registry.url / 凭据 -->

```json
{
  "version": "...",
  "allowPull": true,
  "allowDelete": false,
  ...
}
```

## 关联 Issue / 线索

<!-- 已知问题 / 类似报告 / PR -->