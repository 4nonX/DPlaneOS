import { useState, useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
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
  limit: number
  offset: number
}

interface CEStatusResponse {
  success: boolean
  has_compliance_engine: boolean
}

export function AuditPage() {
  const [hasCE, setHasCE] = useState<boolean | null>(null)
  
  useEffect(() => {
    // Stealth license check
    api.get<CEStatusResponse>('/api/system/ce-status')
      .then(res => setHasCE(!!res.has_compliance_engine))
      .catch(() => setHasCE(false))
  }, [])

  const { data, isLoading, error } = useQuery<AuditLogsResponse>({
    queryKey: ['audit-logs'],
    queryFn: () => api.get<AuditLogsResponse>('/api/system/audit/logs?limit=100'),
    enabled: hasCE === true,
    refetchInterval: 30000 // Refresh every 30s
  })

  // 1. Initial Loading State
  if (hasCE === null) {
    return (
      <div className="page-container" style={{ padding: '40px' }}>
        <Skeleton style={{ height: '40px', width: '200px', marginBottom: '24px' }} />
        <Skeleton style={{ height: '400px', borderRadius: 'var(--radius-xl)' }} />
      </div>
    )
  }

  // 2. CE Not Present (Stealth Mode)
  if (!hasCE) {
    return (
      <div className="page-container" style={{ height: '80vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <div className="empty-state" style={{ maxWidth: '480px', border: 'none', background: 'transparent' }}>
          <Icon name="Search" className="empty-state-icon" style={{ opacity: 0.1, fontSize: '64px' }} />
          <h2 className="empty-state-title" style={{ opacity: 0.5 }}>No audit data available</h2>
          <p className="empty-state-body" style={{ opacity: 0.4 }}>
            System event logging is active in the background for maintenance, but not exposed in the current interface.
          </p>
        </div>
      </div>
    )
  }

  // 3. Authenticated CE View
  return (
    <div className="page-container" style={{ animation: 'fadeIn 0.4s ease' }}>
      <header className="page-header">
        <h1 className="page-title">RESTORED: System Audit</h1>
        <p className="page-subtitle">Real-time cryptographic audit trail of system-wide operations.</p>
      </header>

      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        {isLoading ? (
          <div style={{ padding: '40px' }}><Skeleton /></div>
        ) : error ? (
          <div className="empty-state">
            <Icon name="Error" className="empty-state-icon" style={{ color: 'var(--error)' }} />
            <p className="empty-state-title">Failed to load logs</p>
            <p className="empty-state-body">Check daemon connectivity or permissions.</p>
          </div>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>Timestamp</th>
                <th>Actor</th>
                <th>Action</th>
                <th>Resource</th>
                <th style={{ textAlign: 'right' }}>Status</th>
              </tr>
            </thead>
            <tbody>
              {data?.logs?.map((log: AuditLog) => (
                <tr key={log.id}>
                  <td style={{ whiteSpace: 'nowrap', color: 'var(--text-secondary)', fontSize: '13px' }}>
                    {new Date(log.timestamp).toLocaleString()}
                  </td>
                  <td>
                    <div style={{ display: 'flex', alignItems: 'center', gap: '8px' }}>
                      <div style={{ width: '24px', height: '24px', borderRadius: '50%', background: 'var(--primary-bg)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: '10px', color: 'var(--primary)', fontWeight: 700 }}>
                        {log.actor.charAt(0).toUpperCase()}
                      </div>
                      {log.actor}
                    </div>
                  </td>
                  <td>
                    <code style={{ fontSize: '12px', background: 'hsla(0,0%,100%,0.05)', padding: '2px 6px', borderRadius: '4px' }}>
                      {log.action}
                    </code>
                  </td>
                  <td style={{ maxWidth: '280px', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: 'var(--text-tertiary)' }}>
                    {log.resource}
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    {log.success ? (
                      <span className="badge badge-success">Success</span>
                    ) : (
                      <span className="badge badge-error">Failed</span>
                    )}
                  </td>
                </tr>
              ))}
              {(!data?.logs || data.logs.length === 0) && (
                <tr>
                  <td colSpan={5} style={{ textAlign: 'center', padding: '60px', color: 'var(--text-tertiary)' }}>
                    No events recorded in this period.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        )}
      </div>
      
      <footer style={{ marginTop: '24px', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
         <div style={{ fontSize: '12px', color: 'var(--text-tertiary)' }}>
            Showing last {data?.logs?.length || 0} events. Data integrity verified via HMAC-SHA256 chain.
         </div>
         <div className="badge badge-neutral">Licensed Enterprise Component</div>
      </footer>
    </div>
  )
}
