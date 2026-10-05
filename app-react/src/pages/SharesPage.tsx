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
 *
 * Time Machine, shadow copies (Previous Versions), recycle bin and host
 * restrictions are per-share options. The global card only switches the
 * Apple SMB extensions and can apply shadow copies / recycle bin to all shares.
 */

import type React from 'react'
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { itemOpacity } from '@/lib/listFilter'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { Modal } from '@/components/ui/Modal'
import { Tooltip } from '@/components/ui/Tooltip'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface Share {
  id:                  number
  name:                string
  path:                string
  comment:             string
  read_only:           boolean
  guest_ok:            boolean
  browsable:           boolean
  valid_users?:        string
  time_machine?:       boolean
  time_machine_quota?: string
  shadow_copy?:        boolean
  recycle_bin?:        boolean
  hosts_allow?:        string
  hosts_deny?:         string
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

function ProtocolOptions({ shares }: { shares: Share[] }) {
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
      qc.invalidateQueries({ queryKey: ['shares', 'list'] })
      toast.success('SMB settings updated')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const s = settingsQ.data
  const tmShares = shares.filter(sh => sh.time_machine).length
  const appleOn = (s?.time_machine ?? false) || tmShares > 0

  // all / some / none of the shares have the option
  function coverage(key: 'shadow_copy' | 'recycle_bin'): 'all' | 'some' | 'none' {
    const n = shares.filter(sh => sh[key]).length
    return n === 0 ? 'none' : n === shares.length ? 'all' : 'some'
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
          label="macOS support (Apple SMB extensions)"
          description={tmShares > 0
            ? `On automatically: ${tmShares} share${tmShares === 1 ? ' is a' : 's are'} Time Machine target${tmShares === 1 ? '' : 's'}. Choose targets in each share's settings.`
            : 'Loads the Apple SMB extensions (fruit) on every share for faster, more reliable macOS access. Time Machine targets are chosen per share.'}
          checked={appleOn}
          onChange={v => updateMut.mutate({ time_machine: v })}
          disabled={updateMut.isPending || tmShares > 0}
          extra={tmShares > 0 && s?.avahi_file_ok ? (
            <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--success)', fontWeight: 600 }}>Time Machine advertised</span>
          ) : tmShares > 0 && !s?.avahi_file_ok ? (
            <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--warning)', fontWeight: 600 }}>Bonjour advertisement missing</span>
          ) : null}
        />
        <ToggleRow
          icon="history"
          label="Shadow copies on all shares"
          description={`Exposes ZFS snapshots as Windows Previous Versions. Currently on for ${coverage('shadow_copy')} of the shares; toggling applies to every share. Set it per share in the share's settings.`}
          checked={coverage('shadow_copy') === 'all'}
          indeterminate={coverage('shadow_copy') === 'some'}
          onChange={v => updateMut.mutate({ shadow_copy: v })}
          disabled={updateMut.isPending || shares.length === 0}
        />
        <ToggleRow
          icon="delete"
          label="Recycle bin on all shares"
          description={`Moves deleted files to a per-user .recycle folder. Currently on for ${coverage('recycle_bin')} of the shares; toggling applies to every share.`}
          checked={coverage('recycle_bin') === 'all'}
          indeterminate={coverage('recycle_bin') === 'some'}
          onChange={v => updateMut.mutate({ recycle_bin: v })}
          disabled={updateMut.isPending || shares.length === 0}
        />
      </div>
    </div>
  )
}

