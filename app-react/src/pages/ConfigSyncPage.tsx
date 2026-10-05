/**
 * pages/ConfigSyncPage.tsx - Configuration sync between nodes (Design 0001, Phase 2)
 *
 * Paired nodes exchange changes to datasets, shares, NFS exports, users,
 * groups and replication jobs. Changes to different things merge
 * automatically; the same thing changed on two nodes is shown here for a
 * decision. A node that cannot reach its peers keeps working normally.
 *
 * Calls:
 *   GET    /api/config/sync/status
 *   POST   /api/config/sync/now
 *   POST   /api/config/peers/token            → { token, expires_at }
 *   POST   /api/config/peers/preview          { url, token } → { preview }
 *   POST   /api/config/peers/join             { url, token, self_url }
 *   DELETE /api/config/peers/{id}
 *   POST   /api/config/detach
 *   GET    /api/config/conflicts              → { conflicts[], pending[] }
 *   POST   /api/config/conflicts/{id}/resolve { choice, payload? }
 */

import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'
import { useSyncStatus, type SyncStatus } from '@/components/layout/NodeStateBanner'

interface FieldChange { path: string; old: unknown; new: unknown }
interface Revision { uid: string; kind: string; key: string; payload: Record<string, unknown> | null; origin_node: string; author: string; created_at: string }
interface Conflict { id: number; peer_id: string; peer_name: string; kind: string; key: string; local: Revision | null; remote: Revision | null; changes: FieldChange[]; created_at: string }
interface Pending { peer_name: string; kind: string; key: string; status: 'waiting' | 'blocked'; detail: string }
interface PreviewItem { kind: string; key: string; action: string; detail?: string; changes?: FieldChange[] }
interface Preview { node_id: string; name: string; fingerprint: string; counts: Record<string, number>; items: PreviewItem[] }

const KIND_LABELS: Record<string, string> = {
  dataset: 'Dataset', share: 'SMB share', nfs: 'NFS export', user: 'User', group: 'Group', replication: 'Replication',
  settings: 'Shared settings',
}

const MODES: Record<SyncStatus['mode'], { label: string; cls: string; text: string }> = {
  standalone:  { label: 'Standalone',  cls: 'badge-neutral', text: 'This node is not paired with other nodes.' },
  connected:   { label: 'Connected',   cls: 'badge-success', text: 'Changes are exchanged with all paired nodes.' },
  isolated:    { label: 'Isolated',    cls: 'badge-warning', text: 'At least one paired node cannot be reached. This node keeps working normally; changes are exchanged when the connection is back.' },
  independent: { label: 'Independent', cls: 'badge-neutral', text: 'This node was detached and runs on its own. You can pair it again at any time; you will see what changes before anything is merged.' },
}

const PREVIEW_ACTIONS: Record<string, { label: string; text: string }> = {
  add:      { label: 'Added here',        text: 'exists only on the other node; will be created on this node' },
  update:   { label: 'Updated here',      text: 'changed on the other node; this node takes the change' },
  delete:   { label: 'Deleted here',      text: 'deleted on the other node; will be deleted on this node (safety rules apply)' },
  send:     { label: 'Sent to the other', text: 'exists or changed only on this node; the other node takes it' },
  conflict: { label: 'Conflict',          text: 'different on both nodes; you decide after pairing' },
  wait:     { label: 'Waiting',           text: 'needs a pool that is not imported on this node' },
  same:     { label: 'Same',              text: 'identical on both nodes' },
}

function fmtValue(v: unknown): string {
  if (v === null || v === undefined || v === '') return '—'
  if (typeof v === 'string') return v.startsWith('sha256:') ? '(secret)' : v
  if (typeof v === 'boolean') return v ? 'yes' : 'no'
  return JSON.stringify(v)
}

function fmtTime(s: string | null | undefined) {
  return s ? new Date(s).toLocaleString() : '—'
}

