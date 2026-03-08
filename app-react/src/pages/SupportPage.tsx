/**
 * pages/SupportPage.tsx — Support & Diagnostics (Phase 8)
 *
 * POST /api/system/support-bundle           → binary download (tar.gz)
 * GET  /api/system/metrics                  → { cpu, memory, disk, ... }
 * GET  /api/system/audit/verify-chain       → { valid }
 * GET  /api/nixos/pre-upgrade-snapshots     → { snapshots: Snapshot[] }
 */

import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api, getSessionId, getCsrfToken } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

interface SysMetrics { cpu_model?:string; cpu_percent?:number; memory_total?:number; memory_used?:number; uptime?:string; os?:string; kernel?:string; load_avg?:number[] }
interface Snapshot   { name?:string; snapshot?:string; created?:string; size?:number }

function fmtSize(b?:number):string { if(!b)return'—'; const u=['B','KB','MB','GB','TB']; const i=Math.min(Math.floor(Math.log(b)/Math.log(1024)),4); return`${(b/1024**i).toFixed(1)} ${u[i]}` }
function fmtDate(s?:string){if(!s)return'—';try{return new Date(s).toLocaleString('de-DE',{dateStyle:'short',timeStyle:'short'})}catch{return s}}

export function SupportPage() {
  const [downloading, setDownloading] = useState(false)

  const metricsQ  = useQuery({ queryKey:['system','metrics'],  queryFn:({signal})=>api.get<SysMetrics&{success:boolean}>('/api/system/metrics',signal) })
  const chainQ    = useQuery({ queryKey:['audit','chain'],     queryFn:({signal})=>api.get<{success:boolean;valid:boolean}>('/api/system/audit/verify-chain',signal) })
  const snapshotsQ= useQuery({ queryKey:['nixos','pre-snaps'], queryFn:({signal})=>api.get<{success:boolean;snapshots:Snapshot[]}>('/api/nixos/pre-upgrade-snapshots',signal) })

  // Support bundle: binary download — can't use api.post, need raw fetch
  async function downloadBundle() {
    setDownloading(true)
    try {
      const res = await fetch('/api/system/support-bundle', {
        method: 'POST',
        headers: {
          'X-Session-ID': getSessionId() ?? '',
          'X-CSRF-Token': getCsrfToken() ?? '',
        },
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `dplaneos-support-${new Date().toISOString().slice(0,16).replace(/:/g,'-')}.tar.gz`
      a.click()
      URL.revokeObjectURL(url)
      toast.success('Support bundle downloaded')
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setDownloading(false)
    }
  }

  const m = metricsQ.data
  const chainOk = chainQ.data?.valid !== false
  const snaps = snapshotsQ.data?.snapshots ?? []

  const metricRows = m ? [
    { label:'CPU Model',    value: m.cpu_model || '—' },
    { label:'CPU Usage',    value: m.cpu_percent != null ? `${m.cpu_percent.toFixed(1)}%` : '—' },
    { label:'Memory',       value: (m.memory_total && m.memory_used) ? `${fmtSize(m.memory_used)} / ${fmtSize(m.memory_total)}` : '—' },
    { label:'Load Average', value: m.load_avg ? m.load_avg.map(l=>l.toFixed(2)).join(', ') : '—' },
    { label:'Uptime',       value: m.uptime || '—' },
    { label:'OS',           value: m.os || '—' },
    { label:'Kernel',       value: m.kernel || '—' },
  ] : []

  return (
    <div style={{ maxWidth: 900 }}>
      <div className="page-header">
        <h1 className="page-title">Support</h1>
        <p className="page-subtitle">Diagnostics · system info · support bundle</p>
      </div>

      {/* Support bundle */}
      <div style={{ background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-xl)', padding:24, marginBottom:24 }}>
        <div style={{ display:'flex', alignItems:'center', gap:14 }}>
          <div style={{ width:48, height:48, background:'var(--primary-bg)', border:'1px solid rgba(138,156,255,0.2)', borderRadius:'var(--radius-md)', display:'flex', alignItems:'center', justifyContent:'center', flexShrink:0 }}>
            <Icon name="archive" size={24} style={{ color:'var(--primary)' }}/>
          </div>
          <div style={{ flex:1 }}>
            <div style={{ fontWeight:700, fontSize:'var(--text-lg)' }}>Support Bundle</div>
            <div style={{ fontSize:'var(--text-sm)', color:'var(--text-secondary)' }}>Download a compressed archive of logs, config and system state for troubleshooting</div>
          </div>
          <button onClick={downloadBundle} disabled={downloading} className="btn btn-primary">
            <Icon name="download" size={16}/>{downloading ? 'Generating…' : 'Download Bundle'}
          </button>
        </div>
      </div>

      <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr', gap:20 }}>
        {/* System info */}
        <div style={{ background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-xl)', padding:22 }}>
          <div style={{ fontWeight:700, marginBottom:16, display:'flex', alignItems:'center', gap:8 }}>
            <Icon name="computer" size={18} style={{ color:'var(--primary)' }}/>System Info
          </div>
          {metricsQ.isLoading && <Skeleton height={180}/>}
          {metricsQ.isError   && <ErrorState error={metricsQ.error}/>}
          <div style={{ display:'flex', flexDirection:'column', gap:8 }}>
            {metricRows.map(({ label, value }) => (
              <div key={label} style={{ display:'flex', justifyContent:'space-between', padding:'8px 12px', background:'var(--surface)', borderRadius:'var(--radius-sm)', gap:12 }}>
                <span style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', flexShrink:0 }}>{label}</span>
                <span style={{ fontSize:'var(--text-xs)', fontWeight:600, textAlign:'right', overflow:'hidden', textOverflow:'ellipsis', whiteSpace:'nowrap', maxWidth:'60%' }}>{value}</span>
              </div>
            ))}
          </div>
        </div>

        {/* Health checks */}
        <div>
          <div style={{ background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-xl)', padding:22, marginBottom:16 }}>
            <div style={{ fontWeight:700, marginBottom:14, display:'flex', alignItems:'center', gap:8 }}>
              <Icon name="health_and_safety" size={18} style={{ color:'var(--primary)' }}/>Health Checks
            </div>
            <div style={{ display:'flex', flexDirection:'column', gap:8 }}>
              <div style={{ display:'flex', alignItems:'center', gap:12, padding:'10px 14px', background:'var(--surface)', borderRadius:'var(--radius-sm)' }}>
                <Icon name={chainOk?'verified_user':'gpp_bad'} size={18} style={{ color:chainOk?'var(--success)':'var(--error)', flexShrink:0 }}/>
                <div style={{ flex:1 }}>
                  <div style={{ fontWeight:600, fontSize:'var(--text-sm)' }}>Audit Chain</div>
                  <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>{chainOk ? 'Integrity verified' : 'Chain broken — data may be compromised'}</div>
                </div>
                <span className={`badge ${chainOk ? 'badge-success' : 'badge-error'}`}>
                  {chainOk ? 'OK' : 'FAIL'}
                </span>
              </div>
            </div>
          </div>

          {/* Pre-upgrade snapshots */}
          <div style={{ background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-xl)', padding:22 }}>
            <div style={{ fontWeight:700, marginBottom:14, display:'flex', alignItems:'center', gap:8 }}>
              <Icon name="history" size={18} style={{ color:'var(--primary)' }}/>Pre-Upgrade Snapshots
            </div>
            {snapshotsQ.isLoading && <Skeleton height={80}/>}
            {snapshotsQ.isError   && <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>NixOS snapshots unavailable</div>}
            {!snapshotsQ.isLoading && (
              snaps.length > 0 ? (
                <div style={{ display:'flex', flexDirection:'column', gap:6 }}>
                  {snaps.map((s, i) => (
                    <div key={i} style={{ display:'flex', alignItems:'center', gap:10, padding:'8px 12px', background:'var(--surface)', borderRadius:'var(--radius-sm)' }}>
                      <Icon name="camera_alt" size={14} style={{ color:'var(--text-tertiary)', flexShrink:0 }}/>
                      <div style={{ flex:1, minWidth:0 }}>
                        <div style={{ fontFamily:'var(--font-mono)', fontSize:'var(--text-xs)', overflow:'hidden', textOverflow:'ellipsis', whiteSpace:'nowrap' }}>{s.name || s.snapshot}</div>
                        {s.created && <div style={{ fontSize:'var(--text-2xs)', color:'var(--text-tertiary)' }}>{fmtDate(s.created)}{s.size ? ` · ${fmtSize(s.size)}` : ''}</div>}
                      </div>
                    </div>
                  ))}
                </div>
              ) : (
                <div style={{ fontSize:'var(--text-sm)', color:'var(--text-tertiary)' }}>No pre-upgrade snapshots found</div>
              )
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
