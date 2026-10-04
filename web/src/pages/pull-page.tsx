import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  App as AntdApp,
  Button,
  Descriptions,
  Empty,
  Form,
  Input,
  Modal,
  Progress,
  Radio,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  CloudDownloadOutlined,
  HourglassOutlined,
  LoadingOutlined,
  PauseCircleOutlined,
  PlusOutlined,
  StopOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import {
  cancelPullJob,
  createPullJob,
  listCredentials,
  listProxies,
  listPullJobs,
  probePullSource,
  removePullJob,
} from '../api';
import { useAppConfig } from '../config-store';
import type {
  ApiResult,
  AppConfig,
  Credential,
  DestStatus,
  ProxyEntry,
  PullJob,
  PullJobInput,
  PullJobStatus,
  PullPhase,
} from '../types';
import {
  DEST_REPO_PATTERN,
  DEST_TAG_PATTERN,
  formatBytes,
  formatDateTime,
  nextJobExpansion,
  parseImageReference,
  parseKnownHosts,
  shortDigest,
  splitRepoTag,
} from '../utils';
import type { SidebarGroup, SidebarItem, SidebarSelection } from '../components/page-sidebar';
import PageLoading from '../components/page-loading';

interface Props {
  config: AppConfig | null;
  /**
   * v0.5.37.4:从侧栏传入的当前选择（groupKey -> itemKey）。
   * 「状态」组选中某档 → 任务表按 status 过滤；「来源」组按 sourceUrl 的 host 过滤。
   */
  sidebarFilter: SidebarSelection;
  /** 把本页真实分组（含计数）上浮给 App，再统一下发给 PageSidebar。 */
  onPublishGroups: (groups: SidebarGroup[]) => void;
}

interface FormValues {
  image: string;       // 源镜像名（可能含主机前缀）
  destImage?: string;  // 本 registry 内的目标镜像名 <repo>[:<tag>]，不含主机
  // 来源 URL 不再让用户填 —— 镜像名含主机段自动识别，无主机段默认 Docker Hub;
  // 自建源 / 强制覆盖场景走设置页「第三方拉取源」（pull.known_hosts）。
  // 高级选项：源端代理（不用 / 从代理库选 / 临时输入）
  sourceProxyMode?: 'none' | 'library' | 'temp';
  sourceProxyId?: string;
  sourceProxy?: string;
  sourceAuthMode?: 'none' | 'credential' | 'temp';
  sourceCredentialId?: string;
  sourceTempUsername?: string;
  sourceTempPassword?: string;
}

const POLL_INTERVAL_MS = 1500;

/** 把凭据 id 翻译成「名称（账号）」以供预览/列表展示。 */
function resolveCredentialLabel(id: string, list: Credential[] = []): string {
  const c = list.find((x) => x.id === id);
  return c ? `${c.name}（${c.username}）` : id;
}

/** 预览里展示这次用了哪个代理（代理库的名字优先，否则临时地址）。 */
function resolveProxyLabel(input: PullJobInput, list: ProxyEntry[] = []): string {
  if (input.sourceProxyId) {
    const p = list.find((x) => x.id === input.sourceProxyId);
    return p ? `${p.name}（${p.url}）` : input.sourceProxyId;
  }
  return input.sourceProxy ? `临时 ${input.sourceProxy}` : '不使用';
}

/** 从 `<repo>:<tag>` 取出 tag；取不到时返回空串。 */
function sourceTagOf(ref: string): string {
  const colon = ref.lastIndexOf(':');
  if (colon < 0) return '';
  const tag = ref.slice(colon + 1);
  return tag.includes('/') ? '' : tag;
}

/** 完整目的引用的主机前缀，如 `192.0.2.10:10001/`。 */
function hostPrefixOf(host: string): string {
  return host ? `${host}/` : '';
}

/**
 * 把后端稳定的 code 翻译成一句"运维能直接照做"的提示。
 * 摘要放在表格行内，全文在展开区；这里只保留一句最重要的根因。
 *
 * 用 `errorOrigin` 区分源 / 目的：CONNECTION_FAILED 同名但可能是源不可达或目的写不进去。
 */
function failureHint(job: PullJob): string {
  const code = job.errorCode ?? '';
  const origin = job.errorOrigin;
  const side = origin === 'source' ? '源' : origin === 'dest' ? '目的' : '';

  switch (code) {
    case 'CONNECTION_FAILED':
      return side ? `${side} registry 连不上，请检查地址 / 代理` : '镜像仓库连不上';
    case 'SOURCE_UNREACHABLE':
      return '源 registry 连不上，请检查地址 / 代理';
    case 'SOURCE_UNAUTHORIZED':
      // 公开镜像会自动申请匿名令牌，所以走到这里基本是"私有镜像 / 需要账号"。
      return '源要求认证：公开镜像会自动取匿名令牌，这里失败通常是私有镜像，请在「源认证」里配凭据';
    case 'NOT_A_REGISTRY':
      return '该地址不是镜像仓库（可能只是镜像站的网站/反代），请填真正的 registry 地址';
    case 'SOURCE_MANIFEST_NOT_FOUND':
      return '源镜像 / tag 不存在';
    case 'SOURCE_BLOB_NOT_FOUND':
      return '源 blob 缺失，镜像不完整';
    case 'SOURCE_HTTP_FAILED':
      return '源 registry 响应异常';
    case 'SOURCE_MANIFEST_INVALID':
      return '源 manifest 解析失败';
    case 'INVALID_URL':
      return '源地址不合法';
    case 'INVALID_REQUEST':
      return '输入参数不合法';
    case 'DEST_FORBIDDEN':
      return '目的 registry 拒绝写入';
    case 'BLOB_UPLOAD_FAILED':
    case 'BLOB_MOUNT_FAILED':
    case 'BLOB_UPLOAD_INIT_FAILED':
      return '目的 blob 上传失败';
    case 'MANIFEST_PUT_FAILED':
      return '目的 manifest 落库失败';
    case 'CANCELLED':
      return '已取消';
    case 'JOB_NOT_FOUND':
      return '任务不存在';
    case 'PULL_DISABLED':
      return '服务端禁止拉取（allowPull=false）';
    default:
      // 没识别出来的 code：把后端原始 message 兜底展示。
      return job.errorMessage ?? '失败原因未知';
  }
}

const STATUS_META: Record<
  PullJobStatus,
  { label: string; color: string; icon: React.ReactNode }
> = {
  queued: { label: '排队中', color: 'default', icon: <HourglassOutlined /> },
  running: { label: '拉取中', color: 'processing', icon: <LoadingOutlined /> },
  succeeded: { label: '已完成', color: 'success', icon: <CheckCircleOutlined /> },
  failed: { label: '失败', color: 'error', icon: <CloseCircleOutlined /> },
  cancelled: { label: '已取消', color: 'warning', icon: <PauseCircleOutlined /> },
};

/**
 * v0.5.37.4：从任务源地址里取 host（含端口），作为侧栏「来源」分组的分桶键；
 * 解析失败或为空时统一归「未知」。只用于展示与过滤，不改变任务本身的字段。
 */
function sourceHostOf(sourceUrl: string | undefined): string {
  const raw = String(sourceUrl ?? '').trim();
  if (!raw) return '未知';
  try {
    return new URL(raw).host || '未知';
  } catch {
    return '未知';
  }
}

