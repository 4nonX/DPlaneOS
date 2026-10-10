/**
 * PoolIOPanel - pool I/O averages and an on-demand disk read test.
 *
 *   GET /api/zfs/iostat        → { stats: [{device, read_ops, write_ops, read_bw, write_bw, read_wait_ns, write_wait_ns}] }
 *   GET /api/zfs/disk-latency  → { overall, disks: [{device, read_mbs, latency_ms, state, error?}] }
 */
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'

interface IOStat { device: string; read_ops: number; write_ops: number; read_bw: number; write_bw: number; read_wait_ns: number; write_wait_ns: number }
interface DiskTest { device: string; read_mbs: number; latency_ms: number; state: 'ok' | 'slow' | 'zombie'; error?: string }

function rate(b: number): string {
  if (!b) return '0'
  const u = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  const i = Math.min(Math.floor(Math.log(b) / Math.log(1024)), u.length - 1)
  return `${(b / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`
}
const ms = (ns: number) => (ns ? `${(ns / 1e6).toFixed(ns < 1e7 ? 2 : 0)} ms` : '-')

const STATE: Record<string, { label: string; color: string }> = {
  ok: { label: 'OK', color: 'var(--success)' },
  slow: { label: 'Slow', color: 'var(--warning)' },
  zombie: { label: 'Not responding', color: 'var(--error)' },
}

export function PoolIOPanel() {
  const ioQ = useQuery({
    queryKey: ['zfs', 'iostat'],
    queryFn: ({ signal }) => api.get<{ success: boolean; error?: string; stats: IOStat[] }>('/api/zfs/iostat', signal),
    refetchInterval: 30_000,
  })
  // Reads from every pool disk for three seconds: only when asked.
  const testQ = useQuery({
    queryKey: ['zfs', 'disk-read-test'],
    queryFn: ({ signal }) => api.get<{ success: boolean; overall: string; disks: DiskTest[] | null }>('/api/zfs/disk-latency', signal),
    enabled: false,
  })
  const stats = ioQ.data?.stats ?? []
  if (!ioQ.data || (stats.length === 0 && !ioQ.data.error)) return null

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 20, marginTop: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12 }}>
        <Icon name="speed" size={18} style={{ color: 'var(--primary)' }} />
        <span style={{ fontWeight: 700 }}>Pool I/O</span>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>averages since boot</span>
        <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} disabled={testQ.isFetching} onClick={() => testQ.refetch()}
          title="Reads from every pool disk for three seconds and reports the speed">
          <Icon name="network_check" size={14} />{testQ.isFetching ? 'Testing disks…' : 'Test disk read speed'}
        </button>
      </div>
      {ioQ.data.error && <div style={{ color: 'var(--warning)', fontSize: 'var(--text-sm)' }}>{ioQ.data.error}</div>}
      {stats.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table className="table" style={{ width: '100%' }}>
            <thead><tr><th>Pool</th><th>Read</th><th>Write</th><th>Read ops/s</th><th>Write ops/s</th><th>Read wait</th><th>Write wait</th></tr></thead>
            <tbody>
              {stats.map(s => (
                <tr key={s.device}>
                  <td style={{ fontWeight: 600 }}>{s.device}</td>
                  <td>{rate(s.read_bw)}</td><td>{rate(s.write_bw)}</td>
                  <td>{s.read_ops}</td><td>{s.write_ops}</td>
                  <td>{ms(s.read_wait_ns)}</td><td>{ms(s.write_wait_ns)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {testQ.data && (
        <div style={{ marginTop: 14 }}>
          <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)', marginBottom: 6 }}>Disk read test</div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: 8 }}>
            {(testQ.data.disks ?? []).map(d => (
              <div key={d.device} style={{ border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', padding: '8px 10px', fontSize: 'var(--text-sm)' }}>
                <div style={{ fontFamily: 'var(--font-mono)', fontWeight: 600 }}>/dev/{d.device}</div>
                <div style={{ color: STATE[d.state]?.color }}>
                  {STATE[d.state]?.label ?? d.state}{d.read_mbs ? ` · ${d.read_mbs.toFixed(0)} MB/s` : ''}
                </div>
                {d.error && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{d.error}</div>}
              </div>
            ))}
            {(testQ.data.disks ?? []).length === 0 && <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No pool disks found.</span>}
          </div>
        </div>
      )}
    </div>
  )
}
