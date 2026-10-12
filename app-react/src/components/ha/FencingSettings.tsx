/**
 * FencingSettings - node-level protection used by storage groups (ADR-0009):
 * watchdog self-fencing and the SCSI-3 persistent reservations on the disks
 * of shared storage groups. (Power fencing: PowerTargets.tsx.)
 *
 * Calls: GET/POST /api/ha/watchdog/configure; GET /api/ha/scsi/status; POST /api/ha/scsi/probe;
 * GET /api/ha/hardware/detect (what this node has: watchdog device, SAS/SCSI pool disks)
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

interface ReservedDisk {
  device:       string
  state:        'held' | 'unclaimed' | 'foreign' | 'shared' | 'unsupported' | 'error'
  holder?:      string
  holder_node?: string
  keys?:        string[]
  error?:       string
}

interface ReservedPool {
  group:  string
  pool:   string
  owner:  string
  mine:   boolean
  disks:  ReservedDisk[]
  state:  'reserved' | 'partial' | 'unsupported' | 'unreserved' | 'shut_out'
  detail: string
  error?: string
}

interface SCSIStatusResponse {
  success: boolean
  active:  boolean
  key?:    string
  pools:   ReservedPool[]
}

interface SCSIProbeResult {
  device:    string
  supported: boolean
  error?:    string
}

interface SCSIProbeResponse {
  success:         boolean
  auto_enumerated: boolean
  results:         SCSIProbeResult[]
  all_supported:   boolean
  device_count:    number
  message?:        string
}

interface WatchdogConfig {
  enable:           boolean
  device:           string   // /dev/watchdog or /dev/watchdog0
  timeout_secs:     number   // kernel fires reset after this many seconds without a pet
  pet_interval_sec: number   // how often the daemon writes to the device
}


export function SCSIFencingCard() {
  const statusQ = useQuery({
    queryKey: ['ha', 'scsi', 'status'],
    queryFn:  ({ signal }) => api.get<SCSIStatusResponse>('/api/ha/scsi/status', signal),
    refetchInterval: 30_000,
  })

  const [probeResult, setProbeResult] = useState<SCSIProbeResponse | null>(null)
  const [probing, setProbing] = useState(false)

  async function runProbe() {
    setProbing(true)
    setProbeResult(null)
    try {
      const result = await api.post<SCSIProbeResponse>('/api/ha/scsi/probe', {})
      setProbeResult(result)
      if (result.device_count === 0) {
        toast.info(result.message ?? 'No SCSI disks to probe')
      } else if (result.all_supported) {
        toast.success(`All ${result.device_count} disk(s) have persistent reservations`)
      } else {
        toast.error('One or more disks have no persistent reservations: those are protected by the watchdog and multihost only')
      }
    } catch (e: unknown) {
      toast.error(`Probe failed: ${(e as Error).message}`)
    } finally {
      setProbing(false)
    }
  }

  const status = statusQ.data
  const pools = status?.pools ?? []
  const shutOut = pools.some(p => p.state === 'shut_out')
  const tone = (st: ReservedPool['state']) =>
    st === 'reserved' ? 'success' : st === 'shut_out' ? 'error' : st === 'partial' ? 'warning' : 'neutral'
  const label: Record<ReservedPool['state'], string> = {
    reserved: 'Reserved', partial: 'Partly reserved', unsupported: 'Not supported by the disks',
    unreserved: 'Not reserved', shut_out: 'This node is shut out',
  }
  const diskLabel: Record<ReservedDisk['state'], string> = {
    held: 'this node', unclaimed: 'nobody', foreign: 'another node', shared: 'several nodes',
    unsupported: 'no reservations', error: 'error',
  }

  return (
    <div className="card" style={{
      borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24,
      borderLeft: shutOut ? '4px solid var(--error)' : status?.active ? '4px solid var(--success)' : '4px solid var(--border)',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="storage" size={24} style={{ color: shutOut ? 'var(--error)' : status?.active ? 'var(--success)' : 'var(--text-tertiary)' }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontWeight: 700 }}>Disk reservations (SCSI-3)</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            The disks of a shared storage group accept writes only from the node that serves it
          </div>
        </div>
        <div style={{
          padding: '4px 10px', borderRadius: 'var(--radius-md)', fontSize: 'var(--text-xs)', fontWeight: 600,
          background: status?.active ? 'var(--success-bg)' : 'var(--surface)',
          border: `1px solid ${status?.active ? 'var(--success-border)' : 'var(--border)'}`,
          color: status?.active ? 'var(--success)' : 'var(--text-tertiary)',
        }}>
          {statusQ.isLoading ? 'Checking…' : status?.active ? 'In use' : 'Not in use'}
        </div>
      </div>

      <div style={{ marginBottom: 14, fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.6 }}>
        Nothing has to be switched on. A node that imports the pool of a shared storage group takes the reservation on its
        disks first, and takes it away from the node that had it. A node that is hung or cut off can then no longer write to
        the pool, whatever state it is in. Disks without reservations (virtual disks, NVMe, SATA behind most adapters) are
        protected by the watchdog and ZFS multihost alone.
      </div>

      {statusQ.isError && <div style={{ color: 'var(--error)', fontSize: 'var(--text-sm)', marginBottom: 14 }}>{(statusQ.error as Error).message}</div>}

      {pools.length === 0 ? (
        <div style={{ marginBottom: 14, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontStyle: 'italic' }}>
          No shared storage groups.
        </div>
      ) : pools.map(p => (
        <div key={p.group + '/' + p.pool} style={{ marginBottom: 14, padding: '12px 14px', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-md)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
            <span style={{ fontWeight: 600, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }}>{p.pool}</span>
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>group {p.group}</span>
            <span className={`badge badge-${tone(p.state)}`} style={{ marginLeft: 'auto' }}>{label[p.state]}</span>
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: p.state === 'shut_out' ? 'var(--error)' : 'var(--text-secondary)', lineHeight: 1.5, marginBottom: p.disks.length ? 8 : 0 }}>{p.detail}</div>
          {p.disks.length > 0 && (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
              {p.disks.map(d => (
                <span key={d.device} title={d.error ?? (d.holder ? `reservation key ${d.holder}` : '')} style={{
                  fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', padding: '2px 8px', borderRadius: 'var(--radius-sm)',
                  background: 'var(--bg-card)', border: '1px solid var(--border)',
                  color: d.state === 'held' ? 'var(--success)' : d.state === 'error' ? 'var(--error)' : 'var(--text-secondary)',
                }}>
                  <Icon name={d.state === 'held' || d.state === 'foreign' ? 'lock' : 'lock_open'} size={11} style={{ verticalAlign: 'middle', marginRight: 4 }} />
                  {d.device}: {d.state === 'foreign' && d.holder_node ? d.holder_node : diskLabel[d.state]}
                </span>
              ))}
            </div>
          )}
        </div>
      ))}

      {status?.key && (
        <div style={{ marginBottom: 14, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
          Reservation key of this node: <code style={{ fontFamily: 'var(--font-mono)' }}>{status.key}</code>
        </div>
      )}

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 14, marginTop: 4 }}>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 10, lineHeight: 1.6 }}>
          <strong>Do the disks of this node have reservations?</strong> The probe registers a test key on every SCSI disk
          of the pools imported here, checks that the disk lists it, and removes it again. Reading alone proves nothing:
          some adapters answer the question and then refuse the registration. Disks that are already reserved are reported
          as supported without being touched.
        </div>
        <button
          onClick={runProbe}
          disabled={probing}
          className="btn btn-ghost"
        >
          <Icon name="search" size={14} />{probing ? 'Probing disks…' : 'Probe the pool disks'}
        </button>
      </div>

      {probeResult && (
        <div style={{
          marginTop: 16, background: 'var(--surface)', borderRadius: 'var(--radius-md)',
          padding: '12px 16px',
          border: `1px solid ${probeResult.all_supported ? 'var(--success-border)' : 'var(--error-border)'}`,
        }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
            <Icon
              name={probeResult.all_supported ? 'check_circle' : 'cancel'}
              size={16}
              style={{ color: probeResult.all_supported ? 'var(--success)' : 'var(--error)' }}
            />
            <span style={{
              fontWeight: 700, fontSize: 'var(--text-sm)',
              color: probeResult.all_supported ? 'var(--success)' : 'var(--error)',
            }}>
              {probeResult.all_supported
                ? `All ${probeResult.device_count} disk(s) have persistent reservations`
                : probeResult.device_count === 0 ? 'Nothing to probe'
                : `${probeResult.results.filter(r => !r.supported).length} of ${probeResult.device_count} disk(s) have no persistent reservations`}
            </span>
          </div>
          {probeResult.message && (
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 10 }}>
              {probeResult.message}
            </div>
          )}
          {probeResult.results.map((r, i) => (
            <div key={i} style={{ display: 'flex', alignItems: 'flex-start', gap: 8, marginBottom: 6, fontSize: 'var(--text-xs)' }}>
              <Icon
                name={r.supported ? 'check' : 'close'}
                size={13}
                style={{ color: r.supported ? 'var(--success)' : 'var(--error)', flexShrink: 0, marginTop: 1 }}
              />
              <div>
                <span style={{ fontFamily: 'var(--font-mono)', color: r.supported ? 'var(--text)' : 'var(--text-tertiary)' }}>
                  {r.device}
                </span>
                {r.error && (
                  <div style={{ color: 'var(--error)', marginTop: 2, lineHeight: 1.4 }}>{r.error}</div>
                )}
              </div>
            </div>
          ))}
          {probeResult.auto_enumerated && (
            <div style={{ marginTop: 8, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              Devices auto-enumerated from imported ZFS pools.
            </div>
          )}
        </div>
      )}
    </div>
  )
}

export function WatchdogConfigForm() {
  const qc = useQueryClient()
  const q  = useQuery({
    queryKey: ['ha', 'watchdog'],
    queryFn:  ({ signal }) => api.get<{ success: boolean; config: WatchdogConfig }>('/api/ha/watchdog/configure', signal),
  })

  const [dirty, setDirty] = useState<{ enable?: boolean; device?: string; timeout?: number; pet?: number }>({})

  const wd = q.data?.config
  const enable  = dirty.enable  ?? wd?.enable          ?? false
  const device  = dirty.device  ?? wd?.device          ?? '/dev/watchdog'
  const timeout = dirty.timeout ?? wd?.timeout_secs    ?? 30
  const pet     = dirty.pet     ?? wd?.pet_interval_sec ?? 10

  const setEnable  = (v: boolean) => setDirty(p => ({ ...p, enable: v }))
  const setDevice  = (v: string)  => setDirty(p => ({ ...p, device: v }))
  const setTimeoutS = (v: number) => setDirty(p => ({ ...p, timeout: v }))
  const setPet     = (v: number)  => setDirty(p => ({ ...p, pet: v }))

  const save = useMutation({
    mutationFn: (cfg: WatchdogConfig) => api.post('/api/ha/watchdog/configure', cfg),
    onSuccess: () => {
      toast.success('Watchdog self-fence configuration saved')
      qc.invalidateQueries({ queryKey: ['ha', 'watchdog'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (enable && pet >= timeout) {
      toast.error('Pet interval must be less than timeout')
      return
    }
    save.mutate({ enable, device: device.trim(), timeout_secs: timeout, pet_interval_sec: pet })
  }

  return (
    <div className="card" style={{
      borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24,
      borderLeft: enable ? '4px solid var(--warning)' : '4px solid var(--border)',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="timer" size={24} style={{ color: enable ? 'var(--warning)' : 'var(--text-tertiary)' }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontWeight: 700 }}>Hardware Watchdog Self-Fence</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            Node hard-resets itself on quorum loss - removes BMC/PDU network dependency from fencing
          </div>
        </div>
        <div style={{
          padding: '4px 10px', borderRadius: 'var(--radius-md)', fontSize: 'var(--text-xs)', fontWeight: 600,
          background: enable ? 'var(--warning-bg)' : 'var(--surface)',
          border: `1px solid ${enable ? 'var(--warning-border)' : 'var(--border)'}`,
          color: enable ? 'var(--warning)' : 'var(--text-tertiary)',
        }}>
          {enable ? 'Armed' : 'Disabled'}
        </div>
      </div>

      {enable && (
        <div className="alert alert-warning" style={{ marginBottom: 16, padding: '10px 14px' }}>
          <Icon name="warning" size={16} />
          <div style={{ fontSize: 'var(--text-xs)', lineHeight: 1.5 }}>
            <strong>Self-fencing is on.</strong> If this node owns a storage group and loses quorum, it stops resetting
            the watchdog and the kernel resets it after <strong>{timeout}s</strong>. Another node takes the group over
            only after that time plus a margin, so the two never use the pools at the same time.
          </div>
        </div>
      )}

      {!enable && (
        <div style={{ marginBottom: 16, padding: '10px 14px', borderRadius: 'var(--radius-md)', background: 'var(--surface)', border: '1px solid var(--border)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.6 }}>
          When enabled, the daemon resets the watchdog while this node may keep running: always without a cluster,
          and in a cluster while it is quorate or owns no storage. A storage owner that loses quorum stops resetting it
          and the kernel resets the node after the timeout, so another node can take its storage over safely without
          reaching this node's BMC.
          Works on any Linux system with <code>/dev/watchdog</code>; hardware watchdogs (iTCO, sp5100_tco) are preferred
          but <code>softdog</code> (loaded automatically on NixOS) works as a fallback.
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 120px 120px 120px', gap: 12, marginBottom: 12, alignItems: 'end' }}>
        <label className="field">
          <span className="field-label">Watchdog Device</span>
          <input value={device} onChange={e => setDevice(e.target.value)}
            placeholder="/dev/watchdog"
            className="input" style={{ fontFamily: 'var(--font-mono)' }}
            disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Timeout (s)</span>
          <input type="number" min={10} max={300} value={timeout}
            onChange={e => setTimeoutS(Math.max(10, parseInt(e.target.value) || 30))}
            className="input" disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Pet Interval (s)</span>
          <input type="number" min={1} max={timeout - 1} value={pet}
            onChange={e => setPet(Math.max(1, parseInt(e.target.value) || 10))}
            className="input" disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Enable</span>
          <select value={enable ? 'yes' : 'no'} onChange={e => setEnable(e.target.value === 'yes')} className="input" disabled={q.isLoading}>
            <option value="no">Disabled</option>
            <option value="yes">Armed</option>
          </select>
        </label>
      </div>

      <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 14, lineHeight: 1.5 }}>
        Changes to device path and timeout require a daemon restart to fully take effect.
        The enable/disable change is applied immediately to the running daemon.
      </div>

      <button onClick={submit} disabled={save.isPending || q.isLoading} className="btn btn-primary">
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Watchdog Config'}
      </button>
    </div>
  )
}

interface HAHardware {
  watchdog_available: boolean; watchdog_device: string
  pool_sg_devices: string[] | null
  provisional_path: 'shared_storage' | 'replicated'; provisional_path_label: string; provisional_reason: string
  probe_required: boolean
}

/** What this node's hardware supports, read without touching the disks. */
export function HardwareCheckCard() {
  const q = useQuery({
    queryKey: ['ha', 'hardware'],
    queryFn: ({ signal }) => api.get<HAHardware>('/api/ha/hardware/detect', signal),
  })
  const hw = q.data
  const row = (ok: boolean, label: string, detail: string) => (
    <div style={{ display: 'flex', gap: 10, alignItems: 'baseline', fontSize: 'var(--text-sm)' }}>
      <Icon name={ok ? 'check_circle' : 'remove_circle_outline'} size={16} style={{ color: ok ? 'var(--success)' : 'var(--text-tertiary)', alignSelf: 'center' }} />
      <span style={{ fontWeight: 600, minWidth: 190 }}>{label}</span>
      <span style={{ color: 'var(--text-secondary)' }}>{detail}</span>
    </div>
  )
  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 14 }}>
        <Icon name="memory" size={24} style={{ color: 'var(--primary)' }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontWeight: 700 }}>Hardware check</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>What this node has for protecting storage groups</div>
        </div>
        <button className="btn btn-ghost btn-sm" disabled={q.isFetching} onClick={() => q.refetch()}>
          <Icon name="refresh" size={14} />{q.isFetching ? 'Checking…' : 'Check again'}
        </button>
      </div>
      {q.isError && <div style={{ color: 'var(--error)', fontSize: 'var(--text-sm)' }}>{(q.error as Error).message}</div>}
      {hw && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {row(hw.watchdog_available, 'Hardware watchdog', hw.watchdog_available ? hw.watchdog_device : 'No watchdog device: a node that loses quorum cannot reset itself')}
          {row((hw.pool_sg_devices ?? []).length > 0, 'SAS/SCSI pool disks', (hw.pool_sg_devices ?? []).length > 0 ? (hw.pool_sg_devices ?? []).join(', ') : 'None: disks cannot be shared between nodes')}
          <div style={{ marginTop: 6, padding: '10px 12px', borderRadius: 'var(--radius-md)', background: 'var(--surface)', border: '1px solid var(--border)', fontSize: 'var(--text-sm)' }}>
            <div style={{ fontWeight: 600 }}>Suggested setup: {hw.provisional_path_label}</div>
            <div style={{ color: 'var(--text-secondary)', marginTop: 2 }}>{hw.provisional_reason}</div>
          </div>
        </div>
      )}
    </div>
  )
}
