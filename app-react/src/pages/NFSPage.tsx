/**
 * pages/NFSPage.tsx — NFS Export Management (Phase 2)
 *
 * Calls (matching nfs_handler.go routes — registered in daemon fix):
 *   GET    /api/nfs/status
 *   GET    /api/nfs/exports
 *   POST   /api/nfs/exports           (create)
 *   POST   /api/nfs/exports/{id}/update
 *   DELETE /api/nfs/exports/{id}
 *   POST   /api/nfs/reload
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

interface NFSExport {
  id:         number
  path:       string
  clients:    string
  options:    string
  enabled:    boolean
  created_at: string
}
interface NFSExportsResponse { success: boolean; exports: NFSExport[] }

interface NFSStatus {
  running: boolean; version?: string; exports_count?: number; active_connections?: number
}
interface NFSStatusResponse { success: boolean; status: NFSStatus }

// ---------------------------------------------------------------------------
// ExportModal (create / edit)
// ---------------------------------------------------------------------------

const DEFAULT_OPTIONS = 'rw,sync,no_subtree_check,no_root_squash'

function ExportModal({ existing, onClose, onSaved }: {
  existing?: NFSExport; onClose: () => void; onSaved: () => void
}) {
  const [path, setPath] = useState(existing?.path ?? '')
  const [clients, setClients] = useState(existing?.clients ?? '*')
  const [options, setOptions] = useState(existing?.options ?? DEFAULT_OPTIONS)
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)

  const mutation = useMutation({
    mutationFn: () => existing
      ? api.post(`/api/nfs/exports/${existing.id}/update`, { path, clients, options, enabled })
      : api.post('/api/nfs/exports', { path, clients, options, enabled }),
    onSuccess: () => {
      toast.success(existing ? 'Export updated' : 'Export created')
      onSaved(); onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (!path.trim()) { toast.error('Path required'); return }
    if (!path.startsWith('/')) { toast.error('Path must be absolute'); return }
    if (!clients.trim()) { toast.error('Clients required (use * for all)'); return }
    mutation.mutate()
  }

  return (
    <Modal title={existing ? 'Edit NFS Export' : 'Add NFS Export'} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Export Path</span>
          <input value={path} onChange={e => setPath(e.target.value)} placeholder="/tank/media"
            className="input" autoFocus onKeyDown={e => e.key === 'Enter' && submit()} />
        </label>
        <label className="field">
          <span className="field-label">Clients</span>
          <input value={clients} onChange={e => setClients(e.target.value)} placeholder="* or 192.168.1.0/24"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} />
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            * = all, 192.168.1.0/24, hostname.local, or space-separated list
          </span>
        </label>
        <label className="field">
          <span className="field-label">Options</span>
          <input value={options} onChange={e => setOptions(e.target.value)}
            placeholder={DEFAULT_OPTIONS} className="input" style={{ fontFamily: 'var(--font-mono)' }} />
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            Common: rw/ro, sync/async, no_subtree_check, no_root_squash, root_squash
          </span>
        </label>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)}
            style={{ accentColor: 'var(--primary)', width: 16, height: 16 }} />
          Enabled
        </label>
      </div>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 24 }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} disabled={mutation.isPending} className="btn btn-primary">
          {mutation.isPending ? 'Saving…' : existing ? 'Save Changes' : 'Add Export'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ExportRow
// ---------------------------------------------------------------------------

function ExportRow({ exp, onEdit, onDeleted }: {
  exp: NFSExport; onEdit: () => void; onDeleted: () => void
}) {
  const [confirming, setConfirming] = useState(false)

  const deleteMutation = useMutation({
    mutationFn: () => api.delete(`/api/nfs/exports/${exp.id}`, {}),
    onSuccess: () => { toast.success('Export deleted'); onDeleted() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '16px 20px' }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14 }}>
        <Icon name={exp.enabled ? 'share' : 'block'} size={22}
          style={{ color: exp.enabled ? 'var(--primary)' : 'var(--text-tertiary)', flexShrink: 0, marginTop: 2 }} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', fontWeight: 700, marginBottom: 4 }}>
            {exp.path}
          </div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 8 }}>
            {exp.path} {exp.clients}({exp.options})
          </div>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            <Tag label={exp.clients} icon="dns" />
            <Tag label={exp.options} icon="settings" mono />
            {!exp.enabled && <Tag label="Disabled" color="var(--warning)" />}
          </div>
        </div>
        <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
          <button className="btn btn-ghost" onClick={onEdit} title="Edit">
            <Icon name="edit" size={14} />
          </button>
          {!confirming
            ? <button className="btn btn-danger" onClick={() => setConfirming(true)}><Icon name="delete" size={14} /></button>
            : <>
                <button className="btn btn-danger" onClick={() => deleteMutation.mutate()} disabled={deleteMutation.isPending}>
                  {deleteMutation.isPending ? '…' : 'Delete'}
                </button>
                <button className="btn btn-ghost" onClick={() => setConfirming(false)}>Cancel</button>
              </>
          }
        </div>
      </div>
    </div>
  )
}

function Tag({ label, icon, mono, color }: { label: string; icon?: string; mono?: boolean; color?: string }) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, padding: '2px 8px', borderRadius: 'var(--radius-sm)', background: 'var(--surface)', border: '1px solid var(--border)', fontSize: 'var(--text-xs)', color: color ?? 'var(--text-secondary)', fontFamily: mono ? 'var(--font-mono)' : 'var(--font-ui)' }}>
      {icon && <Icon name={icon} size={12} />}
      {label}
    </span>
  )
}

// ---------------------------------------------------------------------------
// NFSPage
// ---------------------------------------------------------------------------

export function NFSPage() {
  const qc = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)
  const [editTarget, setEditTarget] = useState<NFSExport | null>(null)

  const statusQ = useQuery({
    queryKey: ['nfs', 'status'],
    queryFn: ({ signal }) => api.get<NFSStatusResponse>('/api/nfs/status', signal),
    refetchInterval: 30_000,
  })
  const exportsQ = useQuery({
    queryKey: ['nfs', 'exports'],
    queryFn: ({ signal }) => api.get<NFSExportsResponse>('/api/nfs/exports', signal),
    refetchInterval: 30_000,
  })

  const reload = useMutation({
    mutationFn: () => api.post('/api/nfs/reload', {}),
    onSuccess: () => { toast.success('NFS exports reloaded (exportfs -ra)'); qc.invalidateQueries({ queryKey: ['nfs'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  function refresh() { qc.invalidateQueries({ queryKey: ['nfs'] }) }

  const exports = exportsQ.data?.exports ?? []
  const status = statusQ.data?.status

  return (
    <div style={{ maxWidth: 1000 }}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 28 }}>
        <div>
          <h1 className="page-title">NFS Exports</h1>
          <p className="page-subtitle">Manage /etc/exports — changes apply on reload</p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button onClick={() => reload.mutate()} disabled={reload.isPending} className="btn btn-ghost">
            <Icon name="sync" size={16} />{reload.isPending ? 'Reloading…' : 'Reload (exportfs -ra)'}
          </button>
          <button onClick={() => setShowCreate(true)} className="btn btn-primary">
            <Icon name="add" size={16} /> Add Export
          </button>
        </div>
      </div>

      {/* NFS status bar */}
      {statusQ.data && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 16, padding: '12px 20px', background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', marginBottom: 24 }}>
          <div style={{ width: 10, height: 10, borderRadius: '50%', background: status?.running ? 'var(--success)' : 'var(--error)', boxShadow: status?.running ? '0 0 6px var(--success)' : 'none', flexShrink: 0 }} />
          <span style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>
            NFS {status?.running ? 'Running' : 'Stopped'}
          </span>
          {status?.version && <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{status.version}</span>}
          {status?.exports_count !== undefined && (
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{status.exports_count} exports active</span>
          )}
          {status?.active_connections !== undefined && (
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{status.active_connections} clients connected</span>
          )}
        </div>
      )}

      {/* Warning: nfs-kernel-server must be installed */}
      <div className="alert alert-warning" style={{ marginBottom: 20, display: 'flex', gap: 10 }}>
        <Icon name="warning" size={16} style={{ flexShrink: 0, marginTop: 1 }} />
        Requires <code style={{ fontFamily: 'var(--font-mono)' }}>nfs-kernel-server</code> installed on the host. NixOS: managed via <code style={{ fontFamily: 'var(--font-mono)' }}>services.nfs.server</code>.
      </div>

      {/* Exports list */}
      {exportsQ.isLoading && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {[0, 1, 2].map(i => <Skeleton key={i} height={90} style={{ borderRadius: 'var(--radius-lg)' }} />)}
        </div>
      )}
      {exportsQ.isError && <ErrorState error={exportsQ.error} onRetry={refresh} />}
      {!exportsQ.isLoading && !exportsQ.isError && exports.length === 0 && (
        <div style={{ textAlign: 'center', padding: '64px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
          <Icon name="share" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600 }}>No NFS exports configured</div>
          <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>Add an export to share directories over NFS</div>
        </div>
      )}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {exports.map(exp => (
          <ExportRow key={exp.id} exp={exp} onEdit={() => setEditTarget(exp)} onDeleted={refresh} />
        ))}
      </div>

      {showCreate && <ExportModal onClose={() => setShowCreate(false)} onSaved={refresh} />}
      {editTarget && <ExportModal existing={editTarget} onClose={() => setEditTarget(null)} onSaved={refresh} />}
    </div>
  )
}
