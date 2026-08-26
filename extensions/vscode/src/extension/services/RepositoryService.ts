// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

import { execFile } from 'child_process';
import { isAbsolute, relative, resolve, sep } from 'path';
import * as vscode from 'vscode';
import {
  CliRunOptions, FileChange, GitState, RepositoryKind, ReviewMode,
} from '../../shared/types';
import { resolveLocale, t } from '../../shared/i18n';
import { CliService } from './CliService';
import { GitService } from './GitService';

const emptyState = (vcs: RepositoryKind): GitState => ({
  vcs,
  branches: [],
  currentBranch: '',
  recentCommits: [],
  workspaceFiles: [],
});

function commandSucceeds(command: string, args: string[], cwd: string): Promise<boolean> {
  return new Promise((resolve) => {
    execFile(command, args, { cwd, timeout: 15_000 }, (err) => resolve(!err));
  });
}

/** Match the CLI's repository precedence: Git first, then a Subversion working copy. */
export async function detectRepositoryKind(cwd: string): Promise<RepositoryKind> {
  if (await commandSucceeds('git', ['rev-parse', '--git-dir'], cwd)) return 'git';
  if (await commandSucceeds('svn', ['info', '--xml', '--non-interactive', '--depth', 'empty', '--', '.'], cwd)) return 'svn';
  return 'unknown';
}

/** Routes repository operations without making SVN workspaces depend on VS Code's Git API. */
export class RepositoryService {
  private detections = new Map<string, Promise<RepositoryKind>>();

  constructor(
    private cli: CliService,
    private git: GitService,
    private log?: vscode.OutputChannel,
    private detect: typeof detectRepositoryKind = detectRepositoryKind,
  ) {}

  private trace(message: string): void {
    this.log?.appendLine(`[repository] ${message}`);
  }

  private repositoryKind(cwd: string): Promise<RepositoryKind> {
    const key = resolve(cwd);
    const cached = this.detections.get(key);
    if (cached) return cached;
    const pending = this.detect(cwd).catch((error) => {
      this.detections.delete(key);
      throw error;
    });
    this.detections.set(key, pending);
    return pending;
  }

  async getState(mode: ReviewMode, cwd: string): Promise<GitState> {
    const vcs = await this.repositoryKind(cwd);
    if (vcs === 'git') return this.git.getState(mode);
    if (vcs !== 'svn') return emptyState(vcs);

    const state = emptyState('svn');
    if (mode !== ReviewMode.Workspace) return state;
    try {
      const preview = await this.cli.delegatePreview({ mode, vcs }, cwd);
      state.workspaceFiles = preview.reviewableFiles;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      this.trace(`SVN workspace preview failed: ${message}`);
      void vscode.window.showWarningMessage(`${t(resolveLocale(vscode.env.language), 'ext.svn.previewFailed')} ${message}`);
    }
    return state;
  }

  async getModeFiles(opts: CliRunOptions, cwd: string): Promise<FileChange[]> {
    const vcs = opts.vcs && opts.vcs !== 'unknown'
      ? opts.vcs
      : await this.repositoryKind(cwd);
    if (vcs === 'svn' || (opts.svnFromTarget && opts.svnToTarget)) {
      const preview = await this.cli.delegatePreview({ ...opts, vcs: 'svn' }, cwd);
      return preview.reviewableFiles;
    }
    if (vcs !== 'git') return [];
    if (opts.mode === ReviewMode.Branch && opts.from && opts.to) {
      return this.git.getBranchDiff(opts.from, opts.to);
    }
    if (opts.mode === ReviewMode.Commit && opts.commit) {
      return this.git.getCommitFiles(opts.commit);
    }
    return [];
  }

  async openFile(opts: CliRunOptions & Pick<FileChange, 'path' | 'status'>, cwd: string): Promise<void> {
    const vcs = opts.vcs && opts.vcs !== 'unknown'
      ? opts.vcs
      : await this.repositoryKind(cwd);
    if (vcs === 'git') {
      await this.git.openDiff(opts);
      return;
    }
    if (vcs === 'svn' && opts.mode === ReviewMode.Workspace && opts.status !== 'deleted') {
      const root = resolve(cwd);
      const target = resolve(root, opts.path);
      const child = relative(root, target);
      if (!child || child === '..' || child.startsWith(`..${sep}`) || isAbsolute(child)) {
        this.trace(`refused SVN workspace path outside repository: ${opts.path}`);
        void vscode.window.showWarningMessage(
          t(resolveLocale(vscode.env.language), 'ext.repository.pathOutsideWorkspace'),
        );
        return;
      }
      try {
        await vscode.window.showTextDocument(vscode.Uri.file(target), { preview: true });
      } catch (error) {
        this.trace(`open SVN workspace file failed: ${error instanceof Error ? error.message : String(error)}`);
      }
      return;
    }
    if (vcs === 'svn') {
      void vscode.window.showInformationMessage(
        t(resolveLocale(vscode.env.language), 'ext.svn.snapshotSidebarOnly'),
      );
    }
  }

  watchWorkspaceChanges(cwd: string, onUpdate: (state: GitState) => void): vscode.Disposable {
    let disposed = false;
    let watcher: vscode.Disposable | undefined;
    void this.repositoryKind(cwd).then((vcs) => {
      if (!disposed && vcs === 'git') watcher = this.git.watchWorkspaceChanges(onUpdate);
    });
    return new vscode.Disposable(() => {
      disposed = true;
      watcher?.dispose();
    });
  }
}
