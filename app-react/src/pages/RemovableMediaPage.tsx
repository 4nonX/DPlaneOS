/**
 * pages/RemovableMediaPage.tsx — Removable Media (Phase 4)
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/removable/list
 *   POST /api/removable/mount    → { path, mount_point }
 *   POST /api/removable/unmount  → { path }
 *   POST /api/removable/eject    → { path }
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { Modal } from '@/components/ui/Modal'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface RemovableDevice {
  name:        string      // e.g. "sdb"
  path:        string      // e.g. "/dev/sdb1"
  size:        string      // human-readable
  model?:      string
  vendor?:     string
  fstype?:     string
  label?:      string
  mounted:     boolean
  mount_point?: string
  readonly?:   boolean
}

interface DevicesResponse { success: boolean; devices: RemovableDevice[] }

// ---------------------------------------------------------------------------
// MountModal
// ---------------------------------------------------------------------------

function MountModal({ device, onClose, onDone }: { device: RemovableDevice; onClose: () => void; onDone: () => void }) {
  const devName = device.path.split('/').pop() ?? device.name
  const [mountPoint, setMountPoint] = useState(`/mnt/${devName}`)

  const mutation = useMutation({
    mutationFn: () => api.post('/api/removable/mount', { path: device.path, mount_point: mountPoint }),
    onSuccess: () => { toast.success(`Mounted at ${mountPoint}`); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={`Mount ${device.path}`} onClose={onClose} size="sm">
      <label className="field">
        <span className="field-label">Mount point</span>
        <input value={mountPoint} onChange={e => setMountPoint(e.target.value)} className="input" autoFocus
          onKeyDown={e => e.key === 'Enter' && mutation.mutate()} />
      </label>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending} className="btn btn-primary">
          <Icon name="folder_open" size={15} />{mutation.isPending ? 'Mounting…' : 'Mount'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// DeviceCard
// ---------------------------------------------------------------------------

function DeviceCard({ device, onRefresh }: { device: RemovableDevice; onRefresh: () => void }) {
  const [showMount, setShowMount] = useState(false)

  const unmount = useMutation({
    mutationFn: () => api.post('/api/removable/unmount', { path: device.path }),
    onSuccess: () => { toast.success('Unmounted'); onRefresh() },
    onError: (e: Error) => toast.error(e.message),
  })
  const eject = useMutation({
    mutationFn: () => api.post('/api/removable/eject', { path: device.path }),
    onSuccess: () => { toast.success('Ejected'); onRefresh() },
    onError: (e: Error) => toast.error(e.message),
  })

  const busy = unmount.isPending || eject.isPending

  return (
    <>
      <div style={{ background: 'var(--bg-card)', border: `1px solid ${device.mounted ? 'rgba(16,185,129,0.25)' : 'var(--border)'}`, borderRadius: 'var(--radius-xl)', padding: 24, borderLeft: `4px solid ${device.mounted ? 'var(--success)' : 'var(--border)'}` }}>
        <div style={{ display: 'flex', alignItems: 'flex-start', gap: 16, marginBottom: 16 }}>
          <div style={{ width: 48, height: 48, background: device.mounted ? 'var(--success-bg)' : 'var(--surface)', border: `1px solid ${device.mounted ? 'var(--success-border)' : 'var(--border)'}`, borderRadius: 'var(--radius-md)', display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
            <Icon name="usb" size={24} style={{ color: device.mounted ? 'var(--success)' : 'var(--text-tertiary)' }} />
          </div>
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)', marginBottom: 2 }}>
              {device.label || device.model || device.name}
            </div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{device.path}</div>
          </div>
          <span className={`badge ${device.mounted ? 'badge-success' : 'badge-neutral'}`}>
            {device.mounted ? 'MOUNTED' : 'NOT MOUNTED'}
          </span>
        </div>

        {/* Metadata */}
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 16 }}>
          {[
            { label: 'Size', value: device.size },
            device.fstype && { label: 'FS', value: device.fstype },
            device.vendor && { label: 'Vendor', value: device.vendor },
            device.model && { label: 'Model', value: device.model },
            device.mounted && device.mount_point && { label: 'Mount', value: device.mount_point },
            device.readonly && { label: 'Access', value: 'Read-only' },
          ].filter(Boolean).map((m, i) => (
            <span key={i} style={{ padding: '3px 10px', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
              {(m as { label: string; value: string }).label}: <strong>{(m as { label: string; value: string }).value}</strong>
            </span>
          ))}
        </div>

        {/* Actions */}
        <div style={{ display: 'flex', gap: 8 }}>
          {device.mounted ? (
            <>
              <button onClick={() => unmount.mutate()} disabled={busy} className="btn btn-ghost">
                <Icon name="eject" size={15} />{unmount.isPending ? 'Unmounting…' : 'Unmount'}
              </button>
              <button onClick={() => eject.mutate()} disabled={busy} className="btn btn-danger">
                <Icon name="logout" size={15} />{eject.isPending ? 'Ejecting…' : 'Eject'}
              </button>
            </>
          ) : (
            <button onClick={() => setShowMount(true)} className="btn btn-primary">
              <Icon name="folder_open" size={15} />Mount
            </button>
          )}
        </div>
      </div>

      {showMount && <MountModal device={device} onClose={() => setShowMount(false)} onDone={onRefresh} />}
    </>
  )
}

