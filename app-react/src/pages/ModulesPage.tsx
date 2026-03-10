/**
 * pages/ModulesPage.tsx — App Modules / Compose Stacks
 *
 * Shows compose stacks grouped by template. Standalone stacks shown individually.
 * All existing port links, ContainerIcon, and dplaneos.icon label behaviour preserved.
 *
 * Calls:
 *   GET  /api/docker/stacks              → stacks with template_id
 *   GET  /api/docker/templates           → built-in template catalogue
 *   GET  /api/docker/templates/installed → installed template groups
 *   POST /api/docker/stacks/action       → start/stop/restart a stack
 *   POST /api/docker/templates/deploy    → deploy a template { job_id }
 *   GET  /api/docker/icon-map            → icon map for ContainerIcon
 *   GET  /api/jobs/{id}                  → job polling
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ContainerIcon } from '@/components/ui/ContainerIcon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { JobProgress } from '@/components/ui/JobProgress'
import { Modal } from '@/components/ui/Modal'
import { toast } from '@/hooks/useToast'
import type { IconMapEntry, IconMapResponse } from '@/lib/iconTypes'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface StackService {
  Name?:    string
  State?:   string
  Status?:  string
  Service?: string
}

interface StackInfo {
  name:          string
  path:          string
  status:        string  // "running" | "partial" | "stopped"
  services:      StackService[]
  file_size:     number
  created_at:    string
  updated_at:    string
  template_id?:  string
  template_name?: string
}



interface TemplateVariable {
  key:          string
  label:        string
  description?: string
  default?:     string
  required?:    boolean
  secret?:      boolean
}

interface Template {
  id:          string
  name:        string
  description?: string
  icon?:       string
  tags?:       string[]
  stacks?:     string[]
  variables?:  TemplateVariable[]
  network?:    string
  git_url?:    string
}

interface TemplatesResponse { success: boolean; templates: Template[]; count: number }

interface StackGroup {
  template_id:   string
  template_name: string
  stacks:        StackInfo[]
  icon?:         string
}

interface InstalledResponse { success: boolean; groups: StackGroup[]; count: number }

interface JobStartResponse { success: boolean; job_id: string; template_id?: string }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function statusBadgeClass(s: string) {
  if (s === 'running') return 'badge-success'
  if (s === 'partial') return 'badge-warning'
  return 'badge-neutral'
}

function runningCount(stacks: StackInfo[]): number {
  return stacks.filter(s => s.status === 'running').length
}

// ---------------------------------------------------------------------------
// TemplateDeployModal — variable input before deploying a template
// ---------------------------------------------------------------------------

function TemplateDeployModal({ template, onClose, onDeploy }: {
  template: Template
  onClose: () => void
  onDeploy: (vars: Record<string, string>) => void
}) {
  const [vars, setVars] = useState<Record<string, string>>(
    Object.fromEntries((template.variables ?? []).map(v => [v.key, v.default ?? '']))
  )

  function submit(e: React.FormEvent) {
    e.preventDefault()
    onDeploy(vars)
  }

  return (
    <Modal title={`Deploy: ${template.name}`} onClose={onClose}>
      <form onSubmit={submit} style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        {template.description && (
          <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-sm)', marginBottom: 4 }}>
            {template.description}
          </p>
        )}

        {template.stacks && template.stacks.length > 0 && (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
            {template.stacks.map(s => (
              <span key={s} style={{ padding: '3px 9px', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', fontFamily: 'var(--font-mono)' }}>
                {s}
              </span>
            ))}
          </div>
        )}

        {(template.variables ?? []).length > 0 && (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <div style={{ fontWeight: 700, fontSize: 'var(--text-sm)' }}>Configuration</div>
            {template.variables!.map(v => (
              <div key={v.key} style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
                <label style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)', display: 'flex', gap: 6 }}>
                  {v.label}
                  {v.required && <span style={{ color: 'var(--error)' }}>*</span>}
                </label>
                {v.description && (
                  <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>{v.description}</span>
                )}
                <input
                  className="input"
                  type={v.secret ? 'password' : 'text'}
                  value={vars[v.key] ?? ''}
                  onChange={e => setVars(p => ({ ...p, [v.key]: e.target.value }))}
                  placeholder={v.default ?? v.key}
                  required={v.required}
                />
              </div>
            ))}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', paddingTop: 8 }}>
          <button type="button" onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button type="submit" className="btn btn-primary">
            <Icon name="rocket_launch" size={15} />Deploy
          </button>
        </div>
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// StackCard — single stack, used both standalone and inside a template group
// ---------------------------------------------------------------------------

function StackCard({ stack, onRefresh, iconMap, compact = false }: {
  stack: StackInfo
  onRefresh: () => void
  iconMap?: IconMapEntry[]
  compact?: boolean
}) {
  const [jobId,    setJobId]    = useState<string | null>(null)
  const [jobLabel, setJobLabel] = useState('')

  function stackAction(action: 'start' | 'stop' | 'restart') {
    setJobLabel(`${action === 'start' ? 'Starting' : action === 'stop' ? 'Stopping' : 'Restarting'} ${stack.name}…`)
    setJobId(null)
    api.post<JobStartResponse>('/api/docker/stacks/action', { name: stack.name, action })
      .then(d => { if (d.job_id) setJobId(d.job_id); else { toast.success(`${stack.name} ${action}ped`); onRefresh() } })
      .catch((e: Error) => toast.error(e.message))
  }

  const isRunning = stack.status === 'running'
  const isPartial = stack.status === 'partial'
  const busy = !!jobId

  const serviceCount = stack.services?.length ?? 0
  const runningServices = stack.services?.filter(s => s.State === 'running').length ?? 0

  return (
    <div style={{
      background: compact ? 'var(--surface)' : 'var(--bg-card)',
      border: '1px solid var(--border)',
      borderRadius: compact ? 'var(--radius-md)' : 'var(--radius-xl)',
      padding: compact ? '14px 16px' : 24,
    }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: jobId ? 12 : 8 }}>
        {/* Icon */}
        <div style={{ width: compact ? 32 : 40, height: compact ? 32 : 40, background: 'var(--primary-bg)', border: '1px solid rgba(138,156,255,0.2)', borderRadius: 'var(--radius-md)', display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
          <ContainerIcon image={stack.name} iconMap={iconMap} size={compact ? 16 : 20} />
        </div>

        {/* Name + path */}
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontWeight: 700, fontSize: compact ? 'var(--text-sm)' : 'var(--text-base)' }}>
            {stack.name}
          </div>
          {!compact && (
            <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', marginTop: 2 }}>
              {stack.path}
            </div>
          )}
        </div>

        {/* Status badge */}
        <span className={`badge ${statusBadgeClass(stack.status)}`} style={{ fontSize: 10, flexShrink: 0 }}>
          {isPartial ? `${runningServices}/${serviceCount}` : stack.status.toUpperCase()}
        </span>
      </div>

      {/* Job progress */}
      {jobId && (
        <div style={{ marginBottom: 10 }}>
          <JobProgress
            jobId={jobId}
            runningLabel={jobLabel}
            doneLabel="Done"
            onDone={() => { setJobId(null); onRefresh() }}
            onFailed={() => setJobId(null)}
          />
        </div>
      )}

      {/* Services list — show when not compact */}
      {!compact && serviceCount > 0 && (
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, marginBottom: 12 }}>
          {stack.services.map((s, i) => (
            <span key={i} style={{
              display: 'inline-flex', alignItems: 'center', gap: 4,
              padding: '2px 8px', borderRadius: 'var(--radius-sm)',
              background: s.State === 'running' ? 'rgba(74,222,128,0.08)' : 'var(--surface)',
              border: `1px solid ${s.State === 'running' ? 'rgba(74,222,128,0.2)' : 'var(--border)'}`,
              fontSize: 'var(--text-2xs)', fontFamily: 'var(--font-mono)',
              color: s.State === 'running' ? 'var(--success)' : 'var(--text-tertiary)',
            }}>
              <span style={{ width: 5, height: 5, borderRadius: '50%', background: 'currentColor', display: 'inline-block' }} />
              {s.Service ?? s.Name ?? `service-${i}`}
            </span>
          ))}
        </div>
      )}

      {/* Actions */}
      <div style={{ display: 'flex', gap: 6 }}>
        {isRunning || isPartial ? (
          <>
            <button onClick={() => stackAction('restart')} disabled={busy} className="btn btn-ghost" style={{ fontSize: 12, padding: '5px 10px' }}>
              <Icon name="restart_alt" size={13} />Restart
            </button>
            <button onClick={() => stackAction('stop')} disabled={busy} className="btn btn-ghost" style={{ fontSize: 12, padding: '5px 10px', color: 'var(--error)' }}>
              <Icon name="stop" size={13} />Stop
            </button>
          </>
        ) : (
          <button onClick={() => stackAction('start')} disabled={busy} className="btn btn-primary" style={{ fontSize: 12, padding: '5px 10px' }}>
            <Icon name="play_arrow" size={13} />Start
          </button>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// TemplateGroupCard — collapsible group of stacks sharing a template
// ---------------------------------------------------------------------------

function TemplateGroupCard({ group, onRefresh, iconMap }: {
  group: StackGroup
  onRefresh: () => void
  iconMap?: IconMapEntry[]
}) {
  const [expanded, setExpanded] = useState(true)
  const total   = group.stacks.length
  const running = runningCount(group.stacks)
  const allRunning = running === total
  const noneRunning = running === 0

  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', overflow: 'hidden' }}>
      {/* Group header */}
      <button
        onClick={() => setExpanded(p => !p)}
        style={{ width: '100%', display: 'flex', alignItems: 'center', gap: 14, padding: '18px 20px', background: 'none', border: 'none', cursor: 'pointer', textAlign: 'left' }}
      >
        {/* Template icon */}
        <div style={{ width: 44, height: 44, background: 'var(--primary-bg)', border: '1px solid rgba(138,156,255,0.25)', borderRadius: 'var(--radius-md)', display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
          <Icon name={group.icon ?? 'deployed_code'} size={22} style={{ color: 'var(--primary)' }} />
        </div>

        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ fontWeight: 700, fontSize: 'var(--text-base)', display: 'flex', alignItems: 'center', gap: 8 }}>
            {group.template_name || group.template_id}
            <span style={{
              padding: '1px 7px', borderRadius: 'var(--radius-full)', fontSize: 10, fontWeight: 700,
              background: allRunning ? 'rgba(74,222,128,0.12)' : noneRunning ? 'rgba(255,255,255,0.06)' : 'rgba(251,191,36,0.12)',
              color: allRunning ? 'var(--success)' : noneRunning ? 'var(--text-tertiary)' : 'rgba(251,191,36,0.9)',
              border: `1px solid ${allRunning ? 'rgba(74,222,128,0.25)' : noneRunning ? 'var(--border)' : 'rgba(251,191,36,0.25)'}`,
            }}>
              {running}/{total} running
            </span>
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>
            {total} stack{total !== 1 ? 's' : ''} — {group.stacks.map(s => s.name).join(', ')}
          </div>
        </div>

        <Icon name={expanded ? 'expand_less' : 'expand_more'} size={20} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
      </button>

      {/* Expanded stack list */}
      {expanded && (
        <div style={{ padding: '0 16px 16px', display: 'flex', flexDirection: 'column', gap: 8 }}>
          {group.stacks.map(stack => (
            <StackCard key={stack.name} stack={stack} onRefresh={onRefresh} iconMap={iconMap} compact />
          ))}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// TemplateCatalogueTab — browse and deploy templates
// ---------------------------------------------------------------------------

function TemplateCatalogueTab({ onDeploy }: { onDeploy: (t: Template) => void }) {
  const catalogueQ = useQuery({
    queryKey: ['docker', 'templates'],
    queryFn: ({ signal }) => api.get<TemplatesResponse>('/api/docker/templates', signal),
    staleTime: 10 * 60_000,
  })

  if (catalogueQ.isLoading) return <Skeleton height={180} />
  if (catalogueQ.isError)   return <ErrorState error={catalogueQ.error} />

  const templates = catalogueQ.data?.templates ?? []

  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(320px, 1fr))', gap: 16 }}>
      {templates.map(t => (
        <div key={t.id} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 20 }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14, marginBottom: 12 }}>
            <div style={{ width: 44, height: 44, background: 'var(--primary-bg)', border: '1px solid rgba(138,156,255,0.2)', borderRadius: 'var(--radius-md)', display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
              <Icon name={t.icon ?? 'deployed_code'} size={22} style={{ color: 'var(--primary)' }} />
            </div>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ fontWeight: 700, marginBottom: 3 }}>{t.name}</div>
              {t.description && (
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.5 }}>{t.description}</div>
              )}
            </div>
          </div>

          {t.stacks && t.stacks.length > 0 && (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, marginBottom: 12 }}>
              {t.stacks.map(s => (
                <span key={s} style={{ padding: '2px 7px', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xs)', fontSize: 10, fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>
                  {s}
                </span>
              ))}
            </div>
          )}

          {t.tags && t.tags.length > 0 && (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4, marginBottom: 14 }}>
              {t.tags.map(tag => (
                <span key={tag} style={{ padding: '2px 7px', background: 'rgba(138,156,255,0.08)', border: '1px solid rgba(138,156,255,0.2)', borderRadius: 'var(--radius-full)', fontSize: 10, color: 'var(--primary)' }}>
                  {tag}
                </span>
              ))}
            </div>
          )}

          <button onClick={() => onDeploy(t)} className="btn btn-primary" style={{ width: '100%' }}>
            <Icon name="rocket_launch" size={14} />Deploy
          </button>
        </div>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// ModulesPage
