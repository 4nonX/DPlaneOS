/**
 * pages/ClusterPage.tsx - Cluster map (Design 0001 section 5.10, phase 4)
 *
 * One view of the whole setup from this node: the nodes and votes, which
 * node owns which storage group (and which may take it over), epochs,
 * quorum, the coordinator, replication lag, node states and maintenance.
 *
 * Calls:
 *   GET    /api/cluster/map           → { map }
 *   POST   /api/cluster/maintenance   { reason } → { moves[], maintenance }
 *   DELETE /api/cluster/maintenance
 */

import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { toast } from '@/hooks/useToast'

export interface MapNode {
  key?: string; name: string; addr?: string; self: boolean; voter: boolean; in_cluster: boolean; paired: boolean
  online: boolean; reachable: boolean; last_contact?: string; waiting: number; maintenance: boolean; coordinator: boolean
}
interface MapReplication { pool: string; target: string; last_ok_at: string | null; lag_secs: number; error?: string }
export interface MapGroup {
  name: string; topology: 'standalone' | 'shared' | 'replicated'; owner: string; epoch: number; candidates: string[]
  pools: string[]; serving: boolean; changing?: boolean; problems: string[]; failover?: string; interval_secs?: number; replication?: MapReplication[]
}
export interface ClusterMap {
  self: string; mode: 'standalone' | 'connected' | 'isolated' | 'independent'; mode_since?: string
  nodes: MapNode[]; groups: MapGroup[]; coordinator?: string; term?: number
  quorum: {
    configured: boolean; cluster_name?: string; quorate: boolean; expected_votes: number; total_votes: number
    auto_failover: boolean; auto_failover_reason?: string; qdevice?: string; qdevice_voting: boolean
  }
  conflicts: number; pending: number; proposals: number
}
interface MaintenanceMove { group: string; target?: string; result: 'moved' | 'stays' | 'failed' | 'planned'; detail?: string }
type Res = { success: boolean; error?: string }

export function useClusterMap() {
  return useQuery({
    queryKey: ['cluster', 'map'],
    queryFn: ({ signal }) => api.get<{ success: boolean; map: ClusterMap }>('/api/cluster/map', signal),
    refetchInterval: 10_000,
    select: r => r.map,
  })
}

const MODE: Record<ClusterMap['mode'], { label: string; cls: string }> = {
  standalone: { label: 'Standalone', cls: 'badge-neutral' },
  connected: { label: 'Connected', cls: 'badge-success' },
  isolated: { label: 'Isolated', cls: 'badge-warning' },
  independent: { label: 'Independent', cls: 'badge-neutral' },
}
const TOPOLOGY: Record<MapGroup['topology'], string> = { standalone: 'Standalone', shared: 'Shared storage', replicated: 'Replicated' }

export function fmtLag(secs: number) {
  if (secs < 0) return 'never'
  if (secs < 90) return `${secs} s ago`
  if (secs < 5400) return `${Math.round(secs / 60)} min ago`
  if (secs < 172800) return `${Math.round(secs / 3600)} h ago`
  return `${Math.round(secs / 86400)} days ago`
}

