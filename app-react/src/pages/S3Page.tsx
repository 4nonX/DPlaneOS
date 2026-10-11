/**
 * pages/S3Page.tsx - S3 Object Storage
 *
 * Serves a folder of a pool over the S3 protocol (Versity Gateway): buckets
 * are its directories, objects are plain files in them.
 *
 * API:
 *   GET  /api/s3/status   - { installed, active, api_port }
 *   GET  /api/s3/config   - { config: { root_user, root_password(redacted), volume_path, api_port } }
 *   PUT  /api/s3/config   - update config and apply
 *   POST /api/s3/start    - start service
 *   POST /api/s3/stop     - stop service
 *   POST /api/s3/restart  - restart service
 */

import type React from 'react'
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton, Spinner } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface S3Status {
  success: boolean
  installed: boolean
  active: boolean
  api_port: number
}

interface S3ConfigResponse {
  success: boolean
  config: {
    root_user: string
    root_password: string
    volume_path: string
    api_port: number
  }
}

// ---------------------------------------------------------------------------
// Service status badge
// ---------------------------------------------------------------------------

function ServiceBadge({ installed, active }: { installed: boolean; active: boolean }) {
  if (!installed) {
    return (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-sm)', fontWeight: 600, color: 'var(--text-tertiary)' }}>
        <span className="status-dot" style={{ background: 'var(--text-tertiary)' }} />
        Not installed
      </span>
    )
  }
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-sm)', fontWeight: 600, color: active ? 'var(--success)' : 'var(--text-secondary)' }}>
      <span className={`status-dot ${active ? 'online' : 'offline'}`} />
      {active ? 'Running' : 'Stopped'}
    </span>
  )
}

// ---------------------------------------------------------------------------
// S3Page
// ---------------------------------------------------------------------------

