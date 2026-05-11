/**
 * pages/CloudSyncPage.tsx - Cloud Sync + Cold Tier
 *
 * Tab 1 (Remotes): rclone remote configuration and sync jobs.
 * Tab 2 (Cold Tier): persistent rclone FUSE mounts under /mnt/cold/.
 *
 * Remotes API:
 *   GET  /api/cloud-sync?action=remotes   → { remotes: Remote[] }
 *   GET  /api/cloud-sync?action=providers → { providers: Provider[] }
 *   POST /api/cloud-sync { action:'configure'|'sync'|'test'|'delete', ... }
 *
 * Cold Tier API:
 *   GET    /api/storage/cold-tier         → { mounts: ColdTierMount[] }
 *   POST   /api/storage/cold-tier         → create + mount
 *   DELETE /api/storage/cold-tier/{id}    → unmount + delete
 *   POST   /api/storage/cold-tier/{id}/mount
 *   POST   /api/storage/cold-tier/{id}/unmount
 *   POST   /api/storage/cold-tier/{id}/usage → job_id
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'

interface Remote   { name: string; type?: string; status?: string; last_sync?: string; config?: Record<string,unknown> }
interface Provider { id: string; name: string; icon?: string; fields?: ProviderField[] }
interface ProviderField { key: string; label: string; type?: string; required?: boolean }

interface ColdTierMount {
  id: number
  name: string
  remote: string
  remote_path: string
  mount_point: string
  vfs_cache_mode: string
  mounted: boolean
  created_at: string
  last_mount_at: string | null
}



function statusDot(s?: string) {
  const c = s === 'ok' ? 'var(--success)' : s === 'syncing' ? 'var(--primary)' : s === 'error' ? 'var(--error)' : 'var(--text-tertiary)'
  return <span style={{ width:8, height:8, borderRadius:'50%', background:c, display:'inline-block', flexShrink:0 }} />
}

function fmtDate(s?:string){if(!s)return'-';try{return new Date(s).toLocaleString('de-DE',{dateStyle:'short',timeStyle:'short'})}catch{return s}}

const PROVIDER_ICONS: Record<string,string> = { s3:'cloud', b2:'cloud', gdrive:'folder', dropbox:'cloud_download', sftp:'terminal', rclone:'sync' }

// ---------------------------------------------------------------------------
// Cold Tier tab
// ---------------------------------------------------------------------------

const VFS_CACHE_OPTIONS = [
  { value: 'writes', label: 'Writes (recommended)' },
  { value: 'full',   label: 'Full (aggressive caching)' },
  { value: 'minimal', label: 'Minimal' },
  { value: 'off',    label: 'Off' },
]

function ColdTierTab({ remotes }: { remotes: Remote[] }) {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()

  const [showAdd, setShowAdd]     = useState(false)
  const [name, setName]           = useState('')
  const [remote, setRemote]       = useState('')
  const [remotePath, setRemotePath] = useState('')
  const [vfsCache, setVfsCache]   = useState('writes')
  const [usageJobIds, setUsageJobIds] = useState<Record<number, string>>({})

  const { data, isLoading, error } = useQuery<{ success: boolean; mounts: ColdTierMount[] }>({
    queryKey: ['cold-tier'],
    queryFn: () => api.get('/api/storage/cold-tier'),
    refetchInterval: 15000,
  })

  const create = useMutation({
    mutationFn: () => api.post('/api/storage/cold-tier', {
      name: name.trim(),
      remote: remote.trim(),
      remote_path: remotePath.trim(),
      vfs_cache_mode: vfsCache,
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['cold-tier'] })
      toast.success('Mount created - mounting in background')
      setShowAdd(false)
      setName(''); setRemote(''); setRemotePath(''); setVfsCache('writes')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const del = useMutation({
    mutationFn: (id: number) => api.delete(`/api/storage/cold-tier/${id}`),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['cold-tier'] }); toast.success('Mount removed') },
    onError: (e: Error) => toast.error(e.message),
  })

  const mount = useMutation({
    mutationFn: (id: number) => api.post(`/api/storage/cold-tier/${id}/mount`, {}),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['cold-tier'] }); toast.success('Mounting…') },
    onError: (e: Error) => toast.error(e.message),
  })

  const unmount = useMutation({
    mutationFn: (id: number) => api.post(`/api/storage/cold-tier/${id}/unmount`, {}),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['cold-tier'] }); toast.success('Unmounted') },
    onError: (e: Error) => toast.error(e.message),
  })

  const usage = useMutation({
    mutationFn: (id: number) => api.post<{ job_id: string }>(`/api/storage/cold-tier/${id}/usage`, {}),
    onSuccess: (data, id) => {
      setUsageJobIds(prev => ({ ...prev, [id]: data.job_id }))
      toast.success('Usage check started')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const mounts = data?.mounts ?? []

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div>
          <p style={{ margin: 0, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            FUSE mounts of rclone remotes at <code>/mnt/cold/</code>. Mounts persist across daemon restarts.
          </p>
        </div>
        <button className="btn btn-primary" onClick={() => setShowAdd(v => !v)}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Icon name={showAdd ? 'close' : 'add'} size={15} />
            {showAdd ? 'Cancel' : 'Add Mount'}
          </span>
        </button>
      </div>

      {showAdd && (
        <div className="card" style={{ padding: 20 }}>
          <h4 style={{ margin: '0 0 16px', fontSize: 'var(--text-sm)', fontWeight: 600 }}>New Cold Tier Mount</h4>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 14 }}>
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Mount name</label>
              <input className="input" placeholder="e.g. backup-b2" value={name} onChange={e => setName(e.target.value)} />
              <p style={{ margin: '4px 0 0', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Becomes /mnt/cold/&lt;name&gt;</p>
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Remote</label>
              {remotes.length > 0 ? (
                <select className="input" value={remote} onChange={e => setRemote(e.target.value)}>
                  <option value="">Select remote…</option>
                  {remotes.map(r => <option key={r.name} value={r.name}>{r.name}</option>)}
                </select>
              ) : (
                <input className="input" placeholder="remote-name" value={remote} onChange={e => setRemote(e.target.value)} />
              )}
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>
                Remote path <span style={{ color: 'var(--text-tertiary)', fontWeight: 400 }}>(optional)</span>
              </label>
              <input className="input" placeholder="bucket/prefix" value={remotePath} onChange={e => setRemotePath(e.target.value)}
                style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }} />
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>VFS cache mode</label>
              <select className="input" value={vfsCache} onChange={e => setVfsCache(e.target.value)}>
                {VFS_CACHE_OPTIONS.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
              </select>
            </div>
          </div>
          <div style={{ marginTop: 16, display: 'flex', justifyContent: 'flex-end' }}>
            <button className="btn btn-primary" disabled={!name.trim() || !remote.trim() || create.isPending}
              onClick={() => create.mutate()}>
              {create.isPending ? 'Creating…' : 'Create Mount'}
            </button>
          </div>
        </div>
      )}

      {isLoading ? (
        <div className="card" style={{ padding: 24 }}><Skeleton style={{ height: 80 }} /></div>
      ) : error ? (
        <ErrorState error={error} title="Failed to load cold-tier mounts" />
      ) : mounts.length === 0 ? (
        <div style={{ textAlign: 'center', padding: '48px 0', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
          <Icon name="cloud_off" size={40} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          No cold-tier mounts configured
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {mounts.map(m => (
            <div key={m.id} className="card" style={{ padding: '14px 18px', display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>
                <span style={{
                  width: 8, height: 8, borderRadius: '50%', flexShrink: 0,
                  background: m.mounted ? 'var(--success)' : 'var(--text-tertiary)',
                }} />
                <span style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>{m.name}</span>
              </div>
              <code style={{ fontSize: 12, color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)', flex: 1, minWidth: 120 }}>
                {m.remote}:{m.remote_path || ''} → {m.mount_point}
              </code>
              <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', whiteSpace: 'nowrap' }}>
                {m.mounted ? 'mounted' : 'not mounted'} · cache: {m.vfs_cache_mode}
              </span>
              {usageJobIds[m.id] && (
                <span style={{ fontSize: 'var(--text-xs)', color: 'var(--primary)', whiteSpace: 'nowrap' }}>
                  usage job: {usageJobIds[m.id].slice(0, 8)}
                </span>
              )}
              <div style={{ display: 'flex', gap: 6, marginLeft: 'auto', flexShrink: 0 }}>
                {m.mounted ? (
                  <>
                    <button className="btn btn-ghost btn-xs" title="Check usage" style={{ padding: '4px 8px' }}
                      onClick={() => usage.mutate(m.id)} disabled={usage.isPending}>
                      <Icon name="storage" size={13} />
                    </button>
                    <button className="btn btn-ghost btn-xs" style={{ padding: '4px 8px' }}
                      onClick={() => unmount.mutate(m.id)} disabled={unmount.isPending}>
                      Unmount
                    </button>
                  </>
                ) : (
                  <button className="btn btn-ghost btn-xs" style={{ padding: '4px 8px' }}
                    onClick={() => mount.mutate(m.id)} disabled={mount.isPending}>
                    Mount
                  </button>
                )}
                <button className="btn btn-ghost btn-xs" style={{ padding: '4px 8px', color: 'var(--error)' }}
                  onClick={async () => {
                    if (await confirm({ title: `Remove "${m.name}"?`, message: `This will unmount and delete the cold-tier mount. The remote data is not affected.`, danger: true, confirmLabel: 'Remove' }))
                      del.mutate(m.id)
                  }} disabled={del.isPending}>
                  <Icon name="delete" size={13} />
                </button>
              </div>
            </div>
          ))}
        </div>
      )}

      <ConfirmDialog />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function CloudSyncPage() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [tab, setTab] = useState<'remotes' | 'cold-tier'>('remotes')
  const [selectedProvider, setSelectedProvider] = useState<Provider|null>(null)
  const [isEditing, setIsEditing] = useState(false)
  const [configName, setConfigName] = useState('')
  const [configFields, setConfigFields] = useState<Record<string,string>>({})
  const [syncRemote, setSyncRemote] = useState('')
  const [syncPath, setSyncPath] = useState('')
  const [syncDir, setSyncDir] = useState<'push'|'pull'>('push')
  const [dryRun, setDryRun] = useState(false)

  const remotesQ   = useQuery({ queryKey:['cloud','remotes'],   queryFn:({signal})=>api.get<{success:boolean;remotes:Remote[]}>('/api/cloud-sync?action=remotes',signal) })
  const providersQ = useQuery({ queryKey:['cloud','providers'], queryFn:({signal})=>api.get<{success:boolean;providers:Provider[]}>('/api/cloud-sync?action=providers',signal) })

  const configure = useMutation({
    mutationFn: () => api.post('/api/cloud-sync', { 
      action: isEditing ? 'update' : 'configure', 
      name: configName, 
      type: selectedProvider?.id, 
      config: configFields 
    }),
    onSuccess: () => { 
      toast.success(isEditing ? 'Remote updated' : 'Remote configured'); 
      setSelectedProvider(null); 
      setConfigName(''); 
      setConfigFields({}); 
      setIsEditing(false);
      qc.invalidateQueries({queryKey:['cloud']}) 
    },
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

  const handleEdit = (r: Remote) => {
    const provider = providers.find(p => p.id === r.type) || { id: r.type || 'custom', name: r.type || 'Custom' }
    setSelectedProvider(provider)
    setConfigName(r.name)
    setConfigFields(r.config as Record<string, string> || {})
    setIsEditing(true)
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  return (
    <div style={{ maxWidth:1000 }}>
      <div className="page-header">
        <h1 className="page-title">Cloud Sync</h1>
        <p className="page-subtitle">rclone-powered sync and cold-tier FUSE mounts</p>
      </div>

      {/* Tab bar */}
      <div className="tabs" style={{ marginBottom: 24 }}>
        <button className={`tab ${tab === 'remotes' ? 'tab-active' : ''}`} onClick={() => setTab('remotes')}>
          Remotes
        </button>
        <button className={`tab ${tab === 'cold-tier' ? 'tab-active' : ''}`} onClick={() => setTab('cold-tier')}>
          Cold Tier
        </button>
      </div>

      {tab === 'cold-tier' && <ColdTierTab remotes={remotes} />}

      {tab === 'remotes' && <>

      {/* Provider picker - only show if not editing or if we want to change provider */}
      {!selectedProvider && !isEditing && (
        <div className="card card-xl" style={{ marginBottom:24 }}>
          <div style={{ fontWeight:700, marginBottom:14 }}>Add Remote</div>
          {providersQ.isLoading ? <Skeleton height={80}/> : (
            <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fill,minmax(120px,1fr))', gap:8 }}>
              {providers.map(p => (
                <button key={p.id} onClick={() => { setSelectedProvider(p); setConfigName(p.id); setIsEditing(false) }}
                  className="card" style={{ background: 'var(--surface)', borderRadius:'var(--radius-md)', padding:'14px 10px', cursor:'pointer', display:'flex', flexDirection:'column', alignItems:'center', gap:6, transition:'all 0.15s' }}
                  onMouseEnter={e=>(e.currentTarget.style.borderColor='var(--primary)')}
                  onMouseLeave={e=>(e.currentTarget.style.borderColor='var(--border)')}>
                  <Icon name={PROVIDER_ICONS[p.id]||'cloud'} size={22} style={{ color:'var(--primary)' }}/>
                  <span style={{ fontSize:'var(--text-xs)', fontWeight:600 }}>{p.name}</span>
                </button>
              ))}
              {!providersQ.isLoading && providers.length === 0 && (
                <button onClick={() => { setSelectedProvider({ id:'custom', name:'Custom (rclone)' }); setIsEditing(false) }}
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
        <div className="card card-xl" style={{ marginBottom:24 }}>
          <div style={{ fontWeight:700, marginBottom:14 }}>{isEditing ? 'Edit' : 'Configure'}: {selectedProvider.name}</div>
          <div style={{ display:'grid', gap:10 }}>
            <label className="field">
              <span className="field-label">Remote Name</span>
              <input value={configName} onChange={e=>setConfigName(e.target.value)} className="input" disabled={isEditing} />
              {isEditing && <span style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>Remote name cannot be changed</span>}
            </label>
            {(selectedProvider.fields ?? []).map(f => (
              <label key={f.key} className="field">
                <span className="field-label">{f.label}{f.required ? ' *' : ''}</span>
                <input type={f.type === 'password' ? 'password' : 'text'} value={configFields[f.key]??''} onChange={e=>setConfigFields(p=>({...p,[f.key]:e.target.value}))} className="input" />
              </label>
            ))}
            {(!selectedProvider.fields?.length && !isEditing) && (
              <>
                {[['Host / Bucket','host'],['Access Key / User','key'],['Secret','secret']].map(([lbl,k])=>(
                  <label key={k} className="field">
                    <span className="field-label">{lbl}</span>
                    <input value={configFields[k]??''} onChange={e=>setConfigFields(p=>({...p,[k]:e.target.value}))} className="input"/>
                  </label>
                ))}
              </>
            )}
            {/* If editing a custom provider or one without defined fields, show raw keys if any */}
            {isEditing && !selectedProvider.fields?.length && (
              Object.entries(configFields).map(([k, v]) => (
                <label key={k} className="field">
                  <span className="field-label">{k}</span>
                  <input value={String(v)} onChange={e=>setConfigFields(p=>({...p,[k]:e.target.value}))} className="input"/>
                </label>
              ))
            )}
            <div style={{ display:'flex', gap:8 }}>
              <button onClick={()=>{ setSelectedProvider(null); setIsEditing(false) }} className="btn btn-ghost">Cancel</button>
              <button onClick={()=>configure.mutate()} disabled={!configName.trim()||configure.isPending} className="btn btn-primary"><Icon name="save" size={14}/>{configure.isPending?'Saving…':isEditing?'Update Remote':'Save Remote'}</button>
            </div>
          </div>
        </div>
      )}

      {/* Remotes list */}
      {remotesQ.isLoading && <Skeleton height={160}/>}
      {remotesQ.isError  && <ErrorState error={remotesQ.error}/>}
      <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fill,minmax(280px,1fr))', gap:14, marginBottom:28 }}>
        {remotes.map(r => (
          <div key={r.name} className="card card-xl">
            <div style={{ display:'flex', alignItems:'center', gap:10, marginBottom:10 }}>
              <Icon name={PROVIDER_ICONS[r.type||'']||'cloud'} size={22} style={{ color:'var(--primary)', flexShrink:0 }}/>
              <div style={{ flex:1 }}><div style={{ fontWeight:700 }}>{r.name}</div><div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>{r.type}{r.last_sync && ` · ${fmtDate(r.last_sync)}`}</div></div>
              {statusDot(r.status)}
            </div>
            <div style={{ display:'flex', gap:6 }}>
              <button onClick={()=>handleEdit(r)} className="btn btn-ghost"><Icon name="edit" size={13}/>Edit</button>
              <button onClick={()=>testRemote.mutate(r.name)} disabled={testRemote.isPending} className="btn btn-ghost"><Icon name="cable" size={13}/>Test</button>
              <button onClick={()=>{ setSyncRemote(r.name) }} className="btn btn-ghost"><Icon name="sync" size={13}/>Sync</button>
              <button onClick={async ()=>{ if(await confirm({ title:`Remove "${r.name}"?`, message:'This will disconnect the remote and delete its configuration.', danger:true, confirmLabel:'Remove' })) del.mutate(r.name) }} className="btn btn-ghost" style={{ color:'var(--error)', borderColor:'var(--error-border)' }}><Icon name="delete" size={13}/></button>
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
        <div className="card card-xl" style={{ border:'1px solid rgba(138,156,255,0.3)' }}>
          <div style={{ fontWeight:700, marginBottom:14 }}>Sync: {syncRemote}</div>
          <div style={{ display:'grid', gridTemplateColumns:'1fr 120px auto', gap:8, alignItems:'flex-end', marginBottom:10 }}>
            <label className="field">
              <span className="field-label">Local path</span>
              <input value={syncPath} onChange={e=>setSyncPath(e.target.value)} placeholder="/mnt/tank/data" className="input" style={{ fontFamily:'var(--font-mono)' }}/>
            </label>
            <label className="field">
              <span className="field-label">Direction</span>
              <select value={syncDir} onChange={e=>setSyncDir(e.target.value as 'push'|'pull')} className="input" style={{ appearance:'none' }}>
                <option value="push">Push ↑</option>
                <option value="pull">Pull ↓</option>
              </select>
            </label>
            <button onClick={()=>sync.mutate()} disabled={!syncPath.trim()||sync.isPending} className="btn btn-primary" style={{ alignSelf:'flex-end' }}>
              <Icon name="sync" size={14}/>{sync.isPending?'Syncing…':dryRun?'Dry Run':'Sync'}
            </button>
          </div>
          <label style={{ display:'flex', alignItems:'center', gap:8, cursor:'pointer', fontSize:'var(--text-sm)' }}>
            <input type="checkbox" checked={dryRun} onChange={e=>setDryRun(e.target.checked)} style={{ accentColor:'var(--primary)' }}/>
            Dry run (no changes)
          </label>
        </div>
      )}
      <ConfirmDialog />

      </>}
    </div>
  )
}

