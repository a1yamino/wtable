import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { AlertTriangle, RefreshCw } from 'lucide-react'

import { backend } from './backend'
import { AgentDrawer, type AgentFormValues } from './components/AgentDrawer'
import {
  BoardView,
  type BoardViewport,
  type NodeLayout,
} from './components/BoardView'
import { ConfirmDialog } from './components/ConfirmDialog'
import { FocusView } from './components/FocusView'
import { TemplateDrawer } from './components/TemplateDrawer'
import { WorkspaceRail } from './components/WorkspaceRail'
import { WorkspaceDrawer } from './components/WorkspaceDrawer'
import {
  templateFromWorkspace,
  type TemplateAgent,
  type TopologyTemplate,
} from './templates'
import type { Catalog, Pane, Snapshot, WorkspaceInfo } from './types'
import { baseName, errorMessage } from './ui'
import { installWindowDragFallback } from './windowDrag'

type LoadState = 'loading' | 'ready' | 'error'

type WorkspaceUI = {
  mode: 'focus' | 'board'
  mainId: string
  focusId: string
  dockId: string
  dockWidth: number
  minimap: boolean
  selectedId: string
  layouts: Record<string, NodeLayout>
  viewport: BoardViewport
}

type PersistedUI = {
  version: 2
  workspaces: Record<string, WorkspaceUI>
  templates: TopologyTemplate[]
}

type DrawerState =
  | { type: 'agent'; pane?: Pane; placement?: { x: number; y: number } }
  | { type: 'workspace'; creating: boolean }
  | { type: 'templates' }
  | null

type ConfirmState = {
  title: string
  message: string
  confirmLabel: string
  danger?: boolean
  action(): void | Promise<void>
}

const UI_STORAGE = 'wtable-desktop-ui-v2'
const EMPTY_SNAPSHOT: Snapshot = {
  panes: [],
  edges: [],
  workspaces: [],
  revision: 0,
}

