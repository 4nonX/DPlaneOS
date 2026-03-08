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
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

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
// Styles
// ---------------------------------------------------------------------------

const btnGhost: React.CSSProperties = {
  padding: '7px 13px', background: 'var(--surface)', color: 'var(--text-secondary)',
  border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 500, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnPrimary: React.CSSProperties = {
  padding: '8px 16px', background: 'var(--primary)', color: '#000',
  border: 'none', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 700, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnDanger: React.CSSProperties = {
  padding: '6px 12px', background: 'var(--error-bg)', border: '1px solid var(--error-border)',
  borderRadius: 'var(--radius-sm)', cursor: 'pointer', color: 'var(--error)',
  fontSize: 'var(--text-xs)', fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 4,
}
const inputStyle: React.CSSProperties = {
  background: 'var(--surface)', border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)', padding: '8px 12px',
  color: 'var(--text)', fontSize: 'var(--text-sm)', width: '100%',
  fontFamily: 'var(--font-ui)', outline: 'none', boxSizing: 'border-box',
}

function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  return (
    <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 200 }}
      onClick={e => e.target === e.currentTarget && onClose()}>
      <div style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 28, width: 480, maxWidth: '90vw', display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>{title}</div>
        {children}
      </div>
    </div>
  )
}

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
        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Username</span>
          <input value={username} onChange={e => setUsername(e.target.value)} style={inputStyle} autoFocus />
        </label>
      )}
      <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Email</span>
        <input type="email" value={email} onChange={e => setEmail(e.target.value)} style={inputStyle} />
      </label>
      <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Role</span>
        <select value={role} onChange={e => setRole(e.target.value)}
          style={{ ...inputStyle, appearance: 'none' }}>
          {['admin', 'user', 'readonly'].map(r => <option key={r} value={r}>{r}</option>)}
        </select>
      </label>
      <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
          {isEdit ? 'New Password (leave blank to keep current)' : 'Password'}
        </span>
        <input type="password" value={password} onChange={e => setPassword(e.target.value)} style={inputStyle} autoComplete="new-password" />
      </label>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} style={btnGhost}>Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending} style={btnPrimary}>
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
        <button onClick={() => setShowCreate(true)} style={btnPrimary}><Icon name="person_add" size={15} />New User</button>
      </div>

      {usersQ.isLoading && <Skeleton height={200} />}
      {usersQ.isError && <ErrorState error={usersQ.error} onRetry={refresh} />}

      {!usersQ.isLoading && !usersQ.isError && (
        <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead>
              <tr style={{ background: 'rgba(255,255,255,0.03)' }}>
                {['User', 'Email', 'Role', 'Last Login', 'Status', 'Actions'].map(h => (
                  <th key={h} style={{ padding: '10px 16px', textAlign: 'left', fontSize: 'var(--text-2xs)', fontWeight: 700, color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', borderBottom: '1px solid var(--border)' }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {users.map(u => (
                <tr key={u.id} style={{ borderBottom: '1px solid var(--border)', transition: 'background 0.1s' }}
                  onMouseEnter={e => (e.currentTarget.style.background = 'rgba(255,255,255,0.02)')}
                  onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
                >
                  <td style={{ padding: '12px 16px' }}>
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
                  <td style={{ padding: '12px 16px', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{u.email ?? '—'}</td>
                  <td style={{ padding: '12px 16px' }}><RoleBadge role={u.role} /></td>
                  <td style={{ padding: '12px 16px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', whiteSpace: 'nowrap' }}>{fmtDate(u.last_login_at)}</td>
                  <td style={{ padding: '12px 16px' }}>
                    {isLocked(u)
                      ? <span style={{ padding: '2px 8px', borderRadius: 'var(--radius-sm)', background: 'var(--error-bg)', border: '1px solid var(--error-border)', color: 'var(--error)', fontSize: 'var(--text-xs)', fontWeight: 700 }}>Locked</span>
                      : <span style={{ padding: '2px 8px', borderRadius: 'var(--radius-sm)', background: 'var(--success-bg)', border: '1px solid var(--success-border)', color: 'var(--success)', fontSize: 'var(--text-xs)', fontWeight: 700 }}>Active</span>
                    }
                  </td>
                  <td style={{ padding: '12px 16px' }}>
                    <div style={{ display: 'flex', gap: 6 }}>
                      <button onClick={() => setEditUser(u)} style={btnGhost}><Icon name="edit" size={14} /></button>
                      <button onClick={() => { if (window.confirm(`Delete "${u.username}"?`)) deleteUser.mutate(u.id) }} style={btnDanger}><Icon name="delete" size={14} /></button>
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
      <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Group Name</span>
        <input value={name} onChange={e => setName(e.target.value)} style={inputStyle} autoFocus disabled={isEdit} />
      </label>
      <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>GID (optional)</span>
        <input type="number" value={gid} onChange={e => setGid(e.target.value)} style={inputStyle} placeholder="auto" />
      </label>
      <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Members (comma-separated usernames)</span>
        <input value={members} onChange={e => setMembers(e.target.value)} style={inputStyle} placeholder="alice, bob" />
      </label>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} style={btnGhost}>Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending} style={btnPrimary}>
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
        <button onClick={() => setShowCreate(true)} style={btnPrimary}><Icon name="group_add" size={15} />New Group</button>
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
              <button onClick={() => setEditGroup(g)} style={btnGhost}><Icon name="edit" size={14} /></button>
              <button onClick={() => { if (window.confirm(`Delete group "${g.name}"?`)) deleteGroup.mutate(g.name) }} style={btnDanger}><Icon name="delete" size={14} /></button>
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
                <button onClick={() => { if (window.confirm(`Delete role "${role.name}"?`)) deleteRole.mutate(role.id) }} style={btnDanger}>
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
      <div style={{ marginBottom: 28 }}>
        <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>Users & Groups</h1>
        <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>Local user accounts, groups and RBAC roles</p>
      </div>

      <div style={{ display: 'flex', gap: 4, marginBottom: 24, borderBottom: '1px solid var(--border)' }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} style={{ padding: '10px 20px', background: 'none', border: 'none', cursor: 'pointer', fontSize: 'var(--text-sm)', fontWeight: 600, color: tab === t.id ? 'var(--primary)' : 'var(--text-secondary)', borderBottom: tab === t.id ? '2px solid var(--primary)' : '2px solid transparent', marginBottom: -1, display: 'flex', alignItems: 'center', gap: 6 }}>
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
