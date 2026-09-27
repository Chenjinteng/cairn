import { useMemo, useSyncExternalStore } from 'react';

import { fetchConfig } from './api';
import type { ApiFailureInfo, AppConfig } from './types';

/**
 * v0.5.18（F6）：服务配置（GET /api/config）的**单一入口**。
 *
 * 背景：这个接口原来是 5 个页面各自拉一遍 —— 每次切换页面都重打一次，每页
 * 还各存一份失败态，谁也看不到别人已经拿到了。配置在一次运行里基本不变，
 * 所以这里收成一个模块级 store：
 *
 *   - 缓存：拿到结果（成功或失败）就不再自动重复请求；
 *   - 单飞：并发调用只有一个请求在飞（StrictMode 下 effect 双跑也只发一次）；
 *   - 不自动重试：失败后停下等用户点重试。重试会重放一个非幂等接口，这个
 *     判断留给用户，本文件不做静默重试（与 components/load-error.tsx 一致）。
 *
 * 代价只有 React 本身，不引入任何 store 库。
 */
export interface AppConfigState {
  config: AppConfig | null;
  /** 最近一次读取失败的原因；成功或尚未读过时为 null。 */
  failure: ApiFailureInfo | null;
  /** 是否有一轮读取在途。只为按钮 loading 服务，不用来判断「有没有配置」。 */
  loading: boolean;
}

const INITIAL: AppConfigState = { config: null, failure: null, loading: false };

let state: AppConfigState = INITIAL;
const listeners = new Set<() => void>();
let inflight: Promise<void> | null = null;

function setState(next: AppConfigState): void {
  state = next;
  for (const listener of listeners) {
    listener();
  }
}

/**
 * useSyncExternalStore 用 Object.is 比较快照，所以这里**必须**返回稳定引用：
 * 每次新建对象都会被判定为"变了"，然后无限重渲染。
 */
function configSnapshot(): AppConfigState {
  return state;
}

function subscribeConfig(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

async function load(): Promise<void> {
  // 这一句在第一个 await 之前同步执行，所以 StrictMode 的第二次调用一定能看到
  // 上面挂好的 inflight —— 单飞靠的是这个顺序，不是某个微任务的时间差。
  setState({ ...state, loading: true });
  const result = await fetchConfig();
  if (result.success && result.data) {
    setState({ config: result.data, failure: null, loading: false });
    return;
  }
  // 失败**不清空**已有 config：拿到过就继续用旧的（可能只是某次重试失败），
  // 只把失败原因记下来供页面提示。下一次成功的读取会把 failure 抹掉。
  setState({ ...state, failure: result, loading: false });
}

function startLoad(): Promise<void> {
  if (inflight) {
    return inflight;
  }
  inflight = load().finally(() => {
    inflight = null;
  });
  return inflight;
}

/** 首屏装配：已有结果（成功或失败）就什么都不做。失败后不自动重试。 */
export function ensureConfigLoaded(): Promise<void> {
  if (state.config || state.failure) {
    return Promise.resolve();
  }
  return startLoad();
}

/** 用户点「重试」时调用：不管缓存，强制再读一次。 */
export function reloadConfig(): Promise<void> {
  return startLoad();
}

/**
 * PATCH /api/config 成功之后把新配置写回缓存。
 *
 * 不重新 GET：PATCH 的响应就是最新的完整配置，再打一次只会多一次往返，还可能
 * 因为竞态把旧值覆盖回来。
 */
export function publishConfig(config: AppConfig): void {
  setState({ config, failure: null, loading: false });
}

/** 页面侧的唯一读取入口。 */
export function useAppConfig(): AppConfigState & { reload: () => Promise<void> } {
  const snapshot = useSyncExternalStore(subscribeConfig, configSnapshot, configSnapshot);
  // reloadConfig 是模块级常量，deps 只需盯 snapshot 的身份变化。
  return useMemo(() => ({ ...snapshot, reload: reloadConfig }), [snapshot]);
}