export default function App() {
  const [status, setStatus] = useState<LoadState>('loading')
  const [fatal, setFatal] = useState('')
  const [snapshot, setSnapshot] = useState<Snapshot>(EMPTY_SNAPSHOT)
  const [catalog, setCatalog] = useState<Catalog>()
  const [activeWorkspace, setActiveWorkspace] = useState('')
  const [home, setHome] = useState('')
  const [busy, setBusy] = useState(false)
  const [drawer, setDrawer] = useState<DrawerState>(null)
  const [confirm, setConfirm] = useState<ConfirmState>()
  const [templateDraft, setTemplateDraft] = useState<TopologyTemplate>()
  const [makeMain, setMakeMain] = useState(false)
  const [notice, setNotice] = useState<{ text: string; warning?: boolean }>()
  const [ui, setUi] = useState<PersistedUI>(loadUi)
  const noticeTimer = useRef<number | undefined>(undefined)
  const lastPollError = useRef(0)

  const showNotice = useCallback((text: string, warning = false) => {
    setNotice({ text, warning })
    if (noticeTimer.current !== undefined) window.clearTimeout(noticeTimer.current)
    noticeTimer.current = window.setTimeout(() => setNotice(undefined), warning ? 6000 : 3600)
  }, [])

  const refresh = useCallback(async () => {
    const next = await backend.allSnapshot()
    setSnapshot(next)
    return next
  }, [])

  useEffect(() => {
    let active = true
    void backend
      .bootstrap()
      .then(async (result) => {
        if (!active) return
        setHome(result.home)
        setActiveWorkspace(result.workspace)
        setSnapshot(result.snapshot)
        setStatus('ready')
        const [all, available] = await Promise.all([
          backend.allSnapshot(),
          backend.catalog().catch(() => undefined),
        ])
        if (!active) return
        setSnapshot(all)
        setCatalog(available)
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
    if (status !== 'ready') return
    let active = true
    let timer: number
    const poll = async () => {
      try {
        const next = await backend.allSnapshot()
        if (active) setSnapshot(next)
      } catch (error) {
        if (active && Date.now() - lastPollError.current > 8000) {
          lastPollError.current = Date.now()
          showNotice(`连接中断：${errorMessage(error)}`, true)
        }
      } finally {
        if (active) timer = window.setTimeout(poll, 850)
      }
    }
    timer = window.setTimeout(poll, 500)
    return () => {
      active = false
      window.clearTimeout(timer)
    }
  }, [showNotice, status])

  useEffect(() => installWindowDragFallback(), [])

  useEffect(() => {
    try {
      localStorage.setItem(UI_STORAGE, JSON.stringify(ui))
    } catch {
      // UI preferences are non-critical; the live workspace remains usable.
    }
  }, [ui])

  useEffect(
    () => () => {
      if (noticeTimer.current !== undefined) window.clearTimeout(noticeTimer.current)
    },
    [],
  )

  const activePanes = useMemo(
    () =>
      snapshot.panes
        .filter((pane) => pane.workspace === activeWorkspace)
        .sort((left, right) => left.name.localeCompare(right.name)),
    [activeWorkspace, snapshot.panes],
  )
  const workspaceInfo = useMemo(
    () => workspaceWithFallback(snapshot.workspaces ?? [], activeWorkspace),
    [activeWorkspace, snapshot.workspaces],
  )
  const workspaceUi = normalizeWorkspaceUi(
    ui.workspaces[activeWorkspace],
    activePanes,
    workspaceInfo,
  )
  const mainPane =
    activePanes.find((pane) => pane.id === workspaceUi.mainId) ??
    preferredMain(activePanes, workspaceInfo)
  const focusPane =
    activePanes.find((pane) => pane.id === workspaceUi.focusId) ??
    mainPane ??
    activePanes[0]
  const dockPane = activePanes.find(
    (pane) => pane.id === workspaceUi.dockId && pane.id !== focusPane?.id,
  )
  const activeEdges = useMemo(() => {
    const ids = new Set(activePanes.map((pane) => pane.id))
    return (snapshot.edges ?? []).filter((edge) => ids.has(edge.from) && ids.has(edge.to))
  }, [activePanes, snapshot.edges])
  const activeWorkspaceName = baseName(activeWorkspace)
  const preview = useMemo(
    () => (templateDraft ? previewFromTemplate(templateDraft) : undefined),
    [templateDraft],
  )

  useEffect(() => {
    document.title = focusPane ? `${focusPane.name} · wtable` : 'wtable'
  }, [focusPane])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key === '1') {
        event.preventDefault()
        patchWorkspace(activeWorkspace, { mode: 'focus' })
        closeDrawer()
      }
      if ((event.metaKey || event.ctrlKey) && event.key === '2') {
        event.preventDefault()
        patchWorkspace(activeWorkspace, { mode: 'board' })
      }
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 't') {
        event.preventDefault()
        setDrawer({ type: 'templates' })
      }
      if (event.key === 'Escape' && drawer) closeDrawer()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  })

  function patchWorkspace(
    workspace: string,
    patch:
      | Partial<WorkspaceUI>
      | ((current: WorkspaceUI) => Partial<WorkspaceUI>),
  ) {
    setUi((previous) => {
      const panes = snapshot.panes.filter((pane) => pane.workspace === workspace)
      const info = workspaceWithFallback(snapshot.workspaces ?? [], workspace)
      const current = normalizeWorkspaceUi(previous.workspaces[workspace], panes, info)
      const changes = typeof patch === 'function' ? patch(current) : patch
      return {
        ...previous,
        workspaces: {
          ...previous.workspaces,
          [workspace]: { ...current, ...changes },
        },
      }
    })
  }

  async function openWorkspace(path: string) {
    if (!path || path === activeWorkspace || busy) return
    setBusy(true)
    try {
      await backend.openWorkspace(path)
      setActiveWorkspace(path)
      await refresh()
    } catch (error) {
      showNotice(errorMessage(error), true)
    } finally {
      setBusy(false)
    }
  }

  async function selectAgent(pane: Pane) {
    if (pane.workspace && pane.workspace !== activeWorkspace) {
      await openWorkspace(pane.workspace)
      setActiveWorkspace(pane.workspace)
    }
    patchWorkspace(pane.workspace || activeWorkspace, {
      mode: 'focus',
      focusId: pane.id,
      selectedId: pane.id,
    })
    void backend.focusPane(pane.id).catch((error: unknown) => {
      showNotice(errorMessage(error), true)
    })
  }

  async function openBoard(pane?: Pane, expand = false) {
    if (pane?.workspace && pane.workspace !== activeWorkspace) {
      await openWorkspace(pane.workspace)
    }
    const workspace = pane?.workspace || activeWorkspace
    const panes = snapshot.panes.filter((item) => item.workspace === workspace)
    const info = workspaceWithFallback(snapshot.workspaces ?? [], workspace)
    const current = normalizeWorkspaceUi(ui.workspaces[workspace], panes, info)
    const layouts = { ...current.layouts }
    let collision = false
    if (pane) {
      const layout = layouts[pane.id] ?? defaultLayout(pane.id)
      if (expand && !layout.expanded) {
        const candidate = {
          ...layout,
          expanded: true,
          width: Math.max(440, layout.width),
          height: Math.max(320, layout.height),
        }
        collision = panes.some((other) => {
          if (other.id === pane.id) return false
          const otherLayout = layouts[other.id] ?? defaultLayout(other.id)
          return layoutOverlaps(candidate, otherLayout)
        })
        layouts[pane.id] = collision ? layout : candidate
      } else {
        layouts[pane.id] = layout
      }
    }
    patchWorkspace(workspace, {
      mode: 'board',
      selectedId: pane?.id ?? current.selectedId,
      layouts,
    })
    if (collision) showNotice('空间不足，节点保持折叠；请在 Board 中调整位置。', true)
  }

  async function createWorkspace(path: string) {
    setBusy(true)
    try {
      await backend.createWorkspace(path)
      await refresh()
      setActiveWorkspace(path)
      patchWorkspace(path, {
        mode: 'focus',
        mainId: '',
        focusId: '',
        dockId: '',
        selectedId: '',
        layouts: {},
      })
      setDrawer(null)
    } catch (error) {
      showNotice(errorMessage(error), true)
    } finally {
      setBusy(false)
    }
  }

  async function submitAgent(values: AgentFormValues, setAsMain: boolean) {
    const currentDrawer = drawer
    if (!currentDrawer || currentDrawer.type !== 'agent') return
    setBusy(true)
    try {
      if (currentDrawer.pane) {
        await backend.updatePane({
          id: currentDrawer.pane.id,
          name: values.name,
          role: values.role,
          account: values.account,
          workspace: activeWorkspace,
        })
        if (setAsMain) {
          patchWorkspace(activeWorkspace, { mainId: currentDrawer.pane.id })
        }
      } else {
        const pane = await backend.spawnPane({
          name: values.name,
          kind: 'worker',
          preset: values.preset,
          role: values.role,
          account: values.account,
          cwd: values.cwd,
          workspace: activeWorkspace,
          command: values.command,
          injectBrief: false,
        })
        const placement = currentDrawer.placement
        patchWorkspace(activeWorkspace, (current) => ({
          mainId: setAsMain || !mainPane ? pane.id : current.mainId,
          focusId: pane.id,
          selectedId: pane.id,
          layouts: {
            ...current.layouts,
            [pane.id]: {
              x: placement?.x ?? 180,
              y: placement?.y ?? 190,
              width: 440,
              height: 320,
              expanded: false,
            },
          },
        }))
      }
      await refresh()
      setDrawer(null)
    } catch (error) {
      showNotice(errorMessage(error), true)
    } finally {
      setBusy(false)
    }
  }

  function requestDelete(pane: Pane) {
    setConfirm({
      title: `删除 ${pane.name}？`,
      message: '该 Agent 会停止运行，并从当前工作区移除。相关授权连接也会一并删除。',
      confirmLabel: '删除 Agent',
      danger: true,
      action: async () => {
        setBusy(true)
        try {
          await backend.removePane(pane.id)
          await refresh()
        } catch (error) {
          showNotice(errorMessage(error), true)
        } finally {
          setBusy(false)
        }
      },
    })
  }

  async function connectAgents(from: string, to: string) {
    if (from === to) return
    setBusy(true)
    try {
      await backend.addEdge(from, to)
      await backend.addEdge(to, from)
      await refresh()
    } catch (error) {
      showNotice(errorMessage(error), true)
    } finally {
      setBusy(false)
    }
  }

  async function addDirection(from: string, to: string) {
    try {
      await backend.addEdge(from, to)
      await refresh()
    } catch (error) {
      showNotice(errorMessage(error), true)
    }
  }

  async function removeDirection(from: string, to: string) {
    try {
      await backend.removeEdge(from, to)
      await refresh()
    } catch (error) {
      showNotice(errorMessage(error), true)
    }
  }

  function saveTemplate(name: string, description: string) {
    const template = templateFromWorkspace(
      name,
      description,
      activePanes,
      activeEdges,
      workspaceUi.layouts,
      mainPane?.id ?? '',
    )
    setUi((current) => ({
      ...current,
      templates: [template, ...current.templates.filter((item) => item.name !== name)],
    }))
    setTemplateDraft(template)
    showNotice('拓扑模板已保存。')
  }

  function cancelPreview() {
    setTemplateDraft(undefined)
    patchWorkspace(activeWorkspace, { mode: 'focus' })
  }

  function closeDrawer() {
    if (templateDraft) {
      setTemplateDraft(undefined)
      patchWorkspace(activeWorkspace, { mode: 'focus' })
    }
    setDrawer(null)
    setMakeMain(false)
  }

  function requestApplyTemplate() {
    if (!templateDraft) return
    setConfirm({
      title: `应用「${templateDraft.name}」？`,
      message: activePanes.length
        ? `当前 ${activePanes.length} 个 Agent 会停止并被模板替换。这个操作无法自动恢复。`
        : '将按照预览配置创建模板中的 Agent 和授权关系。',
      confirmLabel: '应用模板',
      danger: activePanes.length > 0,
      action: applyTemplate,
    })
  }

  async function applyTemplate() {
    if (!templateDraft) return
    setBusy(true)
    const created: Pane[] = []
    try {
      for (const pane of activePanes) await backend.removePane(pane.id)
      const keyToId = new Map<string, string>()
      for (const agent of templateDraft.agents) {
        const pane = await backend.spawnPane({
          name: agent.name,
          kind: 'worker',
          preset: agent.preset,
          role: agent.role,
          account: agent.account,
          cwd: agent.cwd || activeWorkspace,
          workspace: activeWorkspace,
          command: agent.command,
          injectBrief: false,
        })
        created.push(pane)
        keyToId.set(agent.key, pane.id)
      }
      for (const link of templateDraft.links) {
        const from = keyToId.get(link.from)
        const to = keyToId.get(link.to)
        if (!from || !to) continue
        await backend.addEdge(from, to)
        await backend.addEdge(to, from)
      }
      const layouts: Record<string, NodeLayout> = {}
      for (const agent of templateDraft.agents) {
        const id = keyToId.get(agent.key)
        if (id) layouts[id] = agent.layout
      }
      const mainId = templateDraft.main ? keyToId.get(templateDraft.main) ?? '' : ''
      patchWorkspace(activeWorkspace, {
        mode: 'focus',
        mainId,
        focusId: mainId || created[0]?.id || '',
        dockId: '',
        selectedId: mainId,
        layouts,
      })
      setTemplateDraft(undefined)
      setDrawer(null)
      await refresh()
      showNotice('模板已应用到当前 Workspace。')
    } catch (error) {
      for (const pane of created.reverse()) {
        await backend.removePane(pane.id).catch(() => undefined)
      }
      showNotice(`应用模板失败：${errorMessage(error)}`, true)
    } finally {
      setBusy(false)
    }
  }

  async function confirmAction() {
    if (!confirm) return
    const action = confirm.action
    setConfirm(undefined)
    await action()
  }

  if (status === 'loading') {
    return (
      <main className="launch-screen">
        <div className="launch-mark">
          <span>w</span>
        </div>
        <strong>正在连接本地工作台</strong>
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
          <AlertTriangle size={24} />
        </div>
        <strong>无法打开工作台</strong>
        <p>{fatal}</p>
        <button className="primary-button" type="button" onClick={() => window.location.reload()}>
          <RefreshCw size={15} />
          重试连接
        </button>
      </main>
    )
  }

  const boardPanes = preview?.panes ?? activePanes
  const boardEdges = preview?.edges ?? activeEdges
  const boardLayouts = preview?.layouts ?? workspaceUi.layouts
  const boardMainId = preview?.mainId ?? mainPane?.id ?? ''
  const boardSelectedId = preview ? '' : workspaceUi.selectedId
  const boardWorkspaceName = preview ? templateDraft?.name ?? activeWorkspaceName : activeWorkspaceName
  const railMainByWorkspace = Object.fromEntries(
    (snapshot.workspaces ?? []).map((workspace) => {
      const panes = snapshot.panes.filter((pane) => pane.workspace === workspace.path)
      const stored = ui.workspaces[workspace.path]?.mainId
      return [
        workspace.path,
        panes.some((pane) => pane.id === stored)
          ? stored ?? ''
          : preferredMain(panes, workspace)?.id ?? '',
      ]
    }),
  )

  return (
    <main className={`app-shell ${drawer ? 'has-drawer' : ''}`}>
      <WorkspaceRail
        workspaces={snapshot.workspaces ?? []}
        panes={snapshot.panes}
        activeWorkspace={activeWorkspace}
        focusId={focusPane?.id ?? ''}
        mainByWorkspace={railMainByWorkspace}
        onSelectAgent={(pane) => void selectAgent(pane)}
        onSelectWorkspace={(path) => void openWorkspace(path)}
        onCreateWorkspace={() => setDrawer({ type: 'workspace', creating: true })}
        onWorkspaceSettings={(path) => {
          if (path && path !== activeWorkspace) {
            void openWorkspace(path).then(() => {
              setDrawer({ type: 'workspace', creating: false })
            })
            return
          }
          setDrawer({ type: 'workspace', creating: false })
        }}
      />

      <section className="workbench">
        {workspaceUi.mode === 'focus' ? (
          <FocusView
            pane={focusPane}
            dockPane={dockPane}
            availableDockPanes={activePanes.filter((pane) => pane.id !== focusPane?.id)}
            dockWidth={workspaceUi.dockWidth}
            onDockWidthChange={(dockWidth) => patchWorkspace(activeWorkspace, { dockWidth })}
            onOpenDock={(dockId) => patchWorkspace(activeWorkspace, { dockId })}
            onCloseDock={() => patchWorkspace(activeWorkspace, { dockId: '' })}
            onOpenBoard={(pane) => void openBoard(pane, true)}
            onEditPane={(pane) => {
              setMakeMain(pane.id === mainPane?.id)
              setDrawer({ type: 'agent', pane })
            }}
            onError={(message) => showNotice(message, true)}
          />
        ) : (
          <BoardView
            workspaceName={boardWorkspaceName}
            panes={boardPanes}
            edges={boardEdges}
            mainId={boardMainId}
            selectedId={boardSelectedId}
            layouts={boardLayouts}
            viewport={workspaceUi.viewport}
            minimap={workspaceUi.minimap}
            onSelect={(pane) =>
              patchWorkspace(activeWorkspace, { selectedId: pane.id })
            }
            onFocus={(pane) => {
              patchWorkspace(activeWorkspace, {
                mode: 'focus',
                focusId: pane.id,
                selectedId: pane.id,
              })
            }}
            onEdit={(pane) => {
              setMakeMain(pane.id === mainPane?.id)
              setDrawer({ type: 'agent', pane })
            }}
            onDelete={requestDelete}
            onAddAgent={(x, y) => {
              setMakeMain(activePanes.length === 0)
              setDrawer({ type: 'agent', placement: { x, y } })
            }}
            onOpenTemplates={() => setDrawer({ type: 'templates' })}
            onLayoutChange={(id, layout) =>
              patchWorkspace(activeWorkspace, (current) => ({
                layouts: { ...current.layouts, [id]: layout },
              }))
            }
            onViewportChange={(viewport) => patchWorkspace(activeWorkspace, { viewport })}
            onMinimapChange={(minimap) => patchWorkspace(activeWorkspace, { minimap })}
            onConnect={(from, to) => void connectAgents(from, to)}
            onAddEdge={(from, to) => void addDirection(from, to)}
            onRemoveEdge={(from, to) => void removeDirection(from, to)}
            onError={(message) => showNotice(message, true)}
            previewLabel={preview ? templateDraft?.name : undefined}
          />
        )}
      </section>

      {drawer?.type === 'agent' && (
        <AgentDrawer
          pane={drawer.pane}
          placement={drawer.placement}
          workspace={activeWorkspace}
          catalog={catalog}
          makeMain={makeMain}
          busy={busy}
          onClose={() => {
            closeDrawer()
          }}
          onMakeMainChange={setMakeMain}
          onSubmit={(values, setAsMain) => void submitAgent(values, setAsMain)}
        />
      )}
      {drawer?.type === 'workspace' && (
        <WorkspaceDrawer
          workspace={activeWorkspace}
          creating={drawer.creating}
          busy={busy}
          onClose={closeDrawer}
          onSubmit={(path) => void createWorkspace(path)}
        />
      )}
      {drawer?.type === 'templates' && (
        <TemplateDrawer
          templates={ui.templates}
          draft={templateDraft}
          catalog={catalog}
          hasCurrentAgents={activePanes.length > 0}
          busy={busy}
          onClose={closeDrawer}
          onSave={saveTemplate}
          onSelect={(template) => {
            const copy: TopologyTemplate = {
              ...template,
              agents: template.agents.map((agent) => ({
                ...agent,
                layout: { ...agent.layout },
              })),
              links: template.links.map((link) => ({ ...link })),
            }
            setTemplateDraft(copy)
            patchWorkspace(activeWorkspace, { mode: 'board', selectedId: '' })
          }}
          onDraftChange={setTemplateDraft}
          onCancelPreview={cancelPreview}
          onApply={requestApplyTemplate}
        />
      )}

      {confirm && (
        <ConfirmDialog
          title={confirm.title}
          message={confirm.message}
          confirmLabel={confirm.confirmLabel}
          danger={confirm.danger}
          busy={busy}
          onCancel={() => setConfirm(undefined)}
          onConfirm={() => void confirmAction()}
        />
      )}

      {notice && (
        <div className={`notice ${notice.warning ? 'warning' : ''}`} role="status">
          {notice.warning && <AlertTriangle size={16} />}
          <span>{notice.text}</span>
        </div>
      )}

      <div className="visually-hidden" aria-live="polite">
        {busy ? '正在同步工作台' : home ? `本地状态目录 ${home}` : ''}
      </div>
    </main>
  )
}

