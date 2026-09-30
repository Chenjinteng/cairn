export interface RegistryTag {
  tag: string;
  digest: string;
  size: number;
  layerCount: number;
  architecture: string;
  os: string;
  platformCount: number;
  createdAt: string | null;
}

export interface RegistryRepository {
  name: string;
  tags: RegistryTag[];
  tagCount: number;
  totalSize: number;
}

export interface InventoryError {
  repository: string;
  tag: string;
  code: string;
  message: string;
}

export interface Inventory {
  refreshedAt: string;
  apiVersion: string;
  host: string;
  durationMs: number;
  truncated: boolean;
  repositories: RegistryRepository[];
  errors: InventoryError[];
  errorCount: number;
}

export interface AppConfig {
  name: string;
  /** 当前运行中的版本（服务端从 package.json 读取）；取不到时为空串。 */
  version: string;
  /**
   * v0.5.34: 容器内 cairn 进程监听端口(PORT env 烘进 cfg.Port)。**只读** ——
   * 改端口要走 docker-compose.yml 改 HOST_PORT + 重建容器,UI 不暴露修改入口
   * (改了容器内监听但不改 docker 端口映射,用户视角实际无效)。
   */
  port: number;
  /**
   * v0.5.40: 宿主机侧对外端口(HOST_PORT env,docker-compose 把 .env 的
   * ${HOST_PORT:-8787} 传进来)。0 = 与 port 同值(无端口映射,直接容器访问)。
   * UI 「监听端口」字段同时展示「容器内 port / 宿主机 hostPort」两个值,
   * 帮运维一眼分清两件事。
   */
  hostPort: number;
  url: string;
  host: string;
  /** 是否给本 registry 配了 basic auth（密码不会回传）。 */
  usingAuth: boolean;
  cacheTtlSeconds: number;
  /** false 时服务端会拒绝删除请求，页面也要隐藏删除入口。 */
  allowDelete: boolean;
  /** false 时服务端拒绝一切拉取写入（GET 列表仍可读）。 */
  allowPull: boolean;
  pullQueueSize: number;
  /** false 时服务端没配密钥或加密存储初始化失败，凭据库不可用。 */
  allowCredentials: boolean;
  /** 代理库是否可用（与凭据库同源，取决于 REGISTRY_CREDENTIAL_KEY）。 */
  allowProxies: boolean;
  credentialsDir: string;
  /** 凭据库不可用时的具体原因；可用时为 null。 */
  credentialError: { code: string; message: string } | null;
  /**
   * 热度统计是否可用：开关打开**且**统计库初始化成功。
   * false 时热度页要显示解释性空状态，而不是报错。
   */
  statsEnabled: boolean;
  /** false 时服务端关闭了事件接收（REGISTRY_ALLOW_REGISTRY_EVENTS=false）。 */
  allowRegistryEvents: boolean;
  /** 统计库初始化失败的原因；正常时为 null。用来区分「坏了」和「还没配」。 */
  statsError: { code: string; message: string } | null;
  /** 是否配了事件共享密钥；密钥本身不回传。false 时事件会被全部拒绝。 */
  notifyTokenConfigured: boolean;
  /** 热度数据最早的一天（YYYY-MM-DD）；从未收到事件时为 null。 */
  statsSince: string | null;
  /** 热度数据的保留天数。 */
  statsRetentionDays: number;
  /**
   * 不计入热度的客户端 User-Agent 片段（`REGISTRY_STATS_IGNORE_USERAGENTS`）。
   *
   * 用来排掉 registry 上常驻的同步工具（regsync 之类）—— 它们按点扫全量，
   * 会把每个 tag 的热度刷成同一个数。registry 侧的 `notifications` 只能按
   * action / media type 过滤，没有按客户端过滤的入口，所以只能在这一侧排。
   * 下发到前端是为了能**确认规则生效了**：否则"热度不涨"和"配置没读到"看起来一样。
   */
  statsIgnoreUseragents: string[];

