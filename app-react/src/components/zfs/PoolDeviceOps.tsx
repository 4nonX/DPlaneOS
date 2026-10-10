/**
 * PoolDeviceOps - device and pool operations from the topology view.
 *
 *   POST /api/zfs/pool/operations {pool, operation:'online', device}
 *   POST /api/zfs/pool/offline    {pool, device, temporary}
 *   POST /api/zfs/pool/detach     {pool, disk}
 *   POST /api/zfs/pool/remove-device {pool, device}   (cache, log, spare)
 *   POST /api/zfs/pool/attach     {pool, old_disk, new_disk}
 *   POST /api/zfs/pool/export     {pool, force}       (X-Confirm-Token)
 *   POST /api/zfs/pools/split     {pool, new_pool}
 */
import { useState, type ReactNode } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { api, apiFetch } from '@/lib/api'
import { issueConfirmToken } from '@/lib/confirm'
import { Modal } from '@/components/ui/Modal'
import { Icon } from '@/components/ui/Icon'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'
import type { VDev } from './PoolTopology'

interface DiskRow { name: string; model?: string; size?: string; by_id_path: string }
type Result = { success?: boolean; error?: string; output?: string }

function check(r: Result) {
  if (r && r.success === false) throw new Error(r.error || r.output || 'Operation failed')
  return r
}

export function usePoolDeviceOps(pool: string, onDone: () => void): { handle: (action: string, vdev: VDev) => boolean; element: ReactNode } {
  const { confirm, ConfirmDialog } = useConfirm()
  const [attachTo, setAttachTo] = useState<VDev | null>(null)

  const run = useMutation({
    mutationFn: async ({ path, body }: { path: string; body: Record<string, unknown> }) => check(await api.post<Result>(path, body)),
    onSuccess: () => { toast.success('Done'); onDone() },
    onError: (e: Error) => toast.error(e.message),
  })

  async function handle(action: string, v: VDev): Promise<void> {
    const dev = v.name
    switch (action) {
      case 'online':
        run.mutate({ path: '/api/zfs/pool/operations', body: { pool, operation: 'online', device: dev } })
        return
      case 'offline':
        if (await confirm({ title: `Take ${dev} offline?`, message: 'The pool keeps running on its redundancy; a pool without redundancy left cannot take this disk offline. It stays offline after a reboot until brought online.', confirmLabel: 'Take offline', danger: true }))
          run.mutate({ path: '/api/zfs/pool/offline', body: { pool, device: dev, temporary: false } })
        return
      case 'detach':
        if (await confirm({ title: `Detach ${dev}?`, message: 'The disk leaves its mirror (the mirror loses one copy). Its data is not erased.', confirmLabel: 'Detach', danger: true }))
          run.mutate({ path: '/api/zfs/pool/detach', body: { pool, disk: dev } })
        return
      case 'remove':
        if (await confirm({ title: `Remove ${dev} from ${pool}?`, message: 'Cache, log and spare devices can be removed without data loss.', confirmLabel: 'Remove', danger: true }))
          run.mutate({ path: '/api/zfs/pool/remove-device', body: { pool, device: dev } })
        return
      case 'attach':
        setAttachTo(v)
        return
    }
  }

  return {
    handle: (action, vdev) => {
      if (!['online', 'offline', 'detach', 'remove', 'attach'].includes(action)) return false
      void handle(action, vdev)
      return true
    },
    element: (
      <>
        <ConfirmDialog />
        {attachTo && <AttachDiskModal pool={pool} disk={attachTo.name} onClose={() => setAttachTo(null)} onDone={onDone} />}
      </>
    ),
  }
}

