/**
 * v0.6.21: 全站统一的"加载中"占位 —— 居中 spinner + 文案。
 *
 * v0.6.22: 改成"被外部控制 + 内部平滑淡出"。
 *
 * v0.6.23: 改成"绝对定位覆盖在内容之上",而不是放在 flow 里。
 *
 * v0.6.24: 修两个 v0.6.23 现场 bug(深色模式 UAT 实测):
 *   1. **spinner 背景在深色模式下仍是纯白** —— 原背景用
 *      `var(--color-bg-container, #fff)`,但 theme.css 里没定义
 *      `--color-bg-container`(只有 `--color-bg`,深浅都有)。
 *      CSS 变量 fallback 命中 `#fff`,深色主题下 spinner 蒙板变成
 *      纯白色,跟深色面板「撞色」很突兀。改成 `var(--color-bg)` ——
 *      深色 `#171b24`,浅色 `#ffffff`,跟 `.panel { background: var(--color-bg) }`
 *      完全对齐,蒙板就跟面板融为一体。
 *   2. **spinner 没有完全覆盖 antd Table** —— 0.6.23 用 `inset: 0` 在
 *      `position: absolute` 下填充,实测 proxies-page 的「操作」列
 *      (`fixed: 'right'`,antd v5 内部用 sticky + 内部 z-index)会漏到
 *      蒙板外面。原因 antd Table 内部的 fixed-column 容器有自己的
 *      z-index 栈,z-index: 1 压不过。0.6.24 改成:
 *      - 显式 `width: 100%; height: 100%` 兜底 `inset: 0`(防万一)
 *      - z-index 提到 100,确保盖过 antd Table 内部层级
 *      - left: 0; top: 0 显式写出,跟 inset: 0 等价但更显式
 *
 * 用户期望:蒙板是**完全覆盖**在 panel 上的不透明背景层,跟 panel
 * 背景色一致,中间是 spinner + tip。fade 时是 opacity 1→0 的整体
 * 透明(不是「缩小消失」)。
 *
 * v0.6.28: 加 `delay` prop —— visible=true 后等 `delay` 毫秒才真的渲染蒙板。
 * delay 窗口内 visible 又变回 false(数据来得比 delay 快),延迟定时器被
 * 取消,蒙板根本不弹。这是 NProgress / React Query 的标准做法:
 * 快接口(<200ms)不闪 spinner,慢接口(>200ms)才弹,真卡住的页面永弹。
 * 主要解决 v0.6.27 prefetch 仍未生效的边界场景(用户比 prefetch 完成得
 * 还快 → initialTasks=null → loading=true → PageLoading 闪)。
 *
 * v0.7.50: 默认参数保持 v0.6.28 的「避免闪屏」语义 —— delay=200ms +
 * minDuration=250ms。v0.7.49 短暂改为 delay=0 + minDuration=700ms
 * 让 cairn-mark 入场动画跑完,但代价是每次切页面强制等 700ms 才
 * fade out,本地快接口感受到 ~300-500ms 切换延迟。UAT 反馈:
 * 只要「镜像列表」页面(用户进入控制台看到的第一个页面)看到动画
 * 就好,其它页面保持默认避免闪屏。
 *
 * 所以:**默认仍是 v0.6.28 行为**;**images-page.tsx 显式传**
 * `delay={0} minDuration={700}`,让首屏镜像列表加载有 cairn-mark
 * 入场动画。其它页面的 PageLoading 维持「避免闪屏」语义。
 *
 * 主要解决 v0.6.27 prefetch 仍未生效的边界场景(用户比 prefetch 完成得
 * 还快 → initialTasks=null → loading=true → PageLoading 闪)。
 */

import { useEffect, useRef, useState } from 'react';

import CairnMark from './cairn-mark';

