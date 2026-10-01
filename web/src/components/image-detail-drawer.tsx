import { useMemo, useState } from 'react';
import { Alert, App, Button, Descriptions, Drawer, Dropdown, Empty, Popconfirm, Space, Table, Tooltip } from 'antd';
import { CopyOutlined, DeleteOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import { deleteTag } from '../api';
import type { ApiResult, DeleteTagPayload, RegistryRepository, RegistryTag } from '../types';
import {
  buildPullCommand,
  copyText,
  DEFAULT_PULL_RUNTIME,
  formatBytes,
  formatDateTime,
  PULL_RUNTIMES,
  shortDigest,
  type PullRuntime,
} from '../utils';

interface Props {
  open: boolean;
  host: string;
  repository: RegistryRepository | null;
  /** false 时隐藏删除入口（服务端同样会拒绝）。 */
  allowDelete: boolean;
  onClose: () => void;
  /** 删除成功后把最新仓库快照交回页面，避免整表重扫。 */
  onDeleted: (payload: DeleteTagPayload) => void;
}

export default function ImageDetailDrawer({
  open,
  host,
  repository,
  allowDelete,
  onClose,
  onDeleted,
}: Props) {
  const { message, modal } = App.useApp();
  const [deletingTag, setDeletingTag] = useState<string | null>(null);
  // v0.6.17: 当前行操作要用的 runtime(复制命令)。Drawer 重开时回
  // 落到默认 docker —— 用户多半在同一台机用同一个 CLI,逐行记忆无
  // 意义,反会在切环境后误导。
  const [selectedRuntime, setSelectedRuntime] = useState<PullRuntime>(DEFAULT_PULL_RUNTIME);
  const [notice, setNotice] = useState<ApiResult<DeleteTagPayload> | null>(null);

  // 同一 digest 可能被多个 tag 指向，删除会一次影响它们，确认时必须讲清影响面。
  const digestTagMap = useMemo(() => {
    const map = new Map<string, string[]>();
    for (const item of repository?.tags ?? []) {
      const siblings = map.get(item.digest) ?? [];
      siblings.push(item.tag);
      map.set(item.digest, siblings);
    }
    return map;
  }, [repository]);

  const copy = async (text: string) => {
    if (await copyText(text)) {
      message.success('已复制');
      return;
    }
    // 两条路径都失败时不假装成功：把命令摊开，让用户能手动选中复制。
    modal.error({
      title: '复制失败',
      width: 620,
      content: (
        <div>
          <p style={{ marginBottom: 8 }}>浏览器拒绝了剪贴板操作。请手动选择下面的命令复制：</p>
          <pre
            className="mono"
            style={{
              margin: 0,
              padding: '10px 12px',
              background: 'var(--color-fill-1)',
              border: '1px solid var(--color-border-2)',
              borderRadius: 'var(--radius-sm)',
              fontSize: 12,
              lineHeight: 1.7,
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-all',
              userSelect: 'text',
            }}
          >
            {text}
          </pre>
        </div>
      ),
    });
  };

  const handleDelete = async (record: RegistryTag) => {
    if (!repository) {
      return;
    }
    setDeletingTag(record.tag);
    setNotice(null);
    try {
      const result = await deleteTag(repository.name, record.tag);
      if (result.success && result.data) {
        // 成功走顶部 toast（与 images-page 删除仓库同模式,3 秒自动消失），
        // 别再把「操作回执」塞进 drawer Alert —— 后端 writeJSON 把 message
        // 写成空串,AntD 会渲成「只剩对勾的空白框」(v0.5.27 之前的 bug)。
        message.success(`已删除 tag ${record.tag}`);
        onDeleted(result.data);
      } else {
        // 失败才需要占据 drawer 顶部的 Alert：把后端的 code / message 摊给用户看。
        setNotice(result);
      }
    } finally {
      setDeletingTag(null);
    }
  };

  const columns: ColumnsType<RegistryTag> = [
    {
      title: 'Tag',
      dataIndex: 'tag',
      key: 'tag',
      width: 200,
      render: (value: string) => (
        <Tooltip title={value}>
          <span className="ellipsis" style={{ display: 'block', fontWeight: 500 }}>
            {value || '--'}
          </span>
        </Tooltip>
      ),
    },
    {
      title: 'Digest',
      dataIndex: 'digest',
      key: 'digest',
      width: 190,
      render: (value: string) => (
        <Tooltip title={value}>
          <span className="mono" style={{ color: 'var(--color-text-3)' }}>
            {shortDigest(value)}
          </span>
        </Tooltip>
      ),
    },
    {
      title: '架构',
      dataIndex: 'architecture',
      key: 'architecture',
      width: 150,
      render: (value: string, record) => {
        if (!value) {
          return '--';
        }
        const platform = `${record.os || 'linux'}/${value}`;
        return record.platformCount > 1 ? `${platform} +${record.platformCount - 1}` : platform;
      },
    },
    { title: '层数', dataIndex: 'layerCount', key: 'layerCount', width: 80 },
    {
      title: '大小',
      dataIndex: 'size',
      key: 'size',
      width: 110,
      render: (value: number) => formatBytes(value),
    },
    {
      title: '构建时间',
      dataIndex: 'createdAt',
      key: 'createdAt',
      width: 170,
      render: (value: string | null) => formatDateTime(value),
    },
    {
      title: '操作',
      key: 'actions',
      width: 130,
      fixed: 'right',
      render: (_, record) => {
        const siblings = digestTagMap.get(record.digest) ?? [];
        const repo = repository?.name ?? '';
        return (
          /*
           * v0.6.18: 「操作」列里的两个按钮间距从 2 → 8。
           *
           * 0.6.17 把「复制」变成 Dropdown 后,默认单击会复制当前 runtime。
           * 问题:Dropdown trigger={['click']} + Button 自带 onClick 让
           *   「单击既复制又展开菜单」—— 用户反馈"想用下拉列表代替单击复制,
           *   两个图标间距太近容易误点"。
           *
           * 这版的修法:
           *   1. 复制按钮的 onClick 全部移除,只通过 Dropdown menu 的 onClick
           *      复制 —— 单击 = 仅展开菜单,选 runtime 才复制
           *   2. 复制 / 删除两个按钮间距从 <Space size={2}> 提到 8,避免
           *      误点删除（删除是高危操作,必须明显隔离）
           *   3. Dropdown trigger 改成 'hover' 让用户更明确感知「这是个菜单」
           *      —— 'click' 在 antd 默认下表现会和按钮 onclick 撞
           *
           * 副作用: 一次操作分两步(展开 → 选 runtime)。但 99% 用户只点 docker,
           * "两步" 实际是"鼠标移上去 → 选 docker" → 0.5s 操作成本;
           * 真要"老路 一键复制 docker"的人 0.6.17 之前已经习惯了,
           * 改回两步的好处是"按钮语义明确"(点复制 = 弹菜单,不是「复制 + 弹」)。
           */
          <Space size={8}>
            <Dropdown
              menu={{
                items: PULL_RUNTIMES.map((r) => ({
                  key: r.value,
                  label: r.label,
                })),
                onClick: ({ key }) => {
                  const rt = key as PullRuntime;
                  setSelectedRuntime(rt);
                  const cmd = buildPullCommand(host, repo, record.tag, rt);
                  void copy(cmd);
                  message.success(`已复制 ${rt} 命令: ${cmd}`);
                },
                selectedKeys: [selectedRuntime],
              }}
              trigger={['hover']}
            >
              <Tooltip
                title={
                  buildPullCommand(host, repo, record.tag, selectedRuntime)
                }
              >
                <Button
                  type="text"
                  size="small"
                  icon={<CopyOutlined />}
                  aria-label="复制 pull 命令"
                />
              </Tooltip>
            </Dropdown>
            {allowDelete ? (
              <Popconfirm
                title={`确认删除 ${record.tag}？`}
                description={
                  <div style={{ maxWidth: 320 }}>
                    <div>将按 digest 删除该 manifest，删除后该镜像无法再被拉取。</div>
                    {siblings.length > 1 ? (
                      <div style={{ marginTop: 4, color: 'var(--color-warning)' }}>
                        该 digest 同时被 {siblings.length} 个 tag 指向（{siblings.join('、')}），删除会一并影响它们。
                      </div>
                    ) : null}
                    <div style={{ marginTop: 4, color: 'var(--color-text-3)' }}>
                      删除不会立即释放磁盘空间，需要在 registry 侧运行 registry garbage-collect。
                    </div>
                  </div>
                }
                okText="确认删除"
                cancelText="取消"
                okButtonProps={{ danger: true, loading: deletingTag === record.tag }}
                onConfirm={() => handleDelete(record)}
              >
                <Button type="text" size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            ) : null}
          </Space>
        );
      },
    },
  ];

  return (
    <Drawer
      open={open}
      onClose={onClose}
      width={1020}
      title={repository?.name ?? '镜像详情'}
      destroyOnClose
    >
      <div className="drawer-stack">
        {/*
          v0.5.27 之前:成功也塞 notice，message 是后端 writeJSON 注入的空串，
          AntD 渲成「只剩对勾的空白框」。现在成功走顶部 toast,只有失败才画 Alert;
          顺手清掉 affectedTags 描述 —— 按 digest 删除影响多 tag 是技术副作用,
          不是用户需要看的「事实」。
        */}
        {notice && !notice.success ? (
          <Alert
            type="warning"
            showIcon
            closable
            onClose={() => setNotice(null)}
            message={notice.message || '删除失败'}
            description={notice.code ? <span>错误分类：{notice.code}</span> : undefined}
          />
        ) : null}

        <Descriptions size="small" column={2} bordered>
          <Descriptions.Item label="仓库">{repository?.name ?? '--'}</Descriptions.Item>
          <Descriptions.Item label="Registry">{host || '--'}</Descriptions.Item>
          <Descriptions.Item label="Tag 数">{repository?.tagCount ?? 0}</Descriptions.Item>
          <Descriptions.Item label="镜像层合计">{formatBytes(repository?.totalSize)}</Descriptions.Item>
        </Descriptions>

        <Alert
          type="info"
          showIcon
          message="「镜像层合计」为各 manifest 中 layer 大小之和，不是磁盘占用：不同 tag 与仓库共享底层 blob，registry 本身也不提供存储用量接口。"
        />

        <div className="drawer-scroll">
          {repository?.tags.length ? (
            <Table<RegistryTag>
              rowKey="tag"
              size="small"
              columns={columns}
              dataSource={repository.tags}
              pagination={false}
              scroll={{ x: 1000 }}
            />
          ) : (
            <Empty description="该仓库当前没有可用的 tag" />
          )}
        </div>
      </div>
    </Drawer>
  );
}
