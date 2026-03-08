/**
 * pages/LogsPage.tsx — System Logs
 *
 * APIs:
 *   GET /api/system/logs?limit=N  → CommandResponse { data: LogEntry[] }
 *   GET /api/system/logs/stream   → SSE (event: "log", data: journalctl short-iso line)
 *                                   Optional ?unit=<service> filter
 *
 * SSE note: browsers don't support custom headers on EventSource.
 * Session auth is passed as ?session=<id> URL param. The daemon's
 * LogStreamHandler validates the session from the query string.
 * (If the daemon doesn't yet support ?session= param, the stream will
 * return 401 — in that case fall back to polling /api/system/logs.)
 *
 * Log entries from /api/system/logs have shape:
 *   { time: string, message: string, unit: string, level: "info"|"warning"|"error" }
 *
 * SSE stream sends raw journalctl --output=short-iso lines.
 * Max 2000 lines, then daemon sends event: "info" and closes.
 */

import { useQuery } from '@tanstack/react-query'
import { useEffect, useRef, useState, useCallback } from 'react'
import { api } from '@/lib/api'
import { getSessionId } from '@/lib/api'
import { ErrorState } from '@/components/ui/ErrorState'
import { LoadingState } from '@/components/ui/LoadingSpinner'
import { Icon } from '@/components/ui/Icon'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface LogEntry {
  time:    string
  message: string
  unit:    string
  level:   'info' | 'warning' | 'error'
}

interface LogsResponse {
  success:     boolean
  data:        LogEntry[]
  duration_ms: number
}

type LevelFilter = 'all' | 'warning' | 'error'
type ViewMode    = 'history' | 'live'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function levelColor(level: string): string {
  if (level === 'error')   return 'var(--error)'
  if (level === 'warning') return 'var(--warning)'
  return 'var(--text-tertiary)'
}

function levelBg(level: string): string {
  if (level === 'error')   return 'rgba(239,68,68,0.07)'
  if (level === 'warning') return 'rgba(245,158,11,0.06)'
  return 'transparent'
}

function fmtTime(raw: string): string {
  // Daemon returns __REALTIME_TIMESTAMP (microseconds since epoch as string)
  // or short-iso format like "2024-01-15T10:23:45+0100"
  const n = parseInt(raw, 10)
  if (!isNaN(n) && n > 1_000_000_000_000_000) {
    // microseconds
    return new Date(n / 1000).toLocaleTimeString()
  }
  // Already human-readable or ISO
  try {
    return new Date(raw).toLocaleTimeString()
  } catch {
    return raw
  }
}

// Parse a raw journalctl short-iso line into a display-friendly entry.
// Format: "2024-01-15T10:23:45+0100 hostname unit[pid]: message"
function parseStreamLine(line: string): { time: string; unit: string; message: string; level: string } {
  const match = line.match(/^(\S+)\s+\S+\s+(\S+?)(?:\[\d+\])?:\s+(.*)$/)
  if (match) {
    const [, ts, unit, msg] = match
    const lc = msg.toLowerCase()
    const level = lc.includes('error') || lc.includes('failed') || lc.includes('fatal')
      ? 'error'
      : lc.includes('warn') ? 'warning' : 'info'
    return { time: ts, unit, message: msg, level }
  }
  return { time: '', unit: '', message: line, level: 'info' }
}

// ---------------------------------------------------------------------------
// LogRow
// ---------------------------------------------------------------------------

