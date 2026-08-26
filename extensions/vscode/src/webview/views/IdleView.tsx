// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

import { useState, useEffect } from 'preact/hooks';
import { useT } from '../I18nProvider';
import { GitState, ReviewMode, CliRunOptions, FileChange } from '../../shared/types';
import { FileList } from '../components/FileList';
import { Select } from '../components/Select';


interface Props {
  gitState: GitState;
  modeFiles: FileChange[];
  filesLoading: boolean;
  configured: boolean;
  onModeChange: (mode: ReviewMode) => void;
  onRequestModeFiles: (options: CliRunOptions) => void;
  onInvalidateModeFiles: () => void;
  onOpenFile: (file: FileChange, options: CliRunOptions) => void;
  onStart: (options: CliRunOptions) => void;
  onOpenConfig: () => void;
  running?: boolean;
}

export function IdleView({ gitState, modeFiles, filesLoading, configured, onModeChange, onRequestModeFiles, onInvalidateModeFiles, onOpenFile, onStart, onOpenConfig, running }: Props) {
  const [mode, setMode] = useState<ReviewMode>(ReviewMode.Workspace);
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [commit, setCommit] = useState('');
  const [svnFromTarget, setSvnFromTarget] = useState('');
  const [svnToTarget, setSvnToTarget] = useState('');
  const [prompt, setPrompt] = useState('');
  const t = useT();
  const isSVN = gitState.vcs === 'svn';
  const normalizedFrom = from.trim();
  const normalizedTo = to.trim();
  const normalizedCommit = commit.trim();
  const normalizedFromTarget = svnFromTarget.trim();
  const normalizedToTarget = svnToTarget.trim();
  const targetsPaired = (!normalizedFromTarget && !normalizedToTarget)
    || (!!normalizedFromTarget && !!normalizedToTarget);
  const options: CliRunOptions = {
    mode,
    vcs: gitState.vcs,
    from: normalizedFrom || undefined,
    to: normalizedTo || undefined,
    commit: normalizedCommit || undefined,
    svnFromTarget: normalizedFromTarget || undefined,
    svnToTarget: normalizedToTarget || undefined,
    customPrompt: prompt,
  };

  const getPrimaryLabel = () => {
    if (!configured) return t('view.idle.configFirst');
    if (running) return t('view.idle.reviewing');
    if (!selectionReady) {
      if (mode === ReviewMode.Branch) return isSVN ? t('view.idle.selectRange') : t('view.idle.selectBranch');
      return isSVN ? t('view.idle.selectRevision') : t('view.idle.selectCommit');
    }
    if (files.length === 0) return t('view.idle.noFiles');
    return t('view.idle.reviewAll');
  };

  const switchMode = (m: ReviewMode) => { setMode(m); onModeChange(m); };

  const vcsLabel = () => {
    if (gitState.vcs === 'svn') return t('view.idle.detectedSvn');
    if (gitState.vcs === 'git') return t('view.idle.detectedGit');
    return t('view.idle.noRepository');
  };

  const modeLabel = (candidate: ReviewMode) => {
    if (candidate === ReviewMode.Workspace) return t('view.idle.workspace');
    if (candidate === ReviewMode.Branch) {
      return t(isSVN ? 'view.idle.revisionRange' : 'view.idle.branch');
    }
    return t(isSVN ? 'view.idle.revision' : 'view.idle.commit');
  };

  // 分支两端都选好后,拉取 diff 文件列表
  useEffect(() => {
    if (mode !== ReviewMode.Branch) return undefined;
    onInvalidateModeFiles();
    if (!normalizedFrom || !normalizedTo || !targetsPaired) return undefined;
    const timer = setTimeout(() => onRequestModeFiles(options), isSVN ? 350 : 0);
    return () => clearTimeout(timer);
  }, [mode, from, to, svnFromTarget, svnToTarget, gitState.vcs]);

  // 选中某 commit 后,拉取该 commit 文件列表
  useEffect(() => {
    if (mode !== ReviewMode.Commit) return undefined;
    onInvalidateModeFiles();
    if (!normalizedCommit) return undefined;
    const timer = setTimeout(() => onRequestModeFiles(options), isSVN ? 350 : 0);
    return () => clearTimeout(timer);
  }, [mode, commit, gitState.vcs]);

  const files = mode === ReviewMode.Workspace ? gitState.workspaceFiles : modeFiles;
  // 仅在「确实发起了请求」时显示 loading:分支需选满两端,提交需选中 commit。
  const willRequest = mode === ReviewMode.Workspace
    || (mode === ReviewMode.Branch && !!normalizedFrom && !!normalizedTo && targetsPaired)
    || (mode === ReviewMode.Commit && !!normalizedCommit);
  const loading = filesLoading && willRequest;
  // 可发起审查的前置条件:按 tab 校验选择已就绪,且有待审查文件、不在加载/审查中。
  const selectionReady =
    mode === ReviewMode.Workspace
    || (mode === ReviewMode.Branch && !!normalizedFrom && !!normalizedTo && targetsPaired)
    || (mode === ReviewMode.Commit && !!normalizedCommit);
  const canReview = configured && !running && !loading && selectionReady && files.length > 0;
  const primaryDisabled = configured ? !canReview : running || loading;

  const handlePrimary = () => {
    if (!configured) {
      onOpenConfig();
      return;
    }
    onStart(options);
  };

  return (
    <div class="setup">
      <div class={`vcs-context ${gitState.vcs}`}>
        {vcsLabel()}
      </div>
      <div class="mode-tabs">
        {([ReviewMode.Workspace, ReviewMode.Branch, ReviewMode.Commit]).map((m) => (
          <button key={m} class={`mode-tab${mode === m ? ' active' : ''}`} onClick={() => switchMode(m)}>
            {modeLabel(m)}
          </button>
        ))}
      </div>

      {mode === ReviewMode.Branch && (
        <div class="mode-params active">
          <div class="mode-param-label">{t(isSVN ? 'view.idle.baseRevision' : 'view.idle.baseRef')}</div>
          {isSVN ? (
            <input class="mode-param-input" value={from} placeholder={t('view.idle.enterRevision')}
              onInput={(e) => setFrom((e.target as HTMLInputElement).value)} />
          ) : (
            <Select value={from} placeholder={t('view.idle.chooseBranch')} onChange={setFrom}
              options={gitState.branches.map((b) => ({ value: b, label: b }))} />
          )}
          <div class="mode-param-label">{t(isSVN ? 'view.idle.targetRevision' : 'view.idle.targetRef')}</div>
          {isSVN ? (
            <input class="mode-param-input" value={to} placeholder={t('view.idle.enterRevision')}
              onInput={(e) => setTo((e.target as HTMLInputElement).value)} />
          ) : (
            <Select value={to} placeholder={t('view.idle.chooseBranch')} onChange={setTo}
              options={gitState.branches.map((b) => ({ value: b, label: b }))} />
          )}
          {isSVN && (
            <>
              <div class="mode-param-label">{t('view.idle.svnSourceTarget')}</div>
              <input class="mode-param-input" value={svnFromTarget} placeholder="^/trunk@HEAD"
                onInput={(e) => setSvnFromTarget((e.target as HTMLInputElement).value)} />
              <div class="mode-param-label">{t('view.idle.svnDestinationTarget')}</div>
              <input class="mode-param-input" value={svnToTarget} placeholder="^/branches/feature@HEAD"
                onInput={(e) => setSvnToTarget((e.target as HTMLInputElement).value)} />
              <div class="mode-param-help">{t('view.idle.svnTargetsHelp')}</div>
            </>
          )}
        </div>
      )}

      {mode === ReviewMode.Commit && (
        <div class="mode-params active">
          {isSVN ? (
            <>
              <div class="mode-param-label">{t('view.idle.revisionNumber')}</div>
              <input class="mode-param-input" value={commit} placeholder={t('view.idle.enterRevision')}
                onInput={(e) => setCommit((e.target as HTMLInputElement).value)} />
            </>
          ) : (
            <>
              <div class="files-label">{t('view.idle.commitHistory')}</div>
              <div class="commit-list">
                {gitState.recentCommits.map((c) => (
                  <label key={c.sha} class={`commit-row${commit === c.sha ? ' active' : ''}`} onClick={() => setCommit(c.sha)}>
                    <input type="radio" name="commit" class="commit-radio" checked={commit === c.sha} />
                    <div class="commit-info">
                      <div class="commit-msg">{c.message}</div>
                      <div class="commit-meta"><span class="commit-sha">{c.sha}</span> · {c.relativeTime}</div>
                    </div>
                  </label>
                ))}
              </div>
            </>
          )}
        </div>
      )}

      <FileList files={files} loading={loading}
        onOpenFile={(f) => onOpenFile(f, options)} />

      <textarea class="mode-param-input" rows={3} placeholder={t('view.idle.customPrompt')}
        value={prompt} onInput={(e) => setPrompt((e.target as HTMLTextAreaElement).value)} />

      {configured && (
        <div class="setup-secondary">
          <button type="button" class="link-btn" onClick={onOpenConfig}>{t('view.idle.modelConfig')}</button>
        </div>
      )}

      {loading ? (
        <div class="primary-btn skeleton-btn"><div class="skeleton-bar" style={{ width: '40%' }} /></div>
      ) : (
        <button class={`primary-btn${!configured ? ' configure' : ''}`} disabled={primaryDisabled}
          onClick={handlePrimary}>
          {getPrimaryLabel()}
        </button>
      )}
    </div>
  );
}
