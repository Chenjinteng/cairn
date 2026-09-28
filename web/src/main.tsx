import React, { useEffect, useState } from 'react';
import ReactDOM from 'react-dom/client';
import { ConfigProvider, theme as antdTheme } from 'antd';
import zhCN from 'antd/locale/zh_CN';

import App, { type ThemeMode } from './App';
import ErrorBoundary from './components/error-boundary';
import './theme.css';
import './app.css';

/**
 * AntD 的 token 必须跟着主题走，否则会出现"半深色"：
 * 自定义 CSS 用的 `--color-*` 变深了，而 AntD 的表格 / 弹窗 / 下拉还是浅色，
 * 同一屏里两套配色 —— 这是深色主题最常见的翻车方式。
 *
 * 深色以 AntD 的 `darkAlgorithm` 打底（它负责上百个我们没显式列出的派生色，
 * 比如禁用态、hover 态、阴影），再把语义色**对齐到 theme.css 的深色值** ——
 * 两边必须是同一组值，否则会出现两种不同的蓝。
 */
const LIGHT_TOKENS = {
  /*
   * v0.5.22: 与 theme.css 浅色版同步 —— 主色与 info 都换成 Teal 600
   * (#0D9488),跟 Cairn mark 塔身色一致。语义色 success/warning/error 保留。
   * 字符大小写保留 hex 的 raw 值(未小写),因为 ConfigProvider 的 token
   * 校验对大小写不敏感;但与 theme.css 的小写一致会让「两端不同源」这件事
   * 更显眼,review 时容易发现。
   */
  colorPrimary: '#0d9488',
  colorBgLayout: '#f2f4f7',
  colorBgContainer: '#ffffff',
  colorText: '#1e252e',
  colorTextSecondary: '#475468',
  colorBorder: '#eaecf0',
  colorSuccess: '#27c274',
  colorWarning: '#faad14',
  colorInfo: '#0d9488',
  colorError: '#f43b2c',
};

const DARK_TOKENS = {
  colorPrimary: '#2dd4bf',
  colorBgLayout: '#0f131a',
  colorBgContainer: '#171b24',
  colorText: '#e6eaf2',
  colorTextSecondary: '#aab6c8',
  colorBorder: '#2a313d',
  colorSuccess: '#3ddc84',
  colorWarning: '#ffc53d',
  colorInfo: '#2dd4bf',
  colorError: '#ff6b5e',
};

const STORAGE_KEY = 'registry-manager-theme';

const buildAntdTheme = (mode: ThemeMode) =>
  mode === 'dark'
    ? { cssVar: true, algorithm: antdTheme.darkAlgorithm, token: DARK_TOKENS }
    : { cssVar: true, token: LIGHT_TOKENS };

/**
 * 初值直接读 index.html 已经设好的属性。
 *
 * "上次的选择 > 系统偏好"那套逻辑**只在 index.html 里写一份** ——
 * 这里再判断一次就有两个真相来源，迟早会不一致（而且那是首屏闪烁的根源）。
 */
function readInitialMode(): ThemeMode {
  return document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light';
}

function Root() {
  const [mode, setMode] = useState<ThemeMode>(readInitialMode);

  useEffect(() => {
    document.documentElement.dataset.theme = mode;
    try {
      localStorage.setItem(STORAGE_KEY, mode);
    } catch {
      // 存储不可用（隐私模式）只是记不住选择，不影响本次会话。
    }
  }, [mode]);

  return (
    <ConfigProvider theme={buildAntdTheme(mode)} locale={zhCN}>
      <ErrorBoundary>
        <App
          mode={mode}
          onToggleMode={() => setMode((current) => (current === 'dark' ? 'light' : 'dark'))}
        />
      </ErrorBoundary>
    </ConfigProvider>
  );
}

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <React.StrictMode>
    <Root />
  </React.StrictMode>
);
