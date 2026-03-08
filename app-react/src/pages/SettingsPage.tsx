/**
 * pages/SettingsPage.tsx — System Settings (Phase 6)
 *
 * Tabs: General | NixOS
 *
 * General: hostname, timezone, MOTD — backed by /api/system/settings (key-value store)
 * NixOS: detect, validate, apply (with 60s confirm), generations list & rollback
 *
 * Calls:
 *   GET  /api/system/settings              → { success, settings: Record<string,string> }
 *   POST /api/system/settings              → { [key]: value, ... } (partial upsert)
 *   GET  /api/nixos/detect                 → { success, is_nixos, message }
 *   POST /api/nixos/validate               → { success, valid, errors[] }
 *   POST /api/nixos/apply   { flake_path, timeout_seconds } → { success }
 *   POST /api/nixos/confirm                → confirm applied generation
 *   GET  /api/nixos/generations            → { success, generations: Generation[] }
 *   POST /api/nixos/rollback { generation } → { success }
 */

import { useState, useEffect, useRef } from 'react'
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

interface SettingsResponse { success: boolean; settings: Record<string, string> }
interface NixOSDetect      { success: boolean; is_nixos: boolean; message?: string }
interface NixOSValidate    { success: boolean; valid: boolean; errors?: string[] }
interface Generation       { number: number; date: string; current: boolean; description?: string }
interface GenerationsResp  { success: boolean; generations: Generation[] }

// ---------------------------------------------------------------------------

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
      <label style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>{label}</label>
      {children}
      {hint && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{hint}</span>}
    </div>
  )
}

// ---------------------------------------------------------------------------
// GeneralTab
// ---------------------------------------------------------------------------

