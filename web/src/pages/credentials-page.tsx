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
  Space,
  Table,
  Tag,
  Tooltip,
} from 'antd';
import {
  CheckCircleOutlined,
  CloseCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  KeyOutlined,
  PlusOutlined,
} from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import {
  createCredential,
  deleteCredential,
  listCredentials,
  testCredential,
  updateCredential,
} from '../api';
import LoadError from '../components/load-error';
import PageLoading from '../components/page-loading';
import { useAppConfig } from '../config-store';
import type {
  ApiResult,
  AppConfig,
  Credential,
  CredentialInput,
  CredentialPatch,
} from '../types';
import { formatDateTime } from '../utils';
import type { SidebarGroup, SidebarItem, SidebarSelection } from '../components/page-sidebar';

interface Props {
  config: AppConfig | null;
  /**
   * v0.5.37.4：侧栏选择状态。两组，跨组 AND：
   *   - host：按 registryUrl 的 host[:port] 分桶（去 scheme）
   *   - password：按密码是否已保存（hasPassword）
   */
  sidebarFilter: SidebarSelection;
  /** v0.5.37.4：把真实分组（含真实计数 badge）上浮给 App，由 App 统一下发到侧栏。 */
  onPublishGroups: (groups: SidebarGroup[]) => void;
}

interface FormValues {
  name: string;
  registryUrl: string;
  username: string;
  password?: string;
  note?: string;
}

/**
 * v0.5.37.4：从凭据的 registryUrl 取 host（含端口、去掉 scheme），作为侧栏「主机」分组的
 * 分桶键。表单已强校验 http(s):// 开头，这里再兜一层老数据：解析失败就退化成
 * 「去掉 scheme 后的第一段」。只用于展示与过滤，不改凭据本身。
 */
