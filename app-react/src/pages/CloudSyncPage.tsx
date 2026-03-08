/**
 * pages/CloudSyncPage.tsx — Cloud Sync (Phase 7)
 *
 * GET  /api/cloud-sync?action=status    → { remotes: Remote[] }
 * GET  /api/cloud-sync?action=remotes   → { remotes: Remote[] }
 * GET  /api/cloud-sync?action=providers → { providers: Provider[] }
 * POST /api/cloud-sync { action:'configure', name, type, config }
 * POST /api/cloud-sync { action:'sync', remote, local_path, direction, dry_run }
 * POST /api/cloud-sync { action:'test', remote }
 * POST /api/cloud-sync { action:'delete', remote }
 */

import { useState } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

interface Remote   { name: string; type?: string; status?: string; last_sync?: string; config?: Record<string,unknown> }
interface Provider { id: string; name: string; icon?: string; fields?: ProviderField[] }
interface ProviderField { key: string; label: string; type?: string; required?: boolean }

const S = {
  btn:  { padding:'8px 14px', background:'var(--surface)', color:'var(--text-secondary)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', cursor:'pointer', fontSize:'var(--text-sm)', fontWeight:500, display:'inline-flex', alignItems:'center', gap:6 } as React.CSSProperties,
  btnP: { padding:'8px 16px', background:'var(--primary)', color:'#000', border:'none', borderRadius:'var(--radius-sm)', cursor:'pointer', fontSize:'var(--text-sm)', fontWeight:700, display:'inline-flex', alignItems:'center', gap:6 } as React.CSSProperties,
  inp:  { background:'var(--surface)', border:'1px solid var(--border)', borderRadius:'var(--radius-sm)', padding:'8px 12px', color:'var(--text)', fontSize:'var(--text-sm)', width:'100%', outline:'none', boxSizing:'border-box' as const, fontFamily:'var(--font-ui)' },
  card: { background:'var(--bg-card)', border:'1px solid var(--border)', borderRadius:'var(--radius-xl)', padding:20 } as React.CSSProperties,
}

function statusDot(s?: string) {
  const c = s === 'ok' ? 'var(--success)' : s === 'syncing' ? 'var(--primary)' : s === 'error' ? 'var(--error)' : 'var(--text-tertiary)'
  return <span style={{ width:8, height:8, borderRadius:'50%', background:c, display:'inline-block', flexShrink:0 }} />
}

function fmtDate(s?:string){if(!s)return'—';try{return new Date(s).toLocaleString('de-DE',{dateStyle:'short',timeStyle:'short'})}catch{return s}}

const PROVIDER_ICONS: Record<string,string> = { s3:'cloud', b2:'cloud', gdrive:'folder', dropbox:'cloud_download', sftp:'terminal', rclone:'sync' }

