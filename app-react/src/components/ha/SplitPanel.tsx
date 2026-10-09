/**
 * SplitPanel - move an HA pair off the shared Patroni database (Design 0001
 * phase 3e).
 *
 * Shown while this node uses the shared database, and during a migration.
 * Each node keeps its copy of the database as its own; the node holding the
 * pools then forms the cluster and turns them into a storage group.
 *
 * Calls: GET /api/ha/split, POST /api/ha/split, POST /api/ha/split/cancel
 */

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'

interface SplitNode {
  machine_id: string
  hostname: string
  ip: string
  patroni_role: string
  status: 'ready' | 'failed' | 'switching' | 'split'
  error: string
}
interface SplitStatus {
  success: boolean
  shared_database: boolean
  self: string
  progress?: string
  plan: null | {
    state: 'planned' | 'go' | 'finished' | 'cancelled'
    coordinator: string
    group_name: string
    topology: string
    pools: string[]
    address: string
    interface: string
    error: string
  }
  nodes: SplitNode[] | null
  preflight?: {
    problems: string[]
    self: string
    other: string
    self_role: string
    pools: string[]
    topology: 'shared' | 'replicated'
    address: string
    interface: string
    interfaces: string[]
  }
}
type Res = { success: boolean; error?: string }

const NODE_STATUS: Record<SplitNode['status'], string> = {
  ready: 'ready',
  failed: 'cannot take part',
  switching: 'switching to its own database',
  split: 'on its own database',
}

export function SplitPanel() {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['ha', 'split'],
    queryFn: ({ signal }) => api.get<SplitStatus>('/api/ha/split', signal),
    refetchInterval: 5_000,
    retry: true, // the daemon restarts during the switch
  })
  const pf = q.data?.preflight
  // The form starts from each new preflight result (keyed below).
  const pfKey = pf ? `${pf.topology}|${pf.address}|${pf.interface}|${pf.pools.join(',')}` : ''
  const cancel = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res>('/api/ha/split/cancel', {})),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['ha', 'split'] }),
    onError: (e: Error) => toast.error(e.message),
  })

  const d = q.data
  if (!d) return null
  const plan = d.plan && d.plan.state !== 'cancelled' ? d.plan : null
  if (!d.shared_database && (!plan || plan.state === 'finished')) {
    if (plan?.state === 'finished') {
      return (
        <div className="card" style={{ marginBottom: 20, fontSize: 'var(--text-sm)' }}>
          <Icon name="check_circle" size={16} style={{ color: 'var(--success)', verticalAlign: 'middle' }} />{' '}
          This node has its own database. The nodes form a cluster and <strong>{plan.group_name}</strong> is a storage group (below).
        </div>
      )
    }
    return null
  }

  return (
    <div className="card" style={{ marginBottom: 20 }}>
      <h3 style={{ marginTop: 0 }}>Leave the shared database</h3>
      <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        Both nodes keep their configuration in one database replicated by Patroni. A node cut off from it cannot save changes,
        and its management can stop when the database fails over. After the migration each node keeps its copy as its own
        database; the nodes stay paired and exchange changes, form a cluster with Corosync, and the pools become a storage group
        whose floating address replaces keepalived's. Patroni, etcd, HAProxy and keepalived are no longer used.
      </p>

      {!plan && pf && (
        <>
          {pf.problems.length > 0 ? (
            <ul style={{ color: 'var(--error)', fontSize: 'var(--text-sm)' }}>{pf.problems.map(p => <li key={p}>{p}</li>)}</ul>
          ) : (
            <SplitForm key={pfKey} pf={pf} />
          )}
        </>
      )}

      {plan && (
        <div style={{ fontSize: 'var(--text-sm)', display: 'flex', flexDirection: 'column', gap: 6 }}>
          <div>
            Migration {plan.state === 'planned' ? 'planned' : 'in progress'}: storage group <strong>{plan.group_name}</strong> ({plan.topology}, pools {plan.pools.join(', ')}
            {plan.address ? `, address ${plan.address} on ${plan.interface}` : ''}).
          </div>
          {(d.nodes ?? []).map(n => (
            <div key={n.machine_id}>
              <Icon name={n.status === 'failed' ? 'error' : n.status === 'split' ? 'check_circle' : 'hourglass_top'} size={14}
                style={{ verticalAlign: 'middle', color: n.status === 'failed' ? 'var(--error)' : n.status === 'split' ? 'var(--success)' : 'var(--text-secondary)' }} />{' '}
              {n.hostname} ({n.ip}{n.patroni_role ? `, ${n.patroni_role}` : ''}): {NODE_STATUS[n.status] ?? n.status}{n.error ? ` - ${n.error}` : ''}
            </div>
          ))}
          {d.progress && <div style={{ color: 'var(--text-secondary)' }}>This node: {d.progress}</div>}
          {plan.error && <div style={{ color: 'var(--warning)' }}>{plan.error}</div>}
          {plan.state === 'planned' && (
            <button className="btn btn-ghost btn-sm" style={{ alignSelf: 'flex-start' }} disabled={cancel.isPending} onClick={() => cancel.mutate()}>Cancel</button>
          )}
        </div>
      )}
    </div>
  )
}

