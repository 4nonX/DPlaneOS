/**
 * pages/SandboxPage.tsx - ZFS Sandbox
 *
 * Ephemeral ZFS clone environments for safe testing.
 * Destroy to revert - zero data risk to production datasets.
 *
 * APIs:
 *   GET    /api/sandbox/list              → { success, sandboxes, count }
 *   POST   /api/sandbox/create            → { success, sandbox, mountpoint, origin, duration_ms, hint }
 *   POST   /api/sandbox/destroy           → { success, destroyed, origin_cleaned }
 *   POST   /api/sandbox/cleanup           → { success, output }
 *   GET    /api/zfs/datasets              → { success, data: ZFSDataset[] }
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

interface SandboxInfo {
  name:       string   // e.g. tank/sandboxes/sandbox-20250215
  origin:     string   // source snapshot
  mountpoint: string
  used:       string
  creation:   string
}

interface SandboxListResponse {
  success:   boolean
  sandboxes: SandboxInfo[]
  count:     number
}

interface SandboxCreateResponse {
  success:     boolean
  sandbox?:    string
  mountpoint?: string
  origin?:     string
  duration_ms?: number
  hint?:       string
  error?:      string
}

interface SandboxDestroyResponse {
  success:         boolean
  destroyed?:      string
  origin_cleaned?: boolean
  error?:          string
}

interface SandboxCleanupResponse {
  success: boolean
  output?: string
  error?:  string
}

interface ZFSDataset {
  name:  string
  used:  string
  avail: string
}

interface DatasetsResponse {
  success: boolean
  data:    ZFSDataset[]
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Returns the short display name for a sandbox dataset path */
function sandboxShortName(fullName: string): string {
  const parts = fullName.split('/')
  return parts[parts.length - 1] ?? fullName
}

/** Truncates a Docker container ID for display */
function truncateId(id: string, len = 12): string {
  return id.length > len ? id.slice(0, len) : id
}

// ---------------------------------------------------------------------------
// New Sandbox Modal
// ---------------------------------------------------------------------------

interface NewSandboxModalProps {
  onClose: () => void
  datasets: ZFSDataset[]
  datasetsLoading: boolean
}

