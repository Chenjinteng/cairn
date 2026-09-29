import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { App as AntdApp, Button, Segmented, Tag, Tooltip } from 'antd';
import {
  ApiOutlined,
  BarChartOutlined,
  CloudDownloadOutlined,
  DockerOutlined,
  KeyOutlined,
  MoonOutlined,
  SettingOutlined,
  SunOutlined,
} from '@ant-design/icons';

import { ensureConfigLoaded, publishConfig, useAppConfig } from './config-store';

import ImagesPage from './pages/images-page';
import PullPage from './pages/pull-page';
import CredentialsPage from './pages/credentials-page';
import ProxiesPage from './pages/proxies-page';
import SettingsPage from './pages/settings-page';
import StatsPage from './pages/stats-page';
import CairnMark from './components/cairn-mark';
import PageSidebar, {
  type PageKey,
  type SidebarGroup,
  type SidebarSelection,
} from './components/page-sidebar';
import type { Inventory } from './types';

/**
 * v0.5.37.4：每页 sidebar 的选择状态。
 *
 * 从 v0.5.37.3 的「一个字符串」改成「每页一张 groupKey -> itemKey 的表」，
 * 因为现在侧栏可以有几组并存的过滤条件（拉取页的「状态」+「来源」、
 * 代理页的「协议」+「状态」），跨组是 AND 关系，不能再挤进一个字符串里。
 *
 * 切页时不重置 —— 比如「在镜像列表选 library 后切到凭据管理再切回来」，
 * 选择还在，跟浏览器表单持久化语义一致。
 */
type PageFilter = Record<PageKey, SidebarSelection>;

/** 页面还没上浮分组数据时用的空数组：常量引用，避免每次渲染换身份。 */
const NO_GROUPS: SidebarGroup[] = [];

/** 界面主题。持久化与首屏应用都在 main.tsx / index.html 里，这里只负责展示与切换。 */
export type ThemeMode = 'light' | 'dark';

/**
 * v0.5.37：整体布局
 *   header（顶栏 56px；brand + 顶部 Segmented 6 Tab 导航 + user）→
 *   main（p-4）→ body（grid: 220px 侧栏 + 1fr 内容区）→ 页面内容
 *
 * 顶栏 Segmented 6 Tab 是顶级导航，不动；
 * 220px 侧栏是**每页内部**的 sub-nav（仓库筛选 / 拉取状态 / 时间窗 / 协议分类 等）。
 *
 * v0.5.37.4：侧栏分组不再由 PageSidebar 自己写死（老实现是 6 页假 badge），
 * 改为**各页上浮真实分组**：页面 useMemo 出自己的 groups（badge 来自它手上那份数据），
 * 通过 onPublishGroups 交给 App 存进 pageGroups，App 再统一下发给 PageSidebar。
 * 这样侧栏里的计数和表格里的行永远同源。
 */
const NAV_ITEMS: { key: PageKey; label: string; icon: ReactNode }[] = [
  { key: 'images', label: '镜像列表', icon: <DockerOutlined /> },
  { key: 'pull', label: '镜像拉取', icon: <CloudDownloadOutlined /> },
  { key: 'stats', label: '镜像热度', icon: <BarChartOutlined /> },
  { key: 'credentials', label: '凭据管理', icon: <KeyOutlined /> },
  { key: 'proxies', label: '代理管理', icon: <ApiOutlined /> },
  { key: 'settings', label: '设置', icon: <SettingOutlined /> },
];

