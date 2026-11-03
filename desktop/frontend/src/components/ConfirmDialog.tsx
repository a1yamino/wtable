import { AlertTriangle, X } from 'lucide-react'

type Props = {
  title: string
  message: string
  confirmLabel: string
  busy?: boolean
  danger?: boolean
  onCancel(): void
  onConfirm(): void
}

export function ConfirmDialog({
  title,
  message,
  confirmLabel,
  busy,
  danger,
  onCancel,
  onConfirm,
}: Props) {
  return (
    <div className="dialog-backdrop" role="presentation" onPointerDown={onCancel}>
      <section
        className="confirm-dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="confirm-title"
        aria-describedby="confirm-message"
        onPointerDown={(event) => event.stopPropagation()}
      >
        <header>
          <span className={`dialog-icon ${danger ? 'danger' : ''}`}>
            <AlertTriangle size={18} />
          </span>
          <button className="icon-button" type="button" aria-label="关闭" title="关闭" onClick={onCancel}>
            <X size={16} />
          </button>
        </header>
        <h2 id="confirm-title">{title}</h2>
        <p id="confirm-message">{message}</p>
        <footer>
          <button className="secondary-button" type="button" onClick={onCancel}>
            取消
          </button>
          <button
            className={danger ? 'danger-button' : 'primary-button'}
            type="button"
            disabled={busy}
            onClick={onConfirm}
          >
            {busy ? '正在处理…' : confirmLabel}
          </button>
        </footer>
      </section>
    </div>
  )
}
