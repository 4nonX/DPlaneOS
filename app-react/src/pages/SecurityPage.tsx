/**
 * pages/SecurityPage.tsx — Security (Phase 5)
 *
 * Tabs: TOTP | API Tokens | Audit Log
 *
 * Calls:
 *   GET    /api/auth/totp/setup           → { success, secret, otpauth_uri }
 *   POST   /api/auth/totp/setup           → { code } verify+enable
 *   DELETE /api/auth/totp/setup           → disable TOTP
 *   GET    /api/auth/tokens               → { success, tokens: ApiToken[] }
 *   POST   /api/auth/tokens  { name }     → { success, token: string (shown once) }
 *   DELETE /api/auth/tokens  { id }       → revoke token
 *   GET    /api/system/audit/stats        → { success, ... }
 *   GET    /api/system/audit/verify-chain → { success, valid: bool }
 */

import { useState } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface TotpSetupResponse  { success: boolean; secret?: string; otpauth_uri?: string; enabled?: boolean }
interface ApiToken           { id: number | string; name: string; created_at: string; last_used?: string; prefix?: string }
interface TokensResponse     { success: boolean; tokens: ApiToken[] }
interface AuditStats         { success: boolean; total_entries?: number; last_entry?: string; chain_valid?: boolean }

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const btnGhost: React.CSSProperties = {
  padding: '8px 14px', background: 'var(--surface)', color: 'var(--text-secondary)',
  border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 500, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnPrimary: React.CSSProperties = {
  padding: '9px 20px', background: 'var(--primary)', color: '#000',
  border: 'none', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 700, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnDanger: React.CSSProperties = {
  padding: '6px 12px', background: 'var(--error-bg)', border: '1px solid var(--error-border)',
  borderRadius: 'var(--radius-sm)', cursor: 'pointer', color: 'var(--error)',
  fontSize: 'var(--text-xs)', fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 4,
}
const inputStyle: React.CSSProperties = {
  background: 'var(--surface)', border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)', padding: '9px 13px',
  color: 'var(--text)', fontSize: 'var(--text-sm)', width: '100%',
  fontFamily: 'var(--font-ui)', outline: 'none', boxSizing: 'border-box',
}

function fmtDate(s?: string) {
  if (!s) return 'Never'
  try { return new Date(s).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'short' }) }
  catch { return s }
}

// ---------------------------------------------------------------------------
// QR Code renderer (uses Google Charts — no external dep)
// ---------------------------------------------------------------------------

function QRImage({ uri }: { uri: string }) {
  const url = `https://api.qrserver.com/v1/create-qr-code/?size=180x180&data=${encodeURIComponent(uri)}`
  return <img src={url} alt="TOTP QR Code" width={180} height={180} style={{ borderRadius: 8, border: '4px solid #fff', display: 'block' }} />
}

// ---------------------------------------------------------------------------
// TOTPTab
// ---------------------------------------------------------------------------

