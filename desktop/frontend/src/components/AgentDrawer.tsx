import { useEffect, useState } from 'react'
import { AlertCircle, X } from 'lucide-react'

import type { Catalog, Pane } from '../types'

export type AgentFormValues = {
  name: string
  preset: string
  command: string[]
  role: string
  account: string
  cwd: string
}

type Props = {
  pane?: Pane
  placement?: { x: number; y: number }
  workspace: string
  catalog?: Catalog
  makeMain: boolean
  busy: boolean
  onClose(): void
  onSubmit(values: AgentFormValues, makeMain: boolean): void
  onMakeMainChange(value: boolean): void
}

export function AgentDrawer({
  pane,
  placement,
  workspace,
  catalog,
  makeMain,
  busy,
  onClose,
  onSubmit,
  onMakeMainChange,
}: Props) {
  const presets = catalog?.presets.length
    ? catalog.presets
    : [
        { id: 'codex-pty', name: 'Codex · 终端(PTY)', backend: 'pty', command: ['codex'], available: true },
        { id: 'claude-pty', name: 'Claude · 终端(PTY)', backend: 'pty', command: ['claude'], available: true },
        { id: 'agy-pty', name: 'Agy · 终端(PTY)', backend: 'pty', command: ['agy'], available: true },
        { id: 'shell', name: 'Shell · 终端', backend: 'pty', command: ['/bin/zsh'], available: true },
      ]
  const initialPreset =
    pane?.preset ||
    presets.find((preset) => preset.available)?.id ||
    'shell'
  const [name, setName] = useState(pane?.name ?? '')
  const [presetId, setPresetId] = useState(initialPreset)
  const [role, setRole] = useState(pane?.role ?? '')
  const [account, setAccount] = useState(pane?.account ?? '')
  const [cwd, setCwd] = useState(pane?.cwd ?? workspace)
  const [error, setError] = useState('')
  const accountLocked =
    Boolean(pane) && ['running', 'starting', 'stopping'].includes(pane?.state ?? '')

  useEffect(() => {
    setName(pane?.name ?? '')
    setPresetId(pane?.preset || presets.find((preset) => preset.available)?.id || 'shell')
    setRole(pane?.role ?? '')
    setAccount(pane?.account ?? '')
    setCwd(pane?.cwd ?? workspace)
    setError('')
  }, [pane?.id, pane?.name, pane?.preset, pane?.role, pane?.account, pane?.cwd, workspace])

  const submit = (event: React.FormEvent) => {
    event.preventDefault()
    if (!name.trim() || !cwd.trim()) {
      setError('请填写 Agent 名称和工作目录。')
      return
    }
    const selected = presets.find((preset) => preset.id === presetId)
    if (!selected) {
      setError('请选择可用的 Agent 底座。')
      return
    }
    if (!selected.available) {
      setError(selected.detail || '这个 Agent 底座当前不可用。')
      return
    }
    setError('')
    onSubmit(
      {
        name: name.trim(),
        preset: selected.id,
        command: selected.command,
        role: role.trim(),
        account: account.trim(),
        cwd: cwd.trim(),
      },
      makeMain,
    )
  }

  return (
    <aside className="side-drawer" aria-label={pane ? '编辑 Agent' : '新增 Agent'}>
      <header className="drawer-header">
        <div>
          <h2>{pane ? '编辑 Agent' : '配置新 Agent'}</h2>
          {!pane && placement && (
            <p>节点位置已确定，保存后加入当前 Workspace。</p>
          )}
        </div>
        <button className="icon-button" type="button" aria-label="关闭" title="关闭" onClick={onClose}>
          <X size={17} />
        </button>
      </header>

      <form className="drawer-form" onSubmit={submit}>
        <label className="field">
          <span>名称</span>
          <input
            autoFocus
            value={name}
            maxLength={80}
            placeholder="例如 architect"
            onChange={(event) => setName(event.target.value)}
          />
        </label>

        <label className="field">
          <span>Agent 底座</span>
          <select
            value={presetId}
            disabled={Boolean(pane)}
            onChange={(event) => setPresetId(event.target.value)}
          >
            {presets.map((preset) => (
              <option key={preset.id} value={preset.id} disabled={!preset.available}>
                {preset.name}{preset.available ? '' : ' · 不可用'}
              </option>
            ))}
          </select>
          {pane && <small>运行中的 Agent 不能直接更换底座，请新建实例。</small>}
        </label>

        <label className="field">
          <span>角色说明</span>
          <textarea
            value={role}
            rows={3}
            maxLength={256}
            placeholder="例如：负责架构与最终集成"
            onChange={(event) => setRole(event.target.value)}
          />
        </label>

        <label className="field">
          <span>账号配置</span>
          <input
            value={account}
            list="wtable-accounts"
            disabled={accountLocked}
            onChange={(event) => setAccount(event.target.value)}
          />
          <datalist id="wtable-accounts">
            {(catalog?.accounts.length ? catalog.accounts : ['Personal']).map((item) => (
              <option key={item} value={item} />
            ))}
          </datalist>
          {accountLocked && <small>运行中的 Agent 不能切换账号；停止后可以修改。</small>}
        </label>

        <label className="field">
          <span>工作目录</span>
          <input
            className="mono-input"
            value={cwd}
            disabled={Boolean(pane)}
            onChange={(event) => setCwd(event.target.value)}
          />
          {pane && <small>目录需要重新创建 Agent 才能修改。</small>}
        </label>

        <label className="checkbox-field">
          <input
            type="checkbox"
            checked={makeMain}
            onChange={(event) => onMakeMainChange(event.target.checked)}
          />
          <span>设为当前 Workspace 的主 Agent</span>
        </label>

        {error && (
          <p className="form-error" role="alert">
            <AlertCircle size={15} />
            {error}
          </p>
        )}

        <footer className="drawer-footer">
          <button className="secondary-button" type="button" onClick={onClose}>
            取消
          </button>
          <button className="primary-button" type="submit" disabled={busy}>
            {busy ? '正在处理…' : pane ? '保存设置' : '创建 Agent'}
          </button>
        </footer>
      </form>
    </aside>
  )
}
