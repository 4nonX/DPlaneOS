/**
 * GroupsPanel - storage groups (Design 0001 phase 3b).
 *
 * A storage group is the unit of ownership: pools, the nodes that may own
 * them, a topology, the current owner and the epoch (increases with every
 * change of owner; a node with an older epoch must not write).
 *
 * Calls: GET/POST /api/groups, POST /api/groups/{name}/move, DELETE /api/groups/{name}
 */

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'

interface GroupStatus {
  name: string
  topology: 'standalone' | 'shared' | 'replicated'
  pools: { name: string; guid: string }[]
  candidates: string[]
  owner: string
  epoch: number
  updated_at: string
  role: 'owner' | 'standby' | 'other'
  can_write: boolean
  problems: string[]
  failover: { action: 'none' | 'wait' | 'takeover' | ''; reason?: string; wait_until?: string }
}
interface GroupsResponse {
  success: boolean
  groups: GroupStatus[]
  self: string
  names: Record<string, string>
  members: { key: string; name: string }[]
  imported_pools: string[]
}
type Res = { success: boolean; error?: string }

const TOPOLOGY: Record<string, { label: string; text: string }> = {
  standalone: { label: 'Standalone', text: 'One node; no failover.' },
  shared:     { label: 'Shared storage', text: 'All candidates see the same disks (SAS or SATA JBOD, SAN, NVMe-oF). Zero data loss on failover; only one node imports the pools at a time.' },
  replicated: { label: 'Replicated', text: 'Each candidate has its own disks, kept in sync by ZFS replication. Works with any drives; on failover you lose up to one replication interval.' },
}

function CreateGroup({ data }: { data: GroupsResponse }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [topology, setTopology] = useState<'standalone' | 'shared' | 'replicated'>(data.members.length > 1 ? 'shared' : 'standalone')
  const [pools, setPools] = useState<string[]>([])
  const others = data.members.filter(m => m.key !== data.self)
  const [cands, setCands] = useState<string[]>(others.map(m => m.key))
  const free = data.imported_pools.filter(p => !data.groups.some(g => g.pools.some(gp => gp.name === p)))
  const create = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res>('/api/groups', {
      name, topology, pools, candidates: topology === 'standalone' ? [] : cands,
    })),
    onSuccess: () => { toast.success(`Group ${name} created`); setName(''); setPools([]); qc.invalidateQueries({ queryKey: ['groups'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const toggle = (list: string[], set: (v: string[]) => void, v: string) => set(list.includes(v) ? list.filter(x => x !== v) : [...list, v])

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginTop: 12, maxWidth: 560 }}>
      <h4 style={{ margin: 0 }}>New storage group</h4>
      <input className="input" placeholder="Name, e.g. data" value={name} onChange={e => setName(e.target.value.trim())} aria-label="Group name" />
      <select className="input" value={topology} onChange={e => setTopology(e.target.value as typeof topology)} aria-label="Topology">
        {Object.entries(TOPOLOGY).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}
      </select>
      <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: 0 }}>{TOPOLOGY[topology].text}</p>
      <div style={{ fontSize: 'var(--text-sm)' }}>
        Pools (imported on this node):{' '}
        {free.length === 0 ? <em>none available</em> : free.map(p => (
          <label key={p} style={{ marginRight: 12 }}>
            <input type="checkbox" checked={pools.includes(p)} onChange={() => toggle(pools, setPools, p)} /> {p}
          </label>
        ))}
      </div>
      {topology !== 'standalone' && (
        <div style={{ fontSize: 'var(--text-sm)' }}>
          Other nodes that may own it:{' '}
          {others.length === 0 ? <em>form a cluster first (above)</em> : others.map(m => (
            <label key={m.key} style={{ marginRight: 12 }}>
              <input type="checkbox" checked={cands.includes(m.key)} onChange={() => toggle(cands, setCands, m.key)} /> {m.name}
            </label>
          ))}
        </div>
      )}
      <button className="btn btn-primary" style={{ alignSelf: 'flex-start' }} disabled={!name || pools.length === 0 || create.isPending} onClick={() => create.mutate()}>
        <Icon name="add" size={16} />Create group
      </button>
    </div>
  )
}

