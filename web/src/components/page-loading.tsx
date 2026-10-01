/**
 * v0.6.21: 全站统一的"加载中"占位 —— 居中 spinner + 文案。
 *
 * v0.6.22: 改成"被外部控制 + 内部平滑淡出"。
 *
 * 用户反馈 v0.6.21 的两个问题:
 *   1. 转圈太快,看着「一闪一闪的」—— 加慢 animation-duration 到 2s(默认 1s)。
 *   2. 「数据回来 → spinner 立刻消失 → 真表格出现」太突兀,希望「等页面元素
 *      都渲染好后再淡出 spinner」。
 *
 * 第 2 条的实现思路:
 *   - 不再让调用方用 ternary `{loading ? <PageLoading /> : <Table />}` —
 *     那样 spinner 元素会被 React 立即卸载,CSS transition 没用。
 *   - 改成受控: `<PageLoading visible={loading} />` + `<Table hidden={loading} />`
 *     让两个元素**始终共存**。当 loading 从 true → false:
 *       a. Table 的 `hidden` 解除,立刻渲染(React 同步提交,首帧就在 DOM 里)
 *       b. PageLoading 走内部状态机:再展示 minDuration(默认 250ms) →
 *          opacity 1→0 淡出 fadeDuration(默认 350ms) → 卸载
 *     用户看到的就是:「spinner → spinner 半透明(底下能瞄到表格) → 表格」
 *     三段渐变,而不是硬切。
 *
 *   - 为什么要 minDuration?  答: 接口 < 200ms 回来时,「spinner 闪一下消失」
 *     比「持续展示 250ms+ 再淡出」更刺眼。250ms 是参考值,跟 antd Skeleton
 *     默认 200ms 接近,但留 50ms 余量。
 *
 * 设计要点:
 *   - 颜色自动跟 ConfigProvider 的 colorPrimary 对齐(默认 teal)
 *   - 居中布局,默认 200px 高 —— 调用方可以传 height
 *   - 不再要 title / description / rows / columns 等骨架参数 —— 真不假装表格了
 *   - 销毁时机严格靠 fadeDuration,不会提前(避免淡出过程中又被 visible=true 触发闪)
 */

import { Spin } from 'antd';
import { useEffect, useRef, useState } from 'react';

export interface PageLoadingProps {
  /** 调用方告知 spinner 是否需要展示。配合 `<Table hidden={!visible} />` 使用。 */
  visible: boolean;
  /** 加载文案;默认「加载中…」 */
  tip?: string;
  /** 占位高度(像素);默认 200。页面内容区更高时设大点,免得 spinner 缩在角落。 */
  height?: number;
  /**
   * 最短展示时间(毫秒)。`visible` 从 true 变 false 时,至少再展示这么久才开始淡出。
   * 默认 250ms。设 0 = 接口一回来立刻开始淡出。
   */
  minDuration?: number;
  /** 淡出动画时长(毫秒);默认 350ms。 */
  fadeDuration?: number;
}

export default function PageLoading({
  visible,
  tip = '加载中…',
  height = 200,
  minDuration = 250,
  fadeDuration = 350,
}: PageLoadingProps) {
  /**
   * 状态机:
   *   shown=true,  hiding=false  → 正常展示(opacity 1)
   *   shown=true,  hiding=true   → 淡出中(opacity 1→0,持续 fadeDuration ms)
   *   shown=false                → 已卸载(返回 null)
   *
   * 转移:
   *   visible=false → 等 minDuration → hiding=true → 等 fadeDuration → shown=false
   *   visible=true  → 取消所有定时器,shown=true, hiding=false
   */
  const [shown, setShown] = useState<boolean>(visible);
  const [hiding, setHiding] = useState<boolean>(false);
  const timersRef = useRef<number[]>([]);

  useEffect(() => {
    // 先清掉上一次的(visible 反复翻转时避免旧定时器把 shown 提前关掉)
    timersRef.current.forEach((t) => window.clearTimeout(t));
    timersRef.current = [];

    if (visible) {
      // 调用方要求展示 → 立刻回到「正常展示」状态
      setShown(true);
      setHiding(false);
      return;
    }

    // visible === false: 延迟 minDuration → 开始淡出 → 再 fadeDuration 后卸载
    const minTimer = window.setTimeout(() => {
      setHiding(true);
      const fadeTimer = window.setTimeout(() => {
        setShown(false);
      }, fadeDuration);
      timersRef.current.push(fadeTimer);
    }, minDuration);
    timersRef.current.push(minTimer);

    return () => {
      timersRef.current.forEach((t) => window.clearTimeout(t));
      timersRef.current = [];
    };
  }, [visible, minDuration, fadeDuration]);

  if (!shown) return null;

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
        opacity: hiding ? 0 : 1,
        transition: hiding ? `opacity ${fadeDuration}ms ease` : 'none',
        // 淡出期间不再阻挡下面的 Table,让用户提前看到真表格「露脸」过渡
        pointerEvents: hiding ? 'none' : 'auto',
      }}
    >
      <Spin size="large" />
      <span>{tip}</span>
    </div>
  );
}