/**
 * pages/SharesPage.tsx - SMB/NFS Shares (Phase 2)
 *
 * Calls (matching daemon routes exactly):
 *   GET    /api/shares/list
 *   GET    /api/shares
 *   POST   /api/shares          (create)
 *   DELETE /api/shares          (delete, body: { name })
 *   POST   /api/shares/smb/reload
 *   POST   /api/shares/smb/test
 *   POST   /api/shares/nfs/reload
 *   GET    /api/smb/settings
 *   POST   /api/smb/settings
 */

import type React from 'react'
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { Modal } from '@/components/ui/Modal'
import { Tooltip } from '@/components/ui/Tooltip'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface Share {
  name:          string
  path:          string
  comment:       string
  read_only:     boolean  // backend field name
  guest_ok:      boolean  // backend field name
  browsable:     boolean  // backend field name
  valid_users?:  string   // backend field name
}

interface SharesListResponse { success: boolean; shares?: Share[]; data?: Share[] }

interface SMBSettings {
  success: boolean
  time_machine: boolean
  shadow_copy: boolean
  recycle_bin: boolean
  avahi_file_ok: boolean
}

interface SMBSession {
  id:           string
  user:         string
  ip:           string
  shares?:      string[]
  open_files?:  number
  connected_at?: string
}

// ---------------------------------------------------------------------------
// Protocol Options card
// ---------------------------------------------------------------------------

function ProtocolOptions() {
  const qc = useQueryClient()

  const settingsQ = useQuery<SMBSettings>({
    queryKey: ['smb', 'settings'],
    queryFn: () => api.get<SMBSettings>('/api/smb/settings'),
    refetchInterval: 60_000,
  })

  const updateMut = useMutation({
    mutationFn: (patch: { time_machine?: boolean; shadow_copy?: boolean; recycle_bin?: boolean }) =>
      api.post('/api/smb/settings', patch),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['smb', 'settings'] })
      toast.success('SMB settings updated')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const s = settingsQ.data

  function toggle(field: 'time_machine' | 'shadow_copy' | 'recycle_bin', value: boolean) {
    updateMut.mutate({ [field]: value })
  }

  if (settingsQ.isLoading) {
    return <div className="card" style={{ padding: 20, marginBottom: 24 }}><Skeleton style={{ height: 60 }} /></div>
  }

  return (
    <div className="card" style={{ padding: 20, marginBottom: 24 }}>
      <div style={{ fontWeight: 700, fontSize: 'var(--text-sm)', marginBottom: 14, display: 'flex', alignItems: 'center', gap: 8 }}>
        <Icon name="tune" size={16} style={{ color: 'var(--text-tertiary)' }} />
        Protocol Options
      </div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        <ToggleRow
          icon="computer"
          label="Time Machine (macOS)"
          description="Enables Samba fruit VFS and Avahi mDNS advertisement for macOS Time Machine backups"
          checked={s?.time_machine ?? false}
          onChange={v => toggle('time_machine', v)}
          disabled={updateMut.isPending}
          extra={s?.time_machine && s.avahi_file_ok ? (
            <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--success)', fontWeight: 600 }}>Avahi advertised</span>
          ) : s?.time_machine && !s.avahi_file_ok ? (
            <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--warning)', fontWeight: 600 }}>avahi-daemon not running</span>
          ) : null}
        />
        <ToggleRow
          icon="history"
          label="Shadow Copies (Previous Versions)"
          description="Exposes ZFS snapshots as Windows Previous Versions via Samba shadow_copy2. Works with snapshots created by the Snapshot Scheduler using any auto- prefix."
          checked={s?.shadow_copy ?? false}
          onChange={v => toggle('shadow_copy', v)}
          disabled={updateMut.isPending}
        />
        <ToggleRow
          icon="delete"
          label="Recycle Bin"
          description="Moves deleted files to a per-user .recycle folder instead of immediate deletion"
          checked={s?.recycle_bin ?? false}
          onChange={v => toggle('recycle_bin', v)}
          disabled={updateMut.isPending}
        />
      </div>
    </div>
  )
}

function ToggleRow({ icon, label, description, checked, onChange, disabled, extra }: {
  icon: string
  label: string
  description: string
  checked: boolean
  onChange: (v: boolean) => void
  disabled: boolean
  extra?: React.ReactNode
}) {
  return (
    <label style={{ display: 'flex', alignItems: 'flex-start', gap: 12, cursor: disabled ? 'not-allowed' : 'pointer', opacity: disabled ? 0.7 : 1 }}>
      <input type="checkbox" checked={checked} onChange={e => onChange(e.target.checked)} disabled={disabled}
        style={{ accentColor: 'var(--primary)', width: 16, height: 16, marginTop: 2, flexShrink: 0 }} />
      <Icon name={icon} size={16} style={{ color: 'var(--text-tertiary)', marginTop: 2, flexShrink: 0 }} />
      <div style={{ flex: 1 }}>
        <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, display: 'flex', alignItems: 'center', gap: 8 }}>
          {label}
          {extra}
        </div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>{description}</div>
      </div>
    </label>
  )
}

