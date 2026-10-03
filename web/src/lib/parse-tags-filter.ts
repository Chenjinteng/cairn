/**
 * v0.7.21+: 镜像 Go 端 internal/sync/filter.go:ParseTagsFilter 的纯 TS 实现。
 *
 * 这个函数是 **UI 实时解析预览** 的核心:用户编辑 Tags 过滤 textarea
 * 时,我们用这个函数把输入切成"有效 spec / 跳过行 + 原因",立刻反馈给用户
 * (sync-page.tsx 下方那个绿色 + 红色的预览条)。不依赖后端往返,所以输入
 * 卡顿 = 0。
 *
 * 与 Go 端语义保持一致(逐行):
 *   - "" / 纯空白 → 空(valid=[],skipped=[])→ "没填,走 catalog 路径"
 *   - 行首 '#'(含整行都是注释)→ 跳过(注释)
 *   - 行内只有空白 → 跳过(空行)
 *   - 行不含 ':' → 跳过(没 tag)
 *   - 行首 / 行尾冒号('foo:' 或 ':bar')→ 跳过(空 tag / 空 repo)
 *
 * 设计要点(改这里时同时改 Go 测试 + TS 测试):
 *   1. **字符串切第一个冒号**:tag 里合法字符不包含 ':'(OCI / docker 约定),
 *      所以 strings.Cut('foo:bar:baz', ':') = ('foo', 'bar:baz') 是 OK 的。
 *      不需要 splitN 之类。
 *   2. **trim 行 + trim repo + trim tag**:用户在 textarea 里随手
 *      粘的 '  foo : bar  ' 也要识别;空 repo / 空 tag 当成坏行。
 *   3. **不静默改写**:解析结果原样回,UI 层负责标红提示。不要偷偷把
 *      'foo' 改成 'foo:latest' — 那是 v0.7.23+ 才考虑的事。
 */
export type ParseTagsFilterSkippedReason = 'comment' | 'blank' | 'no_tag' | 'empty_repo' | 'empty_tag';

export interface ParseTagsFilterSkipped {
  /** 1-based 行号,对应 textarea 第几行(便于 UI 高亮原始那行) */
  lineNumber: number;
  /** 原始行内容(已 trim,显示给用户看的版本) */
  raw: string;
  /** 跳过的具体原因 */
  reason: ParseTagsFilterSkippedReason;
}

export interface ParseTagsFilterResult {
  /** 解析成功的 (repo, tag) 配对 */
  valid: Array<{ repository: string; tag: string }>;
  /** 被跳过的行 + 原因 */
  skipped: ParseTagsFilterSkipped[];
  /**
   * 原始输入是不是"完全空"——和空字符串或只有空行的 textarea 区分开。
   * UI 可以据此展示不同的提示("留空 = 走 Include 模式")而不是"5 行全是空行"。
   */
  allBlank: boolean;
}

export function parseTagsFilter(input: string): ParseTagsFilterResult {
  const result: ParseTagsFilterResult = {
    valid: [],
    skipped: [],
    allBlank: true,
  };
  // 完全空 / 纯空白 → 早返,跳过循环(避免给空字符串吐一堆"空行 skipped")。
  if (input.trim() === '') {
    return result;
  }
  for (const [idx, rawLine] of input.split('\n').entries()) {
    const line = rawLine.trim();
    if (line === '') {
      result.skipped.push({ lineNumber: idx + 1, raw: rawLine, reason: 'blank' });
      continue;
    }
    if (line.startsWith('#')) {
      result.skipped.push({ lineNumber: idx + 1, raw: rawLine, reason: 'comment' });
      continue;
    }
    const cutIdx = line.indexOf(':');
    if (cutIdx < 0) {
      result.skipped.push({ lineNumber: idx + 1, raw: rawLine, reason: 'no_tag' });
      continue;
    }
    const repo = line.slice(0, cutIdx).trim();
    const tag = line.slice(cutIdx + 1).trim();
    if (repo === '') {
      result.skipped.push({ lineNumber: idx + 1, raw: rawLine, reason: 'empty_repo' });
      continue;
    }
    if (tag === '') {
      result.skipped.push({ lineNumber: idx + 1, raw: rawLine, reason: 'empty_tag' });
      continue;
    }
    result.valid.push({ repository: repo, tag });
    result.allBlank = false;
  }
  // 注意:allBlank 的语义是"有没有至少一条 valid spec";不是"输入是否含空白行"。
  // 上面循环只在 valid 时把 allBlank 设成 false,正确实现了这个语义。
  return result;
}

/**
 * v0.7.22+: 镜像 Go 端 ParseLongTimeoutRepos。逗号分隔的 repo 名列表,
 * 精确匹配(不打 glob)。空 / 纯空白 = nil。
 *
 * UI 用得少(只在 sync-page 的 longTimeoutRepos input onChange 里调用一次),
 * 但留在这里跟 parseTagsFilter 同处,后续 test 一起覆盖。
 */
export function parseLongTimeoutRepos(input: string): string[] {
  if (input.trim() === '') {
    return [];
  }
  const out: string[] = [];
  for (const part of input.split(',')) {
    const s = part.trim();
    if (s === '') continue;
    out.push(s);
  }
  return out;
}
