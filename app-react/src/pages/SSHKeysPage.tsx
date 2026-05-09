/**
 * pages/SSHKeysPage.tsx - SSH Authorized Key Management
 *
 * Manages SSH public keys in each system user's ~/.ssh/authorized_keys.
 * SSH daemon settings (port, password auth) are NixOS-managed and shown
 * here as read-only status only.
 *
 * API:
 *   GET  /api/ssh/status           → { active, port }
 *   GET  /api/ssh/keys             → { keys: SSHManagedKey[] }
 *   GET  /api/ssh/keys?username=X  → filtered list
 *   POST /api/ssh/keys             → add key { username, label, public_key }
 *   DELETE /api/ssh/keys/{id}      → remove key
 *   GET  /api/rbac/users           → system user list
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
// Page
// ---------------------------------------------------------------------------

export function SSHKeysPage() {
  const qc = useQueryClient()
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
          <h1 className="page-title">SSH Keys</h1>
          <p className="page-subtitle">Manage SSH public keys for system user accounts.</p>
        </div>
        <button className="btn btn-primary" onClick={() => setShowAdd(true)} disabled={activeUsers.length === 0}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Icon name="add" size={16} /> Add Key</span>
        </button>
      </header>

      {/* ── SSH daemon status ── */}
      <div className="card" style={{ padding: 20, marginBottom: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 12 }}>
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
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            SSH daemon settings (port, password auth) are managed via NixOS configuration.
          </span>
        </div>
      </div>

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
