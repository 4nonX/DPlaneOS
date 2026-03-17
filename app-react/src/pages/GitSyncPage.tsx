/**
 * pages/GitSyncPage.tsx - Git Repository Sync (Phase 7)
 * Tabs: Repos | Credentials
 *
 * GET  /api/git-sync/repos                 → { repos: Repo[] }
 * POST /api/git-sync/repos                 → save repo
 * POST /api/git-sync/repos/delete          → { id }
 * POST /api/git-sync/repos/pull            → { id }
 * POST /api/git-sync/repos/push            → { id }
 * POST /api/git-sync/repos/deploy          → { id }
 * POST /api/git-sync/repos/export          → { id }
 * GET  /api/git-sync/credentials           → { credentials: Cred[] }
 * POST /api/git-sync/credentials           → save cred
 * POST /api/git-sync/credentials/test      → { id }
 * POST /api/git-sync/credentials/delete    → { id }
 * GET  /api/git-sync/status                → { syncing, last_sync }
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'

interface Repo { id: string|number; url: string; branch?: string; path?: string; name?: string; status?: string; last_sync?: string; credential_id?: string|number }
interface Cred { id: string|number; name: string; type?: 'ssh'|'token'|'password'; username?: string }



function statusDot(s?: string) {
  const c = s === 'synced' ? 'var(--success)' : s === 'syncing' ? 'var(--primary)' : s === 'error' ? 'var(--error)' : 'var(--text-tertiary)'
  return <span style={{ width:8, height:8, borderRadius:'50%', background:c, boxShadow:s==='syncing'?`0 0 6px ${c}`:'none', display:'inline-block', flexShrink:0 }} />
}

function fmtDate(s?:string){if(!s)return'-';try{return new Date(s).toLocaleString('de-DE',{dateStyle:'short',timeStyle:'short'})}catch{return s}}

// ---- RepoForm ----
function RepoForm({ onDone, creds }: { onDone: () => void; creds: Cred[] }) {
  const [url, setUrl] = useState(''); const [branch, setBranch] = useState('main')
  const [path, setPath] = useState(''); const [credId, setCredId] = useState('')
  const save = useMutation({
    mutationFn: () => api.post('/api/git-sync/repos', { url, branch, path, credential_id: credId || undefined }),
    onSuccess: () => { toast.success('Repo added'); onDone() },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div className="card card-xl" style={{ marginBottom: 24 }}>
      <div style={{ fontWeight: 700, marginBottom: 14 }}>Add Repository</div>
      <div style={{ display: 'grid', gap: 10 }}>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 120px', gap: 8 }}>
          <label className="field">
            <span className="field-label">Repository URL</span>
            <input value={url} onChange={e=>setUrl(e.target.value)} placeholder="https://github.com/user/repo.git" className="input" style={{ fontFamily:'var(--font-mono)' }} />
          </label>
          <label className="field">
            <span className="field-label">Branch</span>
            <input value={branch} onChange={e=>setBranch(e.target.value)} className="input" />
          </label>
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
          <label className="field">
            <span className="field-label">Local path</span>
            <input value={path} onChange={e=>setPath(e.target.value)} placeholder="/opt/stacks/myapp" className="input" style={{ fontFamily:'var(--font-mono)' }} />
          </label>
          <label className="field">
            <span className="field-label">Credential</span>
            <select value={credId} onChange={e=>setCredId(e.target.value)} className="input" style={{ appearance:'none' }}>
              <option value="">None (public repo)</option>
              {creds.map(c => <option key={c.id} value={String(c.id)}>{c.name}</option>)}
            </select>
          </label>
        </div>
        <button onClick={() => save.mutate()} disabled={!url.trim() || save.isPending} className="btn btn-primary" style={{ alignSelf:'flex-start' }}>
          <Icon name="add" size={14} />{save.isPending ? 'Adding…' : 'Add Repo'}
        </button>
      </div>
    </div>
  )
}

// ---- ReposTab ----
function ReposTab() {
  const qc = useQueryClient()
  const [showForm, setShowForm] = useState(false)
  const reposQ = useQuery({ queryKey:['git-sync','repos'], queryFn:({signal})=>api.get<{success:boolean;repos:Repo[]}>('/api/git-sync/repos',signal) })
  const credsQ = useQuery({ queryKey:['git-sync','creds'], queryFn:({signal})=>api.get<{success:boolean;credentials:Cred[]}>('/api/git-sync/credentials',signal) })

  const repos = reposQ.data?.repos ?? []
  const creds = credsQ.data?.credentials ?? []

  if (reposQ.isLoading) return <Skeleton height={200} />
  if (reposQ.isError) return <ErrorState error={reposQ.error} />

  return (
    <>
      <div style={{ display:'flex', justifyContent:'flex-end', marginBottom:16 }}>
        <button onClick={() => setShowForm(f => !f)} className="btn btn-primary"><Icon name="add" size={14} />{showForm ? 'Cancel' : 'Add Repo'}</button>
      </div>
      {showForm && <RepoForm onDone={() => { setShowForm(false); qc.invalidateQueries({ queryKey:['git-sync','repos'] }) }} creds={creds} />}
      <div style={{ display:'flex', flexDirection:'column', gap:12 }}>
        {repos.map(repo => <RepoCard key={repo.id} repo={repo} onRefresh={() => qc.invalidateQueries({ queryKey:['git-sync','repos'] })} />)}
        {repos.length === 0 && <div style={{ textAlign:'center', padding:'48px 0', border:'1px dashed var(--border)', borderRadius:'var(--radius-xl)', color:'var(--text-tertiary)' }}><Icon name="source" size={40} style={{ opacity:0.3, display:'block', margin:'0 auto 12px' }} />No repositories added</div>}
      </div>
    </>
  )
}

function RepoCard({ repo, onRefresh }: { repo: Repo; onRefresh: () => void }) {
  const { confirm, ConfirmDialog } = useConfirm()
  const pull   = useMutation({ mutationFn: () => api.post('/api/git-sync/repos/pull',   { id: repo.id }), onSuccess: () => { toast.success('Pulled'); onRefresh() }, onError: (e: Error) => toast.error(e.message) })
  const push   = useMutation({ mutationFn: () => api.post('/api/git-sync/repos/push',   { id: repo.id }), onSuccess: () => { toast.success('Pushed'); onRefresh() }, onError: (e: Error) => toast.error(e.message) })
  const deploy = useMutation({ mutationFn: () => api.post('/api/git-sync/repos/deploy', { id: repo.id }), onSuccess: () => toast.success('Deployed'), onError: (e: Error) => toast.error(e.message) })
  const del    = useMutation({ mutationFn: () => api.post('/api/git-sync/repos/delete', { id: repo.id }), onSuccess: () => { toast.success('Removed'); onRefresh() }, onError: (e: Error) => toast.error(e.message) })
  const busy   = pull.isPending || push.isPending || deploy.isPending || del.isPending

  return (
    <div className="card card-xl">
      <div style={{ display:'flex', alignItems:'flex-start', gap:12, marginBottom:12 }}>
        {statusDot(repo.status)}
        <div style={{ flex:1, minWidth:0 }}>
          <div style={{ fontWeight:700 }}>{repo.name || repo.url.split('/').pop()?.replace('.git','') || repo.url}</div>
          <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', fontFamily:'var(--font-mono)', marginTop:2, overflow:'hidden', textOverflow:'ellipsis', whiteSpace:'nowrap' }}>{repo.url}</div>
          <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)', marginTop:2 }}>
            branch: {repo.branch || 'main'}{repo.path && ` · ${repo.path}`}{repo.last_sync && ` · synced ${fmtDate(repo.last_sync)}`}
          </div>
        </div>
      </div>
      <div style={{ display:'flex', gap:6, flexWrap:'wrap' }}>
        <button onClick={() => pull.mutate()}   disabled={busy} className="btn btn-ghost"><Icon name="download" size={13} />{pull.isPending ? 'Pulling…' : 'Pull'}</button>
        <button onClick={() => push.mutate()}   disabled={busy} className="btn btn-ghost"><Icon name="upload" size={13} />{push.isPending ? 'Pushing…' : 'Push'}</button>
        <button onClick={() => deploy.mutate()} disabled={busy} className="btn btn-ghost"><Icon name="rocket_launch" size={13} />{deploy.isPending ? 'Deploying…' : 'Deploy'}</button>
        <button onClick={async () => { if (await confirm({ title: 'Remove this repo?', message: 'The repository will be removed from Git Sync. Local files are not affected.', danger: true, confirmLabel: 'Remove' })) del.mutate() }} disabled={busy}
          className="btn btn-ghost" style={{ color:'var(--error)', borderColor:'var(--error-border)' }}><Icon name="delete" size={13} />Remove</button>
      </div>
      <ConfirmDialog />
    </div>
  )
}

// ---- CredentialsTab ----
function CredentialsTab() {
  const qc = useQueryClient()
  const [name, setName] = useState(''); const [type, setType] = useState<'token'|'ssh'|'password'>('token')
  const [user, setUser] = useState(''); const [secret, setSecret] = useState('')

  const credsQ = useQuery({ queryKey:['git-sync','creds'], queryFn:({signal})=>api.get<{success:boolean;credentials:Cred[]}>('/api/git-sync/credentials',signal) })
  const save = useMutation({
    mutationFn: () => api.post('/api/git-sync/credentials', { name, type, username: user, secret }),
    onSuccess: () => { toast.success('Credential saved'); setName(''); setUser(''); setSecret(''); qc.invalidateQueries({ queryKey:['git-sync','creds'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const del  = useMutation({
    mutationFn: (id: string|number) => api.post('/api/git-sync/credentials/delete', { id }),
    onSuccess: () => { toast.success('Removed'); qc.invalidateQueries({ queryKey:['git-sync','creds'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const test = useMutation({
    mutationFn: (id: string|number) => api.post('/api/git-sync/credentials/test', { id }),
    onSuccess: () => toast.success('Credential valid'),
    onError: (e: Error) => toast.error(e.message),
  })

  const creds = credsQ.data?.credentials ?? []

  return (
    <>
      <div className="card card-xl" style={{ marginBottom: 20 }}>
        <div style={{ fontWeight: 700, marginBottom: 14 }}>Add Credential</div>
        <div style={{ display:'grid', gridTemplateColumns:'1fr 120px 1fr 1fr auto', gap:8, alignItems:'flex-end' }}>
          {[['Name', name, setName, 'my-github'], ['Username', user, setUser, 'git']].map(([lbl, val, setter, ph]) => (
            <label key={lbl as string} className="field">
              <span className="field-label">{lbl as string}</span>
              <input value={val as string} onChange={e=>(setter as React.Dispatch<React.SetStateAction<string>>)(e.target.value)} placeholder={ph as string} className="input" />
            </label>
          ))}
          <label className="field">
            <span className="field-label">Type</span>
            <select value={type} onChange={e=>setType(e.target.value as typeof type)} className="input" style={{ appearance:'none' }}>
              <option value="token">Token</option><option value="ssh">SSH Key</option><option value="password">Password</option>
            </select>
          </label>
          <label className="field">
            <span className="field-label">{type === 'ssh' ? 'Private Key' : 'Token / Password'}</span>
            <input type="password" value={secret} onChange={e=>setSecret(e.target.value)} className="input" style={{ fontFamily:'var(--font-mono)' }} autoComplete="new-password" />
          </label>
          <button onClick={() => save.mutate()} disabled={!name.trim() || save.isPending} className="btn btn-primary" style={{ alignSelf:'flex-end' }}>
            <Icon name="add" size={14} />{save.isPending ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>

      {credsQ.isLoading && <Skeleton height={100} />}
      <div style={{ display:'flex', flexDirection:'column', gap:8 }}>
        {creds.map(c => (
          <div key={c.id} className="card card-xl" style={{ display:'flex', alignItems:'center', gap:12 }}>
            <Icon name="key" size={18} style={{ color:'var(--primary)', flexShrink:0 }} />
            <div style={{ flex:1 }}>
              <div style={{ fontWeight:600 }}>{c.name}</div>
              <div style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>{c.type} · {c.username || 'no username'}</div>
            </div>
            <button onClick={() => test.mutate(c.id)} disabled={test.isPending} className="btn btn-ghost"><Icon name="cable" size={13} />Test</button>
            <button onClick={() => del.mutate(c.id)} className="btn btn-ghost" style={{ color:'var(--error)', borderColor:'var(--error-border)' }}><Icon name="delete" size={13} />Remove</button>
          </div>
        ))}
        {!credsQ.isLoading && creds.length === 0 && <div style={{ textAlign:'center', padding:'32px 0', color:'var(--text-tertiary)' }}>No credentials stored</div>}
      </div>
    </>
  )
}

type GTab = 'repos'|'credentials'
export function GitSyncPage() {
  const [tab, setTab] = useState<GTab>('repos')
  const TABS = [{ id:'repos' as GTab, label:'Repositories', icon:'source' }, { id:'credentials' as GTab, label:'Credentials', icon:'key' }]
  return (
    <div style={{ maxWidth: 1000 }}>
      <div className="page-header">
        <h1 className="page-title">Git Sync</h1>
        <p className="page-subtitle">Track and deploy configuration from Git repositories</p>
      </div>
      <div className="tabs-underline">
        {TABS.map(t => <button key={t.id} onClick={()=>setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}><Icon name={t.icon} size={16}/>{t.label}</button>)}
      </div>
      {tab === 'repos'       && <ReposTab />}
      {tab === 'credentials' && <CredentialsTab />}
    </div>
  )
}