export function CloudSyncPage() {
  const qc = useQueryClient()
  const [selectedProvider, setSelectedProvider] = useState<Provider|null>(null)
  const [configName, setConfigName] = useState('')
  const [configFields, setConfigFields] = useState<Record<string,string>>({})
  const [syncRemote, setSyncRemote] = useState('')
  const [syncPath, setSyncPath] = useState('')
  const [syncDir, setSyncDir] = useState<'push'|'pull'>('push')
  const [dryRun, setDryRun] = useState(false)

  const remotesQ   = useQuery({ queryKey:['cloud','remotes'],   queryFn:({signal})=>api.get<{success:boolean;remotes:Remote[]}>('/api/cloud-sync?action=remotes',signal) })
  const providersQ = useQuery({ queryKey:['cloud','providers'], queryFn:({signal})=>api.get<{success:boolean;providers:Provider[]}>('/api/cloud-sync?action=providers',signal) })

  const configure = useMutation({
    mutationFn: () => api.post('/api/cloud-sync', { action:'configure', name:configName, type:selectedProvider!.id, config:configFields }),
    onSuccess: () => { toast.success('Remote configured'); setSelectedProvider(null); setConfigName(''); setConfigFields({}); qc.invalidateQueries({queryKey:['cloud']}) },
    onError: (e:Error)=>toast.error(e.message),
  })
  const sync = useMutation({
    mutationFn: () => api.post('/api/cloud-sync', { action:'sync', remote:syncRemote, local_path:syncPath, direction:syncDir, dry_run:dryRun }),
    onSuccess: () => toast.success(dryRun ? 'Dry run complete' : 'Sync started'),
    onError: (e:Error)=>toast.error(e.message),
  })
  const testRemote = useMutation({
    mutationFn: (name:string) => api.post('/api/cloud-sync', { action:'test', remote:name }),
    onSuccess: () => toast.success('Connection OK'),
    onError: (e:Error)=>toast.error(e.message),
  })
  const del = useMutation({
    mutationFn: (name:string) => api.post('/api/cloud-sync', { action:'delete', remote:name }),
    onSuccess: () => { toast.success('Remote removed'); qc.invalidateQueries({queryKey:['cloud']}) },
    onError: (e:Error)=>toast.error(e.message),
  })

  const remotes   = remotesQ.data?.remotes ?? []
  const providers = providersQ.data?.providers ?? []

  return (
    <div style={{ maxWidth:1000 }}>
      <div style={{ marginBottom:28 }}><h1 style={{ fontSize:'var(--text-3xl)', fontWeight:700, letterSpacing:'-1px', marginBottom:6 }}>Cloud Sync</h1><p style={{ color:'var(--text-secondary)' }}>rclone-powered sync to S3, Backblaze B2, SFTP and more</p></div>

      {/* Provider picker */}
      {!selectedProvider && (
        <div style={{ ...S.card, marginBottom:24 }}>
          <div style={{ fontWeight:700, marginBottom:14 }}>Add Remote</div>
          {providersQ.isLoading ? <Skeleton height={80}/> : (
            <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fill,minmax(120px,1fr))', gap:8 }}>
              {providers.map(p => (
                <button key={p.id} onClick={() => { setSelectedProvider(p); setConfigName(p.id) }}
                  style={{ background:'var(--surface)', border:'1px solid var(--border)', borderRadius:'var(--radius-md)', padding:'14px 10px', cursor:'pointer', display:'flex', flexDirection:'column', alignItems:'center', gap:6, transition:'all 0.15s' }}
                  onMouseEnter={e=>(e.currentTarget.style.borderColor='var(--primary)')}
                  onMouseLeave={e=>(e.currentTarget.style.borderColor='var(--border)')}>
                  <Icon name={PROVIDER_ICONS[p.id]||'cloud'} size={22} style={{ color:'var(--primary)' }}/>
                  <span style={{ fontSize:'var(--text-xs)', fontWeight:600 }}>{p.name}</span>
                </button>
              ))}
              {!providersQ.isLoading && providers.length === 0 && (
                <button onClick={() => setSelectedProvider({ id:'custom', name:'Custom (rclone)' })}
                  style={{ background:'var(--surface)', border:'1px dashed var(--border)', borderRadius:'var(--radius-md)', padding:'14px 10px', cursor:'pointer', display:'flex', flexDirection:'column', alignItems:'center', gap:6 }}>
                  <Icon name="add" size={22} style={{ color:'var(--text-tertiary)' }}/>
                  <span style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>Custom</span>
                </button>
              )}
            </div>
          )}
        </div>
      )}

      {/* Config form */}
      {selectedProvider && (
        <div style={{ ...S.card, marginBottom:24 }}>
          <div style={{ fontWeight:700, marginBottom:14 }}>Configure: {selectedProvider.name}</div>
          <div style={{ display:'grid', gap:10 }}>
            <label style={{ display:'flex', flexDirection:'column', gap:5 }}>
              <span style={{ fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>Remote Name</span>
              <input value={configName} onChange={e=>setConfigName(e.target.value)} style={S.inp} />
            </label>
            {(selectedProvider.fields ?? []).map(f => (
              <label key={f.key} style={{ display:'flex', flexDirection:'column', gap:5 }}>
                <span style={{ fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>{f.label}{f.required ? ' *' : ''}</span>
                <input type={f.type === 'password' ? 'password' : 'text'} value={configFields[f.key]??''} onChange={e=>setConfigFields(p=>({...p,[f.key]:e.target.value}))} style={S.inp} />
              </label>
            ))}
            {!selectedProvider.fields?.length && (
              <>
                {[['Host / Bucket','host'],['Access Key / User','key'],['Secret','secret']].map(([lbl,k])=>(
                  <label key={k} style={{ display:'flex', flexDirection:'column', gap:5 }}>
                    <span style={{ fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>{lbl}</span>
                    <input value={configFields[k]??''} onChange={e=>setConfigFields(p=>({...p,[k]:e.target.value}))} style={S.inp}/>
                  </label>
                ))}
              </>
            )}
            <div style={{ display:'flex', gap:8 }}>
              <button onClick={()=>setSelectedProvider(null)} style={S.btn}>Cancel</button>
              <button onClick={()=>configure.mutate()} disabled={!configName.trim()||configure.isPending} style={S.btnP}><Icon name="save" size={14}/>{configure.isPending?'Saving…':'Save Remote'}</button>
            </div>
          </div>
        </div>
      )}

      {/* Remotes list */}
      {remotesQ.isLoading && <Skeleton height={160}/>}
      {remotesQ.isError  && <ErrorState error={remotesQ.error}/>}
      <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fill,minmax(280px,1fr))', gap:14, marginBottom:28 }}>
        {remotes.map(r => (
          <div key={r.name} style={S.card}>
            <div style={{ display:'flex', alignItems:'center', gap:10, marginBottom:10 }}>
              <Icon name={PROVIDER_ICONS[r.type||'']||'cloud'} size={22} style={{ color:'var(--primary)', flexShrink:0 }}/>
              <div style={{ flex:1 }}><div style={{ fontWeight:700 }}>{r.name}</div><div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>{r.type}{r.last_sync && ` · ${fmtDate(r.last_sync)}`}</div></div>
              {statusDot(r.status)}
            </div>
            <div style={{ display:'flex', gap:6 }}>
              <button onClick={()=>testRemote.mutate(r.name)} disabled={testRemote.isPending} style={S.btn}><Icon name="cable" size={13}/>Test</button>
              <button onClick={()=>{ setSyncRemote(r.name) }} style={S.btn}><Icon name="sync" size={13}/>Sync</button>
              <button onClick={()=>{ if(window.confirm(`Remove "${r.name}"?`)) del.mutate(r.name) }} style={{ ...S.btn, color:'var(--error)', borderColor:'var(--error-border)' }}><Icon name="delete" size={13}/></button>
            </div>
          </div>
        ))}
        {!remotesQ.isLoading && remotes.length === 0 && (
          <div style={{ gridColumn:'1/-1', textAlign:'center', padding:'40px 0', border:'1px dashed var(--border)', borderRadius:'var(--radius-xl)', color:'var(--text-tertiary)' }}>
            <Icon name="cloud_off" size={40} style={{ opacity:0.3, display:'block', margin:'0 auto 12px' }}/>No remotes configured
          </div>
        )}
      </div>

      {/* Sync panel */}
      {syncRemote && (
        <div style={{ ...S.card, border:'1px solid rgba(138,156,255,0.3)' }}>
          <div style={{ fontWeight:700, marginBottom:14 }}>Sync: {syncRemote}</div>
          <div style={{ display:'grid', gridTemplateColumns:'1fr 120px auto', gap:8, alignItems:'flex-end', marginBottom:10 }}>
            <label style={{ display:'flex', flexDirection:'column', gap:5 }}>
              <span style={{ fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>Local path</span>
              <input value={syncPath} onChange={e=>setSyncPath(e.target.value)} placeholder="/mnt/tank/data" style={{ ...S.inp, fontFamily:'var(--font-mono)' }}/>
            </label>
            <label style={{ display:'flex', flexDirection:'column', gap:5 }}>
              <span style={{ fontSize:'var(--text-xs)', color:'var(--text-secondary)' }}>Direction</span>
              <select value={syncDir} onChange={e=>setSyncDir(e.target.value as 'push'|'pull')} style={{ ...S.inp, appearance:'none' }}>
                <option value="push">Push ↑</option>
                <option value="pull">Pull ↓</option>
              </select>
            </label>
            <button onClick={()=>sync.mutate()} disabled={!syncPath.trim()||sync.isPending} style={{ ...S.btnP, alignSelf:'flex-end' }}>
              <Icon name="sync" size={14}/>{sync.isPending?'Syncing…':dryRun?'Dry Run':'Sync'}
            </button>
          </div>
          <label style={{ display:'flex', alignItems:'center', gap:8, cursor:'pointer', fontSize:'var(--text-sm)' }}>
            <input type="checkbox" checked={dryRun} onChange={e=>setDryRun(e.target.checked)} style={{ accentColor:'var(--primary)' }}/>
            Dry run (no changes)
          </label>
        </div>
      )}
    </div>
  )
}
