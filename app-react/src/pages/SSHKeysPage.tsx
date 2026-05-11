/**
 * pages/SSHKeysPage.tsx - SSH Authorized Key Management + Daemon Settings
 *
 * Tab 1 (Keys): Manages SSH public keys in each system user's authorized_keys.
 * Tab 2 (Daemon): NixOS SSH daemon settings (port, password auth, permit root login).
 *
 * API:
 *   GET  /api/ssh/status             → { active, port }
 *   GET  /api/ssh/keys               → { keys: SSHManagedKey[] }
 *   GET  /api/ssh/keys?username=X    → filtered list
 *   POST /api/ssh/keys               → add key { username, label, public_key }
 *   DELETE /api/ssh/keys/{id}        → remove key
 *   GET  /api/system/ssh-daemon      → { port, password_auth, permit_root_login }
 *   POST /api/system/ssh-daemon      → update daemon settings
 *   GET  /api/rbac/users             → system user list
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Spinner, Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { Modal } from '@/components/ui/Modal'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'
import type React from 'react'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface SSHManagedKey {
  id: string
  username: string
  label: string
  public_key: string
  key_type: string
  comment: string
  added_by: string
  added_at: string
  imported: boolean
}

interface KeysResponse  { success: boolean; keys: SSHManagedKey[] }
interface StatusResponse { success: boolean; active: boolean; port: string }

interface SystemUser { id: number; username: string; active: boolean }
interface UsersResponse { users: SystemUser[] }

interface SSHDaemonResponse {
  success: boolean
  port: number
  password_auth: boolean | null
  permit_root_login: string
}

// ---------------------------------------------------------------------------
// Key type badge
// ---------------------------------------------------------------------------

function KeyTypeBadge({ keyType }: { keyType: string }) {
  const color = keyType === 'ssh-ed25519'
    ? 'var(--success)'
    : keyType.startsWith('ecdsa')
    ? 'var(--primary)'
    : 'var(--text-tertiary)'

  const short = keyType === 'ssh-ed25519' ? 'ed25519'
    : keyType === 'ssh-rsa' ? 'RSA'
    : keyType.startsWith('ecdsa') ? 'ECDSA'
    : keyType.replace('ssh-', '')

  return (
    <span style={{ fontFamily: 'var(--font-mono)', fontSize: 11, fontWeight: 700, color, background: color + '18', padding: '2px 7px', borderRadius: 'var(--radius-xs)', border: `1px solid ${color}40` }}>
      {short}
    </span>
  )
}

// ---------------------------------------------------------------------------
// Add key modal
// ---------------------------------------------------------------------------

interface AddKeyModalProps {
  users: SystemUser[]
  defaultUsername?: string
  onClose: () => void
}

function AddKeyModal({ users, defaultUsername, onClose }: AddKeyModalProps) {
  const qc = useQueryClient()
  const [username, setUsername] = useState(defaultUsername ?? (users[0]?.username ?? ''))
  const [label, setLabel]       = useState('')
  const [pubKey, setPubKey]     = useState('')

  const mut = useMutation({
    mutationFn: () => api.post<{ success: boolean; key: SSHManagedKey }>('/api/ssh/keys', {
      username,
      label: label.trim(),
      public_key: pubKey.trim(),
    }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['ssh', 'keys'] })
      toast.success('SSH key added')
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!username) { toast.error('Select a user'); return }
    if (!pubKey.trim()) { toast.error('Public key is required'); return }
    mut.mutate()
  }

  return (
    <Modal title="Add SSH Public Key" onClose={onClose}>
      <form onSubmit={handleSubmit}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>System user</label>
            <select className="input" value={username} onChange={e => setUsername(e.target.value)} disabled={mut.isPending}>
              {users.map(u => <option key={u.username} value={u.username}>{u.username}</option>)}
            </select>
            <p style={{ marginTop: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              The key will be written to this user's ~/.ssh/authorized_keys.
            </p>
          </div>

          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>
              Label <span style={{ color: 'var(--text-tertiary)', fontWeight: 400 }}>(optional)</span>
            </label>
            <input className="input" type="text" placeholder="e.g. Work laptop, GitHub Actions"
              value={label} onChange={e => setLabel(e.target.value)} disabled={mut.isPending} />
          </div>

          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Public key</label>
            <textarea className="input" rows={4} placeholder="ssh-ed25519 AAAA... user@host"
              value={pubKey} onChange={e => setPubKey(e.target.value)} disabled={mut.isPending}
              style={{ fontFamily: 'var(--font-mono)', fontSize: 12, resize: 'vertical' }} />
            <p style={{ marginTop: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              Paste the contents of your <code>~/.ssh/id_ed25519.pub</code> or equivalent public key file.
            </p>
          </div>
        </div>

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 10, marginTop: 20 }}>
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={mut.isPending}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={mut.isPending}>
            {mut.isPending
              ? <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Spinner size={14} color="rgba(0,0,0,0.7)" /> Adding…</span>
              : <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Icon name="add" size={15} /> Add Key</span>
            }
          </button>
        </div>
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// Daemon settings tab
// ---------------------------------------------------------------------------

const PERMIT_ROOT_OPTIONS = [
  { value: '', label: 'NixOS default' },
  { value: 'no', label: 'No' },
  { value: 'prohibit-password', label: 'Prohibit password (key-only)' },
  { value: 'forced-commands-only', label: 'Forced commands only' },
  { value: 'yes', label: 'Yes (not recommended)' },
]

function DaemonSettingsTab() {
  const qc = useQueryClient()

  const { data, isLoading, error } = useQuery<SSHDaemonResponse>({
    queryKey: ['ssh', 'daemon'],
    queryFn: () => api.get<SSHDaemonResponse>('/api/system/ssh-daemon'),
  })

  const [port, setPort]                   = useState<string>('')
  const [passwordAuth, setPasswordAuth]   = useState<string>('unset')
  const [permitRoot, setPermitRoot]       = useState<string>('')
  const [initialised, setInitialised]     = useState(false)

  if (data && !initialised) {
    setPort(data.port > 0 ? String(data.port) : '')
    setPasswordAuth(data.password_auth === null ? 'unset' : data.password_auth ? 'true' : 'false')
    setPermitRoot(data.permit_root_login ?? '')
    setInitialised(true)
  }

  const mut = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = {}
      const p = parseInt(port, 10)
      if (port !== '' && !isNaN(p)) body.port = p
      if (passwordAuth === 'true')  body.password_auth = true
      if (passwordAuth === 'false') body.password_auth = false
      body.permit_root_login = permitRoot
      return api.post('/api/system/ssh-daemon', body)
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['ssh', 'daemon'] })
      toast.success('SSH daemon settings saved - apply NixOS config to activate')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  if (isLoading) return (
    <div className="card" style={{ padding: 24 }}>
      <Skeleton style={{ height: 120 }} />
    </div>
  )
  if (error) return <ErrorState error={error} title="Failed to load SSH daemon settings" />

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      <div className="card" style={{ padding: 24 }}>
        <h3 style={{ margin: '0 0 4px', fontSize: 'var(--text-base)', fontWeight: 600 }}>SSH Daemon Settings</h3>
        <p style={{ margin: '0 0 20px', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          Settings are written to the NixOS state file. Use Apply NixOS Configuration to activate changes.
        </p>

        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: 20 }}>
          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>
              Listen port
            </label>
            <input
              className="input"
              type="number"
              min={1}
              max={65535}
              placeholder="22 (NixOS default)"
              value={port}
              onChange={e => setPort(e.target.value)}
              disabled={mut.isPending}
            />
            <p style={{ marginTop: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              Leave blank to use the NixOS default (22).
            </p>
          </div>

          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>
              Password authentication
            </label>
            <select
              className="input"
              value={passwordAuth}
              onChange={e => setPasswordAuth(e.target.value)}
              disabled={mut.isPending}
            >
              <option value="unset">NixOS default</option>
              <option value="false">Disabled (keys only)</option>
              <option value="true">Enabled</option>
            </select>
          </div>

          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>
              Permit root login
            </label>
            <select
              className="input"
              value={permitRoot}
              onChange={e => setPermitRoot(e.target.value)}
              disabled={mut.isPending}
            >
              {PERMIT_ROOT_OPTIONS.map(o => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </div>
        </div>

        <div style={{ marginTop: 24, display: 'flex', justifyContent: 'flex-end' }}>
          <button className="btn btn-primary" onClick={() => mut.mutate()} disabled={mut.isPending}>
            {mut.isPending
              ? <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Spinner size={14} color="rgba(0,0,0,0.7)" /> Saving…</span>
              : <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Icon name="save" size={15} /> Save Settings</span>
            }
          </button>
        </div>
      </div>

      <div className="card" style={{ padding: 16, background: 'var(--bg-secondary)' }}>
        <div style={{ display: 'flex', alignItems: 'flex-start', gap: 10 }}>
          <Icon name="info" size={16} style={{ color: 'var(--text-tertiary)', marginTop: 1, flexShrink: 0 }} />
          <p style={{ margin: 0, fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.6 }}>
            These settings map to <code>services.openssh</code> in NixOS. Changing the port will also require updating your firewall rules.
            Disabling password authentication without at least one authorized key in your account will lock you out.
          </p>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function SSHKeysPage() {
  const qc = useQueryClient()
  const [tab, setTab]                 = useState<'keys' | 'daemon'>('keys')
  const [showAdd, setShowAdd]         = useState(false)
  const [filterUser, setFilterUser]   = useState<string>('all')
  const { confirm, ConfirmDialog }    = useConfirm()

  const { data: statusData } = useQuery<StatusResponse>({
    queryKey: ['ssh', 'status'],
    queryFn: () => api.get<StatusResponse>('/api/ssh/status'),
    refetchInterval: 30000,
  })

  const { data: keysData, isLoading: keysLoading, error: keysError, refetch } = useQuery<KeysResponse>({
    queryKey: ['ssh', 'keys'],
    queryFn: () => api.get<KeysResponse>('/api/ssh/keys'),
  })

  const { data: usersData } = useQuery<UsersResponse>({
    queryKey: ['rbac', 'users'],
    queryFn: () => api.get<UsersResponse>('/api/rbac/users'),
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.delete(`/api/ssh/keys/${id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['ssh', 'keys'] })
      toast.success('Key removed')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  async function handleDelete(key: SSHManagedKey) {
    const label = key.label || key.comment || key.key_type
    const ok = await confirm({
      title: 'Remove SSH key?',
      message: `"${label}" will be removed from ${key.username}'s authorized_keys. That client will no longer be able to log in via this key.`,
      danger: true,
      confirmLabel: 'Remove',
    })
    if (ok) deleteMut.mutate(key.id)
  }

  const allKeys = keysData?.keys ?? []
  const activeUsers = usersData?.users?.filter(u => u.active) ?? []

  // Unique usernames that have at least one key
  const usersWithKeys = [...new Set(allKeys.map(k => k.username))].sort()

  const displayed = filterUser === 'all' ? allKeys : allKeys.filter(k => k.username === filterUser)

  // Group by username for the table
  const grouped = usersWithKeys.reduce<Record<string, SSHManagedKey[]>>((acc, u) => {
    acc[u] = displayed.filter(k => k.username === u)
    return acc
  }, {})

  const status = statusData

  return (
    <div className="page-container">
      <header className="page-header">
        <div>
          <h1 className="page-title">SSH</h1>
          <p className="page-subtitle">Manage authorized keys and SSH daemon settings.</p>
        </div>
        {tab === 'keys' && (
          <button className="btn btn-primary" onClick={() => setShowAdd(true)} disabled={activeUsers.length === 0}>
            <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Icon name="add" size={16} /> Add Key</span>
          </button>
        )}
      </header>

      {/* ── SSH daemon status banner ── */}
      <div className="card" style={{ padding: '12px 20px', marginBottom: 20 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 20 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span className={`status-dot ${status?.active ? 'online' : 'offline'}`} />
            <span style={{ fontWeight: 600, fontSize: 'var(--text-sm)', color: status?.active ? 'var(--success)' : 'var(--text-tertiary)' }}>
              SSH daemon {status?.active ? 'running' : 'stopped'}
            </span>
          </div>
          {status?.active && status.port && (
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
              Port <span style={{ fontFamily: 'var(--font-mono)', fontWeight: 600 }}>{status.port}</span>
            </span>
          )}
        </div>
      </div>

      {/* ── Tab bar ── */}
      <div className="tabs" style={{ marginBottom: 20 }}>
        <button className={`tab ${tab === 'keys' ? 'tab-active' : ''}`} onClick={() => setTab('keys')}>
          Authorized Keys
        </button>
        <button className={`tab ${tab === 'daemon' ? 'tab-active' : ''}`} onClick={() => setTab('daemon')}>
          Daemon Settings
        </button>
      </div>

      {tab === 'daemon' ? (
        <DaemonSettingsTab />
      ) : (
        <>
          {/* ── Filter bar ── */}
          {usersWithKeys.length > 1 && (
            <div className="tabs" style={{ marginBottom: 20 }}>
              <button className={`tab ${filterUser === 'all' ? 'tab-active' : ''}`} onClick={() => setFilterUser('all')}>
                All users
              </button>
              {usersWithKeys.map(u => (
                <button key={u} className={`tab ${filterUser === u ? 'tab-active' : ''}`} onClick={() => setFilterUser(u)}>
                  {u}
                </button>
              ))}
            </div>
          )}

          {/* ── Key table ── */}
          {keysLoading ? (
            <div className="card" style={{ padding: 24 }}>
              {[0, 1, 2].map(i => <Skeleton key={i} style={{ height: 48, marginBottom: 10 }} />)}
            </div>
          ) : keysError ? (
            <ErrorState error={keysError} title="Failed to load SSH keys" onRetry={() => refetch()} />
          ) : allKeys.length === 0 ? (
            <div className="empty-state">
              <Icon name="key" size={48} className="empty-state-icon" />
              <h3 className="empty-state-title">No SSH keys yet</h3>
              <p className="empty-state-body">
                Add a public key to allow passwordless SSH login for a system user.
                Existing authorized_keys entries are automatically imported on first use.
              </p>
            </div>
          ) : filterUser !== 'all' ? (
            <SingleUserTable
              keys={displayed}
              username={filterUser}
              onDelete={handleDelete}
              pending={deleteMut.isPending}
            />
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
              {usersWithKeys.filter(u => (grouped[u]?.length ?? 0) > 0).map(u => (
                <SingleUserTable
                  key={u}
                  keys={grouped[u] ?? []}
                  username={u}
                  onDelete={handleDelete}
                  pending={deleteMut.isPending}
                />
              ))}
            </div>
          )}
        </>
      )}

      {showAdd && (
        <AddKeyModal
          users={activeUsers}
          defaultUsername={filterUser !== 'all' ? filterUser : undefined}
          onClose={() => setShowAdd(false)}
        />
      )}

      <ConfirmDialog />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Per-user key table
// ---------------------------------------------------------------------------

function SingleUserTable({ keys, username, onDelete, pending }: {
  keys: SSHManagedKey[]
  username: string
  onDelete: (k: SSHManagedKey) => void
  pending: boolean
}) {
  return (
    <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
      <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 10 }}>
        <Icon name="person" size={16} style={{ color: 'var(--text-tertiary)' }} />
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 14, fontWeight: 600 }}>{username}</span>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
          {keys.length} key{keys.length !== 1 ? 's' : ''}
        </span>
      </div>
      <table className="data-table">
        <thead>
          <tr>
            <th>Type</th>
            <th>Label / Comment</th>
            <th>Public key</th>
            <th>Added by</th>
            <th>Added</th>
            <th style={{ textAlign: 'right' }}></th>
          </tr>
        </thead>
        <tbody>
          {keys.map(k => (
            <tr key={k.id} style={{ opacity: k.imported ? 0.75 : 1 }}>
              <td><KeyTypeBadge keyType={k.key_type} /></td>
              <td>
                <div style={{ fontWeight: k.label ? 500 : 400, color: k.label ? 'var(--text)' : 'var(--text-tertiary)' }}>
                  {k.label || k.comment || <span style={{ fontStyle: 'italic' }}>no label</span>}
                </div>
                {k.imported && (
                  <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>imported</div>
                )}
              </td>
              <td style={{ maxWidth: 300 }}>
                <code style={{ fontFamily: 'var(--font-mono)', fontSize: 11, color: 'var(--text-secondary)', display: 'block', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {k.public_key}
                </code>
              </td>
              <td style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{k.added_by}</td>
              <td style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                {new Date(k.added_at).toLocaleDateString()}
              </td>
              <td style={{ textAlign: 'right' }}>
                <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                  <button className="btn btn-ghost btn-xs" style={{ padding: '4px 8px' }}
                    onClick={() => { navigator.clipboard.writeText(k.public_key); toast.success('Copied') }}
                    title="Copy public key">
                    <Icon name="content_copy" size={13} />
                  </button>
                  <button className="btn btn-ghost btn-xs" style={{ padding: '4px 8px', color: 'var(--error)' }}
                    onClick={() => onDelete(k)} disabled={pending}
                    title="Remove key">
                    <Icon name="delete" size={13} />
                  </button>
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
