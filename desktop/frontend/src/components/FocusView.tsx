import { useEffect, useRef, useState } from 'react'
import {
  Folder,
  Maximize2,
  MoreHorizontal,
  PanelRightOpen,
  Settings2,
  X,
} from 'lucide-react'

import type { Pane } from '../types'
import { agentState, backendLabel, stateLabel } from '../ui'
import { TerminalSurface } from './TerminalSurface'

type Props = {
  pane?: Pane
  dockPane?: Pane
  availableDockPanes: Pane[]
  dockWidth: number
  onDockWidthChange(width: number): void
  onOpenDock(id: string): void
  onCloseDock(): void
  onOpenBoard(pane: Pane): void
  onEditPane(pane: Pane): void
  onError(message: string): void
}

export function FocusView({
  pane,
  dockPane,
  availableDockPanes,
  dockWidth,
  onDockWidthChange,
  onOpenDock,
  onCloseDock,
  onOpenBoard,
  onEditPane,
  onError,
}: Props) {
  const [pickerOpen, setPickerOpen] = useState(false)
  const [menuOpen, setMenuOpen] = useState(false)

  useEffect(() => {
    setPickerOpen(false)
    setMenuOpen(false)
  }, [pane?.id])

  if (!pane) {
    return (
      <section className="focus-empty" aria-label="空工作区">
        <div className="empty-orbit" aria-hidden="true">
          <span>›_</span>
        </div>
        <h1>从一个 Agent 开始</h1>
        <p>进入 Board 放置 Agent，或从左侧选择一个现有会话。</p>
      </section>
    )
  }

  const state = agentState(pane)

  return (
    <section className="focus-layout" aria-label="Focus 工作区">
      <TerminalPane
        pane={pane}
        state={state}
        pickerOpen={pickerOpen}
        menuOpen={menuOpen}
        availableDockPanes={availableDockPanes}
        onTogglePicker={() => setPickerOpen((open) => !open)}
        onToggleMenu={() => setMenuOpen((open) => !open)}
        onOpenDock={(id) => {
          setPickerOpen(false)
          onOpenDock(id)
        }}
        onOpenBoard={() => onOpenBoard(pane)}
        onEditPane={() => onEditPane(pane)}
        onError={onError}
      />
      {dockPane && (
        <DockPane
          pane={dockPane}
          width={dockWidth}
          onWidthChange={onDockWidthChange}
          onOpenBoard={() => onOpenBoard(dockPane)}
          onEdit={() => onEditPane(dockPane)}
          onClose={onCloseDock}
          onError={onError}
        />
      )}
    </section>
  )
}

type TerminalPaneProps = {
  pane: Pane
  state: ReturnType<typeof agentState>
  pickerOpen: boolean
  menuOpen: boolean
  availableDockPanes: Pane[]
  onTogglePicker(): void
  onToggleMenu(): void
  onOpenDock(id: string): void
  onOpenBoard(): void
  onEditPane(): void
  onError(message: string): void
}

function TerminalPane({
  pane,
  state,
  pickerOpen,
  menuOpen,
  availableDockPanes,
  onTogglePicker,
  onToggleMenu,
  onOpenDock,
  onOpenBoard,
  onEditPane,
  onError,
}: TerminalPaneProps) {
  return (
    <article className="terminal-panel main-terminal">
      <header className="pane-heading">
        <span className={`state-dot ${state}`} role="img" aria-label={stateLabel[state]} />
        <h1>{pane.name}</h1>
        <span className="backend-badge">{backendLabel(pane)}</span>
        <div className="pane-actions">
          {availableDockPanes.length > 0 && (
            <div className="menu-anchor">
              <button
                className={`icon-button ${pickerOpen ? 'active' : ''}`}
                type="button"
                aria-label="打开辅助终端"
                title="打开辅助终端"
                aria-expanded={pickerOpen}
                onClick={onTogglePicker}
              >
                <PanelRightOpen size={16} strokeWidth={1.7} />
              </button>
              {pickerOpen && (
                <div className="icon-popover agent-picker" role="menu">
                  <span className="popover-caption">在右侧查看</span>
                  {availableDockPanes.map((candidate) => (
                    <button
                      key={candidate.id}
                      type="button"
                      role="menuitem"
                      onClick={() => onOpenDock(candidate.id)}
                    >
                      <span
                        className={`state-dot ${agentState(candidate)}`}
                        aria-hidden="true"
                      />
                      <span>{candidate.name}</span>
                      <span className="backend-badge">{backendLabel(candidate)}</span>
                    </button>
                  ))}
                </div>
              )}
            </div>
          )}
          <div className="menu-anchor">
            <button
              className={`icon-button ${menuOpen ? 'active' : ''}`}
              type="button"
              aria-label="终端操作"
              title="终端操作"
              aria-expanded={menuOpen}
              onClick={onToggleMenu}
            >
              <MoreHorizontal size={17} strokeWidth={1.8} />
            </button>
            {menuOpen && (
              <div className="icon-popover" role="menu">
                <MenuItem label="在 Board 中打开" icon={<Maximize2 size={15} />} onClick={onOpenBoard} />
                <MenuItem label="编辑 Agent" icon={<Settings2 size={15} />} onClick={onEditPane} />
              </div>
            )}
          </div>
        </div>
      </header>
      <div className="terminal-location">
        <Folder size={13} strokeWidth={1.7} />
        <span>{pane.cwd}</span>
      </div>
      <div className="terminal-body">
        <TerminalSurface pane={pane} onError={onError} />
      </div>
    </article>
  )
}

