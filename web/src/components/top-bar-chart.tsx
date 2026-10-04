import { Tooltip } from 'antd';

import type { StatsTopItem } from '../types';
import { formatDateTime } from '../utils';

/**
 * Top 榜单 —— 横向条形图。
 *
 * 选型理由：「Top N」本质是按量级排序的单变量排名,横向条形图（也叫 Pareto
 * 排名图）就是把这件事的视觉糖剥掉 —— 直接比条长,不用读数字再脑内比较。
 * 标签再长也不影响布局（左侧预留宽度 + ellipsis）,不会出现表格那种
 * 「列太宽撑出横向滚动条 / 列太窄换行」的两难。
 *
 * 与按天趋势的关系：热力图回答「什么时候热」（时间维度）,条形图回答
 * 「什么最热」（对象维度）。两个图互补,所以按用户最新反馈把热力图放
 * 前面、Top 条形图放后面,先看节奏再看排行。
 *
 * 不引图表库,纯 HTML + CSS（fill 走语义 token）—— 跟按天趋势同一套
 * 「不背 echarts/g2 包袱」的克制。
 */
export default function TopBarChart({ items }: { items: StatsTopItem[] }) {
  if (items.length === 0) {
    return <div className="top-bar-empty">该时间窗内没有可排行的数据</div>;
  }
  const max = Math.max(...items.map((i) => i.events), 1);
  return (
    <ul className="top-bar-list">
      {items.map((item, idx) => {
        // 第一名 = 满条;之后按 events / max 比例填。
        // max 用项内的最大值,不用全局 100%,让第一名永远是满条,后续
        // 直观比较相对差距 —— 这是排名图的核心。
        const widthPct = (item.events / max) * 100;
        const label = item.tag ? `${item.repository}:${item.tag}` : item.repository;
        const tooltip = (
          <div style={{ lineHeight: 1.6 }}>
            <div className="mono">{label}</div>
            <div style={{ opacity: 0.85, marginTop: 2 }}>
              合计 {item.events} 次 · 拉 {item.pull} / 推 {item.push}
            </div>
            <div style={{ opacity: 0.85 }}>
              最近活动：{formatDateTime(item.lastAt, '—')}
            </div>
          </div>
        );
        return (
          <li
            key={`${item.repository}\u0000${item.tag ?? ''}`}
            className="top-bar-row"
          >
            <span className="top-bar-rank">#{idx + 1}</span>
            <Tooltip title={tooltip} placement="topLeft">
              <span className="top-bar-label mono">{label}</span>
            </Tooltip>
            <span className="top-bar-track">
              <span
                className="top-bar-fill"
                style={{ width: `${widthPct}%` }}
              />
            </span>
            <span className="top-bar-value">{item.events}</span>
          </li>
        );
      })}
    </ul>
  );
}