/**
 * pages/UsersPage.tsx - Users, Groups & Roles (Phase 5)
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
import { fmtDateTime } from '@/lib/fmt'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'
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
  is_system?:  boolean
  user_count?: number
}

interface PermissionDef { id: number; resource: string; action: string; display_name: string; category: string }

// The role chosen for an account is also its built-in RBAC role.
const ACCOUNT_ROLES = ['admin', 'operator', 'user', 'viewer'] as const

interface UsersResponse  { success: boolean; users:  User[]  }
interface GroupsResponse { success: boolean; groups: Group[] }
interface RolesResponse  { success: boolean; roles:  Role[]  }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fmtDate(s?: string) {
  if (!s) return 'Never'
  return fmtDateTime(s)
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
  const [role, setRole] = useState(user?.role === 'readonly' ? 'viewer' : (user?.role ?? 'user'))
  const [password, setPassword] = useState('')
  const [confirmPw, setConfirmPw] = useState('')

  const isEdit = !!user

  const mutation = useMutation({
    mutationFn: () => {
      if (!isEdit) {
        if (!/^[a-z_][a-z0-9_-]*$/.test(username.trim())) {
          throw new Error('Username must start with a letter or underscore, followed by lowercase letters, numbers, underscores, or hyphens')
        }
        if (username.trim().length < 2 || username.trim().length > 32) {
          throw new Error('Username must be 2-32 characters')
        }
      }
      if (!confirmPw) throw new Error('Enter your current password to authorize this change')
      const body: Record<string, unknown> = isEdit
        ? { action: 'update', id: user!.id, email, role, confirm_password: confirmPw, ...(password ? { password } : {}) }
        : { action: 'create', username, email, password, role, confirm_password: confirmPw }
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
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            Lowercase letters, numbers, underscores, hyphens. Must start with a letter.
          </span>
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
          {ACCOUNT_ROLES.map(r => <option key={r} value={r}>{r}</option>)}
        </select>
      </label>
      <label className="field">
        <span className="field-label">
          {isEdit ? 'New Password (leave blank to keep current)' : 'Password'}
        </span>
        <input type="password" value={password} onChange={e => setPassword(e.target.value)} className="input" autoComplete="new-password" />
      </label>
      <ConfirmPasswordField value={confirmPw} onChange={setConfirmPw} />
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending || !confirmPw} className="btn btn-primary">
          {mutation.isPending ? 'Saving…' : isEdit ? 'Save' : 'Create'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// Current-password confirmation. The API requires the signed-in admin's own
// password for user and group changes (confirm_password).
// ---------------------------------------------------------------------------

function ConfirmPasswordField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <label className="field">
      <span className="field-label">Your current password</span>
      <input type="password" value={value} onChange={e => onChange(e.target.value)} className="input" autoComplete="current-password" />
      <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
        Required to authorize changes to accounts and groups.
      </span>
    </label>
  )
}

function PasswordPromptModal({ title, message, confirmLabel, pending, onConfirm, onClose }: {
  title: string
  message: string
  confirmLabel: string
  pending: boolean
  onConfirm: (password: string) => void
  onClose: () => void
}) {
  const [pw, setPw] = useState('')
  return (
    <Modal title={title} onClose={onClose} size="sm">
      <p style={{ margin: 0, color: 'var(--text-secondary)' }}>{message}</p>
      <ConfirmPasswordField value={pw} onChange={setPw} />
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => onConfirm(pw)} disabled={!pw || pending} className="btn btn-danger">
          {pending ? 'Working…' : confirmLabel}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ResetPasswordModal - admin sets a temporary password for another user
// ---------------------------------------------------------------------------

function ResetPasswordModal({ user, onClose }: { user: User; onClose: () => void }) {
  const [tempPassword, setTempPassword] = useState('')
  const [showPass, setShowPass] = useState(false)

  const mutation = useMutation({
    mutationFn: () => api.post(`/api/users/${user.id}/reset-password`, { temp_password: tempPassword }),
    onSuccess: () => {
      toast.success(`Temporary password set for ${user.username} - they must change it on next login`)
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={`Reset password: ${user.username}`} onClose={onClose}>
      <div style={{ marginBottom: 16, padding: '10px 14px', background: 'var(--warning-bg, rgba(251,191,36,0.08))', border: '1px solid var(--warning-border, rgba(251,191,36,0.2))', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        <Icon name="info" size={14} style={{ verticalAlign: 'middle', marginRight: 6, color: 'var(--warning, #f59e0b)' }} />
        Set a temporary password. The user will be required to change it on next login. All existing sessions for this user will be revoked.
      </div>
      <label className="field">
        <span className="field-label">Temporary password</span>
        <div style={{ position: 'relative' }}>
          <input
            type={showPass ? 'text' : 'password'}
            value={tempPassword}
            onChange={e => setTempPassword(e.target.value)}
            className="input"
            autoFocus
            autoComplete="new-password"
            style={{ paddingRight: 40 }}
            onKeyDown={e => e.key === 'Enter' && tempPassword.length >= 8 && mutation.mutate()}
          />
          <button type="button" onClick={() => setShowPass(v => !v)}
            style={{ position: 'absolute', right: 10, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', display: 'flex', alignItems: 'center', padding: 4 }}>
            <Icon name={showPass ? 'visibility_off' : 'visibility'} size={16} />
          </button>
        </div>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 4, display: 'block' }}>
          Min 8 characters with uppercase, lowercase, number, and special character.
        </span>
      </label>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending || tempPassword.length < 8} className="btn btn-primary">
          <Icon name="lock_reset" size={14} />
          {mutation.isPending ? 'Setting…' : 'Set Temporary Password'}
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
  const [resetUser, setResetUser] = useState<User | null>(null)
  const [deletingUser, setDeletingUser] = useState<User | null>(null)
  const [rolesUser, setRolesUser] = useState<User | null>(null)

  const usersQ = useQuery({
    queryKey: ['rbac', 'users'],
    queryFn: ({ signal }) => api.get<UsersResponse>('/api/rbac/users', signal),
  })

  const deleteUser = useMutation({
    mutationFn: ({ id, password }: { id: number; password: string }) => api.post('/api/rbac/users', { action: 'delete', id, confirm_password: password }),
    onSuccess: () => { toast.success('User deleted'); setDeletingUser(null); qc.invalidateQueries({ queryKey: ['rbac', 'users'] }) },
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
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
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
                  <td style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{u.email ?? '-'}</td>
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
                      <button onClick={() => setEditUser(u)} className="btn btn-ghost" title="Edit user"><Icon name="edit" size={14} /></button>
                      <button onClick={() => setRolesUser(u)} className="btn btn-ghost" title="Roles"><Icon name="shield" size={14} /></button>
                      <button onClick={() => setResetUser(u)} className="btn btn-ghost" title="Reset password" style={{ color: 'var(--warning, #f59e0b)' }}><Icon name="lock_reset" size={14} /></button>
                      <button onClick={() => setDeletingUser(u)} className="btn btn-danger" title="Delete user"><Icon name="delete" size={14} /></button>
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
      {resetUser  && <ResetPasswordModal user={resetUser} onClose={() => setResetUser(null)} />}
      {rolesUser  && <UserRolesModal user={rolesUser} onClose={() => setRolesUser(null)} />}
      {deletingUser && (
        <PasswordPromptModal
          title={`Delete "${deletingUser.username}"?`}
          message="This user will be permanently removed, and their sessions ended."
          confirmLabel="Delete"
          pending={deleteUser.isPending}
          onConfirm={password => deleteUser.mutate({ id: deletingUser.id, password })}
          onClose={() => setDeletingUser(null)}
        />
      )}
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
  const [confirmPw, setConfirmPw] = useState('')
  const isEdit = !!group

  const mutation = useMutation({
    mutationFn: () => {
      if (!confirmPw) throw new Error('Enter your current password to authorize this change')
      const body = { action: isEdit ? 'update' : 'create', name, gid: gid ? Number(gid) : undefined, members: members.split(',').map(m => m.trim()).filter(Boolean), confirm_password: confirmPw }
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
      <ConfirmPasswordField value={confirmPw} onChange={setConfirmPw} />
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending || !confirmPw} className="btn btn-primary">
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
  const [deletingGroup, setDeletingGroup] = useState<Group | null>(null)

  const groupsQ = useQuery({
    queryKey: ['rbac', 'groups'],
    queryFn: ({ signal }) => api.get<GroupsResponse>('/api/rbac/groups', signal),
  })

  const deleteGroup = useMutation({
    mutationFn: ({ name, password }: { name: string; password: string }) => api.post('/api/rbac/groups', { action: 'delete', name, confirm_password: password }),
    onSuccess: () => { toast.success('Group deleted'); setDeletingGroup(null); qc.invalidateQueries({ queryKey: ['rbac', 'groups'] }) },
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
          <div key={g.name} className="card" style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 18px', borderRadius: 'var(--radius-md)' }}>
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
              <button onClick={() => setDeletingGroup(g)} className="btn btn-danger" title="Delete group"><Icon name="delete" size={14} /></button>
            </div>
          </div>
        ))}
        {!groupsQ.isLoading && groups.length === 0 && (
          <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>No groups found</div>
        )}
      </div>

      {showCreate && <GroupModal onClose={() => setShowCreate(false)} onDone={refresh} />}
      {editGroup  && <GroupModal group={editGroup} onClose={() => setEditGroup(null)} onDone={refresh} />}
      {deletingGroup && (
        <PasswordPromptModal
          title={`Delete group "${deletingGroup.name}"?`}
          message="Members will not be deleted, only the group."
          confirmLabel="Delete"
          pending={deleteGroup.isPending}
          onConfirm={password => deleteGroup.mutate({ name: deletingGroup.name, password })}
          onClose={() => setDeletingGroup(null)}
        />
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// RoleModal (create + edit)
// ---------------------------------------------------------------------------

function RoleModal({ role, onClose, onDone }: { role?: Role; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState(role?.name ?? '')
  const [description, setDescription] = useState(role?.description ?? '')
  const [perms, setPerms] = useState<Set<string>>(new Set(role?.permissions ?? []))
  const isEdit = !!role
  const isAdmin = role?.name === 'admin'

  const permsQ = useQuery({
    queryKey: ['rbac', 'permissions'],
    queryFn: ({ signal }) => api.get<{ permissions: PermissionDef[] }>('/api/rbac/permissions', signal),
  })
  const byCategory = new Map<string, PermissionDef[]>()
  for (const p of permsQ.data?.permissions ?? []) {
    byCategory.set(p.category, [...(byCategory.get(p.category) ?? []), p])
  }

  function toggle(key: string) {
    setPerms(prev => { const n = new Set(prev); if (n.has(key)) n.delete(key); else n.add(key); return n })
  }

  const mutation = useMutation({
    mutationFn: () => {
      const body = { name, description, permissions: [...perms] }
      return isEdit
        ? api.put(`/api/rbac/roles/${role.id}`, body)
        : api.post('/api/rbac/roles', body)
    },
    onSuccess: () => { toast.success(isEdit ? 'Role updated' : 'Role created'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={isEdit ? `Edit Role: ${role!.name}` : 'Create Role'} onClose={onClose} size="lg">
      <label className="field">
        <span className="field-label">Role Name</span>
        <input value={name} onChange={e => setName(e.target.value)} className="input" autoFocus placeholder="e.g. storage-admin" disabled={isEdit} />
      </label>
      <label className="field">
        <span className="field-label">Description</span>
        <input value={description} onChange={e => setDescription(e.target.value)} className="input" placeholder="Manage storage and datasets" disabled={role?.is_system} />
      </label>
      <div className="field">
        <span className="field-label">Permissions</span>
        {isAdmin && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>The admin role always has every permission.</div>}
        {permsQ.isLoading && <Skeleton height={120} />}
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: 12, maxHeight: 360, overflowY: 'auto' }}>
          {[...byCategory.entries()].map(([cat, list]) => (
            <div key={cat}>
              <div style={{ fontSize: 'var(--text-xs)', fontWeight: 700, textTransform: 'uppercase', color: 'var(--text-tertiary)', marginBottom: 4 }}>{cat}</div>
              {list.map(p => {
                const key = `${p.resource}:${p.action}`
                return (
                  <label key={key} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-sm)', padding: '2px 0' }} title={key}>
                    <input type="checkbox" checked={isAdmin || perms.has(key)} disabled={isAdmin} onChange={() => toggle(key)} />
                    {p.display_name}
                  </label>
                )
              })}
            </div>
          ))}
        </div>
      </div>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 10 }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => mutation.mutate()} disabled={mutation.isPending || isAdmin || !name} className="btn btn-primary">
          {mutation.isPending ? 'Saving…' : isEdit ? 'Save' : 'Create'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// UserRolesModal - extra roles of an account (besides its account role)
// ---------------------------------------------------------------------------

function UserRolesModal({ user, onClose }: { user: User; onClose: () => void }) {
  const qc = useQueryClient()
  const rolesQ = useQuery({
    queryKey: ['rbac', 'roles'],
    queryFn: ({ signal }) => api.get<RolesResponse>('/api/rbac/roles', signal),
  })
  const mineQ = useQuery({
    queryKey: ['rbac', 'user-roles', user.id],
    queryFn: ({ signal }) => api.get<{ roles: Role[] | null }>(`/api/rbac/users/${user.id}/roles`, signal),
  })
  const held = new Set((mineQ.data?.roles ?? []).map(r => String(r.id)))
  const primary = user.role === 'readonly' ? 'viewer' : user.role

  const toggle = useMutation({
    mutationFn: (r: Role) => held.has(String(r.id))
      ? api.delete(`/api/rbac/users/${user.id}/roles/${r.id}`)
      : api.post(`/api/rbac/users/${user.id}/roles`, { role_id: Number(r.id) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['rbac', 'user-roles', user.id] }),
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title={`Roles: ${user.username}`} onClose={onClose}>
      <p style={{ margin: 0, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        The account role ({primary}) is set in the user's settings. Additional roles add their permissions.
      </p>
      {(rolesQ.isLoading || mineQ.isLoading) && <Skeleton height={120} />}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
        {(rolesQ.data?.roles ?? []).map(r => (
          <label key={r.id} style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 'var(--text-sm)' }}>
            <input type="checkbox" checked={held.has(String(r.id))} disabled={r.name === primary || toggle.isPending} onChange={() => toggle.mutate(r)} />
            <strong>{r.name}</strong>
            {r.description && <span style={{ color: 'var(--text-tertiary)' }}>{r.description}</span>}
          </label>
        ))}
      </div>
      <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-primary">Done</button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// RolesTab
// ---------------------------------------------------------------------------

function RolesTab() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog: ConfirmRoles } = useConfirm()
  const [expanded, setExpanded] = useState<string | number | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [editRole, setEditRole] = useState<Role | null>(null)

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

  function refresh() { qc.invalidateQueries({ queryKey: ['rbac', 'roles'] }) }

  if (rolesQ.isLoading) return <Skeleton height={200} />
  if (rolesQ.isError) return <ErrorState error={rolesQ.error} onRetry={refresh} />

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 8 }}>
        <button onClick={() => setShowCreate(true)} className="btn btn-primary btn-sm"><Icon name="add" size={14} />New Role</button>
      </div>

      {roles.map(role => (
        <div key={role.id} className="card" style={{ borderRadius: 'var(--radius-md)', overflow: 'hidden' }}>
          <button
            type="button"
            style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 18px', cursor: 'pointer', background: 'none', border: 'none', width: '100%', textAlign: 'left', fontFamily: 'inherit', color: 'inherit' }}
            aria-expanded={expanded === role.id}
            aria-controls={`role-panel-${String(role.id)}`}
            onClick={() => setExpanded(expanded === role.id ? null : role.id)}
          >
            <Icon name="shield" size={18} style={{ color: 'var(--primary)', flexShrink: 0 }} />
            <div style={{ flex: 1 }}>
              <div style={{ fontWeight: 700 }}>{role.name}{role.is_system && <span className="badge" style={{ marginLeft: 8 }}>built-in</span>}{role.user_count ? <span style={{ marginLeft: 8, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{role.user_count} user{role.user_count !== 1 ? 's' : ''}</span> : null}</div>
              {role.description && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 2 }}>{role.description}</div>}
            </div>
            {role.permissions && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{role.permissions.length} permission{role.permissions.length !== 1 ? 's' : ''}</span>}
            <Icon name={expanded === role.id ? 'expand_less' : 'expand_more'} size={16} style={{ color: 'var(--text-tertiary)' }} />
          </button>
          {expanded === role.id && role.permissions && (
            <div id={`role-panel-${String(role.id)}`} style={{ padding: '0 18px 14px', borderTop: '1px solid var(--border)' }}>
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, paddingTop: 12 }}>
                {role.permissions.map(perm => (
                  <span key={perm} style={{ padding: '3px 8px', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>{perm}</span>
                ))}
              </div>
              <div style={{ display: 'flex', gap: 8, marginTop: 12 }}>
                <button onClick={() => setEditRole(role)} className="btn btn-ghost">
                  <Icon name="edit" size={13} />Edit Role
                </button>
                {!role.is_system && <button onClick={async () => { if (await confirm({ title: `Delete role "${role.name}"?`, message: 'Users assigned this role will lose its permissions.', danger: true, confirmLabel: 'Delete' })) deleteRole.mutate(role.id) }} className="btn btn-danger">
                  <Icon name="delete" size={13} />Delete Role
                </button>}
              </div>
            </div>
          )}
        </div>
      ))}
      {roles.length === 0 && (
        <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>No roles defined</div>
      )}

      {showCreate && <RoleModal onClose={() => setShowCreate(false)} onDone={refresh} />}
      {editRole && <RoleModal role={editRole} onClose={() => setEditRole(null)} onDone={refresh} />}
      <ConfirmRoles />
    </div>
  )
}

// ---------------------------------------------------------------------------
// SessionsTab
// ---------------------------------------------------------------------------

interface Session {
  id:            string
  ip_address:    string
  user_agent:    string
  created_at:    number
  last_activity: number
  is_current:    boolean
}

interface SessionsResponse {
  success:  boolean
  sessions: Session[]
}

function SessionsTab() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog: ConfirmSessions } = useConfirm()

  const sessionsQ = useQuery({
    queryKey: ['auth', 'sessions'],
    queryFn: ({ signal }) => api.get<SessionsResponse>('/api/auth/sessions', signal),
  })

  const revoke = useMutation({
    mutationFn: (id: string) => api.delete('/api/auth/sessions', { id }),
    onSuccess: () => { toast.success('Session revoked'); qc.invalidateQueries({ queryKey: ['auth', 'sessions'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  function refresh() { qc.invalidateQueries({ queryKey: ['auth', 'sessions'] }) }

  const sessions = sessionsQ.data?.sessions ?? []

  return (
    <>
      {sessionsQ.isLoading && <Skeleton height={200} />}
      {sessionsQ.isError && <ErrorState error={sessionsQ.error} onRetry={refresh} />}

      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {sessions.map(s => (
          <div key={s.id} className="card" style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 18px', borderRadius: 'var(--radius-md)', border: s.is_current ? '1px solid var(--primary-border)' : '1px solid var(--border)' }}>
            <div style={{ width: 36, height: 36, borderRadius: 'var(--radius-sm)', background: s.is_current ? 'var(--primary-bg)' : 'var(--surface)', display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
              <Icon name={s.user_agent.toLowerCase().includes('mobile') ? 'smartphone' : 'desktop_windows'} 
                    size={20} style={{ color: s.is_current ? 'var(--primary)' : 'var(--text-tertiary)' }} />
            </div>
            
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>{s.ip_address}</span>
                {s.is_current && <span className="badge badge-primary">THIS SESSION</span>}
              </div>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                {s.user_agent}
              </div>
              <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>
                Last activity: {new Date(s.last_activity * 1000).toLocaleString()}
              </div>
            </div>

            {!s.is_current && (
              <button onClick={async () => { if (await confirm({ title: 'Logout this session?', message: 'The device will be forced to log in again.', danger: true, confirmLabel: 'Revoke' })) revoke.mutate(s.id) }} 
                      className="btn btn-danger btn-sm" disabled={revoke.isPending}>
                <Icon name="logout" size={14} />Revoke
              </button>
            )}
          </div>
        ))}
        {!sessionsQ.isLoading && sessions.length === 0 && (
          <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>No active sessions found</div>
        )}
      </div>
      <ConfirmSessions />
    </>
  )
}

// ---------------------------------------------------------------------------
// UsersPage
// ---------------------------------------------------------------------------

type Tab = 'users' | 'groups' | 'roles' | 'sessions'

export function UsersPage() {
  const [tab, setTab] = useState<Tab>('users')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'users',    label: 'Users',    icon: 'person' },
    { id: 'groups',   label: 'Groups',   icon: 'group' },
    { id: 'roles',    label: 'Roles',    icon: 'shield' },
    { id: 'sessions', label: 'Sessions', icon: 'devices' },
  ]

  return (
    <div style={{ maxWidth: 1000 }}>
      <div className="page-header">
        <h1 className="page-title">Users &amp; Groups</h1>
        <p className="page-subtitle">Local user accounts, groups and RBAC roles</p>
      </div>

      <div
        className="tabs-underline"
        role="tablist"
        aria-label="User management sections"
        onKeyDown={(e) => {
          // Manual Activation: arrows move focus only; each panel makes a
          // live API call on mount, so activating on every arrow keypress
          // would fire unnecessary daemon requests. Enter/Space activates.
          const activeIdx = TABS.findIndex(t => t.id === tab)
          const focusedEl = document.activeElement
          const focusedIdx = TABS.findIndex(t => focusedEl?.id === `users-tab-${t.id}`)
          const fromIdx = focusedIdx >= 0 ? focusedIdx : activeIdx

          if (e.key === 'ArrowRight') {
            e.preventDefault()
            document.getElementById(`users-tab-${TABS[(fromIdx + 1) % TABS.length].id}`)?.focus()
          } else if (e.key === 'ArrowLeft') {
            e.preventDefault()
            document.getElementById(`users-tab-${TABS[(fromIdx - 1 + TABS.length) % TABS.length].id}`)?.focus()
          } else if ((e.key === 'Enter' || e.key === ' ') && focusedIdx >= 0) {
            e.preventDefault()
            setTab(TABS[focusedIdx].id)
          }
        }}
      >
        {TABS.map(t => (
          <button
            key={t.id}
            role="tab"
            aria-selected={tab === t.id}
            aria-controls={`users-panel-${t.id}`}
            id={`users-tab-${t.id}`}
            tabIndex={tab === t.id ? 0 : -1}
            onClick={() => setTab(t.id)}
            className={`tab-underline${tab === t.id ? ' active' : ''}`}
          >
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'users' && (
        <div role="tabpanel" id="users-panel-users" aria-labelledby="users-tab-users">
          <UsersTab />
        </div>
      )}
      {tab === 'groups' && (
        <div role="tabpanel" id="users-panel-groups" aria-labelledby="users-tab-groups">
          <GroupsTab />
        </div>
      )}
      {tab === 'roles' && (
        <div role="tabpanel" id="users-panel-roles" aria-labelledby="users-tab-roles">
          <RolesTab />
        </div>
      )}
      {tab === 'sessions' && (
        <div role="tabpanel" id="users-panel-sessions" aria-labelledby="users-tab-sessions">
          <SessionsTab />
        </div>
      )}
    </div>
  )
}

