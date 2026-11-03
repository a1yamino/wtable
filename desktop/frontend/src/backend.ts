import type { Bootstrap, Pane, Snapshot, TerminalAttach, TerminalChunk } from './types'

type WailsApp = {
  Bootstrap(): Promise<Bootstrap>
  GetSnapshot(workspace: string): Promise<Snapshot>
  OpenWorkspace(path: string): Promise<Snapshot>
  FocusPane(id: string): Promise<void>
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
  openWorkspace: async (path: string) =>
    normalizeSnapshot(await bridge().OpenWorkspace(path)),
  focusPane: async (id: string) => bridge().FocusPane(id),
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
    workspaces: snapshot.workspaces ?? [],
    graph: snapshot.graph
      ? {
          nodes: snapshot.graph.nodes ?? [],
          edges: snapshot.graph.edges ?? [],
        }
      : undefined,
  }
}
