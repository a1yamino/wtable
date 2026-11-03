import { useEffect, useState } from 'react'
import { AlertCircle, LayoutTemplate, Save, X } from 'lucide-react'

import type { Catalog } from '../types'
import type { TemplateAgent, TopologyTemplate } from '../templates'

type Props = {
  templates: TopologyTemplate[]
  draft?: TopologyTemplate
  catalog?: Catalog
  hasCurrentAgents: boolean
  busy: boolean
  onClose(): void
  onSave(name: string, description: string): void
  onSelect(template: TopologyTemplate): void
  onDraftChange(draft: TopologyTemplate): void
  onCancelPreview(): void
  onApply(): void
}

export function TemplateDrawer({
  templates,
  draft,
  catalog,
  hasCurrentAgents,
  busy,
  onClose,
  onSave,
  onSelect,
  onDraftChange,
  onCancelPreview,
  onApply,
}: Props) {
  const [saving, setSaving] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')

  useEffect(() => {
    if (!saving) {
      setName('')
      setDescription('')
    }
  }, [saving])

  const updateAgent = (key: string, patch: Partial<TemplateAgent>) => {
    if (!draft) return
    const presets = catalog?.presets ?? []
    onDraftChange({
      ...draft,
      agents: draft.agents.map((agent) => {
        if (agent.key !== key) return agent
        if (patch.preset) {
          const preset = presets.find((item) => item.id === patch.preset)
          return {
            ...agent,
            ...patch,
            preset: patch.preset,
            backend: preset?.name.split(' ')[0] ?? agent.backend,
            command: preset?.command ?? agent.command,
          }
        }
        return { ...agent, ...patch }
      }),
    })
  }

  return (
    <aside className="side-drawer template-drawer" aria-label="拓扑模板">
      <header className="drawer-header">
        <div>
          <h2>{saving ? '保存全局模板' : '拓扑模板'}</h2>
          <p>{saving ? '保存后可在任意 Workspace 中预览并应用。' : '模板保留 Agent 配置、位置、尺寸与授权关系。'}</p>
        </div>
        <button className="icon-button" type="button" aria-label="关闭" title="关闭" onClick={onClose}>
          <X size={17} />
        </button>
      </header>

      {saving ? (
        <form
          className="drawer-form"
          onSubmit={(event) => {
            event.preventDefault()
            if (!name.trim()) return
            onSave(name.trim(), description.trim())
            setSaving(false)
          }}
        >
          <label className="field">
            <span>模板名称</span>
            <input
              autoFocus
              value={name}
              maxLength={50}
              placeholder="例如：实现与复核"
              onChange={(event) => setName(event.target.value)}
            />
          </label>
          <label className="field">
            <span>说明</span>
            <textarea
              rows={3}
              value={description}
              placeholder="说明这个拓扑适合什么工作。"
              onChange={(event) => setDescription(event.target.value)}
            />
          </label>
          <footer className="drawer-footer">
            <button className="secondary-button" type="button" onClick={() => setSaving(false)}>
              返回模板
            </button>
            <button className="primary-button" type="submit" disabled={!name.trim()}>
              保存模板
            </button>
          </footer>
        </form>
      ) : (
        <>
          <div className="drawer-form template-list-panel">
            <button
              className="template-save-button"
              type="button"
              disabled={!hasCurrentAgents}
              onClick={() => setSaving(true)}
            >
              <Save size={16} />
              保存当前拓扑
            </button>
            <p className="drawer-help">
              {hasCurrentAgents
                ? '保存当前 Workspace 的完整结构，不复制正在运行的进程。'
                : '当前 Workspace 没有可保存的 Agent。'}
            </p>

            <div className="template-list">
              {templates.map((template) => (
                <button
                  key={template.id}
                  className={`template-row ${draft?.id === template.id ? 'selected' : ''}`}
                  type="button"
                  onClick={() => onSelect(template)}
                >
                  <span className="template-icon">
                    <LayoutTemplate size={16} />
                  </span>
                  <span>
                    <strong>{template.name}</strong>
                    <small>
                      {template.agents.length} 个 Agent · {template.links.length} 条连接
                    </small>
                  </span>
                </button>
              ))}
              {templates.length === 0 && (
                <p className="drawer-empty">还没有保存的模板。</p>
              )}
            </div>
          </div>

          {draft && (
            <section className="template-preview-panel">
              <div className="preview-heading">
                <div>
                  <span>画板预览</span>
                  <h3>{draft.name}</h3>
                </div>
                <button type="button" onClick={onCancelPreview}>
                  退出预览
                </button>
              </div>
              <p>{draft.description || '保存的节点配置、位置、尺寸与授权关系。'}</p>
              <div className="template-agent-list">
                {draft.agents.map((agent) => (
                  <details key={agent.key}>
                    <summary>
                      <span>{agent.name}</span>
                      <span className="backend-badge">{agent.backend}</span>
                    </summary>
                    <div className="template-agent-fields">
                      <label className="field">
                        <span>Agent 底座</span>
                        <select
                          value={agent.preset}
                          onChange={(event) => updateAgent(agent.key, { preset: event.target.value })}
                        >
                          {(catalog?.presets.length ? catalog.presets : fallbackPresets).map((preset) => (
                            <option key={preset.id} value={preset.id} disabled={!preset.available}>
                              {preset.name}
                            </option>
                          ))}
                        </select>
                      </label>
                      <label className="field">
                        <span>账号配置</span>
                        <input
                          value={agent.account}
                          onChange={(event) => updateAgent(agent.key, { account: event.target.value })}
                        />
                      </label>
                      <label className="field">
                        <span>工作目录</span>
                        <input
                          className="mono-input"
                          value={agent.cwd}
                          onChange={(event) => updateAgent(agent.key, { cwd: event.target.value })}
                        />
                      </label>
                    </div>
                  </details>
                ))}
              </div>

              {hasCurrentAgents && (
                <p className="template-warning">
                  <AlertCircle size={15} />
                  应用时会替换当前 Workspace 的 Agent 与授权关系。
                </p>
              )}

              <footer className="drawer-footer">
                <button className="secondary-button" type="button" onClick={onCancelPreview}>
                  取消预览
                </button>
                <button className="primary-button" type="button" disabled={busy} onClick={onApply}>
                  {busy ? '正在应用…' : '应用模板'}
                </button>
              </footer>
            </section>
          )}
        </>
      )}
    </aside>
  )
}

const fallbackPresets = [
  { id: 'codex-pty', name: 'Codex · 终端(PTY)', backend: 'pty', command: ['codex'], available: true },
  { id: 'claude-pty', name: 'Claude · 终端(PTY)', backend: 'pty', command: ['claude'], available: true },
  { id: 'agy-pty', name: 'Agy · 终端(PTY)', backend: 'pty', command: ['agy'], available: true },
  { id: 'shell', name: 'Shell · 终端', backend: 'pty', command: ['/bin/zsh'], available: true },
]
