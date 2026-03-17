import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useWsStore } from '@/stores/ws'

interface GitopsStatus { success: boolean; state?: string; pending_changes?: number; last_applied?: string; drift?: boolean }
interface Change       { resource?: string; action?: 'create'|'update'|'delete'|string; description?: string }

// Payload from daemon gitops.drift WS event
interface DriftPayload {
  drifted:       boolean
  error?:        string
  checked_at:    string
  create_count:  number
  modify_count:  number
  delete_count:  number
  blocked_count: number
  safe_to_apply: boolean
}

function fmtDate(s?:string){if(!s)return'—';try{return new Date(s).toLocaleString('de-DE',{dateStyle:'short',timeStyle:'short'})}catch{return s}}

function changeColor(a?:string):string {
  if (a === 'create') return 'var(--success)'
  if (a === 'delete') return 'var(--error)'
  return 'var(--warning)'
}

interface GitOpsSettings {
  enabled: boolean;
  repo_id: number;
  state_path: string;
  sync_storage: boolean;
  sync_access: boolean;
  sync_app: boolean;
  sync_identity: boolean;
  sync_protection: boolean;
  sync_system: boolean;
  updated_at: string;
}

interface Repo { id: string|number; url: string; name?: string }
interface Cred { id: string|number; name: string }