  /**
   * v0.5.1: runtime-editable settings shown + edited on the settings page.
   * `registryUrl` here is the *current effective* value (mutable override
   * (single source of truth: SQLite settings, no env fallback).
   * the user is looking at a "db" override or the "env" bootstrap default.
   */
  mutable: MutableSettings;
}

/**
 * v0.5.3: editable settings (full set). The server-side update config
 * handler validates each key against config.MutableKeys; this type only
 * surfaces the fields the UI actually edits. The password is stored
 * plaintext in SQLite and is NEVER echoed back -- the UI sends it on
 * save and the server applies it.
 */
export interface MutableSettings {
  registryUrl: string;
  registryName: string;
  registryUsername: string;
  /** true when username + password are both set (env or db). */
  usingAuth: boolean;
  // v0.5.2 toggles surfaced on the settings page (v0.5.4). cache.ttl.seconds
  // was removed from the editable set in v0.5.4 (no runtime consumer).
  allowDelete: boolean;
  allowPull: boolean;
  allowRegistryEvents: boolean;
  statsRetentionDays: number;
  /**
   * Platform allow-list applied to multi-arch image indexes on pull.
   * Empty string = "all platforms" (current behaviour). CSV of
   * "<os>/<arch>[/<variant>]" tokens (e.g. "linux/amd64,linux/arm64"
   * or "linux/amd64,linux/arm/v7"). Lowercased server-side.
   */
  pullPlatforms: string;
  /**
   * v0.5.48: CSV of operator-added third-party registry base URLs
   * ("<scheme>://host[:port]"). Normalised + deduped server-side on
   * save. Merged with the built-in well-known hosts for image-reference
   * parsing and offered in the pull page's source autocomplete.
   */
  pullKnownHosts: string;
}

/**
 * v0.5.18（F2）：一次失败的**可诊断信息**。
 *
 * 三类失败在界面上的处置完全不同，但过去都塌进同一句话（"无法连接管理服务"）：
 *
 *   1. 服务没回 —— `NETWORK_ERROR`（连不上 / 正文读不全）或 `TIMEOUT`
 *      （预算内没读完），`status` 为 0；
 *   2. 回话的不是 cairn —— 网关 HTML 错误页、反代 502、非 JSON 正文，
 *      `INVALID_RESPONSE` + 真实 HTTP `status` + `detail`（正文摘要）；
 *   3. cairn 明确拒绝 —— 后端信封 `success:false`，`code` 是领域原因
 *      （例如 registry 未开启删除），另带 `status` 便于与访问日志对账。
 *
 * 页面凭这个结构决定「重试」还是「把原因念给用户听」，
 * 见 components/load-error.tsx。
 */
export interface ApiFailureInfo {
  code: string;
  message: string;
  /** HTTP 状态码；0 / undefined 表示没拿到响应（连不上或超时）。 */
  status?: number;
  /** 非预期响应的正文摘要（已折叠空白并截断，见 api.ts summarizeBody）。 */
  detail?: string;
}

export interface ApiResult<T> extends ApiFailureInfo {
  success: boolean;
  data?: T;
}

export interface DeleteTagPayload {
  deletedTag: string;
  digest: string;
  affectedTags: string[];
  repository: RegistryRepository;
}

// v0.5.0: 删除整个仓库的结果（不可逆；UI 要二次确认）。
export interface DeleteRepositoryPayload {
  repo: string;
  deleted: true;
}

// v0.5.0: 按 digest 删除 manifest 的结果。
export interface DeleteManifestPayload {
  repo: string;
  digest: string;
  affectedTags: string[];
  deleted: true;
}

// v0.5.0: 存储 GC 一次扫描的回收量。
// v0.5.20: 加 removedEmptyRepos / emptyRepoFreedBytes,仅当请求带
// cleanEmptyRepos=true 时才返回(否则 omitempty 不在 JSON 里出现)。
export interface GCResult {
  removedBlobs: number;
  freedBytes: number;
  removedEmptyRepos?: string[];
  emptyRepoFreedBytes?: number;
}

/** v0.5.20: runGC 的请求体 —— 默认空 body 仍走 v0.5.18 路径。 */
export interface GCOption {
  cleanEmptyRepos?: boolean;
}

