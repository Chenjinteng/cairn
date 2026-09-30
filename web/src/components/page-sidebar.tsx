/**
 * v0.5.37：UI 改版 — 页内 sub-nav 侧栏（220 px），对齐 docs/design/cairn-ui-design.html § 6。
 *
 * 这里是**顶栏 Segmented 6 Tab 之外的第二层导航**：顶栏负责"切到哪一页"，
 * 侧栏负责"当前这一页在看哪一块"。两者不是同一件事，所以同一个入口不要出现两遍。
 *
 * v0.5.37.3 起受控（selected / onSelect）；v0.5.37.4 起分组数据**不再写死在本文件里**：
 * 各页把自己真实的数据分组（含真实计数 badge）通过 onPublishGroups 上浮给 App，
 * App 再统一下发到这里。这样侧栏里的数字和表格里的行永远同源，
 * 不会出现「侧栏写死 4 / 实际 7 条」那种没人能对上的假 badge。
 *
 * 两条硬约束（v0.5.37.4 定）：
 *   1. 侧栏只放**视图状态**（过滤 / 切换 / 锚点跳转）。有副作用的操作
 *      （刷新、重新扫描、运行 GC、导出配置…）一律归页头 `.page-actions`。
 *   2. 同一状态只出现一次：页头已经有时间窗切换的，侧栏就别再放一份。
 *
 * 两种 group：
 *   - mode `'filter'`（默认）：item 是页面过滤条件。点击 → onSelect(groupKey, itemKey)，
 *     由 App 存进 pageFilter 再透传回页面。itemKey === `'all'` 表示"这一组不过滤"。
 *   - mode `'anchor'`：item key 就是页面里某个区块的 DOM id。点击平滑滚动过去，
 *     并随滚动做 scroll-spy 高亮（设置页那种"一页若干区块"的长页用）。
 */

import { useEffect, useMemo, useRef, useState } from 'react';

export type PageKey = 'images' | 'stats' | 'pull' | 'sync' | 'credentials' | 'proxies' | 'settings';

export interface SidebarItem {
  /** 组内唯一。`'all'` 是保留值：表示这一组不过滤。anchor 模式下就是目标区块的 DOM id。 */
  key: string;
  label: string;
  /** 真实计数。undefined = 不显示 badge —— 没有数据支撑的数字不要编。 */
  badge?: number;
}

export interface SidebarGroup {
  key: string;
  label: string;
  items: SidebarItem[];
  mode?: 'filter' | 'anchor';
}

/** 每页一份：groupKey -> itemKey。null / 缺失 = 这一组没有过滤。 */
export type SidebarSelection = Record<string, string | null>;

export interface PageSidebarProps {
  /** 由当前页上浮（onPublishGroups），App 原样下发。空数组 → 渲染空态。 */
  groups: SidebarGroup[];
  /** 当前页的选择状态。 */
  selected: SidebarSelection;
  /** filter 模式 item 点击回调；anchor 模式不回调（只滚动）。 */
  onSelect: (groupKey: string, itemKey: string) => void;
  /** 运行中的版本号（/api/config 给的）。footer 不再写死，免得跟镜像 tag 漂移。 */
  version?: string;
}

