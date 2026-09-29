/**
 * v0.5.41:表格加载骨架屏 —— 替代 antd Table 的 `loading` 属性(它只显示一个居中 spinner,
 * 视觉上像「页面是空的」)。本组件用 antd `Skeleton` 画 N 行表格骨架,让用户在数据
 * 到达前看到「加载中」的占位结构,切换页面/刷新页面时不再「空白→突然满」。
 *
 * 设计原则:
 *   - 不引入新的设计语言,直接复用 antd Skeleton 的灰块 + 主题色变量
 *   - 行数与列数都是「目测」,真正的列宽由外层容器决定;这里用 `width: '60%'` 之类
 *     让骨架看起来像表格内容,而不是死板的等宽
 *   - 标题 + 描述可选,放在顶部;表格头部占位可选,放在列表之上
 */

import { Skeleton } from 'antd';

export interface TableSkeletonProps {
  /** 行数。默认 8 行。 */
  rows?: number;
  /** 列数(每行几个灰块)。默认 5 列。 */
  columns?: number;
  /** 顶部要不要标题占位(灰条 + 副标题灰条) */
  title?: boolean;
  /** 顶部描述占位(副标题下方再来一条灰小条) */
  description?: boolean;
}

export default function TableSkeleton({
  rows = 8,
  columns = 5,
  title = true,
  description = true,
}: TableSkeletonProps) {
  return (
    <div style={{ padding: '16px 0' }}>
      {title ? (
        <Skeleton.Input
          active
          size="small"
          style={{ width: 180, height: 22, marginBottom: 8 }}
        />
      ) : null}
      {description ? (
        <Skeleton.Input
          active
          size="small"
          style={{ width: 320, height: 14, marginBottom: 16 }}
        />
      ) : null}
      {/* 表格头占位 —— 列宽不等更接近真实表格 */}
      <div style={{ display: 'flex', gap: 16, padding: '10px 0', borderBottom: '1px solid var(--color-border)' }}>
        {Array.from({ length: columns }).map((_, i) => (
          <Skeleton.Input
            key={`h-${i}`}
            active
            size="small"
            style={{ width: 60 + ((i * 11) % 50), height: 14 }}
          />
        ))}
      </div>
      {/* 表格行占位 */}
      {Array.from({ length: rows }).map((_, r) => (
        <div
          key={`r-${r}`}
          style={{
            display: 'flex',
            gap: 16,
            padding: '14px 0',
            borderBottom: '1px solid var(--color-border-1)',
          }}
        >
          {Array.from({ length: columns }).map((_, c) => {
            // 每格宽度做点抖动:首列宽一点(像 repo 名 / 库名)、中间列短(像状态 / 平台)、末列窄(像延迟 / 时间)
            const widths = [40, 28, 22, 18, 14];
            const w = widths[c] ?? widths[widths.length - 1];
            const cellWidth = 40 + ((w + (r * 7) % 30));
            return (
              <Skeleton.Input
                key={`c-${r}-${c}`}
                active
                size="small"
                style={{ width: cellWidth, height: 14 }}
              />
            );
          })}
        </div>
      ))}
    </div>
  );
}