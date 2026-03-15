/**
 * stores/auth.ts — D-PlaneOS Auth Store
 *
 * Manages session state. Session is validated against GET /api/auth/check
 * on every app load — never assumed from sessionStorage alone.
 *
 * Login response from daemon:
 *   { success, session_id, username, expires_at, must_change_password }
 *
 * Session response from GET /api/auth/session:
 *   { success, user: { id, username, email, role, must_change_password } }
 */

import { create } from 'zustand'
import { api, storeSession, clearSession, getSessionId } from '@/lib/api'

// ---------------------------------------------------------------------------
// Types (matching daemon response shapes)
// ---------------------------------------------------------------------------

export interface SessionUser {
  id: number
  username: string
  email: string
  role: string
  must_change_password: boolean
}

interface LoginResponse {
  success: boolean
  session_id?: string
  username?: string
  expires_at?: number
  must_change_password?: boolean
  requires_totp?: boolean
  pending_token?: string
  error?: string
}

interface SessionResponse {
  success: boolean
  user?: SessionUser
  error?: string
}

interface AuthCheckResponse {
  authenticated: boolean
  username?: string
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

interface AuthState {
  user: SessionUser | null
  isAuthenticated: boolean
  isLoading: boolean

  /** Called on app startup. Validates session against /api/auth/check then /api/auth/session. */
  validateSession: () => Promise<void>

  /**
   * POST /api/auth/login
   * Returns { requiresTotp, pendingToken } if TOTP is enabled,
   * or void on full success.
   */
  login: (
    username: string,
    password: string,
  ) => Promise<{ requiresTotp: true; pendingToken: string } | void>

  /** POST /api/auth/logout */
  logout: () => Promise<void>

  /** Dev-only: bypass backend to preview UI */
  mockLogin: () => void
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  isAuthenticated: false,
  isLoading: true,

  mockLogin: () => {
    sessionStorage.setItem('dplane_mock_active', 'true')
    set({
      isAuthenticated: true,
      user: {
        id: 1,
        username: 'admin',
        email: 'admin@dplaneos.local',
        role: 'admin',
        must_change_password: false
      },
      isLoading: false
    })
  },

  validateSession: async () => {
    const sessionId = getSessionId()
    if (!sessionId) {
      set({ isLoading: false, isAuthenticated: false, user: null })
      return
    }

    try {
      // First check: is the session token still valid?
      const check = await api.get<AuthCheckResponse>('/api/auth/check')
      if (!check.authenticated) {
        clearSession()
        set({ isLoading: false, isAuthenticated: false, user: null })
        return
      }

      // Second: load full user details
      const session = await api.get<SessionResponse>('/api/auth/session')
      if (session.success && session.user) {
        set({ isLoading: false, isAuthenticated: true, user: session.user })
      } else {
        clearSession()
        set({ isLoading: false, isAuthenticated: false, user: null })
      }
    } catch {
      // Network error or 401 — treat as unauthenticated
      clearSession()
      set({ isLoading: false, isAuthenticated: false, user: null })
    }
  },

  login: async (username, password) => {
    const data = await api.post<LoginResponse>('/api/auth/login', { username, password })

    if (!data.success) {
      throw new Error(data.error ?? 'Login failed')
    }

    // TOTP required — return pending state to the login page
    if (data.requires_totp && data.pending_token) {
      return { requiresTotp: true as const, pendingToken: data.pending_token }
    }

    if (!data.session_id || !data.username) {
      throw new Error('Malformed login response from daemon')
    }

    storeSession(data.session_id, data.username)

    // Load full user record now that session is stored
    const session = await api.get<SessionResponse>('/api/auth/session')
    if (!session.success || !session.user) {
      throw new Error('Failed to load user session after login')
    }

    set({ isAuthenticated: true, user: session.user })
  },

  logout: async () => {
    try {
      await api.post('/api/auth/logout')
    } catch {
      // Best-effort — clear local state regardless
    }
    clearSession()
    set({ isAuthenticated: false, user: null })
  },
}))