export function GroupsPanel() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [creating, setCreating] = useState(false)
  const q = useQuery({
    queryKey: ['groups'],
    queryFn: ({ signal }) => api.get<GroupsResponse>('/api/groups', signal),
    refetchInterval: 10_000,
  })
  const move = useMutation({
    mutationFn: async (v: { name: string; target: string }) => ensureOk(await api.post<Res & { result?: { warnings?: string[] } }>(`/api/groups/${encodeURIComponent(v.name)}/move`, { target: v.target })),
    onSuccess: r => {
      toast.success('Group moved')
      r.result?.warnings?.forEach(w => toast.error(w))
      qc.invalidateQueries({ queryKey: ['groups'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const takeover = useMutation({
    mutationFn: async (name: string) => ensureOk(await api.post<Res>(`/api/groups/${encodeURIComponent(name)}/takeover`, { confirm_owner_off: true })),
    onSuccess: () => { toast.success('This node now owns the group'); qc.invalidateQueries({ queryKey: ['groups'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const remove = useMutation({
    mutationFn: async (name: string) => ensureOk(await api.delete<Res>(`/api/groups/${encodeURIComponent(name)}`)),
    onSuccess: () => { toast.success('Group removed'); qc.invalidateQueries({ queryKey: ['groups'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  if (!q.data) return null
  const d = q.data
  const nm = (k: string) => d.names[k] ?? k.slice(0, 8)

  return (
    <div className="card" style={{ marginBottom: 20 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <h3 style={{ margin: 0 }}>Storage groups</h3>
        <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} onClick={() => setCreating(c => !c)}>
          <Icon name={creating ? 'close' : 'add'} size={14} />{creating ? 'Close' : 'New group'}
        </button>
      </div>
      <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        A group is what moves between nodes: its pools, shares, exports and apps. Exactly one node owns it at a time; the epoch increases with every change of owner, and a node with an older epoch is not allowed to write.
      </p>
      {d.groups.length === 0 && !creating && <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>No groups yet.</p>}
      {d.groups.map(g => (
        <div key={g.name} style={{ borderTop: '1px solid var(--border-subtle)', padding: '10px 0' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
            <strong>{g.name}</strong>
            <span className="badge badge-neutral">{TOPOLOGY[g.topology]?.label ?? g.topology}</span>
            <span style={{ fontSize: 'var(--text-sm)' }}>owner <strong>{nm(g.owner)}</strong>{g.owner === d.self ? ' (this node)' : ''}</span>
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>epoch {g.epoch} · pools {g.pools.map(p => p.name).join(', ')}</span>
            {g.role === 'owner' && (g.can_write
              ? <span className="badge badge-success">Serving</span>
              : <span className="badge badge-error">Not serving</span>)}
            <span style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
              {g.role === 'owner' && g.topology === 'shared' && g.candidates.filter(c => c !== d.self).map(c => (
                <button key={c} className="btn btn-ghost btn-sm" disabled={move.isPending} onClick={async () => {
                  if (await confirm({ title: `Move ${g.name} to ${nm(c)}?`, message: `The pools are exported here and imported on ${nm(c)}. Clients are interrupted for the moment of the move. If ${nm(c)} cannot import them, this node takes them back.`, confirmLabel: 'Move' })) move.mutate({ name: g.name, target: c })
                }}><Icon name="swap_horiz" size={14} />Move to {nm(c)}</button>
              ))}
              <button className="btn btn-ghost btn-sm" style={{ color: 'var(--error)' }} onClick={async () => {
                if (await confirm({ title: `Remove group ${g.name}?`, message: 'Only the group definition is removed, on every member; pools and data stay where they are.', confirmLabel: 'Remove', danger: true })) remove.mutate(g.name)
              }} aria-label={`Remove ${g.name}`}><Icon name="delete" size={14} /></button>
            </span>
          </div>
          {g.failover?.reason && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 6, fontSize: 'var(--text-sm)', color: g.failover.action === 'wait' ? 'var(--warning)' : 'var(--text-secondary)' }}>
              <Icon name={g.failover.action === 'wait' ? 'hourglass_top' : 'info'} size={14} />{g.failover.reason}
              {g.role === 'standby' && g.failover.action === 'none' && (
                <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} disabled={takeover.isPending} onClick={async () => {
                  if (await confirm({
                    title: `Take over ${g.name}?`,
                    message: `Only do this when ${nm(g.owner)} is switched off or disconnected from the disks: two nodes must never use the pools at the same time. ZFS multihost still refuses the import if ${nm(g.owner)} is in fact still using them.`,
                    confirmLabel: `${nm(g.owner)} is off - take over`, danger: true,
                  })) takeover.mutate(g.name)
                }}><Icon name="front_hand" size={14} />Take over</button>
              )}
            </div>
          )}
          {g.problems.length > 0 && (
            <ul style={{ color: 'var(--error)', fontSize: 'var(--text-sm)', margin: '6px 0 0' }}>{g.problems.map(p => <li key={p}>{p}</li>)}</ul>
          )}
        </div>
      ))}
      {creating && <CreateGroup data={d} />}
      <ConfirmDialog />
    </div>
  )
}