function loadUi(): PersistedUI {
  try {
    const raw = localStorage.getItem(UI_STORAGE)
    if (!raw) return { version: 2, workspaces: {}, templates: [] }
    const parsed = JSON.parse(raw) as PersistedUI
    if (parsed?.version !== 2) return { version: 2, workspaces: {}, templates: [] }
    return {
      version: 2,
      workspaces: parsed.workspaces ?? {},
      templates: parsed.templates ?? [],
    }
  } catch {
    return { version: 2, workspaces: {}, templates: [] }
  }
}

function preferredMain(panes: Pane[], info: WorkspaceInfo) {
  const remembered = panes.find((pane) => pane.id === info.last_focus)
  if (remembered && ['running', 'starting', 'stopping'].includes(remembered.state)) {
    return remembered
  }
  return (
    panes.find((pane) => pane.kind === 'foreground' && pane.state === 'running') ??
    panes.find((pane) => pane.state === 'running') ??
    remembered ??
    panes[0]
  )
}

function normalizeWorkspaceUi(
  current: WorkspaceUI | undefined,
  panes: Pane[],
  info: WorkspaceInfo,
): WorkspaceUI {
  const main = preferredMain(panes, info)
  const base: WorkspaceUI = {
    mode: 'focus',
    mainId: main?.id ?? '',
    focusId: main?.id ?? '',
    dockId: '',
    dockWidth: 380,
    minimap: false,
    selectedId: main?.id ?? '',
    layouts: {},
    viewport: { x: 0, y: 0, zoom: 1 },
  }
  if (!current) return base
  const mainId = panes.some((pane) => pane.id === current.mainId)
    ? current.mainId
    : base.mainId
  const focusId = panes.some((pane) => pane.id === current.focusId)
    ? current.focusId
    : mainId
  const dockId =
    current.dockId &&
    current.dockId !== focusId &&
    panes.some((pane) => pane.id === current.dockId)
      ? current.dockId
      : ''
  return {
    ...base,
    ...current,
    mainId,
    focusId,
    dockId,
    layouts: current.layouts ?? {},
    viewport: current.viewport ?? base.viewport,
  }
}

