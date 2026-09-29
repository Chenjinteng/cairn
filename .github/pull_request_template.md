## 描述

<!-- 一句话说明这个 PR 改了什么,解决了什么问题 -->

## 关联 Issue

<!-- "Closes #123" / "Refs #456" 链接到对应 issue;无 -->

## 类型

<!-- 单选 / 多选 -->

- [ ] 功能 / 新模块
- [ ] 缺陷修复
- [ ] 重构 / 优化
- [ ] 文档
- [ ] 测试
- [ ] 构建 / 工具

## 改动清单

<!-- 简要列出主要文件 / 主要行为 -->
- <p align="left"></p>
- <p align="left"></p>

## 自查清单(必填)

<!-- 提交前自己跑过 / 自查过 -->

- [ ] 本机跑过 `make gates` 全绿
- [ ] 新增 / 改动的 Go 代码有单元测试覆盖
- [ ] 改动触发了文档同步(`README.md` / `AGENTS.md` / `CHANGELOG.md` / `docs/`)
- [ ] 改动触发了 `internal/version/version.go` 之外的版本号同步(5 处检查清单)
- [ ] commit message 遵循 [Conventional Commits](https://www.conventionalcommonical.org/) 规范
- [ ] 改动不命中 `docs/ROADMAP.md`「刻意不做」段列出的禁项
- [ ] 没有引入不必要的依赖(`go.mod` / `web/package.json` 变化已自查)
- [ ] 没有调试代码 / `console.log` / `fmt.Println` 残留

## 测试说明

<!-- 怎么验证这个 PR 的改动是对的 -->

- 复现步骤:<p align="left"></p>
- 预期行为:<p align="left"></p>
- 实际行为(修复前):<p align="left"></p>

## 截图(如有 UI 变化)

<!-- 拖图;CI / 排版没影响可以不填 -->

## 影响范围

<!-- 这次改动对用户的影响 -->

- **是** 行为变化(用户能看到的功能差异)
- **是** 部署面变化(`.env` / `docker-compose.yml` / 数据目录结构)
- **是** API breaking(`/api/*` 接口字段 / 路径变化)
- **否** 仅内部重构

## 关联文档更新

<!-- "已更新 CHANGELOG.md 0.5.X 条目" / "无" -->

## 评审者关注点(可选)

<!-- 哪些设计决策特别需要 reviewer 看一下 -->