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
        fontSize: 13,
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
      <Spin size="large" />
      <span>{tip}</span>
    </div>
  );
}