function ChangeTable({ changes, left, right }: { changes: FieldChange[]; left: string; right: string }) {
  if (changes.length === 0) return <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>No differences</div>
  return (
    <table className="data-table" style={{ fontSize: 'var(--text-sm)' }}>
      <thead><tr><th>Setting</th><th>{left}</th><th>{right}</th></tr></thead>
      <tbody>
        {changes.map(c => (
          <tr key={c.path}>
            <td style={{ fontFamily: 'var(--font-mono)' }}>{c.path}</td>
            <td style={{ wordBreak: 'break-all' }}>{fmtValue(c.old)}</td>
            <td style={{ wordBreak: 'break-all' }}>{fmtValue(c.new)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

// ── Pairing ───────────────────────────────────────────────────────────────────

function InviteCard() {
  const [invite, setInvite] = useState<{ token: string; expires_at: string } | null>(null)
  const create = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; error?: string; token: string; expires_at: string }>('/api/config/peers/token', {})),
    onSuccess: r => setInvite({ token: r.token, expires_at: r.expires_at }),
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div className="card" style={{ flex: 1, minWidth: 300 }}>
      <h3 style={{ marginTop: 0 }}>Let another node join this one</h3>
      <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-sm)' }}>
        Create a join code, then on the other node choose <em>Join another node</em> and enter this node's address and the code.
        The code works once and expires after 15 minutes.
      </p>
      {invite ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>This node's address</label>
          <code style={{ padding: 8, background: 'var(--surface)', borderRadius: 6, wordBreak: 'break-all' }}>{window.location.origin}</code>
          <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Join code (valid until {new Date(invite.expires_at).toLocaleTimeString()})</label>
          <div style={{ display: 'flex', gap: 8 }}>
            <code style={{ flex: 1, padding: 8, background: 'var(--surface)', borderRadius: 6, wordBreak: 'break-all' }}>{invite.token}</code>
            <button className="btn btn-ghost btn-sm" onClick={() => { void navigator.clipboard?.writeText(invite.token); toast.success('Copied') }}>
              <Icon name="content_copy" size={14} />Copy
            </button>
          </div>
        </div>
      ) : (
        <button className="btn btn-primary" onClick={() => create.mutate()} disabled={create.isPending}>
          <Icon name="key" size={16} />Create join code
        </button>
      )}
    </div>
  )
}

function JoinCard() {
  const qc = useQueryClient()
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [selfUrl, setSelfUrl] = useState(window.location.origin)
  const [preview, setPreview] = useState<Preview | null>(null)

  const doPreview = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; error?: string; preview: Preview }>('/api/config/peers/preview', { url, token })),
    onSuccess: r => setPreview(r.preview),
    onError: (e: Error) => toast.error(e.message),
  })
  const join = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; error?: string }>('/api/config/peers/join', { url, token, self_url: selfUrl })),
    onSuccess: () => {
      toast.success(`Paired with ${preview?.name ?? 'the other node'}`)
      setPreview(null); setToken('')
      qc.invalidateQueries({ queryKey: ['config'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const order = ['conflict', 'delete', 'update', 'add', 'send', 'wait', 'same']
  return (
    <div className="card" style={{ flex: 1, minWidth: 300 }}>
      <h3 style={{ marginTop: 0 }}>Join another node</h3>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }} htmlFor="peer-url">Other node's address</label>
        <input id="peer-url" className="input" placeholder="https://nas2.example.lan" value={url} onChange={e => { setUrl(e.target.value); setPreview(null) }} />
        <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }} htmlFor="peer-token">Join code from the other node</label>
        <input id="peer-token" className="input" placeholder="dpj_..." value={token} onChange={e => { setToken(e.target.value.trim()); setPreview(null) }} />
        <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }} htmlFor="self-url">This node's address, as the other node reaches it</label>
        <input id="self-url" className="input" value={selfUrl} onChange={e => setSelfUrl(e.target.value)} />
        <button className="btn btn-ghost" onClick={() => doPreview.mutate()} disabled={!url || !token || doPreview.isPending}>
          <Icon name="preview" size={16} />{doPreview.isPending ? 'Comparing…' : 'Preview the merge'}
        </button>
      </div>
      {preview && (
        <div style={{ marginTop: 16 }}>
          <p style={{ fontSize: 'var(--text-sm)' }}>
            Pairing with <strong>{preview.name}</strong>
            {preview.fingerprint && <> · certificate <code style={{ fontSize: 'var(--text-xs)', wordBreak: 'break-all' }}>{preview.fingerprint}</code></>}
          </p>
          {preview.fingerprint && (
            <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              Compare this fingerprint with the certificate your browser shows for the other node. It is remembered and checked on every connection.
            </p>
          )}
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, marginBottom: 10 }}>
            {order.filter(a => preview.counts[a]).map(a => (
              <span key={a} className={`badge ${a === 'conflict' ? 'badge-warning' : a === 'delete' ? 'badge-error' : 'badge-neutral'}`} title={PREVIEW_ACTIONS[a].text}>
                {PREVIEW_ACTIONS[a].label}: {preview.counts[a]}
              </span>
            ))}
          </div>
          <div style={{ maxHeight: 280, overflowY: 'auto', border: '1px solid var(--border-subtle)', borderRadius: 8 }}>
            <table className="data-table" style={{ fontSize: 'var(--text-sm)' }}>
              <tbody>
                {[...preview.items].sort((a, b) => order.indexOf(a.action) - order.indexOf(b.action)).filter(i => i.action !== 'same').map(i => (
                  <tr key={`${i.kind}/${i.key}`}>
                    <td style={{ color: 'var(--text-tertiary)' }}>{KIND_LABELS[i.kind] ?? i.kind}</td>
                    <td style={{ fontFamily: 'var(--font-mono)' }}>{i.key}</td>
                    <td title={PREVIEW_ACTIONS[i.action]?.text}>{PREVIEW_ACTIONS[i.action]?.label ?? i.action}{i.detail ? ` (${i.detail})` : ''}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <button className="btn btn-primary" style={{ marginTop: 12 }} onClick={() => join.mutate()} disabled={join.isPending || !selfUrl}>
            <Icon name="link" size={16} />Pair with {preview.name}
          </button>
        </div>
      )}
    </div>
  )
}

