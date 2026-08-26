// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

import { I18nContext, resolveLocale } from './I18nProvider';
import { useEffect, useReducer, useRef } from 'preact/hooks';
import { reducer, initialState } from './store';
import { bridge } from './bridge';
import { isConfigReady } from '../shared/configUtils';
import { CliRunOptions, FileChange, ReviewMode } from '../shared/types';
import { IdleView } from './views/IdleView';
import { RunningView } from './views/RunningView';
import { DoneView } from './views/DoneView';
import { EmptyView } from './views/EmptyView';
import { CancelledView } from './views/CancelledView';
import { FailedView } from './views/FailedView';
import './styles/global.css';

export function App() {
  const [state, dispatch] = useReducer(reducer, initialState);
  const modeFilesRequest = useRef(0);

  useEffect(() => {
    const unsub = bridge.onMessage((msg) => dispatch(msg));
    bridge.post({ type: 'ready' });
    return unsub;
  }, []);

  const configured = isConfigReady(state.config);
  const invalidateModeFiles = () => {
    dispatch({ type: 'filesLoading', requestId: ++modeFilesRequest.current });
  };
  const start = (options: CliRunOptions) => {
    dispatch({ type: 'startReview', mode: options.mode });
    bridge.post({ type: 'startReview', options });
  };
  const onModeChange = (mode: ReviewMode) => {
    invalidateModeFiles();
    bridge.post({ type: 'getGitState', mode });
  };
  const requestModeFiles = (options: CliRunOptions) => {
    const requestId = ++modeFilesRequest.current;
    dispatch({ type: 'filesLoading', requestId });
    bridge.post({ type: 'getModeFiles', requestId, options });
  };
  const openFile = (file: FileChange, options: CliRunOptions) => {
    bridge.post({ type: 'openFileDiff', path: file.path, status: file.status, options });
  };

  return (
    <I18nContext.Provider value={resolveLocale(state.locale)}>
      <div class="ocr-root">
        <div class="action-region">
          <IdleView gitState={state.gitState} modeFiles={state.modeFiles} filesLoading={state.filesLoading}
            configured={configured} onModeChange={onModeChange} onRequestModeFiles={requestModeFiles}
            onInvalidateModeFiles={invalidateModeFiles}
            onOpenFile={openFile} onStart={start} onOpenConfig={() => bridge.post({ type: 'openConfigPanel' })}
            running={state.view === 'running'} />

          {state.view !== 'idle' && (
            <div class="result-region">
              {state.view === 'running' && <RunningView logs={state.logs} onCancel={() => bridge.post({ type: 'cancelReview' })} />}
              {state.view === 'done' && state.session.result && (
                <DoneView result={state.session.result} commentStatus={state.commentStatus}
                  commentJumpable={state.commentJumpable} logs={state.logs}
                  onOpen={(i) => bridge.post({ type: 'jumpToComment', index: i })}
                  onAction={(i, action) => bridge.post({ type: 'commentAction', index: i, action })} />
              )}
              {state.view === 'empty' && <EmptyView logs={state.logs} />}
              {state.view === 'cancelled' && <CancelledView />}
              {state.view === 'failed' && <FailedView error={state.session.error} onRetry={() => start({ mode: ReviewMode.Workspace, vcs: state.gitState.vcs })} />}
            </div>
          )}
        </div>
      </div>
    </I18nContext.Provider>
  );
}
