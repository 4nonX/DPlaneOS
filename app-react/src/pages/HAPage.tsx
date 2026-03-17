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

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'

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

// ---------------------------------------------------------------------------
// NodeCard
// ---------------------------------------------------------------------------

function NodeCard({ node, isLocal, canPromote, onPromote, onRemove, pending }: {
  node:       HANode
  isLocal:    boolean
  canPromote: boolean
  onPromote:  () => void
  onRemove:   () => void
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

      {/* Actions - only for non-local nodes */}
      {!isLocal && (
        <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
          {canPromote && !isActive && (
            <button onClick={onPromote} disabled={pending} className="btn btn-primary">
              <Icon name="upgrade" size={14} />Promote
            </button>
          )}
          <button onClick={onRemove} disabled={pending} className="btn btn-danger">
            <Icon name="delete" size={13} />
          </button>
        </div>
      )}
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

  const pending = addPeer.isPending || removePeer.isPending || promotePeer.isPending

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

  const activeNode =
    cluster.active_node ??
    allNodes.find(n => n.role === 'active')

  if (statusQ.isLoading || localQ.isLoading) return <Skeleton height={360} />
  if (statusQ.isError) return (
    <ErrorState error={statusQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['ha', 'status'] })} />
  )

  return (
    <div style={{ maxWidth: 900 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 28 }}>
        <div>
          <h1 className="page-title">HA Cluster</h1>
          <p className="page-subtitle">High availability - nodes, quorum and failover</p>
        </div>
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
              if (await confirm({ title: `Promote ${node.name ?? node.id} to active?`, message: 'This will trigger an immediate failover. The current active node will become standby.', danger: false, confirmLabel: 'Promote' })) {
                promotePeer.mutate({ id: node.id, name: node.name ?? node.id })
              }
            }}
            onRemove={async () => {
              if (await confirm({ title: `Remove ${node.name ?? node.id}?`, message: 'This node will be removed from the cluster.', danger: true, confirmLabel: 'Remove' })) {
                removePeer.mutate(node.id)
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
      <ConfirmDialog />
    </div>
  )
}

