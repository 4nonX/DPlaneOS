/**
 * pages/PoolsPage.tsx - ZFS Pools (Phase 2)
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/zfs/pools
 *   GET  /api/zfs/datasets
 *   GET  /api/zfs/encryption/list
 *   POST /api/zfs/encryption/unlock
 *   POST /api/zfs/encryption/lock
 *   POST /api/zfs/scrub/start
 *   POST /api/zfs/scrub/stop
 *   GET  /api/zfs/scrub/status?pool=X
 *   GET  /api/zfs/scrub/schedule?pool=X
 *   POST /api/zfs/scrub/schedule
 *   GET  /api/zfs/resilver/status?pool=X
 *   POST /api/zfs/datasets           (create dataset)
 *   GET  /api/zfs/datasets/search    (global dataset search - used by search bar)
 *   // NEW: Pool Lifecycle
 *   GET  /api/zfs/disks              (get list of unassigned physical disks)
 *   POST /api/zfs/pools/create       (create a new pool)
 *   POST /api/zfs/pools/expand       (add VDEV to existing pool)
 *   POST /api/zfs/pools/destroy      (destroy a pool)
 */

import { useState, useEffect, useMemo, useRef } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton, Spinner } from '@/components/ui/LoadingSpinner'
import { Tooltip } from '@/components/ui/Tooltip'
import { toast } from '@/hooks/useToast'
import { useWsStore } from '@/stores/ws'
import { Modal } from '@/components/ui/Modal'
import { PoolTopologyView, PoolTopology } from '@/components/zfs/PoolTopology'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ZFSPool {
  name: string; size: string; alloc: string; free: string
  used: string; capacity: string; health: string; type: string
  compression: string; dedup: string
  topology?: PoolTopology
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

interface ScrubStatusResponse {
  success: boolean
  scrub: string
  in_progress?: boolean
  percent_done?: number
  bytes_done?: string
  eta?: string
  errors?: number
  completed?: boolean
  completed_at?: string | null
}

interface ScrubSchedule {
  pool: string
  interval: string  // daily | weekly | monthly
  hour: number
  day: number       // day_of_week for weekly (0=Sun), day_of_month for monthly
}
interface ScrubSchedulesResponse { success: boolean; schedules: ScrubSchedule[] }

interface ResilverStatusResponse {
  pool: string
  resilvering: boolean
  percent_done: number
  bytes_done: string
  eta: string
  errors: number
  completed: boolean
  completed_at: string | null
}

interface Disk { name: string; size: string; model: string; path: string }
interface Snapshot { name: string; dataset: string; snap_name: string; used: string; refer: string; creation: string }

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

const DAY_NAMES = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

function formatScrubSchedule(s: ScrubSchedule): string {
  const hourStr = `${String(s.hour).padStart(2, '0')}:00`
  if (s.interval === 'daily') return `Daily at ${hourStr}`
  if (s.interval === 'weekly') return `Weekly on ${DAY_NAMES[s.day] ?? `Day ${s.day}`} at ${hourStr}`
  if (s.interval === 'monthly') return `Monthly on day ${s.day} at ${hourStr}`
  return s.interval
}

/** Helper to parse ZFS human sizes (e.g. 8.2TB, 500GB) into numeric bytes roughly */
function parseZfsSize(size: string): number {
  if (!size || size === '0' || size === '-' || size === '-') return 0
  const match = size.match(/^([\d.]+)([KMGT]B)$/)
  if (!match) return 0
  const val = parseFloat(match[1])
  const unit = match[2]
  const multipliers: Record<string, number> = {
    'KB': 1024,
    'MB': 1024 ** 2,
    'GB': 1024 ** 3,
    'TB': 1024 ** 4,
  }
  return val * (multipliers[unit] || 1)
}

function formatBytes(bytes: number): string {
  if (bytes === 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.floor(Math.log(bytes) / Math.log(1024))
  return `${(bytes / Math.pow(1024, i)).toFixed(1)}${units[i]}`
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
// DatasetSearchBar
// ---------------------------------------------------------------------------

interface SearchBarProps {
  query:      string
  onChange:   (q: string) => void
  matchCount: number
  totalCount: number
}

function DatasetSearchBar({ query, onChange, matchCount, totalCount }: SearchBarProps) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
      <div style={{ position: 'relative', flex: 1, maxWidth: 380 }}>
        <Icon name="search" size={16} style={{ position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-tertiary)', pointerEvents: 'none' }} />
        <input
          value={query}
          onChange={e => onChange(e.target.value)}
          placeholder="Filter datasets…"
          className="input"
          style={{ paddingLeft: 34, paddingRight: query ? 32 : 12 }}
        />
        {query && (
          <Tooltip content="Clear filter">
            <button
              onClick={() => onChange('')}
              style={{ position: 'absolute', right: 8, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', display: 'flex', padding: 2 }}
            >
              <Icon name="close" size={14} />
            </button>
          </Tooltip>
        )}
      </div>
      {query && (
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', whiteSpace: 'nowrap' }}>
          {matchCount} of {totalCount} datasets
        </span>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// DatasetNode (recursive)
// ---------------------------------------------------------------------------

function DatasetNode({ node, depth, onCreateChild, onEdit, onDelete, onAction }: {
  node: TreeNode; depth: number; onCreateChild: (name: string) => void; onEdit: (node: TreeNode) => void; onDelete: (name: string) => void; onAction: (action: string, node: TreeNode) => void
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
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexShrink: 0 }}>
          <div style={{ textAlign: 'right' }}>
            <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, fontFamily: 'var(--font-mono)' }}>{node.used || '-'}</div>
            <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>used</div>
          </div>
          <div style={{ textAlign: 'right' }}>
            <div style={{ fontSize: 'var(--text-sm)', fontFamily: 'var(--font-mono)' }}>{node.avail || '-'}</div>
            <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>avail</div>
          </div>
          <div style={{ display:'flex', gap:2 }}>
            <DatasetActionMenu node={node} onAction={onAction} />
          </div>
        </div>
      </div>
      {open && node.children.map(c => <DatasetNode key={c.name} node={c} depth={depth + 1} onCreateChild={onCreateChild} onEdit={onEdit} onDelete={onDelete} onAction={onAction} />)}
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
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} disabled={mutation.isPending} className="btn btn-primary">
          {mutation.isPending ? 'Creating…' : 'Create Dataset'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// EditDatasetModal (Set properties)
// ---------------------------------------------------------------------------

function EditDatasetModal({ node, onClose, onUpdated }: {
  node: TreeNode; onClose: () => void; onUpdated: () => void
}) {
  const [compression, setCompression] = useState('inherit')
  const [quota, setQuota] = useState(node.quota === 'none' ? '' : node.quota)

  const mutation = useMutation({
    mutationFn: async () => {
      // Set compression
      await api.post('/api/zfs/command', {
        command: 'zfs_set_property',
        args: ['set', `compression=${compression}`, node.name],
        session_id: localStorage.getItem('session_id'),
        user: localStorage.getItem('username')
      })
      // Set quota
      await api.post('/api/zfs/command', {
        command: 'zfs_set_property',
        args: ['set', `quota=${quota || 'none'}`, node.name],
        session_id: localStorage.getItem('session_id'),
        user: localStorage.getItem('username')
      })
    },
    onSuccess: () => { toast.success(`Dataset ${node.name} updated`); onUpdated(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={<>Edit: <span style={{ fontFamily: 'var(--font-mono)' }}>{node.name}</span></>} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Compression</span>
          <select value={compression} onChange={e => setCompression(e.target.value)} className="input">
            <option value="inherit">Inherit</option>
            <option value="lz4">LZ4 (recommended)</option>
            <option value="zstd">ZSTD (best ratio)</option>
            <option value="gzip">GZIP</option>
            <option value="off">Off</option>
          </select>
        </label>
        <label className="field">
          <span className="field-label">Quota (e.g. 500G or empty for none)</span>
          <input value={quota} onChange={e => setQuota(e.target.value)} placeholder="none" className="input" />
        </label>
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending} className="btn btn-primary">
          {mutation.isPending ? 'Saving…' : 'Save Changes'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// DestroyDatasetModal
// ---------------------------------------------------------------------------

function DestroyDatasetModal({ name, onClose, onDestroyed }: {
  name: string; onClose: () => void; onDestroyed: () => void
}) {
  const [confirmName, setConfirmName] = useState('')
  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/command', {
      command: 'zfs_destroy',
      args: ['destroy', '-r', name],
      session_id: localStorage.getItem('session_id'),
      user: localStorage.getItem('username')
    }),
    onSuccess: () => { toast.success(`Dataset ${name} destroyed`); onDestroyed(); onClose() },
    onError: (e: Error) => toast.error(e.message)
  })

  return (
    <Modal title={<span style={{ color: 'var(--error)' }}>Destroy Dataset</span>} onClose={onClose} size="sm">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div className="alert alert-error" style={{ fontSize: 'var(--text-xs)' }}>
          <Icon name="warning" size={16} />
          <strong>DANGER:</strong> This will permanently delete <strong>{name}</strong> and all its snapshots/children.
        </div>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          Type the dataset name to confirm: <strong>{name}</strong>
        </p>
        <input value={confirmName} onChange={e => setConfirmName(e.target.value)} placeholder={name} className="input" autoFocus />
      </div>
      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-danger" disabled={confirmName !== name || mutation.isPending} onClick={() => mutation.mutate()}>
          {mutation.isPending ? 'Destroying…' : 'Destroy Dataset'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// StorageSummary
// ---------------------------------------------------------------------------

function StorageSummary({ pools }: { pools: ZFSPool[] }) {
  const totals = useMemo(() => {
    let total = 0; let used = 0; let free = 0;
    pools.forEach(p => {
      total += parseZfsSize(p.size)
      used += parseZfsSize(p.used || p.alloc)
      free += parseZfsSize(p.free)
    })
    return { total, used, free, pct: total > 0 ? (used / total) * 100 : 0 }
  }, [pools])

  if (pools.length === 0) return null

  return (
    <div className="card" style={{ marginBottom: 32, background: 'var(--surface)', padding: '24px 32px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 24 }}>
        <div style={{ flex: 1 }}>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 8 }}>Overall Storage Efficiency</div>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
            <span style={{ fontSize: 'var(--text-4xl)', fontWeight: 700 }}>{formatBytes(totals.used)}</span>
            <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-lg)' }}>/ {formatBytes(totals.total)} used</span>
          </div>
          <div style={{ height: 10, background: 'rgba(255,255,255,0.05)', borderRadius: 999, overflow: 'hidden', margin: '16px 0 8px' }}>
            <div style={{ height: '100%', width: `${totals.pct}%`, background: capacityColor(totals.pct), borderRadius: 999, transition: 'width 0.8s' }} />
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            <span>{totals.pct.toFixed(1)}% Capacity Used</span>
            <span>{formatBytes(totals.free)} Available</span>
          </div>
        </div>
        <div style={{ width: 1, height: 80, background: 'var(--border-subtle)' }} />
        <div style={{ textAlign: 'right', minWidth: 120 }}>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontWeight: 600, textTransform: 'uppercase', marginBottom: 4 }}>Total Pools</div>
          <div style={{ fontSize: 'var(--text-2xl)', fontWeight: 700 }}>{pools.length}</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--success)', fontWeight: 600, marginTop: 4, display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 4 }}>
            <Icon name="check_circle" size={12} /> All Healthy
          </div>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ScrubScheduleModal
// ---------------------------------------------------------------------------

function ScrubScheduleModal({ pool, current, onClose, onSaved }: {
  pool: string
  current: ScrubSchedule | null
  onClose: () => void
  onSaved: () => void
}) {
  const [interval, setInterval] = useState<string>(current?.interval ?? 'weekly')
  const [hour, setHour] = useState<number>(current?.hour ?? 2)
  const [day, setDay] = useState<number>(current?.day ?? 0)

  const saveMutation = useMutation({
    mutationFn: () => {
      const payload: ScrubSchedule[] = interval === 'disabled'
        ? []
        : [{ pool, interval, hour, day }]
      return api.post('/api/zfs/scrub/schedule', payload)
    },
    onSuccess: () => {
      toast.success(interval === 'disabled'
        ? `Scrub schedule removed for ${pool}`
        : `Scrub schedule saved for ${pool}`)
      onSaved()
      onClose()
    },
    onError: (e: Error) => toast.error(`Failed to save schedule: ${e.message}`),
  })

  const hours = Array.from({ length: 24 }, (_, i) => i)

  return (
    <Modal
      title={
        <>
          Scrub Schedule -{' '}
          <span style={{ color: 'var(--primary)', fontFamily: 'var(--font-mono)' }}>{pool}</span>
        </>
      }
      onClose={onClose}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Frequency</span>
          <select value={interval} onChange={e => setInterval(e.target.value)} className="input">
            <option value="disabled">Disabled (no automatic scrubs)</option>
            <option value="daily">Daily</option>
            <option value="weekly">Weekly</option>
            <option value="monthly">Monthly</option>
          </select>
        </label>

        {interval !== 'disabled' && (
          <label className="field">
            <span className="field-label">Hour of day</span>
            <select value={hour} onChange={e => setHour(Number(e.target.value))} className="input">
              {hours.map(h => (
                <option key={h} value={h}>{String(h).padStart(2, '0')}:00</option>
              ))}
            </select>
          </label>
        )}

        {interval === 'weekly' && (
          <label className="field">
            <span className="field-label">Day of week</span>
            <select value={day} onChange={e => setDay(Number(e.target.value))} className="input">
              {DAY_NAMES.map((d, i) => (
                <option key={i} value={i}>{d}</option>
              ))}
            </select>
          </label>
        )}

        {interval === 'monthly' && (
          <label className="field">
            <span className="field-label">Day of month</span>
            <select value={day} onChange={e => setDay(Number(e.target.value))} className="input">
              {Array.from({ length: 28 }, (_, i) => i + 1).map(d => (
                <option key={d} value={d}>{d}</option>
              ))}
            </select>
          </label>
        )}

        {interval !== 'disabled' && (
          <div className="card" style={{ background: 'var(--surface)', 
            padding: '8px 12px', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)'}}>
            Schedule: <strong>{formatScrubSchedule({ pool, interval, hour, day })}</strong>
          </div>
        )}
      </div>

      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button
          onClick={() => saveMutation.mutate()}
          disabled={saveMutation.isPending}
          className="btn btn-primary"
        >
          {saveMutation.isPending ? <><Spinner size={14} /> Saving…</> : 'Save Schedule'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ResilverProgressCard
// ---------------------------------------------------------------------------

function ResilverProgressCard({ pool }: { pool: string }) {
  const resilverQ = useQuery({
    queryKey: ['zfs', 'resilver', pool],
    queryFn: ({ signal }) =>
      api.get<ResilverStatusResponse>(`/api/zfs/resilver/status?pool=${encodeURIComponent(pool)}`, signal),
    refetchInterval: (query) => {
      // Auto-refresh every 10s while active; stop polling once completed
      const d = query.state.data
      if (d?.resilvering && !d?.completed) return 10_000
      return false
    },
  })

  const data = resilverQ.data
  if (!data || !data.resilvering) return null

  const pct = Math.min(100, Math.max(0, data.percent_done ?? 0))
  const isComplete = data.completed

  return (
    <div style={{
      background: 'var(--bg-card)',
      border: `1px solid ${isComplete ? 'var(--success-border)' : 'var(--warning-border)'}`,
      borderLeft: `4px solid ${isComplete ? 'var(--success)' : 'var(--warning)'}`,
      borderRadius: 'var(--radius-xl)',
      padding: '18px 22px',
      display: 'flex',
      flexDirection: 'column',
      gap: 12}}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <Icon
          name={isComplete ? 'check_circle' : 'sync'}
          size={20}
          style={{ color: isComplete ? 'var(--success)' : 'var(--warning)', flexShrink: 0 }}
        />
        <div style={{ flex: 1 }}>
          <div style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>
            {isComplete ? 'Resilver Complete' : 'Resilver In Progress'}
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)' }}>
            Pool: {pool}
          </div>
        </div>
        {!isComplete && (
          <div style={{ fontSize: 'var(--text-sm)', fontWeight: 700, color: 'var(--warning)', flexShrink: 0 }}>
            {pct.toFixed(1)}%
          </div>
        )}
      </div>

      {/* Progress bar */}
      {!isComplete && (
        <div>
          <div style={{ height: 8, background: 'rgba(255,255,255,0.07)', borderRadius: 999, overflow: 'hidden' }}>
            <div style={{
              height: '100%',
              width: `${pct}%`,
              background: 'var(--warning)',
              borderRadius: 999,
              transition: 'width 1s'}} />
          </div>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            <span>{data.bytes_done ? `${data.bytes_done} resilvered` : ''}</span>
            <span>{data.eta ? `ETA ${data.eta}` : ''}</span>
          </div>
        </div>
      )}

      {/* Completed summary */}
      {isComplete && (
        <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          {data.bytes_done && <span>{data.bytes_done} resilvered · </span>}
          {data.errors === 0 ? (
            <span style={{ color: 'var(--success)' }}>No errors</span>
          ) : (
            <span style={{ color: 'var(--error)' }}>{data.errors} error{data.errors !== 1 ? 's' : ''}</span>
          )}
          {data.completed_at && (
            <span style={{ marginLeft: 8, color: 'var(--text-tertiary)' }}>
              Completed {data.completed_at}
            </span>
          )}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// WipeDiskModal
// ---------------------------------------------------------------------------

function WipeDiskModal({ device, onClose, onWiped }: { device: string; onClose: () => void; onWiped: () => void }) {
  const [confirm, setConfirm] = useState(false)
  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/disk/wipe', { device }),
    onSuccess: () => {
      toast.success(`Disk ${device} wiped successfully`)
      onWiped()
      onClose()
    },
    onError: (e: Error) => toast.error(`Wipe failed: ${e.message}`)
  })

  return (
    <Modal title={<span style={{ color: 'var(--error)' }}>Wipe Disk</span>} onClose={onClose} size="sm">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div className="alert alert-error">
          <Icon name="warning" size={16} />
          <strong>DANGER:</strong> This will permanently wipe all partition tables and ZFS labels from <strong>{device}</strong>.
        </div>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          This operation is irreversible. Ensure the disk is not part of any active ZFS pool.
        </p>
        <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer', userSelect: 'none', padding: '12px', background: 'rgba(255,255,255,0.03)', borderRadius: 'var(--radius-md)' }}>
          <input type="checkbox" checked={confirm} onChange={e => setConfirm(e.target.checked)} />
          <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-primary)' }}>
            I understand this will destroy all data on the disk
          </span>
        </label>
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button 
          onClick={() => mutation.mutate()} 
          disabled={!confirm || mutation.isPending} 
          className="btn btn-danger"
        >
          {mutation.isPending ? <><Spinner size={14} /> Wiping…</> : 'Wipe Disk'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ReplaceDiskModal
// ---------------------------------------------------------------------------

function ReplaceDiskModal({ pool, oldDisk, onClose, onStarted }: { pool: string; oldDisk: string; onClose: () => void; onStarted: () => void }) {
  const [newDisk, setNewDisk] = useState('')
  const [force, setForce] = useState(false)
  const [wipeFirst, setWipeFirst] = useState(false)
  
  const disksQ = useQuery({ 
    queryKey: ['zfs', 'disks'], 
    queryFn: () => api.get<any>('/api/zfs/disks') 
  })
  const unassignedDisks = (disksQ.data?.disks ?? []).filter((d: any) => d.status === 'unused')

  const mutation = useMutation({
    mutationFn: async () => {
      if (wipeFirst) {
        await api.post('/api/zfs/disk/wipe', { device: newDisk })
      }
      return api.post('/api/zfs/pool/replace', { pool, old_disk: oldDisk, new_disk: newDisk, force })
    },
    onSuccess: () => {
      toast.success(`Disk replacement started for ${oldDisk}`)
      onStarted()
      onClose()
    },
    onError: (e: Error) => toast.error(`Replacement failed: ${e.message}`)
  })

  return (
    <Modal title={`Replace Disk in ${pool}`} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div style={{ padding: '12px 14px', background: 'var(--surface)', borderRadius: 'var(--radius-md)', borderLeft: '4px solid var(--warning)' }}>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontWeight: 600, textTransform: 'uppercase', marginBottom: 4 }}>Replacing Device</div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', fontWeight: 700 }}>{oldDisk}</div>
        </div>

        <label className="field">
          <span className="field-label">Select Replacement Disk</span>
          <select value={newDisk} onChange={e => setNewDisk(e.target.value)} className="input">
            <option value="">-- Choose an unassigned disk --</option>
            {unassignedDisks.map((d: any) => (
              <option key={d.name} value={d.path}>{d.path} ({d.size})</option>
            ))}
          </select>
          {unassignedDisks.length === 0 && (
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--error)', marginTop: 4 }}>
              No unassigned disks available.
            </div>
          )}
        </label>

        {newDisk && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
              <input type="checkbox" checked={wipeFirst} onChange={e => setWipeFirst(e.target.checked)} />
              <span style={{ fontSize: 'var(--text-sm)' }}>Wipe new disk before replacement (Recommended)</span>
            </label>
            <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
              <input type="checkbox" checked={force} onChange={e => setForce(e.target.checked)} />
              <span style={{ fontSize: 'var(--text-sm)' }}>Force replacement (Use if disk sizes differ slightly)</span>
            </label>
          </div>
        )}
      </div>

      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button 
          onClick={() => mutation.mutate()} 
          disabled={!newDisk || mutation.isPending} 
          className="btn btn-primary"
        >
          {mutation.isPending ? <><Spinner size={14} /> Starting…</> : 'Start Replacement'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// WipeDiskModalInner (Selection of unassigned disks)
// ---------------------------------------------------------------------------

function WipeDiskModalInner({ onWiped, onClose }: { onWiped: () => void; onClose: () => void }) {
  const [selected, setSelected] = useState<string | null>(null)
  
  // Need to fetch system disks (not just ZFS disks, since we want any unassigned disk)
  const disksQ = useQuery({
    queryKey: ['system', 'disks'],
    queryFn: () => api.get<{ disks: Disk[] }>('/api/system/disks')
  })
  
  // Use heuristic to find unassigned disks: they aren't in any pool (backend handles safety too)
  const disks = (disksQ.data as any)?.disks ?? []

  if (selected) {
    return <WipeDiskModal device={selected} onClose={() => setSelected(null)} onWiped={onWiped} />
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      <div className="card" style={{ height: 300, overflowY: 'auto', padding: 8, background: 'var(--bg-elevated)', border: '1px solid var(--border-subtle)' }}>
        {disks.length === 0 && !disksQ.isLoading && (
          <div style={{ textAlign: 'center', color: 'var(--text-tertiary)', paddingTop: 120 }}>No disks found</div>
        )}
        {disks.map((d: Disk) => (
          <button 
            key={d.path} 
            className="btn btn-ghost" 
            style={{ width: '100%', justifyContent: 'flex-start', textAlign: 'left', marginBottom: 6, display: 'flex', flexDirection: 'column', alignItems: 'flex-start', padding: '12px 14px', borderRadius: 'var(--radius-md)' }}
            onClick={() => setSelected(d.path)}
          >
            <div style={{ fontWeight: 700, fontSize: 'var(--text-sm)', display: 'flex', alignItems: 'center', gap: 8 }}>
              <Icon name="dns" size={14} /> {d.path} ({d.size})
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>{d.model} · {d.name}</div>
          </button>
        ))}
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// PoolCard
// ---------------------------------------------------------------------------

function PoolCard({ pool, datasets, filter, onRefresh }: { pool: ZFSPool; datasets: ZFSDataset[]; filter?: string; onRefresh: () => void }) {
  const qc = useQueryClient()
  const [treeOpen, setTreeOpen] = useState(true)
  const [createParent, setCreateParent] = useState<string | null>(null)
  const [editDataset,   setEditDataset]    = useState<TreeNode | null>(null)
  const [destroyDataset, setDestroyDataset] = useState<string | null>(null)
  const [showScheduleModal, setShowScheduleModal] = useState(false)
  const [showDestroyModal, setShowDestroyModal] = useState(false)
  const [showExpandModal, setShowExpandModal] = useState(false)
  const [showFixerModal, setShowFixerModal] = useState(false)
  const [showCacheModal, setShowCacheModal] = useState(false)
  const [showTopology, setShowTopology] = useState(false)
  const [replaceDisk, setReplaceDisk] = useState<string | null>(null)
  const [wipeDisk,    setWipeDisk]    = useState<string | null>(null)
  const [sharingDataset,  setSharingDataset]  = useState<TreeNode | null>(null)
  const [snapshotDataset, setSnapshotDataset] = useState<TreeNode | null>(null)
  const [rollbackDataset, setRollbackDataset] = useState<TreeNode | null>(null)
  const [cloneDataset,   setCloneDataset]   = useState<TreeNode | null>(null)
  const pct = parseCapacityPct(pool.capacity)

  // Apply client-side filter
  const filterLower = (filter ?? '').toLowerCase()
  const effectiveFilter = filterLower.startsWith('pool:') ? filterLower.slice(5) : filterLower
  const filteredDatasets = effectiveFilter
    ? datasets.filter(d => {
        const nameLower = d.name.toLowerCase()
        const mntLower  = (d.mountpoint || '').toLowerCase()
        return nameLower.includes(effectiveFilter) || mntLower.includes(effectiveFilter)
      })
    : datasets

  const tree = buildTree(filteredDatasets, pool.name)

  // ── Scrub status ──────────────────────────────────────────────────────────
  const scrubQ = useQuery({
    queryKey: ['zfs', 'scrub', 'status', pool.name],
    queryFn: ({ signal }) =>
      api.get<ScrubStatusResponse>(`/api/zfs/scrub/status?pool=${encodeURIComponent(pool.name)}`, signal),
    refetchInterval: 10_000,
  })

  // ── Scrub schedule ────────────────────────────────────────────────────────
  const scheduleQ = useQuery({
    queryKey: ['zfs', 'scrub', 'schedule', pool.name],
    queryFn: ({ signal }) =>
      api.get<ScrubSchedulesResponse>(`/api/zfs/scrub/schedule?pool=${encodeURIComponent(pool.name)}`, signal),
  })
  const currentSchedule = scheduleQ.data?.schedules?.[0] ?? null

  // ── Scrub mutations ───────────────────────────────────────────────────────
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

  const scrubStatus = scrubQ.data
  const isScrubbing = scrubStatus?.in_progress ?? /in progress|scrubbing/i.test(scrubStatus?.scrub ?? '')
  const scrubPct = scrubStatus?.percent_done ?? 0
  const scrubEta = scrubStatus?.eta ?? ''
  const scrubDone = scrubStatus?.bytes_done ?? ''
  const scrubText = scrubStatus?.scrub || ''

  // ── Resilver status ───────────────────────────────────────────────────────
  const resilverQ = useQuery({
    queryKey: ['zfs', 'resilver', pool.name],
    queryFn: ({ signal }) =>
      api.get<ResilverStatusResponse>(`/api/zfs/resilver/status?pool=${encodeURIComponent(pool.name)}`, signal),
    refetchInterval: (query) => {
      const d = query.state.data
      if (d?.resilvering && !d?.completed) return 10_000
      return false
    },
  })
  const isResilvering = resilverQ.data?.resilvering && !resilverQ.data?.completed

  // ── Topology status ────────────────────────────────────────────────────────
  const topologyQ = useQuery({
    queryKey: ['zfs', 'topology', pool.name],
    queryFn: ({ signal }) =>
      api.get<PoolTopology>(`/api/zfs/pool/topology?pool=${encodeURIComponent(pool.name)}`, signal),
    enabled: showTopology,
    refetchInterval: isResilvering ? 10_000 : false,
  })

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 28, borderLeft: `4px solid ${healthColor(pool.health)}` }}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 16, marginBottom: 16 }}>
        <Icon name="storage" size={32} style={{ color: 'var(--primary)', flexShrink: 0 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontSize: 'var(--text-xl)', fontWeight: 700, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{pool.name}</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: 2, fontFamily: 'var(--font-mono)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
            {pool.type || 'unknown'} · {pool.used || 'N/A'} / {pool.size || 'N/A'} ({pool.capacity || '0%'})
          </div>
        </div>
        {/* Health badge */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '5px 12px', borderRadius: 'var(--radius-full)', background: `${healthColor(pool.health)}18`, border: `1px solid ${healthColor(pool.health)}40`, color: healthColor(pool.health), fontSize: 'var(--text-sm)', fontWeight: 600, whiteSpace: 'nowrap', flexShrink: 0 }}>
          <Icon name={healthIcon(pool.health)} size={15} />
          {pool.health}
        </div>
        {/* Scrub btn */}
        <button
          onClick={() => isScrubbing ? scrubStop.mutate() : scrubStart.mutate()}
          disabled={scrubStart.isPending || scrubStop.isPending}
          style={{ background: isScrubbing ? 'var(--warning-bg)' : 'var(--surface)', border: `1px solid ${isScrubbing ? 'var(--warning-border)' : 'var(--border)'}`, borderRadius: 'var(--radius-sm)', padding: '7px 12px', cursor: 'pointer', color: isScrubbing ? 'var(--warning)' : 'var(--text-secondary)', display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-sm)', whiteSpace: 'nowrap', flexShrink: 0 }}
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
        {scrubText && !isScrubbing && (
          <span style={{ padding: '4px 10px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)' }}>
            {scrubText.split('\n')[0].trim().slice(0, 60)}
          </span>
        )}
      </div>

      {/* ── Scrub progress (shown when in progress) ───────────────────────── */}
      {isScrubbing && (
        <div style={{
          background: 'var(--warning-bg)',
          border: '1px solid var(--warning-border)',
          borderRadius: 'var(--radius-md)',
          padding: '12px 14px',
          marginBottom: 16}}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-sm)', color: 'var(--warning)', fontWeight: 600 }}>
              <Spinner size={14} />
              Scrubbing…
            </div>
            <div style={{ fontSize: 'var(--text-sm)', fontWeight: 700, color: 'var(--warning)' }}>
              {scrubPct > 0 ? `${scrubPct.toFixed(1)}%` : ''}
            </div>
          </div>
          {scrubPct > 0 && (
            <>
              <div className="progress" style={{ height: 6, background: 'rgba(255,255,255,0.1)', borderRadius: 999, overflow: 'hidden' }}>
                <div
                  className="progress-fill"
                  style={{ height: '100%', width: `${scrubPct}%`, background: 'var(--warning)', borderRadius: 999, transition: 'width 0.5s' }}
                />
              </div>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                <span>{scrubDone ? `${scrubDone} scanned` : ''}</span>
                <span>{scrubEta ? `ETA ${scrubEta}` : ''}</span>
              </div>
            </>
          )}
          {scrubPct === 0 && scrubText && (
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)' }}>
              {scrubText.split('\n')[0].trim().slice(0, 80)}
            </div>
          )}
        </div>
      )}

      {/* ── Scrub schedule section ────────────────────────────────────────── */}
      <div className="card" style={{ background: 'var(--surface)', 
        display: 'flex',
        alignItems: 'center',
        gap: 10,
        marginBottom: 16,
        padding: '10px 14px',
        borderRadius: 'var(--radius-md)'}}>
        <Icon name="event_repeat" size={16} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
        <div style={{ flex: 1 }}>
          {currentSchedule ? (
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
              Scrubs <strong>{formatScrubSchedule(currentSchedule)}</strong>
            </span>
          ) : (
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              No automatic scrub scheduled
            </span>
          )}
        </div>
        <button
          className="btn btn-ghost"
          onClick={() => setShowScheduleModal(true)}
          style={{ fontSize: 'var(--text-xs)', padding: '4px 10px', flexShrink: 0 }}
        >
          <Icon name="edit_calendar" size={13} style={{ marginRight: 4 }} />
          Configure Schedule
        </button>

        <div style={{ width: 1, height: 16, background: 'var(--border-subtle)', margin: '0 4px' }} />

        <div style={{ display: 'flex', gap: 10, marginTop: 10 }}>
          <button onClick={() => setShowTopology(!showTopology)} className={`btn btn-sm ${showTopology ? 'btn-primary' : 'btn-ghost'}`}>
            <Icon name="account_tree" size={14} /> {showTopology ? 'Hide Topology' : 'View Topology'}
          </button>
          {pool.health !== 'ONLINE' && (
            <button onClick={() => setShowFixerModal(true)} className="btn btn-sm btn-warning">
              <Icon name="build" size={14} /> Fixer Wizard
            </button>
          )}
          <button onClick={() => setShowCacheModal(true)} className="btn btn-sm btn-ghost">
            <Icon name="speed" size={14} /> Manage Cache
          </button>
          <button onClick={() => setShowExpandModal(true)} className="btn btn-sm btn-ghost">
            <Icon name="add" size={14} /> Expand Pool
          </button>
          <button onClick={() => setShowDestroyModal(true)} className="btn btn-sm btn-ghost btn-danger-hover">
            <Icon name="delete" size={14} /> Destroy Pool
          </button>
        </div>
      </div>

      {/* Topology View */}
      {showTopology && (
        <div style={{ marginBottom: 24, padding: '16px 20px', background: 'rgba(0,0,0,0.2)', borderRadius: 'var(--radius-lg)', border: '1px solid var(--border-subtle)' }}>
           {topologyQ.isLoading && <div style={{ textAlign: 'center', padding: 20 }}><Spinner size={20} /> Loading topology…</div>}
           {topologyQ.data && (
             <PoolTopologyView 
                topology={topologyQ.data} 
                onAction={(action, vdev) => {
                   if (action === 'replace') setReplaceDisk(vdev.name)
                }}
             />
           )}
           <div style={{ marginTop: 16, borderTop: '1px solid var(--border-subtle)', paddingTop: 12, display: 'flex', gap: 12 }}>
              <button className="btn btn-xs btn-ghost" onClick={() => setWipeDisk('true')}>
                 <Icon name="cleaning_services" size={12} /> Wipe a disk
              </button>
           </div>
        </div>
      )}

      {/* Resilver progress card */}
      {isResilvering && <ResilverProgressCard pool={pool.name} />}

      {/* Dataset tree */}
      {tree.length > 0 && (
        <div style={{ borderTop: '1px solid var(--border)', paddingTop: 12 }}>
          <button onClick={() => setTreeOpen(o => !o)} style={{ width: '100%', background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 8, padding: '4px 0 8px', color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
            <Icon name={treeOpen ? 'expand_more' : 'chevron_right'} size={16} />
            <Icon name="account_tree" size={14} />
            Datasets ({effectiveFilter ? `${filteredDatasets.length} of ` : ''}{datasets.length})
          </button>
          {treeOpen && tree.map(n => (
            <DatasetNode 
              key={n.name} 
              node={n} 
              depth={0} 
              onCreateChild={setCreateParent} 
              onEdit={setEditDataset} 
              onDelete={setDestroyDataset}
              onAction={(action, node) => {
                if (action === 'manage_shares') setSharingDataset(node)
                if (action === 'snapshot') setSnapshotDataset(node)
                if (action === 'rollback') setRollbackDataset(node)
                if (action === 'clone') setCloneDataset(node)
                if (action === 'create_child') setCreateParent(node.name)
                if (action === 'edit') setEditDataset(node)
                if (action === 'delete') setDestroyDataset(node.name)
              }}
            />
          ))}
          {treeOpen && effectiveFilter && filteredDatasets.length === 0 && (
            <div style={{ padding: '16px 12px', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)', textAlign: 'center' }}>
              No datasets match "{filter}"
            </div>
          )}
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

      {editDataset && (
        <EditDatasetModal
          node={editDataset}
          onClose={() => setEditDataset(null)}
          onUpdated={() => { qc.invalidateQueries({ queryKey: ['zfs', 'datasets'] }); onRefresh() }}
        />
      )}

      {destroyDataset && (
        <DestroyDatasetModal
          name={destroyDataset}
          onClose={() => setDestroyDataset(null)}
          onDestroyed={() => { qc.invalidateQueries({ queryKey: ['zfs', 'datasets'] }); onRefresh() }}
        />
      )}

      {showScheduleModal && (
        <ScrubScheduleModal
          pool={pool.name}
          current={currentSchedule}
          onClose={() => setShowScheduleModal(false)}
          onSaved={() => qc.invalidateQueries({ queryKey: ['zfs', 'scrub', 'schedule', pool.name] })}
        />
      )}

      {showDestroyModal && (
        <DestroyPoolModal 
          poolName={pool.name} 
          onClose={() => setShowDestroyModal(false)} 
          onDestroyed={onRefresh} 
        />
      )}

      {showExpandModal && (
        <CreatePoolModal 
          onClose={() => setShowExpandModal(false)} 
          onCreated={onRefresh} 
        />
      )}

      {showFixerModal && (
        <PoolFixerWizard 
          pool={pool} 
          onClose={() => setShowFixerModal(false)} 
          onRefresh={onRefresh} 
        />
      )}

      {showCacheModal && (
        <CacheManageModal 
          pool={pool} 
          onClose={() => setShowCacheModal(false)} 
          onRefresh={onRefresh} 
        />
      )}

      {replaceDisk && (
        <ReplaceDiskModal 
          pool={pool.name} 
          oldDisk={replaceDisk} 
          onClose={() => setReplaceDisk(null)} 
          onStarted={() => { onRefresh(); qc.invalidateQueries({ queryKey: ['zfs', 'topology', pool.name] }) }} 
        />
      )}

      {sharingDataset && (
        <DatasetSharingModal
          node={sharingDataset}
          onClose={() => setSharingDataset(null)}
        />
      )}

      {snapshotDataset && (
        <SnapshotModal
          node={snapshotDataset}
          onClose={() => setSnapshotDataset(null)}
          onCreated={() => { qc.invalidateQueries({ queryKey: ['zfs', 'datasets'] }); onRefresh() }}
        />
      )}

      {rollbackDataset && (
        <RollbackModal
          node={rollbackDataset}
          onClose={() => setRollbackDataset(null)}
          onRollback={() => { qc.invalidateQueries({ queryKey: ['zfs', 'datasets'] }); onRefresh() }}
        />
      )}

      {cloneDataset && (
        <CloneSnapshotModal
          node={cloneDataset}
          onClose={() => setCloneDataset(null)}
          onCloned={() => { qc.invalidateQueries({ queryKey: ['zfs', 'datasets'] }); onRefresh() }}
        />
      )}

      {wipeDisk && (
        <Modal title="Wipe System Disk" onClose={() => setWipeDisk(null)} size="sm">
          <WipeDiskModalInner onWiped={onRefresh} onClose={() => setWipeDisk(null)} />
        </Modal>
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
          <div key={d.name} className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '16px 20px', display: 'flex', alignItems: 'center', gap: 16 }}>
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

      {unlockTarget && (
        <Modal title="Unlock Dataset" onClose={() => setUnlockTarget(null)}>
          <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 20, fontFamily: 'var(--font-mono)' }}>{unlockTarget}</p>
          <input type="password" value={passphrase} onChange={e => setPassphrase(e.target.value)}
            placeholder="Passphrase" className="input" autoFocus
            onKeyDown={e => e.key === 'Enter' && passphrase && unlock.mutate({ name: unlockTarget, passphrase })}
          />
          <div className="modal-footer">
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
// Lifecycle Modals
// ---------------------------------------------------------------------------

function DestroyPoolModal({ poolName, onClose, onDestroyed }: { poolName: string; onClose: () => void; onDestroyed: () => void }) {
  const [confirmName, setConfirmName] = useState('')
  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/pools/destroy', { name: poolName }),
    onSuccess: () => { toast.success(`Pool ${poolName} destroyed`); onDestroyed(); onClose() },
    onError: (e: Error) => toast.error(e.message)
  })

  return (
    <Modal title={<span style={{ color: 'var(--error)' }}>Destroy Pool</span>} onClose={onClose} size="sm">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div className="alert alert-error" style={{ fontSize: 'var(--text-xs)' }}>
          <Icon name="warning" size={16} />
          <strong>DANGER:</strong> This will permanently delete all data in pool <strong>{poolName}</strong>. This cannot be undone.
        </div>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          To confirm, please type the name of the pool: <strong>{poolName}</strong>
        </p>
        <input 
          value={confirmName} 
          onChange={e => setConfirmName(e.target.value)} 
          placeholder={poolName}
          className="input"
          autoFocus
        />
      </div>
      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button 
          className="btn btn-danger" 
          disabled={confirmName !== poolName || mutation.isPending}
          onClick={() => mutation.mutate()}
        >
          {mutation.isPending ? 'Destroying…' : 'Destroy Pool'}
        </button>
      </div>
    </Modal>
  )
}



function CreatePoolModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [layout, setLayout] = useState('stripe')
  const [selectedDisks, setSelectedDisks] = useState<string[]>([])

  const disksQ = useQuery({
    queryKey: ['system', 'disks'],
    queryFn: () => api.get<{ disks: Disk[] }>('/api/system/disks')
  })

  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/pools/create', { name, layout, disks: selectedDisks }),
    onSuccess: () => { toast.success(`Pool ${name} created`); onCreated(); onClose() },
    onError: (e: Error) => toast.error(e.message)
  })

  const disks = (disksQ.data as any)?.disks ?? []

  return (
    <Modal title="Create New ZFS Pool" onClose={onClose} size="lg">
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 24 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <label className="field">
            <span className="field-label">Pool Name</span>
            <input value={name} onChange={e => setName(e.target.value)} className="input" placeholder="e.g. tank" />
          </label>
          <label className="field">
            <span className="field-label">Pool Layout</span>
            <select value={layout} onChange={e => setLayout(e.target.value)} className="input">
              <option value="stripe">Stripe (No redundancy)</option>
              <option value="mirror">Mirror (RAID 1)</option>
              <option value="raidz1">RAID-Z1 (1-disk parity)</option>
              <option value="raidz2">RAID-Z2 (2-disk parity)</option>
              <option value="raidz3">RAID-Z3 (3-disk parity)</option>
            </select>
          </label>
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <span className="field-label">Select Disks ({selectedDisks.length})</span>
          <div className="card" style={{ height: 260, overflowY: 'auto', padding: 12, background: 'var(--bg-elevated)' }}>
            {disks.length === 0 && !disksQ.isLoading && <div style={{ textAlign: 'center', color: 'var(--text-tertiary)', paddingTop: 80, fontSize: 'var(--text-sm)' }}>No unassigned disks found</div>}
            {disks.map((d: Disk) => (
              <label key={d.path} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: 8, borderRadius: 'var(--radius-sm)', cursor: 'pointer', border: '1px solid var(--border-subtle)', marginBottom: 8 }}>
                <input 
                  type="checkbox" 
                  checked={selectedDisks.includes(d.path)}
                  onChange={e => e.target.checked ? setSelectedDisks([...selectedDisks, d.path]) : setSelectedDisks(selectedDisks.filter(p => p !== d.path))}
                />
                <div style={{ flex: 1 }}>
                  <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>{d.name} ({d.size})</div>
                  <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>{d.model} · {d.path}</div>
                </div>
              </label>
            ))}
          </div>
        </div>
      </div>
      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" disabled={!name || selectedDisks.length === 0 || mutation.isPending} onClick={() => mutation.mutate()}>
          {mutation.isPending ? 'Creating…' : 'Create Pool'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// PoolsPage
// ---------------------------------------------------------------------------

type Tab = 'pools' | 'encryption'

export function PoolsPage() {
  const [tab, setTab] = useState<Tab>('pools')
  const [datasetFilter, setDatasetFilter] = useState('')
  const [createPoolOpen, setCreatePoolOpen] = useState(false)
  const qc   = useQueryClient()
  const wsOn = useWsStore((s) => s.on)
  const [mountAlert, setMountAlert] = useState<{ pool: string; mountpoint: string } | null>(null)

  const poolsQ = useQuery({
    queryKey: ['zfs', 'pools'],
    queryFn: ({ signal }) => api.get<PoolsResponse>('/api/zfs/pools', signal),
    refetchInterval: 60_000, // fallback poll - WS events trigger immediate refetch
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

  // WS: mount error → show inline banner + refetch pools
  useEffect(() => {
    return wsOn('mountError', (data) => {
      const d = data as { pool?: string; mountpoint?: string; error?: string }
      if (!d?.pool) return
      if (d.error && d.error !== 'clear') {
        setMountAlert({ pool: d.pool, mountpoint: d.mountpoint ?? '' })
        toast.error(`Mount error on pool ${d.pool}: ${d.error}`)
        qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })
      } else {
        setMountAlert(prev => prev?.pool === d.pool ? null : prev)
      }
    })
  }, [wsOn, qc])

  // WS: resilver events → refetch pools + resilver status + show toast on complete
  useEffect(() => {
    const unsub1 = wsOn('resilverStarted', () => {
      qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })
      qc.invalidateQueries({ queryKey: ['zfs', 'resilver'] })
      toast.info('Resilver started')
    })
    const unsub2 = wsOn('resilverProgress', () => {
      qc.invalidateQueries({ queryKey: ['zfs', 'resilver'] })
    })
    const unsub3 = wsOn('resilverCompleted', () => {
      qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })
      qc.invalidateQueries({ queryKey: ['zfs', 'resilver'] })
      toast.success('Resilver completed')
    })
    return () => { unsub1(); unsub2(); unsub3() }
  }, [wsOn, qc])

  const pools    = poolsQ.data?.pools ?? poolsQ.data?.data ?? []
  const datasets = datasetsQ.data?.data ?? []

  // Compute global match count for the search bar summary
  const { totalDatasets, filteredDatasetCount } = useMemo(() => {
    const total = datasets.length
    if (!datasetFilter) return { totalDatasets: total, filteredDatasetCount: total }
    const effectiveFilter = datasetFilter.startsWith('pool:')
      ? datasetFilter.slice(5).toLowerCase()
      : datasetFilter.toLowerCase()
    const count = datasets.filter(d => {
      const n = d.name.toLowerCase()
      const m = (d.mountpoint || '').toLowerCase()
      return n.includes(effectiveFilter) || m.includes(effectiveFilter)
    }).length
    return { totalDatasets: total, filteredDatasetCount: count }
  }, [datasets, datasetFilter])

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
        <div style={{ display: 'flex', gap: 10 }}>
          <button onClick={() => setCreatePoolOpen(true)} className="btn btn-primary">
            <Icon name="add_circle" size={18} /> Add Pool
          </button>
          <Tooltip content="Refresh">
            <button onClick={refresh} className="btn btn-ghost">
              <Icon name="refresh" size={16} />
            </button>
          </Tooltip>
        </div>
      </div>

      <StorageSummary pools={pools} />

      {/* Mount error alert - shown when background monitor detects an unwritable mountpoint */}
      {mountAlert && (
        <div className="alert alert-error" style={{ marginBottom: 16 }}>
          <Icon name="folder_off" size={16} />
          <span>
            Pool <strong>{mountAlert.pool}</strong> mountpoint <strong>{mountAlert.mountpoint}</strong> is not writable. The pool may be full, read-only, or the filesystem may have errors.
          </span>
          <button onClick={() => setMountAlert(null)} style={{ marginLeft:'auto', background:'none', border:'none', cursor:'pointer', color:'var(--error)', display:'flex' }}>
            <Icon name="close" size={15} />
          </button>
        </div>
      )}

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
          {/* Dataset search / filter bar */}
          {!datasetsQ.isLoading && datasets.length > 0 && (
            <DatasetSearchBar
              query={datasetFilter}
              onChange={setDatasetFilter}
              matchCount={filteredDatasetCount}
              totalCount={totalDatasets}
            />
          )}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
            {pools.map(pool => (
              <PoolCard key={pool.name} pool={pool}
                datasets={datasets.filter(d => d.name === pool.name || d.name.startsWith(pool.name + '/'))}
                filter={datasetFilter}
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

      {createPoolOpen && (
        <CreatePoolModal onClose={() => setCreatePoolOpen(false)} onCreated={refresh} />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// PoolFixerWizard
// ---------------------------------------------------------------------------

function PoolFixerWizard({ pool, onClose, onRefresh }: { pool: any; onClose: () => void; onRefresh: () => void }) {
  const [step, setStep] = useState(1)
  
  const clearMutation = useMutation({
    mutationFn: () => api.post('/api/zfs/pool/operations', { pool: pool.name, op: 'clear' }),
    onSuccess: () => { toast.success('Errors cleared'); onRefresh() },
    onError: (e: Error) => toast.error(e.message)
  })

  const onlineMutation = useMutation({
    mutationFn: (device: string) => api.post('/api/zfs/pool/operations', { pool: pool.name, op: 'online', device }),
    onSuccess: () => { toast.success('Disk onlined'); onRefresh() },
    onError: (e: Error) => toast.error(e.message)
  })

  // Identify unhealthy disks - prefer topology for complex VDEVs, fallback to pool.disks
  const unhealthyDisks = useMemo(() => {
    // If we have topology data, traverse it for leaf disks
    if (pool.topology) {
      const disks: any[] = []
      const traverse = (v: any) => {
        if (v.type === 'disk' && v.state !== 'ONLINE') {
          disks.push(v)
        }
        if (v.children) v.children.forEach(traverse)
      }
      traverse(pool.topology)
      if (disks.length > 0) return disks
    }
    // Fallback to legacy flat disks list
    return (pool.disks || []).filter((d: any) => d.state !== 'ONLINE')
  }, [pool.topology, pool.disks])

  return (
    <Modal title={`Pool Fixer: ${pool.name}`} onClose={onClose} size="md">
      <div style={{ padding: '0 8px' }}>
        {step === 1 && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            <p style={{ fontSize: 'var(--text-sm)' }}>
              This wizard helps resolve common ZFS pool issues. Your pool is currently <strong>{pool.health}</strong>.
            </p>
            <div className="card" style={{ padding: 16, background: 'rgba(255,255,255,0.03)' }}>
              <h4 style={{ fontSize: 'var(--text-xs)', fontWeight: 800, marginBottom: 8, textTransform: 'uppercase' }}>Recommended First Step</h4>
              <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 12 }}>
                Oftentimes, transient errors can be resolved by clearing the pool error count.
              </p>
              <button 
                className="btn btn-sm btn-primary" 
                onClick={() => clearMutation.mutate()}
                disabled={clearMutation.isPending}
              >
                Clear Pool Errors
              </button>
            </div>
            {unhealthyDisks.length > 0 && (
              <button className="btn btn-ghost btn-sm" onClick={() => setStep(2)} style={{ alignSelf: 'center' }}>
                Next: Manage Disks →
              </button>
            )}
          </div>
        )}

        {step === 2 && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            <h4 style={{ fontSize: 'var(--text-xs)', fontWeight: 800, textTransform: 'uppercase' }}>Unhealthy Disks</h4>
            {unhealthyDisks.map((d: any) => (
              <div key={d.device} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', padding: '10px 14px', background: 'rgba(255,0,0,0.05)', borderRadius: 'var(--radius-md)', border: '1px solid rgba(255,0,0,0.1)' }}>
                <div>
                  <div style={{ fontSize: 'var(--text-sm)', fontWeight: 700 }}>{d.device}</div>
                  <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--error)' }}>State: {d.state}</div>
                </div>
                <button 
                  className="btn btn-xs btn-primary"
                  onClick={() => onlineMutation.mutate(d.device)}
                  disabled={onlineMutation.isPending}
                >
                  Try Bring Online
                </button>
              </div>
            ))}
            <button className="btn btn-ghost btn-sm" onClick={() => setStep(1)} style={{ alignSelf: 'center' }}>
              ← Return
            </button>
          </div>
        )}
      </div>
      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={onClose}>Finish</button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// CacheManageModal
// ---------------------------------------------------------------------------

function CacheManageModal({ pool, onClose, onRefresh }: { pool: any; onClose: () => void; onRefresh: () => void }) {
  const [type, setType] = useState<'cache' | 'log' | 'special'>('cache')
  const [selectedDisks, setSelectedDisks] = useState<string[]>([])

  const disksQ = useQuery({
    queryKey: ['system', 'disks'],
    queryFn: () => api.get<{ disks: Disk[] }>('/api/system/disks')
  })

  const addMutation = useMutation({
    mutationFn: () => api.post('/api/zfs/pool/add-vdev', { pool: pool.name, vdev_type: type, disks: selectedDisks }),
    onSuccess: () => { toast.success(`${type} added`); onRefresh(); onClose() },
    onError: (e: Error) => toast.error(e.message)
  })

  const disks = disksQ.data?.disks ?? []

  return (
    <Modal title={`Manage Cache: ${pool.name}`} onClose={onClose} size="lg">
      <div style={{ display: 'grid', gridTemplateColumns: '240px 1fr', gap: 24 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <h4 style={{ fontSize: 'var(--text-xs)', fontWeight: 800, textTransform: 'uppercase' }}>VDEV Type</h4>
          {(['cache', 'log', 'special'] as const).map(t => (
            <div 
              key={t}
              onClick={() => setType(t)}
              style={{
                padding: '12px',
                borderRadius: 'var(--radius-md)',
                border: '1px solid',
                borderColor: type === t ? 'var(--primary)' : 'var(--border-subtle)',
                background: type === t ? 'var(--primary-bg)' : 'transparent',
                cursor: 'pointer',
                transition: 'all 0.15s'
              }}
            >
              <div style={{ fontSize: 'var(--text-sm)', fontWeight: 700, textTransform: 'capitalize' }}>{t}</div>
              <div style={{ fontSize: 'var(--text-3xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>
                {t === 'cache' && 'L2ARC: Enhances read performance.'}
                {t === 'log' && 'SLOG: Accelerates synchronous writes.'}
                {t === 'special' && 'Metadata: Offloads small files/metadata.'}
              </div>
            </div>
          ))}
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <h4 style={{ fontSize: 'var(--text-xs)', fontWeight: 800, textTransform: 'uppercase' }}>Select Disks</h4>
          <div className="card" style={{ height: 260, overflowY: 'auto', padding: 12, background: 'var(--bg-elevated)' }}>
            {disks.length === 0 && !disksQ.isLoading && <div style={{ textAlign: 'center', color: 'var(--text-tertiary)', paddingTop: 80, fontSize: 'var(--text-sm)' }}>No unassigned disks found</div>}
            {disks.map((d: Disk) => (
              <label key={d.path} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: 8, borderRadius: 'var(--radius-sm)', cursor: 'pointer', border: '1px solid var(--border-subtle)', marginBottom: 8 }}>
                <input 
                  type="checkbox" 
                  checked={selectedDisks.includes(d.path)}
                  onChange={e => e.target.checked ? setSelectedDisks([...selectedDisks, d.path]) : setSelectedDisks(selectedDisks.filter(p => p !== d.path))}
                />
                <div style={{ flex: 1 }}>
                  <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>{d.name} ({d.size})</div>
                  <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>{d.model}</div>
                </div>
              </label>
            ))}
          </div>
        </div>
      </div>
      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button 
          className="btn btn-primary" 
          disabled={selectedDisks.length === 0 || addMutation.isPending}
          onClick={() => addMutation.mutate()}
        >
          Add {type}
        </button>
      </div>
	</Modal>
	)
}

// ---------------------------------------------------------------------------
// DatasetActionMenu (v7.3.0)
// ---------------------------------------------------------------------------

function DatasetActionMenu({ node, onAction }: {
	node: TreeNode
	onAction: (action: string, node: TreeNode) => void
}) {
	const [open, setOpen] = useState(false)
	const menuRef = useRef<HTMLDivElement>(null)

	useEffect(() => {
		if (!open) return
		const handleClick = (e: MouseEvent) => {
			if (menuRef.current && !menuRef.current.contains(e.target as Node)) setOpen(false)
		}
		document.addEventListener('mousedown', handleClick)
		return () => document.removeEventListener('mousedown', handleClick)
	}, [open])

	return (
		<div style={{ position: 'relative' }} ref={menuRef}>
			<button 
				onClick={(e) => { e.stopPropagation(); setOpen(!open) }}
				className={`btn btn-xs ${open ? 'btn-primary' : 'btn-ghost'}`}
				style={{ width: 28, height: 28, padding: 0 }}
			>
				<Icon name="more_vert" size={16} />
			</button>
			
			{open && (
				<div style={{
					position: 'absolute', right: 0, top: '100%', marginTop: 4,
					zIndex: 100, width: 180, background: 'var(--bg-elevated)',
					backdropFilter: 'var(--blur-glass)', border: '1px solid var(--border-highlight)',
					borderRadius: 'var(--radius-md)', boxShadow: 'var(--shadow-lg)',
					overflow: 'hidden', animation: 'fadeIn 0.15s ease'
				}}>
					<MenuBtn icon="add" label="Create Child" onClick={() => { setOpen(false); onAction('create_child', node) }} />
					<MenuBtn icon="camera" label="Snapshot" onClick={() => { setOpen(false); onAction('snapshot', node) }} />
					<MenuBtn icon="history" label="Rollback" onClick={() => { setOpen(false); onAction('rollback', node) }} />
					<MenuBtn icon="fork_right" label="Clone Snapshot" onClick={() => { setOpen(false); onAction('clone', node) }} />
					<div style={{ height: 1, background: 'var(--border-subtle)', margin: '4px 0' }} />
					<MenuBtn icon="folder_shared" label="Manage Shares" onClick={() => { setOpen(false); onAction('manage_shares', node) }} />
					<div style={{ height: 1, background: 'var(--border-subtle)', margin: '4px 0' }} />
					<MenuBtn icon="settings" label="Properties" onClick={() => { setOpen(false); onAction('edit', node) }} />
					<MenuBtn icon="delete" label="Destroy" danger onClick={() => { setOpen(false); onAction('delete', node) }} />
				</div>
			)}
		</div>
	)
}

function MenuBtn({ icon, label, onClick, danger }: { icon: string; label: string; onClick: () => void; danger?: boolean }) {
	return (
		<button
			onClick={(e) => { e.stopPropagation(); onClick() }}
			style={{
				width: '100%', display: 'flex', alignItems: 'center', gap: 10, padding: '10px 14px',
				background: 'none', border: 'none', cursor: 'pointer', color: danger ? 'var(--error)' : 'var(--text)',
				fontSize: 'var(--text-xs)', fontWeight: 500, transition: 'background 0.1s'
			}}
			onMouseEnter={e => (e.currentTarget.style.background = 'hsla(0,0%,100%,0.05)')}
			onMouseLeave={e => (e.currentTarget.style.background = 'none')}
		>
			<Icon name={icon} size={14} style={{ opacity: 0.7 }} />
			{label}
		</button>
	)
}

// ---------------------------------------------------------------------------
// DatasetSharingModal (v7.3.0)
// ---------------------------------------------------------------------------

function DatasetSharingModal({ node, onClose }: { node: TreeNode; onClose: () => void }) {
	const { data, isLoading } = useQuery({
		queryKey: ['shares', 'by-path', node.name],
		queryFn: () => api.get<any>(`/api/shares/by-path?path=/${node.name}`)
	})

	return (
		<Modal title={<>Sharing: <span style={{ color: 'var(--primary)', fontFamily: 'var(--font-mono)' }}>/{node.name}</span></>} onClose={onClose} size="lg">
			<div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
				
				{/* SMB Section */}
				<section>
					<div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
						<Icon name="folder_shared" style={{ color: 'var(--primary)' }} />
						<h3 style={{ fontSize: 'var(--text-md)', fontWeight: 700 }}>SMB (Windows Shares)</h3>
					</div>
					<div className="card" style={{ padding: 0, overflow: 'hidden' }}>
						{isLoading ? <div style={{ padding: 20 }}><Skeleton /></div> : (
							<table className="data-table">
								<thead>
									<tr><th>Share Name</th><th>Status</th><th>Comment</th></tr>
								</thead>
								<tbody>
									{data?.smb?.map((s: any) => (
										<tr key={s.name}>
											<td style={{ fontWeight: 600 }}>{s.name}</td>
											<td>{s.enabled ? <span className="badge badge-success">Enabled</span> : <span className="badge badge-neutral">Disabled</span>}</td>
											<td style={{ color: 'var(--text-tertiary)', fontSize: '13px' }}>{s.comment || '-'}</td>
										</tr>
									))}
									{(!data?.smb || data.smb.length === 0) && (
										<tr><td colSpan={3} style={{ textAlign: 'center', padding: '24px', color: 'var(--text-tertiary)' }}>No SMB shares for this path.</td></tr>
									)}
								</tbody>
							</table>
						)}
					</div>
				</section>

				{/* NFS Section */}
				<section>
					<div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
						<Icon name="cloud" style={{ color: 'var(--info)' }} />
						<h3 style={{ fontSize: 'var(--text-md)', fontWeight: 700 }}>NFS (UNIX Exports)</h3>
					</div>
					<div className="card" style={{ padding: 0, overflow: 'hidden' }}>
						{isLoading ? <div style={{ padding: 20 }}><Skeleton /></div> : (
							<table className="data-table">
								<thead>
									<tr><th>Clients</th><th>Status</th><th>Options</th></tr>
								</thead>
								<tbody>
									{data?.nfs?.map((n: any) => (
										<tr key={n.id}>
											<td style={{ fontWeight: 600 }}>{n.clients}</td>
											<td>{n.enabled ? <span className="badge badge-success">Enabled</span> : <span className="badge badge-neutral">Disabled</span>}</td>
											<td style={{ color: 'var(--text-tertiary)', fontSize: '13px' }}>{n.options}</td>
										</tr>
									))}
									{(!data?.nfs || data.nfs.length === 0) && (
										<tr><td colSpan={3} style={{ textAlign: 'center', padding: '24px', color: 'var(--text-tertiary)' }}>No NFS exports for this path.</td></tr>
									)}
								</tbody>
							</table>
						)}
					</div>
				</section>
			</div>
			<div className="modal-footer">
				<button onClick={onClose} className="btn btn-ghost">Close</button>
				<button className="btn btn-primary" onClick={() => window.location.hash = '#/shares'}>Go to Sharing Page</button>
			</div>
		</Modal>
	)
}

// ---------------------------------------------------------------------------
// SnapshotModal (v7.3.0)
// ---------------------------------------------------------------------------

function SnapshotModal({ node, onClose, onCreated }: { node: TreeNode; onClose: () => void; onCreated: () => void }) {
	const [name, setName] = useState('')
	const mutation = useMutation({
		mutationFn: () => api.post('/api/zfs/snapshots', { dataset: node.name, name: name.trim() || undefined }),
		onSuccess: () => { toast.success(`Snapshot created for ${node.name}`); onCreated(); onClose() },
		onError: (e: Error) => toast.error(e.message)
	})

	return (
		<Modal title={`Create Snapshot: ${node.name}`} onClose={onClose} size="sm">
			<div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
				<label className="field">
					<span className="field-label">Snapshot Name (suffix)</span>
					<input 
						value={name} 
						onChange={e => setName(e.target.value)} 
						placeholder="Optional (e.g. 'manual-1')" 
						className="input" 
						autoFocus 
						onKeyDown={e => e.key === 'Enter' && mutation.mutate()}
					/>
					<div style={{ fontSize: 'var(--text-3xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>
						Format: {node.name}@SNAPSHOT_NAME. If omitted, a timestamp will be used.
					</div>
				</label>
			</div>
			<div className="modal-footer">
				<button onClick={onClose} className="btn btn-ghost">Cancel</button>
				<button onClick={() => mutation.mutate()} disabled={mutation.isPending} className="btn btn-primary">
					{mutation.isPending ? 'Creating…' : 'Create Snapshot'}
				</button>
			</div>
		</Modal>
	)
}

// ---------------------------------------------------------------------------
// CloneSnapshotModal (v7.5.1)
// ---------------------------------------------------------------------------

function CloneSnapshotModal({ node, onClose, onCloned }: { node: TreeNode; onClose: () => void; onCloned: () => void }) {
	const [selectedSnap, setSelectedSnap] = useState('')
	const [cloneName, setCloneName] = useState('')

	const snapsQ = useQuery({
		queryKey: ['zfs', 'snapshots', node.name],
		queryFn: ({ signal }) => api.get<{ snapshots: Snapshot[] }>(`/api/zfs/snapshots?dataset=${encodeURIComponent(node.name)}`, signal),
	})

	const snapshots = snapsQ.data?.snapshots ?? []

	// Default clone name derived from selected snapshot
	const derivedName = selectedSnap
		? node.name + '-clone-' + selectedSnap.split('@')[1]
		: ''

	const targetName = cloneName.trim() || derivedName

	const mutation = useMutation({
		mutationFn: () => api.post('/api/zfs/snapshots/clone', { snapshot: selectedSnap, clone: targetName }),
		onSuccess: () => { toast.success(`Cloned ${selectedSnap} to ${targetName}`); onCloned(); onClose() },
		onError: (e: Error) => toast.error(e.message),
	})

	return (
		<Modal title={<>Clone Snapshot: <span style={{ color: 'var(--primary)', fontFamily: 'var(--font-mono)' }}>{node.name}</span></>} onClose={onClose} size="sm">
			<div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
				<label className="field">
					<span className="field-label">Source Snapshot</span>
					{snapsQ.isLoading ? (
						<Skeleton />
					) : snapshots.length === 0 ? (
						<div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)', padding: '8px 0' }}>
							No snapshots exist for this dataset. Create one first.
						</div>
					) : (
						<select
							className="input"
							value={selectedSnap}
							onChange={e => { setSelectedSnap(e.target.value); setCloneName('') }}
						>
							<option value="">Select a snapshot...</option>
							{snapshots.map(s => (
								<option key={s.name} value={s.name}>{s.snap_name} ({s.refer})</option>
							))}
						</select>
					)}
				</label>

				{selectedSnap && (
					<label className="field">
						<span className="field-label">Clone Dataset Name</span>
						<input
							className="input"
							value={cloneName}
							onChange={e => setCloneName(e.target.value)}
							placeholder={derivedName}
						/>
						<div style={{ fontSize: 'var(--text-3xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>
							Will be created as: {targetName}
						</div>
					</label>
				)}
			</div>
			<div className="modal-footer">
				<button onClick={onClose} className="btn btn-ghost">Cancel</button>
				<button
					onClick={() => mutation.mutate()}
					disabled={!selectedSnap || !targetName || mutation.isPending}
					className="btn btn-primary"
				>
					{mutation.isPending ? 'Cloning…' : 'Clone'}
				</button>
			</div>
		</Modal>
	)
}

// ---------------------------------------------------------------------------
// RollbackModal (v7.3.0)
// ---------------------------------------------------------------------------

function RollbackModal({ node, onClose, onRollback }: { node: TreeNode; onClose: () => void; onRollback: () => void }) {
	const [confirm, setConfirm] = useState(false)
	const mutation = useMutation({
		mutationFn: () => api.post('/api/zfs/snapshots/rollback', { dataset: node.name }),
		onSuccess: () => { toast.success(`Rollback of ${node.name} initiated`); onRollback(); onClose() },
		onError: (e: Error) => toast.error(e.message)
	})

	return (
		<Modal title={<span style={{ color: 'var(--warning)' }}>Rollback Dataset</span>} onClose={onClose} size="sm">
			<div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
				<div className="alert alert-warning">
					<Icon name="history" size={16} />
					<strong>Warning:</strong> This will roll {node.name} back to its most recent snapshot. 
					<strong> All data changes made since that snapshot will be lost.</strong>
				</div>
				<label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer', padding: 12, background: 'rgba(255,255,255,0.03)', borderRadius: 'var(--radius-md)' }}>
					<input type="checkbox" checked={confirm} onChange={e => setConfirm(e.target.checked)} />
					<span style={{ fontSize: 'var(--text-sm)' }}>I confirm I want to rollback and lose recent changes</span>
				</label>
			</div>
			<div className="modal-footer">
				<button onClick={onClose} className="btn btn-ghost">Cancel</button>
				<button onClick={() => mutation.mutate()} disabled={!confirm || mutation.isPending} className="btn btn-warning">
					{mutation.isPending ? 'Rolling back…' : 'Rollback'}
				</button>
			</div>
		</Modal>
	)
}