export default function PullPage({ config, sidebarFilter, onPublishGroups }: Props) {
  const { message, modal } = AntdApp.useApp();
  const [form] = Form.useForm<FormValues>();
  const [jobs, setJobs] = useState<PullJob[]>([]);
  const [error, setError] = useState<ApiResult<unknown> | null>(null);
  const [submitting, setSubmitting] = useState(false);
  // v0.5.41: 首屏拉列表的 loading —— 之前没这个状态,切到本页面时直接显示空表格
  // + Empty 文案,观感像「坏了」。配上 TableSkeleton 让用户看到「正在拉」。
  // 用 jobs 为空 + loading=true 作为首屏判定(避免每次轮询都闪骨架)。
  const [loading, setLoading] = useState(true);
  /**
   * v0.5.18（F2/F6）：配置读不到时这一页的**表单与历史任务仍然可用**（只是拿不到
   * allowPull / host 这些约束），所以不做整页错误态，只加一条提示 + 重试，
   * 不把还能干的事一起遮掉。配置本身从模块级 store 取，不再自己拉。
   */
  const { failure: configFailure, loading: configLoading, reload: reloadConfig } = useAppConfig();
  /** 创建前的预览：表单点击"加入队列"后打开 Modal 确认 + 源预检。 */
  const [pendingInput, setPendingInput] = useState<PullJobInput | null>(null);
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [proxies, setProxies] = useState<ProxyEntry[]>([]);
  /**
   * v0.5.18（F5）：轮询每一跳都新起一个请求，慢的时候会叠起来（registry 侧一慢，
   * 1.5s 一跳就攒成一串）—— 加一个在途守卫，上一跳没回来就跳过这一跳。
   * **只守轮询**：手动刷新该等就等，不进这个闸。
   */
  const pollingRef = useRef(false);
  /** 本仓库的 host[:port]；作为固定前缀展示，不可编辑。 */
  const host = config?.host ?? '';
  /** 用户是否手动改过「目标镜像名」——改过就不再跟随源镜像，免得把人的输入冲掉。 */
  const destTouchedRef = useRef(false);

  /**
   * v0.5.48: 设置页「第三方拉取源」（pull.known_hosts）→ host[:port] 映射表。
   * 传给 parseImageReference：镜像名带这些主机前缀时（含无点内网主机如
   * `harbor/team/app`，点/端口启发式认不出来）也能正确拆出源；显式
   * http:// 条目还能强制协议。用户条目优先于内置知名源表。
   */
  const userHosts = useMemo(
    () => parseKnownHosts(config?.mutable.pullKnownHosts ?? ''),
    [config?.mutable.pullKnownHosts],
  );

  /**「来源地址」不再让用户在拉取表单里填 —— 镜像名含主机段自动识别,无主机段默认 Docker Hub;
     自建源 / 强制覆盖场景统一走设置页「第三方拉取源」(pull.known_hosts)。 */

  /**
   * 任务行的展开状态（受控）。
   *
   * 行为：
   *   - **首屏保持收起** —— 那是历史记录，一上来就铺满详情很吵；
   *   - 之后**新出现**的失败 / 取消任务自动展开一次，让你不用点就能看到原因；
   *   - 用户手动收放完全自由，且收起后不会被 1.5s 一次的轮询再弹开。
   *
   * 必须传 onExpandedRowsChange：只给 expandedRowKeys 是"受控但没有回调"，
   * antd 无法改状态，于是点开就收不回去（之前就是这个 bug）。
   */
  const [expandedKeys, setExpandedKeys] = useState<string[]>([]);
  const autoExpandedRef = useRef<Set<string>>(new Set());
  const firstLoadRef = useRef(true);
  useEffect(() => {
    // 迁移逻辑抽在 utils.nextJobExpansion 里（纯函数、已单测），
    // 组件这里只负责把结果落进 state。
    const next = nextJobExpansion({
      jobs,
      prevKeys: expandedKeys,
      autoHandled: autoExpandedRef.current,
      firstLoad: firstLoadRef.current,
    });
    autoExpandedRef.current = next.autoHandled;
    firstLoadRef.current = next.firstLoad;
    if (next.keys.length !== expandedKeys.length || next.keys.some((id, i) => id !== expandedKeys[i])) {
      setExpandedKeys(next.keys);
    }
    // expandedKeys 故意不进依赖：这个 effect 只应由任务列表变化驱动，
    // 否则会形成"改了 state 又触发自己"的回环。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobs]);

  /**
   * 源镜像变化时，把目标镜像名同步成"同名"（去掉主机前缀后的 <repo>:<tag>）。
   *
   * 只在用户没手动改过目标时才跟随；把目标清空即恢复跟随（否则一旦改过就再也回不到自动）。
   */
  const handleValuesChange = (changed: Partial<FormValues>) => {
    const autoFor = (sourceImage: string | undefined) =>
      parseImageReference(sourceImage ?? '', userHosts).sourceRef;

    if ('destImage' in changed) {
      // 判断这次变化是不是我们自己 setFieldValue 触发的自动填充。
      if (changed.destImage !== autoFor(form.getFieldValue('image'))) {
        destTouchedRef.current = true;
      }
      if (!changed.destImage) {
        // 清空 = 恢复跟随源镜像
        destTouchedRef.current = false;
        form.setFieldValue('destImage', autoFor(form.getFieldValue('image')));
      }
      return;
    }

    if ('image' in changed) {
      if (!destTouchedRef.current) {
        const auto = autoFor(changed.image);
        if (form.getFieldValue('destImage') !== auto) {
          form.setFieldValue('destImage', auto);
        }
      }
    }
  };

  // 凭据库可用时拉一次；不可用不请求（listCredentials 仍能调，但服务端会返 CREDENTIAL_KEY_MISSING）。
  // v0.5.18（F6）：直接读 prop 并把开关写进 deps —— 配置晚到时 effect 会重跑，
  // 不再需要那种"绕开 deps 的隐式新鲜度"。
  const refreshCredentials = useCallback(async () => {
    if (!config?.allowCredentials) {
      setCredentials([]);
      return;
    }
    const result = await listCredentials();
    if (result.success && result.data) {
      setCredentials(result.data);
    } else {
      setCredentials([]);
    }
  }, [config?.allowCredentials]);

  useEffect(() => {
    void refreshCredentials();
  }, [refreshCredentials]);

  /** 代理库与凭据库同源，可用性一起判断。 */
  const refreshProxies = useCallback(async () => {
    if (!config?.allowProxies) {
      setProxies([]);
      return;
    }
    const result = await listProxies();
    setProxies(result.success && result.data ? result.data : []);
  }, [config?.allowProxies]);

  useEffect(() => {
    void refreshProxies();
  }, [refreshProxies]);

  const refresh = useCallback(async () => {
    const result = await listPullJobs();
    if (result.success && result.data) {
      setJobs(result.data);
      setError(null);
    } else {
      setError(result);
    }
    setLoading(false); // v0.5.41: 首屏/轮询都收尾 —— 即便失败也退掉骨架,展示错误
  }, []);

  // 首屏 + 配置就绪后拉一次；后续只在有未完成任务时持续轮询。
  const hasActive = useMemo(
    () => jobs.some((job) => job.status === 'running' || job.status === 'queued'),
    [jobs]
  );
  useEffect(() => {
    void refresh();
  }, [refresh]);

  useEffect(() => {
    if (!hasActive) {
      return undefined;
    }
    const timer = setInterval(() => {
      // v0.5.18（F5）：上一跳还没回来就跳过这一跳（守卫见上面的 pollingRef）。
      if (pollingRef.current) {
        return;
      }
      pollingRef.current = true;
      void refresh().finally(() => {
        pollingRef.current = false;
      });
    }, POLL_INTERVAL_MS);
    return () => {
      clearInterval(timer);
      pollingRef.current = false;
    };
  }, [hasActive, refresh]);

  const runningJob = useMemo(() => jobs.find((job) => job.status === 'running'), [jobs]);
  const queuedJobs = useMemo(
    () => jobs.filter((job) => job.status === 'queued'),
    [jobs]
  );

  /**
   * v0.5.37.4：侧栏分组 —— 只放视图状态（筛选），有副作用的操作留在页头。
   * 两组的 badge 全部来自真实任务列表，侧栏数字与表格行永远同源：
   *   - 状态：全部 / 进行中（排队 + 拉取合并）/ 已完成 / 失败 / 已取消
   *   - 来源：按任务源地址的 host 分桶（空值 / 解析失败归「未知」）
   * 任务列表为空时下发空组，侧栏显示占位文案而不是空壳。
   */
  const sidebarGroups = useMemo<SidebarGroup[]>(() => {
    if (jobs.length === 0) return [];

    const statusItems: SidebarItem[] = [
      { key: 'all', label: '全部', badge: jobs.length },
      {
        key: 'active',
        label: '进行中',
        badge: jobs.filter((job) => job.status === 'queued' || job.status === 'running').length,
      },
      {
        key: 'succeeded',
        label: '已完成',
        badge: jobs.filter((job) => job.status === 'succeeded').length,
      },
      {
        key: 'failed',
        label: '失败',
        badge: jobs.filter((job) => job.status === 'failed').length,
      },
      {
        key: 'cancelled',
        label: '已取消',
        badge: jobs.filter((job) => job.status === 'cancelled').length,
      },
    ];

    const sourceCounts = new Map<string, number>();
    jobs.forEach((job) => {
      const host = sourceHostOf(job.sourceUrl);
      sourceCounts.set(host, (sourceCounts.get(host) ?? 0) + 1);
    });
    const sourceItems: SidebarItem[] = Array.from(sourceCounts.entries())
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .map(([host, count]) => ({ key: host, label: host, badge: count }));

    return [
      { key: 'status', label: '状态', items: statusItems },
      {
        key: 'source',
        label: '来源',
        items: [{ key: 'all', label: '全部', badge: jobs.length }, ...sourceItems],
      },
    ];
  }, [jobs]);

  useEffect(() => {
    onPublishGroups(sidebarGroups);
  }, [onPublishGroups, sidebarGroups]);

  /**
   * 表格数据 = 全量任务按侧栏两组选择做 AND 过滤。
   * 「进行中」是 queued + running 的合并口径，与侧栏 badge 同一算法。
   */
  const visibleJobs = useMemo(() => {
    const status = sidebarFilter.status ?? null;
    const source = sidebarFilter.source ?? null;
    return jobs.filter((job) => {
      if (status && status !== 'all') {
        if (status === 'active') {
          if (job.status !== 'queued' && job.status !== 'running') {
            return false;
          }
        } else if (job.status !== status) {
          return false;
        }
      }
      if (source && source !== 'all' && sourceHostOf(job.sourceUrl) !== source) {
        return false;
      }
      return true;
    });
  }, [jobs, sidebarFilter]);

  const handleSubmit = async (values: FormValues) => {
    if (config && !config.allowPull) {
      message.warning('当前为禁止拉取模式（allowPull=false），无法创建任务');
      return;
    }
    const image = values.image?.trim();
    if (!image) {
      message.error('请填写镜像名');
      return;
    }
    // 智能解析：用户写 `ghcr.io/owner/repo:tag` 这种含主机前缀的引用，
    // 自动拆出 sourceUrl；写 `alpine:3.19` / `library/alpine:3.19` 默认走 docker.io。
    // v0.5.48: 设置页自配的第三方源也参与识别（userHosts，优先于内置表）。
    // 自建源 / 强制覆盖场景统一走设置页「第三方拉取源」，UI 不再让用户填。
    const parsed = parseImageReference(image, userHosts);
    const sourceUrlEffective = parsed.sourceUrl;
    const sourceRefEffective = parsed.sourceRef || image;

    // 目标引用 = 本仓库地址（固定）+ 目标镜像名。
    // 目标镜像名留空则与源镜像同名；用户可以改它来换落地路径
    // （例如把 library/alpine:3.19 落成 alpine:3.19）。
    // 主机部分来自服务配置，不可能指到别的 registry。
    const destRef = values.destImage?.trim() || sourceRefEffective;
    const { repo: destRepo, tag: destTag } = splitRepoTag(destRef);
    if (!DEST_REPO_PATTERN.test(destRepo)) {
      message.error('目标镜像名的仓库路径不合法（只能小写字母 / 数字 / ._- 分段，且不能带主机）');
      return;
    }

    // 源端认证：credential / temp / none。
    let sourceCredentialId: string | undefined;
    let sourceAuthInline: { username: string; password: string } | undefined;
    if (values.sourceAuthMode === 'credential' && values.sourceCredentialId) {
      sourceCredentialId = values.sourceCredentialId;
    } else if (values.sourceAuthMode === 'temp') {
      const u = values.sourceTempUsername?.trim();
      const p = values.sourceTempPassword ?? '';
      if (u && p) {
        sourceAuthInline = { username: u, password: p };
      }
    }

    // 打开预览 Modal，让用户看清将要做什么 + 预检，再真正入队。
    setPendingInput({
      sourceUrl: sourceUrlEffective,
      sourceRef: sourceRefEffective,
      destRepo,
      // 目标镜像名里没写 tag 时留空，让服务端沿用源 tag（同一套默认口径）。
      destTag: destTag || undefined,
      sourceProxyId:
        values.sourceProxyMode === 'library' && values.sourceProxyId
          ? values.sourceProxyId
          : undefined,
      sourceProxy:
        values.sourceProxyMode === 'temp' ? values.sourceProxy?.trim() || undefined : undefined,
      sourceCredentialId,
      sourceAuthInline,
    });
  };

  /** Modal 里点确认才真正创建。 */
  const handleConfirmCreate = async () => {
    if (!pendingInput) return;
    setSubmitting(true);
    try {
      const result = await createPullJob(pendingInput);
      if (!result.success) {
        setError(result);
        message.error(result.message || '创建任务失败');
        return;
      }
      message.success(
        `已加入队列：从 ${pendingInput.sourceUrl} 拉取 ${pendingInput.sourceRef} → ${pendingInput.destRepo}`
      );
      setPendingInput(null);
      form.resetFields();
      await refresh();
    } finally {
      setSubmitting(false);
    }
  };

  const handleCancelPreview = () => {
    setPendingInput(null);
  };

  const handleCancel = async (job: PullJob) => {
    const result = await cancelPullJob(job.id);
    if (!result.success) {
      message.error(result.message || '取消失败');
      return;
    }
    message.info('已请求取消，正在传输的 chunk 会写完再退出');
    await refresh();
  };

  const handleRemove = (job: PullJob) => {
    modal.confirm({
      title: '从历史中移除该任务？',
      content: '不会删除已落库的镜像，仅清理任务记录。',
      okText: '移除',
      cancelText: '取消',
      onOk: async () => {
        const result = await removePullJob(job.id);
        if (!result.success) {
          message.error(result.message || '移除失败');
          return;
        }
        await refresh();
      },
    });
  };

  const columns: ColumnsType<PullJob> = [
    {
      title: '来源',
      key: 'source',
      // v0.7.11: 280 → 220,主区域 ~1100px 时避免表格横滚;URL 走 ellipsis。
      width: 220,
      render: (_, job) => (
        <Space direction="vertical" size={2} style={{ lineHeight: 1.4 }}>
          <Tooltip title={job.sourceUrl}>
            <span className="mono ellipsis" style={{ maxWidth: 200, display: 'inline-block' }}>
              {job.sourceUrl.replace(/^https?:\/\//i, '')}
            </span>
          </Tooltip>
          <span className="mono ellipsis" style={{ color: 'var(--color-text-3)', maxWidth: 200, display: 'inline-block' }}>
            {job.sourceRepo}:{job.sourceTag}
          </span>
        </Space>
      ),
    },
    {
      title: '目标',
      key: 'dest',
      // v0.7.11: 200 → 140,`library/alpine:3.19` 之类 21 字符 + padding 足够。
      width: 140,
      render: (_, job) => (
        <Tooltip title={`${job.destRepo}:${job.destTag}`}>
          <span className="mono ellipsis" style={{ maxWidth: 120, display: 'inline-block' }}>
            {job.destRepo}:{job.destTag}
          </span>
        </Tooltip>
      ),
    },
    {
      title: '状态',
      key: 'status',
      // v0.7.11: 用户反馈「状态列可以窄一点」—— 200 → 140。Tag + 单行
      // 错误描述靠 ellipsis 截断。
      width: 140,
      render: (_, job) => {
        const meta = STATUS_META[job.status];
        const failureReason = job.errorMessage ? failureHint(job) : null;
        return (
          <Space direction="vertical" size={2} style={{ lineHeight: 1.3 }}>
            <Tag color={meta.color} icon={meta.icon} style={{ margin: 0 }}>
              {meta.label}
            </Tag>
            {failureReason ? (
              <Tooltip title={failureReason}>
                <span style={{ fontSize: 12, color: 'var(--color-text-3)' }} className="ellipsis">
                  {failureReason}
                </span>
              </Tooltip>
            ) : null}
          </Space>
        );
      },
    },
    {
      // v0.7.7: 显示当前任务实际要拉的架构 —— 直接读设置页
      // pullPlatforms(同一份 Mutable.pullPlatforms,全 UI 共享)。
      // 任务提交时刻的 allow-list 已经被存储引擎 snapshot,展示当前设置值
      // 是「下次拉取会用」的指示,不一定是这次任务用的 —— 任务已经在跑
      // 时改设置不影响当前 job。
      // v0.7.11: 200 → 140,跟上面对齐列宽,避免横滚。
      title: '拉取架构',
      key: 'platforms',
      width: 140,
      render: () => {
        // 共享 pullPlatforms —— 每次 render 重读 config,响应最新设置。
        const csv = config?.mutable?.pullPlatforms ?? '';
        const list = csv
          .split(',')
          .map((s) => s.trim())
          .filter(Boolean);
        if (list.length === 0) {
          return (
            <Tooltip title="未限制(等于拉所有架构 —— 上游 image index 列出的每个 child 都会下)">
              <span style={{ color: 'var(--color-text-3)' }}>未限制</span>
            </Tooltip>
          );
        }
        const text = list.join(', ');
        return (
          <Tooltip placement="topLeft" title={text}>
            <span className="mono ellipsis" style={{ display: 'block', maxWidth: 120 }}>
              {text}
            </span>
          </Tooltip>
        );
      },
    },
    {
      title: '进度',
      key: 'progress',
      // v0.7.11: 220 → 180,JobProgress 进度条 + 字节文本够了。
      width: 180,
      render: (_, job) => <JobProgress job={job} />,
    },
    {
      title: '提交于',
      key: 'createdAt',
      width: 150,
      render: (_, job) => (
        <Tooltip title={job.createdAt}>{formatDateTime(job.createdAt)}</Tooltip>
      ),
    },
    {
      title: '操作',
      key: 'actions',
      width: 140,
      fixed: 'right',
      render: (_, job) => (
        <Space size={4}>
          {job.status === 'running' || job.status === 'queued' ? (
            <Button
              type="link"
              size="small"
              icon={<StopOutlined />}
              onClick={() => void handleCancel(job)}
            >
              取消
            </Button>
          ) : null}
          <Button type="link" size="small" onClick={() => void handleRemove(job)}>
            移除
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h2 className="page-title">镜像拉取</h2>
          <p className="page-subtitle">
            像 <code>docker pull</code> 一样贴一个镜像名就可以拉取。源在公网或受限网段时，
            在「高级选项」里填一个来源代理（仅作用于本次任务）。
          </p>
        </div>
      </div>

      {error?.message ? (
        <Alert
          type="warning"
          showIcon
          closable
          onClose={() => setError(null)}
          message={error.message}
          description={error.code ? `错误分类：${error.code}` : undefined}
          action={
            <Button size="small" onClick={() => void refresh()}>
              重试
            </Button>
          }
        />
      ) : null}

      {configFailure ? (
        <Alert
          type="warning"
          showIcon
          message="服务配置读取失败，拉取表单可能不完整"
          description={
            <div>
              <div>{configFailure.message}</div>
              <div style={{ marginTop: 4, color: 'var(--color-text-3)' }}>
                {`错误分类：${configFailure.code}`}
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

      {config && !config.allowPull ? (
        <Alert
          type="info"
          showIcon
          message="当前为禁止拉取模式（allowPull=false），仅可查看历史任务。"
        />
      ) : null}

      <div className="panel" style={{ padding: 16 }}>
        <Form<FormValues>
          form={form}
          layout="vertical"
          /*
           * 不再预设 initialValues —— 源 / 目标镜像输入框以 placeholder
           * 作为示例提示（`alpine:3.19` / `与源镜像同名`），留给用户
           * 自己填。预填常见 image 会让人误以为是「默认值」/「已有任务」，
           * 实际每次都新开任务，不该给错觉。
           */
          onFinish={handleSubmit}
          onValuesChange={handleValuesChange}
          disabled={Boolean(config && !config.allowPull)}
        >
          {/*
            v0.7.37 布局调整：左侧源 / 目标镜像纵向堆叠,右侧高级选项窄卡片并排。
            高级选项不再折叠 —— 「源端代理」「源认证」这两个选择是高频操作,
            每次新建任务都可能要切,折叠一次点一次太烦;放右侧窄卡片里
            既不挤占源 / 目标的主输入空间,又保持可见。
            源 / 目标上下布局(而不是左右)是为了视觉重心偏向「主输入区」,
            高级选项作为辅助选项在右侧更协调。
          */}
          <div className="pull-form-row">
            <div className="pull-main-card">
              <Form.Item
                label="源镜像名"
                name="image"
                extra="支持任意 docker pull 引用：alpine:3.19、library/alpine:3.19、ghcr.io/owner/img:1.0、192.0.2.20:10001/library/alpine:3.9 等。"
                rules={[
                  { required: true, message: '请填写镜像名' },
                  {
                    // v0.5.49: 空值由上面的 required 独占负责,这条 validator 只在
                    // 有值时跑「缺 tag」检查。旧版本这里也判 !ref,空值时两条规则
                    // 同时触发「请填写镜像名」→ 截图里两条红字叠在一起。
                    validator: (_, value: string) => {
                      if (!value || !value.trim()) {
                        return Promise.resolve();
                      }
                      // 不能用 `value.includes(':')` 判断：主机前缀里也有冒号
                      // （`192.0.2.20:10001/library/alpine` 会被误判为"已有 tag"），
                      // 于是提交后才被后端拒，报错还跟输入对不上。
                      // 正解是先剥掉主机段，再看剩下部分有没有 tag。
                      const parsed = parseImageReference(value, userHosts);
                      const ref = parsed.sourceRef;
                      const colon = ref.lastIndexOf(':');
                      const tag = colon >= 0 ? ref.slice(colon + 1) : '';
                      if (!tag || tag.includes('/')) {
                        return Promise.reject(
                          new Error('缺少 tag，请写成 <repo>:<tag>，例如 alpine:3.19')
                        );
                      }
                      return Promise.resolve();
                    },
                  },
                ]}
              >
                <Input placeholder="alpine:3.19" allowClear autoFocus />
              </Form.Item>

              <Form.Item
                label="目标镜像名"
                name="destImage"
                extra={
                  host
                    ? `本仓库地址 ${host}/ 固定不可改；留空表示与源镜像同名。改这里可以把镜像落到别的路径（例如去掉 library/ 前缀）。`
                    : '本仓库地址固定不可改；留空表示与源镜像同名。'
                }
                rules={[
                  {
                    validator: (_, value: string | undefined) => {
                      if (!value || !value.trim()) {
                        return Promise.resolve(); // 留空 = 沿用源镜像
                      }
                      const { repo, tag } = splitRepoTag(value);
                      if (!DEST_REPO_PATTERN.test(repo)) {
                        return Promise.reject(
                          new Error('仓库路径只能是小写字母 / 数字 / ._- 分段，且不能带主机')
                        );
                      }
                      if (tag && !DEST_TAG_PATTERN.test(tag)) {
                        return Promise.reject(new Error('tag 只能包含字母数字与 ._-'));
                      }
                      return Promise.resolve();
                    },
                  },
                ]}
              >
                {/* 固定前缀用 addonBefore 呈现：视觉上就是"不可编辑的一段"。 */}
                <Input
                  addonBefore={host ? <span className="mono">{host}/</span> : undefined}
                  placeholder="与源镜像同名"
                  allowClear
                />
              </Form.Item>
            </div>

            <div className="pull-advanced">
              <div className="pull-advanced-title">高级选项</div>
              <Form.Item
                label="源端代理"
                extra="仅作用于本次拉取访问源；本仓库自身的代理走服务配置。"
              >
                <Input.Group compact>
                  <Form.Item name="sourceProxyMode" noStyle initialValue="none">
                    <Radio.Group optionType="button" buttonStyle="solid">
                      <Radio.Button value="none">不用</Radio.Button>
                      <Radio.Button value="library">代理库</Radio.Button>
                      <Radio.Button value="temp">临时输入</Radio.Button>
                    </Radio.Group>
                  </Form.Item>
                </Input.Group>
              </Form.Item>

              <Form.Item
                noStyle
                shouldUpdate={(prev, current) =>
                  prev.sourceProxyMode !== current.sourceProxyMode
                }
              >
                {({ getFieldValue }) => {
                  const mode = getFieldValue('sourceProxyMode');
                  if (mode === 'library') {
                    return (
                      <Form.Item
                        label="选择代理"
                        name="sourceProxyId"
                        rules={[{ required: true, message: '请选择一个代理' }]}
                      >
                        <Select
                          placeholder={
                            proxies.length === 0
                              ? '代理库还是空的，请先到「代理管理」新增'
                              : '选择代理'
                          }
                          disabled={proxies.length === 0}
                          options={proxies.map((p) => {
                            const status = p.lastProbeStatus || '';
                            const tag =
                              status === 'ok'
                                ? ' [可用]'
                                : status === 'failed'
                                  ? ' [不可用]'
                                  : '';
                            return {
                              value: p.id,
                              label: `${p.name}（${p.url}${p.hasAuth ? '，带认证' : ''}）${tag}`,
                              disabled: status === 'failed',
                            };
                          })}
                        />
                      </Form.Item>
                    );
                  }
                  if (mode === 'temp') {
                    return (
                      <Form.Item
                        label="临时代理地址"
                        name="sourceProxy"
                        rules={[
                          { required: true, message: '请填写代理地址' },
                          {
                            validator: (_, value: string | undefined) =>
                              !value || /^https?:\/\//i.test(value.trim())
                                ? Promise.resolve()
                                : Promise.reject(
                                    new Error('需要以 http:// 或 https:// 开头')
                                  ),
                          },
                        ]}
                        extra="只用于本次任务，不写入代理库。需要认证时写成 http://用户:密码@主机:端口。"
                      >
                        <Input placeholder="http://proxy.example.com:8080" allowClear />
                      </Form.Item>
                    );
                  }
                  return null;
                }}
              </Form.Item>

              <Form.Item
                label="源认证"
                extra={
                  config?.allowCredentials
                    ? '凭据库由「凭据管理」维护；临时输入不会落盘。'
                    : '凭据库未配置，只能临时输入账号 / 密码（不会落盘）。'
                }
              >
                <Input.Group compact>
                  <Form.Item name="sourceAuthMode" noStyle initialValue="none">
                    <Radio.Group
                      optionType="button"
                      buttonStyle="solid"
                      onChange={() => form.resetFields(['sourceCredentialId'])}
                    >
                      <Radio.Button value="none">不用</Radio.Button>
                      <Radio.Button value="credential">凭据库</Radio.Button>
                      <Radio.Button value="temp">临时输入</Radio.Button>
                    </Radio.Group>
                  </Form.Item>
                </Input.Group>
              </Form.Item>

              <Form.Item
                noStyle
                shouldUpdate={(prev, current) =>
                  prev.sourceAuthMode !== current.sourceAuthMode
                }
              >
                {({ getFieldValue }) => {
                  const mode = getFieldValue('sourceAuthMode');
                  if (mode === 'credential') {
                    // 凭据库里全是外部源凭据，没有"用途"维度，直接全列。
                    const sourceCandidates = credentials;
                    return (
                      <Form.Item
                        label="选择源凭据"
                        name="sourceCredentialId"
                        rules={[
                          { required: true, message: '请选择一条凭据' },
                        ]}
                      >
                        <Select
                          placeholder={
                            sourceCandidates.length === 0
                              ? '凭据库里还没有凭据，请先到「凭据管理」新增'
                              : '选择凭据'
                          }
                          disabled={sourceCandidates.length === 0}
                          options={sourceCandidates.map((c) => ({
                            value: c.id,
                            label: `${c.name}（${c.username} @ ${c.registryUrl}）`,
                          }))}
                        />
                      </Form.Item>
                    );
                  }
                  if (mode === 'temp') {
                    return (
                      <>
                        <Form.Item
                          label="临时账号"
                          name="sourceTempUsername"
                          rules={[{ required: true, message: '请填写用户名' }]}
                        >
                          <Input autoComplete="off" placeholder="username" />
                        </Form.Item>
                        <Form.Item
                          label="临时密码"
                          name="sourceTempPassword"
                          rules={[{ required: true, message: '请填写密码' }]}
                          extra="只用于本次任务，不会写入凭据库。"
                        >
                          <Input.Password
                            autoComplete="new-password"
                            placeholder="••••••"
                          />
                        </Form.Item>
                      </>
                    );
                  }
                  return null;
                }}
              </Form.Item>
            </div>
          </div>

          {/*
            v0.7.37: 提交按钮 + 单并发说明放回 panel 内部,留在卡片下面,
            跟源/目标 / 高级选项卡片拉开距离(margin-top 20px)。按钮
            属于「整张 panel 的提交动作」,不放卡片外面,避免视觉上
            跟 panel 脱钩;FIFO 仍是 muted 说明文字,不挂 chip 边框。
          */}
          <div className="pull-actions">
            <Button
              type="primary"
              icon={<PlusOutlined />}
              htmlType="submit"
              loading={submitting}
              disabled={Boolean(config && !config.allowPull)}
            >
              加入队列
            </Button>
            <Tooltip title="单并发：当前任务完成后才会启动下一个">
              <span className="pull-actions-note">
                <CloudDownloadOutlined />
                单并发 / FIFO
              </span>
            </Tooltip>
          </div>
        </Form>
      </div>

      {runningJob ? (
        <div className="panel" style={{ padding: 16 }}>
          <div className="pull-running-head">
            <strong>当前任务</strong>
            <span className="mono ellipsis" style={{ color: 'var(--color-text-3)' }}>
              {runningJob.sourceRepo}:{runningJob.sourceTag} → {runningJob.destRepo}:{runningJob.destTag}
            </span>
          </div>
          <JobProgress job={runningJob} detailed />
          <div style={{ marginTop: 12 }}>
            <Button
              icon={<StopOutlined />}
              danger
              onClick={() => void handleCancel(runningJob)}
            >
              优雅取消
            </Button>
            <span style={{ marginLeft: 12, color: 'var(--color-text-3)', fontSize: 12 }}>
              取消时，正在传输的 chunk 会写完再退出；目的端不会留下半截 manifest。
            </span>
          </div>
        </div>
      ) : null}

      {queuedJobs.length > 0 ? (
        <Alert
          type="info"
          showIcon
          message={`排队中 ${queuedJobs.length} 个任务`}
          description={
            <span>
              {queuedJobs
                .slice(0, 3)
                .map(
                  (job) =>
                    `${job.sourceRepo}:${job.sourceTag} → ${job.destRepo}:${job.destTag}`
                )
                .join('、')}
              {queuedJobs.length > 3 ? ` 等 ${queuedJobs.length} 个` : ''}
            </span>
          }
        />
      ) : null}

      <div className="panel" style={{ position: 'relative', minHeight: 480 }}>
        {/*
         * v0.6.23: PageLoading 改成 position: absolute 覆盖在 panel 内,不再
         * 占 flow 高度 —— 解决 0.6.22 「拉址感」(spinner 在 flow 里跟 Table
         * 上下挤)。Table hidden=true 时 panel 内空,spinner 覆盖整个 panel
         * 200px 区域;Table 露脸后 spinner 在 Table **上面**淡出。
         *
         * 同时保持 0.6.22 的"等元素渲染完再淡出"语义:visible=false 后内部
         * 状态机走 minDuration=250ms + fadeDuration=350ms。
         *
         * `loading && jobs.length === 0` (首屏场景)才需要居中 spinner ——
         * 后续轮询 jobs.length > 0 时不闪(antd Table 自带半透层)。
         */}
        <div hidden={loading && jobs.length === 0}>
        <Table<PullJob>
          rowKey="id"
          size="middle"
          columns={columns}
          dataSource={visibleJobs}
          /*
           * v0.7.39 修复「状态筛选为 0 时页面晃动」:
           *
           * 之前 `scroll={{ x: 1000 }}` 与列宽之和(220+140+140+140+180+150+140 = 1110)
           * 不一致 —— 表格有数据时,antd Table 把 tbody 内容宽度撑到 1110px,容器内
           * 出现横向滚动条;筛选切到 0 行(Empty 状态)后,tbody 不再被内容撑大,
           * 横向滚动条消失,容器宽度抖回 scroll.x(1000px),整页因此 reflow,
           * 视觉上"晃动"。
           *
           * 同时 `tableLayout="fixed"` —— 列宽全部锁定为 width 属性,不再让 antd 用
           * cell 内容反算列宽(空数据时没有 cell,反算会得到不同结果,进一步触发
           * reflow)。两条一起改,空数据 / 有数据两个状态下表格尺寸完全一致,
           * 不再触发任何 layout shift。
           */
          scroll={{ x: 1110 }}
          tableLayout="fixed"
          pagination={false}
            expandable={{
              expandedRowRender: (job) => <JobPhases job={job} />,
              // 每一行都可展开：成功 / 进行中的任务也常有"看看到底走到哪一层"的需求，
              // 而且每行都有箭头才不会让人以为"这行点不动"。
              rowExpandable: () => true,
              expandedRowKeys: expandedKeys,
              onExpandedRowsChange: (keys) => setExpandedKeys(keys.map(String)),
            }}
            locale={{
              emptyText: (
                <Empty
                  description={
                    jobs.length > 0
                      ? '当前筛选下没有任务，换个筛选条件试试'
                      : '还没有任务，填写上方表单加入第一个'
                  }
                />
              ),
            }}
          />
        </div>
        <PageLoading
          visible={loading && jobs.length === 0}
          tip="正在读取拉取任务…"
        />
      </div>

      <PullPreviewModal
        input={pendingInput}
        host={config?.host ?? ''}
        usingAuth={Boolean(config?.usingAuth)}
        credentials={credentials}
        proxies={proxies}
        onConfirm={handleConfirmCreate}
        onCancel={handleCancelPreview}
        submitting={submitting}
      />
    </div>
  );
}

/**
 * 创建任务前的预览 Modal：
 *   - 列出解析后的全部字段（源 / 目的 / 代理）
 *   - 真实打一次源端 GET /v2/，把"能不能连"立刻告诉用户
 *   - 源不通时不允许"确认入队"，避免浪费一次任务
 *   - 目的端的可达性由 /api/probe 单独验证（沿用既有 endpoint）
 */
function PullPreviewModal({
  input,
  host,
  usingAuth,
  credentials,
  proxies,
  onConfirm,
  onCancel,
  submitting,
}: {
  input: PullJobInput | null;
  /** 本仓库的 host[:port]，用于拼出完整的目的引用。 */
  host: string;
  /** 本仓库是否配了 basic auth（来自服务配置，任务级不可改）。 */
  usingAuth: boolean;
  credentials: Credential[];
  proxies: ProxyEntry[];
  onConfirm: () => void;
  onCancel: () => void;
  submitting: boolean;
}) {
  // v0.7.8: 跟设置页 pullPlatforms 共享同一份 Mutable —— 弹窗预览
  // 「这次拉取实际要哪些架构」让用户入队前就能看明白,免得跑起来才发现
  // arm64 child 没下(arm/v6 / arm/v7 同理 — chip 没勾就没拉到)。
  const { config } = useAppConfig();
  const pullPlatformsText = (() => {
    const csv = config?.mutable?.pullPlatforms ?? '';
    return csv
      .split(',')
      .map((s: string) => s.trim())
      .filter(Boolean);
  })();

  const [probeResult, setProbeResult] = useState<
    | { state: 'idle' }
    | { state: 'loading' }
    | {
        state: 'ok';
        apiVersion: string;
        host: string;
        authRequired?: boolean;
        tokenRealm?: string;
        tokenError?: string;
        /** v0.5.17：后端按真实源引用校验过 manifest，回填真正被探测的 repo / tag。 */
        sourceRepo?: string;
        sourceTag?: string;
        dest?: DestStatus;
        /** v0.5.17：dest 段探测自身失败的原因（此时 dest 是缺字段的兜底对象）。 */
        destError?: string;
      }
    | { state: 'failed'; message: string; origin?: 'source' | 'dest' }
  >({ state: 'idle' });

  // 打开时主动跑一次源端预检。
  useEffect(() => {
    if (!input) {
      setProbeResult({ state: 'idle' });
      return;
    }
    let cancelled = false;
    setProbeResult({ state: 'loading' });
    probePullSource({
      sourceUrl: input.sourceUrl,
      sourceProxy: input.sourceProxy,
      credentialId: input.sourceCredentialId,
      proxyId: input.sourceProxyId,
      sourceRef: input.sourceRef,
      destRepo: input.destRepo,
      destTag: input.destTag,
    })
      .then((result) => {
        if (cancelled) return;
        const data = result.data;
        if (result.success && data) {
          // v0.5.17：信封上的 success 只说明这次 HTTP 调用成功，不代表源可用。
          // 此前判定漏读 data.ok，于是"源 registry 不可达"也会被渲染成绿色的
          // 「源可达」，用户确认入队后任务必然卡在拉取中——正是这个缺陷。
          if (data.ok === false) {
            setProbeResult({
              state: 'failed',
              message: data.error || '源端预检未通过',
              origin: 'source',
            });
          } else {
            setProbeResult({
              state: 'ok',
              apiVersion: data.apiVersion,
              host: data.host,
              authRequired: data.authRequired,
              tokenRealm: data.tokenRealm,
              tokenError: data.tokenError,
              sourceRepo: data.sourceRepo,
              sourceTag: data.sourceTag,
              dest: data.dest,
              destError: data.destError,
            });
          }
        } else {
          setProbeResult({
            state: 'failed',
            message: result.message,
            origin: (result as { origin?: 'source' | 'dest' }).origin,
          });
        }
      })
      .catch((error) => {
        if (cancelled) return;
        setProbeResult({ state: 'failed', message: String(error?.message ?? error) });
      });
    return () => {
      cancelled = true;
    };
  }, [input]);

  return (
    <Modal
      open={Boolean(input)}
      title="即将创建拉取任务"
      okText="确认入队"
      cancelText="再改改"
      okButtonProps={{
        disabled:
          probeResult.state === 'failed' ||
          probeResult.state === 'loading' ||
          // 源 tag 不存在时不让入队：入队也必然失败，还白占一次队列。
          (probeResult.state === 'ok' && probeResult.dest?.sourceExists === false) ||
          submitting,
        loading: submitting,
      }}
      onCancel={onCancel}
      onOk={onConfirm}
      destroyOnClose
    >
      {input ? (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          {/* v0.7.10: label 列加宽 + nowrap —— 中文 label 默认 80px 会换
               行成「源 / registry」「目 的 / 引 用」(用户反馈),看着别扭。
               min-width 设 108 够放下「目的引用」四字,whiteSpace: nowrap
               阻止任何换行;vertical-align: top 让多行 content 时 label 顶部
               对齐,不再被内容拉高。 */}
          <Descriptions
            size="small"
            column={1}
            bordered
            labelStyle={{
              minWidth: 108,
              whiteSpace: 'nowrap',
              verticalAlign: 'top',
            }}
          >
            <Descriptions.Item label="源 registry">
              <span className="mono">{input.sourceUrl}</span>
              {input.sourceProxyId || input.sourceProxy ? (
                <Tag color="gold" style={{ marginLeft: 8 }}>
                  源端代理：{resolveProxyLabel(input, proxies)}
                </Tag>
              ) : null}
            </Descriptions.Item>
            <Descriptions.Item label="源认证">
              {input.sourceCredentialId
                ? `${resolveCredentialLabel(input.sourceCredentialId, credentials)}（凭据库）`
                : input.sourceAuthInline
                ? `临时账号 ${input.sourceAuthInline.username}（不保存）`
                : '不使用'}
            </Descriptions.Item>
            <Descriptions.Item label="源镜像">
              <span className="mono">{input.sourceRef}</span>
            </Descriptions.Item>
            <Descriptions.Item label="目的引用">
              <span className="mono">
                {hostPrefixOf(host)}
                {input.destRepo}:{input.destTag ?? sourceTagOf(input.sourceRef)}
              </span>
              <span style={{ marginLeft: 8, color: 'var(--color-text-3)' }}>
                自动补全：本仓库地址 + 源镜像路径
              </span>
            </Descriptions.Item>
            <Descriptions.Item label="目的认证">
              {usingAuth ? (
                <span>
                  <Tag color="blue">已配置</Tag>
                  <span style={{ color: 'var(--color-text-3)' }}>
                    来自 registry.config.json / REGISTRY_USERNAME，所有本仓库请求自动携带
                  </span>
                </span>
              ) : (
                <span style={{ color: 'var(--color-text-3)' }}>
                  未配置（匿名访问本仓库）
                </span>
              )}
            </Descriptions.Item>
            {/* v0.7.8: 拉取架构预览 —— 与设置页「拉取镜像的架构」chip 同源
                 (config.mutable.pullPlatforms)。空 = 「未限制」(拉所有架构);
                 非空 = 列出实际要下的 child manifest 集合。要改得去设置页,
                 这边只展示,不入队前用户就看得明白。 */}
            <Descriptions.Item label="拉取架构">
              {pullPlatformsText.length === 0 ? (
                <Tooltip title="未限制(等于拉所有架构 —— 上游 image index 列出的每个 child 都会下)">
                  <span style={{ color: 'var(--color-text-3)' }}>未限制</span>
                </Tooltip>
              ) : (
                <Tooltip placement="topLeft" title={pullPlatformsText.join(', ')}>
                  <span className="mono">{pullPlatformsText.join(', ')}</span>
                </Tooltip>
              )}
              <span style={{ marginLeft: 8, color: 'var(--color-text-3)' }}>
                跟设置页「拉取镜像的架构」同步
              </span>
            </Descriptions.Item>
          </Descriptions>

          {/* 源侧 tag 不存在：拼错了在这里就拦下，别等入队后才失败。 */}
          {probeResult.state === 'ok' && probeResult.dest?.sourceExists === false ? (
            <Alert
              type="error"
              showIcon
              message={`源镜像不存在：${input.sourceRef}`}
              description={
                <span style={{ color: 'var(--color-text-3)' }}>
                  源 registry 可达，但没有这个 tag。请检查镜像名与 tag 是否拼写正确。
                </span>
              }
            />
          ) : null}

          {/* 目标 tag 已存在的提示：manifest PUT 是覆盖语义，替掉前必须让用户知道。 */}
          {probeResult.state === 'ok' && probeResult.dest?.identical ? (
            <Alert
              type="info"
              showIcon
              message="目标 tag 已存在，且与源 digest 一致"
              description={
                <span style={{ color: 'var(--color-text-3)' }}>
                  本仓库已有 <span className="mono">{probeResult.dest.destRepo}:{probeResult.dest.destTag}</span>
                  （{shortDigest(probeResult.dest.existingDigest)}），与源相同，重复拉取不会改变内容。
                </span>
              }
            />
          ) : null}
          {probeResult.state === 'ok' && probeResult.dest?.willReplace ? (
            <Alert
              type="warning"
              showIcon
              message="目标 tag 已存在，本次拉取将替换它"
              description={
                <div>
                  <div>
                    本仓库 <span className="mono">{probeResult.dest.destRepo}:{probeResult.dest.destTag}</span>{' '}
                    当前指向 <span className="mono">{shortDigest(probeResult.dest.existingDigest)}</span>，
                    拉取后将指向 <span className="mono">{shortDigest(probeResult.dest.sourceDigest)}</span>。
                  </div>
                  <div style={{ marginTop: 4, color: 'var(--color-text-3)' }}>
                    如果这个 tag 已被其他系统固定引用，替换后它们拿到的镜像会变。原 manifest 不会保留。
                  </div>
                </div>
              }
            />
          ) : null}
          {probeResult.state === 'ok' && probeResult.dest && !probeResult.dest.exists && !probeResult.dest.probeError ? (
            <Alert
              type="success"
              showIcon
              message="目标 tag 不存在，将新建"
              description={
                <span style={{ color: 'var(--color-text-3)' }}>
                  <span className="mono">{probeResult.dest.destRepo}:{probeResult.dest.destTag}</span>{' '}
                  在本仓库中尚不存在。
                </span>
              }
            />
          ) : null}
          {probeResult.state === 'ok' && (probeResult.dest?.probeError || probeResult.destError) ? (
            <Alert
              type="warning"
              showIcon
              message="未能确认目标 tag 的现状"
              description={
                <span style={{ color: 'var(--color-text-3)' }}>
                  {probeResult.dest?.probeError ?? probeResult.destError}
                </span>
              }
            />
          ) : null}

          <Alert
            type={
              probeResult.state === 'ok'
                ? 'success'
                : probeResult.state === 'failed'
                ? 'error'
                : 'info'
            }
            showIcon
            message={
              probeResult.state === 'idle'
                ? '准备预检'
                : probeResult.state === 'loading'
                ? '正在校验源镜像是否可拉取…'
                : probeResult.state === 'ok'
                ? probeResult.sourceRepo
                  ? `源镜像可拉取 · ${probeResult.sourceRepo}:${probeResult.sourceTag}（API ${probeResult.apiVersion}）`
                  : `源可达 · API ${probeResult.apiVersion}（${probeResult.host}）`
                : '源端预检未通过'
            }
            description={
              probeResult.state === 'failed' ? (
                <span>
                  <strong style={{ display: 'block', marginBottom: 4 }}>{probeResult.message}</strong>
                  <span style={{ color: 'var(--color-text-3)' }}>
                    请确认源地址是否正确；若在内网/受限网段，请在「高级选项」里填一个来源代理。
                  </span>
                </span>
              ) : probeResult.state === 'ok' ? (
                <span style={{ color: 'var(--color-text-3)' }}>
                  {probeResult.authRequired ? (
                    <>
                      该源使用<strong>令牌认证</strong>（Bearer）
                      {probeResult.tokenRealm ? `，令牌服务 ${probeResult.tokenRealm}` : ''}。
                      公开镜像会自动申请匿名令牌，<strong>无需在此配凭据</strong>；
                      只有私有镜像才需要在「源认证」里选一条凭据。
                    </>
                  ) : (
                    <>源 registry 已就绪（匿名可读），无需配凭据。</>
                  )}{' '}
                  目的端的写入权限由本仓库决定，不在此处预检。
                </span>
              ) : null
            }
          />
        </Space>
      ) : null}
    </Modal>
  );
}

function JobProgress({ job, detailed = false }: { job: PullJob; detailed?: boolean }) {
  const total = job.totalBytes ?? 0;
  const percent =
    total > 0 ? Math.min(100, Math.round((job.bytes / total) * 100)) : job.status === 'succeeded' ? 100 : 0;
  const currentPhase = job.phases.find((phase) => phase.status === 'running');
  return (
    <Space direction="vertical" size={4} style={{ width: detailed ? '100%' : 'auto' }}>
      <Progress
        percent={percent}
        size="small"
        status={
          job.status === 'failed'
            ? 'exception'
            : job.status === 'cancelled'
            ? 'normal'
            : job.status === 'succeeded'
            ? 'success'
            : 'active'
        }
        showInfo={false}
        style={detailed ? { width: '100%' } : undefined}
      />
      <span style={{ fontSize: 12, color: 'var(--color-text-3)' }}>
        {formatBytes(job.bytes)}
        {total > 0 ? ` / ${formatBytes(total)}` : ''}
        {currentPhase ? ` · 正在 ${phaseLabel(currentPhase)}` : ''}
      </span>
      {detailed && currentPhase?.message ? (
        <span style={{ fontSize: 12, color: 'var(--color-text-3)' }} className="mono">
          {currentPhase.message}
        </span>
      ) : null}
    </Space>
  );
}

function JobPhases({ job }: { job: PullJob }) {
  if (!job.phases.length) {
    // 从数据库读出来的历史任务：**成功任务不存阶段明细**（一次 20 层的拉取有 22 条 phase，
    // 存了只会把行撑胖），所以展开是空的不是坏了。说一句，别让人对着空白区域猜。
    return job.fromHistory ? (
      <div className="pull-phase-empty">历史记录只保留汇总；阶段明细仅在失败 / 取消的任务上保存。</div>
    ) : null;
  }
  return (
    <div className="pull-phase-list">
      {job.phases.map((phase, index) => (
        <div key={`${phase.name}-${index}`} className="pull-phase-row">
          <Tag color={phaseStatusColor(phase.status)} style={{ minWidth: 80, textAlign: 'center' }}>
            {phaseLabel(phase)}
          </Tag>
          <Tooltip title={phase.digest}>
            <span className="mono ellipsis" style={{ maxWidth: 320 }}>
              {phase.digest ? shortDigest(phase.digest) : '—'}
            </span>
          </Tooltip>
          {/*
           * v0.6.18: bytes cell 始终渲染,空值用 `—` 占位。
           *
           * 0.6.18 之前:manifest phase 没有 totalBytes,bytes cell 直接
           * 渲染空字符串("") ;blob phase 有 totalBytes,渲染 "0 B / 973 B"。
           * 两行的 bytes cell 宽度从 0 跳到 80px → 整行 reflow → 用户
           * 反馈「详细信息面板反复横跳」。
           *
           * 修法:cell 内容无论如何都渲染一个固定宽度的占位符 (单字符 `—`)。
           * 一行的高度与宽度从此稳定,后续 phase.message 是否为空不影响
           * reflow。
           *
           * 同步把 message cell 也改为始终渲染(空时显示 `—`),不再依赖
           * {cond ? <span/> : null} 的条件渲染。否则失败 / 跳过 / 成功的
           * message 长度不同 → 整行宽度再次跳变。
           */}
          <span
            className="mono"
            style={{
              color: 'var(--color-text-3)',
              fontSize: 12,
              minWidth: 90,
              display: 'inline-block',
            }}
          >
            {phase.totalBytes != null
              ? `${formatBytes(phase.bytes)} / ${formatBytes(phase.totalBytes)}`
              : phase.bytes
              ? formatBytes(phase.bytes)
              : '—'}
          </span>
          <span
            style={{
              fontSize: 12,
              color: phase.status === 'failed' ? 'var(--color-fail)' : 'var(--color-text-3)',
              minWidth: 180,
              display: 'inline-block',
            }}
          >
            {phase.message || '—'}
          </span>
        </div>
      ))}
      {job.errorMessage ? (
        <div
          className="pull-phase-row"
          style={{ color: 'var(--color-fail)', background: 'var(--color-fail-bg)' }}
        >
          <Tag color="error">失败</Tag>
          <span style={{ fontSize: 12 }}>
            {job.errorOrigin === 'source'
              ? '源端'
              : job.errorOrigin === 'dest'
              ? '目的端'
              : ''}
            {failedPhaseLabel(job) ? ` · ${failedPhaseLabel(job)}` : ''}
            {job.errorCode ? ` · ${job.errorCode}` : ''}：{job.errorMessage}
          </span>
        </div>
      ) : null}
    </div>
  );
}

/** 找到失败时正在跑的 phase，用它来定位失败发生在哪一步。 */
function failedPhaseLabel(job: PullJob): string {
  const failed = job.phases.find((p) => p.status === 'failed');
  if (!failed) {
    return '';
  }
  return phaseLabel(failed);
}

function phaseLabel(phase: PullPhase): string {
  if (phase.name === 'manifest') {
    return 'manifest';
  }
  if (phase.name === 'config') {
    return 'config';
  }
  if (phase.name.startsWith('blob:')) {
    const index = phase.name.slice('blob:'.length);
    return `blob #${index}`;
  }
  return phase.name;
}

function phaseStatusColor(status: PullPhase['status']): string {
  switch (status) {
    case 'success':
      return 'success';
    case 'failed':
      return 'error';
    case 'running':
      return 'processing';
    case 'skipped':
      return 'default';
    default:
      return 'default';
  }
}