// ---------------------------------------------------------------------------

type Tab = 'installed' | 'catalogue'

export function ModulesPage() {
  const qc  = useQueryClient()
  const [tab,    setTab]    = useState<Tab>('installed')
  const [filter, setFilter] = useState('')
  const [deployModal, setDeployModal] = useState<Template | null>(null)
  const [deployJobId, setDeployJobId] = useState<string | null>(null)
  const [deployLabel, setDeployLabel] = useState('')

  // Installed stacks grouped by template
  const installedQ = useQuery({
    queryKey: ['docker', 'templates', 'installed'],
    queryFn: ({ signal }) => api.get<InstalledResponse>('/api/docker/templates/installed', signal),
    refetchInterval: 20_000,
  })

  // Icon map — shared, cached 1h
  const iconMapQ = useQuery({
    queryKey: ['docker', 'icon-map'],
    queryFn: ({ signal }) => api.get<IconMapResponse>('/api/docker/icon-map', signal),
    staleTime: 60 * 60_000,
  })

  const deployM = useMutation({
    mutationFn: ({ id, vars }: { id: string; vars: Record<string, string> }) =>
      api.post<JobStartResponse>('/api/docker/templates/deploy', { template_id: id, variables: vars }),
    onSuccess: (data) => {
      setDeployModal(null)
      setDeployLabel(`Deploying template…`)
      setDeployJobId(data.job_id)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function refresh() {
    qc.invalidateQueries({ queryKey: ['docker', 'templates', 'installed'] })
  }

  const groups = installedQ.data?.groups ?? []
  const iconMap = iconMapQ.data?.map

  // Separate template groups from standalone stacks
  const templateGroups = groups.filter(g => g.template_id !== '__standalone__')
  const standalone     = groups.find(g => g.template_id === '__standalone__')?.stacks ?? []

  // Apply filter
  const filteredGroups = filter
    ? templateGroups.filter(g =>
        g.template_name?.toLowerCase().includes(filter.toLowerCase()) ||
        g.stacks.some(s => s.name.toLowerCase().includes(filter.toLowerCase()))
      )
    : templateGroups

  const filteredStandalone = filter
    ? standalone.filter(s => s.name.toLowerCase().includes(filter.toLowerCase()))
    : standalone

  const isLoading = installedQ.isLoading

  return (
    <div style={{ maxWidth: 1100 }}>
      <div className="page-header">
        <h1 className="page-title">App Modules</h1>
        <p className="page-subtitle">Compose stacks — deploy from templates or manage individually</p>
      </div>

      {/* Global deploy job progress */}
      {deployJobId && (
        <div style={{ marginBottom: 20 }}>
          <JobProgress
            jobId={deployJobId}
            runningLabel={deployLabel}
            doneLabel="Template deployed"
            onDone={() => { setDeployJobId(null); refresh(); toast.success('Template deployed') }}
            onFailed={() => { setDeployJobId(null); toast.error('Template deployment failed') }}
          />
        </div>
      )}

      {/* Tabs */}
      <div className="tabs-underline" style={{ marginBottom: 24 }}>
        {(['installed', 'catalogue'] as Tab[]).map(t => (
          <button key={t} onClick={() => setTab(t)} className={`tab ${tab === t ? 'active' : ''}`} style={{ textTransform: 'capitalize' }}>
            {t === 'installed' ? 'Installed' : 'Template Catalogue'}
          </button>
        ))}
        <div style={{ flex: 1 }} />
        {tab === 'installed' && (
          <button onClick={refresh} className="btn btn-ghost" style={{ fontSize: 'var(--text-xs)', padding: '5px 10px' }}>
            <Icon name="refresh" size={13} />Refresh
          </button>
        )}
      </div>

      {tab === 'catalogue' && (
        <TemplateCatalogueTab onDeploy={t => setDeployModal(t)} />
      )}

      {tab === 'installed' && (
        <>
          {/* Filter */}
          <div style={{ marginBottom: 20, position: 'relative' }}>
            <Icon name="search" size={16} style={{ position: 'absolute', left: 12, top: '50%', transform: 'translateY(-50%)', color: 'var(--text-tertiary)', pointerEvents: 'none' }} />
            <input value={filter} onChange={e => setFilter(e.target.value)} placeholder="Filter stacks…"
              className="input" style={{ maxWidth: 360, paddingLeft: 36 }} />
          </div>

          {installedQ.isError && <ErrorState error={installedQ.error} onRetry={refresh} />}

          {isLoading && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
              {[0, 1].map(i => <Skeleton key={i} height={120} style={{ borderRadius: 'var(--radius-xl)' }} />)}
            </div>
          )}

          {!isLoading && filteredGroups.length === 0 && filteredStandalone.length === 0 && (
            <div style={{ textAlign: 'center', padding: '64px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
              <Icon name="deployed_code" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
              <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600 }}>
                {filter ? 'No modules match your filter' : 'No stacks deployed yet'}
              </div>
              <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>
                {!filter && (
                  <button onClick={() => setTab('catalogue')} className="btn btn-primary" style={{ marginTop: 16 }}>
                    <Icon name="add" size={14} />Browse templates
                  </button>
                )}
              </div>
            </div>
          )}

          {/* Template groups */}
          {filteredGroups.length > 0 && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 16, marginBottom: filteredStandalone.length > 0 ? 32 : 0 }}>
              {filteredGroups.map(group => (
                <TemplateGroupCard key={group.template_id} group={group} onRefresh={refresh} iconMap={iconMap} />
              ))}
            </div>
          )}

          {/* Standalone stacks */}
          {filteredStandalone.length > 0 && (
            <>
              {filteredGroups.length > 0 && (
                <div style={{ fontWeight: 700, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 12, paddingTop: 8 }}>
                  Standalone stacks
                </div>
              )}
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(340px, 1fr))', gap: 16 }}>
                {filteredStandalone.map(stack => (
                  <StackCard key={stack.name} stack={stack} onRefresh={refresh} iconMap={iconMap} />
                ))}
              </div>
            </>
          )}
        </>
      )}

      {/* Template deploy modal */}
      {deployModal && (
        <TemplateDeployModal
          template={deployModal}
          onClose={() => setDeployModal(null)}
          onDeploy={vars => deployM.mutate({ id: deployModal.id, vars })}
        />
      )}
    </div>
  )
}
