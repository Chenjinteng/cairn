import type {
  ApiResult,
  AppConfig,
  Credential,
  CredentialInput,
  CredentialPatch,
  DeleteManifestPayload,
  DeleteRepositoryPayload,
  DeleteTagPayload,
  DestStatus,
  GCResult,
  GCOption,
  HeatPurgeResult,
  IgnoreRules,
  Inventory,
  ProxyEntry,
  ProxyInput,
  ProxyPatch,
  ProxyProbeAllResult,
  ProxyTestInput,
  ProxyTestResult,
  PullJob,
  PullJobInput,
  StatsClients,
  StatsEvents,
  StatsRepositories,
  StatsSeries,
  StatsSummary,
  StatsTop,
  StatsTopBy,
} from './types';

/**
 * 所有接口都返回 `{ success, code, message, data }`。
 * 失败不抛异常而是返回结构体：错误原因是页面要内联展示的领域事实
 * （例如 registry 未开启删除），需要连同 code 一起渲染。
 *
 * v0.5.18（F1）：每个请求都带 AbortController + 超时预算。
 *
 * 在这之前 fetch 是不设防的——后端进程 hang 住时（SIGSTOP 停住、连接被
 * 中间设备黑洞、上游 registry 卡住不返回）请求永远不 settle，页面就永久
 * 停在 loading：既没有错误提示，也没有重试入口。超时后返回**可区分**的
 * TIMEOUT code，让「服务在但没回应」和「连不上服务」（NETWORK_ERROR）
 * 能分别呈现，而不是都塌进同一个网络错误。
 *
 * v0.5.18（F2）：失败结果再带上 `status` 与 `detail`（见类型的 ApiFailureInfo），
 * 让调用方能区分「服务没回 / 回话的不是 cairn / cairn 明确拒绝」，从而
 * 决定是给重试入口还是把领域原因念给用户听。
 */

/** 本地 SQLite / 内存读写的快接口预算。 */
const DEFAULT_TIMEOUT_MS = 10_000;

/**
 * 出网探测、全量扫描、删除类操作的预算。
 *
 * 这一档也不设成「无限」：管理界面宁可超时后给用户一个明确的重试入口，
 * 也不要一个永不结束的转圈。服务端 handler 用的是 r.Context()，前端
 * abort 会连带取消服务端正在跑的扫描，不会留下无人认领的后台工作。
 */
const SLOW_TIMEOUT_MS = 120_000;

interface RequestOptions {
  /** 覆盖该次请求的超时预算（毫秒），默认 DEFAULT_TIMEOUT_MS。 */
  timeoutMs?: number;
}

/**
 * 非预期正文的摘要上限。
 *
 * 200 字符足够回答「这是谁回的」——网关错误页的标题行、反代版本行、HTML
 * 骨架都在前 200 字符里 —— 又不会把整页 HTML 塞进界面。
 */
const DETAIL_MAX_CHARS = 200;

/** 折叠空白 + 截断，把任意正文变成一行能放进 Alert 的摘要。 */
function summarizeBody(body: string): string {
  const collapsed = body.replace(/\s+/g, ' ').trim();
  return collapsed.length <= DETAIL_MAX_CHARS
    ? collapsed
    : `${collapsed.slice(0, DETAIL_MAX_CHARS)}\u2026`;
}

/** 把 unknown 形式的异常收敛成一句人话。 */
function describeError(error: unknown): string {
  return (error as Error)?.message ?? String(error);
}

/**
 * 超时结果集中一处：header 之前、header 之后、body 读取中三个位置都会超时，
 * 文案必须一致，否则用户看到的「10 秒」「120 秒」会随分支漂移。
 */
function timeoutResult<T>(timeoutMs: number, path: string, status: number): ApiResult<T> {
  return {
    success: false,
    code: 'TIMEOUT',
    message: `请求超时（${Math.round(timeoutMs / 1000)} 秒）已中止: ${path}`,
    status,
  };
}

