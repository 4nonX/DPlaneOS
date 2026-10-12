/**
 * Cloud sync tasks: a folder of this system and a folder inside a remote,
 * copied one way, by hand or on a schedule.
 *
 *   GET    /api/cloud-sync/tasks            → { tasks: Task[] }
 *   POST   /api/cloud-sync/tasks            → create
 *   PUT    /api/cloud-sync/tasks/{id}       → update
 *   DELETE /api/cloud-sync/tasks/{id}
 *   POST   /api/cloud-sync/tasks/{id}/run   → { dry_run? } → { job_id }
 *   GET    /api/cloud-sync/jobs             → the 20 most recent transfers
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { fmtDateTime } from '@/lib/fmt'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'

interface Task {
  id: string
  name: string
  remote: string
  remote_path: string
  local_path: string
  direction: 'upload' | 'download'
  mode: 'copy' | 'mirror'
  interval: 'manual' | 'hourly' | 'daily' | 'weekly'
  hour: number
  day_of_week: number
  enabled: boolean
  last_run?: string
  last_status?: 'running' | 'done' | 'failed'
  last_error?: string
}

interface SyncJob { id: string; provider: string; action: string; source: string; destination: string; status: 'running' | 'done' | 'failed'; started_at: string; finished_at?: string; error?: string }

const DAYS = ['Sunday', 'Monday', 'Tuesday', 'Wednesday', 'Thursday', 'Friday', 'Saturday']

const EMPTY = { name: '', remote: '', remote_path: '', local_path: '', direction: 'upload' as Task['direction'], mode: 'copy' as Task['mode'], interval: 'daily' as Task['interval'], hour: 2, day_of_week: 0, enabled: true }

function when(t: Task) {
  const hh = `${String(t.hour).padStart(2, '0')}:00`
  if (!t.enabled && t.interval !== 'manual') return 'Paused'
  switch (t.interval) {
    case 'hourly': return 'Every hour'
    case 'daily':  return `Daily at ${hh}`
    case 'weekly': return `${DAYS[t.day_of_week]}s at ${hh}`
    default:       return 'By hand'
  }
}

export function CloudTasksTab({ remotes, preselect }: { remotes: { name: string }[]; preselect?: string }) {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [form, setForm] = useState({ ...EMPTY, remote: preselect ?? '' })
  const [editing, setEditing] = useState<string | null>(null)
  const set = <K extends keyof typeof EMPTY>(k: K, v: (typeof EMPTY)[K]) => setForm(f => ({ ...f, [k]: v }))


  const tasksQ = useQuery({
    queryKey: ['cloud', 'tasks'],
    queryFn: ({ signal }) => api.get<{ success: boolean; tasks: Task[] | null }>('/api/cloud-sync/tasks', signal),
    refetchInterval: q => (q.state.data?.tasks?.some(t => t.last_status === 'running') ? 3_000 : 30_000),
  })
  const jobsQ = useQuery({
    queryKey: ['cloud', 'jobs'],
    queryFn: ({ signal }) => api.get<{ success: boolean; jobs: SyncJob[] | null }>('/api/cloud-sync/jobs', signal),
    refetchInterval: q => (q.state.data?.jobs?.some(j => j.status === 'running') ? 3_000 : 30_000),
  })
  const tasks = tasksQ.data?.tasks ?? []
  const jobs = jobsQ.data?.jobs ?? []
  const refresh = () => qc.invalidateQueries({ queryKey: ['cloud'] })

  const save = useMutation({
    mutationFn: () => editing ? api.put(`/api/cloud-sync/tasks/${editing}`, form) : api.post('/api/cloud-sync/tasks', form),
    onSuccess: () => { toast.success(editing ? 'Task updated' : 'Task created'); setForm({ ...EMPTY }); setEditing(null); refresh() },
    onError: (e: Error) => toast.error(e.message),
  })
  const run = useMutation({
    mutationFn: ({ id, dry }: { id: string; dry: boolean }) => api.post(`/api/cloud-sync/tasks/${id}/run`, { dry_run: dry }),
    onSuccess: (_d, v) => { toast.success(v.dry ? 'Dry run started: see Recent transfers' : 'Transfer started'); refresh() },
    onError: (e: Error) => toast.error(e.message),
  })
  const del = useMutation({
    mutationFn: (id: string) => api.delete(`/api/cloud-sync/tasks/${id}`, {}),
    onSuccess: () => { toast.success('Task removed'); refresh() },
    onError: (e: Error) => toast.error(e.message),
  })

  const edit = (t: Task) => {
    setEditing(t.id)
    setForm({ name: t.name, remote: t.remote, remote_path: t.remote_path, local_path: t.local_path, direction: t.direction, mode: t.mode, interval: t.interval, hour: t.hour, day_of_week: t.day_of_week, enabled: t.enabled })
    window.scrollTo({ top: 0, behavior: 'smooth' })
  }

  const source = form.direction === 'upload' ? (form.local_path || 'the local folder') : `${form.remote || 'remote'}:${form.remote_path || '…'}`
  const dest   = form.direction === 'upload' ? `${form.remote || 'remote'}:${form.remote_path || '…'}` : (form.local_path || 'the local folder')
  const ready  = form.name.trim() && form.remote && form.remote_path.trim() && form.local_path.trim()

  const submit = async () => {
    if (form.mode === 'mirror') {
      const ok = await confirm({
        title: 'Mirror deletes at the destination',
        message: `Everything in ${dest} that is not in ${source} will be deleted there at every run${form.direction === 'download' ? ', on this system' : ''}. Use "Dry run" on the task first to see what would happen.`,
        danger: true, confirmLabel: editing ? 'Save as mirror' : 'Create as mirror',
      })
      if (!ok) return
    }
    save.mutate()
  }

  return (
    <div>
      <div className="card card-xl" style={{ marginBottom: 24, border: editing ? '1px solid var(--primary)' : undefined }}>
        <div style={{ fontWeight: 700, marginBottom: 14 }}>{editing ? 'Edit task' : 'New task'}</div>
        {remotes.length === 0 ? (
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>Add a remote first (tab Remotes).</div>
        ) : (
          <>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
              <label className="field">
                <span className="field-label">Name</span>
                <input className="input" value={form.name} onChange={e => set('name', e.target.value)} placeholder="offsite-photos" />
              </label>
              <label className="field">
                <span className="field-label">Direction</span>
                <select className="input" style={{ appearance: 'none' }} value={form.direction} onChange={e => set('direction', e.target.value as Task['direction'])}>
                  <option value="upload">Upload: this system → remote</option>
                  <option value="download">Download: remote → this system</option>
                </select>
              </label>
              <label className="field">
                <span className="field-label">Folder on this system</span>
                <input className="input" style={{ fontFamily: 'var(--font-mono)' }} value={form.local_path} onChange={e => set('local_path', e.target.value)} placeholder="/mnt/tank/photos" />
              </label>
              <div style={{ display: 'grid', gridTemplateColumns: '140px 1fr', gap: 8 }}>
                <label className="field">
                  <span className="field-label">Remote</span>
                  <select className="input" style={{ appearance: 'none' }} value={form.remote} onChange={e => set('remote', e.target.value)}>
                    <option value="">Select…</option>
                    {remotes.map(r => <option key={r.name} value={r.name}>{r.name}</option>)}
                  </select>
                </label>
                <label className="field">
                  <span className="field-label">Bucket / folder in the remote</span>
                  <input className="input" style={{ fontFamily: 'var(--font-mono)' }} value={form.remote_path} onChange={e => set('remote_path', e.target.value)} placeholder="my-bucket/nas/photos" />
                </label>
              </div>
              <label className="field">
                <span className="field-label">What happens to files that are gone at the source</span>
                <select className="input" style={{ appearance: 'none' }} value={form.mode} onChange={e => set('mode', e.target.value as Task['mode'])}>
                  <option value="copy">Copy: they stay at the destination (nothing is ever deleted)</option>
                  <option value="mirror">Mirror: they are deleted at the destination too</option>
                </select>
              </label>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 8 }}>
                <label className="field">
                  <span className="field-label">Runs</span>
                  <select className="input" style={{ appearance: 'none' }} value={form.interval} onChange={e => set('interval', e.target.value as Task['interval'])}>
                    <option value="manual">By hand</option>
                    <option value="hourly">Every hour</option>
                    <option value="daily">Daily</option>
                    <option value="weekly">Weekly</option>
                  </select>
                </label>
                {(form.interval === 'daily' || form.interval === 'weekly') && (
                  <label className="field">
                    <span className="field-label">At</span>
                    <select className="input" style={{ appearance: 'none' }} value={form.hour} onChange={e => set('hour', parseInt(e.target.value, 10))}>
                      {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{String(h).padStart(2, '0')}:00</option>)}
                    </select>
                  </label>
                )}
                {form.interval === 'weekly' && (
                  <label className="field">
                    <span className="field-label">On</span>
                    <select className="input" style={{ appearance: 'none' }} value={form.day_of_week} onChange={e => set('day_of_week', parseInt(e.target.value, 10))}>
                      {DAYS.map((d, i) => <option key={d} value={i}>{d}</option>)}
                    </select>
                  </label>
                )}
              </div>
            </div>
            {form.mode === 'mirror' && (
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--warning)', marginTop: 10 }}>
                Mirror makes <code>{dest}</code> identical to <code>{source}</code>: what is only at the destination is deleted there.
              </div>
            )}
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 10 }}>
              A task always works inside a bucket or folder of the remote, never on the remote as a whole. A run that was missed while the system was off is made up for once after the start. A failed scheduled run raises the alert <code>backup.failed</code>.
            </div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 14 }}>
              {form.interval !== 'manual' && (
                <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: 'var(--text-sm)' }}>
                  <input type="checkbox" checked={form.enabled} onChange={e => set('enabled', e.target.checked)} style={{ accentColor: 'var(--primary)' }} />
                  Schedule active
                </label>
              )}
              <div style={{ flex: 1 }} />
              {editing && <button className="btn btn-ghost" onClick={() => { setEditing(null); setForm({ ...EMPTY }) }}>Cancel</button>}
              <button className="btn btn-primary" disabled={!ready || save.isPending} onClick={submit}>
                <Icon name={editing ? 'save' : 'add'} size={14} />{save.isPending ? 'Saving…' : editing ? 'Save' : 'Create task'}
              </button>
            </div>
          </>
        )}
      </div>

      {tasksQ.isLoading && <Skeleton height={120} />}
      {tasksQ.isError && <ErrorState error={tasksQ.error} />}

      <div className="card" style={{ borderRadius: 'var(--radius-lg)', overflow: 'hidden', marginBottom: 24 }}>
        <table className="data-table">
          <thead><tr>{['Task', 'From', 'To', 'Mode', 'Runs', 'Last run', 'Actions'].map(h => <th key={h}>{h}</th>)}</tr></thead>
          <tbody>
            {tasks.map(t => {
              const remote = `${t.remote}:${t.remote_path}`
              return (
                <tr key={t.id}>
                  <td style={{ fontWeight: 600 }}>{t.name}</td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{t.direction === 'upload' ? t.local_path : remote}</td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{t.direction === 'upload' ? remote : t.local_path}</td>
                  <td style={{ fontSize: 'var(--text-xs)', color: t.mode === 'mirror' ? 'var(--warning)' : 'var(--text-secondary)' }}>{t.mode === 'mirror' ? 'Mirror (deletes)' : 'Copy'}</td>
                  <td style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>{when(t)}</td>
                  <td style={{ fontSize: 'var(--text-xs)', color: t.last_status === 'failed' ? 'var(--error)' : t.last_status === 'done' ? 'var(--success)' : 'var(--text-secondary)' }} title={t.last_error}>
                    {t.last_status === 'running' ? 'Running…'
                      : !t.last_run ? 'Never'
                      : t.last_status === 'failed' ? `Failed ${fmtDateTime(t.last_run)}: ${t.last_error ?? ''}`
                      : `Done ${fmtDateTime(t.last_run)}`}
                  </td>
                  <td>
                    <div style={{ display: 'flex', gap: 6 }}>
                      <button className="btn btn-sm btn-ghost" disabled={t.last_status === 'running' || run.isPending} onClick={() => run.mutate({ id: t.id, dry: false })}><Icon name="play_arrow" size={13} />Run</button>
                      <button className="btn btn-sm btn-ghost" disabled={t.last_status === 'running' || run.isPending} onClick={() => run.mutate({ id: t.id, dry: true })} title="Shows what a run would do, without changing anything">Dry run</button>
                      <button className="btn btn-sm btn-ghost" onClick={() => edit(t)}><Icon name="edit" size={13} />Edit</button>
                      <button className="btn btn-sm btn-danger" onClick={async () => { if (await confirm({ title: `Remove task "${t.name}"?`, message: 'The files on both sides stay as they are.', danger: true, confirmLabel: 'Remove' })) del.mutate(t.id) }}><Icon name="delete" size={13} /></button>
                    </div>
                  </td>
                </tr>
              )
            })}
            {!tasksQ.isLoading && tasks.length === 0 && (
              <tr><td colSpan={7} style={{ padding: '36px 16px', textAlign: 'center', color: 'var(--text-tertiary)' }}>No tasks yet</td></tr>
            )}
          </tbody>
        </table>
      </div>

      {jobs.length > 0 && (
        <div className="card card-xl">
          <div style={{ fontWeight: 700, marginBottom: 10 }}>Recent transfers</div>
          <div style={{ overflowX: 'auto' }}>
            <table className="table" style={{ width: '100%' }}>
              <thead><tr><th>Started</th><th>Action</th><th>From</th><th>To</th><th>State</th></tr></thead>
              <tbody>
                {jobs.map(j => (
                  <tr key={j.id}>
                    <td style={{ whiteSpace: 'nowrap' }}>{fmtDateTime(j.started_at)}</td>
                    <td>{j.action}</td>
                    <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{j.source}</td>
                    <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{j.destination}</td>
                    <td style={{ fontWeight: 600, color: j.status === 'failed' ? 'var(--error)' : j.status === 'done' ? 'var(--success)' : 'var(--primary)' }} title={j.error}>
                      {j.status === 'running' ? 'Running…' : j.status === 'done' ? `Done${j.finished_at ? ` · ${fmtDateTime(j.finished_at)}` : ''}` : `Failed${j.error ? `: ${j.error}` : ''}`}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      )}
      <ConfirmDialog />
    </div>
  )
}
