/**
 * FencingSettings - node-level protection used by storage groups (ADR-0009):
 * watchdog self-fencing, power fencing (IPMI/Redfish, PDU) and the SCSI-3
 * persistent reservation probe for shared disks.
 *
 * Calls: GET/POST /api/ha/watchdog/configure, /api/ha/fencing/configure,
 * /api/ha/pdu/configure; GET /api/ha/scsi/status; POST /api/ha/scsi/probe
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

interface FencingConfig {
  enable:                    boolean
  bmc_ip:                    string
  bmc_user:                  string
  bmc_password_file:         string
  jitter_max_ms:             number
  disk_fault_tolerance_pct?: number
}

interface PDUConfig {
  enable:          boolean
  outlet_off_url:  string
  method:          string   // GET | POST
  username:        string
  password_file:   string
  timeout_secs:    number
  expected_status: number   // 0 = any 2xx
}

interface SCSIStatusResponse {
  success:  boolean
  running:  boolean
  key?:     string
  devices?: string[]
  message?: string
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


export function FencingConfigForm() {
  const qc = useQueryClient()
  const q  = useQuery({
    queryKey: ['ha', 'fencing'],
    queryFn:  ({ signal }) => api.get<{ success: boolean; config: FencingConfig }>('/api/ha/fencing/configure', signal),
  })

  const [dirty, setDirty] = useState<{
    enable?: boolean; ip?: string; user?: string; passFile?: string; jitterMs?: number; diskTolPct?: number
  }>({})

  const sc = q.data?.config
  const enable     = dirty.enable     ?? sc?.enable                    ?? false
  const ip         = dirty.ip         ?? sc?.bmc_ip                    ?? ''
  const user       = dirty.user       ?? sc?.bmc_user                  ?? ''
  const passFile   = dirty.passFile   ?? sc?.bmc_password_file         ?? ''
  const jitterMs   = dirty.jitterMs   ?? sc?.jitter_max_ms             ?? 3000
  const diskTolPct = dirty.diskTolPct ?? sc?.disk_fault_tolerance_pct  ?? 10

  const setEnable     = (v: boolean) => setDirty(p => ({ ...p, enable: v }))
  const setIp         = (v: string)  => setDirty(p => ({ ...p, ip: v }))
  const setUser       = (v: string)  => setDirty(p => ({ ...p, user: v }))
  const setPassFile   = (v: string)  => setDirty(p => ({ ...p, passFile: v }))
  const setJitterMs   = (v: number)  => setDirty(p => ({ ...p, jitterMs: v }))
  const setDiskTolPct = (v: number)  => setDirty(p => ({ ...p, diskTolPct: v }))

  const save = useMutation({
    mutationFn: (cfg: FencingConfig) => api.post('/api/ha/fencing/configure', cfg),
    onSuccess: () => { toast.success('IPMI fencing configuration saved'); qc.invalidateQueries({ queryKey: ['ha', 'fencing'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    save.mutate({ enable, bmc_ip: ip.trim(), bmc_user: user.trim(), bmc_password_file: passFile.trim(), jitter_max_ms: jitterMs, disk_fault_tolerance_pct: diskTolPct })
  }

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24, borderLeft: enable ? '4px solid var(--error)' : '4px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="memory" size={24} style={{ color: enable ? 'var(--error)' : 'var(--text-tertiary)' }} />
        <div>
          <div style={{ fontWeight: 700 }}>IPMI / BMC Fencing (STONITH)</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            Chassis power-off via out-of-band IPMI LAN+ - requires Baseboard Management Controller
          </div>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 120px', gap: 12, marginBottom: 12 }}>
        <label className="field">
          <span className="field-label">BMC IP Address</span>
          <input value={ip} onChange={e => setIp(e.target.value)} placeholder="10.0.0.10"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">BMC Username</span>
          <input value={user} onChange={e => setUser(e.target.value)} placeholder="admin" className="input" disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">BMC Password File (0600)</span>
          <input value={passFile} onChange={e => setPassFile(e.target.value)} placeholder="/etc/dplaneos/bmc.secret"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Enable</span>
          <select value={enable ? 'yes' : 'no'} onChange={e => setEnable(e.target.value === 'yes')} className="input">
            <option value="no">Disabled</option>
            <option value="yes">Armed</option>
          </select>
        </label>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '180px 180px 1fr', gap: 12, marginBottom: 16, alignItems: 'end' }}>
        <label className="field">
          <span className="field-label">Jitter Window (ms)</span>
          <input type="number" min={0} max={30000} step={500} value={jitterMs}
            onChange={e => setJitterMs(Math.min(30000, Math.max(0, parseInt(e.target.value) || 0)))}
            className="input" disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Disk Fault Tolerance (%)</span>
          <input type="number" min={0} max={50} step={1} value={diskTolPct}
            onChange={e => setDiskTolPct(Math.min(50, Math.max(0, parseInt(e.target.value) || 0)))}
            className="input" disabled={q.isLoading} />
        </label>
        <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: 0, paddingBottom: 6 }}>
          Jitter: random delay before firing to prevent mutual destruction. Disk tolerance: % of pool disks that may fail SCSI-3 PR without aborting (0 = all-or-nothing, default 10).
        </p>
      </div>

      <button onClick={submit} disabled={save.isPending || q.isLoading} className="btn btn-primary"
        style={{ background: enable ? 'var(--error)' : 'var(--primary)', color: 'var(--text-on-primary)', border: 'none' }}>
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save IPMI Config'}
      </button>
    </div>
  )
}

export function PDUConfigForm() {
  const qc = useQueryClient()
  const q  = useQuery({
    queryKey: ['ha', 'pdu'],
    queryFn:  ({ signal }) => api.get<{ success: boolean; config: PDUConfig }>('/api/ha/pdu/configure', signal),
  })

  const [dirty, setDirty] = useState<{
    enable?: boolean; offUrl?: string; method?: string; username?: string; passFile?: string; timeoutS?: number; expStatus?: number
  }>({})

  const sp = q.data?.config
  const enable    = dirty.enable    ?? sp?.enable          ?? false
  const offUrl    = dirty.offUrl    ?? sp?.outlet_off_url  ?? ''
  const method    = dirty.method    ?? sp?.method          ?? 'GET'
  const username  = dirty.username  ?? sp?.username        ?? ''
  const passFile  = dirty.passFile  ?? sp?.password_file   ?? ''
  const timeoutS  = dirty.timeoutS  ?? sp?.timeout_secs    ?? 10
  const expStatus = dirty.expStatus ?? sp?.expected_status ?? 0

  const setEnable    = (v: boolean) => setDirty(p => ({ ...p, enable: v }))
  const setOffUrl    = (v: string)  => setDirty(p => ({ ...p, offUrl: v }))
  const setMethod    = (v: string)  => setDirty(p => ({ ...p, method: v }))
  const setUsername  = (v: string)  => setDirty(p => ({ ...p, username: v }))
  const setPassFile  = (v: string)  => setDirty(p => ({ ...p, passFile: v }))
  const setTimeoutS  = (v: number)  => setDirty(p => ({ ...p, timeoutS: v }))
  const setExpStatus = (v: number)  => setDirty(p => ({ ...p, expStatus: v }))

  const save = useMutation({
    mutationFn: (cfg: PDUConfig) => api.post('/api/ha/pdu/configure', cfg),
    onSuccess: () => { toast.success('PDU fencing configuration saved'); qc.invalidateQueries({ queryKey: ['ha', 'pdu'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (enable && !offUrl.trim()) { toast.error('Outlet Off URL is required when PDU fencing is enabled'); return }
    save.mutate({ enable, outlet_off_url: offUrl.trim(), method, username: username.trim(), password_file: passFile.trim(), timeout_secs: timeoutS, expected_status: expStatus })
  }

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24, borderLeft: enable ? '4px solid var(--error)' : '4px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="power" size={24} style={{ color: enable ? 'var(--error)' : 'var(--text-tertiary)' }} />
        <div>
          <div style={{ fontWeight: 700 }}>PDU Out-of-Band Fencing</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            Physically cuts outlet power via HTTP - works even when the data network is fully partitioned (Digital Loggers, iBoot, Raritan, etc.)
          </div>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 90px 90px 90px 120px', gap: 12, marginBottom: 12 }}>
        <label className="field">
          <span className="field-label">Outlet Off URL</span>
          <input value={offUrl} onChange={e => setOffUrl(e.target.value)} placeholder="http://pdu.local/outlet/2/off"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Method</span>
          <select value={method} onChange={e => setMethod(e.target.value)} className="input" style={{ appearance: 'none' }} disabled={q.isLoading}>
            <option value="GET">GET</option>
            <option value="POST">POST</option>
          </select>
        </label>
        <label className="field">
          <span className="field-label">Timeout (s)</span>
          <input type="number" min={1} max={60} value={timeoutS}
            onChange={e => setTimeoutS(Math.max(1, parseInt(e.target.value) || 10))} className="input" disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Exp. Status</span>
          <input type="number" min={0} max={599} value={expStatus}
            onChange={e => setExpStatus(parseInt(e.target.value) || 0)} className="input"
            placeholder="0" title="Expected HTTP status code. 0 = accept any 2xx." disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Enable</span>
          <select value={enable ? 'yes' : 'no'} onChange={e => setEnable(e.target.value === 'yes')} className="input" disabled={q.isLoading}>
            <option value="no">Disabled</option>
            <option value="yes">Armed</option>
          </select>
        </label>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginBottom: 16 }}>
        <label className="field">
          <span className="field-label">Username (optional)</span>
          <input value={username} onChange={e => setUsername(e.target.value)} placeholder="admin" className="input" disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Password File (0600)</span>
          <input value={passFile} onChange={e => setPassFile(e.target.value)} placeholder="/etc/dplaneos/pdu.secret"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} disabled={q.isLoading} />
        </label>
      </div>

      <button onClick={submit} disabled={save.isPending || q.isLoading} className="btn btn-primary"
        style={{ background: enable ? 'var(--error)' : 'var(--primary)', color: 'var(--text-on-primary)', border: 'none' }}>
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save PDU Config'}
      </button>
    </div>
  )
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
      if (result.all_supported) {
        toast.success(`All ${result.device_count} disk(s) support SCSI-3 PR`)
      } else {
        toast.error('One or more disks do not support SCSI-3 PR - shared-storage fencing will fail on those devices')
      }
    } catch (e: unknown) {
      toast.error(`Probe failed: ${(e as Error).message}`)
    } finally {
      setProbing(false)
    }
  }

  const status = statusQ.data
  const fencedDevices = status?.devices ?? []

  return (
    <div className="card" style={{
      borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24,
      borderLeft: status?.running ? '4px solid var(--success)' : '4px solid var(--border)',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="storage" size={24} style={{ color: status?.running ? 'var(--success)' : 'var(--text-tertiary)' }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontWeight: 700 }}>SCSI-3 Persistent Reservations</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            Shared-storage write exclusivity via dplane-fenced - hardware split-brain protection
          </div>
        </div>
        <div style={{
          padding: '4px 10px', borderRadius: 'var(--radius-md)', fontSize: 'var(--text-xs)', fontWeight: 600,
          background: status?.running ? 'var(--success-bg)' : 'var(--surface)',
          border: `1px solid ${status?.running ? 'var(--success-border)' : 'var(--border)'}`,
          color: status?.running ? 'var(--success)' : 'var(--text-tertiary)',
        }}>
          {statusQ.isLoading ? 'Checking…' : status?.running ? 'Active' : 'Not Running'}
        </div>
      </div>

      {status?.running ? (
        <>
          <div style={{ marginBottom: 14 }}>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 6, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
              Reservation Key
            </div>
            <code style={{ fontSize: 'var(--text-xs)', fontFamily: 'var(--font-mono)', background: 'var(--surface)', padding: '3px 8px', borderRadius: 'var(--radius-sm)', border: '1px solid var(--border)' }}>
              {status.key ?? 'unknown'}
            </code>
          </div>

          <div style={{ marginBottom: 14 }}>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 8, textTransform: 'uppercase', letterSpacing: '0.05em' }}>
              Reserved Devices ({fencedDevices.length})
            </div>
            {fencedDevices.length === 0 ? (
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontStyle: 'italic' }}>
                No devices currently reserved. Pool may not be imported or dplane-fenced is starting up.
              </div>
            ) : (
              <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
                {fencedDevices.map(dev => (
                  <span key={dev} style={{
                    fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)',
                    background: 'var(--success-bg)', border: '1px solid var(--success-border)',
                    color: 'var(--success)', padding: '2px 8px', borderRadius: 'var(--radius-sm)',
                  }}>
                    <Icon name="lock" size={11} style={{ verticalAlign: 'middle', marginRight: 4 }} />{dev}
                  </span>
                ))}
              </div>
            )}
          </div>
        </>
      ) : (
        <div style={{ marginBottom: 14, fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.6 }}>
          {status?.message ?? 'dplane-fenced is not running. Shared-storage SCSI-3 PR fencing requires the dplane-fenced service to be enabled in NixOS configuration.'}
        </div>
      )}

      <div style={{ borderTop: '1px solid var(--border)', paddingTop: 14, marginTop: 4 }}>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 10, lineHeight: 1.6 }}>
          <strong>Validate before enabling shared-storage HA.</strong> The probe runs a full PROUT write round-trip
          on each pool disk - the only reliable test. PRIN read-only probes produce false positives on drives that
          answer READ KEYS but reject REGISTER, which silently arms the cluster with broken fencing.
        </div>
        <button
          onClick={runProbe}
          disabled={probing}
          className="btn btn-ghost"
        >
          <Icon name="search" size={14} />{probing ? 'Probing disks…' : 'Probe PR Support on Pool Disks'}
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
                ? `All ${probeResult.device_count} device(s) support SCSI-3 PR - shared-storage fencing is safe`
                : `${probeResult.results.filter(r => !r.supported).length} of ${probeResult.device_count} device(s) do NOT support SCSI-3 PR`}
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
