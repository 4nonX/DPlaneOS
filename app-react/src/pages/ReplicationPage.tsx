/**
 * pages/ReplicationPage.tsx - ZFS Replication (Phase 2)
 *
 * Calls (matching daemon routes exactly):
 *   POST /api/replication/send            → async job { job_id }
 *   POST /api/replication/send-incremental → async job { job_id }
 *   POST /api/replication/test
 *   POST /api/replication/ssh-keygen
 *   GET  /api/replication/ssh-pubkey
 *   POST /api/replication/ssh-copy-id
 *   GET  /api/zfs/datasets                (populate dataset picker)
 *   GET  /api/replication/schedules       (schedules tab)
 *   POST /api/replication/schedules       (create schedule)
 *   DELETE /api/replication/schedules/{id}
 *   POST /api/replication/schedules/{id}/run
 *
 * Both send endpoints return { job_id } immediately (v3.3.2+ async).
 * Poll GET /api/jobs/{id} via useJob() hook.
 */

import { useState } from 'react'
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

interface ZFSDataset { name: string; used: string; avail: string }
interface DatasetsResponse { success: boolean; data: ZFSDataset[] }

interface TestResult { success: boolean; error?: string; latency_ms?: number }
interface SSHKeyResponse { success: boolean; public_key?: string; error?: string }
interface JobStartResponse { job_id: string }

interface ReplicationSchedule {
  id:                   string
  name:                 string
  source_dataset:       string
  remote_id?:           string
  remote_host:          string
  remote_user:          string
  remote_port:          number
  remote_pool:          string
  ssh_key_path?:        string
  interval:             'hourly' | 'daily' | 'weekly' | 'manual'
  trigger_on_snapshot:  boolean
  compress:             boolean
  rate_limit_mb:        number
  enabled:              boolean
  last_run?:            string
  last_status?:         string
  last_job_id?:         string
}
interface ReplSchedulesResponse { success: boolean; schedules: ReplicationSchedule[] }

// ---------------------------------------------------------------------------
// JobStatusBanner
// ---------------------------------------------------------------------------

