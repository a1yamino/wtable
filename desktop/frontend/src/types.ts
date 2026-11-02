export type Pane = {
  id: string
  name: string
  kind?: 'foreground' | 'worker' | ''
  backend: string
  command: string[]
  cwd: string
  workspace?: string
  state: string
  turn_state?: string
  preset?: string
  role?: string
  unread?: number
  last_output?: string
  last_activity?: string
  readiness?: string
}

export type GraphNode = {
  id: string
  name: string
  preset: string
  backend: string
  layer: string
  state: string
  attention?: string[]
  unread: number
}

export type GraphEdge = {
  from: string
  to: string
  kind: 'grant' | 'virtual'
  active: number
}

export type WorkspaceInfo = {
  path: string
  last_opened: string
  last_focus?: string
  pane_count: number
  running_count: number
  missing?: boolean
}

export type Snapshot = {
  panes: Pane[]
  graph?: {
    nodes: GraphNode[]
    edges: GraphEdge[]
  }
  workspaces?: WorkspaceInfo[]
  workspace?: string
  last_focus?: string
  revision: number
}

export type Bootstrap = {
  workspace: string
  snapshot: Snapshot
  home: string
}

export type TerminalChunk = {
  data: string
  cursor: number
}