export interface PageLoadingProps {
  /** 调用方告知 spinner 是否需要展示。 */
  visible: boolean;
  /** 加载文案;默认「加载中…」 */
  tip?: string;
  /**
   * 延迟显示时长(毫秒)。`visible` 从 false 变 true 后,等这么久才真正
   * 渲染蒙板;delay 窗口内若 visible 又变回 false,延迟定时器被取消,
   * 蒙板根本不弹出来 —— 避免「闪一下」。默认 200ms。设 0 = 旧行为
   * (visible=true 立刻弹,跟 v0.6.27 一样)。
   */
  delay?: number;
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
  delay = 200,
  minDuration = 250,
  fadeDuration = 350,
}: PageLoadingProps) {
  /**
   * 状态机:
   *   shown=false                       → 不渲染(返回 null)
   *   shown=true,  hiding=false         → 正常展示(opacity 1,覆盖在内容之上)
   *   shown=true,  hiding=true          → 淡出中(opacity 1→0,持续 fadeDuration ms)
   *
   * v0.6.28: shown 初值由 `useState<boolean>(visible)` 改成 `false`,因为加了
   * delay —— 哪怕 visible 一上来就是 true,也要等 delay 才渲染,否则跟没加
   * delay 没区别。
   *
   * shownRef 用来在闭包内追踪当前是否已「真出现」(v0.6.28):visible=true →
   * 进入 effect 时如果已经 shown,就不要再走一次「等 delay 再起」的逻辑(避免
   * visible=true→false→true 短时间内反复切时把已经在展示的 spinner 又藏一次)。
   */
  const [shown, setShown] = useState<boolean>(false);
  const [hiding, setHiding] = useState<boolean>(false);
  const shownRef = useRef<boolean>(false);
  /**
   * v0.6.28: 单独的 delay 定时器 ref —— 不放进 timersRef,因为它在 visible=false
   * 切换时(数据回来太快)需要单独取消来取消,防止「延迟完了之后才弹」。分离之后
   * 语义更清晰。
   */
  const delayTimerRef = useRef<number | null>(null);
  const timersRef = useRef<number[]>([]);

  useEffect(() => {
    /*
     * 进入 effect 第一件事:清掉所有挂起的定时器。
     * - delayTimerRef:上一次 visible=true 触发的「等 delay 才弹」定时器。
     *   这次 visible 翻成 false 就取消它,避免「延迟窗口内刚弹出来」。
     * - timersRef:minDuration + fadeDuration 的隐藏链定时器。
     *   同理,visible 再翻回 true 时取消隐藏链,避免「蒙板已经要消失了又被拽回来」。
     */
    if (delayTimerRef.current !== null) {
      window.clearTimeout(delayTimerRef.current);
      delayTimerRef.current = null;
    }
    timersRef.current.forEach((t) => window.clearTimeout(t));
    timersRef.current = [];

    if (visible) {
      setHiding(false);
      if (shownRef.current) {
        // 已经在展示中(visible=true 期间 deps 变了,例如 delay 调整),什么都不做。
        return;
      }
      if (delay > 0) {
        delayTimerRef.current = window.setTimeout(() => {
          delayTimerRef.current = null;
          shownRef.current = true;
          setShown(true);
        }, delay);
      } else {
        shownRef.current = true;
        setShown(true);
      }
      return;
    }

    // visible=false:决定要不要走「已经在展示 → 走 minDuration 淡出」流程。
    // delay 窗口内 visible 又翻 false(数据 < delay ms 就回):shownRef 仍是 false,
    // 蒙板根本没渲染,直接跳过整个 hide 链。
    if (shownRef.current) {
      shownRef.current = false;
      const minTimer = window.setTimeout(() => {
        setHiding(true);
        const fadeTimer = window.setTimeout(() => {
          setShown(false);
        }, fadeDuration);
        timersRef.current.push(fadeTimer);
      }, minDuration);
      timersRef.current.push(minTimer);
    }
  }, [visible, delay, minDuration, fadeDuration]);

  /*
   * 卸载时清掉所有挂起定时器。effect 主体的 cleanup 函数只在 deps 变化时
   * 跑,卸载时跑不到(那个分支 visible=false 时才返回 cleanup,而 visible=true
   时没返回),所以这里单独跑一个 mount → return cleanup 的 effect 兜底。
   */
  useEffect(() => {
    return () => {
      if (delayTimerRef.current !== null) {
        window.clearTimeout(delayTimerRef.current);
        delayTimerRef.current = null;
      }
      timersRef.current.forEach((t) => window.clearTimeout(t));
      timersRef.current = [];
    };
  }, []);

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
        /*
         * inset: 0 + 显式 left/top/width/height 兜底 —— 0.6.23 实测在
         * antd Table 的 fixed-column(操作列 fixed:'right')场景下蒙板
         * 没盖住右侧 fixed 区域。显式写出来更稳,跟 inset: 0 等价。
         */
        top: 0,
        left: 0,
        right: 0,
        bottom: 0,
        width: '100%',
        height: '100%',
        display: 'flex',
        justifyContent: 'center',
        alignItems: 'center',
        flexDirection: 'column',
        gap: 12,
        /*
         * 背景跟 panel 一致(.panel { background: var(--color-bg) }),
         * 深浅主题都不会撞色;`--color-bg` 在 theme.css 深色段定义为
         * `#171b24`,浅色 `#ffffff`,都跟 .panel 完全一致。
         */
        background: 'var(--color-bg)',
        color: 'var(--color-text-tertiary, #999)',
        /*
         * v0.7.47: 字号 13 → 12 —— 跟 cairn-mark size=64 的视觉重量平衡;
         * 原 13 跟 antd Spin size="large" 配套,现在 cairn-mark 视觉占位更
         * 收敛(纯色块 vs Spin 有圆环动效),字号一并收一下。
         */
        fontSize: 12,
        opacity: hiding ? 0 : 1,
        transition: hiding ? `opacity ${fadeDuration}ms ease` : 'none',
        pointerEvents: hiding ? 'none' : 'auto',
        /*
         * z-index: 100 压过 antd Table 内部层级 —— antd v5 的 fixed-column
         * 容器自己有 z-index(实际值约 2-10),原 0.6.23 的 1 不够。
         */
        zIndex: 100,
      }}
    >
      {/* v0.7.47: antd Spin → cairn-mark 石塔建造动画。详见 cairn-mark.tsx
          顶部注释和 web/src/app.css 的 .cairn-mark-anim-build。*/}
      <CairnMark size={64} animation="build" />
      <span>{tip}</span>
    </div>
  );
}