/**
 * pages/ReportingPage.tsx - System Reporting & Metrics
 *
 * APIs:
 *   GET /api/metrics/history?period=hour|day|week  → history data points
 *   GET /api/system/metrics                        → current snapshot
 *   GET /api/system/health                         → filesystem RO, NTP status
 *   WS  /ws/monitor                                → live state updates
 */

import { useQuery } from '@tanstack/react-query'
import { useState, useEffect } from 'react'
import { api } from '@/lib/api'
import { useWsStore } from '@/stores/ws'
import { ErrorState } from '@/components/ui/ErrorState'
import { LoadingState, Skeleton } from '@/components/ui/LoadingSpinner'
import { Icon } from '@/components/ui/Icon'


// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type Period = 'hour' | 'day' | 'week'

interface HistoryPoint {
  ts: number
  load1?: string
  load5?: string
  load15?: string
  mem_total?: string
  mem_avail?: string
  arc_used?: string
  arc_limit?: string
  iowait?: number
}

interface MetricsHistory {
  success: boolean
  period:  string
  history: HistoryPoint[]
}

interface SystemMetrics {
  memory:  { used: number; total: number; percent: number }
  arc:     { used: number; limit: number; percent: number }
  iowait:  number
  inotify: { used: number; limit: number; percent: number }
}

