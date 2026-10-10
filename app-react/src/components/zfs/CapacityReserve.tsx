/**
 * CapacityReserve - pool fill level and the emergency reserve.
 *
 * A full ZFS pool cannot even delete files (deleting needs free space). The
 * reserve is a dataset holding back 2% of the pool; releasing it when the
 * pool is full gives room to clean up.
 *
 *   GET  /api/zfs/capacity                 → { pools: PoolCapacity[] }
 *   POST /api/zfs/capacity/reserve {pool}  → { success, reserve_human }
 *   POST /api/zfs/capacity/release {pool}  → { success, message }
 */
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'

interface PoolCapacity {
  pool: string; used_percent: number; used: string; free: string; total: string
  state: 'ok' | 'warning' | 'critical' | 'emergency'; reserved: string; has_reserve: boolean
}

function fmtBytes(raw: string): string {
  const b = Number(raw)
  if (!Number.isFinite(b) || b <= 0) return raw === 'none' ? 'none' : '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  const i = Math.min(Math.floor(Math.log(b) / Math.log(1024)), u.length - 1)
  return `${(b / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`
}

const STATE_COLOR: Record<string, string> = {
  ok: 'var(--success)', warning: 'var(--warning)', critical: 'var(--error)', emergency: 'var(--error)',
}

export function CapacityReserve() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const q = useQuery({
    queryKey: ['zfs', 'capacity'],
    queryFn: ({ signal }) => api.get<{ success: boolean; pools: PoolCapacity[] | null }>('/api/zfs/capacity', signal),
    refetchInterval: 60_000,
  })
  const done = () => qc.invalidateQueries({ queryKey: ['zfs'] })
  const reserve = useMutation({
    mutationFn: async (pool: string) => ensureOk(await api.post<{ success: boolean; reserve_human: string }>('/api/zfs/capacity/reserve', { pool })),
    onSuccess: (r, pool) => { toast.success(`${r.reserve_human} reserved on ${pool}`); done() },
    onError: (e: Error) => toast.error(e.message),
  })
  const release = useMutation({
    mutationFn: async (pool: string) => ensureOk(await api.post<{ success: boolean; message: string }>('/api/zfs/capacity/release', { pool })),
    onSuccess: r => { toast.success(r.message); done() },
    onError: (e: Error) => toast.error(e.message),
  })

  const pools = q.data?.pools ?? []
  if (pools.length === 0) return null

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 20, marginTop: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
        <Icon name="shield" size={18} style={{ color: 'var(--primary)' }} />
        <span style={{ fontWeight: 700 }}>Capacity and emergency reserve</span>
      </div>
      <p style={{ margin: '0 0 12px', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        A completely full pool cannot delete files. The reserve holds back 2% of a pool; release it when the pool is full to make room for cleaning up.
      </p>
      <div style={{ overflowX: 'auto' }}>
        <table className="table" style={{ width: '100%' }}>
          <thead><tr><th>Pool</th><th>Used</th><th>Free</th><th>State</th><th>Reserve</th><th /></tr></thead>
          <tbody>
            {pools.map(p => (
              <tr key={p.pool}>
                <td style={{ fontWeight: 600 }}>{p.pool}</td>
                <td>{p.used_percent}% <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>({fmtBytes(p.used)} of {fmtBytes(p.total)})</span></td>
                <td>{fmtBytes(p.free)}</td>
                <td><span style={{ color: STATE_COLOR[p.state] ?? 'var(--text-secondary)', fontWeight: 600, textTransform: 'capitalize' }}>{p.state}</span></td>
                <td>{p.has_reserve ? fmtBytes(p.reserved) : <span style={{ color: 'var(--text-tertiary)' }}>none</span>}</td>
                <td style={{ textAlign: 'right' }}>
                  {p.has_reserve ? (
                    <button className="btn btn-xs btn-ghost" disabled={release.isPending}
                      onClick={async () => {
                        if (await confirm({ title: `Release the reserve on ${p.pool}?`, message: 'The held-back space becomes available. Do this when the pool is full and you need room to delete or move data; set the reserve up again afterwards.', confirmLabel: 'Release' }))
                          release.mutate(p.pool)
                      }}>
                      <Icon name="lock_open" size={14} />Release
                    </button>
                  ) : (
                    <button className="btn btn-xs btn-ghost" disabled={reserve.isPending} onClick={() => reserve.mutate(p.pool)}>
                      <Icon name="lock" size={14} />Reserve 2%
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <ConfirmDialog />
    </div>
  )
}