function AttachDiskModal({ pool, disk, onClose, onDone }: { pool: string; disk: string; onClose: () => void; onDone: () => void }) {
  const [newDisk, setNewDisk] = useState('')
  const disksQ = useQuery({
    queryKey: ['zfs', 'pool', 'replacement-disks'],
    queryFn: () => api.get<{ success: boolean; disks: DiskRow[] }>('/api/zfs/pool/replacement-disks'),
  })
  const attach = useMutation({
    mutationFn: async () => check(await api.post<Result>('/api/zfs/pool/attach', { pool, old_disk: disk, new_disk: newDisk })),
    onSuccess: () => { toast.success('Mirror disk attached: resilvering'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })
  const candidates = disksQ.data?.disks ?? []
  return (
    <Modal title={`Attach a mirror disk to ${disk}`} onClose={onClose}>
      <p style={{ margin: 0, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        The new disk becomes a mirror of <code>{disk}</code> (a single disk turns into a mirror, a mirror gets another copy). It must be at least as large.
      </p>
      <label className="field">
        <span className="field-label">New disk</span>
        <select value={newDisk} onChange={e => setNewDisk(e.target.value)} className="input">
          <option value="">-- Unassigned disks --</option>
          {candidates.map(d => <option key={d.by_id_path} value={d.by_id_path}>{d.model || d.name} · {d.size} · {d.by_id_path}</option>)}
        </select>
        {!disksQ.isLoading && candidates.length === 0 && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--error)' }}>No unassigned disks.</span>}
      </label>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => attach.mutate()} disabled={!newDisk || attach.isPending} className="btn btn-primary">
          {attach.isPending ? 'Attaching…' : 'Attach'}
        </button>
      </div>
    </Modal>
  )
}

/** Export and split for a whole pool. */
export function PoolLifecycleButtons({ pool, mirrorsOnly, onDone }: { pool: string; mirrorsOnly: boolean; onDone: () => void }) {
  const { confirm, ConfirmDialog } = useConfirm()
  const [split, setSplit] = useState(false)
  const [newPool, setNewPool] = useState(`${pool}-split`)

  const exportPool = useMutation({
    mutationFn: async () => {
      const token = await issueConfirmToken('pool_export', pool)
      return check(await apiFetch<Result>('/api/zfs/pool/export', { method: 'POST', body: { pool, force: false }, headers: { 'X-Confirm-Token': token } }))
    },
    onSuccess: () => { toast.success(`${pool} exported`); onDone() },
    onError: (e: Error) => toast.error(e.message),
  })
  const splitPool = useMutation({
    mutationFn: async () => check(await api.post<Result>('/api/zfs/pools/split', { pool, new_pool: newPool })),
    onSuccess: () => { toast.success(`Split off ${newPool}`); setSplit(false); onDone() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <>
      <button className="btn btn-xs btn-ghost" disabled={exportPool.isPending}
        onClick={async () => { if (await confirm({ title: `Export ${pool}?`, message: 'Its shares, apps and datasets go offline until the pool is imported again (here or on another machine).', confirmLabel: 'Export', danger: true, confirmText: pool })) exportPool.mutate() }}>
        <Icon name="eject" size={12} /> Export pool
      </button>
      {mirrorsOnly && (
        <button className="btn btn-xs btn-ghost" onClick={() => setSplit(true)}>
          <Icon name="call_split" size={12} /> Split mirror
        </button>
      )}
      {split && (
        <Modal title={`Split ${pool}`} onClose={() => setSplit(false)} size="sm">
          <p style={{ margin: 0, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            One disk of every mirror leaves the pool and becomes a new, exported pool with the same data (for example to take a copy offsite). {pool} keeps running with one copy less.
          </p>
          <label className="field">
            <span className="field-label">New pool name</span>
            <input value={newPool} onChange={e => setNewPool(e.target.value)} className="input" />
          </label>
          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
            <button onClick={() => setSplit(false)} className="btn btn-ghost">Cancel</button>
            <button onClick={() => splitPool.mutate()} disabled={!newPool || splitPool.isPending} className="btn btn-danger">
              {splitPool.isPending ? 'Splitting…' : 'Split'}
            </button>
          </div>
        </Modal>
      )}
      <ConfirmDialog />
    </>
  )
}
