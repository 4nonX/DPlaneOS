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
    headers['Content-Type'] = 'application/json'
    headers['X-CSRF-Token'] = getCsrfToken()
  }

  const sessionId = getSessionId()
  const username = getUsername()
  if (sessionId) headers['X-Session-ID'] = sessionId
  if (username) headers['X-User'] = username

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
    // Throw so React Query marks the query as errored even during redirect
    throw new ApiError(401, 'Session expired. Redirecting to login.')
  }

  if (!res.ok) {
    // Attempt to extract the daemon's error message
    let message = `${res.status} ${res.statusText}`
    try {
      const errBody = await res.json()
      if (errBody.error) message = errBody.error
      else if (errBody.message) message = errBody.message
    } catch {
      // non-JSON error body — use the HTTP status text
    }
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
}
