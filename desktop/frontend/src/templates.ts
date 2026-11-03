import type { Edge, Pane } from './types'
import type { NodeLayout } from './components/BoardView'
import { backendLabel } from './ui'

export type TemplateAgent = {
  key: string
  name: string
  backend: string
  preset: string
  command: string[]
  role: string
  account: string
  cwd: string
  layout: NodeLayout
}

export type TemplateLink = {
  from: string
  to: string
}

export type TopologyTemplate = {
  id: string
  name: string
  description: string
  main?: string
  agents: TemplateAgent[]
  links: TemplateLink[]
}

export function templateFromWorkspace(
  name: string,
  description: string,
  panes: Pane[],
  edges: Edge[],
  layouts: Record<string, NodeLayout>,
  mainId: string,
): TopologyTemplate {
  const keyById = new Map<string, string>()
  const agents = panes.map((pane, index) => {
    const key = `agent-${index}`
    keyById.set(pane.id, key)
    return {
      key,
      name: pane.name,
      backend: backendLabel(pane),
      preset: pane.preset || presetFromPane(pane),
      command: pane.command ?? [],
      role: pane.role ?? '',
      account: pane.account || 'Personal',
      cwd: pane.cwd,
      layout: layouts[pane.id] ?? {
        x: 100 + (index % 3) * 340,
        y: 170 + Math.floor(index / 3) * 210,
        width: 440,
        height: 320,
        expanded: false,
      },
    }
  })
  const pairKeys = new Set<string>()
  const links: TemplateLink[] = []
  for (const edge of edges) {
    const from = keyById.get(edge.from)
    const to = keyById.get(edge.to)
    if (!from || !to) continue
    const key = [from, to].sort().join(':')
    if (pairKeys.has(key)) continue
    pairKeys.add(key)
    links.push({ from, to })
  }
  return {
    id: `template-${crypto.randomUUID()}`,
    name,
    description,
    main: keyById.get(mainId),
    agents,
    links,
  }
}

export function presetFromPane(pane: Pane) {
  const backend = backendLabel(pane).toLowerCase()
  if (backend === 'shell') return 'shell'
  return `${backend}-pty`
}
