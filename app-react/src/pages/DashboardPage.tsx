/**
 * pages/DashboardPage.tsx — D-PlaneOS Dashboard
 *
 * APIs:
 *   GET /api/system/metrics      → SystemMetrics
 *   GET /api/system/status       → version, uptime, ecc_warning
 *   GET /api/zfs/pools           → pools list
 *   GET /api/docker/containers   → containers list
 *   GET /api/system/ups          → UPS status
 *   WS  /ws/monitor              → live state_update events
 */

import { useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { api } from '@/lib/api'
import { useWsStore } from '@/stores/ws'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { Icon } from '@/components/ui/Icon'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface SystemMetrics {
  inotify: { used: number; limit: number; percent: number }
  memory:  { used: number; total: number; percent: number }
  arc:     { used: number; limit: number; percent: number }
  iowait:  number
}
interface SystemStatus {
  success: boolean; version: string; uptime_seconds: number
  ecc_warning: boolean; ecc_warning_msg: string
}
interface ZFSPool {
  name: string; size: string; alloc: string; free: string; health: string
}
interface PoolsResponse { success: boolean; data: ZFSPool[] }
interface DockerContainer { Id: string; Names: string[]; Image: string; State: string; Status: string }
interface ContainersResponse { success: boolean; containers: DockerContainer[]; total_containers: number }
interface UPSData { status: string; battery_charge: string; battery_runtime: string }
interface UPSResponse { success: boolean; data?: UPSData }
interface WsStateUpdate {
  memory?: { percent: number; used: number; total: number }
  iowait?: number; ups?: UPSData
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

function fmtUptime(s: number): string {
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

function pctColor(pct: number) {
  if (pct > 85) return 'var(--error)'
  if (pct > 70) return 'var(--warning)'
  return 'var(--primary)'
}

// ---------------------------------------------------------------------------
// MetricCard — glowing accent number with mini progress bar
// ---------------------------------------------------------------------------

interface MetricCardProps {
  icon: string; label: string; value: string; sub?: string
  percent?: number; loading?: boolean; onClick?: () => void
  accent?: string
}

function MetricCard({ icon, label, value, sub, percent, loading, onClick, accent }: MetricCardProps) {
  const [hov, setHov] = useState(false)
  const color = accent ?? (percent !== undefined ? pctColor(percent) : 'var(--primary)')

  return (
    <div
      onClick={onClick}
      onMouseEnter={() => setHov(true)}
      onMouseLeave={() => setHov(false)}
      style={{
        background: 'var(--bg-card)',
        border: `1px solid ${hov && onClick ? 'rgba(138,156,255,0.25)' : 'var(--border)'}`,
        borderRadius: 'var(--radius-xl)',
        padding: '20px 22px',
        cursor: onClick ? 'pointer' : 'default',
        transform: hov && onClick ? 'translateY(-2px)' : 'none',
        transition: 'all var(--transition-base)',
        position: 'relative', overflow: 'hidden',
      }}
    >
      {/* Subtle top-left glow */}
      <div style={{
        position: 'absolute', top: -20, left: -20,
        width: 80, height: 80, borderRadius: '50%',
        background: `radial-gradient(circle, ${color}18 0%, transparent 70%)`,
        pointerEvents: 'none',
      }} />

      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 14 }}>
        <div style={{
          width: 34, height: 34, borderRadius: 10,
          background: `${color}18`,
          border: `1px solid ${color}30`,
          display: 'flex', alignItems: 'center', justifyContent: 'center',
        }}>
          <Icon name={icon} size={18} style={{ color }} />
        </div>
        {onClick && <Icon name="open_in_new" size={14} style={{ color: 'var(--text-tertiary)', opacity: hov ? 1 : 0, transition: 'opacity 0.15s' }} />}
      </div>

      {loading ? (
        <>
          <Skeleton height={28} width="60%" style={{ marginBottom: 6 }} />
          <Skeleton height={11} width="45%" />
        </>
      ) : (
        <>
          <div style={{
            fontFamily: 'var(--font-mono)', fontSize: 26, fontWeight: 800,
            color, lineHeight: 1, marginBottom: 4,
            textShadow: `0 0 20px ${color}40`,
          }}>
            {value}
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 4 }}>{label}</div>
          {sub && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>{sub}</div>}
          {percent !== undefined && (
            <div style={{ height: 3, background: 'rgba(255,255,255,0.07)', borderRadius: 999, marginTop: 12, overflow: 'hidden' }}>
              <div style={{
                height: '100%', width: `${Math.min(percent, 100)}%`, borderRadius: 999,
                background: color, transition: 'width 0.6s ease',
                boxShadow: percent > 70 ? `0 0 6px ${color}` : 'none',
              }} />
            </div>
          )}
        </>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// SectionCard
// ---------------------------------------------------------------------------

function SectionCard({ title, icon, children, onAction, actionLabel }: {
  title: string; icon: string; children: React.ReactNode
  onAction?: () => void; actionLabel?: string
}) {
  return (
    <div style={{
      background: 'var(--bg-card)', border: '1px solid var(--border)',
      borderRadius: 'var(--radius-xl)', overflow: 'hidden',
    }}>
      <div style={{
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        padding: '16px 20px', borderBottom: '1px solid var(--border-subtle)',
      }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon name={icon} size={17} style={{ color: 'var(--primary)' }} />
          <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>{title}</span>
        </div>
        {onAction && (
          <button onClick={onAction} className="btn btn-ghost btn-xs" style={{ gap: 4 }}>
            <Icon name="open_in_new" size={12} />
            {actionLabel ?? 'View all'}
          </button>
        )}
      </div>
      <div style={{ padding: '12px 16px', display: 'flex', flexDirection: 'column', gap: 4 }}>
        {children}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// PoolRow
// ---------------------------------------------------------------------------

function PoolRow({ pool, onClick }: { pool: ZFSPool; onClick: () => void }) {
  const [hov, setHov] = useState(false)
  const isOnline = pool.health === 'ONLINE'
  const isDeg    = pool.health === 'DEGRADED'
  const badgeCls = isOnline ? 'badge-success' : isDeg ? 'badge-warning' : 'badge-error'

  // Parse alloc/size to get percent — daemon returns human strings like "1.23T"
  // Best effort: just show health badge
  return (
    <div
      onClick={onClick}
      onMouseEnter={() => setHov(true)}
      onMouseLeave={() => setHov(false)}
      style={{
        display: 'flex', alignItems: 'center', gap: 12,
        padding: '10px 12px', borderRadius: 'var(--radius-md)',
        background: hov ? 'var(--surface)' : 'transparent',
        cursor: 'pointer', transition: 'background var(--transition-fast)',
      }}
    >
      <div style={{
        width: 36, height: 36, borderRadius: 10,
        background: isOnline ? 'var(--success-bg)' : isDeg ? 'var(--warning-bg)' : 'var(--error-bg)',
        border: `1px solid ${isOnline ? 'var(--success-border)' : isDeg ? 'var(--warning-border)' : 'var(--error-border)'}`,
        display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0,
      }}>
        <Icon name="storage" size={18}
          style={{ color: isOnline ? 'var(--success)' : isDeg ? 'var(--warning)' : 'var(--error)' }} />
      </div>

      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)', marginBottom: 2 }}>{pool.name}</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)',
          fontFamily: 'var(--font-mono)' }}>
          {pool.alloc} used / {pool.size}
        </div>
      </div>

      <span className={`badge ${badgeCls}`}>{pool.health}</span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ContainerRow
// ---------------------------------------------------------------------------

function ContainerRow({ c, onClick }: { c: DockerContainer; onClick: () => void }) {
  const [hov, setHov] = useState(false)
  const name = c.Names[0]?.replace(/^\//, '') ?? c.Id.slice(0, 12)
  const running = c.State === 'running'

  return (
    <div
      onClick={onClick}
      onMouseEnter={() => setHov(true)}
      onMouseLeave={() => setHov(false)}
      style={{
        display: 'flex', alignItems: 'center', gap: 10,
        padding: '8px 12px', borderRadius: 'var(--radius-sm)',
        background: hov ? 'var(--surface)' : 'transparent',
        cursor: 'pointer', transition: 'background var(--transition-fast)',
      }}
    >
      <span className={`status-dot ${running ? 'online' : 'offline'}`} />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 500, fontSize: 'var(--text-sm)',
          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{name}</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)',
          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
          fontFamily: 'var(--font-mono)' }}>{c.Image}</div>
      </div>
      <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', flexShrink: 0 }}>
        {c.Status}
      </span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

import type React from 'react'

export function DashboardPage() {
  const navigate  = useNavigate()
  const wsOn      = useWsStore((s) => s.on)
  const [live, setLive] = useState<WsStateUpdate | null>(null)

  useEffect(() => wsOn('stateUpdate', (d) => setLive(d as WsStateUpdate)), [wsOn])

  const metricsQ    = useQuery({ queryKey: ['system','metrics'],     queryFn: ({ signal }) => api.get<SystemMetrics>('/api/system/metrics', signal),     refetchInterval: 30_000 })
  const statusQ     = useQuery({ queryKey: ['system','status'],      queryFn: ({ signal }) => api.get<SystemStatus>('/api/system/status', signal),       refetchInterval: 60_000 })
  const poolsQ      = useQuery({ queryKey: ['zfs','pools'],          queryFn: ({ signal }) => api.get<PoolsResponse>('/api/zfs/pools', signal),           refetchInterval: 30_000 })
  const containersQ = useQuery({ queryKey: ['docker','containers'],  queryFn: ({ signal }) => api.get<ContainersResponse>('/api/docker/containers', signal), refetchInterval: 30_000 })
  const upsQ        = useQuery({ queryKey: ['system','ups'],         queryFn: ({ signal }) => api.get<UPSResponse>('/api/system/ups', signal),            refetchInterval: 60_000 })

  const memPct   = live?.memory?.percent ?? metricsQ.data?.memory.percent   ?? 0
  const memUsed  = live?.memory?.used    ?? metricsQ.data?.memory.used      ?? 0
  const memTotal = live?.memory?.total   ?? metricsQ.data?.memory.total     ?? 0
  const iowait   = live?.iowait          ?? metricsQ.data?.iowait           ?? 0
  const pools    = poolsQ.data?.data     ?? []
  const containers = containersQ.data?.containers ?? []
  const running  = containers.filter((c) => c.State === 'running')
  const ups      = live?.ups ?? upsQ.data?.data
  const upsAlert = ups && ups.status !== 'OL' && ups.status !== 'ONLINE'

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20, maxWidth: 1400 }}>

      {/* ECC warning */}
      {statusQ.data?.ecc_warning && (
        <div className="alert alert-warning">
          <Icon name="warning" size={16} />
          {statusQ.data.ecc_warning_msg || 'Non-ECC RAM detected — ZFS data integrity risk.'}
        </div>
      )}

      {/* UPS alert */}
      {upsAlert && ups && (
        <div
          className={`alert ${parseInt(ups.battery_charge) < 20 ? 'alert-error' : 'alert-warning'}`}
          onClick={() => navigate({ to: '/ups' })}
          style={{ cursor: 'pointer' }}
        >
          <Icon name="battery_alert" size={16} />
          <span>UPS on battery — {ups.battery_charge}% charge · runtime {ups.battery_runtime}</span>
          <span style={{ marginLeft: 'auto', fontSize: 'var(--text-xs)', opacity: 0.7 }}>View UPS →</span>
        </div>
      )}

      {/* Metric cards */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: 14 }}>
        <MetricCard
          icon="memory" label="Memory"
          value={metricsQ.isLoading ? '…' : `${Math.round(memPct)}%`}
          sub={!metricsQ.isLoading ? `${fmtBytes(memUsed)} / ${fmtBytes(memTotal)}` : undefined}
          percent={memPct} loading={metricsQ.isLoading}
          onClick={() => navigate({ to: '/reporting' })} />

        <MetricCard
          icon="dns" label="ZFS ARC"
          value={metricsQ.isLoading ? '…' : `${Math.round(metricsQ.data?.arc.percent ?? 0)}%`}
          sub={!metricsQ.isLoading ? `${fmtBytes(metricsQ.data?.arc.used ?? 0)} / ${fmtBytes(metricsQ.data?.arc.limit ?? 0)}` : undefined}
          percent={metricsQ.data?.arc.percent} loading={metricsQ.isLoading}
          accent="var(--info)"
          onClick={() => navigate({ to: '/reporting' })} />

        <MetricCard
          icon="speed" label="I/O Wait"
          value={metricsQ.isLoading ? '…' : `${iowait}%`}
          percent={iowait} loading={metricsQ.isLoading}
          accent={iowait > 20 ? 'var(--warning)' : 'var(--success)'}
          onClick={() => navigate({ to: '/reporting' })} />

        <MetricCard
          icon="storage" label="Pools"
          value={poolsQ.isLoading ? '…' : String(pools.length)}
          sub={!poolsQ.isLoading ? `${pools.filter(p => p.health === 'ONLINE').length} online` : undefined}
          loading={poolsQ.isLoading}
          accent={pools.some(p => p.health !== 'ONLINE') ? 'var(--warning)' : 'var(--success)'}
          onClick={() => navigate({ to: '/pools' })} />

        <MetricCard
          icon="deployed_code" label="Containers"
          value={containersQ.isLoading ? '…' : String(running.length)}
          sub={!containersQ.isLoading ? `${containers.length} total` : undefined}
          loading={containersQ.isLoading}
          onClick={() => navigate({ to: '/docker' })} />

        {statusQ.isLoading
          ? <MetricCard icon="timer" label="Uptime" value="…" loading />
          : statusQ.data
            ? <MetricCard icon="timer" label="Uptime"
                value={fmtUptime(statusQ.data.uptime_seconds)}
                sub={`v${statusQ.data.version}`}
                accent="var(--text-secondary)" />
            : null}
      </div>

      {/* Errors */}
      {metricsQ.isError && (
        <ErrorState error={metricsQ.error} title="Failed to load system metrics" onRetry={() => metricsQ.refetch()} />
      )}

      {/* Pools + Containers */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(340px, 1fr))', gap: 16 }}>
        <SectionCard title="Storage Pools" icon="database" onAction={() => navigate({ to: '/pools' })}>
          {poolsQ.isLoading && [1,2].map(k => <Skeleton key={k} height={56} borderRadius="var(--radius-md)" />)}
          {poolsQ.isError && <ErrorState error={poolsQ.error} onRetry={() => poolsQ.refetch()} />}
          {!poolsQ.isLoading && !poolsQ.isError && pools.length === 0 && (
            <div style={{ padding: '24px 0', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
              No ZFS pools configured
            </div>
          )}
          {pools.map(p => <PoolRow key={p.name} pool={p} onClick={() => navigate({ to: '/pools' })} />)}
        </SectionCard>

        <SectionCard title="Running Containers" icon="deployed_code" onAction={() => navigate({ to: '/docker' })}>
          {containersQ.isLoading && [1,2,3].map(k => <Skeleton key={k} height={44} borderRadius="var(--radius-sm)" />)}
          {containersQ.isError && <ErrorState error={containersQ.error} onRetry={() => containersQ.refetch()} />}
          {!containersQ.isLoading && !containersQ.isError && running.length === 0 && (
            <div style={{ padding: '24px 0', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
              No running containers
            </div>
          )}
          {running.slice(0, 8).map(c => <ContainerRow key={c.Id} c={c} onClick={() => navigate({ to: '/docker' })} />)}
          {running.length > 8 && (
            <div
              onClick={() => navigate({ to: '/docker' })}
              style={{ fontSize: 'var(--text-xs)', color: 'var(--primary)', textAlign: 'center',
                cursor: 'pointer', paddingTop: 6, paddingBottom: 4 }}
            >
              +{running.length - 8} more →
            </div>
          )}
        </SectionCard>
      </div>

      {/* inotify warning */}
      {metricsQ.data && metricsQ.data.inotify.percent > 70 && (
        <div
          className="alert alert-warning"
          onClick={() => navigate({ to: '/monitoring' })}
          style={{ cursor: 'pointer' }}
        >
          <Icon name="notifications_active" size={16} />
          inotify at {Math.round(metricsQ.data.inotify.percent)}% capacity
          ({metricsQ.data.inotify.used.toLocaleString()} / {metricsQ.data.inotify.limit.toLocaleString()} watches)
          <span style={{ marginLeft: 'auto', fontSize: 'var(--text-xs)', opacity: 0.7 }}>View Monitoring →</span>
        </div>
      )}
    </div>
  )
}
