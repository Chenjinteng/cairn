import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  App,
  Button,
  Empty,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import { ApiOutlined, DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import {
  fetchIgnoreRules,
  fetchInventory,
  fetchStatsEvents,
  probeRegistry,
  purgeHeat,
  removeIgnoreRule,
  updateConfig,
} from '../api';
import IgnoreRuleModal from '../components/ignore-rule-modal';
import LoadError from '../components/load-error';
import type { SidebarGroup, SidebarSelection } from '../components/page-sidebar';
import { useAppConfig } from '../config-store';
import type { ApiResult, AppConfig, IgnoreRules, Inventory } from '../types';
import { hostOnly, HOST_PORT_PATTERN, PRESET_SOURCES, stripUrlProtocol } from '../utils';

interface Props {
  config: AppConfig | null;
  onConfigChange: (config: AppConfig) => void;
  onInventoryChange: (inventory: Inventory) => void;
  /**
   * v0.5.37.4：设置页侧栏是**锚点导航**，这里没有可筛的数据，只为对齐 App 的统一
   * props 契约而收下，页面自身不读它（真正的跳转由 PageSidebar 按 DOM id 处理）。
   */
  sidebarFilter: SidebarSelection;
  /** v0.5.37.4：把本页侧栏分组上浮给 App，由 App 统一下发给 PageSidebar。 */
  onPublishGroups: (groups: SidebarGroup[]) => void;
}

/**
 * v0.5.14: 只读值组件 — 只显示当前值，不带编辑控件。
 * 编辑入口统一收在面板顶部的全局「编辑」按钮上，不再逐项给按钮。
 */
function ReadonlyValue({ value, mono }: { value: string; mono?: boolean }) {
  return (
    <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
      <span
        className={mono ? 'mono' : undefined}
        style={{
          color: 'var(--color-text-2, #666)',
          padding: '4px 11px',
          background: 'var(--color-fill-2, #f5f5f5)',
          border: '1px solid var(--color-border, #d9d9d9)',
          borderRadius: 6,
          fontSize: 14,
        }}
      >
        {value}
      </span>
    </div>
  );
}

export default function SettingsPage({
  config,
  onConfigChange,
  onInventoryChange,
  onPublishGroups,
}: Props) {
  const { message, modal } = App.useApp();
  /**
   * v0.5.18（F6/F7）：读取/缓存/单飞都在 config-store；本页只在 config 缺失时
   * 用「加载中 / 加载失败」替换整页表单。
   */
  const { failure: configFailure, loading: configLoading, reload: reloadConfig } = useAppConfig();
  const [probing, setProbing] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [purging, setPurging] = useState(false);
  const [ignoreRules, setIgnoreRules] = useState<IgnoreRules | null>(null);
  const [ignoreModalOpen, setIgnoreModalOpen] = useState(false);
  /** 预览素材：最近收到的客户端。进这一页时取一次即可。 */
  const [knownUseragents, setKnownUseragents] = useState<string[]>([]);
  const [notice, setNotice] = useState<ApiResult<unknown> | null>(null);
  // v0.5.1: 编辑中的 REGISTRY_URL 草稿；提交时 PATCH /api/config，服务器
  // 写 SQLite + 热替换 cfg.Mutable，新值对下一个入队的 pull job 立即生效。
  const [registryUrlDraft, setRegistryUrlDraft] = useState<string>('');

  // v0.5.2: drafts for every other editable field. Server-side keys mirror
  // config.MutableKeys so the PATCH payload is just {mutable: {key: val}}.
  const [registryNameDraft, setRegistryNameDraft] = useState<string>('');
  const [allowDeleteDraft, setAllowDeleteDraft] = useState<boolean>(true);
  const [allowPullDraft, setAllowPullDraft] = useState<boolean>(true);
  const [statsRetentionDraft, setStatsRetentionDraft] = useState<number>(365);
  // Platform allow-list applied to multi-arch image indexes on pull.
  // Empty array = "pull every platform" (the behaviour before the
  // allow-list existed); non-empty = only fetch children whose
  // OS/architecture matches. The server stores this as a CSV string.
  //
  // v0.6.19: 出厂默认从「空(全架构)」收紧到「linux/amd64」。
  // v0.6.33: chip 列表从 8 个选项砍到 2 个 —— x86 (linux/amd64) 与
  // arm64 (linux/arm64)。其余架构(arm/v7、386、ppc64le、s390x、
  // riscv64、windows)彻底移出 UI:产品口径只关心 x86 + arm64 两
  // 个主流目标,其它场景不在本期支持范围;真要拉这几个架构,在
  // 服务端 SQLite 的 `pull.platforms` 直接 PATCH CSV 仍可生效,
  // UI 不提供入口。
  //
  // 默认值不变 —— 仍只勾 amd64。要 arm64 的用户在 chip 上点一下
  // 保存即可,跟之前一样。
  //
  // 注意:storage 里既存的空值(老用户的「拉所有架构」配置)不受影响,
  // 这次只改「未填过的用户」初始值 —— 老用户的空 = 拉所有平台 不变,
  // 升级后第一次进设置页点保存才正式落到 X86-only。
  const DEFAULT_PULL_PLATFORMS = ['linux/amd64'];
  const [pullPlatformsDraft, setPullPlatformsDraft] = useState<string[]>(DEFAULT_PULL_PLATFORMS);
  // v0.5.48: 第三方拉取源（pull.known_hosts）。服务端存归一化后的
  // "<scheme>://host[:port]" CSV（保存时校验、小写、去重），UI 侧说数组。
  // 用途：拉取页镜像名智能解析认这些主机（含无点内网主机）+ 源地址
  // AutoComplete 候选。空 = 只认内置知名源。
  const [pullKnownHostsDraft, setPullKnownHostsDraft] = useState<string[]>([]);
  // v0.5.3: registry self-auth (Basic). Password is NEVER seeded from
  // config -- server deliberately doesn't echo it back, so we always
  // start blank; user typing = "set", blank on save = "clear override".
  const [registryUsernameDraft, setRegistryUsernameDraft] = useState<string>('');
  const [registryPasswordDraft, setRegistryPasswordDraft] = useState<string>('');
  const [savingBulk, setSavingBulk] = useState(false);
  // v0.5.9 hotfix 2 起：整页编辑 — 点顶部「编辑」按钮后整个面板变可输入,
  // 「保存所有修改」把所有变更一次性 PATCH,「取消」还原 draft + 退出。
  // v0.5.14 起：字段级「编辑」按钮全部移除,编辑入口只剩顶部这一个。
  const [editing, setEditing] = useState<boolean>(false);

  const beginEdit = () => setEditing(true);
  const cancelEdit = () => {
    setEditing(false);
    // v0.5.18（F7）：config 缺失时整页已早退，这里只是防御。
    if (!config) return;
    const m = config.mutable;
    // v0.5.28: 历史值可能还带 http:// 前缀,加载到编辑态时剥掉,完成一次性迁移。
    // v0.5.39: 跟 useEffect 一样的预填逻辑 —— 取消编辑也回到「badge 默认值」,
    // 而不是回到空字符串(避免「取消后再点编辑又回到自动填」的不一致)。
    // v0.5.40: 自动填用 hostOnly 剥掉 r.Host 里的端口(那是操作员访问路径)。
    const savedUrl = stripUrlProtocol(m.registryUrl);
    const autoFilled = !savedUrl && config.url ? hostOnly(stripUrlProtocol(config.url)) : savedUrl;
    setRegistryUrlDraft(autoFilled);
    setRegistryNameDraft(m.registryName ?? '');
    setAllowDeleteDraft(m.allowDelete ?? false);
    setAllowPullDraft(m.allowPull ?? false);
    setStatsRetentionDraft(m.statsRetentionDays ?? 365);
    setPullPlatformsDraft(
      (m.pullPlatforms ?? '').split(',').map((s) => s.trim()).filter(Boolean)
    );
    setPullKnownHostsDraft(
      (m.pullKnownHosts ?? '').split(',').map((s) => s.trim()).filter(Boolean)
    );
    setRegistryUsernameDraft('');
    setRegistryPasswordDraft('');
  };

  /** 一次性 PATCH 所有变更字段 — 复用 v0.5.9 之前的 handleSaveBulk diff 逻辑。 */
  const handleSaveAll = async () => {
    // v0.5.18（F7）：config 缺失时整页已早退，这里只是防御。
    if (!config) return;
    const m = config.mutable;
    const patch: Record<string, string> = {};
    if (registryUrlDraft.trim() !== (m.registryUrl ?? '')) {
      const v = registryUrlDraft.trim();
      // v0.5.42: 字段只接受裸 host(无端口)—— 端口由 HOST_PORT env 决定。
      //   协议(http:// / https://)留给 v0.6.0 统一切换;这里也拒绝。
      if (v !== '' && !HOST_PORT_PATTERN.test(v)) {
        message.error('地址格式:只接受 IP 或域名(如 192.168.1.10 或 registry.example.com)。不要带端口或协议 —— 端口由 docker-compose 的 HOST_PORT env 决定,改完 docker compose up -d');
        return;
      }
      patch['registry.url'] = v;
    }
    if (registryNameDraft.trim() !== (m.registryName ?? '')) {
      patch['registry.name'] = registryNameDraft.trim();
    }
    if (allowDeleteDraft !== (m.allowDelete ?? false)) {
      patch['allow.delete'] = allowDeleteDraft ? 'true' : 'false';
    }
    if (allowPullDraft !== (m.allowPull ?? false)) {
      patch['allow.pull'] = allowPullDraft ? 'true' : 'false';
    }
    if (statsRetentionDraft !== (m.statsRetentionDays ?? 365)) {
      if (!Number.isFinite(statsRetentionDraft) || statsRetentionDraft < 1) {
        message.error('热度保留天数必须 >= 1');
        return;
      }
      patch['stats.retention.days'] = String(statsRetentionDraft);
    }
    const currentChips = (m.pullPlatforms ?? '')
      .split(',').map((s) => s.trim()).filter(Boolean).sort();
    const draftChips = [...pullPlatformsDraft].sort();
    if (
      currentChips.length !== draftChips.length ||
      currentChips.some((c, i) => c !== draftChips[i])
    ) {
      patch['pull.platforms'] = pullPlatformsDraft.join(',');
    }
    // v0.5.48: 第三方拉取源。原样提交用户输入,后端 normalizeHostCSV 负责
    // 归一化(补协议/小写/去重)与校验(路径/凭据/非法 scheme → 400)。
    const currentKnownHosts = (m.pullKnownHosts ?? '')
      .split(',').map((s) => s.trim()).filter(Boolean).sort();
    const draftKnownHosts = pullKnownHostsDraft
      .map((s) => s.trim()).filter(Boolean).sort();
    if (
      currentKnownHosts.length !== draftKnownHosts.length ||
      currentKnownHosts.some((c, i) => c !== draftKnownHosts[i])
    ) {
      patch['pull.known_hosts'] = pullKnownHostsDraft
        .map((s) => s.trim()).filter(Boolean).join(',');
    }
    if (registryUsernameDraft.trim() !== (m.registryUsername ?? '')) {
      patch['registry.username'] = registryUsernameDraft.trim();
    }
    if (registryPasswordDraft !== '') {
      patch['registry.password'] = registryPasswordDraft;
    } else if (m.usingAuth) {
      // 清空密码框 = 显式关闭认证。
      patch['registry.password'] = '';
    }
    if (Object.keys(patch).length === 0) {
      message.info('没有变更');
      setEditing(false);
      return;
    }
    setSavingBulk(true);
    try {
      const r = await updateConfig({ mutable: patch });
      if (r.success && r.data) {
        onConfigChange(r.data);
        message.success(`已保存 ${Object.keys(patch).length} 项设置`);
        setEditing(false);
        setRegistryPasswordDraft('');
      } else {
        message.error(r.message ?? '保存失败');
      }
    } catch (e) {
      message.error(`保存失败:${(e as Error).message ?? e}`);
    } finally {
      setSavingBulk(false);
    }
  };


  /**
   * v0.6.15 (UI-3): 「测试连接」与「刷新清单」两条用户操作的反馈统一走
   * message toast,与 0.6.14 之后建立的原则保持一致。
   *
   * 旧行为:
   *   - handleProbe:成功只 push 顶部 `<Alert>`(绿色 success 级),不弹 toast
   *   - handleRefresh:成功**同时** push Alert + 右上 toast,两条反馈同屏并存
   * 0.6.14 已确立「弹窗内嵌 Alert 改成 message toast」,但本页是页面级按钮,
   * 旧 Alert 没改,导致同一页同时出现两种提示载体。
   *
   * 新行为:两种操作的成功 / 失败都走 message channel,顶部不再留 `<Alert>`
   * 反馈条。0.6.14 的边界划分(「页面级 Alert 保留 load failure / vault 不可用」)
   * 不变 —— 那是页面状态,本条只处理**操作反馈**。
   */
  const handleProbe = async () => {
    setProbing(true);
    try {
      const result = await probeRegistry();
      if (result.success && result.data) {
        message.success(
          `连接成功（API ${result.data.apiVersion ?? 'registry/2.0'}）`,
        );
      } else {
        message.error(result.message ?? '连接失败');
      }
    } finally {
      setProbing(false);
    }
  };

  const handleRefresh = async () => {
    setRefreshing(true);
    try {
      const result = await fetchInventory();
      if (result.success && result.data) {
        onInventoryChange(result.data);
        message.success(`清单刷新完成，用时 ${result.data.durationMs} ms`);
      } else {
        message.error(result.message ?? '清单刷新失败');
      }
    } finally {
      setRefreshing(false);
    }
  };

  const statsEnabled = config?.statsEnabled === true;

  // 规则与预览素材各取一次。热度不可用时服务端会回空结构，不用单独降级。
  useEffect(() => {
    // v0.5.18（F7）：config 缺失时整页已早退，这里只是防御。
    if (!config) return;
    const m = config.mutable;
    // v0.5.39: 未配置时表单也跟着 badge 自动填 —— 之前「badge 有 IP / 表单是
    // 空」观感很怪,改成:m.registryUrl 为空时,用 config.url(后端 r.Host 兜底
    // 出来的 http://host:port)剥掉协议作为草稿默认值。这样 badge 与表单视觉一致,
    // 用户「看到什么就改什么」。用户点 Save 且未改动 → 草稿 == 空 → 不写盘;
    // 用户改后再 Save → 落盘新值,之后草稿来自 mutable.registryUrl(不再自动填)。
    //
    // v0.5.40: 自动填时只取 host、剥端口。config.url 的端口是当前请求的 r.Host
    // 端口(操作员访问路径,可能经反代/隧道),不是「对外规范地址」的端口。
    // v0.5.42: 校验只接受 IP / 域名(见 utils.ts 的 HOST_PORT_PATTERN),
    // 端口统一由 docker-compose 配,这里自动填出来的值不带端口。
    const savedUrl = stripUrlProtocol(m.registryUrl);
    const autoFilled = !savedUrl && config.url ? hostOnly(stripUrlProtocol(config.url)) : savedUrl;
    setRegistryUrlDraft(autoFilled);
    setRegistryNameDraft(m.registryName ?? '');
    setAllowDeleteDraft(m.allowDelete ?? false);
    setAllowPullDraft(m.allowPull ?? false);
    setStatsRetentionDraft(m.statsRetentionDays ?? 365);
    // Server stores the allow-list as CSV under mutable.pullPlatforms;
    // split it back into the UI's array form. Empty CSV → empty array
    // ("all platforms").
    setPullPlatformsDraft(
      (m.pullPlatforms ?? '')
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean),
    );
    setPullKnownHostsDraft(
      (m.pullKnownHosts ?? '')
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean),
    );
    setRegistryUsernameDraft(m.registryUsername ?? '');
    // Password: intentionally blank (server doesn't echo stored value).
  }, [config]);



  useEffect(() => {
    if (!statsEnabled) {
      return;
    }
    void (async () => {
      const [rules, events] = await Promise.all([fetchIgnoreRules(), fetchStatsEvents(50)]);
      if (rules.success && rules.data) {
        setIgnoreRules(rules.data);
      }
      if (events.success && events.data) {
        setKnownUseragents([
          ...new Set(events.data.items.map((event) => event.useragent).filter(Boolean)),
        ]);
      }
    })();
  }, [statsEnabled]);

  /** 规则表：环境变量来的标出来、不给删；界面加的可以删。 */
  const ruleRows = useMemo(() => {
    const env = (ignoreRules?.env ?? []).map((rule) => ({ key: `env:${rule}`, rule, source: 'env' as const }));
    const panel = (ignoreRules?.panel ?? []).map((rule) => ({ key: `panel:${rule}`, rule, source: 'panel' as const }));
    return [...env, ...panel];
  }, [ignoreRules]);

  /** 生效规则同步回顶层配置，热度页标题上的「已忽略：…」才不会滞后。 */
  const applyRules = (rules: IgnoreRules) => {
    setIgnoreRules(rules);
    if (config) {
      onConfigChange({ ...config, statsIgnoreUseragents: rules.effective });
    }
  };

  const handleRemoveRule = async (rule: string) => {
    const result = await removeIgnoreRule(rule);
    if (result.success && result.data) {
      message.success(result.message || '已删除规则');
      applyRules(result.data);
    } else {
      setNotice(result);
    }
  };

  const ruleColumns: ColumnsType<{ key: string; rule: string; source: 'env' | 'panel' }> = [
    {
      title: '规则片段',
      dataIndex: 'rule',
      key: 'rule',
      ellipsis: { showTitle: false },
      render: (value: string) => (
        <Tooltip title={value}>
          <span className="mono ellipsis" style={{ display: 'block' }}>
            {value}
          </span>
        </Tooltip>
      ),
    },
    {
      title: '来源',
      dataIndex: 'source',
      key: 'source',
      width: 150,
      render: () => <Tag style={{ marginInlineEnd: 0 }}>界面</Tag>,
    },
    {
      title: '操作',
      key: 'actions',
      width: 90,
      render: (_, record) =>
        record.source === 'panel' ? (
          <Button
            type="link"
            size="small"
            danger
            style={{ padding: 0, height: 'auto' }}
            onClick={() => void handleRemoveRule(record.rule)}
          >
            删除
          </Button>
        ) : (
          // 说清楚为什么这里没有删除按钮，否则会被当成 bug。
          <span style={{ color: 'var(--color-text-4)', fontSize: 12 }}>不可在此删除</span>
        ),
    },
  ];

  const handlePurgeHeat = async () => {
    setPurging(true);
    setNotice(null);
    try {
      const result = await purgeHeat();
      if (result.success) {
        message.success(result.message || '已清空热度数据');
      } else {
        setNotice(result);
      }
    } finally {
      setPurging(false);
    }
  };

  /**
   * v0.5.37.4：设置页侧栏 = **页内锚点导航**（点击平滑滚动 + 随滚动高亮）。
   *
   * 这一页没有可"筛选"的数据：唯一一张表是热度忽略规则，条目数取决于规则数，
   * 拿它当过滤维度没有意义（筛完还是同一批字段，只是少几行）。所以侧栏放的是
   * 四个区块的跳转 —— 表单有 8 个字段，想回头找「允许拉取」得从底滚回中段。
   *
   * config 未到手时整页是「加载中 / 加载失败」，锚点目标根本不存在，
   * 下发空组让侧栏显示空态，而不是一排点了没反应的按钮。
   */
  const sidebarGroups = useMemo<SidebarGroup[]>(
    () =>
      config
        ? [
            {
              key: 'settings-nav',
              label: '快速跳转',
              mode: 'anchor',
              items: [
                { key: 'settings-connection', label: '仓库连接' },
                { key: 'settings-switches', label: '功能开关' },
                { key: 'settings-platforms', label: '拉取镜像的架构' },
                { key: 'settings-heat', label: '热度记录' },
              ],
            },
          ]
        : [],
    [config],
  );

  useEffect(() => {
    onPublishGroups(sidebarGroups);
  }, [onPublishGroups, sidebarGroups]);

  /**
   * v0.5.18（F7）：页头与提示条提前算好，供早退分支与主分支共用。
   * 两个按钮（测试连接 / 刷新清单）不依赖 config，所以早退分支里也保留。
   */
  const header = (
    <div className="page-header">
      <div>
        <h2 className="page-title">设置</h2>
        <p className="page-subtitle">当前管理的镜像仓库、连接状态与清单缓存。</p>
      </div>
      <div className="page-actions">
        <Button icon={<ApiOutlined />} loading={probing} onClick={() => void handleProbe()}>
          测试连接
        </Button>
        <Button type="primary" icon={<ReloadOutlined />} loading={refreshing} onClick={() => void handleRefresh()}>
          刷新清单
        </Button>
      </div>
    </div>
  );

  /*
    只在有文案时渲染。成功路径的 message 是空串（扫描结果由顶部 toast 和下方
    「清单状态」展示），无条件渲染会得到一个没有内容的空绿框。
  */
  const noticeAlert = notice?.message ? (
    <Alert
      type={notice.success ? 'success' : 'warning'}
      showIcon
      closable
      onClose={() => setNotice(null)}
      message={notice.message}
    />
  ) : null;

  // v0.5.18（F7）：配置没到手就整页只给「加载中 / 加载失败」，
  // 不再让几十个字段各自读 config?.mutable.* 落成半残界面。
  if (!config) {
    return (
      <div className="page">
        {header}
        {noticeAlert}
        {configFailure ? (
          <LoadError
            title="服务配置加载失败，设置项无法显示"
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

  return (
    <div className="page">
      {header}

      {noticeAlert}

      <div className="panel" style={{ padding: 16 }}>
        {/* 全局操作条（v0.5.14 起是唯一编辑入口）。点「编辑」-> 整页进入编辑模式(所有字段切换为 input),点「保存所有修改」一次性 PATCH 所有变更,「取消」还原 draft + 退出编辑态。 */}
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 16 }}>
          {editing ? (
            <>
              <Button type="primary" loading={savingBulk} onClick={() => void handleSaveAll()}>
                保存所有修改
              </Button>
              <Button onClick={cancelEdit} disabled={savingBulk}>
                取消
              </Button>
              <span style={{ color: 'var(--color-text-3)', fontSize: 12 }}>所有改动一起保存</span>
            </>
          ) : (
            <>
              <Button type="primary" icon={<EditOutlined />} onClick={beginEdit}>
                编辑
              </Button>
              <span style={{ color: 'var(--color-text-3)', fontSize: 12 }}>
                点「编辑」后下面所有参数一起改，改完一次保存
              </span>
            </>
          )}
        </div>

        <Form layout="vertical" size="middle" colon={false}>
          {/* v0.5.37.4：侧栏「仓库连接」锚点 —— 地址 / 端口 / 认证 / 展示名是一件事：
              这个仓库对外长什么样、怎么访问。 */}
          <div id="settings-connection" className="settings-anchor">
            {/* 仓库地址 */}
            <Form.Item
              label={<span>仓库地址</span>}
              extra={
                <>
                  {/* v0.5.42: 精简 —— 一句话说清用途,不再展开「端口/对外/容器内」细节。
                    端口由 docker-compose 的 HOST_PORT 决定,UI 在「监听端口」字段单独显示。 */}
                  供 docker login / docker push / docker pull 使用的对外地址(仅 IP 或域名)。
                </>
              }
            >
              {editing ? (
                <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                  {/* v0.5.28: addonBefore 把当前协议 (http) 显式画出来,用户不会被「我刚填的为啥报错」困扰;
                      v0.6.0 加 https 切换时,这个 badge 改成 Select。
                      v0.5.39: 草稿默认从 badge 预填(见 useEffect) —— 这里只管受控显示。 */}
                  <Input
                    className="mono"
                    value={registryUrlDraft}
                    onChange={(e) => setRegistryUrlDraft(e.target.value)}
                    disabled={savingBulk}
                    allowClear
                    addonBefore={<Tag color="cyan" bordered={false}>http://</Tag>}
                    placeholder="registry.example.com"
                    style={{ maxWidth: 560 }}
                  />
                </div>
              ) : (
                // v0.5.40: 只读视图也剥掉端口 —— 端口是操作员访问路径的端口,
                // 不是对外规范地址的端口(参见下面 help 文字)。
                <ReadonlyValue
                  value={
                    (() => {
                      const saved = stripUrlProtocol(config?.mutable.registryUrl ?? '');
                      if (saved) return `http://${saved}`;
                      const auto = hostOnly(stripUrlProtocol(config?.url ?? ''));
                      return auto
                        ? `http://${auto}（跟随访问地址,未保存）`
                        : '(空 — pull 任务回退到 Docker Hub)';
                    })()
                  }
                  mono
                />
              )}
            </Form.Item>

            {/* v0.5.40: 同时显示「容器内 / 宿主机」两个端口 —— 改端口要走 docker-compose.yml
                的 HOST_PORT + docker compose up -d 重建容器,UI 不暴露修改入口
                (改了容器内监听但不改 docker 端口映射,用户视角实际无效)。
                容器内 = PORT(env 固定 8787);宿主机 = HOST_PORT(env,docker-compose
                把 .env 的 ${HOST_PORT:-8787} 传进来)。两者相等时合并成一个。 */}
            <Form.Item
              label={<span>监听端口</span>}
              extra={
                config?.port
                  ? config.hostPort && config.hostPort !== config.port
                    ? `容器内 cairn 进程监听 ${config.port};宿主机侧对外端口 ${config.hostPort}(来自 docker-compose 的 HOST_PORT env)。改宿主机端口要重启容器(docker compose up -d)。`
                    : `容器内 cairn 进程监听 ${config.port};宿主机没显式传 HOST_PORT,默认与容器内一致。`
                  : '读取中…'
              }
            >
              <ReadonlyValue
                value={
                  config?.port
                    ? config.hostPort && config.hostPort !== config.port
                      ? `${config.port}（容器内） / ${config.hostPort}（宿主机）`
                      : String(config.port)
                    : '--'
                }
                mono
              />
            </Form.Item>

            {/* Registry 认证 */}
            <Form.Item
              label={<span>Registry 认证</span>}
              extra={
                editing
                  ? '输入新用户名 / 密码覆盖；密码不回显；保存后立即生效，无需重启。'
                  : config?.mutable.usingAuth
                    ? '当前已开启 Basic 认证；客户端需要先 docker login 才能 push/pull。'
                    : '留空 = 关闭认证（任何人可访问）。配了之后客户端需要 docker login。'
              }
            >
              {editing ? (
                // editing=true: 同时显示用户名 + 密码两个 input，整体保存时
                // 一起 PATCH(username 改了就发,密码留空表示不动)。
                <Space wrap>
                  <Input
                    placeholder="username"
                    value={registryUsernameDraft}
                    onChange={(e) => setRegistryUsernameDraft(e.target.value)}
                    style={{ maxWidth: 220 }}
                  />
                  <Input.Password
                    placeholder="新密码（输入即覆盖；留空 = 不动；清空输入 = 关闭认证）"
                    value={registryPasswordDraft}
                    onChange={(e) => setRegistryPasswordDraft(e.target.value)}
                    style={{ maxWidth: 420 }}
                  />
                </Space>
              ) : (
                <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                  <ReadonlyValue
                    value={
                      config?.mutable.usingAuth
                        ? '已开启（用户名 ' + (config.mutable.registryUsername || '?') + '）'
                        : '关闭（任何人都可访问）'
                    }
                  />
                </div>
              )}
            </Form.Item>

            {/* v0.5.53: extra 加 "浏览器标签页 title" —— 顶上 brand 写死的
                "Cairn" 是产品名,操作员多 tab 时要靠这个区分"内网离线镜像源" /
                "测试环境"。 */}
            <Form.Item
              label={<span>展示名称</span>}
              extra="顶部 / 设置页显示名,也是浏览器标签页 title"
            >
              {editing ? (
                <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                  <Input
                    value={registryNameDraft}
                    onChange={(e) => setRegistryNameDraft(e.target.value)}
                    placeholder="内网离线镜像源"
                    style={{ maxWidth: 420 }}
                  />
                </div>
              ) : (
                <ReadonlyValue
                  value={config?.mutable.registryName || '镜像仓库'}
                />
              )}
            </Form.Item>
          </div>

          {/* v0.5.37.4：侧栏「功能开关」锚点 —— 两个 allow.* 语义开关，保存即生效。 */}
          <div id="settings-switches" className="settings-anchor">
            {/* 允许删除 */}
            <Form.Item
              label={<span>允许删除</span>}
              extra="关闭后所有删除端点（仓库 / manifest-by-digest / GC）返回 403"
            >
              {editing ? (
                <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                  <Switch
                    checked={allowDeleteDraft}
                    onChange={setAllowDeleteDraft}
                    checkedChildren="启用"
                    unCheckedChildren="只读"
                  />
                </div>
              ) : (
                <ReadonlyValue
                  value={config?.mutable.allowDelete ? '启用' : '只读（关闭）'}
                />
              )}
            </Form.Item>

            {/* 允许拉取 */}
            <Form.Item
              label={<span>允许拉取</span>}
              extra="关闭后 /api/pull/* 写入端点拒绝"
            >
              {editing ? (
                <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                  <Switch
                    checked={allowPullDraft}
                    onChange={setAllowPullDraft}
                    checkedChildren="启用"
                    unCheckedChildren="禁用"
                  />
                </div>
              ) : (
                <ReadonlyValue
                  value={config?.mutable.allowPull ? '启用' : '禁用'}
                />
              )}
            </Form.Item>
          </div>

          {/* v0.5.37.4：侧栏「拉取平台」锚点。之前它排在「热度保留天数」之后，
              跟「多架构镜像怎么拉」的开关分散在两处；挪到开关组后面更顺。
              v0.6.19：标题从「拉取平台白名单」→「拉取镜像的架构」+ 默认改 X86。 */}
          <div id="settings-platforms" className="settings-anchor">
            {/* 拉取镜像的架构 —— 决定 cairn 拉取 multi-arch 镜像时只下哪些架构的 blobs。
                v0.6.19 之前叫「拉取平台白名单」(功能等价但术语偏后端),
                v0.6.19 改成更直白的「拉取镜像的架构」+ 默认勾上 X86。 */}
            <Form.Item
              label={<span>拉取镜像的架构</span>}
              extra={
                editing
                  ? '勾选目标架构；保存后对下一个 pull 任务立即生效。X86 默认勾上;真要内网 K8s 节点用的 arm64 镜像,在 chip 列表里点一下就行,不需要改代码。'
                  : pullPlatformsDraft.length === 0
                    ? '当前未限制(等于拉所有架构)。常见原因:从未保存过设置页、storage 是老版本默认值;在「编辑」状态下保存一次即可生效。'
                    : `当前限定：${pullPlatformsDraft.join(', ')}。其它架构的镜像不会下下来 —— x86 主机上要拉 arm64 镜像,在这里勾上「linux/arm64」保存,下次 pull 任务就会一起把 arm64 的 blobs 下下来。`
              }
            >
              {editing ? (
                <div>
                  <Space wrap>
                    {[
                      { id: 'linux/amd64', label: 'linux/amd64 (x86_64)' },
                      { id: 'linux/arm64', label: 'linux/arm64 (aarch64)' },
                    ].map((opt) => {
                      const on = pullPlatformsDraft.includes(opt.id);
                      return (
                        <Tag.CheckableTag
                          key={opt.id}
                          checked={on}
                          onChange={(checked) => {
                            setPullPlatformsDraft((prev) => {
                              const set = new Set(prev);
                              if (checked) set.add(opt.id);
                              else set.delete(opt.id);
                              return Array.from(set);
                            });
                          }}
                        >
                          {opt.label}
                        </Tag.CheckableTag>
                      );
                    })}
                  </Space>
                  <div style={{ marginTop: 12, display: 'flex', gap: 8 }}>
                    {pullPlatformsDraft.length > 0 ? (
                      <Button type="link" onClick={() => setPullPlatformsDraft(['linux/amd64'])}>
                        重置为仅 X86
                      </Button>
                    ) : null}
                  </div>
                </div>
              ) : (
                <ReadonlyValue
                  value={
                    pullPlatformsDraft.length === 0
                      ? '未限制（拉取所有架构 — 旧版本默认值）'
                      : pullPlatformsDraft.join(', ')
                  }
                />
              )}
            </Form.Item>

            {/* v0.5.48：第三方拉取源。让 cairn 在 pull 时知道哪些 host 走匿名 token(不发送
                Basic Auth 头)。匿名源如果不加进来,镜像名前缀识别不到时拉取会 401。
                v0.6.19：标题从「第三方拉取源」→「其它匿名的第三方源」+ extra 收紧到只讲匿名场景。
                已知公网匿名源已内置在 utils.ts KNOWN_HOSTS,这里只追加未内置的:
                比如 nvcr.io(NVIDIA NGC)、docker.elastic.co、内网 HTTP registry(也是匿名场景)等。
                需要账号密码的私有 registry 走「凭据管理」。 */}
            <Form.Item
              label={<span>其它匿名的第三方源</span>}
              extra={
                editing
                  ? '输入 host[:port] 或完整 URL 后回车;也可从下拉直接选匿名公网源。裸主机默认按 https 识别(端口非 443/8443/5000 时按 http)。这里加的都是**匿名可访问**的源 —— pull 时不发送 Basic Auth。需要账号密码的私有 registry 不在这里加,改去「凭据管理」配。'
                  : pullKnownHostsDraft.length === 0
                    ? '未配置。仅识别内置匿名源(Docker Hub / quay.io / ghcr.io / gcr.io / registry.k8s.io / mcr.microsoft.com / public.ecr.aws / registry.access.redhat.com);这些够覆盖大多数匿名镜像源,真要 nvcr.io / docker.elastic.co 之类再加。'
                    : `已配置 ${pullKnownHostsDraft.length} 个匿名源,镜像名带这些主机前缀时 cairn 不发送 Basic Auth。`
              }
            >
              {editing ? (
                <Select
                  mode="tags"
                  style={{ width: '100%' }}
                  value={pullKnownHostsDraft}
                  onChange={(vals: string[]) =>
                    setPullKnownHostsDraft(
                      vals.map((v) => v.trim()).filter(Boolean)
                    )
                  }
                  options={PRESET_SOURCES}
                  tokenSeparators={[',']}
                  placeholder="如 nvcr.io、k8s.gcr.io、docker.elastic.co;回车添加"
                />
              ) : (
                <ReadonlyValue
                  value={
                    pullKnownHostsDraft.length === 0
                      ? '未配置（仅内置匿名源）'
                      : pullKnownHostsDraft.join(', ')
                  }
                  mono
                />
              )}
            </Form.Item>
          </div>

          {/* v0.5.37.4：侧栏「热度记录」锚点 —— 保留天数在这里，忽略规则表与
              「清空热度数据」紧接在下方信息条之后。 */}
          <div id="settings-heat" className="settings-anchor">
            {/* 热度保留天数 */}
            <Form.Item
              label={<span>热度保留天数</span>}
              extra="超过的天数会被 /api/stats/heat 自动清掉"
            >
              {editing ? (
                <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                  <InputNumber
                    min={1}
                    max={3650}
                    value={statsRetentionDraft}
                    onChange={(v) => setStatsRetentionDraft(v ?? 365)}
                    style={{ width: 180 }}
                  />
                </div>
              ) : (
                <ReadonlyValue
                  value={`${config?.mutable.statsRetentionDays ?? 365} 天`}
                />
              )}
            </Form.Item>
          </div>

        </Form>
      </div>

      <Alert
        type="info"
        showIcon
        message="改这些参数不用碰环境变量"
        description={
          <div>
            <div>
              仓库地址、认证、功能开关、拉取平台与热度保留天数都存在本机 SQLite 里 —— 在
              上面的面板里点「编辑」直接改，点「保存所有修改」即刻生效，不用改环境变量、
              也不用重启容器。
            </div>
            <div style={{ marginTop: 4, color: 'var(--color-text-3)' }}>
              仍然要走容器的只有两件事：对外端口映射（<span className="mono">docker-compose.yml</span>
              的 <span className="mono">HOST_PORT</span>，改完 <span className="mono">docker compose up -d</span>
              重建容器）与数据目录位置（<span className="mono">HOST_DATA_DIR</span>）。
            </div>
          </div>
        }
      />

      {/*
        规则管理放这里，但**添加的主要入口在热度页那一行上** —— 要排掉某个客户端时人正看着
        那条 UA，不该被赶到设置页来手动粘。这里负责"看全量 + 删 + 标出来源"。
      */}
      {statsEnabled ? (
        <div className="panel" style={{ padding: 16 }}>
          <div className="stats-panel-head">
            <h3 className="stats-panel-title">热度忽略规则</h3>
            <Button size="small" icon={<PlusOutlined />} onClick={() => setIgnoreModalOpen(true)}>
              添加规则
            </Button>
          </div>
          <p style={{ margin: '0 0 12px', color: 'var(--color-text-3)', fontSize: 13 }}>
            命中的客户端不计入热度（子串匹配、忽略大小写）。加规则不用重启 ——
            嫌麻烦的话，热度页「最近事件」每一行的「忽略」是最快的入口。
          </p>
          <Table
            rowKey="key"
            size="small"
            columns={ruleColumns}
            dataSource={ruleRows}
            pagination={false}
            locale={{
              emptyText: <Empty description="还没有规则：所有客户端的事件都会计入热度" />,
            }}
          />
        </div>
      ) : null}

      {config?.statsEnabled ? (
        <Alert
          type="info"
          showIcon
          message="热度数据可以从头重计"
          description={
            <div>
              <div>
                热度按天聚合在本地数据库里，保留 {config.statsRetentionDays} 天
                {config.statsSince ? `（最早一天 ${config.statsSince}）` : ''}。 如果统计口径改过
                —— 例如发现某个自动化进程（镜像同步工具）也在按点扫全量、把热度刷了上去 ——
                可以把它清空、从现在重新累计。
              </div>
              <div style={{ marginTop: 4, color: 'var(--color-text-3)' }}>
                会清除热度聚合、幂等去重记录与「见过的客户端」清单（后者的计入数与热度同源，
                留着会自相矛盾），<strong>拉取历史不受影响</strong>。此操作不可撤销。
              </div>
              <Button
                danger
                size="small"
                icon={<DeleteOutlined />}
                loading={purging}
                style={{ marginTop: 8 }}
                onClick={() =>
                  modal.confirm({
                    title: '清空全部热度数据？',
                    content:
                      '已统计的热度会全部归零，从现在重新累计。「见过的客户端」清单也会一起清掉（它会在几小时内被重新填回来）。拉取历史不受影响。此操作不可撤销。',
                    okText: '清空',
                    okButtonProps: { danger: true },
                    cancelText: '取消',
                    onOk: handlePurgeHeat,
                  })
                }
              >
                清空热度数据
              </Button>
            </div>
          }
        />
      ) : null}

      <Alert
        type={config?.allowDelete ? 'warning' : 'info'}
        showIcon
        message={config?.allowDelete ? '删除已启用，操作不可撤销' : '当前为只读模式'}
        description={
          <div>
            {config?.allowDelete ? (
              <>
                <div>
                  删除按 digest 生效，会移除 manifest，该镜像随即无法再被拉取。删除前页面会列出同一 digest
                  下的全部 tag。
                </div>
                <div style={{ marginTop: 4 }}>
                  若 registry 侧未开启删除，请求会被拒绝，页面会给出对应提示。此外，删除 manifest 只是解除引用，
                  磁盘空间要运行 <span className="mono">registry garbage-collect</span> 才会真正回收。
                </div>
                <div style={{ marginTop: 4, color: 'var(--color-text-3)' }}>
                  想完全关掉破坏性操作：把上面的「允许删除」开关切到「只读」并保存，立即生效，无需重启。
                </div>
              </>
            ) : (
              <div>
                「允许删除」当前为关闭，删除入口已隐藏，所有删除请求都会被拒绝。要重新放开：
                回到上面的面板点「编辑」，把开关切到「启用」再保存。
              </div>
            )}
          </div>
        }
      />

      <IgnoreRuleModal
        open={ignoreModalOpen}
        knownUseragents={knownUseragents}
        onCancel={() => setIgnoreModalOpen(false)}
        onSaved={(rules) => {
          setIgnoreModalOpen(false);
          applyRules(rules);
        }}
      />
    </div>
  );
}
