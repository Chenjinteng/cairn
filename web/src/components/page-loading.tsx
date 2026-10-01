/**
 * v0.6.21: 全站统一的"加载中"占位 —— 居中 spinner + 文案。
 *
 * v0.6.22: 改成"被外部控制 + 内部平滑淡出"。
 *
 * v0.6.23: 改成"绝对定位覆盖在内容之上",而不是放在 flow 里。
 *
 * v0.6.22 的实现 (现在 controlled) 会:
 *   - 不再让调用方用 ternary `{loading ? <PageLoading /> : <Table />}` —
 *     那样 spinner 元素会被 React 立即卸载,CSS transition 没用。
 *   - 改成受控: `<PageLoading visible={loading} />` + `<Table hidden={loading} />`
 *     让两个元素**始终共存**。当 loading 从 true → false:
 *       a. Table 的 `hidden` 解除,立刻渲染(React 同步提交,首帧就在 DOM 里)
 *       b. PageLoading 走内部状态机:再展示 minDuration(默认 250ms) →
 *          opacity 1→0 淡出 fadeDuration(默认 350ms) → 卸载
 *
 * v0.6.22 的 bug(用户反馈):
 *   - 「spinner 跟表格是同一层」—— 等等,我把 PageLoading 写在 Table 后面,
 *     在 normal flow 里 PageLoading 应该堆在 Table **下面**,不重叠。Table
 *     hidden 时 PageLoading 占位 200px 高,Table 露脸时 PageLoading 又
 *     占 200px 高度 —— 整页内容区在切换瞬间被「推」了一下,就是用户说的
 *     「拉址感」(页面被拽了一下)。
 *   - 真正想要的语义是:**spinner 是覆盖在表格上面的一层**,而不是 flow 里
 *     的一行。Table 在底层正常渲染,spinner 浮在上面,淡出时不挤占空间。
 *
 * v0.6.23 的实现:
 *   - PageLoading 改成 `position: absolute; inset: 0`,需要父容器是
 *     `position: relative`(6 个页面已经把容器包好,详见各页注释)。
 *   - 加 `background: var(--color-bg-container, #fff)`,否则透下去能看到
 *     表格数据 → spinner 像「浮在数据上」,跟设计意图不符。
 *   - 保留 v0.6.22 的状态机(minDuration + fadeDuration 淡出),
 *     现在 fade 时 spinner 在 Table **上面**淡出 → 用户看到「spinner
 *     半透明 → 表格透出来」,而不是「spinner 拉走 → 表格弹进」。
 *   - 保留 v0.6.22 的 [role=status][aria-live=polite] 让 app.css 的
 *     慢速转圈 CSS 还能命中(2s/圈)。
 */

import { Spin } from 'antd';
import { useEffect, useRef, useState } from 'react';

export interface PageLoadingProps {
  /** 调用方告知 spinner 是否需要展示。 */
  visible: boolean;
  /** 加载文案;默认「加载中…」 */
  tip?: string;
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
  minDuration = 250,
  fadeDuration = 350,
}: PageLoadingProps) {
  /**
   * 状态机:
   *   shown=true,  hiding=false  → 正常展示(opacity 1,覆盖在内容之上)
   *   shown=true,  hiding=true   → 淡出中(opacity 1→0,持续 fadeDuration ms)
   *   shown=false                → 已卸载(返回 null)
   */
  const [shown, setShown] = useState<boolean>(visible);
  const [hiding, setHiding] = useState<boolean>(false);
  const timersRef = useRef<number[]>([]);

  useEffect(() => {
    timersRef.current.forEach((t) => window.clearTimeout(t));
    timersRef.current = [];

    if (visible) {
      setShown(true);
      setHiding(false);
      return;
    }

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
      /*
       * 关键:position: absolute + inset: 0 把自身覆盖到父容器(content box)上,
       * 不参与 flow —— 这就是用户要的「覆盖在表格上面一层」的语义。
       *
       * 父容器必须 position: relative;否则会以视口为定位基准,跑到屏幕中央
       * 去(各页代码里都加了,见 *-page.tsx 的注释)。
       *
       * background 用主背景色,挡住下层 Table —— 否则 spinner 浮在数据
       * 上方,看着像「数据正在加载」,跟实际语义(数据还没回来)冲突。
       * 淡出到 opacity 0 期间会自然露出来,过渡顺。
       */
      style={{
        position: 'absolute',
        inset: 0,
        display: 'flex',
        justifyContent: 'center',
        alignItems: 'center',
        flexDirection: 'column',
        gap: 12,
        color: 'var(--color-text-tertiary, #999)',
        fontSize: 13,
        background: 'var(--color-bg-container, #fff)',
        opacity: hiding ? 0 : 1,
        transition: hiding ? `opacity ${fadeDuration}ms ease` : 'none',
        pointerEvents: hiding ? 'none' : 'auto',
        zIndex: 1,
      }}
    >
      <Spin size="large" />
      <span>{tip}</span>
    </div>
  );
}