function TOTPTab() {
  const qc = useQueryClient()
  const [code, setCode] = useState('')
  const [step, setStep] = useState<'idle' | 'setup'>('idle')

  const setupQ = useQuery({
    queryKey: ['totp', 'setup'],
    queryFn: ({ signal }) => api.get<TotpSetupResponse>('/api/auth/totp/setup', signal),
    enabled: step === 'setup',
  })

  const verify = useMutation({
    mutationFn: () => api.post('/api/auth/totp/setup', { code }),
    onSuccess: () => { toast.success('TOTP enabled'); setStep('idle'); setCode(''); qc.invalidateQueries({ queryKey: ['totp', 'setup'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const disable = useMutation({
    mutationFn: () => api.delete('/api/auth/totp/setup'),
    onSuccess: () => { toast.success('TOTP disabled'); setStep('idle'); qc.invalidateQueries({ queryKey: ['totp', 'setup'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const setupData = setupQ.data

  return (
    <div style={{ maxWidth: 520 }}>
      <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 28 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 24 }}>
          <div style={{ width: 48, height: 48, background: 'var(--primary-bg)', border: '1px solid rgba(138,156,255,0.2)', borderRadius: 'var(--radius-md)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
            <Icon name="phonelink_lock" size={24} style={{ color: 'var(--primary)' }} />
          </div>
          <div>
            <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>Two-Factor Authentication</div>
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>TOTP via authenticator app (Google Authenticator, Aegis, etc.)</div>
          </div>
        </div>

        {step === 'idle' && (
          <>
            <div style={{ padding: '14px 18px', background: 'var(--surface)', borderRadius: 'var(--radius-md)', marginBottom: 20, display: 'flex', alignItems: 'center', gap: 10, fontSize: 'var(--text-sm)' }}>
              <Icon name="info" size={16} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
              <span style={{ color: 'var(--text-secondary)' }}>TOTP status is per-session. Click Setup to generate a new secret and QR code.</span>
            </div>
            <div style={{ display: 'flex', gap: 8 }}>
              <button onClick={() => setStep('setup')} style={btnPrimary}><Icon name="add_circle" size={15} />Setup TOTP</button>
              <button onClick={() => disable.mutate()} disabled={disable.isPending} style={btnDanger}>
                <Icon name="delete" size={14} />{disable.isPending ? 'Disabling…' : 'Disable TOTP'}
              </button>
            </div>
          </>
        )}

        {step === 'setup' && (
          <>
            {setupQ.isLoading && <Skeleton height={220} />}
            {setupQ.isError && <ErrorState error={setupQ.error} />}
            {setupData && (
              <>
                <div style={{ display: 'flex', justifyContent: 'center', marginBottom: 20 }}>
                  {setupData.otpauth_uri ? <QRImage uri={setupData.otpauth_uri} /> : <Icon name="qr_code_2" size={120} style={{ opacity: 0.3 }} />}
                </div>
                {setupData.secret && (
                  <div style={{ marginBottom: 20 }}>
                    <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 6 }}>Manual entry key</div>
                    <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', color: 'var(--primary)', letterSpacing: '2px', textAlign: 'center', wordBreak: 'break-all', padding: '10px 14px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)' }}>
                      {setupData.secret.match(/.{1,4}/g)?.join(' ')}
                    </div>
                  </div>
                )}
                <div style={{ marginBottom: 16 }}>
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 6 }}>Enter 6-digit code from your app to verify</div>
                  <input value={code} onChange={e => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
                    placeholder="000000" maxLength={6} style={{ ...inputStyle, fontFamily: 'var(--font-mono)', letterSpacing: '4px', fontSize: 'var(--text-xl)', textAlign: 'center' }}
                    autoFocus onKeyDown={e => e.key === 'Enter' && code.length === 6 && verify.mutate()} />
                </div>
                <div style={{ display: 'flex', gap: 8 }}>
                  <button onClick={() => setStep('idle')} style={btnGhost}>Cancel</button>
                  <button onClick={() => verify.mutate()} disabled={verify.isPending || code.length !== 6} style={btnPrimary}>
                    <Icon name="verified" size={15} />{verify.isPending ? 'Verifying…' : 'Verify & Enable'}
                  </button>
                </div>
              </>
            )}
          </>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// TokensTab
// ---------------------------------------------------------------------------

function TokensTab() {
  const qc = useQueryClient()
  const [newName, setNewName] = useState('')
  const [newToken, setNewToken] = useState<string | null>(null)

  const tokensQ = useQuery({
    queryKey: ['auth', 'tokens'],
    queryFn: ({ signal }) => api.get<TokensResponse>('/api/auth/tokens', signal),
  })

  const create = useMutation({
    mutationFn: () => api.post<{ success: boolean; token: string }>('/api/auth/tokens', { name: newName }),
    onSuccess: data => { setNewToken(data.token); setNewName(''); qc.invalidateQueries({ queryKey: ['auth', 'tokens'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const revoke = useMutation({
    mutationFn: (id: number | string) => api.delete('/api/auth/tokens', { id }),
    onSuccess: () => { toast.success('Token revoked'); qc.invalidateQueries({ queryKey: ['auth', 'tokens'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const tokens = tokensQ.data?.tokens ?? []

  return (
    <>
      {/* New token display — shown once */}
      {newToken && (
        <div style={{ marginBottom: 20, padding: 20, background: 'var(--success-bg)', border: '1px solid var(--success-border)', borderRadius: 'var(--radius-lg)' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
            <Icon name="check_circle" size={16} style={{ color: 'var(--success)' }} />
            <span style={{ fontWeight: 700, color: 'var(--success)' }}>Token created — copy it now, it won't be shown again</span>
          </div>
          <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
            <code style={{ flex: 1, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', background: 'var(--surface)', padding: '10px 14px', borderRadius: 'var(--radius-sm)', overflow: 'auto', wordBreak: 'break-all', color: 'var(--text)' }}>
              {newToken}
            </code>
            <button onClick={() => { navigator.clipboard.writeText(newToken); toast.success('Copied') }} style={btnGhost}><Icon name="content_copy" size={14} /></button>
          </div>
          <button onClick={() => setNewToken(null)} style={{ ...btnGhost, marginTop: 10, fontSize: 'var(--text-xs)' }}>
            <Icon name="close" size={14} />Dismiss
          </button>
        </div>
      )}

      {/* Create form */}
      <div style={{ display: 'flex', gap: 8, marginBottom: 20 }}>
        <input value={newName} onChange={e => setNewName(e.target.value)} placeholder="Token name (e.g. backup-script)"
          style={{ ...inputStyle, flex: 1 }} onKeyDown={e => e.key === 'Enter' && newName.trim() && create.mutate()} />
        <button onClick={() => create.mutate()} disabled={!newName.trim() || create.isPending} style={btnPrimary}>
          <Icon name="add" size={15} />{create.isPending ? 'Creating…' : 'Create Token'}
        </button>
      </div>

      {tokensQ.isLoading && <Skeleton height={120} />}
      {tokensQ.isError && <ErrorState error={tokensQ.error} />}

      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {tokens.map(token => (
          <div key={token.id} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '12px 18px', background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-md)' }}>
            <Icon name="key" size={18} style={{ color: 'var(--primary)', flexShrink: 0 }} />
            <div style={{ flex: 1 }}>
              <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)' }}>{token.name}</div>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>
                Created: {fmtDate(token.created_at)}
                {token.last_used && ` · Last used: ${fmtDate(token.last_used)}`}
                {token.prefix && ` · ${token.prefix}…`}
              </div>
            </div>
            <button onClick={() => { if (window.confirm(`Revoke token "${token.name}"?`)) revoke.mutate(token.id) }} style={btnDanger}>
              <Icon name="delete" size={13} />Revoke
            </button>
          </div>
        ))}
        {!tokensQ.isLoading && tokens.length === 0 && (
          <div style={{ textAlign: 'center', padding: '32px 0', color: 'var(--text-tertiary)' }}>No API tokens created yet</div>
        )}
      </div>
    </>
  )
}

// ---------------------------------------------------------------------------
// AuditTab
// ---------------------------------------------------------------------------

function AuditTab() {
  const qc = useQueryClient()

  const statsQ = useQuery({
    queryKey: ['audit', 'stats'],
    queryFn: ({ signal }) => api.get<AuditStats>('/api/system/audit/stats', signal),
  })

  const chainQ = useQuery({
    queryKey: ['audit', 'chain'],
    queryFn: ({ signal }) => api.get<{ success: boolean; valid: boolean }>('/api/system/audit/verify-chain', signal),
  })

  if (statsQ.isLoading || chainQ.isLoading) return <Skeleton height={180} />
  if (statsQ.isError) return <ErrorState error={statsQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['audit'] })} />

  const stats = statsQ.data
  const chainOk = chainQ.data?.valid !== false

  return (
    <div style={{ maxWidth: 600 }}>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 16, marginBottom: 24 }}>
        <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '18px 22px' }}>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 8 }}>Total Entries</div>
          <div style={{ fontSize: 28, fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--primary)' }}>{stats?.total_entries ?? '—'}</div>
        </div>
        <div style={{ background: 'var(--bg-card)', border: `1px solid ${chainOk ? 'var(--success-border)' : 'var(--error-border)'}`, borderRadius: 'var(--radius-lg)', padding: '18px 22px' }}>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', marginBottom: 8 }}>Audit Chain</div>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Icon name={chainOk ? 'verified_user' : 'gpp_bad'} size={22} style={{ color: chainOk ? 'var(--success)' : 'var(--error)' }} />
            <span style={{ fontWeight: 700, color: chainOk ? 'var(--success)' : 'var(--error)' }}>{chainOk ? 'Valid' : 'Broken'}</span>
          </div>
        </div>
      </div>

      {stats?.last_entry && (
        <div style={{ padding: '12px 16px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
          Last entry: {fmtDate(stats.last_entry)}
        </div>
      )}

      <div style={{ marginTop: 16 }}>
        <button onClick={() => qc.invalidateQueries({ queryKey: ['audit'] })} style={btnGhost}><Icon name="refresh" size={14} />Refresh</button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// SecurityPage
// ---------------------------------------------------------------------------

type Tab = 'totp' | 'tokens' | 'audit'

export function SecurityPage() {
  const [tab, setTab] = useState<Tab>('totp')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'totp',   label: '2FA / TOTP',  icon: 'phonelink_lock' },
    { id: 'tokens', label: 'API Tokens',  icon: 'key' },
    { id: 'audit',  label: 'Audit Log',   icon: 'policy' },
  ]

  return (
    <div style={{ maxWidth: 860 }}>
      <div style={{ marginBottom: 28 }}>
        <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>Security</h1>
        <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>Two-factor authentication, API tokens and audit chain</p>
      </div>

      <div style={{ display: 'flex', gap: 4, marginBottom: 24, borderBottom: '1px solid var(--border)' }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} style={{ padding: '10px 20px', background: 'none', border: 'none', cursor: 'pointer', fontSize: 'var(--text-sm)', fontWeight: 600, color: tab === t.id ? 'var(--primary)' : 'var(--text-secondary)', borderBottom: tab === t.id ? '2px solid var(--primary)' : '2px solid transparent', marginBottom: -1, display: 'flex', alignItems: 'center', gap: 6 }}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'totp'   && <TOTPTab />}
      {tab === 'tokens' && <TokensTab />}
      {tab === 'audit'  && <AuditTab />}
    </div>
  )
}
