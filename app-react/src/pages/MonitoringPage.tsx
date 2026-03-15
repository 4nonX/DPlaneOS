/**
 * pages/MonitoringPage.tsx — System Monitoring
 *
 * APIs:
 *   GET /api/monitoring/inotify  → InotifyStats (poll, initial load)
 *   WS  /ws/monitor              → event: "inotify_status" { data: InotifyStats, level: string }
 *
 * Daemon emits "inotify_status" WS events whenever the background monitor
 * runs (every ~60s) with level: "info" | "warning" | "critical" | "clear".
 * The page subscribes to live updates and overrides the polled data.
 */

import { useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { api } from '@/lib/api'
import { useWsStore } from '@/stores/ws'
import { ErrorState } from '@/components/ui/ErrorState'
import { LoadingState } from '@/components/ui/LoadingSpinner'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface InotifyStats {
  used:     number
  limit:    number
  percent:  number
  warning:  boolean
  critical: boolean
}

interface WsInotifyEvent {
  data:  InotifyStats
  level: 'info' | 'warning' | 'critical' | 'clear'
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function statusColor(stats: InotifyStats): string {
  if (stats.critical) return 'var(--error)'
  if (stats.warning)  return 'var(--warning)'
  return 'var(--success)'
}

function statusLabel(stats: InotifyStats): string {
  if (stats.critical) return 'CRITICAL'
  if (stats.warning)  return 'WARNING'
  return 'OK'
}

function statusIcon(stats: InotifyStats): string {
  if (stats.critical) return 'error'
  if (stats.warning)  return 'warning'
  return 'check_circle'
}

// ---------------------------------------------------------------------------
// InotifyCard
// ---------------------------------------------------------------------------

interface InotifyCardProps {
  stats:       InotifyStats
  lastUpdated: Date | null
}

function InotifyCard({ stats, lastUpdated }: InotifyCardProps) {
  const color   = statusColor(stats)
  const label   = statusLabel(stats)
  const icon    = statusIcon(stats)
  const barPct  = Math.min(stats.percent, 100)
  const barColor = stats.critical ? 'var(--error)' : stats.warning ? 'var(--warning)' : 'var(--success)'
  const available = stats.limit - stats.used

  return (
    <div style={{
      background: 'var(--bg-card)',
      border: `1px solid ${stats.critical ? 'var(--error-border)' : stats.warning ? 'var(--warning-border)' : 'var(--border)'}`,
      borderRadius: 'var(--radius-xl)',
      padding: 28,
      maxWidth: 560}}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <Icon name="notifications_active" size={22} style={{ color: 'var(--primary)' }} />
          <span style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>Inotify File Watches</span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 6,
          padding: '4px 10px', borderRadius: 'var(--radius-full)',
          background: stats.critical ? 'var(--error-bg)' : stats.warning ? 'var(--warning-bg)' : 'var(--success-bg)',
          border: `1px solid ${stats.critical ? 'var(--error-border)' : stats.warning ? 'var(--warning-border)' : 'var(--success-border)'}`}}>
          <Icon name={icon} size={14} style={{ color }} />
          <span style={{ fontSize: 'var(--text-xs)', fontWeight: 700, color, letterSpacing: '0.5px' }}>{label}</span>
        </div>
      </div>

      {/* Big percent */}
      <div style={{ fontFamily: 'var(--font-mono)', fontSize: 52, fontWeight: 800, lineHeight: 1,
        color, marginBottom: 4 }}>
        {stats.percent.toFixed(1)}%
      </div>
      <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)', marginBottom: 20 }}>
        of system inotify watch limit
      </div>

      {/* Progress bar */}
      <div style={{ height: 8, background: 'rgba(255,255,255,0.08)', borderRadius: 4,
        overflow: 'hidden', marginBottom: 20 }}>
        <div style={{
          height: '100%', width: `${barPct}%`,
          background: barColor, borderRadius: 4,
          transition: 'width 0.6s ease',
          boxShadow: stats.critical || stats.warning ? `0 0 8px ${barColor}` : 'none'}} />
      </div>

      {/* Stats grid */}
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 16, marginBottom: 20 }}>
        {[
          { label: 'Used',      value: stats.used.toLocaleString(),      icon: 'visibility' },
          { label: 'Limit',     value: stats.limit.toLocaleString(),     icon: 'fence' },
          { label: 'Available', value: available.toLocaleString(),       icon: 'check_circle' },
        ].map(({ label, value, icon: ic }) => (
          <div key={label} style={{ background: 'rgba(255,255,255,0.03)',
            border: '1px solid var(--border-subtle)', borderRadius: 'var(--radius-md)',
            padding: '12px 14px' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 5, marginBottom: 6 }}>
              <Icon name={ic} size={14} style={{ color: 'var(--text-tertiary)' }} />
              <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontWeight: 500 }}>{label}</span>
            </div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-md)', fontWeight: 700 }}>{value}</div>
          </div>
        ))}
      </div>

      {/* Advisory messages */}
      {stats.warning && !stats.critical && (
        <div className="alert alert-warning" style={{ marginBottom: 12 }}>
          <strong>Watches at {stats.percent.toFixed(1)}%.</strong> Real-time file monitoring may
          fail soon. Consider increasing <code style={{ fontFamily: 'var(--font-mono)',
            background: 'rgba(0,0,0,0.2)', padding: '1px 4px', borderRadius: 2 }}>
            fs.inotify.max_user_watches</code> in <code style={{ fontFamily: 'var(--font-mono)',
            background: 'rgba(0,0,0,0.2)', padding: '1px 4px', borderRadius: 2 }}>
            /etc/sysctl.conf</code>.
        </div>
      )}
      {stats.critical && (
        <div className="alert alert-error" style={{ marginBottom: 12 }}>
          <strong>Inotify exhaustion imminent.</strong> File indexing may fail silently.
          Immediately increase <code style={{ fontFamily: 'var(--font-mono)',
            background: 'rgba(0,0,0,0.2)', padding: '1px 4px', borderRadius: 2 }}>
            fs.inotify.max_user_watches</code> or disable file watching services.
        </div>
      )}

      {/* Sysctl fix hint */}
      {(stats.warning || stats.critical) && (
        <div style={{ background: 'rgba(255,255,255,0.03)', border: '1px solid var(--border-subtle)',
          borderRadius: 'var(--radius-md)', padding: '10px 14px' }}>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 6 }}>
            Recommended fix:
          </div>
          <pre style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)',
            color: 'var(--success)', margin: 0, userSelect: 'all' }}>
{`echo "fs.inotify.max_user_watches=524288" >> /etc/sysctl.conf
sysctl -p`}
          </pre>
        </div>
      )}

      {/* Last updated */}
      <div style={{ marginTop: 16, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)',
        display: 'flex', alignItems: 'center', gap: 6 }}>
        <span style={{ width: 6, height: 6, borderRadius: '50%',
          background: 'var(--success)', display: 'inline-block',
          boxShadow: '0 0 5px var(--success)' }} />
        Live via WebSocket
        {lastUpdated && (
          <span style={{ marginLeft: 4 }}>
            · Last update {lastUpdated.toLocaleTimeString()}
          </span>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function MonitoringPage() {
  const wsOn = useWsStore((s) => s.on)
  const [liveStats, setLiveStats] = useState<InotifyStats | null>(null)
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null)

  const statsQ = useQuery({
    queryKey: ['monitoring', 'inotify'],
    queryFn: ({ signal }) => api.get<InotifyStats>('/api/monitoring/inotify', signal),
    refetchInterval: 60_000,
  })

  // Subscribe to live WS events
  useEffect(() => {
    return wsOn('inotifyStats', (event) => {
      const e = event as WsInotifyEvent
      setLiveStats(e.data)
      setLastUpdated(new Date())
      // Toast only on level changes that need attention
      if (e.level === 'critical') {
        toast.error(`inotify at ${e.data.percent.toFixed(1)}% — exhaustion imminent`)
      } else if (e.level === 'warning') {
        toast.warning(`inotify watches at ${e.data.percent.toFixed(1)}%`)
      }
    })
  }, [wsOn])

  const stats = liveStats ?? statsQ.data ?? null

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24, maxWidth: 900 }}>

      <div className="page-header">
        <h1 className="page-title">Monitoring</h1>
        <p className="page-subtitle">Kernel resource limits and real-time watchdog status</p>
      </div>

      {/* Explanatory header */}
      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: '20px 24px',
        display: 'flex', alignItems: 'flex-start', gap: 14 }}>
        <Icon name="info" size={20} style={{ color: 'var(--primary)', flexShrink: 0, marginTop: 2 }} />
        <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', lineHeight: 1.6 }}>
          <strong style={{ color: 'var(--text)' }}>inotify</strong> is the Linux kernel mechanism
          used for real-time file change notifications. Services like Syncthing, Immich, and
          Docker use it extensively. Exhaustion causes silent failures in file watching and
          indexing. ZFS itself does not use inotify, but services running on top of it do.
        </div>
      </div>

      {/* Stats card */}
      {statsQ.isLoading && !liveStats && <LoadingState message="Loading inotify stats…" />}
      {statsQ.isError && !liveStats && (
        <ErrorState error={statsQ.error} title="Failed to load inotify stats" onRetry={() => statsQ.refetch()} />
      )}
      {stats && <InotifyCard stats={stats} lastUpdated={lastUpdated} />}

      {/* Prometheus note */}
      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: '20px 24px',
        display: 'flex', alignItems: 'center', gap: 14 }}>
        <Icon name="monitoring" size={20} style={{ color: 'var(--primary)', flexShrink: 0 }} />
        <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          Prometheus metrics are available at{' '}
          <code style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)',
            background: 'rgba(255,255,255,0.06)', padding: '2px 6px', borderRadius: 3 }}>
            GET /metrics
          </code>
          {' '}(unauthenticated, for scraping).
        </div>
      </div>
    </div>
  )
}
