import { useState } from 'react'
import {
  ChevronDown,
  Folder,
  MoreHorizontal,
  Plus,
  Settings2,
} from 'lucide-react'

import type { Pane, WorkspaceInfo } from '../types'
import { agentState, aggregateState, backendLabel, baseName, stateLabel } from '../ui'

type Props = {
  workspaces: WorkspaceInfo[]
  panes: Pane[]
  activeWorkspace: string
  focusId: string
  mainByWorkspace: Record<string, string>
  onSelectAgent(pane: Pane): void
  onSelectWorkspace(path: string): void
  onCreateWorkspace(): void
  onWorkspaceSettings(path?: string): void
}

export function WorkspaceRail({
  workspaces,
  panes,
  activeWorkspace,
  focusId,
  mainByWorkspace,
  onSelectAgent,
  onSelectWorkspace,
  onCreateWorkspace,
  onWorkspaceSettings,
}: Props) {
  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({})
  const mergedWorkspaces = [...workspaces]
  const knownPaths = new Set(mergedWorkspaces.map((workspace) => workspace.path))
  for (const pane of panes) {
    if (pane.workspace && !knownPaths.has(pane.workspace)) {
      knownPaths.add(pane.workspace)
      mergedWorkspaces.push(
        {
          path: pane.workspace,
          last_opened: '',
          pane_count: 0,
          running_count: 0,
        },
      )
    }
  }

  return (
    <aside className="workspace-rail" aria-label="工作区与 Agent">
      <div className="window-drag-region" aria-hidden="true" />
      <header className="rail-heading">
        <div className="brand-lockup">
          <span className="brand-symbol" aria-hidden="true">
            <span />
            <span />
            <span />
          </span>
          <strong>wtable</strong>
        </div>
        <button
          className="icon-button"
          type="button"
          aria-label="新建工作区"
          title="新建工作区"
          onClick={onCreateWorkspace}
        >
          <Plus size={16} strokeWidth={1.8} />
        </button>
      </header>

      <div className="workspace-list">
        {mergedWorkspaces.map((workspace) => {
          const workspacePanes = panes.filter((pane) => pane.workspace === workspace.path)
          const active = workspace.path === activeWorkspace
          const isCollapsed = collapsed[workspace.path] ?? false
          const mainId = mainByWorkspace[workspace.path]
          const sorted = [...workspacePanes].sort((left, right) => {
            if (left.id === mainId) return -1
            if (right.id === mainId) return 1
            return left.name.localeCompare(right.name)
          })

          return (
            <section
              className={`workspace-group ${active ? 'active' : ''} ${isCollapsed ? 'collapsed' : ''}`}
              key={workspace.path}
              aria-label={`${baseName(workspace.path)} 工作区`}
            >
              <div className="workspace-group-heading">
                <button
                  className="workspace-toggle"
                  type="button"
                  aria-expanded={!isCollapsed}
                  aria-label={isCollapsed ? '展开工作区' : '折叠工作区'}
                  title={isCollapsed ? '展开' : '折叠'}
                  onClick={() =>
                    setCollapsed((current) => ({
                      ...current,
                      [workspace.path]: !isCollapsed,
                    }))
                  }
                >
                  <ChevronDown size={14} strokeWidth={1.8} />
                </button>
                <button
                  className="workspace-name-button"
                  type="button"
                  onClick={() => onSelectWorkspace(workspace.path)}
                >
                  <span className="workspace-name">{baseName(workspace.path)}</span>
                  {isCollapsed && (
                    <span
                      className={`state-dot ${aggregateState(workspacePanes)}`}
                      role="img"
                      aria-label={stateLabel[aggregateState(workspacePanes)]}
                    />
                  )}
                </button>
                <button
                  className="icon-button subtle"
                  type="button"
                  aria-label={`${baseName(workspace.path)} 工作区操作`}
                  title="工作区操作"
                  onClick={() => onWorkspaceSettings(workspace.path)}
                >
                  <MoreHorizontal size={16} strokeWidth={1.8} />
                </button>
              </div>

              {!isCollapsed && (
                <>
                  <div className="workspace-directory" title={workspace.path}>
                    <Folder size={12} strokeWidth={1.7} />
                    <span>{workspace.path}</span>
                  </div>
                  <div className="agent-list">
                    {sorted.map((pane) => {
                      const state = agentState(pane)
                      const selected = active && pane.id === focusId
                      return (
                        <button
                          key={pane.id}
                          className={`agent-row ${pane.id === mainId ? 'main-agent' : ''} ${selected ? 'selected' : ''}`}
                          type="button"
                          onClick={() => onSelectAgent(pane)}
                          aria-label={`${pane.name}，${backendLabel(pane)}，${stateLabel[state]}`}
                          title={`${pane.name} · ${stateLabel[state]}`}
                        >
                          <span className={`state-dot ${state}`} aria-hidden="true" />
                          <span className="agent-name">{pane.name}</span>
                          <span className="backend-badge">{backendLabel(pane)}</span>
                        </button>
                      )
                    })}
                    {sorted.length === 0 && (
                      <p className="rail-empty">这个工作区还没有 Agent。</p>
                    )}
                  </div>
                </>
              )}
            </section>
          )
        })}
      </div>

      <footer className="rail-footer">
        <button
          className="rail-settings"
          type="button"
          onClick={() => onWorkspaceSettings()}
        >
          <Settings2 size={15} strokeWidth={1.7} />
          工作区设置
        </button>
      </footer>
    </aside>
  )
}
