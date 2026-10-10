/**
 * DatasetOps - rename, promote and snapshot holds for a dataset.
 *
 *   POST /api/zfs/rename  {old_name, new_name}
 *   POST /api/zfs/promote {dataset}
 *   GET  /api/zfs/snapshots?dataset=  → { snapshots: [{name, snap_name, creation, used}] }
 *   GET  /api/zfs/holds?snapshot=     → { holds: [{tag, timestamp}] }
 *   POST /api/zfs/hold    {snapshot, tag}
 *   POST /api/zfs/release {snapshot, tag}
 */
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Modal } from '@/components/ui/Modal'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

type Result = { success?: boolean; error?: string; output?: string }
function check(r: Result) {
  if (r && r.success === false) throw new Error(r.error || r.output || 'Operation failed')
  return r
}

export function RenameDatasetModal({ name, onClose, onDone }: { name: string; onClose: () => void; onDone: () => void }) {
  const [newName, setNewName] = useState(name)
  const pool = name.split('/')[0]
  const rename = useMutation({
    mutationFn: async () => check(await api.post<Result>('/api/zfs/rename', { old_name: name, new_name: newName.trim() })),
    onSuccess: () => { toast.success(`Renamed to ${newName.trim()}`); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })
  const valid = newName.trim() !== name && newName.trim().startsWith(pool + '/')
  return (
    <Modal title={`Rename ${name}`} onClose={onClose} size="sm">
      <p style={{ margin: 0, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        Renaming also moves the dataset (within {pool}). Shares and apps that use its mount point must be updated.
      </p>
      <label className="field">
        <span className="field-label">New name</span>
        <input value={newName} onChange={e => setNewName(e.target.value)} className="input" style={{ fontFamily: 'var(--font-mono)' }} />
      </label>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => rename.mutate()} disabled={!valid || rename.isPending} className="btn btn-primary">{rename.isPending ? 'Renaming…' : 'Rename'}</button>
      </div>
    </Modal>
  )
}

export async function promoteDataset(name: string): Promise<void> {
  check(await api.post<Result>('/api/zfs/promote', { dataset: name }))
}

interface Snap { name: string; snap_name: string; creation: string; used: string }

export function SnapshotHoldsModal({ dataset, onClose }: { dataset: string; onClose: () => void }) {
  const snapsQ = useQuery({
    queryKey: ['zfs', 'snapshots', dataset],
    queryFn: ({ signal }) => api.get<{ success: boolean; snapshots: Snap[] }>(`/api/zfs/snapshots?dataset=${encodeURIComponent(dataset)}`, signal),
  })
  const snaps = (snapsQ.data?.snapshots ?? []).filter(s => s.name.startsWith(dataset + '@'))
  return (
    <Modal title={`Snapshot holds: ${dataset}`} onClose={onClose} size="lg">
      <p style={{ margin: 0, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        A held snapshot cannot be destroyed, neither by hand nor by retention, until every hold is released.
      </p>
      {snapsQ.isLoading && <Skeleton height={120} />}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6, maxHeight: 420, overflowY: 'auto' }}>
        {snaps.map(s => <SnapshotHoldRow key={s.name} snap={s} />)}
        {!snapsQ.isLoading && snaps.length === 0 && <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No snapshots.</div>}
      </div>
    </Modal>
  )
}

function SnapshotHoldRow({ snap }: { snap: Snap }) {
  const qc = useQueryClient()
  const [tag, setTag] = useState('')
  const holdsQ = useQuery({
    queryKey: ['zfs', 'holds', snap.name],
    queryFn: ({ signal }) => api.get<{ success: boolean; holds: { tag: string; timestamp: string }[]; error?: string }>(`/api/zfs/holds?snapshot=${encodeURIComponent(snap.name)}`, signal),
  })
  const refresh = () => qc.invalidateQueries({ queryKey: ['zfs', 'holds', snap.name] })
  const hold = useMutation({
    mutationFn: async () => check(await api.post<Result>('/api/zfs/hold', { snapshot: snap.name, tag: tag.trim() })),
    onSuccess: () => { setTag(''); refresh() },
    onError: (e: Error) => toast.error(e.message),
  })
  const release = useMutation({
    mutationFn: async (t: string) => check(await api.post<Result>('/api/zfs/release', { snapshot: snap.name, tag: t })),
    onSuccess: refresh,
    onError: (e: Error) => toast.error(e.message),
  })
  const holds = holdsQ.data?.holds ?? []
  return (
    <div style={{ padding: '8px 12px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <Icon name={holds.length ? 'lock' : 'photo_camera'} size={14} style={{ color: holds.length ? 'var(--warning)' : 'var(--text-tertiary)' }} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }}>@{snap.snap_name}</span>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{snap.creation} · {snap.used}</span>
        <input value={tag} onChange={e => setTag(e.target.value)} placeholder="hold tag" className="input" style={{ width: 140, marginLeft: 'auto' }} />
        <button className="btn btn-xs btn-ghost" disabled={!tag.trim() || hold.isPending} onClick={() => hold.mutate()}>
          <Icon name="lock" size={12} /> Hold
        </button>
      </div>
      {holds.length > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginTop: 6 }}>
          {holds.map(h => (
            <span key={h.tag} className="badge" title={h.timestamp} style={{ display: 'inline-flex', alignItems: 'center', gap: 4 }}>
              {h.tag}
              <button className="btn btn-xs btn-ghost" style={{ padding: 0 }} title="Release" disabled={release.isPending} onClick={() => release.mutate(h.tag)}>
                <Icon name="close" size={12} />
              </button>
            </span>
          ))}
        </div>
      )}
    </div>
  )
}
