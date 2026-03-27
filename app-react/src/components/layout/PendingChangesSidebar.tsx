/**
 * components/layout/PendingChangesSidebar.tsx
 *
 * Right-side drawer showing the structured diff between current intent
 * and applied system state.
 */

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Icon } from '@/components/ui/Icon'
import { api } from '@/lib/api'
import { useNotificationsStore } from '@/stores/notifications'
import { LoadingState } from '@/components/ui/LoadingSpinner'

interface Change {
  path: string
  from: any
  to: any
  op: 'add' | 'remove' | 'modify'
}

export function PendingChangesSidebar() {
  const { isSidebarOpen, setSidebarOpen } = useNotificationsStore()
  const queryClient = useQueryClient()

  const diffQ = useQuery({
    queryKey: ['nixos', 'diff-intent'],
    queryFn: () => api.get<{ success: boolean; changes: Change[] }>('/api/nixos/diff-intent'),
    enabled: isSidebarOpen,
    refetchInterval: isSidebarOpen ? 5000 : false
  })

  const reconcileM = useMutation({
    mutationFn: () => api.post('/api/nixos/reconcile', {}),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['nixos', 'status'] })
      queryClient.invalidateQueries({ queryKey: ['nixos', 'diff-intent'] })
      setSidebarOpen(false)
    }
  })

  if (!isSidebarOpen) return null

  const changes = diffQ.data?.changes ?? []

  return (
    <>
      {/* Backdrop */}
      <div 
        onClick={() => setSidebarOpen(false)}
        style={{ 
          position: 'fixed', inset: 0, zIndex: 1001, 
          background: 'rgba(0,0,0,0.4)', backdropFilter: 'blur(4px)',
          animation: 'fadeIn 0.2s ease-out'
        }} 
      />

      {/* Sidebar */}
      <aside style={{
        position: 'fixed', top: 0, right: 0, bottom: 0,
        width: 450, background: 'var(--surface)',
        borderLeft: '1px solid var(--border)',
        zIndex: 1002, display: 'flex', flexDirection: 'column',
        boxShadow: '-10px 0 40px rgba(0,0,0,0.5)',
        animation: 'slideInRight 0.3s cubic-bezier(0.16, 1, 0.3, 1)'
      }}>
        <div style={{ padding: '24px 32px', borderBottom: '1px solid var(--border-subtle)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <div>
            <h2 style={{ fontSize: 'var(--text-lg)', fontWeight: 700, margin: 0 }}>Pending Changes</h2>
            <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: '4px 0 0 0' }}>Declarative Intent vs. Applied State</p>
          </div>
          <button 
            onClick={() => setSidebarOpen(false)}
            style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)' }}
          >
            <Icon name="close" size={24} />
          </button>
        </div>

        <div style={{ flex: 1, overflowY: 'auto', padding: '24px 32px' }}>
          {diffQ.isLoading ? (
            <LoadingState />
          ) : changes.length === 0 ? (
            <div style={{ textAlign: 'center', padding: '60px 20px' }}>
              <Icon name="check_circle" size={48} style={{ color: 'var(--success)', opacity: 0.5, marginBottom: 16 }} />
              <p style={{ color: 'var(--text-secondary)' }}>No pending changes found. Your system is in sync.</p>
            </div>
          ) : (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
              {changes.map((c, i) => (
                <div key={i} style={{ 
                  padding: 16, background: 'rgba(255,255,255,0.02)', 
                  border: '1px solid var(--border-subtle)', borderRadius: 'var(--radius-md)' 
                }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8 }}>
                    <span style={{ fontSize: 'var(--text-xs)', fontWeight: 700, color: 'var(--primary)', textTransform: 'uppercase' }}>{c.path}</span>
                    <span className={`badge badge-${c.op === 'add' ? 'success' : c.op === 'remove' ? 'error' : 'warning'}`} style={{ fontSize: 9 }}>
                      {c.op.toUpperCase()}
                    </span>
                  </div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 'var(--text-sm)' }}>
                    {c.op === 'modify' && (
                      <>
                        <span style={{ color: 'var(--text-tertiary)', textDecoration: 'line-through' }}>{JSON.stringify(c.from)}</span>
                        <Icon name="arrow_forward" size={14} style={{ color: 'var(--text-tertiary)' }} />
                      </>
                    )}
                    <span style={{ fontWeight: 600, color: c.op === 'add' ? 'var(--success)' : c.op === 'remove' ? 'var(--error)' : 'var(--warning)' }}>
                      {JSON.stringify(c.to ?? c.from)}
                    </span>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        <div style={{ padding: '32px', borderTop: '1px solid var(--border-subtle)', background: 'rgba(0,0,0,0.1)' }}>
          <div style={{ marginBottom: 20, padding: 16, background: 'rgba(var(--warning-rgb), 0.1)', border: '1px solid var(--warning-border)', borderRadius: 'var(--radius-md)', display: 'flex', gap: 12 }}>
            <Icon name="info" size={20} style={{ color: 'var(--warning)', flexShrink: 0 }} />
            <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', margin: 0, lineHeight: 1.5 }}>
              Applying these changes will trigger a <strong>nixos-rebuild switch</strong>. 
              Active sessions may be briefly interrupted if network services are restarted.
            </p>
          </div>
          <button 
            onClick={() => reconcileM.mutate()}
            disabled={reconcileM.isPending || changes.length === 0}
            className="btn btn-primary" 
            style={{ width: '100%', height: 48, fontWeight: 700 }}
          >
            {reconcileM.isPending ? 'Reconciling System...' : 'Apply & Reconcile Now'}
          </button>
        </div>
      </aside>
    </>
  )
}
