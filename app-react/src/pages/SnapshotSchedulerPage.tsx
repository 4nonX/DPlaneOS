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
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

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
// Styles
// ---------------------------------------------------------------------------

const inputStyle: React.CSSProperties = {
  background: 'var(--surface)', border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)', padding: '9px 13px',
  color: 'var(--text)', fontSize: 'var(--text-sm)', width: '100%',
  fontFamily: 'var(--font-ui)', outline: 'none', boxSizing: 'border-box',
}
const btnPrimary: React.CSSProperties = {
  padding: '9px 20px', background: 'var(--primary)', color: '#000',
  border: 'none', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 600,
}
const btnGhost: React.CSSProperties = {
  padding: '9px 16px', background: 'var(--surface)', color: 'var(--text-secondary)',
  border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 500, display: 'inline-flex', alignItems: 'center', gap: 6,
}

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
    <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 200 }}
      onClick={e => e.target === e.currentTarget && onClose()}>
      <div style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 32, width: 460, maxWidth: '92vw' }}>
        <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 700, marginBottom: 24 }}>New Snapshot Schedule</h3>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Dataset</span>
            <select value={dataset} onChange={e => setDataset(e.target.value)} style={inputStyle}>
              {datasets.map(d => <option key={d.name} value={d.name}>{d.name}</option>)}
            </select>
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Frequency</span>
            <select value={frequency} onChange={e => setFrequency(e.target.value as SnapshotSchedule['frequency'])} style={inputStyle}>
              {(Object.keys(FREQ_LABELS) as SnapshotSchedule['frequency'][]).map(f => (
                <option key={f} value={f}>{FREQ_LABELS[f]}</option>
              ))}
            </select>
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Keep (count)</span>
            <input type="number" value={retention} min={1} max={365}
              onChange={e => setRetention(parseInt(e.target.value) || 1)} style={inputStyle} />
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Prefix</span>
            <input value={prefix} onChange={e => setPrefix(e.target.value)} placeholder="auto" style={inputStyle} />
          </label>
        </div>
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 24 }}>
          <button onClick={onClose} style={btnGhost}>Cancel</button>
          <button onClick={submit} style={btnPrimary}>Create Schedule</button>
        </div>
      </div>
    </div>
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
        <button onClick={onRunNow} disabled={running} style={btnGhost} title="Run now">
          <Icon name="play_arrow" size={14} />{running ? 'Running…' : 'Run Now'}
        </button>
        <button onClick={onToggle} style={btnGhost} title={schedule.enabled ? 'Disable' : 'Enable'}>
          <Icon name={schedule.enabled ? 'pause_circle' : 'play_circle'} size={14} />
        </button>
        {!confirming
          ? <button onClick={() => setConfirming(true)} style={{ ...btnGhost, color: 'var(--error)', borderColor: 'var(--error-border)' }}>
              <Icon name="delete" size={14} />
            </button>
          : <>
              <button onClick={onDelete} style={{ ...btnGhost, color: 'var(--error)' }}>Delete</button>
              <button onClick={() => setConfirming(false)} style={btnGhost}>Cancel</button>
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
          <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>Snapshot Scheduler</h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>Automated ZFS snapshots with retention policies</p>
        </div>
        {tab === 'schedules' && (
          <button onClick={() => setShowCreate(true)} style={btnPrimary}>
            <Icon name="add" size={16} /> Add Schedule
          </button>
        )}
      </div>

      {/* Tabs */}
      <div style={{ display: 'flex', gap: 4, marginBottom: 28, borderBottom: '1px solid var(--border)' }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} style={{ padding: '10px 20px', background: 'none', border: 'none', cursor: 'pointer', fontSize: 'var(--text-sm)', fontWeight: 600, color: tab === t.id ? 'var(--primary)' : 'var(--text-secondary)', borderBottom: tab === t.id ? '2px solid var(--primary)' : '2px solid transparent', marginBottom: -1, display: 'flex', alignItems: 'center', gap: 6 }}>
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
