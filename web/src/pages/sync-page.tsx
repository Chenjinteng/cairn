/**
 * v0.6.5: 镜像同步（cairn↔cairn）页面。
 *
 * 列任务表 + 新建/编辑 Modal + 立即运行 + 历史查看 Modal。后端路由为
 * /api/sync（CRUD）+ /api/sync/{id}/run（**异步受理**,返 202）+ /api/sync/{id}/runs（历史）
 * + /api/sync/test（探测远端 cairn 连通 + 认证状态）。
 *
 * 复杂度说明：v0.6.0 只支持「手动 + Basic auth + include 过滤」,v0.6.5 新增「测试连接」,
 * v0.7.0 修 SYNC-1~4——
 *   - 选错方向时镜像会从对端被覆盖/反覆盖,UI 上 direction 走 Radio 而非下拉,
 *     减少误操作（pull 是「我拉对端」,push 是「我推对端」,含义相反但都是英文短词,
 *     单字面下拉很容易选反）。
 *   - 【SYNC-3】远端凭据不再手填,改成三档：**匿名 / 引用凭据 / 保留内嵌凭据**。
 *     「引用凭据」从「凭据管理」里挑一条（以后改密码只动凭据,任务不用改）;
 *     「保留内嵌凭据」只在编辑**老任务**（有 remoteUsername 且无凭据引用）时出现,
 *     免得编辑一次就被迫重建凭据。remotePassword 后端用 json:"-" 屏蔽——UI 永远拿不到
 *     明文,编辑留空 = 后端保留旧值（详见 sync_handlers.go UpdateTask）。
 *   - 【SYNC-1/4】「立即运行」变成**异步受理**：后端 TryLock + 落 running 行后立刻返
 *     202,执行跑在后台 goroutine（不再挂在请求上,所以不会被前端 10s 超时中止）。
 *     按钮可用性以 `task.lastRunStatus` 为准——**刷新页面后仍在跑的任务按钮依然是灰的**;
 *     有 running 任务时本页 3s 轮询一次列表,跑完自动解锁。前端置灰 + 后端 409 双保险。
 *   - 「测试连接」按钮（v0.6.5 新增）走 POST /api/sync/test——填错 URL / 凭据时,
 *     提交前就能看到「远端不可达 / 401 缺凭据 / 凭据错 / OK」四档分类,
 *     避免「Save 后才看到第一行 sync run 失败」的长反馈环。
 *   - 历史 Modal 按 task 懒加载,不在主列表预取——避免一屏打满请求。
 */
import { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Alert,
  App as AntdApp,
  Button,
  Empty,
  Form,
  Input,
  Modal,
  Popconfirm,
  Radio,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import type { FormInstance } from 'antd';
import {
  ApiOutlined,
  CheckCircleOutlined,
  ClockCircleOutlined,
  CloseCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  ExclamationCircleOutlined,
  HistoryOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  SwapOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import {
  createSyncTask,
  deleteSyncTask,
  listCredentials,
  listSyncRuns,
  listSyncTasks,
  runSyncTask,
  testSyncConnection,
  updateSyncTask,
} from '../api';
import type {
  ApiResult,
  Credential,
  SyncDirection,
  SyncProbeAuthStatus,
  SyncProbeResult,
  SyncRun,
  SyncRunStatus,
  SyncTask,
  SyncTaskInput,
} from '../types';
import type { SidebarGroup, SidebarItem, SidebarSelection } from '../components/page-sidebar';
import TableSkeleton from '../components/table-skeleton';

interface Props {
  /**
   * 侧栏分组状态（v0.5.37.4 起每页一组）。sync 这一页只用 direction 作为分组。
   * 父组件传过来的是当前页的 selection，本组件读 + 写。
   */
  sidebarFilter: SidebarSelection;
  onPublishGroups: (groups: SidebarGroup[]) => void;
}

/**
 * v0.7.0（SYNC-3）：远端凭据三档。
 *   anonymous  — 对端没配 auth,引擎不发 Authorization header
 *   credential — 引用「凭据管理」库里的一条凭据（推荐）
 *   inline     — 沿用任务里原有的内嵌用户名密码（只在编辑老任务时出现,新建不提供）
 */
type CredMode = 'anonymous' | 'credential' | 'inline';

interface FormValues {
  name: string;
  direction: SyncDirection;
  remoteUrl: string;
  /** v0.7.0：credMode === 'credential' 时选中的凭据库 id。 */
  remoteCredentialId?: string;
  /** 远端 cairn 的 Basic-auth 用户名（只在 inline 档渲染）。 */
  remoteUsername?: string;
  /** 远端 cairn 的 Basic-auth 密码（只在 inline 档渲染）。编辑时可空 = 保留旧值（后端处理）。 */
  remotePassword?: string;
  include: string;
  enabled: boolean;
}

/** 短相对时间：「3 分钟前」「刚刚」「昨天」—— 比 antd 默认的绝对时间更易扫。 */
function formatRelative(iso: string | undefined): string {
  if (!iso) return '—';
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return '—';
  const diff = Date.now() - then;
  if (diff < 60_000) return '刚刚';
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  return `${Math.floor(diff / 86_400_000)} 天前`;
}

/** 一次运行的时长（毫秒 → 「1.2s」「45ms」）。 */
function formatDurationMs(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

/** run.status → antd Tag 的颜色。表 + 历史 Modal 共用。 */
function statusTagColor(status: SyncRunStatus): string {
  switch (status) {
    case 'success': return 'green';
    case 'partial': return 'gold';
    case 'failed':  return 'red';
    case 'running': return 'blue';
    default:        return 'default';
  }
}

/** run.status → 中文短标签。 */
function statusLabel(status: SyncRunStatus): string {
  switch (status) {
    case 'success': return '成功';
    case 'partial': return '部分失败';
    case 'failed':  return '失败';
    case 'running': return '运行中';
    default:        return status;
  }
}

/** direction → 中文。pull 是「我拉对端」,push 是「我推对端」—— 文案按操作者视角。 */
function directionLabel(d: SyncDirection): string {
  return d === 'pull' ? '拉（Pull）' : '推（Push）';
}

/**
 * 任务表 + 表头操作 + 历史查看 Modal。Modal 拆两个：
 *  1) 任务编辑（create / edit 共用）
 *  2) 历史查看（按 task 懒拉取 sync_runs）
 */
export default function SyncPage({ sidebarFilter, onPublishGroups }: Props) {
  const { message, modal } = AntdApp.useApp();
  const [tasks, setTasks] = useState<SyncTask[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ApiResult<unknown> | null>(null);

  /** 编辑/新建 Modal：editing == null → 新建；非空 → 编辑（token 字段空表示保留旧值）。 */
  const [editing, setEditing] = useState<SyncTask | null>(null);
  const [modalOpen, setModalOpen] = useState(false);
  const [form] = Form.useForm<FormValues>();
  const [submitting, setSubmitting] = useState(false);

  /** 历史 Modal：按 task.id 懒加载 runs。 */
  const [historyTask, setHistoryTask] = useState<SyncTask | null>(null);
  const [historyRuns, setHistoryRuns] = useState<SyncRun[]>([]);
  const [historyLoading, setHistoryLoading] = useState(false);

  /** 立即运行中的 task id（按钮 spinner）。 */
  const [runningId, setRunningId] = useState<number | null>(null);

  /**
   * 「测试连接」结果（v0.6.5 新增）。null = 没测过 / 改了表单字段被自动清空；
   * 非空 = 后端最后一次探测结果,顶部 Alert 渲染。改表单字段不清,让操作员看到
   * 上一次结果对照现在的输入——改完「远端 URL」再点「测试连接」覆盖。
   */
  const [probe, setProbe] = useState<SyncProbeResult | null>(null);
  /** 「测试连接」进行中——按钮 loading + 顶部 Alert 收起。 */
  const [testing, setTesting] = useState(false);

  /**
   * v0.7.0（SYNC-3）：Modal 里的远端凭据档位。放本地 state 而不是 Form 字段——
   * 条件渲染要读它,而 `Form.useWatch` 在 destroyOnClose 的 Modal 上有挂载时序坑;
   * 本地 state 是稳定的真值来源（submit 直接读,不再从 validateFields 里拿）。
   */
  const [credMode, setCredMode] = useState<CredMode>('anonymous');
  /** v0.7.0（SYNC-3）：凭据库列表（Modal 打开时拉一次;凭据是低频数据,不轮询）。 */
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [credLoading, setCredLoading] = useState(false);

  const refresh = useCallback(async () => {
    const result = await listSyncTasks();
    if (result.success && result.data) {
      setTasks(result.data);
      setError(null);
    } else {
      setError(result);
    }
    setLoading(false);
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  /**
   * v0.7.0（SYNC-1/4）：有任务在后台跑时每 3s 刷一次列表,把 running 追成终态
   * （跑完自动解锁「立即运行」+ 出新状态）。**没有 running 任务时不建定时器**——
   * 空闲页不该每 3s 打一次 /api/sync。
   *
   * 判定源是后端返回的 `lastRunStatus`（列表查询里的子查询,以 DB 为准）,不是本地
   * setState 的残留——所以**刷新页面后依然认得「还在跑」**。
   */
  const hasRunning = tasks.some((t) => t.lastRunStatus === 'running');
  useEffect(() => {
    if (!hasRunning) return;
    const timer = window.setInterval(() => {
      void refresh();
    }, 3000);
    return () => window.clearInterval(timer);
  }, [hasRunning, refresh]);

  /**
   * v0.7.0（SYNC-3）：拉凭据库列表。失败只提示不阻断——用户仍可切「匿名」档保存,
   * 不该因为凭据库读不到就把整条新建流程卡死。
   */
  const loadCredentials = async () => {
    setCredLoading(true);
    try {
      const result = await listCredentials();
      if (result.success && result.data) {
        setCredentials(result.data);
      } else {
        message.error(`加载凭据列表失败：${result.message}`);
      }
    } finally {
      setCredLoading(false);
    }
  };

  /**
   * v0.5.37.4 风格：侧栏只展示「过滤」,不显示业务操作。sync 这页唯一有意义的过滤是
   * direction（pull / push / 全部）—— 操作员跑完一批 push 任务想看下一批 pull 时,
   * 左侧一点就筛掉 hidden 的。
   */
  const sidebarGroups = useMemo<SidebarGroup[]>(() => {
    if (tasks.length === 0) return [];
    const pulls = tasks.filter((t) => t.direction === 'pull').length;
    const pushes = tasks.length - pulls;
    const items: SidebarItem[] = [
      { key: 'all', label: '全部', badge: tasks.length },
      { key: 'pull', label: '拉（Pull）', badge: pulls },
      { key: 'push', label: '推（Push）', badge: pushes },
    ];
    return [{ key: 'direction', label: '方向', items }];
  }, [tasks]);

  useEffect(() => {
    onPublishGroups(sidebarGroups);
  }, [onPublishGroups, sidebarGroups]);

  /** 表格数据 = 全量任务按侧栏「direction」过滤。 */
  const visibleTasks = useMemo(() => {
    const d = sidebarFilter.direction ?? null;
    if (!d || d === 'all') return tasks;
    return tasks.filter((t) => t.direction === d);
  }, [tasks, sidebarFilter]);

  // ── 编辑 / 新建 ──────────────────────────────────────────────────

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    form.setFieldsValue({
      direction: 'pull',
      include: '',
      enabled: true,
    });
    setCredMode('anonymous');  // v0.7.0（SYNC-3）：新建默认匿名,要认证就选「引用凭据」
    setProbe(null);  // 新建时清掉上次探测结果——避免「A 任务的探测结果留在 B 任务 Modal 上」
    setModalOpen(true);
    void loadCredentials();  // 让「引用凭据」档的选择框一开就有数据
  };

  const openEdit = (task: SyncTask) => {
    setEditing(task);
    /**
     * v0.7.0（SYNC-3）：按任务现状推断档位,**不强行**把老任务迁到凭据引用——
     *   remoteCredentialId 非空 → credential（已经在用凭据库）
     *   内联用户名非空          → inline（0.7.0 之前建的,保留原内嵌凭据）
     *   都空                    → anonymous
     */
    setCredMode(
      task.remoteCredentialId ? 'credential' : task.remoteUsername ? 'inline' : 'anonymous',
    );
    form.setFieldsValue({
      name: task.name,
      direction: task.direction,
      remoteUrl: task.remoteUrl,
      /** inline 档预填（用户名不敏感）;其它档留着也无妨,提交时按档清空。 */
      remoteUsername: task.remoteUsername,
      remoteCredentialId: task.remoteCredentialId ?? undefined,
      /** 编辑时密码留空——后端看到空字符串会保留旧值。UI 不应该假装知道旧密码。 */
      remotePassword: '',
      include: task.include,
      enabled: task.enabled,
    });
    setProbe(null);  // 编辑同上——避免上一个任务的探测残留
    setModalOpen(true);
    void loadCredentials();
  };

  const closeModal = () => {
    setModalOpen(false);
    setEditing(null);
    form.resetFields();
  };

  const submit = async () => {
    let values: FormValues;
    try {
      values = await form.validateFields();
    } catch {
      return; // antd form 已内的错误提示
    }
    /**
     * v0.6.6 hotfix:把 setSubmitting(true) + input 构造 + if/else 全包在同一个
     * try/finally 里。原来的写法 setSubmitting(true) 在 try 外面,一旦 .trim()
     * 抛 TypeError,finally 永远不会执行,保存按钮一直转圈「卡着」。
     *
     * 同时三个 string 字段全做 ?? '' ——`remoteUsername` 是匿名模式的核心场景
     * (0.6.4 起 username/password 都空 = 匿名),openCreate 不初始化该字段,
     * antd Form.getFieldsValue() 会回 undefined;不 null-safe 一调用就崩。
     * name / remoteUrl 理论上 required 校验保证非空,但防御性写一下也不亏。
     */
    setSubmitting(true);
    try {
      /**
       * v0.7.0（SYNC-3）：按档位组装**互斥**的三态。后端 Validate 会把「引用 + 内联」
       * 判为冲突、把「用户名密码只填一个」判为不完整,所以这里必须只发一档。
       * 后端合并语义（sync_handlers.go UpdateTask）：引用非空 → 清内联;内联任一非空 →
       * 清引用;三者全空 → 匿名。
       */
      const input: SyncTaskInput = {
        name: (values.name ?? '').trim(),
        direction: values.direction,
        remoteUrl: (values.remoteUrl ?? '').trim(),
        remoteUsername: credMode === 'inline' ? (values.remoteUsername ?? '').trim() : '',
        remotePassword: credMode === 'inline' ? (values.remotePassword ?? '') : '',
        remoteCredentialId: credMode === 'credential' ? (values.remoteCredentialId ?? '') : '',
        include: values.include ?? '',
        enabled: values.enabled,
      };
      const result = editing
        ? await updateSyncTask(editing.id, input)
        : await createSyncTask(input);
      if (result.success) {
        message.success(editing ? '已更新' : '已创建');
        closeModal();
        await refresh();
      } else if (!formError(form, result)) {
        // v0.7.0：字段级映射没兜住时必须给全局提示,否则「点了保存什么都没发生」。
        message.error(`保存失败：${result.message}`);
      }
    } finally {
      setSubmitting(false);
    }
  };

  // ── 删除 ─────────────────────────────────────────────────────────

  const handleDelete = (task: SyncTask) => {
    modal.confirm({
      title: `删除任务 "${task.name}"？`,
      content: '任务的历史运行记录会一起删除（外键 CASCADE），无法恢复。',
      okText: '删 除',
      okButtonProps: { danger: true },
      cancelText: '取 消',
      onOk: async () => {
        const result = await deleteSyncTask(task.id);
        if (result.success) {
          message.success('已删除');
          await refresh();
        } else {
          message.error(`删除失败：${result.message}`);
        }
      },
    });
  };

  // ── 立即运行 ─────────────────────────────────────────────────────

  const handleRun = async (task: SyncTask) => {
    setRunningId(task.id);
    try {
      const result = await runSyncTask(task.id);
      if (result.success && result.data) {
        /**
         * v0.7.0（SYNC-1/4）：run 接口改成**异步受理**——后端 TryLock + 落 running 行
         * 后立刻返 202,真正的同步跑在后台 goroutine（不再挂在请求上,所以不会被前端
         * 10s 超时中止）。这里只提示「已受理」;终态由上面的列表轮询追出来
         * （lastRunStatus 从 running 变终态时自动刷新 + 解锁按钮）。
         */
        message.success('已受理，同步在后台执行中');
        await refresh();
      } else if (result.code === 'CONFLICT') {
        // SYNC-4：后端 TryLock 失败 = 这个任务在后台还在跑,别重复发起。
        message.warning('该任务正在后台同步中，等本次跑完再试');
        await refresh();
      } else {
        message.error(`运行失败：${result.message}`);
      }
    } finally {
      setRunningId(null);
    }
  };

  // ── 历史 ─────────────────────────────────────────────────────────

  const openHistory = async (task: SyncTask) => {
    setHistoryTask(task);
    setHistoryRuns([]);
    setHistoryLoading(true);
    const result = await listSyncRuns(task.id, 50);
    if (result.success && result.data) {
      setHistoryRuns(result.data);
    } else {
      message.error(`加载历史失败：${result.message}`);
    }
    setHistoryLoading(false);
  };

  const closeHistory = () => {
    setHistoryTask(null);
    setHistoryRuns([]);
  };

  // ── 表格列 ───────────────────────────────────────────────────────

  const columns: ColumnsType<SyncTask> = [
    {
      title: '名称',
      dataIndex: 'name',
      key: 'name',
      render: (name: string, row) => (
        <Space size={4} align="center">
          <SwapOutlined />
          <span style={{ fontWeight: 500 }}>{name}</span>
          {/* v0.7.0（SYNC-1/4）：以 DB 为准的「运行中」标记——刷新页面后依然在。 */}
          {row.lastRunStatus === 'running' && (
            <Tag color="processing" icon={<ClockCircleOutlined />}>运行中</Tag>
          )}
          {!row.enabled && <Tag color="default">已停用</Tag>}
        </Space>
      ),
    },
    {
      title: '方向',
      dataIndex: 'direction',
      key: 'direction',
      width: 100,
      render: (d: SyncDirection) => (
        <Tag color={d === 'pull' ? 'blue' : 'purple'}>{directionLabel(d)}</Tag>
      ),
    },
    {
      title: '远端 URL',
      dataIndex: 'remoteUrl',
      key: 'remoteUrl',
      ellipsis: true,
      render: (url: string) => (
        <Tooltip title={url}>
          <span className="mono" style={{ fontSize: 12 }}>{url}</span>
        </Tooltip>
      ),
    },
    {
      title: 'Include',
      dataIndex: 'include',
      key: 'include',
      width: 140,
      ellipsis: true,
      render: (inc: string) => inc ? (
        <Tooltip title={inc}>
          <code style={{ fontSize: 12 }}>{inc.split('\n')[0]}{inc.includes('\n') ? ' …' : ''}</code>
        </Tooltip>
      ) : <span style={{ color: '#999' }}>全部</span>,
    },
    {
      title: '创建时间',
      dataIndex: 'createdAt',
      key: 'createdAt',
      width: 110,
      render: (iso: string) => (
        <Tooltip title={iso}>
          <span style={{ fontSize: 12, color: '#666' }}>{formatRelative(iso)}</span>
        </Tooltip>
      ),
    },
    {
      title: '操作',
      key: 'actions',
      width: 280,
      render: (_, task) => {
        /** v0.7.0（SYNC-1/4）：后台还在跑 → 按钮置灰,防重复发起（后端 409 兜底）。 */
        const running = task.lastRunStatus === 'running';
        return (
        <Space size={4}>
          <Tooltip
            title={
              running
                ? '该任务正在后台同步中，跑完才能再次发起'
                : '立即运行（异步受理，后台执行）'
            }
          >
            {/*
              v0.7.0（SYNC-4）：disabled 的 Button 自身不派发 mouseenter,Tooltip 会失效——
              必须包一层 <span>,「为什么点不了」才显示得出来。
            */}
            <span>
              <Button
                size="small"
                type="text"
                icon={<PlayCircleOutlined />}
                loading={runningId === task.id}
                disabled={running}
                onClick={() => void handleRun(task)}
              >
                运行
              </Button>
            </span>
          </Tooltip>
          <Button size="small" type="text" icon={<EditOutlined />} onClick={() => openEdit(task)}>
            编辑
          </Button>
          <Button size="small" type="text" icon={<HistoryOutlined />} onClick={() => void openHistory(task)}>
            历史
          </Button>
          <Popconfirm
            title={`删除任务 "${task.name}"？`}
            okText="删 除"
            okButtonProps={{ danger: true }}
            cancelText="取 消"
            onConfirm={() => handleDelete(task)}
          >
            <Button size="small" type="text" danger icon={<DeleteOutlined />}>
              删除
            </Button>
          </Popconfirm>
        </Space>
        );
      },
    },
  ];

  // ── 历史列 ───────────────────────────────────────────────────────

  const historyColumns: ColumnsType<SyncRun> = [
    {
      title: '开始时间',
      dataIndex: 'startedAt',
      key: 'startedAt',
      width: 170,
      render: (iso: string, run) => (
        <Tooltip title={iso}>
          <Space direction="vertical" size={0}>
            <span>{formatRelative(iso)}</span>
            {run.finishedAt && (() => {
              const ms = new Date(run.finishedAt).getTime() - new Date(run.startedAt).getTime();
              return <span style={{ fontSize: 11, color: '#999' }}>耗时 {formatDurationMs(ms)}</span>;
            })()}
          </Space>
        </Tooltip>
      ),
    },
    {
      title: '状态',
      dataIndex: 'status',
      key: 'status',
      width: 100,
      render: (status: SyncRunStatus) => (
        <Tag color={statusTagColor(status)} icon={statusIcon(status)}>
          {statusLabel(status)}
        </Tag>
      ),
    },
    {
      title: '仓库',
      key: 'repos',
      width: 160,
      render: (_, run) => (
        <Space size={4}>
          <span>成功 <strong style={{ color: '#52c41a' }}>{run.reposSynced}</strong></span>
          {run.reposTotal > 0 && <span style={{ color: '#999' }}>/ {run.reposTotal}</span>}
          {run.reposFailed > 0 && (
            <span style={{ color: '#cf1322' }}>· 失败 <strong>{run.reposFailed}</strong></span>
          )}
        </Space>
      ),
    },
    {
      title: '错误',
      dataIndex: 'error',
      key: 'error',
      ellipsis: true,
      render: (msg: string | undefined) => msg ? (
        <Tooltip title={msg}>
          <span style={{ color: '#cf1322', fontSize: 12 }}>
            <CloseCircleOutlined /> {msg}
          </span>
        </Tooltip>
      ) : <span style={{ color: '#999' }}>—</span>,
    },
  ];

  // ── 渲染 ─────────────────────────────────────────────────────────

  return (
    <div className="page-content">
      <div style={{ display: 'flex', alignItems: 'center', marginBottom: 16, gap: 12 }}>
        <h2 style={{ margin: 0, flex: 1 }}>
          <SwapOutlined /> 镜像同步
        </h2>
        <Tooltip title="新建同步任务">
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            新建同步任务
          </Button>
        </Tooltip>
      </div>

      {error && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={`加载失败：${error.message}`}
          action={<Button size="small" onClick={() => void refresh()}>重试</Button>}
        />
      )}

      {loading ? (
        <TableSkeleton columns={6} />
      ) : tasks.length === 0 ? (
        <Empty
          description={
            <div>
              <p>还没有同步任务。</p>
              <p style={{ color: '#999', fontSize: 12 }}>
                点右上角「新建同步任务」配一条「从哪个 cairn 同步哪些仓库到本节点」的规则。
              </p>
            </div>
          }
        >
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            新建同步任务
          </Button>
        </Empty>
      ) : (
        <Table<SyncTask>
          rowKey="id"
          columns={columns}
          dataSource={visibleTasks}
          pagination={{ pageSize: 20, showSizeChanger: false }}
          size="middle"
        />
      )}

      {/* 编辑 / 新建 Modal */}
      <Modal
        title={editing ? `编辑任务：${editing.name}` : '新建同步任务'}
        open={modalOpen}
        onCancel={closeModal}
        onOk={submit}
        confirmLoading={submitting}
        okText="保 存"
        cancelText="取 消"
        width={600}
        destroyOnClose
        // 测试连接按钮放在 footer 左侧;ok 按钮保持原状。
        // onClick 闭包从 form 取实时值,不需要等用户先点 Save。
        footer={
          <Space style={{ width: '100%', justifyContent: 'space-between' }}>
            <Button
              icon={<ApiOutlined />}
              loading={testing}
              disabled={testing}
              onClick={async () => {
                // 用 form.getFieldsValue() 拿实时值,不依赖 Validate
                // 全部通过（匿名档本来就不需要用户名密码）。
                const cur = form.getFieldsValue() as Partial<FormValues>;
                if (!cur.remoteUrl) {
                  message.warning('先填「远端 cairn 地址」再测');
                  return;
                }
                /**
                 * v0.7.0（SYNC-3）：按当前档位前置拦截——探测得真拿得到用户名密码,
                 * 否则只会得到「required_but_missing」这种误导性结论。
                 *   credential 档没选凭据 → 先让选（后端只会回 400）
                 *   inline 档没填密码     → 编辑态的旧密码前端拿不到,必须重输才能测
                 */
                if (credMode === 'credential' && !cur.remoteCredentialId) {
                  message.warning('先选一条凭据再测（或切到「匿名」）');
                  return;
                }
                if (credMode === 'inline' && !(cur.remotePassword ?? '')) {
                  message.warning('内联凭据模式下,测连接需要重填一次「远端密码」');
                  return;
                }
                setProbe(null);
                setTesting(true);
                try {
                  const result = await testSyncConnection({
                    remoteUrl: cur.remoteUrl ?? '',
                    remoteCredentialId:
                      credMode === 'credential' ? (cur.remoteCredentialId ?? '') : '',
                    remoteUsername: credMode === 'inline' ? (cur.remoteUsername ?? '') : '',
                    remotePassword: credMode === 'inline' ? (cur.remotePassword ?? '') : '',
                  });
                  if (result.success && result.data) {
                    setProbe(result.data);
                  } else {
                    message.error(`探测失败：${result.message}`);
                    setProbe({
                      reachable: false,
                      authStatus: 'unknown',
                      httpStatus: 0,
                      message: result.message,
                    });
                  }
                } finally {
                  // v0.6.6 hotfix:与 submit 同样原因 —— 探测请求本身抛错时,
                  // setTesting(false) 必跑,否则「测试连接」按钮一直转圈。
                  setTesting(false);
                }
              }}
            >
              测试连接
            </Button>
            <Space>
              <Button onClick={closeModal} disabled={submitting}>
                取 消
              </Button>
              <Button
                type="primary"
                loading={submitting}
                disabled={testing}
                onClick={submit}
              >
                保 存
              </Button>
            </Space>
          </Space>
        }
      >
        {/* probe 结果挂在最顶部,操作员改完 URL/凭据可立刻重测看变化 */}
        {probe && (
          <Alert
            type={probeAlertType(probe.authStatus)}
            showIcon
            style={{ marginBottom: 16 }}
            message={
              <Space size={4}>
                {probe.authStatus === 'ok' || probe.authStatus === 'no_auth_required' ? (
                  <Tag color="green">OK</Tag>
                ) : probe.authStatus === 'wrong_creds' || probe.authStatus === 'required_but_missing' ? (
                  <Tag color="red">认证</Tag>
                ) : probe.authStatus === 'not_registry' ? (
                  <Tag color="orange">URL</Tag>
                ) : (
                  <Tag>HTTP {probe.httpStatus}</Tag>
                )}
                <span>{probe.message}</span>
              </Space>
            }
            description={
              probe.reachable
                ? `可达,HTTP ${probe.httpStatus}`
                : '远端不可达 (网络问题 / URL 错 / 防火墙)'
            }
          />
        )}

        <Form<FormValues> form={form} layout="vertical" requiredMark="optional">
          <Form.Item
            name="name"
            label="名称"
            rules={[
              { required: true, message: '名称必填' },
              { max: 64, message: '不超过 64 字符' },
            ]}
          >
            <Input placeholder="例如 staging-from-prod" autoComplete="off" />
          </Form.Item>

          <Form.Item
            name="direction"
            label="方向"
            rules={[{ required: true, message: '请选择方向' }]}
            extra={
              <span style={{ fontSize: 12, color: '#999' }}>
                Pull = 「本节点从远端 cairn 拉镜像」；Push = 「本节点把本地镜像推到远端 cairn」。
              </span>
            }
          >
            <Radio.Group>
              <Radio.Button value="pull">
                <SwapOutlined /> 拉（Pull）
              </Radio.Button>
              <Radio.Button value="push">
                <SwapOutlined rotate={90} /> 推（Push）
              </Radio.Button>
            </Radio.Group>
          </Form.Item>

          <Form.Item
            name="remoteUrl"
            label="远端 cairn 地址"
            rules={[
              { required: true, message: '远端地址必填' },
              {
                pattern: /^https?:\/\/.+/,
                message: '需要 http(s):// 开头的完整 URL',
              },
            ]}
          >
            <Input placeholder="https://cairn-staging.example.com" autoComplete="off" />
          </Form.Item>

          {/*
            v0.7.0（SYNC-3）：远端凭据改成「三档 Radio + 条件渲染」。
            背景：原来手填用户名密码,换密码要回每条任务里改、密码也没法在任务间复用。
            现在推荐先去「凭据管理」建凭据,再来这里选;换密码只动凭据,任务不动。
          */}
          <Form.Item label="远端凭据" required>
            <Radio.Group
              value={credMode}
              onChange={(e) => setCredMode(e.target.value as CredMode)}
            >
              <Radio.Button value="anonymous">匿名</Radio.Button>
              <Radio.Button value="credential">引用凭据</Radio.Button>
              {/* 「保留内嵌凭据」只对老任务开放——新建的任务不该再走内联。 */}
              {editing && editing.remoteUsername ? (
                <Radio.Button value="inline">保留内嵌凭据</Radio.Button>
              ) : null}
            </Radio.Group>
            <div style={{ fontSize: 12, color: '#999', marginTop: 4 }}>
              {credMode === 'anonymous'
                ? '对端 cairn 没配 auth 时用这档,引擎不发 Authorization header。'
                : credMode === 'credential'
                  ? '从「凭据管理」里选一条；以后换密码只改凭据,这条任务不用动。'
                  : '沿用这条任务原有的用户名密码（编辑时密码留空 = 保留原值）。'}
            </div>
          </Form.Item>

          {credMode === 'credential' && (
            <Form.Item
              name="remoteCredentialId"
              label="选择凭据"
              rules={[{ required: true, message: '选一条凭据（或切到「匿名」）' }]}
              extra={
                <span style={{ fontSize: 12, color: '#999' }}>
                  列表来自「凭据管理」；一条都没有就先过去新建。按名称 / 用户名 / 地址都能搜。
                </span>
              }
            >
              <Select
                showSearch
                optionFilterProp="label"
                loading={credLoading}
                placeholder="从凭据库中选择"
                notFoundContent={credLoading ? '加载中…' : '凭据库为空'}
                options={credentials.map((c) => ({
                  value: c.id,
                  label: `${c.name}（${c.username}@${c.registryUrl}）`,
                }))}
              />
            </Form.Item>
          )}

          {credMode === 'inline' && (
            <>
              <Form.Item
                name="remoteUsername"
                label="远端用户名"
                rules={[{ required: true, message: '内嵌凭据需要用户名' }]}
                extra={
                  <span style={{ fontSize: 12, color: '#999' }}>
                    对端 cairn 的 Basic-auth 用户名。想改由凭据库管理,把上面切成「引用凭据」即可。
                  </span>
                }
              >
                <Input placeholder="admin" autoComplete="off" />
              </Form.Item>

              <Form.Item
                name="remotePassword"
                label="远端密码"
                rules={[]}
                extra={
                  <span style={{ fontSize: 12, color: '#999' }}>
                    留空 = 保留当前密码（只在换密码时填）。
                  </span>
                }
              >
                <Input.Password placeholder="留空保留旧值" autoComplete="off" />
              </Form.Item>
            </>
          )}

          <Form.Item
            name="include"
            label="Include 模式"
            extra={
              <span style={{ fontSize: 12, color: '#999' }}>
                一行一个 glob（`*` 通配）；空 = 同步所有仓库。例：<code>library/*</code>。
              </span>
            }
          >
            <Input.TextArea rows={3} placeholder={'library/nginx\nlibrary/redis'} />
          </Form.Item>

          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch checkedChildren="启用" unCheckedChildren="停用" />
          </Form.Item>
        </Form>
      </Modal>

      {/* 历史 Modal */}
      <Modal
        title={historyTask ? `历史：${historyTask.name}` : ''}
        open={historyTask !== null}
        onCancel={closeHistory}
        footer={<Button onClick={closeHistory}>关 闭</Button>}
        width={800}
        destroyOnClose
      >
        {historyLoading ? (
          <TableSkeleton columns={4} />
        ) : historyRuns.length === 0 ? (
          <Empty description="还没有运行记录" />
        ) : (
          <Table<SyncRun>
            rowKey="id"
            columns={historyColumns}
            dataSource={historyRuns}
            pagination={{ pageSize: 10, showSizeChanger: false }}
            size="small"
          />
        )}
      </Modal>
    </div>
  );
}

/**
 * 把后端错误翻译到表单字段错误上（name 重名 / 凭据冲突等）。
 *
 * v0.7.0：改成返回「是否已定位到字段」。调用方在失败且返回 false 时补一条全局
 * message —— 否则（比如错误指向一个当前没渲染的条件字段）点了保存会毫无反馈。
 */
function formError(form: FormInstance<FormValues>, result: ApiResult<unknown>): boolean {
  const code = result.code ?? '';
  const message = result.message ?? '请求失败';
  if (code === 'CONFLICT' && message.includes('name')) {
    form.setFields([{ name: 'name', errors: [message] }]);
    return true;
  }
  /**
   * v0.7.0（SYNC-3）：凭据三态的冲突 / 找不到。带 "credential" 的两种文案
   * （ErrCredentialConflict / ErrCredentialNotFound）都指回选择框。
   * 顺序必须在下面 username/password 之前——ErrCredentialConflict 原文同时含
   * "credential",先匹配更具体的一档。
   */
  if (code === 'BAD_REQUEST' && message.includes('credential')) {
    form.setFields([{ name: 'remoteCredentialId', errors: [message] }]);
    return true;
  }
  /** ErrCredentialIncomplete：用户名密码只填一个——指回用户名（inline 档必渲染）。 */
  if (code === 'BAD_REQUEST' && message.includes('username') && message.includes('password')) {
    form.setFields([{ name: 'remoteUsername', errors: [message] }]);
    return true;
  }
  if (code === 'BAD_REQUEST' && message.includes('URL')) {
    form.setFields([{ name: 'remoteUrl', errors: [message] }]);
    return true;
  }
  if (code === 'BAD_REQUEST' && (message.includes('password') || message.includes('Password'))) {
    form.setFields([{ name: 'remotePassword', errors: [message] }]);
    return true;
  }
  if (code === 'BAD_REQUEST' && (message.includes('username') || message.includes('Username'))) {
    form.setFields([{ name: 'remoteUsername', errors: [message] }]);
    return true;
  }
  // 兜底：没法定位到字段 → 调用方给全局提示。
  return false;
}

function statusIcon(status: SyncRunStatus) {
  switch (status) {
    case 'success': return <CheckCircleOutlined />;
    case 'partial': return <ExclamationCircleOutlined />;
    case 'failed':  return <CloseCircleOutlined />;
    case 'running': return <ClockCircleOutlined />;
    default:        return <PauseCircleOutlined />;
  }
}

/**
 * probe.authStatus → antd Alert 的视觉等级（v0.6.5 新增）。
 *   ok / no_auth_required → success（绿）
 *   wrong_creds / required_but_missing → error（红）
 *   not_registry → warning（黄）
 *   unknown → info（蓝灰）—— 兜底,后端目前不会出这个,留着
 */
function probeAlertType(status: SyncProbeAuthStatus): 'success' | 'error' | 'warning' | 'info' {
  switch (status) {
    case 'ok':
    case 'no_auth_required':
      return 'success';
    case 'wrong_creds':
    case 'required_but_missing':
      return 'error';
    case 'not_registry':
      return 'warning';
    default:
      return 'info';
  }
}