export default function App({
  mode,
  onToggleMode,
}: {
  mode: ThemeMode;
  onToggleMode: () => void;
}) {
  // 两个页面共享同一份清单：切换页面不该重新抓取 registry。
  const [page, setPage] = useState<PageKey>('images');
  const [inventory, setInventory] = useState<Inventory | null>(null);
  // v0.5.37.4：每页 sidebar 的当前选择（groupKey -> itemKey）。
  // stats 的时间窗默认 30 天，跟页面自身的 days 默认值对齐 —— 否则首屏侧栏
  // 会显示「30d 高亮」而页面按别的天数在拉数据，看着像坏了。
  const [pageFilter, setPageFilter] = useState<PageFilter>({
    images: {},
    stats: { window: '30d' },
    pull: {},
    credentials: {},
    proxies: {},
    settings: {},
  });
  const currentFilter = pageFilter[page];

  /** 各页上浮上来的侧栏分组（v0.5.37.4）。页面没上浮前就是空态。 */
  const [pageGroups, setPageGroups] = useState<Partial<Record<PageKey, SidebarGroup[]>>>({});

  /**
   * 页面 effect 会把 groups 推上来。同一次渲染里重复推同一个数组引用直接返回 prev，
   * 避免「推 → App 重渲染 → 页面 effect 再跑 → 再推」的自激循环。
   */
  const publishGroups = useCallback((key: PageKey, groups: SidebarGroup[]) => {
    setPageGroups((prev) => (prev[key] === groups ? prev : { ...prev, [key]: groups }));
  }, []);

  /**
   * 各页拿到的 onPublishGroups 必须是**稳定引用**，否则页面里
   * `useEffect(..., [groups, onPublishGroups])` 每次 App 渲染都会重跑。
   */
  const publishHandlers = useMemo(() => {
    const bind = (key: PageKey) => (groups: SidebarGroup[]) => publishGroups(key, groups);
    return {
      images: bind('images'),
      stats: bind('stats'),
      pull: bind('pull'),
      credentials: bind('credentials'),
      proxies: bind('proxies'),
      settings: bind('settings'),
    };
  }, [publishGroups]);

  /**
   * filter 模式 item 点击：写进当前页的选择表。`'all'` 存成 null（"这一组不过滤"）。
   * 点已经选中的那一项是**无操作** —— 不做 toggle-off，否则「再点一下取消过滤」
   * 和「点全部取消过滤」两种清空方式并存，反而看不出当前到底有没有过滤。
   */
  const handleSidebarSelect = useCallback(
    (groupKey: string, itemKey: string) => {
      const next = itemKey === 'all' ? null : itemKey;
      setPageFilter((prev) => {
        if ((prev[page][groupKey] ?? null) === next) return prev;
        return { ...prev, [page]: { ...prev[page], [groupKey]: next } };
      });
    },
    [page],
  );
  /**
   * v0.5.18（F6）：服务配置收在模块级 store 里（/api/config 单一入口，带缓存 +
   * 单飞）。过去 5 个页面各自拉一遍，除了重复请求，还各存一份失败态。
   *
   * 这里只负责把它启动起来；页面通过 useAppConfig() 自取所需。
   */
  const {
    config,
    failure: configFailure,
    loading: configLoading,
    reload: reloadConfig,
  } = useAppConfig();

  useEffect(() => {
    void ensureConfigLoaded();
  }, []);

  const segmentedOptions = NAV_ITEMS.map((item) => ({
    value: item.key,
    label: (
      <span className="app-nav-label">
        <span className="app-nav-icon">{item.icon}</span>
        {item.label}
      </span>
    ),
  }));

  return (
    <AntdApp>
      <div className="app-shell">
        <header className="app-header">
          <div className="app-brand">
            {/* v0.5.21: 品牌升级 —— 顶部 brand 从 antd DockerOutlined + "镜像仓库管理"
                换成自定义的 Cairn 标记 + 产品名 "Cairn"。NAV_ITEMS 里的
                DockerOutlined 还在用(给"镜像列表"页当 icon),不动。 */}
            <CairnMark size={26} />
            <span>Cairn</span>
            {/* 运行中的版本：服务端从 package.json 读，界面上不写死 */}
            {config?.version ? (
              <Tooltip title={`当前运行版本 v${config.version}`}>
                <span className="app-brand-version">v{config.version}</span>
              </Tooltip>
            ) : null}
          </div>
          <div className="app-header-meta">
            {/* v0.5.9: 经代理 Tag removed — per-credential proxy in proxy library */}
            {config && !config.allowDelete ? <Tag color="green">只读模式</Tag> : null}
            {config && !config.allowPull ? <Tag color="default">禁止拉取</Tag> : null}
            {/* v0.5.28: 右上角主显示改「展示名称」,url 退到括号里的副文本。
               v0.5.30: 之前两个独立 span 被 antd Tooltip 包成单一 inline-block,
               父容器的 gap:8px 进不去 —— 「Carin Dev 环境」和「http://...」挤一坨
               看着像少了空格。现在把 url 嵌进主 span 内,括号做天然分隔符。 */}
            {config ? (
              <Tooltip title={config.url}>
                <span className="app-registry-name ellipsis">
                  {config.name || '镜像仓库'}
                  {' ('}
                  <span className="app-registry-url mono" title={config.url}>
                    {config.url}
                  </span>
                  {')'}
                </span>
              </Tooltip>
            ) : configFailure ? (
              /* v0.5.18（F6）：顶栏也要有出口 —— 否则读配置失败时各页各报各的，
                 最上面却一直写「加载中…」，看起来像整站卡住。 */
              <Button
                type="link"
                size="small"
                loading={configLoading}
                onClick={() => void reloadConfig()}
              >
                配置读取失败，点此重试
              </Button>
            ) : (
              <span className="ellipsis mono">加载中…</span>
            )}
            {/* 图标显示的是"点了会变成什么"，所以深色下显示太阳。 */}
            <Tooltip title={mode === 'dark' ? '切换到浅色主题' : '切换到深色主题'}>
              <Button
                type="text"
                size="small"
                className="app-theme-toggle"
                aria-label={mode === 'dark' ? '切换到浅色主题' : '切换到深色主题'}
                icon={mode === 'dark' ? <SunOutlined /> : <MoonOutlined />}
                onClick={onToggleMode}
              />
            </Tooltip>
          </div>
        </header>

        <main className="app-main">
          <div className="app-nav">
            <Segmented
              options={segmentedOptions}
              value={page}
              onChange={(value) => setPage(value as PageKey)}
            />
          </div>
          <div className="app-body">
            <PageSidebar
              groups={pageGroups[page] ?? NO_GROUPS}
              selected={currentFilter}
              onSelect={handleSidebarSelect}
              version={config?.version}
            />
            <div className="app-content">
              {page === 'images' ? (
                <ImagesPage
                  config={config}
                  inventory={inventory}
                  onInventoryChange={setInventory}
                  onGoSettings={() => setPage('settings')}
                  sidebarFilter={currentFilter}
                  onPublishGroups={publishHandlers.images}
                />
              ) : page === 'pull' ? (
                <PullPage
                  config={config}
                  sidebarFilter={currentFilter}
                  onPublishGroups={publishHandlers.pull}
                />
              ) : page === 'stats' ? (
                <StatsPage
                  config={config}
                  onConfigChange={publishConfig}
                  sidebarFilter={currentFilter}
                  onPublishGroups={publishHandlers.stats}
                />
              ) : page === 'credentials' ? (
                <CredentialsPage
                  config={config}
                  sidebarFilter={currentFilter}
                  onPublishGroups={publishHandlers.credentials}
                />
              ) : page === 'proxies' ? (
                <ProxiesPage
                  config={config}
                  sidebarFilter={currentFilter}
                  onPublishGroups={publishHandlers.proxies}
                />
              ) : (
                <SettingsPage
                  config={config}
                  onConfigChange={publishConfig}
                  onInventoryChange={setInventory}
                  sidebarFilter={currentFilter}
                  onPublishGroups={publishHandlers.settings}
                />
              )}
            </div>
          </div>
        </main>
      </div>
    </AntdApp>
  );
}
