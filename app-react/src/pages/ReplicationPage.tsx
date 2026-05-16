/**
 * pages/ReplicationPage.tsx - ZFS Replication
 *
 * Routes used:
 *   GET    /api/replication/remotes               peer list
 *   POST   /api/replication/remotes               create peer
 *   PUT    /api/replication/remotes/{id}          update peer
 *   DELETE /api/replication/remotes/{id}          delete peer
 *   POST   /api/replication/remotes/{id}/authorize        push replication key (one-time password)
 *   POST   /api/replication/remotes/{id}/test             verify key-based SSH + ZFS readiness
 *   POST   /api/replication/remotes/{id}/reset-fingerprint clear pinned TOFU host key
 *   GET    /api/replication/ssh-pubkey             daemon public key (sovereign targets)
 *   POST   /api/replication/ssh-keygen             regenerate keypair
 *   GET    /api/zfs/datasets                       dataset picker
 *   GET    /api/replication/schedules
 *   POST   /api/replication/schedules
 *   PUT    /api/replication/schedules/{id}
 *   DELETE /api/replication/schedules/{id}
 *   POST   /api/replication/schedules/{id}/run
 *   POST   /api/replication/remote                 one-shot async send
 */

import { useState, useMemo, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { useJob } from '@/hooks/useJob'
import { toast } from '@/hooks/useToast'
import { Modal } from '@/components/ui/Modal'
import { Tooltip } from '@/components/ui/Tooltip'
import { useConfirm } from '@/components/ui/ConfirmDialog'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface Remote {
  id:            string
  name:          string
  host:          string
  user:          string
  port:          number
  fingerprint:   string
  key_installed: boolean
  last_tested:   string
  test_ok:       boolean
  created_at:    string
}
interface RemotesResponse { success: boolean; remotes: Remote[] }

interface ZFSDataset { name: string; used: string; avail: string }
interface DatasetsResponse { success: boolean; data: ZFSDataset[] }

interface Snapshot { name: string; dataset: string; snap_name: string; used: string; refer: string; creation: string }
interface SnapshotsResponse { success: boolean; snapshots: Snapshot[]; count: number }

interface PubKeyResponse { success: boolean; exists: boolean; public_key?: string; key_path?: string }

interface JobStartResponse { job_id: string }

interface ReplicationSchedule {
  id:                       string
  name:                     string
  source_dataset:           string
  remote_id:                string
  remote_pool:              string
  interval:                 'hourly' | 'daily' | 'weekly' | 'manual'
  trigger_on_snapshot:      boolean
  incremental:              boolean
  resume:                   boolean
  compress:                 boolean
  non_recursive?:           boolean
  rate_limit_mb:            number
  enabled:                  boolean
  last_run?:                string
  last_status?:             string
  last_job_id?:             string
  last_replicated_snapshot?: string
}
interface ReplSchedulesResponse { success: boolean; schedules: ReplicationSchedule[] }

type ConfirmFn = (opts: { title: string; message?: string; confirmLabel?: string; cancelLabel?: string; danger?: boolean }) => Promise<boolean>

// ---------------------------------------------------------------------------
// JobStatusBanner
// ---------------------------------------------------------------------------

function JobStatusBanner({ jobId, onDone }: { jobId: string | null; onDone?: () => void }) {
  const job = useJob(jobId)

  if (!jobId) return null

  if (job.isLoading) return (
    <div className="alert alert-info" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <Icon name="sync" size={16} style={{ animation: 'spin 1s linear infinite' }} />
      Starting job...
    </div>
  )

  const status = job.data?.status
  const progress = job.data?.progress

  if (job.interrupted) return (
    <div className="alert alert-warning" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <Icon name="warning" size={16} />
      Job interrupted - daemon may have restarted
    </div>
  )

  if (status === 'running') {
    const hasProgress = progress && progress.bytes_sent != null
    return (
      <div className="alert alert-info" style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <Icon name="sync" size={16} style={{ animation: 'spin 2s linear infinite' }} />
          <span style={{ flex: 1 }}>
            Replication running...
            <span style={{ color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', marginLeft: 8 }}>
              job:{jobId.slice(0, 8)}
            </span>
          </span>
          {hasProgress && progress.rate_mbs != null && (
            <span style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>
              {progress.rate_mbs.toFixed(1)} MB/s
            </span>
          )}
        </div>
        {hasProgress && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 'var(--text-2xs)', color: 'var(--text-secondary)' }}>
              <span>{formatBytes(progress.bytes_sent ?? 0)} {progress.total_bytes ? `of ${formatBytes(progress.total_bytes)}` : ''}</span>
              {progress.eta_seconds != null && progress.eta_seconds > 0 && (
                <span>ETA: {formatDuration(progress.eta_seconds)}</span>
              )}
            </div>
            <div style={{ width: '100%', height: 6, background: 'rgba(255,255,255,0.1)', borderRadius: 3, overflow: 'hidden' }}>
              <div style={{ width: `${progress.percent ?? 0}%`, height: '100%', background: 'var(--primary)', transition: 'width 0.5s ease-out' }} />
            </div>
          </div>
        )}
      </div>
    )
  }

  if (status === 'done') {
    onDone?.()
    return (
      <div className="alert alert-success" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <Icon name="check_circle" size={16} />
        Replication completed successfully
      </div>
    )
  }

  if (status === 'failed') return (
    <div className="alert alert-error" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <Icon name="error" size={16} />
      Replication failed: {job.data?.error ?? 'unknown error'}
    </div>
  )

  return null
}

function formatBytes(bytes: number) {
  if (bytes === 0) return '0 B'
  const k = 1024
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.floor(Math.log(bytes) / Math.log(k))
  return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i]
}

