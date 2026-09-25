import { useEffect, useMemo, useState } from 'react';
import {
  Alert,
  App,
  Button,
  Descriptions,
  Empty,
  Form,
  Input,
  InputNumber,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import { ApiOutlined, DeleteOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import {
  fetchIgnoreRules,
  fetchStatsEvents,
  probeRegistry,
  purgeHeat,
  refreshInventory,
  removeIgnoreRule,
  updateConfig,
} from '../api';
import IgnoreRuleModal from '../components/ignore-rule-modal';
import type { ApiResult, AppConfig, IgnoreRules, Inventory } from '../types';
import { formatDateTime } from '../utils';

interface Props {
  config: AppConfig | null;
  onConfigChange: (config: AppConfig) => void;
  inventory: Inventory | null;
  onInventoryChange: (inventory: Inventory) => void;
}

/**
 * v0.5.9 amend B: 灰显值组件 — 显示当前值 + ✏️ 编辑图标按钮。
 * 不带校验、不带保存逻辑（由父级 useState + handlers 驱动）。
 */
function ReadonlyValue({
  value,
  mono,
  onEdit,
}: {
  value: string;
  mono?: boolean;
  onEdit?: () => void;
}) {
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
      {onEdit ? (
        <Button size="small" type="link" onClick={onEdit}>
          ✏️ 编辑
        </Button>
      ) : null}
    </div>
  );
}

export default function SettingsPage({ config, onConfigChange, inventory, onInventoryChange }: Props) {
  const { message, modal } = App.useApp();
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
  const [savingRegistryUrl, setSavingRegistryUrl] = useState(false);

  // v0.5.2: drafts for every other editable field. Server-side keys mirror
  // config.MutableKeys so the PATCH payload is just {mutable: {key: val}}.
  const [registryNameDraft, setRegistryNameDraft] = useState<string>('');
  const [allowDeleteDraft, setAllowDeleteDraft] = useState<boolean>(true);
  const [allowPullDraft, setAllowPullDraft] = useState<boolean>(true);
  const [statsRetentionDraft, setStatsRetentionDraft] = useState<number>(365);
  // v0.6.0: platform allow-list applied to multi-arch image indexes on
  // pull. Empty array = "pull every platform" (preserves pre-v0.6.0
  // behaviour); non-empty = only fetch children whose OS/architecture
  // matches. The server stores this as a CSV string under
  // config.MutableKey "pull.platforms"; the UI speaks a string array.
  const [pullPlatformsDraft, setPullPlatformsDraft] = useState<string[]>([]);
  // v0.5.3: registry self-auth (Basic). Password is NEVER seeded from
  // config -- server deliberately doesn't echo it back, so we always
  // start blank; user typing = "set", blank on save = "clear override".
  const [registryUsernameDraft, setRegistryUsernameDraft] = useState<string>('');
  const [registryPasswordDraft, setRegistryPasswordDraft] = useState<string>('');
  const [savingBulk, setSavingBulk] = useState(false);
  // v0.5.9: 灰显+编辑模式 — 同一时刻只编辑一个字段；点 ✏️ 进入编辑态，
  // 点 ✓ 发 PATCH，点 ✕ 还原草稿并退出编辑态。editingKey 是 settings
  // 表里的字段 key（"registry.url" 等）。
  const [editingKey, setEditingKey] = useState<string | null>(null);

  /** 进入编辑模式：把当前 Mutable 值拷贝到 draft（已通过 load effect 同步）。
   *  离开编辑模式：恢复 draft = 当前 Mutable（丢弃未保存的输入）。 */
  const beginEdit = (key: string) => {
    setEditingKey(key);
  };
  const cancelEdit = () => {
    setEditingKey(null);
    // 重置所有 draft 到当前 Mutable（useEffect 不会再跑，强制 reload）。
    if (!config) return;
    const m = config.mutable;
    setRegistryUrlDraft(m.registryUrl ?? '');
    setRegistryNameDraft(m.registryName ?? '');
    setAllowDeleteDraft(m.allowDelete ?? false);
    setAllowPullDraft(m.allowPull ?? false);
    setStatsRetentionDraft(m.statsRetentionDays ?? 365);
    setPullPlatformsDraft(
      (m.pullPlatforms ?? '').split(',').map((s) => s.trim()).filter(Boolean)
    );
    setRegistryUsernameDraft('');
    setRegistryPasswordDraft('');
  };


  const handleProbe = async () => {
    setProbing(true);
    setNotice(null);
    try {
      const result = await probeRegistry();
      setNotice(
        result.success
          ? { ...result, message: `连接成功（API ${result.data?.apiVersion ?? 'registry/2.0'}）` }
          : result
      );
    } finally {
      setProbing(false);
    }
  };

  const handleRefresh = async () => {
    setRefreshing(true);
    setNotice(null);
    try {
      const result = await refreshInventory();
      setNotice(result);
      if (result.success && result.data) {
        onInventoryChange(result.data);
        message.success(`扫描完成，用时 ${result.data.durationMs} ms`);
      }
    } finally {
      setRefreshing(false);
    }
  };

  const statsEnabled = config?.statsEnabled === true;

  // 规则与预览素材各取一次。热度不可用时服务端会回空结构，不用单独降级。
  useEffect(() => {
    if (!config) return;
    const m = config.mutable;
    setRegistryUrlDraft(m.registryUrl);
    setRegistryNameDraft(m.registryName ?? '');
    setAllowDeleteDraft(m.allowDelete ?? false);
    setAllowPullDraft(m.allowPull ?? false);
    setStatsRetentionDraft(m.statsRetentionDays ?? 365);
    // v0.6.0: server stores CSV under mutable.pullPlatforms; split it back
    // into the UI's array form. Empty CSV → empty array ("all platforms").
    setPullPlatformsDraft(
      (m.pullPlatforms ?? '')
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean),
    );
    setRegistryUsernameDraft(m.registryUsername ?? '');
    // Password: intentionally blank (server doesn't echo stored value).
  }, [config]);

  // v0.5.9 amend B: 每个字段独立保存 — 不再有"保存全部"。PATCH 只发一个 key。
  const patchKey = async (key: string, value: string) => {
    setSavingBulk(true);
    try {
      const r = await updateConfig({ mutable: { [key]: value } });
      if (r.success && r.data) {
        onConfigChange(r.data);
        message.success(`已保存`);
        setEditingKey(null);
      } else {
        message.error(r.message ?? '保存失败');
      }
    } catch (e) {
      message.error(`保存失败:${(e as Error).message ?? e}`);
    } finally {
      setSavingBulk(false);
    }
  };

  const handleSaveUrl = async () => {
    const v = registryUrlDraft.trim();
    if (v !== '' && !/^https?:\/\//.test(v)) {
      message.error('地址必须以 http:// 或 https:// 开头（清空则回退到 Docker Hub）');
      return;
    }
    await patchKey('registry.url', v);
  };
  const handleSaveName = async () => {
    await patchKey('registry.name', registryNameDraft.trim());
  };
  const handleSaveAllowDelete = async () => {
    await patchKey('allow.delete', allowDeleteDraft ? 'true' : 'false');
  };
  const handleSaveAllowPull = async () => {
    await patchKey('allow.pull', allowPullDraft ? 'true' : 'false');
  };
  const handleSaveStatsRetention = async () => {
    if (!Number.isFinite(statsRetentionDraft) || statsRetentionDraft < 1) {
      message.error('热度保留天数必须 >= 1');
      return;
    }
    await patchKey('stats.retention.days', String(statsRetentionDraft));
  };
  const handleSavePullPlatforms = async () => {
    await patchKey('pull.platforms', pullPlatformsDraft.join(','));
  };
  const handleSaveUsername = async () => {
    await patchKey('registry.username', registryUsernameDraft.trim());
  };
  const handleSavePassword = async () => {
    if (!config) return;
    const m = config.mutable;
    // 用户在编辑密码时输入空=显式清除；未输入(空)则视为不修改。
    // 用本地 sentinel: 我们把 useState 初值固定为 '',所以清空输入框表示
    // "我要把密码改成空"——这点跟旧 UX 一致。
    const v = registryPasswordDraft;
    await patchKey('registry.password', v);
    setRegistryPasswordDraft('');
  };


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

  return (
    <div className="page">
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
            重新扫描
          </Button>
        </div>
      </div>

      {/*
        只在有文案时渲染。成功路径的 message 是空串（扫描结果由顶部 toast 和下方
        「清单状态」展示），无条件渲染会得到一个没有内容的空绿框。
      */}
      {notice?.message ? (
        <Alert
          type={notice.success ? 'success' : 'warning'}
          showIcon
          closable
          onClose={() => setNotice(null)}
          message={notice.message}
        />
      ) : null}

      <div className="panel" style={{ padding: 16 }}>
        <Form layout="vertical" size="middle" colon={false}>
          {/* 仓库地址 */}
          <Form.Item
            label={<span>仓库地址（/前缀）</span>}
            extra="配置本仓库对外暴露的地址（docker login / docker push 用）。示例：http://registry.example.com:8787 或 https://devhub..io；写哪个客户端就连哪个，无需重启。"
          >
            {editingKey === 'registry.url' ? (
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <Input
                  className="mono"
                  value={registryUrlDraft}
                  onChange={(e) => setRegistryUrlDraft(e.target.value)}
                  disabled={savingBulk}
                  allowClear
                  style={{ maxWidth: 560 }}
                />
                <Button type="primary" loading={savingBulk} onClick={() => void handleSaveUrl()}>
                  保存
                </Button>
                <Button onClick={cancelEdit} disabled={savingBulk}>
                  取消
                </Button>
              </div>
            ) : (
              <ReadonlyValue
                value={config?.mutable.registryUrl || '(空 — pull 任务回退到 Docker Hub)'}
                mono
                onEdit={() => beginEdit('registry.url')}
              />
            )}
          </Form.Item>

          {/* Registry 认证 */}
          <Form.Item
            label={<span>Registry 认证</span>}
            extra={
              editingKey === 'registry.username' || editingKey === 'registry.password'
                ? '输入新用户名 / 密码覆盖；密码不回显；保存后立即生效，无需重启。'
                : config?.mutable.usingAuth
                  ? '当前已开启 Basic 认证；客户端需要先 docker login 才能 push/pull。'
                  : '留空 = 关闭认证（任何人可访问）。配了之后客户端需要 docker login。'
            }
          >
            {editingKey === 'registry.username' ? (
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <Input
                  placeholder="username"
                  value={registryUsernameDraft}
                  onChange={(e) => setRegistryUsernameDraft(e.target.value)}
                  style={{ maxWidth: 220 }}
                />
                <Button type="primary" loading={savingBulk} onClick={() => void handleSaveUsername()}>
                  保存
                </Button>
                <Button onClick={cancelEdit}>取消</Button>
              </div>
            ) : editingKey === 'registry.password' ? (
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <Input.Password
                  placeholder="新密码（输入即覆盖；清空 = 关闭认证）"
                  value={registryPasswordDraft}
                  onChange={(e) => setRegistryPasswordDraft(e.target.value)}
                  style={{ maxWidth: 420 }}
                />
                <Button type="primary" loading={savingBulk} onClick={() => void handleSavePassword()}>
                  保存
                </Button>
                <Button onClick={cancelEdit}>取消</Button>
              </div>
            ) : (
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <ReadonlyValue
                  value={
                    config?.mutable.usingAuth
                      ? '已开启（用户名 ' + (config.mutable.registryUsername || '?') + '）'
                      : '关闭（任何人都可访问）'
                  }
                />
                <Button size="small" type="link" onClick={() => beginEdit('registry.username')}>
                  ✏️ 改用户名
                </Button>
                <Button size="small" type="link" onClick={() => beginEdit('registry.password')}>
                  ✏️ 改密码
                </Button>
              </div>
            )}
          </Form.Item>

          {/* 展示名称 */}
          <Form.Item
            label={<span>展示名称</span>}
            extra="顶部 / 设置页显示名"
          >
            {editingKey === 'registry.name' ? (
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <Input
                  value={registryNameDraft}
                  onChange={(e) => setRegistryNameDraft(e.target.value)}
                  placeholder="内网离线镜像源"
                  style={{ maxWidth: 420 }}
                />
                <Button type="primary" loading={savingBulk} onClick={() => void handleSaveName()}>
                  保存
                </Button>
                <Button onClick={cancelEdit}>取消</Button>
              </div>
            ) : (
              <ReadonlyValue
                value={config?.mutable.registryName || '镜像仓库'}
                onEdit={() => beginEdit('registry.name')}
              />
            )}
          </Form.Item>

          {/* 允许删除 */}
          <Form.Item
            label={<span>允许删除</span>}
            extra="关闭后所有删除端点（仓库 / manifest-by-digest / GC）返回 403"
          >
            {editingKey === 'allow.delete' ? (
              <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                <Switch
                  checked={allowDeleteDraft}
                  onChange={setAllowDeleteDraft}
                  checkedChildren="启用"
                  unCheckedChildren="只读"
                />
                <Button type="primary" loading={savingBulk} onClick={() => void handleSaveAllowDelete()}>
                  保存
                </Button>
                <Button onClick={cancelEdit}>取消</Button>
              </div>
            ) : (
              <ReadonlyValue
                value={config?.mutable.allowDelete ? '启用' : '只读（关闭）'}
                onEdit={() => beginEdit('allow.delete')}
              />
            )}
          </Form.Item>

          {/* 允许拉取 */}
          <Form.Item
            label={<span>允许拉取</span>}
            extra="关闭后 /api/pull/* 写入端点拒绝"
          >
            {editingKey === 'allow.pull' ? (
              <div style={{ display: 'flex', gap: 12, alignItems: 'center' }}>
                <Switch
                  checked={allowPullDraft}
                  onChange={setAllowPullDraft}
                  checkedChildren="启用"
                  unCheckedChildren="禁用"
                />
                <Button type="primary" loading={savingBulk} onClick={() => void handleSaveAllowPull()}>
                  保存
                </Button>
                <Button onClick={cancelEdit}>取消</Button>
              </div>
            ) : (
              <ReadonlyValue
                value={config?.mutable.allowPull ? '启用' : '禁用'}
                onEdit={() => beginEdit('allow.pull')}
              />
            )}
          </Form.Item>

          {/* 热度保留天数 */}
          <Form.Item
            label={<span>热度保留天数</span>}
            extra="超过的天数会被 /api/stats/heat 自动清掉"
          >
            {editingKey === 'stats.retention.days' ? (
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <InputNumber
                  min={1}
                  max={3650}
                  value={statsRetentionDraft}
                  onChange={(v) => setStatsRetentionDraft(v ?? 365)}
                  style={{ width: 180 }}
                />
                <Button type="primary" loading={savingBulk} onClick={() => void handleSaveStatsRetention()}>
                  保存
                </Button>
                <Button onClick={cancelEdit}>取消</Button>
              </div>
            ) : (
              <ReadonlyValue
                value={`${config?.mutable.statsRetentionDays ?? 365} 天`}
                onEdit={() => beginEdit('stats.retention.days')}
              />
            )}
          </Form.Item>

          {/* 拉取平台白名单 */}
          <Form.Item
            label={<span>拉取平台白名单</span>}
            extra={
              editingKey === 'pull.platforms'
                ? '勾选目标平台；取消勾选 = 排除；保存后对下一个 pull 任务立即生效。'
                : pullPlatformsDraft.length === 0
                  ? '当前未启用过滤：多架构镜像会按上游索引全部拉取（与 v0.6.0 之前的行为一致）。'
                  : `已选 ${pullPlatformsDraft.length} 个：${pullPlatformsDraft.join(', ')}。`
            }
          >
            {editingKey === 'pull.platforms' ? (
              <div>
                <Space wrap>
                  {[
                    { id: 'linux/amd64', label: 'linux/amd64 (x86_64)' },
                    { id: 'linux/arm64', label: 'linux/arm64 (aarch64)' },
                    { id: 'linux/arm/v7', label: 'linux/arm/v7 (32-bit ARMv7)' },
                    { id: 'linux/386', label: 'linux/386' },
                    { id: 'linux/ppc64le', label: 'linux/ppc64le' },
                    { id: 'linux/s390x', label: 'linux/s390x' },
                    { id: 'linux/riscv64', label: 'linux/riscv64' },
                    { id: 'windows/amd64', label: 'windows/amd64' },
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
                  <Button type="primary" loading={savingBulk} onClick={() => void handleSavePullPlatforms()}>
                    保存
                  </Button>
                  <Button onClick={cancelEdit}>取消</Button>
                  {pullPlatformsDraft.length > 0 ? (
                    <Button type="link" onClick={() => setPullPlatformsDraft([])}>
                      清空（恢复全部）
                    </Button>
                  ) : null}
                </div>
              </div>
            ) : (
              <ReadonlyValue
                value={
                  pullPlatformsDraft.length === 0
                    ? '未启用（拉取所有平台）'
                    : pullPlatformsDraft.join(', ')
                }
                onEdit={() => beginEdit('pull.platforms')}
              />
            )}
          </Form.Item>
        </Form>
      </div>

      <Alert
        type="info"
        showIcon
        message="如何修改要管理的镜像仓库"
        description={
          <div>
            <div>这个工具一次管理一个 registry。地址通过环境变量或配置文件提供，改完重启服务即可。</div>
            <pre
              className="mono"
              style={{
                margin: '8px 0 0',
                padding: '10px 12px',
                background: 'var(--color-fill-1)',
                border: '1px solid var(--color-border-2)',
                borderRadius: 'var(--radius-sm)',
                fontSize: 12,
                lineHeight: 1.7,
                whiteSpace: 'pre-wrap',
              }}
            >{`# 方式一：环境变量
REGISTRY_URL=http://192.0.2.10:10001 \\
REGISTRY_PROXY=http://proxy.example.com:8080 \\
PORT=8787 pnpm start

# 方式二：项目根目录 registry.config.json
{
  "name": "内网离线镜像源",
  "url": "http://192.0.2.10:10001",
  "proxy": "",
  "cacheTtlSeconds": 60,
  "port": 8787
}`}</pre>
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
                  想完全关掉破坏性操作：在配置里设置 <span className="mono">allowDelete: false</span>
                  （或环境变量 <span className="mono">REGISTRY_ALLOW_DELETE=false</span>）后重启服务。
                </div>
              </>
            ) : (
              <div>
                服务端已设置 <span className="mono">allowDelete=false</span>，删除入口已隐藏，
                所有删除请求都会被拒绝。
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
