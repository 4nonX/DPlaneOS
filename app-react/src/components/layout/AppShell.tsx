/**
 * components/layout/AppShell.tsx
 *
 * Main application layout: Sidebar + TopBar + page content area.
 *
 * If must_change_password is true on the user object (read live from
 * /api/auth/session on every load), renders a blocking overlay calling
 * POST /api/auth/change-password { current_password, new_password }.
 * The daemon bcrypt-hashes the new password, writes it to users.password_hash,
 * and sets must_change_password=0. After validateSession() the flag is gone.
 *
 * Password requirements (enforced by daemon validatePasswordStrength):
 *   min 8 chars, uppercase, lowercase, digit, special character.
 */

import { useState, useEffect } from 'react'
import type React from 'react'
import { Outlet } from '@tanstack/react-router'
import { Sidebar } from './Sidebar'
import { TopBar } from './TopBar'
import { ToastContainer } from '@/components/ui/Toast'
import { useWsStore } from '@/stores/ws'
import { useAuthStore } from '@/stores/auth'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'

// ---------------------------------------------------------------------------
// StrengthBar — mirrors daemon validatePasswordStrength exactly
// ---------------------------------------------------------------------------

function strengthScore(pw: string): { score: number; color: string; missing: string[] } {
  let hasUpper = false, hasLower = false, hasDigit = false, hasSpecial = false
  for (const ch of pw) {
    if (/[A-Z]/.test(ch)) hasUpper = true
    else if (/[a-z]/.test(ch)) hasLower = true
    else if (/[0-9]/.test(ch)) hasDigit = true
    else hasSpecial = true
  }
  const missing: string[] = []
  if (pw.length < 8)  missing.push('8+ characters')
  if (!hasUpper)      missing.push('uppercase')
  if (!hasLower)      missing.push('lowercase')
  if (!hasDigit)      missing.push('number')
  if (!hasSpecial)    missing.push('special char')
  const score = Math.max(0, 4 - missing.length)
  const colors = ['var(--border)', 'var(--error)', 'var(--warning)', '#3b82f6', 'var(--success)']
  return { score, color: colors[score] ?? 'var(--border)', missing }
}

