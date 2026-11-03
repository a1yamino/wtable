import type {
  Bootstrap,
  Catalog,
  Edge,
  Pane,
  Snapshot,
  SpawnPaneRequest,
  TerminalAttach,
  TerminalChunk,
  UpdatePaneRequest,
} from './types'

type WailsApp = {
  Bootstrap(): Promise<Bootstrap>
  GetSnapshot(workspace: string): Promise<Snapshot>
  GetAllSnapshot(): Promise<Snapshot>
  GetCatalog(): Promise<Catalog>
  OpenWorkspace(path: string): Promise<Snapshot>
  CreateWorkspace(path: string): Promise<Snapshot>
  FocusPane(id: string): Promise<void>
  SpawnPane(args: SpawnPaneRequest): Promise<Pane>
  UpdatePane(args: UpdatePaneRequest): Promise<Pane>
  RemovePane(id: string): Promise<void>
  AddEdge(from: string, to: string): Promise<Edge>
  RemoveEdge(from: string, to: string): Promise<Edge>
  CreateShell(name: string): Promise<Pane>
  SendInput(id: string, data: string): Promise<void>
  ResizeTerminal(id: string, cols: number, rows: number): Promise<void>
  AttachTerminal(id: string): Promise<TerminalAttach>
  ReadTerminal(id: string, cursor: number): Promise<TerminalChunk>
}

declare global {
  interface Window {
    go?: {
      main?: {
        App?: WailsApp
      }
    }
  }
}

function bridge() {
  const app = window.go?.main?.App
  if (!app) {
    throw new Error('Wails 后端尚未就绪')
  }
  return app
}

export const backend = {
  bootstrap: async () => {
    const result = await bridge().Bootstrap()
    return { ...result, snapshot: normalizeSnapshot(result.snapshot) }
  },
  snapshot: async (workspace: string) =>
    normalizeSnapshot(await bridge().GetSnapshot(workspace)),
  allSnapshot: async () => normalizeSnapshot(await bridge().GetAllSnapshot()),
  catalog: async () => {
    const result = await bridge().GetCatalog()
    return {
      ...result,
      presets: result.presets ?? [],
      account_profiles: result.account_profiles ?? [],
      accounts: result.accounts ?? [],
    }
  },
  openWorkspace: async (path: string) =>
    normalizeSnapshot(await bridge().OpenWorkspace(path)),
  createWorkspace: async (path: string) =>
    normalizeSnapshot(await bridge().CreateWorkspace(path)),
  focusPane: async (id: string) => bridge().FocusPane(id),
  spawnPane: async (args: SpawnPaneRequest) => bridge().SpawnPane(args),
  updatePane: async (args: UpdatePaneRequest) => bridge().UpdatePane(args),
  removePane: async (id: string) => bridge().RemovePane(id),
  addEdge: async (from: string, to: string) => bridge().AddEdge(from, to),
  removeEdge: async (from: string, to: string) => bridge().RemoveEdge(from, to),
  createShell: async (name: string) => bridge().CreateShell(name),
  sendInput: async (id: string, data: string) => bridge().SendInput(id, data),
  resizeTerminal: async (id: string, cols: number, rows: number) =>
    bridge().ResizeTerminal(id, cols, rows),
  attachTerminal: async (id: string) => bridge().AttachTerminal(id),
  readTerminal: async (id: string, cursor: number) => bridge().ReadTerminal(id, cursor),
}

export function decodeBase64(value: string) {
  if (!value) return new Uint8Array()
  const binary = atob(value)
  const bytes = new Uint8Array(binary.length)
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i)
  }
  return bytes
}

function normalizeSnapshot(value: Snapshot | null | undefined): Snapshot {
  const snapshot = value ?? { panes: [], revision: 0 }
  return {
    ...snapshot,
    panes: snapshot.panes ?? [],
    edges: snapshot.edges ?? [],
    workspaces: snapshot.workspaces ?? [],
    graph: snapshot.graph
      ? {
          nodes: snapshot.graph.nodes ?? [],
          edges: snapshot.graph.edges ?? [],
        }
      : undefined,
  }
}