// ---------------------------------------------------------------------------
// CreateShareModal
// ---------------------------------------------------------------------------

function ShareModal({ onClose, onSaved, editingShare }: { onClose: () => void; onSaved: () => void; editingShare?: Share }) {
  const [name, setName] = useState(editingShare?.name ?? '')
  const [path, setPath] = useState(editingShare?.path ?? '')
  const [comment, setComment] = useState(editingShare?.comment ?? '')
  const [readonly, setReadonly] = useState(editingShare?.read_only ?? false)
  const [guestok, setGuestok] = useState(editingShare?.guest_ok ?? false)
  const [validusers, setValidusers] = useState(editingShare?.valid_users ?? '')

  const mutation = useMutation({
    mutationFn: () => api.post('/api/shares', { 
      action: editingShare ? 'update' : 'create', 
      name, path, comment, read_only: readonly, guest_ok: guestok, browsable: true, valid_users: validusers 
    }),
    onSuccess: () => { 
      toast.success(editingShare ? `Share "${name}" updated` : `Share "${name}" created`); 
      onSaved(); 
      onClose() 
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (!name.trim()) { toast.error('Share name required'); return }
    if (!path.trim()) { toast.error('Path required'); return }
    if (!path.startsWith('/')) { toast.error('Path must be absolute (start with /)'); return }
    mutation.mutate()
  }

  return (
    <Modal title={editingShare ? "Edit SMB Share" : "Create SMB Share"} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Share Name</span>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="e.g. media" className="input" autoFocus
            disabled={!!editingShare}
            onKeyDown={e => e.key === 'Enter' && submit()} />
          {editingShare && <span style={{ fontSize:'var(--text-xs)', color:'var(--text-tertiary)' }}>Share name cannot be changed</span>}
        </label>
        <label className="field">
          <span className="field-label">Path</span>
          <input value={path} onChange={e => setPath(e.target.value)} placeholder="/tank/media" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Comment (optional)</span>
          <input value={comment} onChange={e => setComment(e.target.value)} placeholder="Media library" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Valid Users (optional, space-separated)</span>
          <input value={validusers} onChange={e => setValidusers(e.target.value)} placeholder="alice bob @media" className="input" />
        </label>
        <div style={{ display: 'flex', gap: 24 }}>
          <CheckRow label="Read-only" checked={readonly} onChange={setReadonly} />
          <CheckRow label="Guest access" checked={guestok} onChange={setGuestok} />
        </div>
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} disabled={mutation.isPending} className="btn btn-primary">
          {mutation.isPending ? 'Saving…' : editingShare ? 'Save Changes' : 'Create Share'}
        </button>
      </div>
    </Modal>
  )
}

function CheckRow({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
      <input type="checkbox" checked={checked} onChange={e => onChange(e.target.checked)}
        style={{ accentColor: 'var(--primary)', width: 16, height: 16 }} />
      {label}
    </label>
  )
}

// ---------------------------------------------------------------------------
// ShareCard
// ---------------------------------------------------------------------------

function ShareCard({ share, onDeleted, onEdit }: { share: Share; onDeleted: () => void; onEdit: () => void }) {
  const [confirming, setConfirming] = useState(false)

  const deleteMutation = useMutation({
    mutationFn: () => api.delete('/api/shares', { name: share.name }),
    onSuccess: () => { toast.success(`Share "${share.name}" deleted`); onDeleted() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', display: 'flex', alignItems: 'flex-start', gap: 16 }}>
      <Icon name="folder_shared" size={28} style={{ color: 'var(--primary)', flexShrink: 0, marginTop: 2 }} />
      <div style={{ flex: 1 }}>
        <div style={{ fontSize: 'var(--text-md)', fontWeight: 700, marginBottom: 4 }}>{share.name}</div>
        <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', marginBottom: 8 }}>{share.path}</div>
        {share.comment && <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 8 }}>{share.comment}</div>}
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          {share.read_only && <Badge label="Read-only" color="var(--warning)" />}
          {share.guest_ok && <Badge label="Guest OK" color="var(--info)" />}
          {share.valid_users && <Badge label={`Users: ${share.valid_users}`} color="var(--text-tertiary)" />}
        </div>
      </div>
      <div style={{ display: 'flex', gap: 8, flexShrink: 0 }}>
        {!confirming
          ? <>
              <button className="btn btn-ghost" onClick={onEdit}><Icon name="edit" size={14} />Edit</button>
              <button className="btn btn-danger" onClick={() => setConfirming(true)}><Icon name="delete" size={14} />Delete</button>
            </>
          : <>
              <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', alignSelf: 'center' }}>Sure?</span>
              <button className="btn btn-danger" onClick={() => deleteMutation.mutate()} disabled={deleteMutation.isPending}>
                {deleteMutation.isPending ? '…' : 'Yes'}
              </button>
              <button className="btn btn-ghost" onClick={() => setConfirming(false)}>No</button>
            </>
        }
      </div>
    </div>
  )
}

function Badge({ label, color }: { label: string; color: string }) {
  return (
    <span style={{ padding: '2px 8px', borderRadius: 'var(--radius-full)', background: `${color}18`, border: `1px solid ${color}30`, color, fontSize: 'var(--text-2xs)', fontWeight: 600 }}>
      {label}
    </span>
  )
}

// ---------------------------------------------------------------------------
// SharesPage
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// ActiveSessions
// ---------------------------------------------------------------------------

function ActiveSessions() {
  const sessionsQ = useQuery({
    queryKey: ['smb', 'sessions'],
    queryFn: ({ signal }) => api.get<{ success: boolean; sessions: SMBSession[] }>('/api/shares/smb/sessions', signal),
    refetchInterval: 15_000,
  })

  const disconnectMut = useMutation({
    mutationFn: (id: string) => api.post('/api/shares/smb/sessions/disconnect', { id }),
    onSuccess: () => { toast.success('Session disconnected'); sessionsQ.refetch() },
    onError: (e: Error) => toast.error(e.message),
  })

  const sessions = sessionsQ.data?.sessions ?? []

  if (sessionsQ.isLoading) return <Skeleton height={120} style={{ borderRadius: 'var(--radius-xl)' }} />
  if (sessionsQ.isError)   return <ErrorState error={sessionsQ.error} onRetry={sessionsQ.refetch} />

  if (sessions.length === 0) {
    return (
      <div className="empty-state">
        <Icon name="person_off" className="ms" style={{ fontSize: 48, opacity: 0.3, display: 'block', margin: '0 auto 16px' }} />
        <div className="empty-state-title">No active SMB sessions</div>
        <div style={{ fontSize: 'var(--text-sm)', marginTop: 4 }}>Connected Windows and macOS clients will appear here</div>
      </div>
    )
  }

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 0, overflow: 'hidden' }}>
      <table className="data-table" aria-label="Active SMB sessions">
        <thead>
          <tr>
            <th scope="col">User</th>
            <th scope="col">IP Address</th>
            <th scope="col">Shares</th>
            <th scope="col">Open Files</th>
            <th scope="col">Connected</th>
            <th scope="col"><span className="sr-only">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          {sessions.map(s => (
            <tr key={s.id}>
              <td style={{ fontWeight: 600 }}>{s.user || 'guest'}</td>
              <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{s.ip}</td>
              <td style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                {s.shares?.join(', ') || '-'}
              </td>
              <td style={{ fontFamily: 'var(--font-mono)', textAlign: 'right' }}>
                {s.open_files ?? '-'}
              </td>
              <td style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                {s.connected_at ? new Date(s.connected_at).toLocaleTimeString() : '-'}
              </td>
              <td>
                <button
                  onClick={() => disconnectMut.mutate(s.id)}
                  disabled={disconnectMut.isPending}
                  className="btn btn-ghost"
                  style={{ fontSize: 'var(--text-xs)', padding: '4px 10px' }}
                  aria-label={`Disconnect session for ${s.user || 'guest'} from ${s.ip}`}
                >
                  <Icon name="logout" size={13} />Disconnect
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// ---------------------------------------------------------------------------
// SharesPage
// ---------------------------------------------------------------------------

export function SharesPage() {
  const qc = useQueryClient()
  const [tab, setTab] = useState<'shares' | 'sessions'>('shares')
  const [showCreate, setShowCreate] = useState(false)
  const [editingShare, setEditingShare] = useState<Share|null>(null)

  const sharesQ = useQuery({
    queryKey: ['shares', 'list'],
    queryFn: ({ signal }) => api.get<SharesListResponse>('/api/shares/list', signal),
    refetchInterval: 30_000,
  })

  const smbReload = useMutation({
    mutationFn: () => api.post('/api/shares/smb/reload', {}),
    onSuccess: () => toast.success('SMB config reloaded'),
    onError: (e: Error) => toast.error(e.message),
  })
  const smbTest = useMutation({
    mutationFn: () => api.post<{ success: boolean; output?: string; error?: string }>('/api/shares/smb/test', {}),
    onSuccess: (data) => {
      if (data.success) toast.success('SMB config OK')
      else toast.error(`SMB test failed: ${data.error || 'unknown'}`)
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const nfsReload = useMutation({
    mutationFn: () => api.post('/api/shares/nfs/reload', {}),
    onSuccess: () => toast.success('NFS exports reloaded'),
    onError: (e: Error) => toast.error(e.message),
  })

  const shares = sharesQ.data?.shares ?? sharesQ.data?.data ?? []
  function refresh() { qc.invalidateQueries({ queryKey: ['shares', 'list'] }) }

  return (
    <div style={{ maxWidth: 1000 }}>
      <div className="page-header">
        <div>
          <h1 className="page-title">Shares</h1>
          <p className="page-subtitle">SMB/NFS network shares and active connections</p>
        </div>
        {tab === 'shares' && (
          <div style={{ display: 'flex', gap: 8 }}>
            <Tooltip content="Test SMB config">
              <button onClick={() => smbTest.mutate()} disabled={smbTest.isPending} className="btn btn-ghost">
                <Icon name="bug_report" size={16} />{smbTest.isPending ? 'Testing…' : 'Test Config'}
              </button>
            </Tooltip>
            <Tooltip content="Reload SMB">
              <button onClick={() => smbReload.mutate()} disabled={smbReload.isPending} className="btn btn-ghost">
                <Icon name="restart_alt" size={16} />{smbReload.isPending ? 'Reloading…' : 'Reload SMB'}
              </button>
            </Tooltip>
            <Tooltip content="Reload NFS exports">
              <button onClick={() => nfsReload.mutate()} disabled={nfsReload.isPending} className="btn btn-ghost">
                <Icon name="sync" size={16} />{nfsReload.isPending ? 'Reloading…' : 'Reload NFS'}
              </button>
            </Tooltip>
            <button onClick={() => setShowCreate(true)} className="btn btn-primary">
              <Icon name="add" size={16} /> Add Share
            </button>
          </div>
        )}
      </div>

      {/* Tab bar */}
      <div role="tablist" aria-label="Shares sections" style={{ display: 'flex', gap: 4, marginBottom: 24, borderBottom: '1px solid var(--border)' }}>
        {(['shares', 'sessions'] as const).map(t => (
          <button
            key={t}
            role="tab"
            aria-selected={tab === t}
            aria-controls={`shares-panel-${t}`}
            id={`shares-tab-${t}`}
            onClick={() => setTab(t)}
            style={{
              padding: '8px 16px', background: 'none', border: 'none', cursor: 'pointer',
              fontSize: 'var(--text-sm)', fontWeight: 600, fontFamily: 'inherit',
              color: tab === t ? 'var(--primary)' : 'var(--text-tertiary)',
              borderBottom: `2px solid ${tab === t ? 'var(--primary)' : 'transparent'}`,
              marginBottom: -1, transition: 'all 0.15s',
            }}
          >
            {t === 'shares' ? 'Shares' : 'Active Sessions'}
          </button>
        ))}
      </div>

      <div role="tabpanel" id={`shares-panel-${tab}`} aria-labelledby={`shares-tab-${tab}`}>
        {tab === 'shares' ? (
          <>
            <ProtocolOptions />
            {sharesQ.isLoading && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
                {[0, 1, 2].map(i => <Skeleton key={i} height={100} style={{ borderRadius: 'var(--radius-lg)' }} />)}
              </div>
            )}
            {sharesQ.isError && <ErrorState error={sharesQ.error} onRetry={refresh} />}
            {!sharesQ.isLoading && !sharesQ.isError && shares.length === 0 && (
              <div className="empty-state">
                <Icon name="folder_shared" className="ms" style={{ fontSize: 48, opacity: 0.3, display: 'block', margin: '0 auto 16px' }} />
                <div className="empty-state-title">No shares configured</div>
                <div style={{ fontSize: 'var(--text-sm)', marginTop: 4 }}>Create a share to access data over the network</div>
              </div>
            )}
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              {shares.map(share => (
                <ShareCard key={share.name} share={share} onDeleted={refresh} onEdit={() => setEditingShare(share)} />
              ))}
            </div>
          </>
        ) : (
          <ActiveSessions />
        )}
      </div>

      {showCreate && (
        <ShareModal onClose={() => setShowCreate(false)} onSaved={refresh} />
      )}
      {editingShare && (
        <ShareModal editingShare={editingShare} onClose={() => setEditingShare(null)} onSaved={refresh} />
      )}
    </div>
  )
}

