/**
 * pages/FleetPage.tsx - Fleet (Design 0001 section 5.7, phase 6)
 *
 * Any node can be the fleet manager: it registers other DPlaneOS nodes
 * (standalone or cluster members) with an API token of each, shows their
 * inventory and health, holds fleet-wide users, groups and shared settings
 * and rolls them out wave by wave. Remotes never depend on the manager.
 *
 * Calls:
 *   GET    /api/fleet                         → { settings, membership, remotes, resources, rollouts }
 *   PUT    /api/fleet/settings                { enabled, name }
 *   POST   /api/fleet/remotes                 { name, url, token, labels }
 *   DELETE /api/fleet/remotes/{id}
 *   POST   /api/fleet/poll
 *   PUT    /api/fleet/resources               { kind, key, payload }
 *   DELETE /api/fleet/resources/{kind}/{key}
 *   GET    /api/fleet/local/{kind}/{key}      → { payload }
 *   POST   /api/fleet/rollouts                { name, resources[], waves[], soak_secs, auto_advance }
 *   POST   /api/fleet/rollouts/{id}/{continue|pause|cancel}
 */

import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'

interface NodeStatus {
  node_id: string; name: string; version: string; mode: string; cluster?: string; quorate: boolean; coordinator: boolean
  maintenance: boolean; pools: { name: string; health: string }[]; groups_owned: string[]; conflicts: number; proposals: number
  holds: number; fleet?: string; reported_at: string
}
interface Remote {
  id: number; name: string; url: string; fingerprint: string; labels: Record<string, string>; node_id: string
  added_at: string; last_seen?: string; last_error?: string; status?: NodeStatus
}
interface FleetResource { kind: 'user' | 'group' | 'settings'; key: string; payload: Record<string, unknown> | null; version: number; updated_at: string; updated_by: string }
interface Target { remote_id: number; name: string; wave: number; state: 'pending' | 'done' | 'failed' | 'skipped'; detail?: string; attempts: number; applied_at?: string }
interface Rollout {
  id: number; name: string; state: 'running' | 'paused' | 'done' | 'failed' | 'cancelled'
  resources: { kind: string; key: string; version: number }[]; waves: { name: string; remotes: number[] }[]
  current_wave: number; soak_secs: number; auto_advance: boolean; wave_done_at?: string; message?: string
  created_by: string; created_at: string; targets: Target[]
}
interface Overview {
  success: boolean
  settings: { enabled: boolean; name: string }
  membership: { fleet: string; manager: string; last_apply_at?: string; last_rollout?: string; last_contact_at?: string; unreachable: boolean }
  remotes: Remote[]; resources: FleetResource[]; rollouts: Rollout[]
}
type Res = { success: boolean; error?: string }

const KIND_LABEL: Record<FleetResource['kind'], string> = { user: 'User', group: 'Group', settings: 'Shared settings' }
const STATE_BADGE: Record<Rollout['state'], string> = { running: 'badge-primary', paused: 'badge-warning', done: 'badge-success', failed: 'badge-error', cancelled: 'badge-neutral' }
const TARGET_BADGE: Record<Target['state'], string> = { pending: 'badge-neutral', done: 'badge-success', failed: 'badge-error', skipped: 'badge-neutral' }
const hint: React.CSSProperties = { fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }

function healthy(s?: NodeStatus) {
  if (!s) return false
  return s.pools.every(p => p.health === 'ONLINE') && s.mode !== 'isolated' && s.holds === 0 && s.conflicts === 0 && (!s.cluster || s.quorate)
}

function parseLabels(text: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const part of text.split(',').map(p => p.trim()).filter(Boolean)) {
    const [k, v = ''] = part.split('=').map(x => x.trim())
    out[k] = v
  }
  return out
}

// ── Role ──────────────────────────────────────────────────────────────────────

