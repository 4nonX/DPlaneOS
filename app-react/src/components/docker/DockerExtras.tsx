/**
 * Docker images, live stats and safe container updates.
 *
 *   GET    /api/docker/images             → { images: Image[] }
 *   DELETE /api/docker/images/{id}[?force=true]   (X-Confirm-Token: docker_rmi/{id})
 *   GET    /api/docker/stats              → { containers: Stat[] }
 *   GET    /api/docker/preflight          → { all_pass, checks: [{check, pass, error?, driver?, pools?}] }
 *   POST   /api/docker/convert-run {command} → { yaml, name }
 *   POST   /api/docker/update {container_name, image?, zfs_dataset?, skip_snapshot, health_check_seconds?}
 *          → { job_id } ; job result { success, steps[], error?, snapshot?, rollback? }
 */
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, apiFetch } from '@/lib/api'
import { issueConfirmToken } from '@/lib/confirm'
import { useJob } from '@/hooks/useJob'
import { Modal } from '@/components/ui/Modal'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'
import { fmtDateTime } from '@/lib/fmt'

interface DockerImage { id: string; repo_tags: string[] | null; size: number; created: number }
interface ContainerStat { name: string; cpu: string; memory: string; mem_perc: string; net_io: string; block_io: string; pids: string }
interface UpdateStep { step: string; success: boolean; detail?: string }

function fmtBytes(b: number): string {
  if (!b) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(b) / Math.log(1024)), u.length - 1)
  return `${(b / 1024 ** i).toFixed(i ? 1 : 0)} ${u[i]}`
}

// ---------------------------------------------------------------------------
// Images
// ---------------------------------------------------------------------------

export function ImagesTab() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [filter, setFilter] = useState('')
  const imagesQ = useQuery({
    queryKey: ['docker', 'images'],
    queryFn: ({ signal }) => api.get<{ success: boolean; error?: string; images: DockerImage[] }>('/api/docker/images', signal),
  })
  const remove = useMutation({
    mutationFn: async ({ id, force }: { id: string; force: boolean }) => {
      const token = await issueConfirmToken('docker_rmi', id)
      const r = await apiFetch<{ success: boolean; error?: string }>(
        `/api/docker/images/${encodeURIComponent(id)}${force ? '?force=true' : ''}`, { method: 'DELETE', headers: { 'X-Confirm-Token': token } })
      if (!r.success) throw new Error(r.error ?? 'Remove failed')
    },
    onSuccess: () => { toast.success('Image removed'); qc.invalidateQueries({ queryKey: ['docker', 'images'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  async function removeImage(img: DockerImage) {
    const label = img.repo_tags?.[0] ?? img.id.replace('sha256:', '').slice(0, 12)
    if (await confirm({ title: `Remove ${label}?`, message: 'The image is deleted from this machine; it can be pulled again. Images used by a container are refused.', confirmLabel: 'Remove', danger: true }))
      remove.mutate({ id: img.id, force: false })
  }

  if (imagesQ.isLoading) return <Skeleton height={200} />
  if (imagesQ.isError) return <ErrorState error={imagesQ.error} onRetry={() => imagesQ.refetch()} />
  if (imagesQ.data && !imagesQ.data.success) return <ErrorState error={new Error(imagesQ.data.error ?? 'Docker unavailable')} onRetry={() => imagesQ.refetch()} />

  const images = (imagesQ.data?.images ?? [])
    .filter(i => !filter || (i.repo_tags ?? []).some(t => t.toLowerCase().includes(filter.toLowerCase())) || i.id.includes(filter))
    .sort((a, b) => b.created - a.created)
  const total = (imagesQ.data?.images ?? []).reduce((n, i) => n + i.size, 0)

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 16 }}>
      <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginBottom: 12, flexWrap: 'wrap' }}>
        <input className="input" placeholder="Filter images…" value={filter} onChange={e => setFilter(e.target.value)} style={{ maxWidth: 280 }} />
        <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>{imagesQ.data?.images.length ?? 0} images · {fmtBytes(total)}</span>
        <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} onClick={() => imagesQ.refetch()}><Icon name="refresh" size={14} />Refresh</button>
      </div>
      <div style={{ overflowX: 'auto' }}>
        <table className="table" style={{ width: '100%' }}>
          <thead><tr><th>Repository:tag</th><th>ID</th><th>Size</th><th>Created</th><th /></tr></thead>
          <tbody>
            {images.map(img => (
              <tr key={img.id}>
                <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>
                  {(img.repo_tags ?? []).filter(t => t !== '<none>:<none>').join(', ') || <span style={{ color: 'var(--text-tertiary)' }}>&lt;untagged&gt;</span>}
                </td>
                <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{img.id.replace('sha256:', '').slice(0, 12)}</td>
                <td>{fmtBytes(img.size)}</td>
                <td style={{ fontSize: 'var(--text-xs)' }}>{img.created ? fmtDateTime(img.created * 1000) : ''}</td>
                <td style={{ textAlign: 'right' }}>
                  <button className="btn btn-xs btn-ghost" style={{ color: 'var(--error)' }} disabled={remove.isPending} onClick={() => removeImage(img)}>
                    <Icon name="delete" size={14} />Remove
                  </button>
                </td>
              </tr>
            ))}
            {images.length === 0 && <tr><td colSpan={5} style={{ textAlign: 'center', color: 'var(--text-tertiary)' }}>No images.</td></tr>}
          </tbody>
        </table>
      </div>
      <ConfirmDialog />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Live stats
// ---------------------------------------------------------------------------

const PREFLIGHT_LABELS: Record<string, string> = {
  docker_running: 'Docker daemon running',
  storage_driver_zfs: 'Docker storage driver is zfs (informational: overlay2 on ZFS 2.2+ works too)',
  zfs_pools: 'ZFS pools imported and healthy',
  no_stale_locks: 'No stale storage operation locks',
}

function PreflightCard() {
  const q = useQuery({
    queryKey: ['docker', 'preflight'],
    queryFn: ({ signal }) => api.get<{ success: boolean; all_pass: boolean; checks: Array<{ check: string; pass: boolean; error?: string; driver?: string }> }>('/api/docker/preflight', signal),
  })
  if (!q.data) return null
  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 16, marginBottom: 16 }}>
      <div style={{ fontWeight: 600, marginBottom: 8 }}>Pre-flight checks</div>
      {q.data.checks.map(c => {
        const info = c.check === 'storage_driver_zfs'
        return (
          <div key={c.check} style={{ display: 'flex', gap: 8, alignItems: 'center', fontSize: 'var(--text-sm)', padding: '2px 0' }}>
            <Icon name={c.pass ? 'check_circle' : info ? 'info' : 'error'} size={15}
              style={{ color: c.pass ? 'var(--success)' : info ? 'var(--text-tertiary)' : 'var(--error)' }} />
            <span>{PREFLIGHT_LABELS[c.check] ?? c.check}{c.driver ? ` (now: ${c.driver})` : ''}{c.error ? `: ${c.error}` : ''}</span>
          </div>
        )
      })}
    </div>
  )
}

