/**
 * v0.5.37：UI 改版 — 页内 sub-nav 侧栏（220 px），对齐 docs/design/cairn-ui-design.html § 6。
 *
 * 设计意图（来自 demo-A-*.html）：
 *   - 顶栏 Segmented 6 Tab 仍是顶级导航（不变），本组件是**每个 page 内部**的 sub-nav。
 *   - 每个 page 自己一组 group + item，例如：
 *       images-page       → 仓库（全部/library/registry-manager/webauto-push）/ 操作（扫描清单 / 运行 GC）
 *       credentials-page  → 分组（全部 / 公司内网 / 公共 / 第三方）/ 最近使用（24h / 7d / 从未使用）
 *       pull-page         → 来源（公共 registry / 内网 registry / 自定义）/ 历史（24h / 7d / 全部）
 *       stats-page        → 时间窗（7d / 30d / 90d / 全部）/ 客户端 UA
 *       proxies-page      → 协议（http / https / socks5）/ 状态（连通 / 从未测试 / 失败）
 *       settings-page     → 分类（Registry / 鉴权 / 拉取 / 热度 / 通知）
 *   - v0.5.37 范围内先做"UI 占位 + active 高亮"，不联动 page 内容过滤
 *     （避免触动每个 page 内部 state）；后续 0.5.x 优化时再加联动。
 */

import { useState } from 'react';

export type PageKey = 'images' | 'stats' | 'pull' | 'credentials' | 'proxies' | 'settings';

type SideItem = { label: string; badge?: string | number };
type SideGroup = { label: string; items: SideItem[] };

const SIDEBAR_MAP: Record<PageKey, SideGroup[]> = {
  images: [
    {
      label: '仓库',
      items: [
        { label: '全部', badge: 4 },
        { label: 'library', badge: 1 },
        { label: 'registry-manager', badge: 1 },
        { label: 'webauto-push', badge: 2 },
      ],
    },
    {
      label: '操作',
      items: [{ label: '扫描清单' }, { label: '运行 GC' }, { label: '备份' }],
    },
  ],
  credentials: [
    {
      label: '分组',
      items: [
        { label: '全部', badge: 5 },
        { label: '公司内网', badge: 2 },
        { label: '公共', badge: 2 },
        { label: '第三方', badge: 1 },
      ],
    },
    {
      label: '最近使用',
      items: [{ label: '最近 24h' }, { label: '最近 7d' }, { label: '从未使用' }],
    },
    {
      label: '状态',
      items: [
        { label: '连通正常', badge: 3 },
        { label: '从未测试', badge: 1 },
        { label: '上次失败', badge: 1 },
      ],
    },
  ],
  pull: [
    {
      label: '来源',
      items: [
        { label: '公共 registry', badge: 3 },
        { label: '内网 registry', badge: 2 },
        { label: '自定义', badge: 1 },
      ],
    },
    {
      label: '历史',
      items: [{ label: '最近 24h' }, { label: '最近 7d' }, { label: '全部' }],
    },
  ],
  stats: [
    {
      label: '时间窗',
      items: [{ label: '最近 7d' }, { label: '最近 30d' }, { label: '最近 90d' }, { label: '全部' }],
    },
    {
      label: '客户端 UA',
      items: [
        { label: '全部', badge: 5 },
        { label: 'docker' },
        { label: 'skopeo' },
        { label: 'containerd' },
      ],
    },
  ],
  proxies: [
    {
      label: '协议',
      items: [{ label: 'http', badge: 3 }, { label: 'https', badge: 2 }, { label: 'socks5', badge: 1 }],
    },
    {
      label: '状态',
      items: [
        { label: '连通', badge: 4 },
        { label: '从未测试', badge: 1 },
        { label: '上次失败', badge: 1 },
      ],
    },
  ],
  settings: [
    {
      label: '分类',
      items: [
        { label: 'Registry' },
        { label: '鉴权' },
        { label: '拉取' },
        { label: '热度' },
        { label: '通知' },
      ],
    },
    {
      label: '操作',
      items: [{ label: '导出配置' }, { label: '导入配置' }, { label: '重置' }],
    },
  ],
};

export interface PageSidebarProps {
  page: PageKey;
}

export default function PageSidebar({ page }: PageSidebarProps) {
  const groups = SIDEBAR_MAP[page];
  // 第一个 group 的第一个 item 默认 active（demo 行为）；后续 0.5.x 联动时改成受控。
  const [active, setActive] = useState<string>(() => `${groups[0]?.label}:${groups[0]?.items[0]?.label ?? ''}`);

  return (
    <aside className="app-page-sidebar" aria-label={`${page} sub navigation`}>
      <div className="page-sidebar-content">
        {groups.map((group) => (
          <div key={group.label} className="page-sidebar-group">
            <div className="page-sidebar-group-label">{group.label}</div>
            {group.items.map((item) => {
              const itemId = `${group.label}:${item.label}`;
              const isActive = active === itemId;
              return (
                <button
                  key={item.label}
                  type="button"
                  className={`page-sidebar-item${isActive ? ' is-active' : ''}`}
                  onClick={() => setActive(itemId)}
                >
                  <span className="page-sidebar-item-label">{item.label}</span>
                  {item.badge !== undefined ? (
                    <span className="page-sidebar-item-badge">{item.badge}</span>
                  ) : null}
                </button>
              );
            })}
          </div>
        ))}
      </div>
      {/* v0.5.37：sidebar footer block — 让 grid 撑满 100vh 时底部不再是大段空白。
          设计稿 § 6 demo 没画 footer，但 grid 撑满是 sidebar 内容少时的预期表现，
          加 footer 让 sidebar 看起来是"完整结构"而不是"上面 200px + 下面 300px 空白"。 */}
      <div className="page-sidebar-footer mono">
        v0.5.37 · © Cairn
      </div>
    </aside>
  );
}