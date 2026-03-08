/**
 * pages/ReplicationPage.tsx — ZFS Replication (Phase 2)
 *
 * Calls (matching daemon routes exactly):
 *   POST /api/replication/send            → async job { job_id }
 *   POST /api/replication/send-incremental → async job { job_id }
 *   POST /api/replication/test
 *   POST /api/replication/ssh-keygen
 *   GET  /api/replication/ssh-pubkey
 *   POST /api/replication/ssh-copy-id
 *   GET  /api/zfs/datasets                (populate dataset picker)
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

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ZFSDataset { name: string; used: string; avail: string }
interface DatasetsResponse { success: boolean; data: ZFSDataset[] }

interface TestResult { success: boolean; error?: string; latency_ms?: number }
interface SSHKeyResponse { success: boolean; public_key?: string; error?: string }
interface JobStartResponse { job_id: string }

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

  if (job.interrupted) return (
    <div className="alert alert-warning" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <Icon name="warning" size={16} />
      Job interrupted — daemon may have restarted
    </div>
  )

  if (status === 'running') return (
    <div className="alert alert-info" style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
      <Icon name="sync" size={16} />
      <span style={{ flex: 1 }}>Replication running… <span style={{ color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>job:{jobId.slice(0, 8)}</span></span>
    </div>
  )

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

  // Clear job on new submission
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
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 28 }}>
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

      {/* Job status */}
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
      else toast.error('No public key found — generate one first')
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
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 28 }}>
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
          <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', padding: '10px 14px', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', wordBreak: 'break-all', color: 'var(--text-secondary)', maxHeight: 80, overflow: 'auto' }}>
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
// ReplicationPage
// ---------------------------------------------------------------------------

type Tab = 'replicate' | 'ssh'

export function ReplicationPage() {
  const [tab, setTab] = useState<Tab>('replicate')
  const qc = useQueryClient()

  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets'],
    queryFn: ({ signal }) => api.get<DatasetsResponse>('/api/zfs/datasets', signal),
  })

  const datasets = datasetsQ.data?.data ?? []

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'replicate', label: 'Replicate', icon: 'sync_alt' },
    { id: 'ssh', label: 'SSH Keys', icon: 'key' },
  ]

  return (
    <div style={{ maxWidth: 900 }}>
      <div className="page-header">
        <h1 className="page-title">Replication</h1>
        <p className="page-subtitle">ZFS send/receive — replicate datasets to remote hosts</p>
      </div>

      {/* Info banner */}
      <div className="alert alert-info" style={{ marginBottom: 24, display: 'flex', gap: 10 }}>
        <Icon name="info" size={16} style={{ flexShrink: 0, marginTop: 1 }} />
        Replication jobs run asynchronously. Long transfers may take hours — job status updates every 2s.
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

      {tab === 'ssh' && <SSHKeyManager />}
    </div>
  )
}