export type PullJobStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled';

export type PullPhaseStatus = 'pending' | 'running' | 'success' | 'failed' | 'skipped';

export interface PullPhase {
  /** 'manifest' / 'config' / 'blob:<index>'。layer phase 用 blob:<index> 表达"第 N 个 layer"。 */
  name: string;
  digest: string;
  status: PullPhaseStatus;
  bytes: number;
  totalBytes: number | null;
  message: string;
}

export interface PullJob {
  id: string;
  sourceUrl: string;
  sourceRef: string;
  /** 仅用于判断"是否填了代理"；不回显具体地址以避免误以为是凭据。 */
  sourceProxy: string;
  sourceProxyId?: string;
  sourceRepo: string;
  sourceTag: string;
  destRepo: string;
  destTag: string;
  status: PullJobStatus;
  bytes: number;
  totalBytes: number | null;
  phases: PullPhase[];
  finalDigest?: string;
  errorCode?: string;
  errorMessage?: string;
  /** 'source' | 'dest' | undefined。区分错误发生在源还是目的端。 */
  errorOrigin?: 'source' | 'dest';
  sourceCredentialId?: string;
  createdAt: string;
  startedAt?: string;
  finishedAt?: string;
  /**
   * 这条任务是**从数据库的历史里读出来的**（不是本次运行内存里的）。
   * 历史任务没有实时进度，且成功任务不保留阶段明细 —— 界面据此区别对待。
   */
  fromHistory?: boolean;
}

export interface PullJobInput {
  sourceUrl: string;
  sourceRef: string;
  sourceProxy?: string;
  /** 代理库里的代理 id；与 sourceProxy 二选一（id 优先）。 */
  sourceProxyId?: string;
  destRepo: string;
  destTag?: string;
  sourceCredentialId?: string;
  /** 临时 inline 凭据：不落库，仅当次任务使用。目的端凭据来自服务配置，不在此处。 */
  sourceAuthInline?: { username: string; password: string };
}

/**
 * 预览时对目标 tag 现状的探测结果。
 * 目标引用一律是「本仓库地址 + 源镜像路径」，所以这里只涉及本 registry 内的路径。
 */
export interface DestStatus {
  sourceRepo: string;
  sourceTag: string;
  destRepo: string;
  destTag: string;
  /** 目标 tag 是否已存在。probeError 存在时此字段无意义。 */
  exists?: boolean;
  existingDigest?: string | null;
  /** 源侧该 tag 是否存在 —— 拼错 tag 在这里就能拦下，不必等入队后失败。 */
  sourceExists?: boolean;
  sourceDigest?: string | null;
  /** 已存在且 digest 与源不同 —— 拉取会替换现有 tag。 */
  willReplace?: boolean;
  /** 已存在且 digest 与源相同 —— 重复拉取没有意义。 */
  identical?: boolean;
  /** 目标探测失败的原因（不影响源可达性判断）。 */
  probeError?: string;
}

/**
 * 凭据库只存**外部源**的 basic auth。
 * 本 registry 自身的凭据属于部署配置（registry.config.json / REGISTRY_USERNAME），
 * 因此这里没有"用途"维度。
 */
export interface Credential {
  id: string;
  name: string;
  registryUrl: string;
  username: string;
  /** 是否设置过密码；密码本身不会回传到前端。 */
  hasPassword: boolean;
  note?: string;
  createdAt: string;
  updatedAt: string;
}

export interface CredentialInput {
  name: string;
  registryUrl: string;
  username: string;
  password: string;
  note?: string;
}

export interface CredentialPatch {
  name?: string;
  registryUrl?: string;
  username?: string;
  /** 传空字符串视为不更新密码；省略同空。 */
  password?: string;
  note?: string;
}

/**
 * 代理库条目：只服务**外部源**。
 * 本 registry 自身的代理属于部署配置（registry.config.json 的 proxy），不在这里。
 */
