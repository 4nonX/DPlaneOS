/**
 * pages/UsersPage.tsx — Users, Groups & Roles (Phase 5)
 *
 * Tabs: Users | Groups | Roles
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/rbac/users                    → { success, users: User[] }
 *   GET  /api/rbac/users?id={id}            → { success, user: User }
 *   POST /api/rbac/users  {action:'create'} → create user
 *   POST /api/rbac/users  {action:'update'} → update user
 *   POST /api/rbac/users  {action:'delete'} → delete user
 *   GET  /api/rbac/groups                   → { success, groups: Group[] }
 *   POST /api/rbac/groups                   → create/update/delete group
 *   GET  /api/rbac/roles                    → { success, roles: Role[] }
 *   GET  /api/rbac/roles/{id}               → { success, role: Role }
 *   POST /api/rbac/roles                    → create role
 *   PUT  /api/rbac/roles/{id}               → update role
 *   DELETE /api/rbac/roles/{id}             → delete role
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { Modal } from '@/components/ui/Modal'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface User {
  id:             number
  username:       string
  email?:         string
  role:           string
  created_at:     string
  last_login_at?: string
  locked_until?:  string
}

interface Group {
  id?:      number
  name:     string
  gid?:     number
  members?: string[]
}

interface Role {
  id:          number | string
  name:        string
  description?: string
  permissions?: string[]
}

interface UsersResponse  { success: boolean; users:  User[]  }
interface GroupsResponse { success: boolean; groups: Group[] }
interface RolesResponse  { success: boolean; roles:  Role[]  }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fmtDate(s?: string) {
  if (!s) return 'Never'
  try { return new Date(s).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'short' }) }
  catch { return s }
}

function isLocked(u: User): boolean {
  return !!(u.locked_until && new Date(u.locked_until) > new Date())
}

const ROLE_COLORS: Record<string, string> = {
  admin: 'var(--error)',
  user: 'var(--primary)',
  readonly: 'var(--text-secondary)',
}

function RoleBadge({ role }: { role: string }) {
  const c = ROLE_COLORS[role] ?? 'var(--info)'
  return (
    <span style={{ padding: '2px 8px', borderRadius: 'var(--radius-sm)', background: `${c}18`, border: `1px solid ${c}30`, color: c, fontSize: 'var(--text-xs)', fontWeight: 700 }}>
      {role}
    </span>
  )
}

// ---------------------------------------------------------------------------
// UserModal (create + edit)
// ---------------------------------------------------------------------------

function UserModal({ user, onClose, onDone }: { user?: User; onClose: () => void; onDone: () => void }) {
  const [username, setUsername] = useState(user?.username ?? '')
  const [email, setEmail] = useState(user?.email ?? '')
  const [role, setRole] = useState(user?.role ?? 'user')
  const [password, setPassword] = useState('')

  const isEdit = !!user

  const mutation = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = isEdit
        ? { action: 'update', id: user!.id, email, role, ...(password ? { password } : {}) }
        : { action: 'create', username, email, password, role }
      return api.post('/api/rbac/users', body)
    },
    onSuccess: () => { toast.success(isEdit ? 'User updated' : 'User created'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={isEdit ? `Edit: ${user!.username}` : 'Create User'} onClose={onClose}>
      {!isEdit && (
        <label className="field">
          <span className="field-label">Username</span>
          <input value={username} onChange={e => setUsername(e.target.value)} className="input" autoFocus />
        </label>
      )}
      <label className="field">
        <span className="field-label">Email</span>
        <input type="email" value={email} onChange={e => setEmail(e.target.value)} className="input" />
      </label>
      <label className="field">
        <span className="field-label">Role</span>
        <select value={role} onChange={e => setRole(e.target.value)}
          className="input" style={{ appearance: 'none' }}>
          {['admin', 'user', 'readonly'].map(r => <option key={r} value={r}>{r}</option>)}
        </select>
      </label>
      <label className="field">
        <span className="field-label">
          {isEdit ? 'New Password (leave blank to keep current)' : 'Password'}
        </span>
        <input type="password" value={password} onChange={e => setPassword(e.target.value)} className="input" autoComplete="new-password" />
      </label>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending} className="btn btn-primary">
          {mutation.isPending ? 'Saving…' : isEdit ? 'Save' : 'Create'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// UsersTab
// ---------------------------------------------------------------------------

function UsersTab() {
  const qc = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)
  const [editUser, setEditUser] = useState<User | null>(null)

  const usersQ = useQuery({
    queryKey: ['rbac', 'users'],
    queryFn: ({ signal }) => api.get<UsersResponse>('/api/rbac/users', signal),
  })

  const deleteUser = useMutation({
    mutationFn: (id: number) => api.post('/api/rbac/users', { action: 'delete', id }),
    onSuccess: () => { toast.success('User deleted'); qc.invalidateQueries({ queryKey: ['rbac', 'users'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  function refresh() { qc.invalidateQueries({ queryKey: ['rbac', 'users'] }) }

  const users = usersQ.data?.users ?? []

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
        <button onClick={() => setShowCreate(true)} className="btn btn-primary"><Icon name="person_add" size={15} />New User</button>
      </div>

      {usersQ.isLoading && <Skeleton height={200} />}
      {usersQ.isError && <ErrorState error={usersQ.error} onRetry={refresh} />}

      {!usersQ.isLoading && !usersQ.isError && (
        <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
          <table className="data-table">
            <thead>
              <tr style={{ background: 'rgba(255,255,255,0.03)' }}>
                {['User', 'Email', 'Role', 'Last Login', 'Status', 'Actions'].map(h => (
                  <th key={h}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {users.map(u => (
                <tr key={u.id}
                  onMouseEnter={e => (e.currentTarget.style.background = 'rgba(255,255,255,0.02)')}
                  onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
                >
                  <td>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                      <div style={{ width: 32, height: 32, borderRadius: '50%', background: 'var(--primary-bg)', border: '1px solid rgba(138,156,255,0.2)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontWeight: 700, fontSize: 'var(--text-sm)', color: 'var(--primary)', flexShrink: 0 }}>
                        {u.username.charAt(0).toUpperCase()}
                      </div>
                      <div>
                        <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>{u.username}</div>
                        <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>ID: {u.id}</div>
                      </div>
                    </div>
                  </td>
                  <td style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{u.email ?? '—'}</td>
                  <td><RoleBadge role={u.role} /></td>
                  <td style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', whiteSpace: 'nowrap' }}>{fmtDate(u.last_login_at)}</td>
                  <td>
                    {isLocked(u)
                      ? <span className="badge badge-error">Locked</span>
                      : <span className="badge badge-success">Active</span>
                    }
                  </td>
                  <td>
                    <div style={{ display: 'flex', gap: 6 }}>
                      <button onClick={() => setEditUser(u)} className="btn btn-ghost"><Icon name="edit" size={14} /></button>
                      <button onClick={() => { if (window.confirm(`Delete "${u.username}"?`)) deleteUser.mutate(u.id) }} className="btn btn-danger"><Icon name="delete" size={14} /></button>
                    </div>
                  </td>
                </tr>
              ))}
              {users.length === 0 && (
                <tr><td colSpan={6} style={{ padding: '40px 16px', textAlign: 'center', color: 'var(--text-tertiary)' }}>No users found</td></tr>
              )}
            </tbody>
          </table>
        </div>
      )}

      {showCreate && <UserModal onClose={() => setShowCreate(false)} onDone={refresh} />}
      {editUser   && <UserModal user={editUser} onClose={() => setEditUser(null)} onDone={refresh} />}
    </>
  )
}

// ---------------------------------------------------------------------------
// GroupModal
// ---------------------------------------------------------------------------

function GroupModal({ group, onClose, onDone }: { group?: Group; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState(group?.name ?? '')
  const [gid, setGid] = useState(String(group?.gid ?? ''))
  const [members, setMembers] = useState((group?.members ?? []).join(', '))
  const isEdit = !!group

  const mutation = useMutation({
    mutationFn: () => {
      const body = { action: isEdit ? 'update' : 'create', name, gid: gid ? Number(gid) : undefined, members: members.split(',').map(m => m.trim()).filter(Boolean) }
      return api.post('/api/rbac/groups', body)
    },
    onSuccess: () => { toast.success(isEdit ? 'Group updated' : 'Group created'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={isEdit ? `Edit: ${group!.name}` : 'Create Group'} onClose={onClose}>
      <label className="field">
        <span className="field-label">Group Name</span>
        <input value={name} onChange={e => setName(e.target.value)} className="input" autoFocus disabled={isEdit} />
      </label>
      <label className="field">
        <span className="field-label">GID (optional)</span>
        <input type="number" value={gid} onChange={e => setGid(e.target.value)} className="input" placeholder="auto" />
      </label>
      <label className="field">
        <span className="field-label">Members (comma-separated usernames)</span>
        <input value={members} onChange={e => setMembers(e.target.value)} className="input" placeholder="alice, bob" />
      </label>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending} className="btn btn-primary">
          {mutation.isPending ? 'Saving…' : isEdit ? 'Save' : 'Create'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// GroupsTab
// ---------------------------------------------------------------------------

function GroupsTab() {
  const qc = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)
  const [editGroup, setEditGroup] = useState<Group | null>(null)

  const groupsQ = useQuery({
    queryKey: ['rbac', 'groups'],
    queryFn: ({ signal }) => api.get<GroupsResponse>('/api/rbac/groups', signal),
  })

  const deleteGroup = useMutation({
    mutationFn: (name: string) => api.post('/api/rbac/groups', { action: 'delete', name }),
    onSuccess: () => { toast.success('Group deleted'); qc.invalidateQueries({ queryKey: ['rbac', 'groups'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  function refresh() { qc.invalidateQueries({ queryKey: ['rbac', 'groups'] }) }

  const groups = groupsQ.data?.groups ?? []

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
        <button onClick={() => setShowCreate(true)} className="btn btn-primary"><Icon name="group_add" size={15} />New Group</button>
      </div>

      {groupsQ.isLoading && <Skeleton height={200} />}
      {groupsQ.isError && <ErrorState error={groupsQ.error} onRetry={refresh} />}

      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {groups.map(g => (
          <div key={g.name} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 18px', background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-md)' }}>
            <Icon name="group" size={20} style={{ color: 'var(--primary)', flexShrink: 0 }} />
            <div style={{ flex: 1 }}>
              <div style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>{g.name}</div>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>
                {g.gid !== undefined ? `GID: ${g.gid}` : ''}
                {g.members && g.members.length > 0 ? ` · ${g.members.length} member${g.members.length !== 1 ? 's' : ''}: ${g.members.slice(0, 4).join(', ')}${g.members.length > 4 ? '…' : ''}` : ''}
              </div>
            </div>
            <div style={{ display: 'flex', gap: 6 }}>
              <button onClick={() => setEditGroup(g)} className="btn btn-ghost"><Icon name="edit" size={14} /></button>
              <button onClick={() => { if (window.confirm(`Delete group "${g.name}"?`)) deleteGroup.mutate(g.name) }} className="btn btn-danger"><Icon name="delete" size={14} /></button>
            </div>
          </div>
        ))}
        {!groupsQ.isLoading && groups.length === 0 && (
          <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>No groups found</div>
        )}
      </div>

      {showCreate && <GroupModal onClose={() => setShowCreate(false)} onDone={refresh} />}
      {editGroup  && <GroupModal group={editGroup} onClose={() => setEditGroup(null)} onDone={refresh} />}
    </>
  )
}

// ---------------------------------------------------------------------------
// RolesTab
// ---------------------------------------------------------------------------

function RolesTab() {
  const qc = useQueryClient()
  const [expanded, setExpanded] = useState<string | number | null>(null)

  const rolesQ = useQuery({
    queryKey: ['rbac', 'roles'],
    queryFn: ({ signal }) => api.get<RolesResponse>('/api/rbac/roles', signal),
  })

  const deleteRole = useMutation({
    mutationFn: (id: string | number) => api.delete(`/api/rbac/roles/${id}`),
    onSuccess: () => { toast.success('Role deleted'); qc.invalidateQueries({ queryKey: ['rbac', 'roles'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const roles = rolesQ.data?.roles ?? []

  if (rolesQ.isLoading) return <Skeleton height={200} />
  if (rolesQ.isError) return <ErrorState error={rolesQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['rbac', 'roles'] })} />

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      {roles.map(role => (
        <div key={role.id} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 18px', cursor: 'pointer' }}
            onClick={() => setExpanded(expanded === role.id ? null : role.id)}>
            <Icon name="shield" size={18} style={{ color: 'var(--primary)', flexShrink: 0 }} />
            <div style={{ flex: 1 }}>
              <div style={{ fontWeight: 700 }}>{role.name}</div>
              {role.description && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 2 }}>{role.description}</div>}
            </div>
            {role.permissions && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{role.permissions.length} permission{role.permissions.length !== 1 ? 's' : ''}</span>}
            <Icon name={expanded === role.id ? 'expand_less' : 'expand_more'} size={16} style={{ color: 'var(--text-tertiary)' }} />
          </div>
          {expanded === role.id && role.permissions && (
            <div style={{ padding: '0 18px 14px', borderTop: '1px solid var(--border)' }}>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, paddingTop: 12 }}>
                {role.permissions.map(perm => (
                  <span key={perm} style={{ padding: '3px 8px', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>{perm}</span>
                ))}
              </div>
              <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
                <button onClick={() => { if (window.confirm(`Delete role "${role.name}"?`)) deleteRole.mutate(role.id) }} className="btn btn-danger">
                  <Icon name="delete" size={13} />Delete Role
                </button>
              </div>
            </div>
          )}
        </div>
      ))}
      {roles.length === 0 && (
        <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>No custom roles defined</div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// UsersPage
// ---------------------------------------------------------------------------

type Tab = 'users' | 'groups' | 'roles'

export function UsersPage() {
  const [tab, setTab] = useState<Tab>('users')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'users',  label: 'Users',  icon: 'person' },
    { id: 'groups', label: 'Groups', icon: 'group' },
    { id: 'roles',  label: 'Roles',  icon: 'shield' },
  ]

  return (
    <div style={{ maxWidth: 1000 }}>
      <div className="page-header">
        <h1 className="page-title">Users &amp; Groups</h1>
        <p className="page-subtitle">Local user accounts, groups and RBAC roles</p>
      </div>

      <div className="tabs-underline">
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'users'  && <UsersTab />}
      {tab === 'groups' && <GroupsTab />}
      {tab === 'roles'  && <RolesTab />}
    </div>
  )
}
