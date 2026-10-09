/**
 * pages/HAPage.tsx - High Availability Cluster
 *
 * API surface:
 *   GET  /api/ha/status                     → { success, cluster: Cluster, witness: WitnessConfig }
 *   GET  /api/ha/local
 *   POST /api/ha/peers                       { id, name, address, role }
 *   DELETE /api/ha/peers/{id}
 *   POST /api/ha/peers/{id}/role             { role:'active' }
 *   POST /api/ha/promote
 *   POST /api/ha/fence                       { node_id }
 *   POST /api/ha/toggle                      { enable }
 *   POST /api/ha/maintenance                 { seconds }
 *   POST /api/ha/clear_fault
 *   GET  /api/ha/witness/configure
 *   POST /api/ha/witness/configure
 *   POST /api/ha/witness/test
 *   GET  /api/ha/fencing/configure
 *   POST /api/ha/fencing/configure
 *   GET  /api/ha/pdu/configure
 *   POST /api/ha/pdu/configure
 *   GET  /api/ha/sbd/configure
 *   POST /api/ha/sbd/configure
 *   GET  /api/ha/replication/configure
 *   POST /api/ha/replication/configure
 *   GET  /api/ha/watchdog/configure
 *   POST /api/ha/watchdog/configure
 *   GET  /api/ha/timing
 *   POST /api/ha/timing
 *   GET  /api/ha/scsi/status
 *   POST /api/ha/scsi/probe
 *   GET  /api/ha/hardware/detect
 */

import { useState, useEffect, useRef } from 'react'
import { QuorumPanel } from '@/components/ha/QuorumPanel'
import { GroupsPanel } from '@/components/ha/GroupsPanel'
import { ProtectionCard } from '@/components/ha/ProtectionCard'
import { SplitPanel } from '@/components/ha/SplitPanel'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { useFrozenLayout } from '@/hooks/useFrozenLayout'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { JobProgress } from '@/components/ui/JobProgress'
import { useJobStore } from '@/stores/jobs'
import { JobConsole } from '@/components/ui/JobConsole'
import { useWsStore } from '@/stores/ws'
import { CTDBConfig } from '@/components/ha/CTDBConfig'

// ─── Types ────────────────────────────────────────────────────────────────────

interface HANode {
  id:            string
  name?:         string
  address?:      string
  role?:         string   // active | standby
  state?:        string   // healthy | degraded | unreachable | unknown
  missed_beats?: number
  last_seen?:    string
  last_seen_unix?: number
  version?:      string
}

interface Cluster {
  quorum?:            boolean
  active_node?:       HANode
  local_node?:        HANode
  peers?:             HANode[]
  ha_enabled?:        boolean
  maintenance_active?: boolean
  maintenance_until?:  number
  subordinate_mode?:   boolean   // node is catching up stale data after zombie boot
  hysteresis_active?:  boolean   // flap-guard suppressing auto-failover
  last_failover_at?:   number    // unix timestamp; 0 = never
  last_promotion?: {             // outcome of the last automatic promotion, step by step
    failed: boolean; degraded: boolean; finished_at: string
    steps: { name: string; ok: boolean; skipped?: boolean; detail?: string }[]
  }
}

interface HAStatusResponse {
  success:  boolean
  cluster?: Cluster
  witness?: WitnessConfig
}
interface HALocalResponse {
  success:  boolean
  id?:      string
  node_id?: string
  address?: string
  role?:    string
  name?:    string
}

interface ReplicationConfig {
  local_pool:   string
  remote_pool:  string
  remote_host:  string
  remote_user:  string
  remote_port:  number
  ssh_key_path: string
  interval_secs: number
}

interface FencingConfig {
  enable:                    boolean
  bmc_ip:                    string
  bmc_user:                  string
  bmc_password_file:         string
  jitter_max_ms:             number
  disk_fault_tolerance_pct?: number
}

interface WitnessEntry {
  url:                 string
  expected_status:     number   // 0 = any valid HTTP response
  expected_body_regex: string   // '' = skip body check
  strict_tls:          boolean  // enforce cert verification
}

interface WitnessConfig {
  enable:           boolean
  witnesses:        WitnessEntry[]
  required_healthy: number
  timeout_secs:     number
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

interface SBDConfig {
  pool:           string   // ZFS pool name; '' = SBD disabled
  dataset:        string   // dataset under pool for the lease token
  lease_ttl_secs: number   // seconds before a stale lease is considered dead
}

interface SBDResponse {
  success:           boolean
  config:            SBDConfig
  lease_active:      boolean
  last_renewal_unix: number
}

interface ClusterSecretStatus {
  success:    boolean
  configured: boolean
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

interface TimingConfig {
  failover_after_seconds:     number
  hysteresis_window_minutes:  number
  heartbeat_interval_seconds: number
}

interface CTDBConfig {
  enable:           boolean
  data_pool:        string
  data_dataset:     string
  public_addresses: string[]
  node_timeout:     number
  recovery_timeout: number
  log_level:        number
}

// HAPath identifies which HA topology this cluster is using.
// shared_storage = Path A': SCSI-3 PR hardware arbitrates writes (SAS/enterprise NVMe-oF)
// replicated     = Path B:  ZFS replication + watchdog self-fence (SATA/NVMe consumer)
type HAPath = 'shared_storage' | 'replicated' | 'unknown'

interface HWDetectResult {
  success:                boolean
  watchdog_available:     boolean
  watchdog_device:        string
  fenced_running:         boolean
  fenced_devices:         string[]
  pool_sg_devices:        string[]
  provisional_path:       HAPath
  provisional_path_label: string
  provisional_reason:     string
  probe_required:         boolean
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

function fmtDate(s?: string): string {
  if (!s) return 'Never'
  try { return new Date(s).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'short' }) }
  catch { return s }
}

function fmtUnix(ts?: number): string {
  if (!ts || ts <= 0) return 'Never'
  try { return new Date(ts * 1000).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'short' }) }
  catch { return 'Unknown' }
}