function workspaceWithFallback(workspaces: WorkspaceInfo[], active: string): WorkspaceInfo {
  return (
    workspaces.find((workspace) => workspace.path === active) ?? {
      path: active,
      last_opened: '',
      pane_count: 0,
      running_count: 0,
    }
  )
}

function defaultLayout(id: string): NodeLayout {
  let hash = 0
  for (const character of id) hash = (hash * 31 + character.charCodeAt(0)) >>> 0
  const index = hash % 12
  return {
    x: 110 + (index % 4) * 320,
    y: 170 + Math.floor(index / 4) * 205,
    width: 440,
    height: 320,
    expanded: false,
  }
}

function layoutOverlaps(left: NodeLayout, right: NodeLayout) {
  const leftSize = left.expanded
    ? { width: left.width, height: left.height }
    : { width: 248, height: 58 }
  const rightSize = right.expanded
    ? { width: right.width, height: right.height }
    : { width: 248, height: 58 }
  const gap = 20
  return (
    left.x < right.x + rightSize.width + gap &&
    left.x + leftSize.width + gap > right.x &&
    left.y < right.y + rightSize.height + gap &&
    left.y + leftSize.height + gap > right.y
  )
}

function previewFromTemplate(template: TopologyTemplate) {
  const panes: Pane[] = template.agents.map((agent) => ({
    id: `preview-${agent.key}`,
    name: agent.name,
    backend: 'pty',
    command: agent.command,
    cwd: agent.cwd,
    workspace: 'preview',
    state: 'exited',
    preset: agent.preset,
    role: agent.role,
    account: agent.account,
  }))
  const edges = template.links.map((link) => ({
    from: `preview-${link.from}`,
    to: `preview-${link.to}`,
  }))
  return {
    panes,
    edges,
    layouts: Object.fromEntries(
      template.agents.map((agent) => [
        `preview-${agent.key}`,
        { ...agent.layout, expanded: false },
      ]),
    ),
    mainId: template.main ? `preview-${template.main}` : '',
  }
}
