/**
 * pages/PoolsPage.tsx — ZFS Pools (Phase 2)
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/zfs/pools
 *   GET  /api/zfs/datasets
 *   GET  /api/zfs/encryption/list
 *   POST /api/zfs/encryption/unlock
 *   POST /api/zfs/encryption/lock
 *   POST /api/zfs/scrub/start
 *   POST /api/zfs/scrub/stop
 *   GET  /api/zfs/scrub/status
 *   POST /api/zfs/datasets           (create dataset)
 */

import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useWsStore } from '@/stores/ws'
import { Modal } from '@/components/ui/Modal'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ZFSPool {
  name: string; size: string; alloc: string; free: string
  used: string; capacity: string; health: string; type: string
  compression: string; dedup: string
}
interface PoolsResponse { success: boolean; pools?: ZFSPool[]; data?: ZFSPool[] }

interface ZFSDataset {
  name: string; used: string; avail: string; mountpoint: string; quota: string
}
interface DatasetsResponse { success: boolean; data: ZFSDataset[] }

interface EncryptedDataset {
  name: string; encryption: string; keyformat: string; keylocation: string; keystatus: string
}
interface EncryptionListResponse { success: boolean; datasets: EncryptedDataset[] }

interface ScrubStatusResponse { success: boolean; scrub: string }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const healthColor = (h: string) =>
  h === 'ONLINE' ? 'var(--success)' : h === 'DEGRADED' ? 'var(--warning)' : 'var(--error)'

const healthIcon = (h: string) =>
  h === 'ONLINE' ? 'check_circle' : h === 'DEGRADED' ? 'warning' : 'error'

const capacityColor = (pct: number) =>
  pct >= 90 ? 'var(--error)' : pct >= 75 ? 'var(--warning)' : 'var(--primary)'

function parseCapacityPct(capacity: string): number {
  return Math.min(100, Math.max(0, parseInt((capacity || '0').replace('%', '')) || 0))
}

interface TreeNode extends ZFSDataset { children: TreeNode[] }

function buildTree(datasets: ZFSDataset[], poolName: string): TreeNode[] {
  const nodes: Record<string, TreeNode> = {}
  datasets.forEach(d => { nodes[d.name] = { ...d, children: [] } })
  datasets.forEach(d => {
    const parts = d.name.split('/')
    if (parts.length > 1) {
      const parentName = parts.slice(0, -1).join('/')
      if (nodes[parentName]) nodes[parentName].children.push(nodes[d.name])
    }
  })
  return nodes[poolName] ? [nodes[poolName]] : []
}

// ---------------------------------------------------------------------------
// DatasetNode (recursive)
// ---------------------------------------------------------------------------

function DatasetNode({ node, depth, onCreateChild }: {
  node: TreeNode; depth: number; onCreateChild: (name: string) => void
}) {
  const [open, setOpen] = useState(depth === 0)
  const shortName = node.name.split('/').pop() || node.name
  const isMounted = node.mountpoint && node.mountpoint !== 'none' && node.mountpoint !== '-'
  return (
    <div>
      <div
        style={{ display: 'flex', alignItems: 'center', gap: 8, padding: `8px 12px 8px ${12 + depth * 20}px`, borderRadius: 'var(--radius-sm)', transition: 'background 0.15s' }}
        onMouseEnter={e => (e.currentTarget.style.background = 'var(--surface-hover)')}
        onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
      >
        {node.children.length > 0
          ? <button onClick={() => setOpen(o => !o)} style={{ background: 'none', border: 'none', cursor: 'pointer', padding: 0, color: 'var(--text-tertiary)', display: 'flex' }}>
              <Icon name={open ? 'expand_more' : 'chevron_right'} size={16} />
            </button>
          : <span style={{ width: 16 }} />
        }
        <Icon name={depth === 0 ? 'storage' : node.children.length > 0 ? 'folder' : 'dataset'} size={16}
          style={{ color: depth === 0 ? 'var(--primary)' : 'var(--text-tertiary)', flexShrink: 0 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 'var(--text-sm)', fontWeight: depth === 0 ? 700 : 500 }}>{shortName}</div>
          {isMounted && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)' }}>{node.mountpoint}</div>}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 16, flexShrink: 0 }}>
          <div style={{ textAlign: 'right' }}>
            <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, fontFamily: 'var(--font-mono)' }}>{node.used || '—'}</div>
            <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>used</div>
          </div>
          <div style={{ textAlign: 'right' }}>
            <div style={{ fontSize: 'var(--text-sm)', fontFamily: 'var(--font-mono)' }}>{node.avail || '—'}</div>
            <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>avail</div>
          </div>
          <button onClick={() => onCreateChild(node.name)} className="btn btn-sm btn-ghost" title="New child dataset">
            <Icon name="add" size={13} />
          </button>
        </div>
      </div>
      {open && node.children.map(c => <DatasetNode key={c.name} node={c} depth={depth + 1} onCreateChild={onCreateChild} />)}
    </div>
  )
}

