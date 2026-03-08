/**
 * pages/AuditPage.tsx — Audit Log (Phase 8)
 *
 * GET  /api/system/logs               → { success, logs: AuditEntry[] }
 * GET  /api/system/audit/stats        → { success, total_entries, last_entry, chain_valid }
 * GET  /api/system/audit/verify-chain → { success, valid: bool }
 * POST /api/system/audit/rotate       → rotate logs
 */

import { useState, useMemo } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

interface AuditEntry { timestamp: string; username?: string; action: string; details?: string; ip_address?: string }
interface LogsResponse { success: boolean; logs: AuditEntry[] }
interface AuditStats  { success: boolean; total_entries?: number; last_entry?: string; chain_valid?: boolean }

function actionColor(action: string): string {
  const a = action.toLowerCase()
  if (a.includes('delete') || a.includes('destroy') || a.includes('fail')) return 'var(--error)'
  if (a.includes('create') || a.includes('add') || a.includes('success')) return 'var(--success)'
  if (a.includes('login') || a.includes('logout') || a.includes('auth'))  return 'var(--primary)'
  if (a.includes('update') || a.includes('edit') || a.includes('modify')) return 'var(--warning)'
  return 'var(--text-secondary)'
}

function fmtDate(s?: string) {
  if (!s) return '—'
  try { return new Date(s).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'medium' }) }
  catch { return s }
}