function registryHostOf(registryUrl: string | undefined): string {
  const raw = String(registryUrl ?? '').trim();
  if (!raw) return '未知';
  try {
    return new URL(raw).host || '未知';
  } catch {
    const host = raw.replace(/^[a-z][a-z0-9+.-]*:\/\//i, '').split('/')[0];
    return host || '未知';
  }
}

export default function CredentialsPage({ config, sidebarFilter, onPublishGroups }: Props) {
  const { message, modal } = AntdApp.useApp();
  const [credentials, setCredentials] = useState<Credential[]>([]);
  const [editing, setEditing] = useState<Credential | null>(null);
  const [form] = Form.useForm<FormValues>();
  const [modalOpen, setModalOpen] = useState(false);
  const [testingId, setTestingId] = useState<string | null>(null);
  const [error, setError] = useState<ApiResult<unknown> | null>(null);
  // v0.5.41: 首屏拉列表的 loading —— 之前切到本页直接显示「空表」,配 TableSkeleton。
  const [loading, setLoading] = useState(true);
  /**
   * v0.5.18（F6）：配置从模块级 store 取，不再自己拉。
   *
   * 「本部署不允许管凭据」和「压根没读到配置」在这一页的呈现是相反的：
   * 前者是正常态（该提示去哪开开关），后者是故障态（必须给重试入口）。
   * 过去两种情况都只渲染空表 + emptyText，用户看不出区别。
   */
  const { failure: configFailure, loading: configLoading, reload: reloadConfig } = useAppConfig();

  const refresh = useCallback(async () => {
    if (!config?.allowCredentials) {
      setLoading(false); // v0.5.41: 禁用管凭据也收口,免得切回来一直转
      return;
    }
    const result = await listCredentials();
    if (result.success && result.data) {
      setCredentials(result.data);
      setError(null);
    } else {
      setError(result);
    }
    setLoading(false);
  }, [config?.allowCredentials]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  /**
   * v0.5.37.4：侧栏分组 —— 两组都只放**视图状态**（过滤），有副作用的操作留在页头。
   * badge 全部来自真实凭据列表，侧栏数字与表格行永远同源：
   *   - 主机：registryUrl 的 host[:port] 分桶（去 scheme；解析失败归「未知」）
   *   - 密码状态：已保存 / 未保存（按 hasPassword 派生）
   * 凭据列表为空时下发空组，侧栏显示占位文案而不是空壳。
   * 注意：本部署禁止管凭据时 refresh 会提前返回、列表恒为空，这里自然下发空组。
   */
  const sidebarGroups = useMemo<SidebarGroup[]>(() => {
    if (credentials.length === 0) return [];

    const hostCounts = new Map<string, number>();
    credentials.forEach((c) => {
      const host = registryHostOf(c.registryUrl);
      hostCounts.set(host, (hostCounts.get(host) ?? 0) + 1);
    });
    const hostItems: SidebarItem[] = Array.from(hostCounts.entries())
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .map(([host, count]) => ({ key: host, label: host, badge: count }));

    const withPassword = credentials.filter((c) => c.hasPassword).length;

    return [
      {
        key: 'host',
        label: '主机',
        items: [{ key: 'all', label: '全部', badge: credentials.length }, ...hostItems],
      },
      {
        key: 'password',
        label: '密码状态',
        // 「全部」必须留在组内：否则选中后无法回到不过滤状态（点已选项是 no-op）。
        items: [
          { key: 'all', label: '全部', badge: credentials.length },
          { key: 'set', label: '已保存', badge: withPassword },
          { key: 'empty', label: '未保存', badge: credentials.length - withPassword },
        ],
      },
    ];
  }, [credentials]);

  useEffect(() => {
    onPublishGroups(sidebarGroups);
  }, [onPublishGroups, sidebarGroups]);

  /** 表格数据 = 全量凭据按侧栏两组选择做 AND 过滤。 */
  const visibleCredentials = useMemo(() => {
    const host = sidebarFilter.host ?? null;
    const password = sidebarFilter.password ?? null;
    return credentials.filter((c) => {
      if (host && host !== 'all' && registryHostOf(c.registryUrl) !== host) {
        return false;
      }
      if (password === 'set' && !c.hasPassword) return false;
      if (password === 'empty' && c.hasPassword) return false;
      return true;
    });
  }, [credentials, sidebarFilter]);

  const handleOpenCreate = () => {
    setEditing(null);
    form.resetFields();
    setModalOpen(true);
  };

  const handleOpenEdit = (c: Credential) => {
    setEditing(c);
    form.resetFields();
    form.setFieldsValue({
      name: c.name,
      registryUrl: c.registryUrl,
      username: c.username,
      password: '',
      note: c.note,
    });
    setModalOpen(true);
  };

  const handleSubmit = async () => {
    let values: FormValues;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    try {
      if (editing) {
        // 密码字段留空表示不更新；非空才覆盖。
        const patch: CredentialPatch = {
          name: values.name,
          registryUrl: values.registryUrl,
          username: values.username,
          note: values.note,
        };
        if (values.password && values.password.length > 0) {
          patch.password = values.password;
        }
        const result = await updateCredential(editing.id, patch);
        if (!result.success) {
          message.error(result.message || '更新失败');
          return;
        }
        message.success('已更新凭据');
      } else {
        if (!values.password) {
          message.error('新建凭据必须填写密码');
          return;
        }
        const input: CredentialInput = {
          name: values.name,
          registryUrl: values.registryUrl,
          username: values.username,
          password: values.password,
          note: values.note,
        };
        const result = await createCredential(input);
        if (!result.success) {
          message.error(result.message || '创建失败');
          return;
        }
        message.success('已创建凭据');
      }
      setModalOpen(false);
      await refresh();
    } catch (error) {
      message.error(String((error as Error)?.message ?? error));
    }
  };

  const handleTest = async (c: Credential) => {
    setTestingId(c.id);
    try {
      const result = await testCredential(c.id);
      if (!result.success) {
        message.error(`连接失败：${result.message}`);
      } else if (result.data) {
        message.success(`连接成功 · API ${result.data.apiVersion}`);
      }
    } finally {
      setTestingId(null);
    }
  };

  const handleDelete = (c: Credential) => {
    modal.confirm({
      title: `删除凭据「${c.name}」？`,
      content: '不会影响已落库的镜像，但使用此凭据的任务 / probe 将立即失败。',
      okText: '删除',
      okButtonProps: { danger: true },
      cancelText: '取消',
      onOk: async () => {
        const result = await deleteCredential(c.id);
        if (!result.success) {
          message.error(result.message || '删除失败');
          return;
        }
        message.success('已删除凭据');
        await refresh();
      },
    });
  };

  const columns: ColumnsType<Credential> = [
    {
      title: '名称',
      key: 'name',
      width: 200,
      render: (_, c) => (
        <Space direction="vertical" size={0} style={{ lineHeight: 1.3 }}>
          <strong>{c.name}</strong>
          {c.note ? (
            <span style={{ fontSize: 12, color: 'var(--color-text-3)' }}>{c.note}</span>
          ) : null}
        </Space>
      ),
    },
    {
      title: 'Registry URL',
      key: 'registryUrl',
      render: (_, c) => <span className="mono">{c.registryUrl}</span>,
    },
    {
      title: '账号',
      key: 'username',
      width: 160,
      render: (_, c) => <span className="mono">{c.username}</span>,
    },
    {
      title: '密码',
      key: 'hasPassword',
      width: 100,
      render: (_, c) =>
        c.hasPassword ? (
          <Tag color="success" icon={<CheckCircleOutlined />}>
            已设置
          </Tag>
        ) : (
          <Tag color="warning" icon={<CloseCircleOutlined />}>
            空
          </Tag>
        ),
    },
    {
      title: '更新时间',
      key: 'updatedAt',
      width: 150,
      render: (_, c) => <Tooltip title={c.updatedAt}>{formatDateTime(c.updatedAt)}</Tooltip>,
    },
    {
      title: '操作',
      key: 'actions',
      width: 220,
      fixed: 'right',
      render: (_, c) => (
        <Space size={4}>
          <Button
            type="link"
            size="small"
            loading={testingId === c.id}
            onClick={() => void handleTest(c)}
          >
            测试
          </Button>
          <Button type="link" size="small" icon={<EditOutlined />} onClick={() => handleOpenEdit(c)}>
            编辑
          </Button>
          <Popconfirm
            title="确定删除？"
            okText="删除"
            cancelText="取消"
            okButtonProps={{ danger: true }}
            onConfirm={() => handleDelete(c)}
          >
            <Button type="link" size="small" danger icon={<DeleteOutlined />}>
              删除
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  if (config && !config.allowCredentials) {
    // 区分两种失败，否则会把「密钥明明配了」的人引去反复检查 env：
    //   CREDENTIAL_KEY_MISSING          → 确实没配密钥
    //   CREDENTIAL_STORE_INIT_FAILED    → 密钥读到了，但目录不可写等
    const err = config.credentialError;
    const keyMissing = !err || err.code === 'CREDENTIAL_KEY_MISSING';
    return (
      <div className="page">
        <div className="page-header">
          <div>
            <h2 className="page-title">凭据管理</h2>
            <p className="page-subtitle">管理外部 registry 的 basic auth 凭据。</p>
          </div>
        </div>
        <Alert
          type="warning"
          showIcon
          message={
            keyMissing
              ? '服务端未配置 REGISTRY_CREDENTIAL_KEY，凭据库不可用。'
              : `凭据库初始化失败（${err.code}）`
          }
          description={
            <div>
              {keyMissing ? (
                <div>
                  请设置环境变量 <span className="mono">REGISTRY_CREDENTIAL_KEY</span>（任意随机 32+ 字符）
                  后重启服务。镜像拉取仍可工作（匿名源 / 临时输入）。
                </div>
              ) : (
                <>
                  <div style={{ whiteSpace: 'pre-wrap' }}>{err.message}</div>
                  <div style={{ marginTop: 8 }}>
                    当前凭据目录：<span className="mono">{config.credentialsDir}</span>
                  </div>
                  <div style={{ marginTop: 8, color: 'var(--color-text-3)' }}>
                    密钥本身没问题（服务端已读到）。镜像拉取仍可工作（匿名源 / 临时输入），
                    修好目录后重启即可恢复凭据库。
                  </div>
                </>
              )}
            </div>
          }
        />
      </div>
    );
  }

  return (
    <div className="page">
      <div className="page-header">
        <div>
          <h2 className="page-title">凭据管理</h2>
          {/* 只写"这页能做什么"。为什么不在这里管本仓库自身的认证、文件怎么加密、
              丢了会怎样 —— 那属于设计说明，见 docs/design.md §3.1 / §3.2。 */}
          <p className="page-subtitle">管理外部 registry 的 basic auth 凭据。</p>
        </div>
        <div className="page-actions">
          <Button type="primary" icon={<PlusOutlined />} onClick={handleOpenCreate}>
            新增凭据
          </Button>
        </div>
      </div>

      {!config ? (
        /* 配置读不到：「是否允许管凭据」都无从判断，空表会把故障伪装成"还没添加"。 */
        configFailure ? (
          <LoadError
            title="服务配置加载失败，凭据列表无法确认"
            failure={configFailure}
            retrying={configLoading}
            onRetry={() => void reloadConfig()}
          />
        ) : (
          /* 还没拿到结果（首屏在途）：比空表诚实，也不与「本部署禁止管凭据」混淆。 */
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

          <div className="panel" style={{ position: 'relative', minHeight: 200 }}>
            {/*
             * v0.6.23: PageLoading 改成 position: absolute 覆盖在 panel 内,
             * 不再占 flow 高度(解决 0.6.22 「拉址感」)。Table hidden=true
             * 时 panel 内空,spinner 覆盖整个 panel 200px 区域;Table 露脸后
             * spinner 在 Table **上面**淡出。
             *
             * `loading && credentials.length === 0` 才需要居中 spinner
             * (后续轮询走 antd Table 自带半透层)。
             */}
            <div hidden={loading && credentials.length === 0}>
            <Table<Credential>
              rowKey="id"
              size="middle"
              columns={columns}
              dataSource={visibleCredentials}
              pagination={false}
              locale={{
                emptyText:
                  credentials.length > 0
                    ? '当前筛选下没有凭据，换个筛选条件试试'
                    : '还没有凭据，点击右上「新增凭据」',
              }}
            />
            </div>
            <PageLoading
              visible={loading && credentials.length === 0}
              tip="正在读取凭据…"
            />
          </div>
        </>
      )}

      <Modal
        open={modalOpen}
        title={editing ? `编辑凭据：${editing.name}` : '新增凭据'}
        okText={editing ? '保存' : '创建'}
        cancelText="取消"
        onCancel={() => setModalOpen(false)}
        onOk={handleSubmit}
        destroyOnClose
      >
        <Form<FormValues> form={form} layout="vertical" preserve={false}>
          <Form.Item
            label="名称"
            name="name"
            rules={[{ required: true, message: '请填写名称' }]}
            extra="仅本工具内部用作识别，不会被传输。"
          >
            <Input placeholder="公司内网 registry" />
          </Form.Item>
          <Form.Item
            label="Registry URL"
            name="registryUrl"
            rules={[
              { required: true, message: '请填写 registry 地址' },
              {
                // v0.5.49: 空值由上面的 required 独占负责,这条 validator 只在
                // 有值时跑「必须带协议头」检查。
                validator: (_, value: string) => {
                  if (!value || !value.trim()) return Promise.resolve();
                  return /^https?:\/\//i.test(value.trim())
                    ? Promise.resolve()
                    : Promise.reject(new Error('需要以 http:// 或 https:// 开头'));
                },
              },
            ]}
            extra="必须严格匹配任务要打的源/目的 registry 地址；不一致时任务会被拒绝使用。"
          >
            <Input placeholder="https://registry-1.docker.io" />
          </Form.Item>
          <Form.Item
            label="用户名"
            name="username"
            rules={[{ required: true, message: '请填写用户名' }]}
          >
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item
            label={editing ? '密码（留空保留原密码）' : '密码'}
            name="password"
            rules={editing ? [] : [{ required: true, message: '请填写密码' }]}
            extra="密码不会回显；落盘前加密存储。"
          >
            <Input.Password autoComplete="new-password" placeholder="••••••" />
          </Form.Item>
          <Form.Item label="备注（可选）" name="note">
            <Input placeholder="例如：仅个人 token / CI 用" />
          </Form.Item>
        </Form>
      </Modal>

      <Alert
        type="info"
        showIcon
        icon={<KeyOutlined />}
        message="密钥丢失将无法解密凭据文件"
        description={
          <div>
            <div>
              凭据文件位于 <span className="mono">{config?.credentialsDir ?? '/app/data'}/credentials.json</span>。
            </div>
            <div>
              备份时务必<strong>同时备份</strong>文件 + 密钥 <span className="mono">REGISTRY_CREDENTIAL_KEY</span>；缺一不可。
            </div>
          </div>
        }
        style={{ marginTop: 16 }}
      />
    </div>
  );
}