function RoleCard({ o }: { o: Overview }) {
  const qc = useQueryClient()
  const [name, setName] = useState(o.settings.name)
  const save = useMutation({
    mutationFn: async (enabled: boolean) => ensureOk(await api.put<Res>('/api/fleet/settings', { enabled, name: name.trim() })),
    onSuccess: () => { toast.success('Saved'); qc.invalidateQueries({ queryKey: ['fleet'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const { confirm, ConfirmDialog } = useConfirm()
  const leave = useMutation({
    mutationFn: async () => ensureOk(await api.delete<Res>('/api/fleet/membership')),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['fleet'] }); qc.invalidateQueries({ queryKey: ['config', 'sync', 'status'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const m = o.membership
  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <h3 style={{ marginTop: 0 }}>Fleet manager</h3>
      {o.settings.enabled ? (
        <p style={hint}>This node manages the fleet <strong>{o.settings.name}</strong>.</p>
      ) : (
        <p style={hint}>
          Make this node the fleet manager to see many DPlaneOS nodes in one place and roll out users, groups and shared settings to them.
          The nodes keep working on their own; they never depend on the manager.
        </p>
      )}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
        <input className="input" style={{ maxWidth: 260 }} value={name} onChange={e => setName(e.target.value)} placeholder="Fleet name, e.g. acme-storage" aria-label="Fleet name" />
        {o.settings.enabled
          ? <><button className="btn btn-ghost" disabled={save.isPending} onClick={() => save.mutate(true)}>Rename</button>
            <button className="btn btn-ghost" disabled={save.isPending} onClick={() => save.mutate(false)}>Stop managing</button></>
          : <button className="btn btn-primary" disabled={save.isPending || !name.trim()} onClick={() => save.mutate(true)}><Icon name="hub" size={16} />Make this node the fleet manager</button>}
      </div>
      <p style={{ ...hint, marginBottom: 0, marginTop: 12 }}>
        {m.fleet
          ? <>This node belongs to the fleet <strong>{m.fleet}</strong> (manager {m.manager || 'unknown'})
            {m.last_apply_at ? `; configuration last applied on ${new Date(m.last_apply_at).toLocaleString()}` : ''}{m.last_rollout ? ` (rollout ${m.last_rollout})` : ''}.
            {m.unreachable && <> <strong style={{ color: 'var(--warning)' }}>The manager has not contacted this node {m.last_contact_at ? `since ${new Date(m.last_contact_at).toLocaleString()}` : 'yet'}.</strong> If this node was removed from the fleet, leave it here.</>}</>
          : <>To add this node to a fleet, create an API token with admin scope (Security › API tokens) and enter it with this node's address on the fleet manager.</>}
      </p>
      {m.fleet && (
        <button className="btn btn-ghost btn-sm" style={{ marginTop: 8 }} disabled={leave.isPending} onClick={async () => {
          if (await confirm({ title: `Leave the fleet ${m.fleet}?`, message: 'This node stops expecting its fleet manager. Users, groups and settings the fleet applied stay. Revoke the API token you gave the manager (Security › API tokens) so it can no longer make changes here.', confirmLabel: 'Leave fleet' })) leave.mutate()
        }}>Leave fleet</button>
      )}
      <ConfirmDialog />
    </div>
  )
}

// ── Remotes ───────────────────────────────────────────────────────────────────

function AddRemote({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const [f, setF] = useState({ name: '', url: 'https://', token: '', labels: '' })
  const add = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res & { fingerprint: string }>('/api/fleet/remotes', {
      name: f.name.trim(), url: f.url.trim(), token: f.token.trim(), labels: parseLabels(f.labels) })),
    onSuccess: r => {
      toast.success(r.fingerprint ? `Added; certificate ${r.fingerprint.slice(0, 23)}… pinned` : 'Added')
      qc.invalidateQueries({ queryKey: ['fleet'] }); onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <Modal title="Add a node to the fleet" onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <label style={hint}>Name<input className="input" value={f.name} onChange={e => setF({ ...f, name: e.target.value })} placeholder="nas-berlin-1" /></label>
        <label style={hint}>Address<input className="input" value={f.url} onChange={e => setF({ ...f, url: e.target.value })} placeholder="https://nas-berlin-1.example.lan" /></label>
        <label style={hint}>API token of that node (admin scope)<input className="input" type="password" value={f.token} onChange={e => setF({ ...f, token: e.target.value })} placeholder="dpl_…" /></label>
        <label style={hint}>Labels (optional)<input className="input" value={f.labels} onChange={e => setF({ ...f, labels: e.target.value })} placeholder="site=berlin, tier=canary" /></label>
        <p style={{ ...hint, margin: 0 }}>The node must answer with the token now. Its TLS certificate is remembered; a different certificate is refused later.</p>
      </div>
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 12 }}>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" disabled={add.isPending || !f.name || !f.token} onClick={() => add.mutate()}>{add.isPending ? 'Contacting…' : 'Add'}</button>
      </div>
    </Modal>
  )
}

function RemotesCard({ o }: { o: Overview }) {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [adding, setAdding] = useState(false)
  const poll = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res>('/api/fleet/poll', {})),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['fleet'] }),
    onError: (e: Error) => toast.error(e.message),
  })
  const remove = useMutation({
    mutationFn: async (id: number) => ensureOk(await api.delete<Res>(`/api/fleet/remotes/${id}`)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['fleet'] }),
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <h3 style={{ margin: 0 }}>Nodes</h3>
        <span style={{ marginLeft: 'auto' }} />
        <button className="btn btn-ghost btn-sm" disabled={poll.isPending} onClick={() => poll.mutate()}><Icon name="refresh" size={14} />Refresh</button>
        <button className="btn btn-primary btn-sm" onClick={() => setAdding(true)}><Icon name="add" size={14} />Add node</button>
      </div>
      {o.remotes.length === 0 ? <p style={hint}>No nodes yet.</p> : (
        <div style={{ overflowX: 'auto' }}>
          <table className="data-table" style={{ fontSize: 'var(--text-sm)', marginTop: 8 }}>
            <thead><tr><th>Node</th><th>State</th><th>Cluster</th><th>Pools</th><th>Version</th><th>Labels</th><th>Last report</th><th /></tr></thead>
            <tbody>
              {o.remotes.map(r => {
                const s = r.status
                return (
                  <tr key={r.id}>
                    <td><strong>{r.name}</strong><div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{r.url}</div></td>
                    <td>
                      {r.last_error ? <span className="badge badge-error" title={r.last_error}>Not reachable</span>
                        : healthy(s) ? <span className="badge badge-success">Healthy</span>
                          : <span className="badge badge-warning">Needs attention</span>}
                      {s?.maintenance && <span className="badge badge-warning" style={{ marginLeft: 4 }}>Maintenance</span>}
                      {s && (s.holds > 0 || s.conflicts > 0 || s.proposals > 0) && (
                        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                          {[s.holds && `${s.holds} on hold`, s.conflicts && `${s.conflicts} conflicts`, s.proposals && `${s.proposals} proposals`].filter(Boolean).join(', ')}
                        </div>
                      )}
                      {r.last_error && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--error)', maxWidth: 260 }}>{r.last_error}</div>}
                    </td>
                    <td>{s?.cluster ? <>{s.cluster}{s.coordinator ? ' (coordinator)' : ''}{!s.quorate && <span style={{ color: 'var(--error)' }}> no quorum</span>}</> : <span style={{ color: 'var(--text-tertiary)' }}>standalone</span>}</td>
                    <td>{s ? (s.pools.length ? s.pools.map(p => <div key={p.name} style={{ color: p.health === 'ONLINE' ? undefined : 'var(--error)' }}>{p.name} {p.health}</div>) : '—') : '—'}</td>
                    <td>{s?.version ?? '—'}</td>
                    <td>{Object.entries(r.labels).map(([k, v]) => <span key={k} className="badge badge-neutral" style={{ marginRight: 4 }}>{v ? `${k}=${v}` : k}</span>)}</td>
                    <td style={{ fontSize: 'var(--text-xs)' }}>{r.last_seen ? new Date(r.last_seen).toLocaleString() : 'never'}</td>
                    <td><button className="btn btn-ghost btn-sm" aria-label={`Remove ${r.name}`} onClick={async () => {
                      if (await confirm({ title: `Remove ${r.name} from the fleet?`, message: 'The manager forgets the node and its token. Nothing changes on the node.', confirmLabel: 'Remove', danger: true })) remove.mutate(r.id)
                    }}><Icon name="link_off" size={14} /></button></td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}
      {adding && <AddRemote onClose={() => setAdding(false)} />}
      <ConfirmDialog />
    </div>
  )
}

// ── Fleet configuration ───────────────────────────────────────────────────────

function ResourceEditor({ initial, onClose }: { initial?: FleetResource; onClose: () => void }) {
  const qc = useQueryClient()
  const [kind, setKind] = useState<FleetResource['kind']>(initial?.kind ?? 'user')
  const p = (initial?.payload ?? {}) as Record<string, unknown>
  const [user, setUser] = useState({ username: String(p.username ?? ''), email: String(p.email ?? ''), role: String(p.role ?? 'user'), active: p.active !== false, password_hash: '' })
  const [group, setGroup] = useState({ name: String(p.name ?? ''), description: String(p.description ?? ''), gid: Number(p.gid ?? 0), members: ((p.members as string[]) ?? []).join(', ') })
  const [json, setJson] = useState(kind === 'settings' && initial?.payload ? JSON.stringify(initial.payload, null, 2) : '')
  const save = useMutation({
    mutationFn: async () => {
      let key: string, payload: Record<string, unknown>
      if (kind === 'user') {
        key = user.username.trim()
        payload = { username: key, email: user.email.trim(), role: user.role, active: user.active, ...(user.password_hash.trim() ? { password_hash: user.password_hash.trim() } : {}) }
      } else if (kind === 'group') {
        key = group.name.trim()
        payload = { name: key, description: group.description, gid: group.gid, members: group.members.split(',').map(x => x.trim()).filter(Boolean) }
      } else {
        key = 'settings'
        try { payload = JSON.parse(json) } catch { throw new Error('Not valid JSON') }
      }
      return ensureOk(await api.put<Res>('/api/fleet/resources', { kind, key, payload }))
    },
    onSuccess: () => { toast.success('Saved; roll it out to apply it'); qc.invalidateQueries({ queryKey: ['fleet'] }); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })
  const copy = useMutation({
    mutationFn: async () => ensureOk(await api.get<Res & { payload: Record<string, unknown> }>(`/api/fleet/local/settings/settings`)),
    onSuccess: r => setJson(JSON.stringify(r.payload, null, 2)),
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <Modal title={initial ? `Edit ${KIND_LABEL[initial.kind]} ${initial.key}` : 'Fleet-wide resource'} onClose={onClose} size="lg">
      {!initial && (
        <select className="input" style={{ maxWidth: 240, marginBottom: 10 }} value={kind} onChange={e => setKind(e.target.value as FleetResource['kind'])} aria-label="Kind">
          {Object.entries(KIND_LABEL).map(([k, l]) => <option key={k} value={k}>{l}</option>)}
        </select>
      )}
      {kind === 'user' && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
          <label style={hint}>User name<input className="input" disabled={!!initial} value={user.username} onChange={e => setUser({ ...user, username: e.target.value })} /></label>
          <label style={hint}>Email<input className="input" value={user.email} onChange={e => setUser({ ...user, email: e.target.value })} /></label>
          <label style={hint}>Role<select className="input" value={user.role} onChange={e => setUser({ ...user, role: e.target.value })}>
            {['admin', 'operator', 'user', 'viewer'].map(r => <option key={r}>{r}</option>)}</select></label>
          <label style={{ ...hint, display: 'flex', gap: 6, alignItems: 'center' }}><input type="checkbox" checked={user.active} onChange={e => setUser({ ...user, active: e.target.checked })} />Active</label>
          <label style={{ ...hint, gridColumn: '1 / -1' }}>Initial password as bcrypt hash (optional; set only when the user is created on a node)
            <input className="input" value={user.password_hash} onChange={e => setUser({ ...user, password_hash: e.target.value })} placeholder="$2b$12$…" style={{ fontFamily: 'var(--font-mono)' }} /></label>
        </div>
      )}
      {kind === 'group' && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
          <label style={hint}>Name<input className="input" disabled={!!initial} value={group.name} onChange={e => setGroup({ ...group, name: e.target.value })} /></label>
          <label style={hint}>GID<input className="input" type="number" value={group.gid} onChange={e => setGroup({ ...group, gid: Number(e.target.value) })} /></label>
          <label style={{ ...hint, gridColumn: '1 / -1' }}>Description<input className="input" value={group.description} onChange={e => setGroup({ ...group, description: e.target.value })} /></label>
          <label style={{ ...hint, gridColumn: '1 / -1' }}>Members (comma-separated)<input className="input" value={group.members} onChange={e => setGroup({ ...group, members: e.target.value })} /></label>
        </div>
      )}
      {kind === 'settings' && (
        <>
          <p style={hint}>Shared settings replace all of time zone, DNS, NTP, firewall ports, Samba and SSH settings on the nodes. Start from this node's and change what you need.</p>
          <button className="btn btn-ghost btn-sm" disabled={copy.isPending} onClick={() => copy.mutate()}><Icon name="content_copy" size={14} />Start from this node's settings</button>
          <textarea className="input" rows={14} value={json} onChange={e => setJson(e.target.value)} aria-label="Shared settings (JSON)"
            style={{ width: '100%', marginTop: 8, fontFamily: 'var(--font-mono)', fontSize: 12 }} />
        </>
      )}
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 12 }}>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" disabled={save.isPending || (kind === 'settings' && !json.trim())} onClick={() => save.mutate()}>Save</button>
      </div>
    </Modal>
  )
}

function ResourcesCard({ o }: { o: Overview }) {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [editing, setEditing] = useState<FleetResource | null | 'new'>(null)
  const del = useMutation({
    mutationFn: async (r: FleetResource) => ensureOk(await api.put<Res>('/api/fleet/resources', { kind: r.kind, key: r.key, payload: null })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['fleet'] }),
    onError: (e: Error) => toast.error(e.message),
  })
  const forget = useMutation({
    mutationFn: async (r: FleetResource) => ensureOk(await api.delete<Res>(`/api/fleet/resources/${r.kind}/${encodeURIComponent(r.key)}`)),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['fleet'] }),
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <h3 style={{ margin: 0 }}>Fleet-wide configuration</h3>
        <button className="btn btn-primary btn-sm" style={{ marginLeft: 'auto' }} onClick={() => setEditing('new')}><Icon name="add" size={14} />Add</button>
      </div>
      <p style={hint}>Nothing reaches the nodes until it is rolled out. On a node, the fleet's version wins; a change made there since is kept as a proposal on that node.</p>
      {o.resources.length === 0 ? <p style={hint}>None yet.</p> : (
        <table className="data-table" style={{ fontSize: 'var(--text-sm)' }}>
          <thead><tr><th>Kind</th><th>Name</th><th>Version</th><th>Changed</th><th /></tr></thead>
          <tbody>
            {o.resources.map(r => (
              <tr key={`${r.kind}/${r.key}`}>
                <td>{KIND_LABEL[r.kind]}</td>
                <td><code>{r.key}</code>{r.payload === null && <span className="badge badge-error" style={{ marginLeft: 6 }}>to be deleted</span>}</td>
                <td>{r.version}</td>
                <td style={{ fontSize: 'var(--text-xs)' }}>{new Date(r.updated_at).toLocaleString()}{r.updated_by ? ` by ${r.updated_by}` : ''}</td>
                <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                  {r.payload !== null && <button className="btn btn-ghost btn-sm" onClick={() => setEditing(r)}>Edit</button>}
                  {r.payload !== null && r.kind !== 'settings' && <button className="btn btn-ghost btn-sm" onClick={async () => {
                    if (await confirm({ title: `Delete ${r.key} on the nodes?`, message: 'Marked for deletion; the next rollout that includes it deletes it on the nodes.', confirmLabel: 'Mark for deletion', danger: true })) del.mutate(r)
                  }}>Delete on nodes</button>}
                  <button className="btn btn-ghost btn-sm" title="Stop managing it fleet-wide; nothing changes on the nodes" onClick={() => forget.mutate(r)}>Stop managing</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {editing && <ResourceEditor initial={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} />}
      <ConfirmDialog />
    </div>
  )
}

// ── Rollouts ──────────────────────────────────────────────────────────────────

function NewRollout({ o, onClose }: { o: Overview; onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const [chosen, setChosen] = useState<string[]>(o.resources.map(r => `${r.kind}/${r.key}`))
  const [canary, setCanary] = useState<number[]>(o.remotes.slice(0, 1).map(r => r.id))
  const [soakMin, setSoakMin] = useState(10)
  const [auto, setAuto] = useState(true)
  const rest = o.remotes.filter(r => !canary.includes(r.id)).map(r => r.id)
  const create = useMutation({
    mutationFn: async () => {
      const waves = [{ name: 'canary', remotes: canary }, ...(rest.length ? [{ name: 'everything else', remotes: rest }] : [])].filter(w => w.remotes.length)
      return ensureOk(await api.post<Res>('/api/fleet/rollouts', {
        name: name.trim(), resources: chosen.map(c => { const [kind, ...k] = c.split('/'); return { kind, key: k.join('/') } }),
        waves, soak_secs: soakMin * 60, auto_advance: auto,
      }))
    },
    onSuccess: () => { toast.success('Rollout started'); qc.invalidateQueries({ queryKey: ['fleet'] }); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })
  const toggle = <T,>(list: T[], set: (v: T[]) => void, v: T) => set(list.includes(v) ? list.filter(x => x !== v) : [...list, v])
  return (
    <Modal title="Roll out fleet configuration" onClose={onClose} size="lg">
      <label style={hint}>Name<input className="input" value={name} onChange={e => setName(e.target.value)} placeholder="new staff accounts" /></label>
      <div style={{ ...hint, marginTop: 10 }}>What to roll out:</div>
      {o.resources.map(r => {
        const id = `${r.kind}/${r.key}`
        return <label key={id} style={{ display: 'block', fontSize: 'var(--text-sm)' }}><input type="checkbox" checked={chosen.includes(id)} onChange={() => toggle(chosen, setChosen, id)} /> {KIND_LABEL[r.kind]} {r.key}{r.payload === null ? ' (delete)' : ''}</label>
      })}
      <div style={{ ...hint, marginTop: 10 }}>Wave 1 (canary): the nodes that get it first. All other nodes follow in wave 2.</div>
      {o.remotes.map(r => (
        <label key={r.id} style={{ display: 'inline-flex', gap: 4, marginRight: 12, fontSize: 'var(--text-sm)' }}>
          <input type="checkbox" checked={canary.includes(r.id)} onChange={() => toggle(canary, setCanary, r.id)} />{r.name}
        </label>
      ))}
      <div style={{ display: 'flex', gap: 12, alignItems: 'center', marginTop: 10, fontSize: 'var(--text-sm)', flexWrap: 'wrap' }}>
        <label>Wait <input className="input" type="number" min={0} style={{ width: 80, display: 'inline-block' }} value={soakMin} onChange={e => setSoakMin(Math.max(0, Number(e.target.value) || 0))} aria-label="Soak minutes" /> minutes after the canary</label>
        <label><input type="checkbox" checked={auto} onChange={e => setAuto(e.target.checked)} /> then continue by itself (off: you continue)</label>
      </div>
      <p style={{ ...hint, marginTop: 10 }}>If a node fails (after three attempts), the rollout pauses. Nothing is reverted; fix the cause and continue, or cancel.</p>
      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
        <button className="btn btn-primary" disabled={create.isPending || !name.trim() || chosen.length === 0 || o.remotes.length === 0} onClick={() => create.mutate()}>Start rollout</button>
      </div>
    </Modal>
  )
}

function RolloutsCard({ o }: { o: Overview }) {
  const qc = useQueryClient()
  const [creating, setCreating] = useState(false)
  const act = useMutation({
    mutationFn: async (v: { id: number; action: 'continue' | 'pause' | 'cancel' }) => ensureOk(await api.post<Res>(`/api/fleet/rollouts/${v.id}/${v.action}`, {})),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['fleet'] }),
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div className="card" style={{ marginBottom: 16 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <h3 style={{ margin: 0 }}>Rollouts</h3>
        <button className="btn btn-primary btn-sm" style={{ marginLeft: 'auto' }} disabled={o.resources.length === 0 || o.remotes.length === 0} onClick={() => setCreating(true)}><Icon name="rocket_launch" size={14} />New rollout</button>
      </div>
      {o.rollouts.length === 0 && <p style={hint}>No rollouts yet.</p>}
      {o.rollouts.map(r => {
        const waitingForOperator = r.state === 'running' && r.wave_done_at && !r.auto_advance && r.current_wave + 1 < r.waves.length
        return (
          <div key={r.id} style={{ borderTop: '1px solid var(--border-subtle)', padding: '10px 0' }}>
            <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
              <strong>{r.name}</strong>
              <span className={`badge ${STATE_BADGE[r.state]}`}>{r.state}</span>
              <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                wave {Math.min(r.current_wave + 1, r.waves.length)} of {r.waves.length} · {r.resources.map(x => `${x.kind} ${x.key}`).join(', ')}
              </span>
              <span style={{ marginLeft: 'auto', display: 'flex', gap: 6 }}>
                {(r.state === 'paused' || waitingForOperator) && <button className="btn btn-primary btn-sm" onClick={() => act.mutate({ id: r.id, action: 'continue' })}>Continue</button>}
                {r.state === 'running' && <button className="btn btn-ghost btn-sm" onClick={() => act.mutate({ id: r.id, action: 'pause' })}>Pause</button>}
                {(r.state === 'running' || r.state === 'paused') && <button className="btn btn-ghost btn-sm" onClick={() => act.mutate({ id: r.id, action: 'cancel' })}>Cancel</button>}
              </span>
            </div>
            {r.message && <p style={{ fontSize: 'var(--text-sm)', color: r.state === 'paused' ? 'var(--warning)' : 'var(--text-secondary)', margin: '4px 0' }}>{r.message}</p>}
            {r.waves.map((w, i) => (
              <div key={i} style={{ fontSize: 'var(--text-sm)', marginTop: 4 }}>
                <span style={{ color: 'var(--text-tertiary)' }}>Wave {i + 1} ({w.name}):</span>{' '}
                {r.targets.filter(t => t.wave === i).map(t => (
                  <span key={t.remote_id} className={`badge ${TARGET_BADGE[t.state]}`} title={t.detail} style={{ marginRight: 4 }}>
                    {t.name}: {t.state}{t.state === 'pending' && t.attempts > 0 ? ` (try ${t.attempts + 1})` : ''}
                  </span>
                ))}
              </div>
            ))}
          </div>
        )
      })}
      {creating && <NewRollout o={o} onClose={() => setCreating(false)} />}
    </div>
  )
}

export function FleetPage() {
  const q = useQuery({ queryKey: ['fleet'], queryFn: ({ signal }) => api.get<Overview>('/api/fleet', signal), refetchInterval: 10_000 })
  if (q.isLoading) return <div className="page-container"><Skeleton height={300} /></div>
  if (q.isError || !q.data) return <div className="page-container"><ErrorState error={q.error} onRetry={() => q.refetch()} /></div>
  const o = q.data
  return (
    <div className="page-container" style={{ maxWidth: 1100 }}>
      <header className="page-header">
        <h1 className="page-title">Fleet</h1>
        <p className="page-subtitle">Many DPlaneOS nodes in one view, and fleet-wide users, groups and settings rolled out wave by wave. See also the <Link to="/cluster">Cluster Map</Link> for this node's own cluster.</p>
      </header>
      <RoleCard key={o.settings.name} o={o} />
      {o.settings.enabled && <>
        <RemotesCard o={o} />
        <ResourcesCard o={o} />
        <RolloutsCard o={o} />
      </>}
    </div>
  )
}
