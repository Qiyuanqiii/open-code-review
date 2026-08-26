// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

import {
  CliResult, CliRunOptions, DelegatePreview, FileChange, LogLine, RepositoryKind,
  ReviewComment, ReviewMode,
} from '../../shared/types';

function appendReviewSelection(args: string[], opts: CliRunOptions): void {
  if (opts.mode === ReviewMode.Branch) {
    if (opts.from) args.push('--from', opts.from);
    if (opts.to) args.push('--to', opts.to);
    if (opts.svnFromTarget) args.push('--svn-from-target', opts.svnFromTarget);
    if (opts.svnToTarget) args.push('--svn-to-target', opts.svnToTarget);
  } else if (opts.mode === ReviewMode.Commit) {
    if (opts.commit) args.push('--commit', opts.commit);
  }
}

export function buildReviewArgs(opts: CliRunOptions): string[] {
  const args: string[] = ['review'];
  appendReviewSelection(args, opts);
  args.push('--format', 'json');
  // JSON 结果走 stdout，进度日志走 stderr，供扩展实时回显
  // TODO: 待 CLI 发布支持 --progress-stderr 后再启用（当前已安装版本不识别该 flag）
  // args.push('--progress-stderr');
  if (opts.customPrompt && opts.customPrompt.trim()) {
    args.push('--background', opts.customPrompt.trim());
  }
  if (typeof opts.concurrency === 'number') {
    args.push('--concurrency', String(opts.concurrency));
  }
  return args;
}

export function buildDelegatePreviewArgs(opts: CliRunOptions): string[] {
  const args = ['delegate', 'preview'];
  appendReviewSelection(args, opts);
  args.push('--format', 'json');
  return args;
}

function parseRepositoryKind(value: unknown): RepositoryKind {
  return value === 'git' || value === 'svn' ? value : 'unknown';
}

interface RawPreviewFile {
  path?: unknown;
  status?: unknown;
}

function parsePreviewFile(raw: RawPreviewFile): FileChange | null {
  const status = raw?.status;
  if (typeof raw?.path !== 'string' || typeof status !== 'string'
    || !['added', 'modified', 'deleted', 'renamed', 'binary'].includes(status)) return null;
  return { path: raw.path, status: status as FileChange['status'] };
}

export function parseDelegatePreview(stdout: string): DelegatePreview {
  const start = stdout.indexOf('{');
  if (start < 0) throw new Error('no JSON in CLI output');
  const json = JSON.parse(stdout.slice(start));
  const files = Array.isArray(json.reviewable_files)
    ? json.reviewable_files.map(parsePreviewFile).filter((f: FileChange | null): f is FileChange => f !== null)
    : [];
  return {
    vcs: parseRepositoryKind(json.vcs),
    mode: typeof json.mode === 'string' ? json.mode : '',
    resolvedBase: json.resolved_base || undefined,
    resolvedHead: json.resolved_head || undefined,
    exactRange: json.exact_range || undefined,
    resolvedBasePeg: json.resolved_base_peg || undefined,
    resolvedHeadPeg: json.resolved_head_peg || undefined,
    reviewableFiles: files,
  };
}

function toComment(raw: any): ReviewComment {
  return {
    path: raw.path,
    content: raw.content,
    suggestionCode: raw.suggestion_code || undefined,
    existingCode: raw.existing_code || undefined,
    startLine: raw.start_line,
    endLine: raw.end_line,
    thinking: raw.thinking || undefined,
  };
}

export function parseCliResult(stdout: string): CliResult {
  const start = stdout.indexOf('{');
  if (start < 0) throw new Error('no JSON in CLI output');
  const json = JSON.parse(stdout.slice(start));
  const s = json.summary;
  return {
    status: json.status,
    message: json.message,
    comments: Array.isArray(json.comments) ? json.comments.map(toComment) : [],
    warnings: Array.isArray(json.warnings) ? json.warnings : [],
    summary: s ? {
      filesReviewed: s.files_reviewed,
      comments: s.comments,
      totalTokens: s.total_tokens,
      inputTokens: s.input_tokens,
      outputTokens: s.output_tokens,
      elapsed: s.elapsed,
    } : undefined,
  };
}

/** 从 CLI stderr 中提取最有用的报错文本：优先 `Error:` 行，否则取最后一行非空内容。 */
export function extractCliError(stderr: string): string {
  const lines = stderr.split('\n').map((l) => l.trim()).filter(Boolean);
  const errLine = [...lines].reverse().find((l) => /^error:/i.test(l));
  const message = errLine ? errLine.replace(/^error:\s*/i, '') : (lines.length ? lines[lines.length - 1] : '');
  return redactCliError(message);
}

/** Remove repository targets before CLI diagnostics are displayed or retained by the extension. */
export function redactCliError(message: string, sensitiveValues: Array<string | undefined> = []): string {
  let redacted = message;
  for (const value of sensitiveValues) {
    const target = value?.trim();
    if (target) redacted = redacted.split(target).join('[SVN target redacted]');
  }
  return redacted.replace(
    /\b(?:https?|svn(?:\+[a-z0-9.+-]+)?|file):\/\/[^\s'"<>]+/gi,
    '[SVN target redacted]',
  );
}

export function parseLogLine(raw: string): LogLine | null {
  const text = raw.replace(/\s+$/, '');
  if (!text.trim()) return null;
  const level: LogLine['level'] = /retrying|warning|warn/i.test(text) ? 'warn' : 'info';
  return { text, level };
}