function NewSandboxModal({ onClose, datasets, datasetsLoading }: NewSandboxModalProps) {
  const qc = useQueryClient()
  const [sandboxName, setSandboxName] = useState('')
  const [sourceDataset, setSourceDataset] = useState('')

  const createMutation = useMutation({
    mutationFn: (vars: { dataset: string; name: string }) =>
      api.post<SandboxCreateResponse>('/api/sandbox/create', vars),
    onSuccess: (data) => {
      if (data.success) {
        toast.success(`Sandbox "${data.sandbox}" created`)
        qc.invalidateQueries({ queryKey: ['sandbox', 'list'] })
        onClose()
      } else {
        toast.error(`Failed to create sandbox: ${data.error ?? 'Unknown error'}`)
      }
    },
    onError: (err) => {
      toast.error(`Create failed: ${(err as Error).message}`)
    },
  })

  function handleCreate() {
    if (!sourceDataset) { toast.warning('Select a source dataset'); return }
    createMutation.mutate({ dataset: sourceDataset, name: sandboxName.trim() })
  }

  return (
    <Modal title="New Sandbox" onClose={onClose} size="md">
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16, padding: '4px 0' }}>
        {/* Sandbox Name */}
        <div className="form-group">
          <label className="form-label">Sandbox Name <span style={{ color: 'var(--text-tertiary)', fontWeight: 400 }}>(optional - auto-generated if empty)</span></label>
          <input
            type="text"
            className="form-input"
            placeholder="e.g. test-migration"
            value={sandboxName}
            onChange={(e) => setSandboxName(e.target.value)}
            maxLength={64}
          />
        </div>

        {/* Source Dataset */}
        <div className="form-group">
          <label className="form-label">Source Dataset <span style={{ color: 'var(--error)' }}>*</span></label>
          {datasetsLoading ? (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--text-secondary)', fontSize: 'var(--text-sm)' }}>
              <Spinner size={14} /> Loading datasets…
            </div>
          ) : (
            <select
              className="form-select"
              value={sourceDataset}
              onChange={(e) => setSourceDataset(e.target.value)}
            >
              <option value="">- Select dataset -</option>
              {datasets.map((d) => (
                <option key={d.name} value={d.name}>{d.name}</option>
              ))}
            </select>
          )}
        </div>

        {/* Info note */}
        <div style={{
          display: 'flex', gap: 8, padding: '10px 12px',
          background: 'rgba(138,156,255,0.06)', borderRadius: 'var(--radius-sm)',
          border: '1px solid rgba(138,156,255,0.15)', fontSize: 'var(--text-xs)',
          color: 'var(--text-secondary)'}}>
          <Icon name="info" size={14} style={{ color: 'var(--primary)', flexShrink: 0, marginTop: 1 }} />
          <span>
            A snapshot will be taken of the source dataset, then an instant ZFS clone created from it.
            The sandbox is writable but isolated - destroy it to revert all changes.
          </span>
        </div>

        {createMutation.isPending && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            <Spinner size={14} /> Creating sandbox…
          </div>
        )}
        {createMutation.isError && (
          <ErrorState error={createMutation.error} title="Create failed" />
        )}
      </div>

      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={onClose} disabled={createMutation.isPending}>
          Cancel
        </button>
        <button
          className="btn btn-primary"
          onClick={handleCreate}
          disabled={createMutation.isPending || !sourceDataset}
        >
          {createMutation.isPending ? (
            <><Spinner size={14} /> Creating…</>
          ) : (
            <>
              <Icon name="add" size={15} /> Create Sandbox
            </>
          )}
        </button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// Sandbox Card
// ---------------------------------------------------------------------------

interface SandboxCardProps {
  sandbox: SandboxInfo
  onDestroy: (name: string) => void
  isDestroying: boolean
}

