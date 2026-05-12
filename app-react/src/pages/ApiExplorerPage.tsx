/**
 * pages/ApiExplorerPage.tsx - API Explorer
 *
 * Interactive reference for D-PlaneOS daemon endpoints.
 * Makes live requests using the same auth/CSRF headers as the rest of the UI.
 */

import { useState, useId } from 'react'
import { Icon } from '@/components/ui/Icon'
import { getCsrfToken, getSessionId, getUsername } from '@/lib/api'

// ---------------------------------------------------------------------------
// Endpoint catalog
// ---------------------------------------------------------------------------

type HttpMethod = 'GET' | 'POST' | 'PUT' | 'DELETE' | 'PATCH'

interface Endpoint {
  method: HttpMethod
  path: string
  description: string
  defaultBody?: string
}

interface EndpointGroup {
  label: string
  icon: string
  endpoints: Endpoint[]
}

const CATALOG: EndpointGroup[] = [
  {
    label: 'Auth', icon: 'lock',
    endpoints: [
      { method: 'GET',  path: '/api/auth/check',   description: 'Check if the current session is authenticated' },
      { method: 'GET',  path: '/api/auth/session',  description: 'Get current session details' },
      { method: 'GET',  path: '/api/csrf',           description: 'Fetch a fresh CSRF token' },
    ],
  },
  {
    label: 'ZFS Pools', icon: 'water',
    endpoints: [
      { method: 'GET',  path: '/api/zfs/pools',     description: 'List all ZFS pools with health and capacity' },
    ],
  },
  {
    label: 'ZFS Datasets', icon: 'dataset',
    endpoints: [
      { method: 'GET',  path: '/api/zfs/datasets',  description: 'List all ZFS datasets across all pools' },
      { method: 'POST', path: '/api/zfs/datasets',  description: 'Create a new dataset',
        defaultBody: '{\n  "name": "pool/dataset",\n  "compression": "lz4",\n  "quota": ""\n}' },
    ],
  },
  {
    label: 'ZFS Encryption', icon: 'lock',
    endpoints: [
      { method: 'GET',  path: '/api/zfs/encryption/list',   description: 'List all encrypted datasets with key status' },
      { method: 'POST', path: '/api/zfs/encryption/create', description: 'Create a new encrypted dataset',
        defaultBody: '{\n  "name": "pool/secret",\n  "encryption": "aes-256-gcm",\n  "key": ""\n}' },
      { method: 'POST', path: '/api/zfs/encryption/lock',   description: 'Lock an encrypted dataset (unload key)',
        defaultBody: '{\n  "dataset": "pool/secret"\n}' },
      { method: 'POST', path: '/api/zfs/encryption/unlock', description: 'Unlock an encrypted dataset (load key)',
        defaultBody: '{\n  "dataset": "pool/secret",\n  "key": ""\n}' },
      { method: 'POST', path: '/api/zfs/encryption/change-key', description: 'Change encryption key',
        defaultBody: '{\n  "dataset": "pool/secret",\n  "old_key": "",\n  "new_key": ""\n}' },
    ],
  },
  {
    label: 'ZFS Snapshots', icon: 'camera',
    endpoints: [
      { method: 'GET',  path: '/api/zfs/snapshots', description: 'List snapshots. Add ?dataset=name to filter.' },
      { method: 'POST', path: '/api/zfs/snapshots', description: 'Create a snapshot',
        defaultBody: '{\n  "dataset": "pool/data",\n  "name": "manual-1"\n}' },
      { method: 'POST', path: '/api/zfs/snapshots/rollback', description: 'Rollback to latest snapshot',
        defaultBody: '{\n  "dataset": "pool/data"\n}' },
      { method: 'POST', path: '/api/zfs/snapshots/clone', description: 'Clone a snapshot into a new dataset',
        defaultBody: '{\n  "snapshot": "pool/data@snap",\n  "clone": "pool/data-clone"\n}' },
    ],
  },
  {
    label: 'Shares', icon: 'folder_shared',
    endpoints: [
      { method: 'GET',  path: '/api/shares/list',          description: 'List all SMB shares' },
      { method: 'GET',  path: '/api/shares/smb/sessions',  description: 'List active SMB sessions' },
      { method: 'GET',  path: '/api/shares/settings',      description: 'Get SMB global settings' },
    ],
  },
  {
    label: 'Docker', icon: 'developer_board',
    endpoints: [
      { method: 'GET',  path: '/api/docker/containers',    description: 'List Docker containers' },
    ],
  },
  {
    label: 'Hardware', icon: 'memory',
    endpoints: [
      { method: 'GET',  path: '/api/hardware',             description: 'Get hardware details: CPU, RAM, disks, temperatures' },
    ],
  },
  {
    label: 'Network', icon: 'lan',
    endpoints: [
      { method: 'GET',  path: '/api/network/interfaces',   description: 'List network interfaces and their current state' },
    ],
  },
  {
    label: 'System', icon: 'tune',
    endpoints: [
      { method: 'GET',  path: '/api/system/info',          description: 'Get system info: hostname, version, uptime' },
      { method: 'GET',  path: '/api/system/metrics',       description: 'Get live system metrics: CPU, memory, ARC' },
      { method: 'GET',  path: '/api/monitoring/inotify',   description: 'Get inotify watch limit usage' },
      { method: 'GET',  path: '/api/alerts',               description: 'List system alerts' },
    ],
  },
]

