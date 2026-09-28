/**
 * v0.5.21：Cairn 品牌标识 —— 3 块圆角矩形堆叠成塔身 + 顶部琥珀色标记点。
 *
 * 三块石头分别代表 cairn 三个能力面（存储 / 拉取 / UI），琥珀点是
 * 「目标服务器的位置标记」—— 也是 cairn 的核心隐喻。
 *
 * 提供 3 个尺寸档位 + dark 变体。复用 SVG,不依赖任何外部资源,
 * favicon / app nav / hero 都可以直接用,色彩变量取自 antd 的 CSS
 * 变量以保持主题一致性（虽然本组件目前只用硬编码色,因为 mark
 * 是品牌资产,主题切换不应当改变品牌色）。
 */

export type CairnMarkVariant = 'light' | 'dark';

export interface CairnMarkProps {
  /** 像素尺寸。16 (favicon) / 24 / 32 (nav) / 64 (hero) / 128 (splash)。 */
  size?: number;
  /**
   * 色彩变体：
   * - `light`（默认）：深 teal 塔身 + amber 点,用于浅色底
   * - `dark`：teal-300 塔身 + amber-400 点,用于深色底
   */
  variant?: CairnMarkVariant;
  /** 额外 className,用于布局微调。 */
  className?: string;
  /** 给无障碍读屏器读的字。默认"Cairn"。 */
  title?: string;
}

const COLORS = {
  light: {
    body: '#0d9488', // teal-600
    dot: '#f59e0b',  // amber-500
  },
  dark: {
    body: '#5eead4', // teal-300
    dot: '#fbbf24',  // amber-400
  },
} as const;

export default function CairnMark({
  size = 32,
  variant = 'light',
  className,
  title = 'Cairn',
}: CairnMarkProps) {
  const c = COLORS[variant];
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      xmlns="http://www.w3.org/2000/svg"
      className={className}
      role="img"
      aria-label={title}
    >
      <title>{title}</title>
      {/* 三块堆叠 + 标记点。viewBox 64x64,几何关系见 cairn-brand.html 的 Construction 段 */}
      <rect x="6" y="46" width="52" height="12" rx="3" fill={c.body} />
      <rect x="14" y="32" width="36" height="10" rx="2.5" fill={c.body} />
      <rect x="22" y="20" width="20" height="9" rx="2" fill={c.body} />
      <circle cx="32" cy="10" r="3.5" fill={c.dot} />
    </svg>
  );
}
