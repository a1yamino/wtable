import { useEffect, useRef, useState } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'

import { backend, decodeBase64 } from '../backend'
import type { Pane } from '../types'

type Props = {
  pane?: Pane
  onError(message: string): void
  autoFocus?: boolean
}

export type StreamState = 'idle' | 'live' | 'retry'

export function TerminalSurface({ pane, onError, autoFocus = true }: Props) {
  const hostRef = useRef<HTMLDivElement>(null)
  const terminalRef = useRef<Terminal | undefined>(undefined)
  const paneRef = useRef<Pane | undefined>(pane)
  const errorRef = useRef(onError)
  const mountedRef = useRef(false)
  const [streamState, setStreamState] = useState<StreamState>('idle')

  paneRef.current = pane
  errorRef.current = onError

  useEffect(() => {
    if (!hostRef.current) return
    mountedRef.current = true
    const terminal = new Terminal({
      allowProposedApi: true,
      cursorBlink: true,
      cursorStyle: 'bar',
      cursorWidth: 2,
      fontFamily: '"SFMono-Regular", "Cascadia Code", "Menlo", monospace',
      fontSize: 12.5,
      fontWeight: 'normal',
      fontWeightBold: 'bold',
      letterSpacing: 0,
      lineHeight: 1.35,
      scrollback: 10000,
      smoothScrollDuration: 70,
      theme: {
        background: '#080a0d',
        foreground: '#d8dde4',
        cursor: '#d9ddd8',
        cursorAccent: '#080a0d',
        selectionBackground: '#47515d80',
        black: '#171b20',
        red: '#d57f7f',
        green: '#7bb592',
        yellow: '#d0a363',
        blue: '#9eafc3',
        magenta: '#b5a1c8',
        cyan: '#92b9b1',
        white: '#d8dde4',
        brightBlack: '#69727e',
        brightRed: '#edaaaa',
        brightGreen: '#a8d3b8',
        brightYellow: '#e7c185',
        brightBlue: '#bdcad9',
        brightMagenta: '#cebfd9',
        brightCyan: '#b6d2cc',
        brightWhite: '#ffffff',
      },
    })
    const fit = new FitAddon()
    terminal.loadAddon(fit)
    terminal.open(hostRef.current)
    terminalRef.current = terminal

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
      if (inputTimer === undefined) inputTimer = window.setTimeout(flushInput, 8)
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
        // The host can briefly have zero size while a split or node is resized.
      }
    }
    const observer = new ResizeObserver(fitTerminal)
    observer.observe(hostRef.current)
    const raf = requestAnimationFrame(() => {
      fitTerminal()
      if (autoFocus) terminal.focus()
    })

    return () => {
      mountedRef.current = false
      cancelAnimationFrame(raf)
      observer.disconnect()
      if (inputTimer !== undefined) window.clearTimeout(inputTimer)
      inputDisposable.dispose()
      resizeDisposable.dispose()
      terminalRef.current = undefined
      terminal.dispose()
    }
  }, [autoFocus])

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

    const wait = () =>
      new Promise<void>((resolve) => window.setTimeout(resolve, 900))
    const isActive = () => active && mountedRef.current

    const pump = async () => {
      while (isActive()) {
        while (isActive()) {
          try {
            const attachment = await backend.attachTerminal(pane.id)
            if (!isActive()) return
            terminal.reset()
            if (attachment.history) terminal.write(decodeBase64(attachment.history))
            if (attachment.snapshot) terminal.write(decodeBase64(attachment.snapshot))
            cursor = attachment.cursor
            setStreamState('live')
            break
          } catch (error) {
            if (!isActive()) return
            setStreamState('retry')
            errorRef.current(errorMessage(error))
            await wait()
          }
        }
        while (isActive()) {
          try {
            const chunk = await backend.readTerminal(pane.id, cursor)
            if (!isActive()) return
            if (chunk.cursor < cursor) break
            if (chunk.data) terminal.write(decodeBase64(chunk.data))
            cursor = chunk.cursor
            setStreamState('live')
          } catch (error) {
            if (!isActive()) return
            setStreamState('retry')
            errorRef.current(errorMessage(error))
            await wait()
          }
        }
      }
    }

    void pump()
    if (autoFocus) requestAnimationFrame(() => terminal.focus())
    return () => {
      active = false
    }
  }, [pane?.id, autoFocus])

  return (
    <div
      ref={hostRef}
      className="terminal-surface"
      data-stream-state={streamState}
      aria-label={pane ? `${pane.name} 终端` : '终端'}
    />
  )
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : String(error)
}