async function request<T>(
  path: string,
  init?: RequestInit,
  options?: RequestOptions
): Promise<ApiResult<T>> {
  const timeoutMs = options?.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  const controller = new AbortController();
  // 计时器挂到响应体读完为止：响应头回来了但 body 卡住（大清单 / 半截响应）
  // 同样属于 hang，abort 会一并中断 body 读取。
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetch(path, {
      headers: { 'Content-Type': 'application/json' },
      ...init,
      // 必须排在 ...init 之后：写在前面会被调用方传入的同名字段覆盖。
      signal: controller.signal,
    });
    // v0.5.18（F2）：先按文本读、再自己解析，好把三类响应分开。
    //
    // 过去直接 `await response.json()`：网关的 HTML 错误页、反代的 502 页、
    // 半截 JSON 全都塌进同一个 NETWORK_ERROR（文案还是"无法连接管理服务"），
    // 把「服务在、但回话的不是 cairn」误报成「连不上」。现在：
    //   - 正文读不到        → NETWORK_ERROR（确实没拿到完整响应，含被掐断）
    //   - 读到了但不是 JSON → INVALID_RESPONSE + HTTP status + 正文摘要
    //   - JSON 但不是信封   → INVALID_RESPONSE + HTTP status + 正文摘要
    //   - 信封 success:false → 原样透传，另附 HTTP status
    let body: string;
    try {
      body = await response.text();
    } catch (error) {
      if (controller.signal.aborted) {
        return timeoutResult<T>(timeoutMs, path, response.status);
      }
      return {
        success: false,
        code: 'NETWORK_ERROR',
        message: `读取服务响应失败: ${describeError(error)}`,
        status: response.status,
      };
    }
    // body 读完了，但读的过程中已经超时：语义仍是超时，不能当成正常响应。
    if (controller.signal.aborted) {
      return timeoutResult<T>(timeoutMs, path, response.status);
    }
    let payload: ApiResult<T>;
    try {
      payload = JSON.parse(body) as ApiResult<T>;
    } catch {
      return {
        success: false,
        code: 'INVALID_RESPONSE',
        message: `服务返回了非 JSON 响应（HTTP ${response.status}）: ${path}`,
        status: response.status,
        detail: summarizeBody(body),
      };
    }
    if (typeof payload?.success !== 'boolean') {
      return {
        success: false,
        code: 'INVALID_RESPONSE',
        message: `服务返回了非预期响应（HTTP ${response.status}）: ${path}`,
        status: response.status,
        detail: summarizeBody(body),
      };
    }
    if (!payload.success) {
      // cairn 明确拒绝：领域 code / message 是页面要展示的事实，原样保留，
      // 只补一个 HTTP status 方便跟访问日志对账。
      return { ...payload, status: response.status };
    }
    return payload;
  } catch (error) {
    if (controller.signal.aborted) {
      return timeoutResult<T>(timeoutMs, path, 0);
    }
    return {
      success: false,
      code: 'NETWORK_ERROR',
      message: `无法连接管理服务: ${describeError(error)}`,
      status: 0,
    };
  } finally {
    clearTimeout(timer);
  }
}

/**
 * 走 SLOW_TIMEOUT_MS 预算的请求：出网探测 / 全量扫描 / 删除类操作。
 * 只读写本地 SQLite 与内存的接口直接用 request()。
 */
function requestSlow<T>(path: string, init?: RequestInit): Promise<ApiResult<T>> {
  return request<T>(path, init, { timeoutMs: SLOW_TIMEOUT_MS });
}

export const fetchConfig = () => request<AppConfig>('/api/config');

/**
 * v0.5.2: persist runtime-editable settings. The server-side UpdateConfig
 * accepts a map of key->stringValue pairs and validates each key against
 * config.MutableKeys (env-secret / restart-required fields are rejected).
 * Pass an empty string to clear an override (falls back to env).
 */
export type MutablePatch = Record<string, string | undefined>;

export interface ConfigPatch {
  mutable?: MutablePatch;
}

export const updateConfig = (patch: ConfigPatch) =>
  request<AppConfig>('/api/config', { method: 'PATCH', body: JSON.stringify(patch) });

export const fetchInventory = () => requestSlow<Inventory>('/api/inventory');

export const refreshInventory = () =>
  requestSlow<Inventory>('/api/refresh', { method: 'POST' });

export const probeRegistry = () =>
  requestSlow<{ apiVersion: string; host: string }>('/api/probe', { method: 'POST' });

export const deleteTag = (repository: string, tag: string) =>
  requestSlow<DeleteTagPayload>(
    `/api/tags?repository=${encodeURIComponent(repository)}&tag=${encodeURIComponent(tag)}`,
    { method: 'DELETE' }
  );

// v0.5.0: 删除整个仓库（不可逆；UI 必须二次确认）。
export const deleteRepository = (repo: string) =>
  requestSlow<DeleteRepositoryPayload>(
    `/api/repositories/${encodeURIComponent(repo)}`,
    { method: 'DELETE' }
  );

// v0.5.0: 按 digest 删除 manifest，返回受影响 tag 列表。
export const deleteManifestByDigest = (repo: string, digest: string) =>
  requestSlow<DeleteManifestPayload>(
    `/api/repositories/${encodeURIComponent(repo)}/manifests/${encodeURIComponent(digest)}`,
    { method: 'DELETE' }
  );