export function S3Page() {
  const qc = useQueryClient()
  const [dirtyForm, setDirtyForm] = useState<{
    root_user?: string
    root_password?: string
    volume_path?: string
    api_port?: number
  }>({})
  const [showPassword, setShowPassword] = useState(false)

  const statusQ = useQuery<S3Status>({
    queryKey: ['s3', 'status'],
    queryFn: () => api.get<S3Status>('/api/s3/status'),
    refetchInterval: 10_000,
  })

  const configQ = useQuery<S3ConfigResponse>({
    queryKey: ['s3', 'config'],
    queryFn: () => api.get<S3ConfigResponse>('/api/s3/config'),
  })

  // Effective form: user edits OR server value (no seeding effect needed)
  const sc = configQ.data?.config
  const form = {
    root_user:     dirtyForm.root_user     ?? sc?.root_user    ?? '',
    root_password: dirtyForm.root_password ?? '',
    volume_path:   dirtyForm.volume_path   ?? sc?.volume_path  ?? '',
    api_port:      dirtyForm.api_port      ?? sc?.api_port     ?? 9000,
  }
  const dirty = Object.keys(dirtyForm).length > 0

  const saveMut = useMutation({
    mutationFn: () => api.put('/api/s3/config', form),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['s3', 'status'] })
      qc.invalidateQueries({ queryKey: ['s3', 'config'] })
      toast.success('Configuration saved and applied')
      setDirtyForm({})
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const startMut = useMutation({
    mutationFn: () => api.post('/api/s3/start', {}),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['s3', 'status'] }); toast.success('S3 service started') },
    onError: (e: Error) => toast.error(e.message),
  })
  const stopMut = useMutation({
    mutationFn: () => api.post('/api/s3/stop', {}),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['s3', 'status'] }); toast.success('S3 service stopped') },
    onError: (e: Error) => toast.error(e.message),
  })
  const restartMut = useMutation({
    mutationFn: () => api.post('/api/s3/restart', {}),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['s3', 'status'] }); toast.success('S3 service restarted') },
    onError: (e: Error) => toast.error(e.message),
  })

  const status    = statusQ.data
  const isActive  = status?.active ?? false
  const anyPending = startMut.isPending || stopMut.isPending || restartMut.isPending || saveMut.isPending

  function field<K extends keyof typeof form>(key: K, value: (typeof form)[K]) {
    setDirtyForm(prev => ({ ...prev, [key]: value }))
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    saveMut.mutate()
  }

  return (
    <div className="page-container">
      <header className="page-header">
        <div>
          <h1 className="page-title">S3 Object Storage</h1>
          <p className="page-subtitle">Serve a folder of a pool over the S3 protocol. Buckets are its directories; objects are ordinary files in them.</p>
        </div>
      </header>

      {/* Status card */}
      <div className="card" style={{ padding: 20, marginBottom: 24 }}>
        {statusQ.isLoading ? (
          <Skeleton style={{ height: 40 }} />
        ) : (
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 12 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 20 }}>
              <ServiceBadge installed={status?.installed ?? false} active={isActive} />
              {isActive && (
                <>
                  <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
                    API port <span style={{ fontFamily: 'var(--font-mono)', fontWeight: 600 }}>{status?.api_port}</span>
                  </span>
                </>
              )}
            </div>
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
              {isActive ? (
                <>
                  <button className="btn btn-ghost" onClick={() => restartMut.mutate()} disabled={anyPending}>
                    {restartMut.isPending ? <Spinner size={14} /> : <Icon name="restart_alt" size={15} />}
                    Restart
                  </button>
                  <button className="btn btn-danger" onClick={() => stopMut.mutate()} disabled={anyPending}>
                    {stopMut.isPending ? <Spinner size={14} /> : <Icon name="stop" size={15} />}
                    Stop
                  </button>
                </>
              ) : (
                <button className="btn btn-primary" onClick={() => startMut.mutate()}
                  disabled={anyPending || !status?.installed}>
                  {startMut.isPending ? <Spinner size={14} color="rgba(0,0,0,0.7)" /> : <Icon name="play_arrow" size={15} />}
                  Start
                </button>
              )}
            </div>
          </div>
        )}
        {!status?.installed && !statusQ.isLoading && (
          <div style={{ marginTop: 12, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
The S3 server is not part of this system (it was built without <code>services.dplaneos.s3.enable</code>).
          </div>
        )}
        {isActive && (
          <div style={{ marginTop: 12, display: 'flex', gap: 16, flexWrap: 'wrap' }}>
            <EndpointLink label="S3 API" port={status?.api_port} />
          </div>
        )}
      </div>

      {/* Config form */}
      {configQ.isLoading ? (
        <div className="card" style={{ padding: 24 }}>
          {[0, 1, 2, 3].map(i => <Skeleton key={i} style={{ height: 40, marginBottom: 14 }} />)}
        </div>
      ) : form ? (
        <div className="card" style={{ padding: 24 }}>
          <div style={{ fontWeight: 700, fontSize: 'var(--text-sm)', marginBottom: 20, display: 'flex', alignItems: 'center', gap: 8 }}>
            <Icon name="settings" size={16} style={{ color: 'var(--text-tertiary)' }} />
            Configuration
          </div>
          <form onSubmit={handleSubmit}>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
              <div>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Access key</label>
                <input className="input" value={form.root_user} onChange={e => field('root_user', e.target.value)}
                  placeholder="s3admin" disabled={saveMut.isPending} />
              </div>
              <div>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Secret key</label>
                <div style={{ position: 'relative' }}>
                  <input className="input" type={showPassword ? 'text' : 'password'}
                    value={form.root_password}
                    onChange={e => field('root_password', e.target.value)}
                    placeholder={configQ.data?.config?.root_password ? 'Leave blank to keep current' : 'Min 8 characters'}
                    disabled={saveMut.isPending}
                    style={{ paddingRight: 36 }} />
                  <button type="button" onClick={() => setShowPassword(p => !p)}
                    style={{ position: 'absolute', right: 8, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', display: 'flex' }}>
                    <Icon name={showPassword ? 'visibility_off' : 'visibility'} size={16} />
                  </button>
                </div>
              </div>
              <div style={{ gridColumn: '1 / -1' }}>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Folder</label>
                <input className="input" value={form.volume_path} onChange={e => field('volume_path', e.target.value)}
                  placeholder="/mnt/tank/s3" disabled={saveMut.isPending} />
                <p style={{ marginTop: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                  A dataset's mount point or a folder below it. Each bucket is a directory in it, each object a file: snapshots,
                  replication and the file browser see them like any other data.
                </p>
              </div>
              <div>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>API port (S3 endpoint)</label>
                <input className="input" type="number" min={1} max={65535} value={form.api_port}
                  onChange={e => field('api_port', Number(e.target.value))} disabled={saveMut.isPending} />
              </div>
            </div>

            <div style={{ marginTop: 20, padding: '12px 16px', background: 'rgba(255,255,255,0.03)', borderRadius: 'var(--radius-md)', border: '1px solid var(--border)' }}>
              <div style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-tertiary)', marginBottom: 8, textTransform: 'uppercase', letterSpacing: '0.05em' }}>S3 Endpoint</div>
              <code style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--text-secondary)' }}>
                http://&lt;server-ip&gt;:{form.api_port}
              </code>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>
                Use this as your S3-compatible endpoint in rclone, AWS CLI, or any S3 SDK with <code>path-style = true</code>.
              </div>
            </div>

            <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 20 }}>
              <button type="submit" className="btn btn-primary" disabled={saveMut.isPending || !dirty}>
                {saveMut.isPending
                  ? <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Spinner size={14} color="rgba(0,0,0,0.7)" /> Saving…</span>
                  : 'Save & Apply'
                }
              </button>
            </div>
          </form>
        </div>
      ) : null}
    </div>
  )
}

function EndpointLink({ label, port }: { label: string; port?: number }) {
  if (!port) return null
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
      <Icon name="open_in_new" size={13} />
      <span style={{ fontWeight: 600 }}>{label}:</span>
      <code style={{ fontFamily: 'var(--font-mono)' }}>:{port}</code>
    </div>
  )
}
