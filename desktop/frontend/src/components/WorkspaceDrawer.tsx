import { useEffect, useState } from 'react'
import { Folder, X } from 'lucide-react'

import { baseName } from '../ui'

type Props = {
  workspace: string
  creating: boolean
  busy: boolean
  onClose(): void
  onSubmit(path: string): void
}

export function WorkspaceDrawer({
  workspace,
  creating,
  busy,
  onClose,
  onSubmit,
}: Props) {
  const [path, setPath] = useState(creating ? '' : workspace)

  useEffect(() => setPath(creating ? '' : workspace), [creating, workspace])

  return (
    <aside className="side-drawer" aria-label={creating ? '新建工作区' : '工作区设置'}>
      <header className="drawer-header">
        <div>
          <h2>{creating ? '新建工作区' : '工作区设置'}</h2>
          <p>{creating ? '新工作区默认从一个空白画板开始。' : '当前 Workspace 的配置。'}</p>
        </div>
        <button className="icon-button" type="button" aria-label="关闭" title="关闭" onClick={onClose}>
          <X size={17} />
        </button>
      </header>
      <form
        className="drawer-form"
        onSubmit={(event) => {
          event.preventDefault()
          if (path.trim()) onSubmit(path.trim())
        }}
      >
        <label className="field">
          <span>项目目录</span>
          <div className="input-with-icon">
            <Folder size={15} />
            <input
              autoFocus
              className="mono-input"
              value={path}
              placeholder="/Users/pluto/Projects/new-workspace"
              onChange={(event) => setPath(event.target.value)}
            />
          </div>
          <small>
            {creating
              ? '如果目录不存在，wtable 会创建它。'
              : `Workspace 名称由目录决定：${baseName(path) || '—'}`}
          </small>
        </label>
        <footer className="drawer-footer">
          <button className="secondary-button" type="button" onClick={onClose}>
            取消
          </button>
          <button className="primary-button" type="submit" disabled={busy || !path.trim()}>
            {busy ? '正在打开…' : creating ? '创建工作区' : '打开此工作区'}
          </button>
        </footer>
      </form>
    </aside>
  )
}