export default function PageSidebar({ groups, selected, onSelect, version }: PageSidebarProps) {
  const anchorIds = useMemo(
    () =>
      groups
        .filter((group) => group.mode === 'anchor')
        .flatMap((group) => group.items.map((item) => item.key)),
    [groups],
  );

  /** scroll-spy 当前命中的锚点 id。点击时先手动设上，滚动过程中不闪回。 */
  const [activeAnchor, setActiveAnchor] = useState<string | null>(null);
  const visibleRef = useRef<Map<string, boolean>>(new Map());

  useEffect(() => {
    if (anchorIds.length === 0) {
      setActiveAnchor(null);
      return;
    }
    let observer: IntersectionObserver | null = null;
    let retryTimer: number | null = null;
    let retries = 0;
    visibleRef.current = new Map();

    const pick = () => {
      // 取"落在观察带里"的第一个锚点（与侧栏从上到下的顺序一致）。
      // 用第一个而不是"交叉面积最大"的那个，避免快速滚动时在两个区块间来回跳。
      const hit = anchorIds.find((id) => visibleRef.current.get(id));
      if (hit) setActiveAnchor(hit);
    };

    const attach = () => {
      const nodes = anchorIds
        .map((id) => document.getElementById(id))
        .filter((node): node is HTMLElement => node !== null);
      // 锚点由各页渲染，首帧可能还没进 DOM（页面自己在等接口）。
      // 有限重试几次，还拿不到就静默放弃 —— 顶多没有 scroll-spy 高亮，
      // 点击跳转仍然可用。
      if (nodes.length < anchorIds.length && retries < 6) {
        retries += 1;
        retryTimer = window.setTimeout(attach, 250);
        return;
      }
      if (nodes.length === 0) return;
      observer = new IntersectionObserver(
        (entries) => {
          entries.forEach((entry) => {
            visibleRef.current.set(entry.target.id, entry.isIntersecting);
          });
          pick();
        },
        // 观察带压到视口 20%~40% 之间那条横带：区块滚到接近顶部就算"正在看它"，
        // 比"完全可见"更接近人的判断，也不会一进页面就整屏高亮。
        { rootMargin: '-20% 0px -60% 0px', threshold: 0 },
      );
      nodes.forEach((node) => observer?.observe(node));
    };

    attach();
    return () => {
      if (retryTimer !== null) window.clearTimeout(retryTimer);
      observer?.disconnect();
    };
  }, [anchorIds]);

  const handleAnchorClick = (id: string) => {
    setActiveAnchor(id);
    document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  };

  const isActive = (group: SidebarGroup, item: SidebarItem) => {
    if (group.mode === 'anchor') return activeAnchor === item.key;
    // 这一组还没选过东西：有「全部」就亮「全部」，没有（比如时间窗，页面已有默认值）
    // 就整组不亮，等选中再亮。
    const current = selected[group.key] ?? null;
    return current === null ? item.key === 'all' : current === item.key;
  };

  return (
    <aside className="app-page-sidebar" aria-label="页面内导航">
      <div className="page-sidebar-content">
        {groups.length > 0 ? (
          groups.map((group) => (
            <div key={group.key} className="page-sidebar-group">
              <div className="page-sidebar-group-label">{group.label}</div>
              {group.items.map((item) => {
                const active = isActive(group, item);
                return (
                  <button
                    key={item.key}
                    type="button"
                    className={`page-sidebar-item${active ? ' is-active' : ''}`}
                    aria-current={active ? 'true' : undefined}
                    onClick={() =>
                      group.mode === 'anchor'
                        ? handleAnchorClick(item.key)
                        : onSelect(group.key, item.key)
                    }
                  >
                    <span className="page-sidebar-item-label">{item.label}</span>
                    {item.badge !== undefined ? (
                      <span className="page-sidebar-item-badge">{item.badge}</span>
                    ) : null}
                  </button>
                );
              })}
            </div>
          ))
        ) : (
          // 页面数据还没到（或这一页此刻确实没有可筛的东西）时不要留一片空白，
          // 否则看起来像侧栏坏了。
          <div className="page-sidebar-empty">当前页面暂时没有可用的筛选项</div>
        )}
      </div>
      {/* v0.5.37：sidebar footer block — 让 grid 撑满 100vh 时底部不再是大段空白。
          v0.5.37.4：版本号从写死的字符串改成读 /api/config 的运行中版本，
          跟顶栏那个 badge 同源，避免侧栏还写着上个版本。
          v0.5.37.6：footer 挂「产品介绍」入口 —— 该页随二进制分发（web/public/
          经 vite publicDir 拷进 dist，被 go:embed all:dist 收进二进制）。
          v0.5.51：去掉 target="_blank"，改同 tab 打开。0.5.37.6 加 _blank
          是怕跳页丢筛选状态，但代价是用户被强行推到新 tab；现在点 cairn-intro
          footer 的「管理控制台 →」按钮 / 浏览器后退就能回来，体验更顺。 */}
      <div className="page-sidebar-footer mono">
        {version ? `v${version} · ` : ''}
        <a href="/cairn-intro.html">
          产品介绍
        </a>
        {' · © Cairn'}
      </div>
    </aside>
  );
}