function fmtAgo(ts?: number): string {
  if (!ts || ts <= 0) return ''
  const secs = Math.floor(Date.now() / 1000) - ts
  if (secs < 60)   return `${secs}s ago`
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`
  return `${Math.floor(secs / 3600)}h ago`
}

// ─── PathBadge ────────────────────────────────────────────────────────────────
// Small inline badge displayed on the dashboard showing which HA path is active.

function PathBadge({ path, label, reason, probeRequired }: {
  path:          HAPath
  label:         string
  reason:        string
  probeRequired: boolean
}) {
  const isShared = path === 'shared_storage'
  const isUnknown = path === 'unknown'
  const color = isShared ? 'var(--success)' : isUnknown ? 'var(--text-tertiary)' : 'var(--primary)'
  const bg    = isShared ? 'var(--success-bg)' : isUnknown ? 'var(--surface)' : 'var(--primary-bg)'
  const border = isShared ? 'var(--success-border)' : isUnknown ? 'var(--border)' : 'hsla(var(--hue-primary),100%,72%,.2)'
  const icon  = isShared ? 'storage' : isUnknown ? 'help_outline' : 'sync'

  return (
    <div style={{
      display: 'flex', alignItems: 'flex-start', gap: 10, padding: '10px 14px',
      background: bg, border: `1px solid ${border}`, borderRadius: 'var(--radius-md)',
      marginBottom: 16,
    }}>
      <Icon name={icon} size={16} style={{ color, flexShrink: 0, marginTop: 1 }} />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 600, fontSize: 'var(--text-xs)', color }}>{label}</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 2 }}>{reason}</div>
        {probeRequired && (
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--warning)', marginTop: 3 }}>
            Run the PROUT probe in the SCSI-3 PR section below to confirm PR write support.
          </div>
        )}
      </div>
    </div>
  )
}

// ─── NodeCard ─────────────────────────────────────────────────────────────────

function NodeCard({ node, isLocal, canPromote, onPromote, onRemove, onFence, pending }: {
  node:        HANode
  isLocal:     boolean
  canPromote:  boolean
  onPromote?:  () => void
  onRemove:    () => void
  onFence?:    () => void
  pending:     boolean
}) {
  const isActive      = node.role  === 'active'
  const isHealthy     = node.state === 'healthy'
  const isDegraded    = node.state === 'degraded'
  const isUnreachable = node.state === 'unreachable'
  const dotColor = isHealthy ? 'var(--success)' : isDegraded ? 'var(--warning)' : isUnreachable ? 'var(--error)' : 'var(--text-tertiary)'
  const dotGlow  = isHealthy ? '0 0 5px var(--success)' : isDegraded ? '0 0 5px var(--warning)' : 'none'

  return (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 16, padding: '16px 20px',
      background: 'var(--bg-card)',
      border: `1px solid ${isActive ? 'var(--success-border)' : isLocal ? 'hsla(var(--hue-primary),100%,72%,.2)' : 'var(--border)'}`,
      borderRadius: 'var(--radius-lg)' }}>
      <div style={{
        width: 42, height: 42, borderRadius: 'var(--radius-md)', flexShrink: 0,
        background: isActive ? 'var(--success-bg)' : isLocal ? 'var(--primary-bg)' : 'var(--surface)',
        border: `1px solid ${isActive ? 'var(--success-border)' : isLocal ? 'hsla(var(--hue-primary),100%,72%,.2)' : 'var(--border)'}`,
        display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
        <Icon name="computer" size={22} style={{ color: isActive ? 'var(--success)' : isLocal ? 'var(--primary)' : 'var(--text-tertiary)' }} />
      </div>

      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 3 }}>
          <span style={{ fontWeight: 700 }}>{node.name ?? node.id}</span>
          {isLocal  && <span className="badge badge-primary">THIS NODE</span>}
          {isActive && <span className="badge badge-success">ACTIVE</span>}
          {!isActive && node.role === 'standby' && <span className="badge badge-neutral">STANDBY</span>}
          {isDegraded    && <span className="badge badge-warning">DEGRADED</span>}
          {isUnreachable && <span className="badge badge-error">UNREACHABLE</span>}
          {(node.missed_beats ?? 0) > 0 && !isUnreachable && (
            <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--warning)', fontFamily: 'var(--font-mono)' }}>
              {node.missed_beats} missed
            </span>
          )}
          <span style={{ width: 8, height: 8, borderRadius: '50%', background: dotColor, boxShadow: dotGlow, display: 'inline-block', flexShrink: 0 }} />
        </div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          {node.address   && <span style={{ fontFamily: 'var(--font-mono)' }}>{node.address}</span>}
          {node.id !== node.name && node.id && <span style={{ color: 'var(--text-tertiary)' }}>ID: {node.id}</span>}
          {node.last_seen && <span>Seen: {fmtDate(node.last_seen)}</span>}
          {node.version   && <span style={{ color: 'var(--text-tertiary)' }}>v{node.version}</span>}
        </div>
      </div>

      <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
        {isLocal && !isActive && onPromote && (
          <button onClick={onPromote} disabled={pending} className="btn btn-primary">
            <Icon name="upgrade" size={14} />Failover (Promote)
          </button>
        )}
        {!isLocal && onFence && !isActive && (
          <button onClick={onFence} disabled={pending} className="btn" style={{ color: 'var(--error)', borderColor: 'var(--error-border)' }}>
            <Icon name="power_settings_new" size={14} />Fence Node
          </button>
        )}
        {!isLocal && canPromote && !isActive && onPromote && (
          <button onClick={onPromote} disabled={pending} className="btn btn-primary">
            <Icon name="upgrade" size={14} />Promote
          </button>
        )}
        {!isLocal && (
          <button onClick={onRemove} disabled={pending} className="btn btn-danger">
            <Icon name="delete" size={13} />
          </button>
        )}
      </div>
    </div>
  )
}

// ─── AddPeerForm ──────────────────────────────────────────────────────────────

function AddPeerForm({ onAdd, pending }: {
  onAdd: (peer: { id: string; name: string; address: string; role: string }) => void
  pending: boolean
}) {
  const [id,      setId]      = useState('')
  const [name,    setName]    = useState('')
  const [address, setAddress] = useState('')
  const [role,    setRole]    = useState('standby')

  function submit() {
    if (!id.trim())      { toast.error('Node ID is required'); return }
    if (!address.trim()) { toast.error('Address is required'); return }
    onAdd({ id: id.trim(), name: name.trim() || id.trim(), address: address.trim(), role })
    setId(''); setName(''); setAddress('')
  }

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24 }}>
      <div style={{ fontWeight: 700, marginBottom: 16 }}>Register Peer Node</div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 120px', gap: 12, marginBottom: 12 }}>
        <label className="field">
          <span className="field-label">Node ID</span>
          <input value={id} onChange={e => setId(e.target.value)} placeholder="node-2"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </label>
        <label className="field">
          <span className="field-label">Display Name</span>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="NAS-2 (optional)" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Daemon Address</span>
          <input value={address} onChange={e => setAddress(e.target.value)} placeholder="http://192.168.1.11:9000"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </label>
        <label className="field">
          <span className="field-label">Initial Role</span>
          <select value={role} onChange={e => setRole(e.target.value)} className="input" style={{ appearance: 'none' }}>
            <option value="standby">Standby</option>
            <option value="active">Active</option>
          </select>
        </label>
      </div>
      <button onClick={submit} disabled={pending} className="btn btn-primary">
        <Icon name="add" size={15} />{pending ? 'Registering…' : 'Register Peer'}
      </button>
    </div>
  )
}

// ─── WitnessConfigForm ────────────────────────────────────────────────────────

interface WitnessTestResult { url: string; reachable: boolean }
interface WitnessTestResponse { quorum_satisfied: boolean; healthy: number; required: number; results: WitnessTestResult[] }

function WitnessConfigForm() {
  const qc = useQueryClient()
  const q  = useQuery({
    queryKey: ['ha', 'witness'],
    queryFn:  ({ signal }) => api.get<{ success: boolean; config: WitnessConfig }>('/api/ha/witness/configure', signal),
  })

  const [dirty, setDirty] = useState<{
    enable?: boolean; required?: number; timeout?: number; witnesses?: WitnessEntry[]
  }>({})
  const [testOut, setTestOut] = useState<WitnessTestResponse | null>(null)

  const serverCfg = q.data?.config
  const enable    = dirty.enable    ?? serverCfg?.enable             ?? false
  const required  = dirty.required  ?? serverCfg?.required_healthy   ?? 1
  const timeout   = dirty.timeout   ?? serverCfg?.timeout_secs       ?? 5
  const witnesses = dirty.witnesses ?? serverCfg?.witnesses          ?? []

  const setEnable    = (v: boolean)        => setDirty(p => ({ ...p, enable: v }))
  const setRequired  = (v: number)         => setDirty(p => ({ ...p, required: v }))
  const setTimeoutS  = (v: number)         => setDirty(p => ({ ...p, timeout: v }))
  const setWitnesses = (v: WitnessEntry[]) => setDirty(p => ({ ...p, witnesses: v }))

  const save = useMutation({
    mutationFn: (cfg: WitnessConfig) => api.post('/api/ha/witness/configure', cfg),
    onSuccess: () => {
      toast.success('Witness configuration saved')
      setDirty({})
      qc.invalidateQueries({ queryKey: ['ha', 'witness'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const test = useMutation({
    mutationFn: (cfg: WitnessConfig) => api.post<WitnessTestResponse>('/api/ha/witness/test', cfg),
    onSuccess: (data) => { setTestOut(data); if (data.quorum_satisfied) { toast.success('Witness quorum satisfied') } else { toast.error('Witness quorum NOT satisfied') } },
    onError: (e: Error) => toast.error(`Witness test failed: ${e.message}`),
  })

  function addWitness() {
    setWitnesses([...witnesses, { url: '', expected_status: 0, expected_body_regex: '', strict_tls: false }])
    setTestOut(null)
  }

  function removeWitness(i: number) {
    setWitnesses(witnesses.filter((_, idx) => idx !== i))
    setTestOut(null)
  }

  function updateWitness(i: number, patch: Partial<WitnessEntry>) {
    const w = [...witnesses]; w[i] = { ...w[i], ...patch }; setWitnesses(w); setTestOut(null)
  }

  const cfg: WitnessConfig = { enable, witnesses, required_healthy: required, timeout_secs: timeout }

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24, borderLeft: enable ? '4px solid var(--primary)' : '4px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="public" size={24} style={{ color: enable ? 'var(--primary)' : 'var(--text-tertiary)' }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontWeight: 700 }}>Quorum Witness Array</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            Proves this node has network access before firing failover - prevents split-brain in a partition
          </div>
        </div>
      </div>

      {/* Global settings row */}
      <div style={{ display: 'grid', gridTemplateColumns: '120px 120px 120px 1fr', gap: 12, marginBottom: 16, alignItems: 'end' }}>
        <label className="field">
          <span className="field-label">Enable</span>
          <select value={enable ? 'yes' : 'no'} onChange={e => setEnable(e.target.value === 'yes')} className="input">
            <option value="no">Disabled</option>
            <option value="yes">Active</option>
          </select>
        </label>
        <label className="field">
          <span className="field-label">Required Healthy</span>
          <input type="number" min={1} max={witnesses.length || 1} value={required}
            onChange={e => setRequired(Math.max(1, parseInt(e.target.value) || 1))} className="input" />
        </label>
        <label className="field">
          <span className="field-label">Timeout (s)</span>
          <input type="number" min={1} max={30} value={timeout}
            onChange={e => setTimeoutS(Math.max(1, parseInt(e.target.value) || 5))} className="input" />
        </label>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', paddingBottom: 6 }}>
          {witnesses.length > 0 ? `${required} of ${witnesses.length} witness${witnesses.length !== 1 ? 'es' : ''} must respond` : 'Add at least one witness URL below'}
        </div>
      </div>

      {/* Witness entries */}
      {witnesses.length > 0 && (
        <div style={{ marginBottom: 12 }}>
          {/* Header */}
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0,2fr) 70px minmax(0,1fr) 70px 36px', gap: 8, marginBottom: 4, padding: '0 4px' }}>
            {['URL', 'Status', 'Body Regex', 'TLS Cert', ''].map(h => (
              <span key={h} style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.05em' }}>{h}</span>
            ))}
          </div>
          {witnesses.map((w, i) => (
            <div key={i} style={{ display: 'grid', gridTemplateColumns: 'minmax(0,2fr) 70px minmax(0,1fr) 70px 36px', gap: 8, marginBottom: 8, alignItems: 'center' }}>
              <input value={w.url} onChange={e => updateWitness(i, { url: e.target.value })}
                placeholder="https://1.1.1.1" className="input" style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }} />
              <input type="number" min={0} max={599} value={w.expected_status}
                onChange={e => updateWitness(i, { expected_status: parseInt(e.target.value) || 0 })}
                placeholder="0" className="input" style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', textAlign: 'center' }} title="Expected HTTP status code. 0 = any valid response." />
              <input value={w.expected_body_regex} onChange={e => updateWitness(i, { expected_body_regex: e.target.value })}
                placeholder="optional regex" className="input" style={{ fontSize: 'var(--text-xs)' }} title="Regex matched against the first 1KB of the response body." />
              <label style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 6, cursor: 'pointer', height: 36 }}>
                <input type="checkbox" checked={w.strict_tls} onChange={e => updateWitness(i, { strict_tls: e.target.checked })} />
                <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', userSelect: 'none' }}>Verify</span>
              </label>
              <button onClick={() => removeWitness(i)} className="btn btn-danger" style={{ padding: '0 8px', height: 36, minWidth: 'unset' }}>
                <Icon name="delete" size={13} />
              </button>
            </div>
          ))}
        </div>
      )}

      {witnesses.length === 0 && (
        <div style={{ padding: '20px 0', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)', border: '2px dashed var(--border)', borderRadius: 'var(--radius-md)', marginBottom: 12 }}>
          No witnesses configured. Add a public DNS, local gateway, or any reachable HTTP endpoint.
        </div>
      )}

      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <button onClick={addWitness} className="btn btn-ghost">
          <Icon name="add" size={14} />Add Witness
        </button>
        {witnesses.length > 0 && (
          <button onClick={() => test.mutate(cfg)} disabled={test.isPending} className="btn btn-ghost">
            <Icon name="wifi_tethering" size={14} />{test.isPending ? 'Testing…' : 'Test All'}
          </button>
        )}
        <button onClick={() => save.mutate(cfg)} disabled={save.isPending || q.isLoading} className="btn btn-primary" style={{ marginLeft: 'auto' }}>
          <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Witness Config'}
        </button>
      </div>

      {/* Test results */}
      {testOut && (
        <div style={{ marginTop: 16, background: 'var(--surface)', borderRadius: 'var(--radius-md)', padding: '12px 16px', border: `1px solid ${testOut.quorum_satisfied ? 'var(--success-border)' : 'var(--error-border)'}` }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
            <Icon name={testOut.quorum_satisfied ? 'check_circle' : 'cancel'} size={16}
              style={{ color: testOut.quorum_satisfied ? 'var(--success)' : 'var(--error)' }} />
            <span style={{ fontWeight: 700, fontSize: 'var(--text-sm)', color: testOut.quorum_satisfied ? 'var(--success)' : 'var(--error)' }}>
              {testOut.quorum_satisfied ? 'Quorum satisfied' : 'Quorum NOT satisfied'} - {testOut.healthy}/{testOut.required} required healthy
            </span>
          </div>
          {testOut.results.map((r, i) => (
            <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4, fontSize: 'var(--text-xs)' }}>
              <Icon name={r.reachable ? 'check' : 'close'} size={13}
                style={{ color: r.reachable ? 'var(--success)' : 'var(--error)', flexShrink: 0 }} />
              <span style={{ fontFamily: 'var(--font-mono)', color: r.reachable ? 'var(--text)' : 'var(--text-tertiary)' }}>{r.url}</span>
              <span style={{ color: r.reachable ? 'var(--success)' : 'var(--error)' }}>{r.reachable ? 'reachable' : 'unreachable'}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ─── FencingConfigForm ────────────────────────────────────────────────────────

function FencingConfigForm() {
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

// ─── PDUConfigForm ────────────────────────────────────────────────────────────

function PDUConfigForm() {
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

// ─── SBDConfigForm ───────────────────────────────────────────────────────────

function SBDConfigForm() {
  const qc = useQueryClient()
  const q  = useQuery({
    queryKey: ['ha', 'sbd'],
    queryFn:  ({ signal }) => api.get<SBDResponse>('/api/ha/sbd/configure', signal),
  })

  const [dirty, setDirty] = useState<{ pool?: string; dataset?: string; ttl?: number }>({})

  const ss = q.data?.config
  const pool    = dirty.pool    ?? ss?.pool           ?? ''
  const dataset = dirty.dataset ?? ss?.dataset        ?? 'sbd-lease'
  const ttl     = dirty.ttl     ?? ss?.lease_ttl_secs ?? 30

  const setPool    = (v: string) => setDirty(p => ({ ...p, pool: v }))
  const setDataset = (v: string) => setDirty(p => ({ ...p, dataset: v }))
  const setTtl     = (v: number) => setDirty(p => ({ ...p, ttl: v }))

  const save = useMutation({
    mutationFn: (cfg: SBDConfig) => api.post('/api/ha/sbd/configure', cfg),
    onSuccess: () => { toast.success('SBD fencing configuration saved'); qc.invalidateQueries({ queryKey: ['ha', 'sbd'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (pool.trim() && !dataset.trim()) { toast.error('Dataset name is required when pool is set'); return }
    save.mutate({ pool: pool.trim(), dataset: dataset.trim() || 'sbd-lease', lease_ttl_secs: ttl })
  }

  const configured   = (q.data?.config?.pool ?? '') !== ''
  const leaseActive  = q.data?.lease_active === true
  const lastRenewal  = q.data?.last_renewal_unix ?? 0

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24, borderLeft: configured ? '4px solid var(--warning)' : '4px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <Icon name="dataset" size={24} style={{ color: configured ? 'var(--warning)' : 'var(--text-tertiary)' }} />
          <div>
            <div style={{ fontWeight: 700 }}>SBD Lease Fencing (ZFS token)</div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
              Self-fence via ZFS property lease - fences this node if it loses storage access (no BMC required)
            </div>
          </div>
        </div>
        {configured && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '4px 10px', borderRadius: 'var(--radius-md)', background: leaseActive ? 'var(--success-bg)' : 'var(--error-bg)', border: `1px solid ${leaseActive ? 'var(--success-border)' : 'var(--error-border)'}` }}>
            <div style={{ width: 7, height: 7, borderRadius: '50%', background: leaseActive ? 'var(--success)' : 'var(--error)', flexShrink: 0 }} />
            <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: leaseActive ? 'var(--success)' : 'var(--error)' }}>
              {leaseActive ? `Lease active${lastRenewal > 0 ? ' · ' + fmtAgo(lastRenewal) : ''}` : 'Lease manager not running'}
            </span>
          </div>
        )}
      </div>

      {!configured && (
        <div className="alert alert-info" style={{ marginBottom: 16, padding: '10px 14px' }}>
          <Icon name="info" size={16} />
          <div style={{ fontSize: 'var(--text-xs)' }}>
            SBD is opt-in. Leave Pool empty to keep this node in single-node mode with no lease overhead.
          </div>
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 120px', gap: 12, marginBottom: 16 }}>
        <label className="field">
          <span className="field-label">ZFS Pool</span>
          <input value={pool} onChange={e => setPool(e.target.value)} placeholder="tank (leave empty to disable)"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Dataset Name</span>
          <input value={dataset} onChange={e => setDataset(e.target.value)} placeholder="sbd-lease"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Lease TTL (s)</span>
          <input type="number" min={5} max={300} step={5} value={ttl}
            onChange={e => setTtl(Math.min(300, Math.max(5, parseInt(e.target.value) || 30)))}
            className="input" disabled={q.isLoading} />
        </label>
      </div>

      <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: '0 0 16px' }}>
        Lease renews every {Math.floor(ttl / 3)}s. If renewal stops for {ttl}s the node self-reboots via <code>reboot -f</code>. Requires the dataset <code>{pool || 'pool'}/{dataset || 'sbd-lease'}</code> to exist and be writable by root.
      </p>

      <button onClick={submit} disabled={save.isPending || q.isLoading} className="btn btn-primary"
        style={{ background: configured ? 'var(--warning)' : 'var(--primary)', color: 'var(--text-on-primary)', border: 'none' }}>
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save SBD Config'}
      </button>
    </div>
  )
}

// ─── NetworkWitnessForm ──────────────────────────────────────────────────────

interface NetworkWitnessConfig {
  enable:      boolean
  target:      string
  method:      'icmp' | 'http' | 'https'
  timeout_ms:  number
  count:       number
  description: string
}

interface NetworkWitnessProbeResult {
  reachable:   boolean
  latency_ms?: number
  error?:      string
}

function NetworkWitnessForm() {
  const qc = useQueryClient()
  const q  = useQuery({
    queryKey: ['ha', 'network-witness'],
    queryFn:  ({ signal }) => api.get<{ success: boolean; config: NetworkWitnessConfig }>('/api/ha/network-witness', signal),
  })

  const [dirty, setDirty] = useState<{
    enable?: boolean; target?: string; method?: 'icmp'|'http'|'https'; timeout?: number; count?: number; desc?: string
  }>({})
  const [probing, setProbing] = useState(false)
  const [probeResult, setProbeResult] = useState<NetworkWitnessProbeResult | null>(null)

  const nw = q.data?.config
  const enable  = dirty.enable  ?? nw?.enable      ?? false
  const target  = dirty.target  ?? nw?.target      ?? ''
  const method  = dirty.method  ?? nw?.method      ?? 'icmp'
  const timeout = dirty.timeout ?? nw?.timeout_ms  ?? 2000
  const count   = dirty.count   ?? nw?.count       ?? 3
  const desc    = dirty.desc    ?? nw?.description ?? ''

  const setEnable  = (v: boolean)               => setDirty(p => ({ ...p, enable: v }))
  const setTarget  = (v: string)                => setDirty(p => ({ ...p, target: v }))
  const setMethod  = (v: 'icmp'|'http'|'https') => setDirty(p => ({ ...p, method: v }))
  const setTimeout = (v: number)                => setDirty(p => ({ ...p, timeout: v }))
  const setCount   = (v: number)                => setDirty(p => ({ ...p, count: v }))
  const setDesc    = (v: string)                => setDirty(p => ({ ...p, desc: v }))

  const save = useMutation({
    mutationFn: (cfg: NetworkWitnessConfig) => api.post('/api/ha/network-witness', cfg),
    onSuccess: () => { toast.success('Network witness configuration saved'); qc.invalidateQueries({ queryKey: ['ha', 'network-witness'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  async function probe() {
    if (!target.trim()) { toast.error('Enter a target IP or URL first'); return }
    setProbing(true)
    setProbeResult(null)
    try {
      const r = await api.post<{ success: boolean; result: NetworkWitnessProbeResult }>(
        '/api/ha/network-witness/probe', { target: target.trim(), method })
      setProbeResult(r.result)
    } catch (e) {
      setProbeResult({ reachable: false, error: String(e) })
    } finally {
      setProbing(false)
    }
  }

  function submit() {
    if (enable && !target.trim()) { toast.error('Target IP or URL is required when witness is enabled'); return }
    save.mutate({ enable, target: target.trim(), method, timeout_ms: timeout, count, description: desc.trim() })
  }

  const configured = enable && target.trim() !== ''

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24, borderLeft: configured ? '4px solid var(--primary)' : '4px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <Icon name="network_check" size={24} style={{ color: configured ? 'var(--primary)' : 'var(--text-tertiary)' }} />
          <div>
            <div style={{ fontWeight: 700 }}>Network Quorum Witness</div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
              Neutral VPS or cloud endpoint that both nodes probe independently to detect network isolation. No software installation required on the target.
            </div>
          </div>
        </div>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
          <input type="checkbox" checked={enable} onChange={e => setEnable(e.target.checked)} />
          <span style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>Enabled</span>
        </label>
      </div>

      <div className="alert alert-info" style={{ marginBottom: 16, padding: '10px 14px' }}>
        <Icon name="info" size={16} />
        <div style={{ fontSize: 'var(--text-xs)' }}>
          Both nodes independently check if this target is reachable. If your node can reach it but the peer cannot, the peer is network-isolated and it is safe to promote. If <em>neither</em> node can reach it, do not promote — the cluster may be in a full partition. Any VPS, cloud metadata endpoint, or DNS server works; no software needed on the target.
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 120px', gap: 12, marginBottom: 12 }}>
        <label className="field">
          <span className="field-label">Target IP or URL</span>
          <input value={target} onChange={e => setTarget(e.target.value)}
            placeholder="e.g. 95.217.1.1 or https://witness.example.com"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Method</span>
          <select value={method} onChange={e => setMethod(e.target.value as 'icmp' | 'http' | 'https')} className="input" disabled={q.isLoading}>
            <option value="icmp">TCP Ping</option>
            <option value="http">HTTP</option>
            <option value="https">HTTPS</option>
          </select>
        </label>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12, marginBottom: 12 }}>
        <label className="field">
          <span className="field-label">Timeout (ms)</span>
          <input type="number" min={100} max={10000} step={100} value={timeout}
            onChange={e => setTimeout(Math.min(10000, Math.max(100, parseInt(e.target.value) || 2000)))}
            className="input" disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Probe Count</span>
          <input type="number" min={1} max={10} value={count}
            onChange={e => setCount(Math.min(10, Math.max(1, parseInt(e.target.value) || 3)))}
            className="input" disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Label (optional)</span>
          <input value={desc} onChange={e => setDesc(e.target.value)} placeholder="e.g. Hetzner VPS Frankfurt"
            className="input" disabled={q.isLoading} />
        </label>
      </div>

      {probeResult && (
        <div style={{ marginBottom: 14, padding: '8px 12px', borderRadius: 'var(--radius-md)', background: probeResult.reachable ? 'var(--success-bg)' : 'var(--error-bg)', border: `1px solid ${probeResult.reachable ? 'var(--success-border)' : 'var(--error-border)'}`, fontSize: 'var(--text-xs)' }}>
          {probeResult.reachable
            ? `✓ Reachable${probeResult.latency_ms !== undefined ? ` (avg ${probeResult.latency_ms}ms)` : ''}`
            : `✗ Unreachable — ${probeResult.error ?? 'no response'}`}
        </div>
      )}

      <div style={{ display: 'flex', gap: 10 }}>
        <button onClick={probe} disabled={probing || q.isLoading} className="btn btn-secondary">
          <Icon name="network_ping" size={15} />{probing ? 'Probing…' : 'Test Connectivity'}
        </button>
        <button onClick={submit} disabled={save.isPending || q.isLoading} className="btn btn-primary">
          <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Witness Config'}
        </button>
      </div>
    </div>
  )
}

// ─── ClusterSecretForm ───────────────────────────────────────────────────────

function ClusterSecretForm() {
  const qc = useQueryClient()
  const q  = useQuery({
    queryKey: ['ha', 'cluster-secret'],
    queryFn:  ({ signal }) => api.get<ClusterSecretStatus>('/api/ha/cluster-secret/configure', signal),
  })

  const [secret, setSecret] = useState('')
  const [show,   setShow]   = useState(false)

  function generateSecret() {
    const buf = new Uint8Array(32)
    crypto.getRandomValues(buf)
    setSecret(Array.from(buf).map(b => b.toString(16).padStart(2, '0')).join(''))
  }

  const save = useMutation({
    mutationFn: (s: string) => api.post('/api/ha/cluster-secret/configure', { secret: s }),
    onSuccess: () => {
      toast.success(secret === '' ? 'Cluster secret cleared' : 'Cluster secret updated - takes effect immediately')
      setSecret('')
      qc.invalidateQueries({ queryKey: ['ha', 'cluster-secret'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const configured = q.data?.configured === true

  return (
    <div className="card" style={{
      borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24,
      borderLeft: configured ? '4px solid var(--success)' : '4px solid var(--error)',
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="key" size={24} style={{ color: configured ? 'var(--success)' : 'var(--error)' }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontWeight: 700 }}>Peer Authentication Secret</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            Pre-shared key that peer daemons must include in every heartbeat. Prevents any host on the management network from injecting itself as a cluster member.
          </div>
        </div>
        <div style={{
          padding: '4px 10px', borderRadius: 'var(--radius-md)', fontSize: 'var(--text-xs)', fontWeight: 600,
          background: configured ? 'var(--success-bg)' : 'var(--error-bg)',
          border: `1px solid ${configured ? 'var(--success-border)' : 'var(--error-border)'}`,
          color: configured ? 'var(--success)' : 'var(--error)',
        }}>
          {configured ? 'Active' : 'Not Set'}
        </div>
      </div>

      {!configured && (
        <div className="alert alert-warning" style={{ marginBottom: 16, padding: '10px 14px' }}>
          <Icon name="warning" size={16} />
          <div style={{ fontSize: 'var(--text-xs)' }}>
            No peer secret is configured. Any host on the management network can register itself as a cluster peer and influence HA decisions. Set a secret on all nodes in the cluster.
          </div>
        </div>
      )}

      <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end' }}>
        <label className="field" style={{ flex: 1 }}>
          <span className="field-label">{configured ? 'New Secret (leave blank to clear)' : 'Secret'}</span>
          <div style={{ position: 'relative' }}>
            <input
              type={show ? 'text' : 'password'}
              value={secret}
              onChange={e => setSecret(e.target.value)}
              placeholder={configured ? 'Enter new secret to rotate…' : 'Enter secret or generate one'}
              className="input"
              style={{ fontFamily: 'var(--font-mono)', paddingRight: 36 }}
            />
            <button
              type="button"
              onClick={() => setShow(s => !s)}
              style={{ position: 'absolute', right: 8, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', cursor: 'pointer', padding: 0, color: 'var(--text-tertiary)' }}
            >
              <Icon name={show ? 'visibility_off' : 'visibility'} size={16} />
            </button>
          </div>
        </label>
        <button onClick={generateSecret} className="btn btn-ghost" style={{ flexShrink: 0, marginBottom: 1 }}>
          <Icon name="shuffle" size={14} />Generate
        </button>
        <button
          onClick={() => save.mutate(secret)}
          disabled={save.isPending || q.isLoading}
          className="btn btn-primary"
          style={{ flexShrink: 0, marginBottom: 1 }}
        >
          <Icon name="save" size={15} />{save.isPending ? 'Saving…' : configured ? 'Update Secret' : 'Set Secret'}
        </button>
      </div>

      {configured && (
        <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: '12px 0 0' }}>
          The secret is write-only. To rotate it, enter a new value and save. Set the same secret on all peer nodes before saving here to avoid disrupting active heartbeats.
        </p>
      )}
    </div>
  )
}

// ─── ReplicationConfigForm ────────────────────────────────────────────────────

function ReplicationConfigForm() {
  const qc = useQueryClient()
  const q  = useQuery({
    queryKey: ['ha', 'replication'],
    queryFn:  ({ signal }) => api.get<{ success: boolean; config: ReplicationConfig }>('/api/ha/replication/configure', signal),
  })

  const [dirtyCfg, setDirtyCfg] = useState<ReplicationConfig | null>(null)

  const defaultCfg: ReplicationConfig = {
    local_pool: '', remote_pool: '', remote_host: '', remote_user: 'root', remote_port: 22, ssh_key_path: '/root/.ssh/id_rsa', interval_secs: 30
  }
  const cfg = dirtyCfg ?? q.data?.config ?? defaultCfg
  const setCfg = (v: ReplicationConfig | ((prev: ReplicationConfig) => ReplicationConfig)) =>
    setDirtyCfg(typeof v === 'function' ? v(cfg) : v)

  const save = useMutation({
    mutationFn: (c: ReplicationConfig) => api.post('/api/ha/replication/configure', c),
    onSuccess: () => { toast.success('Replication configuration saved'); qc.invalidateQueries({ queryKey: ['ha', 'replication'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24, borderLeft: '4px solid var(--primary)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="sync" size={24} style={{ color: 'var(--primary)' }} />
        <div>
          <div style={{ fontWeight: 700 }}>Continuous Storage Replication (ZFS)</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Asynchronous Active-to-Standby ZFS snapshot shipping</div>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12, marginBottom: 12 }}>
        <label className="field">
          <span className="field-label">Local Pool</span>
          <input value={cfg.local_pool} onChange={e => setCfg({ ...cfg, local_pool: e.target.value })} placeholder="tank" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Remote Pool</span>
          <input value={cfg.remote_pool} onChange={e => setCfg({ ...cfg, remote_pool: e.target.value })} placeholder="tank" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Sync Interval (s)</span>
          <input type="number" min="10" value={cfg.interval_secs}
            onChange={e => setCfg({ ...cfg, interval_secs: parseInt(e.target.value) || 30 })} className="input" />
        </label>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12, marginBottom: 8 }}>
        <label className="field">
          <span className="field-label">Remote Host</span>
          <input value={cfg.remote_host} onChange={e => setCfg({ ...cfg, remote_host: e.target.value })}
            placeholder="10.0.0.11" className="input" />
        </label>
        <label className="field">
          <span className="field-label">SSH User & Port</span>
          <div style={{ display: 'flex', gap: 8 }}>
            <input value={cfg.remote_user} onChange={e => setCfg({ ...cfg, remote_user: e.target.value })}
              placeholder="root" className="input" style={{ flex: 2 }} />
            <input type="number" value={cfg.remote_port} onChange={e => setCfg({ ...cfg, remote_port: parseInt(e.target.value) || 22 })}
              className="input" style={{ flex: 1 }} />
          </div>
        </label>
        <label className="field">
          <span className="field-label">SSH Identity File</span>
          <input value={cfg.ssh_key_path} onChange={e => setCfg({ ...cfg, ssh_key_path: e.target.value })}
            placeholder="/root/.ssh/id_ed25519" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </label>
      </div>
      <p style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', margin: '0 0 16px', lineHeight: 1.5 }}>
        Pool names: letters, digits, <code>_</code> <code>-</code> <code>.</code> — max 255 chars, must start with a letter.
        SSH user: lowercase Linux username (e.g. <code>root</code>).
        Identity file: absolute path, no spaces or shell special characters.
      </p>

      <button onClick={() => save.mutate(cfg)} disabled={save.isPending || q.isLoading} className="btn btn-primary">
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Replication Config'}
      </button>
    </div>
  )
}

// ─── SCSIFencingCard ──────────────────────────────────────────────────────────
// Shows dplane-fenced reservation status and lets the operator run the full
// PROUT round-trip probe before trusting shared-storage fencing. The probe
// catches drives that answer PRIN READ KEYS (read-only capability check) but
// reject PROUT REGISTER - the false-positive that silently arms the cluster
// with broken fencing.

function SCSIFencingCard() {
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

// ─── WatchdogConfigForm ───────────────────────────────────────────────────────

function WatchdogConfigForm() {
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
            <strong>Self-fence is active.</strong> If this node loses quorum and cannot reach any configured witness,
            it will stop petting the watchdog and the kernel will hard-reset it after <strong>{timeout}s</strong>.
            Ensure <code>timeout_secs</code> is less than <code>failover_after_seconds</code> in timing config so the
            survivor knows the loser is gone before promoting.
          </div>
        </div>
      )}

      {!enable && (
        <div style={{ marginBottom: 16, padding: '10px 14px', borderRadius: 'var(--radius-md)', background: 'var(--surface)', border: '1px solid var(--border)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.6 }}>
          When enabled, the daemon writes to the watchdog device on every heartbeat while the cluster has quorum.
          If quorum is lost <em>and</em> no witness can be reached, the daemon stops writing and the kernel resets
          the node after the timeout - guaranteeing the survivor can promote without needing to reach the peer's BMC.
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

// ─── TimingConfigForm ──────────────────────────────────────────────────────────

function TimingConfigForm() {
  const qc = useQueryClient()
  const q  = useQuery({
    queryKey: ['ha', 'timing'],
    queryFn:  ({ signal }) => api.get<{ success: boolean; note?: string } & TimingConfig>('/api/ha/timing', signal),
  })

  const [dirty, setDirty] = useState<{ failover?: number; hysteresis?: number; heartbeat?: number }>({})

  const tc = q.data
  const failover   = dirty.failover   ?? tc?.failover_after_seconds     ?? 45
  const hysteresis = dirty.hysteresis ?? tc?.hysteresis_window_minutes  ?? 60
  const heartbeat  = dirty.heartbeat  ?? tc?.heartbeat_interval_seconds ?? 15

  const setFailover   = (v: number) => setDirty(p => ({ ...p, failover: v }))
  const setHysteresis = (v: number) => setDirty(p => ({ ...p, hysteresis: v }))
  const setHeartbeat  = (v: number) => setDirty(p => ({ ...p, heartbeat: v }))

  const save = useMutation({
    mutationFn: (cfg: TimingConfig) => api.post('/api/ha/timing', cfg),
    onSuccess: () => {
      toast.success('Timing configuration saved')
      qc.invalidateQueries({ queryKey: ['ha', 'timing'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (failover < heartbeat * 3) {
      toast.error(`Failover threshold must be >= heartbeat × 3 (${heartbeat * 3}s)`)
      return
    }
    save.mutate({
      failover_after_seconds:     failover,
      hysteresis_window_minutes:  hysteresis,
      heartbeat_interval_seconds: heartbeat,
    })
  }

  const minFailover = heartbeat * 3

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24, borderLeft: '4px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="schedule" size={24} style={{ color: 'var(--text-tertiary)' }} />
        <div>
          <div style={{ fontWeight: 700 }}>Cluster Timing</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            Failover threshold, heartbeat interval, and flap-guard window
          </div>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12, marginBottom: 12 }}>
        <label className="field">
          <span className="field-label">Failover After (s)</span>
          <input type="number" min={minFailover} max={300} value={failover}
            onChange={e => setFailover(Math.max(minFailover, parseInt(e.target.value) || 45))}
            className="input" disabled={q.isLoading}
            title={`Peer must be unreachable for this many seconds before STONITH fires. Minimum: heartbeat × 3 = ${minFailover}s`} />
        </label>
        <label className="field">
          <span className="field-label">Heartbeat (s)</span>
          <input type="number" min={5} max={60} value={heartbeat}
            onChange={e => setHeartbeat(Math.max(5, parseInt(e.target.value) || 15))}
            className="input" disabled={q.isLoading}
            title="How often this node pings its peers. Shorter = faster detection, higher network load." />
        </label>
        <label className="field">
          <span className="field-label">Flap Guard (min)</span>
          <input type="number" min={1} max={1440} value={hysteresis}
            onChange={e => setHysteresis(Math.max(1, parseInt(e.target.value) || 60))}
            className="input" disabled={q.isLoading}
            title="Auto-failover is suppressed for this many minutes after a failover to prevent flapping." />
        </label>
      </div>

      {failover < minFailover && (
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--error)', marginBottom: 10 }}>
          Failover threshold must be at least {minFailover}s (heartbeat × 3) to allow three missed beats before triggering STONITH.
        </div>
      )}

      <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 14, lineHeight: 1.5 }}>
        <strong>Hysteresis</strong> (flap guard) takes effect immediately.
        <strong> Failover threshold</strong> and <strong>heartbeat interval</strong> require a daemon restart.
        {q.data?.note && <span> {q.data.note}</span>}
      </div>

      <button onClick={submit} disabled={save.isPending || q.isLoading || failover < minFailover} className="btn btn-primary">
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Timing Config'}
      </button>
    </div>
  )
}

// ─── MaintenanceModeCard ──────────────────────────────────────────────────────

function MaintenanceModeCard({ active, until, onToggle }: {
  active:   boolean
  until:    number
  onToggle: (seconds: number) => void
}) {
  const [duration, setDuration] = useState(300)
  const [rem,      setRem]      = useState(0)

  useEffect(() => {
    if (!active || !until) {
      // Schedule state update as a callback to avoid synchronous setState in effect body
      const t = setTimeout(() => setRem(0), 0)
      return () => clearTimeout(t)
    }
    const timer = setInterval(() => {
      const s = Math.max(0, until - Math.floor(Date.now() / 1000))
      setRem(s)
      if (s <= 0) clearInterval(timer)
    }, 1000)
    return () => clearInterval(timer)
  }, [active, until])

  const fmtRem = (s: number) => `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`

  return (
    <div className="card" style={{
      borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24,
      borderLeft: active ? '4px solid var(--warning)' : '4px solid var(--border)',
      background: active ? 'var(--warning-bg)' : 'var(--bg-card)'
    }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: active ? 0 : 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <Icon name="build_circle" size={24} style={{ color: active ? 'var(--warning)' : 'var(--text-tertiary)' }} />
          <div>
            <div style={{ fontWeight: 700 }}>Maintenance Mode</div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
              {active
                ? `All fencing suspended. Auto-resumes in ${fmtRem(rem)}`
                : 'Enable during scheduled maintenance to suspend automated fencing.'}
            </div>
          </div>
        </div>
        <button onClick={() => onToggle(active ? 0 : duration)}
          className={`btn ${active ? 'btn-warning' : 'btn-ghost'}`}
          style={{ border: active ? 'none' : '1px solid var(--border)' }}>
          {active ? 'Exit Maintenance' : 'Enter Maintenance'}
        </button>
      </div>
      {!active && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Duration:</span>
          <select value={duration} onChange={e => setDuration(parseInt(e.target.value))} className="input"
            style={{ width: 140, height: 32, padding: '0 8px', fontSize: 'var(--text-xs)' }}>
            <option value={300}>5 Minutes</option>
            <option value={900}>15 Minutes</option>
            <option value={1800}>30 Minutes</option>
            <option value={3600}>1 Hour</option>
            <option value={14400}>4 Hours</option>
          </select>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontStyle: 'italic' }}>
            Fencing automatically resumes after timeout.
          </span>
        </div>
      )}
    </div>
  )
}

// ─── HAPage ───────────────────────────────────────────────────────────────────

export function HAPage() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()

  const statusQ = useQuery({
    queryKey: ['ha', 'status'],
    queryFn:  ({ signal }) => api.get<HAStatusResponse>('/api/ha/status', signal),
    refetchInterval: 15_000,
  })

  const localQ = useQuery({
    queryKey: ['ha', 'local'],
    queryFn:  ({ signal }) => api.get<HALocalResponse>('/api/ha/local', signal),
    refetchInterval: 30_000,
  })

  const secretQ = useQuery({
    queryKey: ['ha', 'cluster-secret'],
    queryFn:  ({ signal }) => api.get<ClusterSecretStatus>('/api/ha/cluster-secret/configure', signal),
  })

  // nodeRefreshKey increments after structural mutations (add/remove peer, promote)
  // so useFrozenLayout updates the snapshot even if the poll hasn't fired yet.
  const [nodeRefreshKey, setNodeRefreshKey] = useState(0)

  const addPeer = useMutation({
    mutationFn: (peer: { id: string; name: string; address: string; role: string }) =>
      api.post('/api/ha/peers', peer),
    onSuccess: () => {
      toast.success('Peer registered - heartbeat starting')
      qc.invalidateQueries({ queryKey: ['ha', 'status'] })
      setNodeRefreshKey(k => k + 1)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const removePeer = useMutation({
    mutationFn: (id: string) => api.delete(`/api/ha/peers/${encodeURIComponent(id)}`),
    onSuccess: () => {
      toast.success('Peer removed')
      qc.invalidateQueries({ queryKey: ['ha', 'status'] })
      setNodeRefreshKey(k => k + 1)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const promotePeer = useMutation({
    mutationFn: ({ id }: { id: string; name: string }) =>
      api.post(`/api/ha/peers/${encodeURIComponent(id)}/role`, { role: 'active' }),
    onSuccess: (_data, { name }) => {
      toast.success(`${name} promoted to active`)
      qc.invalidateQueries({ queryKey: ['ha', 'status'] })
      setNodeRefreshKey(k => k + 1)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const fencePeer = useMutation({
    mutationFn: (id: string) => api.post('/api/ha/fence', { node_id: id }),
    onSuccess: () => toast.success('Fencing sequence initiated asynchronously.'),
    onError: (e: Error) => toast.error(`Fencing dispatch failed: ${e.message}`),
  })

  const localPromote = useMutation({
    mutationFn: () => api.post('/api/ha/promote', {}),
    onSuccess: () => { toast.success('Local failover triggered.'); qc.invalidateQueries({ queryKey: ['ha', 'status'] }) },
    onError: (e: Error) => toast.error(`Promotion failed: ${e.message}`),
  })

  const clearFault = useMutation({
    mutationFn: () => api.post('/api/ha/clear_fault', {}),
    onSuccess: () => { toast.success('Fault cleared. Auto-failover re-enabled.'); qc.invalidateQueries({ queryKey: ['ha', 'status'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const [jobId, setJobId] = useState<string | null>(null)
  const [showHAConsole, setShowHAConsole] = useState(false)
  const setJob = useJobStore(s => s.setActiveJob)

  // blockState holds the structured block response from the backend so the
  // UI can render a guided workflow rather than a raw error string.
  const [haBlock, setHaBlock] = useState<{
    code: string; error: string; guide: string; action: string
  } | null>(null)

  const switchover = useMutation({
    mutationFn: () => api.post<{ success: boolean; job_id?: string; error?: string }>(
      '/api/ha/switchover', {}),
    onSuccess: (data) => {
      if (!data.success) {
        toast.error(data.error ?? 'Primary handoff failed')
        return
      }
      if (data.job_id) {
        setJobId(data.job_id)
        setJob(data.job_id, 'Switching Primary to Standby')
        setHaBlock(null) // Clear the block panel - switchover started
      }
    },
    onError: (e: Error) => toast.error(`Primary handoff failed: ${e.message}`),
  })

  const toggleHA = useMutation({
    mutationFn: ({ enable, force = false }: { enable: boolean; force?: boolean }) =>
      api.post<{ success: boolean; job_id?: string; warnings?: string[]; error?: string; guide?: string; code?: string; action?: string }>(
        '/api/ha/toggle', { enable, force }),
    onSuccess: (data, vars) => {
      if (!data.success) {
        if (data.code) {
          // Structured block - render a guided workflow panel, not a toast
          setHaBlock({
            code:   data.code,
            error:  data.error  ?? 'Operation blocked',
            guide:  data.guide  ?? '',
            action: data.action ?? '',
          })
        } else {
          toast.error(data.error ?? 'HA toggle failed')
        }
        return
      }
      setHaBlock(null)
      if (data.warnings?.length) {
        data.warnings.forEach(w => toast.warning(w))
      }
      if (data.job_id) {
        setJobId(data.job_id)
        setJob(data.job_id, vars.enable ? 'Enabling HA' : 'Disabling HA')
      } else {
        toast.success(vars.enable ? 'High Availability enabled.' : 'High Availability disabled.')
        qc.invalidateQueries({ queryKey: ['ha', 'status'] })
      }
    },
    onError: (e: Error) => toast.error(`HA toggle failed: ${e.message}`),
  })

  const toggleMaintenance = useMutation({
    mutationFn: (seconds: number) => api.post('/api/ha/maintenance', { seconds }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['ha', 'status'] }); toast.success('Maintenance mode updated') },
    onError: (e: Error) => toast.error(e.message),
  })

  const [wizardStep, setWizardStep] = useState<number | null>(null)

  // selectedPath is the HA topology the operator chose (or detected) for this cluster.
  // It drives the path-aware wizard steps and the dissolution RPO warning.
  const [selectedPath, setSelectedPath] = useState<HAPath | null>(null)

  // Hardware detection: non-destructive scan run once per page load.
  // Full PROUT write probe is operator-triggered via SCSIFencingCard.
  const hwDetectQ = useQuery({
    queryKey: ['ha', 'hardware-detect'],
    queryFn:  ({ signal }) => api.get<HWDetectResult>('/api/ha/hardware/detect', signal),
    staleTime: 5 * 60 * 1000,
  })

  // Effective path: operator selection overrides auto-detection.
  const effectivePath: HAPath = selectedPath ?? hwDetectQ.data?.provisional_path ?? 'unknown'

  // RPO-aware disable: for replicated clusters warn about the non-zero RPO
  // before tearing down the replication link.
  async function handleDisableHA(force = false) {
    if (!force && effectivePath === 'replicated') {
      const ok = await confirm({
        title:        'Disable HA - Non-Zero RPO',
        message:      'This cluster uses ZFS replication. The standby copy may lag behind the active node by up to the replication interval. Before disabling: note which node holds the most recent data, and ensure no writes are in progress. The standby will keep its last-replicated snapshot.',
        danger:       true,
        confirmLabel: 'Disable HA',
        confirmText:  'DISABLE',
      })
      if (!ok) return
    }
    toggleHA.mutate({ enable: false, force })
  }

  const ws = useWsStore()
  const [haReplProgress, setHaReplProgress] = useState<Record<string, unknown> | null>(null)
  const haReplClearRef = useRef<number | undefined>(undefined)

  useEffect(() => {
    const off = ws.on('haReplicationProgress', (d) => {
      setHaReplProgress(d)
      if (haReplClearRef.current) window.clearTimeout(haReplClearRef.current)
      haReplClearRef.current = window.setTimeout(() => setHaReplProgress(null), 25_000)
    })
    return () => {
      off()
      if (haReplClearRef.current) window.clearTimeout(haReplClearRef.current)
    }
  }, [ws])

  const pending = addPeer.isPending || removePeer.isPending || promotePeer.isPending || fencePeer.isPending || localPromote.isPending

  const cluster    = statusQ.data?.cluster ?? {}
  const localID    = localQ.data?.id ?? localQ.data?.node_id ?? ''
  const localNode: HANode | null = localID ? {
    id:      localID,
    name:    localQ.data?.name,
    address: localQ.data?.address,
    role:    localQ.data?.role ?? 'active',
    state:   'healthy',
  } : cluster.local_node ?? null

  const peers    = cluster.peers ?? []
  const allNodes = localNode ? [localNode, ...peers] : peers

  // Frozen layout: node list structure is stable across 15s poll ticks.
  // Only adds/removes update the DOM order; health/state metrics update in place.
  const { snapshot: nodeSnapshot, liveById: nodeLive, forceRefresh: refreshNodes } =
    useFrozenLayout(allNodes, n => n.id, nodeRefreshKey)

  const hasQuorum = cluster.quorum === true
  const haEnabled = cluster.ha_enabled === true
  const activeNode = cluster.active_node ?? allNodes.find(n => n.role === 'active')
  const subordinate = cluster.subordinate_mode === true
  const hysteresis  = cluster.hysteresis_active  === true
  const lastFailover = cluster.last_failover_at ?? 0

  if (statusQ.isLoading || localQ.isLoading) return <Skeleton height={360} />
  if (statusQ.isError) return (
    <ErrorState error={statusQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['ha', 'status'] })} />
  )

  // ── Setup Wizard (path-aware) ────────────────────────────────────────────────
  if (wizardStep !== null) {
    const isSharedStorage = effectivePath === 'shared_storage'
    const TOTAL_STEPS = 8
    const detect = hwDetectQ.data

    return (
      <div style={{ maxWidth: 700, margin: '0 auto' }}>
        <div style={{ marginBottom: 32 }}>
          <button onClick={() => setWizardStep(null)} className="btn btn-ghost" style={{ marginBottom: 12 }}>
            <Icon name="arrow_back" size={14} />Cancel Wizard
          </button>
          <h1 className="page-title">High Availability Setup</h1>
          {/* Path pill in wizard header */}
          {effectivePath !== 'unknown' && (
            <div style={{ display: 'inline-flex', alignItems: 'center', gap: 6, marginTop: 8, padding: '3px 10px', borderRadius: 'var(--radius-md)', fontSize: 'var(--text-xs)', fontWeight: 600, background: isSharedStorage ? 'var(--success-bg)' : 'var(--primary-bg)', color: isSharedStorage ? 'var(--success)' : 'var(--primary)', border: `1px solid ${isSharedStorage ? 'var(--success-border)' : 'hsla(var(--hue-primary),100%,72%,.2)'}` }}>
              <Icon name={isSharedStorage ? 'storage' : 'sync'} size={12} />
              {isSharedStorage ? 'Path A’: Shared Storage' : 'Path B: Replicated ZFS'}
            </div>
          )}
          <div style={{ display: 'flex', gap: 4, marginTop: 16 }}>
            {Array.from({ length: TOTAL_STEPS }, (_, i) => i + 1).map(s => (
              <div key={s} style={{ height: 4, flex: 1, borderRadius: 2, background: s <= wizardStep ? 'var(--primary)' : 'var(--border)', opacity: s === wizardStep ? 1 : 0.4 }} />
            ))}
          </div>
          <div style={{ marginTop: 8, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Step {wizardStep} of {TOTAL_STEPS}</div>
        </div>

        {/* ── Step 1: Hardware Detection + Path Selection ── */}
        {wizardStep === 1 && (
          <div className="card fade-in" style={{ padding: 32 }}>
            <Icon name="hardware" size={48} style={{ color: 'var(--primary)', marginBottom: 20 }} />
            <h2 style={{ marginBottom: 12 }}>Step 1: Hardware Detection</h2>
            <p style={{ color: 'var(--text-secondary)', lineHeight: 1.6, marginBottom: 24 }}>
              D-PlaneOS HA supports two topologies. The right one depends on your hardware.
              The system has scanned your node and is recommending a path below.
            </p>

            {hwDetectQ.isLoading && (
              <div style={{ padding: '20px 0', textAlign: 'center', color: 'var(--text-tertiary)' }}>
                <Icon name="radar" size={28} style={{ marginBottom: 8, display: 'block', margin: '0 auto 8px' }} />
                Scanning hardware…
              </div>
            )}

            {detect && (
              <>
                {/* Hardware summary chips */}
                <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 20 }}>
                  <span style={{ padding: '4px 10px', borderRadius: 'var(--radius-md)', fontSize: 'var(--text-xs)', fontWeight: 600, background: detect.watchdog_available ? 'var(--success-bg)' : 'var(--surface)', border: `1px solid ${detect.watchdog_available ? 'var(--success-border)' : 'var(--border)'}`, color: detect.watchdog_available ? 'var(--success)' : 'var(--text-tertiary)' }}>
                    <Icon name="timer" size={11} style={{ verticalAlign: 'middle', marginRight: 4 }} />
                    Watchdog: {detect.watchdog_available ? detect.watchdog_device : 'not found'}
                  </span>
                  <span style={{ padding: '4px 10px', borderRadius: 'var(--radius-md)', fontSize: 'var(--text-xs)', fontWeight: 600, background: detect.fenced_running ? 'var(--success-bg)' : 'var(--surface)', border: `1px solid ${detect.fenced_running ? 'var(--success-border)' : 'var(--border)'}`, color: detect.fenced_running ? 'var(--success)' : 'var(--text-tertiary)' }}>
                    <Icon name="lock" size={11} style={{ verticalAlign: 'middle', marginRight: 4 }} />
                    dplane-fenced: {detect.fenced_running ? `${detect.fenced_devices.length} device(s) reserved` : 'not running'}
                  </span>
                  <span style={{ padding: '4px 10px', borderRadius: 'var(--radius-md)', fontSize: 'var(--text-xs)', fontWeight: 600, background: 'var(--surface)', border: '1px solid var(--border)', color: 'var(--text-secondary)' }}>
                    <Icon name="storage" size={11} style={{ verticalAlign: 'middle', marginRight: 4 }} />
                    SG devices: {detect.pool_sg_devices.length} in pool
                  </span>
                </div>

                {/* Path choice */}
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginBottom: 24 }}>
                  {(['shared_storage', 'replicated'] as HAPath[]).map(p => {
                    const isP = p === 'shared_storage'
                    const chosen = effectivePath === p
                    const recommended = detect.provisional_path === p
                    return (
                      <button
                        key={p}
                        onClick={() => setSelectedPath(p)}
                        style={{
                          padding: '16px', textAlign: 'left', borderRadius: 'var(--radius-lg)', cursor: 'pointer',
                          background: chosen ? (isP ? 'var(--success-bg)' : 'var(--primary-bg)') : 'var(--surface)',
                          border: `2px solid ${chosen ? (isP ? 'var(--success)' : 'var(--primary)') : 'var(--border)'}`,
                          transition: 'border-color 0.15s',
                        }}
                      >
                        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
                          <Icon name={isP ? 'storage' : 'sync'} size={18} style={{ color: chosen ? (isP ? 'var(--success)' : 'var(--primary)') : 'var(--text-tertiary)' }} />
                          <span style={{ fontWeight: 700, fontSize: 'var(--text-sm)' }}>
                            {isP ? 'Path A’: Shared Storage' : 'Path B: Replicated ZFS'}
                          </span>
                          {recommended && <span className="badge badge-primary" style={{ fontSize: 'var(--text-2xs)', marginLeft: 'auto' }}>Detected</span>}
                        </div>
                        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.5 }}>
                          {isP
                            ? 'SAS/enterprise storage with SCSI-3 PR. Hardware arbitrates writes - zero RPO, clean dissolution, no replication lag.'
                            : 'SATA/NVMe consumer hardware. ZFS replication + watchdog self-fence. Non-zero RPO; dissolution requires choosing canonical copy.'}
                        </div>
                      </button>
                    )
                  })}
                </div>

                {detect.probe_required && effectivePath === 'shared_storage' && (
                  <div className="alert alert-warning" style={{ marginBottom: 20 }}>
                    <Icon name="warning" size={16} />
                    <div style={{ fontSize: 'var(--text-xs)' }}>
                      Confirm SCSI-3 PR write support in Step 5. The PROUT probe is the only reliable test - drives that pass the read-only check can still reject writes.
                    </div>
                  </div>
                )}

                {effectivePath === 'replicated' && (
                  <div className="alert alert-info" style={{ marginBottom: 20 }}>
                    <Icon name="info" size={16} />
                    <div style={{ fontSize: 'var(--text-xs)' }}>
                      <strong>Non-zero RPO.</strong> The standby may lag behind by up to the replication interval. A quorum witness is required in Step 7 for the hardware watchdog to safely distinguish a partition from a dead peer.
                    </div>
                  </div>
                )}
              </>
            )}

            <button onClick={() => setWizardStep(2)} disabled={effectivePath === 'unknown'} className="btn btn-primary btn-lg" style={{ width: '100%', justifyContent: 'center' }}>
              Continue with {effectivePath === 'shared_storage' ? 'Shared Storage' : effectivePath === 'replicated' ? 'Replicated ZFS' : '…'} <Icon name="arrow_forward" size={16} />
            </button>
          </div>
        )}

        {/* ── Step 2: Enable HA ── */}
        {wizardStep === 2 && (
          <div className="card fade-in" style={{ padding: 32 }}>
            <h2 style={{ marginBottom: 8 }}>Step 2: Enable HA Service Mesh</h2>
            <p style={{ color: 'var(--text-secondary)', marginBottom: 24 }}>Enable Patroni + etcd + HAProxy layers in NixOS. This node initialises as the leader if no cluster exists.</p>
            <div style={{ background: 'var(--surface)', padding: 20, borderRadius: 'var(--radius-md)', marginBottom: 24, border: '1px solid var(--border)' }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <div>
                  <div style={{ fontWeight: 700 }}>HA Stack</div>
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Patroni + etcd + HAProxy</div>
                </div>
                <button onClick={() => toggleHA.mutate({ enable: !haEnabled })} disabled={toggleHA.isPending || !!jobId}
                  className={`btn ${haEnabled ? 'btn-danger' : 'btn-success'}`}>
                  {haEnabled ? 'Disable HA' : 'Enable HA'}
                </button>
              </div>
            </div>
            {jobId && (
              <div style={{ marginBottom: 24 }}>
                <JobProgress jobId={jobId} runningLabel="Rebuilding NixOS with HA modules…" doneLabel="NixOS Rebuild Complete"
                  onDone={() => { setJobId(null); qc.invalidateQueries({ queryKey: ['ha', 'status'] }) }}
                  onFailed={() => setJobId(null)} />
                <button onClick={() => setShowHAConsole(true)} className="btn btn-ghost btn-sm" style={{ marginTop: 8 }}>
                  <Icon name="terminal" size={13} />View Rebuild Logs
                </button>
                {showHAConsole && <JobConsole jobId={jobId} title="HA Rebuild Console" onClose={() => setShowHAConsole(false)} />}
              </div>
            )}
            <div style={{ display: 'flex', gap: 12 }}>
              <button onClick={() => setWizardStep(1)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(3)} disabled={!haEnabled || !!jobId} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: Peer Registration
              </button>
            </div>
          </div>
        )}

        {/* ── Step 3: Peer Registration ── */}
        {wizardStep === 3 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 3: Register Peer Nodes</h2>
            <p style={{ color: 'var(--text-secondary)', marginBottom: 24 }}>Register the other nodes in your cluster so they can begin heartbeating.</p>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 24 }}>
              {peers.map(p => <NodeCard key={p.id} node={p} isLocal={false} canPromote={false} onRemove={() => removePeer.mutate(p.id)} pending={pending} />)}
              {peers.length === 0 && <div style={{ padding: 32, textAlign: 'center', color: 'var(--text-tertiary)', border: '2px dashed var(--border)', borderRadius: 'var(--radius-lg)' }}>No peers registered yet</div>}
            </div>
            <AddPeerForm onAdd={p => addPeer.mutate(p)} pending={addPeer.isPending} />
            <div style={{ display: 'flex', gap: 12, marginTop: 32 }}>
              <button onClick={() => setWizardStep(2)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(4)} disabled={peers.length === 0} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: Peer Security
              </button>
            </div>
          </div>
        )}

        {/* ── Step 4: Peer Authentication ── */}
        {wizardStep === 4 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 4: Peer Authentication</h2>
            <p style={{ color: 'var(--text-secondary)', marginBottom: 16 }}>
              Set a shared secret so only authorised nodes can register themselves as cluster peers. Both nodes must use the same secret.
            </p>
            <div className="alert alert-warning" style={{ marginBottom: 24 }}>
              <Icon name="warning" size={18} />
              <div>Without a secret, any host on the management network that can reach port 9000 can inject itself as a cluster peer and affect failover decisions.</div>
            </div>
            <ClusterSecretForm />
            <div style={{ display: 'flex', gap: 12, marginTop: 32 }}>
              <button onClick={() => setWizardStep(3)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(5)} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: {isSharedStorage ? 'SCSI-3 PR Validation' : 'Storage Replication'}
              </button>
            </div>
          </div>
        )}

        {/* ── Step 5: Storage Layer (PATH-AWARE) ── */}
        {wizardStep === 5 && (
          <div className="fade-in">
            {isSharedStorage ? (
              <>
                <h2 style={{ marginBottom: 8 }}>Step 5: Verify SCSI-3 PR Support</h2>
                <p style={{ color: 'var(--text-secondary)', marginBottom: 16 }}>
                  Run the PROUT write probe to confirm your pool disks support SCSI-3 Persistent Reservations.
                  This is the hardware mechanism that prevents dual-write corruption - the disk controller
                  rejects writes from any node that does not hold the reservation.
                </p>
                <div className="alert alert-warning" style={{ marginBottom: 24 }}>
                  <Icon name="warning" size={18} />
                  <div style={{ lineHeight: 1.5 }}>
                    <strong>Do not skip this step.</strong> Drives that answer PRIN READ KEYS (read-only check)
                    can still silently reject PROUT REGISTER (the write that arms fencing). A false positive
                    here means the cluster will appear healthy but fail to fence during a real partition.
                  </div>
                </div>
                <SCSIFencingCard />
              </>
            ) : (
              <>
                <h2 style={{ marginBottom: 8 }}>Step 5: Storage Replication</h2>
                <p style={{ color: 'var(--text-secondary)', marginBottom: 16 }}>
                  Configure continuous ZFS snapshot shipping from the active node to the standby.
                  This is how data reaches the standby in Path B - the standby receives periodic snapshots
                  and lags by up to the replication interval (non-zero RPO).
                </p>
                <div className="alert alert-info" style={{ marginBottom: 24 }}>
                  <Icon name="info" size={18} />
                  <div>
                    The standby pool is kept read-only between snapshots. On failover, the promoter
                    sets readonly=off and promotes any clones before restarting services.
                  </div>
                </div>
                <ReplicationConfigForm />
              </>
            )}
            <div style={{ display: 'flex', gap: 12, marginTop: 32 }}>
              <button onClick={() => setWizardStep(4)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(6)} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: {isSharedStorage ? 'Fencing' : 'Fencing'}
              </button>
            </div>
          </div>
        )}

        {/* ── Step 6: Fencing (PATH-AWARE) ── */}
        {wizardStep === 6 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 6: Fencing</h2>
            {isSharedStorage ? (
              <>
                <p style={{ color: 'var(--text-secondary)', marginBottom: 16 }}>
                  The hardware watchdog is the recommended fencing floor for Path A’.
                  SCSI-3 PR already prevents dual-write at the disk level - the watchdog
                  removes the BMC/PDU network dependency so the survivor never needs to
                  reach the peer to confirm it is gone.
                </p>
                <div className="alert alert-info" style={{ marginBottom: 16 }}>
                  <Icon name="info" size={18} />
                  <div style={{ lineHeight: 1.5 }}>
                    IPMI and PDU are optional secondary methods on Path A’.
                    The disk reservation provides hardware-level write exclusivity even without them.
                    Add them for defence in depth if your hardware has BMC.
                  </div>
                </div>
              </>
            ) : (
              <>
                <p style={{ color: 'var(--text-secondary)', marginBottom: 16 }}>
                  On Path B there is no disk-level arbiter - the watchdog self-fence is your primary
                  protection. Configure it first. IPMI and PDU are important secondary methods.
                </p>
                <div className="alert alert-warning" style={{ marginBottom: 16 }}>
                  <Icon name="warning" size={18} />
                  <div style={{ lineHeight: 1.5 }}>
                    <strong>Watchdog is critical on Path B.</strong> Without it, a network partition where
                    both nodes are alive but cannot see each other has no automatic resolution - the cluster
                    stalls until operator intervention. With the watchdog and a configured witness, the
                    isolated side self-fences and the connected side promotes automatically.
                  </div>
                </div>
              </>
            )}
            <WatchdogConfigForm />
            <FencingConfigForm />
            <PDUConfigForm />
            <div style={{ display: 'flex', gap: 12, marginTop: 32 }}>
              <button onClick={() => setWizardStep(5)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(7)} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: Quorum Witness
              </button>
            </div>
          </div>
        )}

        {/* ── Step 7: Witness (PATH-AWARE) ── */}
        {wizardStep === 7 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 7: Quorum Witness</h2>
            {isSharedStorage ? (
              <>
                <p style={{ color: 'var(--text-secondary)', marginBottom: 16 }}>
                  A quorum witness is recommended on Path A’. SCSI-3 PR already prevents
                  dual-write, so the witness adds an extra layer of safety for the control plane
                  (Patroni promotion decisions) rather than being the primary split-brain guard.
                </p>
                <div className="alert alert-info" style={{ marginBottom: 24 }}>
                  <Icon name="info" size={18} />
                  <div>Add your local gateway, a public DNS (1.1.1.1 or 8.8.8.8), or any stable HTTP endpoint reachable from both nodes but independent of the peer.</div>
                </div>
              </>
            ) : (
              <>
                <p style={{ color: 'var(--text-secondary)', marginBottom: 16 }}>
                  A quorum witness is <strong>required</strong> on Path B when the hardware watchdog is enabled.
                  Without it, a two-node partition where both nodes are alive and the peer is simply unreachable
                  looks identical to a dead peer - the watchdog cannot distinguish between them and will self-fence
                  the survivor along with the loser.
                </p>
                <div className="alert alert-warning" style={{ marginBottom: 24 }}>
                  <Icon name="warning" size={18} />
                  <div style={{ lineHeight: 1.5 }}>
                    <strong>Required for safe self-fence.</strong> The watchdog only stops petting when
                    quorum is lost <em>and</em> all witnesses are unreachable. Without at least one witness,
                    any peer unreachability event (maintenance, reboot, cable pull) will trigger an
                    unintended self-fence.
                  </div>
                </div>
              </>
            )}
            <WitnessConfigForm />
            <NetworkWitnessForm />
            <div style={{ display: 'flex', gap: 12, marginTop: 32 }}>
              <button onClick={() => setWizardStep(6)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(8)} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: Review &amp; Finish
              </button>
            </div>
          </div>
        )}

        {/* ── Step 8: Finish (PATH-AWARE) ── */}
        {wizardStep === 8 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 8: Setup Complete</h2>
            {isSharedStorage ? (
              <>
                <div className="alert alert-success" style={{ marginBottom: 24 }}>
                  <Icon name="check_circle" size={18} />
                  <div>
                    <strong>Path A’: Shared Storage cluster configured.</strong> The disk controller
                    arbitrates writes via SCSI-3 PR. Failover is hardware-guaranteed: the losing node's
                    writes are rejected at the controller level before the survivor imports the pool.
                  </div>
                </div>
                <div className="card" style={{ padding: '16px 20px', marginBottom: 16, borderLeft: '4px solid var(--success)' }}>
                  <div style={{ fontWeight: 700, marginBottom: 8 }}>Dissolution (Disable HA)</div>
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.6 }}>
                    Clean and lossless. Disable HA from the dashboard - this node keeps the pool imported
                    with every byte intact. No replication lag, no canonical-copy decision, no data migration.
                    The second node can be repurposed immediately.
                  </div>
                </div>
              </>
            ) : (
              <>
                <div className="alert alert-success" style={{ marginBottom: 24 }}>
                  <Icon name="check_circle" size={18} />
                  <div>
                    <strong>Path B: Replicated ZFS cluster configured.</strong> ZFS replication provides
                    the standby copy. The hardware watchdog self-fence ensures the loser resets before the
                    survivor promotes.
                  </div>
                </div>
                <div className="card" style={{ padding: '16px 20px', marginBottom: 16, borderLeft: '4px solid var(--warning)' }}>
                  <div style={{ fontWeight: 700, marginBottom: 8 }}>
                    <Icon name="warning" size={14} style={{ verticalAlign: 'middle', marginRight: 6, color: 'var(--warning)' }} />
                    Dissolution (Disable HA) - Non-Zero RPO
                  </div>
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.6 }}>
                    Before disabling HA, confirm which node holds the most recent data
                    (compare ZFS TXG or check the last replication timestamp). The standby snapshot may
                    lag by up to the replication interval. The system will prompt you to confirm before
                    tearing down the replication link.
                  </div>
                </div>
              </>
            )}
            <SBDConfigForm />
            <div style={{ display: 'flex', gap: 12, marginTop: 24 }}>
              <button onClick={() => setWizardStep(7)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(null)} className="btn btn-success" style={{ flex: 1, justifyContent: 'center' }}>
                Finish &amp; Go to Dashboard
              </button>
            </div>
          </div>
        )}

        <ConfirmDialog />
      </div>
    )
  }

  // ── Empty / Disabled state ──────────────────────────────────────────────────
  if (!haEnabled && peers.length === 0) {
    return (
      <div style={{ maxWidth: 800, margin: '60px auto', textAlign: 'center' }}>
        <Icon name="topology" size={64} style={{ color: 'var(--text-tertiary)', opacity: 0.2, marginBottom: 24 }} />
        <h1>High Availability is Off</h1>
        <p style={{ color: 'var(--text-secondary)', maxWidth: 520, margin: '16px auto 12px' }}>
          This node is running as a standalone system. All NAS features (storage, shares, replication, Docker) are fully operational without HA.
        </p>
        <p style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)', maxWidth: 480, margin: '0 auto 32px' }}>
          Enable HA when you are ready to add a second node for automatic failover and Virtual IP migration. HA can be enabled and disabled at any time from this page.
        </p>
        <div style={{ display: 'flex', gap: 12, justifyContent: 'center', flexWrap: 'wrap' }}>
          <button
            onClick={() => toggleHA.mutate({ enable: true })}
            disabled={toggleHA.isPending}
            className="btn btn-primary btn-lg"
          >
            <Icon name="toggle_on" size={18} />
            {toggleHA.isPending ? 'Enabling…' : 'Enable High Availability'}
          </button>
          <button onClick={() => { setSelectedPath(null); qc.invalidateQueries({ queryKey: ['ha', 'hardware-detect'] }); setWizardStep(1) }} className="btn btn-ghost">
            <Icon name="settings" size={14} />Setup Wizard
          </button>
        </div>
        {toggleHA.isError && (
          <p style={{ color: 'var(--error)', marginTop: 16, fontSize: 'var(--text-sm)' }}>
            {String((toggleHA.error as Error)?.message ?? 'Failed to enable HA')}
          </p>
        )}
        {haBlock && (
          <div style={{ marginTop: 20, maxWidth: 520, margin: '20px auto 0', padding: '16px 20px', borderRadius: 'var(--radius-lg)', background: 'var(--warning-bg)', borderLeft: '4px solid var(--warning)', textAlign: 'left' }}>
            <div style={{ fontWeight: 600, marginBottom: 8 }}>{haBlock.error}</div>
            {haBlock.guide && (
              <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 12 }}>
                {haBlock.guide}
              </div>
            )}
            <button onClick={() => setHaBlock(null)} className="btn btn-ghost" style={{ fontSize: 'var(--text-xs)' }}>Dismiss</button>
          </div>
        )}
      </div>
    )
  }

  // ── Main Dashboard ──────────────────────────────────────────────────────────
  return (
    <div style={{ maxWidth: 900 }}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 28 }}>
        <div>
          <h1 className="page-title">HA Cluster</h1>
          <p className="page-subtitle">High availability - nodes, quorum and failover</p>
        </div>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
          <button onClick={() => { setSelectedPath(null); qc.invalidateQueries({ queryKey: ['ha', 'hardware-detect'] }); setWizardStep(1) }} className="btn btn-ghost">
            <Icon name="settings" size={14} />Setup Wizard
          </button>
          <button onClick={() => {
            qc.invalidateQueries({ queryKey: ['ha', 'status'] })
            qc.invalidateQueries({ queryKey: ['ha', 'local'] })
            refreshNodes()
          }} className="btn btn-ghost">
            <Icon name="refresh" size={14} />Refresh
          </button>
          <button
            onClick={() => handleDisableHA()}
            disabled={toggleHA.isPending}
            className="btn btn-ghost"
            style={{ color: 'var(--error)', borderColor: 'var(--error-border)' }}
            title="Disable HA. If this node is the Patroni primary, run patronictl switchover first. The system remains fully operational as a standalone node after disable."
          >
            <Icon name="toggle_off" size={14} />
            {toggleHA.isPending ? 'Disabling…' : 'Disable HA'}
          </button>
          {haBlock?.code === 'patroni_primary' && (
            <button
              onClick={() => handleDisableHA(true)}
              disabled={toggleHA.isPending}
              className="btn btn-ghost"
              style={{ color: 'var(--error)', fontSize: 'var(--text-xs)' }}
              title="Force disable without primary handoff. Active database connections will be dropped."
            >
              <Icon name="warning" size={13} />Force Disable
            </button>
          )}
        </div>
      </div>

      <SplitPanel />
      <QuorumPanel />
      <ProtectionCard />
      <GroupsPanel />

      {cluster.last_promotion && (cluster.last_promotion.failed || cluster.last_promotion.degraded) && (
        <div role="alert" className="card" style={{ marginBottom: 20, border: '1px solid var(--error-border)', background: 'var(--error-bg)' }}>
          <strong style={{ color: 'var(--error)' }}>
            {cluster.last_promotion.failed
              ? 'The last automatic promotion failed: this node does not serve the data.'
              : 'The last automatic promotion is incomplete: the pools are imported, but some steps failed.'}
          </strong>
          <ul style={{ fontSize: 'var(--text-sm)', margin: '8px 0 0' }}>
            {cluster.last_promotion.steps.filter(s => !s.ok && !s.skipped).map(s => <li key={s.name}><strong>{s.name}</strong>: {s.detail}</li>)}
          </ul>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 6 }}>{new Date(cluster.last_promotion.finished_at).toLocaleString()}</div>
        </div>
      )}

      {/* ── Stat Cards ──────────────────────────────────────────────────────── */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 16, marginBottom: 20 }}>
        {/* Quorum */}
        <div style={{ background: 'var(--bg-card)', borderRadius: 'var(--radius-lg)', padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14, border: `1px solid ${hasQuorum ? 'var(--success-border)' : 'var(--error-border)'}` }}>
          <Icon name={hasQuorum ? 'verified' : 'dangerous'} size={28} style={{ color: hasQuorum ? 'var(--success)' : 'var(--error)', flexShrink: 0 }} />
          <div>
            <div style={{ fontWeight: 700, color: hasQuorum ? 'var(--success)' : 'var(--error)' }}>{hasQuorum ? 'Quorum' : 'No Quorum'}</div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Cluster state</div>
          </div>
        </div>

        {/* Node count */}
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14 }}>
          <Icon name="computer" size={28} style={{ color: 'var(--primary)', flexShrink: 0 }} />
          <div>
            <div style={{ fontWeight: 700, fontSize: 28, fontFamily: 'var(--font-mono)', lineHeight: 1 }}>{allNodes.length}</div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Total nodes</div>
          </div>
        </div>

        {/* Active node */}
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14 }}>
          <Icon name="star" size={28} style={{ color: 'var(--warning)', flexShrink: 0 }} />
          <div style={{ minWidth: 0 }}>
            <div style={{ fontWeight: 700, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {activeNode?.name ?? activeNode?.id ?? '-'}
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Active node</div>
          </div>
        </div>

        {/* Last failover */}
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14, border: hysteresis ? '1px solid var(--warning-border)' : undefined }}>
          <Icon name="history" size={28} style={{ color: hysteresis ? 'var(--warning)' : 'var(--text-tertiary)', flexShrink: 0 }} />
          <div style={{ minWidth: 0 }}>
            <div style={{ fontWeight: 700, fontSize: lastFailover > 0 ? 'var(--text-sm)' : undefined, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', color: hysteresis ? 'var(--warning)' : undefined }}>
              {lastFailover > 0 ? fmtAgo(lastFailover) : 'Never'}
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              {lastFailover > 0 ? fmtUnix(lastFailover) : 'Last failover'}
            </div>
          </div>
        </div>
      </div>

      {/* ── Operational Banners ──────────────────────────────────────────────── */}

      {haReplProgress && (haReplProgress.bytes_sent != null || haReplProgress.percent != null) && (
        <div className="alert alert-info" style={{ marginBottom: 16, borderRadius: 'var(--radius-lg)' }}>
          <Icon name="sync" size={18} style={{ animation: 'spin 2s linear infinite' }} />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ fontWeight: 700 }}>Continuous ZFS replication (active → standby)</div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)', marginTop: 4 }}>
              {String(haReplProgress.local_pool ?? '')} → {String(haReplProgress.remote_host ?? '')}
              {typeof haReplProgress.rate_mbs === 'number' && ` · ${haReplProgress.rate_mbs.toFixed(1)} MB/s`}
              {typeof haReplProgress.percent === 'number' && ` · ${haReplProgress.percent.toFixed(1)}%`}
            </div>
            <div className="progress" style={{ height: 6, marginTop: 8, background: 'rgba(255,255,255,0.08)', borderRadius: 4, overflow: 'hidden' }}>
              <div
                className="progress-fill"
                style={{
                  height: '100%',
                  width: `${Math.min(100, Math.max(0, Number(haReplProgress.percent) || 0))}%`,
                  background: 'var(--primary)',
                  transition: 'width 0.4s ease-out',
                }}
              />
            </div>
          </div>
        </div>
      )}

      {/* Physical triage panel - shown when a peer node has been unreachable long enough to
          indicate a real failure rather than a transient blip. Guides the operator through
          the physical checks required before triggering a manual fence or promotion. */}
      {peers.some(p => p.state === 'unreachable') && (
        <div className="card" style={{
          marginBottom: 16, borderRadius: 'var(--radius-lg)', padding: '20px 24px',
          borderLeft: '4px solid var(--error)', background: 'var(--error-bg)',
        }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
            <Icon name="hardware" size={24} style={{ color: 'var(--error)', flexShrink: 0 }} />
            <div style={{ flex: 1 }}>
              <div style={{ fontWeight: 700, color: 'var(--error)' }}>
                Node Unreachable - Physical Triage Required
              </div>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 2 }}>
                {peers.filter(p => p.state === 'unreachable').map(p => p.name ?? p.id).join(', ')} has not responded to health checks.
                Before promoting or fencing, verify the physical cause. Promoting without confirmation risks split-brain data corruption.
              </div>
            </div>
          </div>

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginBottom: 16 }}>
            {[
              { icon: 'cable',           label: 'SAS / Storage Cable',   check: 'Reseat or replace the SAS/NVMe cable between the disk shelf and HBA. A frayed cable causes intermittent I/O errors that look like node failure.' },
              { icon: 'memory',          label: 'Disk Controller / HBA', check: 'Check HBA LEDs and logs. A failed controller will take the node down even if the OS is healthy. Look for SCSI errors in journalctl -u dplaned.' },
              { icon: 'router',          label: 'Management Network',    check: 'Ping the peer on the management VLAN. A dead core switch or misconfigured VLAN will trigger the unreachable state without any storage failure.' },
              { icon: 'developer_board', label: 'BMC / IPMI Console',    check: 'Open the out-of-band management console. If the node is truly dead the BMC is your only view. Check for kernel panics, OOM kills, or hardware faults in the SOL log.' },
            ].map(item => (
              <div key={item.label} style={{
                background: 'var(--bg-card)', borderRadius: 'var(--radius-md)',
                padding: '12px 14px', border: '1px solid var(--error-border)',
              }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
                  <Icon name={item.icon} size={16} style={{ color: 'var(--error)', flexShrink: 0 }} />
                  <span style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>{item.label}</span>
                </div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.5 }}>{item.check}</div>
              </div>
            ))}
          </div>

          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 12, padding: '8px 12px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)' }}>
            <strong>Hysteresis:</strong> Auto-failover is suppressed for 60 minutes after a recent failover to prevent flapping.
            If the peer is confirmed dead and you need to act immediately, use Clear Fault to override, then fence and promote below.
          </div>

          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            {peers.filter(p => p.state === 'unreachable').map(p => (
              <button
                key={p.id}
                onClick={async () => {
                  if (await confirm({
                    title: `STONITH: Terminate ${p.name ?? p.id}?`,
                    message: 'I have physically verified the node is down or isolated. Proceed with chassis power-off via out-of-band management (IPMI BMC or PDU). This will import ZFS pools on this node and promote it to active.',
                    danger: true,
                    confirmLabel: 'Fence & Promote',
                    confirmText: 'STONITH',
                  })) {
                    fencePeer.mutate(p.id)
                  }
                }}
                disabled={pending}
                className="btn btn-danger"
              >
                <Icon name="power_settings_new" size={14} />Fence {p.name ?? p.id} & Promote
              </button>
            ))}
            <button
              onClick={() => toggleMaintenance.mutate(1800)}
              disabled={toggleMaintenance.isPending}
              className="btn btn-warning"
            >
              <Icon name="build_circle" size={14} />Enter 30-min Maintenance
            </button>
          </div>
        </div>
      )}

      {/* Peer authentication warning - shown when HA has peers but no secret is set */}
      {peers.length > 0 && secretQ.data?.configured === false && (
        <div className="alert alert-warning" style={{ marginBottom: 16, borderRadius: 'var(--radius-lg)' }}>
          <Icon name="key_off" size={20} />
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 700, marginBottom: 4 }}>Peer Authentication Not Configured</div>
            <div style={{ fontSize: 'var(--text-xs)', opacity: 0.85 }}>
              Any host on the management network can register itself as a cluster peer. Set a shared secret in the Peer Authentication section below (or via <code>--ha-cluster-secret</code>) on all nodes.
            </div>
          </div>
          <button onClick={() => setWizardStep(4)} className="btn btn-warning" style={{ flexShrink: 0 }}>
            <Icon name="key" size={14} />Configure
          </button>
        </div>
      )}

      {/* Subordinate Mode - highest priority */}
      {subordinate && (
        <div className="alert alert-warning" style={{ marginBottom: 16, borderRadius: 'var(--radius-lg)' }}>
          <Icon name="sync" size={20} />
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 700, marginBottom: 4 }}>Catch-Up Sync in Progress - Subordinate Mode Active</div>
            <div style={{ fontSize: 'var(--text-xs)', opacity: 0.85 }}>
              This node booted with stale ZFS data and is receiving a full catch-up sync from the active peer. Auto-failover is disabled until the sync completes to prevent serving outdated files.
              If the sync is stuck or you have resolved it manually, use Clear Fault below.
            </div>
          </div>
          <button onClick={async () => {
            if (await confirm({ title: 'Clear Fault?', message: 'This will disable Subordinate Mode and re-enable auto-failover immediately, even if the catch-up sync has not completed. Only do this if you have manually verified data integrity.', danger: true, confirmLabel: 'Clear Fault' })) {
              clearFault.mutate()
            }
          }} disabled={clearFault.isPending} className="btn btn-warning" style={{ flexShrink: 0 }}>
            <Icon name="lock_open" size={14} />Clear Fault
          </button>
        </div>
      )}

      {/* Hysteresis - flap guard */}
      {hysteresis && !subordinate && (
        <div className="alert alert-warning" style={{ marginBottom: 16, borderRadius: 'var(--radius-lg)' }}>
          <Icon name="schedule" size={20} />
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 700, marginBottom: 4 }}>Flap Guard Active - Auto-Failover Suppressed</div>
            <div style={{ fontSize: 'var(--text-xs)', opacity: 0.85 }}>
              A failover occurred {fmtAgo(lastFailover)}. Auto-failover is suppressed for 60 minutes after a failover to prevent ping-pong flapping on an unstable network. Use "Clear Fault" once the root cause has been resolved.
            </div>
          </div>
          <button onClick={async () => {
            if (await confirm({ title: 'Clear Fault & Re-enable Auto-Failover?', message: `Last failover: ${fmtUnix(lastFailover)}. Clearing the fault re-enables automated STONITH immediately. Only proceed once you have confirmed the root cause of the last failover has been resolved.`, danger: true, confirmLabel: 'Re-enable Auto-Failover' })) {
              clearFault.mutate()
            }
          }} disabled={clearFault.isPending} className="btn btn-warning" style={{ flexShrink: 0 }}>
            <Icon name="check_circle" size={14} />{clearFault.isPending ? 'Clearing…' : 'Clear Fault'}
          </button>
        </div>
      )}

      {/* ── Topology path indicator ──────────────────────────────────────────── */}
      {hwDetectQ.data && (
        <PathBadge
          path={effectivePath}
          label={hwDetectQ.data.provisional_path_label}
          reason={hwDetectQ.data.provisional_reason}
          probeRequired={hwDetectQ.data.probe_required && effectivePath === 'shared_storage'}
        />
      )}

      {/* ── Node List ────────────────────────────────────────────────────────── */}
      <div style={{ fontWeight: 700, marginBottom: 12 }}>Nodes</div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {(nodeSnapshot ?? allNodes).map(snapshotNode => {
          // Structure from snapshot (stable across polls), metrics from live data
          const node = nodeLive.get(snapshotNode.id) ?? snapshotNode
          return (
            <NodeCard
              key={node.id}
              node={node}
              isLocal={node.id === localID}
              canPromote={(nodeSnapshot ?? allNodes).length >= 2}
              onPromote={async () => {
                if (node.id === localID) {
                  if (await confirm({ title: 'Assume Primary Role Locally?', message: 'This node will force-import all storage pools and execute the failover protocol. Ensure the current active node is offline or fenced first to prevent split-brain.', danger: true, confirmLabel: 'Failover Now', confirmText: 'FAILOVER' })) {
                    localPromote.mutate()
                  }
                } else {
                  if (await confirm({ title: `Promote ${node.name ?? node.id}?`, message: 'Registers this peer as the active node. Promotion propagates through Patroni.', danger: false, confirmLabel: 'Promote' })) {
                    promotePeer.mutate({ id: node.id, name: node.name ?? node.id })
                  }
                }
              }}
              onRemove={async () => {
                if (await confirm({ title: `Remove ${node.name ?? node.id}?`, message: 'This node will be removed from the cluster tracking pool.', danger: true, confirmLabel: 'Remove' })) {
                  removePeer.mutate(node.id)
                }
              }}
              onFence={async () => {
                if (await confirm({ title: `STONITH: Terminate ${node.name ?? node.id}?`, message: 'Issues a chassis power-off via out-of-band management (IPMI BMC or PDU). Data loss may occur if the node has unsynchronised writes. Proceed?', danger: true, confirmLabel: 'Terminate Chassis', confirmText: 'STONITH' })) {
                  fencePeer.mutate(node.id)
                }
              }}
              pending={pending}
            />
          )
        })}

        {allNodes.length === 0 && (
          <div className="card" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '48px 0', gap: 12, borderRadius: 'var(--radius-lg)' }}>
            <Icon name="device_hub" size={40} style={{ color: 'var(--text-tertiary)', opacity: 0.4 }} />
            <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No cluster nodes found</div>
            <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>Register peer nodes below to form a cluster</div>
          </div>
        )}
      </div>

      {/* ── Guided action panel - shown when an operation is blocked ─────────── */}
      {haBlock && (
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginBottom: 24, borderLeft: '4px solid var(--warning)', background: 'var(--warning-bg)' }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14, marginBottom: 16 }}>
            <Icon name="info" size={22} style={{ color: 'var(--warning)', flexShrink: 0, marginTop: 2 }} />
            <div>
              <div style={{ fontWeight: 700, marginBottom: 6 }}>{haBlock.error}</div>
              {haBlock.guide && (
                <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', lineHeight: 1.6 }}>
                  {haBlock.guide}
                </div>
              )}
            </div>
          </div>

          <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap' }}>
            {haBlock.action === 'switchover' && (
              <button
                onClick={() => switchover.mutate()}
                disabled={switchover.isPending || !!jobId}
                className="btn btn-primary"
              >
                <Icon name="swap_horiz" size={16} />
                {switchover.isPending ? 'Handing off primary…' : 'Switch Primary to Standby'}
              </button>
            )}
            {haBlock.action === 'configure_fencing' && (
              <button
                onClick={() => {
                  setHaBlock(null)
                  // Scroll to the fencing configuration section
                  document.getElementById('fencing-config-section')?.scrollIntoView({ behavior: 'smooth', block: 'start' })
                }}
                className="btn btn-primary"
              >
                <Icon name="security" size={16} />Configure Fencing
              </button>
            )}
            <button
              onClick={() => setHaBlock(null)}
              className="btn btn-ghost"
            >
              Dismiss
            </button>
          </div>

          {switchover.isError && (
            <div style={{ marginTop: 12, padding: '8px 12px', borderRadius: 'var(--radius-sm)', background: 'var(--error-bg)', color: 'var(--error)', fontSize: 'var(--text-xs)' }}>
              {String((switchover.error as Error)?.message ?? 'Switchover failed')}
            </div>
          )}
        </div>
      )}

      <AddPeerForm onAdd={peer => addPeer.mutate(peer)} pending={pending} />

      {/* ── Configuration Section ─────────────────────────────────────────────  */}
      <div style={{ marginTop: 40, marginBottom: 12, fontWeight: 700, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: '0.07em' }}>
        Configuration
      </div>

      <MaintenanceModeCard
        active={cluster.maintenance_active || false}
        until={cluster.maintenance_until  || 0}
        onToggle={(secs) => toggleMaintenance.mutate(secs)}
      />
      <ClusterSecretForm />
      <WitnessConfigForm />
      <div id="fencing-config-section">
        <FencingConfigForm />
        <PDUConfigForm />
        <WatchdogConfigForm />
        <SCSIFencingCard />
      </div>
      <SBDConfigForm />
      <NetworkWitnessForm />
      <ReplicationConfigForm />
      <TimingConfigForm />
      <CTDBConfig />

      <ConfirmDialog />
    </div>
  )
}
