import { useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  Alert,
  Button,
  Collapse,
  Empty,
  Segmented,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd';
import {
  CloudDownloadOutlined,
  CloudUploadOutlined,
  DatabaseOutlined,
  ReloadOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import IgnoreRuleModal from '../components/ignore-rule-modal';
import LoadError from '../components/load-error';
import PageLoading from '../components/page-loading';
import TopBarChart from '../components/top-bar-chart';

import { useAppConfig } from '../config-store';

import {
  fetchStatsClients,
  fetchStatsEvents,
  fetchStatsSeries,
  fetchStatsSummary,
  fetchStatsTop,
} from '../api';
import MetricCard from '../components/metric-card';
import ContributionHeatmap from '../components/contribution-heatmap';
import type { SidebarGroup, SidebarSelection } from '../components/page-sidebar';
import type {
  ApiResult,
  AppConfig,
  IgnoreRules,
  StatsClientItem,
  StatsEventItem,
  StatsEvents,
  StatsSeriesPoint,
  StatsSummary,
  StatsTopBy,
  StatsTopItem,
  StatsWindow,
} from '../types';
import { formatDateTime } from '../utils';

interface Props {
  config: AppConfig | null;
  onConfigChange: (config: AppConfig) => void;
  /**
   * v0.5.37.4:从侧栏传入的当前选择（groupKey -> itemKey）。
   * 「时间窗」组选中某档 → 下面的 days 跟着变；侧栏没有选择时兜底 30 天。
   */
  sidebarFilter: SidebarSelection;
  /** 把本页真实分组上浮给 App，再统一下发给 PageSidebar。热度统计未启用时下发空组。 */
  onPublishGroups: (groups: SidebarGroup[]) => void;
  /**
   * v0.6.26: App 层缓存的 stats 数据(summary/topItems/points/events/
   * clients/totals)。null = 首次进入 → 走 fetch + PageLoading;非 null = 之前看过
   * → 用作初始值,后台静默刷新。详细说明见 App.tsx 注释。
   */
  initialData: {
    summary: StatsSummary | null;
    topItems: StatsTopItem[];
    points: StatsSeriesPoint[];
    events: StatsEventItem[];
    clients: StatsClientItem[];
    totals: StatsEvents['totals'] | null;
  } | null;
  /** v0.6.26: stats 数据任一字段更新时回调,App 用它写回缓存层。 */
  onDataChange?: (data: Props['initialData']) => void;
}

/**
 * v0.5.37.4:时间窗选项挪进了侧栏（侧栏存的是离散 item key），
 * 这里只留 key → 天数的映射；页头原来那个 Segmented 是同一状态的第二入口，已删除。
 */
const WINDOW_VALUES: Record<string, StatsWindow> = {
  '7d': 7,
  '30d': 30,
  '90d': 90,
};
const DEFAULT_WINDOW: StatsWindow = 30;

/**
 * 日历的跨度：**固定 12 个月，不随时间窗变化**。
 *
 * 为什么不跟 Segmented 走：日历的宽度由**列数**决定（一周一列）。30 天只有 5 列，
 * 不论格子多大都只在面板左侧占一小块，右侧一大片空白 —— 实测被反馈"太不好看"。
 * 要像 GitHub 那样铺满，只能靠足够长的跨度：12 个月 ≈ 53 列。
 *
 * 代价：热度数据的保留期默认 90 天，更早的日期会显示成灰色空格。
 * 这不是"没有活动"，而是"数据已被保留期清掉"，所以 caption 里必须写明，
 * 否则会被读成"那段时间没人用"。
 */
const HEATMAP_DAYS = 365;

/**
 * v0.6.15 (UI-5): Top 榜单硬上限 10 条。
 *
 * 用户诉求「只要 Top 10 就行,太长会出双滚动条」,本质上是嵌套滚动条
 * 可读性问题(`.table-scroll--bounded` max-height 460px + 15 条目时
 * 内层出第二条竖向滚动条)。Top 10 大致 ~320px + 表头 ≈ 360px,
 * 落在 bounded 容器内,不再触发内层滚动。
 *
 * 服务端目前不限条数,客户端切片是双保险。
 */
const TOP_LIMIT = 10;

const BY_OPTIONS: { label: string; value: StatsTopBy }[] = [
  { label: '按仓库', value: 'repository' },
  { label: '按 tag', value: 'tag' },
];

/**
 * registry 侧的通知配置片段。刻意放在前端硬编码：
 * 它是 Distribution 的配置格式，不是本服务的接口契约，从后端取反而会把两件事耦合起来。
 */
// v0.5.8: this snippet is for EXTERNAL registries (Docker Distribution,
// Harbor, etc.) that want to feed events here. The built-in registry
// bundled with cairn already auto-feeds without any of this.
const NOTIFY_CONFIG_YAML = `notifications:
  endpoints:
    - name: registry-manager
      url: http://registry-manager:8787/api/registry-events
      headers:
        Authorization: [Bearer <与 REGISTRY_NOTIFY_TOKEN 相同的密钥>]
      timeout: 2s
      threshold: 5
      backoff: 1s`;

export default function StatsPage({
  config,
  onConfigChange,
  sidebarFilter,
  onPublishGroups,
  initialData,
  onDataChange,
}: Props) {
  /**
   * v0.5.37.4：时间窗不再自带 state —— 它在侧栏里，App 存 choice，这里只是读出来。
   * 侧栏没选（首屏）时兜底 30 天，跟 App 初值 `{ window: '30d' }` 一致。
   */
  const days = WINDOW_VALUES[sidebarFilter.window ?? ''] ?? DEFAULT_WINDOW;
  const [topBy, setTopBy] = useState<StatsTopBy>('repository');
  /*
   * v0.6.26: 5 个数据字段用 initialData(来自 App 层缓存)初始化 —— 切走再回时
   * 立刻有数据可显示,loading=false 不闪 PageLoading;useEffect 仍然走
   * 5 路 fetch,silent update 写入本地 + 通过 onDataChange 写回 App。
   *
   * initialData=null 是首次进入,所有字段 fallback 到空 + loading=true 走 fetch。
   */
  const [summary, setSummary] = useState<StatsSummary | null>(initialData?.summary ?? null);
  const [topItems, setTopItems] = useState<StatsTopItem[]>(initialData?.topItems ?? []);
  const [points, setPoints] = useState<StatsSeriesPoint[]>(initialData?.points ?? []);
  const [events, setEvents] = useState<StatsEventItem[]>(initialData?.events ?? []);
  const [clients, setClients] = useState<StatsClientItem[]>(initialData?.clients ?? []);
  /**
   * v0.5.52: 「见过的客户端」 默认 "all" —— 该面板落 SQLite 之后,主用途是
   * "看看有没有非法的在打",限定时间窗反而把"上次重启前那个可疑客户端"挡掉了。
   * 仍提供 7/30/90 切换,跟时间窗对齐看趋势。
   */
  const [clientsDays, setClientsDays] = useState<number | 'all'>('all');
  const [eventTotals, setEventTotals] = useState<StatsEvents['totals'] | null>(
    initialData?.totals ?? null,
  );
  /*
   * v0.6.26: loading 初值跟 initialData 绑定 —— 首次进入 (initialData=null)
   * → loading=true 走 fetch + PageLoading;切走再回 (initialData 非 null)
   * → loading=false,PageLoading 不出现,后台 useEffect 仍然走 fetch,silent
   * update 通过 onDataChange 写回 App 缓存。
   */
  const [loading, setLoading] = useState<boolean>(initialData === null);
  const [error, setError] = useState<ApiResult<unknown> | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
  /**
   * v0.5.18（F2 / F3 / F6）：配置读不到时，这一页过去会永远停在「正在读取服务配置…」
   * —— 既不是加载中，也没有任何出口。现在失败落到 configFailure，由下面的
   * LoadError 呈现并给重试入口。
   *
   * 取配置这件事本身挪进了 config-store（F6）：五个页面共用一份缓存与单飞，
   * 失败不清空已经拿到的配置；自动加载只在 store 里做一次，失败后不自动重试。
   */
  const { failure: configFailure, loading: configLoading, reload: reloadConfig } = useAppConfig();
  /**
   * 「忽略这个客户端」的弹框。入口在「最近事件」每一行的「客户端」格子上 ——
   * 你要排掉某个客户端时，人正看着那条 UA，不该被赶到设置页去手动粘一遍。
   */
  const [ignoreModal, setIgnoreModal] = useState<{ open: boolean; suggested: string }>({
    open: false,
    suggested: '',
  });

  const statsEnabled = config?.statsEnabled === true;

  /**
   * v0.5.37.4：侧栏只放这一页真实存在的「视图状态」。热度统计未启用时
   * 时间窗没有意义（页面本身就是空态），下发空组让侧栏显示占位文案。
   */
  const sidebarGroups = useMemo<SidebarGroup[]>(
    () =>
      statsEnabled
        ? [
            {
              key: 'window',
              label: '时间窗',
              items: [
                { key: '7d', label: '最近 7 天' },
                { key: '30d', label: '最近 30 天' },
                { key: '90d', label: '最近 90 天' },
              ],
            },
          ]
        : [],
    [statsEnabled]
  );

  useEffect(() => {
    onPublishGroups(sidebarGroups);
  }, [onPublishGroups, sidebarGroups]);

  /** 已经生效的规则（环境变量 ∪ 界面）。已经命中的客户端就不再给「忽略」入口。 */
  const ignoreRules = config?.statsIgnoreUseragents ?? [];
  const isIgnoredUa = (useragent: string) => {
    const value = String(useragent ?? '').toLowerCase();
    return value.length > 0 && ignoreRules.some((rule) => value.includes(rule.toLowerCase()));
  };

  /** 弹框里"会匹配到什么"的预览素材：当前缓冲里出现过的客户端。 */
  const knownUseragents = useMemo(
    () => [...new Set(events.map((event) => event.useragent).filter(Boolean))],
    [events]
  );

  /** 保存规则后：立刻重拉事件（新规则当场就生效），并把生效规则同步回配置。 */
  const handleIgnoreSaved = (rules: IgnoreRules) => {
    setIgnoreModal({ open: false, suggested: '' });
    if (config) {
      onConfigChange({ ...config, statsIgnoreUseragents: rules.effective });
    }
    setReloadKey((key) => key + 1);
  };

  useEffect(() => {
    if (!statsEnabled) {
      // v0.5.18（F4）：未启用也要把 loading 落下来。上一轮（启用中）跑出去的
      // setLoading(true) 在 cleanup 之后没人收口，页面会永远停在「加载中」。
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setError(null);
    void (async () => {
      // 五路并发：任何一路失败都不影响其它块渲染，缺哪块提示哪块。
      const [summaryResult, topResult, seriesResult, eventsResult, clientsResult] =
        await Promise.all([
          fetchStatsSummary(days),
          fetchStatsTop(days, topBy),
          // 日历固定看 12 个月，与上面的时间窗无关（原因见 HEATMAP_DAYS 的注释）。
          fetchStatsSeries(HEATMAP_DAYS),
          fetchStatsEvents(50),
          fetchStatsClients(clientsDays),
        ]);
      if (cancelled) {
        return;
      }
      if (summaryResult.success && summaryResult.data) {
        setSummary(summaryResult.data);
      }
      if (topResult.success && topResult.data) {
        setTopItems(topResult.data.items);
      }
      if (seriesResult.success && seriesResult.data) {
        setPoints(seriesResult.data.points);
      }
      if (eventsResult.success && eventsResult.data) {
        setEvents(eventsResult.data.items);
        setEventTotals(eventsResult.data.totals);
      }
      if (clientsResult.success && clientsResult.data) {
        setClients(clientsResult.data.items);
      }
      /*
       * v0.6.26: 把当前 stats 数据快照写回 App 缓存层。
       *
       * 在 setLoading(false) 之前调用 —— 此时 latest 字段全是这一轮 fetch
       * 的新值,React 还没 commit,本地 state 是上一轮的旧值(来自
       * useState 初值或上次的 silent update)。直接读闭包变量,不读 state,
       * 避免读到旧值。
       *
       * 仅在失败次数 < 全部时写回 —— 全失败的快照会污染 App 缓存,下次
       * 切回来仍然过不了 fetch(loading=false 但数据是空的),体感更糟。
       */
      const anyFailed = [summaryResult, topResult, seriesResult, eventsResult, clientsResult].some(
        (item) => !item.success,
      );
      if (!anyFailed && onDataChange) {
        onDataChange({
          summary: summaryResult.success ? summaryResult.data ?? null : null,
          topItems: topResult.success ? topResult.data?.items ?? [] : [],
          points: seriesResult.success ? seriesResult.data?.points ?? [] : [],
          events: eventsResult.success ? eventsResult.data?.items ?? [] : [],
          clients: clientsResult.success ? clientsResult.data?.items ?? [] : [],
          totals: eventsResult.success ? eventsResult.data?.totals ?? null : null,
        });
      }
      const failure = [summaryResult, topResult, seriesResult, eventsResult, clientsResult].find(
        (item) => !item.success
      );
      if (failure) {
        setError(failure);
      }
    })().finally(() => {
      // v0.5.18（F4）：收口放 finally；cancelled 早退的旧轮不得关掉新轮的 loading。
      if (!cancelled) {
        setLoading(false);
      }
    });
    return () => {
      cancelled = true;
    };
  }, [days, topBy, statsEnabled, reloadKey]);

  /**
   * 「见过的客户端」列。
   *
   * 这一块存在的理由：200 条的「最近事件」实测只覆盖最近十几小时，而且重启就空 ——
   * 一个一天只来一次的客户端，你还没打开页面它就已经被挤出去了，但热度每天都在被它污染。
   * 按 UA 聚合之后行数与事件量无关（现实里十几个），所以"有没有我没见过的在打"随时答得上来。
   */
  const clientColumns: ColumnsType<StatsClientItem> = [
    {
      title: '客户端',
      dataIndex: 'useragent',
      key: 'useragent',
      ellipsis: { showTitle: false },
      render: (value: string) => (
        <Tooltip title={value}>
          <span className="mono ellipsis" style={{ display: 'block' }}>
            {value || '—'}
          </span>
        </Tooltip>
      ),
    },
    {
      title: '事件数',
      dataIndex: 'events',
      key: 'events',
      width: 90,
      sorter: (left, right) => left.events - right.events,
    },
    {
      title: '计入',
      dataIndex: 'counted',
      key: 'counted',
      width: 90,
      sorter: (left, right) => left.counted - right.counted,
      // 一条都没计入 = 已经被规则排掉了，弱化显示；有计入的才是"正在影响热度"的。
      render: (value: number, record) =>
        value > 0 ? (
          <span style={{ color: 'var(--color-text-1)' }}>{value}</span>
        ) : (
          <Tooltip title={record.self ? '本工具自己的读取请求，本来就不计入' : '全部被忽略规则排掉了'}>
            <span style={{ color: 'var(--color-text-4)' }}>0</span>
          </Tooltip>
        ),
    },
    {
      title: '首次见到',
      dataIndex: 'firstSeenAt',
      key: 'firstSeenAt',
      width: 170,
      sorter: (left, right) => left.firstSeenAt.localeCompare(right.firstSeenAt),
      render: (value: string) => formatDateTime(value),
    },
    {
      title: '最近见到',
      dataIndex: 'lastSeenAt',
      key: 'lastSeenAt',
      width: 170,
      defaultSortOrder: 'descend',
      sorter: (left, right) => left.lastSeenAt.localeCompare(right.lastSeenAt),
      render: (value: string) => formatDateTime(value),
    },
    {
      title: '操作',
      key: 'actions',
      width: 100,
      render: (_, record) => {
        if (record.self) {
          return <span style={{ color: 'var(--color-text-4)', fontSize: 12 }}>本工具</span>;
        }
        if (isIgnoredUa(record.useragent)) {
          return <Tag style={{ marginInlineEnd: 0 }}>已忽略</Tag>;
        }
        return (
          <Button
            type="link"
            size="small"
            style={{ padding: 0, height: 'auto' }}
            onClick={() => setIgnoreModal({ open: true, suggested: record.useragent.split(' ')[0] })}
          >
            忽略
          </Button>
        );
      },
    },
  ];

  const eventColumns: ColumnsType<StatsEventItem> = [
    {
      title: '收到时间',
      dataIndex: 'at',
      key: 'at',
      width: 160,
      render: (value: string, record) => (
        // registry 的事件时间戳小数位不固定，原样放在悬停里，不做截断。
        <Tooltip title={`事件时间：${record.eventAt || '—'}`}>
          <span>{formatDateTime(value)}</span>
        </Tooltip>
      ),
    },
    {
      title: 'action',
      dataIndex: 'action',
      key: 'action',
      width: 90,
      render: (value: string) => (
        <Tag color={value === 'push' ? 'green' : value === 'pull' ? 'blue' : 'default'}>
          {value || '—'}
        </Tag>
      ),
    },
    { title: 'method', dataIndex: 'method', key: 'method', width: 90, render: monoOrDash },
    {
      title: 'mediaType',
      dataIndex: 'mediaType',
      key: 'mediaType',
      width: 210,
      /*
       * `ellipsis` 必须写在**列**上，不能只靠单元格里那个 `.ellipsis` 类。
       * 表格是 auto 布局（没有它 AntD 就不会切到 fixed），auto 布局下长且不可折行的
       * 内容会把列撑到自身宽度、把 `width` 当摆设 —— 结果是整张表横向滚动，
       * 最右边的「是否计入」被推出视野。实测：mediaType 声明 240 实际撑到 454。
       * `showTitle: false` 是因为下面已经有内容更全的自定义 Tooltip，不要再来一个原生的。
       */
      ellipsis: { showTitle: false },
      render: (value: string) => (
        <Tooltip title={value || '—'}>
          <span className="mono ellipsis" style={{ display: 'block' }}>
            {value || '—'}
          </span>
        </Tooltip>
      ),
    },
    {
      title: '仓库',
      dataIndex: 'repository',
      key: 'repository',
      ellipsis: { showTitle: false },
      render: (value: string) => (
        <Tooltip title={value || '—'}>
          <span className="ellipsis" style={{ display: 'block' }}>
            {value || '—'}
          </span>
        </Tooltip>
      ),
    },
    { title: 'tag', dataIndex: 'tag', key: 'tag', width: 130, render: monoOrDash },
    {
      title: '客户端',
      dataIndex: 'useragent',
      key: 'useragent',
      width: 210,
      ellipsis: { showTitle: false },
      /*
       * 排查"热度是不是被自动化进程刷高了"的关键一列：真人用 docker CLI，
       * 同步工具用 regclient / skopeo，User-Agent 一眼分得开。
       * 另外三个身份字段（来源 addr / host / actor）放在悬停里 —— 它们通常没有区分度
       * （端口映射下 addr 是网桥网关、未开认证时 actor 为空），占一整列不值当。
       */
      render: (value: string, record) => {
        const detail = [
          `User-Agent：${record.useragent || '—'}`,
          `来源：${record.addr || '—'}`,
          `Host：${record.host || '—'}`,
          `账号：${record.actor || '（未认证）'}`,
        ].join('\n');
        // 已经被某条规则命中的，就不再给入口 —— 重复加只会让人以为没生效。
        const already = isIgnoredUa(record.useragent);
        return (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
            <Tooltip title={<span style={{ whiteSpace: 'pre-line' }}>{detail}</span>}>
              <span className="mono ellipsis" style={{ flex: '1 1 auto', minWidth: 0 }}>
                {value || '—'}
              </span>
            </Tooltip>
            {value && !already ? (
              <Tooltip title="以后不再统计这个客户端的热度">
                <Button
                  type="link"
                  size="small"
                  style={{ flex: 'none', padding: 0, height: 'auto' }}
                  onClick={() => setIgnoreModal({ open: true, suggested: value.split(' ')[0] })}
                >
                  忽略
                </Button>
              </Tooltip>
            ) : null}
          </div>
        );
      },
    },
    {
      title: '是否计入',
      key: 'counted',
      width: 260,
      ellipsis: { showTitle: false },
      render: (_, record) =>
        record.counted ? (
          <Tag color="green">计入</Tag>
        ) : (
          /*
           * 用 Fragment 而不是 <Space>：Space 是 inline-flex，`flex-wrap: nowrap` 下长
           * reason 既不会折行也不会收缩，会把单元格撑出表格、逼出整表横向滚动
           * （实测 `IGNORED_USERAGENT:regclient/regsync` 撑出 80px）。
           * 普通行内流 + `.reason-text` 的 overflow-wrap 才能正常折行。
           */
          <>
            <Tag color="default">未计入</Tag>
            {/* 去重丢弃的事件 reason 仍是 OK，直接显示会让用户以为是误报。 */}
            <span className="reason-text" style={{ color: 'var(--color-text-3)' }}>
              {record.duplicate ? '重复投递，已按 event.id 去重' : readReason(record.reason)}
            </span>
          </>
        ),
    },
  ];

  const header = (
    <div className="page-header">
      <div>
        <h2 className="page-title">镜像热度</h2>
        {/* 只写"这页能做什么"。数字的统计口径（哪些事件计入、为什么）属于设计说明，
            见 docs/design.md §3.3。 */}
        <p className="page-subtitle">统计每个仓库与 tag 被推送、拉取的次数。</p>
      </div>
      <div className="page-actions">
        {/* v0.5.37.4：时间窗移到了左侧栏（视图状态归侧栏），页头只留「刷新」。 */}
        <Button
          icon={<ReloadOutlined />}
          loading={loading}
          onClick={() => setReloadKey((key) => key + 1)}
        >
          刷新
        </Button>
      </div>
    </div>
  );

  if (!config) {
    return (
      <div className="page">
        {header}
        {configFailure ? (
          /* v0.5.18（F2）：读配置失败原本会永远停在这一屏，没有任何出口。 */
          <LoadError
            title="服务配置加载失败，热度统计不可用"
            failure={configFailure}
            retrying={configLoading}
            onRetry={() => void reloadConfig()}
          />
        ) : (
          <div className="panel" style={{ padding: 16 }}>
            <Empty description="正在读取服务配置…" />
          </div>
        )}
      </div>
    );
  }

  /**
   * 「为什么没有热度数据」的分档说明。
   *
   * 注意 statsEnabled = 统计库可用 && 开关打开，因此**缺密钥并不会让 statsEnabled 变 false**
   * —— 那种情况会以 total === 0 的形式出现在已启用路径上。所以同一个解释要同时服务
   * 「未启用」和「已启用但窗口内没有数据」两处，避免只在其中一边给出正确原因。
   */
  const notice = statsNotice(config);

  // 未启用:仅在「确实有错误」(DB 坏/禁收事件)时显示告警 + 配置片段;无错就让
  // 页面空白显示 metric 0 —— v0.5.26 之前这里固定 alert + NotifyConfigSnippet
  // (外部 registry 配置),现在 Cairn 不接外部,这段保留也不合适,但用户只要求
  // 去掉「没事件」那条,本轮保守保留 NotifyConfigSnippet,留给后续单独清理。
  if (!config.statsEnabled) {
    return (
      <div className="page">
        {header}
        {notice ? (
          <Alert
            type={notice.type}
            showIcon
            message={notice.message}
            description={notice.description}
          />
        ) : null}
        {notice ? (
          <div className="panel" style={{ padding: 16 }}>
            <h3 className="stats-panel-title">外部 registry 需要这样配（可选）</h3>
            <NotifyConfigSnippet />
          </div>
        ) : null}
      </div>
    );
  }

  const empty = summary !== null && summary.total === 0;

  return (
    <div className="page">
      {header}

      {error?.message ? (
        <Alert
          type="warning"
          showIcon
          closable
          onClose={() => setError(null)}
          message={`部分热度数据未能加载：${error.message}`}
          description={
            <span>
              镜像列表与其它功能不受影响。若服务刚重启，稍后点「刷新」即可。
              {error.code ? `（错误分类：${error.code}）` : ''}
            </span>
          }
        />
      ) : null}

      <div className="metric-grid">
        <MetricCard
          icon={<ThunderboltOutlined />}
          label={`事件总数（${days} 天）`}
          value={summary?.total ?? 0}
        />
        <MetricCard
          icon={<CloudDownloadOutlined />}
          label="拉取次数"
          value={summary?.pull ?? 0}
        />
        <MetricCard
          icon={<CloudUploadOutlined />}
          iconColor="var(--color-success)"
          iconBackground="var(--color-fill-2)"
          label="推送次数"
          value={summary?.push ?? 0}
        />
        <MetricCard
          icon={<DatabaseOutlined />}
          iconColor="var(--color-text-3)"
          iconBackground="var(--color-fill-2)"
          label="有活动的仓库数"
          value={summary?.repositories ?? 0}
        />
      </div>

      {/* v0.5.26 起空态不再画任何提示：Cairn 只管理自带 registry 的热度,「事件=0」只表示
          当前窗口内没有 pulls,不需要用户去配 REGISTRY_NOTIFY_TOKEN / 排查外部 registry。
          真要排查走下面的「最近事件」面板与 KPI 自检。 */}

      {/* v0.7.37：按天趋势先于 Top 榜单 —— 先看「什么时候热」,再看「什么最热」,
          两个图互补的视觉顺序。Top 榜单从表格改成横向条形图（见 TopBarChart
          注释）：5~6 列的表格太宽撑出横向滚动条,条形图直接比条长,
          标签再长也不影响。 */}

      <div className="panel" style={{ padding: 16 }}>
        <h3 className="stats-panel-title">按天趋势（近 12 个月）</h3>
        <ContributionHeatmap
          points={points}
          days={HEATMAP_DAYS}
          retentionDays={config?.statsRetentionDays ?? null}
          since={config?.statsSince ?? null}
        />
      </div>

      {!empty ? (
        <div className="panel" style={{ padding: 16 }}>
          <div className="stats-panel-head">
            {/* v0.6.15 (UI-5): 标题从「Top 榜单」改成「Top 10 榜单」。
             *  硬上限 10 条 → 表头文案也要直接说清,不再让用户去「猜是不是漏看了」。
             *  v0.7.37: 表身从 antd Table 改为 TopBarChart,见组件注释。 */}
            <h3 className="stats-panel-title">Top 10 榜单</h3>
            <Segmented
              options={BY_OPTIONS}
              value={topBy}
              onChange={(value) => setTopBy(value as StatsTopBy)}
            />
          </div>
          <div hidden={loading && topItems.length === 0}>
            <TopBarChart items={topItems.slice(0, TOP_LIMIT)} />
          </div>
          {/*
           * `loading && topItems.length === 0` 才居中 spinner —— 切时间窗时
           * 已有旧数据,不需要渐隐过度。位置改在 chart 之后渲染（而不是
           * 覆盖），跟下面 client/events 表格的 PageLoading 模式一致。
           */}
          <PageLoading
            visible={loading && topItems.length === 0}
            tip="正在读取 Top 10 榜单…"
          />
        </div>
      ) : null}

      {/*
        放在「最近事件」**上面**：这一块才是"谁在打"的答案（不受 200 条窗口限制、
        重启也不丢），下面那个回答的是"最近发生了什么细节"。
      */}
      <div className="panel" style={{ padding: 16 }}>
        <div className="stats-panel-head">
          <h3 className="stats-panel-title">
            见过的客户端（
            {clientsDays === 'all' ? '全部时间' : `近 ${clientsDays} 天`}
            ）
          </h3>
          <Space size={12}>
            <Select
              size="small"
              value={clientsDays}
              style={{ width: 120 }}
              onChange={(v) => setClientsDays(v)}
              options={[
                { value: 'all', label: '全部时间' },
                { value: 7, label: '近 7 天' },
                { value: 30, label: '近 30 天' },
                { value: 90, label: '近 90 天' },
              ]}
            />
            <span style={{ fontSize: 12, color: 'var(--color-text-3)' }}>
              「计入 0」= 收到过但被规则排掉了；这一行不存在才是真的没来过
            </span>
          </Space>
        </div>
        {/*
           * v0.6.23: PageLoading 改成 position: absolute 覆盖在 panel 内
           * (解决 0.6.22 「拉址感」)。外层 wrap div 加 position: relative +
           * minHeight: 240px 给 spinner 区域。
           *
           * `loading && clients.length === 0` 是首屏场景;后续轮询走
           * antd Table 自带半透层。
           */}
          <div style={{ position: 'relative', minHeight: 240 }}>
          <div hidden={loading && clients.length === 0}>
          <Table<StatsClientItem>
            rowKey="useragent"
            size="small"
            columns={clientColumns}
            dataSource={clients}
            pagination={{
              size: 'small',
              hideOnSinglePage: true,
              defaultPageSize: 10,
              showTotal: (total) => `共 ${total} 个客户端`,
            }}
            locale={{ emptyText: <Empty description="这个时间窗内还没收到任何事件" /> }}
          />
          </div>
          </div>
          <PageLoading
            visible={loading && clients.length === 0}
            tip="正在读取客户端列表…"
          />
      </div>

      <Collapse
        items={[
          {
            key: 'events',
            label: (
              <Space size={8} wrap>
                <span>最近事件（排查用）</span>
                {/*
                  这张表**长得像历史记录**，其实只是内存里的排查缓存：不落盘、重启即清空。
                  不标出来的话，用户会拿它当历史用 —— 对着一段三五天前的窗口找"上周是谁在刷"，
                  找不到就以为"没发生过"。长期的那份是上面的「见过的客户端」。
                */}
                <span style={{ fontSize: 12, color: 'var(--color-text-4)' }}>
                  内存缓存 · 不落盘
                </span>
                <span style={{ fontSize: 12, color: 'var(--color-text-3)' }}>
                  计入 {eventTotals?.accepted ?? 0} / 未计入 {eventTotals?.rejected ?? 0}
                </span>
                {/*
                  服务端自身请求（它的 User-Agent 是 registry-manager/<版本>）单列一个数字。
                  不列出来的话，一次「重新扫描」就会按 tag 数量产生一批事件（实测 178 条），
                  把这张表冲成自己的噪音 —— 真正要查的东西反而看不见了。
                */}
                {eventTotals?.self ? (
                  <Tooltip title="服务端自己发往 registry 的读取请求（重新扫描时按 tag 读 manifest 与 image config，一次上百条）。它们永远不计入热度，纯噪音，所以折叠成一个数字。注意：服务端自己**计入热度**的那些（用「镜像拉取」搬进来的 manifest）照常列在下方。">
                    <span style={{ fontSize: 12, color: 'var(--color-text-4)' }}>
                      自身读取 {eventTotals.self} 条
                    </span>
                  </Tooltip>
                ) : null}
                {eventTotals?.ignored ? (
                  <Tooltip title="已被忽略规则排掉的事件：既不计入热度，也不再占这个 200 条的窗口（窗口实测只覆盖最近十几小时，该留给你还没处理过的客户端）。它们的账在上面「见过的客户端」里。">
                    <span style={{ fontSize: 12, color: 'var(--color-text-4)' }}>
                      已忽略折叠 {eventTotals.ignored} 条
                    </span>
                  </Tooltip>
                ) : null}
                {/*
                  把"忽略了哪些客户端"写在这里，是为了让配置**看得见**：
                  否则"规则生效了所以热度不涨"和"规则没读到所以热度不涨"长得一样。
                  被忽略的事件仍然留在下面这张表里（reason 写着 IGNORED_USERAGENT），
                  这样"被排掉了"和"事件根本没到"才分得清。
                */}
                {config?.statsIgnoreUseragents?.length ? (
                  <Tooltip title="这些客户端的事件不计入热度（REGISTRY_STATS_IGNORE_USERAGENTS）">
                    <Tag color="default" style={{ marginInlineEnd: 0 }}>
                      已忽略：{config.statsIgnoreUseragents.join('、')}
                    </Tag>
                  </Tooltip>
                ) : null}
              </Space>
            ),
            children: (
              <>
                {/*
                  展开后也要再说一遍，而且说全：标签上那四个字在折叠状态下够用，
                  真对着表格找东西时需要的却是"到底能往前看多久、重启会不会没"。
                  条数取服务端回显的 bufferSize，不在这里写死 200 —— 写死了改容量时
                  界面会继续说旧数字，而且不报错。
                */}
                <div style={{ fontSize: 12, color: 'var(--color-text-3)', marginBottom: 8 }}>
                  这张表只保留最近 {eventTotals?.bufferSize ?? 0} 条，存到本地 SQLite
                  的 event_log 表里 —— 服务重启不丢，但每 24h 由 retentionLoop
                  跑一次 EventLogEnforceLimit 把超过 {eventTotals?.bufferSize ?? 200}
                  条的最老事件清掉。
                </div>
                {/*
                 * v0.6.23: PageLoading 改成 position: absolute 覆盖在 Collapse
                 * panel 内(解决 0.6.22 「拉址感」)。外层 wrap div 加
                 * position: relative + minHeight: 200px 给 spinner 区域。
                 *
                 * `loading && events.length === 0` 才需要居中 spinner(后续
                 * 轮询走 antd Table 自带半透层)。
                 */}
                <div style={{ position: 'relative', minHeight: 200 }}>
                <div hidden={loading && events.length === 0}>
                <Table<StatsEventItem>
                  rowKey={(record) => `${record.at}-${record.id}`}
                  size="small"
                  columns={eventColumns}
                  dataSource={events}
                  scroll={{ x: 1000 }}
                  pagination={{
                    size: 'small',
                    defaultPageSize: 10,
                    pageSizeOptions: [10, 20, 50],
                    showSizeChanger: true,
                    showTotal: (total) => `共 ${total} 条`,
                  }}
                  locale={{ emptyText: <Empty description="还没收到任何事件" /> }}
                />
                </div>
                </div>
                <PageLoading
                  visible={loading && events.length === 0}
                  tip="正在读取最近事件…"
                />
              </>
            ),
          },
        ]}
      />

      <IgnoreRuleModal
        open={ignoreModal.open}
        suggested={ignoreModal.suggested}
        knownUseragents={knownUseragents}
        onCancel={() => setIgnoreModal({ open: false, suggested: '' })}
        onSaved={handleIgnoreSaved}
      />
    </div>
  );
}

/**
 * 「为什么没有热度数据」。按原因分档，因为处置方式完全不同：
 * 库坏了要找服务端看数据目录，开关关了要改环境变量，
 * 而一切就绪时可能只是真的没人用 —— 这种情况不再展示提示,因为 metric 卡片
 * 已经老老实实显示「0」,再插一条「还没收到事件」的解释只会让操作员误以为
 * 系统没在干活。Cairn 只负责自带的 registry 产生的 push / pull,不接外部
 * registry,所以「没配 NotifyToken / 别忘了加外部配置」这类提示已无意义。
 *
 * v0.5.26: 移除原本「一切就绪 + 窗口内没有事件」分支的 info 提示 —— 那是基于
 * 历史假设(cairn 还能收外部 registry 的事件),现在 Cairn 定位改成
 * 「自带 registry 专属」之后,「事件=0」就是「事件=0」,不再有「你是不是
 * 配错了」的延伸解释。
 */
function statsNotice(config: AppConfig): {
  type: 'warning';
  message: string;
  description: ReactNode;
} | null {
  const statsError = config.statsError;

  if (statsError) {
    return {
      type: 'warning',
      message: '热度统计不可用：统计库初始化失败',
      description: (
        <div>
          <div>{statsError.message}</div>
          <div style={{ marginTop: 4, color: 'var(--color-text-3)' }}>
            错误分类：<span className="mono">{statsError.code}</span>
            。这是服务端数据目录或磁盘的问题（与 registry 配置无关）；修复后重启服务即可开始统计。
          </div>
        </div>
      ),
    };
  }

  if (!config.allowRegistryEvents) {
    return {
      type: 'warning',
      message: '服务端已关闭热度事件接收',
      description: (
        <div>
          <div>
            服务端设置了 <span className="mono">REGISTRY_ALLOW_REGISTRY_EVENTS=false</span>
            ，registry 推来的事件会被直接拒绝（HTTP 503），因此不会有任何热度数据。
          </div>
          <div style={{ marginTop: 4, color: 'var(--color-text-3)' }}>
            需要热度就把这个变量改成 <span className="mono">true</span> 后重启服务。
          </div>
        </div>
      ),
    };
  }

  // v0.5.26 之前:这里返回 info「还没收到任何热度事件」+ 长篇延伸解释(怎么排查、
  // 怎么接外部 registry)。删除原因:Cairn 现在只管理自带 registry 的热度,
  // metric 卡片显示「事件总数 0」已是准确表达;插一条「你可能配错了」的延伸
  // 会让操作员误以为系统没在干活。
  return null;
}

/** 可复制的 registry notifications 配置片段 + 重启提醒。 */
function NotifyConfigSnippet() {
  return (
    <div>
      <div className="stats-code-head">
        <span style={{ color: 'var(--color-text-3)', fontSize: 12 }}>
          registry 容器的 config.yml（片段）
        </span>
        <TextCopyButton text={NOTIFY_CONFIG_YAML} />
      </div>
      <pre className="stats-code mono">{NOTIFY_CONFIG_YAML}</pre>
      <div style={{ marginTop: 8, color: 'var(--color-text-3)' }}>
        <strong>改完必须重启 registry 容器</strong> —— Distribution 没有配置热重载，
        不重启配置不会生效，也不会有任何报错。
      </div>
    </div>
  );
}

/**
 * 只带复制按钮的 Typography.Text。
 * `navigator.clipboard` 在非安全上下文不存在，AntD 内部已带 execCommand 回退。
 */
function TextCopyButton({ text }: { text: string }) {
  return (
    <Typography.Text
      copyable={{ text, tooltips: ['复制配置', '已复制'] }}
      style={{ color: 'var(--color-text-3)' }}
    />
  );
}

/** 空值统一显示成 —，避免空白单元格看起来像渲染失败。 */
function monoOrDash(value: string) {
  return <span className="mono">{value || '—'}</span>;
}

/**
 * 把服务端的 `reason` 翻成人话。
 *
 * 只有被排除的客户端需要翻译：原样显示是 `IGNORED_USERAGENT:regclient/regsync`，
 * 又长又难读，还会把单元格撑爆。**数据里保留完整的 reason**（排查时要 grep 它、
 * 接口也要能读），只在界面上换个说法 —— 命中的规则片段照旧带出来，
 * 否则用户看不出是哪条规则生效了。
 */
function readReason(reason: string) {
  const ignored = /^IGNORED_USERAGENT:(.+)$/.exec(reason ?? '');
  if (ignored) {
    return `已忽略客户端 ${ignored[1]}`;
  }
  return reason || '未知原因';
}