function SplitForm({ pf }: { pf: NonNullable<SplitStatus['preflight']> }) {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [groupName, setGroupName] = useState('data')
  const [topology, setTopology] = useState<'shared' | 'replicated'>(pf.topology)
  const [pools, setPools] = useState<string[]>(pf.pools)
  const [address, setAddress] = useState(pf.address)
  const [iface, setIface] = useState(pf.interface || pf.interfaces[0] || '')
  const start = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res>('/api/ha/split', {
      group_name: groupName, topology, pools, address: address.trim(), interface: address.trim() ? iface : '',
    })),
    onSuccess: () => { toast.success('Migration started'); qc.invalidateQueries({ queryKey: ['ha', 'split'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <>
      <ConfirmDialog />
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8, maxWidth: 600, fontSize: 'var(--text-sm)' }}>
        <div>This node: {pf.self} ({pf.self_role}); other node: {pf.other}.</div>
        <label>Storage group name{' '}
          <input className="input" style={{ width: 180, display: 'inline-block' }} value={groupName} onChange={e => setGroupName(e.target.value.trim())} aria-label="Storage group name" />
        </label>
        <label>Storage{' '}
          <select className="input" style={{ width: 'auto', display: 'inline-block' }} value={topology} onChange={e => setTopology(e.target.value as typeof topology)} aria-label="Storage topology">
            <option value="shared">Shared disks (both nodes see the same disks)</option>
            <option value="replicated">Replicated (each node has its own disks)</option>
          </select>
        </label>
        <div>
          Pools:{' '}
          {pf.pools.map(p => (
            <label key={p} style={{ marginRight: 12 }}>
              <input type="checkbox" checked={pools.includes(p)} onChange={() => setPools(pools.includes(p) ? pools.filter(x => x !== p) : [...pools, p])} /> {p}
            </label>
          ))}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap' }}>
          Floating address
          <input className="input" style={{ width: 170 }} placeholder="192.168.1.50/24" value={address} onChange={e => setAddress(e.target.value)} aria-label="Floating address" />
          on
          <select className="input" style={{ width: 'auto' }} value={iface} onChange={e => setIface(e.target.value)} aria-label="Interface">
            {pf.interfaces.map(i => <option key={i} value={i}>{i}</option>)}
          </select>
          {pf.address && <span style={{ color: 'var(--text-tertiary)' }}>(taken from keepalived)</span>}
        </div>
        <p style={{ color: 'var(--text-tertiary)', margin: 0 }}>
          The other node switches first, then this one; each applies a new NixOS configuration and restarts its daemon.
          Data stays imported and shares keep running; the floating address is gone for a moment until the storage group takes it over.
          Until you add a third vote again (the cluster panel shows how), failover is manual.
        </p>
        <button className="btn btn-primary" style={{ alignSelf: 'flex-start' }} disabled={!groupName || pools.length === 0 || start.isPending}
          onClick={async () => {
            if (await confirm({
              title: 'Leave the shared database?',
              message: 'Both nodes apply a new NixOS configuration without Patroni, etcd, HAProxy and keepalived. This cannot be undone from here: going back means setting up Patroni again.',
              confirmLabel: 'Start the migration',
            })) start.mutate()
          }}>
          <Icon name="call_split" size={16} />Start the migration
        </button>
      </div>
    </>
  )
}
