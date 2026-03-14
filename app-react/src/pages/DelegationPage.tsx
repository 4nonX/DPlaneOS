/**
 * pages/DelegationPage.tsx — ZFS Delegation
 *
 * Grant fine-grained ZFS permissions to non-root users via `zfs allow`.
 *
 * APIs (matching daemon routes exactly):
 *   GET  /api/zfs/delegation?dataset=X   → { success, dataset, delegations: string (raw zfs allow output) }
 *   POST /api/zfs/delegation              → { dataset, user, permissions } → { success }
 *   POST /api/zfs/delegation/revoke       → { dataset, user, permissions } → { success }
 *   GET  /api/zfs/datasets                → { success, data: ZFSDataset[] }
 *
 * Note: The daemon returns raw `zfs allow` text output per dataset.
 * This page maintains a client-side list of delegations that the user
 * has added in this session, plus displays the raw per-dataset delegation
 * text for auditing. Revoke sends the exact permissions string back.
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { ErrorState } from '@/components/ui/ErrorState'
import { LoadingState, Spinner } from '@/components/ui/LoadingSpinner'
import { Tooltip } from '@/components/ui/Tooltip'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ZFSDataset {
  name:  string
  used:  string
  avail: string
}

interface DatasetsResponse {
  success: boolean
  data:    ZFSDataset[]
}

interface DelegationResponse {
  success:     boolean
  dataset?:    string
  delegations?: string
  error?:      string
}

interface DelegationSetResponse {
  success:     boolean
  dataset?:    string
  user?:       string
  permissions?: string
  error?:      string
}

// A structured delegation entry we track client-side after adding
interface DelegationEntry {
  id:          string   // synthetic local ID for React keying
  dataset:     string
  principal:   string   // maps to `user` field in API
  permissions: string   // comma-separated
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const ALL_PERMISSIONS = [
  'create', 'destroy', 'mount', 'snapshot', 'rollback',
  'clone', 'send', 'receive', 'quota', 'reservation',
  'hold', 'release',
] as const

type Permission = typeof ALL_PERMISSIONS[number]

// ---------------------------------------------------------------------------
// Add Delegation Modal
// ---------------------------------------------------------------------------

interface AddDelegationModalProps {
  onClose:   () => void
  datasets:  ZFSDataset[]
  datasetsLoading: boolean
  onAdded:   (entry: DelegationEntry) => void
}

function AddDelegationModal({ onClose, datasets, datasetsLoading, onAdded }: AddDelegationModalProps) {
  const qc = useQueryClient()
  const [dataset, setDataset]         = useState('')
  const [principal, setPrincipal]     = useState('')
  const [selected, setSelected]       = useState<Set<Permission>>(new Set())

  function togglePerm(p: Permission) {
    setSelected((prev) => {
      const next = new Set(prev)
      next.has(p) ? next.delete(p) : next.add(p)
      return next
    })
  }

  function selectAll()   { setSelected(new Set(ALL_PERMISSIONS)) }
  function clearAll()    { setSelected(new Set()) }

  const addMutation = useMutation({
    mutationFn: (vars: { dataset: string; user: string; permissions: string }) =>
      api.post<DelegationSetResponse>('/api/zfs/delegation', vars),
    onSuccess: (data, vars) => {
      if (data.success) {
        toast.success(`Delegation granted on ${vars.dataset} for ${vars.user}`)
        const entry: DelegationEntry = {
          id:          `${vars.dataset}::${vars.user}::${vars.permissions}::${Date.now()}`,
          dataset:     vars.dataset,
          principal:   vars.user,
          permissions: vars.permissions,
        }
        onAdded(entry)
        qc.invalidateQueries({ queryKey: ['zfs', 'delegation'] })
        onClose()
      } else {
        toast.error(`Failed to grant delegation: ${data.error ?? 'Unknown error'}`)
      }
    },
    onError: (err) => {
      toast.error(`Grant failed: ${(err as Error).message}`)
    },
  })

  function handleSave() {
    if (!dataset)    { toast.warning('Select a dataset'); return }
    if (!principal.trim()) { toast.warning('Enter a principal (username or @groupname)'); return }
    if (selected.size === 0) { toast.warning('Select at least one permission'); return }
    addMutation.mutate({
      dataset,
      user: principal.trim(),
      permissions: [...selected].join(','),
    })
  }

  return (
    <Modal title="Add Delegation" onClose={onClose} size="md">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, padding: '4px 0' }}>

        {/* Dataset */}
        <div className="form-group">
          <label className="form-label">Dataset <span style={{ color: 'var(--error)' }}>*</span></label>
          {datasetsLoading ? (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--text-secondary)', fontSize: 'var(--text-sm)' }}>
              <Spinner size={14} /> Loading datasets…
            </div>
          ) : (
            <select
              className="form-select"
              value={dataset}
              onChange={(e) => setDataset(e.target.value)}
            >
              <option value="">— Select dataset —</option>
              {datasets.map((d) => (
                <option key={d.name} value={d.name}>{d.name}</option>
              ))}
            </select>
          )}
        </div>

        {/* Principal */}
        <div className="form-group">
          <label className="form-label">
            Principal <span style={{ color: 'var(--error)' }}>*</span>
            <span style={{ color: 'var(--text-tertiary)', fontWeight: 400, marginLeft: 6 }}>
              username or @groupname
            </span>
          </label>
          <input
            type="text"
            className="form-input"
            placeholder="e.g. alice or @storage-team"
            value={principal}
            onChange={(e) => setPrincipal(e.target.value)}
            maxLength={64}
          />
        </div>

        {/* Permissions */}
        <div className="form-group">
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 8 }}>
            <label className="form-label" style={{ margin: 0 }}>
              Permissions <span style={{ color: 'var(--error)' }}>*</span>
            </label>
            <div style={{ display: 'flex', gap: 8 }}>
              <button
                type="button"
                className="btn btn-ghost"
                style={{ fontSize: 'var(--text-xs)', padding: '3px 8px' }}
                onClick={selectAll}
              >
                All
              </button>
              <button
                type="button"
                className="btn btn-ghost"
                style={{ fontSize: 'var(--text-xs)', padding: '3px 8px' }}
                onClick={clearAll}
              >
                None
              </button>
            </div>
          </div>
          <div style={{
            display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: '8px 12px',
            padding: '12px 14px', background: 'var(--surface)',
            border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
          }}>
            {ALL_PERMISSIONS.map((p) => (
              <label
                key={p}
                style={{
                  display: 'flex', alignItems: 'center', gap: 7,
                  cursor: 'pointer', fontSize: 'var(--text-sm)',
                  color: selected.has(p) ? 'var(--text)' : 'var(--text-secondary)',
                  userSelect: 'none',
                }}
              >
                <input
                  type="checkbox"
                  checked={selected.has(p)}
                  onChange={() => togglePerm(p)}
                  style={{ accentColor: 'var(--primary)', width: 14, height: 14 }}
                />
                <code style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{p}</code>
              </label>
            ))}
          </div>
        </div>

        {/* Selected preview */}
        {selected.size > 0 && (
          <div style={{
            fontSize: 'var(--text-xs)', color: 'var(--text-secondary)',
            fontFamily: 'var(--font-mono)', padding: '8px 12px',
            background: 'rgba(138,156,255,0.06)', borderRadius: 'var(--radius-xs)',
            border: '1px solid rgba(138,156,255,0.15)',
          }}>
            {[...selected].join(', ')}
          </div>
        )}

        {addMutation.isPending && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            <Spinner size={14} /> Granting permissions…
          </div>
        )}
        {addMutation.isError && <ErrorState error={addMutation.error} title="Grant failed" />}
      </div>

      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={onClose} disabled={addMutation.isPending}>
          Cancel
        </button>
        <button
          className="btn btn-primary"
          onClick={handleSave}
          disabled={addMutation.isPending || !dataset || !principal.trim() || selected.size === 0}
        >
          {addMutation.isPending ? (
            <><Spinner size={14} /> Saving…</>
          ) : (
            <>
              <Icon name="lock_open" size={15} /> Save Delegation
            </>
          )}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// Dataset Delegation Detail (expandable raw view)