// ── Conflicts ─────────────────────────────────────────────────────────────────

function ConflictCard({ c }: { c: Conflict }) {
  const qc = useQueryClient()
  const [editing, setEditing] = useState<string | null>(null)
  const resolve = useMutation({
    mutationFn: async (body: { choice: string; payload?: unknown }) =>
      ensureOk(await api.post<{ success: boolean; error?: string }>(`/api/config/conflicts/${c.id}/resolve`, body)),
    onSuccess: () => { toast.success('Conflict resolved'); setEditing(null); qc.invalidateQueries({ queryKey: ['config'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const label = `${KIND_LABELS[c.kind] ?? c.kind} "${c.key}"`
  const deletedHere = c.local && c.local.payload === null
  const deletedThere = c.remote && c.remote.payload === null

  function saveEdited() {
    if (editing === null) return
    try {
      resolve.mutate({ choice: 'merged', payload: JSON.parse(editing) })
    } catch {
      toast.error('Not valid JSON')
    }
  }

  return (
    <div className="card" style={{ padding: 16 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', marginBottom: 10 }}>
        <span className="badge badge-warning">Conflict</span>
        <strong>{label}</strong>
        <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>changed here and on {c.peer_name || c.peer_id}</span>
      </div>
      {deletedHere || deletedThere ? (
        <p style={{ fontSize: 'var(--text-sm)' }}>{deletedHere ? 'Deleted on this node' : 'Changed on this node'}; {deletedThere ? `deleted on ${c.peer_name}` : `changed on ${c.peer_name}`}.</p>
      ) : null}
      <ChangeTable changes={c.changes} left="This node" right={c.peer_name || 'Other node'} />
      <div style={{ display: 'flex', gap: 8, marginTop: 12, flexWrap: 'wrap' }}>
        <button className="btn btn-ghost btn-sm" onClick={() => resolve.mutate({ choice: 'local' })} disabled={resolve.isPending}>Keep this node's version</button>
        <button className="btn btn-ghost btn-sm" onClick={() => resolve.mutate({ choice: 'remote' })} disabled={resolve.isPending}>Take {c.peer_name || 'the other node'}'s version</button>
        {c.local?.payload && (
          <button className="btn btn-ghost btn-sm" onClick={() => setEditing(JSON.stringify(c.local?.payload, null, 2))} disabled={resolve.isPending}>Edit a combined version…</button>
        )}
      </div>
      {editing !== null && (
        <Modal title={`Combine ${label}`} onClose={() => setEditing(null)} size="lg">
          <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            Starts from this node's version. Change the settings you want from {c.peer_name}; keep the name.
          </p>
          <textarea className="input" style={{ width: '100%', minHeight: 280, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }}
            value={editing} onChange={e => setEditing(e.target.value)} aria-label="Combined version (JSON)" />
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, marginTop: 12 }}>
            <button className="btn btn-ghost" onClick={() => setEditing(null)}>Cancel</button>
            <button className="btn btn-primary" onClick={saveEdited} disabled={resolve.isPending}>Apply combined version</button>
          </div>
        </Modal>
      )}
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

export function ConfigSyncPage() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const status = useSyncStatus()
  const conflicts = useQuery({
    queryKey: ['config', 'conflicts'],
    queryFn: ({ signal }) => api.get<{ success: boolean; conflicts: Conflict[]; pending: Pending[] }>('/api/config/conflicts', signal),
    refetchInterval: 30_000,
  })
  const syncNow = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; error?: string }>('/api/config/sync/now', {})),
    onSuccess: () => { toast.success('Synchronised'); qc.invalidateQueries({ queryKey: ['config'] }) },
    onError: (e: Error) => { toast.error(e.message); qc.invalidateQueries({ queryKey: ['config'] }) },
  })
  const removePeer = useMutation({
    mutationFn: async (id: string) => ensureOk(await api.delete<{ success: boolean; error?: string }>(`/api/config/peers/${encodeURIComponent(id)}`)),
    onSuccess: () => { toast.success('Node removed'); qc.invalidateQueries({ queryKey: ['config'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const detach = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; error?: string }>('/api/config/detach', {})),
    onSuccess: () => { toast.success('This node now runs independently'); qc.invalidateQueries({ queryKey: ['config'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  if (status.isLoading) return <div className="page-container"><Skeleton height={300} /></div>
  if (status.isError || !status.data) return <div className="page-container"><ErrorState error={status.error} onRetry={() => status.refetch()} /></div>
  const st = status.data
  const mode = MODES[st.mode]

  async function askRemove(p: SyncStatus['peers'][number]) {
    if (await confirm({ title: `Remove ${p.name}?`, message: `This node stops exchanging changes with ${p.name}. The configuration on both nodes stays as it is.`, confirmLabel: 'Remove', danger: true })) {
      removePeer.mutate(p.id)
    }
  }
  async function askDetach() {
    if (await confirm({
      title: 'Detach this node?',
      message: 'This node keeps its whole configuration and runs on its own, like a standalone system. It stops exchanging changes with every paired node. You can pair it again later and see what would change before merging.',
      confirmLabel: 'Detach', danger: true,
    })) detach.mutate()
  }

  return (
    <div className="page-container">
      <header className="page-header" style={{ display: 'flex', alignItems: 'flex-end', gap: 16, flexWrap: 'wrap' }}>
        <div style={{ flex: 1, minWidth: 260 }}>
          <h1 className="page-title">Configuration Sync</h1>
          <p className="page-subtitle">Keep datasets, shares, NFS exports, users, groups, replication jobs and shared settings (time zone, DNS, NTP, firewall, Samba, SSH) in step across nodes. Each node keeps working on its own if the network fails.</p>
        </div>
        {st.peers.length > 0 && (
          <button className="btn btn-ghost" onClick={() => syncNow.mutate()} disabled={syncNow.isPending}>
            <Icon name="sync" size={16} />{syncNow.isPending ? 'Synchronising…' : 'Sync now'}
          </button>
        )}
      </header>

      <div className="card" style={{ marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <span className={`badge ${mode.cls}`}>{mode.label}</span>
          <strong>{st.name}</strong>
          {st.since && (st.mode === 'isolated' || st.mode === 'independent') && <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>since {fmtTime(st.since)}</span>}
          {st.mode !== 'standalone' && st.mode !== 'independent' && (
            <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} onClick={askDetach}>
              <Icon name="link_off" size={14} />Detach this node
            </button>
          )}
        </div>
        <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-sm)', marginBottom: 0 }}>{mode.text}</p>
        {!st.writer && (
          <p style={{ color: 'var(--warning)', fontSize: 'var(--text-sm)', marginBottom: 0 }}>
            This node does not exchange changes right now: {st.writer_reason || 'it is not the active node'}.
          </p>
        )}
      </div>

      {(conflicts.data?.conflicts.length ?? 0) > 0 && (
        <section style={{ marginBottom: 16 }}>
          <h2 style={{ fontSize: 'var(--text-lg)' }}>Needs your decision</h2>
          <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-sm)' }}>
            These were changed differently on two nodes. Nothing is overwritten until you choose; the choice is then applied on both nodes.
          </p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {conflicts.data!.conflicts.map(c => <ConflictCard key={c.id} c={c} />)}
          </div>
        </section>
      )}

      {(conflicts.data?.pending.length ?? 0) > 0 && (
        <section className="card" style={{ marginBottom: 16 }}>
          <h3 style={{ marginTop: 0 }}>Received but not applied yet</h3>
          <table className="data-table" style={{ fontSize: 'var(--text-sm)' }}>
            <thead><tr><th>From</th><th>Type</th><th>Name</th><th>Reason</th></tr></thead>
            <tbody>
              {conflicts.data!.pending.map(p => (
                <tr key={`${p.kind}/${p.key}/${p.peer_name}`}>
                  <td>{p.peer_name}</td>
                  <td>{KIND_LABELS[p.kind] ?? p.kind}</td>
                  <td style={{ fontFamily: 'var(--font-mono)' }}>{p.key}</td>
                  <td>{p.status === 'waiting' ? 'Waiting: ' : 'Refused: '}{p.detail}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 0 }}>Retried automatically every few minutes and on <em>Sync now</em>.</p>
        </section>
      )}

      {st.peers.length > 0 && (
        <section className="card" style={{ marginBottom: 16, padding: 0, overflow: 'hidden' }}>
          <table className="data-table">
            <thead><tr><th>Paired node</th><th>Address</th><th>Status</th><th>Last contact</th><th>Not yet sent</th><th /></tr></thead>
            <tbody>
              {st.peers.map(p => (
                <tr key={p.id}>
                  <td><strong>{p.name || p.id}</strong></td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }} title={p.fingerprint_display ? `Certificate ${p.fingerprint_display}` : undefined}>{p.url}</td>
                  <td>
                    {p.reachable ? <span className="badge badge-success">Reachable</span>
                      : p.last_contact ? <span className="badge badge-warning" title={p.last_error}>Unreachable since {fmtTime(p.unreachable_since)}</span>
                      : <span className="badge badge-neutral" title={p.last_error}>Not contacted yet</span>}
                    {p.last_error && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', maxWidth: 360, wordBreak: 'break-word' }}>{p.last_error}</div>}
                  </td>
                  <td style={{ fontSize: 'var(--text-sm)' }}>{fmtTime(p.last_contact)}</td>
                  <td>{p.waiting}</td>
                  <td style={{ textAlign: 'right' }}>
                    <button className="btn btn-ghost btn-sm" onClick={() => askRemove(p)} aria-label={`Remove ${p.name}`}><Icon name="link_off" size={14} />Remove</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}

      <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
        <InviteCard />
        <JoinCard />
      </div>
      <ConfirmDialog />
    </div>
  )
}