function ToggleRow({ icon, label, description, checked, indeterminate, onChange, disabled, extra }: {
  icon: string
  label: string
  description: string
  checked: boolean
  indeterminate?: boolean
  onChange: (v: boolean) => void
  disabled: boolean
  extra?: React.ReactNode
}) {
  return (
    <label style={{ display: 'flex', alignItems: 'flex-start', gap: 12, cursor: disabled ? 'not-allowed' : 'pointer', opacity: disabled ? 0.7 : 1 }}>
      <input type="checkbox" checked={checked} onChange={e => onChange(e.target.checked)} disabled={disabled}
        ref={el => { if (el) el.indeterminate = !!indeterminate }}
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

type Purpose = 'default' | 'timemachine' | 'windows' | 'public'

const PURPOSES: { id: Purpose; label: string; hint: string }[] = [
  { id: 'default', label: 'Default share', hint: 'Plain SMB share for Windows, macOS and Linux.' },
  { id: 'timemachine', label: 'Time Machine backups', hint: 'Macs can back up here. Set a size cap so backups do not fill the pool.' },
  { id: 'windows', label: 'Windows with file history', hint: 'Previous Versions from ZFS snapshots, plus a recycle bin for deleted files.' },
  { id: 'public', label: 'Public read-only', hint: 'Anyone on the network can read, nobody can write.' },
]

function purposeOf(sh?: Share): Purpose {
  if (!sh) return 'default'
  if (sh.time_machine) return 'timemachine'
  if (sh.guest_ok && sh.read_only) return 'public'
  if (sh.shadow_copy && sh.recycle_bin) return 'windows'
  return 'default'
}

function ShareModal({ onClose, onSaved, editingShare }: { onClose: () => void; onSaved: () => void; editingShare?: Share }) {
  const [name, setName] = useState(editingShare?.name ?? '')
  const [path, setPath] = useState(editingShare?.path ?? '')
  const [comment, setComment] = useState(editingShare?.comment ?? '')
  const [readonly, setReadonly] = useState(editingShare?.read_only ?? false)
  const [guestok, setGuestok] = useState(editingShare?.guest_ok ?? false)
  const [validusers, setValidusers] = useState(editingShare?.valid_users ?? '')
  const [purpose, setPurpose] = useState<Purpose>(purposeOf(editingShare))
  const [timeMachine, setTimeMachine] = useState(editingShare?.time_machine ?? false)
  const [tmQuota, setTmQuota] = useState(editingShare?.time_machine_quota ?? '')
  const [shadowCopy, setShadowCopy] = useState(editingShare?.shadow_copy ?? false)
  const [recycleBin, setRecycleBin] = useState(editingShare?.recycle_bin ?? false)
  const [hostsAllow, setHostsAllow] = useState(editingShare?.hosts_allow ?? '')
  const [hostsDeny, setHostsDeny] = useState(editingShare?.hosts_deny ?? '')
  const [showAccess, setShowAccess] = useState(!!(editingShare?.hosts_allow || editingShare?.hosts_deny))

  function pickPurpose(p: Purpose) {
    setPurpose(p)
    setTimeMachine(p === 'timemachine')
    setShadowCopy(p === 'windows')
    setRecycleBin(p === 'windows')
    if (p === 'public') { setGuestok(true); setReadonly(true) }
    else if (purpose === 'public') { setGuestok(false); setReadonly(false) }
  }

  const mutation = useMutation({
    // Failures (e.g. Samba not updated) come back as 200 { success: false }.
    mutationFn: async () => ensureOk(await api.post<{ success?: boolean; error?: string; warning?: string }>('/api/shares', {
      action: editingShare ? 'update' : 'create',
      ...(editingShare ? { id: editingShare.id } : {}),
      name, path, comment, read_only: readonly, guest_ok: guestok, browsable: true, valid_users: validusers,
      time_machine: timeMachine,
      time_machine_quota: timeMachine ? tmQuota.trim() : '',
      shadow_copy: shadowCopy,
      recycle_bin: recycleBin,
      hosts_allow: hostsAllow.trim(),
      hosts_deny: hostsDeny.trim(),
    })),
    onSuccess: (res) => {
      toast.success(editingShare ? `Share "${name}" updated` : `Share "${name}" created`);
      if (res?.warning) toast.warning(res.warning)
      onSaved();
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (!name.trim()) { toast.error('Share name required'); return }
    if (!path.trim()) { toast.error('Path required'); return }
    if (!path.startsWith('/')) { toast.error('Path must be absolute (start with /)'); return }
    if (timeMachine && tmQuota.trim() && !/^[1-9][0-9]{0,8}[KMGT]$/.test(tmQuota.trim())) {
      toast.error('Time Machine size cap: a number with K, M, G or T (e.g. 500G)'); return
    }
    mutation.mutate()
  }

  return (
    <Modal title={editingShare ? "Edit SMB Share" : "Create SMB Share"} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14, maxHeight: '70vh', overflowY: 'auto', paddingRight: 4 }}>
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
          <span className="field-label">Purpose</span>
          <select value={purpose} onChange={e => pickPurpose(e.target.value as Purpose)} className="input">
            {PURPOSES.map(p => <option key={p.id} value={p.id}>{p.label}</option>)}
          </select>
          <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>{PURPOSES.find(p => p.id === purpose)!.hint}</span>
        </label>
        <label className="field">
          <span className="field-label">Comment (optional)</span>
          <input value={comment} onChange={e => setComment(e.target.value)} placeholder="Media library" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Valid Users (optional, space-separated)</span>
          <input value={validusers} onChange={e => setValidusers(e.target.value)} placeholder="alice bob @media" className="input" />
        </label>
        <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
          <CheckRow label="Read-only" checked={readonly} onChange={setReadonly} />
          <CheckRow label="Guest access" checked={guestok} onChange={setGuestok} />
        </div>

        <div className="card" style={{ padding: 14, display: 'flex', flexDirection: 'column', gap: 10, background: 'var(--bg-elevated)' }}>
          <span className="field-label" style={{ margin: 0 }}>Share options</span>
          <CheckRow label="Time Machine target (macOS backups)" checked={timeMachine} onChange={setTimeMachine} />
          {timeMachine && (
            <label className="field" style={{ marginLeft: 24 }}>
              <span className="field-label">Size cap (optional, e.g. 500G)</span>
              <input value={tmQuota} onChange={e => setTmQuota(e.target.value)} placeholder="no cap" className="input" style={{ maxWidth: 200 }} />
            </label>
          )}
          <CheckRow label="Previous Versions (ZFS snapshots as shadow copies)" checked={shadowCopy} onChange={setShadowCopy} />
          <CheckRow label="Recycle bin (.recycle/<user>)" checked={recycleBin} onChange={setRecycleBin} />
          {timeMachine && readonly && (
            <div className="alert alert-warning" style={{ fontSize: 'var(--text-xs)' }}>
              <Icon name="warning" size={16} /><span>Time Machine needs write access. Turn off Read-only.</span>
            </div>
          )}
        </div>

        <button type="button" className="btn btn-ghost btn-sm" style={{ alignSelf: 'flex-start' }} onClick={() => setShowAccess(a => !a)}>
          <Icon name="lan" size={14} />{showAccess ? 'Hide' : 'Show'} network access restrictions
        </button>
        {showAccess && (
          <div className="card" style={{ padding: 14, display: 'flex', flexDirection: 'column', gap: 12, background: 'var(--bg-elevated)' }}>
            <label className="field">
              <span className="field-label">Hosts allow (IPs, CIDRs or host names)</span>
              <input value={hostsAllow} onChange={e => setHostsAllow(e.target.value)} placeholder="192.168.1.0/24 10.0.0.5" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
            </label>
            <label className="field">
              <span className="field-label">Hosts deny</span>
              <input value={hostsDeny} onChange={e => setHostsDeny(e.target.value)} placeholder="ALL" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
            </label>
            <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>
              Empty means no restriction. When a client matches both lists, hosts allow wins.
            </span>
          </div>
        )}
      </div>
      <div className="modal-footer">
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} disabled={mutation.isPending || (timeMachine && readonly)} className="btn btn-primary">
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
  const { confirm, ConfirmDialog } = useConfirm()

  const deleteMutation = useMutation({
    mutationFn: async () => ensureOk(await api.delete<{ success?: boolean; error?: string; warning?: string }>('/api/shares', { name: share.name })),
    onSuccess: (res) => {
      toast.success(`Share "${share.name}" deleted`)
      if (res?.warning) toast.warning(res.warning)
      onDeleted()
    },
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
          {share.time_machine && <Badge label={share.time_machine_quota ? `Time Machine · ${share.time_machine_quota}` : 'Time Machine'} color="var(--primary)" />}
          {share.shadow_copy && <Badge label="Previous Versions" color="var(--success)" />}
          {share.recycle_bin && <Badge label="Recycle bin" color="var(--success)" />}
          {(share.hosts_allow || share.hosts_deny) && <Badge label="Host restricted" color="var(--warning)" />}
        </div>
      </div>
      <div style={{ display: 'flex', gap: 8, flexShrink: 0 }}>
        <button className="btn btn-ghost" onClick={onEdit}><Icon name="edit" size={14} />Edit</button>
        <button className="btn btn-danger" onClick={async () => { if (await confirm({ title: `Delete share "${share.name}"?`, message: 'This SMB share will be removed.', danger: true, confirmLabel: 'Delete' })) { deleteMutation.mutate() } }} disabled={deleteMutation.isPending}>
          <Icon name="delete" size={14} />Delete
        </button>
      </div>
      <ConfirmDialog />
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
  const [shareFilter, setShareFilter] = useState('')

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
  const shares = sharesQ.data?.shares ?? sharesQ.data?.data ?? []
  function refresh() { qc.invalidateQueries({ queryKey: ['shares', 'list'] }) }

  return (
    <div style={{ maxWidth: 1000 }}>
      <div className="page-header">
        <div>
          <h1 className="page-title">SMB Shares</h1>
          <p className="page-subtitle">Manage Samba (SMB) file shares. For NFS exports, see NFS Exports in the sidebar.</p>
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
            <ProtocolOptions shares={shares} />
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
            {shares.length > 3 && (
              <div style={{ position: 'relative', marginBottom: 12 }}>
                <Icon name="search" size={15} style={{ position: 'absolute', left: 10, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-tertiary)', pointerEvents: 'none' }} />
                <input
                  value={shareFilter}
                  onChange={e => setShareFilter(e.target.value)}
                  placeholder="Filter shares…"
                  className="input"
                  style={{ paddingLeft: 32, height: 34, fontSize: 'var(--text-sm)' }}
                />
                {shareFilter && (
                  <button onClick={() => setShareFilter('')} style={{ position: 'absolute', right: 8, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', display: 'flex', padding: 2 }}>
                    <Icon name="close" size={14} />
                  </button>
                )}
              </div>
            )}
            <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
              {shares.map(share => (
                <div key={share.name} style={{ opacity: itemOpacity(shareFilter, share.name, share.path ?? ''), transition: 'opacity 0.12s', pointerEvents: itemOpacity(shareFilter, share.name, share.path ?? '') < 1 ? 'none' : undefined }}>
                  <ShareCard share={share} onDeleted={refresh} onEdit={() => setEditingShare(share)} />
                </div>
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

