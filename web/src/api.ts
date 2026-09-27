import type {
  ApiResult,
  AppConfig,
  Credential,
  CredentialInput,
  CredentialPatch,
  DeleteTagPayload,
  DestStatus,
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
 */
async function request<T>(path: string, init?: RequestInit): Promise<ApiResult<T>> {
  try {
    const response = await fetch(path, {
      headers: { 'Content-Type': 'application/json' },
      ...init,
    });
    const payload = (await response.json()) as ApiResult<T>;
    if (typeof payload?.success !== 'boolean') {
      return { success: false, code: 'INVALID_RESPONSE', message: '服务返回了非预期响应' };
    }
    return payload;
  } catch (error) {
    return {
      success: false,
      code: 'NETWORK_ERROR',
      message: `无法连接管理服务: ${(error as Error)?.message ?? error}`,
    };
  }
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

export const fetchInventory = () => request<Inventory>('/api/inventory');

export const refreshInventory = () => request<Inventory>('/api/refresh', { method: 'POST' });

export const probeRegistry = () =>
  request<{ apiVersion: string; host: string }>('/api/probe', { method: 'POST' });

export const deleteTag = (repository: string, tag: string) =>
  request<DeleteTagPayload>(
    `/api/tags?repository=${encodeURIComponent(repository)}&tag=${encodeURIComponent(tag)}`,
    { method: 'DELETE' }
  );

// v0.5.0: 删除整个仓库（不可逆；UI 必须二次确认）。
export const deleteRepository = (repo: string) =>
  request<DeleteRepositoryPayload>(
    `/api/repositories/${encodeURIComponent(repo)}`,
    { method: 'DELETE' }
  );

// v0.5.0: 按 digest 删除 manifest，返回受影响 tag 列表。
export const deleteManifestByDigest = (repo: string, digest: string) =>
  request<DeleteManifestPayload>(
    `/api/repositories/${encodeURIComponent(repo)}/manifests/${encodeURIComponent(digest)}`,
    { method: 'DELETE' }
  );

// v0.5.0: 触发一次存储 GC 扫描，返回本次回收的 blob 数与字节数。
export const runGC = () =>
  request<GCResult>('/api/gc', { method: 'POST' });

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
  request<{
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
  request<{ apiVersion: string; host: string; registryUrl: string; purpose: string }>(
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
  request<{
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
  request<ProxyProbeAllResult>('/api/proxies/probe', { method: 'POST' });

/** 测试代理连通性；targetUrl 留空则服务端用本 registry 的 /v2/。 */
export const testProxy = (id: string, targetUrl?: string) =>
  request<ProxyTestResult>(`/api/proxies/${encodeURIComponent(id)}/test`, {
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
  request<ProxyTestResult>('/api/proxies/test', {
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

/** 按客户端聚合的"见过的客户端"。`days=0` 表示不限（当前实现里由页面传时间窗）。 */
export const fetchStatsClients = (days: number) =>
  request<StatsClients>(`/api/stats/clients?days=${days}`);

/**
 * 清空全部热度数据（不按保留期），用于口径改正后从头重计。
 * **只清热度**，拉取历史不受影响。
 */
export const purgeHeat = () => request<HeatPurgeResult>('/api/stats/heat', { method: 'DELETE' });

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
