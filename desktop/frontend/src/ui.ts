import type { Pane } from './types'

export type AgentState = 'working' | 'waiting' | 'error' | 'idle'

export const stateRank: Record<AgentState, number> = {
  error: 4,
  waiting: 3,
  working: 2,
  idle: 1,
}

export const stateLabel: Record<AgentState, string> = {
  working: '正在工作',
  waiting: '等待人类处理',
  error: '异常',
  idle: '空闲',
}

export function agentState(pane: Pane): AgentState {
  if (pane.state === 'failed' || pane.state === 'interrupted') return 'error'
  if (
    pane.readiness === 'blocked' ||
    pane.turn_state === 'waiting-permission' ||
    pane.turn_state === 'waiting'
  ) {
    return 'waiting'
  }
  if (
    pane.turn_state === 'running' ||
    pane.readiness === 'busy' ||
    pane.state === 'starting' ||
    pane.state === 'stopping'
  ) {
    return 'working'
  }
  return 'idle'
}

export function aggregateState(panes: Pane[]): AgentState {
  return panes.reduce<AgentState>(
    (result, pane) => {
      const next = agentState(pane)
      return stateRank[next] > stateRank[result] ? next : result
    },
    'idle',
  )
}

export function backendLabel(pane: Pane) {
  const preset = (pane.preset ?? '').toLowerCase()
  if (preset.includes('codex')) return 'Codex'
  if (preset.includes('claude')) return 'Claude'
  if (preset.includes('agy')) return 'Agy'
  if (pane.backend === 'pty' && (pane.command.at(-1) ?? '').match(/zsh|bash|fish|sh$/u)) {
    return 'Shell'
  }
  return pane.backend.toUpperCase()
}

export function baseName(path: string) {
  const clean = path.replace(/\/+$/u, '')
  return clean.slice(clean.lastIndexOf('/') + 1) || clean
}

export function cleanOutput(value?: string) {
  return value?.replace(/\s+/gu, ' ').trim() ?? ''
}

export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error)
}
