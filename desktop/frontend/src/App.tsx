import { useEffect, useMemo, useRef, useState } from 'react'

import { backend } from './backend'
import { AgentCanvas } from './components/AgentCanvas'
import { TerminalPane } from './components/TerminalPane'
import type { Bootstrap, Pane, Snapshot } from './types'

type LoadState = 'loading' | 'ready' | 'error'

export default function App() {
  const [bootstrap, setBootstrap] = useState<Bootstrap>()
  const [snapshot, setSnapshot] = useState<Snapshot>({ panes: [], revision: 0 })
  const [workspace, setWorkspace] = useState('')
  const [selectedId, setSelectedId] = useState('')
  const [status, setStatus] = useState<LoadState>('loading')
  const [fatal, setFatal] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [split, setSplit] = useState(57)
  const selectedRef = useRef('')
  const noticeTimer = useRef<number | undefined>(undefined)

  const showError = (message: string) => {
    setNotice(message)
    if (noticeTimer.current !== undefined) window.clearTimeout(noticeTimer.current)
    noticeTimer.current = window.setTimeout(() => setNotice(''), 4200)
  }

  useEffect(() => {
    let active = true
    void backend
      .bootstrap()
      .then((result) => {
        if (!active) return
        setBootstrap(result)
        setSnapshot(result.snapshot)
        setWorkspace(result.workspace)
        const initial = initialPane(result.snapshot)
        selectedRef.current = initial
        setSelectedId(initial)
        setStatus('ready')
      })
      .catch((error: unknown) => {
        if (!active) return
        setFatal(errorMessage(error))
        setStatus('error')
      })
    return () => {
      active = false
    }
  }, [])

  useEffect(() => {
    if (!workspace || status !== 'ready') return
    let active = true
    let timer: number

    const refresh = async () => {
      try {
        const next = await backend.snapshot(workspace)
        if (!active) return
        setSnapshot(next)
        if (!next.panes.some((pane) => pane.id === selectedRef.current)) {
          const replacement = initialPane(next)
          selectedRef.current = replacement
          setSelectedId(replacement)
        }
      } catch (error) {
        if (active) showError(errorMessage(error))
      } finally {
        if (active) timer = window.setTimeout(refresh, 800)
      }
    }
    timer = window.setTimeout(refresh, 350)
    return () => {
      active = false
      window.clearTimeout(timer)
    }
  }, [workspace, status])

  useEffect(() => {
    const pane = snapshot.panes.find((item) => item.id === selectedRef.current)
    if (pane) document.title = `${pane.name} · wtable`
    return () => {
      document.title = 'wtable'
    }
  }, [snapshot, selectedId])

  useEffect(() => {
    return () => {
      if (noticeTimer.current !== undefined) window.clearTimeout(noticeTimer.current)
    }
  }, [])

  const selectedPane = useMemo(
    () => snapshot.panes.find((pane) => pane.id === selectedId),
    [selectedId, snapshot.panes],
  )
  const sortedPanes = useMemo(() => sortPanes(snapshot.panes), [snapshot.panes])
  const workspaceName = baseName(workspace)
  const running = snapshot.panes.filter((pane) => pane.state === 'running').length
  const attention = snapshot.panes.reduce(
    (total, pane) => total + (pane.unread ?? 0) + (pane.state === 'failed' ? 1 : 0),
    0,
  )

  const selectPane = (id: string) => {
    selectedRef.current = id
    setSelectedId(id)
    void backend.focusPane(id).catch((error: unknown) => showError(errorMessage(error)))
  }

  const changeWorkspace = async (path: string) => {
    if (!path || path === workspace || busy) return
    setBusy(true)
    try {
      const next = await backend.openWorkspace(path)
      setWorkspace(path)
      setSnapshot(next)
      const initial = initialPane(next)
      selectedRef.current = initial
      setSelectedId(initial)
    } catch (error) {
      showError(errorMessage(error))
    } finally {
      setBusy(false)
    }
  }

  const createShell = async () => {
    if (busy) return
    setBusy(true)
    try {
      const pane = await backend.createShell(nextShellName(snapshot.panes))
      const next = await backend.snapshot(workspace)
      setSnapshot(next)
      selectedRef.current = pane.id
      setSelectedId(pane.id)
    } catch (error) {
      showError(errorMessage(error))
    } finally {
      setBusy(false)
    }
  }

  const refresh = async () => {
    setBusy(true)
    try {
      setSnapshot(await backend.snapshot(workspace))
    } catch (error) {
      showError(errorMessage(error))
    } finally {
      setBusy(false)
    }
  }

  const startResize = (event: React.PointerEvent) => {
    event.currentTarget.setPointerCapture(event.pointerId)
    const frame = event.currentTarget.parentElement
    if (!frame) return
    const bounds = frame.getBoundingClientRect()
    const move = (pointerEvent: PointerEvent) => {
      const percent = ((pointerEvent.clientX - bounds.left) / bounds.width) * 100
      setSplit(Math.min(70, Math.max(38, percent)))
    }
    const stop = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', stop)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', stop)
  }

  if (status === 'loading') {
    return (
      <main className="launch-screen">
        <div className="launch-mark">
          <span>w</span>
        </div>
        <div className="launch-copy">
          <strong>wtable</strong>
          <p>正在连接本地工作台</p>
        </div>
        <div className="launch-track">
          <i />
        </div>
      </main>
    )
  }

  if (status === 'error') {
    return (
      <main className="launch-screen error-screen">
        <div className="launch-mark warning">
          <span>!</span>
        </div>
        <div className="launch-copy">
          <strong>无法打开工作台</strong>
          <p>{fatal}</p>
        </div>
        <button className="primary-button" onClick={() => window.location.reload()}>
          重试连接
        </button>
      </main>
    )
  }

  return (
    <main className="app-shell">
      <header className="titlebar">
        <div className="brand">
          <span className="brand-mark">w</span>
          <div>
            <strong>wtable</strong>
            <span>visual workbench</span>
          </div>
        </div>
        <div className="workspace-switcher">
          <span className="workspace-kicker">WORKSPACE</span>
          <select
            value={workspace}
            disabled={busy}
            onChange={(event) => void changeWorkspace(event.target.value)}
          >
            {snapshot.workspaces?.map((item) => (
              <option key={item.path} value={item.path}>
                {baseName(item.path)}
              </option>
            ))}
            {!snapshot.workspaces?.some((item) => item.path === workspace) && (
              <option value={workspace}>{workspaceName || '选择工作区'}</option>
            )}
          </select>
          <span className="workspace-path">{workspace}</span>
        </div>
        <div className="title-actions">
          <div className="health-copy">
            <span><i className="live-dot" /> daemon</span>
            <span>{running} running</span>
            <span>{attention} attention</span>
          </div>
          <button
            className="icon-button"
            title="刷新"
            disabled={busy}
            onClick={() => void refresh()}
          >
            ↻
          </button>
          <button className="primary-button compact" disabled={busy} onClick={() => void createShell()}>
            <span>＋</span>
            新终端
          </button>
        </div>
      </header>

      <div className="workspace-grid">
        <aside className="rail">
          <div className="rail-heading">
            <div>
              <span className="eyebrow">AGENTS</span>
              <strong>{snapshot.panes.length} 个会话</strong>
            </div>
            <span className="rail-count">{running}</span>
          </div>
          <div className="rail-list">
            {sortedPanes.map((pane) => (
              <button
                key={pane.id}
                className={`pane-row ${pane.id === selectedId ? 'active' : ''}`}
                onClick={() => selectPane(pane.id)}
              >
                <span className={`pane-state ${pane.state}`} />
                <span className="pane-row-copy">
                  <span className="pane-row-title">
                    <strong>{pane.name}</strong>
                    {pane.kind === 'foreground' && <em>FG</em>}
                  </span>
                  <span className="pane-row-detail">
                    {pane.role || pane.preset || pane.backend}
                  </span>
                  <span className="pane-row-output">
                    {cleanOutput(pane.last_output) || '等待终端输出'}
                  </span>
                </span>
                {(pane.unread ?? 0) > 0 && <b className="rail-unread">{pane.unread}</b>}
              </button>
            ))}
            {snapshot.panes.length === 0 && (
              <div className="rail-empty">
                <strong>这里还很安静</strong>
                <p>创建一个 shell 开始。</p>
                <button className="text-button" onClick={() => void createShell()}>
                  创建终端 →
                </button>
              </div>
            )}
          </div>
          <div className="rail-footer">
            <span className="shortcut-key">⌘</span>
            <span>拖动节点切换上下文</span>
          </div>
        </aside>

        <div
          className="split-workspace"
          style={{ gridTemplateColumns: `minmax(430px, ${split}%) 8px minmax(390px, 1fr)` }}
        >
          <TerminalPane pane={selectedPane} onError={showError} />
          <div
            className="splitter"
            onPointerDown={startResize}
            title="拖动调整比例"
          >
            <i />
          </div>
          <AgentCanvas
            snapshot={snapshot}
            selectedId={selectedId}
            onSelect={selectPane}
          />
        </div>
      </div>

      <footer className="statusbar">
        <span><i className="live-dot" /> {bootstrap?.home}</span>
        <span>{busy ? '正在同步…' : '本地实时同步'}</span>
      </footer>

      {notice && (
        <div className="toast" role="status">
          <span>!</span>
          {notice}
        </div>
      )}
    </main>
  )
}