// ---------------------------------------------------------------------------
// Colors
// ---------------------------------------------------------------------------

const METHOD_COLOR: Record<HttpMethod, string> = {
  GET:    'var(--success)',
  POST:   'var(--primary)',
  PUT:    'var(--warning)',
  DELETE: 'var(--error)',
  PATCH:  'var(--info)',
}
const METHOD_BG: Record<HttpMethod, string> = {
  GET:    'var(--success-bg)',
  POST:   'var(--primary-bg)',
  PUT:    'var(--warning-bg)',
  DELETE: 'var(--error-bg)',
  PATCH:  'var(--info-bg)',
}

function statusColor(s: number) {
  if (s >= 500) return 'var(--error)'
  if (s >= 400) return 'var(--warning)'
  if (s >= 200) return 'var(--success)'
  return 'var(--text-tertiary)'
}
function statusBg(s: number) {
  if (s >= 500) return 'var(--error-bg)'
  if (s >= 400) return 'var(--warning-bg)'
  if (s >= 200) return 'var(--success-bg)'
  return 'var(--surface)'
}

// ---------------------------------------------------------------------------
// Fetch helper
// ---------------------------------------------------------------------------

interface ExplorerResult { status: number; data: unknown; ms: number }

async function explorerFetch(method: HttpMethod, path: string, body?: unknown): Promise<ExplorerResult> {
  const start = Date.now()
  const headers: Record<string, string> = { Accept: 'application/json' }

  if (method !== 'GET') {
    headers['Content-Type'] = 'application/json'
    try { headers['X-CSRF-Token'] = getCsrfToken() } catch { /* not initialized */ }
  }
  const sid = getSessionId(); const user = getUsername()
  if (sid) headers['X-Session-ID'] = sid
  if (user) headers['X-User'] = user

  try {
    const res = await fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
    let data: unknown
    const ct = res.headers.get('content-type') ?? ''
    try { data = ct.includes('json') ? await res.json() : await res.text() } catch { data = '[parse error]' }
    return { status: res.status, data, ms: Date.now() - start }
  } catch (err) {
    return { status: 0, data: { error: String(err) }, ms: Date.now() - start }
  }
}

// ---------------------------------------------------------------------------
// EndpointRow
// ---------------------------------------------------------------------------