// ---------------------------------------------------------------------------
// CreateDatasetModal
// ---------------------------------------------------------------------------

function CreateDatasetModal({ parentName, onClose, onCreated }: {
  parentName: string; onClose: () => void; onCreated: () => void
}) {
  const [childName, setChildName] = useState('')
  const [compression, setCompression] = useState('lz4')
  const [quota, setQuota] = useState('')

  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/datasets', {
      name: `${parentName}/${childName}`,
      mountpoint: `/${parentName}/${childName}`,
      quota,
      compression,
    }),
    onSuccess: () => { toast.success(`Dataset ${parentName}/${childName} created`); onCreated(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (!childName.trim()) { toast.error('Dataset name required'); return }
    if (!/^[a-zA-Z0-9_-]+$/.test(childName)) { toast.error('Name: letters, numbers, - and _ only'); return }
    mutation.mutate()
  }

  return (
    <Modal title={<>New Dataset under <span style={{ color: 'var(--primary)', fontFamily: 'var(--font-mono)' }}>{parentName}</span></>} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Dataset Name</span>
          <input value={childName} onChange={e => setChildName(e.target.value)} placeholder="e.g. photos"
            className="input" onKeyDown={e => e.key === 'Enter' && submit()} autoFocus />
        </label>
        <label className="field">
          <span className="field-label">Compression</span>
          <select value={compression} onChange={e => setCompression(e.target.value)} className="input">
            <option value="lz4">LZ4 (recommended)</option>
            <option value="zstd">ZSTD (best ratio)</option>
            <option value="gzip">GZIP</option>
            <option value="off">Off</option>
          </select>
        </label>
        <label className="field">
          <span className="field-label">Quota (optional, e.g. 100G)</span>
          <input value={quota} onChange={e => setQuota(e.target.value)} placeholder="100G" className="input" />
        </label>
      </div>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 24 }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} disabled={mutation.isPending} className="btn btn-primary">
          {mutation.isPending ? 'Creating…' : 'Create Dataset'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// PoolCard
// ---------------------------------------------------------------------------

function PoolCard({ pool, datasets, onRefresh }: { pool: ZFSPool; datasets: ZFSDataset[]; onRefresh: () => void }) {
  const qc = useQueryClient()
  const [treeOpen, setTreeOpen] = useState(true)
  const [createParent, setCreateParent] = useState<string | null>(null)
  const pct = parseCapacityPct(pool.capacity)
  const tree = buildTree(datasets, pool.name)

  const scrubQ = useQuery({
    queryKey: ['zfs', 'scrub', 'status', pool.name],
    queryFn: ({ signal }) => api.get<ScrubStatusResponse>(`/api/zfs/scrub/status?pool=${encodeURIComponent(pool.name)}`, signal),
    refetchInterval: 10_000,
  })
  const scrubStart = useMutation({
    mutationFn: () => api.post('/api/zfs/scrub/start', { pool: pool.name }),
    onSuccess: () => { toast.success('Scrub started'); qc.invalidateQueries({ queryKey: ['zfs', 'scrub', 'status', pool.name] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const scrubStop = useMutation({
    mutationFn: () => api.post('/api/zfs/scrub/stop', { pool: pool.name }),
    onSuccess: () => { toast.success('Scrub stopped'); qc.invalidateQueries({ queryKey: ['zfs', 'scrub', 'status', pool.name] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const scrubText = scrubQ.data?.scrub || ''
  const isScrubbing = /in progress|scrubbing/i.test(scrubText)

  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 28, borderLeft: `4px solid ${healthColor(pool.health)}` }}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 16, marginBottom: 16 }}>
        <Icon name="storage" size={32} style={{ color: 'var(--primary)', flexShrink: 0 }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontSize: 'var(--text-xl)', fontWeight: 700 }}>{pool.name}</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: 2, fontFamily: 'var(--font-mono)' }}>
            {pool.type || 'unknown'} · {pool.used || 'N/A'} / {pool.size || 'N/A'} ({pool.capacity || '0%'})
          </div>
        </div>
        {/* Health badge */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '5px 12px', borderRadius: 'var(--radius-full)', background: `${healthColor(pool.health)}18`, border: `1px solid ${healthColor(pool.health)}40`, color: healthColor(pool.health), fontSize: 'var(--text-sm)', fontWeight: 600 }}>
          <Icon name={healthIcon(pool.health)} size={15} />
          {pool.health}
        </div>
        {/* Scrub btn */}
        <button
          onClick={() => isScrubbing ? scrubStop.mutate() : scrubStart.mutate()}
          disabled={scrubStart.isPending || scrubStop.isPending}
          style={{ background: isScrubbing ? 'var(--warning-bg)' : 'var(--surface)', border: `1px solid ${isScrubbing ? 'var(--warning-border)' : 'var(--border)'}`, borderRadius: 'var(--radius-sm)', padding: '7px 12px', cursor: 'pointer', color: isScrubbing ? 'var(--warning)' : 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-sm)' }}
        >
          <Icon name={isScrubbing ? 'stop_circle' : 'cleaning_services'} size={16} />
          {isScrubbing ? 'Stop Scrub' : 'Scrub'}
        </button>
      </div>

      {/* Capacity bar */}
      <div style={{ height: 8, background: 'rgba(255,255,255,0.05)', borderRadius: 999, overflow: 'hidden', marginBottom: 12 }}>
        <div style={{ height: '100%', width: `${pct}%`, background: capacityColor(pct), borderRadius: 999, transition: 'width 0.5s' }} />
      </div>

      {/* Meta tags */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 16, flexWrap: 'wrap' }}>
        {[
          { label: 'Compression', value: pool.compression || 'off' },
          { label: 'Dedup', value: pool.dedup || 'off' },
        ].map(m => (
          <span key={m.label} style={{ padding: '4px 10px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            {m.label}: <strong>{m.value}</strong>
          </span>
        ))}
        {scrubText && (
          <span style={{ padding: '4px 10px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)' }}>
            {scrubText.split('\n')[0].trim().slice(0, 60)}
          </span>
        )}
      </div>

      {/* Dataset tree */}
      {tree.length > 0 && (
        <div style={{ borderTop: '1px solid var(--border)', paddingTop: 12 }}>
          <button onClick={() => setTreeOpen(o => !o)} style={{ width: '100%', background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 8, padding: '4px 0 8px', color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
            <Icon name={treeOpen ? 'expand_more' : 'chevron_right'} size={16} />
            <Icon name="account_tree" size={14} />
            Datasets ({datasets.length})
          </button>
          {treeOpen && tree.map(n => <DatasetNode key={n.name} node={n} depth={0} onCreateChild={setCreateParent} />)}
        </div>
      )}

      <button
        onClick={() => setCreateParent(pool.name)}
        style={{ marginTop: 12, background: 'none', border: '1px dashed var(--border)', borderRadius: 'var(--radius-sm)', padding: '8px 14px', cursor: 'pointer', color: 'var(--text-tertiary)', display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-sm)', width: '100%', transition: 'all 0.15s' }}
        onMouseEnter={e => { (e.currentTarget as HTMLButtonElement).style.borderColor = 'var(--primary)'; (e.currentTarget as HTMLButtonElement).style.color = 'var(--primary)' }}
        onMouseLeave={e => { (e.currentTarget as HTMLButtonElement).style.borderColor = 'var(--border)'; (e.currentTarget as HTMLButtonElement).style.color = 'var(--text-tertiary)' }}
      >
        <Icon name="add" size={16} /> Add Dataset
      </button>

      {createParent !== null && (
        <CreateDatasetModal
          parentName={createParent}
          onClose={() => setCreateParent(null)}
          onCreated={() => { qc.invalidateQueries({ queryKey: ['zfs', 'datasets'] }); onRefresh() }}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// EncryptionTab
// ---------------------------------------------------------------------------

function EncryptionTab() {
  const qc = useQueryClient()
  const [unlockTarget, setUnlockTarget] = useState<string | null>(null)
  const [passphrase, setPassphrase] = useState('')

  const encQ = useQuery({
    queryKey: ['zfs', 'encryption'],
    queryFn: ({ signal }) => api.get<EncryptionListResponse>('/api/zfs/encryption/list', signal),
    refetchInterval: 30_000,
  })
  const unlock = useMutation({
    mutationFn: ({ name, passphrase }: { name: string; passphrase: string }) =>
      api.post('/api/zfs/encryption/unlock', { name, passphrase }),
    onSuccess: () => { toast.success('Dataset unlocked'); qc.invalidateQueries({ queryKey: ['zfs', 'encryption'] }); setUnlockTarget(null) },
    onError: (e: Error) => toast.error(e.message),
  })
  const lock = useMutation({
    mutationFn: (name: string) => api.post('/api/zfs/encryption/lock', { name }),
    onSuccess: () => { toast.success('Dataset locked'); qc.invalidateQueries({ queryKey: ['zfs', 'encryption'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  if (encQ.isLoading) return <Skeleton height={120} />
  if (encQ.isError) return <ErrorState error={encQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['zfs', 'encryption'] })} />

  const datasets = encQ.data?.datasets ?? []

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      {datasets.length === 0 && (
        <div style={{ textAlign: 'center', padding: '48px 0', color: 'var(--text-tertiary)' }}>
          <Icon name="lock_open" size={40} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          No encrypted datasets found
        </div>
      )}
      {datasets.map(d => {
        const locked = d.keystatus === 'unavailable'
        return (
          <div key={d.name} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '16px 20px', display: 'flex', alignItems: 'center', gap: 16 }}>
            <Icon name={locked ? 'lock' : 'lock_open'} size={22} style={{ color: locked ? 'var(--warning)' : 'var(--success)', flexShrink: 0 }} />
            <div style={{ flex: 1 }}>
              <div style={{ fontWeight: 600, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }}>{d.name}</div>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>{d.encryption} · {d.keyformat}</div>
            </div>
            <span style={{ padding: '3px 10px', borderRadius: 'var(--radius-full)', background: locked ? 'var(--warning-bg)' : 'var(--success-bg)', border: `1px solid ${locked ? 'var(--warning-border)' : 'var(--success-border)'}`, color: locked ? 'var(--warning)' : 'var(--success)', fontSize: 'var(--text-2xs)', fontWeight: 700 }}>
              {locked ? 'LOCKED' : 'UNLOCKED'}
            </span>
            {locked
              ? <button className="btn btn-sm btn-ghost" onClick={() => { setUnlockTarget(d.name); setPassphrase('') }}>Unlock</button>
              : <button className="btn btn-sm btn-danger" onClick={() => lock.mutate(d.name)} disabled={lock.isPending}>Lock</button>
            }
          </div>
        )
      })}

      {/* Unlock modal */}
      {unlockTarget && (
        <Modal title="Unlock Dataset" onClose={() => setUnlockTarget(null)}>
          <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 20, fontFamily: 'var(--font-mono)' }}>{unlockTarget}</p>
          <input type="password" value={passphrase} onChange={e => setPassphrase(e.target.value)}
            placeholder="Passphrase" className="input" autoFocus
            onKeyDown={e => e.key === 'Enter' && passphrase && unlock.mutate({ name: unlockTarget, passphrase })}
          />
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 20 }}>
            <button onClick={() => setUnlockTarget(null)} className="btn btn-ghost">Cancel</button>
            <button disabled={!passphrase || unlock.isPending} onClick={() => unlock.mutate({ name: unlockTarget, passphrase })} className="btn btn-primary">
              {unlock.isPending ? 'Unlocking…' : 'Unlock'}
            </button>
          </div>
        </Modal>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// PoolsPage
// ---------------------------------------------------------------------------

type Tab = 'pools' | 'encryption'

export function PoolsPage() {
  const [tab, setTab] = useState<Tab>('pools')
  const qc   = useQueryClient()
  const wsOn = useWsStore((s) => s.on)

  const poolsQ = useQuery({
    queryKey: ['zfs', 'pools'],
    queryFn: ({ signal }) => api.get<PoolsResponse>('/api/zfs/pools', signal),
    refetchInterval: 60_000, // fallback poll — WS events trigger immediate refetch
  })
  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets'],
    queryFn: ({ signal }) => api.get<DatasetsResponse>('/api/zfs/datasets', signal),
    refetchInterval: 60_000,
  })

  // WS: pool health changes → immediate refetch
  useEffect(() => {
    return wsOn('poolHealthChange', () => {
      qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })
    })
  }, [wsOn, qc])

  // WS: scrub events → refetch pool list (scrub status embedded in pool output)
  useEffect(() => {
    return wsOn('scrubEvent', () => {
      qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })
    })
  }, [wsOn, qc])

  // WS: resilver events → refetch pools + show toast on complete
  useEffect(() => {
    const unsub1 = wsOn('resilverStarted', () => {
      qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })
      toast.info('Resilver started')
    })
    const unsub2 = wsOn('resilverProgress', () => {
      qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })
    })
    const unsub3 = wsOn('resilverCompleted', () => {
      qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })
      toast.success('Resilver completed')
    })
    return () => { unsub1(); unsub2(); unsub3() }
  }, [wsOn, qc])

  const pools = poolsQ.data?.pools ?? poolsQ.data?.data ?? []
  const datasets = datasetsQ.data?.data ?? []

  function refresh() {
    qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })
    qc.invalidateQueries({ queryKey: ['zfs', 'datasets'] })
  }

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'pools', label: 'Pools & Datasets', icon: 'storage' },
    { id: 'encryption', label: 'Encryption', icon: 'lock' },
  ]

  return (
    <div style={{ maxWidth: 1100 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 32 }}>
        <div>
          <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>ZFS Storage</h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>Pools · Datasets · Scrub · Encryption</p>
        </div>
        <button onClick={refresh} className="btn btn-ghost" title="Refresh"><Icon name="refresh" size={16} /></button>
      </div>

      {/* Tabs */}
      <div className="tabs-underline" style={{ marginBottom: 28 }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {/* Pools content */}
      {tab === 'pools' && (
        <>
          {poolsQ.isLoading && <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>{[0, 1].map(i => <Skeleton key={i} height={200} style={{ borderRadius: 'var(--radius-xl)' }} />)}</div>}
          {poolsQ.isError && <ErrorState error={poolsQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })} />}
          {!poolsQ.isLoading && !poolsQ.isError && pools.length === 0 && (
            <div style={{ textAlign: 'center', padding: '64px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
              <Icon name="storage" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
              <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600 }}>No ZFS pools found</div>
            </div>
          )}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
            {pools.map(pool => (
              <PoolCard key={pool.name} pool={pool}
                datasets={datasets.filter(d => d.name === pool.name || d.name.startsWith(pool.name + '/'))}
                onRefresh={refresh}
              />
            ))}
          </div>
        </>
      )}

      {/* Encryption content */}
      {tab === 'encryption' && (
        <>
          <div className="alert alert-info" style={{ marginBottom: 16 }}>
            <Icon name="info" size={18} style={{ color: 'var(--info)', flexShrink: 0, marginTop: 1 }} />
            ZFS native encryption. Locked datasets are inaccessible until unlocked with the passphrase.
          </div>
          <EncryptionTab />
        </>
      )}
    </div>
  )
}