// v0.5.0: 触发一次存储 GC 扫描，返回本次回收的 blob 数与字节数。
// v0.5.20: opts.cleanEmptyRepos=true 时，额外清理 0 tag 且无 24h 内
// 上传会话的仓库（默认 false 保持 v0.5.18 行为）。
export const runGC = (opts: GCOption = {}) =>
  requestSlow<GCResult>('/api/gc', {
    method: 'POST',
    body: JSON.stringify(opts),
  });

export const createPullJob = (input: PullJobInput) =>
  request<PullJob>('/api/pull/jobs', {
    method: 'POST',
    body: JSON.stringify(input),
  });

export const listPullJobs = () => request<PullJob[]>('/api/pull/jobs');

export const getPullJob = (id: string) => request<PullJob>(`/api/pull/jobs/${encodeURIComponent(id)}`);

export const cancelPullJob = (id: string) =>
  request<PullJob>(`/api/pull/jobs/${encodeURIComponent(id)}/cancel`, { method: 'POST' });

export const removePullJob = (id: string) =>
  request<{ id: string }>(`/api/pull/jobs/${encodeURIComponent(id)}`, { method: 'DELETE' });

export const probePullSource = (input: {
  sourceUrl: string;
  sourceProxy?: string;
  proxyId?: string;
  credentialId?: string;
  /** 传了源引用，服务端就会顺带探测目标 tag 现状（是否已存在 / 会不会被覆盖）。 */
  sourceRef?: string;
  destRepo?: string;
  destTag?: string;
}) =>
  requestSlow<{
    /**
     * 服务端预检的真结论。false = 别入队，这一单必然拉不下来：源 registry
     * 不可达，或源 registry 可达但没有这个源镜像 / 源镜像探测失败。
     * 必须与信封上的 success 区分——success 只代表这次 HTTP 调用成功。
     */
    ok: boolean;
    /** ok=false 的原因，可直接展示给用户。 */
    error?: string;
    apiVersion: string;
    host: string;
    sourceUrl: string;
    usingProxy: boolean;
    /** 该源使用 Bearer 令牌认证（公开镜像也会匿名取 token，属于正常情况）。 */
    authRequired?: boolean;
    /** 令牌服务地址，便于排查。 */
    tokenRealm?: string;
    /** 令牌申请失败的原因（此时仍算"可达"，只是拿不到 token）。 */
    tokenError?: string;
    /**
     * 源镜像的真实路径与结论。服务端按拉取任务同一套限定规则解析
     * （Docker Hub 上裸 nginx → library/nginx），所以传了 sourceRef 时
     * 这里就是任务实际会去拉的引用。
     */
    sourceRepo?: string;
    sourceTag?: string;
    sourceExists?: boolean;
    sourceDigest?: string;
    dest?: DestStatus;
    /** 目标 tag 现状探测失败的原因（源可达性不受影响）。 */
    destError?: string;
  }>('/api/pull/probe', { method: 'POST', body: JSON.stringify(input) });

export const listCredentials = () => request<Credential[]>('/api/credentials');

export const getCredential = (id: string) =>
  request<Credential>(`/api/credentials/${encodeURIComponent(id)}`);

export const createCredential = (input: CredentialInput) =>
  request<Credential>('/api/credentials', { method: 'POST', body: JSON.stringify(input) });

export const updateCredential = (id: string, patch: CredentialPatch) =>
  request<Credential>(`/api/credentials/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  });

export const deleteCredential = (id: string) =>
  request<{ id: string }>(`/api/credentials/${encodeURIComponent(id)}`, { method: 'DELETE' });

export const testCredential = (id: string) =>
  requestSlow<{
    apiVersion: string;
    host: string;
    registryUrl: string;
    purpose: string
  }>(
    `/api/credentials/${encodeURIComponent(id)}/test`,
    { method: 'POST' }
  );

export const listProxies = () => request<ProxyEntry[]>('/api/proxies');

export const getProxy = (id: string) =>
  request<ProxyEntry>(`/api/proxies/${encodeURIComponent(id)}`);

export const createProxy = (input: ProxyInput) =>
  request<ProxyEntry>('/api/proxies', { method: 'POST', body: JSON.stringify(input) });

export const updateProxy = (id: string, patch: ProxyPatch) =>
  request<ProxyEntry>(`/api/proxies/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(patch),
  });

export const deleteProxy = (id: string) =>
  request<{ id: string }>(`/api/proxies/${encodeURIComponent(id)}`, { method: 'DELETE' });

