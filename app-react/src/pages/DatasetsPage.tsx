/**
 * pages/DatasetsPage.tsx
 *
 * Global cross-pool dataset browser. Renders all pools and their datasets
 * in a single searchable tree - purpose-built for large storage environments
 * with many pools and deep dataset hierarchies.
 *
 * API:
 *   GET  /api/zfs/pools
 *   GET  /api/zfs/datasets
 *   POST /api/zfs/datasets              (create)
 *   POST /api/zfs/command               (edit properties, destroy)
 *   POST /api/zfs/snapshots             (create snapshot)
 *   POST /api/zfs/snapshots/rollback
 *   POST /api/zfs/snapshots/clone
 *   GET  /api/zfs/snapshots?dataset=X
 *   GET  /api/shares/by-path?path=X
 */

import { useState, useEffect, useRef, useMemo, useId } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { Tooltip } from '@/components/ui/Tooltip'
import { toast } from '@/hooks/useToast'
import { Modal } from '@/components/ui/Modal'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ZFSPool {
  name: string; size: string; alloc: string; free: string
  used: string; capacity: string; health: string; type: string
}
interface PoolsResponse { success: boolean; pools?: ZFSPool[]; data?: ZFSPool[] }

interface ZFSDataset {
  name: string; used: string; avail: string; mountpoint: string; quota: string
  compression?: string
  compressratio?: string
  refcompressratio?: string
  encryption?: string   // e.g. "aes-256-gcm" or "off"
  keystatus?: string    // "available" | "unavailable" | "-"
}
interface DatasetsResponse { success: boolean; data: ZFSDataset[] }

interface Snapshot { name: string; dataset: string; snap_name: string; used: string; refer: string; creation: string }

interface TreeNode extends ZFSDataset { children: TreeNode[] }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

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

function formatCompressRatio(d: Pick<ZFSDataset, 'compressratio' | 'refcompressratio'>): string {
  const ref = (d.refcompressratio ?? '').trim()
  const total = (d.compressratio ?? '').trim()
  if (ref && ref !== '-') return ref
  if (total && total !== '-') return total
  return '-'
}

function ratioTooltip(d: ZFSDataset): string {
  const ref = (d.refcompressratio ?? '').trim()
  const cr = (d.compressratio ?? '').trim()
  const parts: string[] = []
  if (ref && ref !== '-') parts.push(`Referenced data: ${ref}`)
  if (cr && cr !== '-') parts.push(`Used space (dataset): ${cr}`)
  if (parts.length === 0) return 'No ratio from ZFS (compression off, empty dataset, or unavailable).'
  return `${parts.join('. ')}.`
}

function parseCapacityPct(capacity: string): number {
  return Math.min(100, Math.max(0, parseInt((capacity || '0').replace('%', '')) || 0))
}

const healthColor = (h: string) =>
  h === 'ONLINE' ? 'var(--success)' : h === 'DEGRADED' ? 'var(--warning)' : 'var(--error)'

const ZSTD_LEVELS = Array.from({ length: 19 }, (_, i) => `zstd-${i + 1}`)

// ---------------------------------------------------------------------------
// StorageTreemap helpers
// ---------------------------------------------------------------------------

function parseBytes(s: string): number {
  const str = (s ?? '').trim().toUpperCase().replace(/\s/g, '')
  if (!str || str === '-' || str === 'NONE') return 0
  const num = parseFloat(str)
  if (isNaN(num)) return 0
  if (str.endsWith('T')) return num * 1_000_000_000_000
  if (str.endsWith('G')) return num * 1_000_000_000
  if (str.endsWith('M')) return num * 1_000_000
  if (str.endsWith('K')) return num * 1_000
  return num
}

interface TMapItem { name: string; pool: string; usedStr: string; area: number }
interface TMapRect { name: string; pool: string; usedStr: string; x: number; y: number; w: number; h: number }

function toRect(item: TMapItem, x: number, y: number, w: number, h: number): TMapRect {
  return { name: item.name, pool: item.pool, usedStr: item.usedStr, x, y, w, h }
}

function tmapWorstRatio(rowAreas: number[], rowArea: number, short: number): number {
  const s = rowArea / short
  return Math.max(...rowAreas.map(a => { const l = (a / rowArea) * short; return Math.max(s / l, l / s) }))
}

function tmapLayout(items: TMapItem[], x: number, y: number, w: number, h: number): TMapRect[] {
  if (!items.length || w < 1 || h < 1) return []
  if (items.length === 1) return [toRect(items[0], x, y, w, h)]

  const short = Math.min(w, h)
  let row: TMapItem[] = []
  let rowArea = 0
  let best = Infinity

  for (const item of items) {
    const cand = [...row, item]
    const ca = rowArea + item.area
    const r = tmapWorstRatio(cand.map(c => c.area), ca, short)
    if (row.length > 0 && r > best) break
    row = cand; rowArea = ca; best = r
  }

  const rects: TMapRect[] = []
  const rowW = rowArea / short
  const horiz = w >= h
  let pos = horiz ? y : x

  for (const item of row) {
    const len = (item.area / rowArea) * short
    rects.push(horiz
      ? toRect(item, x, pos, rowW, len)
      : toRect(item, pos, y, len, rowW)
    )
    pos += len
  }

  const rest = items.slice(row.length)
  if (rest.length) {
    rects.push(...(horiz
      ? tmapLayout(rest, x + rowW, y, w - rowW, h)
      : tmapLayout(rest, x, y + rowW, w, h - rowW)
    ))
  }
  return rects
}

// [normal, hovered] fill pairs, one per pool slot
const POOL_CLR: [string, string][] = [
  ['hsla(236,78%,65%,.72)', 'hsla(236,78%,75%,.92)'],
  ['hsla(190,80%,45%,.72)', 'hsla(190,80%,55%,.92)'],
  ['hsla(142,60%,48%,.72)', 'hsla(142,60%,58%,.92)'],
  ['hsla(28,90%,55%,.72)',  'hsla(28,90%,65%,.92)'],
  ['hsla(280,60%,58%,.72)', 'hsla(280,60%,68%,.92)'],
  ['hsla(340,70%,58%,.72)', 'hsla(340,70%,68%,.92)'],
]

