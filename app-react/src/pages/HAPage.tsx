/**
 * pages/HAPage.tsx — High Availability Cluster
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
 *   GET  /api/ha/replication/configure
 *   POST /api/ha/replication/configure
 */

import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { JobProgress } from '@/components/ui/JobProgress'
import { useJobStore } from '@/stores/jobs'
import { JobConsole } from '@/components/ui/JobConsole'

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
  enable:            boolean
  bmc_ip:            string
  bmc_user:          string
  bmc_password_file: string
  jitter_max_ms:     number
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
      border: `1px solid ${isActive ? 'rgba(16,185,129,0.25)' : isLocal ? 'rgba(138,156,255,0.2)' : 'var(--border)'}`,
      borderRadius: 'var(--radius-lg)' }}>
      <div style={{
        width: 42, height: 42, borderRadius: 'var(--radius-md)', flexShrink: 0,
        background: isActive ? 'rgba(16,185,129,0.1)' : isLocal ? 'var(--primary-bg)' : 'var(--surface)',
        border: `1px solid ${isActive ? 'rgba(16,185,129,0.25)' : isLocal ? 'rgba(138,156,255,0.2)' : 'var(--border)'}`,
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
          <button onClick={onFence} disabled={pending} className="btn" style={{ color: 'var(--error)', borderColor: 'rgba(239,68,68,0.3)' }}>
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

  const [enable,    setEnable]    = useState(false)
  const [required,  setRequired]  = useState(1)
  const [timeout,   setTimeoutS]  = useState(5)
  const [witnesses, setWitnesses] = useState<WitnessEntry[]>([])
  const [testOut,   setTestOut]   = useState<WitnessTestResponse | null>(null)

  useEffect(() => {
    if (q.data?.config) {
      const c = q.data.config
      setEnable(c.enable)
      setRequired(c.required_healthy || 1)
      setTimeoutS(c.timeout_secs || 5)
      setWitnesses(c.witnesses || [])
    }
  }, [q.data])

  const save = useMutation({
    mutationFn: (cfg: WitnessConfig) => api.post('/api/ha/witness/configure', cfg),
    onSuccess: () => { toast.success('Witness configuration saved'); qc.invalidateQueries({ queryKey: ['ha', 'witness'] }) },
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
            Proves this node has network access before firing failover — prevents split-brain in a partition
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
        <div style={{ marginTop: 16, background: 'var(--surface)', borderRadius: 'var(--radius-md)', padding: '12px 16px', border: `1px solid ${testOut.quorum_satisfied ? 'rgba(16,185,129,0.3)' : 'rgba(239,68,68,0.3)'}` }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
            <Icon name={testOut.quorum_satisfied ? 'check_circle' : 'cancel'} size={16}
              style={{ color: testOut.quorum_satisfied ? 'var(--success)' : 'var(--error)' }} />
            <span style={{ fontWeight: 700, fontSize: 'var(--text-sm)', color: testOut.quorum_satisfied ? 'var(--success)' : 'var(--error)' }}>
              {testOut.quorum_satisfied ? 'Quorum satisfied' : 'Quorum NOT satisfied'} — {testOut.healthy}/{testOut.required} required healthy
            </span>
          </div>
          {testOut.results.map((r, i) => (
            <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4, fontSize: 'var(--text-xs)' }}>
              <Icon name={r.reachable ? 'check' : 'close'} size={13}
                style={{ color: r.reachable ? 'var(--success)' : 'var(--error)', flexShrink: 0 }} />
              <span style={{ fontFamily: 'var(--font-mono)', color: r.reachable ? 'var(--text-primary)' : 'var(--text-tertiary)' }}>{r.url}</span>
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

  const [enable,    setEnable]    = useState(false)
  const [ip,        setIp]        = useState('')
  const [user,      setUser]      = useState('')
  const [passFile,  setPassFile]  = useState('')
  const [jitterMs,  setJitterMs]  = useState(3000)

  useEffect(() => {
    if (q.data?.config) {
      const c = q.data.config
      setEnable(c.enable)
      setIp(c.bmc_ip)
      setUser(c.bmc_user)
      setPassFile(c.bmc_password_file)
      setJitterMs(c.jitter_max_ms ?? 3000)
    }
  }, [q.data])

  const save = useMutation({
    mutationFn: (cfg: FencingConfig) => api.post('/api/ha/fencing/configure', cfg),
    onSuccess: () => { toast.success('IPMI fencing configuration saved'); qc.invalidateQueries({ queryKey: ['ha', 'fencing'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    save.mutate({ enable, bmc_ip: ip.trim(), bmc_user: user.trim(), bmc_password_file: passFile.trim(), jitter_max_ms: jitterMs })
  }

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24, borderLeft: enable ? '4px solid var(--error)' : '4px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="memory" size={24} style={{ color: enable ? 'var(--error)' : 'var(--text-tertiary)' }} />
        <div>
          <div style={{ fontWeight: 700 }}>IPMI / BMC Fencing (STONITH)</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            Chassis power-off via out-of-band IPMI LAN+ — requires Baseboard Management Controller
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

      <div style={{ display: 'grid', gridTemplateColumns: '180px 1fr', gap: 12, marginBottom: 16, alignItems: 'end' }}>
        <label className="field">
          <span className="field-label">Jitter Window (ms)</span>
          <input type="number" min={0} max={30000} step={500} value={jitterMs}
            onChange={e => setJitterMs(Math.min(30000, Math.max(0, parseInt(e.target.value) || 0)))}
            className="input" disabled={q.isLoading} />
        </label>
        <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: 0, paddingBottom: 6 }}>
          Random delay (0–{jitterMs}ms) before firing. Prevents simultaneous mutual destruction if both nodes hit the 45s threshold at the same instant. Default 3000.
        </p>
      </div>

      <button onClick={submit} disabled={save.isPending || q.isLoading} className="btn btn-primary"
        style={{ background: enable ? 'var(--error)' : 'var(--primary)', color: '#fff', border: 'none' }}>
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

  const [enable,    setEnable]    = useState(false)
  const [offUrl,    setOffUrl]    = useState('')
  const [method,    setMethod]    = useState('GET')
  const [username,  setUsername]  = useState('')
  const [passFile,  setPassFile]  = useState('')
  const [timeoutS,  setTimeoutS]  = useState(10)
  const [expStatus, setExpStatus] = useState(0)

  useEffect(() => {
    if (q.data?.config) {
      const c = q.data.config
      setEnable(c.enable)
      setOffUrl(c.outlet_off_url)
      setMethod(c.method || 'GET')
      setUsername(c.username)
      setPassFile(c.password_file)
      setTimeoutS(c.timeout_secs || 10)
      setExpStatus(c.expected_status ?? 0)
    }
  }, [q.data])

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
            Physically cuts outlet power via HTTP — works even when the data network is fully partitioned (Digital Loggers, iBoot, Raritan, etc.)
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
        style={{ background: enable ? 'var(--error)' : 'var(--primary)', color: '#fff', border: 'none' }}>
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save PDU Config'}
      </button>
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

  const [cfg, setCfg] = useState<ReplicationConfig>({
    local_pool: '', remote_pool: '', remote_host: '', remote_user: 'root', remote_port: 22, ssh_key_path: '/root/.ssh/id_rsa', interval_secs: 30
  })

  useEffect(() => { if (q.data?.config) setCfg(q.data.config) }, [q.data])

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
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12, marginBottom: 16 }}>
        <label className="field">
          <span className="field-label">Remote Host IP</span>
          <input value={cfg.remote_host} onChange={e => setCfg({ ...cfg, remote_host: e.target.value })} placeholder="10.0.0.11" className="input" />
        </label>
        <label className="field">
          <span className="field-label">SSH User & Port</span>
          <div style={{ display: 'flex', gap: 8 }}>
            <input value={cfg.remote_user} onChange={e => setCfg({ ...cfg, remote_user: e.target.value })} className="input" style={{ flex: 2 }} />
            <input type="number" value={cfg.remote_port} onChange={e => setCfg({ ...cfg, remote_port: parseInt(e.target.value) || 22 })} className="input" style={{ flex: 1 }} />
          </div>
        </label>
        <label className="field">
          <span className="field-label">SSH Identity File</span>
          <input value={cfg.ssh_key_path} onChange={e => setCfg({ ...cfg, ssh_key_path: e.target.value })} className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </label>
      </div>

      <button onClick={() => save.mutate(cfg)} disabled={save.isPending || q.isLoading} className="btn btn-primary">
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Replication Config'}
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
    if (!active || !until) { setRem(0); return }
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
      background: active ? 'rgba(245,158,11,0.03)' : 'var(--bg-card)'
    }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: active ? 0 : 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <Icon name="build_circle" size={24} style={{ color: active ? 'var(--warning)' : 'var(--text-tertiary)' }} />
          <div>
            <div style={{ fontWeight: 700 }}>Maintenance Mode</div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
              {active
                ? `All fencing suspended. Auto-resumes in ${fmtRem(rem)}`
                : 'Enable before planned work to suspend automated fencing.'}
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

  const addPeer = useMutation({
    mutationFn: (peer: { id: string; name: string; address: string; role: string }) =>
      api.post('/api/ha/peers', peer),
    onSuccess: () => { toast.success('Peer registered — heartbeat starting'); qc.invalidateQueries({ queryKey: ['ha', 'status'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const removePeer = useMutation({
    mutationFn: (id: string) => api.delete(`/api/ha/peers/${encodeURIComponent(id)}`),
    onSuccess: () => { toast.success('Peer removed'); qc.invalidateQueries({ queryKey: ['ha', 'status'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const promotePeer = useMutation({
    mutationFn: ({ id }: { id: string; name: string }) =>
      api.post(`/api/ha/peers/${encodeURIComponent(id)}/role`, { role: 'active' }),
    onSuccess: (_data, { name }) => { toast.success(`${name} promoted to active`); qc.invalidateQueries({ queryKey: ['ha', 'status'] }) },
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

  const toggleHA = useMutation({
    mutationFn: (enable: boolean) => api.post<{ success: boolean; job_id?: string }>('/api/ha/toggle', { enable }),
    onSuccess: (data) => {
      if (data.job_id) { setJobId(data.job_id); setJob(data.job_id, 'HA Stack Rebuild') }
      else { toast.success('HA configuration updated.'); qc.invalidateQueries({ queryKey: ['ha', 'status'] }) }
    },
    onError: (e: Error) => toast.error(`HA toggle failed: ${e.message}`),
  })

  const toggleMaintenance = useMutation({
    mutationFn: (seconds: number) => api.post('/api/ha/maintenance', { seconds }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['ha', 'status'] }); toast.success('Maintenance mode updated') },
    onError: (e: Error) => toast.error(e.message),
  })

  const [wizardStep, setWizardStep] = useState<number | null>(null)

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

  // ── Setup Wizard ────────────────────────────────────────────────────────────
  if (wizardStep !== null) {
    const TOTAL_STEPS = 6
    return (
      <div style={{ maxWidth: 700, margin: '0 auto' }}>
        <div style={{ marginBottom: 32 }}>
          <button onClick={() => setWizardStep(null)} className="btn btn-ghost" style={{ marginBottom: 12 }}>
            <Icon name="arrow_back" size={14} />Cancel Wizard
          </button>
          <h1 className="page-title">High Availability Setup</h1>
          <div style={{ display: 'flex', gap: 4, marginTop: 16 }}>
            {Array.from({ length: TOTAL_STEPS }, (_, i) => i + 1).map(s => (
              <div key={s} style={{ height: 4, flex: 1, borderRadius: 2, background: s <= wizardStep ? 'var(--primary)' : 'var(--border)', opacity: s === wizardStep ? 1 : 0.4 }} />
            ))}
          </div>
          <div style={{ marginTop: 8, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Step {wizardStep} of {TOTAL_STEPS}</div>
        </div>

        {/* Step 1 — Introduction */}
        {wizardStep === 1 && (
          <div className="card fade-in" style={{ padding: 32 }}>
            <Icon name="topology" size={48} style={{ color: 'var(--primary)', marginBottom: 20 }} />
            <h2 style={{ marginBottom: 12 }}>Introduction to D-PlaneOS HA</h2>
            <p style={{ color: 'var(--text-secondary)', lineHeight: 1.6, marginBottom: 24 }}>
              D-PlaneOS High Availability transforms your standalone server into a resilient cluster.
              It uses <strong>Patroni</strong> and <strong>etcd</strong> for database consensus,
              and <strong>Keepalived</strong> for Virtual IP failover.
            </p>
            <div className="alert alert-info" style={{ marginBottom: 32 }}>
              <Icon name="info" size={18} />
              <div><strong>Prerequisite:</strong> Two nodes with static IPs on the same management network. A third network-reachable endpoint (router, public DNS) is recommended as a quorum witness.</div>
            </div>
            <button onClick={() => setWizardStep(2)} className="btn btn-primary btn-lg" style={{ width: '100%', justifyContent: 'center' }}>
              Start Configuration <Icon name="arrow_forward" size={16} />
            </button>
          </div>
        )}

        {/* Step 2 — Enable HA */}
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
                <button onClick={() => toggleHA.mutate(!haEnabled)} disabled={toggleHA.isPending || !!jobId}
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

        {/* Step 3 — Peer Registration */}
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
                Next: Storage Replication
              </button>
            </div>
          </div>
        )}

        {/* Step 4 — Replication */}
        {wizardStep === 4 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 4: Storage Replication</h2>
            <p style={{ color: 'var(--text-secondary)', marginBottom: 24 }}>Configure ZFS snapshot shipping so data is available on all nodes.</p>
            <ReplicationConfigForm />
            <div style={{ display: 'flex', gap: 12, marginTop: 32 }}>
              <button onClick={() => setWizardStep(3)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(5)} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: Quorum Witness
              </button>
            </div>
          </div>
        )}

        {/* Step 5 — Witness */}
        {wizardStep === 5 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 5: Quorum Witness</h2>
            <p style={{ color: 'var(--text-secondary)', marginBottom: 16 }}>
              A witness proves this node has network access before firing STONITH — preventing split-brain when the peer is unreachable due to a network partition rather than a real failure.
            </p>
            <div className="alert alert-info" style={{ marginBottom: 24 }}>
              <Icon name="info" size={18} />
              <div>Add your local gateway, a public DNS server (1.1.1.1 or 8.8.8.8), or any stable HTTP endpoint that is reachable from both nodes but independent of the peer.</div>
            </div>
            <WitnessConfigForm />
            <div style={{ display: 'flex', gap: 12, marginTop: 32 }}>
              <button onClick={() => setWizardStep(4)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(6)} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: Fencing (STONITH)
              </button>
            </div>
          </div>
        )}

        {/* Step 6 — Fencing */}
        {wizardStep === 6 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 6: Automated Fencing (STONITH)</h2>
            <p style={{ color: 'var(--text-secondary)', marginBottom: 16 }}>
              Configure at least one out-of-band power control method. IPMI uses the BMC over the management network; PDU cuts the physical outlet — both bypass the peer OS entirely.
            </p>
            <FencingConfigForm />
            <PDUConfigForm />
            <div className="alert alert-success" style={{ marginTop: 32, marginBottom: 24 }}>
              <Icon name="check_circle" size={18} />
              <div>Setup complete! You can now monitor the cluster from the main dashboard.</div>
            </div>
            <div style={{ display: 'flex', gap: 12 }}>
              <button onClick={() => setWizardStep(5)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(null)} className="btn btn-success" style={{ flex: 1, justifyContent: 'center' }}>
                Finish & Go to Dashboard
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
        <h1>High Availability is Disabled</h1>
        <p style={{ color: 'var(--text-secondary)', maxWidth: 500, margin: '16px auto 32px' }}>
          Your D-PlaneOS instance is running as a standalone node. Enable HA to support automatic failover, Virtual IP migration, and storage redundancy.
        </p>
        <button onClick={() => setWizardStep(1)} className="btn btn-primary btn-lg">
          <Icon name="bolt" size={18} />Launch HA Setup Wizard
        </button>
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
          <p className="page-subtitle">High availability — nodes, quorum and failover</p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button onClick={() => setWizardStep(1)} className="btn btn-ghost">
            <Icon name="settings" size={14} />Setup Wizard
          </button>
          <button onClick={() => {
            qc.invalidateQueries({ queryKey: ['ha', 'status'] })
            qc.invalidateQueries({ queryKey: ['ha', 'local'] })
          }} className="btn btn-ghost">
            <Icon name="refresh" size={14} />Refresh
          </button>
        </div>
      </div>

      {/* ── Stat Cards ──────────────────────────────────────────────────────── */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 16, marginBottom: 20 }}>
        {/* Quorum */}
        <div style={{ background: 'var(--bg-card)', borderRadius: 'var(--radius-lg)', padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14, border: `1px solid ${hasQuorum ? 'rgba(16,185,129,0.25)' : 'rgba(239,68,68,0.25)'}` }}>
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
          <Icon name="star" size={28} style={{ color: 'rgba(251,191,36,0.9)', flexShrink: 0 }} />
          <div style={{ minWidth: 0 }}>
            <div style={{ fontWeight: 700, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {activeNode?.name ?? activeNode?.id ?? '—'}
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Active node</div>
          </div>
        </div>

        {/* Last failover */}
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14, border: hysteresis ? '1px solid rgba(245,158,11,0.35)' : undefined }}>
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

      {/* Subordinate Mode — highest priority */}
      {subordinate && (
        <div className="alert alert-warning" style={{ marginBottom: 16, borderRadius: 'var(--radius-lg)' }}>
          <Icon name="sync" size={20} />
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 700, marginBottom: 4 }}>Catch-Up Sync in Progress — Subordinate Mode Active</div>
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

      {/* Hysteresis — flap guard */}
      {hysteresis && !subordinate && (
        <div className="alert alert-warning" style={{ marginBottom: 16, borderRadius: 'var(--radius-lg)' }}>
          <Icon name="schedule" size={20} />
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 700, marginBottom: 4 }}>Flap Guard Active — Auto-Failover Suppressed</div>
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

      {/* ── Node List ────────────────────────────────────────────────────────── */}
      <div style={{ fontWeight: 700, marginBottom: 12 }}>Nodes</div>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {allNodes.map(node => (
          <NodeCard
            key={node.id}
            node={node}
            isLocal={node.id === localID}
            canPromote={allNodes.length >= 2}
            onPromote={async () => {
              if (node.id === localID) {
                if (await confirm({ title: 'Assume Primary Role Locally?', message: 'This node will force-import all storage pools and execute the failover protocol. Ensure the current active node is offline or fenced first to prevent split-brain.', danger: true, confirmLabel: 'Failover Now' })) {
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
              if (await confirm({ title: `STONITH: Terminate ${node.name ?? node.id}?`, message: 'Issues a chassis power-off via out-of-band management (IPMI BMC or PDU). Data loss may occur if the node has unsynchronised writes. Proceed?', danger: true, confirmLabel: 'Terminate Chassis' })) {
                fencePeer.mutate(node.id)
              }
            }}
            pending={pending}
          />
        ))}

        {allNodes.length === 0 && (
          <div className="card" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '48px 0', gap: 12, borderRadius: 'var(--radius-lg)' }}>
            <Icon name="device_hub" size={40} style={{ color: 'var(--text-tertiary)', opacity: 0.4 }} />
            <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No cluster nodes found</div>
            <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>Register peer nodes below to form a cluster</div>
          </div>
        )}
      </div>

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
      <WitnessConfigForm />
      <FencingConfigForm />
      <PDUConfigForm />
      <ReplicationConfigForm />

      <ConfirmDialog />
    </div>
  )
}
