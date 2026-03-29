import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { useWsStore } from '@/stores/ws'
import { useJobStore } from '@/stores/jobs'

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

function fmtDate(s?:string){if(!s)return'-';try{return new Date(s).toLocaleString('de-DE',{dateStyle:'short',timeStyle:'short'})}catch{return s}}

function changeColor(a?:string):string {
  if (a === 'create') return 'var(--success)'
  if (a === 'delete') return 'var(--error)'
  return 'var(--warning)'
}

interface GitOpsSettings {
  enabled: boolean;
  repo_id: number | null;
  nixos_repo_id: number | null;
  state_path: string;
  sync_storage: boolean;
  sync_access: boolean;
  sync_app: boolean;
  sync_identity: boolean;
  sync_protection: boolean;
  sync_system: boolean;
  updated_at: string;
}

interface Repo { id: string|number; url: string; branch?: string; path?: string; name?: string; status?: string; last_sync?: string; credential_id?: string|number }
interface Cred { id: string|number; name: string; type?: 'ssh'|'token'|'password'; username?: string }

function statusDot(s?: string) {
  const c = s === 'synced' ? 'var(--success)' : s === 'syncing' ? 'var(--primary)' : s === 'error' ? 'var(--error)' : 'var(--text-tertiary)'
  return <span style={{ width:8, height:8, borderRadius:'50%', background:c, boxShadow:s==='syncing'?`0 0 6px ${c}`:'none', display:'inline-block', flexShrink:0 }} />
}