function EndpointRow({ ep }: { ep: Endpoint }) {
  const panelId = useId()
  const btnId   = useId()
  const [open, setOpen] = useState(false)
  const [body, setBody] = useState(ep.defaultBody ?? '')
  const [result, setResult] = useState<ExplorerResult | null>(null)
  const [loading, setLoading] = useState(false)
  const [pathInput, setPathInput] = useState(ep.path)
  const needsBody = ep.method !== 'GET'

  async function send() {
    setLoading(true); setResult(null)
    let parsedBody: unknown
    if (needsBody && body.trim()) {
      try { parsedBody = JSON.parse(body) } catch {
        setResult({ status: 0, data: { error: 'Invalid JSON in request body' }, ms: 0 })
        setLoading(false); return
      }
    }
    const r = await explorerFetch(ep.method, pathInput, parsedBody)
    setResult(r); setLoading(false)
  }

  return (
    <div style={{ borderBottom: '1px solid var(--border-subtle)' }}>
      <button
        id={btnId}
        onClick={() => setOpen(o => !o)}
        aria-expanded={open}
        aria-controls={panelId}
        style={{
          width: '100%', display: 'flex', alignItems: 'center', gap: 12,
          padding: '12px 16px', background: 'none', border: 'none', cursor: 'pointer',
          textAlign: 'left', transition: 'background 0.1s',
        }}
        onMouseEnter={e => (e.currentTarget.style.background = 'var(--surface-hover)')}
        onMouseLeave={e => (e.currentTarget.style.background = 'none')}
      >
        <span style={{
          minWidth: 54, textAlign: 'center', fontSize: 'var(--text-2xs)', fontWeight: 700,
          padding: '2px 6px', borderRadius: 'var(--radius-sm)',
          color: METHOD_COLOR[ep.method], background: METHOD_BG[ep.method],
          letterSpacing: '0.4px',
        }}>
          {ep.method}
        </span>
        <span style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', flex: 1, color: 'var(--text)' }}>
          {ep.path}
        </span>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', flex: 1, textAlign: 'left' }}>
          {ep.description}
        </span>
        {result && (
          <span style={{
            fontSize: 'var(--text-2xs)', fontWeight: 700, padding: '2px 7px',
            borderRadius: 'var(--radius-sm)', marginLeft: 8,
            color: statusColor(result.status), background: statusBg(result.status),
          }}>
            {result.status || 'ERR'}
          </span>
        )}
        <Icon name={open ? 'expand_less' : 'expand_more'} size={16} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
      </button>

      {open && (
        <div
          id={panelId}
          role="region"
          aria-labelledby={btnId}
          style={{ padding: '0 16px 16px', display: 'flex', flexDirection: 'column', gap: 12 }}
        >
          {/* Path editor */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontWeight: 600, minWidth: 32 }}>URL</span>
            <input
              value={pathInput}
              onChange={e => setPathInput(e.target.value)}
              className="input"
              style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)' }}
              aria-label="Request URL"
            />
          </div>

          {/* Body editor */}
          {needsBody && (
            <div>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontWeight: 600, marginBottom: 6 }}>
                Request body (JSON)
              </div>
              <textarea
                value={body}
                onChange={e => setBody(e.target.value)}
                rows={6}
                className="input"
                style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', resize: 'vertical', width: '100%', boxSizing: 'border-box' }}
                aria-label="Request body JSON"
                spellCheck={false}
              />
            </div>
          )}

          <div>
            <button
              onClick={send}
              disabled={loading}
              className="btn btn-primary btn-sm"
              style={{ minWidth: 80 }}
            >
              {loading
                ? <><Icon name="progress_activity" size={14} style={{ animation: 'spin 1s linear infinite' }} /> Sending…</>
                : <><Icon name="send" size={14} /> Send</>
              }
            </button>
          </div>

          {/* Response */}
          {result && (
            <div aria-live="polite" aria-atomic="true">
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
                <span style={{
                  fontSize: 'var(--text-sm)', fontWeight: 700, padding: '3px 10px',
                  borderRadius: 'var(--radius-sm)',
                  color: statusColor(result.status), background: statusBg(result.status),
                }}>
                  {result.status || 'Network Error'}
                </span>
                <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                  {result.ms} ms
                </span>
                <button
                  onClick={() => { try { navigator.clipboard.writeText(JSON.stringify(result.data, null, 2)) } catch { /* ignore */ } }}
                  className="btn btn-ghost btn-xs"
                  aria-label="Copy response JSON"
                  style={{ marginLeft: 'auto' }}
                >
                  <Icon name="content_copy" size={12} /> Copy
                </button>
              </div>
              <pre style={{
                margin: 0, padding: '12px 14px', borderRadius: 'var(--radius-md)',
                background: 'var(--bg)', border: '1px solid var(--border-subtle)',
                fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)',
                color: 'var(--text-secondary)', overflow: 'auto', maxHeight: 320,
                lineHeight: 1.6,
              }}>
                {JSON.stringify(result.data, null, 2)}
              </pre>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// ApiExplorerPage
// ---------------------------------------------------------------------------

export function ApiExplorerPage() {
  const [openGroups, setOpenGroups] = useState<Set<string>>(new Set())

  function toggleGroup(label: string) {
    setOpenGroups(prev => {
      const next = new Set(prev)
      if (next.has(label)) next.delete(label); else next.add(label)
      return next
    })
  }

  return (
    <div style={{ padding: '32px 28px', maxWidth: 1100 }}>
      <div className="page-header" style={{ marginBottom: 28 }}>
        <h1 className="page-title">API Explorer</h1>
        <p className="page-subtitle">
          Browse and test D-PlaneOS daemon endpoints. Requests are made with your current session credentials.
        </p>
      </div>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {CATALOG.map(group => {
          const isOpen = openGroups.has(group.label)
          const groupBtnId = `grp-${group.label}`
          const groupPanelId = `grp-panel-${group.label}`
          return (
            <div key={group.label} className="card" style={{ padding: 0, overflow: 'hidden' }}>
              <button
                id={groupBtnId}
                onClick={() => toggleGroup(group.label)}
                aria-expanded={isOpen}
                aria-controls={groupPanelId}
                style={{
                  width: '100%', display: 'flex', alignItems: 'center', gap: 12,
                  padding: '14px 16px', background: 'none', border: 'none', cursor: 'pointer',
                  borderBottom: isOpen ? '1px solid var(--border-subtle)' : 'none',
                  transition: 'background 0.1s',
                }}
                onMouseEnter={e => (e.currentTarget.style.background = 'var(--surface-hover)')}
                onMouseLeave={e => (e.currentTarget.style.background = 'none')}
              >
                <Icon name={group.icon} size={18} style={{ color: 'var(--primary)', flexShrink: 0 }} />
                <span style={{ fontSize: 'var(--text-md)', fontWeight: 700, flex: 1, textAlign: 'left' }}>
                  {group.label}
                </span>
                <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                  {group.endpoints.length} endpoint{group.endpoints.length !== 1 ? 's' : ''}
                </span>
                <Icon name={isOpen ? 'expand_less' : 'expand_more'} size={18} style={{ color: 'var(--text-tertiary)', flexShrink: 0, marginLeft: 4 }} />
              </button>

              {isOpen && (
                <div id={groupPanelId} role="list" aria-labelledby={groupBtnId}>
                  {group.endpoints.map(ep => (
                    <div key={`${ep.method}:${ep.path}`} role="listitem">
                      <EndpointRow ep={ep} />
                    </div>
                  ))}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