function SandboxCard({ sandbox, onDestroy, isDestroying }: SandboxCardProps) {
  const shortName  = sandboxShortName(sandbox.name)
  // Extract the source dataset name from the origin snapshot (strip @snapshot suffix)
  const sourceDataset = sandbox.origin.includes('@')
    ? sandbox.origin.split('@')[0]
    : sandbox.origin

  return (
    <div style={{
      background: 'rgba(255,255,255,0.02)',
      border: '1px solid var(--border-subtle)',
      borderRadius: 'var(--radius-md)',
      padding: '16px 18px',
      display: 'flex',
      flexDirection: 'column',
      gap: 10}}>
      {/* Header row */}
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12 }}>
        <div style={{
          width: 36, height: 36, borderRadius: 'var(--radius-sm)',
          background: 'var(--primary-bg)', display: 'flex', alignItems: 'center',
          justifyContent: 'center', flexShrink: 0,
          border: '1px solid rgba(138,156,255,0.2)'}}>
          <Icon name="science" size={18} style={{ color: 'var(--primary)' }} />
        </div>

        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontWeight: 600, fontSize: 'var(--text-md)', marginBottom: 2 }}>
            {shortName}
          </div>
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {sandbox.name}
          </div>
        </div>

        {/* Status badge */}
        <span style={{
          padding: '3px 9px', borderRadius: 'var(--radius-xs)',
          fontSize: 'var(--text-xs)', fontWeight: 600,
          background: 'var(--success-bg)', color: 'var(--success)',
          border: '1px solid var(--success-border)', flexShrink: 0}}>
          Active
        </span>
      </div>

      {/* Metadata grid */}
      <div style={{
        display: 'grid', gridTemplateColumns: '1fr 1fr', gap: '6px 16px',
        fontSize: 'var(--text-xs)', color: 'var(--text-secondary)'}}>
        <div>
          <span style={{ color: 'var(--text-tertiary)' }}>Source dataset: </span>
          <span style={{ fontFamily: 'var(--font-mono)' }}>{sourceDataset}</span>
        </div>
        <div>
          <span style={{ color: 'var(--text-tertiary)' }}>Used: </span>
          {sandbox.used}
        </div>
        <div>
          <span style={{ color: 'var(--text-tertiary)' }}>Mountpoint: </span>
          <span style={{ fontFamily: 'var(--font-mono)' }}>{sandbox.mountpoint}</span>
        </div>
        {sandbox.creation && (
          <div>
            <span style={{ color: 'var(--text-tertiary)' }}>Created: </span>
            {sandbox.creation}
          </div>
        )}
      </div>

      {/* Origin snapshot */}
      <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
        <span>Origin snapshot: </span>
        <span style={{ fontFamily: 'var(--font-mono)' }}>
          {truncateId(sandbox.origin, 48)}
          {sandbox.origin.length > 48 && '…'}
        </span>
      </div>

      {/* Destroy button */}
      <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 4 }}>
        <button
          className="btn btn-danger"
          onClick={() => onDestroy(sandbox.name)}
          disabled={isDestroying}
          style={{ fontSize: 'var(--text-xs)' }}
        >
          {isDestroying ? (
            <><Spinner size={13} /> Destroying…</>
          ) : (
            <><Icon name="delete" size={14} /> Destroy</>
          )}
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function SandboxPage() {
  const qc = useQueryClient()
  const [showNewModal, setShowNewModal]     = useState(false)
  const [destroyingName, setDestroyingName] = useState<string | null>(null)

  const sandboxesQ = useQuery({
    queryKey: ['sandbox', 'list'],
    queryFn: ({ signal }) => api.get<SandboxListResponse>('/api/sandbox/list', signal),
    refetchInterval: 30_000,
  })

  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets'],
    queryFn: ({ signal }) => api.get<DatasetsResponse>('/api/zfs/datasets', signal),
    staleTime: 60_000,
  })

  const destroyMutation = useMutation({
    mutationFn: (sandboxName: string) =>
      api.post<SandboxDestroyResponse>('/api/sandbox/destroy', { sandbox: sandboxName }),
    onMutate: (name) => setDestroyingName(name),
    onSuccess: (data, name) => {
      setDestroyingName(null)
      if (data.success) {
        toast.success(`Sandbox "${sandboxShortName(name)}" destroyed`)
        qc.invalidateQueries({ queryKey: ['sandbox', 'list'] })
      } else {
        toast.error(`Destroy failed: ${data.error ?? 'Unknown error'}`)
      }
    },
    onError: (err) => {
      setDestroyingName(null)
      toast.error(`Destroy failed: ${(err as Error).message}`)
    },
  })

  const cleanupMutation = useMutation({
    mutationFn: () => api.post<SandboxCleanupResponse>('/api/sandbox/cleanup', {}),
    onSuccess: (data) => {
      if (data.success) {
        toast.success('Orphan Docker volumes pruned')
        qc.invalidateQueries({ queryKey: ['sandbox', 'list'] })
      } else {
        toast.error(`Cleanup failed: ${data.error ?? 'Unknown error'}`)
      }
    },
    onError: (err) => {
      toast.error(`Cleanup failed: ${(err as Error).message}`)
    },
  })

  const sandboxes = sandboxesQ.data?.sandboxes ?? []
  const datasets  = datasetsQ.data?.data ?? []

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24, maxWidth: 1100 }}>

      {/* Page header */}
      <div className="page-header">
        <div>
          <h1 className="page-title">Sandbox</h1>
          <p className="page-subtitle">Ephemeral ZFS clone environments - test changes safely, destroy to revert</p>
        </div>
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexShrink: 0 }}>
          <Tooltip content="Remove orphaned Docker volumes left by destroyed sandboxes">
            <button
              className="btn btn-ghost"
              onClick={() => cleanupMutation.mutate()}
              disabled={cleanupMutation.isPending}
            >
              {cleanupMutation.isPending ? (
                <><Spinner size={14} /> Cleaning…</>
              ) : (
                <><Icon name="cleaning_services" size={15} /> Clean Orphans</>
              )}
            </button>
          </Tooltip>
          <button className="btn btn-primary" onClick={() => setShowNewModal(true)}>
            <Icon name="add" size={15} /> New Sandbox
          </button>
        </div>
      </div>

      {/* Clean Orphans warning */}
      <div className="alert alert-warning" style={{ fontSize: 'var(--text-sm)' }}>
        <Icon name="warning" size={16} />
        <span>
          <strong>Clean Orphans</strong> runs <code>docker volume prune -f</code> - this removes
          all unused Docker volumes on the system, not just sandbox volumes. Use with care.
        </span>
      </div>

      {/* Cleanup result */}
      {cleanupMutation.isSuccess && cleanupMutation.data?.success && cleanupMutation.data.output && (
        <div className="alert alert-success" style={{ fontSize: 'var(--text-xs)' }}>
          <Icon name="check_circle" size={14} />
          <pre style={{ margin: 0, fontFamily: 'var(--font-mono)', whiteSpace: 'pre-wrap' }}>
            {cleanupMutation.data.output}
          </pre>
        </div>
      )}

      {/* Sandbox list */}
      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 20 }}>
          <Icon name="science" size={18} style={{ color: 'var(--primary)' }} />
          <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>Active Sandboxes</span>
          {!sandboxesQ.isLoading && (
            <span style={{ marginLeft: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              {sandboxes.length} active
            </span>
          )}
          <Tooltip content="Refresh">
            <button
              onClick={() => sandboxesQ.refetch()}
              style={{
              marginLeft: 'auto', background: 'none', border: 'none', cursor: 'pointer',
              color: 'var(--text-tertiary)', display: 'flex', alignItems: 'center',
              padding: '4px', borderRadius: 'var(--radius-xs)',
              transition: 'color var(--transition-fast)'}}
            onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--text)' }}
            onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-tertiary)' }}
          >
            <Icon name="refresh" size={16} />
          </button>
          </Tooltip>
        </div>

        {sandboxesQ.isLoading && <LoadingState message="Loading sandboxes…" />}
        {sandboxesQ.isError && (
          <ErrorState error={sandboxesQ.error} onRetry={() => sandboxesQ.refetch()} />
        )}

        {!sandboxesQ.isLoading && sandboxes.length === 0 && (
          <div className="empty-state">
            <Icon name="science" size={40} style={{ color: 'var(--text-tertiary)', opacity: 0.5 }} />
            <div style={{ fontWeight: 600, marginTop: 12 }}>No sandboxes yet</div>
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)', marginTop: 4, maxWidth: 360, textAlign: 'center' }}>
              Create a sandbox to get an instant ZFS clone of any dataset.
              Test destructive changes safely - destroy the sandbox to revert.
            </div>
            <button className="btn btn-primary" onClick={() => setShowNewModal(true)} style={{ marginTop: 16 }}>
              <Icon name="add" size={15} /> New Sandbox
            </button>
          </div>
        )}

        {sandboxes.length > 0 && (
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(380px, 1fr))', gap: 14 }}>
            {sandboxes.map((sb) => (
              <SandboxCard
                key={sb.name}
                sandbox={sb}
                isDestroying={destroyingName === sb.name}
                onDestroy={(name) => destroyMutation.mutate(name)}
              />
            ))}
          </div>
        )}
      </div>

      {/* New Sandbox Modal */}
      {showNewModal && (
        <NewSandboxModal
          onClose={() => setShowNewModal(false)}
          datasets={datasets}
          datasetsLoading={datasetsQ.isLoading}
        />
      )}
    </div>
  )
}

