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

/** v0.5.2: small "env / db" tag reused on every editable row. */
function SourceTag({ source }: { source?: 'env' | 'db' }) {
  if (source === 'db') return <Tag color="purple">界面设置（已覆盖）</Tag>;
  return <Tag>环境变量</Tag>;
}

/** v0.5.2: inline style so the Form labels can render the SourceTag inline. */
function SourceTagStyles() {
  return <style>{`.ant-form-item-label label { display: inline-flex; align-items: center; gap: 6px; }`}</style>;
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
  const [registryProxyDraft, setRegistryProxyDraft] = useState<string>('');
  const [registryNameDraft, setRegistryNameDraft] = useState<string>('');
  const [cacheTtlDraft, setCacheTtlDraft] = useState<number>(60);
  const [allowDeleteDraft, setAllowDeleteDraft] = useState<boolean>(true);
  const [allowPullDraft, setAllowPullDraft] = useState<boolean>(true);
  const [allowRegistryEventsDraft, setAllowRegistryEventsDraft] = useState<boolean>(true);
  const [statsRetentionDraft, setStatsRetentionDraft] = useState<number>(365);
  // v0.5.3: registry self-auth (Basic). Password is NEVER seeded from
  // config -- server deliberately doesn't echo it back, so we always
  // start blank; user typing = "set", blank on save = "clear override".
  const [registryUsernameDraft, setRegistryUsernameDraft] = useState<string>('');
  const [registryPasswordDraft, setRegistryPasswordDraft] = useState<string>('');
  const [savingBulk, setSavingBulk] = useState(false);

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
    setRegistryProxyDraft(m.registryProxy ?? '');
    setRegistryNameDraft(m.registryName ?? '');
    setCacheTtlDraft(m.cacheTtlSeconds ?? 60);
    setAllowDeleteDraft(m.allowDelete ?? false);
    setAllowPullDraft(m.allowPull ?? false);
    setAllowRegistryEventsDraft(m.allowRegistryEvents ?? false);
    setStatsRetentionDraft(m.statsRetentionDays ?? 365);
    setRegistryUsernameDraft(m.registryUsername ?? '');
    // Password: intentionally blank (server doesn't echo stored value).
  }, [config]);

  const handleSaveRegistryUrl = async () => {
    if (!config) return;
    const v = registryUrlDraft.trim();
    if (v !== '' && !/^https?:\/\//.test(v)) {
      message.error('地址必须以 http:// 或 https:// 开头（清空则回退到 Docker Hub）');
      return;
    }
    setSavingRegistryUrl(true);
    try {
      const r = await updateConfig({ mutable: { registryUrl: v } });
      if (r.success && r.data) {
        onConfigChange(r.data);
        message.success(v === '' ? '已清除覆盖，恢复使用环境变量默认' : '已保存；新值对后续 pull 任务立即生效');
      } else {
        message.error(r.message ?? '保存失败');
      }
    } catch (e) {
      message.error(`保存失败：${(e as Error).message ?? e}`);
    } finally {
      setSavingRegistryUrl(false);
    }
  };

  // v0.5.2: save all non-URL settings in one PATCH. Only sends keys whose
  // draft differs from the current effective value, so re-clicking save
  // without changes is a no-op (saves a round-trip + db write).
  const handleSaveBulk = async () => {
    if (!config) return;
    const m = config.mutable;
    const patch: Record<string, string> = {};
    const trimmedProxy = registryProxyDraft.trim();
    if (trimmedProxy !== (m.registryProxy ?? '')) {
      if (trimmedProxy !== '' && !/^https?:\/\//.test(trimmedProxy)) {
        message.error('HTTP 代理地址必须以 http:// 或 https:// 开头');
        return;
      }
      patch['registry.proxy'] = trimmedProxy;
    }
    if (registryNameDraft.trim() !== (m.registryName ?? '')) {
      patch['registry.name'] = registryNameDraft.trim();
    }
    if (cacheTtlDraft !== (m.cacheTtlSeconds ?? 60)) {
      patch['cache.ttl.seconds'] = String(cacheTtlDraft);
    }
    if (allowDeleteDraft !== (m.allowDelete ?? false)) {
      patch['allow.delete'] = allowDeleteDraft ? 'true' : 'false';
    }
    if (allowPullDraft !== (m.allowPull ?? false)) {
      patch['allow.pull'] = allowPullDraft ? 'true' : 'false';
    }
    if (allowRegistryEventsDraft !== (m.allowRegistryEvents ?? false)) {
      patch['allow.registry_events'] = allowRegistryEventsDraft ? 'true' : 'false';
    }
    if (statsRetentionDraft !== (m.statsRetentionDays ?? 365)) {
      patch['stats.retention.days'] = String(statsRetentionDraft);
    }
    if (registryUsernameDraft.trim() !== (m.registryUsername ?? '')) {
      patch['registry.username'] = registryUsernameDraft.trim();
    }
    if (registryPasswordDraft !== '') {
      patch['registry.password'] = registryPasswordDraft;
    } else if (m.usingAuth) {
      // User cleared the password field while auth is on: explicit clear.
      patch['registry.password'] = '';
    }
    if (Object.keys(patch).length === 0) {
      message.info('没有变更');
      return;
    }
    setSavingBulk(true);
    try {
      const r = await updateConfig({ mutable: patch });
      if (r.success && r.data) {
        onConfigChange(r.data);
        message.success(`已保存 ${Object.keys(patch).length} 项设置`);
      } else {
        message.error(r.message ?? '保存失败');
      }
    } catch (e) {
      message.error(`保存失败：${(e as Error).message ?? e}`);
    } finally {
      setSavingBulk(false);
    }
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
      render: (source: 'env' | 'panel') =>
        source === 'env' ? (
          <Tooltip title="来自环境变量 REGISTRY_STATS_IGNORE_USERAGENTS，要改得改部署配置并重启">
            <Tag color="gold" style={{ marginInlineEnd: 0 }}>
              环境变量
            </Tag>
          </Tooltip>
        ) : (
          <Tag style={{ marginInlineEnd: 0 }}>界面</Tag>
        ),
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
          <Form.Item
            label={
              <span>
                默认上游地址{' '}
                <SourceTag source={config?.mutable.registryUrlSource} />
              </span>
            }
            extra="留空则使用 Docker Hub（pull.DefaultUpstream）。变更对下一个入队的拉取任务立即生效。"
          >
            <Space.Compact style={{ width: '100%', maxWidth: 560 }}>
              <Input
                className="mono"
                placeholder="http://my-mirror.example.com:5000"
                value={registryUrlDraft}
                onChange={(e) => setRegistryUrlDraft(e.target.value)}
                disabled={savingRegistryUrl}
                allowClear
              />
              <Button
                type="primary"
                loading={savingRegistryUrl}
                onClick={() => void handleSaveRegistryUrl()}
                disabled={!config || registryUrlDraft.trim() === (config.mutable.registryUrl ?? '')}
              >
                保存
              </Button>
            </Space.Compact>
          </Form.Item>

          <Form.Item
            label={<span>HTTP 代理 <SourceTag source={config?.mutable.registryProxySource} /></span>}
            extra="访问外部 registry 时走的上游 HTTP 代理。留空 = 直连。"
          >
            <Input
              className="mono"
              placeholder="http://proxy.example.com:8080"
              value={registryProxyDraft}
              onChange={(e) => setRegistryProxyDraft(e.target.value)}
              allowClear
            />
          </Form.Item>

          <Form.Item
            label={<span>Registry 认证 <SourceTag source={config?.mutable.usingAuth ? 'db' : 'env'} /></span>}
            extra={
              config?.mutable.usingAuth
                ? '当前已开启 Basic 认证；客户端需要先 docker login 才能 push/pull。修改后立即生效，不需要重启。'
                : '留空 = 关闭认证（任何人可访问）。配了之后客户端需要 docker login。密码不回显，提交时输入即覆盖。'
            }
          >
            <Space.Compact style={{ width: '100%', maxWidth: 560 }}>
              <Input
                placeholder="username"
                value={registryUsernameDraft}
                onChange={(e) => setRegistryUsernameDraft(e.target.value)}
                allowClear
                style={{ width: '40%' }}
              />
              <Input.Password
                placeholder="password（输入即覆盖；留空保留原值；当前已开启认证时清空输入框 = 关闭）"
                value={registryPasswordDraft}
                onChange={(e) => setRegistryPasswordDraft(e.target.value)}
              />
            </Space.Compact>
          </Form.Item>

          <Form.Item
            label={<span>展示名称 <SourceTag source={config?.mutable.registryNameSource} /></span>}
            extra="顶部 / 设置页显示名"
          >
            <Input
              value={registryNameDraft}
              onChange={(e) => setRegistryNameDraft(e.target.value)}
              placeholder="内网离线镜像源"
            />
          </Form.Item>

          <Form.Item
            label={<span>清单缓存（秒） <SourceTag source={config?.mutable.cacheTtlSecondsSource} /></span>}
            extra="清单拉取后在内存里保留多长时间"
          >
            <InputNumber
              min={1}
              max={86400}
              value={cacheTtlDraft}
              onChange={(v) => setCacheTtlDraft(v ?? 60)}
              style={{ width: 180 }}
            />
          </Form.Item>

          <Form.Item
            label={<span>允许删除 <SourceTag source={config?.mutable.allowDeleteSource} /></span>}
            extra="关闭后所有删除端点（仓库 / manifest-by-digest / GC）返回 403"
          >
            <Switch
              checked={allowDeleteDraft}
              onChange={setAllowDeleteDraft}
              checkedChildren="启用"
              unCheckedChildren="只读"
            />
          </Form.Item>

          <Form.Item
            label={<span>允许拉取 <SourceTag source={config?.mutable.allowPullSource} /></span>}
            extra="关闭后 /api/pull/* 写入端点拒绝"
          >
            <Switch
              checked={allowPullDraft}
              onChange={setAllowPullDraft}
              checkedChildren="启用"
              unCheckedChildren="禁用"
            />
          </Form.Item>

          <Form.Item
            label={<span>接收 registry events <SourceTag source={config?.mutable.allowRegistryEventsSource} /></span>}
            extra="关闭后 /api/events 直接 403；webhook 仍然配置但不会生效"
          >
            <Switch
              checked={allowRegistryEventsDraft}
              onChange={setAllowRegistryEventsDraft}
              checkedChildren="启用"
              unCheckedChildren="禁用"
            />
          </Form.Item>

          <Form.Item
            label={<span>热度保留天数 <SourceTag source={config?.mutable.statsRetentionDaysSource} /></span>}
            extra="超过的天数会被 /api/stats/heat 自动清掉"
          >
            <InputNumber
              min={1}
              max={3650}
              value={statsRetentionDraft}
              onChange={(v) => setStatsRetentionDraft(v ?? 365)}
              style={{ width: 180 }}
            />
          </Form.Item>

          <Form.Item>
            <Button
              type="primary"
              loading={savingBulk}
              onClick={() => void handleSaveBulk()}
              disabled={!config}
            >
              保存全部设置
            </Button>
            <span style={{ marginInlineStart: 12, color: 'var(--color-text-3)', fontSize: 12 }}>
              只提交实际发生变更的字段
            </span>
          </Form.Item>
        </Form>
        <SourceTagStyles />
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
