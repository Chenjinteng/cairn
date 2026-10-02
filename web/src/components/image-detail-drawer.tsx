import { useMemo, useState } from 'react';
import { Alert, App, Button, Descriptions, Drawer, Dropdown, Empty, Popconfirm, Space, Table, Tooltip } from 'antd';
import { CopyOutlined, DeleteOutlined, DownloadOutlined } from '@ant-design/icons';
import type { ColumnsType } from 'antd/es/table';

import { deleteTag } from '../api';
import type { ApiResult, DeleteTagPayload, RegistryRepository, RegistryTag } from '../types';
import {
  buildPullCommand,
  copyText,
  DEFAULT_PULL_RUNTIME,
  formatBytes,
  formatDate,
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
      // v0.6.19: 这里不再 toast —— 调用方已经做了「已复制 <runtime> 命令」
      // 的 toast,这里再弹一个「已复制」会出现两次 toast(用户反馈)。
      // 调用方负责**唯一**一条成功反馈,本函数只负责"复制 + 失败处理"。
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
      // v0.7.5: 列宽从 200 → 160,配合 ellipsis 让长 tag 自动截断,
      // 整个表格不再撑出抽屉右边(总和原来 1060 > drawer 1020)。
      width: 160,
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
      // shortDigest 已经裁到 sha256:abcdef1234567 形式(20 字符),
      // 140 + Tooltip 提示完整 digest 够用。
      width: 140,
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
      // v0.7.5: 180 → 130。multi-arch 列出全平台时(`linux/amd64, linux/arm64/v8, ...`)
      // 超过 130 字符级截断,Tooltip 提示完整列表。
      width: 130,
      // v0.7.3: render the full platform list so multi-arch tags show
      // "linux/amd64, linux/arm64" instead of "linux/amd64 +1". Older
      // inventory responses won't carry `platforms`; fall back to the
      // back-compat single-arch + "+N" form in that case.
      render: (_, record: RegistryTag) => {
        const list = record.platforms && record.platforms.length > 0
          ? record.platforms
          : record.architecture
            ? [`${record.os || 'linux'}/${record.architecture}`]
            : [];
        if (list.length === 0) {
          return '--';
        }
        const text = list.join(', ');
        return (
          <Tooltip
            placement="topLeft"
            title={text}
            // v0.7.5: 长平台列表横向 ellipsis,跟「Docker Hub 实际有 8 个平台」
            // 这种情况不撑出列宽。Tooltip 永远展示全名。
          >
            <span className="mono ellipsis" style={{ display: 'block' }}>{text}</span>
          </Tooltip>
        );
      },
    },
    // v0.7.5: 80 → 60。单数字居中够用。
    { title: '层数', dataIndex: 'layerCount', key: 'layerCount', width: 60, align: 'right' },
    {
      title: '大小',
      dataIndex: 'size',
      key: 'size',
      // v0.7.5: 110 → 80。formatBytes 出来的最长格式约 7 字符(`12.4 MiB`),
      // 80 够。
      width: 80,
      render: (value: number) => formatBytes(value),
    },
    {
      title: '构建时间',
      dataIndex: 'createdAt',
      key: 'createdAt',
      // v0.7.5: 改 formatDate → YYYY-MM-DD(10 字符),不带时分。
      // 表格里时分不重要,精确到天就够排序 / 过滤;要时分看 /api/.../tags/<tag>
      // 详情接口。列宽从 170 → 110。
      width: 110,
      render: (value: string | null) => formatDate(value),
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
                  // v0.6.19: 唯一一条成功反馈,带上 runtime 名(用户反馈:
                  // 之前会同时弹出「已复制」+「已复制 docker 命令」两条)。
                  message.success(`已复制 ${rt} 命令: ${cmd}`);
                },
                selectedKeys: [selectedRuntime],
              }}
              trigger={['hover']}
            >
              {/*
               * v0.6.19: Tooltip 去掉。
               *
               * 用户反馈:鼠标悬浮在复制按钮上,tooltip 弹出「docker pull <ref>」/
               * 「ctr -n k8s.io images pull <ref>」等完整命令字串 —— 这跟
               * 「复制按钮就是干这个的」语义重复,悬浮的用户本来就是要
               * 复制,看到这条字串只是「预演」一遍;真正复制时已经看到
               * 顶部 toast「已复制 <runtime> 命令: <cmd>」,信息量比 tooltip
               * 还多(包含 runtime 名)。
               *
               * 现在的语义:
               *   - 按钮自身:图标 + aria-label="复制 pull 命令"(屏幕阅读器
               *     友好,鼠标悬浮无字)
               *   - 复制反馈:顶部 toast 一条(已含 runtime + 命令)
               *
               * 用户想提前看到命令怎么办?Hover 进 Dropdown 菜单,菜单
               * 每一项的 label 是 runtime 名(没有命令字串 —— 命令拼装
               * 在点击时才发生,因为 host / repo / tag 三个变量才决定)。
               */}
              <Button
                type="text"
                size="small"
                icon={<CopyOutlined />}
                aria-label="复制 pull 命令"
              />
            </Dropdown>
            {/*
             * v0.7.0: 镜像 tar 下载。
             *
             * 点按钮 → 浏览器 GET /api/repositories/{repo}/tags/{tag}/export,
             * 服务端吐 `docker save`-compatible tar 流,Content-Disposition
             * 让浏览器自动以 <repo>-<tag>-<arch>.tar 存盘(v0.7.3 起文件名带架构)。
             *
             * v0.7.3: 多架构 tag 现在是 Dropdown,菜单列出 platforms 数组,
             * 用户选哪个就下载哪个。文件名前缀固定由后端贴架构(平台 query
             * 显式传了 → 用实际架构;没传 → 用 chip 默认 amd64),不会撞名。
             *
             * 注意事项:
             *   - 用 <a href download> 而不是 fetch + blob,前者让浏览器
             *     自动处理大文件流(避免一次内存合并);后者对几百 MB +
             *     几 GB 镜像不可行。
             *   - URL 拼装要 encodeURIComponent(repo) 因为 repo 名常含
             *     "/" (library/nginx),dispatcher 内部再解码。
             *   - 单架构 tag 走默认 platform(后端 chip 默认 amd64)。
             *   - 镜像 schema1 / 多平台镜像里没请求的 platform 时,后端
             *     会 400;axios 不触发(直接 <a> 走),错误状态码浏览器
             *     看不到,但 tar 头不对的响应会得到一个非 tar 文件。
             */}
            {(() => {
              const platforms = record.platforms && record.platforms.length > 0
                ? record.platforms
                : record.architecture
                  ? [`${record.os || 'linux'}/${record.architecture}`]
                  : [];
              const trigger = (platform?: string) => {
                let url = `/api/repositories/${encodeURIComponent(repo)}/tags/${encodeURIComponent(record.tag)}/export`;
                if (platform) {
                  url += `?platform=${encodeURIComponent(platform)}`;
                }
                const a = document.createElement('a');
                a.href = url;
                a.rel = 'noopener';
                document.body.appendChild(a);
                a.click();
                a.remove();
              };
              if (platforms.length <= 1) {
                return (
                  <Button
                    type="text"
                    size="small"
                    icon={<DownloadOutlined />}
                    aria-label="下载镜像 tar"
                    onClick={() => trigger()}
                  />
                );
              }
              return (
                <Dropdown
                  trigger={['hover']}
                  menu={{
                    items: platforms.map((p) => ({
                      key: p,
                      label: `下载 ${p}`,
                      onClick: () => trigger(p),
                    })),
                  }}
                >
                  <Button
                    type="text"
                    size="small"
                    icon={<DownloadOutlined />}
                    aria-label="下载镜像 tar(选架构)"
                  />
                </Dropdown>
              );
            })()}
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