interface SystemHealth {
  success:       boolean
  ro_partitions: string[]
  filesystem_ro: boolean
  ntp_synced:    boolean
  ntp_offset:    string
  checked_at:    string
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fmtBytes(b: number): string {
  if (b <= 0) return '0 B'
  const u = ['B','KB','MB','GB','TB']
  const i = Math.min(Math.floor(Math.log(b) / Math.log(1024)), u.length - 1)
  return `${(b / Math.pow(1024, i)).toFixed(1)} ${u[i]}`
}

function fmtTs(ts: number, period: Period): string {
  const d = new Date(ts * 1000)
  if (period === 'hour') return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (period === 'day')  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  return d.toLocaleDateString([], { month: 'short', day: 'numeric' })
}

function memPercent(p: HistoryPoint): number {
  const total = parseInt(p.mem_total ?? '0', 10)
  const avail = parseInt(p.mem_avail ?? '0', 10)
  if (!total) return 0
  return ((total - avail) / total) * 100
}

function arcPercent(p: HistoryPoint): number {
  const limit = parseInt(p.arc_limit ?? '0', 10)
  const used  = parseInt(p.arc_used  ?? '0', 10)
  if (!limit) return 0
  return (used / limit) * 100
}

function load1Val(p: HistoryPoint): number {
  return parseFloat(p.load1 ?? '0')
}

// ---------------------------------------------------------------------------
// Tiny inline sparkline chart (SVG, no external library)
// ---------------------------------------------------------------------------

interface SparklineProps {
  points: number[]
  color:  string
  height: number
  width:  number
  fill?:  boolean
}

function Sparkline({ points, color, height, width, fill = true }: SparklineProps) {
  if (points.length < 2) return null
  const max = Math.max(...points, 1)
  const xs = points.map((_, i) => (i / (points.length - 1)) * width)
  const ys = points.map((v) => height - (v / max) * height)
  const linePath = xs.map((x, i) => `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${ys[i].toFixed(1)}`).join(' ')
  const fillPath = `${linePath} L${width},${height} L0,${height} Z`

  return (
    <svg width={width} height={height} style={{ display: 'block', overflow: 'visible' }}>
      {fill && (
        <path d={fillPath} fill={color} fillOpacity={0.15} />
      )}
      <path d={linePath} stroke={color} strokeWidth={1.5} fill="none" strokeLinejoin="round" />
    </svg>
  )
}

// ---------------------------------------------------------------------------
// MetricPanel
// ---------------------------------------------------------------------------

interface MetricPanelProps {
  label:   string
  icon:    string
  current: number
  unit:    string
  sub:     string
  history: number[]
  color:   string
  warn?:   boolean
}

function MetricPanel({ label, icon, current, unit, sub, history, color, warn }: MetricPanelProps) {
  return (
    <div style={{
      background: 'var(--bg-card)', border: `1px solid ${warn ? 'var(--warning-border)' : 'var(--border)'}`,
      borderRadius: 'var(--radius-xl)', padding: 24}}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon name={icon} size={18} style={{ color }} />
          <span style={{ fontWeight: 600, fontSize: 'var(--text-md)' }}>{label}</span>
        </div>
        {warn && <Icon name="warning" size={16} style={{ color: 'var(--warning)' }} />}
      </div>

      <div style={{ display: 'flex', alignItems: 'flex-end', justifyContent: 'space-between', marginBottom: 12 }}>
        <div>
          <div style={{ fontSize: 32, fontWeight: 700, fontFamily: 'var(--font-mono)', lineHeight: 1 }}>
            {Math.round(current)}{unit}
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>{sub}</div>
        </div>
        <div style={{ opacity: 0.8 }}>
          <Sparkline points={history} color={color} height={48} width={120} />
        </div>
      </div>

      {/* Progress bar */}
      <div style={{ height: 4, background: 'rgba(255,255,255,0.08)', borderRadius: 2, overflow: 'hidden' }}>
        <div style={{
          height: '100%', width: `${Math.min(current, 100)}%`,
          background: current > 85 ? 'var(--error)' : current > 70 ? 'var(--warning)' : color,
          borderRadius: 2, transition: 'width 0.5s'}} />
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Period selector
// ---------------------------------------------------------------------------

function PeriodTab({ period, current, onChange }: { period: Period; current: Period; onChange: (p: Period) => void }) {
  const active = period === current
  return (
    <button onClick={() => onChange(period)} style={{
      padding: '6px 14px', background: active ? 'var(--primary-bg)' : 'none',
      border: `1px solid ${active ? 'var(--primary)' : 'var(--border)'}`,
      borderRadius: 'var(--radius-sm)', color: active ? 'var(--primary)' : 'var(--text-secondary)',
      cursor: 'pointer', fontFamily: 'var(--font-ui)', fontSize: 'var(--text-sm)', fontWeight: active ? 600 : 400}}>
      {period === 'hour' ? '1 Hour' : period === 'day' ? '24 Hours' : '7 Days'}
    </button>
  )
}

// ---------------------------------------------------------------------------
// History chart row
// ---------------------------------------------------------------------------

function HistoryChart({ label, color, dataPoints, period, extractFn }: {
  label: string; color: string; dataPoints: HistoryPoint[]
  period: Period; extractFn: (p: HistoryPoint) => number
}) {
  if (dataPoints.length === 0) {
    return (
      <div style={{ padding: '12px 0', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
        No history data for this period
      </div>
    )
  }

  const values = dataPoints.map(extractFn)
  const max = Math.max(...values, 1)
  const chartH = 80
  const chartW = 600
  const pts = values.map((v, i) => ({
    x: (i / Math.max(values.length - 1, 1)) * chartW,
    y: chartH - (v / max) * chartH,
    v,
    ts: dataPoints[i].ts,
  }))

  const linePath = pts.map((p, i) => `${i === 0 ? 'M' : 'L'}${p.x.toFixed(1)},${p.y.toFixed(1)}`).join(' ')
  const fillPath = `${linePath} L${chartW},${chartH} L0,${chartH} Z`

  // Sample labels: show 6 evenly spaced
  const labelCount = 6
  const labelIdxs = Array.from({ length: labelCount }, (_, i) =>
    Math.round((i / (labelCount - 1)) * (dataPoints.length - 1))
  )

  return (
    <div>
      <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, marginBottom: 8, color }}>{label}</div>
      <div style={{ overflowX: 'auto' }}>
        <svg width={chartW} height={chartH + 20} style={{ display: 'block', minWidth: 300 }}>
          <defs>
            <linearGradient id={`grad-${label.replace(/\s/g,'')}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={color} stopOpacity="0.3" />
              <stop offset="100%" stopColor={color} stopOpacity="0" />
            </linearGradient>
          </defs>
          <path d={fillPath} fill={`url(#grad-${label.replace(/\s/g,'')})`} />
          <path d={linePath} stroke={color} strokeWidth={1.5} fill="none" strokeLinejoin="round" />
          {/* Axis labels */}
          {labelIdxs.map((idx) => (
            <text key={idx} x={pts[idx]?.x ?? 0} y={chartH + 14}
              textAnchor="middle" fontSize={10} fill="rgba(255,255,255,0.35)">
              {fmtTs(dataPoints[idx]?.ts ?? 0, period)}
            </text>
          ))}
        </svg>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Health panel
// ---------------------------------------------------------------------------

function HealthPanel({ health }: { health: SystemHealth }) {
  const ok = !health.filesystem_ro && health.ntp_synced

  return (
    <div style={{
      background: 'var(--bg-card)', border: `1px solid ${ok ? 'var(--border)' : 'var(--warning-border)'}`,
      borderRadius: 'var(--radius-xl)', padding: 24}}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16 }}>
        <Icon name={ok ? 'check_circle' : 'warning'} size={18} style={{ color: ok ? 'var(--success)' : 'var(--warning)' }} />
        <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>System Health</span>
        <span style={{ marginLeft: 'auto', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
          Checked {new Date(health.checked_at).toLocaleTimeString()}
        </span>
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        <HealthRow
          label="Filesystem" icon="folder"
          ok={!health.filesystem_ro}
          value={health.filesystem_ro
            ? `Read-only: ${health.ro_partitions.join(', ')}`
            : 'All partitions read-write'}
        />
        <HealthRow
          label="NTP Sync" icon="schedule"
          ok={health.ntp_synced}
          value={health.ntp_synced ? 'Clock synchronised' : 'Clock not synchronised'}
        />
      </div>
    </div>
  )
}

function HealthRow({ label, icon, ok, value }: { label: string; icon: string; ok: boolean; value: string }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '8px 12px',
      background: 'rgba(255,255,255,0.02)', borderRadius: 'var(--radius-sm)' }}>
      <Icon name={icon} size={16} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
      <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', width: 100, flexShrink: 0 }}>{label}</span>
      <span style={{ flex: 1, fontSize: 'var(--text-sm)' }}>{value}</span>
      <Icon name={ok ? 'check_circle' : 'error'} size={16}
        style={{ color: ok ? 'var(--success)' : 'var(--error)', flexShrink: 0 }} />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Main page
// ---------------------------------------------------------------------------

export function ReportingPage() {
  const [period, setPeriod] = useState<Period>('day')
  const wsOn = useWsStore((s) => s.on)
  const [liveMetrics, setLiveMetrics] = useState<Partial<SystemMetrics> | null>(null)

  useEffect(() => {
    return wsOn('stateUpdate', (data) => setLiveMetrics(data as Partial<SystemMetrics>))
  }, [wsOn])

  const historyQ = useQuery({
    queryKey: ['metrics', 'history', period],
    queryFn: ({ signal }) => api.get<MetricsHistory>(`/api/metrics/history?period=${period}`, signal),
  })

  const currentQ = useQuery({
    queryKey: ['system', 'metrics'],
    queryFn: ({ signal }) => api.get<SystemMetrics>('/api/system/metrics', signal),
    refetchInterval: 30_000,
  })

  const healthQ = useQuery({
    queryKey: ['system', 'health'],
    queryFn: ({ signal }) => api.get<SystemHealth>('/api/system/health', signal),
    refetchInterval: 120_000,
  })

  const history = historyQ.data?.history ?? []

  // Current values - prefer live WS
  const memPct  = liveMetrics?.memory?.percent ?? currentQ.data?.memory.percent ?? 0
  const arcPct  = liveMetrics?.arc?.percent    ?? currentQ.data?.arc.percent    ?? 0
  const iowait  = liveMetrics?.iowait          ?? currentQ.data?.iowait         ?? 0
  const inotify = currentQ.data?.inotify

  // History series
  const memHistory   = history.map(memPercent)
  const arcHistory   = history.map(arcPercent)
  const load1History = history.map(load1Val)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24, maxWidth: 1200 }}>

      <div className="page-header">
        <h1 className="page-title">Reporting</h1>
        <p className="page-subtitle">System metrics history and performance trends</p>
      </div>

      {/* Current metrics row */}
      {currentQ.isLoading ? (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 18 }}>
          {[1,2,3,4].map(k => <Skeleton key={k} height={140} borderRadius="var(--radius-xl)" />)}
        </div>
      ) : currentQ.isError ? (
        <ErrorState error={currentQ.error} onRetry={() => currentQ.refetch()} />
      ) : (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 18 }}>
          <MetricPanel label="Memory" icon="developer_board" current={memPct} unit="%" color="var(--primary)"
            sub={`${fmtBytes(currentQ.data?.memory.used ?? 0)} / ${fmtBytes(currentQ.data?.memory.total ?? 0)}`}
            history={memHistory} warn={memPct > 85} />

          <MetricPanel label="ZFS ARC" icon="dns" current={arcPct} unit="%" color="#8b5cf6"
            sub={`${fmtBytes(currentQ.data?.arc.used ?? 0)} / ${fmtBytes(currentQ.data?.arc.limit ?? 0)}`}
            history={arcHistory} />

          <MetricPanel label="Load Average" icon="speed" current={parseFloat(history[history.length-1]?.load1 ?? '0')} unit=""
            color="#06b6d4"
            sub={`5m: ${history[history.length-1]?.load5 ?? '-'}  15m: ${history[history.length-1]?.load15 ?? '-'}`}
            history={load1History} />

          <MetricPanel label="I/O Wait" icon="storage" current={iowait} unit="%" color="#f59e0b"
            sub="CPU time waiting for disk I/O"
            history={history.map((p: HistoryPoint) => p.iowait ?? 0)} warn={iowait > 20} />

          {inotify && (
            <MetricPanel label="inotify Watches" icon="notifications_active"
              current={inotify.percent} unit="%"
              color="var(--success)"
              sub={`${inotify.used.toLocaleString()} / ${inotify.limit.toLocaleString()}`}
              history={[]} warn={inotify.percent > 70} />
          )}
        </div>
      )}

