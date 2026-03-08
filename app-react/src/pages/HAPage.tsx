/**
 * pages/HAPage.tsx — High Availability Cluster (Phase 6)
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
// Styles
// ---------------------------------------------------------------------------

const btnGhost: React.CSSProperties = {
  padding: '7px 13px', background: 'var(--surface)', color: 'var(--text-secondary)',
  border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 500, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnPrimary: React.CSSProperties = {
  padding: '8px 18px', background: 'var(--primary)', color: '#000',
  border: 'none', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 700, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnDanger: React.CSSProperties = {
  padding: '6px 12px', background: 'var(--error-bg)', border: '1px solid var(--error-border)',
  borderRadius: 'var(--radius-sm)', cursor: 'pointer', color: 'var(--error)',
  fontSize: 'var(--text-xs)', fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 4,
}
const inputStyle: React.CSSProperties = {
  background: 'var(--surface)', border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)', padding: '8px 12px',
  color: 'var(--text)', fontSize: 'var(--text-sm)', width: '100%',
  fontFamily: 'var(--font-ui)', outline: 'none', boxSizing: 'border-box',
}

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
      borderRadius: 'var(--radius-lg)',
    }}>
      {/* Icon */}
      <div style={{
        width: 42, height: 42, borderRadius: 'var(--radius-md)', flexShrink: 0,
        background: isActive ? 'rgba(16,185,129,0.1)' : isLocal ? 'var(--primary-bg)' : 'var(--surface)',
        border: `1px solid ${isActive ? 'rgba(16,185,129,0.25)' : isLocal ? 'rgba(138,156,255,0.2)' : 'var(--border)'}`,
        display: 'flex', alignItems: 'center', justifyContent: 'center',
      }}>
        <Icon name="computer" size={22} style={{ color: isActive ? 'var(--success)' : isLocal ? 'var(--primary)' : 'var(--text-tertiary)' }} />
      </div>

      {/* Info */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 3 }}>
          <span style={{ fontWeight: 700 }}>{node.name ?? node.id}</span>

          {isLocal && (
            <span style={{ padding: '1px 6px', borderRadius: 'var(--radius-xs)', background: 'var(--primary-bg)', color: 'var(--primary)', fontSize: 10, fontWeight: 700 }}>
              THIS NODE
            </span>
          )}
          {isActive && (
            <span style={{ padding: '1px 6px', borderRadius: 'var(--radius-xs)', background: 'rgba(76,175,80,0.2)', color: '#81c784', border: '1px solid rgba(76,175,80,0.3)', fontSize: 10, fontWeight: 700 }}>
              ACTIVE
            </span>
          )}
          {!isActive && node.role === 'standby' && (
            <span style={{ padding: '1px 6px', borderRadius: 'var(--radius-xs)', background: 'rgba(33,150,243,0.2)', color: '#64b5f6', border: '1px solid rgba(33,150,243,0.3)', fontSize: 10, fontWeight: 700 }}>
              STANDBY
            </span>
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

      {/* Actions — only for non-local nodes */}
      {!isLocal && (
        <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
          {canPromote && !isActive && (
            <button onClick={onPromote} disabled={pending} style={btnPrimary}>
              <Icon name="upgrade" size={14} />Promote
            </button>
          )}
          <button onClick={onRemove} disabled={pending} style={btnDanger}>
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
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24 }}>
      <div style={{ fontWeight: 700, marginBottom: 16 }}>Register Peer Node</div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr 120px', gap: 12, marginBottom: 12 }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontWeight: 600 }}>Node ID</span>
          <input value={id} onChange={e => setId(e.target.value)} placeholder="node-2"
            style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontWeight: 600 }}>Display Name</span>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="NAS-2 (optional)" style={inputStyle} />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontWeight: 600 }}>Address</span>
          <input value={address} onChange={e => setAddress(e.target.value)} placeholder="192.168.1.11:9000"
            style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontWeight: 600 }}>Role</span>
          <select value={role} onChange={e => setRole(e.target.value)} style={{ ...inputStyle, appearance: 'none' }}>
            <option value="standby">Standby</option>
            <option value="active">Active</option>
          </select>
        </label>
      </div>

      <button onClick={submit} disabled={pending} style={btnPrimary}>
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
      toast.success('Peer registered — heartbeat starting')
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
          <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>HA Cluster</h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>High availability — nodes, quorum and failover</p>
        </div>
        <button
          onClick={() => {
            qc.invalidateQueries({ queryKey: ['ha', 'status'] })
            qc.invalidateQueries({ queryKey: ['ha', 'local'] })
          }}
          style={btnGhost}
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
          border: `1px solid ${hasQuorum ? 'rgba(16,185,129,0.25)' : 'rgba(239,68,68,0.25)'}`,
        }}>
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
        <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14 }}>
          <Icon name="computer" size={28} style={{ color: 'var(--primary)', flexShrink: 0 }} />
          <div>
            <div style={{ fontWeight: 700, fontSize: 28, fontFamily: 'var(--font-mono)', lineHeight: 1 }}>
              {allNodes.length}
            </div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Total nodes</div>
          </div>
        </div>

        {/* Active node */}
        <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '18px 20px', display: 'flex', alignItems: 'center', gap: 14 }}>
          <Icon name="star" size={28} style={{ color: 'rgba(251,191,36,0.9)', flexShrink: 0 }} />
          <div style={{ minWidth: 0 }}>
            <div style={{ fontWeight: 700, fontSize: 'var(--text-md)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {activeNode?.name ?? activeNode?.id ?? '—'}
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
            onPromote={() => {
              if (window.confirm(`Promote ${node.name ?? node.id} to active?\n\nThis will trigger a failover.`)) {
                promotePeer.mutate({ id: node.id, name: node.name ?? node.id })
              }
            }}
            onRemove={() => {
              if (window.confirm(`Remove peer ${node.name ?? node.id} from the cluster?`)) {
                removePeer.mutate(node.id)
              }
            }}
            pending={pending}
          />
        ))}

        {allNodes.length === 0 && (
          <div style={{
            display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
            padding: '48px 0', gap: 12,
            background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)',
          }}>
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
    </div>
  )
}
