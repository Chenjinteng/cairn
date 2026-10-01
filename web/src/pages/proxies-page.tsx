import { useCallback, useEffect, useMemo, useState } from 'react';
import type { CSSProperties } from 'react';
import {
  Alert,
  App as AntdApp,
  Button,
  Descriptions,
  Empty,
  Form,
  Input,
  Modal,
  Space,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import {
  ApiOutlined,
  CheckCircleOutlined,
  CloseCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  LoadingOutlined,
  PlusOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import {
  createProxy,
  deleteProxy,
  listProxies,
  probeAllProxies,
  probeProxy,
  testProxy,
  testProxyDraft,
  updateProxy,
} from '../api';
import LoadError from '../components/load-error';
import PageLoading from '../components/page-loading';
import { useAppConfig } from '../config-store';
import type {
  ApiResult,
  AppConfig,
  ProxyEntry,
  ProxyInput,
  ProxyPatch,
  ProxyProbeAllResult,
  ProxyTestResult,
} from '../types';
import { formatDateTime } from '../utils';
import type { SidebarGroup, SidebarItem, SidebarSelection } from '../components/page-sidebar';

/**
 * v0.5.15: 探测延迟的人类可读格式。探测是纯 TCP 建连，正常在毫秒级；
 * 缺失 / 非有限值 / 0 统一显示「—」，跟状态列的"未探测"对齐。
 */
function formatLatency(ms?: number): string {
  if (ms === undefined || !Number.isFinite(ms) || ms <= 0) {
    return '—';
  }
  return ms >= 10 ? `${Math.round(ms)} ms` : `${ms.toFixed(1)} ms`;
}

/**
 * v0.5.37.4：从代理 URL 取协议（scheme，小写、去冒号），作为侧栏「协议」分组的分桶键。
 * 解析失败 / 为空归「未知」；只看 url 本身，不碰代理的其它字段。
 */
function proxyProtocolOf(url: string | undefined): string {
  const raw = String(url ?? '').trim();
  if (!raw) return '未知';
  try {
    const scheme = new URL(raw).protocol.replace(/:$/, '').toLowerCase();
    return scheme || '未知';
  } catch {
    return '未知';
  }
}

/**
 * v0.5.37.4：探测状态分桶键 —— 与「状态」列同一口径（ok / failed / 其余一律 untested）。
 * 注意只认**已落库**的 lastProbeStatus：行上那个临时的「探测中」标签不参与分桶，
 * 否则一次批量探测会让侧栏计数先跳到别处再跳回来。
 */
function probeBucketOf(p: ProxyEntry): 'ok' | 'failed' | 'untested' {
  const s = p.lastProbeStatus || '';
  if (s === 'ok') return 'ok';
  if (s === 'failed') return 'failed';
  return 'untested';
}

const PROBE_LABELS: Record<'ok' | 'failed' | 'untested', string> = {
  ok: '可用',
  failed: '不可用',
  untested: '未探测',
};

/**
 * v0.6.15 (UI-1): 「状态」列四种状态共用的定宽样式。
 *
 * 「探测中」(3 字) 与「可用」(2 字) 的文字宽度不同，antd <Table> 在 auto layout
 * 下按单元格内容重算列宽 —— 一次探测完成的那一帧，整表重排把右侧固定列的首按钮
 * 挤到文字截断（「探测中…」只露前半截）。给 Tag 定宽 + 文字居中，四种状态同宽，
 * 列宽不再随状态跳变。
 *
 * 宽度取最长的一种状态「不可用」(3 字带图标) 再留一点余量。
 */
const STATUS_TAG_STYLE: CSSProperties = {
  minWidth: 88,
  display: 'inline-flex',
  alignItems: 'center',
  justifyContent: 'center',
  marginInlineEnd: 0,
};

interface Props {
  config: AppConfig | null;
  /**
   * v0.5.37.4：侧栏选择状态。两组，跨组 AND：
   *   - protocol：按代理 URL 的 scheme 分桶
   *   - probe：按已落库的探测状态分桶（可用 / 不可用 / 未探测）
   */
  sidebarFilter: SidebarSelection;
  /** v0.5.37.4：把真实分组（含真实计数 badge）上浮给 App，由 App 统一下发到侧栏。 */
  onPublishGroups: (groups: SidebarGroup[]) => void;
}

interface FormValues {
  name: string;
  url: string;
  username?: string;
  password?: string;
  note?: string;
}

/**
 * v0.6.13 (UI): 把「测试连接」的结果从弹窗内嵌 Alert 改成 message toast,
 * 与本仓「探测 N 个代理」等其他动作共用一条 message channel。具体分类
 * 保留原来的状态码分档(2xx 绿,4xx 蓝,5xx 黄,传输层红),只是渲染载体
 * 从 <Alert> 换到 message.X。两处测试 handler 共用本函数,免得文案漂移。
 *
 * 状态码分档的历史(留作 context,防后人翻 CHANGELOG):
 * - v0.5.13 起:新增/编辑弹窗 + 行内测试共用一份渲染
 * - v0.5.19:2xx 绿 / 4xx 蓝(代理可达,目标按业务规则拒绝) /
 *   5xx 黄(代理可达,上游异常) / 传输层失败红;之前 ok:true 一律绿
 *   会把 Docker Hub / ghcr.io / quay.io 对匿名 /v2/ 的标准 401 误报成
 *   「连通失败」(0.5.18 之前的洞,0.5.19 修)。
 */
function proxyResultToast(
  message: {
    success: (s: string) => void;
    info: (s: string) => void;
    warning: (s: string) => void;
    error: (s: string) => void;
  },
  result: ProxyTestResult,
) {
  const status = result.status;
  if (!result.ok) {
    message.error(`连通失败：${result.error}`);
    return;
  }
  if (status === undefined) {
    message.success(`连通正常 · ${result.elapsedMs} ms`);
    return;
  }
  if (status < 400) {
    message.success(`连通正常 · HTTP ${status} · ${result.elapsedMs} ms`);
    return;
  }
  if (status < 500) {
    message.info(`代理可达 · HTTP ${status} · ${result.elapsedMs} ms`);
    return;
  }
  message.warning(`代理可达,上游异常 · HTTP ${status} · ${result.elapsedMs} ms`);
}

export default function ProxiesPage({ config, sidebarFilter, onPublishGroups }: Props) {
  const { message, modal } = AntdApp.useApp();
  const [proxies, setProxies] = useState<ProxyEntry[]>([]);
  // v0.5.9: per-row spinner for the '立即探测' button.
  const [probingIds, setProbingIds] = useState<Record<string, boolean>>({});
  // v0.5.12: 「探测全部」的页头按钮 loading。批量时每一行也会被点亮成"探测中"，
  // 但那个共用 probingIds —— 这个 state 只表达"整批还在飞"。
  const [probingAll, setProbingAll] = useState(false);
  const [editing, setEditing] = useState<ProxyEntry | null>(null);
  const [form] = Form.useForm<FormValues>();
  const [modalOpen, setModalOpen] = useState(false);
  const [error, setError] = useState<ApiResult<unknown> | null>(null);
  /**
   * v0.5.18（F6）：配置从模块级 store 取，不再自己拉。
   *
   * 「allowProxies=false」是正常态（这一页本来就不该管代理），请求失败则是
   * 故障态 —— 过去两者都只画一张空表，用户看不出该去改开关还是该重试。
   */
  const { failure: configFailure, loading: configLoading, reload: reloadConfig } = useAppConfig();

  /** 连通性测试弹窗状态。 */
  const [testing, setTesting] = useState<ProxyEntry | null>(null);
  const [testTarget, setTestTarget] = useState('');
  const [testRunning, setTestRunning] = useState(false);

  /** v0.5.13: 新增弹窗内的「测试连接」（保存前试连，不落库）。 */
  const [draftTarget, setDraftTarget] = useState('');
  const [draftTesting, setDraftTesting] = useState(false);
  /**
   * v0.5.13: footer 改成自定义按钮之后，antd 不再代管提交按钮的加载态。这里自己
   * 兜两件事 —— 保存期间有忙碌反馈；连点两次不会建出两条。
   */
  const [saving, setSaving] = useState(false);
  // v0.5.41: 首屏拉列表的 loading —— 之前切到本页直接显示「空表」,配 TableSkeleton。
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    if (!config?.allowProxies) {
      setLoading(false); // v0.5.41: 禁用管代理也收口,免得切回来一直转
      return;
    }
    const result = await listProxies();
    if (result.success && result.data) {
      setProxies(result.data);
      setError(null);
    } else {
      setError(result);
    }
    setLoading(false); // v0.5.41: 收尾 —— 即便失败也退掉骨架,展示错误
  }, [config?.allowProxies]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  /**
   * v0.5.37.4：侧栏分组 —— 两组都只放**视图状态**（过滤），有副作用的操作（探测全部 / 新增）
   * 留在页头。badge 全部来自真实代理列表：
   *   - 协议：代理 URL 的 scheme 分桶（http / https / 其它 / 未知）
   *   - 状态：可用 / 不可用 / 未探测（与「状态」列同一口径，见 probeBucketOf）
   * 代理列表为空时下发空组，侧栏显示占位文案而不是空壳。
   * 注意：本部署禁止管代理时 refresh 提前返回、列表恒为空，这里自然下发空组。
   */
  const sidebarGroups = useMemo<SidebarGroup[]>(() => {
    if (proxies.length === 0) return [];

    const protocolCounts = new Map<string, number>();
    proxies.forEach((p) => {
      const scheme = proxyProtocolOf(p.url);
      protocolCounts.set(scheme, (protocolCounts.get(scheme) ?? 0) + 1);
    });
    const protocolItems: SidebarItem[] = Array.from(protocolCounts.entries())
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .map(([scheme, count]) => ({ key: scheme, label: scheme.toUpperCase(), badge: count }));

    const bucketCount = (want: 'ok' | 'failed' | 'untested') =>
      proxies.filter((p) => probeBucketOf(p) === want).length;
    const probeItems: SidebarItem[] = [
      { key: 'all', label: '全部', badge: proxies.length },
      { key: 'ok', label: PROBE_LABELS.ok, badge: bucketCount('ok') },
      { key: 'failed', label: PROBE_LABELS.failed, badge: bucketCount('failed') },
      { key: 'untested', label: PROBE_LABELS.untested, badge: bucketCount('untested') },
    ];

    return [
      {
        key: 'protocol',
        label: '协议',
        items: [{ key: 'all', label: '全部', badge: proxies.length }, ...protocolItems],
      },
      { key: 'probe', label: '状态', items: probeItems },
    ];
  }, [proxies]);

  useEffect(() => {
    onPublishGroups(sidebarGroups);
  }, [onPublishGroups, sidebarGroups]);

  /** 表格数据 = 全量代理按侧栏两组选择做 AND 过滤。 */
  const visibleProxies = useMemo(() => {
    const protocol = sidebarFilter.protocol ?? null;
    const probe = sidebarFilter.probe ?? null;
    return proxies.filter((p) => {
      if (protocol && protocol !== 'all' && proxyProtocolOf(p.url) !== protocol) {
        return false;
      }
      if (
        probe &&
        probe !== 'all' &&
        probeBucketOf(p) !== (probe as 'ok' | 'failed' | 'untested')
      ) {
        return false;
      }
      return true;
    });
  }, [proxies, sidebarFilter]);

  /**
   * v0.5.12: 单条探测的**唯一**实现，逐行按钮与"新增后即测"共用。
   *
   * 之所以抽成一个函数：v0.5.9 引入的 handleProbe 里调用了 `probeProxy`
   * 却从未 import 它，点击后整段逻辑在运行期抛 ReferenceError，
   * 表现就是"点了没反应"。构建脚本只跑 vite build（不含 tsc），所以没拦住。
   * 顺带补上 catch：api 层若改回抛异常，也会变成可见提示而不是静默失败。
   */
  /**
   * v0.6.15 (UI-2): probeOne 的结果不再由本函数自己 toast，改为把结果 return
   * 给调用方决定怎么播报。
   *
   * 之前「新建代理」提交路径上会连弹两条：先「已创建代理，正在探测连通性…」，
   * 再「Bigops53 可用 · 延迟 0.9 ms」—— 探测是纯 TCP 建连（同网段通常 < 5ms），
   * 两条 toast 几乎同时出现，观感是同一件事被播报两次。
   *
   * 现在逐行「探测」按钮的调用方仍会播报（行为不变），只有「新建后自动探测」
   * 这条路径改成合并成一条。
   *
   * 返回值语义：探测**确实拿到结论**时返回该结论（含 ok=false 的不可用），
   * 请求本身失败 / 抛异常时返回 null（此时本函数已经自己播报过错误）。
   */
  const probeOne = async (id: string, name: string, opts?: { silent?: boolean }) => {
    const silent = opts?.silent === true;
    setProbingIds((prev) => ({ ...prev, [id]: true }));
    let outcome: { ok: boolean; latencyMs?: number; error?: string } | null = null;
    try {
      const r = await probeProxy(id);
      if (r.success && r.data) {
        const out = r.data;
        if (silent) {
          // 调用方（新建后自动探测）会自己合并成一条 toast，这里不播报。
          outcome = { ok: out.ok, latencyMs: out.latencyMs, error: out.error };
        } else if (out.ok) {
          // v0.5.15: 探测现在会带回建连延迟，顺手一起告诉用户。
          message.success(`${name} 可用 · 延迟 ${formatLatency(out.latencyMs)}`);
        } else {
          message.warning(`${name} 不可用: ${out.error || '未知错误'}`);
        }
      } else if (!silent) {
        message.error(r.message ?? '探测失败');
      }
    } catch (err) {
      // 异常路径不合并：silent 模式下调用方拿不到 outcome，必须让本函数自己报。
      message.error(`探测 ${name} 出错: ${String((err as Error)?.message ?? err)}`);
    } finally {
      // 先清"探测中"再刷新：反过来的话刷完列表状态又盖回旧值。
      setProbingIds((prev) => ({ ...prev, [id]: false }));
      await refresh();
    }
    return outcome;
  };

  const handleOpenCreate = () => {
    setEditing(null);
    form.resetFields();
    setDraftTarget('');
    setSaving(false);
    setModalOpen(true);
  };

  const handleOpenEdit = (p: ProxyEntry) => {
    setEditing(p);
    form.resetFields();
    form.setFieldsValue({
      name: p.name,
      url: p.url,
      username: p.username,
      // 密码不回显：留空表示不改
      password: '',
      note: p.note,
    });
    setDraftTarget('');
    setSaving(false);
    setModalOpen(true);
  };

  const handleSubmit = async () => {
    if (saving) {
      return;
    }
    let values: FormValues;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    // v0.5.13: 表单校验通过后才上锁；校验失败直接返回，不闪加载态。
    setSaving(true);
    // v0.5.12: 新建成功后要立刻探一次连通性。代理此时已经落库，所以这里只是
    // 记住它，等 modal 关掉、列表刷出来之后再单独探 —— 不让探测拖慢"保存"。
    let created: ProxyEntry | null = null;
    try {
      if (editing) {
        const patch: ProxyPatch = {
          name: values.name,
          url: values.url,
          username: values.username ?? '',
          note: values.note,
        };
        // 密码留空 = 不修改；想改成匿名代理请把用户名也清空。
        if (values.password !== undefined && values.password !== '') {
          patch.password = values.password;
        }
        const result = await updateProxy(editing.id, patch);
        if (!result.success) {
          message.error(result.message || '更新失败');
          return;
        }
        message.success('已更新代理');
      } else {
        const input: ProxyInput = {
          name: values.name,
          url: values.url,
          username: values.username ?? '',
          password: values.password ?? '',
          note: values.note,
        };
        const result = await createProxy(input);
        if (!result.success) {
          message.error(result.message || '创建失败');
          return;
        }
        created = result.data ?? null;
      }
      setModalOpen(false);
      await refresh();
      if (created) {
        // 探测最坏 5 秒（internal/proxies.Probe 的总预算）。此刻 modal 已关、
        // 列表已刷新，用户在行上能看到"探测中"再变成最终状态，不会以为卡住了。
        //
        // v0.6.15 (UI-2): silent=true 走合并路径 —— probeOne 不再自己播报 toast，
        // 本函数按探测结果分流成一条：
        //   - 可用     → success   「已创建代理 Bigops53，可用 · 延迟 0.9 ms」
        //   - 不可用   → warning   「已创建代理 Bigops53，但探测失败 · <原因>」
        //                （必须明确「已创建」—— 代理已落库，不能让用户误以为失败
        //                去重填表单）
        //   - 探测请求失败 / 异常 → 退到两条 toast（旧形态）：
        //     「已创建代理 Bigops53」+ probeOne 自己的 error toast
        //   （silent 模式下探测请求失败 → outcome=null → 走 fallback）
        const outcome = await probeOne(created.id, created.name, { silent: true });
        if (outcome?.ok) {
          message.success(
            `已创建代理 ${created.name}，可用 · 延迟 ${formatLatency(outcome.latencyMs)}`,
          );
        } else if (outcome) {
          message.warning(
            `已创建代理 ${created.name}，但探测失败：${outcome.error || '未知错误'}`,
          );
        } else {
          // 探测请求没拿到结论（请求层失败 / 异常）。代理已落库，先告知成功
          // 创建这一段事实，避免用户以为"创建也没成功"。
          message.success(`已创建代理 ${created.name}`);
        }
      }
    } catch (err) {
      message.error(String((err as Error)?.message ?? err));
    } finally {
      // 成功、失败、提前 return 都要落回可点状态，否则弹窗按钮永久转圈。
      setSaving(false);
    }
  };

  /** v0.5.12: 逐行"探测"按钮的入口，逻辑全在 probeOne。 */
  const handleProbe = (p: ProxyEntry) => {
    void probeOne(p.id, p.name);
  };

  const handleOpenTest = (p: ProxyEntry) => {
    setTesting(p);
    setTestTarget('');
  };

  const handleRunTest = async () => {
    if (!testing) return;
    setTestRunning(true);
    try {
      const result = await testProxy(testing.id, testTarget.trim() || undefined);
      // v0.6.13 (UI): 走 message toast 而不是 setTestResult + <Alert>
      // —— 见文件顶部的 v0.6.13 (UI) 注释。
      if (!result.success) {
        message.error(`连通失败：${result.message}`);
        return;
      }
      if (result.data) {
        proxyResultToast(message, result.data);
      }
    } finally {
      setTestRunning(false);
    }
  };

  /**
   * v0.5.13: 保存前试连。只做「地址非空 + http(s)://」这一层校验 ——
   * 名称等字段不拦测试，因为试连的意义就是在填完一堆校验之前先知道通不通。
   * 服务端不落库，所以这里的结果不代表任何已存条目。
   *
   * v0.5.15: 编辑态复用同一个入口。编辑时密码框刻意留空（留空 = 不改），
   * 所以把条目 id 一并带上：服务端在「密码为空 + 用户名与存量一致」时会回退用
   * 已存的密码去试连 —— 否则一进编辑弹窗点「测试连接」必然是 407。
   */
  const handleTestDraft = async () => {
    const values = form.getFieldsValue() as Partial<FormValues>;
    const url = (values.url ?? '').trim();
    const target = draftTarget.trim();
    // v0.6.13 (UI): 前置校验也走 message toast,不再写状态:
    //   - 空 url:warning
    //   - scheme 不对:warning
    //   - 服务端失败:error
    //   - 服务端成功:proxyResultToast (按状态码分档)
    if (!url) {
      message.warning('请先填写代理地址');
      return;
    }
    if (!/^https?:\/\//i.test(url)) {
      message.warning('代理地址需要以 http:// 或 https:// 开头，例如 http://proxy.example.com:8080');
      return;
    }
    setDraftTesting(true);
    try {
      const result = await testProxyDraft({
        // v0.5.15: 编辑态带上 id，服务端据此在密码留空时回退存量密码。
        id: editing?.id,
        url,
        username: values.username ?? '',
        password: values.password ?? '',
        targetUrl: target,
      });
      if (!result.success) {
        message.error(`连通失败：${result.message}`);
        return;
      }
      if (result.data) {
        proxyResultToast(message, result.data);
      }
    } finally {
      setDraftTesting(false);
    }
  };

  /**
   * v0.5.12: 删除只保留这一层确认。
   * 之前是 Popconfirm 再套 modal.confirm，而"影响面"说明只在第二层，
   * 等于必须连点两次才删得掉。现在留信息量更大的这一层。
   */
  const handleDelete = (p: ProxyEntry) => {
    modal.confirm({
      title: `删除代理「${p.name}」？`,
      content: '不会影响已完成的拉取，但引用它的任务会立即失败。',
      okText: '删除',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        const result = await deleteProxy(p.id);
        if (!result.success) {
          message.error(result.message || '删除失败');
          return;
        }
        message.success('已删除代理');
        await refresh();
      },
    });
  };

  /**
   * v0.5.12: 「探测全部」走服务端的批量端点，而不是前端 for 循环调 N 次。
   * 服务端 ProbeAll 并行探测，整批耗时约等于单条（最坏 5 秒）；
   * 前端循环则是 N×5 秒，而且中途每行的状态会来回闪。
   */
  const handleProbeAll = async () => {
    if (proxies.length === 0) {
      message.info('还没有代理可探测');
      return;
    }
    setProbingAll(true);
    // 整表点亮"探测中"：批量结果等最后一个回来才一起出，逐行 spinner 表达不了。
    const allProbing: Record<string, boolean> = {};
    for (const p of proxies) {
      allProbing[p.id] = true;
    }
    setProbingIds(allProbing);
    try {
      const r = await probeAllProxies();
      if (!r.success || !r.data) {
        message.error(r.message ?? '批量探测失败');
        return;
      }
      const out: ProxyProbeAllResult = r.data;
      const suffix = out.unknown > 0 ? `，${out.unknown} 未出结果` : '';
      const text = `已探测 ${out.total} 个代理：${out.ok} 可用 / ${out.failed} 不可用${suffix}`;
      if (out.failed === 0 && out.unknown === 0) {
        message.success(text);
      } else {
        message.warning(text);
      }
    } catch (err) {
      message.error(`批量探测出错: ${String((err as Error)?.message ?? err)}`);
    } finally {
      setProbingAll(false);
      setProbingIds({});
      await refresh();
    }
  };

  const columns: ColumnsType<ProxyEntry> = [
    {
      title: '名称',
      key: 'name',
      width: 200,
      render: (_, p) => (
        <Space direction="vertical" size={0} style={{ lineHeight: 1.3 }}>
          <strong>{p.name}</strong>
          {p.note ? (
            <span style={{ fontSize: 12, color: 'var(--color-text-3)' }}>{p.note}</span>
          ) : null}
        </Space>
      ),
    },
    {
      title: '代理地址',
      key: 'url',
      // v0.6.15 (UI-1): 给「代理地址」显式宽度。原来没设 width,antd auto layout
      // 会按 URL 文本长度重算列宽——一条 `http://user:pwd@very-long-host.example.com:8080`
      // 进来整张表就重排,把右侧「状态」+「操作」列挤到抖动。定宽后这一列不再
      // 随数据变化,其他列也因此稳定。250px 容纳中等长度的 `http://host:port`,
      // 极长 URL 由 ellipsis 自然截断(下文 `mono` span 不需要 `overflow:hidden`—
      // antd Cell 默认会处理)。
      width: 220,
      render: (_, p) => <span className="mono">{p.url}</span>,
    },
    {
      title: '认证',
      key: 'hasAuth',
      width: 140,
      render: (_, p) =>
        p.hasAuth ? (
          <Tag color="blue" icon={<CheckCircleOutlined />}>
            {p.username}
          </Tag>
        ) : (
          <Tag icon={<CloseCircleOutlined />}>匿名</Tag>
        ),
    },
    {
      title: '更新时间',
      key: 'updatedAt',
      width: 150,
      render: (_, p) => <Tooltip title={p.updatedAt}>{formatDateTime(p.updatedAt)}</Tooltip>,
    },
    {
      /**
       * v0.6.18: 「状态」与「延迟」合并成一列,文案 `可用 (0.9 ms)` /
       * `不可用 (—)` / `探测中 (…)` / `未探测 (—)`。
       *
       * 用户诉求:两列内容天然相关(都来自同一探测动作),分两列看反而要
       * 视觉跳一行找;合并后整表少一列 = 列宽和变小,横滚风险也跟着下降。
       *
       * 「不可用」时延迟显示 `—` 而不是 lastProbeLatencyMs: 探测失败时
       * TCP 都没建连成功,延迟没有意义;用一个明确的占位符让用户区分
       * 「还没探过」与「探过但失败」。
       *
       * 探测中的延迟显示 `…`,让用户看到「还在跑」不是「卡死」;
       * 实际不暴露未完成的数字,因为 probing 时 latency 还没回。
       */
      title: '状态',
      key: 'statusLatency',
      width: 180,
      render: (_, p) => {
        if (probingIds[p.id]) {
          return (
            <Space size={4}>
              <Tag color="processing" icon={<LoadingOutlined />} style={STATUS_TAG_STYLE}>
                探测中
              </Tag>
              <span className="mono" style={{ color: 'var(--color-text-3)' }}>…</span>
            </Space>
          );
        }
        const s = p.lastProbeStatus || '';
        if (s === 'failed') {
          return (
            <Tooltip title={p.lastProbeError || '探测失败'}>
              <Space size={4}>
                <Tag color="error" icon={<CloseCircleOutlined />} style={STATUS_TAG_STYLE}>
                  不可用
                </Tag>
                <span className="mono" style={{ color: 'var(--color-text-3)' }}>—</span>
              </Space>
            </Tooltip>
          );
        }
        const label = s === 'ok' ? '可用' : '未探测';
        const color = s === 'ok' ? 'success' : 'default';
        const icon = s === 'ok' ? <CheckCircleOutlined /> : null;
        return (
          <Space size={4}>
            <Tag color={color} icon={icon} style={STATUS_TAG_STYLE}>
              {label}
            </Tag>
            <span className="mono">{formatLatency(p.lastProbeLatencyMs)}</span>
          </Space>
        );
      },
    },
    {
      title: '最后探测',
      key: 'lastProbeAt',
      width: 150,
      render: (_, p) =>
        p.lastProbeAt ? (
          <Tooltip title={p.lastProbeAt + (p.lastProbeError ? ' — ' + p.lastProbeError : '')}>
            {formatDateTime(p.lastProbeAt)}
          </Tooltip>
        ) : (
          <span style={{ color: 'var(--color-text-3)' }}>—</span>
        ),
    },
    {
      title: '操作',
      key: 'actions',
      // v0.5.12: 230 → 250，容纳"探测中…"（比"探测"宽）。
      width: 250,
      fixed: 'right',
      render: (_, p) => (
        <Space size={4}>
          <Button
            type="link"
            size="small"
            icon={<ApiOutlined />}
            loading={probingIds[p.id]}
            onClick={() => void handleProbe(p)}
          >
            {probingIds[p.id] ? '探测中…' : '探测'}
          </Button>
          <Button type="link" size="small" onClick={() => handleOpenTest(p)}>
            测试
          </Button>
          <Button type="link" size="small" icon={<EditOutlined />} onClick={() => handleOpenEdit(p)}>
            编辑
          </Button>
          {/* v0.5.12: 这里不再套 Popconfirm —— 删除确认由 handleDelete 的
              modal.confirm 单独负责，那一层才写了"影响面"。 */}
          <Button type="link" size="small" danger icon={<DeleteOutlined />} onClick={() => handleDelete(p)}>
            删除
          </Button>
        </Space>
      ),
    },
  ];

  if (config && !config.allowProxies) {
    const err = config.credentialError;
    const keyMissing = !err || err.code === 'CREDENTIAL_KEY_MISSING';
    return (
      <div className="page">
        <div className="page-header">
          <div>
            <h2 className="page-title">代理管理</h2>
            <p className="page-subtitle">管理访问外部源时使用的 HTTP 代理。</p>
          </div>
        </div>
        <Alert
          type="warning"
          showIcon
          message={
            keyMissing
              ? '服务端未配置 REGISTRY_CREDENTIAL_KEY，代理库不可用。'
              : `加密存储初始化失败（${err.code}）`
          }
          description={
            keyMissing ? (
              <div>
                代理可能带账号密码，因此与凭据库共用同一套加密存储和密钥。请设置环境变量{' '}
                <span className="mono">REGISTRY_CREDENTIAL_KEY</span> 后重启服务。
              </div>
            ) : (
              <div style={{ whiteSpace: 'pre-wrap' }}>{err.message}</div>
            )
          }
        />
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h2 className="page-title">代理管理</h2>
          {/* 只写"这页能做什么"。本 registry 自身的代理配在哪、为什么分两处 ——
              属于设计说明，见 docs/design.md §3.1。 */}
          <p className="page-subtitle">管理访问外部源时使用的 HTTP 代理。</p>
        </div>
        <div className="page-actions">
          <Tooltip title="逐个探测所有代理的 ip:端口是否连得通，并测出建连延迟（不穿过代理、不发任何请求），结果更新状态列和延迟列">
            <Button
              icon={<ThunderboltOutlined />}
              loading={probingAll}
              disabled={proxies.length === 0}
              onClick={() => void handleProbeAll()}
            >
              探测全部
            </Button>
          </Tooltip>
          <Button type="primary" icon={<PlusOutlined />} onClick={handleOpenCreate}>
            新增代理
          </Button>
        </div>
      </div>

      {!config ? (
        /* 配置读不到：「是否允许管代理」都无从判断，空表会把故障伪装成"还没添加"。 */
        configFailure ? (
          <LoadError
            title="服务配置加载失败，代理列表无法确认"
            failure={configFailure}
            retrying={configLoading}
            onRetry={() => void reloadConfig()}
          />
        ) : (
          /* 还没拿到结果（首屏在途）：比空表诚实，也不与「本部署禁止管代理」混淆。 */
          <div className="panel" style={{ padding: 16 }}>
            <Empty description="正在读取服务配置…" />
          </div>
        )
      ) : (
        <>
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

          <div className="panel">
            {/* v0.6.21: loading 走 PageLoading 居中 spinner —— 之前的灰块骨架
                shimmer「一闪一闪」,换成 spinner 更明确「在等接口」。 */}
            {loading && proxies.length === 0 ? (
              <PageLoading tip="正在读取代理列表…" />
            ) : (
              <Table<ProxyEntry>
                rowKey="id"
                size="middle"
                columns={columns}
                dataSource={visibleProxies}
                pagination={false}
                /*
                 * v0.6.18: 不再设 tableLayout="fixed" 也不给 scroll.x。
                 *
                 * 0.6.15 一开始用 scroll.x=1200 是为了"容器宽度 < 1200 时整表
                 * 横滚一次,避免纵向溢出后的双滚动条"。但实测里 UAT 用户的
                 * 容器宽度 ≥ 1200(scroll.x 不触发);而在更窄的窗口里反而
                 * 出现了第二条横向滚动条,跟 0.6.15 想消除的"双滚动条"形态
                 * 不一样,但视觉上仍然让用户困扰。
                 *
                 * 这版重新算列宽让总和 ≈ 1100px,删 scroll.x:
                 *   - 容器够宽 → 整表自然铺开,无横向滚动
                 *   - 容器窄 → 列宽固定 (Width 都已显式)+ 内容 ellipsis,
                 *     文字自然被裁,不会有滚动条 —— 这才是"无滚动"的本意
                 *   - 真要看完整 URL 字段,鼠标 hover Tooltip 看完整值
                 *
                 * 状态+延迟合并成一列(状态 latency column)是减少列宽和的
                 * 主要手段 —— 之前 8 列合计 1180,合并后 7 列约 1080。
                 */
                locale={{
                  emptyText:
                    proxies.length > 0
                      ? '当前筛选下没有代理，换个筛选条件试试'
                      : '还没有代理，点击右上「新增代理」',
                }}
              />
            )}
          </div>
        </>
      )}

      <Alert
        type="info"
        showIcon
        message="「探测」和「测试」是两件事"
        description={
          <div>
            <div>
              <strong>探测</strong>：对每个代理的 <span className="mono">ip:端口</span> 做一次纯 TCP 建连（不穿过代理、不发任何请求），结果写进「状态」列和「延迟」列。
            </div>
            <div style={{ marginTop: 4 }}>
              <strong>测试</strong>：实际穿过这个代理去访问一个目标，入口在每行的「测试」按钮，以及新增/编辑弹窗里的「测试连接」。
            </div>
            <div style={{ marginTop: 8 }}>下面这段说的是「测试」：</div>
            <div>
              代理本身没有可直接访问的资源，所以测试会<strong>实际穿过这个代理</strong>去访问一个目标。
            </div>
            <div style={{ marginTop: 4 }}>
              目标留空 = <span className="mono">https://registry-1.docker.io/v2/</span>（验证"能不能出外网"）;想测本仓库或别的 registry 就填完整 URL（含 <span className="mono">https://</span>）。超时上限 12 秒。
            </div>
          </div>
        }
      />

      <Modal
        open={modalOpen}
        title={editing ? `编辑代理：${editing.name}` : '新增代理'}
        onCancel={() => setModalOpen(false)}
        destroyOnClose
        footer={
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            {/* v0.5.15: 编辑态也保留这颗按钮 —— 改完地址最想确认的就是"到底通不通"。 */}
            <Button
              icon={<ApiOutlined />}
              loading={draftTesting}
              disabled={saving}
              onClick={() => void handleTestDraft()}
            >
              测试连接
            </Button>
            <span style={{ flex: 1 }} />
            <Button disabled={saving} onClick={() => setModalOpen(false)}>
              取消
            </Button>
            <Button type="primary" loading={saving} onClick={() => void handleSubmit()}>
              {editing ? '保存' : '创建'}
            </Button>
          </div>
        }
      >
        <Form<FormValues> form={form} layout="vertical" preserve={false}>
          <Form.Item
            label="名称"
            name="name"
            rules={[{ required: true, message: '请填写名称' }]}
            extra="仅本工具内部识别用，可写「公司统一出口」这类描述。"
          >
            <Input placeholder="内网代理" />
          </Form.Item>
          <Form.Item
            label="代理地址"
            name="url"
            rules={[
              { required: true, message: '请填写代理地址' },
              {
                // v0.5.49: 空值由上面的 required 独占负责,这条 validator 只在
                // 有值时跑「必须带协议头」检查。给空值短路也防住以后 message
                // 被改成跟 required 一样时双弹。
                validator: (_, value: string) => {
                  if (!value || !value.trim()) return Promise.resolve();
                  return /^https?:\/\//i.test(value.trim())
                    ? Promise.resolve()
                    : Promise.reject(new Error('需要以 http:// 或 https:// 开头，例如 http://proxy.example.com:8080'));
                },
              },
            ]}
            extra="只填 http://主机:端口，不要带路径，也不要把账号密码写进地址。"
          >
            <Input placeholder="http://proxy.example.com:8080" />
          </Form.Item>
          <Form.Item
            label="用户名（可选，匿名代理留空）"
            name="username"
            extra="留空 = 匿名代理，不会发送 Proxy-Authorization。"
          >
            <Input autoComplete="off" placeholder="proxy-user" />
          </Form.Item>
          <Form.Item
            label={editing ? '密码（留空保留原密码）' : '密码（可选）'}
            name="password"
            extra="密码不会回显；落盘前加密存储。"
          >
            <Input.Password autoComplete="new-password" placeholder="••••••" />
          </Form.Item>
          <Form.Item label="备注（可选）" name="note">
            <Input placeholder="例如：只有它能出外网" />
          </Form.Item>
          <Form.Item
            label="测试目标（可选，不保存）"
            extra={
              <>
                {/* v0.5.38: 默认目标从「本仓库 /v2/」改成 Docker Hub —— 实际场景里
                    代理测试的目的几乎都是「能不能出外网」,用 Docker Hub 更贴合直觉;
                    之前那种「本仓库 /v2/」在 RegistryURL 没配 scheme(裸 hostname)时会
                    报 `unsupported protocol scheme`。要测本仓库请显式填完整 URL。 */}
                留空 = <span className="mono">https://registry-1.docker.io/v2/</span>（验证「能不能出外网」）;想测本仓库或别的 registry 就填完整 URL（含 <span className="mono">https://</span>）。
              </>
            }
          >
            <Input
              placeholder="https://registry-1.docker.io/v2/"
              value={draftTarget}
              onChange={(e) => setDraftTarget(e.target.value)}
              allowClear
            />
          </Form.Item>
          {/* v0.6.13 (UI): 探测结果改走 message toast,见 handleTestDraft 注释。
              这里不再挂 <ProxyTestAlert>,删掉以免旧状态在表单里残留误导用户。 */}
        </Form>
      </Modal>

      <Modal
        open={Boolean(testing)}
        title={testing ? `测试代理：${testing.name}` : '测试代理'}
        footer={null}
        onCancel={() => setTesting(null)}
        destroyOnClose
      >
        {testing ? (
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Descriptions size="small" column={1} bordered>
              <Descriptions.Item label="代理地址">
                <span className="mono">{testing.url}</span>
              </Descriptions.Item>
              <Descriptions.Item label="认证">
                {testing.hasAuth ? `${testing.username}（已配置）` : '匿名'}
              </Descriptions.Item>
            </Descriptions>

            <Input
              addonBefore="访问目标"
              placeholder="https://registry-1.docker.io/v2/"
              value={testTarget}
              onChange={(e) => setTestTarget(e.target.value)}
              allowClear
            />

            <Button type="primary" loading={testRunning} onClick={() => void handleRunTest()}>
              开始测试
            </Button>

            {/* v0.6.13 (UI): 走 message toast,见 handleRunTest 注释。 */}
          </Space>
        ) : null}
      </Modal>
    </div>
  );
}
