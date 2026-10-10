/**
 * pages/SettingsPage.tsx - System Settings
 *
 * Tabs: General | NixOS | SSO / OIDC | Security | License | Features | Maintenance
 *
 * General: hostname, timezone, MOTD - backed by /api/system/settings (key-value store)
 * NixOS: detect, validate, apply (with 60s confirm), generations list & rollback
 * Security: AES-256-GCM key rotation  POST /api/system/secrets/rotate
 * License: activation, status, CE integration
 * Features: enable/disable optional features, hardware requirements
 * Maintenance: DB backup/restore      GET /api/system/db/backup, POST /api/system/db/restore
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
 *   POST /api/system/secrets/rotate        → { success, rotated_count: number }
 *   GET  /api/system/features              → { success, features: Feature[] }
 *   POST /api/system/features/:id/enable   → { success }
 *   POST /api/system/features/:id/disable  → { success }
 *   GET  /api/system/db/backup             → octet-stream download
 *   POST /api/system/db/restore            → { success }
 */

import { useState, useEffect } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, getSessionId, getUsername, getCsrfToken } from '@/lib/api'
import { TuningCard, SecretsStatusCard, NixOSBackupButton } from '@/components/settings/SystemCards'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { pluginSettingsInject } from '../plugins'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface SettingsResponse { success: boolean; settings: Record<string, string> }
interface NixOSDetect      { success: boolean; is_nixos: boolean; message?: string }
interface NixOSValidate    { success: boolean; valid: boolean; errors?: string[] }
interface Generation       { number: number; date: string; current: boolean; description?: string }
interface GenerationsResp  { success: boolean; generations: Generation[] }

interface OIDCConfig {
  enabled?:        boolean
  issuer?:         string
  client_id?:      string
  scopes?:         string
  allowed_algs?:   string
  button_label?:   string
  auto_provision?: boolean
  default_role_id?: number | null
  group_claim?:    string
}