export interface ProxyEntry {
  id: string;
  name: string;
  /** 形如 http://proxy.example.com:8080 */
  url: string;
  username: string;
  /** 是否配了账号（密码不回传）。 */
  hasAuth: boolean;
  note?: string;
  createdAt: string;
  updatedAt: string;
  /** v0.5.9: reachability — 'ok' / 'failed' / 'unknown' (never probed) */
  lastProbeStatus?: 'ok' | 'failed' | 'unknown' | '';
  lastProbeAt?: string;
  lastProbeError?: string;
  /**
   * v0.5.15: 探测的 TCP 建连往返耗时（毫秒）。
   * 探测失败时为 0；旧版本探测过的条目也没有它——两者一律不显示延迟。
   */
  lastProbeLatencyMs?: number;
}

export interface ProxyInput {
  name: string;
  url: string;
  username?: string;
  password?: string;
  note?: string;
}

export interface ProxyPatch {
  name?: string;
  url?: string;
  username?: string;
  /** 传空字符串 = 清掉密码（改成匿名代理）。 */
  password?: string;
  note?: string;
}

/**
 * v0.5.13: 保存前「测试连接」的入参。
 *
 * 这是一次**不落库**的试连，测的就是表单里此刻的值，既不写 proxies.json，
 * 也不改任何条目的探测状态。
 *
 * v0.5.15: 编辑弹窗复用同一端点。带上 id 表示「这是一条已存的代理」：
 * 表单不回显密码，所以密码留空且用户名未改动时，服务端会回退到已存密码，
 * 让试连结论跟用户在列表里看到的那条保持一致。新增（未保存）时省略 id。
 */
export interface ProxyTestInput {
  /** 已存条目的 id；新增（还没保存）时省略。 */
  id?: string;
  url: string;
  username?: string;
  password?: string;
  /** 留空 = 服务端用本 registry 的 /v2/。 */
  targetUrl?: string;
}

/** 代理连通性测试结果。 */
export interface ProxyTestResult {
  ok: boolean;
  status?: number;
  statusText?: string;
  elapsedMs: number;
  targetUrl: string;
  registryApiVersion?: string | null;
  error?: string;
  /**
   * v0.5.19: human-readable explanation when the upstream returned a non-2xx
   * response (e.g. "目标要求认证（HTTP 401）；代理可达,目标在线"). `ok` is still
   * true in that case — see `proxyTestThrough` for the rationale.
   */
  note?: string;
}

/**
 * v0.5.12: 「探测全部」里单个代理的结果。
 * 字段与逐行探测的返回刻意保持一致，前端可以统一渲染。
 */
export interface ProxyProbeSummary {
  id: string;
  name: string;
  ok: boolean;
  /** 落库后的状态：'ok' / 'failed' / ''（从未探测）。 */
  status?: string;
  probedAt?: string;
  error?: string;
  /** v0.5.15: TCP 建连往返耗时（毫秒）；失败时省略。 */
  latencyMs?: number;
}

/**
 * v0.5.12: `POST /api/proxies/probe` 的汇总返回。
 *
 * `unknown` 覆盖"探测期间该条目被删掉"这类边角：不能把没出结果的说成不可用。
 * `ok + failed + unknown === total`。
 */
export interface ProxyProbeAllResult {
  total: number;
  ok: number;
  failed: number;
  unknown: number;
  results: ProxyProbeSummary[];
}

/** 热度统计窗口。页面只提供这三档，服务端本身接受任意天数。 */
export type StatsWindow = 7 | 30 | 90;

/** Top 榜单的聚合维度：按仓库或按 tag。 */
export type StatsTopBy = 'repository' | 'tag';

/** 总览（`/api/stats/summary`）。 */
export interface StatsSummary {
  days: number;
  total: number;
  repositories: number;
  tags: number;
  lastAt: string | null;
  push: number;
  pull: number;
}

/**
 * Top 榜单条目。
 * `by=repository` 时带 `tags`，`by=tag` 时带 `tag` —— 两个字段因此都是可选的。
 */
export interface StatsTopItem {
  repository: string;
  tag?: string;
  events: number;
  pull: number;
  push: number;
  tags?: number;
  lastAt: string | null;
}

export interface StatsTop {
  days: number;
  by: StatsTopBy;
  items: StatsTopItem[];
}

