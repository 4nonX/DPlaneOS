/**
 * pages/SnapshotSchedulerPage.tsx — Snapshot Scheduler (Phase 2)
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/snapshots/schedules
 *   POST /api/snapshots/schedules     (save full schedules array)
 *   POST /api/snapshots/run-now       (body: { dataset, retain })
 *   GET  /api/zfs/datasets            (populate dataset picker)
 *   GET  /api/zfs/snapshots           (list existing snapshots)
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

interface SnapshotSchedule {
  dataset:   string
  frequency: 'hourly' | 'daily' | 'weekly' | 'monthly'
  retention: number
  enabled:   boolean
  prefix?:   string
}

interface SchedulesResponse { success: boolean; schedules: SnapshotSchedule[] }

interface ZFSDataset { name: string; used: string; avail: string; mountpoint: string }
interface DatasetsResponse { success: boolean; data: ZFSDataset[] }

interface Snapshot { name: string; used: string; creation: string }
interface SnapshotsResponse { success: boolean; snapshots?: Snapshot[]; data?: Snapshot[] }

// ---------------------------------------------------------------------------

const FREQ_LABELS: Record<SnapshotSchedule['frequency'], string> = {
  hourly: 'Hourly', daily: 'Daily', weekly: 'Weekly', monthly: 'Monthly',
}
const FREQ_ICONS: Record<SnapshotSchedule['frequency'], string> = {
  hourly: 'schedule', daily: 'today', weekly: 'date_range', monthly: 'calendar_month',
}

// ---------------------------------------------------------------------------
// CreateScheduleModal
// ---------------------------------------------------------------------------

function CreateScheduleModal({ datasets, onClose, onSaved }: {
  datasets: ZFSDataset[]; onClose: () => void; onSaved: (s: SnapshotSchedule) => void
}) {
  const [dataset, setDataset] = useState(datasets[0]?.name ?? '')
  const [frequency, setFrequency] = useState<SnapshotSchedule['frequency']>('daily')
  const [retention, setRetention] = useState(7)
  const [prefix, setPrefix] = useState('auto')

  function submit() {
    if (!dataset) { toast.error('Select a dataset'); return }
    if (retention < 1) { toast.error('Retention must be ≥ 1'); return }
    onSaved({ dataset, frequency, retention, enabled: true, prefix })
    onClose()
  }

  return (
    <Modal title="New Snapshot Schedule" onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Dataset</span>
          <select value={dataset} onChange={e => setDataset(e.target.value)} className="input">
            {datasets.map(d => <option key={d.name} value={d.name}>{d.name}</option>)}
          </select>
        </label>
        <label className="field">
          <span className="field-label">Frequency</span>
          <select value={frequency} onChange={e => setFrequency(e.target.value as SnapshotSchedule['frequency'])} className="input">
            {(Object.keys(FREQ_LABELS) as SnapshotSchedule['frequency'][]).map(f => (
              <option key={f} value={f}>{FREQ_LABELS[f]}</option>
            ))}
          </select>
        </label>
        <label className="field">
          <span className="field-label">Keep (count)</span>
          <input type="number" value={retention} min={1} max={365}
            onChange={e => setRetention(parseInt(e.target.value) || 1)} className="input" />
        </label>
        <label className="field">
          <span className="field-label">Prefix</span>
          <input value={prefix} onChange={e => setPrefix(e.target.value)} placeholder="auto" className="input" />
        </label>
      </div>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 24 }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} className="btn btn-primary">Create Schedule</button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ScheduleRow
// ---------------------------------------------------------------------------

function ScheduleRow({ schedule, index: _index, onToggle, onDelete, onRunNow, running }: {
  schedule: SnapshotSchedule; index: number
  onToggle: () => void; onDelete: () => void; onRunNow: () => void; running: boolean
}) {
  const [confirming, setConfirming] = useState(false)

  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '16px 20px', display: 'flex', alignItems: 'center', gap: 16, opacity: schedule.enabled ? 1 : 0.6 }}>
      <Icon name={FREQ_ICONS[schedule.frequency]} size={24} style={{ color: 'var(--primary)', flexShrink: 0 }} />
      <div style={{ flex: 1 }}>
        <div style={{ fontWeight: 700, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', marginBottom: 4 }}>{schedule.dataset}</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
          {FREQ_LABELS[schedule.frequency]} · keep {schedule.retention}
          {schedule.prefix && ` · prefix: ${schedule.prefix}`}
        </div>
      </div>
      <span style={{ padding: '3px 10px', borderRadius: 'var(--radius-full)', background: schedule.enabled ? 'var(--success-bg)' : 'var(--surface)', border: `1px solid ${schedule.enabled ? 'var(--success-border)' : 'var(--border)'}`, color: schedule.enabled ? 'var(--success)' : 'var(--text-tertiary)', fontSize: 'var(--text-2xs)', fontWeight: 700 }}>
        {schedule.enabled ? 'ACTIVE' : 'DISABLED'}
      </span>
      <div style={{ display: 'flex', gap: 6 }}>
        <button onClick={onRunNow} disabled={running} className="btn btn-ghost" title="Run now">
          <Icon name="play_arrow" size={14} />{running ? 'Running…' : 'Run Now'}
        </button>
        <button onClick={onToggle} className="btn btn-ghost" title={schedule.enabled ? 'Disable' : 'Enable'}>
          <Icon name={schedule.enabled ? 'pause_circle' : 'play_circle'} size={14} />
        </button>
        {!confirming
          ? <button onClick={() => setConfirming(true)} className="btn btn-ghost" style={{ color: 'var(--error)', borderColor: 'var(--error-border)' }}>
              <Icon name="delete" size={14} />
            </button>
          : <>
              <button onClick={onDelete} className="btn btn-ghost" style={{ color: 'var(--error)' }}>Delete</button>
              <button onClick={() => setConfirming(false)} className="btn btn-ghost">Cancel</button>
            </>
        }
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// SnapshotsTab — recent snapshots list
// ---------------------------------------------------------------------------

function SnapshotsTab() {
  const qc = useQueryClient()
  const snapsQ = useQuery({
    queryKey: ['zfs', 'snapshots'],
    queryFn: ({ signal }) => api.get<SnapshotsResponse>('/api/zfs/snapshots', signal),
    refetchInterval: 60_000,
  })
  const snaps = snapsQ.data?.snapshots ?? snapsQ.data?.data ?? []

  if (snapsQ.isLoading) return <Skeleton height={200} />
  if (snapsQ.isError) return <ErrorState error={snapsQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['zfs', 'snapshots'] })} />

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      {snaps.length === 0 && (
        <div style={{ textAlign: 'center', padding: '48px 0', color: 'var(--text-tertiary)' }}>
          <Icon name="camera_alt" size={40} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          No snapshots found
        </div>
      )}
      {snaps.map(s => (
        <div key={s.name} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', padding: '10px 16px', display: 'flex', alignItems: 'center', gap: 12 }}>
          <Icon name="camera_alt" size={16} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
          <span style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }}>{s.name}</span>
          {s.used && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)' }}>{s.used}</span>}
          {s.creation && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{s.creation}</span>}
        </div>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// SnapshotSchedulerPage
// ---------------------------------------------------------------------------

type Tab = 'schedules' | 'snapshots'

export function SnapshotSchedulerPage() {
  const qc = useQueryClient()
  const [tab, setTab] = useState<Tab>('schedules')
  const [showCreate, setShowCreate] = useState(false)
  const [runningIdx, setRunningIdx] = useState<number | null>(null)

  const schedulesQ = useQuery({
    queryKey: ['snapshots', 'schedules'],
    queryFn: ({ signal }) => api.get<SchedulesResponse>('/api/snapshots/schedules', signal),
  })
  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets'],
    queryFn: ({ signal }) => api.get<DatasetsResponse>('/api/zfs/datasets', signal),
  })

  // Save the full schedule list (daemon expects full array replacement)
  const saveSchedules = useMutation({
    mutationFn: (schedules: SnapshotSchedule[]) => api.post('/api/snapshots/schedules', schedules),
    onSuccess: () => { toast.success('Schedules saved'); qc.invalidateQueries({ queryKey: ['snapshots', 'schedules'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const runNow = useMutation({
    mutationFn: ({ dataset, retention }: { dataset: string; retention: number }) =>
      api.post('/api/snapshots/run-now', { dataset, retain: retention }),
    onSuccess: () => toast.success('Snapshot taken'),
    onError: (e: Error) => toast.error(e.message),
    onSettled: () => setRunningIdx(null),
  })

  const schedules = schedulesQ.data?.schedules ?? []
  const datasets = datasetsQ.data?.data ?? []

  function addSchedule(s: SnapshotSchedule) {
    saveSchedules.mutate([...schedules, s])
  }

  function toggleSchedule(idx: number) {
    const updated = schedules.map((s, i) => i === idx ? { ...s, enabled: !s.enabled } : s)
    saveSchedules.mutate(updated)
  }

  function deleteSchedule(idx: number) {
    saveSchedules.mutate(schedules.filter((_, i) => i !== idx))
  }

  function handleRunNow(idx: number) {
    const s = schedules[idx]
    setRunningIdx(idx)
    runNow.mutate({ dataset: s.dataset, retention: s.retention })
  }

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'schedules', label: 'Schedules', icon: 'schedule' },
    { id: 'snapshots', label: 'Existing Snapshots', icon: 'camera_alt' },
  ]

  return (
    <div style={{ maxWidth: 1000 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 32 }}>
        <div>
          <h1 className="page-title">Snapshot Scheduler</h1>
          <p className="page-subtitle">Automated ZFS snapshots with retention policies</p>
        </div>
        {tab === 'schedules' && (
          <button onClick={() => setShowCreate(true)} className="btn btn-primary">
            <Icon name="add" size={16} /> Add Schedule
          </button>
        )}
      </div>

      {/* Tabs */}
      <div className="tabs-underline" style={{ marginBottom: 28 }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'schedules' && (
        <>
          {schedulesQ.isLoading && <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>{[0, 1].map(i => <Skeleton key={i} height={80} style={{ borderRadius: 'var(--radius-lg)' }} />)}</div>}
          {schedulesQ.isError && <ErrorState error={schedulesQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['snapshots', 'schedules'] })} />}
          {!schedulesQ.isLoading && !schedulesQ.isError && schedules.length === 0 && (
            <div style={{ textAlign: 'center', padding: '64px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
              <Icon name="schedule" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
              <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600 }}>No snapshot schedules</div>
              <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>Create a schedule to automate ZFS snapshots</div>
            </div>
          )}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {schedules.map((s, idx) => (
              <ScheduleRow key={`${s.dataset}-${idx}`} schedule={s} index={idx}
                onToggle={() => toggleSchedule(idx)}
                onDelete={() => deleteSchedule(idx)}
                onRunNow={() => handleRunNow(idx)}
                running={runningIdx === idx && runNow.isPending}
              />
            ))}
          </div>
        </>
      )}

      {tab === 'snapshots' && <SnapshotsTab />}

      {showCreate && (
        <CreateScheduleModal
          datasets={datasets}
          onClose={() => setShowCreate(false)}
          onSaved={addSchedule}
        />
      )}
    </div>
  )
}