function formatDuration(seconds: number) {
  if (seconds < 60) return `${Math.round(seconds)}s`
  const mins = Math.floor(seconds / 60)
  const secs = Math.round(seconds % 60)
  if (mins < 60) return `${mins}m ${secs}s`
  const hrs = Math.floor(mins / 60)
  return `${hrs}h ${mins % 60}m`
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

function StatusBadge({ status }: { status?: string }) {
  if (!status) return <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>-</span>
  const colors: Record<string, { bg: string; border: string; text: string }> = {
    running: { bg: 'var(--info-bg,#1a2a3a)', border: 'var(--info-border,#2a4a6a)', text: 'var(--info,#60a5fa)' },
    done:    { bg: 'var(--success-bg)',      border: 'var(--success-border)',       text: 'var(--success)' },
    failed:  { bg: 'var(--error-bg)',        border: 'var(--error-border)',         text: 'var(--error)' },
    pending: { bg: 'var(--surface)',         border: 'var(--border)',               text: 'var(--text-secondary)' },
  }
  const c = colors[status] ?? colors.pending
  return (
    <span style={{ padding: '2px 8px', borderRadius: 'var(--radius-full)', background: c.bg, border: `1px solid ${c.border}`, color: c.text, fontSize: 'var(--text-2xs)', fontWeight: 700, textTransform: 'uppercase' }}>
      {status}
    </span>
  )
}

// ---------------------------------------------------------------------------
// PeerAuthBadge
// ---------------------------------------------------------------------------

function PeerAuthBadge({ peer }: { peer: Remote }) {
  if (peer.key_installed) {
    return (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, padding: '2px 8px', borderRadius: 'var(--radius-full)', background: 'var(--success-bg)', border: '1px solid var(--success-border)', color: 'var(--success)', fontSize: 'var(--text-2xs)', fontWeight: 700 }}>
        <Icon name="check_circle" size={11} /> Authorized
      </span>
    )
  }
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, padding: '2px 8px', borderRadius: 'var(--radius-full)', background: 'var(--warning-bg,#2a1f0a)', border: '1px solid var(--warning-border,#6a4a1a)', color: 'var(--warning,#f59e0b)', fontSize: 'var(--text-2xs)', fontWeight: 700 }}>
      <Icon name="warning" size={11} /> Needs authorization
    </span>
  )
}

function PeerTestBadge({ peer }: { peer: Remote }) {
  if (!peer.last_tested || peer.last_tested === '0001-01-01T00:00:00Z') {
    return <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Never tested</span>
  }
  const when = new Date(peer.last_tested).toLocaleString()
  if (peer.test_ok) {
    return (
      <Tooltip content={`Last tested: ${when}`}>
        <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, color: 'var(--success)', fontSize: 'var(--text-xs)' }}>
          <Icon name="check_circle" size={13} /> OK
        </span>
      </Tooltip>
    )
  }
  return (
    <Tooltip content={`Last tested: ${when}`}>
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, color: 'var(--error)', fontSize: 'var(--text-xs)' }}>
        <Icon name="error" size={13} /> Failed
      </span>
    </Tooltip>
  )
}

// ---------------------------------------------------------------------------
// PeerModal (create + edit)
// ---------------------------------------------------------------------------

