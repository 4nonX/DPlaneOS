/**
 * pages/DockerPage.tsx — Docker Containers & Image Pull (Phase 3)
 *
 * Tabs: Containers | Pull Image | Compose Stacks
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/docker/containers          → stacks[] grouped view
 *   POST /api/docker/action              → start/stop/restart/remove (body: { action, container_id })
 *   POST /api/docker/pull                → async job { job_id }
 *   POST /api/docker/remove              → remove container
 *   POST /api/docker/prune               → system prune
 *   GET  /api/docker/logs?container=     → container logs
 *   GET  /api/docker/stats               → live resource stats
 *   POST /api/docker/compose/up          → async job { job_id }
 *   POST /api/docker/compose/down        → async job { job_id }
 *   GET  /api/docker/compose/status      → compose stacks status
 *   POST /api/docker/update              → safe update (pull + restart)
 */

import { useState, useEffect, useRef } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { JobProgress } from '@/components/ui/JobProgress'
import { Modal } from '@/components/ui/Modal'
import { toast } from '@/hooks/useToast'
import { useWsStore } from '@/stores/ws'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface PortMapping { host_port: number; container_port: number; protocol: string }
interface WebLink { url: string; label: string; port: number }
interface Resources { cpu_percent: number; mem_percent: number; mem_used: string }

interface Container {
  id:           string
  name:         string
  image:        string
  state:        string   // running | exited | paused | created
  status:       string
  uptime?:      string
  health?:      string   // healthy | unhealthy | starting
  ports:        PortMapping[]
  web_links?:   WebLink[]
  stack?:       string
  resources?:   Resources
}

interface Stack {
  name:                string
  containers:          Container[]
  total_containers:    number
  running_containers:  number
  total_ports:         number
}

interface ContainersResponse {
  success:          boolean
  stacks?:          Stack[]
  containers?:      Container[]
  total_containers: number
  total_stacks?:    number
}

interface ComposeStack {
  name:       string
  path:       string
  running:    boolean
  services:   number
  status?:    string
}
interface ComposeStatusResponse { success: boolean; stacks: ComposeStack[] }
interface JobStartResponse { job_id: string }
interface LogsResponse { success: boolean; logs: string }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function stateColor(state: string): string {
  if (state === 'running') return 'var(--success)'
  if (state === 'paused') return 'var(--warning)'
  return 'var(--error)'
}

function healthIcon(h: string): string {
  if (h === 'healthy') return 'check_circle'
  if (h === 'unhealthy') return 'error'
  return 'pending'
}

function resourceBarColor(pct: number): string {
  return pct > 80 ? 'var(--error)' : pct > 50 ? 'var(--warning)' : 'var(--primary)'
}

// ---------------------------------------------------------------------------
// LogsModal
// ---------------------------------------------------------------------------