function JobStatusBanner({ jobId, onDone }: { jobId: string | null; onDone?: () => void }) {
  const job = useJob(jobId)

  if (!jobId) return null

  if (job.isLoading) return (
    <div className="alert alert-info" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <Icon name="sync" size={16} style={{ animation: 'spin 1s linear infinite' }} />
      Starting job…
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
            Replication running… 
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
              <div style={{ 
                width: `${progress.percent ?? 0}%`, 
                height: '100%', 
                background: 'var(--primary)', 
                transition: 'width 0.5s ease-out' 
              }} />
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
  const remainingMins = mins % 60
  return `${hrs}h ${remainingMins}m`
}

// ---------------------------------------------------------------------------
// ReplicateForm
// ---------------------------------------------------------------------------

function ReplicateForm({ datasets }: { datasets: ZFSDataset[] }) {
  const [dataset, setDataset] = useState(datasets[0]?.name ?? '')
  const [targetHost, setTargetHost] = useState('')
  const [targetUser, setTargetUser] = useState('root')
  const [targetPort, setTargetPort] = useState('22')
  const [targetDataset, setTargetDataset] = useState('')
  const [incremental, setIncremental] = useState(false)
  const [compress, setCompress] = useState(true)
  const [jobId, setJobId] = useState<string | null>(null)

  function clearJob() { setJobId(null) }

  const testMutation = useMutation({
    mutationFn: () => api.post<TestResult>('/api/replication/test', {
      host: targetHost, user: targetUser, port: parseInt(targetPort) || 22,
    }),
    onSuccess: data => {
      if (data.success) toast.success(`Connection OK${data.latency_ms != null ? ` (${data.latency_ms}ms)` : ''}`)
      else toast.error(`Connection failed: ${data.error}`)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const sendMutation = useMutation({
    mutationFn: () => api.post<JobStartResponse>(
      incremental ? '/api/replication/send-incremental' : '/api/replication/send',
      {
        dataset, target_host: targetHost, target_user: targetUser,
        target_port: parseInt(targetPort) || 22,
        target_dataset: targetDataset || dataset,
        compress,
      },
    ),
    onSuccess: data => { setJobId(data.job_id) },
    onError: (e: Error) => toast.error(e.message),
  })

  function start() {
    if (!dataset) { toast.error('Select a source dataset'); return }
    if (!targetHost.trim()) { toast.error('Target host required'); return }
    clearJob()
    sendMutation.mutate()
  }

  const isRunning = sendMutation.isPending

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 28 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 24 }}>
        <Icon name="sync_alt" size={28} style={{ color: 'var(--primary)' }} />
        <div>
          <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>Replicate Dataset</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>ZFS send → receive to remote host via SSH</div>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Source Dataset</span>
          <select value={dataset} onChange={e => setDataset(e.target.value)} className="input">
            {datasets.map(d => <option key={d.name} value={d.name}>{d.name}</option>)}
          </select>
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Target Dataset (optional)</span>
          <input value={targetDataset} onChange={e => setTargetDataset(e.target.value)}
            placeholder="Leave empty to match source" className="input" />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
          <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Target Host</span>
          <input value={targetHost} onChange={e => setTargetHost(e.target.value)}
            placeholder="192.168.1.50 or nas.local" className="input" />
        </label>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 80px', gap: 8 }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>SSH User</span>
            <input value={targetUser} onChange={e => setTargetUser(e.target.value)} className="input" />
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Port</span>
            <input value={targetPort} onChange={e => setTargetPort(e.target.value)} className="input" />
          </label>
        </div>

        <div style={{ gridColumn: '1 / -1', display: 'flex', gap: 24 }}>
          <CheckRow label="Incremental (send only changes)" checked={incremental} onChange={setIncremental} />
          <CheckRow label="Compress (lz4)" checked={compress} onChange={setCompress} />
        </div>
      </div>

      {jobId && (
        <div style={{ marginTop: 16 }}>
          <JobStatusBanner jobId={jobId} onDone={clearJob} />
        </div>
      )}

      <div style={{ display: 'flex', gap: 8, marginTop: 20 }}>
        <button onClick={() => testMutation.mutate()} disabled={testMutation.isPending || !targetHost} className="btn btn-ghost">
          <Icon name="wifi" size={16} />{testMutation.isPending ? 'Testing…' : 'Test Connection'}
        </button>
        <button onClick={start} disabled={isRunning} className="btn btn-primary">
          <Icon name={incremental ? 'sync' : 'send'} size={17} />
          {isRunning ? 'Starting…' : incremental ? 'Send Incremental' : 'Start Replication'}
        </button>
      </div>
    </div>
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
// SSHKeyManager
// ---------------------------------------------------------------------------

function SSHKeyManager() {
  const [targetHost, setTargetHost] = useState('')
  const [targetUser, setTargetUser] = useState('root')
  const [targetPass, setTargetPass] = useState('')
  const [pubKey, setPubKey] = useState<string | null>(null)

  const keygen = useMutation({
    mutationFn: () => api.post<SSHKeyResponse>('/api/replication/ssh-keygen', {}),
    onSuccess: data => {
      if (data.success) {
        toast.success('SSH key generated')
        if (data.public_key) setPubKey(data.public_key)
      } else {
        toast.error(data.error ?? 'Keygen failed')
      }
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const getPubKey = useMutation({
    mutationFn: () => api.get<SSHKeyResponse>('/api/replication/ssh-pubkey'),
    onSuccess: data => {
      if (data.public_key) setPubKey(data.public_key)
      else toast.error('No public key found - generate one first')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const copyId = useMutation({
    mutationFn: () => api.post<{ success: boolean; error?: string }>('/api/replication/ssh-copy-id', {
      host: targetHost, user: targetUser, password: targetPass,
    }),
    onSuccess: data => {
      if (data.success) toast.success('Public key copied to target host')
      else toast.error(data.error ?? 'Copy failed')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 28 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 24 }}>
        <Icon name="key" size={28} style={{ color: 'var(--warning)' }} />
        <div>
          <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>SSH Key Management</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Generate and deploy the replication SSH key</div>
        </div>
      </div>

      <div style={{ display: 'flex', gap: 8, marginBottom: 20 }}>
        <button onClick={() => keygen.mutate()} disabled={keygen.isPending} className="btn btn-ghost">
          <Icon name="vpn_key" size={16} />{keygen.isPending ? 'Generating…' : 'Generate Key'}
        </button>
        <button onClick={() => getPubKey.mutate()} disabled={getPubKey.isPending} className="btn btn-ghost">
          <Icon name="content_copy" size={16} />Show Public Key
        </button>
      </div>

      {pubKey && (
        <div style={{ marginBottom: 20 }}>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 6 }}>Public Key (copy to target ~/.ssh/authorized_keys):</div>
          <div className="card" style={{ background: 'var(--surface)', borderRadius: 'var(--radius-sm)', padding: '10px 14px', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', wordBreak: 'break-all', color: 'var(--text-secondary)', maxHeight: 80, overflow: 'auto' }}>
            {pubKey}
          </div>
          <button onClick={() => { navigator.clipboard.writeText(pubKey); toast.success('Copied') }}
            className="btn btn-ghost" style={{ marginTop: 8, fontSize: 'var(--text-xs)' }}>
            <Icon name="content_copy" size={13} /> Copy to clipboard
          </button>
        </div>
      )}

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 20 }}>
        <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, marginBottom: 14 }}>Auto-deploy via ssh-copy-id</div>
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 100px', gap: 8, marginBottom: 8 }}>
          <input value={targetHost} onChange={e => setTargetHost(e.target.value)} placeholder="Target host" className="input" />
          <input value={targetUser} onChange={e => setTargetUser(e.target.value)} placeholder="User" className="input" />
        </div>
        <input type="password" value={targetPass} onChange={e => setTargetPass(e.target.value)}
          placeholder="Target SSH password (one-time use)" className="input" style={{ marginBottom: 10 }} />
        <button onClick={() => copyId.mutate()} disabled={copyId.isPending || !targetHost} className="btn btn-primary">
          <Icon name="upload" size={16} />{copyId.isPending ? 'Deploying…' : 'Deploy Key'}
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Status badge helper
// ---------------------------------------------------------------------------

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

const INTERVAL_LABELS: Record<ReplicationSchedule['interval'], string> = {
  hourly: 'Hourly', daily: 'Daily', weekly: 'Weekly', manual: 'Manual only',
}

function ScheduleModal({ datasets, onClose, onSaved, editingSchedule }: {
  datasets: ZFSDataset[]
  onClose: () => void
  onSaved: () => void
  editingSchedule?: ReplicationSchedule
}) {
  const [name,              setName]              = useState(editingSchedule?.name ?? '')
  const [sourceDataset,     setSourceDataset]     = useState(editingSchedule?.source_dataset ?? datasets[0]?.name ?? '')
  const [remoteHost,        setRemoteHost]        = useState(editingSchedule?.remote_host ?? '')
  const [remoteUser,        setRemoteUser]        = useState(editingSchedule?.remote_user ?? 'root')
  const [remotePort,        setRemotePort]        = useState(String(editingSchedule?.remote_port ?? '22'))
  const [remotePool,        setRemotePool]        = useState(editingSchedule?.remote_pool ?? '')
  const [sshKeyPath,        setSshKeyPath]        = useState(editingSchedule?.ssh_key_path ?? '')
  const [interval,          setInterval]          = useState<ReplicationSchedule['interval']>(editingSchedule?.interval ?? 'daily')
  const [triggerOnSnapshot, setTriggerOnSnapshot] = useState(editingSchedule?.trigger_on_snapshot ?? false)
  const [compress,          setCompress]          = useState(editingSchedule?.compress ?? true)
  const [rateLimitMB,       setRateLimitMB]       = useState(editingSchedule?.rate_limit_mb ?? 0)
  const [enabled,           setEnabled]           = useState(editingSchedule?.enabled ?? true)

  const saveMutation = useMutation({
    mutationFn: (s: Partial<ReplicationSchedule>) =>
      editingSchedule 
        ? api.put(`/api/replication/schedules/${editingSchedule.id}`, s)
        : api.post<{ success: boolean; schedule: ReplicationSchedule }>('/api/replication/schedules', s),
    onSuccess: () => { 
      toast.success(editingSchedule ? 'Replication schedule updated' : 'Replication schedule created'); 
      onSaved(); 
      onClose() 
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (!name.trim()) { toast.error('Name is required'); return }
    if (!sourceDataset) { toast.error('Source dataset is required'); return }
    if (!remoteHost.trim()) { toast.error('Remote host is required'); return }
    saveMutation.mutate({
      name,
      source_dataset:      sourceDataset,
      remote_host:         remoteHost,
      remote_user:         remoteUser || 'root',
      remote_port:         parseInt(remotePort) || 22,
      remote_pool:         remotePool || sourceDataset,
      ssh_key_path:        sshKeyPath || undefined,
      interval,
      trigger_on_snapshot: triggerOnSnapshot,
      compress,
      rate_limit_mb:       rateLimitMB,
      enabled,
    })
  }

  return (
    <Modal title={editingSchedule ? "Edit Replication Schedule" : "New Replication Schedule"} onClose={onClose}>
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

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 100px', gap: 10 }}>
          <label className="field">
            <span className="field-label">Remote Host</span>
            <input value={remoteHost} onChange={e => setRemoteHost(e.target.value)} placeholder="192.168.1.50" className="input" />
          </label>
          <label className="field">
            <span className="field-label">Port</span>
            <input value={remotePort} onChange={e => setRemotePort(e.target.value)} className="input" />
          </label>
        </div>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
          <label className="field">
            <span className="field-label">Remote User</span>
            <input value={remoteUser} onChange={e => setRemoteUser(e.target.value)} placeholder="root" className="input" />
          </label>
          <label className="field">
            <span className="field-label">Remote Pool / Dataset</span>
            <input value={remotePool} onChange={e => setRemotePool(e.target.value)} placeholder="backup/tank" className="input" />
          </label>
        </div>

        <label className="field">
          <span className="field-label">SSH Key Path <span style={{ fontWeight: 400, color: 'var(--text-tertiary)' }}>(optional)</span></span>
          <input value={sshKeyPath} onChange={e => setSshKeyPath(e.target.value)} placeholder="/root/.ssh/id_ed25519" className="input" />
        </label>

        <label className="field">
          <span className="field-label">Rate Limit MB/s <span style={{ fontWeight: 400, color: 'var(--text-tertiary)' }}>(0 = unlimited)</span></span>
          <input type="number" value={rateLimitMB} min={0}
            onChange={e => setRateLimitMB(parseInt(e.target.value) || 0)} className="input" />
        </label>

        <div style={{ display: 'flex', gap: 24 }}>
          <CheckRow label="Compress stream" checked={compress} onChange={setCompress} />
          <CheckRow label="Trigger after each snapshot" checked={triggerOnSnapshot} onChange={setTriggerOnSnapshot} />
          <CheckRow label="Enabled" checked={enabled} onChange={setEnabled} />
        </div>
      </div>

      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 24 }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} disabled={saveMutation.isPending} className="btn btn-primary">
          {saveMutation.isPending ? 'Saving…' : editingSchedule ? 'Save Changes' : 'Create Schedule'}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// SchedulesTab
// ---------------------------------------------------------------------------

function SchedulesTab({ datasets, confirm }: { datasets: ZFSDataset[]; confirm: (opts: { title: string; message?: string; confirmLabel?: string; cancelLabel?: string; danger?: boolean }) => Promise<boolean> }) {
  const qc = useQueryClient()
  const [showAdd, setShowAdd] = useState(false)
  const [editingSchedule, setEditingSchedule] = useState<ReplicationSchedule | undefined>(undefined)
  const [runningId, setRunningId] = useState<string | null>(null)
  const [runJobId, setRunJobId] = useState<string | null>(null)

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
    mutationFn: (id: string) =>
      api.post<{ success: boolean; job_id: string }>(`/api/replication/schedules/${id}/run`, {}),
    onSuccess: (data, id) => {
      toast.success('Replication job started')
      setRunJobId(data.job_id)
      setRunningId(id)
      qc.invalidateQueries({ queryKey: ['replication', 'schedules'] })
    },
    onError: (e: Error) => { toast.error(e.message); setRunningId(null) },
  })

  const schedules = schedulesQ.data?.schedules ?? []

  function formatLastRun(ts?: string) {
    if (!ts) return '-'
    try {
      return new Date(ts).toLocaleString()
    } catch {
      return ts
    }
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
                {['Name', 'Source Dataset', 'Remote', 'Interval', 'Trigger on Snap', 'Status', 'Last Run', 'Actions'].map(h => (
                  <th key={h} style={{ padding: '8px 12px', textAlign: 'left', fontWeight: 600 }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {schedules.map(s => (
                <tr key={s.id} style={{ borderBottom: '1px solid var(--border)', opacity: s.enabled ? 1 : 0.6 }}>
                  <td style={{ padding: '12px 12px', fontWeight: 600 }}>{s.name}</td>
                  <td style={{ padding: '12px 12px', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>{s.source_dataset}</td>
                  <td style={{ padding: '12px 12px', color: 'var(--text-secondary)' }}>
                    {s.remote_host
                      ? <span style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{s.remote_user}@{s.remote_host}:{s.remote_port || 22}</span>
                      : <span style={{ color: 'var(--text-tertiary)' }}>-</span>
                    }
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
                  <td style={{ padding: '12px 12px' }}>
                    <StatusBadge status={s.last_status} />
                  </td>
                  <td style={{ padding: '12px 12px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                    {formatLastRun(s.last_run)}
                  </td>
                  <td style={{ padding: '12px 12px' }}>
                    <div style={{ display: 'flex', gap: 6 }}>
                      <Tooltip content="Run Now">
                        <button
                          onClick={() => runNowMutation.mutate(s.id)}
                          disabled={runNowMutation.isPending && runningId === s.id}
                          className="btn btn-sm btn-ghost"
                        >
                          <Icon name="play_arrow" size={14} />
                          {runNowMutation.isPending && runningId === s.id ? 'Starting…' : 'Run'}
                        </button>
                      </Tooltip>
                      <Tooltip content="Edit">
                        <button
                          onClick={() => setEditingSchedule(s)}
                          className="btn btn-sm btn-ghost"
                        >
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
              ))}
            </tbody>
          </table>
        </div>
      )}

      {showAdd && (
        <ScheduleModal
          datasets={datasets}
          onClose={() => setShowAdd(false)}
          onSaved={() => qc.invalidateQueries({ queryKey: ['replication', 'schedules'] })}
        />
      )}

      {editingSchedule && (
        <ScheduleModal
          datasets={datasets}
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

type Tab = 'replicate' | 'ssh' | 'schedules'

export function ReplicationPage() {
  const [tab, setTab] = useState<Tab>('replicate')
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()

  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets'],
    queryFn: ({ signal }) => api.get<DatasetsResponse>('/api/zfs/datasets', signal),
  })

  const datasets = datasetsQ.data?.data ?? []

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'replicate',  label: 'Replicate',  icon: 'sync_alt' },
    { id: 'schedules',  label: 'Schedules',  icon: 'schedule' },
    { id: 'ssh',        label: 'SSH Keys',   icon: 'key' },
  ]

  return (
    <div style={{ maxWidth: 1100 }}>
      <div className="page-header">
        <h1 className="page-title">Replication</h1>
        <p className="page-subtitle">ZFS send/receive - replicate datasets to remote hosts</p>
      </div>

      {/* Info banner */}
      <div className="alert alert-info" style={{ marginBottom: 24, display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'flex-start' }}>
        <Icon name="info" size={16} style={{ flexShrink: 0, marginTop: 1 }} />
        <span>
          Replication jobs run asynchronously. Long transfers may take hours - job status updates every 2s.
          {' '}For exporting a zvol as a network NVMe disk (alternative to send/receive), use{' '}
          <a href="/nvme-of" style={{ color: 'var(--primary)', fontWeight: 600 }}>NVMe-oF</a>.
        </span>
      </div>

      {/* Tabs */}
      <div className="tabs-underline" style={{ marginBottom: 28 }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'replicate' && (
        <>
          {datasetsQ.isLoading && <Skeleton height={300} style={{ borderRadius: 'var(--radius-xl)' }} />}
          {datasetsQ.isError && <ErrorState error={datasetsQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['zfs', 'datasets'] })} />}
          {!datasetsQ.isLoading && !datasetsQ.isError && (
            <ReplicateForm datasets={datasets} />
          )}
        </>
      )}

      {tab === 'schedules' && (
        <>
          {datasetsQ.isLoading && <Skeleton height={200} style={{ borderRadius: 'var(--radius-xl)' }} />}
          {!datasetsQ.isLoading && (
            <SchedulesTab datasets={datasets} confirm={confirm} />
          )}
        </>
      )}

       {tab === 'ssh' && <SSHKeyManager />}
       <ConfirmDialog />
     </div>
   );
}