const TMAP_H = 260
const TMAP_GAP = 2

function StorageTreemap({ datasets, pools, onSelect }: {
  datasets: ZFSDataset[]
  pools: ZFSPool[]
  onSelect: (name: string) => void
}) {
  const containerRef = useRef<HTMLDivElement>(null)
  const [svgW, setSvgW] = useState(800)
  const [hov, setHov] = useState<string | null>(null)
  const clipPfx = useId().replace(/[^a-zA-Z0-9]/g, '')

  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    setSvgW(Math.floor(el.getBoundingClientRect().width) || 800)
    const obs = new ResizeObserver(es => {
      const w = es[0]?.contentRect.width
      if (w && w > 1) setSvgW(Math.floor(w))
    })
    obs.observe(el)
    return () => obs.disconnect()
  }, [])

  const poolIdx = useMemo(() => {
    const m: Record<string, number> = {}
    pools.forEach((p, i) => { m[p.name] = i % POOL_CLR.length })
    return m
  }, [pools])

  const items = useMemo<TMapItem[]>(() => {
    const pSet = new Set(pools.map(p => p.name))
    return datasets
      .filter(d => !pSet.has(d.name))
      .map(d => ({ name: d.name, pool: d.name.split('/')[0], usedStr: d.used, area: parseBytes(d.used) }))
      .filter(d => d.area > 0)
      .sort((a, b) => b.area - a.area)
  }, [datasets, pools])

  const totalBytes = useMemo(() => items.reduce((s, i) => s + i.area, 0), [items])

  const rects = useMemo<TMapRect[]>(() => {
    if (svgW < 10 || !items.length || !totalBytes) return []
    const totalArea = svgW * TMAP_H
    const scaled = items.map(i => ({ ...i, area: (i.area / totalBytes) * totalArea }))
    return tmapLayout(scaled, 0, 0, svgW, TMAP_H)
  }, [items, svgW, totalBytes])

  if (!items.length) return null

  return (
    <div style={{ marginBottom: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8, flexWrap: 'wrap', gap: 8 }}>
        <span style={{ fontSize: 'var(--text-xs)', fontWeight: 700, color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.7px' }}>
          Storage Map
        </span>
        <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
          {pools.map((p, i) => (
            <div key={p.name} style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
              <div style={{ width: 9, height: 9, borderRadius: 2, background: POOL_CLR[i % POOL_CLR.length][0], flexShrink: 0 }} />
              <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)' }}>{p.name}</span>
            </div>
          ))}
        </div>
      </div>

      <div ref={containerRef} className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <svg
          width={svgW} height={TMAP_H}
          role="img"
          aria-label={`Storage map: ${items.length} datasets across ${pools.length} pool${pools.length !== 1 ? 's' : ''}. Click a cell to filter to that dataset.`}
          style={{ display: 'block' }}
        >
          <defs>
            {rects.map((r, i) => {
              const rw = Math.max(0, r.w - TMAP_GAP)
              const rh = Math.max(0, r.h - TMAP_GAP)
              const rx = r.x + TMAP_GAP / 2
              const ry = r.y + TMAP_GAP / 2
              return (
                <clipPath key={i} id={`${clipPfx}c${i}`}>
                  <rect x={rx + 4} y={ry + 4} width={Math.max(0, rw - 8)} height={Math.max(0, rh - 8)} />
                </clipPath>
              )
            })}
          </defs>

          {rects.map((r, i) => {
            const ci = poolIdx[r.pool] ?? 0
            const isHov = hov === r.name
            const fill = isHov ? POOL_CLR[ci][1] : POOL_CLR[ci][0]
            const rw = Math.max(0, r.w - TMAP_GAP)
            const rh = Math.max(0, r.h - TMAP_GAP)
            const rx = r.x + TMAP_GAP / 2
            const ry = r.y + TMAP_GAP / 2
            const shortName = r.name.split('/').pop() || r.name
            const showLabel = rw > 46 && rh > 20
            const label = rw > 160 && rh > 20 ? r.name : shortName

            return (
              <g
                key={r.name}
                onClick={() => onSelect(r.name)}
                onMouseEnter={() => setHov(r.name)}
                onMouseLeave={() => setHov(null)}
                onFocus={() => setHov(r.name)}
                onBlur={() => setHov(null)}
                onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onSelect(r.name) } }}
                tabIndex={0}
                role="button"
                aria-label={`${r.name}: ${r.usedStr} used. Press Enter to filter.`}
                style={{ cursor: 'pointer', outline: 'none' }}
              >
                <rect
                  x={rx} y={ry} width={rw} height={rh} rx={3}
                  fill={fill}
                  stroke={isHov ? 'rgba(255,255,255,0.5)' : 'none'}
                  strokeWidth={1}
                />
                {showLabel && (
                  <g clipPath={`url(#${clipPfx}c${i})`} style={{ pointerEvents: 'none' }}>
                    <text x={rx + 6} y={ry + 14} fontSize={11} fontWeight="600" fill="rgba(255,255,255,0.95)" style={{ userSelect: 'none' }}>
                      {label}
                    </text>
                    {rh > 32 && (
                      <text x={rx + 6} y={ry + 27} fontSize={9.5} fill="rgba(255,255,255,0.65)" style={{ userSelect: 'none' }}>
                        {r.usedStr}
                      </text>
                    )}
                  </g>
                )}
              </g>
            )
          })}
        </svg>
      </div>

      <details style={{ marginTop: 6 }}>
        <summary style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', cursor: 'pointer', userSelect: 'none', padding: '4px 0' }}>
          View as table ({items.length} datasets)
        </summary>
        <div className="card" style={{ padding: 0, overflow: 'hidden', marginTop: 6 }}>
          <table className="data-table" aria-label="Dataset storage usage">
            <thead><tr><th>Dataset</th><th>Pool</th><th>Used</th></tr></thead>
            <tbody>
              {items.map(d => (
                <tr key={d.name} onClick={() => onSelect(d.name)} style={{ cursor: 'pointer' }}>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{d.name}</td>
                  <td style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-xs)' }}>{d.pool}</td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{d.usedStr}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Modals - mirroring PoolsPage implementations exactly
// ---------------------------------------------------------------------------

function CreateDatasetModal({ parentName, onClose, onCreated }: {
  parentName: string; onClose: () => void; onCreated: () => void
}) {
  const [childName, setChildName] = useState('')
  const [compression, setCompression] = useState('lz4')
  const [dedup, setDedup] = useState('off')
  const [quota, setQuota] = useState('')

  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/datasets', {
      name: `${parentName}/${childName}`,
      mountpoint: `/${parentName}/${childName}`,
      quota,
      compression,
      dedup,
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
            <option value="zstd">ZSTD (default level)</option>
            {ZSTD_LEVELS.map(z => <option key={z} value={z}>{z}</option>)}
            <option value="gzip">GZIP</option>
            <option value="off">Off</option>
          </select>
        </label>
        <label className="field">
          <span className="field-label">Deduplication</span>
          <select value={dedup} onChange={e => setDedup(e.target.value)} className="input">
            <option value="off">Off (recommended)</option>
            <option value="on">On (SHA-256)</option>
            <option value="verify">Verify (byte-for-byte)</option>
            <option value="sha512">SHA-512</option>
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

function EditDatasetModal({ node, onClose, onUpdated }: {
  node: TreeNode; onClose: () => void; onUpdated: () => void
}) {
  const [compression, setCompression] = useState('inherit')
  const [quota, setQuota] = useState(node.quota === 'none' ? '' : node.quota)
  const [advOpen, setAdvOpen] = useState(false)
  const [atime, setAtime] = useState('')
  const [sync, setSync] = useState('')
  const [recordsize, setRecordsize] = useState('')
  const [xattr, setXattr] = useState('')
  const [secondarycache, setSecondarycache] = useState('')

  const sessionId = () => localStorage.getItem('session_id') ?? ''
  const username = () => localStorage.getItem('username') ?? ''

  async function zfsSet(prop: string, value: string) {
    await api.post('/api/zfs/command', {
      command: 'zfs_set_property',
      args: ['set', `${prop}=${value}`, node.name],
      session_id: sessionId(),
      user: username(),
    })
  }

  const mutation = useMutation({
    mutationFn: async () => {
      if (compression !== 'inherit') await zfsSet('compression', compression)
      await zfsSet('quota', quota.trim() || 'none')
      if (advOpen) {
        if (atime) await zfsSet('atime', atime)
        if (sync) await zfsSet('sync', sync)
        if (recordsize) await zfsSet('recordsize', recordsize)
        if (xattr) await zfsSet('xattr', xattr)
        if (secondarycache) await zfsSet('secondarycache', secondarycache)
      }
    },
    onSuccess: () => { toast.success(`${node.name} updated`); onUpdated(); onClose() },
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
            <option value="zstd">ZSTD (default level)</option>
            {ZSTD_LEVELS.map(z => <option key={z} value={z}>{z}</option>)}
            <option value="gzip">GZIP</option>
            <option value="off">Off</option>
          </select>
        </label>
        <label className="field">
          <span className="field-label">Quota (e.g. 500G or empty for none)</span>
          <input value={quota} onChange={e => setQuota(e.target.value)} placeholder="none" className="input" />
        </label>
        <button type="button" className="btn btn-ghost btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => setAdvOpen(a => !a)}>
          <Icon name="settings" size={14} />{advOpen ? 'Hide' : 'Show'} advanced ZFS properties
        </button>
        {advOpen && (
          <div className="card" style={{ padding: 14, display: 'flex', flexDirection: 'column', gap: 12, background: 'var(--bg-elevated)' }}>
            <label className="field">
              <span className="field-label">Atime</span>
              <select value={atime} onChange={e => setAtime(e.target.value)} className="input">
                <option value="">- no change -</option>
                <option value="on">on</option>
                <option value="off">off</option>
              </select>
            </label>
            <label className="field">
              <span className="field-label">Sync</span>
              <select value={sync} onChange={e => setSync(e.target.value)} className="input">
                <option value="">- no change -</option>
                <option value="standard">standard</option>
                <option value="always">always</option>
                <option value="disabled">disabled</option>
              </select>
            </label>
            <label className="field">
              <span className="field-label">Recordsize</span>
              <select value={recordsize} onChange={e => setRecordsize(e.target.value)} className="input">
                <option value="">- no change -</option>
                {['512', '1K', '2K', '4K', '8K', '16K', '32K', '64K', '128K', '256K', '512K', '1M'].map(rs => (
                  <option key={rs} value={rs}>{rs}</option>
                ))}
              </select>
            </label>
            <label className="field">
              <span className="field-label">Xattr (Linux: sa recommended)</span>
              <select value={xattr} onChange={e => setXattr(e.target.value)} className="input">
                <option value="">- no change -</option>
                <option value="sa">sa (system attributes)</option>
                <option value="on">on</option>
                <option value="off">off</option>
                <option value="dir">dir</option>
              </select>
            </label>
            <label className="field">
              <span className="field-label">Secondary cache (L2ARC)</span>
              <select value={secondarycache} onChange={e => setSecondarycache(e.target.value)} className="input">
                <option value="">- no change -</option>
                <option value="all">all</option>
                <option value="metadata">metadata</option>
                <option value="none">none</option>
              </select>
            </label>
          </div>
        )}
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

function DestroyDatasetModal({ name, onClose, onDestroyed }: {
  name: string; onClose: () => void; onDestroyed: () => void
}) {
  const [confirmName, setConfirmName] = useState('')
  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/command', {
      command: 'zfs_destroy',
      args: ['destroy', '-r', name],
      session_id: localStorage.getItem('session_id'),
      user: localStorage.getItem('username'),
    }),
    onSuccess: () => { toast.success(`Dataset ${name} destroyed`); onDestroyed(); onClose() },
    onError: (e: Error) => toast.error(e.message),
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

function SnapshotModal({ node, onClose, onCreated }: { node: TreeNode; onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/snapshots', { dataset: node.name, name: name.trim() || undefined }),
    onSuccess: () => { toast.success(`Snapshot created for ${node.name}`); onCreated(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={`Create Snapshot: ${node.name}`} onClose={onClose} size="sm">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Snapshot Name (suffix)</span>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="Optional (e.g. 'manual-1')"
            className="input" autoFocus onKeyDown={e => e.key === 'Enter' && mutation.mutate()} />
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

function RollbackModal({ node, onClose, onRollback }: { node: TreeNode; onClose: () => void; onRollback: () => void }) {
  const [confirm, setConfirm] = useState(false)
  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/snapshots/rollback', { dataset: node.name }),
    onSuccess: () => { toast.success(`Rollback of ${node.name} initiated`); onRollback(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={<span style={{ color: 'var(--warning)' }}>Rollback Dataset</span>} onClose={onClose} size="sm">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div className="alert alert-warning">
          <Icon name="history" size={16} />
          <strong>Warning:</strong> This will roll {node.name} back to its most recent snapshot.{' '}
          <strong>All data changes made since that snapshot will be lost.</strong>
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

function CloneSnapshotModal({ node, onClose, onCloned }: { node: TreeNode; onClose: () => void; onCloned: () => void }) {
  const [selectedSnap, setSelectedSnap] = useState('')
  const [cloneName, setCloneName] = useState('')

  const snapsQ = useQuery({
    queryKey: ['zfs', 'snapshots', node.name],
    queryFn: ({ signal }) => api.get<{ snapshots: Snapshot[] }>(`/api/zfs/snapshots?dataset=${encodeURIComponent(node.name)}`, signal),
  })
  const snapshots = snapsQ.data?.snapshots ?? []
  const derivedName = selectedSnap ? node.name + '-clone-' + selectedSnap.split('@')[1] : ''
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
          {snapsQ.isLoading ? <Skeleton /> : snapshots.length === 0 ? (
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)', padding: '8px 0' }}>
              No snapshots exist for this dataset. Create one first.
            </div>
          ) : (
            <select className="input" value={selectedSnap} onChange={e => { setSelectedSnap(e.target.value); setCloneName('') }}>
              <option value="">Select a snapshot…</option>
              {snapshots.map(s => <option key={s.name} value={s.name}>{s.snap_name} ({s.refer})</option>)}
            </select>
          )}
        </label>
        {selectedSnap && (
          <label className="field">
            <span className="field-label">Clone Dataset Name</span>
            <input className="input" value={cloneName} onChange={e => setCloneName(e.target.value)} placeholder={derivedName} />
            <div style={{ fontSize: 'var(--text-3xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>Will be created as: {targetName}</div>
          </label>
        )}
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={!selectedSnap || !targetName || mutation.isPending} className="btn btn-primary">
          {mutation.isPending ? 'Cloning…' : 'Clone'}
        </button>
      </div>
    </Modal>
  )
}

function DatasetSharingModal({ node, onClose }: { node: TreeNode; onClose: () => void }) {
  const { data, isLoading } = useQuery({
    queryKey: ['shares', 'by-path', node.name],
    queryFn: () => api.get<any>(`/api/shares/by-path?path=/${node.name}`),
  })

  return (
    <Modal title={<>Sharing: <span style={{ color: 'var(--primary)', fontFamily: 'var(--font-mono)' }}>/{node.name}</span></>} onClose={onClose} size="lg">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
        <section>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
            <Icon name="folder_shared" style={{ color: 'var(--primary)' }} />
            <h3 style={{ fontSize: 'var(--text-md)', fontWeight: 700 }}>SMB (Windows Shares)</h3>
          </div>
          <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
            {isLoading ? <div style={{ padding: 20 }}><Skeleton /></div> : (
              <table className="data-table">
                <thead><tr><th>Share Name</th><th>Status</th><th>Comment</th></tr></thead>
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
        <section>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
            <Icon name="cloud" style={{ color: 'var(--info)' }} />
            <h3 style={{ fontSize: 'var(--text-md)', fontWeight: 700 }}>NFS (UNIX Exports)</h3>
          </div>
          <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
            {isLoading ? <div style={{ padding: 20 }}><Skeleton /></div> : (
              <table className="data-table">
                <thead><tr><th>Clients</th><th>Status</th><th>Options</th></tr></thead>
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
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// Encryption modals
// ---------------------------------------------------------------------------

function EncryptDatasetModal({ node, onClose, onDone }: { node: TreeNode; onClose: () => void; onDone: () => void }) {
  const [passphrase, setPassphrase] = useState('')
  const [confirm, setConfirm]       = useState('')
  const [show, setShow]             = useState(false)
  const mismatch = confirm.length > 0 && passphrase !== confirm

  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/encryption/create', { name: node.name, key: passphrase, encryption: 'aes-256-gcm' }),
    onSuccess: () => { toast.success(`${node.name} encrypted`); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={<>Encrypt: <span style={{ fontFamily: 'var(--font-mono)' }}>{node.name}</span></>} onClose={onClose} size="sm">
      <div role="alert" style={{ display: 'flex', gap: 10, padding: '10px 14px', background: 'var(--warning-bg)', border: '1px solid var(--warning-border)', borderRadius: 'var(--radius-md)', marginBottom: 16, fontSize: 'var(--text-xs)', color: 'var(--warning)' }}>
        <Icon name="warning" size={16} style={{ flexShrink: 0 }} aria-hidden="true" />
        <span>Encryption applies to new data. Existing data must be migrated. The passphrase cannot be recovered if lost.</span>
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Passphrase</span>
          <div style={{ position: 'relative' }}>
            <input
              type={show ? 'text' : 'password'}
              value={passphrase}
              onChange={e => setPassphrase(e.target.value)}
              className="input"
              style={{ paddingRight: 36, width: '100%', boxSizing: 'border-box' }}
              autoFocus
              aria-describedby="enc-passphrase-hint"
            />
            <button type="button" onClick={() => setShow(s => !s)} aria-label={show ? 'Hide passphrase' : 'Show passphrase'}
              style={{ position: 'absolute', right: 8, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', display: 'flex', padding: 4 }}>
              <Icon name={show ? 'visibility_off' : 'visibility'} size={16} />
            </button>
          </div>
          <span id="enc-passphrase-hint" style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Min 8 characters</span>
        </label>
        <label className="field">
          <span className="field-label">Confirm Passphrase</span>
          <input
            type={show ? 'text' : 'password'}
            value={confirm}
            onChange={e => setConfirm(e.target.value)}
            className="input"
            style={{ borderColor: mismatch ? 'var(--error-border)' : undefined }}
            aria-invalid={mismatch}
            onKeyDown={e => e.key === 'Enter' && !mismatch && passphrase.length >= 8 && mutation.mutate()}
          />
          {mismatch && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--error)' }} role="alert">Passphrases do not match</span>}
        </label>
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button
          onClick={() => mutation.mutate()}
          disabled={mutation.isPending || mismatch || passphrase.length < 8}
          className="btn btn-primary"
        >
          <Icon name="lock" size={15} />{mutation.isPending ? 'Encrypting…' : 'Enable Encryption'}
        </button>
      </div>
    </Modal>
  )
}

function UnlockDatasetModal({ node, onClose, onDone }: { node: TreeNode; onClose: () => void; onDone: () => void }) {
  const [passphrase, setPassphrase] = useState('')
  const [show, setShow]             = useState(false)

  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/encryption/unlock', { dataset: node.name, key: passphrase }),
    onSuccess: () => { toast.success(`${node.name} unlocked`); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={<>Unlock: <span style={{ fontFamily: 'var(--font-mono)' }}>{node.name}</span></>} onClose={onClose} size="sm">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Passphrase</span>
          <div style={{ position: 'relative' }}>
            <input
              type={show ? 'text' : 'password'}
              value={passphrase}
              onChange={e => setPassphrase(e.target.value)}
              className="input"
              style={{ paddingRight: 36, width: '100%', boxSizing: 'border-box' }}
              autoFocus
              onKeyDown={e => e.key === 'Enter' && mutation.mutate()}
            />
            <button type="button" onClick={() => setShow(s => !s)} aria-label={show ? 'Hide passphrase' : 'Show passphrase'}
              style={{ position: 'absolute', right: 8, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', display: 'flex', padding: 4 }}>
              <Icon name={show ? 'visibility_off' : 'visibility'} size={16} />
            </button>
          </div>
        </label>
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending || !passphrase} className="btn btn-primary">
          <Icon name="lock_open" size={15} />{mutation.isPending ? 'Unlocking…' : 'Unlock'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// DatasetActionMenu
// ---------------------------------------------------------------------------

function MenuBtn({ icon, label, onClick, danger }: { icon: string; label: string; onClick: () => void; danger?: boolean }) {
  return (
    <button
      onClick={e => { e.stopPropagation(); onClick() }}
      style={{
        width: '100%', display: 'flex', alignItems: 'center', gap: 10, padding: '10px 14px',
        background: 'none', border: 'none', cursor: 'pointer',
        color: danger ? 'var(--error)' : 'var(--text)',
        fontSize: 'var(--text-xs)', fontWeight: 500, transition: 'background 0.1s',
      }}
      onMouseEnter={e => (e.currentTarget.style.background = 'hsla(0,0%,100%,0.05)')}
      onMouseLeave={e => (e.currentTarget.style.background = 'none')}
    >
      <Icon name={icon} size={14} style={{ flexShrink: 0 }} />
      {label}
    </button>
  )
}

function DatasetActionMenu({ node, onAction }: { node: TreeNode; onAction: (action: string, node: TreeNode) => void }) {
  const [open, setOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  return (
    <div style={{ position: 'relative' }} ref={menuRef}>
      <button
        onClick={e => { e.stopPropagation(); setOpen(o => !o) }}
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
          overflow: 'hidden', animation: 'fadeIn 0.15s ease',
        }}>
          <MenuBtn icon="add" label="Create Child" onClick={() => { setOpen(false); onAction('create_child', node) }} />
          <MenuBtn icon="camera" label="Snapshot" onClick={() => { setOpen(false); onAction('snapshot', node) }} />
          <MenuBtn icon="history" label="Rollback" onClick={() => { setOpen(false); onAction('rollback', node) }} />
          <MenuBtn icon="fork_right" label="Clone Snapshot" onClick={() => { setOpen(false); onAction('clone', node) }} />
          <div style={{ height: 1, background: 'var(--border-subtle)', margin: '4px 0' }} />
          <MenuBtn icon="folder_shared" label="Manage Shares" onClick={() => { setOpen(false); onAction('manage_shares', node) }} />
          <div style={{ height: 1, background: 'var(--border-subtle)', margin: '4px 0' }} />
          {/* Encryption actions - contextual on current state */}
          {(!node.encryption || node.encryption === 'off' || node.encryption === '-') && (
            <MenuBtn icon="lock" label="Encrypt…" onClick={() => { setOpen(false); onAction('encrypt', node) }} />
          )}
          {node.encryption && node.encryption !== 'off' && node.encryption !== '-' && node.keystatus === 'available' && (
            <MenuBtn icon="lock" label="Lock" onClick={() => { setOpen(false); onAction('lock', node) }} />
          )}
          {node.encryption && node.encryption !== 'off' && node.encryption !== '-' && node.keystatus === 'unavailable' && (
            <MenuBtn icon="lock_open" label="Unlock…" onClick={() => { setOpen(false); onAction('unlock', node) }} />
          )}
          <div style={{ height: 1, background: 'var(--border-subtle)', margin: '4px 0' }} />
          <MenuBtn icon="settings" label="Properties" onClick={() => { setOpen(false); onAction('edit', node) }} />
          <MenuBtn icon="delete" label="Destroy" danger onClick={() => { setOpen(false); onAction('delete', node) }} />
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// DatasetNode (recursive tree row)
// ---------------------------------------------------------------------------

function DatasetNode({ node, depth, onAction }: {
  node: TreeNode; depth: number; onAction: (action: string, node: TreeNode) => void
}) {
  const [open, setOpen] = useState(depth <= 1)
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
        <Icon
          name={depth === 0 ? 'storage' : node.children.length > 0 ? 'folder' : 'dataset'}
          size={16}
          style={{ color: depth === 0 ? 'var(--primary)' : 'var(--text-tertiary)', flexShrink: 0 }}
        />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', fontWeight: depth === 0 ? 700 : 500 }}>{shortName}</span>
            {node.encryption && node.encryption !== 'off' && node.encryption !== '-' && (
              <Tooltip content={node.keystatus === 'unavailable' ? 'Encrypted - locked' : `Encrypted (${node.encryption})`}>
                <span aria-label={node.keystatus === 'unavailable' ? 'Locked' : 'Encrypted'}>
                  <Icon
                    name={node.keystatus === 'unavailable' ? 'lock' : 'lock_open'}
                    size={12}
                    style={{ color: node.keystatus === 'unavailable' ? 'var(--warning)' : 'var(--success)' }}
                  />
                </span>
              </Tooltip>
            )}
          </div>
          {isMounted && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{node.mountpoint}</div>}
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexShrink: 0 }}>
          <div style={{ textAlign: 'right', minWidth: 56 }}>
            <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, fontFamily: 'var(--font-mono)' }}>{node.used || '-'}</div>
            <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>used</div>
          </div>
          <div style={{ textAlign: 'right', minWidth: 56 }}>
            <div style={{ fontSize: 'var(--text-sm)', fontFamily: 'var(--font-mono)' }}>{node.avail || '-'}</div>
            <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>avail</div>
          </div>
          <div style={{ textAlign: 'right', minWidth: 64 }}>
            <div style={{ fontSize: 'var(--text-xs)', fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}
              title={node.compression || undefined}>
              {node.compression && node.compression !== '-' ? node.compression : '-'}
            </div>
            <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>compress</div>
          </div>
          <div style={{ textAlign: 'right', minWidth: 52 }}>
            <Tooltip content={ratioTooltip(node)}>
              <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, fontFamily: 'var(--font-mono)', cursor: 'help' }}>{formatCompressRatio(node)}</div>
            </Tooltip>
            <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>ratio</div>
          </div>
          <DatasetActionMenu node={node} onAction={onAction} />
        </div>
      </div>
      {open && node.children.map(c => (
        <DatasetNode key={c.name} node={c} depth={depth + 1} onAction={onAction} />
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// PoolSection - one pool's header + dataset tree
// ---------------------------------------------------------------------------

function PoolSection({ pool, datasets, filter, onAction, defaultOpen }: {
  pool: ZFSPool
  datasets: ZFSDataset[]
  filter: string
  onAction: (action: string, node: TreeNode) => void
  defaultOpen: boolean
}) {
  const [open, setOpen] = useState(defaultOpen)
  const pct = parseCapacityPct(pool.capacity)

  const filterLower = filter.toLowerCase()
  const filtered = filterLower
    ? datasets.filter(d =>
        d.name.toLowerCase().includes(filterLower) ||
        (d.mountpoint || '').toLowerCase().includes(filterLower)
      )
    : datasets

  const tree = buildTree(filtered, pool.name)

  const matchCount = filterLower
    ? filtered.filter(d => d.name !== pool.name).length
    : datasets.filter(d => d.name !== pool.name).length

  // Auto-expand when a filter is active and this pool has matches
  const effectiveOpen = filterLower ? filtered.length > 0 : open

  if (filterLower && filtered.length === 0) return null

  const barColor = pct >= 90 ? 'var(--error)' : pct >= 75 ? 'var(--warning)' : 'var(--success)'

  return (
    <div className="card" style={{ padding: 0, overflow: 'hidden', marginBottom: 12 }}>
      {/* Pool header */}
      <div
        style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 20px', cursor: 'pointer', borderBottom: effectiveOpen ? '1px solid var(--border-subtle)' : 'none' }}
        onClick={() => !filterLower && setOpen(o => !o)}
      >
        <Icon name="storage" size={18} style={{ color: 'var(--primary)', flexShrink: 0 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <span style={{ fontSize: 'var(--text-md)', fontWeight: 700, fontFamily: 'var(--font-mono)' }}>{pool.name}</span>
            <span
              className="badge"
              style={{
                fontSize: 'var(--text-2xs)', padding: '2px 6px',
                background: pool.health === 'ONLINE' ? 'var(--success-bg)' : pool.health === 'DEGRADED' ? 'var(--warning-bg)' : 'var(--error-bg)',
                color: healthColor(pool.health),
                border: `1px solid ${pool.health === 'ONLINE' ? 'var(--success-border)' : pool.health === 'DEGRADED' ? 'var(--warning-border)' : 'var(--error-border)'}`,
              }}
            >
              {pool.health}
            </span>
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              {matchCount} dataset{matchCount !== 1 ? 's' : ''}
            </span>
          </div>
          {/* Capacity bar */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 6 }}>
            <div style={{ flex: 1, height: 4, background: 'rgba(255,255,255,0.06)', borderRadius: 999, overflow: 'hidden', maxWidth: 200 }}>
              <div style={{ height: '100%', width: `${pct}%`, background: barColor, borderRadius: 999, transition: 'width 0.6s' }} />
            </div>
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', whiteSpace: 'nowrap' }}>
              {pool.capacity} used
            </span>
          </div>
        </div>
        {!filterLower && (
          <Icon name={effectiveOpen ? 'expand_less' : 'expand_more'} size={18} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
        )}
      </div>

      {/* Dataset tree */}
      {effectiveOpen && (
        <div style={{ padding: '8px 8px' }}>
          {tree.length === 0 ? (
            <div style={{ padding: '16px 12px', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)', textAlign: 'center' }}>
              {filterLower ? `No datasets match "${filter}"` : 'No datasets in this pool.'}
            </div>
          ) : (
            tree.map(root => (
              <DatasetNode key={root.name} node={root} depth={0} onAction={onAction} />
            ))
          )}
          {/* Add dataset shortcut */}
          {!filterLower && (
            <button
              onClick={() => onAction('create_child', { name: pool.name, used: '', avail: '', mountpoint: '', quota: '', children: [] })}
              style={{
                marginTop: 4, background: 'none', border: '1px dashed var(--border)', borderRadius: 'var(--radius-sm)',
                padding: '7px 14px', cursor: 'pointer', color: 'var(--text-tertiary)',
                display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-sm)',
                width: '100%', transition: 'all 0.15s',
              }}
              onMouseEnter={e => { (e.currentTarget as HTMLButtonElement).style.borderColor = 'var(--primary)'; (e.currentTarget as HTMLButtonElement).style.color = 'var(--primary)' }}
              onMouseLeave={e => { (e.currentTarget as HTMLButtonElement).style.borderColor = 'var(--border)'; (e.currentTarget as HTMLButtonElement).style.color = 'var(--text-tertiary)' }}
            >
              <Icon name="add" size={14} /> Add dataset in {pool.name}
            </button>
          )}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// PoolPickerButton - "Create Dataset" that drops a pool list when >1 pool
// ---------------------------------------------------------------------------

function PoolPickerButton({ pools, disabled, onPick }: {
  pools: ZFSPool[]
  disabled: boolean
  onPick: (poolName: string) => void
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  function handleClick() {
    if (pools.length === 0) return
    if (pools.length === 1) { onPick(pools[0].name); return }
    setOpen(o => !o)
  }

  return (
    <div style={{ position: 'relative' }} ref={ref}>
      <button className="btn btn-primary" disabled={disabled} onClick={handleClick}>
        <Icon name="add" size={16} /> Create Dataset
        {pools.length > 1 && <Icon name="arrow_drop_down" size={18} style={{ marginLeft: 2, marginRight: -4 }} />}
      </button>
      {open && (
        <div style={{
          position: 'absolute', right: 0, top: 'calc(100% + 6px)', zIndex: 200,
          minWidth: 220, background: 'var(--bg-elevated)',
          backdropFilter: 'var(--blur-glass)', border: '1px solid var(--border-highlight)',
          borderRadius: 'var(--radius-md)', boxShadow: 'var(--shadow-lg)',
          overflow: 'hidden', animation: 'slideUp 0.2s ease',
        }}>
          <div style={{ padding: '8px 14px 6px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontWeight: 700, textTransform: 'uppercase', letterSpacing: '0.6px' }}>
            Select pool
          </div>
          {pools.map(p => (
            <button
              key={p.name}
              onClick={() => { setOpen(false); onPick(p.name) }}
              style={{
                width: '100%', display: 'flex', alignItems: 'center', gap: 10,
                padding: '10px 14px', background: 'none', border: 'none',
                cursor: 'pointer', transition: 'background 0.1s', textAlign: 'left',
              }}
              onMouseEnter={e => (e.currentTarget.style.background = 'hsla(0,0%,100%,0.05)')}
              onMouseLeave={e => (e.currentTarget.style.background = 'none')}
            >
              <Icon name="storage" size={16} style={{ color: 'var(--primary)', flexShrink: 0 }} />
              <span style={{ flex: 1, fontSize: 'var(--text-sm)', fontWeight: 600, fontFamily: 'var(--font-mono)', color: 'var(--text)' }}>{p.name}</span>
              <span style={{
                fontSize: 'var(--text-2xs)', fontWeight: 700, padding: '2px 6px', borderRadius: 'var(--radius-full)',
                color: healthColor(p.health),
                background: p.health === 'ONLINE' ? 'var(--success-bg)' : p.health === 'DEGRADED' ? 'var(--warning-bg)' : 'var(--error-bg)',
              }}>
                {p.health}
              </span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// DatasetsPage
// ---------------------------------------------------------------------------

export function DatasetsPage() {
  const qc = useQueryClient()

  const [filter, setFilter] = useState('')
  const [createParent, setCreateParent] = useState<string | null>(null)
  const [editDataset, setEditDataset] = useState<TreeNode | null>(null)
  const [destroyDataset, setDestroyDataset] = useState<string | null>(null)
  const [sharingDataset, setSharingDataset] = useState<TreeNode | null>(null)
  const [snapshotDataset, setSnapshotDataset] = useState<TreeNode | null>(null)
  const [rollbackDataset, setRollbackDataset] = useState<TreeNode | null>(null)
  const [cloneDataset, setCloneDataset] = useState<TreeNode | null>(null)
  const [encryptDataset, setEncryptDataset] = useState<TreeNode | null>(null)
  const [unlockDataset, setUnlockDataset] = useState<TreeNode | null>(null)

  const poolsQ = useQuery({
    queryKey: ['zfs', 'pools'],
    queryFn: ({ signal }) => api.get<PoolsResponse>('/api/zfs/pools', signal),
    refetchInterval: 30_000,
  })
  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets'],
    queryFn: ({ signal }) => api.get<DatasetsResponse>('/api/zfs/datasets', signal),
    refetchInterval: 30_000,
  })

  const pools: ZFSPool[] = poolsQ.data?.pools ?? poolsQ.data?.data ?? []
  const allDatasets: ZFSDataset[] = datasetsQ.data?.data ?? []

  // Group datasets by pool (first path component)
  const datasetsByPool = useMemo(() => {
    const map: Record<string, ZFSDataset[]> = {}
    pools.forEach(p => { map[p.name] = [] })
    allDatasets.forEach(d => {
      const pool = d.name.split('/')[0]
      if (map[pool]) map[pool].push(d)
      else map[pool] = [d]
    })
    return map
  }, [pools, allDatasets])

  const totalDatasets = useMemo(
    () => allDatasets.filter(d => !pools.some(p => p.name === d.name)).length,
    [allDatasets, pools]
  )

  const filterLower = filter.toLowerCase()
  const matchedDatasets = useMemo(() => {
    if (!filterLower) return totalDatasets
    return allDatasets.filter(d =>
      !pools.some(p => p.name === d.name) &&
      (d.name.toLowerCase().includes(filterLower) || (d.mountpoint || '').toLowerCase().includes(filterLower))
    ).length
  }, [filterLower, allDatasets, pools, totalDatasets])

  const lockMutation = useMutation({
    mutationFn: (name: string) => api.post('/api/zfs/encryption/lock', { dataset: name }),
    onSuccess: (_, name) => { toast.success(`${name} locked`); refresh() },
    onError: (e: Error) => toast.error(e.message),
  })

  function handleAction(action: string, node: TreeNode) {
    if (action === 'create_child') setCreateParent(node.name)
    if (action === 'edit') setEditDataset(node)
    if (action === 'delete') setDestroyDataset(node.name)
    if (action === 'manage_shares') setSharingDataset(node)
    if (action === 'snapshot') setSnapshotDataset(node)
    if (action === 'rollback') setRollbackDataset(node)
    if (action === 'clone') setCloneDataset(node)
    if (action === 'encrypt') setEncryptDataset(node)
    if (action === 'lock') lockMutation.mutate(node.name)
    if (action === 'unlock') setUnlockDataset(node)
  }

  function refresh() {
    qc.invalidateQueries({ queryKey: ['zfs', 'datasets'] })
  }

  if (poolsQ.isError) return <ErrorState error={poolsQ.error as Error} />

  return (
    <div style={{ padding: '32px 28px' }}>
      {/* Header */}
      <div style={{ marginBottom: 28 }}>
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 16 }}>
          <div>
            <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1.2px', marginBottom: 6 }}>Datasets</h1>
            <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>
              {pools.length} pool{pools.length !== 1 ? 's' : ''} · {totalDatasets} dataset{totalDatasets !== 1 ? 's' : ''}
              {filterLower && ` · ${matchedDatasets} match${matchedDatasets !== 1 ? 'es' : ''}`}
            </p>
          </div>
          <PoolPickerButton
            pools={pools}
            disabled={pools.length === 0}
            onPick={setCreateParent}
          />
        </div>
      </div>

      {/* Stats strip */}
      {!poolsQ.isLoading && pools.length > 0 && (
        <div style={{ display: 'flex', gap: 12, marginBottom: 24, flexWrap: 'wrap' }}>
          {pools.map(p => {
            const pct = parseCapacityPct(p.capacity)
            const barColor = pct >= 90 ? 'var(--error)' : pct >= 75 ? 'var(--warning)' : 'var(--primary)'
            const dsCount = (datasetsByPool[p.name] ?? []).filter(d => d.name !== p.name).length
            return (
              <div key={p.name} className="card" style={{ padding: '12px 16px', flex: '1 1 180px', minWidth: 160, maxWidth: 260 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 6 }}>
                  <span style={{ fontSize: 'var(--text-sm)', fontWeight: 700, fontFamily: 'var(--font-mono)' }}>{p.name}</span>
                  <span style={{ fontSize: 'var(--text-2xs)', color: healthColor(p.health), fontWeight: 600 }}>{p.health}</span>
                </div>
                <div style={{ height: 3, background: 'rgba(255,255,255,0.06)', borderRadius: 999, overflow: 'hidden', marginBottom: 4 }}>
                  <div style={{ height: '100%', width: `${pct}%`, background: barColor, borderRadius: 999 }} />
                </div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                  {p.capacity} · {dsCount} dataset{dsCount !== 1 ? 's' : ''}
                </div>
              </div>
            )
          })}
        </div>
      )}

      {/* Storage map */}
      {!poolsQ.isLoading && !datasetsQ.isLoading && pools.length > 0 && (
        <StorageTreemap datasets={allDatasets} pools={pools} onSelect={setFilter} />
      )}

      {/* Search */}
      <div style={{ marginBottom: 20 }}>
        <div style={{ position: 'relative', maxWidth: 480 }}>
          <Icon name="search" size={16} style={{ position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-tertiary)', pointerEvents: 'none' }} />
          <input
            value={filter}
            onChange={e => setFilter(e.target.value)}
            placeholder="Filter datasets by name or mountpoint across all pools…"
            className="input"
            style={{ paddingLeft: 34, paddingRight: filter ? 32 : 12 }}
          />
          {filter && (
            <Tooltip content="Clear filter">
              <button
                onClick={() => setFilter('')}
                style={{ position: 'absolute', right: 8, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', display: 'flex', padding: 2 }}
              >
                <Icon name="close" size={14} />
              </button>
            </Tooltip>
          )}
        </div>
      </div>

      {/* Loading skeleton */}
      {(poolsQ.isLoading || datasetsQ.isLoading) && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {[1, 2, 3].map(i => <div key={i} className="card" style={{ height: 80 }}><Skeleton /></div>)}
        </div>
      )}

      {/* Pool sections */}
      {!poolsQ.isLoading && !datasetsQ.isLoading && (
        <>
          {pools.length === 0 ? (
            <div className="empty-state">
              <Icon name="storage" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 16px' }} />
              <div className="empty-state-title">No pools found</div>
              <div style={{ fontSize: 'var(--text-sm)', marginTop: 4 }}>Create a ZFS pool first from the ZFS Pools page.</div>
            </div>
          ) : (
            pools.map(pool => (
              <PoolSection
                key={pool.name}
                pool={pool}
                datasets={datasetsByPool[pool.name] ?? []}
                filter={filter}
                onAction={handleAction}
                defaultOpen
              />
            ))
          )}
          {filterLower && matchedDatasets === 0 && pools.length > 0 && (
            <div className="empty-state">
              <Icon name="search_off" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 16px' }} />
              <div className="empty-state-title">No datasets match</div>
              <div style={{ fontSize: 'var(--text-sm)', marginTop: 4, color: 'var(--text-tertiary)' }}>
                No datasets or mountpoints contain "{filter}" across any pool.
              </div>
            </div>
          )}
        </>
      )}

      {/* Modals */}
      {createParent && (
        <CreateDatasetModal
          parentName={createParent}
          onClose={() => setCreateParent(null)}
          onCreated={refresh}
        />
      )}
      {editDataset && (
        <EditDatasetModal
          node={editDataset}
          onClose={() => setEditDataset(null)}
          onUpdated={refresh}
        />
      )}
      {destroyDataset && (
        <DestroyDatasetModal
          name={destroyDataset}
          onClose={() => setDestroyDataset(null)}
          onDestroyed={refresh}
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
          onCreated={refresh}
        />
      )}
      {rollbackDataset && (
        <RollbackModal
          node={rollbackDataset}
          onClose={() => setRollbackDataset(null)}
          onRollback={refresh}
        />
      )}
      {cloneDataset && (
        <CloneSnapshotModal
          node={cloneDataset}
          onClose={() => setCloneDataset(null)}
          onCloned={refresh}
        />
      )}
      {encryptDataset && (
        <EncryptDatasetModal
          node={encryptDataset}
          onClose={() => setEncryptDataset(null)}
          onDone={refresh}
        />
      )}
      {unlockDataset && (
        <UnlockDatasetModal
          node={unlockDataset}
          onClose={() => setUnlockDataset(null)}
          onDone={refresh}
        />
      )}
    </div>
  )
}
