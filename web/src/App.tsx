import { useEffect, useState, type ReactNode } from 'react';
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
import PageSidebar from './components/page-sidebar';
import type { Inventory } from './types';

type PageKey = 'images' | 'stats' | 'pull' | 'credentials' | 'proxies' | 'settings';

/** 界面主题。持久化与首屏应用都在 main.tsx / index.html 里，这里只负责展示与切换。 */
export type ThemeMode = 'light' | 'dark';

/**
 * v0.5.37：整体布局
 *   header（顶栏 56px；brand + 顶部 Segmented 6 Tab 导航 + user）→
 *   main（p-4）→ body（grid: 220px 侧栏 + 1fr 内容区）→ 页面内容
 *
 * 顶栏 Segmented 6 Tab 仍是顶级导航，不动；
 * 220px 侧栏是**每页内部**的 sub-nav（仓库筛选 / 凭据分组 / 时间窗 / 协议分类 等），
 * 由 `PageSidebar` 组件按 page 类型渲染对应 group + item（仅 UI 占位 + active 高亮，
 * v0.5.37 范围内不联动内容过滤，留给后续 0.5.x 优化）。
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
            <PageSidebar page={page} />
            <div className="app-content">
              {page === 'images' ? (
                <ImagesPage
                  config={config}
                  inventory={inventory}
                  onInventoryChange={setInventory}
                  onGoSettings={() => setPage('settings')}
                />
              ) : page === 'pull' ? (
                <PullPage config={config} />
              ) : page === 'stats' ? (
                <StatsPage config={config} onConfigChange={publishConfig} />
              ) : page === 'credentials' ? (
                <CredentialsPage config={config} />
              ) : page === 'proxies' ? (
                <ProxiesPage config={config} />
              ) : (
                <SettingsPage
                  config={config}
                  onConfigChange={publishConfig}
                  onInventoryChange={setInventory}
                />
              )}
            </div>
          </div>
        </main>
      </div>
    </AntdApp>
  );
}