/** 按天趋势的一个点。`day` 是 YYYY-MM-DD。 */
export interface StatsSeriesPoint {
  day: string;
  events: number;
  pull: number;
  push: number;
}

export interface StatsSeries {
  days: number;
  repository: string;
  points: StatsSeriesPoint[];
}

/** 单个仓库在窗口内的热度，供镜像列表页做一次 join。 */
export interface StatsRepositoryStat {
  events: number;
  pull: number;
  push: number;
  lastAt: string | null;
}

export interface StatsRepositories {
  days: number;
  /** 只包含**有热度**的仓库；查不到的仓库表示窗口内没有事件。 */
  items: Record<string, StatsRepositoryStat>;
}

/** 最近收到的原始事件（排查用）。 */
export interface StatsEventItem {
  /** 服务端收到事件的时间（ISO8601）。 */
  at: string;
  /** registry 自己的事件时间戳；小数位不固定，交给 new Date 解析。 */
  eventAt: string;
  id: string;
  action: string;
  method: string;
  mediaType: string;
  repository: string;
  tag: string;
  /**
   * 客户端身份，排查"热度是不是被自动化进程刷高了"时唯一的线索。
   *
   * `useragent` 通常最可靠（`docker/27.x ...` vs `regclient/...`）。另外三个各有局限：
   * `addr` 在端口映射下是 Docker 网桥网关而不是真实客户端，`host` 内网里常常全员相同，
   * `actor` 未开认证时是空的。
   */
  useragent: string;
  addr: string;
  host: string;
  actor: string;
  /** 未计入时的原因（例如 NOT_MANIFEST / METHOD_GET）；计入时为 OK。 */
  reason: string;
  counted: boolean;
  /** true 表示 event.id 之前已经记过，本次按幂等丢弃。 */
  duplicate?: boolean;
}

/**
 * 按客户端聚合的"见过的客户端"。
 *
 * 与「最近事件」是两种视图：那个是**逐条**的内存窗口（十几小时、重启就空），
 * 这个是**按客户端**的持久聚合 —— 行数等于不同 UA 的数量，天然有界，
 * 所以无论客户端来得多慢、中间重启过几次，"有没有我没见过的在打"都答得上来。
 */
export interface StatsClientItem {
  useragent: string;
  /** 第一次见到它（用来发现"新出现的客户端"）。 */
  firstSeenAt: string;
  lastSeenAt: string;
  /** 收到的事件条数（含被规则排掉的）。 */
  events: number;
  /** 其中**计入热度**的条数；events 有值而它是 0 = 收到了但被排掉了。 */
  counted: number;
  /** 本工具自己（`registry-manager/*`）。 */
  self: boolean;
}

export interface StatsClients {
  /** "all" (v0.5.52+) = no time filter, every persisted client;
   *  number = limit to LastSeenAt within the last N days. */
  days: number | 'all';
  items: StatsClientItem[];
}

export interface StatsEvents {
  items: StatsEventItem[];
  totals: {
    accepted: number;
    rejected: number;
    buffered: number;
    /**
     * 排查缓冲的**容量**（服务端回显，别在前端写死数字）。
     *
     * 界面必须写明这张表是**内存缓存、不落盘、重启即清空** —— 它看着像历史记录，
     * 实际只覆盖最近一小段，且没有任何持久性。不写这一句，用户会把它当历史用。
     */
    bufferSize: number;
    /**
     * **被折叠**的自身请求条数 —— 也就是本工具自己的**读取**请求
     * （一次刷新 inventory 按 tag 数量产生一批，实测 178 条，缓冲只有 200）。
     * 它们永远不计入热度，纯噪音，所以折叠成一个数字、不占面板。
     *
     * 注意与"计入热度的自身请求"区分：用「镜像拉取」搬进本仓库的 manifest（PUT）
     * **会计入热度，也会列在 items 里** —— 它改了热度却看不见的话，
     * "热度为什么变了"就查不出来了。
     */
    self: number;
    /**
     * 被折叠的"已命中忽略规则"的事件条数。
     *
     * 已被忽略的事件**不进 items** —— 200 条的窗口实测只覆盖最近十几小时，
     * 而填满它的全是已经处理过的噪音，真正需要你瞄一眼的"还没分类的客户端"反而被挤掉。
     * "收到过但被排掉了"由客户端清单回答（events 有、counted 为 0）。
     */
    ignored: number;
  };
}

