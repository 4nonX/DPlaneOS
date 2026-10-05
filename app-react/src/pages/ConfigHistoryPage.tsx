/**
 * pages/ConfigHistoryPage.tsx - Configuration history (Design 0001, Phase 1)
 *
 * Every change to a managed resource (datasets, shares, NFS, users, groups,
 * stacks, replication, system settings, ...) is recorded as a revision: right
 * after a change in the web UI, and every 5 minutes for changes made elsewhere.
 *
 * Calls:
 *   GET  /api/config/history?kind=&limit=&before=  → { revisions[] }
 *   POST /api/config/rollback { revision_id }      → { success, result }
 *   POST /api/config/capture                       → { changed }
 *   GET  /api/config/export                        → state.yaml download
 */

import { useMemo, useState } from 'react'
import { useInfiniteQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk, getSessionId } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'

interface FieldChange { path: string; old: unknown; new: unknown }
interface Revision {
  id: number
  changeset: string
  scope: string
  scope_id: string
  kind: string
  key: string
  origin: 'baseline' | 'gui' | 'detected' | 'import' | 'rollback' | 'git' | 'peer' | 'merge'
  origin_node: string
  author: string
  note: string
  created_at: string
  base_revision?: number          // previous revision of this resource
  changes: FieldChange[]
  created: boolean
  deleted: boolean
  can_rollback: boolean
}
interface HistoryResponse { success: boolean; revisions: Revision[] }
interface RollbackResponse {
  success: boolean
  error?: string
  result?: { applied?: string[]; no_change?: boolean; blocked?: { name: string; block_reason?: string }[] }
}

const PAGE = 100

const KIND_LABELS: Record<string, string> = {
  dataset: 'Dataset', share: 'SMB share', nfs: 'NFS export', stack: 'Stack', system: 'System settings',
  user: 'User', group: 'Group', replication: 'Replication', ldap: 'Directory (LDAP)', acme: 'ACME',
  certificate: 'Certificate', smart_task: 'SMART task', nvme_fabric: 'NVMe-oF export', pool: 'Pool',
}

const ORIGIN: Record<Revision['origin'], { label: string; cls: string; title: string }> = {
  gui:      { label: 'Web UI',    cls: 'badge-primary', title: 'Recorded right after a change in the web UI' },
  detected: { label: 'Detected',  cls: 'badge-warning', title: 'Found by the periodic check: changed outside the web UI (shell, other tools) or by an apply' },
  rollback: { label: 'Rollback',  cls: 'badge-neutral',    title: 'Result of a rollback from this history' },
  baseline: { label: 'Baseline',  cls: 'badge-neutral', title: 'Configuration when history recording started' },
  import:   { label: 'Import',    cls: 'badge-neutral', title: 'Imported from a state.yaml file' },
  git:      { label: 'Git',       cls: 'badge-neutral', title: 'Applied from the Git repository' },
  peer:     { label: 'Other node', cls: 'badge-primary', title: 'Changed on a paired node and applied here' },
  merge:    { label: 'Merged',    cls: 'badge-neutral', title: 'Joins changes made on two nodes: a resolved conflict, or the same change made on both' },
}

function fmtValue(v: unknown): string {
  if (v === null || v === undefined || v === '') return '—'
  if (typeof v === 'string') return v.startsWith('sha256:') ? '(secret changed)' : v
  if (typeof v === 'boolean') return v ? 'yes' : 'no'
  return JSON.stringify(v)
}

function fmtTime(s: string) {
  return new Date(s).toLocaleString()
}

/** Revisions grouped by changeset, in the order received (newest first). */
function groupByChangeset(revs: Revision[]) {
  const groups: { changeset: string; revisions: Revision[] }[] = []
  const index = new Map<string, number>()
  for (const r of revs) {
    const i = index.get(r.changeset)
    if (i === undefined) {
      index.set(r.changeset, groups.length)
      groups.push({ changeset: r.changeset, revisions: [r] })
    } else {
      groups[i].revisions.push(r)
    }
  }
  return groups
}

