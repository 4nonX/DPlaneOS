/**
 * ConfigRepository - the configuration in a Git repository (Design 0001
 * section 5.8, phase 5).
 *
 * Every node's configuration is kept as state.yaml files in the repository,
 * one per scope (cluster/, groups/<group>/, nodes/<node>/) or one file on a
 * standalone node. The node that owns a scope writes it; changes made in the
 * repository come back through the same merge as changes from other nodes.
 *
 * Calls:
 *   GET  /api/gitops/status                → { status, holds }
 *   POST /api/gitops/sync
 *   GET  /api/gitops/settings              → { settings }
 *   PUT  /api/gitops/settings              settings + webhook_secret ("" keep, "generate", "none")
 *   POST /api/gitops/approve               { scope, sha }
 *   POST /api/gitops/take                  { scope }
 *   POST /api/gitops/holds/{scope}/resume
 *   POST /api/gitops/holds/{scope}/revert
 *   GET  /api/gitops/state?scope=          → { path, sha, content }
 *   PUT  /api/gitops/nixos-repo            { repo_id }
 *   GET  /api/git-sync/credentials, GET /api/git-sync/repos
 */

import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'

interface Remote { url: string; fetch_cred: string; push_cred: string }
export interface GitSettings {
  enabled: boolean
  remotes: Remote[]
  branch: string
  layout: 'single' | 'scoped'
  state_path: string
  categories: Record<string, boolean>
  policy: 'direct' | 'pull_request'
  provider: 'none' | 'github' | 'gitea' | 'gitlab'
  provider_api_url: string
  provider_repo: string
  provider_cred: string
  auto_apply: boolean
  prune: boolean
  self_heal: boolean
  verify_signatures: boolean
  allowed_signers: string
  gpg_keys: string
  sign_commits: boolean
  interval_secs: number
  version: number
  node: string
  webhook_secret_set: boolean
  webhook_path: string
  nixos_repo_id: number | null
}
interface Change { id: string; kind: string; key: string; action: string; deletion: boolean }
interface Hold { id: number; scope: string; reason: string; commit_sha: string; good_sha: string; since: string; set_by: string }
interface ScopeStatus {
  scope: string; path: string; owner: boolean; owner_reason?: string; synced_sha: string
  push: string[]; pull: Change[]; conflicts: Change[]; empty_kinds?: string[]
  pr?: { number: number; url: string; state: string }; declined: boolean; hold?: Hold; error?: string; applied?: string[]; checked_at: string
}
interface GitStatus {
  enabled: boolean; checked_at: string; head: string; error?: string; next_run: string; signing_public_key?: string
  remotes: { url: string; head: string; error?: string }[]
  scopes: ScopeStatus[]
}
type Res = { success: boolean; error?: string }

const CATEGORIES: { id: string; label: string; what: string }[] = [
  { id: 'storage', label: 'Storage', what: 'pools, datasets' },
  { id: 'access', label: 'Sharing', what: 'SMB shares, NFS and NVMe-oF exports' },
  { id: 'apps', label: 'Apps', what: 'Docker stacks' },
  { id: 'identity', label: 'Identity', what: 'users, groups, directory, certificates' },
  { id: 'protection', label: 'Protection', what: 'replication, SMART tasks' },
  { id: 'system', label: 'System', what: 'node settings, shared settings' },
]

const PROVIDERS = {
  none: { label: 'None', api: '' },
  github: { label: 'GitHub', api: 'https://api.github.com' },
  gitea: { label: 'Gitea / Forgejo', api: 'https://git.example.lan/api/v1' },
  gitlab: { label: 'GitLab', api: 'https://gitlab.com/api/v4' },
} as const

function scopeLabel(sc: string) {
  if (sc === 'all') return 'Configuration'
  if (sc === 'cluster') return 'Cluster (shared by all nodes)'
  if (sc.startsWith('group:')) return `Storage group ${sc.slice(6)}`
  if (sc.startsWith('node:')) return `Node ${sc.slice(5)}`
  return sc
}
const short = (sha?: string) => (sha ? sha.slice(0, 12) : '—')

function useCredentials() {
  return useQuery({
    queryKey: ['git-sync', 'creds'],
    queryFn: ({ signal }) => api.get<{ success: boolean; credentials: { id: number; name: string; auth_type?: string }[] }>('/api/git-sync/credentials', signal),
    select: r => r.credentials ?? [],
  })
}

