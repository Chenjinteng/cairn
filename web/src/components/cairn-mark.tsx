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
 *
 * v0.7.47: 加 `animation` prop —— `'none'`（默认,保持现有静态行为）
 *  | `'build'`（石塔建造:从下往上逐层堆,琥珀点最后落入,一次性,无持续动画;
 * 适合加载蒙板场景——蒙板消失 cairn 跟着消失,没必要持续呼吸）
 *  | `'pulse-build'`（'build' + 琥珀点持续呼吸,适合 splash / 长期停留的页头）
 * `prefers-reduced-motion: reduce` 下任何 animation 都降级为 none。
 */

export type CairnMarkVariant = 'light' | 'dark';
export type CairnMarkAnimation = 'none' | 'build' | 'pulse-build';

export interface CairnMarkProps {
  /** 像素尺寸。16 (favicon) / 24 / 32 (nav) / 64 (hero) / 128 (splash)。 */
  size?: number;
  /**
   * 色彩变体：
   * - `light`（默认）：深 teal 塔身 + amber 点,用于浅色底
   * - `dark`：teal-300 塔身 + amber-400 点,用于深色底
   */
  variant?: CairnMarkVariant;
  /**
   * v0.7.47: 动画方案。`'none'`(默认) = 静态;`'build'` = 一次性石塔建造;
   * `'pulse-build'` = 建造 + 琥珀点持续呼吸。详见 CSS 的
   * `.cairn-mark-anim-build` / `.cairn-mark-anim-pulse-build` 类。
   */
  animation?: CairnMarkAnimation;
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
  animation = 'none',
  className,
  title = 'Cairn',
}: CairnMarkProps) {
  const c = COLORS[variant];
  // animation 不是 'none' 时给 svg 加 className 触发 CSS 动画。
  // 类名跟 CSS 里的 .cairn-mark-anim-build / .cairn-mark-anim-pulse-build 一一对应。
  const animClass = animation === 'none' ? '' : `cairn-mark-anim-${animation}`;
  const composedClass = [animClass, className].filter(Boolean).join(' ') || undefined;
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 64 64"
      xmlns="http://www.w3.org/2000/svg"
      className={composedClass}
      role="img"
      aria-label={title}
    >
      <title>{title}</title>
      {/* 三块堆叠 + 标记点。viewBox 64x64,几何关系见 cairn-brand.html 的 Construction 段 */}
      <rect className="stone stone-1" x="6" y="46" width="52" height="12" rx="3" fill={c.body} />
      <rect className="stone stone-2" x="14" y="32" width="36" height="10" rx="2.5" fill={c.body} />
      <rect className="stone stone-3" x="22" y="20" width="20" height="9" rx="2" fill={c.body} />
      <circle className="dot" cx="32" cy="10" r="3.5" fill={c.dot} />
    </svg>
  );
}
