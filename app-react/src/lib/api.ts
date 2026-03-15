/**
 * D-PlaneOS API Client — v4.0
 *
 * Single source of truth for all daemon communication.
 *
 * Rules:
 *  - CSRF token is fetched once from GET /api/csrf on app init (response: { success, csrf_token })
 *    and injected as X-CSRF-Token on every mutating (non-GET) request.
 *  - Session ID is stored in sessionStorage as 'session_id' (set on login).
 *  - Username is stored in sessionStorage as 'username' (set on login).
 *  - X-Session-ID and X-User headers are sent on every request (daemon validates session server-side).
 *  - 401 anywhere → redirect to /login. No silent failures.
 *  - All errors surface the actual daemon error message, never swallowed.
 */

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type ApiMethod = 'GET' | 'POST' | 'PUT' | 'DELETE' | 'PATCH'

/** Wraps every daemon error so callers can display the real message. */
export class ApiError extends Error {
  constructor(
    public readonly status: number,
    message: string,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

// ---------------------------------------------------------------------------
// CSRF
// ---------------------------------------------------------------------------

let csrfToken: string | null = null

/** Fetch and cache the CSRF token. Called once on app startup via initCsrf(). */
export async function initCsrf(): Promise<void> {
  const res = await fetch('/api/csrf', { credentials: 'same-origin' })
  if (!res.ok) {
    if (getSessionId() === 'mock_session_id' || !getSessionId()) {
      console.warn('[DevProxy] CSRF init failed, using mock token for preview mode')
      csrfToken = 'mock_csrf_token'
      return
    }
    throw new ApiError(res.status, `CSRF init failed: ${res.status} ${res.statusText}`)
  }
  const data = await res.json()
  // Daemon returns: { success: true, csrf_token: "..." }
  if (!data.success || !data.csrf_token) {
    throw new ApiError(200, 'CSRF response missing csrf_token field')
  }
  csrfToken = data.csrf_token as string
}

export function getCsrfToken(): string {
  if (!csrfToken) {
    throw new Error('CSRF token not initialised — initCsrf() must complete before any mutation')
  }
  return csrfToken
}

// ---------------------------------------------------------------------------
// Session helpers
// ---------------------------------------------------------------------------

export function getSessionId(): string | null {
  return sessionStorage.getItem('session_id')
}

export function getUsername(): string | null {
  return sessionStorage.getItem('username')
}

export function storeSession(sessionId: string, username: string): void {
  sessionStorage.setItem('session_id', sessionId)
  sessionStorage.setItem('username', username)
}

export function clearSession(): void {
  sessionStorage.removeItem('session_id')
  sessionStorage.removeItem('username')
}

// ---------------------------------------------------------------------------
// Core fetch wrapper
// ---------------------------------------------------------------------------

/**
 * apiFetch — the only way to talk to the daemon.
 *
 * - Injects X-CSRF-Token on mutations.
 * - Injects X-Session-ID and X-User on every request.
 * - Throws ApiError with the actual daemon error message on non-2xx.
 * - Redirects to /login on 401.
 */
export async function apiFetch<T>(
  path: string,
  opts: {
    method?: ApiMethod
    body?: unknown
    signal?: AbortSignal
  } = {},
): Promise<T> {
  const method = opts.method ?? 'GET'
  const isMutating = method !== 'GET'

  const headers: Record<string, string> = {}

  if (isMutating) {
    if (!csrfToken) {
      await initCsrf()
    }
    headers['Content-Type'] = 'application/json'
    headers['X-CSRF-Token'] = getCsrfToken()
  }

  const sessionId = getSessionId()
  const username = getUsername()
  if (sessionId) headers['X-Session-ID'] = sessionId
  if (username) headers['X-User'] = username

  // Hardened Mock Interceptor: Gated by build-time DEV environment check.
  // In production builds, import.meta.env.DEV is false, and this entire block
  // is removed by the minifier/bundler.
  if (import.meta.env.DEV) {
    const isMockActive = sessionStorage.getItem('dplane_mock_active') === 'true'
    const isMockMode = isMockActive && (sessionId === 'mock_session_id' || !sessionId)
    
    if (isMockMode) {
      const mockData: Record<string, any> = {
        '/api/system/metrics': {
          success: true,
          cpu_model: 'AMD Ryzen 9 7950X (16-Core)',
          cpu_percent: 12.4,
          memory_total: 68719476736,
          memory_used: 12884901888,
          uptime: '14 days, 6 hours, 22 minutes',
          os: 'D-PlaneOS v4.0 (NixOS 24.11)',
          kernel: '6.12.8-dplane-pro',
          load_avg: [0.42, 0.58, 0.61],
          inotify: { used: 1200, limit: 100000, percent: 1.2 },
          memory:  { used: 12884901888, total: 68719476736, percent: 18.7 },
          arc:     { used: 4294967296, limit: 16106127360, percent: 26.6 },
          iowait:  0.2
        },
        '/api/system/status': {
          success: true,
          version: '4.0.0-stable',
          uptime_seconds: 1232542,
          ecc_warning: false,
          has_error: false
        },
        '/api/zfs/pools': {
          success: true,
          data: [
            { name: 'tank', size: '24TB', alloc: '8.2TB', free: '15.8TB', health: 'ONLINE' },
            { name: 'ssd-cache', size: '2TB', alloc: '420GB', free: '1.58TB', health: 'ONLINE' }
          ]
        },
        '/api/docker/containers': {
          success: true,
          containers: [
            { Id: '1', Names: ['/plex'], Image: 'plexinc/pms', State: 'running', Status: 'Up 4 days' },
            { Id: '2', Names: ['/home-assistant'], Image: 'homeassistant/home-assistant', State: 'running', Status: 'Up 12 days' },
            { Id: '3', Names: ['/nextcloud'], Image: 'nextcloud', State: 'running', Status: 'Up 2 hours' }
          ],
          total_containers: 3
        },
        '/api/zfs/smart': {
          success: true,
          disks: [
            { device: 'sda', smart_status: { passed: true }, temperature: { current: 32 } },
            { device: 'sdb', smart_status: { passed: true }, temperature: { current: 34 } },
            { device: 'nvme0n1', smart_status: { passed: true }, temperature: { current: 41 } }
          ]
        },
        '/api/system/audit/verify-chain': {
          success: true,
          valid: true
        },
        '/api/system/health': {
          success: true,
          checks: [
            { name: 'Kernel Integrity', status: 'OK', type: 'system' },
            { name: 'ZFS Pool: tank', status: 'OK', type: 'storage' },
            { name: 'Docker Daemon', status: 'OK', type: 'compute' },
            { name: 'ECC Memory', status: 'OK', type: 'hardware' }
          ]
        },
        '/api/nixos/pre-upgrade-snapshots': {
          success: true,
          snapshots: [
            { name: 'post-init-config', created: new Date(Date.now() - 86400000).toISOString(), size: 1048576 * 42 },
            { name: 'pre-kernel-upgrade', created: new Date(Date.now() - 172800000).toISOString(), size: 1048576 * 128 }
          ]
        },
        '/api/docker/compose/deploy': {
          success: true,
          job_id: 'deploy-job-123'
        }
      }

      if (mockData[path]) {
        console.warn(`[Mock] Intercepted ${path}`)
        // Small artificial delay for realism
        await new Promise(r => setTimeout(r, 200))
        return mockData[path] as T
      }
    }
  }

  const res = await fetch(path, {
    method,
    headers,
    credentials: 'same-origin',
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
    signal: opts.signal,
  })

  // Auth failure → redirect to login
  if (res.status === 401) {
    clearSession()
    window.location.href = '/login'
    throw new ApiError(401, 'Session expired. Redirecting to login.')
  }

  if (!res.ok) {
    let message = `${res.status} ${res.statusText}`
    try {
      const errBody = await res.json()
      if (errBody.error) message = errBody.error
      else if (errBody.message) message = errBody.message
    } catch {}
    throw new ApiError(res.status, message)
  }

  return res.json() as Promise<T>
}

// ---------------------------------------------------------------------------
// Convenience wrappers used by TanStack Query queryFns
// ---------------------------------------------------------------------------

export const api = {
  get: <T>(path: string, signal?: AbortSignal) =>
    apiFetch<T>(path, { method: 'GET', signal }),

  post: <T>(path: string, body?: unknown) =>
    apiFetch<T>(path, { method: 'POST', body }),

  put: <T>(path: string, body?: unknown) =>
    apiFetch<T>(path, { method: 'PUT', body }),

  delete: <T>(path: string, body?: unknown) =>
    apiFetch<T>(path, { method: 'DELETE', body }),

  /** Returns true if the client is currently running in mock/demo mode. */
  isMockActive: () => {
    if (!import.meta.env.DEV) return false
    const isMockActiveFlag = sessionStorage.getItem('dplane_mock_active') === 'true'
    const sessionId = sessionStorage.getItem('session_id')
    return isMockActiveFlag && (sessionId === 'mock_session_id' || !sessionId)
  }
}