// ── Scope status ──────────────────────────────────────────────────────────────

function FileModal({ scope, onClose }: { scope: string; onClose: () => void }) {
  const q = useQuery({
    queryKey: ['gitops', 'file', scope],
    queryFn: ({ signal }) => api.get<Res & { path: string; sha: string; content: string }>(`/api/gitops/state?scope=${encodeURIComponent(scope)}`, signal),
  })
  return (
    <Modal title={q.data?.path ? `${q.data.path} at ${short(q.data.sha)}` : 'File in the repository'} onClose={onClose} size="lg">
      {q.isLoading ? <Skeleton height={200} /> : q.data?.success === false ? <p style={{ color: 'var(--error)' }}>{q.data.error}</p> : (
        <pre style={{ background: 'var(--surface)', padding: 12, borderRadius: 8, fontFamily: 'var(--font-mono)', fontSize: 12, maxHeight: 480, overflow: 'auto', whiteSpace: 'pre-wrap' }}>
          {q.data?.content || '(the file does not exist in the repository yet)'}
        </pre>
      )}
    </Modal>
  )
}

function ScopeCard({ s, head }: { s: ScopeStatus; head: string }) {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [viewing, setViewing] = useState(false)
  const done = { onSuccess: (r: Res) => { if (r.error) toast.error(r.error); qc.invalidateQueries({ queryKey: ['gitops'] }) }, onError: (e: Error) => toast.error(e.message) }
  const approve = useMutation({ mutationFn: async () => ensureOk(await api.post<Res>('/api/gitops/approve', { scope: s.scope, sha: head })), ...done })
  const take = useMutation({ mutationFn: async () => ensureOk(await api.post<Res>('/api/gitops/take', { scope: s.scope })), ...done })
  const resume = useMutation({ mutationFn: async () => ensureOk(await api.post<Res>(`/api/gitops/holds/${encodeURIComponent(s.scope)}/resume`, {})), ...done })
  const revert = useMutation({ mutationFn: async () => ensureOk(await api.post<Res>(`/api/gitops/holds/${encodeURIComponent(s.scope)}/revert`, {})), ...done })
  const inSync = s.owner && !s.hold && !s.error && s.push.length + s.pull.length + s.conflicts.length === 0

  return (
    <div className="card" style={{ padding: 16, borderColor: s.hold ? 'var(--error)' : undefined }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <strong>{scopeLabel(s.scope)}</strong>
        <code style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{s.path}</code>
        {!s.owner
          ? <span className="badge badge-neutral" title={s.owner_reason}>Written by another node</span>
          : s.hold ? <span className="badge badge-error">On hold</span>
            : inSync ? <span className="badge badge-success">In step</span>
              : <span className="badge badge-warning">Changes pending</span>}
        <span style={{ marginLeft: 'auto', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>at {short(s.synced_sha)}</span>
        <button className="btn btn-ghost btn-sm" onClick={() => setViewing(true)} aria-label={`View ${s.path}`}><Icon name="description" size={14} />View file</button>
      </div>
      {!s.owner && s.owner_reason && <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', margin: '6px 0 0' }}>{s.owner_reason}.</p>}

      {s.hold && (
        <div role="alert" style={{ marginTop: 10, padding: '10px 12px', borderRadius: 8, background: 'var(--error-bg)', fontSize: 'var(--text-sm)' }}>
          <strong>Nothing from the repository is applied to this scope:</strong> {s.hold.reason}
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 4 }}>
            Since {new Date(s.hold.since).toLocaleString()} · last good commit {short(s.hold.good_sha)}
          </div>
          <div style={{ display: 'flex', gap: 8, marginTop: 8 }}>
            <button className="btn btn-ghost btn-sm" disabled={resume.isPending} onClick={() => resume.mutate()}>
              <Icon name="play_arrow" size={14} />Resume (try again)
            </button>
            {s.hold.good_sha && (
              <button className="btn btn-ghost btn-sm" disabled={revert.isPending} onClick={async () => {
                if (await confirm({ title: `Revert ${s.path}?`, message: `The file is written back as it was in ${short(s.hold!.good_sha)}, as a new commit (or a pull request). Nothing is deleted from the history.`, confirmLabel: 'Revert' })) revert.mutate()
              }}><Icon name="undo" size={14} />Revert to {short(s.hold.good_sha)}</button>
            )}
          </div>
        </div>
      )}

      {s.error && !s.hold && <p style={{ color: 'var(--error)', fontSize: 'var(--text-sm)', margin: '8px 0 0' }}>{s.error}</p>}

      {s.pull.length > 0 && (
        <div style={{ marginTop: 10, fontSize: 'var(--text-sm)' }}>
          <div>Changed in the repository, waiting for your approval:</div>
          <ul style={{ margin: '4px 0' }}>
            {s.pull.map(c => <li key={c.id}><code>{c.id}</code>{c.deletion ? ' (deleted there)' : ''}</li>)}
          </ul>
          {!s.hold && (
            <button className="btn btn-primary btn-sm" disabled={approve.isPending} onClick={async () => {
              if (await confirm({ title: `Apply ${s.pull.length} change(s) from ${short(head)}?`, message: 'They are applied on this node and passed on to the other nodes. The safety rules still apply: a change that would destroy data waits for manual action.', confirmLabel: 'Apply' })) approve.mutate()
            }}><Icon name="download" size={14} />Apply from {short(head)}</button>
          )}
        </div>
      )}
      {s.conflicts.length > 0 && (
        <p style={{ marginTop: 10, fontSize: 'var(--text-sm)' }}>
          <Icon name="call_split" size={14} /> Changed both here and in the repository: {s.conflicts.map(c => c.id).join(', ')}.
          {' '}Decide in <Link to="/config-sync">Configuration Sync</Link>.
        </p>
      )}
      {s.push.length > 0 && (
        <div style={{ marginTop: 10, fontSize: 'var(--text-sm)' }}>
          {s.pr ? (
            <>Proposed in <a href={s.pr.url} target="_blank" rel="noreferrer">pull request #{s.pr.number}</a> ({s.pr.state}): {s.push.join(', ')}</>
          ) : s.declined ? (
            <>
              The pull request with {s.push.join(', ')} was closed without merging. These changes exist only on this node.
              <button className="btn btn-ghost btn-sm" style={{ marginLeft: 8 }} disabled={take.isPending} onClick={async () => {
                if (await confirm({ title: 'Take the repository\'s version?', message: 'This node\'s changes to these resources are replaced by the repository\'s version.', confirmLabel: 'Take it', danger: true })) take.mutate()
              }}>Take the repository's version</button>
            </>
          ) : <>Not yet in the repository: {s.push.join(', ')}</>}
        </div>
      )}
      {(s.applied ?? []).length > 0 && (
        <p style={{ fontSize: 'var(--text-xs)', color: 'var(--success)', margin: '8px 0 0' }}>Applied from the repository: {s.applied!.join(', ')}</p>
      )}
      {viewing && <FileModal scope={s.scope} onClose={() => setViewing(false)} />}
      <ConfirmDialog />
    </div>
  )
}

// ── Settings ──────────────────────────────────────────────────────────────────

function SettingsForm({ initial, onSaved }: { initial: GitSettings; onSaved: () => void }) {
  const [s, setS] = useState<GitSettings>(initial)
  const [shownSecret, setShownSecret] = useState('')
  const creds = useCredentials()
  const set = <K extends keyof GitSettings>(k: K, v: GitSettings[K]) => setS(p => ({ ...p, [k]: v }))
  const save = useMutation({
    mutationFn: async (webhook: '' | 'generate' | 'none') => ensureOk(await api.put<Res & { webhook_secret?: string; warning?: string }>('/api/gitops/settings', { ...s, webhook_secret: webhook })),
    onSuccess: r => {
      toast.success('Saved')
      if (r.warning) toast.error(r.warning)
      if (r.webhook_secret) setShownSecret(r.webhook_secret)
      onSaved()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const credOptions = (creds.data ?? []).map(c => <option key={c.id} value={c.name}>{c.name}</option>)
  const field: React.CSSProperties = { display: 'flex', flexDirection: 'column', gap: 4, fontSize: 'var(--text-sm)' }
  const label: React.CSSProperties = { fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }

  return (
    <div className="card" style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <label style={{ display: 'flex', gap: 8, alignItems: 'center', fontWeight: 600 }}>
        <input type="checkbox" checked={s.enabled} onChange={e => set('enabled', e.target.checked)} /> Keep the configuration in a Git repository
      </label>

      <section style={field}>
        <span style={label}>Remotes (fetched from all; the newest history wins; pushed to all)</span>
        {s.remotes.map((r, i) => (
          <div key={i} style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            <input className="input" style={{ flex: '3 1 260px' }} value={r.url} placeholder="https://git.example.lan/infra/nas.git or git@host:org/nas.git"
              aria-label={`Remote ${i + 1} URL`} onChange={e => set('remotes', s.remotes.map((x, j) => j === i ? { ...x, url: e.target.value.trim() } : x))} />
            <select className="input" style={{ flex: '1 1 140px' }} value={r.fetch_cred} aria-label={`Remote ${i + 1} fetch credential`}
              onChange={e => set('remotes', s.remotes.map((x, j) => j === i ? { ...x, fetch_cred: e.target.value } : x))}>
              <option value="">Fetch: no credential</option>{credOptions}
            </select>
            <select className="input" style={{ flex: '1 1 140px' }} value={r.push_cred} aria-label={`Remote ${i + 1} push credential`}
              onChange={e => set('remotes', s.remotes.map((x, j) => j === i ? { ...x, push_cred: e.target.value } : x))}>
              <option value="">Push: no credential</option>{credOptions}
            </select>
            <button className="btn btn-ghost btn-sm" aria-label={`Remove remote ${i + 1}`} onClick={() => set('remotes', s.remotes.filter((_, j) => j !== i))}><Icon name="close" size={14} /></button>
          </div>
        ))}
        <button className="btn btn-ghost btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => set('remotes', [...s.remotes, { url: '', fetch_cred: '', push_cred: '' }])}>
          <Icon name="add" size={14} />Add remote
        </button>
        <span style={label}>Use a read-only deploy key to fetch and a separate key to push if you can; credentials are under Credentials.</span>
      </section>

      <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
        <label style={{ ...field, flex: '1 1 160px' }}><span style={label}>Branch</span>
          <input className="input" value={s.branch} onChange={e => set('branch', e.target.value.trim())} aria-label="Branch" /></label>
        <label style={{ ...field, flex: '1 1 220px' }}><span style={label}>Files</span>
          <select className="input" value={s.layout} onChange={e => set('layout', e.target.value as GitSettings['layout'])} aria-label="Layout">
            <option value="scoped">One file per scope (cluster/, groups/…, nodes/…)</option>
            <option value="single">One file (standalone node)</option>
          </select></label>
        {s.layout === 'single' && (
          <label style={{ ...field, flex: '1 1 160px' }}><span style={label}>File</span>
            <input className="input" value={s.state_path} onChange={e => set('state_path', e.target.value.trim())} aria-label="State file" /></label>
        )}
        <label style={{ ...field, flex: '0 1 140px' }}><span style={label}>Check every (seconds)</span>
          <input className="input" type="number" min={30} max={86400} value={s.interval_secs} onChange={e => set('interval_secs', Number(e.target.value) || 300)} aria-label="Check interval" /></label>
      </div>
      {s.layout === 'single' && <p style={{ ...label, margin: 0 }}>One file works for a single node. Paired nodes need one file per scope, so each file has exactly one node that writes it.</p>}

      <section style={field}>
        <span style={label}>What the repository manages</span>
        <div style={{ display: 'flex', gap: 14, flexWrap: 'wrap' }}>
          {CATEGORIES.map(c => (
            <label key={c.id} title={c.what} style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
              <input type="checkbox" checked={!!s.categories[c.id]} onChange={e => set('categories', { ...s.categories, [c.id]: e.target.checked })} /> {c.label}
            </label>
          ))}
        </div>
      </section>

      <section style={field}>
        <span style={label}>How changes made here reach the repository</span>
        <label style={{ display: 'flex', gap: 6 }}><input type="radio" name="policy" checked={s.policy === 'direct'} onChange={() => set('policy', 'direct')} />
          Commit to the branch. If the branch changed the same thing meanwhile, you decide in Configuration Sync.</label>
        <label style={{ display: 'flex', gap: 6 }}><input type="radio" name="policy" checked={s.policy === 'pull_request'} onChange={() => set('policy', 'pull_request')} />
          Open a pull request (branch dplaneos/&lt;scope&gt;); the change counts once it is merged.</label>
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          <select className="input" style={{ flex: '1 1 160px' }} value={s.provider} aria-label="Provider"
            onChange={e => { const p = e.target.value as GitSettings['provider']; setS(x => ({ ...x, provider: p, provider_api_url: x.provider_api_url || PROVIDERS[p].api })) }}>
            {Object.entries(PROVIDERS).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}
          </select>
          {s.provider !== 'none' && (
            <>
              <input className="input" style={{ flex: '2 1 220px' }} value={s.provider_api_url} placeholder={PROVIDERS[s.provider].api} onChange={e => set('provider_api_url', e.target.value.trim())} aria-label="API URL" />
              <input className="input" style={{ flex: '1 1 160px' }} value={s.provider_repo} placeholder="owner/name" onChange={e => set('provider_repo', e.target.value.trim())} aria-label="Repository on the provider" />
              <select className="input" style={{ flex: '1 1 140px' }} value={s.provider_cred} onChange={e => set('provider_cred', e.target.value)} aria-label="API token">
                <option value="">API token…</option>{credOptions}
              </select>
            </>
          )}
        </div>
        <span style={label}>With a provider, the result of applying a commit is also shown next to it (commit status).</span>
      </section>

      <section style={field}>
        <span style={label}>Changes from the repository (defaults are the safe ones)</span>
        <label style={{ display: 'flex', gap: 6 }}><input type="checkbox" checked={s.auto_apply} onChange={e => set('auto_apply', e.target.checked)} />
          Apply automatically (off: they wait here for approval)</label>
        <label style={{ display: 'flex', gap: 6 }}><input type="checkbox" checked={s.prune} onChange={e => set('prune', e.target.checked)} />
          Also delete what the repository deletes (off: deletions wait for approval)</label>
        <label style={{ display: 'flex', gap: 6 }}><input type="checkbox" checked={s.self_heal} onChange={e => set('self_heal', e.target.checked)} />
          Undo changes made outside the web interface (shell, other tools) instead of committing them</label>
        <span style={label}>A commit that would remove every resource of a kind is always held, and a failed apply holds the scope at its last good commit. Nothing is ever reverted automatically.</span>
      </section>

      <section style={field}>
        <span style={label}>Signatures</span>
        <label style={{ display: 'flex', gap: 6 }}><input type="checkbox" checked={s.verify_signatures} onChange={e => set('verify_signatures', e.target.checked)} />
          Take only commits signed by an allowed key</label>
        {s.verify_signatures && (
          <>
            <textarea className="input" rows={3} value={s.allowed_signers} placeholder="ssh-ed25519 AAAA… ops@example (one key per line)" onChange={e => set('allowed_signers', e.target.value)} aria-label="Allowed SSH keys" style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }} />
            <textarea className="input" rows={3} value={s.gpg_keys} placeholder="-----BEGIN PGP PUBLIC KEY BLOCK-----" onChange={e => set('gpg_keys', e.target.value)} aria-label="Allowed GPG keys" style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }} />
          </>
        )}
        <label style={{ display: 'flex', gap: 6 }}><input type="checkbox" checked={s.sign_commits} onChange={e => set('sign_commits', e.target.checked)} />
          Sign this node's commits (its public key is shown below once saved; add it to the allowed keys on every node)</label>
      </section>

      <section style={field}>
        <span style={label}>Push notifications (webhook)</span>
        <span>{s.webhook_secret_set ? 'A secret is set.' : 'No secret: the repository is checked on the interval only.'}
          {' '}Address: <code>{window.location.origin}{s.webhook_path}</code></span>
        {shownSecret && (
          <div style={{ padding: 8, background: 'var(--surface)', borderRadius: 6 }}>
            Secret (shown once; enter it in the webhook settings of the repository): <code style={{ wordBreak: 'break-all' }}>{shownSecret}</code>
          </div>
        )}
      </section>

      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <button className="btn btn-primary" disabled={save.isPending} onClick={() => save.mutate('')}><Icon name="save" size={16} />Save</button>
        <button className="btn btn-ghost" disabled={save.isPending} onClick={() => save.mutate('generate')}><Icon name="key" size={16} />Save with a new webhook secret</button>
        {s.webhook_secret_set && <button className="btn btn-ghost" disabled={save.isPending} onClick={() => save.mutate('none')}>Save without webhook</button>}
      </div>
      <p style={{ ...label, margin: 0 }}>Saved settings are sent to the paired nodes with the credentials they use (encrypted); version {s.version}{s.node ? `, last saved on ${s.node}` : ''}.</p>
    </div>
  )
}

function NixOSRepo({ current }: { current: number | null }) {
  const qc = useQueryClient()
  const repos = useQuery({
    queryKey: ['git-sync', 'repos'],
    queryFn: ({ signal }) => api.get<{ success: boolean; repos: { id: number; name?: string; repo_url: string }[] }>('/api/git-sync/repos', signal),
    select: r => r.repos ?? [],
  })
  const save = useMutation({
    mutationFn: async (id: number | null) => ensureOk(await api.put<Res>('/api/gitops/nixos-repo', { repo_id: id })),
    onSuccess: () => { toast.success('Saved'); qc.invalidateQueries({ queryKey: ['gitops', 'settings'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div className="card" style={{ fontSize: 'var(--text-sm)' }}>
      <strong>NixOS configuration</strong>
      <p style={{ color: 'var(--text-secondary)' }}>The tracked repository (Repositories tab) the base system's NixOS configuration is checked against before an update is applied.</p>
      <select className="input" style={{ maxWidth: 420 }} value={current ?? ''} aria-label="NixOS repository"
        onChange={e => save.mutate(e.target.value ? Number(e.target.value) : null)}>
        <option value="">None</option>
        {(repos.data ?? []).map(r => <option key={r.id} value={r.id}>{r.name || r.repo_url}</option>)}
      </select>
    </div>
  )
}

// ── The tab ───────────────────────────────────────────────────────────────────

export function ConfigRepository() {
  const qc = useQueryClient()
  const [editing, setEditing] = useState(false)
  const settings = useQuery({
    queryKey: ['gitops', 'settings'],
    queryFn: ({ signal }) => api.get<{ success: boolean; settings: GitSettings }>('/api/gitops/settings', signal),
    select: r => r.settings,
  })
  const status = useQuery({
    queryKey: ['gitops', 'status'],
    queryFn: ({ signal }) => api.get<{ success: boolean; status: GitStatus; holds: Hold[] }>('/api/gitops/status', signal),
    refetchInterval: 10_000,
  })
  const sync = useMutation({
    mutationFn: async () => api.post<Res & { status: GitStatus }>('/api/gitops/sync', {}),
    onSuccess: r => { if (r.success) toast.success('Synchronised'); else toast.error(r.error ?? 'Failed'); qc.invalidateQueries({ queryKey: ['gitops'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  if (!settings.data) return <Skeleton height={300} />
  const s = settings.data
  const st = status.data?.status
  const showForm = editing || !s.enabled

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      {s.enabled && st && (
        <div className="card" style={{ display: 'flex', gap: 14, alignItems: 'center', flexWrap: 'wrap', fontSize: 'var(--text-sm)' }}>
          <Icon name={st.error ? 'warning' : 'check_circle'} size={20} style={{ color: st.error ? 'var(--warning)' : 'var(--success)' }} />
          <span>Newest commit <code>{short(st.head)}</code> on <strong>{s.branch}</strong></span>
          {st.checked_at && <span style={{ color: 'var(--text-tertiary)' }}>checked {new Date(st.checked_at).toLocaleTimeString()}</span>}
          {st.next_run && <span style={{ color: 'var(--text-tertiary)' }}>next {new Date(st.next_run).toLocaleTimeString()}</span>}
          <span style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
            <button className="btn btn-ghost" disabled={sync.isPending} onClick={() => sync.mutate()}><Icon name="sync" size={16} />{sync.isPending ? 'Synchronising…' : 'Sync now'}</button>
            <button className="btn btn-ghost" onClick={() => setEditing(e => !e)}><Icon name="settings" size={16} />{editing ? 'Close settings' : 'Settings'}</button>
          </span>
          {st.error && <div style={{ flexBasis: '100%', color: 'var(--warning)' }}>{st.error}</div>}
          {st.remotes.some(r => r.error) && (
            <ul style={{ flexBasis: '100%', margin: 0, color: 'var(--warning)' }}>
              {st.remotes.filter(r => r.error).map(r => <li key={r.url}>{r.url}: {r.error}</li>)}
            </ul>
          )}
          {st.signing_public_key && (
            <div style={{ flexBasis: '100%', fontSize: 'var(--text-xs)' }}>This node signs its commits with <code style={{ wordBreak: 'break-all' }}>{st.signing_public_key}</code></div>
          )}
        </div>
      )}
      {showForm && <SettingsForm key={s.version} initial={s} onSaved={() => { setEditing(false); qc.invalidateQueries({ queryKey: ['gitops'] }) }} />}
      {s.enabled && st && st.scopes.map(sc => <ScopeCard key={sc.scope} s={sc} head={st.head} />)}
      <NixOSRepo current={s.nixos_repo_id} />
    </div>
  )
}