      {/* History charts */}
      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 20 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Icon name="monitoring" size={18} style={{ color: 'var(--primary)' }} />
            <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>History</span>
          </div>
          <div style={{ display: 'flex', gap: 8 }}>
            {(['hour','day','week'] as Period[]).map(p => (
              <PeriodTab key={p} period={p} current={period} onChange={setPeriod} />
            ))}
          </div>
        </div>

        {historyQ.isLoading && <LoadingState message="Loading history…" />}
        {historyQ.isError && <ErrorState error={historyQ.error} onRetry={() => historyQ.refetch()} />}
        {!historyQ.isLoading && !historyQ.isError && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 28 }}>
            <HistoryChart label="Memory %" color="var(--primary)"
              dataPoints={history} period={period} extractFn={memPercent} />
            <HistoryChart label="ZFS ARC %" color="#8b5cf6"
              dataPoints={history} period={period} extractFn={arcPercent} />
            <HistoryChart label="Load Average (1m)" color="#06b6d4"
              dataPoints={history} period={period} extractFn={load1Val} />
            <HistoryChart label="I/O Wait %" color="#f59e0b"
              dataPoints={history} period={period} extractFn={p => p.iowait ?? 0} />
          </div>
        )}
      </div>

      {/* System health */}
      {healthQ.isLoading && <Skeleton height={160} borderRadius="var(--radius-xl)" />}
      {healthQ.isError && <ErrorState error={healthQ.error} onRetry={() => healthQ.refetch()} />}
      {healthQ.data && <HealthPanel health={healthQ.data} />}
    </div>
  )
}

