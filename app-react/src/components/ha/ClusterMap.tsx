/**
 * ClusterMap - the nodes and the storage groups in one view (Design 0001,
 * 5.10), and maintenance mode of this node.
 *
 * The map shows what this node knows: who is there, who votes, who owns
 * which group and since which epoch, how far the copies of a replicated
 * group are behind, and what is wrong. Things are changed in the panels
 * below it.
 *
 * Calls: GET /api/ha/map, GET/POST/DELETE /api/ha/maintenance
 */

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { toast } from '@/hooks/useToast'

interface MapNode {
  key: string; name: string; self: boolean; addr?: string; voter: boolean
  state: 'online' | 'away' | 'unreachable' | 'paired'
  since?: string; maintenance: boolean; power_fencing?: 'ok' | 'off' | ''; owns: string[]
}
interface MapReplication { peer_name: string; pool: string; direction: 'out' | 'in'; last_ok_at?: string; lag_seconds: number; error?: string }
interface MapGroup {
  name: string; topology: string; owner: string; owner_name: string; epoch: number
  candidates: string[]; pools: string[]; role: string; serving: boolean; problems: string[]
  failover: { action: string; reason?: string }
  replication: MapReplication[]
}
interface Maintenance { active: boolean; since?: string; reason: string; set_by: string }
export interface ClusterMapData {
  success: boolean; self: string; nodes: MapNode[]; groups: MapGroup[]; maintenance: Maintenance
  quorum: { configured: boolean; quorate: boolean; expected_votes: number; total_votes: number; auto_failover: boolean; auto_failover_reason?: string }
  third_vote: { kind: 'none' | 'qdevice' | 'member'; host?: string; voting?: boolean }
  sync?: { mode: string; waiting: number; conflicts: number }
}
interface MaintStep { group: string; action: 'move' | 'stays' | 'blocked'; target_name?: string; detail: string; done: boolean; error?: string }
interface MaintAnswer { success: boolean; maintenance: Maintenance; plan: MaintStep[]; others_in_maintenance: string[] }
interface EnterAnswer { success: boolean; error?: string; message?: string; plan?: MaintStep[]; blocked?: boolean; not_moved?: number }

const NODE_STATE: Record<MapNode['state'], { label: string; cls: string; hint: string }> = {
  online:      { label: 'Online',        cls: 'badge-success', hint: 'In the same part of the cluster as this node' },
  away:        { label: 'Not reachable', cls: 'badge-error',   hint: 'A member of the cluster that this node does not see' },
  unreachable: { label: 'Not answering', cls: 'badge-error',   hint: 'Paired with this node, but not answering' },
  paired:      { label: 'Paired',        cls: 'badge-neutral', hint: 'Exchanges configuration with this node; not a member of a cluster' },
}

export function lag(seconds: number): string {
  if (seconds < 0) return 'never'
  if (seconds < 90) return `${seconds} s ago`
  if (seconds < 5400) return `${Math.round(seconds / 60)} min ago`
  if (seconds < 129600) return `${Math.round(seconds / 3600)} h ago`
  return `${Math.round(seconds / 86400)} d ago`
}

const STEP_ICON = { move: 'swap_horiz', stays: 'push_pin', blocked: 'block' } as const

