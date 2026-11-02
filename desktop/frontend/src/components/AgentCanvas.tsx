import { useEffect, useMemo, useRef } from 'react'
import {
  Background,
  BackgroundVariant,
  Controls,
  Handle,
  MarkerType,
  MiniMap,
  Panel,
  Position,
  ReactFlow,
  useReactFlow,
  useNodesState,
  type Edge,
  type Node,
  type NodeProps,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'

import type { GraphEdge, GraphNode, Snapshot } from '../types'

type AgentNodeData = {
  agent: GraphNode
  selected: boolean
} & Record<string, unknown>

type AgentFlowNode = Node<AgentNodeData, 'agent'>
const EMPTY_GRAPH: { nodes: GraphNode[]; edges: GraphEdge[] } = { nodes: [], edges: [] }

type Props = {
  snapshot: Snapshot
  selectedId?: string
  onSelect(id: string): void
}

const nodeTypes = { agent: AgentNode }

export function AgentCanvas({ snapshot, selectedId, onSelect }: Props) {
  const graph = useMemo(() => snapshot.graph ?? EMPTY_GRAPH, [snapshot.graph])
  const [nodes, setNodes, onNodesChange] = useNodesState<AgentFlowNode>([])

  useEffect(() => {
    setNodes((current) => {
      const positions = new Map(current.map((node) => [node.id, node.position]))
      return graph.nodes.map((agent, index) => ({
        id: agent.id,
        type: 'agent',
        position: positions.get(agent.id) ?? layoutPosition(index, graph.nodes.length),
        data: { agent, selected: agent.id === selectedId },
        selected: agent.id === selectedId,
      }))
    })
  }, [graph.nodes, selectedId, setNodes])

  const edges = useMemo<Edge[]>(
    () =>
      graph.edges.map((edge) => ({
        id: `${edge.from}-${edge.to}`,
        source: edge.from,
        target: edge.to,
        type: 'smoothstep',
        animated: edge.active > 0,
        markerEnd: { type: MarkerType.ArrowClosed, width: 18, height: 18 },
        style: {
          stroke: edge.kind === 'virtual' ? '#6c7a89' : '#5ee1b5',
          strokeWidth: edge.active > 0 ? 2 : 1.3,
          strokeDasharray: edge.kind === 'virtual' ? '5 7' : undefined,
          opacity: edge.active > 0 ? 0.9 : 0.52,
        },
      })),
    [graph.edges],
  )

  return (
    <section className="canvas-card">
      <header className="panel-heading canvas-heading">
        <div>
          <div className="eyebrow">LIVE CANVAS</div>
          <h2>协作关系</h2>
        </div>
        <div className="canvas-stats">
          <span>{graph.nodes.length} 节点</span>
          <span>{graph.edges.length} 连线</span>
        </div>
      </header>
      <div className="flow-shell">
        <ReactFlow
          nodes={nodes}
          edges={edges}
          nodeTypes={nodeTypes}
          onNodesChange={onNodesChange}
          onNodeClick={(_, node) => onSelect(node.id)}
          fitView
          fitViewOptions={{ padding: 0.34, duration: 520 }}
          minZoom={0.35}
          maxZoom={1.8}
          panOnScroll
          selectionOnDrag={false}
          proOptions={{ hideAttribution: true }}
        >
          <FitFirstGraph nodeCount={graph.nodes.length} />
          <Background
            color="#263241"
            gap={24}
            size={1.2}
            variant={BackgroundVariant.Dots}
          />
          <Panel position="top-left" className="canvas-hint">
            滚动缩放 · 拖动平移 · 点击节点切换终端
          </Panel>
          <MiniMap
            className="canvas-minimap"
            nodeColor={(node) => (node.selected ? '#8df0c8' : '#263342')}
            maskColor="rgba(5, 8, 13, 0.72)"
            pannable
            zoomable
          />
          <Controls showInteractive={false} />
        </ReactFlow>
        {graph.nodes.length === 0 && (
          <div className="canvas-empty">
            <div className="canvas-rings">
              <i />
              <i />
              <i />
            </div>
            <strong>关系画布等待第一个 Agent</strong>
            <p>创建会话后，节点和授权连线会出现在这里。</p>
          </div>
        )}
      </div>
    </section>
  )
}

function FitFirstGraph({ nodeCount }: { nodeCount: number }) {
  const { fitView } = useReactFlow()
  const fitted = useRef(false)
  useEffect(() => {
    if (fitted.current || nodeCount === 0) return
    fitted.current = true
    requestAnimationFrame(() => {
      void fitView({ padding: 0.34, duration: 520 })
    })
  }, [fitView, nodeCount])
  return null
}

function AgentNode({ data }: NodeProps<AgentFlowNode>) {
  const agent = data.agent
  return (
    <div className={`agent-node ${data.selected ? 'selected' : ''}`}>
      <Handle type="target" position={Position.Left} className="agent-handle" />
      <div className="agent-node-top">
        <span className={`pane-state ${agent.state}`} />
        <span className="agent-preset">{agent.preset || agent.backend}</span>
        {agent.unread > 0 && <b className="unread-count">{agent.unread}</b>}
      </div>
      <strong>{agent.name}</strong>
      <div className="agent-node-bottom">
        <span>{agent.state}</span>
        <span>{agent.layer || 'workspace'}</span>
      </div>
      <Handle type="source" position={Position.Right} className="agent-handle" />
    </div>
  )
}

function layoutPosition(index: number, count: number) {
  if (count <= 1) return { x: 360, y: 220 }
  const columns = Math.ceil(Math.sqrt(count))
  const rows = Math.ceil(count / columns)
  const column = index % columns
  const row = Math.floor(index / columns)
  return {
    x: 92 + column * 238 + (rows > 2 ? (row % 2) * 34 : 0),
    y: 82 + row * 156,
  }
}