function SettingsTab({ onSyncNow }: { onSyncNow: () => void }) {
  const qc = useQueryClient()
  const settingsQ = useQuery({ queryKey:['gitops','settings'], queryFn:()=>api.get<{success:boolean;settings:GitOpsSettings}>('/api/gitops/settings') })
  const reposQ    = useQuery({ queryKey:['git-sync','repos'], queryFn:()=>api.get<{success:boolean;repos:Repo[]}>('/api/git-sync/repos') })
  const credsQ    = useQuery({ queryKey:['git-sync','creds'], queryFn:()=>api.get<{success:boolean;credentials:Cred[]}>('/api/git-sync/credentials') })

  const updateSettings = useMutation({
    mutationFn: (s: Partial<GitOpsSettings>) => api.put('/api/gitops/settings', { ...settingsQ.data?.settings, ...s }),
    onSuccess: () => { toast.success('Settings saved'); qc.invalidateQueries({queryKey:['gitops','settings']}) },
    onError: (e: Error) => toast.error(e.message)
  })

  // Manual Sync
  const syncNow = useMutation({
    mutationFn: () => api.post('/api/gitops/sync', {}),
    onSuccess: () => { toast.success('Sync completed'); onSyncNow() },
    onError: (e: Error) => toast.error(e.message)
  })

  const s = settingsQ.data?.settings
  if (!s) return <Skeleton height={300} />

  const categories = [
    { key: 'sync_storage',    label: 'Storage',      desc: 'ZFS Pools & Datasets', icon: 'storage' },
    { key: 'sync_access',     label: 'Data Access',  desc: 'SMB & NFS Shares',     icon: 'folder_shared' },
    { key: 'sync_app',        label: 'Applications', desc: 'Docker Stacks',        icon: 'apps' },
    { key: 'sync_identity',   label: 'Identity',     desc: 'Users, Groups & LDAP', icon: 'person' },
    { key: 'sync_protection', label: 'Protection',   desc: 'Replication Schedules',icon: 'shield' },
    { key: 'sync_system',     label: 'System',       desc: 'Hostname & Network',   icon: 'settings_ethernet' },
  ]

  const [showWizard, setShowWizard] = useState(false)

  return (
    <div style={{ display:'flex', flexDirection:'column', gap:24 }}>
      {/* GitHub Repo Wizard */}
      {showWizard && (
        <RepoSyncWizard 
          onClose={() => setShowWizard(false)} 
          onComplete={(repoId) => {
            updateSettings.mutate({ repo_id: repoId })
            setShowWizard(false)
          }} 
        />
      )}

      {/* Global Toggle */}
      <div className="card-xl" style={{ display:'flex', alignItems:'center', justifyContent:'space-between', padding:'20px 24px', borderLeft:`4px solid ${s.enabled?'var(--primary)':'var(--border)'}` }}>
        <div>
          <div style={{ fontSize:'var(--text-lg)', fontWeight:700 }}>Enable GitOps Synchronization</div>
          <div style={{ color:'var(--text-tertiary)', fontSize:'var(--text-sm)' }}>Sync local appliance state with a remote Git repository</div>
        </div>
        <button onClick={() => updateSettings.mutate({ enabled: !s.enabled })} className={`btn ${s.enabled?'btn-primary':'btn-ghost'}`}>
          <Icon name={s.enabled?'check_circle':'power_settings_new'} size={16} />
          {s.enabled ? 'Enabled' : 'Disabled'}
        </button>
      </div>

      {/* Manual Sync & Repo Setup */}
      <div style={{ display:'grid', gridTemplateColumns:'1fr 1.5fr', gap:20 }}>
        <div className="card-xl">
          <div style={{ fontWeight:700, marginBottom:16 }}>Fallback Operations</div>
          <button onClick={() => syncNow.mutate()} disabled={syncNow.isPending} className="btn btn-ghost" style={{ width:'100%', justifyContent:'flex-start' }}>
            <Icon name="sync" size={16} />
            {syncNow.isPending ? 'Syncing...' : 'Sync Now (Force Pull/Push)'}
          </button>
          <div style={{ marginTop:12, fontSize:'var(--text-2xs)', color:'var(--text-tertiary)' }}>
            Performed a forced pull, write-back of all current DB state, and push to remote.
          </div>
        </div>

        <div className="card-xl">
          <div style={{ display:'flex', alignItems:'center', justifyContent:'space-between', marginBottom:12 }}>
            <div style={{ fontWeight:700 }}>Source Repository</div>
            <button onClick={() => setShowWizard(true)} className="btn btn-ghost" style={{ fontSize:'var(--text-2xs)', color:'var(--primary)' }}>
              <Icon name="auto_fix_high" size={13} /> Wizard
            </button>
          </div>
          <div style={{ display:'grid', gap:10 }}>
            <label className="field">
              <span className="field-label">Repository</span>
              <select value={s.repo_id || ''} onChange={e=>updateSettings.mutate({ repo_id: Number(e.target.value) })} className="input">
                <option value="">(None)</option>
                {reposQ.data?.repos.map(r => <option key={r.id} value={r.id}>{r.name || r.url}</option>)}
              </select>
            </label>
            <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>
              Select a repository to pull state from and push manual changes to.
            </div>
          </div>
        </div>
      </div>

      {/* Granular Matrix */}
      <div className="card-xl">
        <div style={{ fontWeight:700, marginBottom:16 }}>Granular Resource Sync</div>
        <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fill, minmax(280px, 1fr))', gap:12 }}>
          {categories.map(c => (
            <div key={c.key} style={{ padding:'12px 16px', background:'var(--bg)', borderRadius:'var(--radius-md)', border:'1px solid var(--border)', display:'flex', alignItems:'center', gap:14 }}>
              <Icon name={c.icon} size={20} style={{ color:'var(--text-tertiary)' }} />
              <div style={{ flex:1 }}>
                <div style={{ fontWeight:600, fontSize:'var(--text-sm)' }}>{c.label}</div>
                <div style={{ fontSize:'var(--text-2xs)', color:'var(--text-tertiary)' }}>{c.desc}</div>
              </div>
              <input 
                type="checkbox" 
                checked={!!s[c.key as keyof GitOpsSettings]} 
                onChange={() => updateSettings.mutate({ [c.key]: !s[c.key as keyof GitOpsSettings] })}
                style={{ width:18, height:18, cursor:'pointer' }}
              />
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

function RepoSyncWizard({ onClose, onComplete }: { onClose: () => void; onComplete: (id: number) => void }) {
  const qc = useQueryClient()
  const [step, setStep] = useState(1)
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [tokenName, setTokenName] = useState('GitOps Token')
  const [useExisting, setUseExisting] = useState<string|null>(null)

  const credsQ = useQuery({ queryKey:['git-sync','creds'], queryFn:()=>api.get<{success:boolean;credentials:Cred[]}>('/api/git-sync/credentials') })

  const saveCred = useMutation({
    mutationFn: () => api.post('/api/git-sync/credentials', { name: tokenName, type: 'token', username: 'git', secret: token }),
    onSuccess: (res: any) => { setUseExisting(String(res.id)); setStep(3); qc.invalidateQueries({queryKey:['git-sync','creds']}) },
    onError: (e: Error) => toast.error(e.message)
  })

  const saveRepo = useMutation({
    mutationFn: () => api.post('/api/git-sync/repos', { url, branch: 'main', credential_id: useExisting || undefined }),
    onSuccess: (res: any) => { toast.success('Repository linked'); onComplete(res.id); qc.invalidateQueries({queryKey:['git-sync']}) },
    onError: (e: Error) => toast.error(e.message)
  })

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', backdropFilter:'blur(8px)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }}>
      <div className="card-xl" style={{ width:'100%', maxWidth:500, padding:32, background:'var(--bg-card)', border:'1px solid var(--border-highlight)', position:'relative' }}>
        <button onClick={onClose} style={{ position:'absolute', top:20, right:20, background:'none', border:'none', color:'var(--text-tertiary)', cursor:'pointer' }}><Icon name="close" size={20}/></button>
        
        <div style={{ textAlign:'center', marginBottom:32 }}>
          <Icon name="auto_fix_high" size={40} style={{ color:'var(--primary)', marginBottom:12 }} />
          <h2 style={{ fontSize:'var(--text-xl)', fontWeight:800 }}>GitHub Connect Wizard</h2>
          <div style={{ color:'var(--text-tertiary)', fontSize:'var(--text-sm)' }}>Step {step} of 3</div>
        </div>

        {step === 1 && (
          <div style={{ display:'grid', gap:20 }}>
            <div>
              <div style={{ fontWeight:700, marginBottom:4 }}>Repository URL</div>
              <div style={{ color:'var(--text-tertiary)', fontSize:'var(--text-xs)', marginBottom:12 }}>Public or private GitHub/GitLab repository.</div>
              <input value={url} onChange={e=>setUrl(e.target.value)} placeholder="https://github.com/my-org/infra.git" className="input input-lg" style={{ fontFamily:'var(--font-mono)' }} />
            </div>
            <button onClick={() => setStep(2)} disabled={!url} className="btn btn-primary btn-lg" style={{ width:'100%' }}>Next: Authentication <Icon name="arrow_forward" size={16}/></button>
          </div>
        )}

        {step === 2 && (
          <div style={{ display:'grid', gap:20 }}>
            <div style={{ padding:14, background:'var(--surface)', borderRadius:'var(--radius-lg)' }}>
              <div style={{ fontWeight:700, marginBottom:8 }}>Authentication Method</div>
              <div style={{ display:'grid', gap:8 }}>
                {credsQ.data?.credentials.map(c => (
                  <button key={c.id} onClick={() => { setUseExisting(String(c.id)); setStep(3) }} className="btn btn-ghost" style={{ justifyContent:'flex-start', width:'100%', border:'1px solid var(--border)' }}>
                    <Icon name="key" size={14} /> Use Existing: {c.name}
                  </button>
                ))}
                <div style={{ margin:'10px 0', textAlign:'center', fontSize:'var(--text-2xs)', color:'var(--text-tertiary)', position:'relative' }}>
                  <span style={{ background:'var(--surface)', padding:'0 10px', position:'relative', zIndex:1 }}>OR CREATE NEW</span>
                  <div style={{ position:'absolute', top:'50%', left:0, right:0, height:1, background:'var(--border)' }} />
                </div>
                <label className="field">
                  <span className="field-label">Personal Access Token (PAT)</span>
                  <input type="password" value={token} onChange={e=>setToken(e.target.value)} placeholder="ghp_..." className="input" style={{ fontFamily:'var(--font-mono)' }} />
                </label>
                <button onClick={() => saveCred.mutate()} disabled={!token || saveCred.isPending} className="btn btn-primary" style={{ width:'100%' }}>
                  {saveCred.isPending ? 'Saving...' : 'Create & Continue'}
                </button>
              </div>
            </div>
            <button onClick={() => setStep(3)} className="btn btn-ghost" style={{ width:'100%' }}>Skip (Public Repo)</button>
          </div>
        )}

        {step === 3 && (
          <div style={{ textAlign:'center', display:'grid', gap:20 }}>
            <div className="card" style={{ padding:20, background:'var(--surface)' }}>
              <Icon name="cloud_done" size={32} style={{ color:'var(--success)', marginBottom:12 }} />
              <div style={{ fontWeight:700 }}>Ready to Link</div>
              <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', marginTop:4, wordBreak:'break-all' }}>{url}</div>
            </div>
            <button onClick={() => saveRepo.mutate()} disabled={saveRepo.isPending} className="btn btn-primary btn-lg" style={{ width:'100%' }}>
              {saveRepo.isPending ? 'Linking Repository...' : 'Complete Setup'}
            </button>
          </div>
        )}
      </div>
    </div>
  )
}

type GTab = 'status'|'history'|'settings'
export function GitOpsPage() {
  const qc = useQueryClient()
  const wsOn = useWsStore((s) => s.on)
  const [tab, setTab] = useState<GTab>('status')
  const [stateEdit, setStateEdit] = useState<string|null>(null)
  const [driftAlert, setDriftAlert] = useState<DriftPayload | null>(null)

  const statusQ = useQuery({ queryKey:['gitops','status'], queryFn:({signal})=>api.get<GitopsStatus>('/api/gitops/status',signal), refetchInterval:15_000 })
  const planQ   = useQuery({ queryKey:['gitops','plan'],   queryFn:({signal})=>api.get<{success:boolean;changes:Change[]}>('/api/gitops/plan',signal) })
  const stateQ  = useQuery({ queryKey:['gitops','state'],  queryFn:({signal})=>api.get<{success:boolean;state:string}>('/api/gitops/state',signal) })
  const settingsQ = useQuery({ queryKey:['gitops','settings'], queryFn:()=>api.get<{success:boolean;settings:GitOpsSettings}>('/api/gitops/settings') })

  useEffect(() => {
    return wsOn('gitopsDrift', (data) => {
      const d = data as DriftPayload
      if (d.drifted || d.error) {
        setDriftAlert(d)
        qc.invalidateQueries({ queryKey: ['gitops'] })
      } else {
        setDriftAlert(null)
      }
    })
  }, [wsOn, qc])

  const check   = useMutation({ mutationFn: () => api.post('/api/gitops/check',{}), onSuccess: () => toast.success('Config valid'), onError: (e:Error)=>toast.error(e.message) })
  const apply   = useMutation({ mutationFn: () => api.post('/api/gitops/apply',{}), onSuccess: () => { toast.success('Applied'); setDriftAlert(null); qc.invalidateQueries({queryKey:['gitops']}) }, onError: (e:Error)=>toast.error(e.message) })
  const approve = useMutation({ mutationFn: () => api.post('/api/gitops/approve',{}), onSuccess: () => { toast.success('Approved'); qc.invalidateQueries({queryKey:['gitops']}) }, onError: (e:Error)=>toast.error(e.message) })
  const saveState = useMutation({
    mutationFn: () => api.put('/api/gitops/state', { state: stateEdit }),
    onSuccess: () => { toast.success('State saved'); setStateEdit(null); qc.invalidateQueries({queryKey:['gitops']}) },
    onError: (e:Error)=>toast.error(e.message),
  })

  if (settingsQ.data?.settings && !settingsQ.data.settings.enabled && tab !== 'settings') {
    return (
      <div style={{ maxWidth: 800, margin: '40px auto', textAlign:'center' }}>
        <Icon name="git_repository" size={64} style={{ color:'var(--text-tertiary)', opacity:0.2, marginBottom:24 }} />
        <h1 className="page-title">Unlock Declarative Freedom</h1>
        <p className="page-subtitle" style={{ maxWidth: 500, margin:'0 auto 30px' }}>
          GitOps allows you to manage your entire D-PlaneOS instance from a Git repository.
          Version your config, audit changes, and rollback with ease.
        </p>
        <button onClick={() => setTab('settings')} className="btn btn-primary btn-lg">
          <Icon name="settings" size={18} /> Configure GitOps
        </button>
      </div>
    )
  }

  const TABS = [
    { id:'status' as const, label:'Status', icon:'analytics' },
    { id:'history' as const, label:'History', icon:'history', disabled:true },
    { id:'settings' as const, label:'Settings', icon:'settings' },
  ]

  return (
    <div style={{ maxWidth: 1000 }}>
      {/* Header */}
      <div className="page-header" style={{ display:'flex', alignItems:'flex-end' }}>
        <div style={{ flex:1 }}>
          <h1 className="page-title">GitOps Engine</h1>
          <p className="page-subtitle">Continuous reconciliation between Git and Live state</p>
        </div>
        <div style={{ display:'flex', gap:10, marginBottom:4 }}>
          <button onClick={() => api.post('/api/gitops/sync', {}).then(() => { toast.success('Sync triggered'); qc.invalidateQueries({queryKey:['gitops']}) })} className="btn btn-ghost" title="Manual Sync">
            <Icon name="sync" size={16} /> Sync Now
          </button>
          <button onClick={() => setTab('settings')} className="btn btn-ghost" title="Settings">
            <Icon name="settings" size={16} />
          </button>
        </div>
      </div>

      {/* Tabs */}
      <div className="tabs-underline" style={{ marginBottom:24 }}>
        {TABS.map(t => (
          <button key={t.id} onClick={()=>!t.disabled && setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}${t.disabled?' opacity-30 cursor-not-allowed':''}`}>
            <Icon name={t.icon} size={16}/>{t.label}
          </button>
        ))}
      </div>

      {tab === 'status' && (
        <>
          {driftAlert && (
            <div className={`alert ${driftAlert.blocked_count > 0 ? 'alert-error' : 'alert-warning'}`} style={{ marginBottom: 20 }}>
              <Icon name={driftAlert.blocked_count > 0 ? 'error' : 'warning'} size={16} />
              <span>
                {driftAlert.error
                  ? `GitOps error: ${driftAlert.error}`
                  : <>Drift detected at {fmtDate(driftAlert.checked_at)} — <strong>{driftAlert.create_count + driftAlert.modify_count + driftAlert.delete_count}</strong> pending change(s). {driftAlert.safe_to_apply ? 'Safe to apply.' : 'Manual review required.'}</>
                }
              </span>
              <button onClick={() => setDriftAlert(null)} style={{ marginLeft:'auto', background:'none', border:'none', cursor:'pointer', color:'inherit' }}><Icon name="close" size={15} /></button>
            </div>
          )}

          {statusQ.data && (
            <div style={{ display:'flex', alignItems:'center', gap:14, padding:'14px 20px', background:'var(--bg-card)', border:`1px solid ${statusQ.data.drift?'var(--warning-border)':'var(--border)'}`, borderRadius:'var(--radius-lg)', marginBottom:24 }}>
              <Icon name={statusQ.data.drift ? 'warning' : 'check_circle'} size={20} style={{ color: statusQ.data.drift ? 'var(--warning)' : 'var(--success)' }} />
              <div style={{ flex:1 }}>
                <span style={{ fontWeight:700 }}>{statusQ.data.state || 'Synchronized'}</span>
                {statusQ.data.drift && <span style={{ marginLeft:10, fontSize:'var(--text-xs)', color:'var(--warning)' }}>Changes detected</span>}
                {statusQ.data.last_applied && <span style={{ marginLeft:10, fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>Last applied: {fmtDate(statusQ.data.last_applied)}</span>}
              </div>
              <div style={{ display:'flex', gap:8 }}>
                <button onClick={()=>check.mutate()} disabled={check.isPending} className="btn btn-ghost"><Icon name="fact_check" size={14}/>Config Check</button>
                <button onClick={()=>apply.mutate()} disabled={apply.isPending} className="btn btn-primary"><Icon name="rocket_launch" size={14}/>Deploy Now</button>
              </div>
            </div>
          )}

          <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr', gap:20 }}>
            <div>
              <div style={{ fontWeight:700, marginBottom:12, display:'flex', alignItems:'center', gap:8 }}><Icon name="list_alt" size={18} style={{ color:'var(--primary)' }}/>Pending Plan</div>
              <div style={{ display:'flex', flexDirection:'column', gap:6 }}>
                {(planQ.data?.changes ?? []).map((c, i) => (
                  <div key={i} style={{ display:'flex', alignItems:'flex-start', gap:10, padding:'10px 14px', background:'var(--bg-card)', border:`1px solid ${changeColor(c.action)}20`, borderRadius:'var(--radius-md)' }}>
                    <span style={{ padding:'2px 6px', borderRadius:'var(--radius-sm)', background:`${changeColor(c.action)}18`, color:changeColor(c.action), fontSize:'var(--text-2xs)', fontWeight:700, textTransform:'uppercase', flexShrink:0, marginTop:1 }}>{c.action}</span>
                    <div style={{ fontWeight:600, fontSize:'var(--text-sm)' }}>{c.resource}</div>
                  </div>
                ))}
                {(planQ.data?.changes ?? []).length === 0 && <div className="card" style={{ textAlign:'center', padding:'40px 0', opacity:0.5 }}>Zero drift between Git and Live state</div>}
              </div>
            </div>
            <div>
              <div style={{ fontWeight:700, marginBottom:12, display:'flex', alignItems:'center', gap:8 }}><Icon name="code" size={18} style={{ color:'var(--primary)' }}/>Live Manifest</div>
              <pre className="card" style={{ background: 'var(--surface)', padding:'12px 14px', fontFamily:'var(--font-mono)', fontSize:11, maxHeight:320, overflow:'auto', whiteSpace:'pre-wrap' }}>
                {stateQ.data?.state || '(empty)'}
              </pre>
            </div>
          </div>
        </>
      )}

      {tab === 'settings' && <SettingsTab onSyncNow={() => qc.invalidateQueries({queryKey:['gitops']})} />}
    </div>
  )
}