function GeneralTab() {
  const qc = useQueryClient()

  const settingsQ = useQuery({
    queryKey: ['system', 'settings'],
    queryFn:  ({ signal }) => api.get<SettingsResponse>('/api/system/settings', signal),
  })

  const [hostname, setHostname] = useState('')
  const [timezone, setTimezone] = useState('')
  const [motd,     setMotd]     = useState('')
  const [seeded,   setSeeded]   = useState(false)

  useEffect(() => {
    if (settingsQ.data?.settings && !seeded) {
      const s = settingsQ.data.settings
      setHostname(s['hostname'] ?? '')
      setTimezone(s['timezone'] ?? '')
      setMotd(s['motd'] ?? '')
      setSeeded(true)
    }
  }, [settingsQ.data, seeded])

  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, string> = {}
      if (hostname.trim()) body['hostname'] = hostname.trim()
      if (timezone.trim()) body['timezone'] = timezone.trim()
      body['motd'] = motd
      return api.post('/api/system/settings', body)
    },
    onSuccess: () => { toast.success('Settings saved'); qc.invalidateQueries({ queryKey: ['system', 'settings'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  // Common IANA timezones
  const TIMEZONES = [
    'UTC', 'Europe/Berlin', 'Europe/London', 'Europe/Paris', 'Europe/Rome',
    'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
    'Asia/Tokyo', 'Asia/Shanghai', 'Asia/Kolkata', 'Australia/Sydney',
  ]

  if (settingsQ.isLoading) return <Skeleton height={320} />
  if (settingsQ.isError)   return <ErrorState error={settingsQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['system', 'settings'] })} />

  return (
    <div style={{ maxWidth: 560, display: 'flex', flexDirection: 'column', gap: 20 }}>
      <Field label="Hostname" hint="Applied immediately via hostnamectl">
        <input value={hostname} onChange={e => setHostname(e.target.value)}
          placeholder="dplaneos" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
      </Field>

      <Field label="Timezone" hint="Applied immediately via timedatectl">
        {/* Combo: free-text with datalist for common zones */}
        <input value={timezone} onChange={e => setTimezone(e.target.value)}
          list="tz-list" placeholder="Europe/Berlin" className="input" />
        <datalist id="tz-list">
          {TIMEZONES.map(tz => <option key={tz} value={tz} />)}
        </datalist>
      </Field>

      <Field label="Message of the Day (MOTD)" hint="Shown on the dashboard and login page">
        <textarea value={motd} onChange={e => setMotd(e.target.value)}
          rows={4} placeholder="Welcome to D-PlaneOS"
          className="input" style={{ resize: 'vertical', lineHeight: 1.6, fontFamily: 'var(--font-ui)' }} />
      </Field>

      <div>
        <button onClick={() => save.mutate()} disabled={save.isPending} className="btn btn-primary">
          <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Settings'}
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// NixOSConfirmBanner — 60-second countdown
// ---------------------------------------------------------------------------

function NixOSConfirmBanner({ onConfirm, onDismiss }: { onConfirm: () => void; onDismiss: () => void }) {
  const [secs, setSecs] = useState(60)
  const timer = useRef<ReturnType<typeof setInterval> | null>(null)

  useEffect(() => {
    timer.current = setInterval(() => {
      setSecs(prev => {
        if (prev <= 1) { clearInterval(timer.current!); onDismiss(); return 0 }
        return prev - 1
      })
    }, 1000)
    return () => clearInterval(timer.current!)
  }, [onDismiss])

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 20px', background: 'rgba(251,191,36,0.1)', border: '1px solid rgba(251,191,36,0.35)', borderRadius: 'var(--radius-lg)', marginBottom: 20 }}>
      <Icon name="timer" size={22} style={{ color: 'rgba(251,191,36,0.9)', flexShrink: 0 }} />
      <div style={{ flex: 1 }}>
        <div style={{ fontWeight: 700, color: 'rgba(251,191,36,0.9)' }}>NixOS rebuild applied</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Auto-rolling back in {secs}s — confirm to keep this generation</div>
      </div>
      <button onClick={onConfirm} className="btn btn-primary" style={{ background: 'rgba(251,191,36,0.9)' }}>
        <Icon name="check" size={15} />Confirm
      </button>
      <button onClick={onDismiss} className="btn btn-ghost">Rollback</button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// NixOSTab
// ---------------------------------------------------------------------------

function NixOSTab() {
  const qc = useQueryClient()
  const [flakePath,      setFlakePath]      = useState('/etc/nixos')
  const [pendingConfirm, setPendingConfirm] = useState(false)
  const [validateResult, setValidateResult] = useState<NixOSValidate | null>(null)

  const detectQ = useQuery({
    queryKey: ['nixos', 'detect'],
    queryFn:  ({ signal }) => api.get<NixOSDetect>('/api/nixos/detect', signal),
  })

  const gensQ = useQuery({
    queryKey: ['nixos', 'generations'],
    queryFn:  ({ signal }) => api.get<GenerationsResp>('/api/nixos/generations', signal),
  })

  const validate = useMutation({
    mutationFn: () => api.post<NixOSValidate>('/api/nixos/validate', { flake_path: flakePath }),
    onSuccess: result => { setValidateResult(result); result.valid ? toast.success('Config is valid') : toast.error('Validation failed') },
    onError: (e: Error) => toast.error(e.message),
  })

  const apply = useMutation({
    mutationFn: () => api.post('/api/nixos/apply', { flake_path: flakePath, timeout_seconds: 120 }),
    onSuccess: () => { setPendingConfirm(true); toast.success('Rebuild applied — confirm within 60s'); qc.invalidateQueries({ queryKey: ['nixos', 'generations'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const confirm = useMutation({
    mutationFn: () => api.post('/api/nixos/confirm', {}),
    onSuccess: () => { toast.success('Generation confirmed'); setPendingConfirm(false) },
    onError: (e: Error) => toast.error(e.message),
  })

  const rollback = useMutation({
    mutationFn: (generation: number) => api.post('/api/nixos/rollback', { generation }),
    onSuccess: () => { toast.success('Rolled back'); qc.invalidateQueries({ queryKey: ['nixos', 'generations'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const isNixOS = detectQ.data?.is_nixos === true
  const gens    = gensQ.data?.generations ?? []

  if (detectQ.isLoading) return <Skeleton height={200} />

  if (!isNixOS) {
    return (
      <div style={{ display: 'flex', alignItems: 'center', gap: 16, padding: '24px 28px', background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', maxWidth: 500 }}>
        <Icon name="info" size={24} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
        <div>
          <div style={{ fontWeight: 700, marginBottom: 4 }}>Not running on NixOS</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            {detectQ.data?.message ?? 'NixOS features are only available on NixOS systems.'}
          </div>
        </div>
      </div>
    )
  }

  return (
    <div style={{ maxWidth: 720, display: 'flex', flexDirection: 'column', gap: 24 }}>
      {pendingConfirm && (
        <NixOSConfirmBanner
          onConfirm={() => confirm.mutate()}
          onDismiss={() => setPendingConfirm(false)}
        />
      )}

      {/* Apply config */}
      <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '20px 24px' }}>
        <div style={{ fontWeight: 700, marginBottom: 16 }}>Apply Configuration</div>
        <div style={{ display: 'flex', gap: 10, marginBottom: 12 }}>
          <input value={flakePath} onChange={e => setFlakePath(e.target.value)}
            className="input" style={{ flex: 1, fontFamily: 'var(--font-mono)' }}
            placeholder="/etc/nixos" />
        </div>

        {validateResult && (
          <div style={{ marginBottom: 14, padding: '12px 16px', background: validateResult.valid ? 'var(--success-bg)' : 'var(--error-bg)', border: `1px solid ${validateResult.valid ? 'var(--success-border)' : 'var(--error-border)'}`, borderRadius: 'var(--radius-sm)' }}>
            <div style={{ display: 'flex', gap: 8, marginBottom: validateResult.errors?.length ? 8 : 0 }}>
              <Icon name={validateResult.valid ? 'check_circle' : 'error'} size={16} style={{ color: validateResult.valid ? 'var(--success)' : 'var(--error)', flexShrink: 0 }} />
              <span style={{ fontWeight: 600, fontSize: 'var(--text-sm)', color: validateResult.valid ? 'var(--success)' : 'var(--error)' }}>
                {validateResult.valid ? 'Configuration is valid' : 'Validation failed'}
              </span>
            </div>
            {validateResult.errors && validateResult.errors.length > 0 && (
              <ul style={{ margin: '0 0 0 24px', padding: 0, fontSize: 'var(--text-xs)', color: 'var(--error)' }}>
                {validateResult.errors.map((err, i) => <li key={i}>{err}</li>)}
              </ul>
            )}
          </div>
        )}

        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          <button onClick={() => validate.mutate()} disabled={validate.isPending} className="btn btn-ghost">
            <Icon name="fact_check" size={15} />{validate.isPending ? 'Validating…' : 'Validate'}
          </button>
          <button onClick={() => apply.mutate()} disabled={apply.isPending} className="btn btn-primary">
            <Icon name="rocket_launch" size={15} />{apply.isPending ? 'Rebuilding…' : 'nixos-rebuild switch'}
          </button>
        </div>
      </div>

      {/* Generations */}
      <div>
        <div style={{ fontWeight: 700, marginBottom: 12 }}>Generations</div>
        {gensQ.isLoading && <Skeleton height={120} />}
        {gensQ.isError   && <ErrorState error={gensQ.error} />}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          {gens.map(gen => (
            <div key={gen.number} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '12px 18px', background: 'var(--bg-card)', border: `1px solid ${gen.current ? 'rgba(138,156,255,0.3)' : 'var(--border)'}`, borderRadius: 'var(--radius-md)' }}>
              <div style={{ width: 36, height: 36, borderRadius: 'var(--radius-sm)', background: gen.current ? 'var(--primary-bg)' : 'var(--surface)', border: `1px solid ${gen.current ? 'rgba(138,156,255,0.25)' : 'var(--border)'}`, display: 'flex', alignItems: 'center', justifyContent: 'center', fontWeight: 700, fontSize: 'var(--text-sm)', color: gen.current ? 'var(--primary)' : 'var(--text-secondary)', flexShrink: 0 }}>
                {gen.number}
              </div>
              <div style={{ flex: 1 }}>
                <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)', display: 'flex', alignItems: 'center', gap: 8 }}>
                  Generation {gen.number}
                  {gen.current && <span className="badge badge-primary">CURRENT</span>}
                </div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 2 }}>
                  {gen.date}{gen.description ? ` — ${gen.description}` : ''}
                </div>
              </div>
              {!gen.current && (
                <button onClick={() => { if (window.confirm(`Roll back to generation ${gen.number}?`)) rollback.mutate(gen.number) }}
                  disabled={rollback.isPending} className="btn btn-danger">
                  <Icon name="history" size={14} />Rollback
                </button>
              )}
            </div>
          ))}
          {!gensQ.isLoading && gens.length === 0 && (
            <div style={{ textAlign: 'center', padding: '32px 0', color: 'var(--text-tertiary)' }}>No generations found</div>
          )}
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// SettingsPage
// ---------------------------------------------------------------------------

type Tab = 'general' | 'nixos'

export function SettingsPage() {
  const [tab, setTab] = useState<Tab>('general')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'general', label: 'General',  icon: 'tune' },
    { id: 'nixos',   label: 'NixOS',    icon: 'terminal' },
  ]

  return (
    <div style={{ maxWidth: 860 }}>
      <div className="page-header">
        <h1 className="page-title">System Settings</h1>
        <p className="page-subtitle">Hostname, timezone, MOTD and NixOS configuration</p>
      </div>

      <div className="tabs-underline">
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'general' && <GeneralTab />}
      {tab === 'nixos'   && <NixOSTab />}
    </div>
  )
}
