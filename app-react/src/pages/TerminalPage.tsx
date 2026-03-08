/**
 * TerminalPage.tsx — D-PlaneOS v4.1
 *
 * Full PTY terminal over WebSocket (/ws/terminal).
 * Uses xterm.js (same library as VS Code, Proxmox, Cockpit).
 *
 * Protocol:
 *   Client → {"type":"input",  "data":"<keystrokes>"}
 *   Client → {"type":"resize", "cols":N, "rows":N}
 *   Server → {"type":"output", "data":"<raw output>"}
 *   Server → {"type":"exit"}
 *   Server → {"type":"error",  "data":"<message>"}
 */

import { useEffect, useRef, useCallback, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import { getSessionId, getUsername } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type ConnStatus = 'connecting' | 'connected' | 'disconnected' | 'error'

interface TermMsg {
  type: 'output' | 'exit' | 'error'
  data?: string
}

// ---------------------------------------------------------------------------
// Component
// ---------------------------------------------------------------------------

export function TerminalPage() {
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const [status, setStatus] = useState<ConnStatus>('connecting')
  const [errorMsg, setErrorMsg] = useState<string>('')

  // Send a message to daemon if WS is open
  const send = useCallback((msg: object) => {
    if (wsRef.current?.readyState === WebSocket.OPEN) {
      wsRef.current.send(JSON.stringify(msg))
    }
  }, [])

  // Connect / reconnect
  const connect = useCallback(() => {
    // Clean up any existing connection
    wsRef.current?.close()
    termRef.current?.clear()
    setStatus('connecting')
    setErrorMsg('')

    const sessionId = getSessionId()
    const username = getUsername()
    if (!sessionId) {
      setStatus('error')
      setErrorMsg('No active session — please log in again.')
      return
    }

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const url = `${protocol}//${window.location.host}/ws/terminal`
    const ws = new WebSocket(url)
    wsRef.current = ws

    ws.onopen = () => {
      // Authenticate immediately (same pattern as /ws/monitor)
      ws.send(JSON.stringify({
        type: 'auth',
        session_id: sessionId,
        username,
      }))
      setStatus('connected')

      // Send initial terminal size
      if (termRef.current) {
        send({ type: 'resize', cols: termRef.current.cols, rows: termRef.current.rows })
      }
    }

    ws.onmessage = (event) => {
      let msg: TermMsg
      try {
        msg = JSON.parse(event.data as string) as TermMsg
      } catch {
        return
      }

      switch (msg.type) {
        case 'output':
          termRef.current?.write(msg.data ?? '')
          break
        case 'exit':
          termRef.current?.write('\r\n\x1b[90m[Process exited — press Enter to reconnect]\x1b[0m\r\n')
          setStatus('disconnected')
          break
        case 'error':
          setStatus('error')
          setErrorMsg(msg.data ?? 'Terminal error')
          break
      }
    }

    ws.onclose = () => {
      setStatus((prev) => prev === 'connected' ? 'disconnected' : prev)
    }

    ws.onerror = () => {
      setStatus('error')
      setErrorMsg('WebSocket connection failed')
    }
  }, [send])

  // Initialise xterm once on mount
  useEffect(() => {
    if (!containerRef.current) return

    const term = new Terminal({
      theme: {
        background:   '#0d0f14',
        foreground:   '#e2e8f0',
        cursor:       '#a78bfa',
        cursorAccent: '#0d0f14',
        selectionBackground: 'rgba(167,139,250,0.25)',
        black:        '#1e2130',
        red:          '#f87171',
        green:        '#4ade80',
        yellow:       '#fbbf24',
        blue:         '#60a5fa',
        magenta:      '#c084fc',
        cyan:         '#22d3ee',
        white:        '#e2e8f0',
        brightBlack:  '#475569',
        brightRed:    '#fca5a5',
        brightGreen:  '#86efac',
        brightYellow: '#fde68a',
        brightBlue:   '#93c5fd',
        brightMagenta:'#d8b4fe',
        brightCyan:   '#67e8f9',
        brightWhite:  '#f8fafc',
      },
      fontFamily: '"JetBrains Mono Variable", "JetBrains Mono", monospace',
      fontSize: 13,
      lineHeight: 1.5,
      cursorBlink: true,
      cursorStyle: 'block',
      scrollback: 5000,
      allowProposedApi: true,
    })

    const fit = new FitAddon()
    const links = new WebLinksAddon()

    term.loadAddon(fit)
    term.loadAddon(links)
    term.open(containerRef.current)
    fit.fit()

    termRef.current = term
    fitRef.current = fit

    // Forward keystrokes to PTY
    term.onData((data) => {
      send({ type: 'input', data })
    })

    // Initial connection
    connect()

    // Resize observer — refit terminal when container changes size
    const ro = new ResizeObserver(() => {
      fit.fit()
      if (termRef.current) {
        send({ type: 'resize', cols: termRef.current.cols, rows: termRef.current.rows })
      }
    })
    ro.observe(containerRef.current)

    return () => {
      ro.disconnect()
      wsRef.current?.close()
      term.dispose()
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div style={{
      display: 'flex',
      flexDirection: 'column',
      height: '100%',
      background: '#0d0f14',
      borderRadius: '12px',
      overflow: 'hidden',
      border: '1px solid rgba(255,255,255,0.06)',
    }}>
      {/* Title bar */}
      <div style={{
        display: 'flex',
        alignItems: 'center',
        gap: '10px',
        padding: '10px 16px',
        background: 'rgba(255,255,255,0.03)',
        borderBottom: '1px solid rgba(255,255,255,0.06)',
        flexShrink: 0,
      }}>
        <Icon name="terminal" size={18} style={{ color: '#a78bfa' }} />
        <span style={{ fontSize: '13px', fontWeight: 500, color: '#e2e8f0', letterSpacing: '0.01em' }}>
          System Terminal
        </span>
        <StatusDot status={status} />
        {errorMsg && (
          <span style={{ fontSize: '12px', color: '#f87171', marginLeft: 4 }}>{errorMsg}</span>
        )}
        <div style={{ marginLeft: 'auto', display: 'flex', gap: '8px' }}>
          {(status === 'disconnected' || status === 'error') && (
            <button
              onClick={connect}
              style={{
                display: 'flex', alignItems: 'center', gap: '6px',
                padding: '4px 10px', borderRadius: '6px', border: 'none',
                background: 'rgba(167,139,250,0.15)', color: '#a78bfa',
                fontSize: '12px', cursor: 'pointer', fontFamily: 'inherit',
              }}
            >
              <Icon name="refresh" size={14} />
              Reconnect
            </button>
          )}
          <button
            onClick={() => termRef.current?.clear()}
            style={{
              display: 'flex', alignItems: 'center', gap: '6px',
              padding: '4px 10px', borderRadius: '6px', border: 'none',
              background: 'rgba(255,255,255,0.06)', color: '#94a3b8',
              fontSize: '12px', cursor: 'pointer', fontFamily: 'inherit',
            }}
          >
            <Icon name="clear_all" size={14} />
            Clear
          </button>
        </div>
      </div>

      {/* xterm.js container */}
      <div
        ref={containerRef}
        style={{ flex: 1, padding: '8px', minHeight: 0 }}
      />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Status indicator
// ---------------------------------------------------------------------------

function StatusDot({ status }: { status: ConnStatus }) {
  const colors: Record<ConnStatus, string> = {
    connecting:   '#fbbf24',
    connected:    '#4ade80',
    disconnected: '#94a3b8',
    error:        '#f87171',
  }
  const labels: Record<ConnStatus, string> = {
    connecting:   'Connecting…',
    connected:    'Connected',
    disconnected: 'Disconnected',
    error:        'Error',
  }
  return (
    <span style={{ display: 'flex', alignItems: 'center', gap: '5px' }}>
      <span style={{
        width: '7px', height: '7px', borderRadius: '50%',
        background: colors[status],
        boxShadow: status === 'connected' ? `0 0 6px ${colors[status]}` : undefined,
      }} />
      <span style={{ fontSize: '11px', color: '#64748b' }}>{labels[status]}</span>
    </span>
  )
}
