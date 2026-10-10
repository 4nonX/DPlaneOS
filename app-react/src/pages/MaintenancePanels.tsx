/**
 * MaintenancePanels - storage operation records and interrupted-operation
 * locks (Support & Diagnostics page).
 *
 * Calls:
 *   GET    /api/storage/operations?limit=   → { operations: Record[] }
 *   DELETE /api/storage/operations/{id}     → clear a stuck pending operation
 *   GET    /api/system/stale-locks          → { stale_locks: [{dataset, operation}] }
 *   POST   /api/system/stale-locks/clear    { dataset }
 */
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'
import { fmtDateTime } from '@/lib/fmt'

interface StorageOp {
  id: number; operation_type: string; target: string
  state: 'pending' | 'committed' | 'failed'; error?: string
  started_at: string; completed_at?: string
}

export function MaintenancePanels() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()

  const opsQ = useQuery({
    queryKey: ['storage', 'operations'],
    queryFn: ({ signal }) => api.get<{ operations: StorageOp[] | null }>('/api/storage/operations?limit=50', signal),
    refetchInterval: 15_000,
  })
  const locksQ = useQuery({
    queryKey: ['system', 'stale-locks'],
    queryFn: ({ signal }) => api.get<{ success: boolean; stale_locks: { dataset: string; operation: string }[] | null }>('/api/system/stale-locks', signal),
  })

  const clearOp = useMutation({
    mutationFn: (id: number) => api.delete(`/api/storage/operations/${id}`),
    onSuccess: () => { toast.success('Operation cleared'); qc.invalidateQueries({ queryKey: ['storage', 'operations'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const clearLock = useMutation({
    mutationFn: (dataset: string) => api.post<{ success: boolean; error?: string }>('/api/system/stale-locks/clear', { dataset }),
    onSuccess: (r) => { if (r.success) toast.success('Lock cleared'); else toast.error(r.error ?? 'Failed'); qc.invalidateQueries({ queryKey: ['system', 'stale-locks'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const ops = opsQ.data?.operations ?? []
  const locks = locksQ.data?.stale_locks ?? []

  return (
    <>
      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
          <Icon name="pending_actions" size={18} style={{ color: 'var(--primary)' }} />
          <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>Storage operations</span>
        </div>
        <p style={{ margin: '0 0 12px', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          One operation runs per pool or disk at a time. An operation left pending (for example by a crash) blocks new ones on its target until it is cleared.
        </p>
        {opsQ.isLoading ? <Skeleton height={120} /> : (
          <div style={{ maxHeight: 340, overflowY: 'auto' }}>
            <table className="table" style={{ width: '100%' }}>
              <thead><tr><th>Started</th><th>Operation</th><th>Target</th><th>State</th><th /></tr></thead>
              <tbody>
                {ops.map(op => (
                  <tr key={op.id}>
                    <td style={{ whiteSpace: 'nowrap' }}>{fmtDateTime(op.started_at)}</td>
                    <td>{op.operation_type}</td>
                    <td style={{ fontFamily: 'var(--font-mono)' }}>{op.target}</td>
                    <td title={op.error} style={{ color: op.state === 'failed' ? 'var(--error)' : op.state === 'pending' ? 'var(--warning)' : 'var(--success)' }}>
                      {op.state}{op.error ? ` - ${op.error}` : ''}
                    </td>
                    <td>
                      {op.state === 'pending' && (
                        <button className="btn btn-ghost btn-sm" disabled={clearOp.isPending}
                          onClick={async () => { if (await confirm({ title: 'Clear this operation?', message: `Only if nothing is running on ${op.target} any more: it is marked failed and new operations can start.`, confirmLabel: 'Clear', danger: true })) clearOp.mutate(op.id) }}>
                          Clear
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
                {ops.length === 0 && <tr><td colSpan={5} style={{ textAlign: 'center', color: 'var(--text-tertiary)' }}>No operations recorded</td></tr>}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
          <Icon name="lock_clock" size={18} style={{ color: locks.length ? 'var(--warning)' : 'var(--primary)' }} />
          <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>Interrupted operations</span>
        </div>
        {locksQ.isLoading ? <Skeleton height={60} /> : locks.length === 0 ? (
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>No dataset operation was interrupted.</div>
        ) : (
          <>
            <p style={{ margin: '0 0 12px', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
              These datasets had an operation interrupted (power loss, crash). Check the dataset, then clear the lock.
            </p>
            {locks.map(l => (
              <div key={l.dataset} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '6px 0' }}>
                <span style={{ fontFamily: 'var(--font-mono)' }}>{l.dataset}</span>
                <span style={{ color: 'var(--text-tertiary)' }}>{l.operation}</span>
                <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} disabled={clearLock.isPending} onClick={() => clearLock.mutate(l.dataset)}>Clear lock</button>
              </div>
            ))}
          </>
        )}
      </div>
      <ConfirmDialog />
    </>
  )
}
