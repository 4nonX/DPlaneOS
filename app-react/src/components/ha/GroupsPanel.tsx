/**
 * GroupsPanel - storage groups (Design 0001 phase 3b).
 *
 * A storage group is the unit of ownership: pools, the nodes that may own
 * them, a topology, the current owner and the epoch (increases with every
 * change of owner; a node with an older epoch must not write).
 *
 * Calls: GET/POST /api/groups, POST /api/groups/{name}/move, DELETE /api/groups/{name},
 *        GET /api/ha/hardware/detect, POST /api/cluster/test/replication {group}
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
  changing?: boolean // a move or takeover is importing or releasing the pools here
  problems: string[]
  failover: { action: 'none' | 'wait' | 'takeover' | ''; reason?: string; wait_until?: string }
  interval_secs: number
  auto_failover: boolean
  replication: { direction: 'out' | 'in'; peer: string; pool: string; last_snapshot: string; last_ok_at: string | null; last_error: string; discard_ok: boolean }[]
  splits?: { pool: string; name: string; used: number; created: string }[]
  resources?: { stacks: { name: string }[]; nvme: { subsystem_nqn: string; zvol: string }[] }
  address?: string
  interface?: string
}

function ago(iso: string | null) {
  if (!iso) return 'never'
  const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000))
  if (s < 90) return `${s} s ago`
  if (s < 5400) return `${Math.round(s / 60)} min ago`
  return `${Math.round(s / 3600)} h ago`
}
export interface GroupsResponse {
  success: boolean
  groups: GroupStatus[]
  self: string
  names: Record<string, string>
  members: { key: string; name: string }[]
  imported_pools: string[]
  interfaces?: string[]
}
type Res = { success: boolean; error?: string }

const TOPOLOGY: Record<string, { label: string; text: string }> = {
  standalone: { label: 'Standalone', text: 'One node; no failover.' },
  shared:     { label: 'Shared storage', text: 'All candidates see the same disks (SAS or SATA JBOD, SAN, NVMe-oF). Zero data loss on failover; only one node imports the pools at a time.' },
  replicated: { label: 'Replicated', text: 'Each candidate has its own disks, kept in sync by ZFS replication. Works with any drives; on failover you lose up to one replication interval.' },
}

interface HWDetect { provisional_path: 'shared_storage' | 'replicated'; provisional_reason: string; probe_required: boolean; watchdog_available: boolean }

/** What happens when a node fails, per topology, given the cluster's votes. */
const ON_FAILURE: Record<string, string> = {
  standalone: 'If this node fails, the data waits on its disks until the node is back.',
  shared: 'If the owner fails, another candidate imports the same disks: with a third vote automatically after the watchdog delay, otherwise when you click Take over. Nothing written before the failure is lost.',
  replicated: 'If the owner fails, another candidate makes its copy writable: automatically only if you allow it below and there is a third vote, otherwise when you click Take over. Changes since the last replication are lost.',
}

