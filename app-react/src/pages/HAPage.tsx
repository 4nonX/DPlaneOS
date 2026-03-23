/**
 * pages/HAPage.tsx - High Availability Cluster (Phase 6)
 *
 * Shows cluster quorum status, local node info, peer list.
 * Allows registering/removing peers and promoting standby to active.
 *
 * Calls:
 *   GET  /api/ha/status                        → { success, cluster: Cluster }
 *   GET  /api/ha/local                         → { success, id, node_id, address, role, name }
 *   POST /api/ha/peers  { id, name, address, role }
 *   DELETE /api/ha/peers/{id}
 *   POST /api/ha/peers/{id}/role { role:'active' }  → promote to active
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

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface HANode {
  id:         string
  name?:      string
  address?:   string
  role?:      string    // active | standby
  online?:    boolean
  last_seen?: string
}

interface Cluster {
  quorum?:      boolean
  active_node?: HANode
  local_node?:  HANode
  peers?:       HANode[]
  ha_enabled?:  boolean
}

interface HAStatusResponse { success: boolean; cluster?: Cluster }
interface HALocalResponse  {
  success: boolean
  id?:     string
  node_id?: string
  address?: string
  role?:   string
  name?:   string
}

// ---------------------------------------------------------------------------

function fmtDate(s?: string): string {
  if (!s) return 'Never'
  try { return new Date(s).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'short' }) }
  catch { return s }
}

interface ReplicationConfig {
  local_pool: string
  remote_pool: string
  remote_host: string
  remote_user: string
  remote_port: number
  ssh_key_path: string
  interval_secs: number
}

interface FencingConfig {
  enable: boolean
  bmc_ip: string
  bmc_user: string
  bmc_password_file: string
}

// ---------------------------------------------------------------------------
// NodeCard
// ---------------------------------------------------------------------------

function NodeCard({ node, isLocal, canPromote, onPromote, onRemove, onFence, pending }: {
  node:       HANode
  isLocal:    boolean
  canPromote: boolean
  onPromote?: () => void
  onRemove:   () => void
  onFence?:   () => void
  pending:    boolean
}) {
  const isActive = node.role === 'active'
  const isOnline = node.online !== false

  return (
    <div style={{
      display: 'flex', alignItems: 'center', gap: 16, padding: '16px 20px',
      background: 'var(--bg-card)',
      border: `1px solid ${isActive ? 'rgba(16,185,129,0.25)' : isLocal ? 'rgba(138,156,255,0.2)' : 'var(--border)'}`,
      borderRadius: 'var(--radius-lg)'}}>
      {/* Icon */}
      <div style={{
        width: 42, height: 42, borderRadius: 'var(--radius-md)', flexShrink: 0,
        background: isActive ? 'rgba(16,185,129,0.1)' : isLocal ? 'var(--primary-bg)' : 'var(--surface)',
        border: `1px solid ${isActive ? 'rgba(16,185,129,0.25)' : isLocal ? 'rgba(138,156,255,0.2)' : 'var(--border)'}`,
        display: 'flex', alignItems: 'center', justifyContent: 'center'}}>
        <Icon name="computer" size={22} style={{ color: isActive ? 'var(--success)' : isLocal ? 'var(--primary)' : 'var(--text-tertiary)' }} />
      </div>

      {/* Info */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 3 }}>
          <span style={{ fontWeight: 700 }}>{node.name ?? node.id}</span>

          {isLocal && (
            <span className="badge badge-primary">THIS NODE</span>
          )}
          {isActive && (
            <span className="badge badge-success">ACTIVE</span>
          )}
          {!isActive && node.role === 'standby' && (
            <span className="badge badge-neutral">STANDBY</span>
          )}

          {/* Online dot */}
          <span style={{ width: 8, height: 8, borderRadius: '50%', background: isOnline ? 'var(--success)' : 'var(--error)', boxShadow: isOnline ? '0 0 5px var(--success)' : 'none', display: 'inline-block' }} />
        </div>

        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          {node.address  && <span style={{ fontFamily: 'var(--font-mono)' }}>{node.address}</span>}
          {node.id && node.id !== node.name && <span style={{ color: 'var(--text-tertiary)' }}>ID: {node.id}</span>}
          {node.last_seen && <span>Last seen: {fmtDate(node.last_seen)}</span>}
        </div>
      </div>

      {/* Actions */}
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

