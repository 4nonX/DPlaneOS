/**
 * components/zfs/ImportPoolModal.tsx
 *
 * Manually import an exported or foreign ZFS pool.
 *
 *   GET  /api/zfs/pool/importable  → { pools: ImportablePool[] }
 *   POST /api/zfs/pool/import      { guid, new_name?, force? }
 *
 * Import is always by GUID, so two pools with the same name (e.g. two
 * "tank" pools from different machines) can be told apart and renamed.
 */

import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { toast } from '@/hooks/useToast'

interface ImportablePool {
  name: string
  guid: string
  state: string
  status?: string
  action?: string
  config?: string
  needs_force: boolean
}

const POOL_NAME_RE = /^[a-zA-Z][a-zA-Z0-9_.-]{0,254}$/

const stateColor = (s: string) =>
  s === 'ONLINE' ? 'var(--success)' : s === 'DEGRADED' ? 'var(--warning)' : 'var(--error)'

export function ImportPoolModal({ existingPools, onClose, onImported }: {
  existingPools: string[]
  onClose: () => void
  onImported: () => void
}) {
  const [guid, setGuid] = useState('')
  const [newName, setNewName] = useState('')
  const [force, setForce] = useState(false)
  const [showConfig, setShowConfig] = useState(false)

  const scanQ = useQuery({
    queryKey: ['zfs', 'pool', 'importable'],
    queryFn: ({ signal }) => api.get<{ pools: ImportablePool[] }>('/api/zfs/pool/importable', signal),
    staleTime: 0,
  })
  const pools = scanQ.data?.pools ?? []
  const selected = pools.find(p => p.guid === guid)

  const finalName = newName.trim() || selected?.name || ''
  const nameClash = !!selected && existingPools.includes(finalName)
  const nameInvalid = newName.trim() !== '' && !POOL_NAME_RE.test(newName.trim())
  const duplicateNames = new Set(pools.map(p => p.name)).size !== pools.length

  const mutation = useMutation({
    mutationFn: () => api.post('/api/zfs/pool/import', { guid, new_name: newName.trim() || undefined, force }),
    onSuccess: () => { toast.success(`Pool ${finalName} imported`); onImported(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  function select(p: ImportablePool) {
    setGuid(p.guid)
    setNewName('')
    setForce(false)
    setShowConfig(false)
  }

  const canSubmit = !!selected && !nameClash && !nameInvalid && (!selected.needs_force || force) && !mutation.isPending

  return (
    <Modal title="Import Pool" onClose={onClose} size="lg">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, maxHeight: '70vh', overflowY: 'auto', paddingRight: 4 }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            Pools found on attached disks that are not imported on this system.
          </span>
          <button className="btn btn-ghost btn-sm" onClick={() => scanQ.refetch()} disabled={scanQ.isFetching}>
            <Icon name="refresh" size={14} /> {scanQ.isFetching ? 'Scanning…' : 'Rescan'}
          </button>
        </div>

        {scanQ.isLoading ? (
          <div style={{ padding: 24, textAlign: 'center', color: 'var(--text-tertiary)' }}>Scanning disks…</div>
        ) : scanQ.isError ? (
          <div className="alert alert-error" style={{ fontSize: 'var(--text-xs)' }}>
            <Icon name="error" size={16} /><span>{(scanQ.error as Error).message}</span>
          </div>
        ) : pools.length === 0 ? (
          <div style={{ padding: '32px 16px', textAlign: 'center', color: 'var(--text-tertiary)', border: '1px dashed var(--border)', borderRadius: 'var(--radius-md)' }}>
            <Icon name="search_off" size={32} style={{ opacity: 0.4, display: 'block', margin: '0 auto 8px' }} />
            No importable pools found. Pools that are already imported, or whose disks are not attached, do not appear here.
          </div>
        ) : (
          <div role="radiogroup" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
            {duplicateNames && (
              <div className="alert alert-info" style={{ fontSize: 'var(--text-xs)' }}>
                <Icon name="info" size={16} /><span>Several pools share a name. They are listed by GUID; rename on import to keep them apart.</span>
              </div>
            )}
            {pools.map(p => (
              <label
                key={p.guid}
                style={{
                  display: 'flex', gap: 10, alignItems: 'flex-start', cursor: 'pointer', padding: '10px 12px',
                  borderRadius: 'var(--radius-sm)',
                  border: `1px solid ${guid === p.guid ? 'var(--primary)' : 'var(--border-subtle)'}`,
                  background: guid === p.guid ? 'var(--primary-bg)' : 'transparent',
                }}
              >
                <input type="radio" name="import-pool" checked={guid === p.guid} onChange={() => select(p)} style={{ marginTop: 3 }} />
                <span style={{ flex: 1, minWidth: 0 }}>
                  <span style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                    <strong style={{ fontFamily: 'var(--font-mono)' }}>{p.name}</strong>
                    <span style={{ fontSize: 'var(--text-2xs)', fontWeight: 700, color: stateColor(p.state) }}>{p.state}</span>
                    {p.needs_force && <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--warning)' }}>used by another system</span>}
                    {existingPools.includes(p.name) && <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--warning)' }}>name in use here</span>}
                  </span>
                  <span style={{ display: 'block', fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', marginTop: 2 }}>GUID {p.guid}</span>
                  {p.status && <span style={{ display: 'block', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 4 }}>{p.status}</span>}
                </span>
              </label>
            ))}
          </div>
        )}

        {selected && (
          <>
            {selected.config && (
              <div>
                <button type="button" className="btn btn-ghost btn-sm" onClick={() => setShowConfig(s => !s)}>
                  <Icon name={showConfig ? 'expand_less' : 'expand_more'} size={14} /> {showConfig ? 'Hide' : 'Show'} disk layout
                </button>
                {showConfig && (
                  <pre style={{ margin: '8px 0 0', padding: 12, background: 'var(--bg-elevated)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-2xs)', overflowX: 'auto' }}>{selected.config}</pre>
                )}
              </div>
            )}

            <label className="field">
              <span className="field-label">Import as (optional new name)</span>
              <input className="input" value={newName} onChange={e => setNewName(e.target.value)} placeholder={selected.name} style={{ fontFamily: 'var(--font-mono)' }} />
              {nameInvalid && <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--error)', marginTop: 4 }}>Start with a letter; letters, numbers, _ - . only.</span>}
              {nameClash && <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--error)', marginTop: 4 }}>A pool named {finalName} already exists. Choose another name.</span>}
            </label>

            {selected.state !== 'ONLINE' && (
              <div className="alert alert-warning" style={{ fontSize: 'var(--text-xs)' }}>
                <Icon name="warning" size={16} /><span>{selected.action || `This pool is ${selected.state}. Importing it may reduce redundancy until missing disks are replaced.`}</span>
              </div>
            )}

            {selected.needs_force && (
              <div className="alert alert-error" style={{ fontSize: 'var(--text-xs)', flexDirection: 'column', alignItems: 'stretch', gap: 8 }}>
                <div style={{ display: 'flex', gap: 8 }}>
                  <Icon name="warning" size={16} />
                  <span>This pool was last used by another system. If that system still has it imported, importing it here too will corrupt it.</span>
                </div>
                <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
                  <input type="checkbox" checked={force} onChange={e => setForce(e.target.checked)} />
                  The other system is powered off or has exported this pool. Force the import.
                </label>
              </div>
            )}
          </>
        )}
      </div>
      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" disabled={!canSubmit} onClick={() => mutation.mutate()}>
          {mutation.isPending ? 'Importing…' : 'Import Pool'}
        </button>
      </div>
    </Modal>
  )
}
