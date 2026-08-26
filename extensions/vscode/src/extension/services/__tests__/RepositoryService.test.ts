// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

import { ReviewMode } from '../../../shared/types';
import { RepositoryService } from '../RepositoryService';
import * as vscode from 'vscode';

describe('RepositoryService SVN routing', () => {
  beforeEach(() => jest.clearAllMocks());

  it('uses delegate preview for an SVN revision range without calling Git services', async () => {
    const cli = {
      delegatePreview: jest.fn().mockResolvedValue({
        vcs: 'svn',
        mode: 'range',
        reviewableFiles: [{ path: 'src/a.ts', status: 'modified' }],
      }),
    };
    const git = {
      getBranchDiff: jest.fn(),
      getCommitFiles: jest.fn(),
    };
    const detect = jest.fn().mockResolvedValue('svn');
    const service = new RepositoryService(cli as any, git as any, undefined, detect);
    const options = {
      mode: ReviewMode.Branch,
      from: '120',
      to: '128',
      svnFromTarget: '^/trunk@130',
      svnToTarget: '^/branches/feature@130',
    };

    await expect(service.getModeFiles(options, '/workspace')).resolves.toEqual([
      { path: 'src/a.ts', status: 'modified' },
    ]);
    expect(cli.delegatePreview).toHaveBeenCalledWith({ ...options, vcs: 'svn' }, '/workspace');
    expect(git.getBranchDiff).not.toHaveBeenCalled();
    expect(git.getCommitFiles).not.toHaveBeenCalled();
    expect(detect).toHaveBeenCalledWith('/workspace');
  });

  it('detects an SVN workspace and previews it without the VS Code Git API', async () => {
    const cli = {
      delegatePreview: jest.fn().mockResolvedValue({
        vcs: 'svn',
        mode: 'workspace',
        reviewableFiles: [{ path: 'src/worktree.ts', status: 'modified' }],
      }),
    };
    const git = { getState: jest.fn() };
    const detect = jest.fn().mockResolvedValue('svn');
    const service = new RepositoryService(cli as any, git as any, undefined, detect);

    await expect(service.getState(ReviewMode.Workspace, '/workspace')).resolves.toMatchObject({
      vcs: 'svn',
      workspaceFiles: [{ path: 'src/worktree.ts', status: 'modified' }],
    });
    expect(cli.delegatePreview).toHaveBeenCalledWith({ mode: ReviewMode.Workspace, vcs: 'svn' }, '/workspace');
    expect(git.getState).not.toHaveBeenCalled();
  });

  it('keeps immutable SVN snapshots in the sidebar instead of opening Git diffs', async () => {
    const git = { openDiff: jest.fn() };
    const service = new RepositoryService({} as any, git as any);
    const notice = jest.spyOn(vscode.window, 'showInformationMessage');

    await service.openFile({
      mode: ReviewMode.Commit,
      vcs: 'svn',
      commit: '128',
      path: 'src/a.ts',
      status: 'modified',
    }, '/workspace');

    expect(git.openDiff).not.toHaveBeenCalled();
    expect(notice).toHaveBeenCalledWith(expect.stringContaining('sidebar'));
  });

  it('caches repository detection for repeated host requests', async () => {
    const cli = { delegatePreview: jest.fn().mockResolvedValue({ reviewableFiles: [] }) };
    const detect = jest.fn().mockResolvedValue('svn');
    const service = new RepositoryService(cli as any, {} as any, undefined, detect);
    const options = { mode: ReviewMode.Commit, commit: '128' };

    await service.getModeFiles(options, '/workspace');
    await service.getModeFiles(options, '/workspace');

    expect(detect).toHaveBeenCalledTimes(1);
  });

  it('rejects SVN workspace paths that escape the workspace root', async () => {
    const service = new RepositoryService({} as any, {} as any);
    const open = jest.spyOn(vscode.window, 'showTextDocument');
    const warning = jest.spyOn(vscode.window, 'showWarningMessage');

    await service.openFile({
      mode: ReviewMode.Workspace,
      vcs: 'svn',
      path: '../../secret.txt',
      status: 'modified',
    }, '/workspace');

    expect(open).not.toHaveBeenCalled();
    expect(warning).toHaveBeenCalledWith(expect.stringContaining('outside'));
  });
});
