/**
 * pages/DashboardPage.tsx — D-PlaneOS Dashboard
 *
 * Calls (matching daemon routes exactly):
 *   GET /api/system/metrics      → SystemMetrics
 *   GET /api/system/status       → version, uptime, ecc_warning
 *   GET /api/zfs/pools            → pools list
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
import type React from 'react'

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
  success:        boolean
  version:        string
  uptime_seconds: number
  ecc_warning:    boolean
  ecc_warning_msg:string
}

interface ZFSPool {
  name:   string
  size:   string
  alloc:  string
  free:   string
  health: string
}

interface PoolsResponse {
  success: boolean
  data:    ZFSPool[]
}

interface DockerContainer {
  Id:    string
  Names: string[]
  Image: string
  State: string
  Status:string
}

interface ContainersResponse {
  success:          boolean
  containers:       DockerContainer[]
  total_containers: number
}

interface UPSData {
  status:          string
  battery_charge:  string
  battery_runtime: string
}

interface UPSResponse {
  success: boolean
  data?:   UPSData
}

interface WsStateUpdate {
  memory?: { percent: number; used: number; total: number }
  iowait?: number
  ups?:    UPSData
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fmtBytes(b: number): string {
  if (b <= 0) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(b) / Math.log(1024)), u.length - 1)
  return `${(b / Math.pow(1024, i)).toFixed(1)} ${u[i]}`
}

function fmtUptime(s: number): string {
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

// ---------------------------------------------------------------------------
// StatCard
// ---------------------------------------------------------------------------

interface StatCardProps {
  icon: string; label: string; value: string; sub?: string
  percent?: number; loading?: boolean; onClick?: () => void
}

function StatCard({ icon, label, value, sub, percent, loading, onClick }: StatCardProps) {
  const [hovered, setHovered] = useState(false)
  return (
    <div
      onClick={onClick}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => setHovered(false)}
      style={{
        background: 'var(--bg-card)',
        border: `1px solid ${hovered && onClick ? 'rgba(138,156,255,0.4)' : 'var(--border)'}`,
        borderRadius: 'var(--radius-xl)',
        padding: 24,
        cursor: onClick ? 'pointer' : 'default',
        transform: hovered && onClick ? 'translateY(-2px)' : 'none',
        transition: 'border-color 0.2s, transform 0.2s',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12 }}>
        <Icon name={icon} size={22} style={{ color: 'var(--primary)' }} />
        <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', fontWeight: 500 }}>{label}</span>
      </div>
      {loading ? (
        <>
          <Skeleton height={30} width="55%" style={{ marginBottom: 6 }} />
          <Skeleton height={12} width="40%" />
        </>
      ) : (
        <>
          <div style={{ fontSize: 26, fontWeight: 700, marginBottom: 4, fontFamily: 'var(--font-mono)' }}>{value}</div>
          {sub && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{sub}</div>}
          {percent !== undefined && (
            <div style={{ height: 4, background: 'rgba(255,255,255,0.08)', borderRadius: 2, marginTop: 10, overflow: 'hidden' }}>
              <div style={{
                height: '100%',
                width: `${Math.min(percent, 100)}%`,
                background: percent > 85 ? 'var(--error)' : percent > 70 ? 'var(--warning)' : 'var(--primary)',
                borderRadius: 2, transition: 'width 0.5s ease',
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
      borderRadius: 'var(--radius-xl)', padding: 24,
      display: 'flex', flexDirection: 'column', gap: 14,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon name={icon} size={18} style={{ color: 'var(--primary)' }} />
          <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>{title}</span>
        </div>
        {onAction && (
          <button onClick={onAction} style={{
            background: 'var(--surface)', border: '1px solid var(--border)',
            borderRadius: 'var(--radius-sm)', padding: '4px 10px',
            fontSize: 'var(--text-xs)', color: 'var(--text-secondary)',
            cursor: 'pointer', fontFamily: 'var(--font-ui)',
            display: 'flex', alignItems: 'center', gap: 4,
          }}>
            <Icon name="open_in_new" size={12} />
            {actionLabel ?? 'View all'}
          </button>
        )}
      </div>
      {children}
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
  return (
    <div onClick={onClick} onMouseEnter={() => setHov(true)} onMouseLeave={() => setHov(false)}
      style={{
        display: 'flex', alignItems: 'center', gap: 14,
        padding: '12px 14px', background: hov ? 'var(--surface)' : 'rgba(255,255,255,0.02)',
        border: '1px solid var(--border-subtle)', borderRadius: 'var(--radius-md)',
        cursor: 'pointer', transition: 'background 0.15s',
      }}>
      <Icon name="storage" size={26} style={{ color: 'var(--primary)', flexShrink: 0 }} />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 600, fontSize: 'var(--text-md)', marginBottom: 2 }}>{pool.name}</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{pool.alloc} used / {pool.size}</div>
      </div>
      <span style={{
        padding: '3px 8px', borderRadius: 'var(--radius-xs)', fontSize: 'var(--text-xs)', fontWeight: 600,
        background: isOnline ? 'var(--success-bg)' : isDeg ? 'var(--warning-bg)' : 'var(--error-bg)',
        color:      isOnline ? 'var(--success)'    : isDeg ? 'var(--warning)'    : 'var(--error)',
        border: `1px solid ${isOnline ? 'var(--success-border)' : isDeg ? 'var(--warning-border)' : 'var(--error-border)'}`,
      }}>{pool.health}</span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ContainerRow
// ---------------------------------------------------------------------------

function ContainerRow({ c, onClick }: { c: DockerContainer; onClick: () => void }) {
  const [hov, setHov] = useState(false)
  const name = c.Names[0]?.replace(/^\//, '') ?? c.Id.slice(0, 12)
  return (
    <div onClick={onClick} onMouseEnter={() => setHov(true)} onMouseLeave={() => setHov(false)}
      style={{
        display: 'flex', alignItems: 'center', gap: 12,
        padding: '8px 12px', borderRadius: 'var(--radius-sm)',
        background: hov ? 'var(--surface)' : 'transparent',
        cursor: 'pointer', transition: 'background 0.15s',
      }}>
      <span style={{
        width: 8, height: 8, borderRadius: '50%', flexShrink: 0,
        background: c.State === 'running' ? 'var(--success)' : 'var(--error)',
        boxShadow: c.State === 'running' ? '0 0 5px var(--success)' : undefined,
      }} />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 500, fontSize: 'var(--text-sm)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{name}</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{c.Image}</div>
      </div>
      <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', flexShrink: 0 }}>{c.Status}</span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Dashboard
// ---------------------------------------------------------------------------

export function DashboardPage() {
  const navigate = useNavigate()
  const wsOn = useWsStore((s) => s.on)
  const [liveState, setLiveState] = useState<WsStateUpdate | null>(null)

  useEffect(() => {
    return wsOn('stateUpdate', (data) => setLiveState(data as WsStateUpdate))
  }, [wsOn])

  const metricsQ = useQuery({
    queryKey: ['system', 'metrics'],
    queryFn: ({ signal }) => api.get<SystemMetrics>('/api/system/metrics', signal),
    refetchInterval: 30_000,
  })
  const statusQ = useQuery({
    queryKey: ['system', 'status'],
    queryFn: ({ signal }) => api.get<SystemStatus>('/api/system/status', signal),
    refetchInterval: 60_000,
  })
  const poolsQ = useQuery({
    queryKey: ['zfs', 'pools'],
    queryFn: ({ signal }) => api.get<PoolsResponse>('/api/zfs/pools', signal),
    refetchInterval: 30_000,
  })
  const containersQ = useQuery({
    queryKey: ['docker', 'containers'],
    queryFn: ({ signal }) => api.get<ContainersResponse>('/api/docker/containers', signal),
    refetchInterval: 30_000,
  })
  const upsQ = useQuery({
    queryKey: ['system', 'ups'],
    queryFn: ({ signal }) => api.get<UPSResponse>('/api/system/ups', signal),
    refetchInterval: 60_000,
  })

  // Prefer live WS data for latency-sensitive metrics
  const memPct   = liveState?.memory?.percent   ?? metricsQ.data?.memory.percent   ?? 0
  const memUsed  = liveState?.memory?.used       ?? metricsQ.data?.memory.used      ?? 0
  const memTotal = liveState?.memory?.total      ?? metricsQ.data?.memory.total     ?? 0
  const iowait   = liveState?.iowait             ?? metricsQ.data?.iowait           ?? 0
  const pools    = poolsQ.data?.data             ?? []
  const containers = containersQ.data?.containers ?? []
  const running  = containers.filter((c) => c.State === 'running')
  const ups      = liveState?.ups ?? upsQ.data?.data
  const upsAlert = ups && ups.status !== 'OL' && ups.status !== 'ONLINE'

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24, maxWidth: 1400 }}>

      {/* ECC warning */}
      {statusQ.data?.ecc_warning && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 18px',
          background: 'var(--warning-bg)', border: '1px solid var(--warning-border)',
          borderRadius: 'var(--radius-md)', fontSize: 'var(--text-sm)', color: 'var(--warning)' }}>
          <Icon name="warning" size={18} />
          {statusQ.data.ecc_warning_msg || 'Non-ECC RAM detected — ZFS data integrity risk.'}
        </div>
      )}

      {/* UPS alert */}
      {upsAlert && ups && (
        <div onClick={() => navigate({ to: '/ups' })} style={{
          display: 'flex', alignItems: 'center', gap: 10, padding: '12px 18px', cursor: 'pointer',
          background: parseInt(ups.battery_charge) < 20 ? 'var(--error-bg)' : 'var(--warning-bg)',
          border: `1px solid ${parseInt(ups.battery_charge) < 20 ? 'var(--error-border)' : 'var(--warning-border)'}`,
          borderRadius: 'var(--radius-md)', fontSize: 'var(--text-sm)',
          color: parseInt(ups.battery_charge) < 20 ? 'var(--error)' : 'var(--warning)',
        }}>
          <Icon name="battery_alert" size={18} />
          <span>UPS on battery — {ups.battery_charge}% charge · runtime {ups.battery_runtime}</span>
          <span style={{ marginLeft: 'auto', fontSize: 'var(--text-xs)', opacity: 0.7 }}>View UPS →</span>
        </div>
      )}

      {/* Stats */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(190px, 1fr))', gap: 18 }}>
        <StatCard icon="developer_board" label="Memory"
          value={metricsQ.isLoading ? '…' : `${Math.round(memPct)}%`}
          sub={metricsQ.isLoading ? undefined : `${fmtBytes(memUsed)} / ${fmtBytes(memTotal)}`}
          percent={memPct} loading={metricsQ.isLoading}
          onClick={() => navigate({ to: '/reporting' })} />

        <StatCard icon="dns" label="ZFS ARC"
          value={metricsQ.isLoading ? '…' : `${Math.round(metricsQ.data?.arc.percent ?? 0)}%`}
          sub={metricsQ.isLoading ? undefined : `${fmtBytes(metricsQ.data?.arc.used ?? 0)} / ${fmtBytes(metricsQ.data?.arc.limit ?? 0)}`}
          percent={metricsQ.data?.arc.percent} loading={metricsQ.isLoading}
          onClick={() => navigate({ to: '/pools' })} />

        <StatCard icon="storage" label="Storage Pools"
          value={poolsQ.isLoading ? '…' : String(pools.length)}
          sub={!poolsQ.isLoading ? `${pools.filter(p => p.health === 'ONLINE').length} online` : undefined}
          loading={poolsQ.isLoading}
          onClick={() => navigate({ to: '/pools' })} />

        <StatCard icon="deployed_code" label="Containers"
          value={containersQ.isLoading ? '…' : String(running.length)}
          sub={!containersQ.isLoading ? `${containers.length} total` : undefined}
          loading={containersQ.isLoading}
          onClick={() => navigate({ to: '/docker' })} />

        <StatCard icon="speed" label="I/O Wait"
          value={metricsQ.isLoading ? '…' : `${iowait}%`}
          percent={iowait} loading={metricsQ.isLoading}
          onClick={() => navigate({ to: '/reporting' })} />

        {statusQ.isLoading
          ? <StatCard icon="timer" label="Uptime" value="…" loading />
          : statusQ.data
            ? <StatCard icon="timer" label="Uptime"
                value={fmtUptime(statusQ.data.uptime_seconds)}
                sub={`D-PlaneOS ${statusQ.data.version}`} />
            : null}
      </div>

      {/* Metrics errors */}
      {metricsQ.isError && (
        <ErrorState error={metricsQ.error} title="Failed to load system metrics" onRetry={() => metricsQ.refetch()} />
      )}

      {/* Pools + Containers */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(360px, 1fr))', gap: 20 }}>
        <SectionCard title="Storage Pools" icon="database" onAction={() => navigate({ to: '/pools' })}>
          {poolsQ.isLoading && <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {[1,2].map(k => <Skeleton key={k} height={56} borderRadius="var(--radius-md)" />)}</div>}
          {poolsQ.isError && <ErrorState error={poolsQ.error} onRetry={() => poolsQ.refetch()} />}
          {!poolsQ.isLoading && !poolsQ.isError && pools.length === 0 && (
            <div style={{ padding: '20px 0', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
              No ZFS pools found
            </div>
          )}
          {pools.map(p => <PoolRow key={p.name} pool={p} onClick={() => navigate({ to: '/pools' })} />)}
        </SectionCard>

        <SectionCard title="Running Containers" icon="deployed_code" onAction={() => navigate({ to: '/docker' })}>
          {containersQ.isLoading && <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {[1,2,3].map(k => <Skeleton key={k} height={44} borderRadius="var(--radius-sm)" />)}</div>}
          {containersQ.isError && <ErrorState error={containersQ.error} onRetry={() => containersQ.refetch()} />}
          {!containersQ.isLoading && !containersQ.isError && running.length === 0 && (
            <div style={{ padding: '20px 0', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
              No running containers
            </div>
          )}
          {running.slice(0, 8).map(c => <ContainerRow key={c.Id} c={c} onClick={() => navigate({ to: '/docker' })} />)}
          {running.length > 8 && (
            <div onClick={() => navigate({ to: '/docker' })}
              style={{ fontSize: 'var(--text-xs)', color: 'var(--primary)', textAlign: 'center', cursor: 'pointer', paddingTop: 4 }}>
              +{running.length - 8} more
            </div>
          )}
        </SectionCard>
      </div>

      {/* inotify warning */}
      {metricsQ.data && metricsQ.data.inotify.percent > 70 && (
        <div onClick={() => navigate({ to: '/monitoring' })} style={{
          display: 'flex', alignItems: 'center', gap: 10, padding: '12px 18px',
          background: 'var(--warning-bg)', border: '1px solid var(--warning-border)',
          borderRadius: 'var(--radius-md)', fontSize: 'var(--text-sm)', color: 'var(--warning)', cursor: 'pointer',
        }}>
          <Icon name="notifications_active" size={16} />
          inotify at {Math.round(metricsQ.data.inotify.percent)}% capacity
          ({metricsQ.data.inotify.used.toLocaleString()} / {metricsQ.data.inotify.limit.toLocaleString()} watches)
          <span style={{ marginLeft: 'auto', fontSize: 'var(--text-xs)', opacity: 0.7 }}>View Monitoring →</span>
        </div>
      )}
    </div>
  )
}