function MaintenanceDialog({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const [reason, setReason] = useState('')
  const [result, setResult] = useState<EnterAnswer | null>(null)
  const q = useQuery({
    queryKey: ['ha', 'maintenance'],
    queryFn: ({ signal }) => api.get<MaintAnswer>('/api/ha/maintenance', signal),
  })
  const enter = useMutation({
    mutationFn: (force: boolean) => api.post<EnterAnswer>('/api/ha/maintenance', { reason: reason.trim(), force }),
    onSuccess: (r) => {
      setResult(r)
      qc.invalidateQueries({ queryKey: ['ha'] }); qc.invalidateQueries({ queryKey: ['groups'] })
      if (r.success && !r.not_moved) toast.success(r.message || 'This node is in maintenance')
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const plan = result?.plan ?? q.data?.plan ?? []
  const blocked = plan.some(s => s.action === 'blocked')
  const entered = result?.success === true

  return (
    <Modal title="Put this node into maintenance" onClose={onClose} size="lg">
      <div style={{ padding: '0 24px 8px' }}>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: 0 }}>
          For work on this machine: a restart, firmware, a disk shelf. In maintenance this node hands the storage groups it
          owns to another node, takes none over (not automatically and not on request), and the other nodes pass it over.
          Leaving maintenance moves nothing back.
        </p>

        <div style={{ fontWeight: 600, marginBottom: 6 }}>{entered ? 'What was done' : 'What will happen to the groups this node owns'}</div>
        {q.isLoading && <div style={{ color: 'var(--text-tertiary)' }}>Looking…</div>}
        {!q.isLoading && plan.length === 0 && <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>This node owns no storage group: nothing has to move.</div>}
        <ul style={{ listStyle: 'none', padding: 0, margin: 0, fontSize: 'var(--text-sm)' }}>
          {plan.map(s => (
            <li key={s.group} style={{ display: 'flex', gap: 8, padding: '6px 0', borderTop: '1px solid var(--border-subtle)' }}>
              <Icon name={s.error ? 'error' : s.done ? 'check_circle' : STEP_ICON[s.action]} size={16}
                style={{ flexShrink: 0, marginTop: 2, color: s.error || s.action === 'blocked' ? 'var(--error)' : s.done ? 'var(--success)' : 'var(--text-secondary)' }} />
              <span>
                <strong>{s.group}</strong>: {s.detail}
                {s.error && <div style={{ color: 'var(--error)' }}>Not moved, still served here: {s.error}</div>}
              </span>
            </li>
          ))}
        </ul>

        {result && !result.success && (
          <div role="alert" className="alert alert-error" style={{ marginTop: 12, fontSize: 'var(--text-sm)' }}>
            <Icon name="error" size={16} /><span>{result.error}</span>
          </div>
        )}
        {entered && (
          <div role="status" className={`alert ${result?.not_moved ? 'alert-warning' : 'alert-success'}`} style={{ marginTop: 12, fontSize: 'var(--text-sm)' }}>
            <Icon name={result?.not_moved ? 'warning' : 'check_circle'} size={16} /><span>{result?.message}</span>
          </div>
        )}

        {!entered && (
          <label className="field" style={{ marginTop: 14 }}>
            <span className="field-label">Reason (shown on every node)</span>
            <input value={reason} onChange={e => setReason(e.target.value)} maxLength={200} placeholder="Firmware update" className="input" autoFocus />
          </label>
        )}
        {(q.data?.others_in_maintenance.length ?? 0) > 0 && !entered && (
          <p style={{ fontSize: 'var(--text-xs)', color: 'var(--warning)' }}>
            Already in maintenance: {q.data!.others_in_maintenance.join(', ')}.
          </p>
        )}
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">{entered ? 'Close' : 'Cancel'}</button>
        {!entered && blocked && (
          <button onClick={() => enter.mutate(true)} disabled={enter.isPending} className="btn btn-ghost" style={{ color: 'var(--error)' }}
            title="The groups that cannot move stay on this node and are not available while it is down">
            Enter anyway; those groups stay here
          </button>
        )}
        {!entered && (
          <button onClick={() => enter.mutate(false)} disabled={enter.isPending || q.isLoading || blocked} className="btn btn-primary">
            <Icon name="build" size={15} />{enter.isPending ? 'Moving the groups…' : 'Enter maintenance'}
          </button>
        )}
      </div>
    </Modal>
  )
}

export function ClusterMap() {
  const qc = useQueryClient()
  const [dialog, setDialog] = useState(false)
  const q = useQuery({
    queryKey: ['ha', 'map'],
    queryFn: ({ signal }) => api.get<ClusterMapData>('/api/ha/map', signal),
    refetchInterval: 10_000,
  })
  const leave = useMutation({
    mutationFn: () => api.delete<{ success: boolean; message?: string }>('/api/ha/maintenance'),
    onSuccess: (r) => { toast.success(r.message || 'Left maintenance'); qc.invalidateQueries({ queryKey: ['ha'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  if (!q.data) return null
  const d = q.data
  const alone = d.nodes.length === 1 && d.groups.length === 0

  return (
    <div className="card" style={{ marginBottom: 20 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', marginBottom: 12 }}>
        <h3 style={{ margin: 0 }}>Cluster map</h3>
        {d.quorum.configured && (
          <span className={`badge ${d.quorum.quorate ? 'badge-success' : 'badge-error'}`}>
            {d.quorum.quorate ? 'Quorate' : 'No quorum'} · {d.quorum.total_votes} of {d.quorum.expected_votes} votes
          </span>
        )}
        {d.quorum.configured && (
          <span className={`badge ${d.quorum.auto_failover ? 'badge-success' : 'badge-warning'}`} title={d.quorum.auto_failover_reason}>
            Automatic failover {d.quorum.auto_failover ? 'possible' : 'off'}
          </span>
        )}
        {d.sync && d.sync.mode !== 'standalone' && (
          <span className={`badge ${d.sync.mode === 'connected' ? 'badge-neutral' : 'badge-warning'}`}
            title="Configuration exchange with the paired nodes (System, Configuration Sync)">
            Configuration: {d.sync.mode}{d.sync.waiting > 0 ? `, ${d.sync.waiting} waiting` : ''}{d.sync.conflicts > 0 ? `, ${d.sync.conflicts} to review` : ''}
          </span>
        )}
        <span style={{ marginLeft: 'auto' }}>
          {d.maintenance.active
            ? <button className="btn btn-primary btn-sm" onClick={() => leave.mutate()} disabled={leave.isPending}><Icon name="check" size={14} />Leave maintenance</button>
            : <button className="btn btn-ghost btn-sm" onClick={() => setDialog(true)}><Icon name="build" size={14} />Maintenance…</button>}
        </span>
      </div>

      {d.maintenance.active && (
        <div role="status" className="alert alert-warning" style={{ marginBottom: 12, fontSize: 'var(--text-sm)' }}>
          <Icon name="build" size={16} />
          <span>
            This node is in maintenance{d.maintenance.reason ? `: ${d.maintenance.reason}` : ''}
            {d.maintenance.set_by ? ` (${d.maintenance.set_by})` : ''}. It takes no storage group over.
            {d.nodes.find(n => n.self)?.owns.length ? ' It still serves the groups listed under it.' : ''}
          </span>
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(190px, 1fr))', gap: 10, marginBottom: d.groups.length ? 16 : 0 }}>
        {d.nodes.map(n => (
          <div key={n.key} data-testid={`map-node-${n.name}`}
            style={{ border: `1px solid ${n.self ? 'var(--primary)' : 'var(--border)'}`, borderRadius: 'var(--radius-md)', padding: '10px 12px', opacity: n.state === 'online' || n.state === 'paired' ? 1 : 0.75 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <Icon name={n.voter ? 'how_to_vote' : 'dns'} size={16} style={{ color: 'var(--text-secondary)' }} />
              <strong style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{n.name}</strong>
              {n.self && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>this node</span>}
            </div>
            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginTop: 6 }}>
              <span className={`badge ${NODE_STATE[n.state].cls}`} title={NODE_STATE[n.state].hint}>{NODE_STATE[n.state].label}</span>
              {n.voter && <span className="badge badge-neutral" title="Votes only: holds no storage">Vote</span>}
              {n.maintenance && <span className="badge badge-warning" title="Hands its groups over and takes none">Maintenance</span>}
              {n.power_fencing === 'ok' && <span className="badge badge-neutral" title="This node can switch it off before taking over from it">Power fencing</span>}
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 6 }}>
              {n.voter ? 'no storage' : n.owns.length ? <>owns {n.owns.join(', ')}</> : 'owns no group'}
            </div>
          </div>
        ))}
        {d.third_vote.kind === 'qdevice' && (
          <div data-testid="map-third-vote" style={{ border: '1px dashed var(--border)', borderRadius: 'var(--radius-md)', padding: '10px 12px' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <Icon name="how_to_vote" size={16} style={{ color: 'var(--text-secondary)' }} /><strong>Third vote</strong>
            </div>
            <div style={{ marginTop: 6 }}>
              <span className={`badge ${d.third_vote.voting ? 'badge-success' : 'badge-warning'}`}>{d.third_vote.voting ? 'Voting' : 'Not voting'}</span>
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 6, fontFamily: 'var(--font-mono)' }}>{d.third_vote.host}</div>
          </div>
        )}
      </div>

      {alone && (
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', margin: '12px 0 0' }}>
          This system stands alone. To have a second system take over when this one fails, start with <strong>Set up high availability</strong> above.
        </p>
      )}

      {d.groups.length > 0 && (
        <table className="data-table" style={{ fontSize: 'var(--text-sm)' }}>
          <thead><tr><th>Storage group</th><th>Owner</th><th>Takes over, in order</th><th>State</th></tr></thead>
          <tbody>
            {d.groups.map(g => {
              const out = g.replication.filter(r => r.direction === 'out')
              return (
                <tr key={g.name} data-testid={`map-group-${g.name}`}>
                  <td>
                    <strong>{g.name}</strong>
                    <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{g.topology} · {g.pools.join(', ')}</div>
                  </td>
                  <td>{g.owner_name}<div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>epoch {g.epoch}</div></td>
                  <td>{g.candidates.filter(c => c !== g.owner_name).join(' → ') || '—'}</td>
                  <td>
                    {g.role === 'owner'
                      ? <span className={`badge ${g.serving ? 'badge-success' : 'badge-error'}`}>{g.serving ? 'Serving here' : 'Not serving'}</span>
                      : <span className="badge badge-neutral">{g.role === 'standby' ? 'Standby here' : 'Elsewhere'}</span>}
                    {out.map(r => (
                      <div key={r.peer_name + r.pool} style={{ fontSize: 'var(--text-xs)', color: r.error ? 'var(--error)' : 'var(--text-secondary)', marginTop: 4 }}>
                        copy on {r.peer_name}: {lag(r.lag_seconds)}{r.error ? ' (last attempt failed)' : ''}
                      </div>
                    ))}
                    {g.failover.reason && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--warning)', marginTop: 4 }}>{g.failover.reason}</div>}
                    {g.problems.map(p => <div key={p} style={{ fontSize: 'var(--text-xs)', color: 'var(--error)', marginTop: 4 }}>{p}</div>)}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
      {dialog && <MaintenanceDialog onClose={() => setDialog(false)} />}
    </div>
  )
}
