import { useState, useEffect, useRef } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { fmtDateTime } from '@/lib/fmt'
import { Icon } from '@/components/ui/Icon'
import { ConfigRepository } from '@/components/gitops/ConfigRepository'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'

function fmtDate(s?:string){if(!s)return'-';return fmtDateTime(s)}

// Parses GitHub/GitLab/Gitea/Bitbucket branch URLs into { repoUrl, branch }.
// Returns the raw string unchanged and branch=null for plain repo URLs.
function normalizeRepoUrl(u: string) { return u.replace(/\.git$/, '') + '.git' }
function parseGitUrl(raw: string): { repoUrl: string; branch: string | null } {
  const s = raw.trim()
  let m: RegExpMatchArray | null
  // GitHub: https://github.com/user/repo/tree/branch[/...]
  m = s.match(/^(https?:\/\/github\.com\/[^/]+\/[^/]+)\/tree\/([^/?#]+)/)
  if (m) return { repoUrl: normalizeRepoUrl(m[1]), branch: decodeURIComponent(m[2]) }
  // GitLab: https://gitlab.com/user/repo/-/tree/branch
  m = s.match(/^(https?:\/\/[^/]+\/[^/]+\/[^/]+)\/-\/tree\/([^/?#]+)/)
  if (m) return { repoUrl: normalizeRepoUrl(m[1]), branch: decodeURIComponent(m[2]) }
  // Gitea: https://gitea.example.com/user/repo/src/branch/branch-name
  m = s.match(/^(https?:\/\/[^/]+\/[^/]+\/[^/]+)\/src\/branch\/([^/?#]+)/)
  if (m) return { repoUrl: normalizeRepoUrl(m[1]), branch: decodeURIComponent(m[2]) }
  // Bitbucket: https://bitbucket.org/user/repo/src/branch-name
  m = s.match(/^(https?:\/\/bitbucket\.org\/[^/]+\/[^/]+)\/src\/([^/?#]+)/)
  if (m) return { repoUrl: normalizeRepoUrl(m[1]), branch: decodeURIComponent(m[2]) }
  return { repoUrl: s, branch: null }
}


interface Repo { id: string|number; url: string; branch?: string; path?: string; name?: string; status?: string; last_sync?: string; credential_id?: string|number; raw?: ApiRepo }
interface Cred { id: string|number; name: string; type?: 'ssh'|'token'; host?: string; username?: string }

// The git-sync API's field names (repo_url, cred_id, last_sync_at, auth_type).
// Converted at the edges so the components keep their own shapes.
interface ApiRepo {
  id: number; name?: string; repo_url: string; branch?: string; compose_path?: string
  last_sync_at?: string; last_error?: string; cred_id?: number | null
  auto_sync?: boolean; sync_interval?: number; commit_name?: string; commit_email?: string; enabled?: boolean
}
interface ApiCred { id: number; name: string; host?: string; auth_type?: 'token'|'ssh'; username?: string }

function fromApiRepo(r: ApiRepo): Repo {
  return {
    id: r.id, name: r.name, url: r.repo_url, branch: r.branch, path: r.compose_path,
    last_sync: r.last_sync_at || undefined, credential_id: r.cred_id ?? undefined,
    status: r.last_error ? 'error' : r.last_sync_at ? 'synced' : undefined, raw: r,
  }
}

// The API replaces every field on save: start from what it returned.
function toApiRepo(r: Repo, vals: Partial<Repo> = {}) {
  const m = { ...r, ...vals }
  const cred = m.credential_id
  return {
    ...(r.raw ?? {}),
    id: Number(m.id), name: m.name, repo_url: m.url, branch: m.branch,
    compose_path: m.path ?? r.raw?.compose_path,
    credential_id: cred !== undefined && cred !== null && cred !== '' ? Number(cred) : undefined,
    cred_id: undefined, last_sync_at: undefined, last_error: undefined,
  }
}

function fromApiCred(c: ApiCred): Cred {
  return { id: c.id, name: c.name, type: c.auth_type, host: c.host, username: c.username }
}

function hostOf(url: string): string {
  const m = url.match(/^(?:https?:\/\/|ssh:\/\/)?(?:[^@/]+@)?([^/:]+)/)
  return m ? m[1] : 'github.com'
}

function statusDot(s?: string) {
  const c = s === 'synced' ? 'var(--success)' : s === 'syncing' ? 'var(--primary)' : s === 'error' ? 'var(--error)' : 'var(--text-tertiary)'
  return <span style={{ width:8, height:8, borderRadius:'50%', background:c, boxShadow:s==='syncing'?`0 0 6px ${c}`:'none', display:'inline-block', flexShrink:0 }} />
}

// CategoryCard maps sync_* settings keys to their capturable category names.
// Storage (pools/datasets) has no capture button - ZFS topology is machine-specific
// and should never be snapshotted from live into state.yaml automatically.
function BranchSwitcher({ repo, onSelect }: { repo: Repo; onSelect: (b: string) => void }) {
  const [open, setOpen] = useState(false)
  const [branches, setBranches] = useState<string[]>([])
  const [loading, setLoading] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  const current = repo.branch || 'main'

  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  const toggle = async () => {
    if (!open && branches.length === 0) {
      setLoading(true)
      try {
        const qs = new URLSearchParams({ url: repo.url })
        if (repo.credential_id) qs.set('id', String(repo.credential_id))
        const res = await api.get<{ success: boolean; branches: string[] }>(`/api/git-sync/credentials/branches?${qs}`)
        setBranches(res.branches ?? [])
      } catch { setBranches([]) }
      finally { setLoading(false) }
    }
    setOpen(v => !v)
  }

  return (
    <div ref={ref} style={{ position:'relative', display:'inline-flex' }}>
      <button
        onClick={toggle}
        title="Switch tracked branch"
        style={{
          display:'inline-flex', alignItems:'center', gap:4, cursor:'pointer',
          fontFamily:'var(--font-mono)', fontSize:'var(--text-2xs)', fontWeight:600,
          color:'var(--primary)', background:'var(--primary-bg)',
          border:'1px solid color-mix(in srgb, var(--primary) 35%, transparent)',
          borderRadius:99, padding:'2px 8px', lineHeight:1.6,
        }}
      >
        <Icon name="call_split" size={11} />
        {current}
        <Icon name="unfold_more" size={11} style={{ opacity:0.55 }} />
      </button>

      {open && (
        <div style={{
          position:'absolute', top:'calc(100% + 4px)', left:0, zIndex:'var(--z-dropdown)',
          background:'var(--bg-card)', border:'1px solid var(--border)',
          borderRadius:'var(--radius-md)', minWidth:190, maxHeight:240, overflowY:'auto',
          boxShadow:'0 8px 24px rgba(0,0,0,0.18)',
        }}>
          {loading
            ? <div style={{ padding:'12px 16px', color:'var(--text-tertiary)', fontSize:'var(--text-xs)' }}>Fetching branches...</div>
            : branches.length > 0
              ? branches.map(b => (
                  <button key={b} onClick={() => { onSelect(b); setOpen(false) }} style={{
                    display:'flex', alignItems:'center', gap:8, width:'100%', textAlign:'left',
                    padding:'8px 14px', background: b === current ? 'var(--primary-bg)' : 'transparent',
                    color: b === current ? 'var(--primary)' : 'var(--text)',
                    fontSize:'var(--text-xs)', fontFamily:'var(--font-mono)', border:'none',
                    borderBottom:'1px solid var(--border)', cursor:'pointer',
                  }}>
                    {b === current ? <Icon name="check" size={12} /> : <span style={{ width:12 }} />}
                    {b}
                  </button>
                ))
              : <div style={{ padding:'12px 16px', color:'var(--text-tertiary)', fontSize:'var(--text-xs)' }}>No branches found</div>
          }
        </div>
      )}
    </div>
  )
}

// ---- RepoCard (Merged from GitSync) ----
function RepoCard({ repo, onRefresh, onEdit }: { repo: Repo; onRefresh: () => void; onEdit: (r: Repo) => void }) {
  const { confirm, ConfirmDialog } = useConfirm()
  const pull   = useMutation({ mutationFn: () => api.post(`/api/git-sync/repos/pull?id=${repo.id}`, {}), onSuccess: () => { toast.success('Pull triggered'); onRefresh() }, onError: (e: Error) => toast.error(e.message) })
  const saveBranch = useMutation({
    mutationFn: (branch: string) => api.post('/api/git-sync/repos', toApiRepo(repo, { branch })),
    onSuccess: (_, branch) => {
      toast.success(`Now tracking ${branch}`)
      api.post(`/api/git-sync/repos/pull?id=${repo.id}`, {}).catch(() => {})
      onRefresh()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const push   = useMutation({ mutationFn: () => api.post(`/api/git-sync/repos/push?id=${repo.id}`, {}), onSuccess: () => { toast.success('Push triggered'); onRefresh() }, onError: (e: Error) => toast.error(e.message) })
  // Deploy is for Docker stacks
  const deploy = useMutation({ mutationFn: () => api.post(`/api/git-sync/repos/deploy?id=${repo.id}`, {}), onSuccess: () => toast.success('Deployment queued'), onError: (e: Error) => toast.error(e.message) })
  const del    = useMutation({ mutationFn: () => api.post(`/api/git-sync/repos/delete?id=${repo.id}`, {}), onSuccess: () => { toast.success('Repository removed'); onRefresh() }, onError: (e: Error) => toast.error(e.message) })
  const busy   = pull.isPending || push.isPending || deploy.isPending || del.isPending

  return (
    <div className="card card-xl" style={{ position:'relative', borderLeft:`4px solid ${repo.status==='error'?'var(--error)':'var(--border)'}` }}>
      <div style={{ display:'flex', alignItems:'flex-start', gap:12, marginBottom:16 }}>
        {statusDot(repo.status)}
        <div style={{ flex:1, minWidth:0 }}>
          <div style={{ fontWeight:700, fontSize:'var(--text-sm)' }}>{repo.name || repo.url.split('/').pop()?.replace('.git','') || repo.url}</div>
          <div style={{ fontSize:'var(--text-2xs)', color:'var(--text-tertiary)', fontFamily:'var(--font-mono)', marginTop:2, overflow:'hidden', textOverflow:'ellipsis', whiteSpace:'nowrap' }}>{repo.url}</div>
          <div style={{ display:'flex', alignItems:'center', gap:6, marginTop:4, flexWrap:'wrap' }}>
            <BranchSwitcher repo={repo} onSelect={(b) => saveBranch.mutate(b)} />
            {repo.path && <span style={{ fontSize:'var(--text-2xs)', color:'var(--text-tertiary)' }}>· {repo.path}</span>}
            {repo.last_sync && <span style={{ fontSize:'var(--text-2xs)', color:'var(--text-tertiary)' }}>· synced {fmtDate(repo.last_sync)}</span>}
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
  const [host, setHost] = useState('github.com'); const [secret, setSecret] = useState('')
  const [username, setUsername] = useState('')

  const credsQ = useQuery({ queryKey:['git-sync','creds'], queryFn:async ({signal}) => {
    const res = await api.get<{success:boolean;credentials:ApiCred[]}>('/api/git-sync/credentials',signal)
    return { ...res, credentials: (res.credentials ?? []).map(fromApiCred) }
  } })

  const resetForm = () => { setEditingCred(null); setName(''); setHost('github.com'); setSecret(''); setType('token'); setUsername('') }

  const save = useMutation({
    mutationFn: () => api.post('/api/git-sync/credentials', {
      id: editingCred ? Number(editingCred.id) : undefined, name, host, auth_type: type, username: type === 'token' ? username.trim() : '',
      ...(secret ? (type === 'ssh' ? { ssh_key: secret } : { token: secret }) : {}),
    }),
    onSuccess: () => { toast.success(editingCred ? 'Credential updated' : 'Credential saved'); resetForm(); qc.invalidateQueries({ queryKey:['git-sync','creds'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const onEdit = (c: Cred) => {
    setEditingCred(c)
    setName(String(c.name))
    setType(c.type || 'token')
    setHost(c.host || 'github.com')
    setUsername(c.username ?? '')
    setSecret('') // don't show old secret for security, user can re-enter if changing
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  const del = useMutation({
    mutationFn: (id: string|number) => api.post(`/api/git-sync/credentials/delete?id=${id}`, {}),
    onSuccess: () => { toast.success('Removed'); qc.invalidateQueries({ queryKey:['git-sync','creds'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const test = useMutation({
    mutationFn: async (id: string|number) => {
      const res = await api.post<{success:boolean;error?:string;hint?:string}>('/api/git-sync/credentials/test', { credential_id: Number(id) })
      if (!res.success) throw new Error([res.error, res.hint].filter(Boolean).join(' - ') || 'Test failed')
      return res
    },
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
            <select value={type} onChange={e=>setType(e.target.value as 'token'|'ssh')} className="input" style={{ appearance:'none' }}>
              <option value="token">Token</option><option value="ssh">SSH Private Key</option>
            </select>
          </label>
          <label className="field">
            <span className="field-label">Host</span>
            <input value={host} onChange={e=>setHost(e.target.value)} placeholder="github.com" className="input" />
          </label>
          {type === 'token' && (
            <label className="field">
              <span className="field-label">User name (optional)</span>
              <input value={username} onChange={e=>setUsername(e.target.value)} placeholder="x-access-token" className="input"
                title="Sent with the token. GitHub accepts any; Gitea, Forgejo and GitLab may need the account's name." />
            </label>
          )}
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
              <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>{c.type} · {c.host || 'github.com'}</div>
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
  const [branch, setBranch] = useState('main')
  const [token, setToken] = useState('')
  const [useExisting, setUseExisting] = useState<string|null>(null)
  const [isTesting, setIsTesting] = useState(false)

  const tokenName = type === 'nixos' ? 'NixOS Base System Token' : 'Repository Token'
  // Lazy initializer runs once at mount - stable random suffix, readable during render
  const [randomSuffix] = useState(() => Math.floor(Math.random() * 1000))
  const repoName = type === 'nixos'
    ? `NixOS-Backup-${randomSuffix}`
    : `Repository-${randomSuffix}`

  const credsQ = useQuery({ queryKey:['git-sync','creds'], queryFn:async () => {
    const res = await api.get<{success:boolean;credentials:ApiCred[]}>('/api/git-sync/credentials')
    return { ...res, credentials: (res.credentials ?? []).map(fromApiCred) }
  } })

  const saveCred = useMutation({
    mutationFn: () => api.post<{ id: number }>('/api/git-sync/credentials', { name: tokenName, host: hostOf(url), auth_type: 'token', token }),
    onSuccess: (res: { id: number }) => { setUseExisting(String(res.id)); setStep(3); qc.invalidateQueries({queryKey:['git-sync','creds']}) },
    onError: (e: Error) => toast.error(e.message)
  })

  const testConnection = async () => {
    setIsTesting(true)
    try {
      // The API tests stored credentials: store a new token first.
      let credId = useExisting
      if (!credId && token) {
        const saved = await api.post<{ id: number }>('/api/git-sync/credentials', { name: tokenName, host: hostOf(url), auth_type: 'token', token })
        credId = String(saved.id)
        setUseExisting(credId)
        qc.invalidateQueries({ queryKey: ['git-sync', 'creds'] })
      }
      const res = await api.post<{success:boolean;error?:string;hint?:string}>('/api/git-sync/credentials/test', {
        repo_url: url,
        credential_id: credId ? Number(credId) : undefined,
      })
      if (res.success) {
        toast.success('Connection successful!')
        setStep(3)
      } else {
        toast.error(`Connection failed: ${res.error}`)
      }
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Connection error')
    } finally {
      setIsTesting(false)
    }
  }

  const saveRepo = useMutation({
    mutationFn: () => api.post<{ id: number }>('/api/git-sync/repos', {
      name: repoName,
      repo_url: url,
      branch,
      credential_id: useExisting ? Number(useExisting) : undefined,
      enabled: true,
    }),
    onSuccess: (res: { id: number }) => { toast.success('Repository linked'); onComplete(res.id); qc.invalidateQueries({queryKey:['git-sync']}) },
    onError: (e: Error) => toast.error(e.message)
  })

  return (
    <div style={{ position:'fixed', inset:0, background:'rgba(0,0,0,0.85)', backdropFilter:'blur(8px)', zIndex:'var(--z-modal)', display:'flex', alignItems:'center', justifyContent:'center', padding:20 }}>
      <div className="card-xl" style={{ width:'100%', maxWidth:500, padding:32, background:'var(--bg-card)', border:'1px solid var(--border-highlight)', position:'relative' }}>
        <button onClick={onClose} style={{ position:'absolute', top:20, right:20, background:'none', border:'none', color:'var(--text-tertiary)', cursor:'pointer' }}><Icon name="close" size={20}/></button>
        
        <div style={{ textAlign:'center', marginBottom:32 }}>
          <Icon name={type === 'nixos' ? 'settings_suggest' : 'auto_fix_high'} size={40} style={{ color:'var(--primary)', marginBottom:12 }} />
          <h2 style={{ fontSize:'var(--text-xl)', fontWeight:800 }}>{type === 'nixos' ? 'Base System' : 'Repository'} Link</h2>
          <div style={{ color:'var(--text-tertiary)', fontSize:'var(--text-sm)' }}>Step {step} of 3</div>
        </div>

        {step === 1 && (
          <div style={{ display:'grid', gap:20 }}>
            <div>
              <div style={{ fontWeight:700, marginBottom:4 }}>Repository URL</div>
              <div style={{ color:'var(--text-tertiary)', fontSize:'var(--text-xs)', marginBottom:12 }}>Public or private Git repository (HTTPS).</div>
              <input value={url} onChange={e => { const p = parseGitUrl(e.target.value); setUrl(p.repoUrl); if (p.branch) setBranch(p.branch) }} placeholder="https://github.com/my-org/infra.git" className="input input-lg" style={{ fontFamily:'var(--font-mono)' }} />
            </div>
            <div>
              <div style={{ fontWeight:700, marginBottom:4 }}>Branch</div>
              <div style={{ color:'var(--text-tertiary)', fontSize:'var(--text-xs)', marginBottom:12 }}>Auto-detected from URL - edit to override.</div>
              <input value={branch} onChange={e=>setBranch(e.target.value)} placeholder="main" className="input" />
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

type GTab = 'config' | 'repos' | 'credentials'
function RepoEditModal({ repo, onExited, onSaved }: { repo: Repo; onExited: () => void; onSaved: () => void }) {
  const [name, setName] = useState(repo.name)
  const [branch, setBranch] = useState(repo.branch || 'main')
  const [credId, setCredId] = useState<string|number|null>(repo.credential_id || null)
  
  const credsQ = useQuery({ queryKey:['git-sync','creds'], queryFn:async () => {
    const res = await api.get<{success:boolean;credentials:ApiCred[]}>('/api/git-sync/credentials')
    return { ...res, credentials: (res.credentials ?? []).map(fromApiCred) }
  } })

  const save = useMutation({
    mutationFn: (vals: Partial<Repo>) => api.post('/api/git-sync/repos', toApiRepo(repo, vals)),
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

// ── BlockedApprovalModal ──────────────────────────────────────────────────────

// ─────────────────────────────────────────────────────────────────────────────

export function GitOpsPage() {
  const qc = useQueryClient()
  const [tab, setTab] = useState<GTab>('config')
  const [editingRepo, setEditingRepo] = useState<Repo | null>(null)
  const [showWizard, setShowWizard] = useState<'state'|'nixos'|null>(null)
  const reposQ = useQuery({ queryKey:['git-sync','repos'], queryFn:async ({signal}) => {
    const res = await api.get<{success:boolean;repos:ApiRepo[]}>('/api/git-sync/repos',signal)
    return { ...res, repos: (res.repos ?? []).map(fromApiRepo) }
  } })

  const TABS = [
    { id:'config' as const,       label:'Configuration repository', icon:'account_tree' },
    { id:'repos' as const,        label:'Stack repositories',       icon:'source' },
    { id:'credentials' as const,  label:'Credentials',              icon:'key' },
  ]

  return (
    <div style={{ maxWidth: 1000 }}>
      {showWizard && (
        <RepoSyncWizard
          type={showWizard}
          onClose={() => setShowWizard(null)}
          onComplete={() => { qc.invalidateQueries({ queryKey:['git-sync'] }); setShowWizard(null) }}
        />
      )}
      <div className="page-header">
        <h1 className="page-title">Git</h1>
        <p className="page-subtitle">Keep this system's configuration in a Git repository, and deploy stacks from repositories.</p>
      </div>

      <div className="tabs-underline" style={{ marginBottom:24 }}>
        {TABS.map(t => (
          <button key={t.id} onClick={()=>setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16}/>{t.label}
          </button>
        ))}
      </div>

      {tab === 'config' && <ConfigRepository />}
      {tab === 'repos' && (
        <RepositoriesTab
          repos={reposQ.data?.repos ?? []}
          isLoading={reposQ.isLoading}
          onWizardClick={(type: 'state'|'nixos') => setShowWizard(type)}
          onRefresh={() => qc.invalidateQueries({ queryKey:['git-sync'] })}
          onEdit={setEditingRepo}
        />
      )}
      {tab === 'credentials' && <CredentialsTab />}

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
