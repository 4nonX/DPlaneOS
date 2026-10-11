/**
 * SystemUpdatePanel - updates of an installed system (Updates page).
 *
 * An update is a signed file per release. Installing it adds the new version
 * next to the running one; it starts at the next restart, on trial, and the
 * system returns to the previous version by itself if it is not healthy.
 *
 *   GET  /api/system/update/status   → versions, update on trial, last result, generations
 *   POST /api/system/update/check    → { available, latest_version, notes, size, message }
 *   POST /api/system/update/install  → { job_id }  (download + install)
 *   POST /api/system/update/upload   (multipart: file) → { job_id }
 *   POST /api/system/update/revert   → back to the previous version
 *   POST /api/system/reboot
 *   GET  /api/jobs/{id}
 */
import { useEffect, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk, getCsrfToken, getSessionId, getUsername } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'

interface Generation { generation: number; version: string; installed: boolean; running: boolean; date: number }

interface CheckResult {
  success?: boolean
  error?: string
  available: boolean
  installed_version?: string
  latest_version?: string
  notes?: string
  release_url?: string
  size?: number
  message?: string
  checked_at?: string
}

export interface UpdateStatus {
  success: boolean
  supported: boolean
  daemon_version?: string
  arch?: string
  running_version?: string
  installed_version?: string
  pending?: { version: string; installed_at: string; running: boolean } | null
  last_result?: { outcome: 'installed' | 'reverted' | 'failed'; version: string; message: string; at: string } | null
  generations?: Generation[]
  can_verify?: boolean
  installing?: boolean
  last_check?: CheckResult | null
}

interface JobSnapshot {
  status: 'running' | 'done' | 'failed' | string
  error?: string
  logs?: string[]
  result?: { version?: string; message?: string }
  progress?: { phase?: string; bytes?: number; total?: number }
}

function fmtSize(n?: number): string {
  if (!n || n <= 0) return ''
  return n >= 1 << 30 ? `${(n / (1 << 30)).toFixed(1)} GB` : `${Math.max(1, Math.round(n / (1 << 20)))} MB`
}

function fmtTime(s?: string | number): string {
  if (!s) return ''
  const d = typeof s === 'number' ? new Date(s * 1000) : new Date(s)
  return isNaN(d.getTime()) ? '' : d.toLocaleString()
}

