/**
 * components/zfs/RollbackModal.tsx
 *
 * Roll a dataset back to a snapshot with an explicit safety level.
 * Shared by PoolsPage and DatasetsPage.
 *
 *   GET  /api/zfs/snapshots?dataset=X
 *   POST /api/zfs/snapshots/rollback { snapshot, mode }
 *
 * Safety levels map to `zfs rollback` flags (see rollbackArgs in the daemon):
 *   safe           no flag  refuses if newer snapshots, bookmarks or clones exist
 *   destroy_newer  -r       destroys newer snapshots, refuses if any has clones
 *   destroy_clones -R       destroys newer snapshots and their clones
 */

import { useMemo, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { toast } from '@/hooks/useToast'

interface Snapshot { name: string; dataset: string; snap_name: string; used: string; refer: string; creation: string }

type RollbackMode = 'safe' | 'destroy_newer' | 'destroy_clones'

const MODES: { value: RollbackMode; label: string; detail: string }[] = [
  {
    value: 'safe',
    label: 'Stop if newer snapshots or clones exist',
    detail: 'Safest. Only works when the selected snapshot is the most recent one.',
  },
  {
    value: 'destroy_newer',
    label: 'Destroy newer snapshots, stop if they have clones',
    detail: 'Deletes every snapshot taken after the selected one (zfs rollback -r).',
  },
  {
    value: 'destroy_clones',
    label: 'Destroy newer snapshots and their clones',
    detail: 'No safety check. Also destroys datasets cloned from those snapshots (zfs rollback -R).',
  },
]

const PREVIEW_LIMIT = 8

export function RollbackModal({ dataset, onClose, onRollback }: {
  dataset: string
  onClose: () => void
  onRollback: () => void
}) {
  const [selectedSnap, setSelectedSnap] = useState('')
  const [mode, setMode] = useState<RollbackMode>('safe')
  const [acknowledged, setAcknowledged] = useState(false)

  const snapsQ = useQuery({
    queryKey: ['zfs', 'snapshots', dataset],
    queryFn: ({ signal }) => api.get<{ snapshots: Snapshot[] }>(`/api/zfs/snapshots?dataset=${encodeURIComponent(dataset)}`, signal),
  })

  // The API lists snapshots recursively, oldest first (zfs list -s creation).
  // Rollback only concerns this dataset's own snapshots; show newest first.
  const snapshots = useMemo(
    () => (snapsQ.data?.snapshots ?? []).filter(s => s.dataset === dataset).reverse(),
    [snapsQ.data, dataset],
  )
  const selectedIdx = snapshots.findIndex(s => s.snap_name === selectedSnap)
  const newer = selectedIdx > 0 ? snapshots.slice(0, selectedIdx) : []
  const destructive = mode !== 'safe' && newer.length > 0
  const safeWillFail = mode === 'safe' && newer.length > 0

  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/snapshots/rollback', { snapshot: `${dataset}@${selectedSnap}`, mode }),
    onSuccess: () => { toast.success(`${dataset} rolled back to @${selectedSnap}`); onRollback(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  function pickMode(m: RollbackMode) {
    setMode(m)
    setAcknowledged(false)
  }

  const canSubmit = !!selectedSnap && !safeWillFail && (!destructive || acknowledged) && !mutation.isPending

  return (
    <Modal title={<span style={{ color: 'var(--warning)' }}>Rollback Dataset</span>} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div className="alert alert-warning" style={{ fontSize: 'var(--text-xs)' }}>
          <Icon name="history" size={16} />
          <span>Rolling back discards all changes made to <strong>{dataset}</strong> after the selected snapshot. This cannot be undone.</span>
        </div>

        <label className="field">
          <span className="field-label">Snapshot to roll back to</span>
          {snapsQ.isLoading ? (
            <div style={{ padding: '8px 0', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>Loading snapshots…</div>
          ) : snapshots.length === 0 ? (
            <div style={{ padding: '8px 0', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No snapshots exist for this dataset.</div>
          ) : (
            <select className="input" value={selectedSnap} onChange={e => { setSelectedSnap(e.target.value); setAcknowledged(false) }}>
              <option value="">Select a snapshot…</option>
              {snapshots.map((s, i) => (
                <option key={s.name} value={s.snap_name}>
                  {s.snap_name} · {s.creation} ({s.refer}){i === 0 ? ' · latest' : ''}
                </option>
              ))}
            </select>
          )}
        </label>

        <div className="field">
          <span className="field-label">Safety level</span>
          <div role="radiogroup" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {MODES.map(m => (
              <label
                key={m.value}
                style={{
                  display: 'flex', gap: 10, alignItems: 'flex-start', cursor: 'pointer', padding: '10px 12px',
                  borderRadius: 'var(--radius-sm)',
                  border: `1px solid ${mode === m.value ? 'var(--primary)' : 'var(--border-subtle)'}`,
                  background: mode === m.value ? 'var(--primary-bg)' : 'transparent',
                }}
              >
                <input type="radio" name="rollback-mode" value={m.value} checked={mode === m.value} onChange={() => pickMode(m.value)} style={{ marginTop: 3 }} />
                <span>
                  <span style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 600 }}>{m.label}</span>
                  <span style={{ display: 'block', fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>{m.detail}</span>
                </span>
              </label>
            ))}
          </div>
        </div>

        {safeWillFail && (
          <div className="alert alert-info" style={{ fontSize: 'var(--text-xs)' }}>
            <Icon name="info" size={16} />
            <span>
              {newer.length} newer snapshot{newer.length === 1 ? '' : 's'} exist{newer.length === 1 ? 's' : ''} after @{selectedSnap}, so the safe level
              would be refused. Pick a level that destroys newer snapshots, or clone the snapshot to a new dataset instead.
            </span>
          </div>
        )}

        {destructive && (
          <div className="alert alert-error" style={{ fontSize: 'var(--text-xs)', flexDirection: 'column', alignItems: 'stretch', gap: 8 }}>
            <div style={{ display: 'flex', gap: 8 }}>
              <Icon name="warning" size={16} />
              <strong>
                {newer.length} snapshot{newer.length === 1 ? '' : 's'} will be destroyed
                {mode === 'destroy_clones' ? ', together with any datasets cloned from them' : ''}:
              </strong>
            </div>
            <ul style={{ margin: 0, paddingLeft: 24, fontFamily: 'var(--font-mono)' }}>
              {newer.slice(0, PREVIEW_LIMIT).map(s => <li key={s.name}>@{s.snap_name}</li>)}
              {newer.length > PREVIEW_LIMIT && <li>…and {newer.length - PREVIEW_LIMIT} more</li>}
            </ul>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
              <input type="checkbox" checked={acknowledged} onChange={e => setAcknowledged(e.target.checked)} />
              I understand these snapshots will be permanently deleted
            </label>
          </div>
        )}
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={!canSubmit} className={destructive ? 'btn btn-danger' : 'btn btn-primary'}>
          {mutation.isPending ? 'Rolling back…' : 'Rollback'}
        </button>
      </div>
    </Modal>
  )
}