function initialPane(snapshot: Snapshot) {
  const last = snapshot.panes.find((pane) => pane.id === snapshot.last_focus)
  if (last) return last.id
  const foreground = snapshot.panes.find(
    (pane) => pane.kind === 'foreground' && pane.state === 'running',
  )
  if (foreground) return foreground.id
  return snapshot.panes.find((pane) => pane.state === 'running')?.id ?? snapshot.panes[0]?.id ?? ''
}

function sortPanes(panes: Pane[]) {
  return [...panes].sort((left, right) => {
    if (left.kind !== right.kind) return left.kind === 'foreground' ? -1 : 1
    if (left.state !== right.state) {
      if (left.state === 'running') return -1
      if (right.state === 'running') return 1
    }
    return left.name.localeCompare(right.name)
  })
}

function nextShellName(panes: Pane[]) {
  let index = 1
  const names = new Set(panes.map((pane) => pane.name))
  while (names.has(`shell-${index}`)) index += 1
  return `shell-${index}`
}

function cleanOutput(value?: string) {
  return value?.replace(/\s+/g, ' ').trim().slice(-88) ?? ''
}

function baseName(path: string) {
  if (!path) return ''
  const clean = path.replace(/\/+$/, '')
  return clean.slice(clean.lastIndexOf('/') + 1) || clean
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error)
}
