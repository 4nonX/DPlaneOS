/**
 * NodeStateBanner - shown on every page while this node cannot reach a paired
 * node (Design 0001 section 5.5). The node keeps working normally; changes
 * are exchanged when the connection is back. Also reports open conflicts.
 */

import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'

export interface SyncStatus {
  node_id: string
  name: string
  mode: 'standalone' | 'connected' | 'isolated' | 'independent'
  since?: string
  waiting: number
  conflicts: number
  pending: number
  writer: boolean
  writer_reason?: string
  peers: {
    id: string
    name: string
    url: string
    fingerprint_display: string
    last_contact: string | null
    unreachable_since: string | null
    last_error: string
    reachable: boolean
    waiting: number
  }[]
}

export interface FleetMembership {
  fleet: string
  manager: string
  last_apply_at?: string
  last_rollout?: string
  last_contact_at?: string
  unreachable: boolean
}

type SyncStatusResponse = { success: boolean; status: SyncStatus; fleet?: FleetMembership }

function useSyncStatusResponse<T>(select: (r: SyncStatusResponse) => T) {
  return useQuery({
    queryKey: ['config', 'sync', 'status'],
    queryFn: ({ signal }) => api.get<SyncStatusResponse>('/api/config/sync/status', signal),
    refetchInterval: 30_000,
    select,
  })
}

export function useSyncStatus() {
  return useSyncStatusResponse(r => r.status)
}

/** The fleet manager has not called for a while (Design 0001 section 5.11). */
function FleetManagerBanner() {
  const { data: f } = useSyncStatusResponse(r => r.fleet)
  if (!f?.unreachable) return null
  return (
    <div role="status" style={{
      display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap',
      padding: '10px 14px', marginBottom: 20, borderRadius: 'var(--radius-lg)',
      border: '1px solid var(--warning)', background: 'var(--warning-bg)', color: 'var(--text-primary)', fontSize: 'var(--text-sm)',
    }}>
      <Icon name="lan" size={18} style={{ color: 'var(--warning)' }} />
      <span style={{ flex: '1 1 320px' }}>
        <strong>Fleet manager unreachable</strong>
        {' — '}
        {f.manager || 'The manager'} of fleet {f.fleet} has not contacted this node {f.last_contact_at ? `since ${fmtTime(f.last_contact_at)}` : 'yet'}.
        This node is not affected; only new fleet changes wait.
      </span>
      <Link to="/fleet" style={{ marginLeft: 'auto', color: 'var(--warning)', fontWeight: 600 }}>Details</Link>
    </div>
  )
}

function fmtTime(s: string) {
  const d = new Date(s)
  const today = new Date().toDateString() === d.toDateString()
  return today ? d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : d.toLocaleString()
}

export function NodeStateBanner() {
  return <><SyncBanner /><FleetManagerBanner /></>
}

function SyncBanner() {
  const { data: st } = useSyncStatus()
  if (!st) return null
  const isolated = st.mode === 'isolated'
  if (!isolated && st.conflicts === 0) return null

  const tone = isolated ? 'var(--warning)' : 'var(--primary)'
  return (
    <div role="status" style={{
      display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap',
      padding: '10px 14px', marginBottom: 20, borderRadius: 'var(--radius-lg)',
      border: `1px solid ${tone}`, background: isolated ? 'var(--warning-bg)' : 'var(--primary-bg)', color: 'var(--text-primary)',
      fontSize: 'var(--text-sm)',
    }}>
      <Icon name={isolated ? 'cloud_off' : 'call_split'} size={18} style={{ color: tone }} />
      {isolated ? (
        <span style={{ flex: '1 1 320px' }}>
          <strong>Operating independently since {st.since ? fmtTime(st.since) : '—'}</strong>
          {' — '}
          {st.waiting > 0 ? `${st.waiting} change${st.waiting === 1 ? '' : 's'} waiting to sync. ` : ''}
          Everything keeps working; changes are exchanged when the other node is reachable again.
        </span>
      ) : null}
      {st.conflicts > 0 && (
        <span style={{ flex: '1 1 320px' }}>
          <strong>{st.conflicts} conflicting change{st.conflicts === 1 ? '' : 's'}</strong> made on two nodes need{st.conflicts === 1 ? 's' : ''} your decision.
        </span>
      )}
      <Link to="/config-sync" style={{ marginLeft: 'auto', color: tone, fontWeight: 600 }}>
        {st.conflicts > 0 ? 'Review' : 'Details'}
      </Link>
    </div>
  )
}