function PeerModal({ peer, onClose, onSaved }: { peer?: Remote; onClose: () => void; onSaved: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState(peer?.name ?? '')
  const [host, setHost] = useState(peer?.host ?? '')
  const [user, setUser] = useState(peer?.user ?? 'root')
  const [port, setPort] = useState(String(peer?.port ?? 22))

  const saveMutation = useMutation({
    mutationFn: () => {
      const body = { name, host, user, port: parseInt(port) || 22 }
      return peer
        ? api.put(`/api/replication/remotes/${peer.id}`, body)
        : api.post<{ success: boolean; remote: Remote }>('/api/replication/remotes', body)
    },
    onSuccess: () => {
      toast.success(peer ? 'Peer updated' : 'Peer added')
      qc.invalidateQueries({ queryKey: ['replication', 'remotes'] })
      onSaved()
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (!name.trim()) { toast.error('Name is required'); return }
    if (!host.trim()) { toast.error('Host is required'); return }
    saveMutation.mutate()
  }

  return (
    <Modal title={peer ? `Edit Peer: ${peer.name}` : 'Add Peer'} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Name</span>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="e.g. Backup-Site-A" className="input" autoFocus />
        </label>
        <label className="field">
          <span className="field-label">Host</span>
          <input value={host} onChange={e => setHost(e.target.value)} placeholder="192.168.1.50 or nas.local" className="input" />
        </label>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 100px', gap: 10 }}>
          <label className="field">
            <span className="field-label">SSH User</span>
            <input value={user} onChange={e => setUser(e.target.value)} placeholder="root" className="input" />
          </label>
          <label className="field">
            <span className="field-label">Port</span>
            <input value={port} onChange={e => setPort(e.target.value)} className="input" />
          </label>
        </div>
        {peer && !peer.key_installed && (
          <div className="alert alert-warning" style={{ fontSize: 'var(--text-sm)' }}>
            <Icon name="warning" size={14} /> Changing connection details clears the authorization state. You will need to re-authorize this peer.
          </div>
        )}
      </div>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 24 }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} disabled={saveMutation.isPending} className="btn btn-primary">
          {saveMutation.isPending ? 'Saving...' : peer ? 'Save Changes' : 'Add Peer'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// AuthorizeModal
// ---------------------------------------------------------------------------

function AuthorizeModal({ peer, onClose, onAuthorized }: { peer: Remote; onClose: () => void; onAuthorized: () => void }) {
  const qc = useQueryClient()
  const [password, setPassword] = useState('')
  const [fingerprint, setFingerprint] = useState<string | null>(null)

  const authMutation = useMutation({
    mutationFn: () => api.post<{ success: boolean; fingerprint?: string; message?: string }>(`/api/replication/remotes/${peer.id}/authorize`, { password }),
    onSuccess: data => {
      if (data.fingerprint) setFingerprint(data.fingerprint)
      toast.success('Peer authorized successfully')
      qc.invalidateQueries({ queryKey: ['replication', 'remotes'] })
      onAuthorized()
      if (!data.fingerprint) onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  if (fingerprint) {
    return (
      <Modal title={`Authorized: ${peer.name}`} onClose={onClose}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <div className="alert alert-success" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Icon name="check_circle" size={16} />
            Replication key installed. Password authentication is no longer required for this peer.
          </div>
          <div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 6 }}>Host fingerprint (SHA256) - verify out-of-band for high-security environments:</div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', padding: '8px 12px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', border: '1px solid var(--border)', wordBreak: 'break-all' }}>
              {fingerprint}
            </div>
          </div>
        </div>
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 24 }}>
          <button onClick={onClose} className="btn btn-primary">Done</button>
        </div>
      </Modal>
    )
  }

  return (
    <Modal title={`Authorize Peer: ${peer.name}`} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', margin: 0 }}>
          Enter the SSH password for <code style={{ fontFamily: 'var(--font-mono)' }}>{peer.user}@{peer.host}</code>.
          This installs the replication key and is a one-time step. The password is never stored.
        </p>
        <label className="field">
          <span className="field-label">SSH Password</span>
          <input
            type="password"
            value={password}
            onChange={e => setPassword(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter' && password) authMutation.mutate() }}
            placeholder="Password for one-time key installation"
            className="input"
            autoFocus
          />
        </label>
        {authMutation.isError && (
          <div className="alert alert-error" style={{ fontSize: 'var(--text-sm)' }}>
            {(authMutation.error as Error).message}
          </div>
        )}
      </div>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 24 }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => authMutation.mutate()} disabled={authMutation.isPending || !password} className="btn btn-primary">
          <Icon name="key" size={15} />
          {authMutation.isPending ? 'Authorizing...' : 'Authorize'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// PeersTab
// ---------------------------------------------------------------------------

function PeersTab({ confirm }: { confirm: ConfirmFn }) {
  const qc = useQueryClient()
  const [addingPeer, setAddingPeer]         = useState(false)
  const [editingPeer, setEditingPeer]       = useState<Remote | undefined>(undefined)
  const [authorizingPeer, setAuthorizingPeer] = useState<Remote | undefined>(undefined)
  const [testingId, setTestingId]           = useState<string | null>(null)

  const remotesQ = useQuery({
    queryKey: ['replication', 'remotes'],
    queryFn: ({ signal }) => api.get<RemotesResponse>('/api/replication/remotes', signal),
    refetchInterval: 30_000,
  })

  const pubKeyQ = useQuery({
    queryKey: ['replication', 'pubkey'],
    queryFn: ({ signal }) => api.get<PubKeyResponse>('/api/replication/ssh-pubkey', signal),
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/api/replication/remotes/${id}`),
    onSuccess: () => { toast.success('Peer deleted'); qc.invalidateQueries({ queryKey: ['replication', 'remotes'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const testMutation = useMutation({
    mutationFn: (id: string) => {
      setTestingId(id)
      return api.post<{ success: boolean; remote_hostname?: string; zfs_version?: string; zfs_ready?: boolean; duration_ms?: number; error?: string }>(`/api/replication/remotes/${id}/test`, {})
    },
    onSuccess: data => {
      setTestingId(null)
      qc.invalidateQueries({ queryKey: ['replication', 'remotes'] })
      if (data.success) {
        const parts = [data.remote_hostname ?? 'unknown host']
        if (data.zfs_ready) parts.push(`ZFS ${data.zfs_version ?? 'ready'}`)
        else parts.push('no ZFS')
        if (data.duration_ms != null) parts.push(`${data.duration_ms}ms`)
        toast.success(`Test OK: ${parts.join(' | ')}`)
      } else {
        toast.error(`Test failed: ${data.error ?? 'unknown error'}`)
      }
    },
    onError: (e: Error) => { setTestingId(null); toast.error(e.message) },
  })

  const keygenMutation = useMutation({
    mutationFn: () => api.post<{ success: boolean }>('/api/replication/ssh-keygen', {}),
    onSuccess: () => {
      toast.success('New keypair generated. All peers must be re-authorized.')
      qc.invalidateQueries({ queryKey: ['replication', 'remotes'] })
      qc.invalidateQueries({ queryKey: ['replication', 'pubkey'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const resetFingerprintMutation = useMutation({
    mutationFn: (id: string) => api.post<{ success: boolean }>(`/api/replication/remotes/${id}/reset-fingerprint`, {}),
    onSuccess: () => {
      toast.success('Host trust cleared. The fingerprint will be re-pinned on next connection.')
      qc.invalidateQueries({ queryKey: ['replication', 'remotes'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const remotes = remotesQ.data?.remotes ?? []
  const pubKey = pubKeyQ.data?.public_key

  async function handleDelete(peer: Remote) {
    if (await confirm({ title: `Delete peer "${peer.name}"?`, message: 'This cannot be undone. Any schedules that reference this peer must be updated first.', danger: true, confirmLabel: 'Delete' })) {
      deleteMutation.mutate(peer.id)
    }
  }

  async function handleResetTrust(peer: Remote) {
    if (await confirm({
      title: `Reset trust for "${peer.name}"?`,
      message: 'The pinned host fingerprint will be cleared. The daemon will re-pin the fingerprint on the next connection. Only do this if the remote host key changed intentionally.',
      confirmLabel: 'Reset Trust',
      danger: true,
    })) {
      resetFingerprintMutation.mutate(peer.id)
    }
  }

  async function handleKeygen() {
    if (await confirm({
      title: 'Generate new keypair?',
      message: 'This invalidates the replication key on all currently authorized peers. Each peer will need to be re-authorized before replication can resume.',
      danger: true,
      confirmLabel: 'Generate New Key',
    })) {
      keygenMutation.mutate()
    }
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24 }}>
      {/* Peer table */}
      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 20 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <Icon name="device_hub" size={24} style={{ color: 'var(--primary)' }} />
            <div>
              <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>Replication Peers</div>
              <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Authorized SSH targets for ZFS send/receive</div>
            </div>
          </div>
          <button onClick={() => setAddingPeer(true)} className="btn btn-primary">
            <Icon name="add" size={16} /> Add Peer
          </button>
        </div>

        {remotesQ.isLoading && <Skeleton height={160} />}
        {remotesQ.isError && <ErrorState error={remotesQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['replication', 'remotes'] })} />}

        {!remotesQ.isLoading && !remotesQ.isError && remotes.length === 0 && (
          <div style={{ textAlign: 'center', padding: '48px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
            <Icon name="device_hub" size={40} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
            <div style={{ fontWeight: 600, fontSize: 'var(--text-lg)' }}>No peers configured</div>
            <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>Add a peer to enable replication</div>
          </div>
        )}

        {remotes.length > 0 && (
          <div style={{ overflowX: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 'var(--text-sm)' }}>
              <thead>
                <tr style={{ borderBottom: '1px solid var(--border)', color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                  {['Name', 'Host', 'Authorization', 'Last Test', 'Actions'].map(h => (
                    <th key={h} style={{ padding: '8px 12px', textAlign: 'left', fontWeight: 600 }}>{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {remotes.map(peer => (
                  <tr key={peer.id} style={{ borderBottom: '1px solid var(--border)' }}>
                    <td style={{ padding: '12px 12px', fontWeight: 600 }}>{peer.name}</td>
                    <td style={{ padding: '12px 12px', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                      {peer.user}@{peer.host}:{peer.port}
                    </td>
                    <td style={{ padding: '12px 12px' }}>
                      <PeerAuthBadge peer={peer} />
                    </td>
                    <td style={{ padding: '12px 12px' }}>
                      <PeerTestBadge peer={peer} />
                    </td>
                    <td style={{ padding: '12px 12px' }}>
                      <div style={{ display: 'flex', gap: 6 }}>
                        <Tooltip content={peer.key_installed ? 'Re-install key (useful after key regeneration or if auth breaks)' : 'Install replication key via one-time password'}>
                          <button onClick={() => setAuthorizingPeer(peer)} className={`btn btn-sm ${peer.key_installed ? 'btn-ghost' : 'btn-primary'}`}>
                            <Icon name="key" size={13} /> {peer.key_installed ? 'Re-auth' : 'Authorize'}
                          </button>
                        </Tooltip>
                        {peer.key_installed && (
                          <Tooltip content="Test SSH connection and ZFS readiness">
                            <button onClick={() => testMutation.mutate(peer.id)} disabled={testMutation.isPending && testingId === peer.id} className="btn btn-sm btn-ghost">
                              <Icon name="wifi" size={13} />
                              {testMutation.isPending && testingId === peer.id ? 'Testing...' : 'Test'}
                            </button>
                          </Tooltip>
                        )}
                        {peer.fingerprint && (
                          <Tooltip content="Clear the pinned host fingerprint. Use this if the remote host key changed intentionally.">
                            <button onClick={() => handleResetTrust(peer)} disabled={resetFingerprintMutation.isPending} className="btn btn-sm btn-ghost" style={{ color: 'var(--warning,#f59e0b)' }}>
                              <Icon name="lock_reset" size={13} /> Reset Trust
                            </button>
                          </Tooltip>
                        )}
                        <Tooltip content="Edit">
                          <button onClick={() => setEditingPeer(peer)} className="btn btn-sm btn-ghost">
                            <Icon name="edit" size={13} />
                          </button>
                        </Tooltip>
                        <Tooltip content="Delete">
                          <button onClick={() => handleDelete(peer)} disabled={deleteMutation.isPending} className="btn btn-sm btn-ghost" style={{ color: 'var(--error)' }}>
                            <Icon name="delete" size={13} />
                          </button>
                        </Tooltip>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Sovereign Target Key */}
      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
          <Icon name="shield" size={22} style={{ color: 'var(--text-tertiary)' }} />
          <div>
            <div style={{ fontWeight: 600, fontSize: 'var(--text-base)' }}>Sovereign Target Key</div>
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>For air-gapped or high-security hosts where password authentication is disabled. Copy this key to the target's authorized_keys manually, then use the Authorize flow will be skipped and Test will verify the connection.</div>
          </div>
        </div>

        {pubKeyQ.isLoading && <Skeleton height={60} />}

        {pubKey ? (
          <div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', padding: '10px 14px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', border: '1px solid var(--border)', wordBreak: 'break-all', color: 'var(--text-secondary)', marginBottom: 10 }}>
              {pubKey}
            </div>
            <div style={{ display: 'flex', gap: 8 }}>
              <button onClick={() => { navigator.clipboard.writeText(pubKey); toast.success('Copied') }} className="btn btn-ghost" style={{ fontSize: 'var(--text-xs)' }}>
                <Icon name="content_copy" size={13} /> Copy
              </button>
              <button onClick={handleKeygen} disabled={keygenMutation.isPending} className="btn btn-ghost" style={{ fontSize: 'var(--text-xs)', color: 'var(--error)' }}>
                <Icon name="autorenew" size={13} />
                {keygenMutation.isPending ? 'Generating...' : 'Generate New Key'}
              </button>
            </div>
          </div>
        ) : (
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>No key generated yet.</span>
            <button onClick={handleKeygen} disabled={keygenMutation.isPending} className="btn btn-ghost" style={{ fontSize: 'var(--text-sm)' }}>
              <Icon name="vpn_key" size={14} />
              {keygenMutation.isPending ? 'Generating...' : 'Generate Key'}
            </button>
          </div>
        )}
      </div>

      {addingPeer && <PeerModal onClose={() => setAddingPeer(false)} onSaved={() => {}} />}
      {editingPeer && <PeerModal peer={editingPeer} onClose={() => setEditingPeer(undefined)} onSaved={() => {}} />}
      {authorizingPeer && (
        <AuthorizeModal
          peer={authorizingPeer}
          onClose={() => setAuthorizingPeer(undefined)}
          onAuthorized={() => setAuthorizingPeer(undefined)}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// ReplicateForm
// ---------------------------------------------------------------------------

function ReplicateForm({ datasets, remotes }: { datasets: ZFSDataset[]; remotes: Remote[] }) {
  const authorizedPeers = remotes.filter(r => r.key_installed)
  const [remoteId, setRemoteId]       = useState(authorizedPeers[0]?.id ?? '')
  const [dataset, setDataset]         = useState(datasets[0]?.name ?? '')
  const [remotePool, setRemotePool]   = useState('')
  const [incremental, setIncremental] = useState(false)
  const [baseSnapshot, setBaseSnapshot] = useState('')
  const [resume, setResume]           = useState(false)
  const [rateLimitMb, setRateLimitMb] = useState(0)
  const [compress, setCompress]       = useState(true)
  const [nonRecursive, setNonRecursive] = useState(false)
  const [jobId, setJobId]             = useState<string | null>(null)

  // Fetch snapshots for the selected dataset when incremental is on
  const snapshotsQ = useQuery({
    queryKey: ['zfs', 'snapshots', dataset],
    queryFn: ({ signal }) => api.get<SnapshotsResponse>(`/api/zfs/snapshots?dataset=${encodeURIComponent(dataset)}`, signal),
    enabled: incremental && !!dataset,
  })
  const snapshots = snapshotsQ.data?.snapshots ?? []

  // Reset base snapshot when source dataset changes
  useEffect(() => { setBaseSnapshot('') }, [dataset])

  const sendMutation = useMutation({
    mutationFn: () => {
      if (!remoteId) throw new Error('Select an authorized peer')
      if (incremental && !baseSnapshot) throw new Error('Select a base snapshot for incremental send')
      return api.post<JobStartResponse>('/api/replication/remote', {
        remote_id:      remoteId,
        source_dataset: dataset,
        remote_pool:    remotePool || dataset,
        incremental,
        base_snapshot:  incremental ? baseSnapshot : undefined,
        resume,
        rate_limit:     rateLimitMb > 0 ? `${rateLimitMb}M` : undefined,
        compressed:     compress,
        non_recursive:  nonRecursive,
      })
    },
    onSuccess: data => setJobId(data.job_id),
    onError: (e: Error) => toast.error(e.message),
  })

  function start() {
    if (!remoteId) { toast.error('Select an authorized peer'); return }
    if (!dataset)  { toast.error('Select a source dataset'); return }
    if (incremental && !baseSnapshot) { toast.error('Select a base snapshot for incremental send'); return }
    setJobId(null)
    sendMutation.mutate()
  }

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 28 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 24 }}>
        <Icon name="sync_alt" size={28} style={{ color: 'var(--primary)' }} />
        <div>
          <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>Replicate Dataset</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>One-shot ZFS send to an authorized peer</div>
        </div>
      </div>

      {authorizedPeers.length === 0 ? (
        <div className="alert alert-warning" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <Icon name="warning" size={16} />
          No authorized peers. Add and authorize a peer in the Peers tab first.
        </div>
      ) : (
        <>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
            <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Peer</span>
              <select value={remoteId} onChange={e => setRemoteId(e.target.value)} className="input">
                {authorizedPeers.map(p => (
                  <option key={p.id} value={p.id}>{p.name} ({p.user}@{p.host}:{p.port})</option>
                ))}
              </select>
            </label>

            <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Source Dataset</span>
              <select value={dataset} onChange={e => setDataset(e.target.value)} className="input">
                {datasets.map(d => <option key={d.name} value={d.name}>{d.name}</option>)}
              </select>
            </label>

            <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Remote Pool / Dataset <span style={{ color: 'var(--text-tertiary)' }}>(optional)</span></span>
              <input value={remotePool} onChange={e => setRemotePool(e.target.value)} placeholder={`Default: ${dataset}`} className="input" />
            </label>

            <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Rate Limit MB/s <span style={{ color: 'var(--text-tertiary)' }}>(0 = unlimited)</span></span>
              <input type="number" value={rateLimitMb} min={0} onChange={e => setRateLimitMb(parseInt(e.target.value) || 0)} className="input" />
            </label>
          </div>

          <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap', marginTop: 14 }}>
            <CheckRow label="Recursive (include child datasets)" checked={!nonRecursive} onChange={v => setNonRecursive(!v)} />
            <CheckRow label="Incremental (send only changes)" checked={incremental} onChange={v => { setIncremental(v); if (!v) setBaseSnapshot('') }} />
            <CheckRow label="Resume interrupted transfer" checked={resume} onChange={setResume} />
            <CheckRow label="Compress stream (lz4)" checked={compress} onChange={setCompress} />
          </div>

          {incremental && (
            <div style={{ marginTop: 14 }}>
              <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Base Snapshot <span style={{ color: 'var(--error)', fontSize: 'var(--text-xs)' }}>required for incremental</span></span>
                {snapshotsQ.isLoading ? (
                  <select className="input" disabled><option>Loading snapshots...</option></select>
                ) : snapshots.length === 0 ? (
                  <div className="alert alert-warning" style={{ fontSize: 'var(--text-sm)', display: 'flex', alignItems: 'center', gap: 8 }}>
                    <Icon name="warning" size={14} /> No snapshots found for {dataset}. Create a snapshot first.
                  </div>
                ) : (
                  <select value={baseSnapshot} onChange={e => setBaseSnapshot(e.target.value)} className="input">
                    <option value="">-- select base snapshot --</option>
                    {snapshots.map(s => (
                      <option key={s.name} value={s.name}>{s.snap_name} ({s.used} used)</option>
                    ))}
                  </select>
                )}
              </label>
            </div>
          )}

          {jobId && <div style={{ marginTop: 16 }}><JobStatusBanner jobId={jobId} onDone={() => setJobId(null)} /></div>}

          <div style={{ marginTop: 20 }}>
            <button onClick={start} disabled={sendMutation.isPending} className="btn btn-primary">
              <Icon name={incremental ? 'sync' : 'send'} size={17} />
              {sendMutation.isPending ? 'Starting...' : incremental ? 'Send Incremental' : 'Start Replication'}
            </button>
          </div>
        </>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// ScheduleModal
// ---------------------------------------------------------------------------

const INTERVAL_LABELS: Record<ReplicationSchedule['interval'], string> = {
  hourly: 'Hourly', daily: 'Daily', weekly: 'Weekly', manual: 'Manual',
}

function ScheduleModal({ datasets, remotes, onClose, onSaved, editingSchedule }: {
  datasets: ZFSDataset[]
  remotes: Remote[]
  onClose: () => void
  onSaved: () => void
  editingSchedule?: ReplicationSchedule
}) {
  const [name,              setName]              = useState(editingSchedule?.name ?? '')
  const [sourceDataset,     setSourceDataset]     = useState(editingSchedule?.source_dataset ?? datasets[0]?.name ?? '')
  const [remoteId,          setRemoteId]          = useState(editingSchedule?.remote_id ?? remotes[0]?.id ?? '')
  const [remotePool,        setRemotePool]        = useState(editingSchedule?.remote_pool ?? '')
  const [interval,          setInterval]          = useState<ReplicationSchedule['interval']>(editingSchedule?.interval ?? 'daily')
  const [triggerOnSnapshot, setTriggerOnSnapshot] = useState(editingSchedule?.trigger_on_snapshot ?? false)
  const [incremental,       setIncremental]       = useState(editingSchedule?.incremental ?? false)
  const [resume,            setResume]            = useState(editingSchedule?.resume ?? false)
  const [compress,          setCompress]          = useState(editingSchedule?.compress ?? true)
  const [nonRecursive,      setNonRecursive]      = useState(editingSchedule?.non_recursive ?? false)
  const [rateLimitMB,       setRateLimitMB]       = useState(editingSchedule?.rate_limit_mb ?? 0)
  const [enabled,           setEnabled]           = useState(editingSchedule?.enabled ?? true)

  const selectedPeer = remotes.find(r => r.id === remoteId)
  const peerNotAuthorized = selectedPeer && !selectedPeer.key_installed

  const saveMutation = useMutation({
    mutationFn: (s: Partial<ReplicationSchedule>) =>
      editingSchedule
        ? api.put(`/api/replication/schedules/${editingSchedule.id}`, s)
        : api.post<{ success: boolean; schedule: ReplicationSchedule }>('/api/replication/schedules', s),
    onSuccess: () => {
      toast.success(editingSchedule ? 'Schedule updated' : 'Schedule created')
      onSaved()
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (!name.trim()) { toast.error('Name is required'); return }
    if (!sourceDataset) { toast.error('Source dataset is required'); return }
    if (!remoteId) { toast.error('Select a peer'); return }
    saveMutation.mutate({
      name,
      source_dataset:      sourceDataset,
      remote_id:           remoteId,
      remote_pool:         remotePool || sourceDataset,
      interval,
      trigger_on_snapshot: triggerOnSnapshot,
      incremental,
      resume,
      compress,
      non_recursive:       nonRecursive,
      rate_limit_mb:       rateLimitMB,
      enabled,
    })
  }

  return (
    <Modal title={editingSchedule ? 'Edit Replication Schedule' : 'New Replication Schedule'} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Name</span>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="e.g. tank-to-backup" className="input" autoFocus />
        </label>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
          <label className="field">
            <span className="field-label">Source Dataset</span>
            <select value={sourceDataset} onChange={e => setSourceDataset(e.target.value)} className="input">
              {datasets.map(d => <option key={d.name} value={d.name}>{d.name}</option>)}
            </select>
          </label>

          <label className="field">
            <span className="field-label">Interval</span>
            <select value={interval} onChange={e => setInterval(e.target.value as ReplicationSchedule['interval'])} className="input">
              {(Object.keys(INTERVAL_LABELS) as ReplicationSchedule['interval'][]).map(i => (
                <option key={i} value={i}>{INTERVAL_LABELS[i]}</option>
              ))}
            </select>
          </label>
        </div>

        <label className="field">
          <span className="field-label">Peer</span>
          <select value={remoteId} onChange={e => setRemoteId(e.target.value)} className="input">
            {remotes.length === 0
              ? <option value="">No peers configured</option>
              : remotes.map(p => (
                  <option key={p.id} value={p.id}>
                    {p.name} ({p.user}@{p.host}:{p.port}){!p.key_installed ? ' - needs authorization' : ''}
                  </option>
                ))
            }
          </select>
        </label>

        {peerNotAuthorized && (
          <div className="alert alert-warning" style={{ fontSize: 'var(--text-sm)', display: 'flex', alignItems: 'center', gap: 8 }}>
            <Icon name="warning" size={14} />
            This peer is not authorized yet. The schedule will be saved but skipped until you authorize the peer from the Peers tab.
          </div>
        )}

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
          <label className="field">
            <span className="field-label">Remote Pool / Dataset <span style={{ fontWeight: 400, color: 'var(--text-tertiary)' }}>(optional)</span></span>
            <input value={remotePool} onChange={e => setRemotePool(e.target.value)} placeholder={`Default: ${sourceDataset}`} className="input" />
          </label>

          <label className="field">
            <span className="field-label">Rate Limit MB/s <span style={{ fontWeight: 400, color: 'var(--text-tertiary)' }}>(0 = unlimited)</span></span>
            <input type="number" value={rateLimitMB} min={0} onChange={e => setRateLimitMB(parseInt(e.target.value) || 0)} className="input" />
          </label>
        </div>

        <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
          <CheckRow label="Recursive (include child datasets)" checked={!nonRecursive} onChange={v => setNonRecursive(!v)} />
          <CheckRow label="Compress stream" checked={compress} onChange={setCompress} />
          <CheckRow label="Incremental (use last snapshot as base)" checked={incremental} onChange={setIncremental} />
          <CheckRow label="Resume interrupted transfer" checked={resume} onChange={setResume} />
          <CheckRow label="Trigger after each snapshot" checked={triggerOnSnapshot} onChange={setTriggerOnSnapshot} />
          <CheckRow label="Enabled" checked={enabled} onChange={setEnabled} />
        </div>

        {interval === 'manual' && triggerOnSnapshot && (
          <div className="alert alert-info" style={{ fontSize: 'var(--text-sm)', display: 'flex', alignItems: 'flex-start', gap: 8 }}>
            <Icon name="info" size={14} style={{ flexShrink: 0, marginTop: 1 }} />
            <span>
              With interval set to Manual and "Trigger after each snapshot" enabled, replication runs automatically after each auto-snapshot but never on a fixed schedule. Use this to replicate immediately after every snapshot without an independent timer.
            </span>
          </div>
        )}
      </div>

      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 24 }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} disabled={saveMutation.isPending || remotes.length === 0} className="btn btn-primary">
          {saveMutation.isPending ? 'Saving...' : editingSchedule ? 'Save Changes' : 'Create Schedule'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// SchedulesTab
// ---------------------------------------------------------------------------

function SchedulesTab({ datasets, remotes, confirm }: { datasets: ZFSDataset[]; remotes: Remote[]; confirm: ConfirmFn }) {
  const qc = useQueryClient()
  const [showAdd, setShowAdd]                     = useState(false)
  const [editingSchedule, setEditingSchedule]     = useState<ReplicationSchedule | undefined>(undefined)
  const [runningId, setRunningId]                 = useState<string | null>(null)
  const [runJobId, setRunJobId]                   = useState<string | null>(null)

  const schedulesQ = useQuery({
    queryKey: ['replication', 'schedules'],
    queryFn: ({ signal }) => api.get<ReplSchedulesResponse>('/api/replication/schedules', signal),
    refetchInterval: 30_000,
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.delete(`/api/replication/schedules/${id}`),
    onSuccess: () => { toast.success('Schedule deleted'); qc.invalidateQueries({ queryKey: ['replication', 'schedules'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const runNowMutation = useMutation({
    mutationFn: (id: string) => api.post<{ success: boolean; job_id: string }>(`/api/replication/schedules/${id}/run`, {}),
    onSuccess: (data, id) => {
      toast.success('Replication job started')
      setRunJobId(data.job_id)
      setRunningId(id)
      qc.invalidateQueries({ queryKey: ['replication', 'schedules'] })
    },
    onError: (e: Error) => { toast.error(e.message); setRunningId(null) },
  })

  const schedules = schedulesQ.data?.schedules ?? []

  // Build a lookup map so each row can resolve its peer name without a nested find per render
  const remoteMap = useMemo(() => Object.fromEntries(remotes.map(r => [r.id, r])), [remotes])

  function formatLastRun(ts?: string) {
    if (!ts) return '-'
    try { return new Date(ts).toLocaleString() } catch { return ts }
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
        <button onClick={() => setShowAdd(true)} className="btn btn-primary">
          <Icon name="add" size={16} /> Add Schedule
        </button>
      </div>

      {runJobId && (
        <div style={{ marginBottom: 16 }}>
          <JobStatusBanner jobId={runJobId} onDone={() => { setRunJobId(null); setRunningId(null) }} />
        </div>
      )}

      {schedulesQ.isLoading && <Skeleton height={200} />}
      {schedulesQ.isError && <ErrorState error={schedulesQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['replication', 'schedules'] })} />}

      {!schedulesQ.isLoading && !schedulesQ.isError && schedules.length === 0 && (
        <div style={{ textAlign: 'center', padding: '64px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
          <Icon name="sync_alt" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600 }}>No replication schedules</div>
          <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>Add a schedule to automate ZFS replication</div>
        </div>
      )}

      {schedules.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 'var(--text-sm)' }}>
            <thead>
              <tr style={{ borderBottom: '1px solid var(--border)', color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                {['Name', 'Source Dataset', 'Peer', 'Interval', 'Trigger on Snap', 'Status', 'Last Run', 'Actions'].map(h => (
                  <th key={h} style={{ padding: '8px 12px', textAlign: 'left', fontWeight: 600 }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {schedules.map(s => {
                const peer = remoteMap[s.remote_id]
                const peerMissing = !peer
                const peerUnauthorized = peer && !peer.key_installed
                return (
                  <tr key={s.id} style={{ borderBottom: '1px solid var(--border)', opacity: s.enabled ? 1 : 0.6 }}>
                    <td style={{ padding: '12px 12px', fontWeight: 600 }}>{s.name}</td>
                    <td style={{ padding: '12px 12px', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>{s.source_dataset}</td>
                    <td style={{ padding: '12px 12px' }}>
                      {peerMissing ? (
                        <span style={{ color: 'var(--error)', fontSize: 'var(--text-xs)' }}>
                          <Icon name="error" size={12} /> Peer deleted
                        </span>
                      ) : (
                        <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-xs)' }}>
                          {peer.name}
                          {peerUnauthorized && (
                            <Tooltip content="Peer needs authorization">
                              <Icon name="warning" size={13} style={{ color: 'var(--warning,#f59e0b)' }} />
                            </Tooltip>
                          )}
                        </span>
                      )}
                    </td>
                    <td style={{ padding: '12px 12px' }}>
                      <span style={{ padding: '2px 8px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', border: '1px solid var(--border)' }}>
                        {INTERVAL_LABELS[s.interval] ?? s.interval}
                      </span>
                    </td>
                    <td style={{ padding: '12px 12px' }}>
                      {s.trigger_on_snapshot
                        ? <Icon name="check_circle" size={16} style={{ color: 'var(--success)' }} />
                        : <Icon name="remove" size={16} style={{ color: 'var(--text-tertiary)' }} />
                      }
                    </td>
                    <td style={{ padding: '12px 12px' }}><StatusBadge status={s.last_status} /></td>
                    <td style={{ padding: '12px 12px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                      {s.last_replicated_snapshot ? (
                        <Tooltip content={`Last snapshot: ${s.last_replicated_snapshot}`}>
                          <span>{formatLastRun(s.last_run)}</span>
                        </Tooltip>
                      ) : formatLastRun(s.last_run)}
                    </td>
                    <td style={{ padding: '12px 12px' }}>
                      <div style={{ display: 'flex', gap: 6 }}>
                        <Tooltip content="Run Now">
                          <button onClick={() => runNowMutation.mutate(s.id)} disabled={runNowMutation.isPending && runningId === s.id} className="btn btn-sm btn-ghost">
                            <Icon name="play_arrow" size={14} />
                            {runNowMutation.isPending && runningId === s.id ? 'Starting...' : 'Run'}
                          </button>
                        </Tooltip>
                        <Tooltip content="Edit">
                          <button onClick={() => setEditingSchedule(s)} className="btn btn-sm btn-ghost">
                            <Icon name="edit" size={14} />
                          </button>
                        </Tooltip>
                        <Tooltip content="Delete">
                          <button
                            onClick={async () => {
                              if (await confirm({ title: `Delete schedule "${s.name}"?`, message: 'This cannot be undone.', danger: true, confirmLabel: 'Delete' })) deleteMutation.mutate(s.id)
                            }}
                            disabled={deleteMutation.isPending}
                            className="btn btn-sm btn-ghost"
                            style={{ color: 'var(--error)' }}
                          >
                            <Icon name="delete" size={14} />
                          </button>
                        </Tooltip>
                      </div>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      )}

      {showAdd && (
        <ScheduleModal
          datasets={datasets}
          remotes={remotes}
          onClose={() => setShowAdd(false)}
          onSaved={() => qc.invalidateQueries({ queryKey: ['replication', 'schedules'] })}
        />
      )}
      {editingSchedule && (
        <ScheduleModal
          datasets={datasets}
          remotes={remotes}
          editingSchedule={editingSchedule}
          onClose={() => setEditingSchedule(undefined)}
          onSaved={() => qc.invalidateQueries({ queryKey: ['replication', 'schedules'] })}
        />
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// ReplicationPage
// ---------------------------------------------------------------------------

type Tab = 'replicate' | 'schedules' | 'peers'

export function ReplicationPage() {
  const [tab, setTab] = useState<Tab>('peers')
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()

  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets'],
    queryFn: ({ signal }) => api.get<DatasetsResponse>('/api/zfs/datasets', signal),
  })

  const remotesQ = useQuery({
    queryKey: ['replication', 'remotes'],
    queryFn: ({ signal }) => api.get<RemotesResponse>('/api/replication/remotes', signal),
    refetchInterval: 30_000,
  })

  const datasets = datasetsQ.data?.data ?? []
  const remotes  = remotesQ.data?.remotes ?? []

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'peers',     label: 'Peers',     icon: 'device_hub' },
    { id: 'schedules', label: 'Schedules', icon: 'schedule'   },
    { id: 'replicate', label: 'Replicate', icon: 'sync_alt'   },
  ]

  return (
    <div style={{ maxWidth: 1100 }}>
      <div className="page-header">
        <h1 className="page-title">Replication</h1>
        <p className="page-subtitle">ZFS send/receive - replicate datasets to remote hosts</p>
      </div>

      <div className="alert alert-info" style={{ marginBottom: 24, display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'flex-start' }}>
        <Icon name="info" size={16} style={{ flexShrink: 0, marginTop: 1 }} />
        <span>
          Replication jobs run asynchronously. Long transfers may take hours - job status updates every 2s.
          {' '}For exporting a zvol as a network NVMe disk (alternative to send/receive), use{' '}
          <a href="/nvme-of" style={{ color: 'var(--primary)', fontWeight: 600 }}>NVMe-oF</a>.
        </span>
      </div>

      <div className="tabs-underline" style={{ marginBottom: 28 }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'peers' && <PeersTab confirm={confirm} />}

      {tab === 'schedules' && (
        <>
          {(datasetsQ.isLoading || remotesQ.isLoading) && <Skeleton height={200} style={{ borderRadius: 'var(--radius-xl)' }} />}
          {!datasetsQ.isLoading && !remotesQ.isLoading && (
            <SchedulesTab datasets={datasets} remotes={remotes} confirm={confirm} />
          )}
        </>
      )}

      {tab === 'replicate' && (
        <>
          {(datasetsQ.isLoading || remotesQ.isLoading) && <Skeleton height={300} style={{ borderRadius: 'var(--radius-xl)' }} />}
          {datasetsQ.isError && <ErrorState error={datasetsQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['zfs', 'datasets'] })} />}
          {!datasetsQ.isLoading && !remotesQ.isLoading && !datasetsQ.isError && (
            <ReplicateForm datasets={datasets} remotes={remotes} />
          )}
        </>
      )}

      <ConfirmDialog />
    </div>
  )
}
