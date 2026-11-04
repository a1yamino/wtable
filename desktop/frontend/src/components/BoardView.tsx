import { useEffect, useMemo, useRef, useState } from 'react'
import {
  ArrowLeft,
  ArrowRight,
  Focus,
  Grip,
  LayoutTemplate,
  Link2,
  Map as MapIcon,
  Maximize2,
  Minimize2,
  Minus,
  MoreHorizontal,
  Plus,
  Scan,
  Settings2,
  Trash2,
  X,
} from 'lucide-react'

import type { Edge, Pane } from '../types'
import { agentState, backendLabel, stateLabel } from '../ui'
import { TerminalSurface } from './TerminalSurface'

export type NodeLayout = {
  x: number
  y: number
  width: number
  height: number
  expanded: boolean
}

export type BoardViewport = {
  x: number
  y: number
  zoom: number
}

type Props = {
  workspaceName: string
  panes: Pane[]
  edges: Edge[]
  mainId: string
  selectedId: string
  layouts: Record<string, NodeLayout>
  viewport: BoardViewport
  minimap: boolean
  onSelect(pane: Pane): void
  onFocus(pane: Pane): void
  onEdit(pane: Pane): void
  onDelete(pane: Pane): void
  onAddAgent(x: number, y: number): void
  onOpenTemplates(): void
  onLayoutChange(id: string, layout: NodeLayout): void
  onViewportChange(viewport: BoardViewport): void
  onMinimapChange(value: boolean): void
  onConnect(from: string, to: string): void
  onAddEdge(from: string, to: string): void
  onRemoveEdge(from: string, to: string): void
  onError(message: string): void
  previewLabel?: string
}

type Pair = {
  a: string
  b: string
  ab: boolean
  ba: boolean
}

const COLLAPSED_WIDTH = 248
const COLLAPSED_HEIGHT = 58
const COLLISION_GAP = 20