interface OIDCRole { id: number; name: string }
interface OIDCRolesResponse { success: boolean; roles: OIDCRole[] }

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

  const [dirty, setDirty] = useState<{ hostname?: string; timezone?: string; motd?: string; licenseKey?: string }>({})
  const [autoReconcile, setAutoReconcile] = useState(false)

  const s = settingsQ.data?.settings
  const hostname   = dirty.hostname   ?? s?.['hostname']    ?? ''
  const timezone   = dirty.timezone   ?? s?.['timezone']    ?? ''
  const motd       = dirty.motd       ?? s?.['motd']        ?? ''
  const licenseKey = dirty.licenseKey ?? s?.['license_key'] ?? ''

  const setHostname   = (v: string) => setDirty(p => ({ ...p, hostname: v }))
  const setTimezone   = (v: string) => setDirty(p => ({ ...p, timezone: v }))
  const setMotd       = (v: string) => setDirty(p => ({ ...p, motd: v }))
  const setLicenseKey = (v: string) => setDirty(p => ({ ...p, licenseKey: v }))

  const diffQ = useQuery({
    queryKey: ['nixos', 'diff-intent'],
    queryFn: () => api.get<{ changes: Array<{ path: string; to: unknown }> }>('/api/nixos/diff-intent'),
  })

  const reconcileM = useMutation({
    mutationFn: () => api.post('/api/nixos/reconcile', {}),
  })

  const save = useMutation({
    mutationFn: () => {
      const body: Record<string, string> = {}
      if (hostname.trim()) body['hostname'] = hostname.trim()
      if (timezone.trim()) body['timezone'] = timezone.trim()
      body['motd'] = motd
      body['license_key'] = licenseKey
      return api.post('/api/system/settings', body)
    },
    onSuccess: async () => {
      setDirty({})
      qc.invalidateQueries({ queryKey: ['system', 'settings'] })
      qc.invalidateQueries({ queryKey: ['nixos', 'diff-intent'] })

      if (autoReconcile) {
        toast.info('Settings stored. Reconciling system...')
        try {
          await reconcileM.mutateAsync()
          toast.success('System settings applied to Nix generation')
        } catch (err: unknown) {
          toast.error(`Reconciliation failed: ${err instanceof Error ? err.message : String(err)}`)
        }
      } else {
        toast.success('Settings staged. Remember to Reconcile to apply changes.')
      }
    },
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
      <Field label="Hostname" hint="Staged in dplane-state.json - reconciles etc/hostname">
        <div style={{ position: 'relative' }}>
          <input value={hostname} onChange={e => setHostname(e.target.value)}
            placeholder="dplaneos" className="input" style={{ width: '100%', fontFamily: 'var(--font-mono)' }} />
          {diffQ.data?.changes.find(c => c.path === 'hostname') && (
            <span className="badge badge-warning" style={{ position: 'absolute', right: -70, top: 10, fontSize: 8 }}>STAGED</span>
          )}
        </div>
      </Field>

      <Field label="Timezone" hint="Staged in dplane-state.json - reconciles NixOS time.timeZone">
        {/* Combo: free-text with datalist for common zones */}
        <div style={{ position: 'relative' }}>
          <input value={timezone} onChange={e => setTimezone(e.target.value)}
            list="tz-list" placeholder="Europe/Berlin" className="input" style={{ width: '100%' }} />
          {diffQ.data?.changes.find(c => c.path === 'timezone') && (
            <span className="badge badge-warning" style={{ position: 'absolute', right: -70, top: 10, fontSize: 8 }}>STAGED</span>
          )}
        </div>
        <datalist id="tz-list">
          {TIMEZONES.map(tz => <option key={tz} value={tz} />)}
        </datalist>
      </Field>

      <Field label="Message of the Day (MOTD)" hint="Shown on the dashboard and login page">
        <textarea value={motd} onChange={e => setMotd(e.target.value)}
          rows={4} placeholder="Welcome to DPlaneOS"
          className="input" style={{ resize: 'vertical', lineHeight: 1.6, fontFamily: 'var(--font-ui)' }} />
      </Field>

      <Field label="Enterprise License Key" hint="Unlocks cryptographic compliance engine and audit automation">
        <input type="password" value={licenseKey} onChange={e => setLicenseKey(e.target.value)}
          placeholder="Unlicensed" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>
          Activates optional add-ons (Compliance Engine, advanced HA features). Leave blank for the standard open-source build.
        </span>
      </Field>

      {pluginSettingsInject({
        hostname, setHostname,
        timezone, setTimezone,
        motd, setMotd,
        licenseKey, setLicenseKey
      }) || null}

      <div>
        <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginBottom: 16 }}>
          <input type="checkbox" checked={autoReconcile} onChange={e => setAutoReconcile(e.target.checked)} />
          Apply & Reconcile immediately (triggers Nix rebuild)
        </label>
        <button onClick={() => save.mutate()} disabled={save.isPending} className="btn btn-primary">
          <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Settings'}
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// NixOSConfirmBanner - 60-second countdown
// ---------------------------------------------------------------------------