export function SystemUpdatePanel({ status }: { status: UpdateStatus }) {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const fileInput = useRef<HTMLInputElement>(null)
  const [jobId, setJobId] = useState<string | null>(null)
  const [check, setCheck] = useState<CheckResult | null>(status.last_check ?? null)
  const [uploading, setUploading] = useState(false)
  const [restarting, setRestarting] = useState(false)

  const refresh = () => qc.invalidateQueries({ queryKey: ['system', 'update', 'status'] })

  const jobQ = useQuery({
    queryKey: ['jobs', jobId],
    queryFn: ({ signal }) => api.get<JobSnapshot>(`/api/jobs/${jobId}`, signal),
    enabled: !!jobId,
    refetchInterval: q => (q.state.data?.status === 'running' || q.state.data === undefined ? 1500 : false),
  })
  const job = jobQ.data
  const jobRunning = !!jobId && (!job || job.status === 'running')

  useEffect(() => {
    if (!job || job.status === 'running') return
    if (job.status === 'done') toast.success(job.result?.message ?? 'Update installed')
    else toast.error(job.error ?? 'The update was not installed')
    qc.invalidateQueries({ queryKey: ['system', 'update', 'status'] })
  }, [job?.status]) // eslint-disable-line react-hooks/exhaustive-deps

  const checkM = useMutation({
    mutationFn: async () => ensureOk(await api.post<CheckResult>('/api/system/update/check', {})),
    onSuccess: res => { setCheck(res); if (!res.available) toast.success(res.message ?? 'This system is up to date.') },
    onError: (e: Error) => toast.error(e.message),
  })

  const installM = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; job_id: string }>('/api/system/update/install', {})),
    onSuccess: res => setJobId(res.job_id),
    onError: (e: Error) => toast.error(e.message),
  })

  const revertM = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; message: string }>('/api/system/update/revert', {})),
    onSuccess: res => { toast.success(res.message); setRestarting(true); refresh() },
    onError: (e: Error) => toast.error(e.message),
  })

  const rebootM = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean }>('/api/system/reboot', {})),
    onSuccess: () => { setRestarting(true); toast.success('The system is restarting') },
    onError: (e: Error) => toast.error(e.message),
  })

  async function upload(file: File) {
    setUploading(true)
    try {
      const fd = new FormData()
      fd.append('file', file, file.name)
      const headers: Record<string, string> = { 'X-CSRF-Token': getCsrfToken() }
      const sid = getSessionId(); if (sid) headers['X-Session-ID'] = sid
      const usr = getUsername(); if (usr) headers['X-User'] = usr
      const res = await fetch('/api/system/update/upload', { method: 'POST', headers, body: fd })
      const data = await res.json().catch(() => ({}))
      if (!res.ok || !data.success) throw new Error(data.error || `Upload failed (HTTP ${res.status})`)
      setJobId(data.job_id)
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setUploading(false)
      if (fileInput.current) fileInput.current.value = ''
    }
  }

  const pending = status.pending ?? null
  const last = status.last_result ?? null
  const generations = status.generations ?? []
  const previous = [...generations].reverse().find(g => !g.installed)
  const busy = jobRunning || uploading || checkM.isPending || installM.isPending || revertM.isPending || rebootM.isPending || !!status.installing
  const progress = job?.progress
  const pct = progress?.phase === 'download' && progress.total && progress.total > 0
    ? Math.min(100, Math.round(((progress.bytes ?? 0) / progress.total) * 100)) : null

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <ConfirmDialog />

      {/* Installed version */}
      <div className="card" style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '16px 20px', borderRadius: 'var(--radius-lg)' }}>
        <Icon name="verified" size={22} style={{ color: 'var(--success)', flexShrink: 0 }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontWeight: 700 }}>D-PlaneOS {status.running_version}</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            {check?.checked_at ? `Checked for updates ${fmtTime(check.checked_at)}` : 'Not checked for updates yet'}
          </div>
        </div>
        <button className="btn btn-ghost" onClick={() => checkM.mutate()} disabled={busy || !!pending}>
          <Icon name="search" size={14} />{checkM.isPending ? 'Checking…' : 'Check for updates'}
        </button>
      </div>

      {restarting && (
        <div className="card" style={{ padding: '14px 20px', borderRadius: 'var(--radius-lg)', border: '1px solid var(--warning-border)', background: 'var(--warning-bg)' }}>
          <div style={{ fontWeight: 700 }}>The system is restarting</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>This page reconnects by itself; reload it if it does not after a few minutes.</div>
        </div>
      )}

      {status.can_verify === false && (
        <div className="card" style={{ padding: '14px 20px', borderRadius: 'var(--radius-lg)', border: '1px solid var(--error-border)' }}>
          <div style={{ fontWeight: 700, color: 'var(--error)' }}>Updates cannot be installed</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>This system has no key to check update files with (it was built without one).</div>
        </div>
      )}

      {/* An update on trial */}
      {pending && !restarting && (
        <div className="card" style={{ padding: '16px 20px', borderRadius: 'var(--radius-lg)', border: '1px solid var(--warning-border)', background: 'var(--warning-bg)' }}>
          {pending.running ? (
            <>
              <div style={{ fontWeight: 700 }}>Version {pending.version} is running on trial</div>
              <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: 4 }}>
                A few minutes after the start the system checks that everything that worked before works again. If it does, {pending.version} stays;
                if not, the system restarts into the previous version by itself.
              </div>
            </>
          ) : (
            <>
              <div style={{ fontWeight: 700 }}>Version {pending.version} is installed and starts at the next restart</div>
              <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', margin: '4px 0 12px' }}>
                Shares, containers and all other services are interrupted while the system restarts. If the new version does not come up
                or is not healthy, the system returns to {status.running_version} by itself.
              </div>
              <div style={{ display: 'flex', gap: 8 }}>
                <button className="btn btn-primary" disabled={busy}
                  onClick={async () => { if (await confirm({ title: 'Restart now?', message: `The system restarts into version ${pending.version}. All services are interrupted for a few minutes.`, confirmLabel: 'Restart' })) rebootM.mutate() }}>
                  <Icon name="restart_alt" size={15} />Restart now
                </button>
                <button className="btn btn-ghost" disabled={busy} onClick={() => revertM.mutate()}>Cancel the update</button>
              </div>
            </>
          )}
        </div>
      )}

      {/* The outcome of the last update */}
      {last && !pending && (
        <div className="card" style={{ padding: '14px 20px', borderRadius: 'var(--radius-lg)', border: `1px solid ${last.outcome === 'installed' ? 'var(--success-border)' : 'var(--error-border)'}` }}>
          <div style={{ fontWeight: 700, color: last.outcome === 'installed' ? 'var(--success)' : 'var(--error)' }}>
            {last.outcome === 'installed' ? `Updated to ${last.version}` : `The update to ${last.version} was not kept`}
            <span style={{ marginLeft: 8, fontWeight: 400, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{fmtTime(last.at)}</span>
          </div>
          {last.outcome !== 'installed' && <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: 4 }}>{last.message}</div>}
        </div>
      )}

      {/* An available update */}
      {check?.available && !pending && (
        <div className="card" style={{ padding: '16px 20px', borderRadius: 'var(--radius-lg)', border: '1px solid var(--primary)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: check.notes ? 12 : 0 }}>
            <Icon name="new_releases" size={22} style={{ color: 'var(--primary)', flexShrink: 0 }} />
            <div style={{ flex: 1 }}>
              <div style={{ fontWeight: 700 }}>Version {check.latest_version} is available</div>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                {[fmtSize(check.size) && `${fmtSize(check.size)} download, plus what the public NixOS cache provides`].filter(Boolean).join(' ')}
              </div>
            </div>
            {check.release_url && (
              <a href={check.release_url} target="_blank" rel="noreferrer" className="btn btn-ghost"><Icon name="open_in_new" size={13} />Release page</a>
            )}
            <button className="btn btn-primary" disabled={busy || status.can_verify === false} onClick={() => installM.mutate()}>
              <Icon name="download" size={15} />Install
            </button>
          </div>
          {check.notes && (
            <pre style={{ margin: 0, padding: 12, maxHeight: 240, overflow: 'auto', background: 'var(--surface)', borderRadius: 'var(--radius-md)', fontSize: 'var(--text-xs)', whiteSpace: 'pre-wrap' }}>{check.notes}</pre>
          )}
        </div>
      )}

      {/* Installation in progress */}
      {jobId && job && (
        <div className="card" style={{ padding: '16px 20px', borderRadius: 'var(--radius-lg)' }}>
          <div style={{ fontWeight: 700, marginBottom: 8 }}>
            {job.status === 'running' ? (pct !== null ? `Downloading… ${pct}%` : 'Installing…') : job.status === 'done' ? 'Installed' : 'Not installed'}
          </div>
          {job.status === 'running' && (
            <div style={{ width: '100%', height: 8, background: 'var(--surface)', borderRadius: 4, overflow: 'hidden', marginBottom: 8 }}>
              <div style={{ width: `${pct ?? 100}%`, height: '100%', background: 'var(--primary)', opacity: pct === null ? 0.4 : 1, transition: 'width 0.4s ease' }} />
            </div>
          )}
          {job.status === 'failed' && <div style={{ color: 'var(--error)', fontSize: 'var(--text-sm)' }}>{job.error}</div>}
          {(job.logs ?? []).length > 0 && (
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{(job.logs ?? [])[(job.logs ?? []).length - 1]}</div>
          )}
        </div>
      )}

      {/* Installed versions */}
      <div className="card" style={{ borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
        <div style={{ display: 'flex', alignItems: 'center', padding: '14px 20px', borderBottom: '1px solid var(--border)' }}>
          <div style={{ flex: 1, fontWeight: 700 }}>Installed versions</div>
          <input ref={fileInput} type="file" accept=".update" style={{ display: 'none' }}
            onChange={e => { const f = e.target.files?.[0]; if (f) upload(f) }} />
          <button className="btn btn-ghost" disabled={busy || !!pending || status.can_verify === false} onClick={() => fileInput.current?.click()}
            title="Install an update file (dplaneos-v…​.update) downloaded from the release page">
            <Icon name="upload_file" size={14} />{uploading ? 'Uploading…' : 'Install from file'}
          </button>
          <button className="btn btn-ghost" disabled={busy || !!pending || !previous}
            title={previous ? `Return to version ${previous.version}` : 'No previous version is installed'}
            onClick={async () => { if (previous && await confirm({ title: `Return to version ${previous.version}?`, message: 'The system restarts into the previous version. All services are interrupted for a few minutes. Settings and data stay as they are.', danger: true, confirmLabel: 'Return and restart' })) revertM.mutate() }}>
            <Icon name="history" size={14} />Return to previous version
          </button>
        </div>
        <table className="data-table">
          <thead><tr>{['Version', 'Installed on', ''].map(h => <th key={h}>{h}</th>)}</tr></thead>
          <tbody>
            {[...generations].reverse().map(g => (
              <tr key={g.generation}>
                <td style={{ fontWeight: 600 }}>{g.version}</td>
                <td style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{fmtTime(g.date)}</td>
                <td>
                  {g.running && <span className="badge badge-success" style={{ marginRight: 6 }}>Running</span>}
                  {g.installed && !g.running && <span className="badge badge-warning">Starts at the next restart</span>}
                  {!g.installed && !g.running && <span className="badge badge-neutral">Previous</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <div style={{ padding: '10px 20px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
          The previous version stays installed so that the system can return to it. Update files are signed; a file that is not from the
          D-PlaneOS release process is refused.
        </div>
      </div>
    </div>
  )
}

/** Loads the update status; renders the panel when this system can update itself. */
export function useSystemUpdateStatus() {
  return useQuery({
    queryKey: ['system', 'update', 'status'],
    queryFn: ({ signal }) => api.get<UpdateStatus>('/api/system/update/status', signal),
    refetchInterval: 15_000,
  })
}

export function SystemUpdateSection() {
  const statusQ = useSystemUpdateStatus()
  if (statusQ.isLoading) return <Skeleton height={200} />
  if (statusQ.isError || !statusQ.data) return <ErrorState error={statusQ.error} onRetry={() => statusQ.refetch()} />
  if (!statusQ.data.supported) return null
  return <SystemUpdatePanel status={statusQ.data} />
}
