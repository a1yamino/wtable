declare global {
  interface Window {
    WailsInvoke?: (message: string) => void
  }
}

/**
 * Wails uses --wails-draggable for native window drags. This fallback sends
 * the same native command after a real pointer movement so the frame remains
 * draggable even when the WebView consumes the initial mouse event.
 */
export function installWindowDragFallback() {
  let cleanupActive: (() => void) | undefined

  const onMouseDown = (event: MouseEvent) => {
    if (event.button !== 0 || !event.target || !window.WailsInvoke) return
    const target = event.target as Element
    const draggable = window
      .getComputedStyle(target)
      .getPropertyValue('--wails-draggable')
      .trim()
    if (draggable !== 'drag') return

    cleanupActive?.()
    const startX = event.clientX
    const startY = event.clientY
    let finished = false

    const cleanup = () => {
      if (finished) return
      finished = true
      document.removeEventListener('mousemove', onMouseMove, true)
      document.removeEventListener('mouseup', cleanup, true)
      cleanupActive = undefined
    }

    const onMouseMove = (moveEvent: MouseEvent) => {
      if (moveEvent.buttons === 0) {
        cleanup()
        return
      }
      if (Math.hypot(moveEvent.clientX - startX, moveEvent.clientY - startY) < 4) {
        return
      }
      moveEvent.preventDefault()
      moveEvent.stopPropagation()
      window.WailsInvoke?.('drag')
      cleanup()
    }

    document.addEventListener('mousemove', onMouseMove, true)
    document.addEventListener('mouseup', cleanup, true)
    cleanupActive = cleanup
  }

  document.addEventListener('mousedown', onMouseDown)
  return () => {
    cleanupActive?.()
    document.removeEventListener('mousedown', onMouseDown)
  }
}