// ---------------------------------------------------------------------------
// AddPeerForm
// ---------------------------------------------------------------------------

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
          <span className="field-label">Address</span>
          <input value={address} onChange={e => setAddress(e.target.value)} placeholder="192.168.1.11:9000"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </label>

        <label className="field">
          <span className="field-label">Role</span>
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

// ---------------------------------------------------------------------------
// FencingConfigForm
// ---------------------------------------------------------------------------

function FencingConfigForm() {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['ha', 'fencing'],
    queryFn: ({ signal }) => api.get<{success: boolean; config: FencingConfig}>('/api/ha/fencing/configure', signal),
  })

  const [enable, setEnable] = useState(false)
  const [ip, setIp] = useState('')
  const [user, setUser] = useState('')
  const [passFile, setPassFile] = useState('')

  useEffect(() => {
    if (q.data?.config) {
      setEnable(q.data.config.enable)
      setIp(q.data.config.bmc_ip)
      setUser(q.data.config.bmc_user)
      setPassFile(q.data.config.bmc_password_file)
    }
  }, [q.data])

  const save = useMutation({
    mutationFn: (cfg: FencingConfig) => api.post('/api/ha/fencing/configure', cfg),
    onSuccess: () => {
      toast.success('STONITH configuration saved')
      qc.invalidateQueries({ queryKey: ['ha', 'fencing'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    save.mutate({ enable, bmc_ip: ip.trim(), bmc_user: user.trim(), bmc_password_file: passFile.trim() })
  }

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24, borderLeft: enable ? '4px solid var(--error)' : '4px solid var(--border)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="power_settings_new" size={24} style={{ color: enable ? 'var(--error)' : 'var(--text-tertiary)' }} />
        <div>
          <div style={{ fontWeight: 700 }}>Intelligent Fencing (STONITH)</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Automated peer termination on quorum failure via IPMI Redfish</div>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 120px', gap: 12, marginBottom: 16 }}>
        <label className="field">
          <span className="field-label">BMC IP Address</span>
          <input value={ip} onChange={e => setIp(e.target.value)} placeholder="10.0.0.10" className="input" style={{ fontFamily: 'var(--font-mono)' }} disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">BMC Username</span>
          <input value={user} onChange={e => setUser(e.target.value)} placeholder="admin" className="input" disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">BMC Password File (0600)</span>
          <input value={passFile} onChange={e => setPassFile(e.target.value)} placeholder="/etc/bmc.secret" className="input" style={{ fontFamily: 'var(--font-mono)' }} disabled={q.isLoading} />
        </label>
        <label className="field">
          <span className="field-label">Enable</span>
          <select value={enable ? 'yes' : 'no'} onChange={e => setEnable(e.target.value === 'yes')} className="input">
            <option value="no">Disabled</option>
            <option value="yes">Armed</option>
          </select>
        </label>
      </div>

      <button onClick={submit} disabled={save.isPending || q.isLoading} className="btn btn-primary" style={{ background: enable ? 'var(--error)' : 'var(--primary)', color: '#fff', border: 'none' }}>
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Fencing Config'}
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ReplicationConfigForm
// ---------------------------------------------------------------------------

function ReplicationConfigForm() {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['ha', 'replication'],
    queryFn: ({ signal }) => api.get<{success: boolean; config: ReplicationConfig}>('/api/ha/replication/configure', signal),
  })

  const [cfg, setCfg] = useState<ReplicationConfig>({
    local_pool: '', remote_pool: '', remote_host: '', remote_user: 'root', remote_port: 22, ssh_key_path: '/root/.ssh/id_rsa', interval_secs: 30
  })

  useEffect(() => {
    if (q.data?.config) setCfg(q.data.config)
  }, [q.data])

  const save = useMutation({
    mutationFn: (c: ReplicationConfig) => api.post('/api/ha/replication/configure', c),
    onSuccess: () => {
      toast.success('Replication configuration saved')
      qc.invalidateQueries({ queryKey: ['ha', 'replication'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24, borderLeft: '4px solid var(--primary)' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
        <Icon name="sync" size={24} style={{ color: 'var(--primary)' }} />
        <div>
          <div style={{ fontWeight: 700 }}>Continuous Storage Replication (ZFS)</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Asynchronous Active-to-Standby block sync</div>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12, marginBottom: 12 }}>
        <label className="field">
          <span className="field-label">Local Pool Target</span>
          <input value={cfg.local_pool} onChange={e => setCfg({...cfg, local_pool: e.target.value})} placeholder="tank" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Remote Pool Target</span>
          <input value={cfg.remote_pool} onChange={e => setCfg({...cfg, remote_pool: e.target.value})} placeholder="tank" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Interval (Seconds)</span>
          <input type="number" min="10" value={cfg.interval_secs} onChange={e => setCfg({...cfg, interval_secs: parseInt(e.target.value)||30})} className="input" />
        </label>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12, marginBottom: 16 }}>
        <label className="field">
          <span className="field-label">Remote Host IP</span>
          <input value={cfg.remote_host} onChange={e => setCfg({...cfg, remote_host: e.target.value})} placeholder="10.0.0.11" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Remote SSH User & Port</span>
          <div style={{ display: 'flex', gap: 8 }}>
            <input value={cfg.remote_user} onChange={e => setCfg({...cfg, remote_user: e.target.value})} className="input" style={{ flex: 2 }} />
            <input type="number" value={cfg.remote_port} onChange={e => setCfg({...cfg, remote_port: parseInt(e.target.value)||22})} className="input" style={{ flex: 1 }} />
          </div>
        </label>
        <label className="field">
          <span className="field-label">SSH Identity File</span>
          <input value={cfg.ssh_key_path} onChange={e => setCfg({...cfg, ssh_key_path: e.target.value})} className="input" />
        </label>
      </div>

      <button onClick={() => save.mutate(cfg)} disabled={save.isPending || q.isLoading} className="btn btn-primary">
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Replication Config'}
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// HAPage
// ---------------------------------------------------------------------------

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
    onSuccess: () => {
      toast.success('Peer registered - heartbeat starting')
      qc.invalidateQueries({ queryKey: ['ha', 'status'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const removePeer = useMutation({
    mutationFn: (id: string) => api.delete(`/api/ha/peers/${encodeURIComponent(id)}`),
    onSuccess: () => {
      toast.success('Peer removed')
      qc.invalidateQueries({ queryKey: ['ha', 'status'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const promotePeer = useMutation({
    mutationFn: ({ id }: { id: string; name: string }) =>
      api.post(`/api/ha/peers/${encodeURIComponent(id)}/role`, { role: 'active' }),
    onSuccess: (_data, { name }) => {
      toast.success(`${name} promoted to active`)
      qc.invalidateQueries({ queryKey: ['ha', 'status'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const fencePeer = useMutation({
    mutationFn: (id: string) => api.post('/api/ha/fence', { node_id: id }),
    onSuccess: () => toast.success('Fencing execution fired asynchronously.'),
    onError: (e: Error) => toast.error(`Fencing dispatch failed: ${e.message}`)
  })
  
  const localPromote = useMutation({
    mutationFn: () => api.post('/api/ha/promote', {}),
    onSuccess: () => {
      toast.success('Local Failover promotion triggered successfully.')
      qc.invalidateQueries({ queryKey: ['ha', 'status'] })
    },
    onError: (e: Error) => toast.error(`Promotion failed: ${e.message}`)
  })

  const [jobId, setJobId] = useState<string | null>(null)

  const toggleHA = useMutation({
    mutationFn: (enable: boolean) => api.post<{success: boolean, job_id?: string}>('/api/ha/toggle', { enable }),
    onSuccess: (data) => {
      if (data.job_id) {
        setJobId(data.job_id)
      } else {
        toast.success('HA configuration updated.')
        qc.invalidateQueries({ queryKey: ['ha', 'status'] })
      }
    },
    onError: (e: Error) => toast.error(`HA Toggle failed: ${e.message}`)
  })

  const [wizardStep, setWizardStep] = useState<number | null>(null)

  const pending = addPeer.isPending || removePeer.isPending || promotePeer.isPending || fencePeer.isPending || localPromote.isPending

  // Build node list
  const cluster   = statusQ.data?.cluster ?? {}
  const localID   = localQ.data?.id ?? localQ.data?.node_id ?? ''
  const localNode: HANode | null = localID ? {
    id:      localID,
    name:    localQ.data?.name,
    address: localQ.data?.address,
    role:    localQ.data?.role ?? 'active',
    online:  true,
  } : cluster.local_node ?? null

  const peers    = cluster.peers ?? []
  const allNodes: HANode[] = localNode ? [localNode, ...peers] : peers
  const hasQuorum = cluster.quorum === true
  const haEnabled = cluster.ha_enabled === true

  const activeNode =
    cluster.active_node ??
    allNodes.find(n => n.role === 'active')

  if (statusQ.isLoading || localQ.isLoading) return <Skeleton height={360} />
  if (statusQ.isError) return (
    <ErrorState error={statusQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['ha', 'status'] })} />
  )

  // ── Setup Wizard View ───────────────────────────────────────────────────────
  if (wizardStep !== null) {
    return (
      <div style={{ maxWidth: 700, margin: '0 auto' }}>
        <div style={{ marginBottom: 32 }}>
          <button onClick={() => setWizardStep(null)} className="btn btn-ghost" style={{ marginBottom: 12 }}>
            <Icon name="arrow_back" size={14} />Cancel Wizard
          </button>
          <h1 className="page-title">High Availability Setup</h1>
          <div style={{ display: 'flex', gap: 4, marginTop: 16 }}>
            {[1, 2, 3, 4, 5].map(s => (
              <div key={s} style={{
                height: 4, flex: 1, borderRadius: 2,
                background: s <= wizardStep ? 'var(--primary)' : 'var(--border)',
                opacity: s === wizardStep ? 1 : 0.4
              }} />
            ))}
          </div>
        </div>

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
              <div>
                <strong>Prerequisite:</strong> You need at least two nodes with static IPs.
                A third "witness" node or external etcd cluster is recommended for production.
              </div>
            </div>
            <button onClick={() => setWizardStep(2)} className="btn btn-primary btn-lg" style={{ width: '100%', justifyContent: 'center' }}>
              Start Configuration <Icon name="arrow_forward" size={16} />
            </button>
          </div>
        )}

        {wizardStep === 2 && (
          <div className="card fade-in" style={{ padding: 32 }}>
            <h2 style={{ marginBottom: 8 }}>Step 2: Database Quorum</h2>
            <p style={{ color: 'var(--text-secondary)', marginBottom: 24 }}>
              Enable the Patroni HA layers in NixOS. This node will initialize as the leader if no cluster exists.
            </p>
            
            <div style={{ background: 'var(--surface)', padding: 20, borderRadius: 'var(--radius-md)', marginBottom: 24, border: '1px solid var(--border)' }}>
              <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
                <div>
                  <div style={{ fontWeight: 700 }}>Enable HA Service Mesh</div>
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Patroni + etcd + HAProxy</div>
                </div>
                <button 
                  onClick={() => toggleHA.mutate(!haEnabled)} 
                  disabled={toggleHA.isPending || !!jobId}
                  className={`btn ${haEnabled ? 'btn-danger' : 'btn-success'}`}
                >
                  {haEnabled ? 'Disable HA' : 'Enable HA'}
                </button>
              </div>
            </div>

            {jobId && (
              <div style={{ marginBottom: 24 }}>
                <JobProgress 
                  jobId={jobId} 
                  runningLabel="Rebuilding NixOS with HA modules..." 
                  doneLabel="NixOS Rebuild Complete"
                  onDone={() => {
                    setJobId(null)
                    qc.invalidateQueries({ queryKey: ['ha', 'status'] })
                  }}
                  onFailed={() => setJobId(null)}
                />
              </div>
            )}

            <div style={{ display: 'flex', gap: 12 }}>
              <button onClick={() => setWizardStep(1)} className="btn btn-ghost" disabled={!!jobId}>Previous</button>
              <button onClick={() => setWizardStep(3)} disabled={!haEnabled || !!jobId} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: Peer Registration
              </button>
            </div>
          </div>
        )}

        {wizardStep === 3 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 3: Peer Registration</h2>
            <p style={{ color: 'var(--text-secondary)', marginBottom: 24 }}>
              Register the other nodes in your cluster so they can begin heartbeating.
            </p>
            
            <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 24 }}>
              {peers.map(p => <NodeCard key={p.id} node={p} isLocal={false} canPromote={false} onRemove={() => removePeer.mutate(p.id)} pending={pending} />)}
              {peers.length === 0 && <div style={{ padding: 32, textAlign: 'center', color: 'var(--text-tertiary)', border: '2px dashed var(--border)', borderRadius: 'var(--radius-lg)' }}>No peers registered yet</div>}
            </div>

            <AddPeerForm onAdd={p => addPeer.mutate(p)} pending={addPeer.isPending} />

            <div style={{ display: 'flex', gap: 12, marginTop: 32 }}>
              <button onClick={() => setWizardStep(2)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(4)} disabled={peers.length === 0} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: Storage Sync
              </button>
            </div>
          </div>
        )}

        {wizardStep === 4 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 4: Storage Replication</h2>
            <p style={{ color: 'var(--text-secondary)', marginBottom: 24 }}>
              Configure ZFS snapshot shipping to ensure data is available on all nodes.
            </p>
            
            <ReplicationConfigForm />

            <div style={{ display: 'flex', gap: 12, marginTop: 32 }}>
              <button onClick={() => setWizardStep(3)} className="btn btn-ghost">Previous</button>
              <button onClick={() => setWizardStep(5)} className="btn btn-primary" style={{ flex: 1, justifyContent: 'center' }}>
                Next: STONITH Fencing
              </button>
            </div>
          </div>
        )}

        {wizardStep === 5 && (
          <div className="fade-in">
            <h2 style={{ marginBottom: 8 }}>Step 5: Fencing (STONITH)</h2>
            <p style={{ color: 'var(--text-secondary)', marginBottom: 24 }}>
              Avoid split-brain by configuring out-of-band power management.
            </p>
            
            <FencingConfigForm />

            <div className="alert alert-success" style={{ marginTop: 32, marginBottom: 24 }}>
              <Icon name="check_circle" size={18} />
              <div>Setup complete! You can now monitor the cluster from the main dashboard.</div>
            </div>

            <div style={{ display: 'flex', gap: 12 }}>
              <button onClick={() => setWizardStep(4)} className="btn btn-ghost">Previous</button>
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

  // ── Standalone / Disabled View ──────────────────────────────────────────────
  if (!haEnabled && peers.length === 0) {
    return (
      <div style={{ maxWidth: 800, margin: '60px auto', textAlign: 'center' }}>
        <Icon name="topology" size={64} style={{ color: 'var(--text-tertiary)', opacity: 0.2, marginBottom: 24 }} />
        <h1>High Availability is Disabled</h1>
        <p style={{ color: 'var(--text-secondary)', maxWidth: 500, margin: '16px auto 32px' }}>
          Your D-PlaneOS instance is running as a standalone node. 
          Enable HA to support automatic database failover, Virtual IP migration, and storage redundancy.
        </p>
        <button onClick={() => setWizardStep(1)} className="btn btn-primary btn-lg">
          <Icon name="bolt" size={18} />Launch HA Setup Wizard
        </button>
      </div>
    )
  }

  // ── Main Dashboard View ─────────────────────────────────────────────────────
  return (
    <div style={{ maxWidth: 900 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 28 }}>
        <div>
          <h1 className="page-title">HA Cluster</h1>
          <p className="page-subtitle">High availability - nodes, quorum and failover</p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button onClick={() => setWizardStep(1)} className="btn btn-ghost">
            <Icon name="settings" size={14} />Setup Wizard
          </button>
          <button
            onClick={() => {
              qc.invalidateQueries({ queryKey: ['ha', 'status'] })
              qc.invalidateQueries({ queryKey: ['ha', 'local'] })
            }}
            className="btn btn-ghost"
          >
            <Icon name="refresh" size={14} />Refresh
          </button>
        </div>
      </div>

      {/* Cluster overview cards */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16, marginBottom: 24 }}>

        {/* Quorum */}
        <div style={{
          background: 'var(--bg-card)', borderRadius: 'var(--radius-lg)', padding: '18px 20px',
          display: 'flex', alignItems: 'center', gap: 14,
          border: `1px solid ${hasQuorum ? 'rgba(16,185,129,0.25)' : 'rgba(239,68,68,0.25)'}`}}>
          <Icon name={hasQuorum ? 'verified' : 'dangerous'} size={28}
            style={{ color: hasQuorum ? 'var(--success)' : 'var(--error)', flexShrink: 0 }} />
          <div>
            <div style={{ fontWeight: 700, fontSize: 'var(--text-md)', color: hasQuorum ? 'var(--success)' : 'var(--error)' }}>
              {hasQuorum ? 'Quorum' : 'No Quorum'}
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Cluster state</div>
          </div>
        </div>

        {/* Node count */}
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14 }}>
          <Icon name="computer" size={28} style={{ color: 'var(--primary)', flexShrink: 0 }} />
          <div>
            <div style={{ fontWeight: 700, fontSize: 28, fontFamily: 'var(--font-mono)', lineHeight: 1 }}>
              {allNodes.length}
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Total nodes</div>
          </div>
        </div>

        {/* Active node */}
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14 }}>
          <Icon name="star" size={28} style={{ color: 'rgba(251,191,36,0.9)', flexShrink: 0 }} />
          <div style={{ minWidth: 0 }}>
            <div style={{ fontWeight: 700, fontSize: 'var(--text-md)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {activeNode?.name ?? activeNode?.id ?? '-'}
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Active node</div>
          </div>
        </div>
      </div>

      {/* Node list */}
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
                if (await confirm({ title: `Assume Primary Role Locally?`, message: 'This node will attempt to aggressively import all storage pools and execute failover protocol mechanically. Proceed?', danger: true, confirmLabel: 'Failover Now' })) {
                  localPromote.mutate()
                }
              } else {
                if (await confirm({ title: `Tag ${node.name ?? node.id} as active?`, message: 'Registers the peer as the target active node. Native promotion occurs autonomously across Patroni layers.', danger: false, confirmLabel: 'Tag Active' })) {
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
              if (await confirm({ title: `STONITH: Terminate Node ${node.name ?? node.id}?`, message: 'This will issue a chassis power off command via out-of-band IPMI Redfish networks to the Baseboard Management Controller. Data loss may occur. Proceed?', danger: true, confirmLabel: 'Terminate Chassis' })) {
                fencePeer.mutate(node.id)
              }
            }}
            pending={pending}
          />
        ))}

        {allNodes.length === 0 && (
          <div className="card" style={{
            display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
            padding: '48px 0', gap: 12, borderRadius: 'var(--radius-lg)'}}>
            <Icon name="device_hub" size={40} style={{ color: 'var(--text-tertiary)', opacity: 0.4 }} />
            <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No cluster nodes found</div>
            <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>
              Register peer nodes below to form a cluster
            </div>
          </div>
        )}
      </div>

      <AddPeerForm
        onAdd={peer => addPeer.mutate(peer)}
        pending={pending}
      />
      <ReplicationConfigForm />
      <FencingConfigForm />
      <ConfirmDialog />
    </div>
  )
}

