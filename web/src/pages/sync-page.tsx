/**
 * v0.6.0: 镜像同步（cairn↔cairn）页面。
 *
 * 列任务表 + 新建/编辑 Modal + 立即运行 + 历史查看 Modal。后端路由为
 * /api/sync（CRUD）+ /api/sync/{id}/run（同步执行,等返回）+ /api/sync/{id}/runs（历史）。
 *
 * 复杂度说明：v0.6.0 只支持「手动 + Basic auth + include 过滤」,所以页面相对克制——
 *   - 选错方向时镜像会从对端被覆盖/反覆盖,UI 上 direction 走 Radio 而非下拉,
 *     减少误操作（pull 是「我拉对端」,push 是「我推对端」,含义相反但都是英文短词,
 *     单字面下拉很容易选反）。
 *   - remotePassword 后端用 json:"-" 屏蔽——UI 永远拿不到明文。新建必填、编辑可省略
 *     （保留旧值,详见 sync_handlers.go UpdateTask）。remoteUsername 可见、可编辑
 *     （用户名不敏感,编辑时 UI 能预填）。
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
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import type { FormInstance } from 'antd';
import {
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
  listSyncRuns,
  listSyncTasks,
  runSyncTask,
  updateSyncTask,
} from '../api';
import type {
  ApiResult,
  SyncDirection,
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

interface FormValues {
  name: string;
  direction: SyncDirection;
  remoteUrl: string;
  /** 远端 cairn 的 Basic-auth 用户名。 */
  remoteUsername: string;
  /** 远端 cairn 的 Basic-auth 密码。编辑时可空 = 保留旧值（后端处理）。 */
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
    setModalOpen(true);
  };

  const openEdit = (task: SyncTask) => {
    setEditing(task);
    form.setFieldsValue({
      name: task.name,
      direction: task.direction,
      remoteUrl: task.remoteUrl,
      remoteUsername: task.remoteUsername,
      /** 编辑时密码留空——后端看到空字符串会保留旧值。UI 不应该假装知道旧密码。 */
      remotePassword: '',
      include: task.include,
      enabled: task.enabled,
    });
    setModalOpen(true);
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
    setSubmitting(true);
    const input: SyncTaskInput = {
      name: values.name.trim(),
      direction: values.direction,
      remoteUrl: values.remoteUrl.trim(),
      remoteUsername: values.remoteUsername.trim(),
      remotePassword: values.remotePassword ?? '',
      include: values.include ?? '',
      enabled: values.enabled,
    };
    try {
      if (editing) {
        const result = await updateSyncTask(editing.id, input);
        if (result.success) {
          message.success('已更新');
          closeModal();
          await refresh();
        } else {
          formError(form, result);
        }
      } else {
        const result = await createSyncTask(input);
        if (result.success) {
          message.success('已创建');
          closeModal();
          await refresh();
        } else {
          formError(form, result);
        }
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
        const run = result.data;
        if (run.status === 'success') {
          message.success(`同步成功：${run.reposSynced}/${run.reposTotal} 个仓库`);
        } else if (run.status === 'partial') {
          message.warning(`部分失败：${run.reposSynced} 成功 / ${run.reposFailed} 失败`);
        } else if (run.status === 'failed') {
          message.error(`同步失败：${run.error ?? '未知错误'}`);
        } else {
          message.info(`状态 ${run.status}`);
        }
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
      render: (_, task) => (
        <Space size={4}>
          <Tooltip title="立即运行（同步执行,等返回）">
            <Button
              size="small"
              type="text"
              icon={<PlayCircleOutlined />}
              loading={runningId === task.id}
              onClick={() => void handleRun(task)}
            >
              运行
            </Button>
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
      ),
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
      >
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

          <Form.Item
            name="remoteUsername"
            label="远端用户名"
            rules={[{ required: true, message: '用户名必填' }]}
            extra={
              <span style={{ fontSize: 12, color: '#999' }}>
                对端 cairn 的 Basic-auth 用户名（跟「设置 → Registry 认证」一致）。
              </span>
            }
          >
            <Input placeholder="admin" autoComplete="off" />
          </Form.Item>

          <Form.Item
            name="remotePassword"
            label="远端密码"
            rules={editing ? [] : [{ required: true, message: '密码必填' }]}
            extra={
              editing ? (
                <span style={{ fontSize: 12, color: '#999' }}>
                  留空 = 保留当前密码。仅当你要换密码时填。
                </span>
              ) : (
                <span style={{ fontSize: 12, color: '#999' }}>
                  对端 cairn 的 Basic-auth 密码（不进 UI,只在新建/改密码时填这一次）。
                </span>
              )
            }
          >
            <Input.Password
              placeholder={editing ? '留空保留旧值' : '密码'}
              autoComplete="off"
            />
          </Form.Item>

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

/** 把后端错误翻译到表单字段错误上（name 重名等）。 */
function formError(form: FormInstance<FormValues>, result: ApiResult<unknown>): void {
  const code = result.code ?? '';
  const message = result.message ?? '请求失败';
  if (code === 'CONFLICT' && message.includes('name')) {
    form.setFields([{ name: 'name', errors: [message] }]);
    return;
  }
  if (code === 'BAD_REQUEST' && message.includes('URL')) {
    form.setFields([{ name: 'remoteUrl', errors: [message] }]);
    return;
  }
  if (code === 'BAD_REQUEST' && (message.includes('password') || message.includes('Password'))) {
    form.setFields([{ name: 'remotePassword', errors: [message] }]);
    return;
  }
  if (code === 'BAD_REQUEST' && (message.includes('username') || message.includes('Username'))) {
    form.setFields([{ name: 'remoteUsername', errors: [message] }]);
    return;
  }
  // 兜底：没法定位到字段时,用全局 message 提示（调用方在 submit 末尾根据 result.success=false 处理）
  // 这里只处理「字段相关」错误,其他交给 modal.message 在外层提示。
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