type DockProps = {
  pane: Pane
  width: number
  onWidthChange(width: number): void
  onOpenBoard(): void
  onEdit(): void
  onClose(): void
  onError(message: string): void
}

function DockPane({
  pane,
  width,
  onWidthChange,
  onOpenBoard,
  onEdit,
  onClose,
  onError,
}: DockProps) {
  const [menuOpen, setMenuOpen] = useState(false)
  const startRef = useRef({ x: 0, width })
  const state = agentState(pane)

  const startResize = (event: React.PointerEvent<HTMLDivElement>) => {
    event.preventDefault()
    startRef.current = { x: event.clientX, width }
    const target = event.currentTarget
    target.setPointerCapture(event.pointerId)
    const move = (moveEvent: PointerEvent) => {
      const max = Math.min(720, window.innerWidth * 0.52)
      onWidthChange(Math.max(300, Math.min(max, startRef.current.width + startRef.current.x - moveEvent.clientX)))
    }
    const stop = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', stop)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', stop)
  }

  return (
    <aside className="dock-pane" style={{ width }} aria-label="辅助终端">
      <div
        className="dock-resizer"
        role="separator"
        aria-orientation="vertical"
        aria-label="调整辅助终端宽度"
        aria-valuemin={300}
        aria-valuemax={720}
        aria-valuenow={width}
        onPointerDown={startResize}
      />
      <header className="pane-heading dock-heading">
        <span className={`state-dot ${state}`} role="img" aria-label={stateLabel[state]} />
        <h2>{pane.name}</h2>
        <span className="backend-badge">{backendLabel(pane)}</span>
        <div className="pane-actions">
          <button
            className="icon-button"
            type="button"
            aria-label="在 Board 中展开"
            title="在 Board 中展开"
            onClick={onOpenBoard}
          >
            <Maximize2 size={15} strokeWidth={1.7} />
          </button>
          <div className="menu-anchor">
            <button
              className={`icon-button ${menuOpen ? 'active' : ''}`}
              type="button"
              aria-label="辅助终端操作"
              title="辅助终端操作"
              onClick={() => setMenuOpen((open) => !open)}
            >
              <MoreHorizontal size={17} strokeWidth={1.8} />
            </button>
            {menuOpen && (
              <div className="icon-popover" role="menu">
                <MenuItem label="编辑 Agent" icon={<Settings2 size={15} />} onClick={onEdit} />
              </div>
            )}
          </div>
          <button
            className="icon-button"
            type="button"
            aria-label="关闭辅助终端"
            title="关闭"
            onClick={onClose}
          >
            <X size={16} strokeWidth={1.8} />
          </button>
        </div>
      </header>
      <div className="terminal-location">
        <Folder size={13} strokeWidth={1.7} />
        <span>{pane.cwd}</span>
      </div>
      <div className="terminal-body">
        <TerminalSurface pane={pane} onError={onError} autoFocus={false} />
      </div>
    </aside>
  )
}

function MenuItem({
  label,
  icon,
  onClick,
}: {
  label: string
  icon: React.ReactNode
  onClick(): void
}) {
  return (
    <button type="button" role="menuitem" onClick={onClick}>
      {icon}
      <span>{label}</span>
    </button>
  )
}