/** 清空热度数据的结果：删掉了多少行。 */
export interface HeatPurgeResult {
  /** 按天聚合的行数（`activity_daily`）。 */
  activity: number;
  /** 幂等去重记录数（`event_seen`）。 */
  seen: number;
}

/**
 * 热度忽略规则，按来源分开。
 *
 * 两个来源**同时生效**（取并集）：
 *  - `env`：环境变量 `REGISTRY_STATS_IGNORE_USERAGENTS` 给的，声明式部署用，**界面上删不掉**；
 *  - `panel`：界面上加的，存 SQLite，增删立即生效、不用重启。
 * 分开返回就是为了让设置页能标出"这条来自环境变量"，否则用户删了没反应时找不到原因。
 */
export interface IgnoreRules {
  env: string[];
  panel: string[];
  /** 并集去重后的最终列表；判定与界面回显用的都是它。 */
  effective: string[];
}

/**
 * v0.6.0: 镜像同步（cairn↔cairn）。
 *
 * 同步任务的方向：pull 把远端 cairn 拉到本地；push 把本地 cairn 推送到远端。
 * 每个 task 单独配方向，双方向需要两条记录。
 *
 * v0.6.1 hotfix：认证从 bearer 改成 Basic（匹配 cairn 自己的 /v2/* Basic
 * middleware）。远端 cairn 的 basic-auth 用户名 + 密码存在 `remoteUsername` /
 * `remotePassword` 两个字段里——`remotePassword` 后端用 json:"-" 屏蔽（API
 * 永远不返明文），`remoteUsername` 可见（让 UI 编辑时能预填）。新建密码必填，
 * 更新时密码字段空 = 保留旧值（编辑其他字段不必重输 secret）。
 */
export type SyncDirection = 'pull' | 'push';

/**
 * 单条同步任务。`remotePassword` 后端用 json:"-" 屏蔽——UI 永远拿不到明文,
 * 列表 / 详情响应都不带这个字段。新建必填,更新可省略(空字符串 = 保留旧的)。
 *
 * v0.6.11（SYNC-3）：`remoteCredentialId` 非空 = 凭据引用模式，运行 / 测连接时
 * 后端从凭据库按 id 取用户名密码；内联的 remoteUsername / remotePassword 不再使用。
 * `lastRunStatus` 来自列表 / 详情查询的子查询（SYNC-1/4），`running` 表示该任务
 * 后台仍在跑，UI 据此禁用「立即运行」（刷新页面后也不会重新可点）。
 */