function SettingsTab({ s, updateSettings, repos, setShowWizard, onSyncNow }: { 
  s: GitOpsSettings; 
  updateSettings: any; 
  repos: Repo[]; 
  setShowWizard: (t: 'state'|'nixos') => void;
  onSyncNow: () => void;
}) {
  const categories = [
    { key: 'sync_storage',    label: 'Storage',      desc: 'ZFS Pools & Datasets', icon: 'storage' },
    { key: 'sync_access',     label: 'Data Access',  desc: 'SMB & NFS Shares',     icon: 'folder_shared' },
    { key: 'sync_app',        label: 'Applications', desc: 'Docker Stacks',        icon: 'apps' },
    { key: 'sync_identity',   label: 'Identity',     desc: 'Users, Groups & LDAP', icon: 'person' },
    { key: 'sync_protection', label: 'Protection',   desc: 'Replication Schedules',icon: 'shield' },
    { key: 'sync_system',     label: 'System',       desc: 'Hostname & Network',   icon: 'settings_ethernet' },
  ]
  const syncNow = useMutation({
    mutationFn: () => api.post('/api/gitops/sync', {}),
    onSuccess: () => { toast.success('Sync completed'); onSyncNow() },
    onError: (e: Error) => toast.error(e.message)
  })

  return (
    <div style={{ display:'flex', flexDirection:'column', gap:24 }}>
      {/* Manual Sync & Repo Setup */}

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
      <div style={{ display:'grid', gridTemplateColumns:'1fr 1fr 1fr', gap:20 }}>
        <div className="card-xl">
          <div style={{ fontWeight:700, marginBottom:16 }}>Fallback Operations</div>
          <button onClick={() => syncNow.mutate()} disabled={syncNow.isPending} className="btn btn-ghost" style={{ width:'100%', justifyContent:'flex-start' }}>
            <Icon name="sync" size={16} />
            {syncNow.isPending ? 'Syncing...' : 'Sync State Now'}
          </button>
          <div style={{ marginTop:12, fontSize:'var(--text-2xs)', color:'var(--text-tertiary)' }}>
            Force pull, write-back all current DB state, and push to remote.
          </div>
        </div>

        <div className="card-xl">
          <div style={{ display:'flex', alignItems:'center', justifyContent:'space-between', marginBottom:12 }}>
            <div style={{ fontWeight:700 }}>System State</div>
            <button onClick={() => setShowWizard('state')} className="btn btn-ghost" style={{ fontSize:'var(--text-2xs)', color:'var(--primary)' }}>
              <Icon name="auto_fix_high" size={13} /> Wizard
            </button>
          </div>
          <div style={{ display:'grid', gap:10 }}>
            <label className="field">
              <span className="field-label">Repository</span>
              <select value={s.repo_id || ''} onChange={e=>updateSettings.mutate({ repo_id: e.target.value ? Number(e.target.value) : null })} className="input">
                <option value="">(None)</option>
                {repos.map(r => <option key={r.id} value={r.id}>{r.name || r.url}</option>)}
              </select>
            </label>
            <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>
              Contains <code>state.yaml</code> with storage, apps, and users config.
            </div>
          </div>
        </div>

        <div className="card-xl">
          <div style={{ display:'flex', alignItems:'center', justifyContent:'space-between', marginBottom:12 }}>
            <div style={{ fontWeight:700 }}>Base System</div>
            <button onClick={() => setShowWizard('nixos')} className="btn btn-ghost" style={{ fontSize:'var(--text-2xs)', color:'var(--primary)' }}>
              <Icon name="auto_fix_high" size={13} /> Wizard
            </button>
          </div>
          <div style={{ display:'grid', gap:10 }}>
            <label className="field">
              <span className="field-label">Repository</span>
              <select value={s.nixos_repo_id || ''} onChange={e=>updateSettings.mutate({ nixos_repo_id: e.target.value ? Number(e.target.value) : null })} className="input">
                <option value="">(None)</option>
                {repos.map(r => <option key={r.id} value={r.id}>{r.name || r.url}</option>)}
              </select>
            </label>
            <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>
              Backs up <code>/etc/nixos</code> (configuration.nix and flake).
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

// ---- RepoCard (Merged from GitSync) ----
function RepoCard({ repo, onRefresh, onEdit }: { repo: Repo; onRefresh: () => void; onEdit: (r: Repo) => void }) {
  const { confirm, ConfirmDialog } = useConfirm()
  const pull   = useMutation({ mutationFn: () => api.post('/api/git-sync/repos/pull',   { id: repo.id }), onSuccess: () => { toast.success('Pull triggered'); onRefresh() }, onError: (e: Error) => toast.error(e.message) })
  const push   = useMutation({ mutationFn: () => api.post('/api/git-sync/repos/push',   { id: repo.id }), onSuccess: () => { toast.success('Push triggered'); onRefresh() }, onError: (e: Error) => toast.error(e.message) })
  // Deploy is for Docker stacks
  const deploy = useMutation({ mutationFn: () => api.post('/api/git-sync/repos/deploy', { id: repo.id }), onSuccess: () => toast.success('Deployment queued'), onError: (e: Error) => toast.error(e.message) })
  const del    = useMutation({ mutationFn: () => api.post('/api/git-sync/repos/delete', { id: repo.id }), onSuccess: () => { toast.success('Repository removed'); onRefresh() }, onError: (e: Error) => toast.error(e.message) })
  const busy   = pull.isPending || push.isPending || deploy.isPending || del.isPending

  return (
    <div className="card card-xl" style={{ position:'relative', borderLeft:`4px solid ${repo.status==='error'?'var(--error)':'var(--border)'}` }}>
      <div style={{ display:'flex', alignItems:'flex-start', gap:12, marginBottom:16 }}>
        {statusDot(repo.status)}
        <div style={{ flex:1, minWidth:0 }}>
          <div style={{ fontWeight:700, fontSize:'var(--text-sm)' }}>{repo.name || repo.url.split('/').pop()?.replace('.git','') || repo.url}</div>
          <div style={{ fontSize:'var(--text-2xs)', color:'var(--text-tertiary)', fontFamily:'var(--font-mono)', marginTop:2, overflow:'hidden', textOverflow:'ellipsis', whiteSpace:'nowrap' }}>{repo.url}</div>
          <div style={{ fontSize:'var(--text-2xs)', color:'var(--text-tertiary)', marginTop:2 }}>
            branch: {repo.branch || 'main'}{repo.path && ` · ${repo.path}`}{repo.last_sync && ` · synced ${fmtDate(repo.last_sync)}`}
          </div>
        </div>
      </div>
      <div style={{ display:'flex', gap:6, flexWrap:'wrap' }}>
        <button onClick={() => pull.mutate()}   disabled={busy} className="btn btn-ghost btn-xs"><Icon name="download" size={13} />Pull</button>
        <button onClick={() => push.mutate()}   disabled={busy} className="btn btn-ghost btn-xs"><Icon name="upload" size={13} />Push</button>
        {repo.path && <button onClick={() => deploy.mutate()} disabled={busy} className="btn btn-ghost btn-xs"><Icon name="rocket_launch" size={13} />Deploy Stack</button>}
        <button onClick={() => onEdit(repo)} className="btn btn-ghost btn-xs"><Icon name="edit" size={13} />Edit</button>
        <button onClick={async () => { if (await confirm({ title: 'Remove Repository', message: 'Stop tracking this Git repository? Local data will not be deleted.', danger: true, confirmLabel: 'Remove' })) del.mutate() }} disabled={busy}
          className="btn btn-ghost btn-xs" style={{ color:'var(--error)', marginLeft:'auto' }}>
          <Icon name="delete" size={13} />
        </button>
      </div>
      <ConfirmDialog />
    </div>
  )
}

// ---- Repositories Tab ----
function RepositoriesTab({ repos, isLoading, onWizardClick, onRefresh, onEdit }: { repos: Repo[]; isLoading: boolean; onWizardClick: (type: 'state'|'nixos') => void; onRefresh: () => void; onEdit: (r: Repo) => void }) {

  if (isLoading) return <Skeleton height={200} />

  return (
    <div style={{ display:'flex', flexDirection:'column', gap:24 }}>
      <div style={{ display:'flex', alignItems:'center', justifyContent:'space-between' }}>
        <div>
          <h3 style={{ fontWeight:700 }}>Tracked Repositories</h3>
          <p style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>Linked Git sources for State, OS configuration, and Stacks</p>
        </div>
        <div style={{ display:'flex', gap:10 }}>
          <button onClick={() => onWizardClick('state')} className="btn btn-primary"><Icon name="auto_fix_high" size={14} />Link Repository</button>
        </div>
      </div>

      <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fill, minmax(320px, 1fr))', gap:16 }}>
        {repos.map(repo => <RepoCard key={repo.id} repo={repo} onRefresh={onRefresh} onEdit={onEdit} />)}
        {repos.length === 0 && (
          <div style={{ gridColumn:'1/-1', textAlign:'center', padding:'60px 0', border:'1px dashed var(--border)', borderRadius:'var(--radius-xl)', opacity:0.5 }}>
            <Icon name="source" size={48} style={{ marginBottom:12 }} />
            <div>No repositories linked yet</div>
          </div>
        )}
      </div>
    </div>
  )
}

// ---- Credentials Tab ----
function CredentialsTab() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [editingCred, setEditingCred] = useState<Cred | null>(null)
  const [name, setName] = useState(''); const [type, setType] = useState<'token'|'ssh'|'password'>('token')
  const [user, setUser] = useState(''); const [secret, setSecret] = useState('')

  const credsQ = useQuery({ queryKey:['git-sync','creds'], queryFn:({signal})=>api.get<{success:boolean;credentials:Cred[]}>('/api/git-sync/credentials',signal) })

  const resetForm = () => { setEditingCred(null); setName(''); setUser(''); setSecret(''); setType('token') }

  const save = useMutation({
    mutationFn: () => api.post('/api/git-sync/credentials', { id: editingCred?.id, name, type, username: user, secret: secret || undefined }),
    onSuccess: () => { toast.success(editingCred ? 'Credential updated' : 'Credential saved'); resetForm(); qc.invalidateQueries({ queryKey:['git-sync','creds'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const onEdit = (c: Cred) => {
    setEditingCred(c)
    setName(String(c.name))
    setType(c.type || 'token')
    setUser(c.username || '')
    setSecret('') // don't show old secret for security, user can re-enter if changing
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  const del = useMutation({
    mutationFn: (id: string|number) => api.post('/api/git-sync/credentials/delete', { id }),
    onSuccess: () => { toast.success('Removed'); qc.invalidateQueries({ queryKey:['git-sync','creds'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const test = useMutation({
    mutationFn: (id: string|number) => api.post('/api/git-sync/credentials/test', { id }),
    onSuccess: () => toast.success('Credential functional'),
    onError: (e: Error) => toast.error(e.message),
  })

  const creds = credsQ.data?.credentials ?? []

  return (
    <div style={{ display:'flex', flexDirection:'column', gap:24 }}>
      <div className="card-xl" style={{ borderLeft: editingCred ? '4px solid var(--primary)' : 'none' }}>
        <div style={{ fontWeight:700, marginBottom:20 }}>{editingCred ? 'Edit Access Credential' : 'Add Access Credential'}</div>
        <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fit, minmax(200px, 1fr)) auto', gap:14, alignItems:'flex-end' }}>
          <label className="field">
            <span className="field-label">Label</span>
            <input value={name} onChange={e=>setName(e.target.value)} placeholder="e.g. My GitHub PAT" className="input" />
          </label>
          <label className="field">
            <span className="field-label">Auth Type</span>
            <select value={type} onChange={e=>setType(e.target.value as any)} className="input" style={{ appearance:'none' }}>
              <option value="token">Token</option><option value="ssh">SSH Private Key</option><option value="password">Password</option>
            </select>
          </label>
          <label className="field">
            <span className="field-label">{type === 'ssh' ? 'Private Key / Content' : 'Secret / Token'} {editingCred && '(Leave empty to keep current)'}</span>
            <input type="password" value={secret} onChange={e=>setSecret(e.target.value)} className="input" style={{ fontFamily:'var(--font-mono)' }} />
          </label>
          <div style={{ display:'flex', gap:8 }}>
            {editingCred && <button onClick={resetForm} className="btn btn-ghost">Cancel</button>}
            <button onClick={() => save.mutate()} disabled={!name || (!secret && !editingCred) || save.isPending} className="btn btn-primary">
              <Icon name={editingCred ? 'save' : 'add'} size={14} /> {save.isPending ? 'Saving...' : editingCred ? 'Update' : 'Add Credential'}
            </button>
          </div>
        </div>
      </div>

      <div style={{ display:'flex', flexDirection:'column', gap:10 }}>
        {creds.map(c => (
          <div key={c.id} className="card-xl" style={{ display:'flex', alignItems:'center', gap:16, padding:'16px 20px' }}>
            <Icon name="key" size={24} style={{ color:'var(--primary)', opacity:0.8 }} />
            <div style={{ flex:1 }}>
              <div style={{ fontWeight:700 }}>{c.name}</div>
              <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>{c.type} · {c.username || 'git'}</div>
            </div>
            <button onClick={() => onEdit(c)} className="btn btn-ghost btn-xs"><Icon name="edit" size={14}/> Edit</button>
            <button onClick={() => test.mutate(c.id)} disabled={test.isPending} className="btn btn-ghost btn-xs"><Icon name="cable" size={14}/> Test</button>
            <button onClick={async () => { if (await confirm({ title: 'Remove Credential', message: `Remove ${c.name}? Repositories using this credential will lose access.`, danger: true })) del.mutate(c.id) }} className="btn btn-ghost btn-xs" style={{ color:'var(--error)' }}><Icon name="delete" size={14}/></button>
          </div>
        ))}
        {creds.length === 0 && <div className="card" style={{ textAlign:'center', padding:40, opacity:0.5 }}>No stored credentials</div>}
      </div>
      <ConfirmDialog />
    </div>
  )
}

function RepoSyncWizard({ type, onClose, onComplete }: { type: 'state'|'nixos'; onClose: () => void; onComplete: (id: number) => void }) {
  const qc = useQueryClient()
  const [step, setStep] = useState(1)
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [useExisting, setUseExisting] = useState<string|null>(null)
  const [isTesting, setIsTesting] = useState(false)

  const tokenName = type === 'nixos' ? 'NixOS Base System Token' : 'Infrastructure State Token'
  const repoName = type === 'nixos' ? `NixOS-Backup-${Math.floor(Math.random()*1000)}` : `System-State-${Math.floor(Math.random()*1000)}`

  const credsQ = useQuery({ queryKey:['git-sync','creds'], queryFn:()=>api.get<{success:boolean;credentials:Cred[]}>('/api/git-sync/credentials') })

  const saveCred = useMutation({
    mutationFn: () => api.post('/api/git-sync/credentials', { name: tokenName, type: 'token', username: 'git', secret: token }),
    onSuccess: (res: any) => { setUseExisting(String(res.id)); setStep(3); qc.invalidateQueries({queryKey:['git-sync','creds']}) },
    onError: (e: Error) => toast.error(e.message)
  })

  const testConnection = async () => {
    setIsTesting(true)
    try {
      const res = await api.post<{success:boolean;error?:string;hint?:string}>('/api/git-sync/credentials/test', { 
        url, 
        type: 'token', 
        secret: token || undefined,
        credential_id: useExisting || undefined
      })
      if (res.success) {
        toast.success('Connection successful!')
        setStep(3)
      } else {
        toast.error(`Connection failed: ${res.error}`)
      }
    } catch (e: any) {
      toast.error(e.message)
    } finally {
      setIsTesting(false)
    }
  }

  const saveRepo = useMutation({
    mutationFn: () => api.post('/api/git-sync/repos', { 
      name: repoName,
      url, 
      branch: 'main', 
      credential_id: useExisting || undefined 
    }),
    onSuccess: (res: any) => { toast.success('Repository linked'); onComplete(res.id); qc.invalidateQueries({queryKey:['git-sync']}) },
    onError: (e: Error) => toast.error(e.message)
  })

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', backdropFilter:'blur(8px)', zIndex:1000, display:'flex', alignItems:'center', justifyContent:'center', padding:20 }}>
      <div className="card-xl" style={{ width:'100%', maxWidth:500, padding:32, background:'var(--bg-card)', border:'1px solid var(--border-highlight)', position:'relative' }}>
        <button onClick={onClose} style={{ position:'absolute', top:20, right:20, background:'none', border:'none', color:'var(--text-tertiary)', cursor:'pointer' }}><Icon name="close" size={20}/></button>
        
        <div style={{ textAlign:'center', marginBottom:32 }}>
          <Icon name={type === 'nixos' ? 'settings_suggest' : 'auto_fix_high'} size={40} style={{ color:'var(--primary)', marginBottom:12 }} />
          <h2 style={{ fontSize:'var(--text-xl)', fontWeight:800 }}>{type === 'nixos' ? 'Base System' : 'System State'} Link</h2>
          <div style={{ color:'var(--text-tertiary)', fontSize:'var(--text-sm)' }}>Step {step} of 3</div>
        </div>

        {step === 1 && (
          <div style={{ display:'grid', gap:20 }}>
            <div>
              <div style={{ fontWeight:700, marginBottom:4 }}>Repository URL</div>
              <div style={{ color:'var(--text-tertiary)', fontSize:'var(--text-xs)', marginBottom:12 }}>Public or private Git repository (HTTPS).</div>
              <input value={url} onChange={e=>setUrl(e.target.value)} placeholder="https://github.com/my-org/infra.git" className="input input-lg" style={{ fontFamily:'var(--font-mono)' }} />
            </div>
            <button onClick={() => setStep(2)} disabled={!url} className="btn btn-primary btn-lg" style={{ width:'100%' }}>Next: Authentication <Icon name="arrow_forward" size={16}/></button>
          </div>
        )}

        {step === 2 && (
          <div style={{ display:'grid', gap:20 }}>
            <div style={{ padding:14, background:'var(--surface)', borderRadius:'var(--radius-lg)' }}>
              <div style={{ fontWeight:700, marginBottom:8 }}>Authentication</div>
              <div style={{ display:'grid', gap:8 }}>
                {credsQ.data?.credentials.map(c => (
                  <button key={c.id} onClick={() => { setUseExisting(String(c.id)); }} className={`btn ${useExisting === String(c.id) ? 'btn-primary' : 'btn-ghost'}`} style={{ justifyContent:'flex-start', width:'100%', border:'1px solid var(--border)' }}>
                    <Icon name="key" size={14} /> Use Existing: {c.name}
                  </button>
                ))}
                <div style={{ margin:'10px 0', textAlign:'center', fontSize:'var(--text-2xs)', color:'var(--text-tertiary)', position:'relative' }}>
                  <span style={{ background:'var(--surface)', padding:'0 10px', position:'relative', zIndex:1 }}>OR NEW TOKEN</span>
                  <div style={{ position:'absolute', top:'50%', left:0, right:0, height:1, background:'var(--border)' }} />
                </div>
                <label className="field">
                  <span className="field-label">Personal Access Token (PAT)</span>
                  <input type="password" value={token} onChange={e=>{setToken(e.target.value); setUseExisting(null)}} placeholder="ghp_..." className="input" style={{ fontFamily:'var(--font-mono)' }} />
                </label>
                <div style={{ display:'flex', gap:10, marginTop:10 }}>
                  <button onClick={testConnection} disabled={(!token && !useExisting) || isTesting} className="btn btn-ghost" style={{ flex:1 }}>
                    {isTesting ? 'Testing...' : 'Test Connection'}
                  </button>
                  <button onClick={() => { if (token) saveCred.mutate(); else setStep(3) }} disabled={(!token && !useExisting) || saveCred.isPending} className="btn btn-primary" style={{ flex:1 }}>
                    Next <Icon name="arrow_forward" size={14} />
                  </button>
                </div>
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

type GTab = 'status'|'repos'|'credentials'|'settings'
function RepoEditModal({ repo, onExited, onSaved }: { repo: Repo; onExited: () => void; onSaved: () => void }) {
  const [name, setName] = useState(repo.name)
  const [branch, setBranch] = useState(repo.branch || 'main')
  const [credId, setCredId] = useState<string|number|null>(repo.credential_id || null)
  
  const credsQ = useQuery({ queryKey:['git-sync','creds'], queryFn:()=>api.get<{success:boolean;credentials:Cred[]}>('/api/git-sync/credentials') })

  const save = useMutation({
    mutationFn: (vals: Partial<Repo>) => api.post('/api/git-sync/repos', { ...repo, id: repo.id, ...vals }),
    onSuccess: () => { toast.success('Repository updated'); onSaved(); onExited() },
    onError: (e: Error) => toast.error(e.message)
  })

  return (
    <div className="modal-overlay" style={{ display:'flex', alignItems:'center', justifyContent:'center' }}>
      <div className="card-xl" style={{ width: 450, padding: 30 }}>
        <div style={{ fontSize:'var(--text-lg)', fontWeight:700, marginBottom:20 }}>Edit Repository</div>
        
        <div style={{ display:'flex', flexDirection:'column', gap:16 }}>
          <label className="field">
            <span className="field-label">Repository Name</span>
            <input className="input" value={name} onChange={e=>setName(e.target.value)} />
            <div style={{ fontSize:10, opacity:0.5, marginTop:4 }}>Changing name will update local clone path.</div>
          </label>

          <label className="field">
            <span className="field-label">Branch</span>
            <input className="input" value={branch} onChange={e=>setBranch(e.target.value)} />
          </label>

          <label className="field">
            <span className="field-label">Credential</span>
            <select className="input" value={credId || ''} onChange={e=>setCredId(e.target.value ? e.target.value : null)}>
              <option value="">None / Public</option>
              {credsQ.data?.credentials.map(c => (
                <option key={c.id} value={c.id}>{c.name}</option>
              ))}
            </select>
          </label>

          <div style={{ display:'flex', gap:12, marginTop:10, justifyContent:'flex-end' }}>
            <button onClick={onExited} className="btn btn-ghost">Cancel</button>
            <button onClick={() => save.mutate({ name, branch, credential_id: credId || undefined })} disabled={save.isPending} className="btn btn-primary">
              {save.isPending ? 'Saving...' : 'Save Changes'}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

export function GitOpsPage() {
  const qc = useQueryClient()
  const wsOn = useWsStore((s) => s.on)
  const [tab, setTab] = useState<GTab>('status')
  const [driftAlert, setDriftAlert] = useState<DriftPayload | null>(null)
  const [editingRepo, setEditingRepo] = useState<Repo | null>(null)

  const statusQ = useQuery({ queryKey:['gitops','status'], queryFn:({signal})=>api.get<GitopsStatus>('/api/gitops/status',signal), refetchInterval:15_000 })
  const planQ   = useQuery({ queryKey:['gitops','plan'],   queryFn:({signal})=>api.get<{success:boolean;changes:Change[]}>('/api/gitops/plan',signal) })
  const stateQ  = useQuery({ queryKey:['gitops','state'],  queryFn:({signal})=>api.get<{success:boolean;state:string}>('/api/gitops/state',signal) })
  const settingsQ = useQuery({ queryKey:['gitops','settings'], queryFn:()=>api.get<{success:boolean;settings:GitOpsSettings}>('/api/gitops/settings') })
  const reposQ    = useQuery({ queryKey:['git-sync','repos'], queryFn:({signal})=>api.get<{success:boolean;repos:Repo[]}>('/api/git-sync/repos',signal) })

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

  const [showWizard, setShowWizard] = useState<'state'|'nixos'|null>(null)
  const updateSettings = useMutation({
    mutationFn: (vals: Partial<GitOpsSettings>) => api.put('/api/gitops/settings', { ...settingsQ.data?.settings, ...vals }),
    onSuccess: () => { toast.success('Settings saved'); qc.invalidateQueries({queryKey:['gitops','settings']}) },
    onError: (e: Error) => toast.error(e.message)
  })

  const check   = useMutation({ mutationFn: () => api.post('/api/gitops/check',{}), onSuccess: () => toast.success('Config valid'), onError: (e:Error)=>toast.error(e.message) })
  const setJob = useJobStore((s) => s.setActiveJob)

  const apply   = useMutation({ 
    mutationFn: () => api.post<{ success: boolean; job_id: string }>('/api/gitops/apply',{}), 
    onSuccess: (data) => { 
      toast.success('Reconciliation Started'); 
      setDriftAlert(null); 
      setJob(data.job_id, 'System Reconciliation');
      qc.invalidateQueries({queryKey:['gitops']}) 
    }, 
    onError: (e:Error)=>toast.error(e.message) 
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
    { id:'status' as const,       label:'Status',       icon:'analytics' },
    { id:'repos' as const,        label:'Repositories', icon:'source' },
    { id:'credentials' as const,  label:'Credentials',  icon:'key' },
    { id:'settings' as const,     label:'Settings',     icon:'settings' },
  ]

  return (
    <div style={{ maxWidth: 1000 }}>
      {showWizard && (
        <RepoSyncWizard 
          type={showWizard}
          onClose={() => setShowWizard(null)} 
          onComplete={(repoId) => {
            if (showWizard === 'state') updateSettings.mutate({ repo_id: repoId })
            else updateSettings.mutate({ nixos_repo_id: repoId })
            setShowWizard(null)
          }} 
        />
      )}
      {/* Header */}
      <div className="page-header" style={{ display:'flex', alignItems:'flex-end' }}>
        <div style={{ flex:1 }}>
          <h1 className="page-title">GitOps Engine</h1>
          <p className="page-subtitle">Continuous reconciliation between Git and Live state (State & Base System)</p>
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
          <button key={t.id} onClick={()=>setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16}/>{t.label}
          </button>
        ))}
      </div>

      {tab === 'status'      && (
        <>
          {driftAlert && (
            <div className={`alert ${driftAlert.blocked_count > 0 ? 'alert-error' : 'alert-warning'}`} style={{ marginBottom: 20 }}>
              <Icon name={driftAlert.blocked_count > 0 ? 'error' : 'warning'} size={16} />
              <span>
                {driftAlert.error
                  ? `GitOps error: ${driftAlert.error}`
                  : <>Drift detected at {fmtDate(driftAlert.checked_at)} - <strong>{driftAlert.create_count + driftAlert.modify_count + driftAlert.delete_count}</strong> pending change(s). {driftAlert.safe_to_apply ? 'Safe to apply.' : 'Manual review required.'}</>
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

      {tab === 'repos'       && (
        <RepositoriesTab 
          repos={reposQ.data?.repos ?? []} 
          isLoading={reposQ.isLoading}
          onWizardClick={(type: 'state'|'nixos') => { setTab('settings'); setShowWizard(type) }} 
          onRefresh={() => qc.invalidateQueries({ queryKey:['git-sync'] })}
          onEdit={setEditingRepo}
        />
      )}
      {tab === 'credentials' && <CredentialsTab />}
      {tab === 'settings'    && settingsQ.data && (
        <SettingsTab 
          s={settingsQ.data.settings} 
          updateSettings={updateSettings}
          repos={reposQ.data?.repos ?? []}
          setShowWizard={setShowWizard}
          onSyncNow={() => qc.invalidateQueries({queryKey:['gitops']})} 
        />
      )}

      {editingRepo && (
        <RepoEditModal 
          repo={editingRepo} 
          onExited={() => setEditingRepo(null)} 
          onSaved={() => qc.invalidateQueries({ queryKey:['git-sync','repos'] })}
        />
      )}
    </div>
  )
}

