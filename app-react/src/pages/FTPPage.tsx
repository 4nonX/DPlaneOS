/**
 * pages/FTPPage.tsx - FTP/FTPS Server
 *
 * Manages vsftpd via the daemon API.
 *
 * API:
 *   GET  /api/ftp/status  → { installed, active }
 *   GET  /api/ftp/config  → { config: FTPConfig }
 *   PUT  /api/ftp/config  → save + apply config
 *   POST /api/ftp/start
 *   POST /api/ftp/stop
 *   POST /api/ftp/restart
 *   GET  /api/rbac/users  → system user list for allowed-users selection
 */

import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Spinner, Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface FTPConfig {
  enabled: boolean
  mode: 'ftp' | 'ftps'
  port: number
  passive_min_port: number
  passive_max_port: number
  allow_anonymous: boolean
  allow_local_users: boolean
  write_enable: boolean
  chroot_local_user: boolean
  max_clients: number
  max_per_ip: number
  banner: string
  tls_cert_path: string
  tls_key_path: string
  allowed_users: string[]
}

interface FTPStatusResponse {
  success: boolean
  installed: boolean
  active: boolean
}

interface FTPConfigResponse {
  success: boolean
  config: FTPConfig
  warning?: string
}

interface SystemUser {
  id: number
  username: string
  active: boolean
}

interface UsersResponse {
  users: SystemUser[]
}

// ---------------------------------------------------------------------------
// Status badge
// ---------------------------------------------------------------------------

