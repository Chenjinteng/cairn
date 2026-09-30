import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Alert, Button, Checkbox, Empty, Input, Popconfirm, Segmented, Table, Tooltip, message } from 'antd';
import {
  ClockCircleOutlined,
  DatabaseOutlined,
  DeleteOutlined,
  HddOutlined,
  ReloadOutlined,
  TagsOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import {
  fetchInventory,
  fetchRepositoryStats,
  runGC,
} from '../api';
import ImageDetailDrawer from '../components/image-detail-drawer';
import MetricCard from '../components/metric-card';
import TableSkeleton from '../components/table-skeleton';
import type { SidebarGroup, SidebarSelection } from '../components/page-sidebar';
import { useAppConfig } from '../config-store';
import type {
  ApiResult,
  AppConfig,
  DeleteTagPayload,
  Inventory,
  RegistryRepository,
  StatsRepositoryStat,
  StatsWindow,
} from '../types';
import { formatBytes, formatDateTime } from '../utils';

interface Props {
  config: AppConfig | null;
  inventory: Inventory | null;
  onInventoryChange: (inventory: Inventory) => void;
  onGoSettings: () => void;
  /**
   * v0.5.37.4:从侧栏传入的当前选择（groupKey -> itemKey）。
   * 「仓库」组选中某命名空间 → 表格按名称第一段路径精确过滤，与搜索框叠加（AND）。
   */
  sidebarFilter: SidebarSelection;
  /** 把本页真实分组（含计数）上浮给 App，再统一下发给 PageSidebar。 */
  onPublishGroups: (groups: SidebarGroup[]) => void;
}

/** 热度时间窗，与「镜像热度」页保持同样的三档。 */
const STATS_WINDOW_OPTIONS: { label: string; value: StatsWindow }[] = [
  { label: '7 天', value: 7 },
  { label: '30 天', value: 30 },
  { label: '90 天', value: 90 },
];

export default function ImagesPage({
  config,
  inventory,
  onInventoryChange,
  onGoSettings,
  sidebarFilter,
  onPublishGroups,
}: Props) {
  /**
   * v0.5.18（F8）：loading 语义统一为「本轮清单请求在途」。首屏用 !inventory 做初值
   * 只为避免配置到手前先闪一帧空表；之后一律由 load() 自己开关，不再掺 inventory 条件。
   */
  const [loading, setLoading] = useState(!inventory);
  const [error, setError] = useState<ApiResult<unknown> | null>(null);
  /**
   * v0.5.18（F2/F6）：config 拿不到不影响镜像清单（清单来自 /api/inventory），
   * 但会让「运行 GC」「删除」这些按钮的可用性判断失真，所以单独提示一条。
   * 读取/缓存/单飞都在 config-store，这里只取失败态与重试入口。
   */
  const { failure: configFailure, loading: configLoading, reload: reloadConfig } = useAppConfig();
  /**
   * 表格的滚动容器。页面用 .page--fill 撑满视口，滚动只发生在这里 ——
   * 所以搜索框、时间窗、KPI 往下翻表格时不会被顶走。
   */
  const tableWrapRef = useRef<HTMLDivElement>(null);
  const [search, setSearch] = useState('');
  const [detailName, setDetailName] = useState<string | null>(null);
  /** 热度时间窗；与热度页的三档一致。 */
  const [statsDays, setStatsDays] = useState<StatsWindow>(30);
  /** 仓库名 → 窗口内热度。查不到 = 窗口内没有事件，界面显示「—」。 */
  const [heat, setHeat] = useState<Record<string, StatsRepositoryStat>>({});
  /**
   * v0.5.20：GC 弹窗里的「同时清理 0 tag 仓库」勾选。默认 false——勾上之后
   * 会删除整个仓库目录，比删 blob 危险得多，必须由操作员主动决定。GC 跑完
   * 还要按勾选状态决定是否刷新 inventory（删了空仓库之后清单会变短）。
   */
  const [cleanEmptyRepos, setCleanEmptyRepos] = useState(false);

  const statsEnabled = config?.statsEnabled === true;

  /**
   * 热度是锦上添花：单独拉、失败就保持空 map。
   * 绝不能因为热度接口挂了就让镜像列表变成错误页 —— 列表来自 /api/inventory。
   */
  const loadHeat = useCallback(async (windowDays: StatsWindow) => {
    const result = await fetchRepositoryStats(windowDays);
    if (result.success && result.data) {
      setHeat(result.data.items);
    }
  }, []);

  // v0.6.10：「刷新」=「重新扫描」—— 后端只有一个 inventory 端点,
  // 区别只在 UI 文案。两个按钮会让用户以为「重新扫描」会触发远端
  // registry 操作,实际啥也没做,只读本地 SQLite + 走 storage 拼 inventory。
  const load = useCallback(
    async () => {
      setLoading(true);
      setError(null);
      const result = await fetchInventory();
      if (result.success && result.data) {
        onInventoryChange(result.data);
      } else {
        setError(result);
      }
      setLoading(false);
    },
    // api 函数是模块级常量，不需要进依赖。
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [onInventoryChange]
  );

  useEffect(() => {
    if (!inventory) {
      // v0.6.10 fix: 之前是 `void load(false)` 带参数,改无参后这里漏改成
      // `void load;`(只读 load 函数引用,不调用),导致首屏永远不会触发
      // /api/inventory 请求,UI 永远停在 skeleton。
      void load();
    }
    // 首屏只拉一次；后续刷新由刷新按钮显式触发。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 配置到手后才知道热度可不可用；不可用就不发请求、也不加那两列。
  useEffect(() => {
    if (!statsEnabled) {
      setHeat({});
      return;
    }
    void loadHeat(statsDays);
  }, [statsEnabled, statsDays, loadHeat]);

  const heatOf = (name: string) => heat[name]?.events ?? 0;
  const lastActivityOf = (name: string) => heat[name]?.lastAt ?? null;

  /**
   * v0.5.37.4:侧栏「仓库」分组 —— 从真实 catalog 派生，不再写死。
   * 取每条仓库名的第一段路径当命名空间（`library/nginx` -> `library`），
   * badge 是该命名空间下的仓库数；「全部」的 badge 是仓库总数。
   * 从**全量** inventory 派生（不随搜索/侧栏过滤变化），否则过滤后侧栏自己会缩水，
   * 就没法从侧栏再切到别的命名空间了。
   */
  const repoGroups = useMemo<SidebarGroup[]>(() => {
    const repositories = inventory?.repositories ?? [];
    if (repositories.length === 0) return [];
    const counts = new Map<string, number>();
    for (const item of repositories) {
      const namespace = item.name.split('/')[0];
      counts.set(namespace, (counts.get(namespace) ?? 0) + 1);
    }
    const namespaces = [...counts.entries()].sort(
      (left, right) => right[1] - left[1] || left[0].localeCompare(right[0])
    );
    return [
      {
        key: 'repo',
        label: '仓库',
        items: [
          { key: 'all', label: '全部', badge: repositories.length },
          ...namespaces.map(([namespace, count]) => ({
            key: namespace,
            label: namespace,
            badge: count,
          })),
        ],
      },
    ];
  }, [inventory]);

  useEffect(() => {
    onPublishGroups(repoGroups);
  }, [onPublishGroups, repoGroups]);

  const rows = useMemo(() => {
    const keyword = search.trim().toLowerCase();
    // 「仓库」组选中命名空间后与搜索框叠加（AND）；点「全部」时 App 存的是 null。
    const namespace = sidebarFilter.repo ?? null;
    return (inventory?.repositories ?? []).filter((item) => {
      if (keyword && !item.name.toLowerCase().includes(keyword)) return false;
      if (namespace && item.name.split('/')[0] !== namespace) return false;
      return true;
    });
  }, [inventory, search, sidebarFilter]);

  const metrics = useMemo(() => {
    const repositories = rows;
    return {
      repositoryCount: repositories.length,
      tagCount: repositories.reduce((total, item) => total + item.tagCount, 0),
      totalSize: repositories.reduce((total, item) => total + item.totalSize, 0),
    };
  }, [rows]);

  const detailRepository = useMemo(
    () => inventory?.repositories.find((item) => item.name === detailName) ?? null,
    [inventory, detailName]
  );

  const handleDeleted = (payload: DeleteTagPayload) => {
    if (!inventory) {
      return;
    }
    const others = inventory.repositories.filter((item) => item.name !== payload.repository.name);
    onInventoryChange({
      ...inventory,
      repositories: [...others, payload.repository].sort((left, right) =>
        left.name.localeCompare(right.name)
      ),
    });
  };

  const columns: ColumnsType<RegistryRepository> = [
    {
      title: '仓库名',
      dataIndex: 'name',
      key: 'name',
      sorter: (left, right) => left.name.localeCompare(right.name),
      render: (value: string, record) => (
        <Tooltip title={value}>
          <button type="button" className="link-cell ellipsis" onClick={() => setDetailName(record.name)}>
            {value}
          </button>
        </Tooltip>
      ),
    },
    {
      title: 'Tag 数',
      dataIndex: 'tagCount',
      key: 'tagCount',
      width: 100,
      sorter: (left, right) => left.tagCount - right.tagCount,
    },
    {
      title: '镜像层合计',
      dataIndex: 'totalSize',
      key: 'totalSize',
      width: 150,
      defaultSortOrder: 'descend',
      sorter: (left, right) => left.totalSize - right.totalSize,
      render: (value: number) => formatBytes(value),
    },
    {
      title: '最新构建时间',
      key: 'latestBuildAt',
      width: 170,
      sorter: (left, right) => latestBuildAt(left).localeCompare(latestBuildAt(right)),
      render: (_, record) => formatDateTime(latestBuildAt(record) || null),
    },
    // 热度不可用时这两列整列不出现：两列全是「—」会被读成"确实没人用"。
    ...(statsEnabled
      ? ([
          {
            title: `热度（${statsDays} 天）`,
            key: 'heat',
            width: 130,
            sorter: (left: RegistryRepository, right: RegistryRepository) =>
              heatOf(left.name) - heatOf(right.name),
            // 没有热度的显示「—」而不是 0 —— 0 会和"确实没人用"混淆。
            render: (_: unknown, record: RegistryRepository) =>
              heatOf(record.name) > 0 ? `${heatOf(record.name)} 次` : '—',
          },
          {
            title: '最近活动',
            key: 'lastActivity',
            width: 170,
            sorter: (left: RegistryRepository, right: RegistryRepository) =>
              (lastActivityOf(left.name) ?? '').localeCompare(lastActivityOf(right.name) ?? ''),
            render: (_: unknown, record: RegistryRepository) =>
              formatDateTime(lastActivityOf(record.name)),
          },
        ] as ColumnsType<RegistryRepository>)
      : []),
    {
      title: '操作',
      key: 'actions',
      width: 100,
      fixed: 'right',
      // v0.5.29：操作员在仓库列表上「一键删整个仓库」不合理 —— 即使有 Popconfirm
      // 也防不住误操作,而且删除本身没意义(磁盘要 GC 才回收)。要清掉一个仓库,正确
      // 路径是:进详情 → 删完所有 tag → 回列表 → 运行 GC + 勾「也清理 0 tag 仓库」。
      render: (_, record) => (
        <Button type="link" size="small" onClick={() => setDetailName(record.name)}>
          详情
        </Button>
      ),
    },
  ];

  return (
    <div className="page page--fill">
      <div className="page-header">
        <div>
          <h2 className="page-title">镜像列表</h2>
          <p className="page-subtitle">浏览并管理 registry 中的镜像。</p>
        </div>
        <div className="page-actions">
          <Input.Search
            allowClear
            style={{ width: 240 }}
            placeholder="搜索仓库名"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
          />
          {statsEnabled ? (
            <Segmented
              options={STATS_WINDOW_OPTIONS}
              value={statsDays}
              onChange={(value) => setStatsDays(value as StatsWindow)}
            />
          ) : null}
          <Button
            type="primary"
            icon={<ReloadOutlined />}
            onClick={() => {
              void load();
              if (statsEnabled) {
                void loadHeat(statsDays);
              }
            }}
            loading={loading}
          >
            刷新
          </Button>
          {config?.allowDelete ? (
            // v0.5.24: 长 GC 解释从「? 图标的 Tooltip」搬到「运行 GC 按钮的 Tooltip」,
            // 配 placement=bottomLeft 让 tooltip 出现在按钮下方偏左 —— 避开按钮
            // 所在页面右上角的右边缘(原先 ? 图标 tooltip 用 placement=top 会往
            // 上飘、长文本直接被浏览器裁切)。
            //
            // Popconfirm 的 description 只留可交互的 checkbox(破坏性开关);
            // 长篇解释已迁到 Tooltip 里。
            <Tooltip
              title={
                <div>
                  GC = Garbage Collection。扫描并清理孤儿 blob（被废弃的上传会话、被解除引用的层），释放磁盘空间。
                  <br />
                  注意：删除 manifest 只是解除引用，真正的磁盘空间要 GC 才回收。
                  <br />
                  勾选弹窗里的「也清理 0 tag 仓库」会<strong>额外删除整个空仓库目录</strong>。
                </div>
              }
              placement="bottomLeft"
            >
              <Popconfirm
                title="确认运行 GC？"
                description={
                  <Checkbox
                    checked={cleanEmptyRepos}
                    onChange={(e) => setCleanEmptyRepos(e.target.checked)}
                    // 点击 checkbox 别冒泡到 Popconfirm 外层(否则会误关弹窗)
                    onClick={(e) => e.stopPropagation()}
                  >
                    也清理 0 tag 的仓库（会删除整个仓库目录,不可恢复）
                  </Checkbox>
                }
                okText="运行"
                cancelText="取消"
                onConfirm={async () => {
                  const hide = message.loading('正在执行 GC 扫描…', 0);
                  try {
                    const r = await runGC({ cleanEmptyRepos });
                    if (!r.success) {
                      message.error(`GC 失败：${r.message}`);
                      return;
                    }
                    const data = r.data ?? {
                      removedBlobs: 0,
                      freedBytes: 0,
                      removedEmptyRepos: [],
                      emptyRepoFreedBytes: 0,
                    };
                    const blobPart =
                      data.removedBlobs === 0
                        ? null
                        : `清理 ${data.removedBlobs} 个孤儿 blob,回收 ${(data.freedBytes / 1024 / 1024).toFixed(2)} MiB`;
                    const repoPart =
                      !cleanEmptyRepos || !data.removedEmptyRepos?.length
                        ? null
                        : `清空 ${data.removedEmptyRepos.length} 个空仓库(${formatRepoList(data.removedEmptyRepos)})`;
                    const head = 'GC 完成:';
                    const body = [blobPart, repoPart].filter(Boolean).join(';') ||
                      (cleanEmptyRepos ? '没有需要清理的孤儿 blob 或空仓库' : '没有需要清理的孤儿 blob');
                    message.success(head + body);
                    // v0.5.20: 删了空仓库之后镜像清单会变短,主动刷一次。
                    // 不在 cleanEmptyRepos=false 时刷,避免无谓的网络往返。
                    if (cleanEmptyRepos && data.removedEmptyRepos?.length) {
                      await load;
                    }
                  } catch (e) {
                    message.error(`GC 失败：${(e as Error).message ?? e}`);
                  } finally {
                    hide();
                  }
                }}
              >
                <Button icon={<DeleteOutlined />}>运行 GC</Button>
              </Popconfirm>
            </Tooltip>
          ) : null}
        </div>
      </div>

      {error ? (
        <Alert
          type="warning"
          showIcon
          message="无法获取镜像清单"
          description={
            <div>
              <div>{error.message}</div>
              <div style={{ marginTop: 4, color: 'var(--color-text-3)' }}>
                请确认 registry 地址可达，并在「设置」中检查配置。
                <Button type="link" size="small" onClick={onGoSettings} style={{ paddingInline: 4 }}>
                  去设置
                </Button>
              </div>
            </div>
          }
          action={
            <Button size="small" loading={loading} onClick={() => void load()}>
              重试
            </Button>
          }
        />
      ) : null}

      {configFailure && !config ? (
        /* 清单还能用，只有按钮可用性失真 —— 给一条提示 + 重试，不做整页错误态。 */
        <Alert
          type="warning"
          showIcon
          message="服务配置读取失败"
          description={
            <div>
              <div>{configFailure.message}</div>
              <div style={{ marginTop: 4, color: 'var(--color-text-3)' }}>
                {`错误分类：${configFailure.code}`}；忽略规则的开关状态可能显示不准。
              </div>
            </div>
          }
          action={
            <Button size="small" loading={configLoading} onClick={() => void reloadConfig()}>
              重试
            </Button>
          }
        />
      ) : null}

      {inventory && inventory.errorCount > 0 ? (
        <Alert
          type="warning"
          showIcon
          message={`有 ${inventory.errorCount} 个 tag 读取失败，已跳过`}
          description={
            <span>
              {inventory.errors
                .slice(0, 5)
                .map((item) => `${item.repository}${item.tag ? `:${item.tag}` : ''}`)
                .join('、')}
              {inventory.errorCount > inventory.errors.length
                ? ` 等 ${inventory.errorCount} 项`
                : ''}
            </span>
          }
        />
      ) : null}

      {inventory?.truncated ? (
        <Alert type="warning" showIcon message="仓库数量超过单次扫描上限，清单已截断。" />
      ) : null}

      <div className="metric-grid">
        <MetricCard icon={<HddOutlined />} label="仓库数" value={metrics.repositoryCount} />
        <MetricCard icon={<TagsOutlined />} label="Tag 数" value={metrics.tagCount} />
        <MetricCard
          icon={<DatabaseOutlined />}
          label="镜像层合计"
          value={formatBytes(metrics.totalSize)}
        />
        <MetricCard
          icon={<ClockCircleOutlined />}
          iconColor="var(--color-text-3)"
          iconBackground="var(--color-fill-2)"
          label="清单刷新时间"
          text
          value={formatDateTime(inventory?.refreshedAt ?? null, '尚未扫描')}
        />
      </div>

      <div className="panel">
        {/* 表格自己滚（表头粘住），页面不滚 —— 见 app.css 的 .page--fill。 */}
        <div className="table-scroll" ref={tableWrapRef}>
          {/* v0.5.41: loading 时改用 TableSkeleton 占位(列数跟实际表对齐),
              替换 antd Table 自带的居中 spinner —— 之前 spinner 让人以为「页面空了」。
              实际表没数据(rows 为空)时仍走 Empty 走文案的逻辑。 */}
          {loading ? (
            <TableSkeleton rows={10} columns={statsEnabled ? 6 : 5} title={false} description={false} />
          ) : (
            <Table<RegistryRepository>
              rowKey="name"
              size="middle"
              columns={columns}
              dataSource={rows}
              scroll={{ x: statsEnabled ? 1100 : 900 }}
              sticky={{ getContainer: () => tableWrapRef.current ?? window }}
              locale={{
                emptyText: (
                  <Empty
                    description={
                      inventory
                        ? '该 registry 没有匹配的镜像'
                        : '还没有清单，点击「刷新」从本地 storage 加载'
                    }
                  />
                ),
              }}
              pagination={{
                size: 'small',
                showSizeChanger: true,
                defaultPageSize: 20,
                pageSizeOptions: [10, 20, 50, 100],
                showTotal: (total) => `共 ${total} 个仓库`,
              }}
            />
          )}
        </div>
      </div>

      <ImageDetailDrawer
        open={Boolean(detailName)}
        host={config?.host ?? ''}
        repository={detailRepository}
        allowDelete={config?.allowDelete ?? false}
        onClose={() => setDetailName(null)}
        onDeleted={handleDeleted}
      />
    </div>
  );
}

/** 仓库的"最新构建时间"取所有 tag 构建时间的最大值；用于排序与展示。 */
function latestBuildAt(repository: RegistryRepository): string {
  return repository.tags.reduce((latest, item) => {
    if (!item.createdAt) {
      return latest;
    }
    return item.createdAt > latest ? item.createdAt : latest;
  }, '');
}

/**
 * v0.5.20: GC toast 把本次清空的仓库名列出来。>3 个截断成「前 3 + 等 N 个」，
 * 与 AGENTS.md 风格的 elsewhere (镜像清单顶部 error 列表) 一致。
 */
function formatRepoList(repos: string[]): string {
  if (repos.length <= 3) {
    return repos.join('、');
  }
  return `${repos.slice(0, 3).join('、')} 等 ${repos.length} 个`;
}