function NodeCard({ n, owned }: { n: MapNode; owned: MapGroup[] }) {
  let status: { label: string; cls: string; title: string }
  if (n.self) status = { label: 'This node', cls: 'badge-primary', title: 'The node you are logged in to' }
  else if (n.in_cluster) status = n.online
    ? { label: 'In the cluster', cls: 'badge-success', title: 'A member of this node\'s quorate partition' }
    : { label: 'Not in this partition', cls: 'badge-error', title: 'Corosync does not see it from this node: off, rebooting, or a network split' }
  else status = n.reachable
    ? { label: 'Reachable', cls: 'badge-success', title: 'Exchanges configuration with this node' }
    : { label: 'Unreachable', cls: 'badge-warning', title: 'Does not answer on the paired-node channel' }
  return (
    <div className="card" style={{ padding: 14, minWidth: 220, flex: '1 1 220px', borderColor: n.maintenance ? 'var(--warning)' : undefined }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
        <Icon name={n.voter ? 'how_to_vote' : 'dns'} size={18} />
        <strong>{n.name || n.key}</strong>
      </div>
      <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginBottom: 8 }}>
        <span className={`badge ${status.cls}`} title={status.title}>{status.label}</span>
        {n.coordinator && <span className="badge badge-neutral" title="Decides conflicts of users, directory, certificates and shared settings">Coordinator</span>}
        {n.voter && <span className="badge badge-neutral" title="Runs only corosync: votes, never owns storage">Voter</span>}
        {n.maintenance && <span className="badge badge-warning" title="Never chosen to take a storage group over">Maintenance</span>}
        {n.paired && !n.self && n.in_cluster && !n.reachable && <span className="badge badge-warning" title="Corosync sees it, but it does not answer on the paired-node channel">Not exchanging</span>}
      </div>
      <dl style={{ display: 'grid', gridTemplateColumns: 'auto 1fr', gap: '2px 10px', fontSize: 'var(--text-sm)', margin: 0 }}>
        {n.addr && <><dt style={{ color: 'var(--text-tertiary)' }}>Cluster address</dt><dd style={{ margin: 0, fontFamily: 'var(--font-mono)' }}>{n.addr}</dd></>}
        {!n.voter && <><dt style={{ color: 'var(--text-tertiary)' }}>Owns</dt><dd style={{ margin: 0 }}>{owned.length ? owned.map(g => g.name).join(', ') : 'no group'}</dd></>}
        {n.paired && <><dt style={{ color: 'var(--text-tertiary)' }}>Not yet taken</dt><dd style={{ margin: 0 }} title="Changes of this node it has not pulled yet">{n.waiting} change{n.waiting === 1 ? '' : 's'}</dd></>}
      </dl>
    </div>
  )
}

