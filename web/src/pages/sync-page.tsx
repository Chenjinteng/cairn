/**
 * v0.6.5: 镜像同步（cairn↔cairn）页面。
 *
 * 列任务表 + 新建/编辑 Modal + 立即运行 + 历史查看 Modal。后端路由为
 * /api/sync（CRUD）+ /api/sync/{id}/run（**异步受理**,返 202）+ /api/sync/{id}/runs（历史）
 * + /api/sync/test（探测远端 cairn 连通 + 认证状态）。
 *
 * 复杂度说明：v0.6.0 只支持「手动 + Basic auth + include 过滤」,v0.6.5 新增「测试连接」,
 * v0.6.11 修 SYNC-1~4——
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
  Divider,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Pagination,
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
  PauseCircleOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  SwapOutlined,
  SyncOutlined,
  CalendarOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import {
  createSyncSchedule,
  createSyncTask,
  deleteSyncSchedule,
  deleteSyncTask,
  listCredentials,
  listSyncRunItems,
  listSyncRuns,
  listSyncSchedules,
  listSyncTasks,
  runSyncTask,
  cancelSyncTask,
  testSyncConnection,
  updateSyncSchedule,
  updateSyncTask,
} from '../api';
import { parseTagsFilter } from '../lib/parse-tags-filter';
import type {
  ApiResult,
  Credential,
  SyncDirection,
  SyncRun,
  SyncRunItem,
  SyncRunItemState,
  SyncRunStatus,
  SyncSchedule,
  SyncScheduleInput,
  SyncTask,
  SyncTaskInput,
} from '../types';
import type { SidebarGroup, SidebarItem, SidebarSelection } from '../components/page-sidebar';
import PageLoading from '../components/page-loading';

interface Props {
  /**
   * 侧栏分组状态（v0.5.37.4 起每页一组）。sync 这一页只用 direction 作为分组。
   * 父组件传过来的是当前页的 selection，本组件读 + 写。
   */
  sidebarFilter: SidebarSelection;
  onPublishGroups: (groups: SidebarGroup[]) => void;
  /**
   * v0.6.26: App 层缓存的 sync 任务列表。挂载时直接用这个做初始 state,
   * 避免切走再回时全屏蒙板闪一次。空数组 = 首次进入 → loading=true 走 fetch;
   * 非空 = 之前看过 → loading=false,后台静默刷新。详细说明见 App.tsx 注释。
   */
  initialTasks: SyncTask[];
  /** v0.6.26: 本页 tasks 更新时回调,App 用它写回缓存层。 */
  onTasksChange?: (tasks: SyncTask[]) => void;
}

/**
 * v0.6.11（SYNC-3）：远端凭据三档。
 *   anonymous  — 对端没配 auth,引擎不发 Authorization header
 *   credential — 引用「凭据管理」库里的一条凭据（推荐）
 *   inline     — 沿用任务里原有的内嵌用户名密码（只在编辑老任务时出现,新建不提供）
 */
type CredMode = 'anonymous' | 'credential' | 'inline';

