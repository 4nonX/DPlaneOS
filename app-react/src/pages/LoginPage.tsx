/**
 * pages/LoginPage.tsx
 *
 * Login page — handles standard login and TOTP second step.
 * On success, navigates to / (or to /setup if first_run).
 * Matches exact daemon auth flow.
 */

import { useState, useEffect, useRef } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth'
import { api, storeSession } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Spinner } from '@/components/ui/LoadingSpinner'

// Daemon /api/system/status response (used to detect first_run)
interface SystemStatus {
  success: boolean
  first_run?: boolean
}

// Daemon /api/auth/totp/verify response
interface TotpVerifyResponse {
  success: boolean
  session_id?: string
  username?: string
  expires_at?: number
  error?: string
}

type LoginStep = 'credentials' | 'totp'

export function LoginPage() {
  const navigate = useNavigate()
  const login = useAuthStore((s) => s.login)
  const isAuthenticated = useAuthStore((s) => s.isAuthenticated)

  const [step, setStep] = useState<LoginStep>('credentials')
  const [pendingToken, setPendingToken] = useState<string>('')

  // Credentials step
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [showPassword, setShowPassword] = useState(false)

  // TOTP step
  const [totpCode, setTotpCode] = useState('')
  const totpRef = useRef<HTMLInputElement>(null)

  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [daemonVersion, setDaemonVersion] = useState<string | null>(null)

  // Already authenticated — redirect out
  useEffect(() => {
    if (isAuthenticated) {
      navigate({ to: '/' })
    }
  }, [isAuthenticated, navigate])

  // Fetch daemon version for display
  useEffect(() => {
    fetch('/health')
      .then((r) => r.json())
      .then((d: { version?: string }) => {
        if (d.version) setDaemonVersion(`v${d.version}`)
      })
      .catch(() => {})
  }, [])

  async function handleCredentialsSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!username.trim() || !password) {
      setError('Please enter username and password')
      return
    }
    setError(null)
    setLoading(true)
    try {
      const result = await login(username.trim(), password)
      if (result?.requiresTotp) {
        setPendingToken(result.pendingToken)
        setStep('totp')
        setTimeout(() => totpRef.current?.focus(), 50)
        return
      }
      // Check first_run to redirect to setup wizard
      try {
        const status = await api.get<SystemStatus>('/api/system/status')
        if (status.first_run) {
          navigate({ to: '/setup' })
          return
        }
      } catch {
        // Non-critical — proceed to dashboard
      }
      navigate({ to: '/' })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Login failed')
    } finally {
      setLoading(false)
    }
  }

  async function handleTotpSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!totpCode.trim()) {
      setError('Please enter your 6-digit code')
      return
    }
    setError(null)
    setLoading(true)
    try {
      // TOTP verify uses the pending_token as session for this one call
      const res = await fetch('/api/auth/totp/verify', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          'X-Session-ID': pendingToken,
        },
        credentials: 'same-origin',
        body: JSON.stringify({ code: totpCode }),
      })
      const data = (await res.json()) as TotpVerifyResponse
      if (!data.success) {
        setError(data.error ?? 'Invalid TOTP code')
        return
      }
      if (!data.session_id || !data.username) {
        setError('Malformed TOTP response from server')
        return
      }
      storeSession(data.session_id, data.username)
      // Re-validate store with new session
      await useAuthStore.getState().validateSession()
      navigate({ to: '/' })
    } catch (err) {
      setError(err instanceof Error ? err.message : 'TOTP verification failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div
      style={{
        minHeight: '100vh',
        background: 'var(--bg)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: '24px',
      }}
    >
      <div style={{ width: '100%', maxWidth: 420 }}>
        {/* Logo */}
        <div style={{ textAlign: 'center', marginBottom: 40 }}>
          <div
            style={{
              fontSize: 28,
              fontWeight: 800,
              color: 'var(--primary)',
              letterSpacing: '-1px',
              marginBottom: 8,
            }}
          >
            D-PlaneOS
          </div>
          {daemonVersion && (
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              {daemonVersion}
            </div>
          )}
        </div>

        {/* Card */}
        <div
          style={{
            background: 'rgba(255,255,255,0.03)',
            border: '1px solid var(--border)',
            borderRadius: 'var(--radius-xl)',
            padding: '32px',
          }}
        >
          <div style={{ marginBottom: 24 }}>
            <h2 style={{ fontSize: 'var(--text-xl)', fontWeight: 700, marginBottom: 4 }}>
              {step === 'credentials' ? 'Sign in' : 'Two-factor authentication'}
            </h2>
            <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
              {step === 'credentials'
                ? 'Enter your credentials to continue'
                : 'Enter the 6-digit code from your authenticator app'}
            </p>
          </div>

          {/* Error banner */}
          {error && (
            <div
              role="alert"
              style={{
                display: 'flex',
                alignItems: 'center',
                gap: '8px',
                padding: '10px 14px',
                background: 'var(--error-bg)',
                border: '1px solid var(--error-border)',
                borderRadius: 'var(--radius-sm)',
                marginBottom: 20,
                fontSize: 'var(--text-sm)',
                color: 'var(--error)',
              }}
            >
              <Icon name="error_outline" size={16} style={{ flexShrink: 0 }} />
              {error}
            </div>
          )}

          {step === 'credentials' ? (
            <form onSubmit={handleCredentialsSubmit} noValidate>
              <div style={{ marginBottom: 16 }}>
                <label
                  htmlFor="username"
                  style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}
                >
                  Username
                </label>
                <input
                  id="username"
                  type="text"
                  value={username}
                  onChange={(e) => setUsername(e.target.value)}
                  autoComplete="username"
                  autoFocus
                  disabled={loading}
                  style={inputStyle}
                />
              </div>

              <div style={{ marginBottom: 24 }}>
                <label
                  htmlFor="password"
                  style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}
                >
                  Password
                </label>
                <div style={{ position: 'relative' }}>
                  <input
                    id="password"
                    type={showPassword ? 'text' : 'password'}
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    autoComplete="current-password"
                    disabled={loading}
                    style={{ ...inputStyle, paddingRight: 44 }}
                  />
                  <button
                    type="button"
                    onClick={() => setShowPassword((v) => !v)}
                    aria-label={showPassword ? 'Hide password' : 'Show password'}
                    style={{
                      position: 'absolute',
                      right: 10,
                      top: '50%',
                      transform: 'translateY(-50%)',
                      background: 'none',
                      border: 'none',
                      cursor: 'pointer',
                      color: showPassword ? 'var(--primary)' : 'var(--text-tertiary)',
                      display: 'flex',
                      alignItems: 'center',
                      padding: 4,
                    }}
                  >
                    <Icon name={showPassword ? 'visibility_off' : 'visibility'} size={20} />
                  </button>
                </div>
              </div>

              <button
                type="submit"
                disabled={loading}
                style={submitButtonStyle}
              >
                {loading ? (
                  <span style={{ display: 'flex', alignItems: 'center', gap: 8, justifyContent: 'center' }}>
                    <Spinner size={16} color="rgba(0,0,0,0.7)" />
                    Signing in…
                  </span>
                ) : (
                  'Sign In'
                )}
              </button>
            </form>
          ) : (
            <form onSubmit={handleTotpSubmit} noValidate>
              <div style={{ marginBottom: 24 }}>
                <label
                  htmlFor="totp"
                  style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}
                >
                  Authentication code
                </label>
                <input
                  id="totp"
                  ref={totpRef}
                  type="text"
                  inputMode="numeric"
                  pattern="[0-9]{6}"
                  maxLength={6}
                  value={totpCode}
                  onChange={(e) => setTotpCode(e.target.value.replace(/\D/g, ''))}
                  autoComplete="one-time-code"
                  disabled={loading}
                  style={{ ...inputStyle, fontFamily: 'var(--font-mono)', fontSize: 24, letterSpacing: 8, textAlign: 'center' }}
                />
              </div>

              <button type="submit" disabled={loading || totpCode.length < 6} style={submitButtonStyle}>
                {loading ? (
                  <span style={{ display: 'flex', alignItems: 'center', gap: 8, justifyContent: 'center' }}>
                    <Spinner size={16} color="rgba(0,0,0,0.7)" />
                    Verifying…
                  </span>
                ) : (
                  'Verify'
                )}
              </button>

              <button
                type="button"
                onClick={() => { setStep('credentials'); setTotpCode(''); setError(null) }}
                style={{
                  width: '100%',
                  marginTop: 10,
                  padding: '10px',
                  background: 'none',
                  border: 'none',
                  cursor: 'pointer',
                  color: 'var(--text-secondary)',
                  fontSize: 'var(--text-sm)',
                  fontFamily: 'var(--font-ui)',
                }}
              >
                ← Back to login
              </button>
            </form>
          )}
        </div>
      </div>
    </div>
  )
}

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '10px 14px',
  background: 'var(--surface)',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)',
  color: 'var(--text)',
  fontSize: 'var(--text-base)',
  fontFamily: 'var(--font-ui)',
  outline: 'none',
  transition: 'border-color 0.15s',
}

const submitButtonStyle: React.CSSProperties = {
  width: '100%',
  padding: '12px',
  background: 'var(--primary)',
  border: 'none',
  borderRadius: 'var(--radius-sm)',
  color: 'var(--text-on-primary)',
  fontSize: 'var(--text-base)',
  fontWeight: 600,
  fontFamily: 'var(--font-ui)',
  cursor: 'pointer',
  transition: 'opacity 0.15s',
}

import type React from 'react'
