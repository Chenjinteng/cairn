import { Component, type ErrorInfo, type ReactNode } from 'react';
import { Button, Result } from 'antd';

interface Props {
  children: ReactNode;
}

interface State {
  error: Error | null;
}

/**
 * 全站兜底错误边界。
 *
 * 任何页面 render 抛异常时，React 的默认行为是整棵树卸载 —— 用户看到白屏，
 * 对一个运维工具来说白屏等于"服务挂了？"，实际上后端好好的。这里接住异常，
 * 展示错误信息 + 刷新按钮，完整堆栈打到 console 方便排查。
 */
export default class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('[cairn] UI 渲染错误:', error, info.componentStack);
  }

  render() {
    if (this.state.error) {
      return (
        <Result
          status="error"
          title="页面出错了"
          subTitle={this.state.error.message}
          extra={
            <Button type="primary" onClick={() => window.location.reload()}>
              刷新页面
            </Button>
          }
        />
      );
    }
    return this.props.children;
  }
}
