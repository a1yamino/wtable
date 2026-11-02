import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { WebglAddon } from '@xterm/addon-webgl'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'

import { backend, decodeBase64 } from '../backend'
import type { Pane } from '../types'

type Props = {
  pane?: Pane
  onError(message: string): void
}

export function TerminalPane({ pane, onError }: Props) {
  const hostRef = useRef<HTMLDivElement>(null)
  const terminalRef = useRef<Terminal | undefined>(undefined)
  const paneRef = useRef<Pane | undefined>(pane)
  const errorRef = useRef(onError)
  const [streamState, setStreamState] = useState<'idle' | 'live' | 'retry'>('idle')

  paneRef.current = pane
  errorRef.current = onError

  useEffect(() => {
    if (!hostRef.current) return
    const terminal = new Terminal({
      allowProposedApi: true,
      cursorBlink: true,
      cursorStyle: 'bar',
      cursorWidth: 2,
      fontFamily: '"SFMono-Regular", "Cascadia Code", "JetBrains Mono", Menlo, monospace',
      fontSize: 13.5,
      fontWeight: 'normal',
      fontWeightBold: 'bold',
      letterSpacing: 0.1,
      lineHeight: 1.24,
      scrollback: 10000,
      smoothScrollDuration: 90,
      theme: {
        background: '#0b1017',
        foreground: '#d7e0eb',
        cursor: '#8df0c8',
        cursorAccent: '#0b1017',
        selectionBackground: '#31526780',
        black: '#1a202a',
        red: '#ff6b7d',
        green: '#8df0c8',
        yellow: '#f4c86b',
        blue: '#72a7ff',
        magenta: '#d59cff',
        cyan: '#72dfe6',
        white: '#d7e0eb',
        brightBlack: '#687585',
        brightRed: '#ff8d9b',
        brightGreen: '#b1f9dd',
        brightYellow: '#ffe091',
        brightBlue: '#9dc1ff',
        brightMagenta: '#e7c2ff',
        brightCyan: '#9aedf1',
        brightWhite: '#ffffff',
      },
    })
    const fit = new FitAddon()
    terminal.loadAddon(fit)
    terminal.open(hostRef.current)
    terminalRef.current = terminal

    try {
      const webgl = new WebglAddon()
      webgl.onContextLoss(() => webgl.dispose())
      terminal.loadAddon(webgl)
    } catch {
      // DOM renderer remains active when WebGL is unavailable in the webview.
    }

    let input = ''
    let inputTimer: number | undefined
    const flushInput = () => {
      inputTimer = undefined
      const data = input
      input = ''
      const current = paneRef.current
      if (!data || !current) return
      void backend.sendInput(current.id, data).catch((error: unknown) => {
        errorRef.current(errorMessage(error))
      })
    }
    const inputDisposable = terminal.onData((data) => {
      input += data
      if (inputTimer === undefined) {
        inputTimer = window.setTimeout(flushInput, 8)
      }
    })
    const resizeDisposable = terminal.onResize(({ cols, rows }) => {
      const current = paneRef.current
      if (!current) return
      void backend.resizeTerminal(current.id, cols, rows).catch((error: unknown) => {
        errorRef.current(errorMessage(error))
      })
    })

    const fitTerminal = () => {
      try {
        fit.fit()
      } catch {
        // The host can briefly have zero dimensions while the split is moving.
      }
    }
    const observer = new ResizeObserver(fitTerminal)
    observer.observe(hostRef.current)
    const raf = requestAnimationFrame(() => {
      fitTerminal()
      terminal.focus()
    })

    return () => {
      cancelAnimationFrame(raf)
      observer.disconnect()
      if (inputTimer !== undefined) window.clearTimeout(inputTimer)
      inputDisposable.dispose()
      resizeDisposable.dispose()
      terminal.dispose()
      terminalRef.current = undefined
    }
  }, [])

  useEffect(() => {
    const terminal = terminalRef.current
    if (!terminal) return
    terminal.reset()
    if (!pane) {
      setStreamState('idle')
      return
    }
    let active = true
    let cursor = 0
    setStreamState('live')

    const pump = async () => {
      while (active) {
        try {
          const chunk = await backend.readTerminal(pane.id, cursor)
          if (!active) return
          if (chunk.cursor < cursor) cursor = 0
          if (chunk.data) terminal.write(decodeBase64(chunk.data))
          cursor = chunk.cursor
          setStreamState('live')
        } catch (error) {
          if (!active) return
          setStreamState('retry')
          errorRef.current(errorMessage(error))
          await new Promise((resolve) => window.setTimeout(resolve, 900))
        }
      }
    }
    void pump()
    requestAnimationFrame(() => terminal.focus())
    return () => {
      active = false
    }
  }, [pane?.id])

  const stateLabel =
    streamState === 'live' ? '实时' : streamState === 'retry' ? '重连中' : '未连接'

  return (
    <section className="terminal-card">
      <header className="panel-heading terminal-heading">
        <div className="terminal-title">
          <span className={`pane-state ${pane?.state ?? 'idle'}`} />
          <div>
            <div className="eyebrow">TERMINAL</div>
            <h2>{pane?.name ?? '选择一个会话'}</h2>
          </div>
        </div>
        <div className="terminal-meta">
          {pane?.kind === 'foreground' && <span className="soft-badge">前台</span>}
          <span className={`stream-badge ${streamState}`}>
            <i />
            {stateLabel}
          </span>
        </div>
      </header>
      <div className="terminal-host">
        <div ref={hostRef} className="xterm-host" />
        {!pane && (
          <div className="terminal-empty">
            <div className="empty-orbit">
              <span>›_</span>
            </div>
            <strong>还没有可显示的终端</strong>
            <p>创建一个 shell，或从左侧打开现有 Agent。</p>
          </div>
        )}
      </div>
      <footer className="terminal-footer">
        <span>{pane?.cwd ?? '—'}</span>
        <span>{pane ? `${pane.backend.toUpperCase()} · ${pane.turn_state ?? pane.state}` : 'PTY'}</span>
      </footer>
    </section>
  )
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error)
}