// ---------------------------------------------------------------------------
// RemovableMediaPage
// ---------------------------------------------------------------------------

export function RemovableMediaPage() {
  const qc = useQueryClient()

  const devicesQ = useQuery({
    queryKey: ['removable', 'list'],
    queryFn: ({ signal }) => api.get<DevicesResponse>('/api/removable/list', signal),
    refetchInterval: 10_000,
  })

  function refresh() { qc.invalidateQueries({ queryKey: ['removable', 'list'] }) }

  const devices = devicesQ.data?.devices ?? []
  const mounted = devices.filter(d => d.mounted)
  const unmounted = devices.filter(d => !d.mounted)

  return (
    <div style={{ maxWidth: 900 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 32 }}>
        <div>
          <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>Removable Media</h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>USB drives, external disks — mount, unmount, eject</p>
        </div>
        <button onClick={refresh} className="btn btn-ghost"><Icon name="refresh" size={15} />Refresh</button>
      </div>

      {devicesQ.isLoading && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {[0, 1].map(i => <Skeleton key={i} height={160} style={{ borderRadius: 'var(--radius-xl)' }} />)}
        </div>
      )}
      {devicesQ.isError && <ErrorState error={devicesQ.error} onRetry={refresh} />}

      {!devicesQ.isLoading && !devicesQ.isError && devices.length === 0 && (
        <div style={{ textAlign: 'center', padding: '80px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
          <Icon name="usb_off" size={56} style={{ opacity: 0.3, display: 'block', margin: '0 auto 16px' }} />
          <div style={{ fontSize: 'var(--text-xl)', fontWeight: 600, marginBottom: 8 }}>No removable devices detected</div>
          <div style={{ fontSize: 'var(--text-sm)' }}>Plug in a USB drive or external disk, then refresh</div>
        </div>
      )}

      {mounted.length > 0 && (
        <div style={{ marginBottom: 28 }}>
          <div style={{ fontSize: 'var(--text-xs)', fontWeight: 700, color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 12 }}>Mounted ({mounted.length})</div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            {mounted.map(d => <DeviceCard key={d.path} device={d} onRefresh={refresh} />)}
          </div>
        </div>
      )}

      {unmounted.length > 0 && (
        <div>
          <div style={{ fontSize: 'var(--text-xs)', fontWeight: 700, color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 12 }}>Not Mounted ({unmounted.length})</div>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            {unmounted.map(d => <DeviceCard key={d.path} device={d} onRefresh={refresh} />)}
          </div>
        </div>
      )}
    </div>
  )
}
