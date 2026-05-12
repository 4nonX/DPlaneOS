/**
 * pages/DockerPage.tsx - Docker Containers & Image Pull (Phase 3)
 *
 * Tabs: Containers | Pull Image | Compose Stacks | GPU / hardware
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/docker/containers          → stacks[] grouped view
 *   POST /api/docker/action              → start/stop/restart/remove (body: { action, container_id })
 *   POST /api/docker/pull                → async job { job_id }
 *   POST /api/docker/stacks/deploy       → synchronous stack deploy { success, error?, output?, ... }
 *   GET  /api/docker/gpu                 → GPU passthrough / host report
 *   POST /api/docker/remove              → remove container
 *   POST /api/docker/prune               → system prune
 *   GET  /api/docker/logs?container=     → container logs
 *   GET  /api/docker/stats               → live resource stats
 *   POST /api/docker/compose/up          → async job { job_id }
 *   POST /api/docker/compose/down        → async job { job_id }
 *   GET  /api/docker/compose/status      → compose stacks status
 *   POST /api/docker/update              → safe update (pull + restart)
 */

import { useState, useEffect, useRef, useCallback } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api, getSessionId, getUsername } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ContainerIcon } from '@/components/ui/ContainerIcon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { JobProgress } from '@/components/ui/JobProgress'
import { Modal } from '@/components/ui/Modal'
import { Tooltip } from '@/components/ui/Tooltip'
import { toast } from '@/hooks/useToast'
import { useWsStore } from '@/stores/ws'
import type { IconMapResponse } from '@/lib/iconTypes'

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
  Labels?:      Record<string, string>  // Docker labels - includes dplaneos.icon
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

interface JobStartResponse { job_id: string }

interface StackDeployResponse {
  success: boolean
  message?: string
  error?: string
  stack?: string
  path?: string
  output?: string
  duration_ms?: number
}

interface GPUPassthroughReport {
  pci_devices: Array<{
    bus_id: string
    class: string
    class_code: string
    vendor_name: string
    vendor_id: string
    device_id: string
    raw_line: string
  }>
  dri_nodes: Array<{ name: string; path: string; mode: string; is_render: boolean }>
  nvidia_gpus?: Array<{ name: string; uuid: string; driver_version: string }>
  docker_runtimes?: string[]
  nvidia_runtime_available: boolean
  nvidia_driver_ok: boolean
  compose_hints: { can_use_nvidia_device_reservation: boolean; can_pass_dri_devices: boolean }
  nixos_docker_nvidia_option: string
  compose_examples: {
    nvidia_device_reservation: string
    nvidia_deploy_snippet: string
    dri_render_group: string
  }
}
interface GPUReportResponse { success: boolean; report: GPUPassthroughReport }
interface LogsResponse { success: boolean; logs: string }

interface PortEntry { host_port: string; container_port: string; protocol: string }
interface VolumeEntry { host_path: string; container_path: string; mode: string }
interface EnvEntry { key: string; value: string }

interface ContainerInspectResponse {
  success: boolean
  id: string
  name: string
  image: string
  icon: string
  restart_policy: string
  ports: PortEntry[]
  volumes: VolumeEntry[]
  env: EnvEntry[]
  state: string
}

interface ComposeService { Name?: string; Service?: string; State?: string; Status?: string }
interface StackInfo {
  name: string
  path: string
  status: 'running' | 'partial' | 'stopped' | 'unknown'
  services: ComposeService[]
  file_size: number
  created_at: string
  updated_at: string
  labels?: Record<string, string>
}
interface StacksResponse { success: boolean; stacks: StackInfo[]; count: number; stacks_dir: string }
interface StackYAMLResponse { success: boolean; name: string; yaml: string; env?: string; path: string }

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

function stackStatusColor(status?: string): string {
  if (status === 'running') return 'var(--success)'
  if (status === 'partial') return 'var(--warning)'
  return 'var(--text-tertiary)'
}

function containerAppUrl(container: Container): string | null {
  if (container.web_links?.length) return container.web_links[0].url
  const port = container.ports?.find(p => p.host_port > 0)
  if (!port) return null
  return `http://${window.location.hostname}:${port.host_port}`
}

function containerPrimaryPort(container: Container): number | null {
  if (container.web_links?.length) return container.web_links[0].port
  return container.ports?.find(p => p.host_port > 0)?.host_port ?? null
}

// ---------------------------------------------------------------------------
// ContainerCard  (grid / ZimaOS-style view)
// ---------------------------------------------------------------------------