export function StatsTab() {
  return <><PreflightCard /><LiveStats /></>
}

function LiveStats() {
  const statsQ = useQuery({
    queryKey: ['docker', 'stats'],
    queryFn: ({ signal }) => api.get<{ success: boolean; error?: string; containers: ContainerStat[] }>('/api/docker/stats', signal),
    refetchInterval: 5_000,
  })
  if (statsQ.isLoading) return <Skeleton height={200} />
  if (statsQ.isError) return <ErrorState error={statsQ.error} onRetry={() => statsQ.refetch()} />
  const rows = [...(statsQ.data?.containers ?? [])].sort((a, b) => parseFloat(b.cpu) - parseFloat(a.cpu))
  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 16 }}>
      {statsQ.data?.error && <div style={{ color: 'var(--warning)', marginBottom: 10 }}>{statsQ.data.error}</div>}
      <div style={{ overflowX: 'auto' }}>
        <table className="table" style={{ width: '100%' }}>
          <thead><tr><th>Container</th><th>CPU</th><th>Memory</th><th>Mem %</th><th>Network I/O</th><th>Disk I/O</th><th>PIDs</th></tr></thead>
          <tbody>
            {rows.map(s => (
              <tr key={s.name}>
                <td style={{ fontWeight: 600 }}>{s.name}</td>
                <td style={{ fontFamily: 'var(--font-mono)' }}>{s.cpu}</td>
                <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{s.memory}</td>
                <td style={{ fontFamily: 'var(--font-mono)' }}>{s.mem_perc}</td>
                <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{s.net_io}</td>
                <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{s.block_io}</td>
                <td style={{ fontFamily: 'var(--font-mono)' }}>{s.pids}</td>
              </tr>
            ))}
            {rows.length === 0 && <tr><td colSpan={7} style={{ textAlign: 'center', color: 'var(--text-tertiary)' }}>No running containers.</td></tr>}
          </tbody>
        </table>
      </div>
      <div style={{ marginTop: 8, fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)' }}>Refreshes every 5 seconds.</div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Safe update of a standalone container (stack containers update with the stack)
// ---------------------------------------------------------------------------

export function SafeUpdateModal({ name, image, onClose, onDone }: { name: string; image: string; onClose: () => void; onDone: () => void }) {
  const [img, setImg] = useState(image)
  const [dataset, setDataset] = useState('')
  const [jobId, setJobId] = useState<string | null>(null)
  const datasetsQ = useQuery({
    queryKey: ['zfs', 'datasets', 'names'],
    queryFn: ({ signal }) => api.get<{ success: boolean; data: Array<{ name: string }> }>('/api/zfs/datasets', signal),
  })
  const job = useJob(jobId)
  const start = useMutation({
    mutationFn: async () => {
      const r = await api.post<{ job_id?: string; success?: boolean; error?: string }>('/api/docker/update', {
        container_name: name, image: img.trim(), zfs_dataset: dataset, skip_snapshot: !dataset,
      })
      if (!r.job_id) throw new Error(r.error ?? 'Update could not start')
      return r.job_id
    },
    onSuccess: id => setJobId(id),
    onError: (e: Error) => toast.error(e.message),
  })

  const result = job.data?.result as { success?: boolean; steps?: UpdateStep[]; error?: string; snapshot?: string } | undefined
  const finished = job.data?.status === 'done' || job.data?.status === 'failed'
  const ok = job.data?.status === 'done' && result?.success

  return (
    <Modal title={`Update ${name}`} onClose={() => { if (finished) onDone(); onClose() }}>
      {!jobId ? (
        <>
          <p style={{ margin: 0, color: 'var(--text-secondary)', fontSize: 'var(--text-sm)' }}>
            Pulls the image, then recreates the container with the same settings. If the new container does not come up healthy,
            the previous one is restored.
          </p>
          <label className="field">
            <span className="field-label">Image</span>
            <input className="input" value={img} onChange={e => setImg(e.target.value)} placeholder="repository:tag" />
          </label>
          <label className="field">
            <span className="field-label">Snapshot this dataset first (optional)</span>
            <select className="input" value={dataset} onChange={e => setDataset(e.target.value)}>
              <option value="">No snapshot</option>
              {(datasetsQ.data?.data ?? []).map(d => <option key={d.name} value={d.name}>{d.name}</option>)}
            </select>
          </label>
          <div style={{ display: 'flex', gap: 10 }}>
            <button className="btn btn-primary" disabled={start.isPending || !img.trim()} onClick={() => start.mutate()}>
              <Icon name="system_update_alt" size={14} />{start.isPending ? 'Starting…' : 'Update'}
            </button>
            <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
          </div>
        </>
      ) : (
        <>
          {!finished && <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}><Icon name="progress_activity" size={16} />Updating… (pulling can take a while)</div>}
          {job.data?.status === 'failed' && <div style={{ color: 'var(--error)' }}>{job.data.error}</div>}
          {result && (
            <>
              <div style={{ color: ok ? 'var(--success)' : 'var(--error)', fontWeight: 600 }}>{ok ? 'Updated.' : result.error}</div>
              <ul style={{ margin: 0, paddingLeft: 18, fontSize: 'var(--text-sm)' }}>
                {(result.steps ?? []).map((s, i) => (
                  <li key={i} style={{ color: s.success ? undefined : 'var(--error)' }}>
                    {s.step}{s.detail ? `: ${s.detail}` : ''}
                  </li>
                ))}
              </ul>
              {result.snapshot && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Snapshot: {result.snapshot}</div>}
            </>
          )}
          {finished && <button className="btn btn-primary" onClick={() => { onDone(); onClose() }}>Close</button>}
        </>
      )}
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// docker run → compose
// ---------------------------------------------------------------------------

export function ConvertRunButton({ onConverted }: { onConverted: (yaml: string, name: string) => void }) {
  const [open, setOpen] = useState(false)
  const [cmd, setCmd] = useState('')
  const convert = useMutation({
    mutationFn: async () => {
      const r = await api.post<{ success: boolean; error?: string; yaml: string; name: string }>('/api/docker/convert-run', { command: cmd })
      if (!r.success) throw new Error(r.error ?? 'Could not convert')
      return r
    },
    onSuccess: r => { onConverted(r.yaml, r.name); setOpen(false); setCmd('') },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <>
      <button className="btn btn-ghost" onClick={() => setOpen(true)}><Icon name="terminal" size={15} />From docker run</button>
      {open && (
        <Modal title="Convert a docker run command" onClose={() => setOpen(false)}>
          <textarea className="input" rows={4} value={cmd} onChange={e => setCmd(e.target.value)} spellCheck={false}
            placeholder="docker run -d --name web -p 8080:80 -v /mnt/tank/web:/usr/share/nginx/html nginx:alpine"
            style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }} />
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Common options are converted; review the result before deploying.</div>
          <div style={{ display: 'flex', gap: 10 }}>
            <button className="btn btn-primary" disabled={!cmd.trim() || convert.isPending} onClick={() => convert.mutate()}>Convert</button>
            <button className="btn btn-ghost" onClick={() => setOpen(false)}>Cancel</button>
          </div>
        </Modal>
      )}
    </>
  )
}
