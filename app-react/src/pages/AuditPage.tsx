/**
 * pages/AuditPage.tsx - Audit Log
 *
 * Who did what, from where, and whether it worked. Read-only: the log is
 * trimmed and its chain is checked under Security Settings.
 *
 * Calls:
 *   GET /api/system/audit/logs?limit&offset&search&level → { success, logs: AuditLog[] }
 *   GET /api/system/audit/verify-chain                   → { success, valid, error? }
 */
import { useMemo, useState, useEffect } from 'react'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'

interface AuditLog {
  id: number
  timestamp: string
  actor: string
  action: string
  resource: string
  details: string
  ip_address: string
  success: boolean
}

interface AuditLogsResponse {
  success: boolean
  logs: AuditLog[]
}

const PAGE = 100

export function AuditPage() {
  const [typed, setTyped] = useState('')
  const [search, setSearch] = useState('')
  const [failuresOnly, setFailuresOnly] = useState(false)

  // Search as the user types, a moment after they stop.
  useEffect(() => {
    const t = setTimeout(() => setSearch(typed.trim()), 300)
    return () => clearTimeout(t)
  }, [typed])

  const logsQ = useInfiniteQuery({
    queryKey: ['audit', 'logs', search, failuresOnly],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) =>
      api.get<AuditLogsResponse>(
        `/api/system/audit/logs?limit=${PAGE}&offset=${pageParam}${search ? `&search=${encodeURIComponent(search)}` : ''}${failuresOnly ? '&level=error' : ''}`,
        signal),
    getNextPageParam: (last, pages) => ((last.logs ?? []).length === PAGE ? pages.length * PAGE : undefined),
    refetchInterval: 30_000,
  })
  const logs = useMemo(() => (logsQ.data?.pages ?? []).flatMap(p => p.logs ?? []), [logsQ.data])

  const chainQ = useQuery({
    queryKey: ['audit', 'chain'],
    queryFn: ({ signal }) => api.get<{ success: boolean; valid: boolean; error?: string }>('/api/system/audit/verify-chain', signal),
    staleTime: 60_000,
  })

  return (
    <div className="page-container">
      <header className="page-header" style={{ display: 'flex', alignItems: 'flex-end', gap: 16, flexWrap: 'wrap' }}>
        <div style={{ flex: 1, minWidth: 260 }}>
          <h1 className="page-title">Audit Log</h1>
          <p className="page-subtitle">Sign-ins and changes made through the web interface and the API: who, what, from which address, and whether it worked.</p>
        </div>
        {chainQ.data && (
          <span className={`badge ${chainQ.data.valid ? 'badge-success' : 'badge-error'}`}
            title={chainQ.data.valid
              ? 'Every entry carries a keyed hash over itself and the entry before it; all of them check out'
              : (chainQ.data.error || 'An entry was altered or removed outside the system')}>
            <Icon name={chainQ.data.valid ? 'verified' : 'gpp_bad'} size={14} />
            {chainQ.data.valid ? 'Chain intact' : 'Chain broken'}
          </span>
        )}
      </header>

      <div style={{ display: 'flex', gap: 12, alignItems: 'center', flexWrap: 'wrap', marginBottom: 12 }}>
        <input className="input" style={{ flex: 1, minWidth: 220, maxWidth: 420 }} aria-label="Search the audit log"
          placeholder="Search user, action, resource or details" value={typed} onChange={e => setTyped(e.target.value)} />
        <label style={{ display: 'flex', gap: 6, alignItems: 'center', fontSize: 'var(--text-sm)' }}>
          <input type="checkbox" checked={failuresOnly} onChange={e => setFailuresOnly(e.target.checked)} />
          Failures only
        </label>
      </div>

      <div className="card" style={{ padding: 0, overflow: 'auto' }}>
        {logsQ.isLoading ? (
          <div style={{ padding: 40 }}><Skeleton /></div>
        ) : logsQ.error ? (
          <div className="empty-state">
            <Icon name="error" className="empty-state-icon" style={{ color: 'var(--error)' }} />
            <p className="empty-state-title">The audit log could not be read</p>
            <p className="empty-state-body">{(logsQ.error as Error).message}</p>
          </div>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>Time</th>
                <th>User</th>
                <th>Action</th>
                <th>Resource</th>
                <th>Details</th>
                <th>Address</th>
                <th style={{ textAlign: 'right' }}>Result</th>
              </tr>
            </thead>
            <tbody>
              {logs.map(log => (
                <tr key={log.id}>
                  <td style={{ whiteSpace: 'nowrap', color: 'var(--text-secondary)', fontSize: 'var(--text-sm)' }}>
                    {new Date(log.timestamp).toLocaleString()}
                  </td>
                  <td>{log.actor || '-'}</td>
                  <td><code style={{ fontSize: 'var(--text-xs)' }}>{log.action}</code></td>
                  <td style={{ maxWidth: 240, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={log.resource}>
                    {log.resource}
                  </td>
                  <td style={{ maxWidth: 360, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--text-secondary)' }} title={log.details}>
                    {log.details}
                  </td>
                  <td style={{ whiteSpace: 'nowrap', color: 'var(--text-secondary)', fontSize: 'var(--text-sm)' }}>{log.ip_address}</td>
                  <td style={{ textAlign: 'right' }}>
                    <span className={`badge ${log.success ? 'badge-success' : 'badge-error'}`}>{log.success ? 'Success' : 'Failed'}</span>
                  </td>
                </tr>
              ))}
              {logs.length === 0 && (
                <tr>
                  <td colSpan={7} style={{ textAlign: 'center', padding: 60, color: 'var(--text-tertiary)' }}>
                    {search || failuresOnly ? 'No entries match.' : 'No entries yet.'}
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        )}
      </div>

      <footer style={{ marginTop: 16, display: 'flex', gap: 16, alignItems: 'center', flexWrap: 'wrap', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
        <span>{logs.length} {logs.length === 1 ? 'entry' : 'entries'} shown, newest first.</span>
        {logsQ.hasNextPage && (
          <button className="btn btn-secondary btn-sm" onClick={() => logsQ.fetchNextPage()} disabled={logsQ.isFetchingNextPage}>
            {logsQ.isFetchingNextPage ? 'Loading…' : 'Show older entries'}
          </button>
        )}
        <span style={{ marginLeft: 'auto' }}>Old entries are removed, and the chain is checked, under Security › Security Settings.</span>
      </footer>
    </div>
  )
}