export function AuditPage() {
  const qc = useQueryClient()
  const [filterUser,   setFilterUser]   = useState('')
  const [filterAction, setFilterAction] = useState('')
  const [filterDays,   setFilterDays]   = useState('0')

  const logsQ  = useQuery({ queryKey:['audit','logs'],  queryFn:({signal})=>api.get<LogsResponse>('/api/system/logs',signal) })
  const statsQ = useQuery({ queryKey:['audit','stats'], queryFn:({signal})=>api.get<AuditStats>('/api/system/audit/stats',signal) })
  const chainQ = useQuery({ queryKey:['audit','chain'], queryFn:({signal})=>api.get<{success:boolean;valid:boolean}>('/api/system/audit/verify-chain',signal) })

  const rotate = useMutation({
    mutationFn: () => api.post('/api/system/audit/rotate', {}),
    onSuccess: () => { toast.success('Audit logs rotated'); qc.invalidateQueries({ queryKey:['audit'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const allLogs = logsQ.data?.logs ?? []

  // Unique users for filter dropdown
  const users = useMemo(() => [...new Set(allLogs.map(l => l.username).filter(Boolean))].sort(), [allLogs])

  // Client-side filtering
  const filtered = useMemo(() => {
    let out = allLogs
    if (filterUser)   out = out.filter(l => l.username === filterUser)
    if (filterAction) out = out.filter(l => l.action.toLowerCase().includes(filterAction.toLowerCase()))
    if (Number(filterDays) > 0) {
      const cutoff = new Date()
      cutoff.setDate(cutoff.getDate() - Number(filterDays))
      out = out.filter(l => new Date(l.timestamp) >= cutoff)
    }
    return out.slice(0, 200)
  }, [allLogs, filterUser, filterAction, filterDays])

  // CSV export
  function exportCSV() {
    const rows = ['Timestamp,User,Action,Details,IP Address', ...filtered.map(l =>
      `"${l.timestamp}","${l.username||'System'}","${l.action}","${(l.details||'').replace(/"/g,'""')}","${l.ip_address||''}"`
    )]
    const blob = new Blob([rows.join('\n')], { type:'text/csv' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `audit-${new Date().toISOString().slice(0,10)}.csv`
    a.click()
  }

  const chainOk = chainQ.data?.valid !== false
  const stats = statsQ.data

  return (
    <div style={{ maxWidth: 1100 }}>
      <div style={{ marginBottom: 28 }}>
        <h1 style={{ fontSize:'var(--text-3xl)', fontWeight:700, letterSpacing:'-1px', marginBottom:6 }}>Audit Log</h1>
        <p style={{ color:'var(--text-secondary)' }}>Immutable tamper-evident log of all system actions</p>
      </div>

      {/* Stats row */}
      <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fit,minmax(180px,1fr))', gap:12, marginBottom:24 }}>
        <div style={{ background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-lg)', padding:'16px 20px' }}>
          <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', textTransform:'uppercase', letterSpacing:'0.5px', marginBottom:6 }}>Total Entries</div>
          <div style={{ fontSize:26, fontWeight:700, fontFamily:'var(--font-mono)', color:'var(--primary)' }}>{stats?.total_entries?.toLocaleString() ?? allLogs.length.toLocaleString()}</div>
        </div>
        <div style={{ background:'var(--bg-card)', border:`1px solid ${chainOk?'rgba(16,185,129,0.25)':'var(--error-border)'}`, borderRadius:'var(--radius-lg)', padding:'16px 20px' }}>
          <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', textTransform:'uppercase', letterSpacing:'0.5px', marginBottom:6 }}>Chain Integrity</div>
          <div style={{ display:'flex', alignItems:'center', gap:8 }}>
            <Icon name={chainOk?'verified_user':'gpp_bad'} size={22} style={{ color:chainOk?'var(--success)':'var(--error)' }}/>
            <span style={{ fontWeight:700, fontSize:'var(--text-lg)', color:chainOk?'var(--success)':'var(--error)' }}>{chainOk?'Valid':'Broken'}</span>
          </div>
        </div>
        {stats?.last_entry && (
          <div style={{ background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-lg)', padding:'16px 20px' }}>
            <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', textTransform:'uppercase', letterSpacing:'0.5px', marginBottom:6 }}>Last Entry</div>
            <div style={{ fontSize:'var(--text-sm)', fontWeight:600 }}>{fmtDate(stats.last_entry)}</div>
          </div>
        )}
      </div>

      {/* Toolbar */}
      <div style={{ display:'flex', gap:8, marginBottom:16, flexWrap:'wrap' }}>
        <select value={filterUser} onChange={e=>setFilterUser(e.target.value)}
          style={{ background:'var(--surface)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', padding:'7px 11px', color:'var(--text)', fontSize:'var(--text-sm)', outline:'none', appearance:'none', minWidth:130 }}>
          <option value="">All Users</option>
          {users.map(u => <option key={u} value={u}>{u}</option>)}
        </select>
        <input value={filterAction} onChange={e=>setFilterAction(e.target.value)} placeholder="Filter action…"
          style={{ background:'var(--surface)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', padding:'7px 11px', color:'var(--text)', fontSize:'var(--text-sm)', outline:'none', fontFamily:'var(--font-ui)', width:160 }}/>
        <select value={filterDays} onChange={e=>setFilterDays(e.target.value)}
          style={{ background:'var(--surface)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', padding:'7px 11px', color:'var(--text)', fontSize:'var(--text-sm)', outline:'none', appearance:'none' }}>
          <option value="0">All time</option>
          <option value="1">Last 24h</option>
          <option value="7">Last 7d</option>
          <option value="30">Last 30d</option>
        </select>
        <div style={{ flex:1 }}/>
        <button onClick={exportCSV} style={{ padding:'7px 14px', background:'var(--surface)', color:'var(--text-secondary)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', cursor:'pointer', fontSize:'var(--text-sm)', fontWeight:500, display:'inline-flex', alignItems:'center', gap:6 }}>
          <Icon name="download" size={14}/>Export CSV
        </button>
        <button onClick={() => rotate.mutate()} disabled={rotate.isPending}
          style={{ padding:'7px 14px', background:'var(--surface)', color:'var(--text-secondary)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', cursor:'pointer', fontSize:'var(--text-sm)', fontWeight:500, display:'inline-flex', alignItems:'center', gap:6 }}>
          <Icon name="rotate_right" size={14}/>{rotate.isPending?'Rotating…':'Rotate Logs'}
        </button>
      </div>

      {/* Table */}
      {logsQ.isLoading && <Skeleton height={300}/>}
      {logsQ.isError   && <ErrorState error={logsQ.error} onRetry={()=>qc.invalidateQueries({queryKey:['audit','logs']})}/>}
      {!logsQ.isLoading && !logsQ.isError && (
        <div style={{ background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-lg)', overflow:'hidden' }}>
          <table style={{ width:'100%', borderCollapse:'collapse' }}>
            <thead><tr style={{ background:'rgba(255,255,255,0.03)' }}>
              {['Timestamp','User','Action','Details','IP'].map(h=>(
                <th key={h} style={{ padding:'10px 14px', textAlign:'left', fontSize:'var(--text-2xs)', fontWeight:700, color:'var(--text-tertiary)', textTransform:'uppercase', letterSpacing:'0.5px', borderBottom:'1px solid var(--border)' }}>{h}</th>
              ))}
            </tr></thead>
            <tbody>
              {filtered.map((entry, i) => {
                const color = actionColor(entry.action)
                return (
                  <tr key={i} style={{ borderBottom:'1px solid var(--border)' }}
                    onMouseEnter={e=>(e.currentTarget.style.background='rgba(255,255,255,0.02)')}
                    onMouseLeave={e=>(e.currentTarget.style.background='transparent')}>
                    <td style={{ padding:'10px 14px', fontSize:'var(--text-xs)', color:'var(--text-tertiary)', whiteSpace:'nowrap', fontFamily:'var(--font-mono)' }}>{fmtDate(entry.timestamp)}</td>
                    <td style={{ padding:'10px 14px' }}>
                      <span style={{ padding:'2px 7px', borderRadius:'var(--radius-full)', background:'var(--surface)', border:'1px solid var(--border)', fontSize:'var(--text-xs)', fontWeight:600 }}>{entry.username || 'System'}</span>
                    </td>
                    <td style={{ padding:'10px 14px' }}>
                      <span style={{ padding:'2px 7px', borderRadius:'var(--radius-sm)', background:`${color}18`, border:`1px solid ${color}30`, color, fontSize:'var(--text-xs)', fontFamily:'var(--font-mono)', fontWeight:600 }}>{entry.action}</span>
                    </td>
                    <td style={{ padding:'10px 14px', fontSize:'var(--text-xs)', color:'var(--text-secondary)', maxWidth:300, overflow:'hidden', textOverflow:'ellipsis', whiteSpace:'nowrap' }}>{entry.details || '—'}</td>
                    <td style={{ padding:'10px 14px', fontFamily:'var(--font-mono)', fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>{entry.ip_address || '—'}</td>
                  </tr>
                )
              })}
              {filtered.length === 0 && (
                <tr><td colSpan={5} style={{ padding:'40px 14px', textAlign:'center', color:'var(--text-tertiary)' }}>No entries match filters</td></tr>
              )}
              {filtered.length === 200 && allLogs.length > 200 && (
                <tr><td colSpan={5} style={{ padding:'12px 14px', textAlign:'center', fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>Showing first 200 of {allLogs.length} entries — export CSV for full log</td></tr>
              )}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