// ---------------------------------------------------------------------------

function DatasetDelegationDetail({ dataset }: { dataset: string }) {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['zfs', 'delegation', dataset],
    queryFn: ({ signal }) =>
      api.get<DelegationResponse>(
        `/api/zfs/delegation?dataset=${encodeURIComponent(dataset)}`,
        signal
      ),
    staleTime: 30_000,
  })

  if (isLoading) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', padding: '8px 0' }}>
        <Spinner size={12} /> Loading…
      </div>
    )
  }

  if (isError) {
    return <ErrorState error={error} title="Could not load delegations" />
  }

  if (!data?.delegations) {
    return (
      <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontStyle: 'italic' }}>
        No delegations configured for this dataset
      </div>
    )
  }

  return (
    <pre style={{
      margin: 0, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)',
      color: 'var(--text-secondary)', whiteSpace: 'pre-wrap', wordBreak: 'break-word',
      background: 'var(--surface)', border: '1px solid var(--border)',
      borderRadius: 'var(--radius-xs)', padding: '10px 12px',
      maxHeight: 200, overflowY: 'auto',
    }}>
      {data.delegations}
    </pre>
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function DelegationPage() {
  const qc = useQueryClient()
  const [showAddModal, setShowAddModal]   = useState(false)
  const [delegations, setDelegations]     = useState<DelegationEntry[]>([])
  const [expandedDataset, setExpandedDataset] = useState<string | null>(null)
  const [revokingId, setRevokingId]       = useState<string | null>(null)

  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets'],
    queryFn: ({ signal }) => api.get<DatasetsResponse>('/api/zfs/datasets', signal),
    staleTime: 60_000,
  })

  const revokeMutation = useMutation({
    mutationFn: (vars: { dataset: string; user: string; permissions: string; entryId: string }) =>
      api.post<DelegationSetResponse>('/api/zfs/delegation/revoke', {
        dataset:     vars.dataset,
        user:        vars.user,
        permissions: vars.permissions,
      }),
    onMutate: (vars) => setRevokingId(vars.entryId),
    onSuccess: (data, vars) => {
      setRevokingId(null)
      if (data.success) {
        setDelegations((prev) => prev.filter((d) => d.id !== vars.entryId))
        toast.success(`Delegation revoked for ${vars.user} on ${vars.dataset}`)
        qc.invalidateQueries({ queryKey: ['zfs', 'delegation'] })
      } else {
        toast.error(`Revoke failed: ${(data as DelegationSetResponse & { error?: string }).error ?? 'Unknown error'}`)
      }
    },
    onError: (err) => {
      setRevokingId(null)
      toast.error(`Revoke failed: ${(err as Error).message}`)
    },
  })

  const datasets = datasetsQ.data?.data ?? []

  function handleAdded(entry: DelegationEntry) {
    setDelegations((prev) => [...prev, entry])
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24, maxWidth: 1100 }}>

      {/* Page header */}
      <div className="page-header">
        <div>
          <h1 className="page-title">ZFS Delegation</h1>
          <p className="page-subtitle">Grant fine-grained ZFS permissions to non-root users via <code>zfs allow</code></p>
        </div>
        <button className="btn btn-primary" onClick={() => setShowAddModal(true)} style={{ flexShrink: 0 }}>
          <Icon name="add" size={15} /> Add Delegation
        </button>
      </div>

      {/* Info card */}
      <div style={{
        display: 'flex', gap: 12, padding: '14px 16px',
        background: 'rgba(138,156,255,0.06)', border: '1px solid rgba(138,156,255,0.18)',
        borderRadius: 'var(--radius-md)',
      }}>
        <Icon name="info" size={18} style={{ color: 'var(--primary)', flexShrink: 0, marginTop: 1 }} />
        <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', lineHeight: 1.6 }}>
          ZFS delegation (<code>zfs allow</code>) lets you grant specific ZFS operations to non-root
          users or groups without giving them full root access. Permissions are enforced by the ZFS kernel
          module — users can only perform the operations explicitly delegated to them on the specified dataset.
        </div>
      </div>

      {/* Delegations table */}
      <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 20 }}>
          <Icon name="lock_open" size={18} style={{ color: 'var(--primary)' }} />
          <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>Delegations</span>
          {delegations.length > 0 && (
            <span style={{ marginLeft: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              {delegations.length} configured this session
            </span>
          )}
        </div>

        {datasetsQ.isLoading && <LoadingState message="Loading datasets…" />}
        {datasetsQ.isError && (
          <ErrorState error={datasetsQ.error} onRetry={() => datasetsQ.refetch()} />
        )}

        {!datasetsQ.isLoading && delegations.length === 0 && (
          <div className="empty-state">
            <Icon name="lock_open" size={40} style={{ color: 'var(--text-tertiary)', opacity: 0.5 }} />
            <div style={{ fontWeight: 600, marginTop: 12 }}>No delegations configured</div>
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)', marginTop: 4, maxWidth: 360, textAlign: 'center' }}>
              Add a delegation to grant fine-grained ZFS permissions to a user or group.
            </div>
            <button className="btn btn-primary" onClick={() => setShowAddModal(true)} style={{ marginTop: 16 }}>
              <Icon name="add" size={15} /> Add Delegation
            </button>
          </div>
        )}

        {delegations.length > 0 && (
          <div style={{ overflowX: 'auto' }}>
            <table className="data-table">
              <thead>
                <tr>
                  <th>Dataset</th>
                  <th>Principal</th>
                  <th>Permissions</th>
                  <th style={{ width: 100 }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {delegations.map((d) => (
                  <tr key={d.id}>
                    <td>
                      <code style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>
                        {d.dataset}
                      </code>
                    </td>
                    <td>
                      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }}>
                        {d.principal}
                      </span>
                    </td>
                    <td>
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                        {d.permissions.split(',').map((p) => (
                          <span
                            key={p}
                            style={{
                              padding: '2px 7px', borderRadius: 'var(--radius-xs)',
                              fontSize: 'var(--text-2xs)', fontWeight: 600,
                              background: 'var(--primary-bg)', color: 'var(--primary)',
                              border: '1px solid rgba(138,156,255,0.2)',
                              fontFamily: 'var(--font-mono)',
                            }}
                          >
                            {p.trim()}
                          </span>
                        ))}
                      </div>
                    </td>
                    <td>
                      <Tooltip content="Revoke this delegation">
                        <button
                          className="btn btn-ghost"
                          onClick={() => revokeMutation.mutate({
                            dataset:     d.dataset,
                            user:        d.principal,
                            permissions: d.permissions,
                            entryId:     d.id,
                          })}
                          disabled={revokingId === d.id}
                          style={{ fontSize: 'var(--text-xs)', color: 'var(--error)' }}
                        >
                          {revokingId === d.id ? (
                            <Spinner size={13} />
                          ) : (
                            <><Icon name="block" size={14} /> Revoke</>
                          )}
                        </button>
                      </Tooltip>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {/* Per-dataset raw delegation view */}
      {datasets.length > 0 && (
        <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 24 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16 }}>
            <Icon name="terminal" size={18} style={{ color: 'var(--primary)' }} />
            <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>Raw Delegation View</span>
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginLeft: 4 }}>
              Live <code>zfs allow</code> output per dataset
            </span>
          </div>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
            {datasets.map((ds) => {
              const isExpanded = expandedDataset === ds.name
              return (
                <div key={ds.name} style={{ border: '1px solid var(--border-subtle)', borderRadius: 'var(--radius-sm)', overflow: 'hidden' }}>
                  <button
                    onClick={() => setExpandedDataset(isExpanded ? null : ds.name)}
                    style={{
                      width: '100%', display: 'flex', alignItems: 'center', gap: 10,
                      padding: '10px 14px', background: isExpanded ? 'rgba(138,156,255,0.06)' : 'none',
                      border: 'none', cursor: 'pointer',
                      color: isExpanded ? 'var(--primary)' : 'var(--text-secondary)',
                      fontSize: 'var(--text-sm)', fontFamily: 'var(--font-ui)',
                      textAlign: 'left',
                    }}
                  >
                    <Icon name="folder" size={15} style={{ flexShrink: 0, opacity: 0.7 }} />
                    <code style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', flex: 1 }}>
                      {ds.name}
                    </code>
                    <Icon
                      name="expand_more"
                      size={15}
                      style={{ transform: isExpanded ? 'rotate(180deg)' : 'rotate(0deg)', transition: 'transform 0.2s', opacity: 0.5 }}
                    />
                  </button>
                  {isExpanded && (
                    <div style={{ padding: '0 14px 14px' }}>
                      <DatasetDelegationDetail dataset={ds.name} />
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      )}

      {/* Add Delegation Modal */}
      {showAddModal && (
        <AddDelegationModal
          onClose={() => setShowAddModal(false)}
          datasets={datasets}
          datasetsLoading={datasetsQ.isLoading}
          onAdded={handleAdded}
        />
      )}
    </div>
  )
}
