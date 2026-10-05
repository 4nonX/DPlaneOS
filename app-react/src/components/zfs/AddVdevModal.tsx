/**
 * components/zfs/AddVdevModal.tsx
 *
 * Expand a pool by adding one data vdev ("Expand Pool" on the pool card).
 *
 *   GET  /api/system/disks
 *   POST /api/zfs/pool/add-vdev { pool, vdev_type, disks }
 *
 * Suggests the layout and width of the pool's existing data vdevs: mixing
 * layouts or widths unbalances the pool, and a data vdev cannot be removed
 * from a pool with RAID-Z vdevs.
 */

import { useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { toast } from '@/hooks/useToast'
import type { PoolTopology } from '@/components/zfs/PoolTopology'
import { type DataLayout, type DiskInfo, LAYOUTS, diskId, formatBytes, minWidth, vdevType } from '@/lib/poolLayout'

/** Layout and width of the pool's first data vdev, if it can be read. */
function existingLayout(topology?: PoolTopology): { layout: DataLayout; width: number } | null {
  const first = topology?.groups?.data?.[0]
  if (!first) return null
  const t = first.type.toLowerCase()
  if (t === 'disk') return { layout: 'stripe', width: 1 }
  const layout: DataLayout | null =
    t.startsWith('mirror') ? 'mirror'
    : t.startsWith('raidz3') ? 'raidz3'
    : t.startsWith('raidz2') ? 'raidz2'
    : t.startsWith('raidz') ? 'raidz1'
    : null
  return layout ? { layout, width: first.children?.length ?? minWidth(layout) } : null
}

export function AddVdevModal({ pool, topology, onClose, onAdded }: {
  pool: string
  topology?: PoolTopology
  onClose: () => void
  onAdded: () => void
}) {
  const current = existingLayout(topology)
  const [layout, setLayout] = useState<DataLayout>(current?.layout ?? 'mirror')
  const [selected, setSelected] = useState<string[]>([])
  const [confirmed, setConfirmed] = useState(false)

  const disksQ = useQuery({
    queryKey: ['system', 'disks'],
    queryFn: () => api.get<{ disks: DiskInfo[] }>('/api/system/disks'),
  })
  const free = useMemo(() => (disksQ.data?.disks ?? []).filter(d => !d.in_use), [disksQ.data])

  const minW = minWidth(layout)
  const tooFew = selected.length < minW
  const layoutMismatch = !!current && current.layout !== layout
  const widthMismatch = !!current && !layoutMismatch && selected.length > 0 && selected.length !== current.width
  const raidz = layout.startsWith('raidz') || current?.layout.startsWith('raidz')

  const mutation = useMutation({
    mutationFn: async () => {
      const res = await api.post<{ success?: boolean; error?: string }>('/api/zfs/pool/add-vdev', {
        pool,
        vdev_type: layout === 'stripe' ? '' : vdevType(layout),
        disks: selected,
      })
      if (res && res.success === false) throw new Error(res.error || 'Adding the vdev failed')
    },
    onSuccess: () => { toast.success(`Added ${selected.length}-disk ${layout} vdev to ${pool}`); onAdded(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  function toggle(id: string, on: boolean) {
    setSelected(s => (on ? [...new Set([...s, id])] : s.filter(x => x !== id)))
    setConfirmed(false)
  }

  return (
    <Modal title={<>Expand Pool <span style={{ fontFamily: 'var(--font-mono)', color: 'var(--primary)' }}>{pool}</span></>} onClose={onClose} size="lg">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14, maxHeight: '70vh', overflowY: 'auto', paddingRight: 4 }}>
        <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          Adds a new data vdev. Capacity grows by that vdev's usable space; existing data stays where it is.
          {current && <> The pool's data vdevs are <strong>{current.width}-wide {current.layout}</strong>.</>}
        </div>

        <label className="field" style={{ maxWidth: 320 }}>
          <span className="field-label">Layout of the new vdev</span>
          <select value={layout} onChange={e => { setLayout(e.target.value as DataLayout); setConfirmed(false) }} className="input">
            {LAYOUTS.map(l => <option key={l.value} value={l.value}>{l.label}</option>)}
          </select>
        </label>

        <div className="field">
          <span className="field-label">Disks ({selected.length} selected{current && !layoutMismatch ? `, ${current.width} to match` : `, at least ${minW}`})</span>
          {disksQ.isLoading ? (
            <div style={{ padding: 16, color: 'var(--text-tertiary)' }}>Loading disks…</div>
          ) : free.length === 0 ? (
            <div style={{ padding: 16, color: 'var(--text-tertiary)', border: '1px dashed var(--border)', borderRadius: 'var(--radius-md)' }}>No unused disks found.</div>
          ) : (
            <div className="card" style={{ maxHeight: 240, overflowY: 'auto', padding: 10, background: 'var(--bg-elevated)' }}>
              {free.map(d => {
                const id = diskId(d)
                return (
                  <label key={id} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: 6, cursor: 'pointer' }}>
                    <input type="checkbox" checked={selected.includes(id)} onChange={e => toggle(id, e.target.checked)} />
                    <span style={{ flex: 1 }}>
                      <span style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>{d.name} · {formatBytes(d.size_bytes)} {d.type}</span>
                      <span style={{ display: 'block', fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>{d.model} · {id}</span>
                    </span>
                  </label>
                )
              })}
            </div>
          )}
        </div>

        {layoutMismatch && (
          <div className="alert alert-warning" style={{ fontSize: 'var(--text-xs)' }}>
            <Icon name="warning" size={16} /><span>The pool uses {current!.layout}. Mixing layouts gives the pool the redundancy of its weakest vdev.</span>
          </div>
        )}
        {widthMismatch && (
          <div className="alert alert-info" style={{ fontSize: 'var(--text-xs)' }}>
            <Icon name="info" size={16} /><span>Existing vdevs are {current!.width} disks wide. Matching widths keep performance and space even.</span>
          </div>
        )}
        {layout === 'stripe' && (
          <div className="alert alert-error" style={{ fontSize: 'var(--text-xs)' }}>
            <Icon name="warning" size={16} /><span>A single-disk vdev has no redundancy: if it fails, the whole pool is lost.</span>
          </div>
        )}
        {!tooFew && (
          <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: 'var(--text-sm)' }}>
            <input type="checkbox" checked={confirmed} onChange={e => setConfirmed(e.target.checked)} />
            {raidz
              ? 'I understand a data vdev cannot be removed from a pool with RAID-Z vdevs.'
              : 'I understand this adds the disks to the pool permanently unless the vdev is later removed.'}
          </label>
        )}
      </div>
      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" disabled={tooFew || !confirmed || mutation.isPending} onClick={() => mutation.mutate()}>
          {mutation.isPending ? 'Adding…' : 'Add vdev'}
        </button>
      </div>
    </Modal>
  )
}