export function CreateGroup({ data, onCreated }: { data: GroupsResponse; onCreated?: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [topology, setTopology] = useState<'standalone' | 'shared' | 'replicated'>(data.members.length > 1 ? 'shared' : 'standalone')
  const [pools, setPools] = useState<string[]>([])
  const others = data.members.filter(m => m.key !== data.self)
  // Store the nodes left out, not the ones chosen: members that appear after
  // the form opened (e.g. the cluster was just formed) are candidates too.
  const [excluded, setExcluded] = useState<string[]>([])
  const cands = others.map(m => m.key).filter(k => !excluded.includes(k))
  const [intervalMin, setIntervalMin] = useState(5)
  const [autoFailover, setAutoFailover] = useState(false)
  const [address, setAddress] = useState('')
  const [iface, setIface] = useState(data.interfaces?.[0] ?? '')
  const free = data.imported_pools.filter(p => !data.groups.some(g => g.pools.some(gp => gp.name === p)))
  const create = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res>('/api/groups', {
      name, topology, pools, candidates: topology === 'standalone' ? [] : cands,
      interval_secs: intervalMin * 60, auto_failover: autoFailover,
      address: topology === 'standalone' ? '' : address.trim(), interface: address.trim() ? iface : '',
    })),
    onSuccess: () => {
      toast.success(`Group ${name} created`); setName(''); setPools([])
      qc.invalidateQueries({ queryKey: ['groups'] }); qc.invalidateQueries({ queryKey: ['cluster'] })
      onCreated?.()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const toggle = (list: string[], set: (v: string[]) => void, v: string) => set(list.includes(v) ? list.filter(x => x !== v) : [...list, v])
  const hw = useQuery({
    queryKey: ['ha', 'hardware'],
    queryFn: ({ signal }) => api.get<HWDetect & { success: boolean }>('/api/ha/hardware/detect', signal),
    enabled: data.members.length > 1,
    staleTime: 60_000,
  })

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginTop: 12, maxWidth: 560 }}>
      <h4 style={{ margin: 0 }}>New storage group</h4>
      <input className="input" placeholder="Name, e.g. data" value={name} onChange={e => setName(e.target.value.trim())} aria-label="Group name" />
      <select className="input" value={topology} onChange={e => setTopology(e.target.value as typeof topology)} aria-label="Topology">
        {Object.entries(TOPOLOGY).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}
      </select>
      <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: 0 }}>{TOPOLOGY[topology].text} {ON_FAILURE[topology]}</p>
      {hw.data && topology !== 'standalone' && (
        <p style={{ fontSize: 'var(--text-xs)', margin: 0, color: (hw.data.provisional_path === 'shared_storage') === (topology === 'shared') ? 'var(--text-secondary)' : 'var(--warning)' }}>
          <Icon name="memory" size={12} /> This node's disks: {hw.data.provisional_reason}
          {hw.data.provisional_path === 'replicated' && topology === 'shared' && ' Shared storage needs disks that every candidate sees.'}
        </p>
      )}
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
              <input type="checkbox" checked={cands.includes(m.key)} onChange={() => toggle(excluded, setExcluded, m.key)} /> {m.name}
            </label>
          ))}
        </div>
      )}
      {topology === 'replicated' && (
        <div style={{ fontSize: 'var(--text-sm)', display: 'flex', flexDirection: 'column', gap: 6 }}>
          <label>
            Replicate every{' '}
            <input type="number" min={1} max={1440} className="input" style={{ width: 80, display: 'inline-block' }} value={intervalMin}
              onChange={e => setIntervalMin(Math.max(1, Number(e.target.value) || 5))} aria-label="Replication interval in minutes" /> minutes
            <span style={{ color: 'var(--text-tertiary)' }}> (the most data a failover can lose)</span>
          </label>
          <label>
            <input type="checkbox" checked={autoFailover} onChange={e => setAutoFailover(e.target.checked)} /> Fail over automatically
            <span style={{ color: 'var(--text-tertiary)' }}> (needs a third vote; changes since the last replication are lost. Off: you take over manually.)</span>
          </label>
          <p style={{ color: 'var(--text-tertiary)', margin: 0 }}>The other nodes need a pool with the same name; its contents are replaced by the first replication if it is empty.</p>
        </div>
      )}
      {topology !== 'standalone' && (
        <AddressFields address={address} setAddress={setAddress} iface={iface} setIface={setIface} interfaces={data.interfaces ?? []} />
      )}
      <button className="btn btn-primary" style={{ alignSelf: 'flex-start' }} disabled={!name || pools.length === 0 || create.isPending} onClick={() => create.mutate()}>
        <Icon name="add" size={16} />Create group
      </button>
    </div>
  )
}

function AddressFields({ address, setAddress, iface, setIface, interfaces }: {
  address: string; setAddress: (v: string) => void; iface: string; setIface: (v: string) => void; interfaces: string[]
}) {
  return (
    <div style={{ fontSize: 'var(--text-sm)', display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6 }}>
      Floating address
      <input className="input" style={{ width: 170 }} placeholder="192.168.1.50/24" value={address} onChange={e => setAddress(e.target.value)} aria-label="Floating address" />
      on
      <select className="input" style={{ width: 'auto' }} value={iface} onChange={e => setIface(e.target.value)} aria-label="Interface">
        {interfaces.map(i => <option key={i} value={i}>{i}</option>)}
      </select>
      <span style={{ color: 'var(--text-tertiary)', flexBasis: '100%' }}>Optional. Clients use this address for the group's shares; it moves with the group. The interface needs the same name on every node.</span>
    </div>
  )
}

function AddressEditor({ g, interfaces, onDone }: { g: GroupStatus; interfaces: string[]; onDone: () => void }) {
  const qc = useQueryClient()
  const [address, setAddress] = useState(g.address ?? '')
  const [iface, setIface] = useState(g.interface || interfaces[0] || '')
  const save = useMutation({
    mutationFn: async () => ensureOk(await api.put<Res>(`/api/groups/${encodeURIComponent(g.name)}/address`, { address: address.trim(), interface: address.trim() ? iface : '' })),
    onSuccess: () => { toast.success('Address saved'); qc.invalidateQueries({ queryKey: ['groups'] }); onDone() },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 6, flexWrap: 'wrap' }}>
      <AddressFields address={address} setAddress={setAddress} iface={iface} setIface={setIface} interfaces={interfaces} />
      <button className="btn btn-primary btn-sm" disabled={save.isPending} onClick={() => save.mutate()}>Save</button>
      <button className="btn btn-ghost btn-sm" onClick={onDone}>Cancel</button>
    </div>
  )
}