function LogsModal({ containerName, onClose }: { containerName: string; onClose: () => void }) {
  const logsQ = useQuery({
    queryKey: ['docker', 'logs', containerName],
    queryFn: ({ signal }) => api.get<LogsResponse>(`/api/docker/logs?container=${encodeURIComponent(containerName)}`, signal),
    refetchInterval: 5_000,
  })

  const ref = useRef<HTMLPreElement>(null)
  useEffect(() => {
    if (ref.current) ref.current.scrollTop = ref.current.scrollHeight
  }, [logsQ.data])

  return (
    <Modal
      title={<><Icon name="description" size={16} style={{ verticalAlign: 'middle', marginRight: 8, color: 'var(--primary)' }} />{containerName}</>}
      onClose={onClose}
      size="lg"
    >
      <div style={{ maxHeight: '60vh', overflow: 'hidden', display: 'flex', flexDirection: 'column', margin: '-4px -4px' }}>
        {logsQ.isLoading && <div style={{ padding: 24, textAlign: 'center', color: 'var(--text-tertiary)' }}>Loading logs…</div>}
        {logsQ.isError && <div style={{ padding: 16 }}><ErrorState error={logsQ.error} /></div>}
        {logsQ.data && (
          <pre ref={ref} style={{ flex: 1, padding: '12px 16px', fontFamily: 'var(--font-mono)', fontSize: 11, lineHeight: 1.6, overflow: 'auto', margin: 0, color: 'rgba(255,255,255,0.75)', whiteSpace: 'pre-wrap', wordBreak: 'break-all', background: 'rgba(0,0,0,0.3)', borderRadius: 'var(--radius-md)' }}>
            {logsQ.data.logs || '(no output)'}
          </pre>
        )}
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ContainerRow
// ---------------------------------------------------------------------------

function ContainerRow({ container, onRefresh }: { container: Container; onRefresh: () => void }) {
  const [showLogs, setShowLogs] = useState(false)
  const [actionPending, setActionPending] = useState<string | null>(null)

  const action = useMutation({
    mutationFn: ({ act, id }: { act: string; id: string }) =>
      api.post('/api/docker/action', { action: act, container_id: id }),
    onMutate: ({ act }) => setActionPending(act),
    onSuccess: () => { toast.success(`Done`); onRefresh() },
    onError: (e: Error) => toast.error(e.message),
    onSettled: () => setActionPending(null),
  })

  const isRunning = container.state === 'running'
  const id = container.id

  return (
    <>
      <tr style={{ borderBottom: '1px solid var(--border)', transition: 'background 0.15s' }}
        onMouseEnter={e => (e.currentTarget.style.background = 'rgba(255,255,255,0.02)')}
        onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
      >
        {/* Name + image */}
        <td style={{ padding: '14px 16px' }}>
          <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>{container.name.replace(/^\//, '')}</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', marginTop: 2 }}>{container.image}</div>
        </td>

        {/* State */}
        <td style={{ padding: '14px 16px' }}>
          <div style={{ display: 'inline-flex', alignItems: 'center', gap: 6, padding: '4px 10px', borderRadius: 'var(--radius-full)', background: `${stateColor(container.state)}18`, border: `1px solid ${stateColor(container.state)}30`, color: stateColor(container.state), fontSize: 'var(--text-xs)', fontWeight: 700 }}>
            <span style={{ width: 7, height: 7, borderRadius: '50%', background: 'currentColor', display: 'inline-block', boxShadow: isRunning ? '0 0 4px currentColor' : 'none' }} />
            {container.state}
          </div>
          {container.health && (
            <div style={{ display: 'inline-flex', alignItems: 'center', gap: 4, marginLeft: 6, fontSize: 'var(--text-2xs)', color: container.health === 'healthy' ? 'var(--success)' : container.health === 'unhealthy' ? 'var(--error)' : 'var(--warning)' }}>
              <Icon name={healthIcon(container.health)} size={11} />
              {container.health}
            </div>
          )}
        </td>

        {/* Ports */}
        <td style={{ padding: '14px 16px', maxWidth: 200 }}>
          {container.web_links && container.web_links.length > 0 ? (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
              {container.web_links.map((l, i) => (
                <a key={i} href={l.url} target="_blank" rel="noreferrer"
                  style={{ display: 'inline-flex', alignItems: 'center', gap: 4, padding: '4px 10px', background: 'var(--primary-bg)', border: '1px solid rgba(138,156,255,0.3)', borderRadius: 'var(--radius-sm)', color: 'var(--primary)', fontSize: 'var(--text-xs)', fontWeight: 600, textDecoration: 'none', whiteSpace: 'nowrap' }}>
                  <Icon name="open_in_new" size={12} />{l.label}
                </a>
              ))}
              {container.ports.filter(p => !container.web_links!.some(l => l.port === p.host_port)).map((p, i) => (
                <span key={`p${i}`} style={{ fontSize: 'var(--text-2xs)', fontFamily: 'var(--font-mono)', color: 'var(--text-tertiary)' }}>
                  {p.host_port}:{p.container_port}
                </span>
              ))}
            </div>
          ) : container.ports.length > 0 ? (
            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
              {container.ports.map((p, i) => (
                <span key={i} style={{ padding: '2px 7px', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xs)', fontSize: 'var(--text-2xs)', fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>
                  {p.host_port}:{p.container_port}/{p.protocol}
                </span>
              ))}
            </div>
          ) : (
            <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>—</span>
          )}
        </td>

        {/* Resources */}
        <td style={{ padding: '14px 16px' }}>
          {container.resources ? (
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'flex', flexDirection: 'column', gap: 4 }}>
              {[['CPU', container.resources.cpu_percent], ['MEM', container.resources.mem_percent]].map(([label, pct]) => (
                <div key={label as string} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <span style={{ width: 32, flexShrink: 0 }}>{label}</span>
                  <span style={{ width: 38, textAlign: 'right', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-2xs)' }}>{(pct as number).toFixed(1)}%</span>
                  <div style={{ flex: 1, height: 4, background: 'rgba(255,255,255,0.08)', borderRadius: 999, overflow: 'hidden' }}>
                    <div style={{ height: '100%', width: `${Math.min(pct as number, 100)}%`, background: resourceBarColor(pct as number), borderRadius: 999, transition: 'width 0.5s' }} />
                  </div>
                </div>
              ))}
            </div>
          ) : <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>—</span>}
        </td>

        {/* Uptime */}
        <td style={{ padding: '14px 16px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', whiteSpace: 'nowrap' }}>
          {container.uptime || '—'}
        </td>

        {/* Actions */}
        <td style={{ padding: '14px 16px' }}>
          <div style={{ display: 'flex', gap: 5 }}>
            <button onClick={() => setShowLogs(true)} className="btn btn-ghost" title="Logs">
              <Icon name="description" size={14} />
            </button>
            {isRunning ? (
              <>
                <button onClick={() => action.mutate({ act: 'restart', id })} disabled={!!actionPending} className="btn btn-ghost" title="Restart">
                  <Icon name="restart_alt" size={14} />
                </button>
                <button onClick={() => action.mutate({ act: 'stop', id })} disabled={!!actionPending} className="btn btn-ghost" title="Stop">
                  <Icon name="stop_circle" size={14} />
                </button>
              </>
            ) : (
              <button onClick={() => action.mutate({ act: 'start', id })} disabled={!!actionPending} className="btn btn-ghost" title="Start">
                <Icon name="play_circle" size={14} />
              </button>
            )}
          </div>
        </td>
      </tr>
      {showLogs && <tr><td colSpan={6} /></tr>}
      {showLogs && <LogsModal containerName={container.name.replace(/^\//, '')} onClose={() => setShowLogs(false)} />}
    </>
  )
}

// ---------------------------------------------------------------------------
// ContainersTab
// ---------------------------------------------------------------------------

function ContainersTab() {
  const qc   = useQueryClient()
  const wsOn = useWsStore((s) => s.on)

  const containersQ = useQuery({
    queryKey: ['docker', 'containers'],
    queryFn: ({ signal }) => api.get<ContainersResponse>('/api/docker/containers', signal),
    refetchInterval: 15_000, // reduced — WS stateUpdate supplements
  })

  // WS stateUpdate fires every ~30s from daemon background monitor.
  // Use it as a push-triggered refetch so container state stays live.
  useEffect(() => {
    return wsOn('stateUpdate', () => {
      qc.invalidateQueries({ queryKey: ['docker', 'containers'] })
    })
  }, [wsOn, qc])

  const prune = useMutation({
    mutationFn: () => api.post('/api/docker/prune', {}),
    onSuccess: () => { toast.success('Docker system pruned'); qc.invalidateQueries({ queryKey: ['docker', 'containers'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const [viewMode, setViewMode] = useState<'stacks' | 'list'>('stacks')

  function refresh() { qc.invalidateQueries({ queryKey: ['docker', 'containers'] }) }

  const stacks = containersQ.data?.stacks ?? []
  const containers = containersQ.data?.containers ?? []
  const total = containersQ.data?.total_containers ?? 0
  const running = [...stacks.flatMap(s => s.containers), ...containers].filter(c => c.state === 'running').length

  if (containersQ.isLoading) return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
      {[0, 1, 2].map(i => <Skeleton key={i} height={60} style={{ borderRadius: 'var(--radius-sm)' }} />)}
    </div>
  )
  if (containersQ.isError) return <ErrorState error={containersQ.error} onRetry={refresh} />

  return (
    <>
      {/* Summary row */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, 1fr)', gap: 12, marginBottom: 20 }}>
        {[
          { label: 'Total', value: total, icon: 'deployed_code', color: 'var(--primary)' },
          { label: 'Running', value: running, icon: 'play_circle', color: 'var(--success)' },
          { label: 'Stacks', value: stacks.length, icon: 'folder', color: 'var(--info)' },
          { label: 'Ports', value: [...stacks.flatMap(s => s.containers), ...containers].reduce((n, c) => n + (c.ports?.length ?? 0), 0), icon: 'lan', color: 'var(--text-secondary)' },
        ].map(s => (
          <div key={s.label} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', padding: '14px 18px' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 6 }}>
              <Icon name={s.icon} size={16} style={{ color: s.color }} />
              <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', fontWeight: 600 }}>{s.label}</span>
            </div>
            <div style={{ fontSize: 28, fontWeight: 700, fontFamily: 'var(--font-mono)', color: s.color }}>{s.value}</div>
          </div>
        ))}
      </div>

      {/* View toggle + actions */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <div style={{ display: 'flex', gap: 4 }}>
          {(['stacks', 'list'] as const).map(v => (
            <button key={v} onClick={() => setViewMode(v)} className="btn btn-ghost" style={{ background: viewMode === v ? 'var(--primary-bg)' : 'var(--surface)', color: viewMode === v ? 'var(--primary)' : 'var(--text-secondary)', borderColor: viewMode === v ? 'rgba(138,156,255,0.3)' : 'var(--border)' }}>
              <Icon name={v === 'stacks' ? 'folder' : 'list'} size={14} />
              {v === 'stacks' ? 'Group by Stack' : 'List View'}
            </button>
          ))}
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button onClick={refresh} className="btn btn-ghost"><Icon name="refresh" size={14} />Refresh</button>
          <button onClick={() => prune.mutate()} disabled={prune.isPending} className="btn btn-ghost" style={{ color: 'var(--error)', borderColor: 'var(--error-border)' }}>
            <Icon name="delete_sweep" size={14} />{prune.isPending ? 'Pruning…' : 'Prune'}
          </button>
        </div>
      </div>

      {/* Container list */}
      {viewMode === 'stacks' && stacks.length === 0 && containers.length === 0 && (
        <EmptyContainers />
      )}
      {viewMode === 'stacks' && stacks.map(stack => (
        <StackSection key={stack.name ?? '__standalone'} stack={stack} onRefresh={refresh} />
      ))}
      {viewMode === 'list' && (
        containers.length === 0 && stacks.flatMap(s => s.containers).length === 0
          ? <EmptyContainers />
          : <ContainerTable containers={[...stacks.flatMap(s => s.containers), ...containers]} onRefresh={refresh} />
      )}
    </>
  )
}

function EmptyContainers() {
  return (
    <div style={{ textAlign: 'center', padding: '64px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
      <Icon name="deployed_code" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
      <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600 }}>No containers found</div>
      <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>Pull an image or deploy a compose stack to get started</div>
    </div>
  )
}

function StackSection({ stack, onRefresh }: { stack: Stack; onRefresh: () => void }) {
  const [open, setOpen] = useState(true)
  const isStandalone = !stack.name
  return (
    <div style={{ marginBottom: 20 }}>
      <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: open ? 'var(--radius-lg) var(--radius-lg) 0 0' : 'var(--radius-lg)', padding: '14px 20px', display: 'flex', justifyContent: 'space-between', alignItems: 'center', cursor: 'pointer' }}
        onClick={() => setOpen(o => !o)}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <Icon name={isStandalone ? 'deployed_code' : 'folder'} size={18} style={{ color: 'var(--primary)' }} />
          <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>{stack.name || 'Standalone Containers'}</span>
          <span style={{ padding: '2px 8px', background: 'var(--bg)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            {stack.running_containers}/{stack.total_containers} running
          </span>
        </div>
        <Icon name={open ? 'expand_less' : 'expand_more'} size={18} style={{ color: 'var(--text-tertiary)' }} />
      </div>
      {open && <ContainerTable containers={stack.containers} onRefresh={onRefresh} topBorder={false} />}
    </div>
  )
}

function ContainerTable({ containers, onRefresh, topBorder = true }: { containers: Container[]; onRefresh: () => void; topBorder?: boolean }) {
  return (
    <div style={{ background: 'var(--surface)', border: '1px solid var(--border)', ...(topBorder ? { borderRadius: 'var(--radius-lg)' } : { borderTop: 'none', borderRadius: '0 0 var(--radius-lg) var(--radius-lg)' }), overflow: 'hidden' }}>
      <table className="data-table">
        <thead>
          <tr style={{ background: 'rgba(255,255,255,0.03)' }}>
            {['Container', 'State', 'Ports', 'Resources', 'Uptime', 'Actions'].map(h => (
              <th key={h}>{h}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {containers.map(c => <ContainerRow key={c.id || c.name} container={c} onRefresh={onRefresh} />)}
        </tbody>
      </table>
    </div>
  )
}

// ---------------------------------------------------------------------------
// PullTab
// ---------------------------------------------------------------------------

function PullTab() {
  const [image, setImage] = useState('')
  const [tag, setTag] = useState('latest')
  const [jobId, setJobId] = useState<string | null>(null)
  const qc = useQueryClient()

  const pull = useMutation({
    mutationFn: () => api.post<JobStartResponse>('/api/docker/pull', { image: `${image}${tag ? ':' + tag : ''}` }),
    onSuccess: data => setJobId(data.job_id),
    onError: (e: Error) => toast.error(e.message),
  })

  function start() {
    if (!image.trim()) { toast.error('Image name required'); return }
    setJobId(null)
    pull.mutate()
  }

  return (
    <div style={{ maxWidth: 600 }}>
      <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 28 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 24 }}>
          <Icon name="download" size={28} style={{ color: 'var(--primary)' }} />
          <div>
            <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>Pull Docker Image</div>
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Download an image from Docker Hub or a registry</div>
          </div>
        </div>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 120px', gap: 10, marginBottom: 16 }}>
          <label className="field">
            <span className="field-label">Image Name</span>
            <input value={image} onChange={e => setImage(e.target.value)} placeholder="nginx, ghcr.io/user/repo"
              className="input" autoFocus onKeyDown={e => e.key === 'Enter' && start()} />
          </label>
          <label className="field">
            <span className="field-label">Tag</span>
            <input value={tag} onChange={e => setTag(e.target.value)} placeholder="latest" className="input" />
          </label>
        </div>

        {jobId && (
          <div style={{ marginBottom: 16 }}>
            <JobProgress
              jobId={jobId}
              runningLabel={`Pulling ${image}:${tag}…`}
              doneLabel="Image pulled successfully"
              onDone={() => { qc.invalidateQueries({ queryKey: ['docker', 'containers'] }); setJobId(null) }}
              onFailed={() => setJobId(null)}
            />
          </div>
        )}

        <button onClick={start} disabled={pull.isPending} className="btn btn-primary">
          <Icon name="download" size={16} />{pull.isPending ? 'Starting…' : 'Pull Image'}
        </button>
      </div>

      {/* Quick reference */}
      <div style={{ marginTop: 20, padding: '16px 20px', background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)' }}>
        <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, marginBottom: 10, color: 'var(--text-secondary)' }}>Common Images</div>
        {[
          ['nginx', 'latest', 'Web server'],
          ['postgres', '16', 'PostgreSQL database'],
          ['redis', 'alpine', 'Redis cache'],
          ['traefik', 'v3', 'Reverse proxy'],
          ['grafana/grafana', 'latest', 'Metrics dashboard'],
        ].map(([img, t, desc]) => (
          <button key={img as string} onClick={() => { setImage(img as string); setTag(t as string) }}
            style={{ display: 'flex', width: '100%', alignItems: 'center', gap: 10, padding: '7px 0', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)', fontSize: 'var(--text-sm)', transition: 'color 0.15s', textAlign: 'left' }}
            onMouseEnter={e => (e.currentTarget.style.color = 'var(--primary)')}
            onMouseLeave={e => (e.currentTarget.style.color = 'var(--text-secondary)')}
          >
            <Icon name="add_circle" size={14} style={{ flexShrink: 0 }} />
            <span style={{ fontFamily: 'var(--font-mono)', flex: 1 }}>{img}:{t}</span>
            <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>{desc}</span>
          </button>
        ))}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ComposeTab
// ---------------------------------------------------------------------------

function ComposeTab() {
  const qc = useQueryClient()
  const [jobId, setJobId] = useState<string | null>(null)
  const [pendingStack, setPendingStack] = useState<string | null>(null)

  const composeQ = useQuery({
    queryKey: ['docker', 'compose', 'status'],
    queryFn: ({ signal }) => api.get<ComposeStatusResponse>('/api/docker/compose/status', signal),
    refetchInterval: 15_000,
  })

  function composeAction(stackName: string, action: 'up' | 'down') {
    setPendingStack(stackName)
    setJobId(null)
    api.post<JobStartResponse>(`/api/docker/compose/${action}`, { stack: stackName })
      .then(data => setJobId(data.job_id))
      .catch((e: Error) => { toast.error(e.message); setPendingStack(null) })
  }

  const stacks = composeQ.data?.stacks ?? []

  return (
    <>
      {jobId && (
        <div style={{ marginBottom: 20 }}>
          <JobProgress
            jobId={jobId}
            runningLabel={`Running compose ${pendingStack}…`}
            doneLabel="Compose operation completed"
            onDone={() => { qc.invalidateQueries({ queryKey: ['docker', 'compose', 'status'] }); setJobId(null); setPendingStack(null) }}
            onFailed={() => { setJobId(null); setPendingStack(null) }}
          />
        </div>
      )}

      {composeQ.isLoading && <Skeleton height={200} style={{ borderRadius: 'var(--radius-lg)' }} />}
      {composeQ.isError && <ErrorState error={composeQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['docker', 'compose', 'status'] })} />}
      {!composeQ.isLoading && !composeQ.isError && stacks.length === 0 && (
        <div style={{ textAlign: 'center', padding: '64px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
          <Icon name="folder" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600 }}>No compose stacks found</div>
          <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>Deploy stacks via Git Sync or Modules</div>
        </div>
      )}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {stacks.map(stack => {
          const isPending = pendingStack === stack.name && !!jobId
          return (
            <div key={stack.name} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '18px 22px', display: 'flex', alignItems: 'center', gap: 16 }}>
              <Icon name="folder" size={24} style={{ color: 'var(--primary)', flexShrink: 0 }} />
              <div style={{ flex: 1 }}>
                <div style={{ fontWeight: 700, marginBottom: 2 }}>{stack.name}</div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)' }}>{stack.path}</div>
                {stack.status && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 3 }}>{stack.status}</div>}
              </div>
              <span style={{ padding: '3px 10px', borderRadius: 'var(--radius-full)', background: stack.running ? 'var(--success-bg)' : 'var(--surface)', border: `1px solid ${stack.running ? 'var(--success-border)' : 'var(--border)'}`, color: stack.running ? 'var(--success)' : 'var(--text-tertiary)', fontSize: 'var(--text-2xs)', fontWeight: 700 }}>
                {stack.running ? 'UP' : 'DOWN'}
              </span>
              <div style={{ display: 'flex', gap: 6 }}>
                <button onClick={() => composeAction(stack.name, 'up')} disabled={isPending} className="btn btn-ghost">
                  <Icon name="play_arrow" size={14} />Up
                </button>
                <button onClick={() => composeAction(stack.name, 'down')} disabled={isPending} className="btn btn-ghost" style={{ color: 'var(--error)', borderColor: 'var(--error-border)' }}>
                  <Icon name="stop" size={14} />Down
                </button>
              </div>
            </div>
          )
        })}
      </div>
    </>
  )
}

// ---------------------------------------------------------------------------
// DockerPage
// ---------------------------------------------------------------------------

type Tab = 'containers' | 'pull' | 'compose'

export function DockerPage() {
  const [tab, setTab] = useState<Tab>('containers')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'containers', label: 'Containers', icon: 'deployed_code' },
    { id: 'pull', label: 'Pull Image', icon: 'download' },
    { id: 'compose', label: 'Compose Stacks', icon: 'folder' },
  ]

  return (
    <div style={{ maxWidth: 1200 }}>
      <div className="page-header">
        <h1 className="page-title">Docker</h1>
        <p className="page-subtitle">Containers · Images · Compose Stacks</p>
      </div>

      {/* Tabs */}
      <div className="tabs-underline" style={{ marginBottom: 28 }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'containers' && <ContainersTab />}
      {tab === 'pull'       && <PullTab />}
      {tab === 'compose'    && <ComposeTab />}
    </div>
  )
}
