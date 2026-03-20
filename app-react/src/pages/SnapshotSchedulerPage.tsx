/**
 * pages/SnapshotSchedulerPage.tsx - Snapshot Scheduler (Phase 2)
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/snapshots/schedules
 *   POST /api/snapshots/schedules     (save full schedules array)
 *   POST /api/snapshots/run-now       (body: { dataset, retain })
 *   GET  /api/zfs/datasets            (populate dataset picker)
 *   GET  /api/zfs/snapshots           (list existing snapshots)
 *   GET  /api/replication/schedules   (for trigger-on-snapshot selector)
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { Modal } from '@/components/ui/Modal'
import { Tooltip } from '@/components/ui/Tooltip'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface SnapshotSchedule {
  dataset:              string
  frequency:            'hourly' | 'daily' | 'weekly' | 'monthly'
  retention:            number
  retention_days?:      number   // days-based retention (in addition to count)
  enabled:              boolean
  prefix?:              string
  trigger_replication?: boolean  // show replication trigger toggle in UI
  replication_id?:      string   // which replication schedule to trigger
}

interface SchedulesResponse { success: boolean; schedules: SnapshotSchedule[] }

interface ZFSDataset { name: string; used: string; avail: string; mountpoint: string }
interface DatasetsResponse { success: boolean; data: ZFSDataset[] }

interface Snapshot { name: string; used: string; creation: string }
interface SnapshotsResponse { success: boolean; snapshots?: Snapshot[]; data?: Snapshot[] }

interface ReplicationSchedule {
  id:             string
  name:           string
  source_dataset: string
  remote_host:    string
  interval:       string
}
interface ReplSchedulesResponse { success: boolean; schedules: ReplicationSchedule[] }

// ---------------------------------------------------------------------------

const FREQ_LABELS: Record<SnapshotSchedule['frequency'], string> = {
  hourly: 'Hourly', daily: 'Daily', weekly: 'Weekly', monthly: 'Monthly',
}
const FREQ_ICONS: Record<SnapshotSchedule['frequency'], string> = {
  hourly: 'schedule', daily: 'today', weekly: 'date_range', monthly: 'calendar_month',
}

// ---------------------------------------------------------------------------
// ScheduleModal
// ---------------------------------------------------------------------------

function ScheduleModal({ datasets, replSchedules, initial, onClose, onSaved }: {
  datasets: ZFSDataset[]
  replSchedules: ReplicationSchedule[]
  initial?: SnapshotSchedule
  onClose: () => void
  onSaved: (s: SnapshotSchedule) => void
}) {
  const [dataset,           setDataset]           = useState(initial?.dataset ?? datasets[0]?.name ?? '')
  const [frequency,         setFrequency]          = useState<SnapshotSchedule['frequency']>(initial?.frequency ?? 'daily')
  const [retention,         setRetention]          = useState(initial?.retention ?? 7)
  const [retentionDays,     setRetentionDays]      = useState(initial?.retention_days ?? 0)
  const [prefix,            setPrefix]             = useState(initial?.prefix ?? 'auto')
  const [triggerRepl,       setTriggerRepl]        = useState(initial?.trigger_replication ?? false)
  const [replicationId,     setReplicationId]      = useState(initial?.replication_id ?? replSchedules[0]?.id ?? '')

  function submit() {
    if (!dataset) { toast.error('Select a dataset'); return }
    if (retention < 1) { toast.error('Retention count must be ≥ 1'); return }

    const sched: SnapshotSchedule = {
      dataset,
      frequency,
      retention,
      retention_days: retentionDays > 0 ? retentionDays : undefined,
      enabled: initial ? initial.enabled : true,
      prefix,
      trigger_replication: triggerRepl || undefined,
      replication_id: triggerRepl ? replicationId : undefined,
    }
    onSaved(sched)
    onClose()
  }

  // Find the replication schedules that match the selected dataset
  const matchingReplScheds = replSchedules.filter(rs =>
    rs.source_dataset === dataset ||
    dataset.startsWith(rs.source_dataset + '/') ||
    rs.source_dataset.startsWith(dataset + '/')
  )
  const replOptions = matchingReplScheds.length > 0 ? matchingReplScheds : replSchedules

  return (
    <Modal title={initial ? "Edit Snapshot Schedule" : "New Snapshot Schedule"} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <label className="field">
          <span className="field-label">Dataset</span>
          <select value={dataset} onChange={e => setDataset(e.target.value)} className="input" disabled={!!initial}>
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

        {/* Retention: count + optional days */}
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
          <label className="field">
            <span className="field-label">Retention Count</span>
            <input type="number" value={retention} min={1} max={365}
              onChange={e => setRetention(parseInt(e.target.value) || 1)} className="input" />
          </label>
          <label className="field">
            <span className="field-label">Retention Days <span style={{ fontWeight: 400, color: 'var(--text-tertiary)' }}>(0 = off)</span></span>
            <input type="number" value={retentionDays} min={0} max={3650}
              onChange={e => setRetentionDays(parseInt(e.target.value) || 0)} className="input" />
          </label>
        </div>

        <label className="field">
          <span className="field-label">Prefix</span>
          <input value={prefix} onChange={e => setPrefix(e.target.value)} placeholder="auto" className="input" />
        </label>

        {/* Trigger replication toggle */}
        <div style={{ borderTop: '1px solid var(--border)', paddingTop: 14 }}>
          <label style={{ display: 'flex', alignItems: 'center', gap: 10, cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={triggerRepl}
              onChange={e => setTriggerRepl(e.target.checked)}
              style={{ accentColor: 'var(--primary)', width: 16, height: 16 }}
            />
            <span style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>
              Trigger Replication after each snapshot
            </span>
          </label>
          {triggerRepl && (
            <div style={{ marginTop: 12 }}>
              {replOptions.length === 0 ? (
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', padding: '8px 12px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)' }}>
                  No replication schedules found - create one in the Replication page first.
                </div>
              ) : (
                <label className="field">
                  <span className="field-label">Replicate to</span>
                  <select value={replicationId} onChange={e => setReplicationId(e.target.value)} className="input">
                    {replOptions.map(rs => (
                      <option key={rs.id} value={rs.id}>
                        {rs.name} ({rs.source_dataset} → {rs.remote_host || 'remote'})
                      </option>
                    ))}
                  </select>
                </label>
              )}
            </div>
          )}
        </div>
      </div>

      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 24 }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={submit} className="btn btn-primary">{initial ? "Save Changes" : "Create Schedule"}</button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ScheduleRow