interface ReplCheck { target: string; pool: string; ok: boolean; detail: string }

function ReplicationTest({ group, name }: { group: string; name: (k: string) => string }) {
  const [res, setRes] = useState<{ ok: boolean; detail: string; checks?: ReplCheck[] } | null>(null)
  const test = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; error?: string; result: { ok: boolean; detail: string; checks?: ReplCheck[] } }>('/api/cluster/test/replication', { group })),
    onSuccess: r => setRes(r.result),
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div style={{ marginTop: 6, fontSize: 'var(--text-sm)' }}>
      <button className="btn btn-ghost btn-sm" disabled={test.isPending} onClick={() => test.mutate()}>
        <Icon name="network_check" size={14} />{test.isPending ? 'Testing…' : 'Test replication'}
      </button>
      {res && (
        <div style={{ color: res.ok ? 'var(--success)' : 'var(--error)' }}>
          {res.detail}
          {(res.checks ?? []).map(c => (
            <div key={c.target + c.pool} style={{ color: c.ok ? 'var(--text-secondary)' : 'var(--error)' }}>{c.pool} → {name(c.target)}: {c.detail}</div>
          ))}
        </div>
      )}
    </div>
  )
}

export function GroupsPanel() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [creating, setCreating] = useState(false)
  const [editingAddr, setEditingAddr] = useState('')
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
  const replicate = useMutation({
    mutationFn: async (name: string) => ensureOk(await api.post<Res>(`/api/groups/${encodeURIComponent(name)}/replicate`, {})),
    onSuccess: () => { toast.success('Replicated'); qc.invalidateQueries({ queryKey: ['groups'] }) },
    onError: (e: Error) => { toast.error(e.message); qc.invalidateQueries({ queryKey: ['groups'] }) },
  })
  // Discarding keeps the changes on the owner first (a separate copy); only
  // when that is impossible may the operator discard them for good.
  type DiscardRes = Res & { kept?: string[]; can_discard_without_keeping?: boolean }
  const discard = useMutation({
    mutationFn: async (v: { name: string; keep: boolean }) =>
      await api.post<DiscardRes>(`/api/groups/${encodeURIComponent(v.name)}/discard-divergent`, { keep: v.keep }),
    onSuccess: async (r, v) => {
      qc.invalidateQueries({ queryKey: ['groups'] })
      if (r.success) {
        toast.success(r.kept?.length ? `Kept on the owner as ${r.kept.join(', ')}; the next replication replaces the changes here` : 'The next replication replaces the changes here')
        return
      }
      if (r.can_discard_without_keeping && await confirm({
        title: 'The changes cannot be kept',
        message: `${r.error ?? ''}. Discard them for good? Copy off anything you need first.`,
        confirmLabel: 'Discard for good', danger: true,
      })) discard.mutate({ name: v.name, keep: false })
      else if (!r.can_discard_without_keeping) toast.error(r.error ?? 'Failed')
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const deleteSplit = useMutation({
    mutationFn: async (v: { group: string; pool: string; name: string }) =>
      ensureOk(await api.delete<Res>(`/api/groups/${encodeURIComponent(v.group)}/splits/${encodeURIComponent(v.pool)}/${encodeURIComponent(v.name)}`)),
    onSuccess: () => { toast.success('Kept copy deleted; replication removes it from the other copies'); qc.invalidateQueries({ queryKey: ['groups'] }) },
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
            {g.changing && <span className="badge badge-warning">Changing owner</span>}
            {g.role === 'owner' && !g.changing && (g.can_write
              ? <span className="badge badge-success">Serving</span>
              : <span className="badge badge-error">Not serving</span>)}
            <span style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
              {g.role === 'owner' && g.topology === 'shared' && g.problems.some(p => p.startsWith('Pools not imported')) && (
                <button className="btn btn-ghost btn-sm" disabled={takeover.isPending} onClick={async () => {
                  if (await confirm({
                    title: `Import the pools of ${g.name} here?`,
                    message: `Only do this when the other nodes of the group are switched off or disconnected from the disks: two nodes must never use the pools at the same time. ZFS multihost still refuses the import if another node is in fact using them.`,
                    confirmLabel: 'The other nodes are off - import', danger: true,
                  })) takeover.mutate(g.name)
                }}><Icon name="download" size={14} />Import here</button>
              )}
              {g.role === 'owner' && g.topology === 'replicated' && (
                <button className="btn btn-ghost btn-sm" disabled={replicate.isPending} onClick={() => replicate.mutate(g.name)}>
                  <Icon name="sync" size={14} />Replicate now
                </button>
              )}
              {g.role === 'owner' && g.topology !== 'standalone' && g.candidates.filter(c => c !== d.self).map(c => (
                <button key={c} className="btn btn-ghost btn-sm" disabled={move.isPending} onClick={async () => {
                  if (await confirm({ title: `Move ${g.name} to ${nm(c)}?`, message: g.topology === 'replicated'
                    ? `This copy becomes read-only, a final replication brings ${nm(c)} up to date, and ${nm(c)}'s copy becomes the writable one; replication then runs from ${nm(c)}. If anything fails, this node stays the owner.`
                    : `The pools are exported here and imported on ${nm(c)}. Clients are interrupted for the moment of the move. If ${nm(c)} cannot import them, this node takes them back.`, confirmLabel: 'Move' })) move.mutate({ name: g.name, target: c })
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
          {g.topology === 'replicated' && g.replication.length > 0 && (
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>
              {g.replication.filter(r => r.direction === 'out').map(r => (
                <div key={r.peer + r.pool}>{r.pool} → {nm(r.peer)}: last replication {ago(r.last_ok_at)}{r.last_error ? ' (last attempt failed)' : ''}</div>
              ))}
              {g.replication.filter(r => r.direction === 'in').map(r => (
                <div key={'in' + r.peer + r.pool}>{r.pool} from {nm(r.peer)}: last received {ago(r.last_ok_at)}</div>
              ))}
            </div>
          )}
          {g.topology === 'replicated' && g.role === 'owner' && <ReplicationTest group={g.name} name={nm} />}
          {g.role === 'standby' && g.replication.some(r => r.direction === 'in' && r.last_error.includes('changes') && !r.discard_ok) && (
            <button className="btn btn-ghost btn-sm" style={{ color: 'var(--error)', marginTop: 6 }} onClick={async () => {
              if (await confirm({
                title: `Discard the changes on this node's copy of ${g.name}?`,
                message: `This copy was changed after the last replication from ${nm(g.owner)} (for example while it was the owner during a network split). The changes are first kept on ${nm(g.owner)} as a separate copy (listed under the group, on every node, until you delete it); then the next replication rolls this copy back to ${nm(g.owner)}'s data.`,
                confirmLabel: 'Keep the changes on the owner and discard them here', danger: true,
              })) discard.mutate({ name: g.name, keep: true })
            }}><Icon name="restore" size={14} />Discard the changes here</button>
          )}
          {(g.splits ?? []).length > 0 && (
            <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>
              <div style={{ color: 'var(--text-secondary)' }}>Kept copies of discarded changes (browse them under the pool's mount point):</div>
              {(g.splits ?? []).map(s => (
                <div key={s.pool + s.name} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <Icon name="inventory_2" size={14} /><code>{s.pool}/{s.name}</code>
                  <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{(s.used / 1048576).toFixed(1)} MiB · {new Date(s.created).toLocaleString()}</span>
                  {g.role === 'owner' && (
                    <button className="btn btn-ghost btn-sm" aria-label={`Delete ${s.name}`} disabled={deleteSplit.isPending} onClick={async () => {
                      if (await confirm({ title: `Delete the kept copy ${s.name}?`, message: 'The changes it holds are deleted on every node.', confirmLabel: 'Delete', danger: true }))
                        deleteSplit.mutate({ group: g.name, pool: s.pool, name: s.name })
                    }}><Icon name="delete" size={14} /></button>
                  )}
                </div>
              ))}
            </div>
          )}
          {g.topology !== 'standalone' && (editingAddr === g.name
            ? <AddressEditor g={g} interfaces={d.interfaces ?? []} onDone={() => setEditingAddr('')} />
            : (
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: 6 }}>
                <Icon name="lan" size={14} />Floating address: {g.address ? <strong>{g.address} on {g.interface}</strong> : 'none'}
                {g.role === 'owner' && <button className="btn btn-ghost btn-sm" onClick={() => setEditingAddr(g.name)}>Change</button>}
              </div>
            ))}
          {g.resources && (g.resources.stacks.length > 0 || g.resources.nvme.length > 0) && (
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: 6 }}>
              Runs on the owner only:{' '}
              {[...g.resources.stacks.map(st => `stack ${st.name}`), ...g.resources.nvme.map(e => `NVMe-oF ${e.zvol}`)].join(', ')}
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