interface FormValues {
  name: string;
  direction: SyncDirection;
  remoteUrl: string;
  /** v0.6.11：credMode === 'credential' 时选中的凭据库 id。 */
  remoteCredentialId?: string;
  /** 远端 cairn 的 Basic-auth 用户名（只在 inline 档渲染）。 */
  remoteUsername?: string;
  /** 远端 cairn 的 Basic-auth 密码（只在 inline 档渲染）。编辑时可空 = 保留旧值（后端处理）。 */
  remotePassword?: string;
  include: string;
  /** v0.7.21：换行分隔的 `repo:tag` 精确清单；非空时引擎跳过 catalog,直接按 spec fetch manifest。 */
  tagsFilter?: string;
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
 * v0.6.30: 定时规则改下拉选择 —— 历史 cron 表达式让用户绕晕,改成 4 档下拉
 * (每小时 / 每日 / 每周 / 每月),每档再选时 / 分 / 周几 / 几号。后端 schema 不动,
 * 前端构造 5 字段 cron 字符串提交。存量 cron 表达式如果不属于这 4 个 pattern
 * (例如「每 N 分钟」「工作日 0 点」这种带范围/通配 cron),UI 标记为「自定义」
 * 并保留原文 —— 编辑时强行走下拉,失去原 cron,但保留可改时区 / 启用的能力
 * (细节见 ScheduleRowEditor)。
 *
 * 整套逻辑三块:
 *   1. cron → Parsed: parseCron 把 cron 字符串反解成 {kind, hour, minute, dow, dom}
 *      任意字段不是简单数字(带通配/范围/列表)或不属于 4 个 pattern,返回 null
 *      (走「自定义」分支)。
 *   2. Parsed → cron: kindToCron 把下拉状态组装成 5 字段字符串提交。
 *   3. cron → 显示: cronSummary 把 cron 翻成中文(用于列表 + 编辑器预览),
 *      解析失败时回落到原文。
 */
type ScheduleKind = 'perNMinutes' | 'hourly' | 'daily' | 'weekly' | 'monthly';

interface ParsedCron {
  kind: ScheduleKind;
  minute: number;
  hour: number;
  /** v0.7.8: 单一「每()分钟」档的步长(1-59,默认 1)。
   *  stepMinutes=1 → 全 * 形式,否则 step 形式 "star&#47;N"。 */
  stepMinutes: number;
  /** 0=周日, 1=周一, ..., 6=周六 —— 跟 cron dow 同款,周一是常见一周起点。 */
  dayOfWeek: number;
  /** 1-31 —— cron dom 同款。 */
  dayOfMonth: number;
}

const KIND_OPTIONS: { value: ScheduleKind; label: string }[] = [
  // v0.7.8: 把 v0.7.4 的「每分钟」(`* * * * *`) 和 v0.7.5 的「每 N 分钟」
  // (`*/N * * * *`) 合成一档「每()分钟」,数可填,1-59,默认 1:
  //   N=1 → "* * * * *"  (每分钟)
  //   N≥2 → "*/N * * * *"  (运维常用 */5 / */15 / */30)
  // 老的 "* * * * *" cron 解析回 perNMinutes(step=1)不丢语义。
  { value: 'perNMinutes', label: '每分钟' },
  { value: 'hourly', label: '每小时' },
  { value: 'daily', label: '每日' },
  { value: 'weekly', label: '每周' },
  { value: 'monthly', label: '每月' },
];

const WEEKDAY_OPTIONS: { value: number; label: string }[] = [
  { value: 1, label: '周一' },
  { value: 2, label: '周二' },
  { value: 3, label: '周三' },
  { value: 4, label: '周四' },
  { value: 5, label: '周五' },
  { value: 6, label: '周六' },
  { value: 0, label: '周日' },
];

const WEEKDAY_LABEL: Record<number, string> = {
  0: '周日',
  1: '周一',
  2: '周二',
  3: '周三',
  4: '周四',
  5: '周五',
  6: '周六',
};

/** 简单整数判断 —— 解析器只接受这一种,带特殊操作符的(例如 `star-slash` `-` `,`)都算「自定义」。 */
function isSimpleInt(s: string): boolean {
  return /^\d+$/.test(s);
}

/**
 * 5 字段 cron → ParsedCron。判定规则(顺序敏感,从最具体到最不具体):
 *   - "* * * * *"        → perNMinutes(step=1,等效 v0.7.4 的「每分钟」)
 *   - "*&#47;N * * * *"  → perNMinutes(step=N,hour + 之后都 *)
 *   - M H D * *     → monthly(M+H+D 都是单个数字,month 必 *)
 *   - M H * * D     → weekly(month 必 *, dom 必 *, dow 单数字)
 *   - M H * * *     → daily(month + dom + dow 都 *)
 *   - M * * * *     → hourly(只 minute 是数字)
 *   - 其他           → null(自定义,UI 走 raw cron 显示路径)
 *
 * 字段是 *, /, -, , 的表达式一律返回 null,不试图「智能」猜测 —— 一旦猜错就
 * 静默覆盖用户原始 cron,代价太高。复杂度让用户走下拉解决。
 */
function parseCron(expr: string): ParsedCron | null {
  const fields = expr.trim().split(/\s+/);
  if (fields.length !== 5) return null;
  const [m, h, dom, mon, dow] = fields;
  // v0.7.8: 全部 * → perNMinutes(step=1)。老的 v0.7.4 写下的 "* * * * *"
  // 解析回同一档,语义不变。
  if (m === '*' && h === '*' && dom === '*' && mon === '*' && dow === '*') {
    return {
      kind: 'perNMinutes',
      minute: 0,
      hour: 0,
      stepMinutes: 1,
      dayOfWeek: 1,
      dayOfMonth: 1,
    };
  }
  // v0.7.5/7.8: perNMinutes = "*&#47;N * * * *" —— 必须在 isSimpleInt(m)
  // 拒掉之前拦下来。step ∈ [1, 59];N=1 走上面全部 * 那条匹配,这条主要处理 N≥2。
  if (h === '*' && dom === '*' && mon === '*' && dow === '*') {
    const stepMatch = m.match(/^\*\/(\d+)$/);
    if (stepMatch) {
      const n = parseInt(stepMatch[1], 10);
      if (n >= 1 && n <= 59) {
        return {
          kind: 'perNMinutes',
          minute: 0,
          hour: 0,
          stepMinutes: n,
          dayOfWeek: 1,
          dayOfMonth: 1,
        };
      }
    }
  }
  // month 必须 * —— 月度定时规则不在本轮下拉覆盖范围(太罕见)
  if (mon !== '*') return null;
  if (!isSimpleInt(m)) return null;

  if (isSimpleInt(dom) && dow === '*') {
    // M H D * *  — monthly
    if (!isSimpleInt(h)) return null;
    return {
      kind: 'monthly',
      minute: parseInt(m, 10),
      hour: parseInt(h, 10),
      stepMinutes: 1,
      dayOfMonth: parseInt(dom, 10),
      dayOfWeek: 1,
    };
  }
  if (dom === '*' && isSimpleInt(dow)) {
    // M H * * D  — weekly
    if (!isSimpleInt(h)) return null;
    return {
      kind: 'weekly',
      minute: parseInt(m, 10),
      hour: parseInt(h, 10),
      stepMinutes: 1,
      dayOfWeek: parseInt(dow, 10),
      dayOfMonth: 1,
    };
  }
  if (dom === '*' && dow === '*' && isSimpleInt(h)) {
    // M H * * *  — daily
    return {
      kind: 'daily',
      minute: parseInt(m, 10),
      hour: parseInt(h, 10),
      stepMinutes: 1,
      dayOfWeek: 1,
      dayOfMonth: 1,
    };
  }
  if (h === '*' && dom === '*' && dow === '*') {
    // M * * * *  — hourly
    return {
      kind: 'hourly',
      minute: parseInt(m, 10),
      hour: 0,
      stepMinutes: 1,
      dayOfWeek: 1,
      dayOfMonth: 1,
    };
  }
  return null;
}

/** ParsedCron → 5 字段 cron。编辑器保存时调用,跟后端 ParseCron 完全对称。 */
function kindToCron(p: ParsedCron): string {
  switch (p.kind) {
    case 'perNMinutes':
      // v0.7.8: 单一「每()分钟」档。step=1 → "* * * * *";step≥2 →
      // "*/N * * * *"。两种形式 cron 等价,但 "*" 形式更直观、scheduler
      // 调试日志更易扫。
      return p.stepMinutes === 1 ? '* * * * *' : `*/${p.stepMinutes} * * * *`;
    case 'hourly':
      return `${p.minute} * * * *`;
    case 'daily':
      return `${p.minute} ${p.hour} * * *`;
    case 'weekly':
      return `${p.minute} ${p.hour} * * ${p.dayOfWeek}`;
    case 'monthly':
      return `${p.minute} ${p.hour} ${p.dayOfMonth} * *`;
  }
}

const pad2 = (n: number): string => (n < 10 ? `0${n}` : String(n));

/**
 * cron 字符串 → 中文摘要,用于列表 + 编辑器预览:
 *   「每分钟」「每 15 分钟」「每小时 第 30 分」「每日 03:30」「每周一 03:30」「每月 1 日 03:30」
 * 解析失败(自定义)直接返回原文 + 「自定义」前缀。
 */
function cronSummary(expr: string): string {
  const parsed = parseCron(expr);
  if (!parsed) return `自定义: ${expr}`;
  switch (parsed.kind) {
    case 'perNMinutes':
      // v0.7.8: step=1 → 「每分钟」(无歧义);step≥2 → 「每 N 分钟」。
      return parsed.stepMinutes === 1 ? '每分钟' : `每 ${parsed.stepMinutes} 分钟`;
    case 'hourly':
      return `每小时 第 ${parsed.minute} 分`;
    case 'daily':
      return `每日 ${pad2(parsed.hour)}:${pad2(parsed.minute)}`;
    case 'weekly':
      return `每${WEEKDAY_LABEL[parsed.dayOfWeek] ?? `周${parsed.dayOfWeek}`} ${pad2(parsed.hour)}:${pad2(parsed.minute)}`;
    case 'monthly':
      return `每月 ${parsed.dayOfMonth} 日 ${pad2(parsed.hour)}:${pad2(parsed.minute)}`;
  }
}

/**
 * 任务表 + 表头操作 + 历史查看 Modal。Modal 拆两个：
 *  1) 任务编辑（create / edit 共用）
 *  2) 历史查看（按 task 懒拉取 sync_runs）
 */
export default function SyncPage({ sidebarFilter, onPublishGroups, initialTasks, onTasksChange }: Props) {
  const { message, modal } = AntdApp.useApp();
  /*
   * v0.6.26: tasks 用 App 层缓存的 initialTasks 初始化 —— 切走再回时立刻
   * 有数据可显示,loading=false,PageLoading 不出现;useEffect 仍然走
   * refresh(),新数据(silent update,setTasks 不会触发 loading 变 true)
   * 覆盖到本地 + 通过 onTasksChange 写回 App 缓存。
   *
   * 第一次进入(initialTasks.length === 0)走原行为:loading=true 触发蒙板。
   */
  const [tasks, setTasks] = useState<SyncTask[]>(initialTasks);
  const [loading, setLoading] = useState(initialTasks.length === 0);
  const [error, setError] = useState<ApiResult<unknown> | null>(null);

  /** 编辑/新建 Modal：editing == null → 新建；非空 → 编辑（token 字段空表示保留旧值）。 */
  const [editing, setEditing] = useState<SyncTask | null>(null);
  const [modalOpen, setModalOpen] = useState(false);
  const [form] = Form.useForm<FormValues>();
  const [submitting, setSubmitting] = useState(false);

  /**
   * v0.6.20: 历史展示从「点按钮弹 Modal」改为「Task 表行直接展开」。
   *
   * 原来两段式:点「运行历史」按钮 → Modal 弹出 → 在 Modal 内展开 run → 看到
   * (repo, tag) 明细。用户反馈「弹出再下拉,就很突兀」——Modal 本身的存在
   * 是冗余的(任务列表已经在做主表,历史是它的二级视图,不应该脱离主表语境)。
   *
   * 现在两段式:点 Task 行的 + → 内嵌 Run 表展开 → 点 Run 行的 + → (repo, tag)
   * 明细。所有交互都在主页面里,跟 UI 主体语境一致。
   *
   * state 拆分:
   *   - expandedTaskIds: 展开的 task.id 集合(对应 Task Table 的 expandable)
   *   - runsByTaskId:    缓存每个 task 的 runs 列表(展开 task 时按需加载)
   *   - expandedRunIds:  展开的 run.id 集合(对应每个 task 展开区里的 Run Table)
   *   - itemsByRunId:    缓存每个 run 的 (repo, tag) 明细
   *   - currentPageByRunId: 用户当前在哪一页。**关键修复**:0.6.18 用
   *     `Math.floor(loadedCount / PAGE_SIZE) + ...` 算 current,最后一页不满
   *     50 条时会算出 1(分页高亮错的根因)。现在改成显式 state。
   */
  const [expandedTaskIds, setExpandedTaskIds] = useState<number[]>([]);
  const [runsByTaskId, setRunsByTaskId] = useState<Record<number, {
    runs: SyncRun[];
    loading: boolean;
    loaded: boolean; // 区分「没加载过」vs「加载过但是 0 条」
  }>>({});
  const [expandedRunIds, setExpandedRunIds] = useState<number[]>([]);
  const [itemsByRunId, setItemsByRunId] = useState<Record<number, {
    items: SyncRunItem[];
    total: number;
    limit: number;
    offset: number;
    loading: boolean;
  }>>({});
  const [currentPageByRunId, setCurrentPageByRunId] = useState<Record<number, number>>({});

  const ITEMS_PAGE_SIZE = 50;

  // v0.6.11: 定时规则 Modal — 跟「历史」Modal 平级,各自独立加载。
  const [scheduleTask, setScheduleTask] = useState<SyncTask | null>(null);
  const [schedules, setSchedules] = useState<SyncSchedule[]>([]);
  const [schedulesLoading, setSchedulesLoading] = useState(false);

  /** 立即运行中的 task id（按钮 spinner）。 */
  const [runningId, setRunningId] = useState<number | null>(null);
  /** v0.7.23: 中止运行中的 task id（按钮 spinner）。跟 runningId 拆开
   * 因为「立即运行」和「中止」可能在不同时机旋转。 */
  const [cancellingId, setCancellingId] = useState<number | null>(null);

  /**
   * v0.6.13 (UI): 「测试连接」结果不再落进 state,而是按状态码分档直接
   * 弹 message.toast(见「测试连接」按钮的 onClick)。弹窗顶部不再渲染
   * Alert,改完 URL/凭据再点「测试连接」立即看到新一条。
   */
  /** 「测试连接」进行中——按钮 loading + 顶部 Alert 收起。 */
  const [testing, setTesting] = useState(false);

  /**
   * v0.6.11（SYNC-3）：Modal 里的远端凭据档位。放本地 state 而不是 Form 字段——
   * 条件渲染要读它,而 `Form.useWatch` 在 destroyOnClose 的 Modal 上有挂载时序坑;
   * 本地 state 是稳定的真值来源（submit 直接读,不再从 validateFields 里拿）。
   */
  const [credMode, setCredMode] = useState<CredMode>('anonymous');
  /** v0.6.11（SYNC-3）：凭据库列表（Modal 打开时拉一次;凭据是低频数据,不轮询）。 */
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [credLoading, setCredLoading] = useState(false);

  const refresh = useCallback(async () => {
    const result = await listSyncTasks();
    if (result.success && result.data) {
      setTasks(result.data);
      // v0.6.26: 把新数据写回 App 层缓存,下次从外部切回 sync 时直接拿这版
      onTasksChange?.(result.data);
      setError(null);
    } else {
      setError(result);
    }
    setLoading(false);
  }, [onTasksChange]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  /**
   * v0.6.11（SYNC-1/4）：有任务在后台跑时每 3s 刷一次列表,把 running 追成终态
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
   * v0.6.11（SYNC-3）：拉凭据库列表。失败只提示不阻断——用户仍可切「匿名」档保存,
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
      tagsFilter: '',
      enabled: true,
    });
    setCredMode('anonymous');  // v0.6.11（SYNC-3）：新建默认匿名,要认证就选「引用凭据」
    setModalOpen(true);
    void loadCredentials();  // 让「引用凭据」档的选择框一开就有数据
  };

  const openEdit = (task: SyncTask) => {
    setEditing(task);
    /**
     * v0.6.11（SYNC-3）：按任务现状推断档位,**不强行**把老任务迁到凭据引用——
     *   remoteCredentialId 非空 → credential（已经在用凭据库）
     *   内联用户名非空          → inline（0.6.10 之前建的,保留原内嵌凭据）
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
      tagsFilter: task.tagsFilter ?? '',
      enabled: task.enabled,
    });
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
       * v0.6.11（SYNC-3）：按档位组装**互斥**的三态。后端 Validate 会把「引用 + 内联」
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
        // v0.7.21: tags_filter 与 include 正交——前者跳过 catalog 走精确 spec,
        // 后者在 catalog 返的 repos 里 glob。两字段都空也能工作,但用户同时配两个
        // 是奇怪的(后面 UI 想加互斥提示可以再说)。Trim 是因为 antd Input.TextArea
        // 会在末尾留 `\n`,直接入库会让无意义的空行走 parser。
        tagsFilter: (values.tagsFilter ?? '').trim(),
        // v0.7.24: longTimeoutRepos 字段已 deprecated —— engine 现在
        // 按 manifest size 自动切 timeout,不需要 user 配置。后端字段
        // 保留兼容,所以这里固定发空串(让 SyncTaskInput 类型对齐);
        // 后端收到空串等价于"无覆盖"。
        longTimeoutRepos: '',
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
        // v0.6.11：字段级映射没兜住时必须给全局提示,否则「点了保存什么都没发生」。
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
      content: '任务的所有历史运行记录会一并删除，无法恢复。',
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
         * v0.6.11（SYNC-1/4）：run 接口改成**异步受理**——后端 TryLock + 落 running 行
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

  /**
   * v0.7.23: 中止运行中的 sync 任务。
   *
   * 点「中止」→ POST /api/sync/{id}/cancel → 后端 dispatch 一个 cancel
   * 信号给 background goroutine → run row 在 background goroutine 跑完
   * 当前 layer (max 一个 layer timeout) 后翻 'failed' 并解锁「立即运行」
   * 按钮。
   *
   * UI 不阻塞等终态:
   *   - 立刻 toast「已请求中止,稍等」(真 user 在意的是「点了有响应」)
   *   - 跑 refresh() 看到 lastRunStatus=running 还在(再等几十秒才翻)
   *   - 现有 3s 轮询自然追上终态(已经在跑,running=true 时 interval 触发)
   *
   * 200 + cancelled=true 表示 cancel 信号已发出;实际 run 还要等当
   * 前 layer / repo 退出才能停。400 表示 task 已不在 running 状态
   * (按钮 enabled 时是 running,click 之前 run 已结束 —— UI race,
   * refresh 后按钮会自动 disable)。
   */
  const handleCancel = async (task: SyncTask) => {
    // v0.7.23: 中止是高危动作,二段确认 —— 避免「手滑点了开始没完没了」。
    modal.confirm({
      title: `中止任务 "${task.name}"？`,
      content:
        '当前正在同步的 (repo, tag) 会跑完当前层后中止,run row 标 failed。' +
        '已经下完的层会保留,下次「立即运行」会跳过它们。',
      okText: '中 止',
      okButtonProps: { danger: true },
      cancelText: '取 消',
      onOk: async () => {
        setCancellingId(task.id);
        try {
          const result = await cancelSyncTask(task.id);
          if (result.success) {
            message.success('已请求中止，稍等当前层跑完');
            await refresh();
          } else if (result.code === 'BAD_REQUEST') {
            // 按钮 enabled 时 running,但 click 之前已结束 —— UI race。
            // 提示后 refresh,3s 轮询会自然解锁按钮。
            message.warning('任务已不在运行状态，正在刷新');
            await refresh();
          } else {
            message.error(`中止失败：${result.message}`);
          }
        } finally {
          setCancellingId(null);
        }
      },
    });
  };

  // ── 历史（v0.6.20 重构：Modal → Task Table 行内嵌展开） ────────────────────────

  /**
   * v0.6.20: 展开 task 行时按需加载 runs。
   *
   * - 已加载过(runsByTaskId[taskId].loaded)直接返回,避免重复打接口
   * - 加载中(loading)直接返回,避免重复刷 spinner
   * - 错误:toast 失败原因,仍标记 loaded=true (否则 loading 永不清掉)
   *
   * v0.6.30: 上限 10 条 run —— 后端 CreateRun 末尾会 trim,每个 task 只保留最近
   * 10 条 sync_runs(CASCADE 带走 run_items)。UI 拿 10 跟后端保留数对齐,不再
   * 默默截断;「仅保留最近 10 条历史」提示常驻在 runs 表头。
   */
  const RUN_HISTORY_LIMIT = 10;
  const loadTaskRuns = async (taskId: number, opts?: { force?: boolean }) => {
    const existing = runsByTaskId[taskId];
    /*
     * v0.6.23: 加 force 参数 —— 给「实时刷新」轮询用,绕过 already-loaded
     * 短路。原短路是为了避免用户狂点 + 触发重复 fetch;轮询是有意重复,
     * 想看到正在跑的 sync 任务的新 run 列表。
     *
     * 仍然拦下「正在加载中」(loading)避免并发覆盖;force 只绕过 loaded 拦。
     */
    if (existing?.loading) return;
    if (!opts?.force && existing?.loaded) return;
    setRunsByTaskId((prev) => ({
      ...prev,
      [taskId]: { runs: prev[taskId]?.runs ?? [], loading: true, loaded: false },
    }));
    const result = await listSyncRuns(taskId, RUN_HISTORY_LIMIT);
    if (result.success && result.data) {
      setRunsByTaskId((prev) => ({
        ...prev,
        [taskId]: { runs: result.data!, loading: false, loaded: true },
      }));
    } else {
      // v0.7.26: 轮询路径（force=true）失败静默 —— setInterval 3s 后会重试,
      // 手机切屏 / VPN 慢速触发的 10s 超时不该作为错误呈现给用户（用户已习惯
      // self-healing 行为,toast 反而是噪声）。首次手动展开（force=false）失败
      // 仍然 toast,那是真信号（用户主动操作后的真实失败）。
      if (!opts?.force) {
        message.error(`加载历史失败：${result.message}`);
      }
      setRunsByTaskId((prev) => ({
        ...prev,
        [taskId]: { runs: prev[taskId]?.runs ?? [], loading: false, loaded: true },
      }));
    }
  };

  /**
   * v0.6.20: 加载某条 run 的 (repo, tag) 明细。每页 50,每次翻页都**替换**
   * items(不 append) —— Pagination 不需要「加载更多」,直接「下一页/末页」即可。
   *
   * 同步写入 `currentPageByRunId[runId]`:这是 Pagination 的高亮真值来源。
   *
   * 0.6.18 的 `current` 用 `Math.floor(loadedCount / PAGE_SIZE) + ...` 算出来,
   * 最后一页不满 PAGE_SIZE 条时会算出 1(把第 5 页算成第 1 页),用户反馈
   * 「点了第 2 页还是高亮第 1 页」就是这个 bug。本轮改成显式 state 记录,
   * 不再从 items.length 推算。
   *
   * - page === 1           → 从 offset 0 加载,记录 currentPage=1
   * - page === {jumpTo:N}  → 从 offset (N-1)*PAGE_SIZE 加载,记录 currentPage=N
   *
   * 失败的 toast 走 message.error:展开行内操作失败不应让用户惊动扩展页,
   * 跟任务列表里失败重试一样的反馈层级。
   *
   * 注意点: 同一 run 被并发触发时 (用户狂点 / 网络抖动重发) 通过 prev guard
   * 拦掉 —— setItemsByRunId 的 updater 先检查 loading,避免竞争覆盖。
   */
  const loadRunItems = async (
    taskId: number,
    runId: number,
    page: 1 | { jumpTo: number } = 1,
  ) => {
    const targetPage = page === 1 ? 1 : page.jumpTo;
    setItemsByRunId((prev) => {
      const existing = prev[runId];
      if (existing?.loading) return prev; // 并发拦截
      return {
        ...prev,
        [runId]: {
          items: existing?.items ?? [],
          total: existing?.total ?? 0,
          limit: existing?.limit ?? ITEMS_PAGE_SIZE,
          offset: existing?.offset ?? 0,
          loading: true,
        },
      };
    });
    const existing = itemsByRunId[runId];
    const offset = (targetPage - 1) * ITEMS_PAGE_SIZE;
    const limit = existing?.limit ?? ITEMS_PAGE_SIZE;
    const result = await listSyncRunItems(taskId, runId, limit, offset);
    if (result.success && result.data) {
      setCurrentPageByRunId((prev) => ({ ...prev, [runId]: targetPage }));
      setItemsByRunId((prev) => ({
        ...prev,
        [runId]: {
          items: result.data!.items,
          total: result.data!.total,
          limit: result.data!.limit,
          offset: result.data!.offset + result.data!.items.length,
          loading: false,
        },
      }));
    } else {
      message.error(`加载明细失败：${result.message ?? '未知错误'}`);
      setItemsByRunId((prev) => ({
        ...prev,
        [runId]: { ...(prev[runId] ?? { items: [], total: 0, limit, offset: 0, loading: false }), loading: false },
      }));
    }
  };

  /**
   * v0.6.23: 实时刷新正在跑的 task 的 runs 列表。
   *
   * 用户反馈:展开 task 看历史时,如果 task 正在跑(running),看到的 runs
   * 是旧数据(展开时 fetch 一次后不更新),得手动刷新页面或重新折叠/展开
   * 才能看到新完成的 run。
   *
   * 做法:对当前已展开的 task,凡是 lastRunStatus === 'running' 的,
   * 每 3s 调一次 loadTaskRuns(taskId, { force: true })。task 不再是
   * running(变成 success / failed)或用户收起 task 时,自动停止轮询。
   *
   * 为什么 3s?跟父页面对 running task 的轮询周期一致(见 refresh() 上面的
   * useEffect),既不过频打接口,也能在 sync run 完成的 ~3s 内反映到 UI。
   * sync 一次跑几十秒到几分钟,3s 延迟肉眼可接受。
   *
   * useEffect deps 用 expandedTaskIds + tasks:
   *   - 任一 task 展开状态变 → 重新计算待轮询集合
   *   - 任一 task 的 running 状态变 → 重新计算
   * 没变的话不会重跑 useEffect,interval 不被清掉。
   */
  useEffect(() => {
    const runningExpanded: SyncTask[] = [];
    for (const id of expandedTaskIds) {
      const t = tasks.find((x) => x.id === id);
      if (t && t.lastRunStatus === 'running') runningExpanded.push(t);
    }
    if (runningExpanded.length === 0) return;
    const interval = window.setInterval(() => {
      for (const t of runningExpanded) {
        void loadTaskRuns(t.id, { force: true });
      }
    }, 3000);
    return () => window.clearInterval(interval);
  }, [expandedTaskIds, tasks]);

  // ── 定时（v0.6.11）────────────────────────────────────────────────

  /**
   * 打开任务的「定时规则」Modal,加载该任务的 schedules 列表。
   * 跟历史 Modal 各自独立 — 后端不提供合并端点(语义不同:历史是 sync_runs,
   * 定时是 sync_schedules),共用一个 Modal 会让列表加载时机很难看。
   */
  const openSchedules = async (task: SyncTask) => {
    setScheduleTask(task);
    setSchedules([]);
    setSchedulesLoading(true);
    const result = await listSyncSchedules(task.id);
    setSchedulesLoading(false);
    if (result.success && result.data) {
      setSchedules(result.data);
    } else {
      message.error(`加载定时失败：${result.message}`);
    }
  };

  const closeSchedules = () => {
    setScheduleTask(null);
    setSchedules([]);
  };

  /** 刷新当前 scheduleTask 的列表 — 用于 create / update / delete 之后 */
  const refreshSchedules = async () => {
    if (!scheduleTask) return;
    const result = await listSyncSchedules(scheduleTask.id);
    if (result.success && result.data) {
      setSchedules(result.data);
    }
  };

  const handleCreateSchedule = async (input: SyncScheduleInput): Promise<ApiResult<SyncSchedule>> => {
    if (!scheduleTask) {
      return { code: 'INTERNAL', message: 'no task', success: false };
    }
    const result = await createSyncSchedule(scheduleTask.id, input);
    if (result.success) {
      message.success('定时规则已创建');
      void refreshSchedules();
    } else {
      message.error(`创建失败：${result.message}`);
    }
    return result;
  };

  const handleUpdateSchedule = async (id: number, input: SyncScheduleInput): Promise<ApiResult<SyncSchedule>> => {
    if (!scheduleTask) {
      return { code: 'INTERNAL', message: 'no task', success: false };
    }
    const result = await updateSyncSchedule(scheduleTask.id, id, input);
    if (result.success) {
      message.success('定时规则已更新');
      void refreshSchedules();
    } else {
      message.error(`更新失败：${result.message}`);
    }
    return result;
  };

  const handleDeleteSchedule = async (id: number) => {
    if (!scheduleTask) return;
    const result = await deleteSyncSchedule(scheduleTask.id, id);
    if (result.success) {
      message.success('已删除');
      void refreshSchedules();
    } else {
      message.error(`删除失败：${result.message}`);
    }
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
          {/* v0.6.11（SYNC-1/4）：以 DB 为准的「运行中」标记——刷新页面后依然在。 */}
          {row.lastRunStatus === 'running' && (
            <Tag color="processing" icon={<ClockCircleOutlined />}>运行中</Tag>
          )}
          {/* v0.6.11：当前正在拉的 (repo, tag) — 解「卡住了?」的可视化。*/}
          {row.lastRunStatus === 'running' && row.lastRunCurrentRepo && (
<Tooltip title={`当前正在拉取${row.lastRunCurrentTag ? ` ${row.lastRunCurrentRepo}:${row.lastRunCurrentTag}` : ` ${row.lastRunCurrentRepo} (列出 tag 中)`}`}>
              <Tag color="blue" style={{ fontFamily: 'monospace', fontSize: 11 }}>
<SyncOutlined spin /> {row.lastRunCurrentRepo}{row.lastRunCurrentTag ? `:${row.lastRunCurrentTag}` : ''}
              </Tag>
            </Tooltip>
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
      // v0.7.23: 加「中止」图标,200 → 230px。
      width: 230,
      render: (_, task) => {
        /** v0.6.11（SYNC-1/4）：后台还在跑 → 按钮置灰,防重复发起（后端 409 兜底）。 */
        const running = task.lastRunStatus === 'running';
        /*
         * 五个动作全改成纯图标 + Tooltip:
         *   - 删除文字标签后,280px 列宽可以压到 200px,不再被「运行」「编辑」等字撑爆
         *   - 删除按钮仍走 Popconfirm 二段确认（高危动作不能一健下去）
         *   - v0.6.10 SYNC-4 的「disabled 包 <span>」技巧对运行按钮仍需要
         *
         * v0.7.23: 新增「中止」图标,只在 running 时显示 —— 让 user 在大
         * 镜像(2GB+ / 30+ 层)拉一半时能主动终止,不必等满 timeout。
         * 二段确认(modal.confirm 在 handleCancel 里)防止手滑。
         */
        return (
          <Space size={0}>
            <Tooltip title={running ? '正在同步中，跑完才能再次发起' : '立即运行（异步受理）'}>
              <span>
                <Button
                  size="small"
                  type="text"
                  icon={<PlayCircleOutlined />}
                  loading={runningId === task.id}
                  disabled={running}
                  onClick={() => void handleRun(task)}
                  aria-label="运行"
                />
              </span>
            </Tooltip>
            {/*
              v0.7.23: 中止按钮。running=true 时可见,其余时候不渲染 —
              不渲染而不是 disabled,避免空 placeholder 撑出列宽。
              antd Button 默认 type='text',显式标 danger=true 是为了
              「中止」语义明显(红色);handler 里有 modal.confirm 二段
              确认,所以这里点一下不会立刻发请求。
            */}
            {running && (
              <Tooltip title="中止当前运行（高危）">
                <Button
                  size="small"
                  type="text"
                  danger
                  icon={<PauseCircleOutlined />}
                  loading={cancellingId === task.id}
                  onClick={() => void handleCancel(task)}
                  aria-label="中止"
                />
              </Tooltip>
            )}
            <Tooltip title="编辑任务">
              <Button
                size="small"
                type="text"
                icon={<EditOutlined />}
                onClick={() => openEdit(task)}
                aria-label="编辑"
              />
            </Tooltip>
            <Tooltip title="定时规则">
              <Button
                size="small"
                type="text"
                icon={<CalendarOutlined />}
                onClick={() => void openSchedules(task)}
                aria-label="定时"
              />
            </Tooltip>
            {/*
             * v0.6.20: 「运行历史」按钮删除 —— 历史现在是 Task 行直接展开,
             * 不再需要单独按钮弹 Modal。「点击行展开」是 Table.expandable 的
             * 默认行为(行左侧 + 按钮),跟 actions 区的图标按钮不冲突。
             */}
            <Popconfirm
              title={`删除任务 "${task.name}"？`}
              okText="删 除"
              okButtonProps={{ danger: true }}
              cancelText="取 消"
              onConfirm={() => handleDelete(task)}
            >
              <Tooltip title="删除任务">
                <Button
                  size="small"
                  type="text"
                  danger
                  icon={<DeleteOutlined />}
                  aria-label="删除"
                />
              </Tooltip>
            </Popconfirm>
          </Space>
        );
      },
    },
  ];

  // ── Run 列（v0.6.20:从 Modal 内列变成 Task 展开区里的内层 Run Table 列） ───

  const runColumns: ColumnsType<SyncRun> = [
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
        <Space direction="vertical" size={0}>
          <Space size={4}>
            <span>成功 <strong style={{ color: '#52c41a' }}>{run.reposSynced}</strong></span>
            {run.reposTotal > 0 && <span style={{ color: '#999' }}>/ {run.reposTotal}</span>}
            {run.reposFailed > 0 && (
              <span style={{ color: '#cf1322' }}>· 失败 <strong>{run.reposFailed}</strong></span>
            )}
          </Space>
          {/* v0.6.11：失败 / 运行中 run 显示最后/当前位置——避免「卡住了?」+ 失败定位。*/}
          {run.currentRepo && run.status !== 'success' && (
            <span style={{ fontSize: 11, color: run.status === 'running' ? '#1677ff' : '#cf1322', fontFamily: 'monospace' }}>
              {run.status === 'running' ? '正在' : '死在'} {run.currentRepo}{run.currentTag ? `:${run.currentTag}` : ''}
            </span>
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

  // ── 历史明细列（v0.6.16）─────────────────────────────────────────────

  /**
   * 内层 items 表的列。展开某条 run 时挂在 expandedRowRender 里,
   * 共享 historyItemsState / 翻页状态。
   *
   * 列宽合计 ~640px,正好填进 Modal width=800 的内边距里 —— 任何窄屏
   * 命中也不会再出 antd 默认的横滚条(列宽都已显式 width)。
   */
  const itemColumns: ColumnsType<SyncRunItem> = [
    {
      title: '仓库',
      dataIndex: 'repository',
      key: 'repository',
      width: 220,
      ellipsis: true,
      render: (v: string) => <span style={{ fontFamily: 'monospace', fontSize: 12 }}>{v}</span>,
    },
    {
      title: 'tag',
      dataIndex: 'tag',
      key: 'tag',
      width: 120,
      ellipsis: true,
      render: (v: string) => <span style={{ fontFamily: 'monospace', fontSize: 12 }}>{v || '—'}</span>,
    },
    {
      title: '结果',
      dataIndex: 'state',
      key: 'state',
      width: 90,
      render: (state: SyncRunItemState) => (
        <Tag color={itemStateColor(state)} icon={itemStateIcon(state)}>
          {itemStateLabel(state)}
        </Tag>
      ),
    },
    {
      title: '耗时',
      key: 'duration',
      width: 150,
      render: (_, item) => {
        if (!item.finishedAt) {
          // v0.7.25: 还在跑的时候也展示 timeout chip —— 如果是 long/extra,
          // 运维看着 6 分钟没动静也不会怀疑 hang。这是 chip 真正的
          // 用途所在。
          if (item.timeoutUsed === 'extra') {
            return (
              <Space size={4}>
                <span style={{ color: '#999' }}>—</span>
                <Tooltip title="manifest > 10 GiB(超大镜像,如 vllm 24 GB),引擎使用 2 小时 deadline">
                  <Tag color="red" style={{ margin: 0 }}>2h</Tag>
                </Tooltip>
              </Space>
            );
          }
          if (item.timeoutUsed === 'long') {
            return (
              <Space size={4}>
                <span style={{ color: '#999' }}>—</span>
                <Tooltip title="manifest > 1 GiB,引擎使用 30 分钟 deadline">
                  <Tag color="orange" style={{ margin: 0 }}>30min</Tag>
                </Tooltip>
              </Space>
            );
          }
          return <span style={{ color: '#999' }}>—</span>;
        }
        const ms = new Date(item.finishedAt).getTime() - new Date(item.startedAt).getTime();
        // v0.7.27: 三档 chip —— extra=红 2h / long=橙 30min / default & push = 无。
        if (item.timeoutUsed === 'extra') {
          return (
            <Space size={4}>
              <span style={{ fontFamily: 'monospace' }}>{formatDurationMs(ms)}</span>
              <Tooltip title="manifest > 10 GiB(超大镜像,如 vllm 24 GB),引擎使用 2 小时 deadline">
                <Tag color="red" style={{ margin: 0 }}>2h</Tag>
              </Tooltip>
            </Space>
          );
        }
        if (item.timeoutUsed === 'long') {
          return (
            <Space size={4}>
              <span style={{ fontFamily: 'monospace' }}>{formatDurationMs(ms)}</span>
              <Tooltip title="manifest > 1 GiB,引擎使用 30 分钟 deadline">
                <Tag color="orange" style={{ margin: 0 }}>30min</Tag>
              </Tooltip>
            </Space>
          );
        }
        return <span style={{ fontFamily: 'monospace' }}>{formatDurationMs(ms)}</span>;
      },
    },
    {
      title: '字节',
      key: 'bytes',
      width: 100,
      render: (_, item) => {
        if (item.bytesTotal === 0) return <span style={{ color: '#999' }}>—</span>;
        // 失败时 bytesDone=0,bytesTotal 是声明的 manifest 大小 ——
        // 用「0 B / X」的形式让用户区分「压根没下载」和「下载了一半」。
        const done = formatBytes(item.bytesDone);
        const total = formatBytes(item.bytesTotal);
        return (
          <span style={{ fontFamily: 'monospace', fontSize: 12, color: item.bytesDone === 0 && item.bytesTotal > 0 ? '#cf1322' : undefined }}>
            {done} / {total}
          </span>
        );
      },
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

  /**
   * v0.6.16: 历史 Modal 的展开行内容。
   *
   * 三种状态:
   *   1. 首次展开(itemsByRunId[runId] 为空) — 触发 loadRunItems,显示骨架
   *   2. 加载完成 — 渲染内层 Table,带分页 (已加载 < total 时显示「加载更多」按钮)
   *   3. 0 条 — empty state,提示「该 run 没有明细」
   *
   * 容器用浅色背景 + 左内边距让「展开区」与外层 run 表视觉区分。
   */
  const renderExpandedItems = (taskId: number, run: SyncRun) => {
    const state = itemsByRunId[run.id];
    const loaded = state?.items ?? [];
    const total = state?.total ?? 0;
    const loading = state?.loading ?? false;
    const loadedCount = loaded.length;
    return (
      <div
        style={{
          background: 'var(--color-fill-quaternary, rgba(0,0,0,0.02))',
          padding: '8px 8px 12px 32px',
          margin: '0 -8px',
        }}
      >
        {!state && (
          <div style={{ padding: 16, color: '#999' }}>加载中…</div>
        )}
        {state && loadedCount === 0 && total === 0 && !loading && (
          <Empty
            image={Empty.PRESENTED_IMAGE_SIMPLE}
            description="该 run 没有明细（旧版本引擎或运行中尚未落库）"
          />
        )}
        {state && (loadedCount > 0 || total > 0) && (
          <>
            <Table<SyncRunItem>
              rowKey="id"
              size="small"
              columns={itemColumns}
              dataSource={loaded}
              pagination={false}
              loading={loading && loadedCount === 0}
            />
            {/*
             * v0.6.20: 不再有「加载更多」按钮。0.6.16 那版 「加载更多」 +
             * Pagination 同时存在 — 翻页就能 append,「加载更多」是冗余动作。
             * 现在翻页 = 整页替换,简单一致。
             *
             * 同步的「分页高亮 bug」修复在这里:0.6.18 用 loadedCount 推算
             * currentPage,最后一页不满 50 条时会算出 1(把第 5 页算成第 1
             * 页)。现在 currentPage 直接读 `currentPageByRunId[run.id]`,
             * loadRunItems 加载完成后由它写回真值。
             */}
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 8 }}>
              <span style={{ fontSize: 12, color: '#999' }}>
                共 {total} 条 · 每页 {ITEMS_PAGE_SIZE} 条
              </span>
              {total > ITEMS_PAGE_SIZE && loadedCount > 0 && (
                <Pagination
                  size="small"
                  current={currentPageByRunId[run.id] ?? 1}
                  pageSize={ITEMS_PAGE_SIZE}
                  total={total}
                  showSizeChanger={false}
                  onChange={(p) => void loadRunItems(taskId, run.id, { jumpTo: p })}
                />
              )}
            </div>
          </>
        )}
      </div>
    );
  };

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

      {/*
       * v0.6.22: 三态渲染从 ternary 改成「同时渲染 + 受控 visible」。
       *   - loading=true 时 Table 用 hidden=true 隐身(spinner 占据位置)
       *   - loading=false 时 Table 露脸;若 tasks 为空,Empty 接管(也是
       *     隐身状态下渲染,这里 `!loading` 决定 Empty 何时出现)
       *   - PageLoading 永远在末尾:DOM 顺序上画在 Table/Empty 上面,
       *     内部状态机 visible→hiding→unmount 走 250ms+350ms 淡出。
       * 视觉上:spinner → spinner 半透明(底下表格露脸) → 表格,不是硬切。
       */}
      {tasks.length === 0 && !loading ? (
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
        <div style={{ position: 'relative', minHeight: 200 }}>
        <div hidden={loading}>
        <Table<SyncTask>
          rowKey="id"
          columns={columns}
          dataSource={visibleTasks}
          pagination={{ pageSize: 20, showSizeChanger: false }}
          size="middle"
          /*
           * v0.6.20: Task Table 加 expand 列,把"点按钮弹 Modal → 二次展开"
           * 收口成"主屏一行直接展开",去掉原 0.6.16-0.6.19 的 historyTask/
           历史Modal。
           *
           * expandedTaskIds 控制展开,onExpand 触发懒加载 runs + 把 taskId
           * 加进 expandedTaskIds (避免重复 fetch)。
           *
           * 展开区渲染内层 Run Table,Run Table 自己也 expandable,展开
           * 单条 run 时触发 loadRunItems。runColumns / renderExpandedItems
           * 从原 Modal 复用,只把 taskId 改成从外层闭包传入。
           */
          expandable={{
            expandedRowKeys: expandedTaskIds,
            onExpand: (open, task) => {
              if (open) {
                setExpandedTaskIds((prev) =>
                  prev.includes(task.id) ? prev : [...prev, task.id],
                );
                void loadTaskRuns(task.id);
              } else {
                setExpandedTaskIds((prev) => prev.filter((id) => id !== task.id));
              }
            },
            expandedRowRender: (task) => {
              const runsState = runsByTaskId[task.id];
              if (!runsState) return <div className="expanded-row-anim"><PageLoading visible tip="正在读取运行历史…" /></div>;
              if (runsState.loading && runsState.runs.length === 0) {
                return <div className="expanded-row-anim"><PageLoading visible tip="正在读取运行历史…" /></div>;
              }
              if (runsState.runs.length === 0) {
                return <div className="expanded-row-anim"><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="还没有运行记录" /></div>;
              }
              return (
                /*
                 * v0.6.23: 外层 div 加 .expanded-row-anim —— 让 Task 展开区(Run
                 * Table + 内容)在挂载时走 250ms opacity+translateY(-4px) → 0
                 * ease-out 动画,跟 PageLoading 淡出节奏对齐,过场更顺。
                 *
                 * 内层 Run Table 也是 expandable,展开单条 run 触发 loadRunItems。
                 * taskId 从外层闭包传入 —— renderExpandedItems 需要它来调
                 * /api/sync/{taskId}/runs/{runId}/items。
                 */
                <div className="expanded-row-anim">
                {/*
                 * v0.6.30: 「仅保留最近 10 条历史」提示 —— 后端 CreateRun 末尾 trim,
                 * 每个 task 只留最近 10 条 sync_runs(CASCADE 带走 run_items)。
                 * 常驻小灰字,不强提示,让用户知道「为什么看不到更早的」。
                 */}
                <div style={{ fontSize: 12, color: '#999', margin: '0 0 6px 4px' }}>
                  仅保留最近 {RUN_HISTORY_LIMIT} 条历史
                </div>
                <Table<SyncRun>
                  rowKey="id"
                  columns={runColumns}
                  dataSource={runsState.runs}
                  pagination={false}
                  size="small"
                  expandable={{
                    expandedRowKeys: expandedRunIds,
                    onExpand: (open, run) => {
                      if (open) {
                        setExpandedRunIds((prev) =>
                          prev.includes(run.id) ? prev : [...prev, run.id],
                        );
                        if (!itemsByRunId[run.id]) {
                          void loadRunItems(task.id, run.id, 1);
                        }
                      } else {
                        setExpandedRunIds((prev) => prev.filter((id) => id !== run.id));
                      }
                    },
                    expandedRowRender: (run) => (
                      <div className="expanded-row-anim">
                        {renderExpandedItems(task.id, run)}
                      </div>
                    ),
                  }}
                />
                </div>
              );
            },
          }}
        />
        </div>
        </div>
      )}
      <PageLoading visible={loading} tip="正在读取同步任务…" />

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
                 * v0.6.11（SYNC-3）：按当前档位前置拦截——探测得真拿得到用户名密码,
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
                setTesting(true);
                try {
                  const result = await testSyncConnection({
                    remoteUrl: cur.remoteUrl ?? '',
                    remoteCredentialId:
                      credMode === 'credential' ? (cur.remoteCredentialId ?? '') : '',
                    remoteUsername: credMode === 'inline' ? (cur.remoteUsername ?? '') : '',
                    remotePassword: credMode === 'inline' ? (cur.remotePassword ?? '') : '',
                  });
                  // v0.6.13 (UI): 结果改用 toast 而不是 modal 顶部内嵌 Alert。
                  // 内嵌 Alert 会撑高 modal、覆盖 form 多出一列内容,在小屏
                  // 上甚至撑出滚动条(operator 改完 URL/凭据后想立刻看结果,
                  // 滚到 Alert 之前还得先滚一下 form);toast 跟「探测 N 个代理」
                  // 等其他动作共用一条 message channel,形态统一。
                  if (result.success && result.data) {
                    const r = result.data;
                    if (
                      r.authStatus === 'ok' ||
                      r.authStatus === 'no_auth_required'
                    ) {
                      message.success(
                        r.reachable
                          ? `${r.message},可达 HTTP ${r.httpStatus}`
                          : r.message,
                      );
                    } else if (
                      r.authStatus === 'wrong_creds' ||
                      r.authStatus === 'required_but_missing'
                    ) {
                      message.error(`认证失败:${r.message} (HTTP ${r.httpStatus})`);
                    } else if (r.authStatus === 'not_registry') {
                      message.warning(`URL 不像 registry:${r.message}`);
                    } else {
                      message.info(r.message);
                    }
                  } else {
                    message.error(`探测失败：${result.message}`);
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
        {/* v0.6.13 (UI): 探测结果不再挂在 modal 顶部 —— 见「测试连接」按钮
            onClick 里的 toast 注释块。结果改用 message.X() 弹窗,
            不占 modal 内部垂直空间,小屏不再撑出滚动条。 */}

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
            v0.6.11（SYNC-3）：远端凭据改成「三档 Radio + 条件渲染」。
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

          {/*
            v0.7.21: 精确 (repo, tag) 清单 —— 远端 registry 对匿名账号返 401 insufficient_scope
            拒列 /v2/_catalog 时(典型: TCR / Harbor),docker pull 能直读 manifest,但
            cairn 的 sync engine 必须 list catalog 才能按 include glob 过滤。把想同步的
            精确 ref 写这里,引擎直接 fetch manifest 跳过 catalog。留空 = 走上面的
            Include 模式(原行为)。

            文案里点出「互斥风格」但实现不强制 —— 用户同时填两个字段时,当前会优先走
            tags_filter(因为 runPull 入口先看它),include 被忽略。这个判定在 runPull
            的注释里有写明,文档 CHANGELOG v0.7.21 会再说明一次。

            v0.7.22: TextArea 下方实时显示解析结果 —— 「✓ N 条有效」+ 跳过的
            行(具体原因 + 行号 + 原文)。直接调 parseTagsFilter(同款 Go 端
            实现,见 ../lib/parse-tags-filter.ts),无后端往返,用户输入时 0
            卡顿。allBlank 区分"用户没填"vs"用户填了但全无效"两种情况,
            给不同提示文案。
          */}
          <Form.Item
            name="tagsFilter"
            label="Tags 过滤（精确清单）"
            extra={
              <span style={{ fontSize: 12, color: '#999' }}>
                一行一条 <code>repo:tag</code>（可写 <code>#</code> 开头的注释行）。留空 = 走 Include 模式。
                典型场景：远端不允许列 catalog，但仍想拉固定的几个镜像。例：<code>bklite/alpine/openssl:3.5.4</code>。
              </span>
            }
          >
            <Input.TextArea
              rows={3}
              placeholder={'bklite/alpine/openssl:3.5.4\n# 注释行会被跳过'}
            />
          </Form.Item>
          {/*
            v0.7.22: 实时解析预览 —— Form.useWatch 拿到 tagsFilter 字段当前值,
            喂给 parseTagsFilter,渲染 valid 数量 + skipped 行(可点击定位)。
            没值 / 没填 → 不渲染这个区域,避免在刚打开 modal 时多一个空 alert。
          */}
          <Form.Item shouldUpdate noStyle>
            {() => {
              const raw = (form.getFieldValue('tagsFilter') as string | undefined) ?? '';
              if (raw.trim() === '') return null;
              const parsed = parseTagsFilter(raw);
              const reasonLabel: Record<string, string> = {
                blank: '空行',
                comment: '注释',
                no_tag: '没写 tag',
                empty_repo: '空 repo',
                empty_tag: '空 tag',
              };
              return (
                <div
                  style={{
                    marginTop: -16,
                    marginBottom: 16,
                    fontSize: 12,
                    lineHeight: 1.6,
                  }}
                >
                  <div style={{ color: parsed.valid.length > 0 ? '#389e0d' : '#cf1322' }}>
                    ✓ {parsed.valid.length} 条有效 spec
                  </div>
                  {parsed.skipped.length > 0 && (
                    <div style={{ color: '#cf1322', marginTop: 4 }}>
                      ✗ 跳过 {parsed.skipped.length} 条:
                      <ul style={{ margin: '4px 0 0 0', paddingLeft: 20 }}>
                        {parsed.skipped.map((s) => (
                          <li key={s.lineNumber}>
                            L{s.lineNumber}{' '}
                            <code style={{ fontSize: 11 }}>{s.raw || '(空白)'}</code>
                            {' '}
                            <span style={{ color: '#999' }}>({reasonLabel[s.reason] ?? s.reason})</span>
                          </li>
                        ))}
                      </ul>
                    </div>
                  )}
                </div>
              );
            }}
          </Form.Item>

          {/*
            v0.7.24 撤掉「长 Timeout 镜像」input —— engine 现在按 manifest
            size 自动切 timeout(> 1GB 走 30min,否则 5min),user 不需要配置。
            后端 SyncTaskInput.longTimeoutRepos 字段保留兼容(omitempty),
            旧任务读回空串走 5min 默认。
          */}

          <Form.Item name="enabled" label="启用" valuePropName="checked">
            <Switch checkedChildren="启用" unCheckedChildren="停用" />
          </Form.Item>
        </Form>
      </Modal>

      {/*
       * v0.6.20: 历史 Modal 删除 —— 见 Task Table 的 expandable。
       *
       * 原来:任务列表点「运行历史」按钮 → 弹 Modal → 在 Modal 里再展开 run →
       * 看到 (repo, tag) 明细。两段式(弹窗 + 下拉)用户反馈「很突兀」。
       *
       * 现在:Task 表行直接展开 → 内嵌 Run 表 → Run 表行再展开 → (repo, tag)
       * 明细。所有交互在主页面,跟任务列表主体语境一致。state 也从
       * historyTask/historyRuns/historyLoading 三个全局态替换为 runsByTaskId
       * 按 task 缓存,支持多 task 同时展开对比。
       */}

      {/* 定时 Modal (v0.6.11) */}
      <Modal
        title={scheduleTask ? `定时：${scheduleTask.name}` : ''}
        open={scheduleTask !== null}
        onCancel={closeSchedules}
        footer={<Button onClick={closeSchedules}>关 闭</Button>}
        width={780}
        destroyOnClose
      >
        <ScheduleTab
          schedules={schedules}
          loading={schedulesLoading}
          onCreate={handleCreateSchedule}
          onUpdate={handleUpdateSchedule}
          onDelete={handleDeleteSchedule}
        />
      </Modal>
    </div>
  );
}

/**
 * 把后端错误翻译到表单字段错误上（name 重名 / 凭据冲突等）。
 *
 * v0.6.11：改成返回「是否已定位到字段」。调用方在失败且返回 false 时补一条全局
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
   * v0.6.11（SYNC-3）：凭据三态的冲突 / 找不到。带 "credential" 的两种文案
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
 * v0.6.16: sync_run_items.state → antd Tag 颜色 / 图标 / 文案。
 *
 * 与外层 SyncRun 的色板保持一致:
 *   - succeeded → green + CheckCircle
 *   - failed    → red + CloseCircle
 *   - cancelled → default + PauseCircle（当前不会写入，保留给未来）
 */
function itemStateColor(state: SyncRunItemState): string {
  switch (state) {
    case 'succeeded': return 'success';
    case 'failed':    return 'error';
    case 'cancelled': return 'default';
  }
}
function itemStateIcon(state: SyncRunItemState) {
  switch (state) {
    case 'succeeded': return <CheckCircleOutlined />;
    case 'failed':    return <CloseCircleOutlined />;
    case 'cancelled': return <PauseCircleOutlined />;
  }
}
function itemStateLabel(state: SyncRunItemState): string {
  switch (state) {
    case 'succeeded': return '成功';
    case 'failed':    return '失败';
    case 'cancelled': return '取消';
  }
}

/**
 * v0.6.16: bytes 的人类可读格式。「镜像层合计」是各 layer + config
 * 大小之和,所以同一镜像 pull / push 显示的是同一个数。这里跟代理
 * 延迟共用 formatLatency 但用「formatBytes」单独实现 — 它们语义
 * 不一样(proxy latency 是 ms 数,这里是 byte 数),别共用一个函数。
 */
function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return v >= 10 ? `${Math.round(v)} ${units[i]}` : `${v.toFixed(1)} ${units[i]}`;
}

/**
 * v0.6.13 (UI): 探测结果的视觉等级（success / error / warning / info）
 * 走 message.X() toast,等级直接体现在调用上（message.success / error /
 * warning / info）。v0.6.5 留下的 probeAlertType 函数已无 caller,删除
 * 以免误导后人去维护一条没在跑的代码路径。
 */

// ── ScheduleTab（v0.6.11）──────────────────────────────────────────────
//
// Modal 内嵌的子组件：列出 schedules + 内嵌新建/编辑表单。
// 与 EditTaskModal 共用 pattern：每次只允许编辑一行（新建或编辑现有）。

interface ScheduleTabProps {
  schedules: SyncSchedule[];
  loading: boolean;
  onCreate: (input: SyncScheduleInput) => Promise<ApiResult<SyncSchedule>>;
  onUpdate: (id: number, input: SyncScheduleInput) => Promise<ApiResult<SyncSchedule>>;
  onDelete: (id: number) => Promise<void>;
}

/**
 * 「定时」Modal 的内容。
 * 上半：每条 schedule 一行 — cron / 时区 / 启用 / 下次触发时间 / 上次 / 编辑+删除按钮。
 * 下半：「新建规则」表单（收起状态：显示一个「新建」按钮）。
 * 单一编辑：编辑某条时,Form 出现,保存或取消。
 */
function ScheduleTab({ schedules, loading, onCreate, onUpdate, onDelete }: ScheduleTabProps) {
  const [editing, setEditing] = useState<{ id: number; input: SyncScheduleInput } | null>(null);
  const [creating, setCreating] = useState(false);

  const columns: ColumnsType<SyncSchedule> = [
    {
      title: '启用',
      dataIndex: 'enabled',
      key: 'enabled',
      width: 70,
      render: (on: boolean) => (on ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>),
    },
    {
      title: '频率',
      dataIndex: 'cronExpr',
      key: 'cronExpr',
      /*
       * v0.6.30: 列名从「cron」改成「频率」,渲染从 raw cron 表达式改成
       * cronSummary —— 中文摘要(每日 03:30 / 每周一 03:30 / 每月 1 日 03:30
       * / 每小时 第 30 分 / 自定义: * * * * *)。「自定义」分支保留 raw 表达式
       * 给运维一眼看到。规则定义见 ScheduleKind / cronSummary。
       */
      render: (e: string) => {
        const summary = cronSummary(e);
        const isCustom = summary.startsWith('自定义:');
        return isCustom ? (
          <Tooltip title={e}>
            <Tag color="orange">{summary}</Tag>
          </Tooltip>
        ) : (
          <span style={{ fontSize: 12 }}>{summary}</span>
        );
      },
    },
    // v0.6.31: 时区列删除 —— cairn 永远用 Asia/Shanghai 评估 cron,
    // UI 上展示「时区」会让切换日月(每日/每周/每月)时整行长度跳变、
    // 触发换行,UX 体验差。删除后,整行宽度由「启用 / 频率 / 下次触发 /
    // 上次 / 操作」5 列构成,row-level 宽度稳定。
    // 列表里没有时区列,运维如果想知道,看 DB sync_schedules.timezone 列
    // (永远是 Asia/Shanghai)。
    {
      // v0.6.31: 时区列删除后,「下次触发」从原来 160 减到 140(去掉「UTC」/ 时区字符串所占宽度),
      // 让整行总宽不变。详细原因见 ScheduleTab columns 上方注释。
      title: '下次触发',
      dataIndex: 'nextRunAt',
      key: 'nextRunAt',
      width: 140,
      render: (iso: string) => <span style={{ fontSize: 12 }}>{formatRelative(iso)}</span>,
    },
    {
      title: '上次',
      key: 'last',
      width: 140,
      render: (_, s) =>
        s.lastRunAt ? (
          <Tooltip title={s.lastRunAt}>
            <span style={{ fontSize: 12, color: '#666' }}>{formatRelative(s.lastRunAt)}</span>
          </Tooltip>
        ) : (
          <span style={{ color: '#999' }}>—</span>
        ),
    },
    {
      title: '操作',
      key: 'actions',
      width: 140,
      render: (_, s) => (
        <Space size={4}>
          <Button
            size="small"
            type="text"
            onClick={() =>
              setEditing({
                id: s.id,
                // v0.6.31: 不再带 timezone 字段 —— 后端永远 Asia/Shanghai,前端不必传。
              input: { cronExpr: s.cronExpr, enabled: s.enabled },
              })
            }
          >
            编辑
          </Button>
          <Popconfirm title="删除这条规则？" onConfirm={() => void onDelete(s.id)}>
            <Button size="small" type="text" danger>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <div>
      {schedules.length === 0 && !loading ? (
        <Empty description="还没有定时规则，点下方「新建」添加" />
      ) : (
        <div style={{ position: 'relative', minHeight: 120 }}>
        <div hidden={loading}>
        <Table<SyncSchedule>
          rowKey="id"
          columns={columns}
          dataSource={schedules}
          pagination={false}
          size="small"
        />
        </div>
        </div>
      )}
      <PageLoading visible={loading} tip="正在读取定时规则…" />

      <Divider style={{ margin: '16px 0' }} />

      {creating ? (
        <ScheduleRowEditor
          mode="create"
          onCancel={() => setCreating(false)}
          onSubmit={async (input) => {
            const r = await onCreate(input);
            if (r.success) setCreating(false);
          }}
        />
      ) : editing ? (
        <ScheduleRowEditor
          mode="edit"
          initial={editing.input}
          onCancel={() => setEditing(null)}
          onSubmit={async (input) => {
            const r = await onUpdate(editing.id, input);
            if (r.success) setEditing(null);
          }}
        />
      ) : (
        <Button type="dashed" block icon={<PlusOutlined />} onClick={() => setCreating(true)}>
          新建定时规则
        </Button>
      )}
    </div>
  );
}

interface ScheduleRowEditorProps {
  mode: 'create' | 'edit';
  initial?: SyncScheduleInput;
  onCancel: () => void;
  onSubmit: (input: SyncScheduleInput) => Promise<void>;
}

/**
 * 单行编辑器：v0.6.30 起从 cron 表达式文本框改成 4 档下拉。
 *
 *   频率下拉:每小时 / 每日 / 每周 / 每月
 *   条件子选择:
 *     - 每小时:分钟(0-59)
 *     - 每日:  时(0-23) + 分(0-59)
 *     - 每周:  时 + 分 + 周几(一-日)
 *     - 每月:  时 + 分 + 几号(1-31)
 *   时区(可选,默认 UTC) + 启用开关
 *
 * 「保存」直接调 onSubmit,后端 Validate 失败会弹 message;前端不做客户端
 * cron 解析(解析器在 internal/sync/cron.go,后端是唯一权威)。前端只负责
 * kindToCron 把下拉状态组装成 5 字段字符串。
 *
 * 存量「自定义」cron(没匹配 4 个 pattern 的,例如 `star-slash 15` 或 `0 0 star star 1-5`):
 *   - 顶部 Alert 提示「这是历史自定义 cron 规则,保存会被下拉选定的规则覆盖」
 *   - 下拉初始值默认「每日」,但 initial cron 不变,只是显示警告
 *   - 用户主动选择 4 档之一 + 保存,原自定义 cron 被覆盖
 *   - 不主动提供「自定义」保留编辑 —— 一旦猜错,代价太高(参见 parseCron 注释)
 */
function ScheduleRowEditor({ mode, initial, onCancel, onSubmit }: ScheduleRowEditorProps) {
  const initialParsed = initial?.cronExpr ? parseCron(initial.cronExpr) : null;
  /*
   * 编辑「自定义」时,下拉默认值定「每日」+ 00:00 —— 历史 cron 没法反推,
   * 给一个明显的「改了才知道要选」的状态,配合 Alert 提示用户:
   * 「保存会变成下方选定的规则,原自定义 cron 没了」。
   */
  const [kind, setKind] = useState<ScheduleKind>(initialParsed?.kind ?? 'daily');
  const [hour, setHour] = useState<number>(initialParsed?.hour ?? 0);
  const [minute, setMinute] = useState<number>(initialParsed?.minute ?? 0);
  // v0.7.8: 单一「每()分钟」档的步长,范围 1-59,默认 1。
  //   1 → "* * * * *"(每分钟)
  //   N≥2 → "*/N * * * *"(运维 */5 / */15 / */30)
  // 默认 1 不再是 15(v0.7.5-v0.7.7 是 15,现在 UI 第一档直接是「每分钟」)。
  const [stepMinutes, setStepMinutes] = useState<number>(initialParsed?.stepMinutes ?? 1);
  const [dayOfWeek, setDayOfWeek] = useState<number>(initialParsed?.dayOfWeek ?? 1);
  const [dayOfMonth, setDayOfMonth] = useState<number>(initialParsed?.dayOfMonth ?? 1);
  // v0.6.31: 时区输入框删除 —— cairn 永远用 Asia/Shanghai 评估 cron,
  // 让用户在 UI 上选时区会让切换日月(每日/每周/每月)时整行长度跳变、
  // 触发换行。运维要看时区,看 DB sync_schedules.timezone 列(永远是 Asia/Shanghai)。
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [submitting, setSubmitting] = useState(false);

  /*
   * initial.cronExpr 存在 + 解析不出 4 档之一 → 标记为「自定义」,顶部
   * 展示 Alert。mode=create 时永远 false(没有 initial)。
   */
  const isCustom = mode === 'edit' && !!initial?.cronExpr && initialParsed === null;
  const previewCron = kindToCron({ kind, minute, hour, stepMinutes, dayOfWeek, dayOfMonth });

  return (
    <Space direction="vertical" style={{ width: '100%' }} size={12}>
      {isCustom ? (
        <Alert
          type="warning"
          showIcon
          message="这是历史自定义 cron 规则"
          description={
            <>
              原始表达式: <code style={{ background: 'rgba(0,0,0,0.05)', padding: '1px 6px', borderRadius: 3 }}>{initial?.cronExpr}</code>
              <br />
              保存时会替换为下方下拉选定的规则(<code>{previewCron}</code>)。
              如需保留原表达式,请勿保存,直接取消。
            </>
          }
        />
      ) : null}
      <Space wrap>
        <Select
          value={kind}
          onChange={setKind}
          options={KIND_OPTIONS}
          style={{ width: 120 }}
        />
        {kind === 'perNMinutes' ? (
          // v0.7.8: 单一「每()分钟」档,数可填。N=1 → "* * * * *";
          // N≥2 → "*/N * * * *"。范围 1-59,默认 1。
          // 跟 v0.7.5-v0.7.7 的双档「每分钟 + 每 N 分钟」合一了,只一个下拉项。
          <InputNumber
            min={1}
            max={59}
            value={stepMinutes}
            onChange={(v) => setStepMinutes(typeof v === 'number' ? v : 1)}
            addonAfter="分钟一次"
            style={{ width: 160 }}
            placeholder="N"
          />
        ) : kind === 'hourly' ? (
          // 每小时只选分
          <InputNumber
            min={0}
            max={59}
            value={minute}
            onChange={(v) => setMinute(typeof v === 'number' ? v : 0)}
            addonAfter="分"
            style={{ width: 140 }}
            placeholder="分"
          />
        ) : (
          // 每日 / 每周 / 每月:选时 + 分
          <>
            <InputNumber
              min={0}
              max={23}
              value={hour}
              onChange={(v) => setHour(typeof v === 'number' ? v : 0)}
              addonAfter="时"
              style={{ width: 140 }}
              placeholder="时"
            />
            <InputNumber
              min={0}
              max={59}
              value={minute}
              onChange={(v) => setMinute(typeof v === 'number' ? v : 0)}
              addonAfter="分"
              style={{ width: 140 }}
              placeholder="分"
            />
          </>
        )}
        {kind === 'weekly' ? (
          <Select
            value={dayOfWeek}
            onChange={setDayOfWeek}
            options={WEEKDAY_OPTIONS}
            style={{ width: 140 }}
          />
        ) : null}
        {kind === 'monthly' ? (
          <InputNumber
            min={1}
            max={31}
            value={dayOfMonth}
            onChange={(v) => setDayOfMonth(typeof v === 'number' ? v : 1)}
            addonAfter="日"
            style={{ width: 140 }}
            placeholder="几号"
          />
        ) : null}
        <Switch
          checkedChildren="启用"
          unCheckedChildren="停用"
          checked={enabled}
          onChange={setEnabled}
        />
      </Space>
      <Space>
        <Button
          type="primary"
          loading={submitting}
          onClick={async () => {
            setSubmitting(true);
            try {
              // v0.6.31: 不再传 timezone —— 后端永远 Asia/Shanghai。
              await onSubmit({
                cronExpr: previewCron,
                enabled,
              });
            } finally {
              setSubmitting(false);
            }
          }}
        >
          {mode === 'create' ? '创建' : '保存'}
        </Button>
        <Button onClick={onCancel}>取消</Button>
        <span style={{ color: '#999', fontSize: 12 }}>
          将保存为:&nbsp;
          <code style={{ background: 'rgba(0,0,0,0.05)', padding: '1px 6px', borderRadius: 3 }}>{previewCron}</code>
          &nbsp;→ {cronSummary(previewCron)}
        </span>
      </Space>
    </Space>
  );
}
