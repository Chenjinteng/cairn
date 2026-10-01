/**
 * v0.6.21: 全站统一的"加载中"占位 —— 居中 spinner + 文案。
 *
 * 之前首屏用的是 `TableSkeleton`(antd 的灰块 shimmer,假装是表格)。
 * 用户反馈:「一闪一闪的」—— 灰块 shimmer 动画频率看着像在闪,而且「假装表格」
 * 跟真实表格差太远,数据到达瞬间「假装 → 真」的硬切也有 flicker 感。
 *
 * 现在换成居中 `<Spin>` + 一行小字:明确告诉用户"我在等接口数据",
 * 数据到达时 spinner 直接收掉,过渡更干净。
 *
 * 设计要点:
 *   - 默认 200px 高的居中块,跟原来 TableSkeleton 高度相近,切换页面不会跳行
 *   - 用 antd 自带 Spin,颜色跟 ConfigProvider 的 colorPrimary 对齐(默认 teal)
 *   - 文案默认「加载中…」,具体页面可以传 `tip` 定制成「正在读取镜像列表…」之类
 *   - 不再要 `title / description / rows / columns` 等骨架参数 —— 真不假装表格了
 */

import { Spin } from 'antd';

export interface PageLoadingProps {
  /** 加载文案;默认「加载中…」 */
  tip?: string;
  /** 占位高度(像素);默认 200。页面内容区更高时设大点,免得 spinner 缩在角落。 */
  height?: number;
}

export default function PageLoading({
  tip = '加载中…',
  height = 200,
}: PageLoadingProps) {
  return (
    <div
      role="status"
      aria-live="polite"
      style={{
        display: 'flex',
        justifyContent: 'center',
        alignItems: 'center',
        minHeight: height,
        flexDirection: 'column',
        gap: 12,
        color: 'var(--color-text-tertiary, #999)',
        fontSize: 13,
      }}
    >
      <Spin size="large" />
      <span>{tip}</span>
    </div>
  );
}