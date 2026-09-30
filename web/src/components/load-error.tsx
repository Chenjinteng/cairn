import { Button, Result, Typography } from 'antd';

import type { ApiFailureInfo } from '../types';

interface Props {
  /** 哪一块没加载出来，例如「服务配置加载失败，凭据列表无法确认」。 */
  title: string;
  /** request() 给出的失败信息（code / message / status / detail）。 */
  failure: ApiFailureInfo;
  /** 用户点「重试」时执行。 */
  onRetry: () => void;
  /** 重试请求在途时的按钮 loading。 */
  retrying?: boolean;
}

/**
 * 首屏加载失败的统一呈现：说清**哪一块**没加载出来，并给一个可点的出口。
 *
 * v0.5.18（F2）之前，页面拿到失败信封只会静默 setState（或什么都不做）：
 * 凭据 / 代理页留下一张空表，热度页永远停在「正在读取服务配置…」。用户
 * 分不清「本来就没有」和「没读到」，也没有任何重试入口，只能刷整页。
 *
 * 刻意**不做自动重试**：这些页面的重试路径会打到非幂等接口（DELETE、
 * /api/refresh 在 v0.6.10 之前也曾在此列；它其实是个**幂等**读端点，被错归类了），
 * 静默重放比让用户多点一次危险得多。要么用户点，要么不重试。
 *
 * 与 error-boundary.tsx 的分工：那个接的是 render 抛异常（前端自身崩了），
 * 这个接的是后端明确回了失败（界面好好的，数据没到）。
 */
export default function LoadError({ title, failure, onRetry, retrying }: Props) {
  // status 为 0 / undefined 表示没拿到响应（连不上或超时），此时报「HTTP 0」
  // 只会误导，所以只在真有状态码时才拼进去。
  const codeLine = `${failure.status ? `HTTP ${failure.status} · ` : ''}${failure.code}`;
  return (
    <Result
      status="error"
      title={title}
      subTitle={failure.message}
      extra={
        <Button type="primary" loading={retrying} onClick={onRetry}>
          重试
        </Button>
      }
    >
      <Typography.Text type="secondary">{codeLine}</Typography.Text>
      {failure.detail ? (
        <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>
          <span style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>{failure.detail}</span>
        </Typography.Paragraph>
      ) : null}
    </Result>
  );
}
