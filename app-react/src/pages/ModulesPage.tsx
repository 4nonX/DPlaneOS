/**
 * pages/ModulesPage.tsx — App Modules / Compose Stacks (Phase 3)
 *
 * Shows compose stacks from git-sync. Each stack is a "module" with
 * start/stop/update controls. Also shows available built-in modules.
 *
 * Calls:
 *   GET  /api/docker/compose/status    → deployed stacks
 *   GET  /api/git-sync/stacks          → git-synced stacks
 *   POST /api/docker/action            → container action
 *   POST /api/docker/compose/up        → async job { job_id }
 *   POST /api/docker/compose/down      → async job { job_id }
 *   POST /api/docker/update            → async job { job_id } (safe pull+restart)
 */

import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ContainerIcon } from '@/components/ui/ContainerIcon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { JobProgress } from '@/components/ui/JobProgress'
import { toast } from '@/hooks/useToast'

interface IconMapEntry { match: string; icon: string }
interface IconMapResponse { success: boolean; map: IconMapEntry[] }

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ComposeStack {
  name:      string
  path:      string
  running:   boolean
  services:  number
  status?:   string
  git_url?:  string
  last_pull?: string
}
interface ComposeStatusResponse { success: boolean; stacks: ComposeStack[] }

interface GitStack {
  name:      string
  path:      string
  git_url?:  string
  deployed:  boolean
  last_sync?: string
}
interface GitStacksResponse { success: boolean; stacks: GitStack[] }

interface JobStartResponse { job_id: string }

// ---------------------------------------------------------------------------
// StackCard
// ---------------------------------------------------------------------------