export function BoardView({
  workspaceName,
  panes,
  edges,
  mainId,
  selectedId,
  layouts,
  viewport,
  minimap,
  onSelect,
  onFocus,
  onEdit,
  onDelete,
  onAddAgent,
  onOpenTemplates,
  onLayoutChange,
  onViewportChange,
  onMinimapChange,
  onConnect,
  onAddEdge,
  onRemoveEdge,
  onError,
  previewLabel,
}: Props) {
  const preview = Boolean(previewLabel)
  const boardRef = useRef<HTMLDivElement>(null)
  const pointerRef = useRef<{ x: number; y: number } | null>(null)
  const [placing, setPlacing] = useState(false)
  const [ghost, setGhost] = useState<{ x: number; y: number; valid: boolean } | null>(null)
  const [connecting, setConnecting] = useState('')
  const [selectedEdgeKey, setSelectedEdgeKey] = useState('')
  const [menuId, setMenuId] = useState('')
  const [drag, setDrag] = useState<
    | {
        type: 'pan' | 'node' | 'resize'
        id?: string
        startX: number
        startY: number
        x: number
        y: number
        width: number
        height: number
        moved: boolean
      }
    | undefined
  >()

  const pairs = useMemo(() => pairEdges(edges), [edges])
  const paneById = useMemo(
    () => new Map(panes.map((pane) => [pane.id, pane])),
    [panes],
  )
  const selectedPair = pairs.find((pair) => pairKey(pair.a, pair.b) === selectedEdgeKey)

  useEffect(() => {
    if (menuId && !paneById.has(menuId)) setMenuId('')
    if (connecting && !paneById.has(connecting)) setConnecting('')
    if (selectedEdgeKey && !selectedPair) setSelectedEdgeKey('')
  }, [connecting, menuId, paneById, selectedEdgeKey, selectedPair])

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      setMenuId('')
      setPlacing(false)
      setConnecting('')
      setSelectedEdgeKey('')
      setGhost(null)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [])

  const sizeOf = (paneId: string) => {
    const layout = layouts[paneId]
    return layout?.expanded
      ? { width: layout.width, height: layout.height }
      : { width: COLLAPSED_WIDTH, height: COLLAPSED_HEIGHT }
  }

  const toWorld = (clientX: number, clientY: number) => {
    const bounds = boardRef.current?.getBoundingClientRect()
    if (!bounds) return { x: 0, y: 0 }
    return {
      x: (clientX - bounds.left - viewport.x) / viewport.zoom,
      y: (clientY - bounds.top - viewport.y) / viewport.zoom,
    }
  }

  const collision = (paneId: string, candidate: NodeLayout) => {
    const candidateSize = candidate.expanded
      ? { width: candidate.width, height: candidate.height }
      : { width: COLLAPSED_WIDTH, height: COLLAPSED_HEIGHT }
    return panes.some((pane) => {
      if (pane.id === paneId) return false
      const other = layouts[pane.id] ?? fallbackLayout(pane.id)
      const otherSize = other.expanded
        ? { width: other.width, height: other.height }
        : { width: COLLAPSED_WIDTH, height: COLLAPSED_HEIGHT }
      return (
        candidate.x < other.x + otherSize.width + COLLISION_GAP &&
        candidate.x + candidateSize.width + COLLISION_GAP > other.x &&
        candidate.y < other.y + otherSize.height + COLLISION_GAP &&
        candidate.y + candidateSize.height + COLLISION_GAP > other.y
      )
    })
  }

  const startNodeDrag = (event: React.PointerEvent, pane: Pane) => {
    if (event.button !== 0) return
    event.preventDefault()
    const layout = layouts[pane.id] ?? fallbackLayout(pane.id)
    onSelect(pane)
    setDrag({
      type: 'node',
      id: pane.id,
      startX: event.clientX,
      startY: event.clientY,
      x: layout.x,
      y: layout.y,
      width: layout.width,
      height: layout.height,
      moved: false,
    })
  }

  const startResize = (event: React.PointerEvent, pane: Pane) => {
    event.preventDefault()
    event.stopPropagation()
    const layout = layouts[pane.id] ?? fallbackLayout(pane.id)
    setDrag({
      type: 'resize',
      id: pane.id,
      startX: event.clientX,
      startY: event.clientY,
      x: layout.x,
      y: layout.y,
      width: layout.width,
      height: layout.height,
      moved: false,
    })
  }

  useEffect(() => {
    if (!drag) return
    const move = (event: PointerEvent) => {
      const dx = (event.clientX - drag.startX) / viewport.zoom
      const dy = (event.clientY - drag.startY) / viewport.zoom
      if (Math.hypot(event.clientX - drag.startX, event.clientY - drag.startY) > 3) {
        drag.moved = true
      }
      if (drag.type === 'pan') {
        onViewportChange({
          ...viewport,
          x: drag.x + event.clientX - drag.startX,
          y: drag.y + event.clientY - drag.startY,
        })
        return
      }
      if (!drag.id) return
      const current = layouts[drag.id] ?? fallbackLayout(drag.id)
      const candidate =
        drag.type === 'resize'
          ? {
              ...current,
              width: Math.max(340, Math.min(860, drag.width + dx)),
              height: Math.max(220, Math.min(760, drag.height + dy)),
            }
          : {
              ...current,
              x: Math.max(20, Math.min(2180, drag.x + dx)),
              y: Math.max(110, Math.min(1580, drag.y + dy)),
            }
      if (!collision(drag.id, candidate)) onLayoutChange(drag.id, candidate)
    }
    const stop = () => setDrag(undefined)
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', stop, { once: true })
    window.addEventListener('pointercancel', stop, { once: true })
    return () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', stop)
      window.removeEventListener('pointercancel', stop)
    }
  }, [drag, layouts, onLayoutChange, onViewportChange, panes, viewport])

  useEffect(() => {
    if (!placing && !connecting) {
      setGhost(null)
      pointerRef.current = null
    }
  }, [connecting, placing])

  const handleBoardPointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    if (!event.currentTarget.contains(event.target as Node)) return
    pointerRef.current = toWorld(event.clientX, event.clientY)
    if (placing) {
      const point = toWorld(event.clientX, event.clientY)
      const x = Math.max(20, point.x - COLLAPSED_WIDTH / 2)
      const y = Math.max(110, point.y - COLLAPSED_HEIGHT / 2)
      const candidate: NodeLayout = {
        x,
        y,
        width: 440,
        height: 320,
        expanded: true,
      }
      setGhost({ x, y, valid: !collision('new', candidate) })
    }
  }

  const handleBoardPointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return
    if ((event.target as HTMLElement).closest('.board-node,.floating-tools,.board-controls,.minimap,.edge-actions,.board-context')) {
      return
    }
    setMenuId('')
    setSelectedEdgeKey('')
    if (placing && !preview) {
      const point = toWorld(event.clientX, event.clientY)
      const x = Math.max(20, point.x - COLLAPSED_WIDTH / 2)
      const y = Math.max(110, point.y - COLLAPSED_HEIGHT / 2)
      const candidate: NodeLayout = { x, y, width: 440, height: 320, expanded: true }
      if (collision('new', candidate)) {
        onError('这个位置会与现有节点重叠，请换一个位置。')
        return
      }
      setPlacing(false)
      setGhost(null)
      onAddAgent(x, y)
      return
    }
    if (connecting && !preview) {
      setConnecting('')
      return
    }
    setDrag({
      type: 'pan',
      startX: event.clientX,
      startY: event.clientY,
      x: viewport.x,
      y: viewport.y,
      width: 0,
      height: 0,
      moved: false,
    })
  }

  const handleWheel = (event: React.WheelEvent<HTMLDivElement>) => {
    if (!(event.altKey || event.ctrlKey || event.metaKey)) return
    event.preventDefault()
    const bounds = boardRef.current?.getBoundingClientRect()
    if (!bounds) return
    const px = event.clientX - bounds.left
    const py = event.clientY - bounds.top
    const zoom = Math.max(0.25, Math.min(1.7, viewport.zoom * Math.exp(-event.deltaY * 0.002)))
    const ratio = zoom / viewport.zoom
    onViewportChange({
      zoom,
      x: px - (px - viewport.x) * ratio,
      y: py - (py - viewport.y) * ratio,
    })
  }

  const zoomAtCenter = (factor: number) => {
    const bounds = boardRef.current?.getBoundingClientRect()
    if (!bounds) return
    const zoom = Math.max(0.25, Math.min(1.7, viewport.zoom * factor))
    const px = bounds.width / 2
    const py = bounds.height / 2
    const ratio = zoom / viewport.zoom
    onViewportChange({
      zoom,
      x: px - (px - viewport.x) * ratio,
      y: py - (py - viewport.y) * ratio,
    })
  }

  const fit = () => {
    const bounds = boardRef.current?.getBoundingClientRect()
    if (!bounds || panes.length === 0) return
    const graph = graphBounds(panes, layouts)
    const zoom = Math.max(
      0.25,
      Math.min(1, (bounds.width - 100) / graph.width, (bounds.height - 180) / graph.height),
    )
    onViewportChange({
      zoom,
      x: (bounds.width - graph.width * zoom) / 2 - graph.x * zoom,
      y: 92 - graph.y * zoom,
    })
  }

  const selectedPane = selectedId ? paneById.get(selectedId) : undefined
  const pointer = pointerRef.current
  const sourceLayout = connecting ? layouts[connecting] ?? fallbackLayout(connecting) : undefined
  const pairForSelection = selectedPair

  return (
    <section
      ref={boardRef}
      className={`board-view ${placing ? 'placing' : ''} ${preview ? 'previewing' : ''}`}
      aria-label="Agent 编排画板"
      onPointerMove={handleBoardPointerMove}
      onPointerDown={handleBoardPointerDown}
      onWheel={handleWheel}
    >
      <header className="board-context" title="拖动窗口">
        <div>
          <h1>{workspaceName}</h1>
          <p>
            {panes.length} 个 Agent
            <span aria-hidden="true">·</span>
            {pairs.length} 条授权连接
          </p>
        </div>
        <span className="board-drag-hint" role="img" aria-label="拖动窗口">
          <Grip size={15} />
        </span>
      </header>

      {preview && (
        <div className="board-preview-banner">
          <LayoutTemplate size={16} />
          <span>
            <strong>{previewLabel}</strong>
            预览 · 尚未应用
          </span>
        </div>
      )}

      {connecting && sourceLayout && pointer && (
        <svg className="connection-preview" aria-hidden="true">
          <path
            d={linkPath(
              {
                x: sourceLayout.x,
                y: sourceLayout.y,
                width: COLLAPSED_WIDTH,
                height: COLLAPSED_HEIGHT,
              },
              { x: pointer.x, y: pointer.y },
            )}
          />
        </svg>
      )}

      <div
        className="board-world"
        style={{
          transform: `translate(${viewport.x}px, ${viewport.y}px) scale(${viewport.zoom})`,
        }}
      >
        <svg className="board-edges" viewBox="0 0 2400 1800" aria-label="授权连接">
          {pairs.map((pair) => {
            const from = paneById.get(pair.a)
            const to = paneById.get(pair.b)
            if (!from || !to) return null
            const fromLayout = layouts[from.id] ?? fallbackLayout(from.id)
            const toLayout = layouts[to.id] ?? fallbackLayout(to.id)
            const fromSize = sizeOf(from.id)
            const toSize = sizeOf(to.id)
            const path = linkPath(
              { x: fromLayout.x, y: fromLayout.y, ...fromSize },
              { x: toLayout.x, y: toLayout.y, ...toSize },
            )
            const key = pairKey(pair.a, pair.b)
            return (
              <g key={key}>
                <path
                  className="edge-hit"
                  d={path}
                  onClick={(event) => {
                    event.stopPropagation()
                    setSelectedEdgeKey(key)
                    setMenuId('')
                  }}
                />
                <path
                  className={`edge ${selectedEdgeKey === key ? 'selected' : ''}`}
                  d={path}
                />
              </g>
            )
          })}
        </svg>

        {panes.map((pane) => {
          const layout = layouts[pane.id] ?? fallbackLayout(pane.id)
          const state = agentState(pane)
          const size = sizeOf(pane.id)
          const isMain = pane.id === mainId
          return (
            <article
              key={pane.id}
              className={[
                'board-node',
                isMain ? 'main-agent' : '',
                selectedId === pane.id ? 'selected' : '',
                connecting === pane.id ? 'connecting' : '',
                connecting && connecting !== pane.id ? 'connect-target' : '',
                layout.expanded ? 'expanded' : '',
              ]
                .filter(Boolean)
                .join(' ')}
              style={{
                left: layout.x,
                top: layout.y,
                width: size.width,
                height: size.height,
              }}
              tabIndex={preview ? -1 : 0}
              aria-label={`${pane.name}，${backendLabel(pane)}，${stateLabel[state]}`}
              onKeyDown={(event) => {
                if (preview || event.target !== event.currentTarget) return
                if (event.key === 'Enter' || event.key === ' ') {
                  event.preventDefault()
                  if (connecting && connecting !== pane.id) {
                    onConnect(connecting, pane.id)
                    setConnecting('')
                    return
                  }
                  onSelect(pane)
                }
              }}
              onPointerDown={(event) => {
                event.stopPropagation()
                if (preview) return
                if ((event.target as HTMLElement).closest('button,.terminal-surface,.node-resizer')) return
                if (connecting && connecting !== pane.id) {
                  onConnect(connecting, pane.id)
                  setConnecting('')
                  return
                }
                startNodeDrag(event, pane)
              }}
            >
              <header className="node-header">
                <span className={`state-dot ${state}`} role="img" aria-label={stateLabel[state]} />
                <span className="node-name">{pane.name}</span>
                <span className="backend-badge">{backendLabel(pane)}</span>
                <div className="node-actions">
                  {!preview && (
                    <>
                  <button
                    className="icon-button"
                    type="button"
                    aria-label={`在 Focus 查看 ${pane.name}`}
                    title="回到 Focus"
                    onClick={() => onFocus(pane)}
                  >
                    <Focus size={15} strokeWidth={1.7} />
                  </button>
                  <button
                    className={`icon-button ${menuId === pane.id ? 'active' : ''}`}
                    type="button"
                    aria-label={`${pane.name} 节点操作`}
                    title="节点操作"
                    onClick={() => {
                      onSelect(pane)
                      setMenuId((open) => (open === pane.id ? '' : pane.id))
                    }}
                  >
                    <MoreHorizontal size={16} strokeWidth={1.8} />
                  </button>
                    </>
                  )}
                </div>
              </header>

              {!preview && menuId === pane.id && (
                <div className="node-action-popover" role="menu" onPointerDown={(event) => event.stopPropagation()}>
                  <button
                    type="button"
                    role="menuitem"
                    title={layout.expanded ? '收起终端' : '展开终端'}
                    aria-label={layout.expanded ? '收起终端' : '展开终端'}
                    onClick={() => {
                      const candidate = {
                        ...layout,
                        expanded: !layout.expanded,
                        width: Math.max(layout.width, 440),
                        height: Math.max(layout.height, 320),
                      }
                      if (candidate.expanded && collision(pane.id, candidate)) {
                        onError('空间不足，移动节点后再展开终端。')
                        return
                      }
                      onLayoutChange(pane.id, candidate)
                      setMenuId('')
                    }}
                  >
                    {layout.expanded ? <Minimize2 size={16} /> : <Maximize2 size={16} />}
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    title="开始连线"
                    aria-label="开始连线"
                    onClick={() => {
                      setConnecting(pane.id)
                      setMenuId('')
                      setSelectedEdgeKey('')
                    }}
                  >
                    <Link2 size={16} />
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    title="编辑设置"
                    aria-label="编辑设置"
                    onClick={() => {
                      onEdit(pane)
                      setMenuId('')
                    }}
                  >
                    <Settings2 size={16} />
                  </button>
                  <button
                    type="button"
                    role="menuitem"
                    className="danger"
                    title="删除"
                    aria-label="删除"
                    onClick={() => {
                      onDelete(pane)
                      setMenuId('')
                    }}
                  >
                    <Trash2 size={16} />
                  </button>
                </div>
              )}

              {!preview && layout.expanded && (
                <>
                  <div className="node-terminal">
                    <TerminalSurface pane={pane} onError={onError} autoFocus={false} />
                  </div>
                  <button
                    className="node-resizer"
                    type="button"
                    aria-label={`调整 ${pane.name} 终端尺寸`}
                    title="调整尺寸"
                    onPointerDown={(event) => startResize(event, pane)}
                  >
                    <Grip size={14} />
                  </button>
                </>
              )}
            </article>
          )
        })}

        {ghost && placing && (
          <div
            className={`placement-ghost ${ghost.valid ? '' : 'invalid'}`}
            style={{
              left: ghost.x,
              top: ghost.y,
              width: COLLAPSED_WIDTH,
              height: COLLAPSED_HEIGHT,
            }}
          >
            <Plus size={16} />
            <span>新 Agent</span>
          </div>
        )}
      </div>

      {!preview && connecting && (
        <div className="connection-banner">
          <span>从 {paneById.get(connecting)?.name} 连接到一个目标</span>
          <button type="button" onClick={() => setConnecting('')}>
            取消
          </button>
        </div>
      )}

      {!preview && pairForSelection && (
        <div className="edge-actions">
          <span>
            {paneById.get(pairForSelection.a)?.name} <span>—</span>{' '}
            {paneById.get(pairForSelection.b)?.name}
          </span>
          <button
            className={pairForSelection.ab ? 'active' : ''}
            type="button"
            aria-label="切换正向授权"
            title="切换正向授权"
            onClick={() => {
              if (pairForSelection.ab) onRemoveEdge(pairForSelection.a, pairForSelection.b)
              else onAddEdge(pairForSelection.a, pairForSelection.b)
            }}
          >
            <ArrowRight size={16} />
          </button>
          <button
            className={pairForSelection.ba ? 'active' : ''}
            type="button"
            aria-label="切换反向授权"
            title="切换反向授权"
            onClick={() => {
              if (pairForSelection.ba) onRemoveEdge(pairForSelection.b, pairForSelection.a)
              else onAddEdge(pairForSelection.b, pairForSelection.a)
            }}
          >
            <ArrowLeft size={16} />
          </button>
          <button
            type="button"
            aria-label="删除授权关系"
            title="删除授权关系"
            onClick={() => {
              if (pairForSelection.ab) onRemoveEdge(pairForSelection.a, pairForSelection.b)
              if (pairForSelection.ba) onRemoveEdge(pairForSelection.b, pairForSelection.a)
              setSelectedEdgeKey('')
            }}
          >
            <Trash2 size={16} />
          </button>
          <button
            type="button"
            aria-label="关闭连接操作"
            title="关闭"
            onClick={() => setSelectedEdgeKey('')}
          >
            <X size={16} />
          </button>
        </div>
      )}

      {!preview && !placing && !connecting && (
        <div className="floating-tools" aria-label="画板工具">
          <button
            className="icon-button"
            type="button"
            aria-label="新增 Agent"
            title="新增 Agent"
            onClick={() => setPlacing(true)}
          >
            <Plus size={17} strokeWidth={1.8} />
          </button>
          <button
            className="icon-button"
            type="button"
            aria-label="拓扑模板"
            title="拓扑模板"
            onClick={onOpenTemplates}
          >
            <LayoutTemplate size={16} strokeWidth={1.7} />
          </button>
        </div>
      )}

      <div className="board-controls">
        <div className="zoom-controls">
          <button
            className="icon-button"
            type="button"
            aria-label="缩小"
            title="缩小"
            onClick={() => zoomAtCenter(1 / 1.15)}
          >
            <Minus size={15} />
          </button>
          <button
            className="icon-button"
            type="button"
            aria-label="适应全部节点"
            title="适应全部节点"
            onClick={fit}
          >
            <Scan size={15} />
          </button>
          <span>{Math.round(viewport.zoom * 100)}%</span>
          <button
            className="icon-button"
            type="button"
            aria-label="放大"
            title="放大"
            onClick={() => zoomAtCenter(1.15)}
          >
            <Plus size={15} />
          </button>
          <i className="toolbar-divider" />
          <button
            className={`icon-button ${minimap ? 'active' : ''}`}
            type="button"
            aria-label={minimap ? '隐藏 MiniMap' : '显示 MiniMap'}
            title={minimap ? '隐藏 MiniMap' : '显示 MiniMap'}
            aria-pressed={minimap}
            onClick={() => onMinimapChange(!minimap)}
          >
            <MapIcon size={15} />
          </button>
        </div>
      </div>

      {!preview && (
        <div className="board-hint">
          {placing ? '点击空白处放置 Agent · Esc 取消' : '拖动画布平移 · Alt + 滚轮缩放'}
        </div>
      )}

      {minimap && (
        <button
          className="minimap"
          type="button"
          aria-label="画布缩略图"
          onClick={(event) => {
            const bounds = event.currentTarget.getBoundingClientRect()
            const graph = graphBounds(panes, layouts)
            const scale = Math.min(bounds.width / graph.width, bounds.height / graph.height)
            const padX = (bounds.width - graph.width * scale) / 2
            const padY = (bounds.height - graph.height * scale) / 2
            const x = graph.x + (event.clientX - bounds.left - padX) / scale
            const y = graph.y + (event.clientY - bounds.top - padY) / scale
            const board = boardRef.current?.getBoundingClientRect()
            if (!board) return
            onViewportChange({
              ...viewport,
              x: board.width / 2 - x * viewport.zoom,
              y: board.height / 2 - y * viewport.zoom,
            })
          }}
        >
          <MiniMap panes={panes} pairs={pairs} layouts={layouts} selectedId={selectedId} />
        </button>
      )}

      {!preview && panes.length === 0 && (
        <div className="board-empty">
          <span className="empty-orbit" aria-hidden="true">
            <span>+</span>
          </span>
          <h2>从一个 Agent 开始</h2>
          <p>放置第一个 Agent，或从全局模板预览一个协作拓扑。</p>
          <div>
            <button className="primary-button" type="button" onClick={() => setPlacing(true)}>
              <Plus size={16} />
              放置 Agent
            </button>
            <button className="secondary-button" type="button" onClick={onOpenTemplates}>
              <LayoutTemplate size={16} />
              拓扑模板
            </button>
          </div>
        </div>
      )}

      {selectedPane && menuId && <button className="board-dismiss" type="button" aria-label="关闭菜单" onClick={() => setMenuId('')} />}
    </section>
  )
}