function StrengthBar({ password }: { password: string }) {
  if (!password) return null
  const { score, color, missing } = strengthScore(password)
  return (
    <div style={{ marginTop: 6 }}>
      <div style={{ display: 'flex', gap: 3, marginBottom: 3 }}>
        {[1,2,3,4].map(i => (
          <div key={i} style={{ flex: 1, height: 3, borderRadius: 2,
            background: i <= score ? color : 'var(--border)', transition: 'background 0.2s' }} />
        ))}
      </div>
      {missing.length > 0 && (
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
          Needs: {missing.join(', ')}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// ForcePasswordChange — blocks all navigation until password is set
// ---------------------------------------------------------------------------

function ForcePasswordChange() {
  const validateSession = useAuthStore((s) => s.validateSession)
  const user = useAuthStore((s) => s.user)
  const [current, setCurrent]     = useState('')
  const [next, setNext]           = useState('')
  const [confirm, setConfirm]     = useState('')
  const [showCurrent, setShowCurrent] = useState(false)
  const [showNext, setShowNext]       = useState(false)
  const [loading, setLoading]     = useState(false)
  const [error, setError]         = useState<string | null>(null)

  const { missing } = strengthScore(next)
  const mismatch = confirm.length > 0 && next !== confirm

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (next !== confirm) { setError('Passwords do not match'); return }
    if (missing.length > 0) { setError('Password does not meet requirements'); return }
    setError(null)
    setLoading(true)
    try {
      await api.post('/api/auth/change-password', { current_password: current, new_password: next })
      await validateSession()   // reloads user — must_change_password is now 0 in DB
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Password change failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div style={{
      position: 'fixed', inset: 0, zIndex: 9999, background: '#080808',
      display: 'flex', alignItems: 'center', justifyContent: 'center'}}>
      <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 40, width: 440, maxWidth: '90vw'}}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
          <Icon name="lock_reset" size={22} style={{ color: 'var(--warning)' }} />
          <h2 style={{ fontSize: 'var(--text-lg)', fontWeight: 700, margin: 0 }}>Set your password</h2>
        </div>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 6, marginTop: 4 }}>
          Welcome{user?.username ? `, ${user.username}` : ''}. Your temporary password must be changed before continuing.
        </p>
        <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 24 }}>
          Min 8 characters with uppercase, lowercase, number, and special character.
        </p>

        {error && (
          <div className="alert alert-error" style={{ marginBottom: 16 }}>
            <Icon name="error" size={14} /> {error}
          </div>
        )}

        <form onSubmit={handleSubmit} noValidate>
          <div style={{ marginBottom: 14 }}>
            <label style={{ display: 'block', fontSize: 'var(--text-xs)', fontWeight: 600,
              color: 'var(--text-secondary)', marginBottom: 6 }}>
              Temporary password (from install output)
            </label>
            <div style={{ position: 'relative' }}>
              <input className="input" type={showCurrent ? 'text' : 'password'} value={current}
                onChange={e => setCurrent(e.target.value)} autoComplete="current-password" required
                style={{ width: '100%', boxSizing: 'border-box', paddingRight: 40 }} />
              <button type="button" onClick={() => setShowCurrent(v => !v)}
                style={{ position: 'absolute', right: 10, top: '50%', transform: 'translateY(-50%)',
                  background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)',
                  display: 'flex', alignItems: 'center', padding: 4 }}>
                <Icon name={showCurrent ? 'visibility_off' : 'visibility'} size={16} />
              </button>
            </div>
          </div>

          <div style={{ marginBottom: 14 }}>
            <label style={{ display: 'block', fontSize: 'var(--text-xs)', fontWeight: 600,
              color: 'var(--text-secondary)', marginBottom: 6 }}>
              New password
            </label>
            <div style={{ position: 'relative' }}>
              <input className="input" type={showNext ? 'text' : 'password'} value={next}
                onChange={e => setNext(e.target.value)} autoComplete="new-password" required
                style={{ width: '100%', boxSizing: 'border-box', paddingRight: 40 }} />
              <button type="button" onClick={() => setShowNext(v => !v)}
                style={{ position: 'absolute', right: 10, top: '50%', transform: 'translateY(-50%)',
                  background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)',
                  display: 'flex', alignItems: 'center', padding: 4 }}>
                <Icon name={showNext ? 'visibility_off' : 'visibility'} size={16} />
              </button>
            </div>
            <StrengthBar password={next} />
          </div>

          <div style={{ marginBottom: 28 }}>
            <label style={{ display: 'block', fontSize: 'var(--text-xs)', fontWeight: 600,
              color: 'var(--text-secondary)', marginBottom: 6 }}>
              Confirm new password
            </label>
            <input className="input" type="password" value={confirm}
              onChange={e => setConfirm(e.target.value)} autoComplete="new-password" required
              style={{ width: '100%', boxSizing: 'border-box',
                borderColor: mismatch ? 'var(--error-border)' : undefined }} />
            {mismatch && (
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--error)', marginTop: 4 }}>
                Passwords do not match
              </div>
            )}
          </div>

          <button type="submit" className="btn btn-primary"
            disabled={loading || !current || !next || !confirm || mismatch || missing.length > 0}
            style={{ width: '100%' }}>
            {loading ? 'Changing…' : 'Set new password'}
          </button>
        </form>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// AppShell
// ---------------------------------------------------------------------------

export function AppShell() {
  const [collapsed, setCollapsed] = useState(false)
  const connect    = useWsStore((s) => s.connect)
  const disconnect = useWsStore((s) => s.disconnect)
  const user       = useAuthStore((s) => s.user)

  useEffect(() => {
    connect()
    return () => disconnect()
  }, [connect, disconnect])

  if (user?.must_change_password) {
    return <ForcePasswordChange />
  }

  const sidebarWidth = collapsed ? 'var(--sidebar-width-collapsed)' : 'var(--sidebar-width)'

  return (
    <>
      <Sidebar collapsed={collapsed} onToggle={() => setCollapsed((c) => !c)} />
      <TopBar sidebarCollapsed={collapsed} />
      <main style={{
        marginLeft: sidebarWidth,
        marginTop: 'var(--topbar-height)',
        minHeight: 'calc(100vh - var(--topbar-height))',
        padding: '32px',
        transition: 'margin-left 0.2s ease',
        maxWidth: '1600px'}}>
        <Outlet />
      </main>
      <ToastContainer />
    </>
  )
}