/**
 * v0.5.9: 探测代理地址本身的可达性。
 *
 * v0.5.15: 探测只做一次 TCP 建连（不握手、不发请求），回答「这个 ip:端口
 * 从本机连得上吗、往返多慢」，因此 http/https/socks5 三种代理一视同仁。
 * 返回 ok=false 不代表 proxy 不能用——只是此刻连不上；想看「能不能真的
 * 代理转发」请用 testProxy（穿过代理去取一个 target）。
 * latencyMs 为 0 / 缺失表示没测到延迟（失败，或条目由旧版本探测过）。
 */
export const probeProxy = (id: string) =>
  requestSlow<{
    id: string;
    ok: boolean;
    status?: string;
    probedAt?: string;
    error?: string;
    latencyMs?: number;
  }>(
    `/api/proxies/${encodeURIComponent(id)}/probe`,
    { method: 'POST' }
  );

/**
 * v0.5.12: 一次探测全部代理的可达性（v0.5.15 起为 TCP 建连，见 probeProxy）。
 *
 * 放服务端做而不是前端循环：服务端 ProbeAll 是并行的，整批最坏约 5 秒；
 * 前端逐条调用在全部不可达时最坏 N×5 秒。
 */
export const probeAllProxies = () =>
  requestSlow<ProxyProbeAllResult>('/api/proxies/probe', { method: 'POST' });

/** 测试代理连通性；targetUrl 留空则服务端用本 registry 的 /v2/。 */
export const testProxy = (id: string, targetUrl?: string) =>
  requestSlow<ProxyTestResult>(`/api/proxies/${encodeURIComponent(id)}/test`, {
    method: 'POST',
    body: JSON.stringify({ targetUrl: targetUrl || '' }),
  });

/**
 * v0.5.13: 用**尚未保存**的代理配置做一次连通性测试（保存前试连）。
 * 服务端不落库、不改探测状态。
 *
 * v0.5.15: 编辑弹窗也用它（入参带 id），那时「未保存」指的是表单里改到
 * 一半的值；服务端最多读一次已存密码，仍然不写任何东西。
 */
export const testProxyDraft = (input: ProxyTestInput) =>
  requestSlow<ProxyTestResult>('/api/proxies/test', {
    method: 'POST',
    body: JSON.stringify(input),
  });

// ── 镜像热度 ──
// 统计不可用时这些接口返回空结构而不是报错（原因由 /api/config 的 stats* 字段解释），
// 所以调用方不需要为它们单独做错误降级。

export const fetchStatsSummary = (days: number) =>
  request<StatsSummary>(`/api/stats/summary?days=${days}`);

export const fetchStatsTop = (days: number, by: StatsTopBy, limit = 20) =>
  request<StatsTop>(`/api/stats/top?days=${days}&limit=${limit}&by=${by}`);

/** repository 传空串表示全部仓库的合计。 */
export const fetchStatsSeries = (days: number, repository = '') =>
  request<StatsSeries>(
    `/api/stats/series?days=${days}&repository=${encodeURIComponent(repository)}`
  );

export const fetchRepositoryStats = (days: number) =>
  request<StatsRepositories>(`/api/stats/repositories?days=${days}`);

export const fetchStatsEvents = (limit = 50) =>
  request<StatsEvents>(`/api/stats/events?limit=${limit}`);

/**
 * 按客户端聚合的"见过的客户端"。
 * v0.5.52+："all" 表示无时间过滤,返回 event_seen 里的全部行(重启不丢);
 * 数字 N 表示只看最近 N 天内 LastSeenAt 的客户端。
 */
export const fetchStatsClients = (days: number | 'all') =>
  request<StatsClients>(`/api/stats/clients?days=${days}`);

/**
 * 清空全部热度数据（不按保留期），用于口径改正后从头重计。
 * **只清热度**，拉取历史不受影响。
 */
export const purgeHeat = () =>
  requestSlow<HeatPurgeResult>('/api/stats/heat', { method: 'DELETE' });

// ── 热度忽略规则（界面上管理，存 SQLite，立即生效）──

export const fetchIgnoreRules = () => request<IgnoreRules>('/api/stats/ignore');

export const addIgnoreRule = (useragent: string) =>
  request<IgnoreRules>('/api/stats/ignore', {
    method: 'POST',
    body: JSON.stringify({ useragent }),
  });

/**
 * 删一条**界面上的**规则。
 *
 * 用 DELETE + body 而不是把 UA 放进路径：规则里带 `/`（`regclient/regsync`），
 * 走路径参数要依赖 %2F 的解码行为，不如放 body 里没有歧义。
 */
export const removeIgnoreRule = (useragent: string) =>
  request<IgnoreRules>('/api/stats/ignore', {
    method: 'DELETE',
    body: JSON.stringify({ useragent }),
  });
