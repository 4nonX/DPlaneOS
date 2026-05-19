/**
 * pages/VolumesPage.tsx - ZFS Block Volumes (Zvols)
 *
 * GET    /api/zfs/volumes              -> { success, volumes: Zvol[] }
 * POST   /api/zfs/volumes              -> { name, size, blocksize?, sparse?, compression? }
 * DELETE /api/zfs/volumes              -> { name, force? }
 * POST   /api/zfs/volumes/resize       -> { name, size }
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { Modal } from '@/components/ui/Modal'

interface PoolsListResponse { success: boolean; pools?: { name: string }[]; data?: { name: string }[] }

interface Zvol {
  name: string
  used: string
  avail: string
  volsize: string
  volblocksize: string
  volmode: string
  compression: string
}

interface ZvolsResponse { success: boolean; volumes: Zvol[] }

function poolFrom(name: string) { return name.split('/')[0] }
function devicePath(name: string) { return `/dev/zvol/${name}` }

// ---------------------------------------------------------------------------
// CreateZvolModal
// ---------------------------------------------------------------------------

function CreateZvolModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const poolsQ = useQuery({
    queryKey: ['zfs', 'pools'],
    queryFn: ({ signal }) => api.get<PoolsListResponse>('/api/zfs/pools', signal),
  })
  const poolNames = (poolsQ.data?.pools ?? poolsQ.data?.data ?? []).map((p: { name: string }) => p.name)

  const [pool, setPool] = useState('')
  const [name, setName] = useState('')
  const [size, setSize] = useState('')
  const [blocksize, setBlocksize] = useState('8K')
  const [volmode, setVolmode] = useState('dev')
  const [sparse, setSparse] = useState(false)
  const [compression, setCompression] = useState('lz4')

  const create = useMutation({
    mutationFn: () => api.post('/api/zfs/volumes', {
      name: `${pool}/${name}`.replace(/\/+/g, '/'),
      size,
      blocksize,
      volmode,
      sparse,
      compression,
    }),
    onSuccess: () => { toast.success('Volume created'); onCreated(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  const fullName = pool && name ? `${pool}/${name}` : pool ? `${pool}/` : ''

  return (
    <Modal title="Create Block Volume" onClose={onClose} size="sm">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, padding: '4px 0' }}>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
          <label className="field">
            <span className="field-label">Pool</span>
            {poolNames.length > 0 ? (
              <select value={pool} onChange={e => setPool(e.target.value)} className="input" style={{ fontFamily: 'var(--font-mono)' }}>
                <option value="">Select pool...</option>
                {poolNames.map((p: string) => <option key={p} value={p}>{p}</option>)}
              </select>
            ) : (
              <input value={pool} onChange={e => setPool(e.target.value)} placeholder="tank" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
            )}
          </label>
          <label className="field">
            <span className="field-label">Volume name</span>
            <input value={name} onChange={e => setName(e.target.value)} placeholder="vm-disk0" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
          </label>
        </div>

        {fullName && (
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', padding: '6px 10px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)' }}>
            {fullName}
          </div>
        )}

        <label className="field">
          <span className="field-label">Size (e.g. 10G, 500M)</span>
          <input value={size} onChange={e => setSize(e.target.value)} placeholder="10G" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </label>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12 }}>
          <label className="field">
            <span className="field-label">Block size</span>
            <select value={blocksize} onChange={e => setBlocksize(e.target.value)} className="input">
              {['512', '1K', '2K', '4K', '8K', '16K', '32K', '64K', '128K'].map(v => (
                <option key={v} value={v}>{v}{v === '8K' ? ' (default)' : ''}</option>
              ))}
            </select>
          </label>
          <label className="field">
            <span className="field-label">Compression</span>
            <select value={compression} onChange={e => setCompression(e.target.value)} className="input">
              <option value="off">Off</option>
              <option value="lz4">LZ4 (default)</option>
              <option value="zstd">Zstd</option>
              <option value="gzip">gzip</option>
              <option value="lzjb">lzjb</option>
            </select>
          </label>
          <label className="field">
            <span className="field-label">Vol mode</span>
            <select value={volmode} onChange={e => setVolmode(e.target.value)} className="input">
              <option value="dev">dev (default)</option>
              <option value="geom">geom</option>
              <option value="none">none</option>
              <option value="default">default</option>
            </select>
          </label>
        </div>

        <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer', fontSize: 'var(--text-sm)' }}>
          <input type="checkbox" checked={sparse} onChange={e => setSparse(e.target.checked)} style={{ width: 15, height: 15, accentColor: 'var(--primary)' }} />
          <span>Sparse (thin-provisioned) - do not reserve space immediately</span>
        </label>

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 4 }}>
          <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button
            onClick={() => create.mutate()}
            disabled={!pool.trim() || !name.trim() || !size.trim() || create.isPending}
            className="btn btn-primary"
          >
            <Icon name="add_circle" size={15} />
            {create.isPending ? 'Creating...' : 'Create'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ResizeModal
// ---------------------------------------------------------------------------

function ResizeModal({ zvol, onClose, onDone }: { zvol: Zvol; onClose: () => void; onDone: () => void }) {
  const [size, setSize] = useState(zvol.volsize)

  const resize = useMutation({
    mutationFn: () => api.post('/api/zfs/volumes/resize', { name: zvol.name, size }),
    onSuccess: () => { toast.success('Volume resized'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={`Resize: ${zvol.name}`} onClose={onClose} size="sm">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, padding: '4px 0' }}>
        <div className="alert alert-warning">
          <Icon name="warning" size={16} style={{ flexShrink: 0 }} />
          ZFS block volumes can only be grown, not shrunk. Shrinking may corrupt data.
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
          <div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 4 }}>Current size</div>
            <div style={{ fontFamily: 'var(--font-mono)', fontWeight: 700 }}>{zvol.volsize}</div>
          </div>
          <div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 4 }}>Used</div>
            <div style={{ fontFamily: 'var(--font-mono)', fontWeight: 700 }}>{zvol.used}</div>
          </div>
        </div>
        <label className="field">
          <span className="field-label">New size (must be larger)</span>
          <input value={size} onChange={e => setSize(e.target.value)} className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </label>
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button onClick={() => resize.mutate()} disabled={!size.trim() || resize.isPending} className="btn btn-primary">
            <Icon name="open_in_full" size={15} />
            {resize.isPending ? 'Resizing...' : 'Resize'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// DeleteModal
// ---------------------------------------------------------------------------

function DeleteModal({ zvol, onClose, onDone }: { zvol: Zvol; onClose: () => void; onDone: () => void }) {
  const [confirm, setConfirm] = useState('')
  const [force, setForce] = useState(false)

  const del = useMutation({
    mutationFn: () => api.delete('/api/zfs/volumes', { name: zvol.name, force }),
    onSuccess: () => { toast.success('Volume destroyed'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title="Destroy Volume" onClose={onClose} size="sm">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, padding: '4px 0' }}>
        <div className="alert alert-error">
          <Icon name="warning" size={16} style={{ flexShrink: 0 }} />
          This will permanently destroy <strong>{zvol.name}</strong> and all data on it. This cannot be undone.
        </div>
        <label className="field">
          <span className="field-label">Type the volume name to confirm</span>
          <input value={confirm} onChange={e => setConfirm(e.target.value)} placeholder={zvol.name} className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </label>
        <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer', fontSize: 'var(--text-sm)' }}>
          <input type="checkbox" checked={force} onChange={e => setForce(e.target.checked)} style={{ width: 15, height: 15, accentColor: 'var(--error)' }} />
          <span>Force destroy (unmount dependents)</span>
        </label>
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button
            onClick={() => del.mutate()}
            disabled={confirm !== zvol.name || del.isPending}
            className="btn btn-danger"
          >
            <Icon name="delete_forever" size={15} />
            {del.isPending ? 'Destroying...' : 'Destroy'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ZvolRow
// ---------------------------------------------------------------------------

function ZvolRow({ zvol, onRefresh }: { zvol: Zvol; onRefresh: () => void }) {
  const [resizing, setResizing] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [showPath, setShowPath] = useState(false)
  const devPath = devicePath(zvol.name)

  return (
    <>
      <div style={{
        display: 'grid',
        gridTemplateColumns: '2fr 1fr 1fr 1fr 1fr 1fr 60px',
        gap: 12,
        padding: '12px 16px',
        alignItems: 'center',
        borderBottom: '1px solid var(--border)',
        fontSize: 'var(--text-sm)',
      }}>
        <div>
          <div style={{ fontFamily: 'var(--font-mono)', fontWeight: 600 }}>{zvol.name}</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>
            <span
              style={{ cursor: 'pointer', textDecoration: 'underline dotted' }}
              onClick={() => setShowPath(v => !v)}
            >
              {showPath ? devPath : poolFrom(zvol.name)}
            </span>
          </div>
        </div>
        <div style={{ fontFamily: 'var(--font-mono)' }}>{zvol.volsize}</div>
        <div style={{ fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>{zvol.used}</div>
        <div style={{ fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>{zvol.volblocksize}</div>
        <div>
          <span style={{
            fontSize: 'var(--text-xs)', padding: '2px 8px',
            borderRadius: 'var(--radius-sm)',
            background: zvol.compression !== 'off' ? 'var(--primary-bg)' : 'var(--surface)',
            color: zvol.compression !== 'off' ? 'var(--primary)' : 'var(--text-tertiary)',
            fontFamily: 'var(--font-mono)',
          }}>
            {zvol.compression}
          </span>
        </div>
        <div>
          <span style={{
            fontSize: 'var(--text-xs)', padding: '2px 8px',
            borderRadius: 'var(--radius-sm)',
            background: 'var(--surface)',
            color: 'var(--text-secondary)',
            fontFamily: 'var(--font-mono)',
          }}>
            {zvol.volmode}
          </span>
        </div>
        <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
          <button
            onClick={() => setResizing(true)}
            className="btn btn-ghost btn-sm"
            title="Resize"
            style={{ padding: '4px 6px' }}
          >
            <Icon name="open_in_full" size={14} />
          </button>
          <button
            onClick={() => setDeleting(true)}
            className="btn btn-ghost btn-sm"
            title="Destroy"
            style={{ padding: '4px 6px', color: 'var(--error)' }}
          >
            <Icon name="delete" size={14} />
          </button>
        </div>
      </div>
      {resizing && <ResizeModal zvol={zvol} onClose={() => setResizing(false)} onDone={onRefresh} />}
      {deleting && <DeleteModal zvol={zvol} onClose={() => setDeleting(false)} onDone={onRefresh} />}
    </>
  )
}

// ---------------------------------------------------------------------------
// VolumesPage
// ---------------------------------------------------------------------------

export function VolumesPage() {
  const [createOpen, setCreateOpen] = useState(false)
  const qc = useQueryClient()

  const zvolsQ = useQuery({
    queryKey: ['zfs', 'volumes'],
    queryFn: ({ signal }) => api.get<ZvolsResponse>('/api/zfs/volumes', signal),
    refetchInterval: 30_000,
  })

  const volumes = zvolsQ.data?.volumes ?? []

  function refresh() { qc.invalidateQueries({ queryKey: ['zfs', 'volumes'] }) }

  return (
    <div style={{ maxWidth: 1100 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 32 }}>
        <div>
          <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>Block Volumes</h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>ZFS zvols - block devices backed by ZFS</p>
        </div>
        <div style={{ display: 'flex', gap: 10 }}>
          <button onClick={() => setCreateOpen(true)} className="btn btn-primary">
            <Icon name="add_circle" size={18} /> New Volume
          </button>
          <button onClick={refresh} className="btn btn-ghost" title="Refresh">
            <Icon name="refresh" size={16} />
          </button>
        </div>
      </div>

      <div className="alert alert-info" style={{ marginBottom: 20 }}>
        <Icon name="info" size={16} style={{ flexShrink: 0 }} />
        Block volumes appear as <code style={{ fontFamily: 'var(--font-mono)', fontSize: 'inherit' }}>/dev/zvol/&lt;pool&gt;/&lt;name&gt;</code>. Use them with iSCSI, NVMe-oF, or directly as VM disk images.
      </div>

      {zvolsQ.isLoading && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {[0, 1, 2].map(i => <Skeleton key={i} height={52} style={{ borderRadius: 'var(--radius-sm)' }} />)}
        </div>
      )}
      {zvolsQ.isError && <ErrorState error={zvolsQ.error} onRetry={refresh} />}

      {!zvolsQ.isLoading && !zvolsQ.isError && (
        <div className="card" style={{ borderRadius: 'var(--radius-xl)', overflow: 'hidden', padding: 0 }}>
          {volumes.length === 0 ? (
            <div style={{ textAlign: 'center', padding: '64px 24px', color: 'var(--text-tertiary)' }}>
              <Icon name="developer_board" size={48} style={{ opacity: 0.25, display: 'block', margin: '0 auto 14px' }} />
              <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600, marginBottom: 8 }}>No block volumes</div>
              <p style={{ fontSize: 'var(--text-sm)', marginBottom: 20 }}>Create a zvol to use as an iSCSI target or VM disk image.</p>
              <button onClick={() => setCreateOpen(true)} className="btn btn-primary">
                <Icon name="add_circle" size={16} /> Create First Volume
              </button>
            </div>
          ) : (
            <>
              <div style={{
                display: 'grid',
                gridTemplateColumns: '2fr 1fr 1fr 1fr 1fr 1fr 60px',
                gap: 12,
                padding: '10px 16px',
                fontSize: 'var(--text-xs)',
                fontWeight: 700,
                textTransform: 'uppercase',
                letterSpacing: '0.05em',
                color: 'var(--text-tertiary)',
                borderBottom: '1px solid var(--border)',
                background: 'var(--surface)',
              }}>
                <div>Name / Pool</div>
                <div>Size</div>
                <div>Used</div>
                <div>Block Size</div>
                <div>Compression</div>
                <div>Mode</div>
                <div />
              </div>
              {volumes.map(v => (
                <ZvolRow key={v.name} zvol={v} onRefresh={refresh} />
              ))}
            </>
          )}
        </div>
      )}

      {createOpen && <CreateZvolModal onClose={() => setCreateOpen(false)} onCreated={refresh} />}
    </div>
  )
}