function ChangeTable({ rev }: { rev: Revision }) {
  if (rev.deleted) return <div style={{ fontSize: 'var(--text-sm)', color: 'var(--error)' }}>Deleted</div>
  if (rev.changes.length === 0) return <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>No field changes</div>
  return (
    <table className="data-table" style={{ fontSize: 'var(--text-sm)' }}>
      <thead><tr><th>Setting</th><th>Before</th><th>After</th></tr></thead>
      <tbody>
        {rev.changes.map(c => (
          <tr key={c.path}>
            <td style={{ fontFamily: 'var(--font-mono)' }}>{c.path}</td>
            <td style={{ color: 'var(--text-tertiary)', wordBreak: 'break-all' }}>{rev.created ? '—' : fmtValue(c.old)}</td>
            <td style={{ wordBreak: 'break-all' }}>{fmtValue(c.new)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

export function ConfigHistoryPage() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [kind, setKind] = useState('')
  const [open, setOpen] = useState<Record<number, boolean>>({})

  const history = useInfiniteQuery({
    queryKey: ['config', 'history', kind],
    initialPageParam: 0,
    queryFn: ({ pageParam, signal }) =>
      api.get<HistoryResponse>(`/api/config/history?limit=${PAGE}&kind=${encodeURIComponent(kind)}${pageParam ? `&before=${pageParam}` : ''}`, signal),
    getNextPageParam: last => (last.revisions.length === PAGE ? last.revisions[last.revisions.length - 1].id : undefined),
    refetchInterval: 30_000,
  })
  const revisions = useMemo(() => (history.data?.pages ?? []).flatMap(p => p.revisions), [history.data])
  const groups = useMemo(() => groupByChangeset(revisions), [revisions])

  const capture = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; error?: string; changed?: boolean }>('/api/config/capture', {})),
    onSuccess: r => { toast.success(r.changed ? 'Changes recorded' : 'No changes since the last record'); qc.invalidateQueries({ queryKey: ['config', 'history'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const rollback = useMutation({
    mutationFn: async (id: number) => ensureOk(await api.post<RollbackResponse>('/api/config/rollback', { revision_id: id })),
    onSuccess: r => {
      toast.success(r.result?.no_change ? 'Already in that state' : 'Change undone')
      qc.invalidateQueries({ queryKey: ['config', 'history'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  /** Undo = set the resource back to its previous revision (the state before this change). */
  async function askUndo(rev: Revision) {
    if (!rev.base_revision) return
    const what = `${KIND_LABELS[rev.kind] ?? rev.kind} "${rev.key}"`
    const notes = [
      rev.deleted
        ? `${what} will be recreated with the settings it had before it was deleted.`
        : `${what} will be set back to how it was before this change. Later changes to it are undone too.`,
      rev.kind === 'dataset' && rev.deleted ? 'The dataset is recreated empty: its data is not restored, only its settings.' : '',
      'Safety rules still apply: a change that would destroy data or interrupt open connections is refused.',
    ].filter(Boolean).join(' ')
    if (await confirm({ title: `Undo this change to ${what}?`, message: notes, confirmLabel: 'Undo change' })) {
      rollback.mutate(rev.base_revision)
    }
  }

  async function exportYAML() {
    // Download through fetch so the session header is sent.
    const res = await fetch('/api/config/export', { headers: { 'X-Session-ID': getSessionId() ?? '' } })
    if (!res.ok) { toast.error(`Export failed (${res.status})`); return }
    const blob = await res.blob()
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = res.headers.get('Content-Disposition')?.match(/filename="([^"]+)"/)?.[1] ?? 'state.yaml'
    a.click()
    URL.revokeObjectURL(a.href)
  }

  return (
    <div className="page-container">
      <header className="page-header" style={{ display: 'flex', alignItems: 'flex-end', gap: 16, flexWrap: 'wrap' }}>
        <div style={{ flex: 1, minWidth: 260 }}>
          <h1 className="page-title">Change History</h1>
          <p className="page-subtitle">Every change to datasets, shares, users, stacks and settings, with what changed and the option to roll back.</p>
        </div>
        <select value={kind} onChange={e => setKind(e.target.value)} className="input" style={{ width: 180 }} aria-label="Filter by type">
          <option value="">All types</option>
          {Object.entries(KIND_LABELS).map(([k, l]) => <option key={k} value={k}>{l}</option>)}
        </select>
        <button className="btn btn-ghost" onClick={() => capture.mutate()} disabled={capture.isPending}>
          <Icon name="history" size={16} />Record now
        </button>
        <button className="btn btn-ghost" onClick={exportYAML}>
          <Icon name="download" size={16} />Export state.yaml
        </button>
      </header>

      {history.isLoading && <Skeleton height={300} />}
      {history.isError && <ErrorState error={history.error} onRetry={() => history.refetch()} />}
      {!history.isLoading && !history.isError && groups.length === 0 && (
        <div className="empty-state">
          <Icon name="history" className="empty-state-icon" />
          <p className="empty-state-title">No history yet</p>
          <p className="empty-state-body">The current configuration is recorded within a few minutes of starting; changes appear here from then on.</p>
        </div>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {groups.map(g => {
          const first = g.revisions[0]
          const origin = ORIGIN[first.origin] ?? ORIGIN.detected
          return (
            <div key={g.changeset} className="card" style={{ padding: 0, overflow: 'hidden' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 16px', borderBottom: '1px solid var(--border-subtle)', flexWrap: 'wrap' }}>
                <span className={`badge ${origin.cls}`} title={origin.title}>{origin.label}</span>
                <span style={{ fontWeight: 600 }}>{fmtTime(first.created_at)}</span>
                {first.author && <span style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-sm)' }}>by {first.author}</span>}
                {first.origin_node && <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>on {first.origin_node}</span>}
                {first.note && <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>· {first.note}</span>}
                <span style={{ marginLeft: 'auto', color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>
                  {g.revisions.length} resource{g.revisions.length === 1 ? '' : 's'}
                </span>
              </div>
              {g.revisions.map(rev => (
                <div key={rev.id} style={{ padding: '10px 16px', borderBottom: '1px solid var(--border-subtle)' }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                    <button type="button" className="btn btn-ghost btn-sm" onClick={() => setOpen(o => ({ ...o, [rev.id]: !o[rev.id] }))}
                      aria-expanded={!!open[rev.id]} aria-label={`Show changes of ${rev.kind} ${rev.key}`}>
                      <Icon name={open[rev.id] ? 'expand_less' : 'expand_more'} size={16} />
                    </button>
                    <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', minWidth: 110 }}>{KIND_LABELS[rev.kind] ?? rev.kind}</span>
                    <span style={{ fontFamily: 'var(--font-mono)', fontWeight: 600 }}>{rev.key}</span>
                    <span style={{ fontSize: 'var(--text-xs)', color: rev.deleted ? 'var(--error)' : rev.created ? 'var(--success)' : 'var(--text-secondary)' }}>
                      {rev.deleted ? 'deleted' : rev.created ? (first.origin === 'baseline' ? 'recorded' : 'created') : `${rev.changes.length} change${rev.changes.length === 1 ? '' : 's'}`}
                    </span>
                    <span style={{ marginLeft: 'auto' }} />
                    {rev.can_rollback && rev.base_revision && !rev.created && (
                      <button className="btn btn-ghost btn-sm" onClick={() => askUndo(rev)} disabled={rollback.isPending}
                        title="Set this resource back to how it was before this change">
                        <Icon name="undo" size={14} />Undo this change
                      </button>
                    )}
                  </div>
                  {open[rev.id] && <div style={{ marginTop: 8 }}><ChangeTable rev={rev} /></div>}
                </div>
              ))}
            </div>
          )
        })}
      </div>

      {history.hasNextPage && (
        <div style={{ display: 'flex', justifyContent: 'center', marginTop: 16 }}>
          <button className="btn btn-ghost" onClick={() => history.fetchNextPage()} disabled={history.isFetchingNextPage}>
            {history.isFetchingNextPage ? 'Loading…' : 'Load older changes'}
          </button>
        </div>
      )}
      <ConfirmDialog />
    </div>
  )
}