function LogRow({ entry }: { entry: LogEntry }) {
  return (
    <div style={{
      display: 'grid',
      gridTemplateColumns: '80px 180px 1fr',
      gap: '0 12px',
      padding: '5px 12px',
      background: levelBg(entry.level),
      borderBottom: '1px solid var(--border-subtle)',
      fontFamily: 'var(--font-mono)',
      fontSize: 'var(--text-xs)',
      lineHeight: 1.5,
      minWidth: 0,
    }}>
      <span style={{ color: levelColor(entry.level), fontWeight: entry.level !== 'info' ? 600 : 400, flexShrink: 0 }}>
        {entry.level.toUpperCase()}
      </span>
      <span style={{ color: 'var(--text-tertiary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {entry.unit || fmtTime(entry.time)}
      </span>
      <span style={{ color: 'var(--text)', wordBreak: 'break-word' }}>
        {entry.message}
      </span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// LiveLogRow (raw SSE line parsed)
// ---------------------------------------------------------------------------

function LiveLogRow({ parsed }: { parsed: ReturnType<typeof parseStreamLine> }) {
  return (
    <div style={{
      display: 'grid',
      gridTemplateColumns: '80px 180px 1fr',
      gap: '0 12px',
      padding: '5px 12px',
      background: levelBg(parsed.level),
      borderBottom: '1px solid var(--border-subtle)',
      fontFamily: 'var(--font-mono)',
      fontSize: 'var(--text-xs)',
      lineHeight: 1.5,
    }}>
      <span style={{ color: levelColor(parsed.level), fontWeight: parsed.level !== 'info' ? 600 : 400 }}>
        {parsed.level.toUpperCase()}
      </span>
      <span style={{ color: 'var(--text-tertiary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {parsed.unit || parsed.time}
      </span>
      <span style={{ color: 'var(--text)', wordBreak: 'break-word' }}>
        {parsed.message}
      </span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

const MAX_LIVE_LINES = 500

export function LogsPage() {
  const [mode, setMode]           = useState<ViewMode>('history')
  const [levelFilter, setLevel]   = useState<LevelFilter>('all')
  const [searchText, setSearch]   = useState('')
  const [historyLimit, setLimit]  = useState(200)
  const [liveLines, setLiveLines] = useState<ReturnType<typeof parseStreamLine>[]>([])
  const [streaming, setStreaming] = useState(false)
  const [streamCapped, setCapped] = useState(false)
  const [streamError, setStreamError] = useState<string | null>(null)

  const bottomRef  = useRef<HTMLDivElement>(null)
  const esRef      = useRef<EventSource | null>(null)
  const autoScroll = useRef(true)

  // ── History query ─────────────────────────────────────────────────────────

  const logsQ = useQuery({
    queryKey: ['system', 'logs', historyLimit],
    queryFn: ({ signal }) => api.get<LogsResponse>(`/api/system/logs?limit=${historyLimit}`, signal),
    enabled: mode === 'history',
  })

  // ── SSE stream ────────────────────────────────────────────────────────────

  const startStream = useCallback(() => {
    if (esRef.current) {
      esRef.current.close()
      esRef.current = null
    }
    setLiveLines([])
    setCapped(false)
    setStreamError(null)
    setStreaming(true)

    const sessionId = getSessionId()
    const url = `/api/system/logs/stream${sessionId ? `?session=${encodeURIComponent(sessionId)}` : ''}`
    const es = new EventSource(url, { withCredentials: true })
    esRef.current = es

    es.addEventListener('log', (e: MessageEvent) => {
      const parsed = parseStreamLine(e.data)
      setLiveLines((prev) => {
        const next = [...prev, parsed]
        return next.length > MAX_LIVE_LINES ? next.slice(-MAX_LIVE_LINES) : next
      })
    })

    es.addEventListener('info', (e: MessageEvent) => {
      if (e.data.includes('capped')) {
        setCapped(true)
        setStreaming(false)
        es.close()
      }
    })

    es.addEventListener('error', () => {
      setStreamError('Stream disconnected — daemon may have restarted.')
      setStreaming(false)
      es.close()
    })
  }, [])

  const stopStream = useCallback(() => {
    esRef.current?.close()
    esRef.current = null
    setStreaming(false)
  }, [])

  // Auto-scroll live view
  useEffect(() => {
    if (mode === 'live' && autoScroll.current) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [liveLines, mode])

  // Clean up SSE on unmount
  useEffect(() => {
    return () => {
      esRef.current?.close()
    }
  }, [])

  // Switch modes
  function switchMode(m: ViewMode) {
    if (m === 'history') stopStream()
    if (m === 'live') startStream()
    setMode(m)
  }

  // ── Filtered history entries ──────────────────────────────────────────────

  const allEntries: LogEntry[] = logsQ.data?.data ?? []
  const filtered = allEntries.filter((e) => {
    if (levelFilter === 'warning' && e.level === 'info')    return false
    if (levelFilter === 'error'   && e.level !== 'error')   return false
    if (searchText && !e.message.toLowerCase().includes(searchText.toLowerCase())
      && !e.unit?.toLowerCase().includes(searchText.toLowerCase())) return false
    return true
  })

  const filteredLive = liveLines.filter((e) => {
    if (levelFilter === 'warning' && e.level === 'info')   return false
    if (levelFilter === 'error'   && e.level !== 'error')  return false
    if (searchText && !e.message.toLowerCase().includes(searchText.toLowerCase())
      && !e.unit?.toLowerCase().includes(searchText.toLowerCase())) return false
    return true
  })

  // ── Render ────────────────────────────────────────────────────────────────

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 0, maxWidth: 1300, height: 'calc(100vh - 140px)' }}>

      {/* Toolbar */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap',
        padding: '14px 16px',
        background: 'var(--bg-card)',
        border: '1px solid var(--border)',
        borderRadius: 'var(--radius-xl) var(--radius-xl) 0 0',
        flexShrink: 0,
      }}>
        {/* Mode tabs */}
        <div style={{ display: 'flex', gap: 4, background: 'var(--surface)',
          borderRadius: 'var(--radius-sm)', padding: 3 }}>
          {(['history', 'live'] as ViewMode[]).map((m) => (
            <button key={m} onClick={() => switchMode(m)} style={{
              padding: '5px 14px', border: 'none', cursor: 'pointer',
              fontFamily: 'var(--font-ui)', fontSize: 'var(--text-sm)', fontWeight: 500,
              borderRadius: 'var(--radius-xs)',
              background: mode === m ? 'var(--primary)' : 'none',
              color: mode === m ? 'var(--text-on-primary)' : 'var(--text-secondary)',
              transition: 'background 0.15s, color 0.15s',
            }}>
              {m === 'history' ? 'History' : 'Live Stream'}
            </button>
          ))}
        </div>

        {/* Level filter */}
        <div style={{ display: 'flex', gap: 4 }}>
          {([['all', 'All'], ['warning', 'Warnings+'], ['error', 'Errors']] as const).map(([v, label]) => (
            <button key={v} onClick={() => setLevel(v)} style={{
              padding: '5px 10px', fontFamily: 'var(--font-ui)', fontSize: 'var(--text-sm)',
              cursor: 'pointer', border: `1px solid ${levelFilter === v ? 'var(--primary)' : 'var(--border)'}`,
              borderRadius: 'var(--radius-xs)',
              background: levelFilter === v ? 'var(--primary-bg)' : 'none',
              color: levelFilter === v ? 'var(--primary)' : 'var(--text-secondary)',
            }}>
              {label}
            </button>
          ))}
        </div>

        {/* Search */}
        <div style={{ flex: 1, minWidth: 180, position: 'relative' }}>
          <Icon name="search" size={16} style={{
            position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)',
            color: 'var(--text-tertiary)', pointerEvents: 'none',
          }} />
          <input
            type="text"
            placeholder="Filter logs…"
            value={searchText}
            onChange={(e) => setSearch(e.target.value)}
            className="input"
            style={{ paddingLeft: 32 }}
          />
        </div>

        {/* History limit / live controls */}
        {mode === 'history' && (
          <select
            value={historyLimit}
            onChange={(e) => setLimit(parseInt(e.target.value, 10))}
            className="input"
            style={{ width: 'auto' }}
          >
            {[50, 100, 200, 500].map(n => (
              <option key={n} value={n}>{n} lines</option>
            ))}
          </select>
        )}

        {mode === 'live' && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ width: 8, height: 8, borderRadius: '50%', flexShrink: 0,
              background: streaming ? 'var(--success)' : 'var(--text-tertiary)',
              boxShadow: streaming ? '0 0 6px var(--success)' : 'none',
            }} />
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              {streaming ? 'Streaming' : 'Stopped'} · {filteredLive.length} lines
            </span>
            {!streaming && (
              <button onClick={startStream} style={{
                padding: '4px 10px', background: 'var(--primary)', border: 'none',
                borderRadius: 'var(--radius-xs)', color: 'var(--text-on-primary)',
                cursor: 'pointer', fontFamily: 'var(--font-ui)', fontSize: 'var(--text-xs)', fontWeight: 600,
              }}>
                Restart
              </button>
            )}
          </div>
        )}

        {/* Refresh for history */}
        {mode === 'history' && (
          <button onClick={() => logsQ.refetch()}
            disabled={logsQ.isFetching}
            className="btn btn-ghost"
            style={{ opacity: logsQ.isFetching ? 0.5 : 1 }}>
            <Icon name="refresh" size={16} style={{
              animation: logsQ.isFetching ? 'spin 1s linear infinite' : 'none',
            }} />
            Refresh
          </button>
        )}
      </div>

      {/* Column headers */}
      <div style={{
        display: 'grid', gridTemplateColumns: '80px 180px 1fr',
        gap: '0 12px', padding: '6px 12px',
        background: 'var(--surface)', borderLeft: '1px solid var(--border)',
        borderRight: '1px solid var(--border)',
        fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)',
        fontWeight: 600, letterSpacing: '0.5px', textTransform: 'uppercase', flexShrink: 0,
      }}>
        <span>Level</span>
        <span>Unit / Time</span>
        <span>Message</span>
      </div>

      {/* Log body */}
      <div
        style={{
          flex: 1, overflowY: 'auto',
          background: 'rgba(0,0,0,0.4)',
          border: '1px solid var(--border)',
          borderRadius: '0 0 var(--radius-xl) var(--radius-xl)',
          fontFamily: 'var(--font-mono)',
        }}
        onScroll={(e) => {
          const el = e.currentTarget
          autoScroll.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40
        }}
      >
        {/* ── History mode ── */}
        {mode === 'history' && (
          <>
            {logsQ.isLoading && <div style={{ padding: 24 }}><LoadingState message="Loading logs…" /></div>}
            {logsQ.isError && <div style={{ padding: 24 }}><ErrorState error={logsQ.error} onRetry={() => logsQ.refetch()} /></div>}
            {!logsQ.isLoading && !logsQ.isError && filtered.length === 0 && (
              <div style={{ padding: 24, textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
                No entries match the current filter
              </div>
            )}
            {filtered.map((e, i) => <LogRow key={i} entry={e} />)}
          </>
        )}

        {/* ── Live stream mode ── */}
        {mode === 'live' && (
          <>
            {filteredLive.length === 0 && streaming && (
              <div style={{ padding: '16px 12px', color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)',
                fontFamily: 'var(--font-mono)' }}>
                Waiting for log lines…
              </div>
            )}
            {streamError && (
              <div style={{ padding: '12px 16px', color: 'var(--error)', fontSize: 'var(--text-sm)',
                display: 'flex', alignItems: 'center', gap: 8 }}>
                <Icon name="error_outline" size={16} />
                {streamError}
              </div>
            )}
            {streamCapped && (
              <div style={{ padding: '10px 12px', color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)',
                fontFamily: 'var(--font-mono)', borderBottom: '1px solid var(--border-subtle)' }}>
                [Stream capped at 2000 lines by daemon — click Restart to resume]
              </div>
            )}
            {filteredLive.map((e, i) => <LiveLogRow key={i} parsed={e} />)}
            <div ref={bottomRef} />
          </>
        )}
      </div>

      {/* Status bar */}
      <div style={{
        padding: '6px 14px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)',
        display: 'flex', gap: 16, flexShrink: 0,
      }}>
        {mode === 'history' && logsQ.data && (
          <>
            <span>{filtered.length} / {allEntries.length} entries</span>
            <span>{logsQ.data.duration_ms}ms</span>
          </>
        )}
        {mode === 'live' && (
          <span>{filteredLive.length} lines (max {MAX_LIVE_LINES} in memory)</span>
        )}
      </div>

      {/* CSS for spin animation */}
      <style>{`@keyframes spin { to { transform: rotate(360deg); } }`}</style>
    </div>
  )
}