// ---------------------------------------------------------------------------

function ScheduleRow({ schedule, index: _index, replSchedules, onToggle, onEdit, onDelete, onRunNow, running }: {
  schedule: SnapshotSchedule
  index: number
  replSchedules: ReplicationSchedule[]
  onToggle: () => void
  onEdit: () => void
  onDelete: () => void
  onRunNow: () => void
  running: boolean
}) {
  const [confirming, setConfirming] = useState(false)

  const replicationName = schedule.trigger_replication && schedule.replication_id
    ? (replSchedules.find(rs => rs.id === schedule.replication_id)?.name ?? schedule.replication_id)
    : null

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '16px 20px', display: 'flex', alignItems: 'center', gap: 16, opacity: schedule.enabled ? 1 : 0.6 }}>
      <Icon name={FREQ_ICONS[schedule.frequency]} size={24} style={{ color: 'var(--primary)', flexShrink: 0 }} />
      <div style={{ flex: 1 }}>
        <div style={{ fontWeight: 700, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', marginBottom: 4 }}>{schedule.dataset}</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
          {FREQ_LABELS[schedule.frequency]}
          {' · keep '}
          {schedule.retention}
          {schedule.retention_days ? ` / ${schedule.retention_days}d` : ''}
          {schedule.prefix && ` · prefix: ${schedule.prefix}`}
          {replicationName && (
            <span style={{ marginLeft: 6, color: 'var(--primary)' }}>
              <Icon name="sync_alt" size={11} style={{ verticalAlign: 'middle', marginRight: 3 }} />
              {replicationName}
            </span>
          )}
        </div>
      </div>
      <span style={{ padding: '3px 10px', borderRadius: 'var(--radius-full)', background: schedule.enabled ? 'var(--success-bg)' : 'var(--surface)', border: `1px solid ${schedule.enabled ? 'var(--success-border)' : 'var(--border)'}`, color: schedule.enabled ? 'var(--success)' : 'var(--text-tertiary)', fontSize: 'var(--text-2xs)', fontWeight: 700 }}>
        {schedule.enabled ? 'ACTIVE' : 'DISABLED'}
      </span>
      <div style={{ display: 'flex', gap: 6 }}>
        <Tooltip content="Run now">
          <button onClick={onRunNow} disabled={running} className="btn btn-ghost">
            <Icon name="play_arrow" size={14} />{running ? 'Running…' : 'Run Now'}
          </button>
        </Tooltip>
        <Tooltip content="Edit">
          <button onClick={onEdit} className="btn btn-ghost">
            <Icon name="edit" size={14} />
          </button>
        </Tooltip>
        <Tooltip content={schedule.enabled ? 'Disable' : 'Enable'}>
          <button onClick={onToggle} className="btn btn-ghost">
            <Icon name={schedule.enabled ? 'pause_circle' : 'play_circle'} size={14} />
          </button>
        </Tooltip>
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
// SnapshotsTab - recent snapshots list
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
        <div key={s.name} className="card" style={{ borderRadius: 'var(--radius-sm)', padding: '10px 16px', display: 'flex', alignItems: 'center', gap: 12 }}>
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
  const [editingIdx, setEditingIdx] = useState<number | null>(null)
  const [runningIdx, setRunningIdx] = useState<number | null>(null)

  const schedulesQ = useQuery({
    queryKey: ['snapshots', 'schedules'],
    queryFn: ({ signal }) => api.get<SchedulesResponse>('/api/snapshots/schedules', signal),
  })
  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets'],
    queryFn: ({ signal }) => api.get<DatasetsResponse>('/api/zfs/datasets', signal),
  })
  const replSchedulesQ = useQuery({
    queryKey: ['replication', 'schedules'],
    queryFn: ({ signal }) => api.get<ReplSchedulesResponse>('/api/replication/schedules', signal),
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

  const schedules     = schedulesQ.data?.schedules ?? []
  const datasets      = datasetsQ.data?.data ?? []
  const replSchedules = replSchedulesQ.data?.schedules ?? []

  function addSchedule(s: SnapshotSchedule) {
    saveSchedules.mutate([...schedules, s])
  }

  function updateSchedule(idx: number, updated: SnapshotSchedule) {
    const next = [...schedules]
    next[idx] = updated
    saveSchedules.mutate(next)
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
                replSchedules={replSchedules}
                onToggle={() => toggleSchedule(idx)}
                onEdit={() => setEditingIdx(idx)}
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
        <ScheduleModal
          datasets={datasets}
          replSchedules={replSchedules}
          onClose={() => setShowCreate(false)}
          onSaved={addSchedule}
        />
      )}

      {editingIdx !== null && (
        <ScheduleModal
          datasets={datasets}
          replSchedules={replSchedules}
          initial={schedules[editingIdx]}
          onClose={() => setEditingIdx(null)}
          onSaved={(s) => updateSchedule(editingIdx, s)}
        />
      )}
    </div>
  )
}