function pairEdges(edges: Edge[]): Pair[] {
  const map = new Map<string, Pair>()
  for (const edge of edges) {
    const a = edge.from
    const b = edge.to
    if (!a || !b || a === b) continue
    const key = pairKey(a, b)
    const current =
      map.get(key) ??
      {
        a: a.localeCompare(b) <= 0 ? a : b,
        b: a.localeCompare(b) <= 0 ? b : a,
        ab: false,
        ba: false,
      }
    if (edge.from === current.a && edge.to === current.b) current.ab = true
    else current.ba = true
    map.set(key, current)
  }
  return [...map.values()]
}

function pairKey(a: string, b: string) {
  return [a, b].sort().join(':')
}

function fallbackLayout(id: string): NodeLayout {
  let hash = 0
  for (const character of id) hash = (hash * 31 + character.charCodeAt(0)) >>> 0
  const index = hash % 12
  return {
    x: 110 + (index % 4) * 330,
    y: 180 + Math.floor(index / 4) * 210,
    width: 440,
    height: 320,
    expanded: false,
  }
}

function graphBounds(panes: Pane[], layouts: Record<string, NodeLayout>) {
  if (panes.length === 0) return { x: 0, y: 0, width: 900, height: 600 }
  const sizes = panes.map((pane) => {
    const layout = layouts[pane.id] ?? fallbackLayout(pane.id)
    return {
      x: layout.x,
      y: layout.y,
      width: layout.expanded ? layout.width : COLLAPSED_WIDTH,
      height: layout.expanded ? layout.height : COLLAPSED_HEIGHT,
    }
  })
  const x = Math.min(...sizes.map((item) => item.x)) - 70
  const y = Math.min(...sizes.map((item) => item.y)) - 70
  const right = Math.max(...sizes.map((item) => item.x + item.width)) + 70
  const bottom = Math.max(...sizes.map((item) => item.y + item.height)) + 70
  return { x, y, width: right - x, height: bottom - y }
}