// The rollback timer runs on the server (GET /api/nixos/watchdog): the banner
// follows it, so it is still there after a reload or in another browser.
function NixOSConfirmBanner({ deadline, onConfirm, confirming }: { deadline: string; onConfirm: () => void; confirming: boolean }) {
  const left = () => Math.max(0, Math.round((new Date(deadline).getTime() - Date.now()) / 1000))
  const [secs, setSecs] = useState(left)

  useEffect(() => {
    const t = setInterval(() => setSecs(left()), 1000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deadline])

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 20px', background: 'var(--warning-bg)', border: '1px solid var(--warning-border)', borderRadius: 'var(--radius-lg)', marginBottom: 20 }}>
      <Icon name="timer" size={22} style={{ color: 'var(--warning)', flexShrink: 0 }} />
      <div style={{ flex: 1 }}>
        <div style={{ fontWeight: 700, color: 'var(--warning)' }}>NixOS rebuild applied</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
          Rolls back automatically in {secs}s unless confirmed. Confirm if the system still works as expected.
        </div>
      </div>
      <button onClick={onConfirm} disabled={confirming} className="btn btn-primary" style={{ background: 'var(--warning)' }}>
        <Icon name="check" size={15} />{confirming ? 'Confirming…' : 'Keep this configuration'}
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// NixOSTab
// ---------------------------------------------------------------------------

function NixOSTab() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [flakePath,      setFlakePath]      = useState('/etc/nixos')
  const [validateResult, setValidateResult] = useState<NixOSValidate | null>(null)

  const detectQ = useQuery({
    queryKey: ['nixos', 'detect'],
    queryFn:  ({ signal }) => api.get<NixOSDetect>('/api/nixos/detect', signal),
  })

  const gensQ = useQuery({
    queryKey: ['nixos', 'generations'],
    queryFn:  ({ signal }) => api.get<GenerationsResp>('/api/nixos/generations', signal),
  })

  const watchdogQ = useQuery({
    queryKey: ['nixos', 'watchdog'],
    queryFn: ({ signal }) => api.get<{ success: boolean; watchdog_active: boolean; deadline: string; remaining_sec: number }>('/api/nixos/watchdog', signal),
    refetchInterval: q => (q.state.data?.watchdog_active ? 3_000 : 20_000),
  })

  const validate = useMutation({
    mutationFn: () => api.post<NixOSValidate>('/api/nixos/validate', { flake_path: flakePath }),
    onSuccess: result => { setValidateResult(result); if (result.valid) { toast.success('Config is valid') } else { toast.error('Validation failed') } },
    onError: (e: Error) => toast.error(e.message),
  })

  const apply = useMutation({
    mutationFn: () => api.post('/api/nixos/apply', { flake_path: flakePath, timeout_seconds: 120 }),
    onSuccess: () => { toast.success('Rebuild applied - confirm to keep it'); qc.invalidateQueries({ queryKey: ['nixos'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const confirmGeneration = useMutation({
    mutationFn: () => api.post('/api/nixos/confirm', {}),
    onSuccess: () => { toast.success('Generation confirmed'); qc.invalidateQueries({ queryKey: ['nixos'] }) },
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
      <div className="card" style={{ display: 'flex', alignItems: 'center', gap: 16, padding: '24px 28px', borderRadius: 'var(--radius-xl)', maxWidth: 500 }}>
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
      {watchdogQ.data?.watchdog_active && (
        <NixOSConfirmBanner
          deadline={watchdogQ.data.deadline}
          confirming={confirmGeneration.isPending}
          onConfirm={() => confirmGeneration.mutate()}
        />
      )}

      {/* Apply config */}
      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px' }}>
        <div style={{ fontWeight: 700, marginBottom: 16 }}>Apply Configuration</div>
        <div style={{ display: 'flex', gap: 10, marginBottom: 12 }}>
          <input value={flakePath} onChange={e => setFlakePath(e.target.value)}
            className="input" style={{ flex: 1, fontFamily: 'var(--font-mono)' }}
            placeholder="/etc/nixos" />
          <NixOSBackupButton />
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
                  {gen.date}{gen.description ? ` - ${gen.description}` : ''}
                </div>
              </div>
              {!gen.current && (
                <button onClick={async () => { if (await confirm({ title: `Roll back to generation ${gen.number}?`, message: 'The system will switch to this NixOS generation. Current generation will be kept as a fallback.', danger: true, confirmLabel: 'Rollback' })) rollback.mutate(gen.number) }}
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
      <ConfirmDialog />
    </div>
  )
}

// ---------------------------------------------------------------------------
// OIDCTab
// ---------------------------------------------------------------------------

function OIDCTab() {
  const qc = useQueryClient()

  const configQ = useQuery({
    queryKey: ['oidc', 'config'],
    queryFn: ({ signal }) => api.get<OIDCConfig & { success: boolean }>('/api/auth/oidc/config', signal),
  })

  const rolesQ = useQuery({
    queryKey: ['rbac', 'roles'],
    queryFn: ({ signal }) => api.get<OIDCRolesResponse>('/api/rbac/roles', signal),
  })

  const [cfg, setCfg] = useState<OIDCConfig | null>(null)
  const [secret, setSecret] = useState('')

  const formCfg: OIDCConfig = cfg ?? configQ.data ?? {}

  function set<K extends keyof OIDCConfig>(k: K, v: OIDCConfig[K]) {
    setCfg(prev => ({ ...(prev ?? configQ.data ?? {}), [k]: v }))
  }

  const save = useMutation({
    mutationFn: () => api.post('/api/auth/oidc/config', { ...formCfg, client_secret: secret }),
    onSuccess: () => {
      toast.success('OIDC configuration saved')
      setSecret('')
      qc.invalidateQueries({ queryKey: ['oidc', 'config'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  if (configQ.isLoading) return <Skeleton height={360} />
  if (configQ.isError) return <ErrorState error={configQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['oidc', 'config'] })} />

  const roles = rolesQ.data?.roles ?? []

  return (
    <div style={{ maxWidth: 620, display: 'flex', flexDirection: 'column', gap: 24 }}>
      <div className="card" style={{ padding: 20 }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 20 }}>
          <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 600, margin: 0 }}>SSO Provider</h3>
          <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
            <input type="checkbox" checked={!!formCfg.enabled} onChange={e => set('enabled', e.target.checked)}
              style={{ width: 16, height: 16, accentColor: 'var(--primary)' }} />
            <span style={{ fontWeight: 600 }}>Enable SSO</span>
          </label>
        </div>

        <div style={{ display: 'grid', gap: 16 }}>
          <Field label="Issuer URL" hint="Discovery document fetched from issuer + /.well-known/openid-configuration">
            <input value={formCfg.issuer ?? ''} onChange={e => set('issuer', e.target.value)}
              placeholder="https://accounts.example.com"
              className="input" style={{ fontFamily: 'var(--font-mono)' }} />
          </Field>

          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
            <Field label="Client ID">
              <input value={formCfg.client_id ?? ''} onChange={e => set('client_id', e.target.value)}
                placeholder="dplaneos" className="input" />
            </Field>
            <Field label="Client Secret" hint="Leave empty to keep existing">
              <input type="password" value={secret} onChange={e => setSecret(e.target.value)}
                placeholder="unchanged" className="input" autoComplete="new-password" />
            </Field>
          </div>

          <Field label="Button Label" hint="Text shown on the SSO login button">
            <input value={formCfg.button_label ?? ''} onChange={e => set('button_label', e.target.value)}
              placeholder="Sign in with SSO" className="input" />
          </Field>

          <details>
            <summary style={{ cursor: 'pointer', padding: '10px 0', fontSize: 'var(--text-sm)', fontWeight: 600 }}>Advanced Settings</summary>
            <div style={{ display: 'grid', gap: 12, paddingTop: 12 }}>
              <Field label="Scopes" hint="Space-separated OpenID Connect scopes">
                <input value={formCfg.scopes ?? ''} onChange={e => set('scopes', e.target.value)}
                  placeholder="openid email profile" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
              </Field>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
                <Field label="Allowed Algorithms" hint="Comma-separated, e.g. RS256,ES256">
                  <input value={formCfg.allowed_algs ?? ''} onChange={e => set('allowed_algs', e.target.value)}
                    placeholder="RS256" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
                </Field>
                <Field label="Group Claim" hint="JWT claim containing group memberships">
                  <input value={formCfg.group_claim ?? ''} onChange={e => set('group_claim', e.target.value)}
                    placeholder="groups" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
                </Field>
              </div>
            </div>
          </details>
        </div>
      </div>

      <div className="card" style={{ padding: 20 }}>
        <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 600, marginBottom: 16 }}>User Provisioning</h3>
        <div style={{ display: 'grid', gap: 16 }}>
          <label style={{ display: 'flex', alignItems: 'flex-start', gap: 10, cursor: 'pointer' }}>
            <input type="checkbox" checked={!!formCfg.auto_provision} onChange={e => set('auto_provision', e.target.checked)}
              style={{ width: 16, height: 16, marginTop: 2, accentColor: 'var(--primary)', flexShrink: 0 }} />
            <div>
              <span style={{ fontWeight: 600, display: 'block' }}>Auto-provision accounts</span>
              <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                Create a local account on first login when no existing account matches by email
              </span>
            </div>
          </label>

          <Field label="Default Role" hint="Assigned to newly provisioned accounts on first login">
            <select
              value={formCfg.default_role_id ?? ''}
              onChange={e => set('default_role_id', e.target.value ? Number(e.target.value) : null)}
              className="input">
              <option value="">None</option>
              {roles.map(r => <option key={r.id} value={r.id}>{r.name}</option>)}
            </select>
          </Field>
        </div>
      </div>

      <div>
        <button onClick={() => save.mutate()} disabled={save.isPending} className="btn btn-primary">
          <Icon name="save" size={15} />{save.isPending ? 'Saving...' : 'Save Configuration'}
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ---------------------------------------------------------------------------
// SecurityTab - encryption key rotation
// ---------------------------------------------------------------------------

function SecurityTab() {
  const [confirming, setConfirming] = useState(false)

  const rotate = useMutation({
    mutationFn: () => api.post<{ success: boolean; rotated_count: number }>('/api/system/secrets/rotate', {}),
    onSuccess: (data) => {
      setConfirming(false)
      toast.success(`Key rotated. ${data.rotated_count} secret${data.rotated_count === 1 ? '' : 's'} re-encrypted.`)
    },
    onError: (e: Error) => {
      setConfirming(false)
      toast.error(e.message)
    },
  })

  return (
    <div style={{ maxWidth: 620, display: 'flex', flexDirection: 'column', gap: 20, paddingTop: 24 }}>
      <div className="card" style={{ padding: 20 }}>
        <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 600, marginBottom: 8 }}>Encryption Key Rotation</h3>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 4 }}>
          All secrets at rest (SMTP password, LDAP credentials, TOTP seeds, git tokens, OIDC client secret, Telegram bot token)
          are encrypted with AES-256-GCM under a 32-byte key stored at{' '}
          <code style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>/var/lib/dplaneos/secrets.key</code>.
        </p>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 16 }}>
          Rotation generates a new key, re-encrypts every stored secret in a single atomic transaction,
          then atomically replaces the key file. Use this if the key file may have been compromised.
        </p>

        {!confirming ? (
          <button onClick={() => setConfirming(true)} className="btn btn-ghost">
            <Icon name="key" size={14} /> Rotate Encryption Key
          </button>
        ) : (
          <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
              Generate a new key and re-encrypt all secrets now?
            </span>
            <button
              onClick={() => rotate.mutate()}
              disabled={rotate.isPending}
              className="btn btn-primary"
              style={{ minWidth: 90 }}
            >
              <Icon name="key" size={14} />{rotate.isPending ? 'Rotating...' : 'Confirm Rotate'}
            </button>
            <button onClick={() => setConfirming(false)} disabled={rotate.isPending} className="btn btn-ghost">
              Cancel
            </button>
          </div>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// MaintenanceTab - database backup / restore
// ---------------------------------------------------------------------------

function MaintenanceTab() {
  const [restoreFile, setRestoreFile] = useState<File | null>(null)
  const [restoring, setRestoring] = useState(false)

  function handleDownload() {
    const a = document.createElement('a')
    a.href = '/api/system/db/backup'
    a.click()
  }

  async function handleRestore() {
    if (!restoreFile) return
    setRestoring(true)
    try {
      const form = new FormData()
      form.append('backup', restoreFile)
      const res = await fetch('/api/system/db/restore', {
        method: 'POST',
        headers: { 'X-Session-ID': getSessionId() ?? '', 'X-User': getUsername() ?? '', 'X-CSRF-Token': getCsrfToken() },
        body: form,
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({ error: res.statusText }))
        throw new Error(body.error ?? res.statusText)
      }
      toast.success('Database restored. Reload the page.')
      setRestoreFile(null)
    } catch (e: unknown) {
      toast.error(e instanceof Error ? e.message : 'Restore failed')
    } finally {
      setRestoring(false)
    }
  }

  return (
    <div style={{ maxWidth: 620, display: 'flex', flexDirection: 'column', gap: 20, paddingTop: 24 }}>
      <TuningCard />
      <SecretsStatusCard />
      <div className="card" style={{ padding: 20 }}>
        <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 600, marginBottom: 8 }}>Database Backup</h3>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 16 }}>
          Downloads a full <code style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>pg_dump</code> of
          the control-plane database (sessions, audit logs, cluster state, all configuration). ZFS pool state is
          intentionally excluded - it lives in <code style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>state.yaml</code>.
        </p>
        <button onClick={handleDownload} className="btn btn-primary">
          <Icon name="download" size={14} /> Download Backup
        </button>
      </div>

      <div className="card" style={{ padding: 20 }}>
        <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 600, marginBottom: 8 }}>Database Restore</h3>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 16 }}>
          Restores the control-plane database from a backup file. The current database is overwritten.
          Reload the page after restore completes.
        </p>
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <label className="btn btn-ghost" style={{ cursor: 'pointer' }}>
            <Icon name="upload" size={14} />
            {restoreFile ? restoreFile.name : 'Choose .dump file'}
            <input
              type="file"
              accept=".dump"
              style={{ display: 'none' }}
              onChange={e => setRestoreFile(e.target.files?.[0] ?? null)}
            />
          </label>
          {restoreFile && (
            <button
              onClick={handleRestore}
              disabled={restoring}
              className="btn btn-primary"
              style={{ minWidth: 100 }}
            >
              <Icon name="database" size={14} />{restoring ? 'Restoring...' : 'Restore'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// LicenseTab
// ---------------------------------------------------------------------------

interface LicenseStatus {
  active: boolean
  customer?: string
  audits_limit?: number
  audits_remaining?: number
  expires_at?: string
  ce_installed?: boolean
  ce_running?: boolean
}

function LicenseTab() {
  const qc = useQueryClient()
  const [licenseKey, setLicenseKey] = useState('')
  const [isActivating, setIsActivating] = useState(false)

  const licenseQ = useQuery({
    queryKey: ['license', 'status'],
    queryFn: ({ signal }) => api.get<LicenseStatus>('/api/system/license/status', signal),
    refetchInterval: 30000,
  })

  const activate = useMutation({
    mutationFn: (key: string) => api.post('/api/system/license/activate', { license_key: key }),
    onSuccess: () => {
      toast.success('License activated. Enterprise features loading...')
      setLicenseKey('')
      qc.invalidateQueries({ queryKey: ['license', 'status'] })
    },
    onError: (e: Error) => toast.error('Activation failed: ' + e.message),
  })

  const handleActivate = async () => {
    if (!licenseKey.trim()) {
      toast.error('Enter a license key')
      return
    }
    setIsActivating(true)
    try {
      await activate.mutateAsync(licenseKey)
    } finally {
      setIsActivating(false)
    }
  }

  const status = licenseQ.data
  const isExpired = status?.active === false && status?.expires_at

  return (
    <div style={{ maxWidth: 620, display: 'flex', flexDirection: 'column', gap: 20, paddingTop: 24 }}>
      {status?.active ? (
        <div className="card" style={{ padding: 20, borderLeft: '4px solid var(--success)', background: 'rgba(34, 197, 94, 0.05)' }}>
          <div style={{ fontWeight: 700, marginBottom: 12, color: 'var(--success)' }}>Enterprise License Active</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', display: 'grid', gap: 6 }}>
            <div>Customer: {status.customer}</div>
            {status.audits_limit === -1 ? (
              <div>Audits: Unlimited</div>
            ) : (
              <div>Audits: {status.audits_remaining} remaining of {status.audits_limit}</div>
            )}
            <div>Expires: {status.expires_at === 'never' ? 'Never' : status.expires_at}</div>
            <div>Compliance Engine: {status.ce_installed ? (status.ce_running ? 'Running' : 'Installed (not running)') : 'Not installed'}</div>
          </div>
        </div>
      ) : isExpired ? (
        <div className="card" style={{ padding: 20, borderLeft: '4px solid var(--error)', background: 'rgba(200, 0, 0, 0.05)' }}>
          <div style={{ fontWeight: 700, marginBottom: 12, color: 'var(--error)' }}>License Expired</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 16 }}>
            Expired on {status.expires_at}. To renew, enter a new license key below.
          </div>
          <div style={{ display: 'flex', gap: 8 }}>
            <input
              type="password"
              placeholder="Paste renewal key"
              value={licenseKey}
              onChange={e => setLicenseKey(e.target.value)}
              disabled={isActivating}
              style={{ flex: 1, padding: '8px 12px', border: '1px solid var(--border)', borderRadius: 4 }}
            />
            <button onClick={handleActivate} disabled={isActivating || !licenseKey.trim()} className="btn btn-primary">
              {isActivating ? 'Activating...' : 'Activate'}
            </button>
          </div>
        </div>
      ) : (
        <div className="card" style={{ padding: 20, borderLeft: '4px solid var(--border)' }}>
          <div style={{ fontWeight: 700, marginBottom: 12 }}>Enterprise License</div>
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 16 }}>
            Status: Not activated
          </div>
          <div style={{ display: 'flex', gap: 8, marginBottom: 16 }}>
            <input
              type="password"
              placeholder="Paste license key"
              value={licenseKey}
              onChange={e => setLicenseKey(e.target.value)}
              disabled={isActivating}
              style={{ flex: 1, padding: '8px 12px', border: '1px solid var(--border)', borderRadius: 4 }}
            />
            <button onClick={handleActivate} disabled={isActivating || !licenseKey.trim()} className="btn btn-primary">
              {isActivating ? 'Activating...' : 'Activate'}
            </button>
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            For licensing, refer to the DPlaneOS documentation or contact the maintainer.
          </div>
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// FeaturesTab
// ---------------------------------------------------------------------------

interface Feature {
  id: string
  name: string
  description: string
  state: 'disabled' | 'beta' | 'stable' | 'deprecated'
  requires_hardware?: string
}

interface FeaturesResponse {
  success: boolean
  features: Feature[]
}

function FeaturesTab() {
  const qc = useQueryClient()

  const featuresQ = useQuery({
    queryKey: ['system', 'features'],
    queryFn: ({ signal }) => api.get<FeaturesResponse>('/api/system/features', signal),
  })

  const enable = useMutation({
    mutationFn: (featureId: string) =>
      api.post(`/api/system/features/${featureId}/enable`, { new_state: 'stable' }),
    onSuccess: () => {
      toast.success('Feature enabled')
      qc.invalidateQueries({ queryKey: ['system', 'features'] })
    },
    onError: (e: Error) => toast.error('Failed to enable: ' + e.message),
  })

  const disable = useMutation({
    mutationFn: (featureId: string) =>
      api.post(`/api/system/features/${featureId}/disable`, {}),
    onSuccess: () => {
      toast.success('Feature disabled')
      qc.invalidateQueries({ queryKey: ['system', 'features'] })
    },
    onError: (e: Error) => toast.error('Failed to disable: ' + e.message),
  })

  if (featuresQ.isLoading) {
    return <Skeleton />
  }

  if (featuresQ.isError || !featuresQ.data) {
    return <ErrorState error="Failed to load features" />
  }

  const features = featuresQ.data.features || []
  const disabled = features.filter(f => f.state === 'disabled')
  const enabled = features.filter(f => f.state !== 'disabled')

  return (
    <div style={{ maxWidth: 620, display: 'flex', flexDirection: 'column', gap: 20, paddingTop: 24 }}>
      {enabled.length > 0 && (
        <div>
          <h3 style={{ fontSize: 'var(--text-md)', fontWeight: 600, marginBottom: 12 }}>Enabled Features</h3>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {enabled.map(f => (
              <div key={f.id} className="card" style={{ padding: 16, display: 'flex', justifyContent: 'space-between', alignItems: 'start' }}>
                <div style={{ flex: 1 }}>
                  <div style={{ fontWeight: 600, marginBottom: 4 }}>{f.name}</div>
                  <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{f.description}</div>
                  {f.state === 'beta' && (
                    <div style={{ fontSize: 'var(--text-xs)', color: 'var(--warning)', marginTop: 6 }}>
                      ⚠ Beta: Not recommended for production
                    </div>
                  )}
                  {f.state === 'deprecated' && (
                    <div style={{ fontSize: 'var(--text-xs)', color: 'var(--error)', marginTop: 6 }}>
                      ⚠ Deprecated: Use a newer feature instead
                    </div>
                  )}
                </div>
                <button
                  onClick={() => disable.mutate(f.id)}
                  disabled={disable.isPending}
                  className="btn btn-ghost"
                  style={{ minWidth: 80 }}
                >
                  {disable.isPending ? 'Disabling...' : 'Disable'}
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {disabled.length > 0 && (
        <div>
          <h3 style={{ fontSize: 'var(--text-md)', fontWeight: 600, marginBottom: 12 }}>
            {enabled.length > 0 ? 'Available Features' : 'Features'}
          </h3>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {disabled.map(f => (
              <div key={f.id} className="card" style={{ padding: 16, display: 'flex', justifyContent: 'space-between', alignItems: 'start', opacity: f.requires_hardware ? 0.6 : 1 }}>
                <div style={{ flex: 1 }}>
                  <div style={{ fontWeight: 600, marginBottom: 4 }}>{f.name}</div>
                  <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{f.description}</div>
                  {f.requires_hardware && (
                    <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 6 }}>
                      Requires: {f.requires_hardware}
                    </div>
                  )}
                </div>
                <button
                  onClick={() => enable.mutate(f.id)}
                  disabled={enable.isPending || !!f.requires_hardware}
                  className="btn btn-primary"
                  style={{ minWidth: 80 }}
                  title={f.requires_hardware ? `Your hardware doesn't support this feature` : ''}
                >
                  {enable.isPending ? 'Enabling...' : 'Enable'}
                </button>
              </div>
            ))}
          </div>
        </div>
      )}

      {features.length === 0 && (
        <div className="card" style={{ padding: 20, textAlign: 'center', color: 'var(--text-secondary)' }}>
          No features found
        </div>
      )}
    </div>
  )
}

// SettingsPage
// ---------------------------------------------------------------------------

type Tab = 'general' | 'nixos' | 'oidc' | 'security' | 'license' | 'features' | 'maintenance'

export function SettingsPage() {
  const [tab, setTab] = useState<Tab>('general')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'general',     label: 'General',     icon: 'tune' },
    { id: 'nixos',       label: 'NixOS',       icon: 'terminal' },
    { id: 'oidc',        label: 'SSO / OIDC',  icon: 'key' },
    { id: 'security',    label: 'Security',    icon: 'lock' },
    { id: 'license',     label: 'License',     icon: 'card_membership' },
    { id: 'features',    label: 'Features',    icon: 'extension' },
    { id: 'maintenance', label: 'Maintenance', icon: 'database' },
  ]

  return (
    <div style={{ maxWidth: 860 }}>
      <div className="page-header">
        <h1 className="page-title">System Settings</h1>
        <p className="page-subtitle">Hostname, timezone, MOTD, NixOS configuration, SSO, features, and maintenance</p>
      </div>

      <div className="tabs-underline">
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'general'     && <GeneralTab />}
      {tab === 'nixos'       && <NixOSTab />}
      {tab === 'oidc'        && <OIDCTab />}
      {tab === 'security'    && <SecurityTab />}
      {tab === 'license'     && <LicenseTab />}
      {tab === 'features'    && <FeaturesTab />}
      {tab === 'maintenance' && <MaintenanceTab />}
    </div>
  )
}

