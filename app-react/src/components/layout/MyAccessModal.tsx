/**
 * MyAccessModal - the signed-in account: its roles and what they allow.
 * Opened from the user chip in the top bar.
 *
 *   GET /api/rbac/me/roles        → { roles: [{id, name, display_name, description}] }
 *   GET /api/rbac/me/permissions  → { permissions: [{resource, action, display_name, category}] }
 */
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Modal } from '@/components/ui/Modal'
import { Skeleton } from '@/components/ui/LoadingSpinner'

interface Role { id: number; name: string; display_name?: string; description?: string }
interface Permission { resource: string; action: string; display_name: string; category: string }

export function PermissionList({ permissions }: { permissions: Permission[] }) {
  const byCategory = new Map<string, Permission[]>()
  for (const p of permissions) byCategory.set(p.category || 'other', [...(byCategory.get(p.category || 'other') ?? []), p])
  if (permissions.length === 0) return <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No permissions.</div>
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(200px, 1fr))', gap: 12, maxHeight: 320, overflowY: 'auto' }}>
      {[...byCategory.entries()].sort((a, b) => a[0].localeCompare(b[0])).map(([cat, list]) => (
        <div key={cat}>
          <div style={{ fontSize: 'var(--text-xs)', fontWeight: 700, textTransform: 'uppercase', color: 'var(--text-tertiary)', marginBottom: 4 }}>{cat}</div>
          {list.map(p => (
            <div key={`${p.resource}:${p.action}`} style={{ fontSize: 'var(--text-sm)', padding: '1px 0' }} title={`${p.resource}:${p.action}`}>
              {p.display_name || `${p.resource}:${p.action}`}
            </div>
          ))}
        </div>
      ))}
    </div>
  )
}

export function MyAccessModal({ username, onClose }: { username: string; onClose: () => void }) {
  const rolesQ = useQuery({
    queryKey: ['rbac', 'me', 'roles'],
    queryFn: ({ signal }) => api.get<{ roles: Role[] | null }>('/api/rbac/me/roles', signal),
  })
  const permsQ = useQuery({
    queryKey: ['rbac', 'me', 'permissions'],
    queryFn: ({ signal }) => api.get<{ permissions: Permission[] | null }>('/api/rbac/me/permissions', signal),
  })
  const roles = rolesQ.data?.roles ?? []
  return (
    <Modal title={`Signed in as ${username}`} onClose={onClose} size="lg">
      {(rolesQ.isLoading || permsQ.isLoading) && <Skeleton height={160} />}
      {(rolesQ.isError || permsQ.isError) && (
        <div style={{ color: 'var(--error)', fontSize: 'var(--text-sm)' }}>{((rolesQ.error ?? permsQ.error) as Error).message}</div>
      )}
      {rolesQ.data && (
        <div>
          <div style={{ fontWeight: 600, marginBottom: 6 }}>Roles</div>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            {roles.map(r => <span key={r.id} className="badge badge-primary" title={r.description}>{r.display_name || r.name}</span>)}
            {roles.length === 0 && <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No roles assigned.</span>}
          </div>
        </div>
      )}
      {permsQ.data && (
        <div>
          <div style={{ fontWeight: 600, marginBottom: 6 }}>What this account may do</div>
          <PermissionList permissions={permsQ.data.permissions ?? []} />
        </div>
      )}
      <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-primary">Close</button>
      </div>
    </Modal>
  )
}
