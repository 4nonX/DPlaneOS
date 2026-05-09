/**
 * pages/BackupPage.tsx - Rsync Backup
 *
 * Tabs:
 *   Run Now   - ad-hoc rsync with job history
 *   Schedules - recurring rsync schedules (CRUD)
 *
 * API:
 *   GET    /api/backup/rsync                    → list recent ad-hoc tasks
 *   POST   /api/backup/rsync                    → enqueue ad-hoc rsync job
 *   DELETE /api/backup/rsync/{id}               → remove task entry
 *   GET    /api/backup/rsync/schedules          → list schedules
 *   POST   /api/backup/rsync/schedules          → create schedule
 *   PUT    /api/backup/rsync/schedules/{id}     → update schedule
 *   DELETE /api/backup/rsync/schedules/{id}     → delete schedule
 *   POST   /api/backup/rsync/schedules/{id}/run → run schedule immediately
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Spinner, Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { JobProgress } from '@/components/ui/JobProgress'
import { Modal } from '@/components/ui/Modal'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'
import type React from 'react'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface BackupTask {
  id: string
  source: string
  dest: string
  options: string
  status: 'running' | 'done' | 'failed'
  started_at: string
  finished_at?: string
  exit_code: number
  job_id: string
}

interface TasksResponse {
  success: boolean
  tasks: BackupTask[]
}

interface RunResponse {
  job_id: string
  task_id: string
}

interface RsyncSchedule {
  id: string
  name: string
  source: string
  destination: string
  options: string
  interval: 'hourly' | 'daily' | 'weekly' | 'monthly'
  hour: number
  day_of_week: number
  day_of_month: number
  enabled: boolean
  last_run?: string
  last_status?: string
  last_job_id?: string
}

interface SchedulesResponse {
  success: boolean
  schedules: RsyncSchedule[]
}

const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

const defaultSchedule = (): Omit<RsyncSchedule, 'id'> => ({
  name: '',
  source: '',
  destination: '',
  options: '-avz --progress',
  interval: 'daily',
  hour: 2,
  day_of_week: 0,
  day_of_month: 1,
  enabled: true,
})

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function StatusBadge({ status }: { status: string }) {
  const cfg: Record<string, { color: string; icon: string; label: string }> = {
    running: { color: 'var(--primary)',  icon: 'sync',         label: 'Running' },
    done:    { color: 'var(--success)',  icon: 'check_circle', label: 'Done'    },
    failed:  { color: 'var(--error)',    icon: 'error',        label: 'Failed'  },
  }
  const { color, icon, label } = cfg[status] ?? { color: 'var(--text-tertiary)', icon: 'help', label: status }
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 4, color, fontSize: 'var(--text-sm)', fontWeight: 600 }}>
      <Icon name={icon} size={14} style={{ animation: status === 'running' ? 'spin 1.5s linear infinite' : 'none' }} />
      {label}
    </span>
  )
}

function elapsed(started: string, finished?: string): string {
  const ms = (finished ? new Date(finished).getTime() : Date.now()) - new Date(started).getTime()
  const sec = Math.round(ms / 1000)
  if (sec < 60) return `${sec}s`
  if (sec < 3600) return `${Math.floor(sec / 60)}m ${sec % 60}s`
  return `${Math.floor(sec / 3600)}h ${Math.floor((sec % 3600) / 60)}m`
}

function intervalLabel(s: RsyncSchedule): string {
  switch (s.interval) {
    case 'hourly':  return 'Every hour'
    case 'daily':   return `Daily at ${String(s.hour).padStart(2, '0')}:00`
    case 'weekly':  return `${DAYS[s.day_of_week]} at ${String(s.hour).padStart(2, '0')}:00`
    case 'monthly': return `Day ${s.day_of_month} at ${String(s.hour).padStart(2, '0')}:00`
  }
}

// ---------------------------------------------------------------------------
// Schedule Modal (create / edit)
// ---------------------------------------------------------------------------

interface ScheduleModalProps {
  initial?: RsyncSchedule
  onClose: () => void
  onSave: (data: Omit<RsyncSchedule, 'id'>) => void
  saving: boolean
}

function ScheduleModal({ initial, onClose, onSave, saving }: ScheduleModalProps) {
  const [form, setForm] = useState<Omit<RsyncSchedule, 'id'>>(
    initial
      ? { name: initial.name, source: initial.source, destination: initial.destination,
          options: initial.options, interval: initial.interval, hour: initial.hour,
          day_of_week: initial.day_of_week, day_of_month: initial.day_of_month, enabled: initial.enabled }
      : defaultSchedule()
  )

  function set<K extends keyof typeof form>(k: K, v: typeof form[K]) {
    setForm(f => ({ ...f, [k]: v }))
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!form.source.trim() || !form.destination.trim()) {
      toast.error('Source and destination are required')
      return
    }
    onSave(form)
  }

  return (
    <Modal title={initial ? 'Edit Schedule' : 'New Schedule'} onClose={onClose}>
      <form onSubmit={handleSubmit}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Name</label>
            <input className="input" type="text" placeholder="e.g. Tank nightly backup"
              value={form.name} onChange={e => set('name', e.target.value)} disabled={saving} />
          </div>

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Source</label>
              <input className="input" type="text" placeholder="/mnt/tank/data"
                value={form.source} onChange={e => set('source', e.target.value)} disabled={saving}
                style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }} />
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Destination</label>
              <input className="input" type="text" placeholder="/mnt/backup  or  user@host:/path"
                value={form.destination} onChange={e => set('destination', e.target.value)} disabled={saving}
                style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }} />
            </div>
          </div>

          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Rsync options</label>
            <input className="input" type="text"
              value={form.options} onChange={e => set('options', e.target.value)} disabled={saving}
              style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }} />
          </div>

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Interval</label>
              <select className="input" value={form.interval}
                onChange={e => set('interval', e.target.value as RsyncSchedule['interval'])} disabled={saving}>
                <option value="hourly">Hourly</option>
                <option value="daily">Daily</option>
                <option value="weekly">Weekly</option>
                <option value="monthly">Monthly</option>
              </select>
            </div>

            {form.interval !== 'hourly' && (
              <div>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Hour (0-23)</label>
                <input className="input" type="number" min={0} max={23}
                  value={form.hour} onChange={e => set('hour', Number(e.target.value))} disabled={saving} />
              </div>
            )}
          </div>

          {form.interval === 'weekly' && (
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Day of week</label>
              <select className="input" value={form.day_of_week}
                onChange={e => set('day_of_week', Number(e.target.value))} disabled={saving}>
                {DAYS.map((d, i) => <option key={i} value={i}>{d}</option>)}
              </select>
            </div>
          )}

          {form.interval === 'monthly' && (
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Day of month (1-28)</label>
              <input className="input" type="number" min={1} max={28}
                value={form.day_of_month} onChange={e => set('day_of_month', Number(e.target.value))} disabled={saving} />
            </div>
          )}

          <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer', userSelect: 'none' }}>
            <input type="checkbox" checked={form.enabled} disabled={saving}
              onChange={e => set('enabled', e.target.checked)} />
            <span style={{ fontSize: 'var(--text-sm)', fontWeight: 500 }}>Enabled</span>
          </label>
        </div>

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 10, marginTop: 24 }}>
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={saving}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={saving}>
            {saving ? (
              <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Spinner size={14} color="rgba(0,0,0,0.7)" /> Saving…</span>
            ) : (
              initial ? 'Save Changes' : 'Create Schedule'
            )}
          </button>
        </div>
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// Run Now tab
// ---------------------------------------------------------------------------

function RunNowTab() {
  const qc = useQueryClient()
  const [source,  setSource]  = useState('')
  const [dest,    setDest]    = useState('')
  const [options, setOptions] = useState('-avz --progress')
  const [runningJobId, setRunningJobId] = useState<string | null>(null)

  const { data, isLoading, error } = useQuery<TasksResponse>({
    queryKey: ['backup', 'tasks'],
    queryFn: () => api.get<TasksResponse>('/api/backup/rsync'),
    refetchInterval: 5000,
  })

  const runMut = useMutation({
    mutationFn: (body: { source: string; destination: string; options: string }) =>
      api.post<RunResponse>('/api/backup/rsync', body),
    onSuccess: (res) => {
      setRunningJobId(res.job_id)
      qc.invalidateQueries({ queryKey: ['backup', 'tasks'] })
      toast.success('Backup started')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.delete(`/api/backup/rsync/${id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['backup', 'tasks'] })
      toast.success('Task removed')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!source.trim() || !dest.trim()) {
      toast.error('Source and destination are required')
      return
    }
    runMut.mutate({ source: source.trim(), destination: dest.trim(), options: options.trim() })
  }

  const tasks = data?.tasks ?? []
  const hasRunning = tasks.some((t) => t.status === 'running')

  return (
    <>
      <div className="card" style={{ padding: 24, marginBottom: 24 }}>
        <h2 style={{ fontSize: 'var(--text-base)', fontWeight: 700, marginBottom: 20 }}>New Backup Job</h2>
        <form onSubmit={handleSubmit}>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16, marginBottom: 16 }}>
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Source path</label>
              <input className="input" type="text" placeholder="/mnt/tank/data"
                value={source} onChange={e => setSource(e.target.value)} disabled={runMut.isPending} />
            </div>
            <div>
              <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Destination</label>
              <input className="input" type="text" placeholder="/mnt/backup  or  user@host:/remote/path"
                value={dest} onChange={e => setDest(e.target.value)} disabled={runMut.isPending} />
            </div>
          </div>

          <div style={{ marginBottom: 20 }}>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Rsync options</label>
            <input className="input" type="text" value={options}
              onChange={e => setOptions(e.target.value)} disabled={runMut.isPending}
              style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }} />
            <p style={{ marginTop: 6, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              Common flags: <code>-avz</code> archive + compress, <code>--delete</code> mirror source, <code>--exclude=*.tmp</code> skip patterns
            </p>
          </div>

          {runningJobId && (
            <div style={{ marginBottom: 16 }}>
              <JobProgress
                jobId={runningJobId}
                runningLabel="Rsync backup running…"
                doneLabel="Backup complete"
                onDone={() => {
                  setRunningJobId(null)
                  qc.invalidateQueries({ queryKey: ['backup', 'tasks'] })
                }}
                onFailed={() => {
                  setRunningJobId(null)
                  qc.invalidateQueries({ queryKey: ['backup', 'tasks'] })
                }}
              />
            </div>
          )}

          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <button type="submit" className="btn btn-primary" disabled={runMut.isPending || hasRunning}>
              {runMut.isPending ? (
                <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Spinner size={14} color="rgba(0,0,0,0.7)" /> Starting…</span>
              ) : (
                <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Icon name="play_arrow" size={16} /> Run Backup</span>
              )}
            </button>
            {hasRunning && (
              <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>A backup is already running</span>
            )}
          </div>
        </form>
      </div>

      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <h2 style={{ fontSize: 'var(--text-base)', fontWeight: 700, margin: 0 }}>Recent Jobs</h2>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Last 50 entries</span>
        </div>

        {isLoading ? (
          <div style={{ padding: 24 }}>
            {[0, 1, 2].map((i) => <Skeleton key={i} style={{ height: 40, marginBottom: 8 }} />)}
          </div>
        ) : error ? (
          <ErrorState error={error} title="Failed to load backup tasks" />
        ) : tasks.length === 0 ? (
          <div style={{ padding: '60px 24px', textAlign: 'center', color: 'var(--text-tertiary)' }}>
            <Icon name="backup" size={40} style={{ opacity: 0.2, display: 'block', margin: '0 auto 12px' }} />
            <div style={{ fontSize: 'var(--text-sm)' }}>No backup jobs yet. Run your first backup above.</div>
          </div>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>Status</th>
                <th>Source</th>
                <th>Destination</th>
                <th>Started</th>
                <th>Duration</th>
                <th style={{ textAlign: 'right' }}></th>
              </tr>
            </thead>
            <tbody>
              {tasks.map((task) => (
                <tr key={task.id}>
                  <td><StatusBadge status={task.status} /></td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 13, maxWidth: 240, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {task.source}
                  </td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 13, maxWidth: 240, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {task.dest}
                  </td>
                  <td style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                    {new Date(task.started_at).toLocaleString()}
                  </td>
                  <td style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                    {elapsed(task.started_at, task.finished_at)}
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    <button className="btn btn-ghost" style={{ padding: '4px 8px', color: 'var(--error)' }}
                      disabled={task.status === 'running' || deleteMut.isPending}
                      onClick={() => deleteMut.mutate(task.id)} title="Remove task entry">
                      <Icon name="delete" size={14} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  )
}

// ---------------------------------------------------------------------------
// Schedules tab
// ---------------------------------------------------------------------------

function SchedulesTab() {
  const qc = useQueryClient()
  const [showModal, setShowModal] = useState(false)
  const [editing, setEditing]     = useState<RsyncSchedule | null>(null)
  const [runningJobId, setRunningJobId] = useState<string | null>(null)
  const { confirm, ConfirmDialog } = useConfirm()

  const { data, isLoading, error, refetch } = useQuery<SchedulesResponse>({
    queryKey: ['backup', 'schedules'],
    queryFn: () => api.get<SchedulesResponse>('/api/backup/rsync/schedules'),
  })

  const createMut = useMutation({
    mutationFn: (body: Omit<RsyncSchedule, 'id'>) =>
      api.post<{ success: boolean; schedule: RsyncSchedule }>('/api/backup/rsync/schedules', body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['backup', 'schedules'] })
      setShowModal(false)
      toast.success('Schedule created')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const updateMut = useMutation({
    mutationFn: ({ id, ...body }: RsyncSchedule) =>
      api.put<{ success: boolean }>(`/api/backup/rsync/schedules/${id}`, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['backup', 'schedules'] })
      setEditing(null)
      toast.success('Schedule updated')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const deleteMut = useMutation({
    mutationFn: (id: string) => api.delete(`/api/backup/rsync/schedules/${id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['backup', 'schedules'] })
      toast.success('Schedule deleted')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  async function handleDelete(s: RsyncSchedule) {
    const ok = await confirm({
      title: 'Delete schedule?',
      message: `"${s.name || s.source}" will be removed and its systemd timer uninstalled. No further automatic backups will run.`,
      danger: true,
      confirmLabel: 'Delete',
    })
    if (ok) deleteMut.mutate(s.id)
  }

  const runNowMut = useMutation({
    mutationFn: (id: string) =>
      api.post<{ success: boolean; job_id: string }>(`/api/backup/rsync/schedules/${id}/run`, {}),
    onSuccess: (res) => {
      setRunningJobId(res.job_id)
      toast.success('Backup started')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const toggleMut = useMutation({
    mutationFn: (s: RsyncSchedule) =>
      api.put<{ success: boolean }>(`/api/backup/rsync/schedules/${s.id}`, { ...s, enabled: !s.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backup', 'schedules'] }),
    onError: (e: Error) => toast.error(e.message),
  })

  const schedules = data?.schedules ?? []

  return (
    <>
      {runningJobId && (
        <div className="card" style={{ padding: 20, marginBottom: 24 }}>
          <JobProgress
            jobId={runningJobId}
            runningLabel="Rsync backup running…"
            doneLabel="Backup complete"
            onDone={() => {
              setRunningJobId(null)
              qc.invalidateQueries({ queryKey: ['backup', 'schedules'] })
            }}
            onFailed={() => {
              setRunningJobId(null)
              qc.invalidateQueries({ queryKey: ['backup', 'schedules'] })
            }}
          />
        </div>
      )}

      <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
        <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
          <h2 style={{ fontSize: 'var(--text-base)', fontWeight: 700, margin: 0 }}>Recurring Schedules</h2>
          <button className="btn btn-primary btn-sm" onClick={() => setShowModal(true)}>
            <span style={{ display: 'flex', alignItems: 'center', gap: 6 }}><Icon name="add" size={15} /> New Schedule</span>
          </button>
        </div>

        {isLoading ? (
          <div style={{ padding: 24 }}>
            {[0, 1, 2].map(i => <Skeleton key={i} style={{ height: 40, marginBottom: 8 }} />)}
          </div>
        ) : error ? (
          <ErrorState error={error} title="Failed to load schedules" onRetry={() => refetch()} />
        ) : schedules.length === 0 ? (
          <div className="empty-state" style={{ padding: '60px 24px' }}>
            <Icon name="schedule" size={48} className="empty-state-icon" />
            <h3 className="empty-state-title">No schedules yet</h3>
            <p className="empty-state-body">Create a recurring backup schedule to run rsync automatically.</p>
          </div>
        ) : (
          <table className="data-table">
            <thead>
              <tr>
                <th>Enabled</th>
                <th>Name</th>
                <th>Source</th>
                <th>Destination</th>
                <th>Schedule</th>
                <th>Last Run</th>
                <th>Last Status</th>
                <th style={{ textAlign: 'right' }}>Actions</th>
              </tr>
            </thead>
            <tbody>
              {schedules.map(s => (
                <tr key={s.id} style={{ opacity: s.enabled ? 1 : 0.55 }}>
                  <td>
                    <button
                      className="btn btn-ghost btn-xs"
                      style={{ padding: '2px 6px', color: s.enabled ? 'var(--success)' : 'var(--text-tertiary)' }}
                      onClick={() => toggleMut.mutate(s)}
                      disabled={toggleMut.isPending}
                      title={s.enabled ? 'Disable' : 'Enable'}
                    >
                      <Icon name={s.enabled ? 'toggle_on' : 'toggle_off'} size={20} />
                    </button>
                  </td>
                  <td style={{ fontWeight: 500 }}>{s.name || s.source}</td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 13, maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {s.source}
                  </td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 13, maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {s.destination}
                  </td>
                  <td style={{ fontSize: 'var(--text-sm)', whiteSpace: 'nowrap' }}>{intervalLabel(s)}</td>
                  <td style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                    {s.last_run ? new Date(s.last_run).toLocaleString() : 'Never'}
                  </td>
                  <td>
                    {s.last_status ? <StatusBadge status={s.last_status} /> : (
                      <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>-</span>
                    )}
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 4 }}>
                      <button
                        className="btn btn-ghost btn-xs"
                        style={{ padding: '4px 8px' }}
                        disabled={runNowMut.isPending}
                        onClick={() => runNowMut.mutate(s.id)}
                        title="Run now"
                      >
                        <Icon name="play_arrow" size={14} />
                      </button>
                      <button
                        className="btn btn-ghost btn-xs"
                        style={{ padding: '4px 8px' }}
                        onClick={() => setEditing(s)}
                        title="Edit"
                      >
                        <Icon name="edit" size={14} />
                      </button>
                      <button
                        className="btn btn-ghost btn-xs"
                        style={{ padding: '4px 8px', color: 'var(--error)' }}
                        onClick={() => handleDelete(s)}
                        disabled={deleteMut.isPending}
                        title="Delete"
                      >
                        <Icon name="delete" size={14} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {showModal && (
        <ScheduleModal
          onClose={() => setShowModal(false)}
          onSave={(data) => createMut.mutate(data)}
          saving={createMut.isPending}
        />
      )}

      {editing && (
        <ScheduleModal
          initial={editing}
          onClose={() => setEditing(null)}
          onSave={(data) => updateMut.mutate({ id: editing.id, ...data })}
          saving={updateMut.isPending}
        />
      )}

      <ConfirmDialog />
    </>
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function BackupPage() {
  const [tab, setTab] = useState<'run' | 'schedules'>('run')

  return (
    <div className="page-container">
      <header className="page-header">
        <div>
          <h1 className="page-title">Backup</h1>
          <p className="page-subtitle">Rsync-based backup to local paths, mounted shares, or remote SSH destinations.</p>
        </div>
      </header>

      <div className="tabs" style={{ marginBottom: 24 }}>
        <button className={`tab ${tab === 'run' ? 'tab-active' : ''}`} onClick={() => setTab('run')}>
          <Icon name="play_arrow" size={15} /> Run Now
        </button>
        <button className={`tab ${tab === 'schedules' ? 'tab-active' : ''}`} onClick={() => setTab('schedules')}>
          <Icon name="schedule" size={15} /> Schedules
        </button>
      </div>

      {tab === 'run'       && <RunNowTab />}
      {tab === 'schedules' && <SchedulesTab />}
    </div>
  )
}