function ServiceBadge({ active, installed }: { active: boolean; installed: boolean }) {
  if (!installed) {
    return (
      <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)', fontWeight: 600 }}>
        <span className="status-dot offline" />
        Not installed
      </span>
    )
  }
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, color: active ? 'var(--success)' : 'var(--text-tertiary)', fontSize: 'var(--text-sm)', fontWeight: 600 }}>
      <span className={`status-dot ${active ? 'online' : 'offline'}`} />
      {active ? 'Running' : 'Stopped'}
    </span>
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function FTPPage() {
  const qc = useQueryClient()

  const { data: statusData, isLoading: statusLoading, refetch: refetchStatus } = useQuery<FTPStatusResponse>({
    queryKey: ['ftp', 'status'],
    queryFn: () => api.get<FTPStatusResponse>('/api/ftp/status'),
    refetchInterval: 10000,
  })

  const { data: configData, isLoading: configLoading, error: configError, refetch: refetchConfig } = useQuery<FTPConfigResponse>({
    queryKey: ['ftp', 'config'],
    queryFn: () => api.get<FTPConfigResponse>('/api/ftp/config'),
  })

  const { data: usersData } = useQuery<UsersResponse>({
    queryKey: ['rbac', 'users'],
    queryFn: () => api.get<UsersResponse>('/api/rbac/users'),
  })

  const [form, setForm] = useState<FTPConfig | null>(null)

  useEffect(() => {
    if (configData?.config && !form) {
      setForm(configData.config)
    }
  }, [configData, form])

  const saveMut = useMutation({
    mutationFn: (cfg: FTPConfig) => api.put<FTPConfigResponse>('/api/ftp/config', cfg),
    onSuccess: (res) => {
      qc.invalidateQueries({ queryKey: ['ftp', 'status'] })
      qc.invalidateQueries({ queryKey: ['ftp', 'config'] })
      if (res.warning) {
        toast.warning(res.warning)
      } else {
        toast.success('FTP configuration saved and applied')
      }
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const startMut = useMutation({
    mutationFn: () => api.post('/api/ftp/start', {}),
    onSuccess: () => {
      refetchStatus()
      toast.success('FTP server started')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const stopMut = useMutation({
    mutationFn: () => api.post('/api/ftp/stop', {}),
    onSuccess: () => {
      refetchStatus()
      toast.success('FTP server stopped')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const restartMut = useMutation({
    mutationFn: () => api.post('/api/ftp/restart', {}),
    onSuccess: () => {
      refetchStatus()
      toast.success('FTP server restarted')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function set<K extends keyof FTPConfig>(k: K, v: FTPConfig[K]) {
    setForm(f => f ? { ...f, [k]: v } : f)
  }

  function toggleUser(username: string) {
    setForm(f => {
      if (!f) return f
      const has = f.allowed_users.includes(username)
      return {
        ...f,
        allowed_users: has
          ? f.allowed_users.filter(u => u !== username)
          : [...f.allowed_users, username],
      }
    })
  }

  function handleSave(e: React.FormEvent) {
    e.preventDefault()
    if (!form) return
    saveMut.mutate(form)
  }

  const status = statusData
  const isLoading = statusLoading || configLoading
  const serviceUsers = usersData?.users?.filter(u => u.active) ?? []
  const actionPending = startMut.isPending || stopMut.isPending || restartMut.isPending

  if (isLoading) {
    return (
      <div className="page-container">
        <header className="page-header">
          <div>
            <h1 className="page-title">FTP / FTPS</h1>
            <p className="page-subtitle">Manage the vsftpd file transfer server.</p>
          </div>
        </header>
        <div className="card" style={{ padding: 24 }}>
          {[0, 1, 2, 3].map(i => <Skeleton key={i} style={{ height: 36, marginBottom: 12 }} />)}
        </div>
      </div>
    )
  }

  if (configError) {
    return (
      <div className="page-container">
        <header className="page-header">
          <div>
            <h1 className="page-title">FTP / FTPS</h1>
            <p className="page-subtitle">Manage the vsftpd file transfer server.</p>
          </div>
        </header>
        <ErrorState error={configError} title="Failed to load FTP config" onRetry={() => refetchConfig()} />
      </div>
    )
  }

  return (
    <div className="page-container">
      <header className="page-header">
        <div>
          <h1 className="page-title">FTP / FTPS</h1>
          <p className="page-subtitle">Manage the vsftpd file transfer server (FTP and explicit TLS).</p>
        </div>
      </header>

      {/* ── Status card ── */}
      <div className="card" style={{ padding: 20, marginBottom: 24, display: 'flex', alignItems: 'center', justifyContent: 'space-between', flexWrap: 'wrap', gap: 16 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 24 }}>
          <ServiceBadge active={status?.active ?? false} installed={status?.installed ?? false} />
          {!status?.installed && (
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>
              Install vsftpd to enable FTP. On NixOS: add <code style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }}>services.vsftpd.enable = true;</code> to your configuration.
            </span>
          )}
        </div>
        {status?.installed && (
          <div style={{ display: 'flex', gap: 8 }}>
            {status.active ? (
              <>
                <button className="btn btn-ghost btn-sm" onClick={() => restartMut.mutate()} disabled={actionPending}>
                  {restartMut.isPending ? <Spinner size={13} color="var(--text)" /> : <Icon name="restart_alt" size={15} />}
                  {' '}Restart
                </button>
                <button className="btn btn-danger btn-sm" onClick={() => stopMut.mutate()} disabled={actionPending}>
                  {stopMut.isPending ? <Spinner size={13} color="var(--text)" /> : <Icon name="stop" size={15} />}
                  {' '}Stop
                </button>
              </>
            ) : (
              <button className="btn btn-primary btn-sm" onClick={() => startMut.mutate()} disabled={actionPending}>
                {startMut.isPending
                  ? <span style={{ display: 'flex', alignItems: 'center', gap: 6 }}><Spinner size={13} color="rgba(0,0,0,0.7)" /> Starting…</span>
                  : <span style={{ display: 'flex', alignItems: 'center', gap: 6 }}><Icon name="play_arrow" size={15} /> Start</span>
                }
              </button>
            )}
          </div>
        )}
      </div>

      {/* ── Config form ── */}
      {form && (
        <form onSubmit={handleSave}>
          {/* General */}
          <div className="card" style={{ padding: 24, marginBottom: 24 }}>
            <h2 style={{ fontSize: 'var(--text-base)', fontWeight: 700, marginBottom: 20 }}>General Settings</h2>

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 16, marginBottom: 20 }}>
              <div>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Mode</label>
                <select className="input" value={form.mode} onChange={e => set('mode', e.target.value as 'ftp' | 'ftps')}>
                  <option value="ftp">Plain FTP (unencrypted)</option>
                  <option value="ftps">FTPS (explicit TLS)</option>
                </select>
              </div>
              <div>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Port</label>
                <input className="input" type="number" min={1} max={65535}
                  value={form.port} onChange={e => set('port', Number(e.target.value))} />
              </div>
              <div>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Banner</label>
                <input className="input" type="text" maxLength={128}
                  value={form.banner} onChange={e => set('banner', e.target.value)} />
              </div>
            </div>

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 16, marginBottom: 20 }}>
              <div>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Passive port min</label>
                <input className="input" type="number" min={1024} max={65534}
                  value={form.passive_min_port} onChange={e => set('passive_min_port', Number(e.target.value))} />
              </div>
              <div>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Passive port max</label>
                <input className="input" type="number" min={1025} max={65535}
                  value={form.passive_max_port} onChange={e => set('passive_max_port', Number(e.target.value))} />
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                <label style={{ fontSize: 'var(--text-sm)', fontWeight: 500 }}>Max clients / per IP</label>
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
                  <input className="input" type="number" min={0} placeholder="Max clients"
                    value={form.max_clients} onChange={e => set('max_clients', Number(e.target.value))} />
                  <input className="input" type="number" min={0} placeholder="Per IP"
                    value={form.max_per_ip} onChange={e => set('max_per_ip', Number(e.target.value))} />
                </div>
              </div>
            </div>

            <div style={{ display: 'flex', flexWrap: 'wrap', gap: 20 }}>
              {(
                [
                  ['enabled',          'Enable FTP service'],
                  ['allow_anonymous',  'Allow anonymous access'],
                  ['allow_local_users','Allow local user login'],
                  ['write_enable',     'Allow uploads (write)'],
                  ['chroot_local_user','Restrict users to home directory (chroot)'],
                ] as [keyof FTPConfig, string][]
              ).map(([key, label]) => (
                <label key={key} style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', userSelect: 'none' }}>
                  <input type="checkbox"
                    checked={form[key] as boolean}
                    onChange={e => set(key, e.target.checked as FTPConfig[typeof key])} />
                  <span style={{ fontSize: 'var(--text-sm)', fontWeight: 500 }}>{label}</span>
                </label>
              ))}
            </div>
          </div>

          {/* TLS - only relevant when ftps */}
          {form.mode === 'ftps' && (
            <div className="card" style={{ padding: 24, marginBottom: 24 }}>
              <h2 style={{ fontSize: 'var(--text-base)', fontWeight: 700, marginBottom: 6 }}>TLS Certificate</h2>
              <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)', marginBottom: 16 }}>
                Paths to the certificate and key files for FTPS. Use the Certificates page to manage certs.
              </p>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16 }}>
                <div>
                  <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Certificate path</label>
                  <input className="input" type="text" placeholder="/etc/ssl/certs/server.crt"
                    value={form.tls_cert_path} onChange={e => set('tls_cert_path', e.target.value)}
                    style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }} />
                </div>
                <div>
                  <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Private key path</label>
                  <input className="input" type="text" placeholder="/etc/ssl/private/server.key"
                    value={form.tls_key_path} onChange={e => set('tls_key_path', e.target.value)}
                    style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }} />
                </div>
              </div>
            </div>
          )}

          {/* Allowed users */}
          <div className="card" style={{ padding: 24, marginBottom: 24 }}>
            <h2 style={{ fontSize: 'var(--text-base)', fontWeight: 700, marginBottom: 6 }}>Allowed Users</h2>
            <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)', marginBottom: 16 }}>
              Only checked users may connect via FTP. Unchecked users are blocked at login even if their system credentials are valid.
            </p>
            {serviceUsers.length === 0 ? (
              <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>No active system users found.</div>
            ) : (
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: 10 }}>
                {serviceUsers.map(u => (
                  <label key={u.username} style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', userSelect: 'none', padding: '8px 12px', border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', background: form.allowed_users.includes(u.username) ? 'var(--primary-bg)' : 'transparent', borderColor: form.allowed_users.includes(u.username) ? 'var(--primary)' : 'var(--border)', transition: 'var(--transition-fast)' }}>
                    <input type="checkbox"
                      checked={form.allowed_users.includes(u.username)}
                      onChange={() => toggleUser(u.username)} />
                    <span style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }}>{u.username}</span>
                  </label>
                ))}
              </div>
            )}
          </div>

          {/* Save */}
          <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
            <button type="submit" className="btn btn-primary" disabled={saveMut.isPending}>
              {saveMut.isPending ? (
                <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Spinner size={14} color="rgba(0,0,0,0.7)" /> Applying…</span>
              ) : (
                <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Icon name="save" size={15} /> Save & Apply</span>
              )}
            </button>
          </div>
        </form>
      )}
    </div>
  )
}