export interface SyncTask {
  id: number;
  name: string;
  direction: SyncDirection;
  remoteUrl: string;
  /** 远端 cairn 的 Basic-auth 用户名；不敏感，API 可见，UI 编辑时可预填。 */
  remoteUsername: string;
  /** v0.6.11：凭据管理库中某条凭据的 id；非空 = 引用模式，忽略内联用户名密码。 */
  remoteCredentialId?: string;
  /** v0.6.11：最近一次运行的状态（列表响应携带）；`running` = 后台仍在跑。 */
  lastRunStatus?: SyncRunStatus;
  /** v0.6.11：当前 run 正在拉的 repo（lastRunStatus==='running' 时显示）。空字符串 = 还没到具体 repo。 */
  lastRunCurrentRepo?: string;
  /** v0.6.11：当前 run 正在拉的 tag。空字符串 = 已进 repo 但还没到具体 tag。 */
  lastRunCurrentTag?: string;
  /** 换行分隔的 glob 模式（`*` 通配），空 = 全匹配。 */
  include: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

/**
 * 新建 / 更新同步任务的请求体。`remotePassword` 在更新时可省略——后端会保留
 * 旧值,编辑「名称」「远端 URL」「Include」时不必重输一次（UI 也根本没机会拿到）。
 *
 * v0.6.11（SYNC-3）：凭据三选一——凭据引用（`remoteCredentialId` 非空，内联必须留空）、
 * 内联用户名密码（`remoteUsername` 非空，引用必须留空）、匿名（三字段全空）。
 */
export interface SyncTaskInput {
  name: string;
  direction: SyncDirection;
  remoteUrl: string;
  remoteUsername: string;
  remotePassword: string;
  /** v0.6.11：非空 = 引用凭据库；与内联用户名密码互斥。 */
  remoteCredentialId: string;
  include: string;
  enabled: boolean;
}

/**
 * 同步任务的执行记录。`status` 决定后续如何渲染:
 *   - running : 引擎仍在跑（v0.6.0 同步触发后等返回,这个状态只短暂出现）；
 *   - success : 全部成功；
 *   - partial : 有 repo 失败,但整体跑完了；
 *   - failed  : 启动前就失败了（远端不可达、token 错误等）。
 */
export type SyncRunStatus = 'running' | 'success' | 'partial' | 'failed';

export interface SyncRun {
  id: number;
  taskId: number;
  startedAt: string;
  finishedAt?: string;
  status: SyncRunStatus;
  reposTotal: number;
  reposSynced: number;
  reposFailed: number;
  error?: string;
  /** v0.6.11：引擎当前正在拉的 repo（同步运行中显示）。空字符串 = 不在任何具体 repo 上。 */
  currentRepo?: string;
  /** v0.6.11：引擎当前正在拉的 tag。失败 run 保留最后位置用于调试。 */
  currentTag?: string;
}

/**
 * v0.6.5: 「测试连接」按钮的返回结构。后端打 `{remoteUrl}/v2/` 探测,
 * 按 HTTP 状态 + WWW-Authenticate 头归类成 AuthStatus。前端按这个
 * 字段选图标 / 颜色 / 文案,统一渲染。HTTP 200 = 探测请求本身成功,
 * 但语义是否「能跑同步」要看 AuthStatus:
 *   ok                  — Basic 认证通过,可以 Run
 *   no_auth_required    — 匿名访问通过,可以 Run
 *   required_but_missing— 对端要求认证,用户名密码留空了 → 改表单
 *   wrong_creds         — 401,凭据被拒 → 改密码
 *   not_registry        — /v2/ 404,URL 不是 OCI registry → 改 URL
 *   unknown             — 其它(401 但 WWW-Auth 不是 Basic / 5xx 等)
 */
export type SyncProbeAuthStatus =
  | 'ok'
  | 'no_auth_required'
  | 'required_but_missing'
  | 'wrong_creds'
  | 'not_registry'
  | 'unknown';

export interface SyncProbeResult {
  reachable: boolean;
  authStatus: SyncProbeAuthStatus;
  httpStatus: number;
  /** 中文一句话,UI 直接 Alert 显示;后端已经填好,前端不翻译。 */
  message: string;
}

export interface SyncTestInput {
  remoteUrl: string;
  /** v0.6.11：非空 = 用凭据库里的凭据探测；此时内联用户名密码忽略。 */
  remoteCredentialId: string;
  remoteUsername: string;
  remotePassword: string;
}


/**
 * v0.6.11：每个 sync 任务可以附加多个定时规则,调度器每 30s 扫一次
 * sync_schedules 表把到期的那条调 Engine.Start(task)。
 *
 * NextRunAt / LastRunAt / LastRunId 由后端计算,前端只读;只有
 * CronExpr / Timezone / Enabled 可写。
 */
export interface SyncSchedule {
  id: number;
  taskId: number;
  cronExpr: string;
  timezone: string;
  enabled: boolean;
  nextRunAt: string;
  lastRunAt?: string;
  lastRunId?: number;
  createdAt: string;
  updatedAt: string;
}

/**
 * POST/PATCH /api/sync/{id}/schedules 的请求体。Timezone 空 = UTC;
 * CronExpr 必须填。Enabled 可省略,默认 true。
 */
export interface SyncScheduleInput {
  cronExpr: string;
  timezone?: string;
  enabled?: boolean;
}