function StackCard({ stack, onRefresh, iconMap }: { stack: ComposeStack; onRefresh: () => void; iconMap?: IconMapEntry[] }) {
  const [jobId, setJobId] = useState<string | null>(null)
  const [jobLabel, setJobLabel] = useState('')

  function startJob(action: 'up' | 'down' | 'update', label: string) {
    setJobLabel(label)
    setJobId(null)
    const endpoint = action === 'update' ? '/api/docker/update' : `/api/docker/compose/${action}`
    api.post<JobStartResponse>(endpoint, { stack: stack.name })
      .then(data => setJobId(data.job_id))
      .catch((e: Error) => toast.error(e.message))
  }

  const busy = !!jobId

  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 24 }}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 16, marginBottom: 16 }}>
        <div style={{ width: 44, height: 44, background: 'var(--primary-bg)', border: '1px solid rgba(138,156,255,0.2)', borderRadius: 'var(--radius-md)', display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
          <ContainerIcon image={stack.name} iconMap={iconMap} size={22} />
        </div>
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)', marginBottom: 2 }}>{stack.name}</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{stack.path}</div>
        </div>
        <span className={`badge ${stack.running ? 'badge-success' : 'badge-neutral'}`}>
          {stack.running ? 'RUNNING' : 'STOPPED'}
        </span>
      </div>

      {/* Meta */}
      <div style={{ display: 'flex', gap: 10, marginBottom: 16, flexWrap: 'wrap' }}>
        {stack.services > 0 && (
          <span style={{ padding: '3px 8px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            {stack.services} service{stack.services !== 1 ? 's' : ''}
          </span>
        )}
        {stack.git_url && (
          <span style={{ padding: '3px 8px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
            {stack.git_url}
          </span>
        )}
        {stack.status && (
          <span style={{ padding: '3px 8px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            {stack.status}
          </span>
        )}
      </div>

      {/* Job progress */}
      {jobId && (
        <div style={{ marginBottom: 14 }}>
          <JobProgress
            jobId={jobId}
            runningLabel={jobLabel}
            doneLabel="Operation completed"
            onDone={() => { setJobId(null); onRefresh() }}
            onFailed={() => setJobId(null)}
          />
        </div>
      )}

      {/* Actions */}
      <div style={{ display: 'flex', gap: 8 }}>
        {stack.running ? (
          <>
            <button onClick={() => startJob('update', `Updating ${stack.name}…`)} disabled={busy} className="btn btn-ghost">
              <Icon name="system_update_alt" size={15} />Update
            </button>
            <button onClick={() => startJob('down', `Stopping ${stack.name}…`)} disabled={busy} className="btn btn-ghost" style={{ color: 'var(--error)', borderColor: 'var(--error-border)' }}>
              <Icon name="stop" size={15} />Stop
            </button>
          </>
        ) : (
          <button onClick={() => startJob('up', `Starting ${stack.name}…`)} disabled={busy} className="btn btn-primary">
            <Icon name="play_arrow" size={15} />Start
          </button>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ModulesPage
// ---------------------------------------------------------------------------

export function ModulesPage() {
  const qc = useQueryClient()
  const [filter, setFilter] = useState('')

  const composeQ = useQuery({
    queryKey: ['docker', 'compose', 'status'],
    queryFn: ({ signal }) => api.get<ComposeStatusResponse>('/api/docker/compose/status', signal),
    refetchInterval: 20_000,
  })
  const gitStacksQ = useQuery({
    queryKey: ['git-sync', 'stacks'],
    queryFn: ({ signal }) => api.get<GitStacksResponse>('/api/git-sync/stacks', signal),
    refetchInterval: 30_000,
  })
  const iconMapQ = useQuery({
    queryKey: ['docker', 'icon-map'],
    queryFn: ({ signal }) => api.get<IconMapResponse>('/api/docker/icon-map', signal),
    staleTime: 60 * 60 * 1000,
  })

  function refresh() {
    qc.invalidateQueries({ queryKey: ['docker', 'compose', 'status'] })
    qc.invalidateQueries({ queryKey: ['git-sync', 'stacks'] })
  }

  const composeStacks = composeQ.data?.stacks ?? []
  const gitStacks = gitStacksQ.data?.stacks ?? []

  // Merge: git stacks that aren't already in compose status
  const allStacks: ComposeStack[] = [
    ...composeStacks,
    ...gitStacks
      .filter(g => !composeStacks.some(c => c.name === g.name))
      .map(g => ({ name: g.name, path: g.path, running: g.deployed, services: 0, git_url: g.git_url })),
  ]

  const filtered = allStacks.filter(s =>
    !filter || s.name.toLowerCase().includes(filter.toLowerCase())
  )

  const isLoading = composeQ.isLoading && gitStacksQ.isLoading

  return (
    <div style={{ maxWidth: 1100 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 32 }}>
        <div>
          <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>App Modules</h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>Compose stacks — start, stop, update</p>
        </div>
        <button onClick={refresh} className="btn btn-ghost"><Icon name="refresh" size={15} />Refresh</button>
      </div>

      {/* Search */}
      <div style={{ marginBottom: 24, position: 'relative' }}>
        <Icon name="search" size={16} style={{ position: 'absolute', left: 12, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-tertiary)', pointerEvents: 'none' }} />
        <input value={filter} onChange={e => setFilter(e.target.value)} placeholder="Filter modules…"
          className="input" style={{ maxWidth: 360, paddingLeft: 36 }} />
      </div>

      {(composeQ.isError || gitStacksQ.isError) && (
        <ErrorState error={composeQ.error ?? gitStacksQ.error} onRetry={refresh} />
      )}

      {isLoading && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(340px, 1fr))', gap: 20 }}>
          {[0, 1, 2].map(i => <Skeleton key={i} height={180} style={{ borderRadius: 'var(--radius-xl)' }} />)}
        </div>
      )}

      {!isLoading && filtered.length === 0 && (
        <div style={{ textAlign: 'center', padding: '64px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
          <Icon name="deployed_code" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600 }}>
            {filter ? 'No modules match your filter' : 'No compose stacks found'}
          </div>
          <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>
            {!filter && 'Configure Git Sync to manage stacks from a git repository'}
          </div>
        </div>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(340px, 1fr))', gap: 20 }}>
        {filtered.map(stack => (
          <StackCard key={stack.name} stack={stack} onRefresh={refresh} iconMap={iconMapQ.data?.map} />
        ))}
      </div>
    </div>
  )
}