function GroupMatrix({ m }: { m: ClusterMap }) {
  const nodes = m.nodes.filter(n => !n.voter && n.key)
  const name = (k: string) => m.nodes.find(n => n.key === k)?.name ?? k.slice(0, 8)
  if (m.groups.length === 0) {
    return <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>No storage groups yet. Create one in <Link to="/ha">High Availability</Link>.</p>
  }
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="data-table" style={{ fontSize: 'var(--text-sm)' }}>
        <thead>
          <tr>
            <th>Group</th>
            {nodes.map(n => <th key={n.key} style={{ textAlign: 'center' }}>{n.name}{n.maintenance ? ' (maintenance)' : ''}</th>)}
            <th>Replication</th>
            <th>State</th>
          </tr>
        </thead>
        <tbody>
          {m.groups.map(g => (
            <tr key={g.name}>
              <td>
                <strong>{g.name}</strong>
                <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>{TOPOLOGY[g.topology]} · epoch {g.epoch} · {g.pools.join(', ')}</div>
              </td>
              {nodes.map(n => {
                const owner = g.owner === n.key
                const cand = g.candidates.indexOf(n.key!)
                return (
                  <td key={n.key} style={{ textAlign: 'center' }}>
                    {owner
                      ? <span className="badge badge-primary" title="Owns the group: serves its shares, exports and apps">Owner</span>
                      : cand >= 0
                        ? <span className="badge badge-neutral" title={`May take it over (priority ${cand + 1})`}>Standby {cand + 1}</span>
                        : <span style={{ color: 'var(--text-tertiary)' }}>—</span>}
                  </td>
                )
              })}
              <td>
                {g.topology !== 'replicated' ? <span style={{ color: 'var(--text-tertiary)' }}>—</span>
                  : (g.replication ?? []).length === 0
                    ? <span style={{ color: 'var(--text-tertiary)' }}>{g.owner === m.self ? 'not yet' : 'shown on the owner'}</span>
                    : g.replication!.map(r => {
                      const late = r.lag_secs < 0 || (g.interval_secs ? r.lag_secs > 2 * g.interval_secs : false)
                      return (
                        <div key={r.pool + r.target} style={{ color: r.error || late ? 'var(--warning)' : undefined }} title={r.error || undefined}>
                          {r.pool} → {name(r.target)}: {fmtLag(r.lag_secs)}{r.error ? ' (failed)' : ''}
                        </div>
                      )
                    })}
              </td>
              <td>
                {g.changing
                  ? <span className="badge badge-warning">Changing owner</span>
                  : g.owner === m.self
                  ? (g.serving ? <span className="badge badge-success">Serving</span> : <span className="badge badge-error">Not serving</span>)
                  : <span style={{ color: 'var(--text-tertiary)' }}>on {name(g.owner)}</span>}
                {g.problems.length > 0 && <div style={{ color: 'var(--error)', fontSize: 'var(--text-xs)', maxWidth: 280 }}>{g.problems.join('; ')}</div>}
                {g.failover && <div style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-xs)', maxWidth: 280 }}>{g.failover}</div>}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function MaintenanceCard({ m }: { m: ClusterMap }) {
  const qc = useQueryClient()
  const self = m.nodes.find(n => n.self)!
  const [asking, setAsking] = useState(false)
  const [reason, setReason] = useState('')
  const [moves, setMoves] = useState<MaintenanceMove[] | null>(null)
  const owned = m.groups.filter(g => g.owner === m.self)
  const enter = useMutation({
    mutationFn: async () => api.post<Res & { moves: MaintenanceMove[] }>('/api/cluster/maintenance', { reason: reason.trim() }),
    onSuccess: r => {
      setMoves(r.moves)
      if (r.success) { toast.success('Maintenance mode on'); setAsking(false) } else toast.error(r.error ?? 'Failed')
      qc.invalidateQueries({ queryKey: ['cluster'] }); qc.invalidateQueries({ queryKey: ['groups'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const exit = useMutation({
    mutationFn: async () => ensureOk(await api.delete<Res>('/api/cluster/maintenance')),
    onSuccess: () => { toast.success('Maintenance mode off'); setMoves(null); qc.invalidateQueries({ queryKey: ['cluster'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <h3 style={{ margin: 0 }}>Maintenance of this node</h3>
        {self.maintenance && <span className="badge badge-warning">On</span>}
        <span style={{ marginLeft: 'auto' }} />
        {self.maintenance
          ? <button className="btn btn-ghost" disabled={exit.isPending} onClick={() => exit.mutate()}><Icon name="play_arrow" size={16} />End maintenance</button>
          : <button className="btn btn-ghost" onClick={() => { setMoves(null); setAsking(true) }}><Icon name="construction" size={16} />Start maintenance…</button>}
      </div>
      <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 0 }}>
        {self.maintenance
          ? 'This node does not take storage groups over and is not a target for moves. Groups do not move back by themselves when maintenance ends.'
          : 'Before updating, rebooting or rewiring this node: its storage groups move to other nodes first, and it is never chosen to take one over while in maintenance.'}
      </p>
      {moves && moves.length > 0 && (
        <ul style={{ fontSize: 'var(--text-sm)', marginBottom: 0 }}>
          {moves.map(mv => (
            <li key={mv.group} style={{ color: mv.result === 'failed' ? 'var(--error)' : undefined }}>
              <strong>{mv.group}</strong>: {mv.result === 'moved' ? `moved to ${m.nodes.find(n => n.key === mv.target)?.name ?? mv.target}` : mv.result === 'stays' ? 'stays here' : 'not moved'}
              {mv.detail ? ` (${mv.detail})` : ''}
            </li>
          ))}
        </ul>
      )}
      {asking && (
        <Modal title="Start maintenance of this node?" onClose={() => setAsking(false)}>
          {owned.length === 0
            ? <p style={{ fontSize: 'var(--text-sm)' }}>This node owns no storage group; nothing moves.</p>
            : (
              <>
                <p style={{ fontSize: 'var(--text-sm)' }}>These groups move to another node first (a planned move: clients are interrupted for the moment of the move):</p>
                <ul style={{ fontSize: 'var(--text-sm)' }}>
                  {owned.map(g => <li key={g.name}><strong>{g.name}</strong>{g.topology === 'standalone' ? ': stays here (standalone group, no other node)' : ''}</li>)}
                </ul>
                <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>If a group has no other node that is online and not in maintenance, nothing changes.</p>
              </>
            )}
          <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }} htmlFor="mt-reason">Reason (shown to other administrators)</label>
          <input id="mt-reason" className="input" maxLength={200} value={reason} onChange={e => setReason(e.target.value)} placeholder="e.g. firmware update" />
          {moves && moves.some(mv => mv.result === 'failed') && (
            <ul style={{ color: 'var(--error)', fontSize: 'var(--text-sm)' }}>
              {moves.filter(mv => mv.result === 'failed').map(mv => <li key={mv.group}>{mv.group}: {mv.detail}</li>)}
            </ul>
          )}
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 12 }}>
            <button className="btn btn-ghost" onClick={() => setAsking(false)}>Cancel</button>
            <button className="btn btn-primary" disabled={enter.isPending} onClick={() => enter.mutate()}>
              {enter.isPending ? 'Moving groups…' : 'Start maintenance'}
            </button>
          </div>
        </Modal>
      )}
    </div>
  )
}

export function ClusterPage() {
  const q = useClusterMap()
  if (q.isLoading) return <div className="page-container"><Skeleton height={320} /></div>
  if (q.isError || !q.data) return <div className="page-container"><ErrorState error={q.error} onRetry={() => q.refetch()} /></div>
  const m = q.data
  const mode = MODE[m.mode]
  const coordinator = m.nodes.find(n => n.coordinator)
  const machines = m.nodes.filter(n => !n.voter)
  const voters = m.nodes.filter(n => n.voter)

  return (
    <div className="page-container">
      <header className="page-header">
        <h1 className="page-title">Cluster Map</h1>
        <p className="page-subtitle">The nodes, votes and storage groups as this node sees them: who owns what, who may take over, and whether everything is in step.</p>
      </header>

      <div className="card" style={{ marginBottom: 16, display: 'flex', gap: 18, flexWrap: 'wrap', alignItems: 'center', fontSize: 'var(--text-sm)' }}>
        <span>This node: <span className={`badge ${mode.cls}`}>{mode.label}</span>{m.mode_since && m.mode !== 'connected' ? ` since ${new Date(m.mode_since).toLocaleString()}` : ''}</span>
        {m.quorum.configured ? (
          <>
            <span>Quorum: <span className={`badge ${m.quorum.quorate ? 'badge-success' : 'badge-error'}`}>{m.quorum.quorate ? 'Quorate' : 'No quorum'}</span> {m.quorum.total_votes} of {m.quorum.expected_votes} votes</span>
            {m.quorum.qdevice && <span>QDevice {m.quorum.qdevice}: {m.quorum.qdevice_voting ? 'voting' : <span style={{ color: 'var(--warning)' }}>not voting</span>}</span>}
            <span title={m.quorum.auto_failover_reason}>Automatic failover: {m.quorum.auto_failover ? 'possible' : <span style={{ color: 'var(--warning)' }}>off</span>}</span>
            <span title="Decides conflicts of users, directory, certificates and shared settings">Coordinator: {coordinator ? <strong>{coordinator.name}</strong> : 'none (no quorum)'}</span>
          </>
        ) : <span style={{ color: 'var(--text-tertiary)' }}>No cluster: storage groups cannot fail over. <Link to="/ha">Set up high availability</Link></span>}
        {(m.conflicts > 0 || m.proposals > 0 || m.pending > 0) && (
          <Link to="/config-sync" style={{ marginLeft: 'auto' }}>
            {[m.conflicts && `${m.conflicts} conflict${m.conflicts === 1 ? '' : 's'}`, m.proposals && `${m.proposals} proposal${m.proposals === 1 ? '' : 's'}`, m.pending && `${m.pending} waiting`].filter(Boolean).join(' · ')}
          </Link>
        )}
      </div>
      {m.quorum.configured && !m.quorum.auto_failover && m.quorum.auto_failover_reason && (
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: -8 }}>Automatic failover is off: {m.quorum.auto_failover_reason}.</p>
      )}

      <h2 style={{ fontSize: 'var(--text-lg)' }}>Nodes</h2>
      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap', marginBottom: 20 }}>
        {machines.map(n => <NodeCard key={n.key ?? n.name} n={n} owned={m.groups.filter(g => g.owner === n.key)} />)}
        {voters.map(n => <NodeCard key={'v' + n.name} n={n} owned={[]} />)}
      </div>

      <h2 style={{ fontSize: 'var(--text-lg)' }}>Storage groups</h2>
      <div className="card" style={{ marginBottom: 20 }}>
        <GroupMatrix m={m} />
      </div>

      {m.quorum.configured && <MaintenanceCard m={m} />}
    </div>
  )
}