function linkPath(
  from: { x: number; y: number; width: number; height: number },
  to: { x: number; y: number; width?: number; height?: number },
) {
  const targetWidth = to.width ?? 0
  const targetHeight = to.height ?? 0
  const right = to.x + targetWidth / 2 >= from.x + from.width / 2
  const x1 = right ? from.x + from.width : from.x
  const x2 = right ? to.x : to.x + targetWidth
  const y1 = from.y + Math.min(from.height / 2, 29)
  const y2 = to.y + Math.min(targetHeight / 2, 29)
  const bend = Math.max(54, Math.abs(x2 - x1) * 0.45)
  return `M${x1} ${y1} C${x1 + (right ? bend : -bend)} ${y1} ${x2 + (right ? -bend : bend)} ${y2} ${x2} ${y2}`
}

function MiniMap({
  panes,
  pairs,
  layouts,
  selectedId,
}: {
  panes: Pane[]
  pairs: Pair[]
  layouts: Record<string, NodeLayout>
  selectedId: string
}) {
  const graph = graphBounds(panes, layouts)
  return (
    <svg viewBox={`${graph.x} ${graph.y} ${graph.width} ${graph.height}`} aria-hidden="true">
      {pairs.map((pair) => {
        const from = panes.find((pane) => pane.id === pair.a)
        const to = panes.find((pane) => pane.id === pair.b)
        if (!from || !to) return null
        const fromLayout = layouts[from.id] ?? fallbackLayout(from.id)
        const toLayout = layouts[to.id] ?? fallbackLayout(to.id)
        return (
          <path
            key={pairKey(pair.a, pair.b)}
            d={linkPath(
              {
                x: fromLayout.x,
                y: fromLayout.y,
                width: fromLayout.expanded ? fromLayout.width : COLLAPSED_WIDTH,
                height: fromLayout.expanded ? fromLayout.height : COLLAPSED_HEIGHT,
              },
              {
                x: toLayout.x,
                y: toLayout.y,
                width: toLayout.expanded ? toLayout.width : COLLAPSED_WIDTH,
                height: toLayout.expanded ? toLayout.height : COLLAPSED_HEIGHT,
              },
            )}
          />
        )
      })}
      {panes.map((pane) => {
        const layout = layouts[pane.id] ?? fallbackLayout(pane.id)
        return (
          <rect
            key={pane.id}
            x={layout.x}
            y={layout.y}
            width={layout.expanded ? layout.width : COLLAPSED_WIDTH}
            height={layout.expanded ? layout.height : COLLAPSED_HEIGHT}
            rx={10}
            className={pane.id === selectedId ? 'selected' : ''}
          />
        )
      })}
    </svg>
  )
}