function ContainerCard({ container, onRefresh }: { container: Container; onRefresh: () => void }) {
  const iconMapQ = useQuery({
    queryKey: ['docker', 'icon-map'],
    queryFn: ({ signal }) => api.get<IconMapResponse>('/api/docker/icon-map', signal),
    staleTime: 60 * 60 * 1000,
  })
  const [showLogs, setShowLogs] = useState(false)
  const [showEdit, setShowEdit] = useState(false)
  const [showDelete, setShowDelete] = useState(false)
  const [actionPending, setActionPending] = useState<string | null>(null)

  const action = useMutation({
    mutationFn: ({ act, id }: { act: string; id: string }) =>
      api.post('/api/docker/action', { action: act, container_id: id }),
    onMutate: ({ act }) => setActionPending(act),
    onSuccess: () => onRefresh(),
    onError: (e: Error) => toast.error(e.message),
    onSettled: () => setActionPending(null),
  })

  const deleteMutation = useMutation({
    mutationFn: () => api.post('/api/docker/remove', { container_name: container.name.replace(/^\//, ''), force: true }),
    onSuccess: () => { toast.success(`${container.name.replace(/^\//, '')} removed`); setShowDelete(false); onRefresh() },
    onError: (e: Error) => toast.error(e.message),
  })

  const isRunning = container.state === 'running'
  const name = container.name.replace(/^\//, '')
  const id = container.id
  const appUrl = containerAppUrl(container)
  const primaryPort = containerPrimaryPort(container)

  return (
    <>
      <div
        className="card"
        style={{ position: 'relative', borderRadius: 'var(--radius-xl)', padding: '20px 14px 14px', display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 6, transition: 'box-shadow 0.2s, border-color 0.2s', cursor: 'default', minHeight: 210 }}
        onMouseEnter={e => { (e.currentTarget as HTMLDivElement).style.boxShadow = '0 4px 24px rgba(0,0,0,0.18)' }}
        onMouseLeave={e => { (e.currentTarget as HTMLDivElement).style.boxShadow = 'none' }}
      >
        {/* Status dot */}
        <div style={{ position: 'absolute', top: 12, right: 12, width: 8, height: 8, borderRadius: '50%', background: stateColor(container.state), boxShadow: isRunning ? `0 0 7px ${stateColor(container.state)}` : 'none' }} />

        {/* Icon - clicking opens the app */}
        {appUrl ? (
          <a href={appUrl} target="_blank" rel="noreferrer" style={{ display: 'flex', textDecoration: 'none', borderRadius: 'var(--radius-lg)', padding: 6, transition: 'background 0.15s' }}
            onMouseEnter={e => (e.currentTarget.style.background = 'var(--primary-bg)')}
            onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
            <ContainerIcon image={container.image} labels={container.Labels} iconMap={iconMapQ.data?.map} size={56} />
          </a>
        ) : (
          <div style={{ padding: 6 }}>
            <ContainerIcon image={container.image} labels={container.Labels} iconMap={iconMapQ.data?.map} size={56} />
          </div>
        )}

        {/* Name */}
        <div style={{ fontWeight: 700, fontSize: 'var(--text-sm)', textAlign: 'center', width: '100%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{name}</div>

        {/* Port - prominent clickable chip */}
        {primaryPort ? (
          <a href={appUrl ?? '#'} target="_blank" rel="noreferrer"
            style={{ display: 'inline-flex', alignItems: 'center', gap: 4, padding: '4px 12px', borderRadius: 'var(--radius-full)', background: 'var(--primary-bg)', border: '1px solid hsla(var(--hue-primary),100%,72%,.3)', color: 'var(--primary)', fontSize: 'var(--text-sm)', fontWeight: 700, fontFamily: 'var(--font-mono)', textDecoration: 'none' }}>
            <Icon name="open_in_new" size={12} />:{primaryPort}
          </a>
        ) : (
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{container.state}</span>
        )}

        {/* Actions */}
        <div style={{ display: 'flex', gap: 3, marginTop: 4, flexWrap: 'wrap', justifyContent: 'center' }}>
          {isRunning ? (
            <>
              <Tooltip content="Restart">
                <button onClick={() => action.mutate({ act: 'restart', id })} disabled={!!actionPending} className="btn btn-ghost" style={{ padding: '5px 7px' }}>
                  <Icon name="restart_alt" size={14} />
                </button>
              </Tooltip>
              <Tooltip content="Stop">
                <button onClick={() => action.mutate({ act: 'stop', id })} disabled={!!actionPending} className="btn btn-ghost" style={{ padding: '5px 7px', color: 'var(--warning)' }}>
                  <Icon name="stop_circle" size={14} />
                </button>
              </Tooltip>
            </>
          ) : (
            <Tooltip content="Start">
              <button onClick={() => action.mutate({ act: 'start', id })} disabled={!!actionPending} className="btn btn-ghost" style={{ padding: '5px 7px', color: 'var(--success)' }}>
                <Icon name="play_circle" size={14} />
              </button>
            </Tooltip>
          )}
          <Tooltip content="Edit">
            <button onClick={() => setShowEdit(true)} className="btn btn-ghost" style={{ padding: '5px 7px' }}>
              <Icon name="tune" size={14} />
            </button>
          </Tooltip>
          <Tooltip content="Logs">
            <button onClick={() => setShowLogs(true)} className="btn btn-ghost" style={{ padding: '5px 7px' }}>
              <Icon name="description" size={14} />
            </button>
          </Tooltip>
          <Tooltip content="Remove">
            <button onClick={() => setShowDelete(true)} className="btn btn-ghost" style={{ padding: '5px 7px', color: 'var(--error)' }}>
              <Icon name="delete" size={14} />
            </button>
          </Tooltip>
        </div>
      </div>

      {showLogs && <LogsModal containerName={name} onClose={() => setShowLogs(false)} />}
      {showEdit && <ContainerEditModal container={container} onClose={() => setShowEdit(false)} onSaved={() => { setShowEdit(false); onRefresh() }} />}
      {showDelete && (
        <Modal title="Remove Container?" onClose={() => setShowDelete(false)}>
          <p style={{ color: 'var(--text-secondary)', marginBottom: 20 }}>
            Remove <strong>{name}</strong>? Stops and deletes the container. Volumes are preserved.
          </p>
          <div style={{ display: 'flex', gap: 10 }}>
            <button onClick={() => deleteMutation.mutate()} disabled={deleteMutation.isPending} className="btn btn-ghost" style={{ color: 'var(--error)', borderColor: 'var(--error-border)' }}>
              <Icon name="delete" size={15} />{deleteMutation.isPending ? 'Removing…' : 'Remove'}
            </button>
            <button onClick={() => setShowDelete(false)} className="btn btn-ghost">Cancel</button>
          </div>
        </Modal>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// ContainerEditModal
// ---------------------------------------------------------------------------

type EditTab = 'general' | 'ports' | 'volumes' | 'env'

function ContainerEditModal({
  container,
  onClose,
  onSaved,
}: {
  container: Container
  onClose: () => void
  onSaved: () => void
}) {
  const qc = useQueryClient()
  const containerName = container.name.replace(/^\//, '')

  const iconMapQ = useQuery({
    queryKey: ['docker', 'icon-map'],
    queryFn: ({ signal }) => api.get<IconMapResponse>('/api/docker/icon-map', signal),
    staleTime: 60 * 60 * 1000,
  })

  const [tab, setTab] = useState<EditTab>('general')
  const [icon, setIcon] = useState('')
  const [restartPolicy, setRestartPolicy] = useState('unless-stopped')
  const [ports, setPorts] = useState<PortEntry[]>([])
  const [volumes, setVolumes] = useState<VolumeEntry[]>([])
  const [envVars, setEnvVars] = useState<EnvEntry[]>([])

  const inspectQ = useQuery({
    queryKey: ['docker', 'inspect', containerName],
    queryFn: ({ signal }) =>
      api.get<ContainerInspectResponse>(
        `/api/docker/containers/${encodeURIComponent(containerName)}/inspect`,
        signal
      ),
  })

  useEffect(() => {
    const d = inspectQ.data
    if (!d?.success) return
    setIcon(d.icon ?? '')
    setRestartPolicy(d.restart_policy ?? 'unless-stopped')
    setPorts(d.ports ?? [])
    setVolumes(d.volumes ?? [])
    setEnvVars(d.env ?? [])
  }, [inspectQ.data])

  const save = useMutation({
    mutationFn: async () => {
      const res = await api.post<{ success: boolean; error?: string }>(
        `/api/docker/containers/${encodeURIComponent(containerName)}/reconfigure`,
        { icon, restart_policy: restartPolicy, ports, volumes, env: envVars }
      )
      if (!res.success) throw new Error(res.error || 'Reconfigure failed')
      return res
    },
    onSuccess: () => {
      toast.success(`${containerName} reconfigured`)
      qc.invalidateQueries({ queryKey: ['docker', 'containers'] })
      onSaved()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const inp = { width: '100%', padding: '6px 10px', background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', color: 'var(--text)', fontSize: 'var(--text-sm)', fontFamily: 'var(--font-mono)', boxSizing: 'border-box' as const }
  const cell = { padding: '4px 4px' }

  const tabBtn = (t: EditTab, label: string) => (
    <button key={t} onClick={() => setTab(t)} style={{ padding: '7px 14px', borderRadius: 'var(--radius-sm)', border: 'none', background: tab === t ? 'var(--primary-bg)' : 'transparent', color: tab === t ? 'var(--primary)' : 'var(--text-secondary)', fontWeight: tab === t ? 700 : 400, cursor: 'pointer', fontSize: 'var(--text-sm)' }}>
      {label}
    </button>
  )

  return (
    <Modal
      title={<><Icon name="tune" size={16} style={{ verticalAlign: 'middle', marginRight: 8, color: 'var(--primary)' }} />Edit: {containerName}</>}
      onClose={onClose}
      size="lg"
    >
      {inspectQ.isLoading && <Skeleton height={200} />}
      {inspectQ.isError && <ErrorState error={inspectQ.error} />}
      {!inspectQ.isLoading && !inspectQ.isError && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {/* Sub-tabs */}
          <div style={{ display: 'flex', gap: 2, borderBottom: '1px solid var(--border)', paddingBottom: 10 }}>
            {tabBtn('general', 'General')}
            {tabBtn('ports', `Ports (${ports.length})`)}
            {tabBtn('volumes', `Volumes (${volumes.length})`)}
            {tabBtn('env', `Environment (${envVars.length})`)}
          </div>

          {/* General */}
          {tab === 'general' && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr auto', gap: 16, alignItems: 'start' }}>
                <label className="field">
                  <span className="field-label">Icon</span>
                  <input value={icon} onChange={e => setIcon(e.target.value)} placeholder="Material Symbol name, filename, or URL" className="input" />
                  <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 4, display: 'block' }}>e.g. "http", "jellyfin.svg", or a remote image URL</span>
                </label>
                <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 4, paddingTop: 20 }}>
                  <ContainerIcon image={container.image} labels={icon ? { 'dplaneos.icon': icon } : container.Labels} iconMap={iconMapQ.data?.map} size={48} />
                  <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>preview</span>
                </div>
              </div>
              <label className="field">
                <span className="field-label">Restart Policy</span>
                <select value={restartPolicy} onChange={e => setRestartPolicy(e.target.value)} className="input" style={{ maxWidth: 220 }}>
                  {['no', 'always', 'unless-stopped', 'on-failure'].map(p => <option key={p} value={p}>{p}</option>)}
                </select>
              </label>
            </div>
          )}

          {/* Ports */}
          {tab === 'ports' && (
            <div>
              <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 'var(--text-sm)' }}>
                <thead>
                  <tr style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', textAlign: 'left' }}>
                    <th style={{ ...cell, fontWeight: 600, paddingBottom: 8 }}>Host Port</th>
                    <th style={{ ...cell, fontWeight: 600, paddingBottom: 8 }}>Container Port</th>
                    <th style={{ ...cell, fontWeight: 600, paddingBottom: 8, width: 90 }}>Protocol</th>
                    <th style={{ ...cell, width: 36 }} />
                  </tr>
                </thead>
                <tbody>
                  {ports.map((p, i) => (
                    <tr key={i}>
                      <td style={cell}><input value={p.host_port} onChange={e => setPorts(ps => ps.map((r, j) => j === i ? { ...r, host_port: e.target.value } : r))} placeholder="8080" style={inp} /></td>
                      <td style={cell}><input value={p.container_port} onChange={e => setPorts(ps => ps.map((r, j) => j === i ? { ...r, container_port: e.target.value } : r))} placeholder="80" style={inp} /></td>
                      <td style={cell}>
                        <select value={p.protocol} onChange={e => setPorts(ps => ps.map((r, j) => j === i ? { ...r, protocol: e.target.value } : r))} style={inp}>
                          <option value="tcp">tcp</option>
                          <option value="udp">udp</option>
                        </select>
                      </td>
                      <td style={cell}><button onClick={() => setPorts(ps => ps.filter((_, j) => j !== i))} className="btn btn-ghost" style={{ color: 'var(--error)', padding: '4px 8px' }}><Icon name="close" size={14} /></button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <button onClick={() => setPorts(ps => [...ps, { host_port: '', container_port: '', protocol: 'tcp' }])} className="btn btn-ghost" style={{ marginTop: 8 }}>
                <Icon name="add" size={14} />Add Port
              </button>
            </div>
          )}

          {/* Volumes */}
          {tab === 'volumes' && (
            <div>
              <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 'var(--text-sm)' }}>
                <thead>
                  <tr style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', textAlign: 'left' }}>
                    <th style={{ ...cell, fontWeight: 600, paddingBottom: 8 }}>Host Path</th>
                    <th style={{ ...cell, fontWeight: 600, paddingBottom: 8 }}>Container Path</th>
                    <th style={{ ...cell, fontWeight: 600, paddingBottom: 8, width: 70 }}>Mode</th>
                    <th style={{ ...cell, width: 36 }} />
                  </tr>
                </thead>
                <tbody>
                  {volumes.map((v, i) => (
                    <tr key={i}>
                      <td style={cell}><input value={v.host_path} onChange={e => setVolumes(vs => vs.map((r, j) => j === i ? { ...r, host_path: e.target.value } : r))} placeholder="/data/app" style={inp} /></td>
                      <td style={cell}><input value={v.container_path} onChange={e => setVolumes(vs => vs.map((r, j) => j === i ? { ...r, container_path: e.target.value } : r))} placeholder="/app/data" style={inp} /></td>
                      <td style={cell}>
                        <select value={v.mode} onChange={e => setVolumes(vs => vs.map((r, j) => j === i ? { ...r, mode: e.target.value } : r))} style={inp}>
                          <option value="rw">rw</option>
                          <option value="ro">ro</option>
                        </select>
                      </td>
                      <td style={cell}><button onClick={() => setVolumes(vs => vs.filter((_, j) => j !== i))} className="btn btn-ghost" style={{ color: 'var(--error)', padding: '4px 8px' }}><Icon name="close" size={14} /></button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <button onClick={() => setVolumes(vs => [...vs, { host_path: '', container_path: '', mode: 'rw' }])} className="btn btn-ghost" style={{ marginTop: 8 }}>
                <Icon name="add" size={14} />Add Volume
              </button>
            </div>
          )}

          {/* Environment */}
          {tab === 'env' && (
            <div>
              <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 'var(--text-sm)' }}>
                <thead>
                  <tr style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', textAlign: 'left' }}>
                    <th style={{ ...cell, fontWeight: 600, paddingBottom: 8 }}>Key</th>
                    <th style={{ ...cell, fontWeight: 600, paddingBottom: 8 }}>Value</th>
                    <th style={{ ...cell, width: 36 }} />
                  </tr>
                </thead>
                <tbody>
                  {envVars.map((e, i) => (
                    <tr key={i}>
                      <td style={cell}><input value={e.key} onChange={ev => setEnvVars(es => es.map((r, j) => j === i ? { ...r, key: ev.target.value } : r))} placeholder="MY_VAR" style={inp} /></td>
                      <td style={cell}><input value={e.value} onChange={ev => setEnvVars(es => es.map((r, j) => j === i ? { ...r, value: ev.target.value } : r))} placeholder="value" style={inp} /></td>
                      <td style={cell}><button onClick={() => setEnvVars(es => es.filter((_, j) => j !== i))} className="btn btn-ghost" style={{ color: 'var(--error)', padding: '4px 8px' }}><Icon name="close" size={14} /></button></td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <button onClick={() => setEnvVars(es => [...es, { key: '', value: '' }])} className="btn btn-ghost" style={{ marginTop: 8 }}>
                <Icon name="add" size={14} />Add Variable
              </button>
            </div>
          )}

          {/* Warning + save */}
          <div style={{ padding: '10px 14px', borderRadius: 'var(--radius-md)', background: 'rgba(234,179,8,0.08)', border: '1px solid rgba(234,179,8,0.2)', fontSize: 'var(--text-xs)', color: 'var(--warning)', display: 'flex', alignItems: 'center', gap: 8 }}>
            <Icon name="warning" size={14} />
            Saving will stop, remove, and recreate the container with the new configuration.
          </div>
          <div style={{ display: 'flex', gap: 10 }}>
            <button onClick={() => save.mutate()} disabled={save.isPending} className="btn btn-primary">
              <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Changes'}
            </button>
            <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          </div>
        </div>
      )}
    </Modal>
  )
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
  const iconMapQ = useQuery({
    queryKey: ['docker', 'icon-map'],
    queryFn: ({ signal }) => api.get<IconMapResponse>('/api/docker/icon-map', signal),
    staleTime: 60 * 60 * 1000,
  })
  const [showLogs, setShowLogs] = useState(false)
  const [showEdit, setShowEdit] = useState(false)
  const [showDelete, setShowDelete] = useState(false)
  const [actionPending, setActionPending] = useState<string | null>(null)

  const action = useMutation({
    mutationFn: ({ act, id }: { act: string; id: string }) =>
      api.post('/api/docker/action', { action: act, container_id: id }),
    onMutate: ({ act }) => setActionPending(act),
    onSuccess: () => { toast.success(`Done`); onRefresh() },
    onError: (e: Error) => toast.error(e.message),
    onSettled: () => setActionPending(null),
  })

  const deleteMutation = useMutation({
    mutationFn: () => api.post('/api/docker/remove', { container_name: container.name.replace(/^\//, ''), force: true }),
    onSuccess: () => { toast.success(`${container.name.replace(/^\//, '')} removed`); setShowDelete(false); onRefresh() },
    onError: (e: Error) => toast.error(e.message),
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
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <ContainerIcon
              image={container.image}
              labels={container.Labels}
              iconMap={iconMapQ.data?.map}
              size={24}
            />
            <div>
              <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>{container.name.replace(/^\//, '')}</div>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', marginTop: 2 }}>{container.image}</div>
            </div>
          </div>
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
                  style={{ display: 'inline-flex', alignItems: 'center', gap: 4, padding: '4px 10px', background: 'var(--primary-bg)', border: '1px solid hsla(var(--hue-primary),100%,72%,.3)', borderRadius: 'var(--radius-sm)', color: 'var(--primary)', fontSize: 'var(--text-xs)', fontWeight: 600, textDecoration: 'none', whiteSpace: 'nowrap' }}>
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
            <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>-</span>
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
          ) : <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>-</span>}
        </td>

        {/* Uptime */}
        <td style={{ padding: '14px 16px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', whiteSpace: 'nowrap' }}>
          {container.uptime || '-'}
        </td>

        {/* Actions */}
        <td style={{ padding: '14px 16px' }}>
          <div style={{ display: 'flex', gap: 5 }}>
            <Tooltip content="Logs">
              <button onClick={() => setShowLogs(true)} className="btn btn-ghost">
                <Icon name="description" size={14} />
              </button>
            </Tooltip>
            <Tooltip content="Edit">
              <button onClick={() => setShowEdit(true)} className="btn btn-ghost">
                <Icon name="tune" size={14} />
              </button>
            </Tooltip>
            {isRunning ? (
              <>
                <Tooltip content="Restart">
                  <button onClick={() => action.mutate({ act: 'restart', id })} disabled={!!actionPending} className="btn btn-ghost">
                    <Icon name="restart_alt" size={14} />
                  </button>
                </Tooltip>
                <Tooltip content="Stop">
                  <button onClick={() => action.mutate({ act: 'stop', id })} disabled={!!actionPending} className="btn btn-ghost">
                    <Icon name="stop_circle" size={14} />
                  </button>
                </Tooltip>
              </>
            ) : (
              <Tooltip content="Start">
                <button onClick={() => action.mutate({ act: 'start', id })} disabled={!!actionPending} className="btn btn-ghost">
                  <Icon name="play_circle" size={14} />
                </button>
              </Tooltip>
            )}
            <Tooltip content="Remove">
              <button onClick={() => setShowDelete(true)} className="btn btn-ghost" style={{ color: 'var(--error)' }}>
                <Icon name="delete" size={14} />
              </button>
            </Tooltip>
          </div>
        </td>
      </tr>
      {showLogs && <tr><td colSpan={6} /></tr>}
      {showLogs && <LogsModal containerName={container.name.replace(/^\//, '')} onClose={() => setShowLogs(false)} />}
      {showEdit && (
        <ContainerEditModal
          container={container}
          onClose={() => setShowEdit(false)}
          onSaved={() => { setShowEdit(false); onRefresh() }}
        />
      )}
      {showDelete && (
        <Modal title="Remove Container?" onClose={() => setShowDelete(false)}>
          <p style={{ color: 'var(--text-secondary)', marginBottom: 20 }}>
            Remove <strong>{container.name.replace(/^\//, '')}</strong>? The container will be stopped and deleted. Volumes are not removed.
          </p>
          <div style={{ display: 'flex', gap: 10 }}>
            <button
              onClick={() => deleteMutation.mutate()}
              disabled={deleteMutation.isPending}
              className="btn btn-ghost"
              style={{ color: 'var(--error)', borderColor: 'var(--error-border)' }}
            >
              <Icon name="delete" size={15} />{deleteMutation.isPending ? 'Removing…' : 'Remove'}
            </button>
            <button onClick={() => setShowDelete(false)} className="btn btn-ghost">Cancel</button>
          </div>
        </Modal>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// ContainersTab
// ---------------------------------------------------------------------------

// IconMapEntry and IconMapResponse are imported from @/lib/iconTypes

function ContainersTab() {
  const qc   = useQueryClient()
  const wsOn = useWsStore((s) => s.on)

  const containersQ = useQuery({
    queryKey: ['docker', 'containers'],
    queryFn: ({ signal }) => api.get<ContainersResponse>('/api/docker/containers', signal),
    refetchInterval: 15_000,
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

  const [viewMode, setViewMode] = useState<'grid' | 'stacks' | 'list'>('grid')

  function refresh() { qc.invalidateQueries({ queryKey: ['docker', 'containers'] }) }

  const stacks = containersQ.data?.stacks ?? []
  const containers = containersQ.data?.containers ?? []
  const total = containersQ.data?.total_containers ?? 0
  const allContainers = [...stacks.flatMap(s => s.containers), ...containers]
  const running = allContainers.filter(c => c.state === 'running').length

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
          { label: 'Ports', value: allContainers.reduce((n, c) => n + (c.ports?.length ?? 0), 0), icon: 'lan', color: 'var(--text-secondary)' },
        ].map(s => (
          <div key={s.label} className="card" style={{ borderRadius: 'var(--radius-md)', padding: '14px 18px' }}>
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
          {([
            { id: 'grid',   icon: 'grid_view',   label: 'Grid'     },
            { id: 'stacks', icon: 'folder',       label: 'By Stack' },
            { id: 'list',   icon: 'table_rows',   label: 'List'     },
          ] as const).map(v => (
            <button key={v.id} onClick={() => setViewMode(v.id)} className="btn btn-ghost" style={{ background: viewMode === v.id ? 'var(--primary-bg)' : 'var(--surface)', color: viewMode === v.id ? 'var(--primary)' : 'var(--text-secondary)', borderColor: viewMode === v.id ? 'hsla(var(--hue-primary),100%,72%,.3)' : 'var(--border)' }}>
              <Icon name={v.icon} size={14} />
              {v.label}
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
      {viewMode === 'grid' && (
        allContainers.length === 0
          ? <EmptyContainers />
          : <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(170px, 1fr))', gap: 16 }}>
              {allContainers.map(c => <ContainerCard key={c.id || c.name} container={c} onRefresh={refresh} />)}
            </div>
      )}
      {viewMode === 'stacks' && stacks.length === 0 && containers.length === 0 && (
        <EmptyContainers />
      )}
      {viewMode === 'stacks' && stacks.map(stack => (
        <StackSection key={stack.name ?? '__standalone'} stack={stack} onRefresh={refresh} />
      ))}
      {viewMode === 'list' && (
        allContainers.length === 0
          ? <EmptyContainers />
          : <ContainerTable containers={allContainers} onRefresh={refresh} />
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
      <div className="card" style={{ background: 'var(--surface)', borderRadius: open ? 'var(--radius-lg) var(--radius-lg) 0 0' : 'var(--radius-lg)', padding: '14px 20px', display: 'flex', justifyContent: 'space-between', alignItems: 'center', cursor: 'pointer' }}
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
    <div className="card" style={{ background: 'var(--surface)', ...(topBorder ? { borderRadius: 'var(--radius-lg)' } : { borderTop: 'none', borderRadius: '0 0 var(--radius-lg) var(--radius-lg)' }), overflow: 'hidden' }}>
      <table className="data-table">
        <thead>
          <tr style={{ background: 'rgba(255,255,255,0.03)' }}>
            <th>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 5 }}>
                Container
                <Tooltip content={
                    'Custom icon: add a dplaneos.icon label in docker-compose.yaml\n' +
                    '  dplaneos.icon: jellyfin        → Material Symbol name\n' +
                    '  dplaneos.icon: mylogo.svg       → file in /var/lib/dplaneos/custom_icons/\n' +
                    '  dplaneos.icon: https://…/logo.png → remote URL'
                  }>
                  <span style={{ cursor: 'help', color: 'var(--text-tertiary)', display: 'inline-flex' }}>
                    <Icon name="info" size={12} />
                  </span>
                </Tooltip>
              </span>
            </th>
            {['State', 'Ports', 'Resources', 'Uptime', 'Actions'].map(h => (
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
// GPUTab - host report + compose hints (GET /api/docker/gpu)
// ---------------------------------------------------------------------------

function GPUTab() {
  const q = useQuery({
    queryKey: ['docker', 'gpu'],
    queryFn: ({ signal }) => api.get<GPUReportResponse>('/api/docker/gpu', signal),
    refetchInterval: 60_000,
  })

  if (q.isLoading) {
    return <div className="card" style={{ padding: 28 }}><Skeleton /></div>
  }
  if (q.isError || !q.data?.success || !q.data.report) {
    return (
      <ErrorState
        title="Could not load GPU report"
        error={q.error ?? new Error('Invalid response')}
        onRetry={() => q.refetch()}
      />
    )
  }

  const rep = q.data.report
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
          <Icon name="memory" size={28} style={{ color: 'var(--primary)' }} />
          <div>
            <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>GPU passthrough readiness</div>
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
              Host facts from lspci, /dev/dri, nvidia-smi, and Docker. Compose is validated against this before deploy.
            </div>
          </div>
        </div>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: 12 }}>
          <div style={{ padding: 12, borderRadius: 'var(--radius-md)', background: 'var(--surface-2)' }}>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 4 }}>NVIDIA driver (nvidia-smi)</div>
            <div style={{ fontWeight: 600 }}>{rep.nvidia_driver_ok ? 'OK' : 'Not detected'}</div>
          </div>
          <div style={{ padding: 12, borderRadius: 'var(--radius-md)', background: 'var(--surface-2)' }}>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 4 }}>Docker nvidia runtime</div>
            <div style={{ fontWeight: 600 }}>{rep.nvidia_runtime_available ? 'Available' : 'Missing'}</div>
          </div>
          <div style={{ padding: 12, borderRadius: 'var(--radius-md)', background: 'var(--surface-2)' }}>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 4 }}>DRI devices</div>
            <div style={{ fontWeight: 600 }}>{rep.dri_nodes?.length ?? 0} node(s)</div>
          </div>
          <div style={{ padding: 12, borderRadius: 'var(--radius-md)', background: 'var(--surface-2)' }}>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 4 }}>NixOS option (toolkit)</div>
            <div style={{ fontFamily: 'var(--font-mono)', fontSize: 12 }}>{rep.nixos_docker_nvidia_option}</div>
          </div>
        </div>
      </div>

      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: 20 }}>
        <div style={{ fontWeight: 600, marginBottom: 10 }}>PCI display-class GPUs</div>
        {(!rep.pci_devices || rep.pci_devices.length === 0) ? (
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>None reported by lspci (or lspci unavailable).</div>
        ) : (
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 'var(--text-sm)' }}>
            {rep.pci_devices.map((p, i) => (
              <li key={i} style={{ marginBottom: 6 }}>
                <span style={{ fontFamily: 'var(--font-mono)' }}>{p.bus_id}</span>
                {' '}{p.vendor_name || p.raw_line}
                {p.vendor_id && p.device_id ? ` [${p.vendor_id}:${p.device_id}]` : ''}
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: 20 }}>
        <div style={{ fontWeight: 600, marginBottom: 10 }}>/dev/dri</div>
        {(!rep.dri_nodes || rep.dri_nodes.length === 0) ? (
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No character devices found.</div>
        ) : (
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 'var(--text-sm)', fontFamily: 'var(--font-mono)' }}>
            {rep.dri_nodes.map(n => (
              <li key={n.path}>{n.path}{n.is_render ? ' (render)' : ''}</li>
            ))}
          </ul>
        )}
      </div>

      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: 20 }}>
        <div style={{ fontWeight: 600, marginBottom: 10 }}>NVIDIA GPUs</div>
        {(!rep.nvidia_gpus || rep.nvidia_gpus.length === 0) ? (
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>nvidia-smi did not list any GPU.</div>
        ) : (
          <ul style={{ margin: 0, paddingLeft: 18, fontSize: 'var(--text-sm)' }}>
            {rep.nvidia_gpus.map((g, i) => (
              <li key={i}>{g.name} - driver {g.driver_version} - {g.uuid}</li>
            ))}
          </ul>
        )}
      </div>

      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: 20 }}>
        <div style={{ fontWeight: 600, marginBottom: 10 }}>Docker runtimes</div>
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }}>{(rep.docker_runtimes || []).join(', ') || '-'}</div>
      </div>

      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: 20 }}>
        <div style={{ fontWeight: 600, marginBottom: 10 }}>Compose reference (copy into your YAML)</div>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: 0 }}>NVIDIA device reservation</p>
        <pre style={{ margin: '0 0 16px', padding: 12, borderRadius: 8, background: 'var(--surface-2)', overflow: 'auto', fontSize: 12 }}>{rep.compose_examples.nvidia_device_reservation}</pre>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>DRI pass-through (adjust group_add GIDs)</p>
        <pre style={{ margin: 0, padding: 12, borderRadius: 8, background: 'var(--surface-2)', overflow: 'auto', fontSize: 12 }}>{rep.compose_examples.dri_render_group}</pre>
      </div>

      <button type="button" className="btn btn-ghost" onClick={() => q.refetch()}><Icon name="refresh" size={16} />Refresh</button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// PullTab
// ---------------------------------------------------------------------------

function DeployComposeSection({ onDone }: { onDone: () => void }) {
  const defaultYaml = 'services:\n  nginx:\n    image: nginx:latest\n    ports:\n      - "80:80"'
  const [name, setName] = useState('')
  const [yaml, setYaml] = useState(defaultYaml)

  const deploy = useMutation({
    mutationFn: async (data: { name: string; yaml: string }) => {
      const res = await api.post<StackDeployResponse>('/api/docker/stacks/deploy', data)
      if (!res.success) {
        throw new Error(res.error || res.message || 'Stack deploy failed')
      }
      return res
    },
    onSuccess: () => {
      toast.success('Stack deployed successfully')
      onDone()
      setName('')
      setYaml(defaultYaml)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function start() {
    if (!name.trim()) { toast.error('Stack name required'); return }
    if (!yaml.trim() || yaml.length < 20) { toast.error('Valid Compose YAML required'); return }
    deploy.mutate({ name, yaml })
  }

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
      <label className="field">
        <span className="field-label">Stack Name</span>
        <input value={name} onChange={e => setName(e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ''))}
          placeholder="my-cool-app" className="input" style={{ maxWidth: 300 }} />
      </label>

      <label className="field" style={{ flex: 1 }}>
        <span className="field-label">docker-compose.yml</span>
        <textarea 
          value={yaml} 
          onChange={e => setYaml(e.target.value)} 
          placeholder="version: '3'..." 
          className="input" 
          style={{ height: 240, fontFamily: 'var(--font-mono)', fontSize: 13, lineHeight: 1.5 }}
        />
      </label>

      <div style={{ display: 'flex', gap: 10 }}>
        <button onClick={start} disabled={deploy.isPending} className="btn btn-primary">
          <Icon name="rocket_launch" size={16} />{deploy.isPending ? 'Deploying…' : 'Deploy Stack'}
        </button>
        <button type="button" onClick={() => { setName(''); setYaml(defaultYaml) }} className="btn btn-ghost">Clear</button>
      </div>
    </div>
  )
}

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
      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 28 }}>
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

      {/* Compose YAML Deployment */}
      <div className="card" style={{ marginTop: 28, borderRadius: 'var(--radius-xl)', padding: 28 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 24 }}>
          <Icon name="add_box" size={28} style={{ color: 'var(--primary)' }} />
          <div>
            <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>Deploy Compose Stack</div>
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Paste a docker-compose.yml to deploy a new stack</div>
          </div>
        </div>

        <DeployComposeSection onDone={() => { qc.invalidateQueries({ queryKey: ['docker', 'containers'] }); qc.invalidateQueries({ queryKey: ['docker', 'compose', 'status'] }) }} />
      </div>

      {/* Quick reference */}
      <div className="card" style={{ marginTop: 20, padding: '16px 20px', borderRadius: 'var(--radius-lg)' }}>
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
// SSE streaming hook (fetch-based so we can send auth headers)
// ---------------------------------------------------------------------------

async function* parseSSEStream(url: string, signal: AbortSignal): AsyncGenerator<{type: string; data: string}> {
  const res = await fetch(url, {
    signal,
    headers: {
      'X-Session-ID': getSessionId() ?? '',
      'X-User': getUsername() ?? '',
    },
  })
  if (!res.ok || !res.body) throw new Error(`Stream error: ${res.status}`)
  const reader = res.body.getReader()
  const dec = new TextDecoder()
  let buf = ''
  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buf += dec.decode(value, { stream: true })
    const chunks = buf.split('\n\n')
    buf = chunks.pop() ?? ''
    for (const chunk of chunks) {
      if (!chunk.trim()) continue
      let type = 'message', data = ''
      for (const line of chunk.split('\n')) {
        if (line.startsWith('event: ')) type = line.slice(7).trim()
        else if (line.startsWith('data: ')) data = line.slice(6)
      }
      yield { type, data }
    }
  }
}

function useSSEStream() {
  const [lines, setLines] = useState<string[]>([])
  const [active, setActive] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const preRef = useRef<HTMLPreElement>(null)

  useEffect(() => {
    if (preRef.current) preRef.current.scrollTop = preRef.current.scrollHeight
  }, [lines])

  useEffect(() => () => { abortRef.current?.abort() }, [])

  const start = useCallback((url: string, onDone?: () => void) => {
    abortRef.current?.abort()
    setLines([])
    setActive(true)
    const ac = new AbortController()
    abortRef.current = ac
    ;(async () => {
      try {
        for await (const ev of parseSSEStream(url, ac.signal)) {
          if (ev.type === 'output' || ev.type === 'log') {
            setLines(prev => [...prev, ev.data])
          } else if (ev.type === 'done') {
            break
          } else if (ev.type === 'error') {
            setLines(prev => [...prev, `Error: ${ev.data}`])
            break
          }
        }
      } catch (e: any) {
        if (e?.name !== 'AbortError') setLines(prev => [...prev, `Stream error: ${e?.message ?? 'unknown'}`])
      } finally {
        setActive(false)
        abortRef.current = null
        onDone?.()
      }
    })()
  }, [])

  const stop = useCallback(() => {
    abortRef.current?.abort()
    abortRef.current = null
    setActive(false)
  }, [])

  const clear = useCallback(() => setLines([]), [])

  return { lines, active, start, stop, clear, preRef }
}

// ---------------------------------------------------------------------------
// ContainerExecModal  (WebSocket PTY into a running container)
// ---------------------------------------------------------------------------

interface ExecTarget { container: string; shell: 'bash' | 'sh' }

function ContainerExecModal({ target, onClose }: { target: ExecTarget; onClose: () => void }) {
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)
  const [shell, setShell] = useState<'bash' | 'sh'>(target.shell)
  const [status, setStatus] = useState<'connecting' | 'connected' | 'error'>('connecting')

  const connect = useCallback((sh: 'bash' | 'sh') => {
    wsRef.current?.close()
    termRef.current?.clear()
    setStatus('connecting')
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
    const sid = getSessionId() ?? ''
    const url = `${proto}//${window.location.host}/ws/docker/exec?container=${encodeURIComponent(target.container)}&shell=${sh}&session=${encodeURIComponent(sid)}`
    const ws = new WebSocket(url)
    wsRef.current = ws
    ws.onopen = () => {
      setStatus('connected')
      if (termRef.current) {
        const { cols, rows } = termRef.current
        ws.send(JSON.stringify({ type: 'resize', cols, rows }))
      }
    }
    ws.onmessage = (e) => {
      try {
        const msg = JSON.parse(e.data as string) as { type: string; data?: string }
        if (msg.type === 'output') termRef.current?.write(msg.data ?? '')
        else if (msg.type === 'exit') { termRef.current?.write('\r\n\x1b[90m[Process exited]\x1b[0m\r\n'); setStatus('error') }
        else if (msg.type === 'error') { termRef.current?.write(`\r\n\x1b[31m${msg.data}\x1b[0m\r\n`); setStatus('error') }
      } catch {}
    }
    ws.onclose = () => setStatus(s => s === 'connected' ? 'error' : s)
  }, [target.container])

  useEffect(() => {
    if (!containerRef.current) return
    const term = new Terminal({
      theme: { background: '#0d0f14', foreground: '#e2e8f0', cursor: '#a78bfa' },
      fontFamily: '"JetBrains Mono Variable", monospace',
      fontSize: 13, lineHeight: 1.5, cursorBlink: true, scrollback: 3000,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(containerRef.current)
    fit.fit()
    termRef.current = term
    fitRef.current = fit
    term.onData(data => {
      if (wsRef.current?.readyState === WebSocket.OPEN)
        wsRef.current.send(JSON.stringify({ type: 'input', data }))
    })
    connect(target.shell)
    const ro = new ResizeObserver(() => {
      fit.fit()
      if (wsRef.current?.readyState === WebSocket.OPEN)
        wsRef.current.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
    })
    ro.observe(containerRef.current)
    return () => { ro.disconnect(); wsRef.current?.close(); term.dispose() }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  function switchShell(sh: 'bash' | 'sh') { setShell(sh); connect(sh) }

  return (
    <div style={{ position: 'fixed', inset: 0, zIndex: 1000, display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'rgba(0,0,0,0.7)', backdropFilter: 'blur(4px)' }}
      onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div style={{ width: '90vw', maxWidth: 960, height: '80vh', display: 'flex', flexDirection: 'column', background: 'var(--surface)', borderRadius: 'var(--radius-xl)', border: '1px solid var(--border)', overflow: 'hidden' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 20px', borderBottom: '1px solid var(--border)', flexShrink: 0, background: 'var(--surface-2)' }}>
          <Icon name="terminal" size={18} style={{ color: 'var(--primary)' }} />
          <span style={{ fontWeight: 700, flex: 1, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }}>
            {target.container}
          </span>
          <div style={{ display: 'flex', gap: 4 }}>
            {(['bash', 'sh'] as const).map(sh => (
              <button key={sh} onClick={() => switchShell(sh)}
                style={{ padding: '3px 10px', borderRadius: 'var(--radius-sm)', border: 'none', fontSize: 'var(--text-xs)', fontFamily: 'var(--font-mono)',
                  background: shell === sh ? 'var(--primary-bg)' : 'var(--surface)',
                  color: shell === sh ? 'var(--primary)' : 'var(--text-secondary)', cursor: 'pointer' }}>
                {sh}
              </button>
            ))}
          </div>
          <span style={{ width: 8, height: 8, borderRadius: '50%', background: status === 'connected' ? 'var(--success)' : status === 'connecting' ? 'var(--warning)' : 'var(--error)', boxShadow: status === 'connected' ? '0 0 5px var(--success)' : 'none' }} />
          <button onClick={onClose} style={{ padding: '4px 8px', borderRadius: 'var(--radius-sm)', border: 'none', background: 'transparent', color: 'var(--text-secondary)', cursor: 'pointer', fontSize: 18, lineHeight: 1 }}>×</button>
        </div>
        <div ref={containerRef} style={{ flex: 1, padding: 8, background: '#0d0f14', minHeight: 0 }} />
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ComposeManager  (Dockge-style)
// ---------------------------------------------------------------------------

const COMPOSE_TEMPLATE = `services:
  app:
    image: nginx:latest
    ports:
      - "80:80"
    restart: unless-stopped
`

function ComposeManager() {
  const qc = useQueryClient()

  const stacksQ = useQuery({
    queryKey: ['docker', 'stacks'],
    queryFn: ({ signal }) => api.get<StacksResponse>('/api/docker/stacks', signal),
    refetchInterval: 10_000,
  })

  const [selected, setSelected] = useState<string | null>(null)
  const [isCreating, setIsCreating] = useState(false)
  const [newName, setNewName] = useState('')
  const [yaml, setYaml] = useState('')
  const [yamlDirty, setYamlDirty] = useState(false)
  const [env, setEnv] = useState('')
  const [envDirty, setEnvDirty] = useState(false)
  const [panelTab, setPanelTab] = useState<'yaml' | 'containers' | 'logs'>('yaml')
  const [deleteConfirm, setDeleteConfirm] = useState(false)
  const [saving, setSaving] = useState(false)
  const [execTarget, setExecTarget] = useState<ExecTarget | null>(null)

  const stream = useSSEStream()
  const logs = useSSEStream()

  const yamlQ = useQuery({
    queryKey: ['docker', 'stacks', 'yaml', selected],
    queryFn: ({ signal }) =>
      api.get<StackYAMLResponse>(`/api/docker/stacks/yaml?name=${encodeURIComponent(selected!)}`, signal),
    enabled: !!selected && !isCreating,
  })

  useEffect(() => {
    if (yamlQ.data?.yaml != null && !yamlDirty) setYaml(yamlQ.data.yaml)
    if (yamlQ.data?.env != null && !envDirty) setEnv(yamlQ.data.env)
  }, [yamlQ.data, yamlDirty, envDirty])

  function selectStack(name: string) {
    setSelected(name); setIsCreating(false); setYamlDirty(false); setEnvDirty(false)
    setPanelTab('yaml'); setDeleteConfirm(false); stream.clear(); logs.stop()
  }

  function startCreating() {
    setSelected(null); setIsCreating(true); setNewName(''); setYaml(COMPOSE_TEMPLATE); setEnv('')
    setYamlDirty(false); setEnvDirty(false); stream.clear()
  }

  function doAction(act: string) {
    if (!selected) return
    stream.start(
      `/api/docker/stacks/stream?name=${encodeURIComponent(selected)}&action=${encodeURIComponent(act)}`,
      () => {
        qc.invalidateQueries({ queryKey: ['docker', 'stacks'] })
        qc.invalidateQueries({ queryKey: ['docker', 'containers'] })
      }
    )
  }

  async function doSave(andDeploy: boolean) {
    if (!selected) return
    setSaving(true)
    try {
      const res = await api.put<{ success: boolean; error?: string }>(
        '/api/docker/stacks/yaml', { name: selected, yaml, env, redeploy: false }
      )
      if (!res.success) { toast.error(res.error ?? 'Save failed'); return }
      setYamlDirty(false); setEnvDirty(false)
      qc.invalidateQueries({ queryKey: ['docker', 'stacks', 'yaml', selected] })
      qc.invalidateQueries({ queryKey: ['docker', 'containers'] })
      if (andDeploy) { doAction('start') } else { toast.success('Saved') }
    } catch (e: any) { toast.error(e?.message ?? 'Save failed') }
    finally { setSaving(false) }
  }

  const deployNew = useMutation({
    mutationFn: async () => {
      if (!newName.trim()) throw new Error('Stack name required')
      const res = await api.post<{ success: boolean; output?: string; error?: string }>('/api/docker/stacks/deploy', { name: newName.trim(), yaml })
      if (!res.success) throw new Error(res.error ?? 'Deploy failed')
      return res
    },
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ['docker', 'stacks'] })
      qc.invalidateQueries({ queryKey: ['docker', 'containers'] })
      selectStack(newName.trim())
      if (res.output) toast.success('Stack deployed')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const doDelete = useMutation({
    mutationFn: async () => {
      const res = await api.delete<{ success: boolean; error?: string }>(`/api/docker/stacks?name=${encodeURIComponent(selected!)}`)
      if (!res.success) throw new Error(res.error ?? 'Delete failed')
      return res
    },
    onSuccess: () => {
      toast.success(`Stack "${selected}" deleted`)
      setSelected(null); setDeleteConfirm(false)
      qc.invalidateQueries({ queryKey: ['docker', 'stacks'] })
      qc.invalidateQueries({ queryKey: ['docker', 'containers'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const stacks = stacksQ.data?.stacks ?? []
  const selectedInfo = stacks.find(s => s.name === selected)
  const isPending = stream.active || saving || deployNew.isPending || doDelete.isPending

  const tabBtn = (id: 'yaml' | 'containers' | 'logs', label: string, icon: string) => (
    <button key={id} onClick={() => setPanelTab(id)} style={{ display: 'flex', alignItems: 'center', gap: 5, padding: '5px 12px', borderRadius: 'var(--radius-sm)', border: 'none', background: panelTab === id ? 'var(--primary-bg)' : 'transparent', color: panelTab === id ? 'var(--primary)' : 'var(--text-secondary)', fontWeight: panelTab === id ? 700 : 400, cursor: 'pointer', fontSize: 'var(--text-sm)' }}>
      <Icon name={icon} size={13} />{label}
    </button>
  )

  const terminalArea = (stream.lines.length > 0 || stream.active) && (
    <div style={{ flexShrink: 0, borderTop: '1px solid var(--border)' }}>
      {stream.active && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, padding: '6px 20px', background: 'rgba(0,0,0,0.3)', fontSize: 'var(--text-xs)', color: 'var(--primary)' }}>
          <span style={{ width: 6, height: 6, borderRadius: '50%', background: 'var(--primary)', animation: 'pulse 1s infinite' }} />
          Running...
          <button onClick={stream.stop} style={{ marginLeft: 'auto', padding: '2px 8px', borderRadius: 'var(--radius-sm)', border: 'none', background: 'rgba(255,255,255,0.08)', color: 'var(--text-secondary)', fontSize: 'var(--text-xs)', cursor: 'pointer' }}>Stop</button>
        </div>
      )}
      <pre ref={stream.preRef} style={{ margin: 0, padding: '10px 20px', background: 'rgba(0,0,0,0.45)', color: 'rgba(255,255,255,0.8)', fontSize: 12, fontFamily: 'var(--font-mono)', lineHeight: 1.5, maxHeight: 220, overflow: 'auto', whiteSpace: 'pre-wrap' }}>
        {stream.lines.join('\n')}
      </pre>
    </div>
  )

  return (
    <div style={{ display: 'flex', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', overflow: 'hidden', minHeight: 660, background: 'var(--surface)' }}>

      {/* ── Left sidebar ─────────────────────────────────────── */}
      <div style={{ width: 236, flexShrink: 0, borderRight: '1px solid var(--border)', display: 'flex', flexDirection: 'column' }}>
        <div style={{ padding: '13px 16px 11px', display: 'flex', justifyContent: 'space-between', alignItems: 'center', borderBottom: '1px solid var(--border)', background: 'var(--surface-2)' }}>
          <span style={{ fontWeight: 700, fontSize: 'var(--text-sm)', display: 'flex', alignItems: 'center', gap: 6 }}>
            <Icon name="folder" size={15} style={{ color: 'var(--primary)' }} />Stacks
          </span>
          <button onClick={startCreating} className="btn btn-ghost" style={{ padding: '4px 8px', fontSize: 'var(--text-xs)', color: 'var(--primary)' }}>
            <Icon name="add" size={14} />New
          </button>
        </div>

        <div style={{ flex: 1, overflow: 'auto' }}>
          {stacksQ.isLoading && <div style={{ padding: 16 }}><Skeleton height={40} /></div>}
          {!stacksQ.isLoading && stacks.length === 0 && (
            <div style={{ padding: 24, textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No stacks yet</div>
          )}
          {stacks.map(s => (
            <button key={s.name} onClick={() => selectStack(s.name)}
              style={{ display: 'flex', width: '100%', alignItems: 'center', gap: 10, padding: '11px 16px', background: selected === s.name && !isCreating ? 'var(--primary-bg)' : 'transparent', border: 'none', borderBottom: '1px solid var(--border)', color: selected === s.name && !isCreating ? 'var(--primary)' : 'var(--text)', cursor: 'pointer', textAlign: 'left', transition: 'background 0.15s' }}>
              <span style={{ width: 8, height: 8, borderRadius: '50%', background: stackStatusColor(s.status), flexShrink: 0, boxShadow: s.status === 'running' ? `0 0 5px ${stackStatusColor(s.status)}` : 'none' }} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.name}</div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 1 }}>{s.services?.length ?? 0} services · {s.status}</div>
              </div>
            </button>
          ))}
        </div>

        {stacks.length > 0 && (
          <div style={{ padding: '8px 16px', borderTop: '1px solid var(--border)', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', textAlign: 'center' }}>
            {stacks.length} stack{stacks.length !== 1 ? 's' : ''} · {stacks.filter(s => s.status === 'running').length} running
          </div>
        )}
      </div>

      {/* ── Right panel ──────────────────────────────────────── */}
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>

        {/* Empty state */}
        {!selected && !isCreating && (
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 12, color: 'var(--text-tertiary)' }}>
            <Icon name="folder_open" size={52} style={{ opacity: 0.25 }} />
            <div style={{ fontSize: 'var(--text-md)', fontWeight: 600 }}>Select a stack to edit</div>
            <button onClick={startCreating} className="btn btn-primary" style={{ marginTop: 8 }}>
              <Icon name="add" size={15} />New Stack
            </button>
          </div>
        )}

        {/* New stack creator */}
        {isCreating && (
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
            <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
              <div style={{ flex: 1 }}>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 4 }}>Stack name</div>
                <input value={newName} onChange={e => setNewName(e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ''))} placeholder="my-stack" className="input" style={{ maxWidth: 200, padding: '6px 10px', fontFamily: 'var(--font-mono)' }} autoFocus />
              </div>
              <div style={{ display: 'flex', gap: 8, alignItems: 'flex-end' }}>
                <button onClick={() => deployNew.mutate()} disabled={deployNew.isPending || !newName.trim()} className="btn btn-primary">
                  <Icon name="rocket_launch" size={15} />{deployNew.isPending ? 'Deploying…' : 'Deploy'}
                </button>
                <button onClick={() => setIsCreating(false)} className="btn btn-ghost">Cancel</button>
              </div>
            </div>
            <textarea value={yaml} onChange={e => setYaml(e.target.value)} spellCheck={false}
              style={{ flex: 1, width: '100%', padding: '16px 20px', fontFamily: 'var(--font-mono)', fontSize: 13, lineHeight: 1.6, background: 'var(--bg)', color: 'var(--text)', border: 'none', outline: 'none', resize: 'none', boxSizing: 'border-box' }}
            />
            {terminalArea}
          </div>
        )}

        {/* Existing stack editor */}
        {selected && !isCreating && selectedInfo && (
          <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
            {/* Header */}
            <div style={{ padding: '13px 20px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap', background: 'var(--surface-2)' }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, flex: 1, minWidth: 0 }}>
                <span style={{ fontWeight: 700, fontSize: 'var(--text-md)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{selected}</span>
                <span style={{ padding: '2px 8px', borderRadius: 'var(--radius-full)', background: selectedInfo.status === 'running' ? 'var(--success-bg)' : selectedInfo.status === 'partial' ? 'rgba(234,179,8,0.1)' : 'var(--surface)', border: `1px solid ${stackStatusColor(selectedInfo.status)}33`, color: stackStatusColor(selectedInfo.status), fontSize: 'var(--text-2xs)', fontWeight: 700, flexShrink: 0 }}>
                  {(selectedInfo.status ?? 'unknown').toUpperCase()}
                </span>
              </div>
              <div style={{ display: 'flex', gap: 5, flexWrap: 'wrap' }}>
                <button onClick={() => doAction('start')} disabled={isPending} className="btn btn-ghost" style={{ color: 'var(--success)', padding: '5px 10px' }}>
                  <Icon name="play_arrow" size={14} />Start
                </button>
                <button onClick={() => doAction('stop')} disabled={isPending} className="btn btn-ghost" style={{ color: 'var(--warning)', padding: '5px 10px' }}>
                  <Icon name="stop" size={14} />Stop
                </button>
                <button onClick={() => doAction('restart')} disabled={isPending} className="btn btn-ghost" style={{ padding: '5px 10px' }}>
                  <Icon name="restart_alt" size={14} />Restart
                </button>
                <button onClick={() => doAction('update')} disabled={isPending} className="btn btn-ghost" style={{ color: 'var(--primary)', padding: '5px 10px' }}>
                  <Icon name="cloud_download" size={14} />Update
                </button>
                {!deleteConfirm ? (
                  <button onClick={() => setDeleteConfirm(true)} disabled={isPending} className="btn btn-ghost" style={{ color: 'var(--error)', padding: '5px 10px' }}>
                    <Icon name="delete" size={14} />Delete
                  </button>
                ) : (
                  <div style={{ display: 'flex', gap: 4, alignItems: 'center' }}>
                    <span style={{ fontSize: 'var(--text-xs)', color: 'var(--error)' }}>Sure?</span>
                    <button onClick={() => doDelete.mutate()} disabled={doDelete.isPending} className="btn btn-ghost" style={{ color: 'var(--error)', padding: '4px 8px' }}>{doDelete.isPending ? '…' : 'Yes'}</button>
                    <button onClick={() => setDeleteConfirm(false)} className="btn btn-ghost" style={{ padding: '4px 8px' }}>No</button>
                  </div>
                )}
              </div>
            </div>

            {/* Sub-tabs */}
            <div style={{ display: 'flex', gap: 2, padding: '8px 20px', borderBottom: '1px solid var(--border)' }}>
              {tabBtn('yaml', 'docker-compose.yml', 'description')}
              {tabBtn('containers', `Containers (${selectedInfo.services?.length ?? 0})`, 'deployed_code')}
              {tabBtn('logs', 'Logs', 'terminal')}
            </div>

            {/* YAML + .env editor */}
            {panelTab === 'yaml' && (
              <>
                {yamlQ.isLoading && <div style={{ flex: 1, padding: 20 }}><Skeleton height={300} /></div>}
                {!yamlQ.isLoading && (
                  <>
                    <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
                      <textarea value={yaml} onChange={e => { setYaml(e.target.value); setYamlDirty(true) }} spellCheck={false}
                        style={{ flex: 1, width: '100%', padding: '16px 20px', fontFamily: 'var(--font-mono)', fontSize: 13, lineHeight: 1.6, background: 'var(--bg)', color: 'var(--text)', border: 'none', outline: 'none', resize: 'none', minHeight: 180, boxSizing: 'border-box' }}
                      />
                      <div style={{ borderTop: '1px solid var(--border)', padding: '6px 20px 0', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', display: 'flex', alignItems: 'center', gap: 6, background: 'var(--surface-2)' }}>
                        <Icon name="key" size={12} />.env
                      </div>
                      <textarea value={env} onChange={e => { setEnv(e.target.value); setEnvDirty(true) }} spellCheck={false}
                        placeholder={'# KEY=value'}
                        style={{ width: '100%', height: 90, padding: '8px 20px', fontFamily: 'var(--font-mono)', fontSize: 12, lineHeight: 1.5, background: 'var(--bg)', color: 'rgba(255,255,255,0.6)', border: 'none', outline: 'none', resize: 'none', boxSizing: 'border-box' }}
                      />
                    </div>
                    <div style={{ padding: '10px 20px', borderTop: '1px solid var(--border)', display: 'flex', gap: 8, alignItems: 'center', background: 'var(--surface)', flexShrink: 0 }}>
                      <button onClick={() => doSave(false)} disabled={isPending || (!yamlDirty && !envDirty)} className="btn btn-ghost">
                        <Icon name="save" size={14} />{saving ? 'Saving…' : 'Save'}
                      </button>
                      <button onClick={() => doSave(true)} disabled={isPending} className="btn btn-primary">
                        <Icon name="rocket_launch" size={14} />{saving ? 'Saving…' : 'Save & Deploy'}
                      </button>
                      {(yamlDirty || envDirty) && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--warning)', marginLeft: 4 }}>unsaved changes</span>}
                    </div>
                  </>
                )}
              </>
            )}

            {/* Containers view with per-service actions */}
            {panelTab === 'containers' && (
              <div style={{ flex: 1, overflow: 'auto', padding: 20 }}>
                {(!selectedInfo.services || selectedInfo.services.length === 0) ? (
                  <div style={{ color: 'var(--text-tertiary)', textAlign: 'center', padding: 40 }}>No containers in this stack</div>
                ) : (
                  <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                    {selectedInfo.services.map((svc, i) => {
                      const raw = ((svc.State ?? svc.Status) || '').toLowerCase()
                      const up = raw.includes('running')
                      const svcName = svc.Service || svc.Name || `service-${i}`
                      const containerName = svc.Name || ''
                      return (
                        <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 14px', background: 'var(--surface-2)', borderRadius: 'var(--radius-md)', border: '1px solid var(--border)' }}>
                          <span style={{ width: 8, height: 8, borderRadius: '50%', flexShrink: 0, background: up ? 'var(--success)' : 'var(--text-tertiary)', boxShadow: up ? '0 0 5px var(--success)' : 'none' }} />
                          <span style={{ fontWeight: 600, fontSize: 'var(--text-sm)', fontFamily: 'var(--font-mono)', flex: 1 }}>{svcName}</span>
                          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginRight: 6 }}>{svc.Status || svc.State || 'unknown'}</span>
                          <button onClick={() => { api.post('/api/docker/stacks/services/action', { stack: selected, service: svcName, action: 'start' }).then(() => { qc.invalidateQueries({ queryKey: ['docker', 'stacks'] }); qc.invalidateQueries({ queryKey: ['docker', 'containers'] }) }) }} className="btn btn-ghost" style={{ padding: '3px 6px', color: 'var(--success)' }} title="Start">
                            <Icon name="play_arrow" size={14} />
                          </button>
                          <button onClick={() => { api.post('/api/docker/stacks/services/action', { stack: selected, service: svcName, action: 'stop' }).then(() => { qc.invalidateQueries({ queryKey: ['docker', 'stacks'] }); qc.invalidateQueries({ queryKey: ['docker', 'containers'] }) }) }} className="btn btn-ghost" style={{ padding: '3px 6px', color: 'var(--warning)' }} title="Stop">
                            <Icon name="stop" size={14} />
                          </button>
                          <button onClick={() => { api.post('/api/docker/stacks/services/action', { stack: selected, service: svcName, action: 'restart' }).then(() => { qc.invalidateQueries({ queryKey: ['docker', 'stacks'] }); qc.invalidateQueries({ queryKey: ['docker', 'containers'] }) }) }} className="btn btn-ghost" style={{ padding: '3px 6px' }} title="Restart">
                            <Icon name="restart_alt" size={14} />
                          </button>
                          {containerName && (
                            <button onClick={() => setExecTarget({ container: containerName, shell: 'bash' })} className="btn btn-ghost" style={{ padding: '3px 6px' }} title="Open shell">
                              <Icon name="terminal" size={14} />
                            </button>
                          )}
                        </div>
                      )
                    })}
                  </div>
                )}
              </div>
            )}

            {/* Combined logs view */}
            {panelTab === 'logs' && (() => {
              // Connect when tab is active
              if (!logs.active && logs.lines.length === 0 && selected) {
                logs.start(`/api/docker/stacks/logs/stream?name=${encodeURIComponent(selected)}`)
              }
              return (
                <div style={{ flex: 1, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
                  <div style={{ padding: '6px 20px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 8, fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', background: 'var(--surface-2)', flexShrink: 0 }}>
                    <span style={{ width: 6, height: 6, borderRadius: '50%', background: logs.active ? 'var(--success)' : 'var(--text-tertiary)', boxShadow: logs.active ? '0 0 4px var(--success)' : 'none' }} />
                    {logs.active ? 'Live' : 'Stopped'} · {logs.lines.length} lines
                    <button onClick={() => logs.active ? logs.stop() : logs.start(`/api/docker/stacks/logs/stream?name=${encodeURIComponent(selected!)}`)} className="btn btn-ghost" style={{ padding: '2px 8px', marginLeft: 'auto', fontSize: 'var(--text-xs)' }}>
                      <Icon name={logs.active ? 'stop' : 'play_arrow'} size={12} />{logs.active ? 'Stop' : 'Connect'}
                    </button>
                    <button onClick={logs.clear} className="btn btn-ghost" style={{ padding: '2px 8px', fontSize: 'var(--text-xs)' }}>
                      <Icon name="clear_all" size={12} />Clear
                    </button>
                  </div>
                  <pre ref={logs.preRef} style={{ flex: 1, margin: 0, padding: '10px 20px', background: '#0d0f14', color: 'rgba(226,232,240,0.85)', fontSize: 12, fontFamily: 'var(--font-mono)', lineHeight: 1.5, overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all' }}>
                    {logs.lines.length === 0 ? (logs.active ? 'Waiting for log output...' : 'No logs yet. Click Connect.') : logs.lines.join('\n')}
                  </pre>
                </div>
              )
            })()}

            {terminalArea}
            {execTarget && <ContainerExecModal target={execTarget} onClose={() => setExecTarget(null)} />}
          </div>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// GitSyncTab - Arcane-style multi-repo git sync for compose stacks
// ---------------------------------------------------------------------------

interface GitRepo {
  id: number
  name: string
  repo_url: string
  branch: string
  local_path: string
  compose_path: string
  auto_sync: boolean
  sync_interval: number
  cred_id: number | null
  cred_name: string
  commit_name: string
  commit_email: string
  last_sync_at: string
  last_commit: string
  last_error: string
  enabled: boolean
}

interface GitCredential {
  id: number
  name: string
  host: string
  auth_type: 'token' | 'ssh'
  notes: string
  created_at: string
  has_token: boolean
  has_ssh: boolean
}

function relativeTime(iso: string): string {
  if (!iso) return 'Never'
  const d = new Date(iso)
  const secs = Math.floor((Date.now() - d.getTime()) / 1000)
  if (secs < 60) return 'Just now'
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`
  return `${Math.floor(secs / 86400)}d ago`
}

// ── Repo form modal ─────────────────────────────────────────────────────────

const EMPTY_REPO_FORM = {
  name: '', repo_url: '', branch: 'main', compose_path: 'docker-compose.yml',
  cred_id: null as number | null, auto_sync: false, sync_interval: 15,
  commit_name: 'D-PlaneOS', commit_email: 'dplaneos@localhost', enabled: true,
}

// ── File browser modal ───────────────────────────────────────────────────────

const COMPOSE_FILENAMES = new Set(['docker-compose.yml', 'docker-compose.yaml', 'compose.yml', 'compose.yaml'])

interface BrowseEntry { name: string; path: string; type: 'file' | 'directory' }

function FileBrowserModal({ repoId, onSelect, onClose }: {
  repoId: number
  onSelect: (path: string) => void
  onClose: () => void
}) {
  const [currentPath, setCurrentPath] = useState('')
  const [entries, setEntries] = useState<BrowseEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [browseError, setBrowseError] = useState('')

  useEffect(() => {
    setLoading(true)
    setBrowseError('')
    const params = new URLSearchParams({ id: String(repoId) })
    if (currentPath) params.set('path', currentPath)
    api.get<{ success: boolean; files: BrowseEntry[]; error?: string }>(`/api/git-sync/repos/browse?${params}`)
      .then(res => {
        if (res.success) setEntries(res.files ?? [])
        else setBrowseError(res.error ?? 'Browse failed')
      })
      .catch(e => setBrowseError(e?.message ?? 'Browse failed'))
      .finally(() => setLoading(false))
  }, [repoId, currentPath])

  const pathParts = currentPath ? currentPath.split('/').filter(Boolean) : []

  return (
    <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.75)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 300 }}>
      <div style={{ background: 'var(--surface)', borderRadius: 'var(--radius)', width: 480, maxHeight: '70vh', display: 'flex', flexDirection: 'column', boxShadow: 'var(--shadow-lg)', border: '1px solid var(--border)' }}>
        <div style={{ padding: '14px 18px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon name="folder_open" size={18} style={{ color: 'var(--primary)' }} />
          <span style={{ fontWeight: 600, fontSize: 'var(--text-base)' }}>Browse Repository</span>
          <button onClick={onClose} className="btn btn-ghost" style={{ marginLeft: 'auto', padding: '4px 8px' }}>
            <Icon name="close" size={16} />
          </button>
        </div>

        {/* Breadcrumb path */}
        <div style={{ padding: '6px 12px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 2, flexWrap: 'wrap', minHeight: 34 }}>
          <button onClick={() => setCurrentPath('')} className="btn btn-ghost"
            style={{ padding: '2px 6px', fontSize: 'var(--text-xs)', color: 'var(--primary)', fontWeight: 600 }}>
            root
          </button>
          {pathParts.map((part, i) => (
            <span key={i} style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
              <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>/</span>
              <button onClick={() => setCurrentPath(pathParts.slice(0, i + 1).join('/'))} className="btn btn-ghost"
                style={{ padding: '2px 6px', fontSize: 'var(--text-xs)', color: i === pathParts.length - 1 ? 'var(--text)' : 'var(--primary)' }}>
                {part}
              </button>
            </span>
          ))}
        </div>

        <div style={{ flex: 1, overflow: 'auto', padding: 6 }}>
          {loading && <div style={{ padding: 20, textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>Loading...</div>}
          {browseError && (
            <div style={{ padding: '12px 16px', color: 'var(--error)', fontSize: 'var(--text-sm)', background: 'color-mix(in srgb, var(--error) 8%, transparent)', margin: 8, borderRadius: 'var(--radius-sm)' }}>
              <Icon name="error" size={14} /> {browseError}
            </div>
          )}
          {!loading && !browseError && entries.length === 0 && (
            <div style={{ padding: 24, textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>Empty directory</div>
          )}
          {!loading && !browseError && entries.map(entry => {
            const isCompose = entry.type === 'file' && COMPOSE_FILENAMES.has(entry.name)
            const clickable = entry.type === 'directory' || isCompose
            return (
              <button key={entry.path} onClick={() => {
                if (entry.type === 'directory') setCurrentPath(entry.path)
                else if (isCompose) { onSelect(entry.path); onClose() }
              }} className="btn btn-ghost" style={{
                display: 'flex', alignItems: 'center', gap: 10, width: '100%',
                textAlign: 'left', padding: '8px 10px', borderRadius: 'var(--radius-sm)',
                cursor: clickable ? 'pointer' : 'default',
                opacity: entry.type === 'file' && !isCompose ? 0.4 : 1,
              }}>
                <Icon name={entry.type === 'directory' ? 'folder' : isCompose ? 'description' : 'insert_drive_file'} size={16}
                  style={{ color: entry.type === 'directory' ? '#f0a500' : isCompose ? 'var(--primary)' : 'var(--text-tertiary)', flexShrink: 0 }} />
                <span style={{ fontSize: 'var(--text-sm)', flex: 1 }}>{entry.name}</span>
                {isCompose && (
                  <span style={{ fontSize: 'var(--text-xs)', color: 'var(--primary)', background: 'var(--primary-bg)', padding: '1px 8px', borderRadius: 10 }}>select</span>
                )}
                {entry.type === 'directory' && (
                  <Icon name="chevron_right" size={14} style={{ color: 'var(--text-tertiary)' }} />
                )}
              </button>
            )
          })}
        </div>
      </div>
    </div>
  )
}

// ── Export result modal ──────────────────────────────────────────────────────

function ExportModal({ repoId, repoName, yaml, onClose }: {
  repoId: number
  repoName: string
  yaml: string
  onClose: () => void
}) {
  const [commitMsg, setCommitMsg] = useState(`Export: ${repoName} running containers`)
  const [pushing, setPushing] = useState(false)

  async function handlePush() {
    if (!commitMsg.trim()) return
    setPushing(true)
    try {
      const res = await api.post<{ success: boolean; error?: string; commit?: string }>(
        `/api/git-sync/repos/push?id=${repoId}`, { message: commitMsg })
      if (!res.success) { toast.error(res.error ?? 'Push failed'); return }
      toast.success(`Pushed${res.commit ? ` (${res.commit.slice(0, 7)})` : ''}`)
      onClose()
    } catch (e: any) { toast.error(e?.message ?? 'Push failed') }
    finally { setPushing(false) }
  }

  const inputS: React.CSSProperties = {
    padding: '8px 10px', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
    background: 'var(--surface-2)', color: 'var(--text)', fontSize: 'var(--text-sm)',
  }

  return (
    <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.75)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 200 }}>
      <div style={{ background: 'var(--surface)', borderRadius: 'var(--radius)', width: 620, maxHeight: '82vh', display: 'flex', flexDirection: 'column', boxShadow: 'var(--shadow-lg)', border: '1px solid var(--border)' }}>
        <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--border)', display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon name="code" size={18} style={{ color: 'var(--primary)' }} />
          <div>
            <div style={{ fontWeight: 600 }}>Exported compose YAML</div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Generated from running containers. Review before committing.</div>
          </div>
          <button onClick={onClose} className="btn btn-ghost" style={{ marginLeft: 'auto', padding: '4px 8px' }}>
            <Icon name="close" size={16} />
          </button>
        </div>
        <textarea readOnly value={yaml} style={{ flex: 1, margin: '12px 16px 0', fontFamily: 'var(--font-mono)', fontSize: 12, background: '#0d0f14', color: 'rgba(226,232,240,0.85)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', padding: '10px 14px', resize: 'none', minHeight: 220, lineHeight: 1.6 }} />
        <div style={{ padding: '12px 16px 16px', display: 'flex', gap: 8, alignItems: 'center' }}>
          <input value={commitMsg} onChange={e => setCommitMsg(e.target.value)}
            placeholder="Commit message" style={{ ...inputS, flex: 1 }} />
          <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button onClick={handlePush} disabled={pushing || !commitMsg.trim()} className="btn btn-primary">
            <Icon name="upload" size={14} />{pushing ? 'Pushing...' : 'Commit & Push'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ── Repo form modal ─────────────────────────────────────────────────────────

function RepoModal({
  initial, credentials, onSave, onClose,
}: {
  initial?: GitRepo | null
  credentials: GitCredential[]
  onSave: () => void
  onClose: () => void
}) {
  const [form, setForm] = useState(initial
    ? { name: initial.name, repo_url: initial.repo_url, branch: initial.branch,
        compose_path: initial.compose_path, cred_id: initial.cred_id,
        auto_sync: initial.auto_sync, sync_interval: initial.sync_interval,
        commit_name: initial.commit_name, commit_email: initial.commit_email,
        enabled: initial.enabled }
    : { ...EMPTY_REPO_FORM })
  const [branches, setBranches] = useState<string[]>([])
  const [loadingBranches, setLoadingBranches] = useState(false)
  const [saving, setSaving] = useState(false)
  const [showBrowser, setShowBrowser] = useState(false)

  async function fetchBranches() {
    if (!form.repo_url) return
    setLoadingBranches(true)
    try {
      const params = new URLSearchParams({ url: form.repo_url })
      if (form.cred_id) params.set('id', String(form.cred_id))
      const res = await api.get<{ success: boolean; branches: { name: string }[]; error?: string }>(
        `/api/git-sync/credentials/branches?${params}`)
      if (res.success) setBranches(res.branches.map(b => b.name))
      else toast.error(res.error ?? 'Failed to list branches')
    } catch (e: any) { toast.error(e?.message ?? 'Failed to list branches') }
    finally { setLoadingBranches(false) }
  }

  async function handleSave() {
    if (!form.name.trim()) { toast.error('Name is required'); return }
    if (!form.repo_url.trim()) { toast.error('Repo URL is required'); return }
    setSaving(true)
    try {
      const body: Record<string, unknown> = { ...form }
      if (initial) body.id = initial.id
      const res = await api.post<{ success: boolean; error?: string }>('/api/git-sync/repos', body)
      if (!res.success) { toast.error(res.error ?? 'Save failed'); return }
      toast.success(initial ? 'Repo updated' : 'Repo added')
      onSave()
    } catch (e: any) { toast.error(e?.message ?? 'Save failed') }
    finally { setSaving(false) }
  }

  const inputStyle: React.CSSProperties = {
    width: '100%', padding: '8px 10px', border: '1px solid var(--border)',
    borderRadius: 'var(--radius-sm)', background: 'var(--surface-2)',
    color: 'var(--text)', fontSize: 'var(--text-sm)', boxSizing: 'border-box',
  }

  return (
    <>
      <Modal title={initial ? 'Edit Repository' : 'Add Repository'} onClose={onClose}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div>
            <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Name</label>
            <input value={form.name} onChange={e => setForm(f => ({ ...f, name: e.target.value }))} style={inputStyle} placeholder="my-stack" disabled={!!initial} />
          </div>
          <div>
            <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Repository URL</label>
            <input value={form.repo_url} onChange={e => setForm(f => ({ ...f, repo_url: e.target.value }))} style={inputStyle} placeholder="https://github.com/user/repo.git" />
          </div>
          <div>
            <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Credential (optional)</label>
            <select value={form.cred_id ?? ''} onChange={e => setForm(f => ({ ...f, cred_id: e.target.value ? Number(e.target.value) : null }))} style={{ ...inputStyle, cursor: 'pointer' }}>
              <option value="">None (public repo)</option>
              {credentials.map(c => <option key={c.id} value={c.id}>{c.name} ({c.auth_type})</option>)}
            </select>
          </div>
          <div>
            <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Branch</label>
            <div style={{ display: 'flex', gap: 8 }}>
              {branches.length > 0 ? (
                <select value={form.branch} onChange={e => setForm(f => ({ ...f, branch: e.target.value }))} style={{ ...inputStyle, flex: 1, cursor: 'pointer' }}>
                  {branches.map(b => <option key={b} value={b}>{b}</option>)}
                </select>
              ) : (
                <input value={form.branch} onChange={e => setForm(f => ({ ...f, branch: e.target.value }))} style={{ ...inputStyle, flex: 1 }} placeholder="main" />
              )}
              <button onClick={fetchBranches} disabled={loadingBranches || !form.repo_url} className="btn btn-ghost" style={{ flexShrink: 0, padding: '8px 12px', fontSize: 'var(--text-xs)' }}>
                {loadingBranches ? '...' : <><Icon name="sync" size={13} />Fetch</>}
              </button>
            </div>
          </div>
          <div>
            <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Compose file path (relative)</label>
            <div style={{ display: 'flex', gap: 8 }}>
              <input value={form.compose_path} onChange={e => setForm(f => ({ ...f, compose_path: e.target.value }))} style={{ ...inputStyle, flex: 1 }} placeholder="docker-compose.yml" />
              {initial && (
                <button onClick={() => setShowBrowser(true)} className="btn btn-ghost" title="Browse cloned repo" style={{ flexShrink: 0, padding: '8px 12px', fontSize: 'var(--text-xs)' }}>
                  <Icon name="folder_open" size={13} />Browse
                </button>
              )}
            </div>
          </div>
          <div style={{ display: 'flex', gap: 12 }}>
            <div style={{ flex: 1 }}>
              <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Auto-sync interval (min)</label>
              <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
                <input type="checkbox" checked={form.auto_sync} onChange={e => setForm(f => ({ ...f, auto_sync: e.target.checked }))} style={{ width: 'auto' }} id="auto-sync-chk" />
                <label htmlFor="auto-sync-chk" style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', cursor: 'pointer' }}>Auto sync</label>
                <input type="number" min={1} max={1440} value={form.sync_interval}
                  onChange={e => setForm(f => ({ ...f, sync_interval: Number(e.target.value) }))}
                  disabled={!form.auto_sync}
                  style={{ ...inputStyle, width: 80, opacity: form.auto_sync ? 1 : 0.4 }} />
              </div>
            </div>
          </div>
          <details style={{ cursor: 'pointer' }}>
            <summary style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', userSelect: 'none' }}>Commit identity</summary>
            <div style={{ display: 'flex', gap: 12, marginTop: 10 }}>
              <div style={{ flex: 1 }}>
                <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Name</label>
                <input value={form.commit_name} onChange={e => setForm(f => ({ ...f, commit_name: e.target.value }))} style={inputStyle} placeholder="D-PlaneOS" />
              </div>
              <div style={{ flex: 1 }}>
                <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Email</label>
                <input value={form.commit_email} onChange={e => setForm(f => ({ ...f, commit_email: e.target.value }))} style={inputStyle} placeholder="dplaneos@localhost" />
              </div>
            </div>
          </details>
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, paddingTop: 4 }}>
            <button onClick={onClose} className="btn btn-ghost">Cancel</button>
            <button onClick={handleSave} disabled={saving} className="btn btn-primary">
              {saving ? 'Saving...' : initial ? 'Update' : 'Add Repository'}
            </button>
          </div>
        </div>
      </Modal>
      {showBrowser && initial && (
        <FileBrowserModal
          repoId={initial.id}
          onSelect={path => setForm(f => ({ ...f, compose_path: path }))}
          onClose={() => setShowBrowser(false)}
        />
      )}
    </>
  )
}

// ── Credential form modal ────────────────────────────────────────────────────

function CredentialModal({
  initial, onSave, onClose,
}: { initial?: GitCredential | null; onSave: () => void; onClose: () => void }) {
  const [name, setName] = useState(initial?.name ?? '')
  const [host, setHost] = useState(initial?.host ?? 'github.com')
  const [authType, setAuthType] = useState<'token' | 'ssh'>(initial?.auth_type ?? 'token')
  const [token, setToken] = useState('')
  const [sshKey, setSshKey] = useState('')
  const [notes, setNotes] = useState(initial?.notes ?? '')
  const [saving, setSaving] = useState(false)

  async function handleSave() {
    if (!name.trim()) { toast.error('Name is required'); return }
    setSaving(true)
    try {
      const body: Record<string, unknown> = { name, host, auth_type: authType, notes }
      if (initial) body.id = initial.id
      if (authType === 'token' && token) body.token = token
      if (authType === 'ssh' && sshKey) body.ssh_key = sshKey
      const res = await api.post<{ success: boolean; error?: string }>('/api/git-sync/credentials', body)
      if (!res.success) { toast.error(res.error ?? 'Save failed'); return }
      toast.success(initial ? 'Credential updated' : 'Credential added')
      onSave()
    } catch (e: any) { toast.error(e?.message ?? 'Save failed') }
    finally { setSaving(false) }
  }

  const inputStyle: React.CSSProperties = {
    width: '100%', padding: '8px 10px', border: '1px solid var(--border)',
    borderRadius: 'var(--radius-sm)', background: 'var(--surface-2)',
    color: 'var(--text)', fontSize: 'var(--text-sm)', boxSizing: 'border-box',
  }

  return (
    <Modal title={initial ? 'Edit Credential' : 'Add Credential'} onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
        <div style={{ display: 'flex', gap: 12 }}>
          <div style={{ flex: 1 }}>
            <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Name</label>
            <input value={name} onChange={e => setName(e.target.value)} style={inputStyle} placeholder="GitHub PAT" />
          </div>
          <div style={{ flex: 1 }}>
            <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Host</label>
            <input value={host} onChange={e => setHost(e.target.value)} style={inputStyle} placeholder="github.com" />
          </div>
        </div>
        <div>
          <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Auth type</label>
          <div style={{ display: 'flex', gap: 8 }}>
            {(['token', 'ssh'] as const).map(t => (
              <button key={t} onClick={() => setAuthType(t)} className="btn btn-ghost"
                style={{ padding: '6px 14px', fontSize: 'var(--text-sm)', fontWeight: authType === t ? 700 : 400, background: authType === t ? 'var(--primary-bg)' : 'transparent', color: authType === t ? 'var(--primary)' : 'var(--text-secondary)', borderRadius: 'var(--radius-sm)' }}>
                <Icon name={t === 'token' ? 'key' : 'terminal'} size={14} />{t === 'token' ? 'Personal Access Token' : 'SSH Key'}
              </button>
            ))}
          </div>
        </div>
        {authType === 'token' ? (
          <div>
            <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>
              {initial ? 'New token (leave blank to keep existing)' : 'Personal Access Token'}
            </label>
            <input type="password" value={token} onChange={e => setToken(e.target.value)} style={inputStyle} placeholder={initial ? '(unchanged)' : 'ghp_...'} />
          </div>
        ) : (
          <div>
            <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>
              {initial ? 'New SSH private key (leave blank to keep existing)' : 'SSH private key'}
            </label>
            <textarea value={sshKey} onChange={e => setSshKey(e.target.value)} rows={6}
              style={{ ...inputStyle, fontFamily: 'var(--font-mono)', fontSize: 12, resize: 'vertical' }}
              placeholder={initial ? '(unchanged)' : '-----BEGIN OPENSSH PRIVATE KEY-----\n...'} />
          </div>
        )}
        <div>
          <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Notes</label>
          <input value={notes} onChange={e => setNotes(e.target.value)} style={inputStyle} placeholder="Optional description" />
        </div>
        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8, paddingTop: 4 }}>
          <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button onClick={handleSave} disabled={saving} className="btn btn-primary">
            {saving ? 'Saving...' : initial ? 'Update' : 'Add Credential'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

// ── GitSyncTab ───────────────────────────────────────────────────────────────

interface RepoStatus {
  checking: boolean
  behind: boolean
  behindCount: string
  recentCommits: string[]
}

function GitSyncTab() {
  const qc = useQueryClient()
  const [subTab, setSubTab] = useState<'repos' | 'credentials'>('repos')
  const [repoModal, setRepoModal] = useState<GitRepo | null | 'new'>(null)
  const [credModal, setCredModal] = useState<GitCredential | null | 'new'>(null)
  const [testCred, setTestCred] = useState<GitCredential | null>(null)
  const [testUrl, setTestUrl] = useState('')
  const [testResult, setTestResult] = useState<{ success: boolean; message: string } | null>(null)
  const [pendingOp, setPendingOp] = useState<Record<number, string>>({})
  const [repoStatuses, setRepoStatuses] = useState<Record<number, RepoStatus>>({})
  const [exportData, setExportData] = useState<{ repoId: number; repoName: string; yaml: string } | null>(null)

  const reposQ = useQuery({
    queryKey: ['git-sync', 'repos'],
    queryFn: ({ signal }) => api.get<{ success: boolean; repos: GitRepo[] }>('/api/git-sync/repos', signal),
    refetchInterval: 30_000,
  })

  const credsQ = useQuery({
    queryKey: ['git-sync', 'credentials'],
    queryFn: ({ signal }) => api.get<{ success: boolean; credentials: GitCredential[] }>('/api/git-sync/credentials', signal),
  })

  const repos = reposQ.data?.repos ?? []
  const credentials = credsQ.data?.credentials ?? []

  async function checkStatus(id: number) {
    setRepoStatuses(prev => ({ ...prev, [id]: { ...(prev[id] ?? { behind: false, behindCount: '', recentCommits: [] }), checking: true } }))
    try {
      const res = await api.get<{
        success: boolean; cloned: boolean
        behind_count: string; behind: boolean; recent_commits: string[]
      }>(`/api/git-sync/repos/status?id=${id}`)
      if (res.success && res.cloned) {
        setRepoStatuses(prev => ({ ...prev, [id]: { checking: false, behind: res.behind, behindCount: res.behind_count, recentCommits: res.recent_commits } }))
      } else {
        setRepoStatuses(prev => ({ ...prev, [id]: { checking: false, behind: false, behindCount: '', recentCommits: [] } }))
        if (res.success && !res.cloned) toast.error('Not cloned yet - pull first')
      }
    } catch (e: any) {
      setRepoStatuses(prev => ({ ...prev, [id]: { checking: false, behind: false, behindCount: '', recentCommits: [] } }))
      toast.error(e?.message ?? 'Status check failed')
    }
  }

  async function repoAction(id: number, action: 'pull' | 'push' | 'deploy' | 'delete' | 'export') {
    setPendingOp(prev => ({ ...prev, [id]: action }))
    try {
      if (action === 'delete') {
        await api.post(`/api/git-sync/repos/delete?id=${id}`, {})
        toast.success('Repository removed')
        qc.invalidateQueries({ queryKey: ['git-sync', 'repos'] })
        return
      }
      if (action === 'export') {
        const res = await api.post<{ success: boolean; yaml?: string; error?: string }>(
          `/api/git-sync/repos/export?id=${id}`, {})
        if (!res.success) { toast.error(res.error ?? 'Export failed'); return }
        const repoName = repos.find(r => r.id === id)?.name ?? String(id)
        setExportData({ repoId: id, repoName, yaml: res.yaml ?? '' })
        return
      }
      const res = await api.post<{ success: boolean; error?: string; commit?: string }>(
        `/api/git-sync/repos/${action}?id=${id}`, {})
      if (!res.success) {
        toast.error(res.error ?? `${action} failed`)
      } else {
        const msg = action === 'pull' ? `Pulled${res.commit ? ` (${res.commit.slice(0, 7)})` : ''}`
                  : action === 'push' ? `Pushed${res.commit ? ` (${res.commit.slice(0, 7)})` : ''}`
                  : 'Deployed'
        toast.success(msg)
        qc.invalidateQueries({ queryKey: ['git-sync', 'repos'] })
        if (action === 'pull') {
          setRepoStatuses(prev => ({ ...prev, [id]: { checking: false, behind: false, behindCount: '0', recentCommits: prev[id]?.recentCommits ?? [] } }))
        }
        if (action === 'deploy') {
          qc.invalidateQueries({ queryKey: ['docker', 'containers'] })
          qc.invalidateQueries({ queryKey: ['docker', 'stacks'] })
        }
      }
    } catch (e: any) { toast.error(e?.message ?? `${action} failed`) }
    finally { setPendingOp(prev => { const n = { ...prev }; delete n[id]; return n }) }
  }

  async function deleteCredential(id: number) {
    try {
      await api.post(`/api/git-sync/credentials/delete?id=${id}`, {})
      toast.success('Credential removed')
      qc.invalidateQueries({ queryKey: ['git-sync', 'credentials'] })
    } catch (e: any) { toast.error(e?.message ?? 'Delete failed') }
  }

  async function runTest() {
    if (!testCred || !testUrl.trim()) return
    try {
      const res = await api.post<{ success: boolean; message?: string; error?: string }>(
        '/api/git-sync/credentials/test', { credential_id: testCred.id, repo_url: testUrl })
      setTestResult({ success: res.success, message: res.success ? (res.message ?? 'OK') : (res.error ?? 'Failed') })
    } catch (e: any) { setTestResult({ success: false, message: e?.message ?? 'Error' }) }
  }

  const chipStyle = (active: boolean): React.CSSProperties => ({
    padding: '5px 14px', borderRadius: 20, border: 'none', cursor: 'pointer', fontSize: 'var(--text-sm)',
    fontWeight: active ? 600 : 400,
    background: active ? 'var(--primary-bg)' : 'transparent',
    color: active ? 'var(--primary)' : 'var(--text-secondary)',
  })

  return (
    <div>
      {/* Sub-tabs */}
      <div style={{ display: 'flex', gap: 4, marginBottom: 20, borderBottom: '1px solid var(--border)', paddingBottom: 0 }}>
        <button style={chipStyle(subTab === 'repos')} onClick={() => setSubTab('repos')}>
          <Icon name="sync" size={14} />Repositories
        </button>
        <button style={chipStyle(subTab === 'credentials')} onClick={() => setSubTab('credentials')}>
          <Icon name="key" size={14} />Credentials
        </button>
      </div>

      {/* ── Repositories ── */}
      {subTab === 'repos' && (
        <div>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
            <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-sm)', margin: 0 }}>
              Git repos polled on a schedule and auto-deployed when the compose file changes.
            </p>
            <button onClick={() => setRepoModal('new')} className="btn btn-primary" style={{ flexShrink: 0 }}>
              <Icon name="add" size={15} />Add Repository
            </button>
          </div>

          {reposQ.isLoading && <Skeleton style={{ height: 80, marginBottom: 12 }} />}
          {!reposQ.isLoading && repos.length === 0 && (
            <div style={{ textAlign: 'center', padding: '48px 20px', color: 'var(--text-tertiary)' }}>
              <Icon name="source" size={40} />
              <p style={{ marginTop: 12 }}>No repositories configured.<br />Add one to start syncing compose stacks from Git.</p>
            </div>
          )}

          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {repos.map(repo => {
              const op = pendingOp[repo.id]
              const hasError = !!repo.last_error
              const status = repoStatuses[repo.id]
              return (
                <div key={repo.id} style={{ background: 'var(--surface)', border: `1px solid ${status?.behind ? 'var(--warning)' : 'var(--border)'}`, borderRadius: 'var(--radius)', padding: '16px 20px', transition: 'border-color 0.2s' }}>
                  <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12 }}>
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                        <span style={{ fontWeight: 600, fontSize: 'var(--text-base)' }}>{repo.name}</span>
                        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', background: 'var(--surface-2)', padding: '2px 6px', borderRadius: 4 }}>
                          {repo.branch}
                        </span>
                        {repo.auto_sync && (
                          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--primary)', background: 'var(--primary-bg)', padding: '2px 8px', borderRadius: 10 }}>
                            <Icon name="schedule" size={11} />auto {repo.sync_interval}m
                          </span>
                        )}
                        {status?.behind && (
                          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--warning)', background: 'color-mix(in srgb, var(--warning) 12%, transparent)', padding: '2px 8px', borderRadius: 10 }}>
                            <Icon name="arrow_downward" size={11} />{status.behindCount} behind
                          </span>
                        )}
                        {status && !status.behind && !status.checking && status.behindCount !== '' && (
                          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--success)', background: 'color-mix(in srgb, var(--success) 10%, transparent)', padding: '2px 8px', borderRadius: 10 }}>
                            <Icon name="check" size={11} />Up to date
                          </span>
                        )}
                        {hasError && (
                          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--error)', background: 'color-mix(in srgb, var(--error) 12%, transparent)', padding: '2px 8px', borderRadius: 10 }}>
                            <Icon name="error" size={11} />Error
                          </span>
                        )}
                      </div>
                      <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 4, display: 'flex', gap: 14, flexWrap: 'wrap' }}>
                        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', maxWidth: 340 }}>{repo.repo_url}</span>
                        {repo.cred_name && <span><Icon name="lock" size={11} />{repo.cred_name}</span>}
                        {repo.last_commit && <span style={{ fontFamily: 'var(--font-mono)' }}>{repo.last_commit.slice(0, 12)}</span>}
                        <span>Synced {relativeTime(repo.last_sync_at)}</span>
                      </div>
                      {status?.recentCommits && status.recentCommits.length > 0 && (
                        <div style={{ marginTop: 6, fontSize: 11, fontFamily: 'var(--font-mono)', color: 'var(--text-tertiary)', lineHeight: 1.6 }}>
                          {status.recentCommits.map((c, i) => <div key={i}>{c}</div>)}
                        </div>
                      )}
                      {hasError && (
                        <div style={{ marginTop: 6, fontSize: 'var(--text-xs)', color: 'var(--error)', fontFamily: 'var(--font-mono)', background: 'color-mix(in srgb, var(--error) 8%, transparent)', padding: '4px 8px', borderRadius: 4, maxHeight: 60, overflow: 'auto' }}>
                          {repo.last_error}
                        </div>
                      )}
                    </div>
                    <div style={{ display: 'flex', gap: 6, flexShrink: 0, alignItems: 'center', flexWrap: 'wrap', justifyContent: 'flex-end' }}>
                      <Tooltip content="Fetch remote and check behind count">
                        <button onClick={() => checkStatus(repo.id)} disabled={!!op || status?.checking} className="btn btn-ghost" style={{ padding: '6px 10px', fontSize: 'var(--text-xs)' }}>
                          {status?.checking ? '...' : <><Icon name="update" size={14} />Check</>}
                        </button>
                      </Tooltip>
                      <Tooltip content="Pull latest from remote">
                        <button onClick={() => repoAction(repo.id, 'pull')} disabled={!!op} className="btn btn-ghost" style={{ padding: '6px 10px', fontSize: 'var(--text-xs)' }}>
                          {op === 'pull' ? '...' : <><Icon name="download" size={14} />Pull</>}
                        </button>
                      </Tooltip>
                      <Tooltip content="Commit and push changes">
                        <button onClick={() => repoAction(repo.id, 'push')} disabled={!!op} className="btn btn-ghost" style={{ padding: '6px 10px', fontSize: 'var(--text-xs)' }}>
                          {op === 'push' ? '...' : <><Icon name="upload" size={14} />Push</>}
                        </button>
                      </Tooltip>
                      <Tooltip content="Export running containers as compose YAML into this repo">
                        <button onClick={() => repoAction(repo.id, 'export')} disabled={!!op} className="btn btn-ghost" style={{ padding: '6px 10px', fontSize: 'var(--text-xs)' }}>
                          {op === 'export' ? '...' : <><Icon name="ios_share" size={14} />Export</>}
                        </button>
                      </Tooltip>
                      <Tooltip content="docker compose up -d">
                        <button onClick={() => repoAction(repo.id, 'deploy')} disabled={!!op} className="btn btn-ghost" style={{ padding: '6px 10px', fontSize: 'var(--text-xs)', color: 'var(--primary)' }}>
                          {op === 'deploy' ? '...' : <><Icon name="play_arrow" size={14} />Deploy</>}
                        </button>
                      </Tooltip>
                      <button onClick={() => setRepoModal(repo)} disabled={!!op} className="btn btn-ghost" style={{ padding: '6px 8px' }}>
                        <Icon name="edit" size={14} />
                      </button>
                      <button onClick={() => repoAction(repo.id, 'delete')} disabled={!!op} className="btn btn-ghost" style={{ padding: '6px 8px', color: 'var(--error)' }}>
                        <Icon name="delete" size={14} />
                      </button>
                    </div>
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      )}

      {/* ── Credentials ── */}
      {subTab === 'credentials' && (
        <div>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
            <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-sm)', margin: 0 }}>
              Stored credentials for authenticating to private Git repositories.
            </p>
            <button onClick={() => setCredModal('new')} className="btn btn-primary" style={{ flexShrink: 0 }}>
              <Icon name="add" size={15} />Add Credential
            </button>
          </div>

          {credsQ.isLoading && <Skeleton style={{ height: 60, marginBottom: 12 }} />}
          {!credsQ.isLoading && credentials.length === 0 && (
            <div style={{ textAlign: 'center', padding: '48px 20px', color: 'var(--text-tertiary)' }}>
              <Icon name="key" size={40} />
              <p style={{ marginTop: 12 }}>No credentials stored.<br />Add a PAT or SSH key to access private repositories.</p>
            </div>
          )}

          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {credentials.map(cred => (
              <div key={cred.id} style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius)', padding: '14px 18px', display: 'flex', alignItems: 'center', gap: 12 }}>
                <Icon name={cred.auth_type === 'token' ? 'key' : 'terminal'} size={20} style={{ color: 'var(--primary)', flexShrink: 0 }} />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>{cred.name}</div>
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                    {cred.host} · {cred.auth_type === 'token' ? 'Personal Access Token' : 'SSH Key'}
                    {cred.notes && ` · ${cred.notes}`}
                  </div>
                </div>
                <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
                  <button onClick={() => { setTestCred(cred); setTestUrl(''); setTestResult(null) }} className="btn btn-ghost" style={{ padding: '5px 10px', fontSize: 'var(--text-xs)' }}>
                    <Icon name="network_check" size={13} />Test
                  </button>
                  <button onClick={() => setCredModal(cred)} className="btn btn-ghost" style={{ padding: '5px 8px' }}>
                    <Icon name="edit" size={13} />
                  </button>
                  <button onClick={() => deleteCredential(cred.id)} className="btn btn-ghost" style={{ padding: '5px 8px', color: 'var(--error)' }}>
                    <Icon name="delete" size={13} />
                  </button>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {/* Modals */}
      {repoModal != null && (
        <RepoModal
          initial={repoModal === 'new' ? null : repoModal}
          credentials={credentials}
          onSave={() => { setRepoModal(null); qc.invalidateQueries({ queryKey: ['git-sync', 'repos'] }) }}
          onClose={() => setRepoModal(null)}
        />
      )}
      {credModal != null && (
        <CredentialModal
          initial={credModal === 'new' ? null : credModal}
          onSave={() => { setCredModal(null); qc.invalidateQueries({ queryKey: ['git-sync', 'credentials'] }) }}
          onClose={() => setCredModal(null)}
        />
      )}
      {exportData && (
        <ExportModal
          repoId={exportData.repoId}
          repoName={exportData.repoName}
          yaml={exportData.yaml}
          onClose={() => { setExportData(null); qc.invalidateQueries({ queryKey: ['git-sync', 'repos'] }) }}
        />
      )}

      {/* Credential test modal */}
      {testCred && (
        <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 200 }}>
          <div style={{ background: 'var(--surface)', borderRadius: 'var(--radius)', padding: 24, width: 440, boxShadow: 'var(--shadow-lg)', border: '1px solid var(--border)' }}>
            <h3 style={{ margin: '0 0 16px', fontSize: 'var(--text-base)' }}>Test: {testCred.name}</h3>
            <div style={{ marginBottom: 12 }}>
              <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'block', marginBottom: 4 }}>Repository URL to test against</label>
              <input value={testUrl} onChange={e => { setTestUrl(e.target.value); setTestResult(null) }}
                style={{ width: '100%', padding: '8px 10px', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', background: 'var(--surface-2)', color: 'var(--text)', fontSize: 'var(--text-sm)', boxSizing: 'border-box' }}
                placeholder="https://github.com/user/repo.git" />
            </div>
            {testResult && (
              <div style={{ padding: '8px 12px', borderRadius: 'var(--radius-sm)', marginBottom: 12, background: testResult.success ? 'color-mix(in srgb, var(--success) 12%, transparent)' : 'color-mix(in srgb, var(--error) 12%, transparent)', color: testResult.success ? 'var(--success)' : 'var(--error)', fontSize: 'var(--text-sm)' }}>
                <Icon name={testResult.success ? 'check_circle' : 'error'} size={14} />{testResult.message}
              </div>
            )}
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 8 }}>
              <button onClick={() => setTestCred(null)} className="btn btn-ghost">Close</button>
              <button onClick={runTest} disabled={!testUrl.trim()} className="btn btn-primary">
                <Icon name="network_check" size={14} />Test Connection
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// DockerPage
// ---------------------------------------------------------------------------

type Tab = 'containers' | 'pull' | 'compose' | 'gpu' | 'gitsync'

export function DockerPage() {
  const [tab, setTab] = useState<Tab>('containers')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'containers', label: 'Containers', icon: 'deployed_code' },
    { id: 'pull', label: 'Pull Image', icon: 'download' },
    { id: 'compose', label: 'Compose', icon: 'folder' },
    { id: 'gitsync', label: 'Git Sync', icon: 'source' },
    { id: 'gpu', label: 'GPU / hardware', icon: 'memory' },
  ]

  return (
    <div style={{ maxWidth: 1200 }}>
      <div className="page-header">
        <h1 className="page-title">Docker</h1>
        <p className="page-subtitle">Containers · Images · Compose Stacks · GPU</p>
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
      {tab === 'compose'    && <ComposeManager />}
      {tab === 'gitsync'    && <GitSyncTab />}
      {tab === 'gpu'        && <GPUTab />}
    </div>
  )
}

