/**
 * pages/DirectoryPage.tsx — LDAP / Directory Service (Phase 5)
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/ldap/config              → { success, ...LdapConfig }
 *   POST /api/ldap/config              → save config
 *   POST /api/ldap/test                → { success, message }
 *   GET  /api/ldap/status              → { success, enabled, last_test_ok, last_sync }
 *   POST /api/ldap/sync                → trigger sync
 *   POST /api/ldap/search-user         → { query } → { success, users }
 *   GET  /api/ldap/mappings            → { success, mappings: Mapping[] }
 *   POST /api/ldap/mappings            → add mapping
 *   DELETE /api/ldap/mappings          → remove mapping
 *   GET  /api/ldap/sync-log            → { success, entries: string[] }
 *   GET  /api/ldap/circuit-breaker     → { success, state }
 *   POST /api/ldap/circuit-breaker/reset
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

interface LdapConfig {
  host?:        string
  port?:        number
  bind_dn?:     string
  bind_pass?:   string
  base_dn?:     string
  enabled?:     number | boolean
  tls?:         boolean
  user_filter?: string
  group_filter?: string
}

interface LdapStatus {
  success:       boolean
  enabled:       boolean
  last_test_ok?: boolean
  last_sync?:    string
  error?:        string
}

interface Mapping {
  id?:        number | string
  ldap_group: string
  local_role: string
}

interface CircuitBreaker {
  success: boolean
  state:   'closed' | 'open' | 'half-open'
  failures?: number
}

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
const inputStyle: React.CSSProperties = {
  background: 'var(--surface)', border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)', padding: '8px 12px',
  color: 'var(--text)', fontSize: 'var(--text-sm)', width: '100%',
  fontFamily: 'var(--font-ui)', outline: 'none', boxSizing: 'border-box',
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
      <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>{label}</span>
      {children}
    </label>
  )
}

function fmtDate(s?: string) {
  if (!s) return 'Never'
  try { return new Date(s).toLocaleString('de-DE', { dateStyle: 'short', timeStyle: 'short' }) }
  catch { return s }
}

// ---------------------------------------------------------------------------
// Status bar
// ---------------------------------------------------------------------------

function StatusBar({ status, onSync }: { status: LdapStatus; onSync: () => void }) {
  const isConnected = status.enabled && status.last_test_ok
  const color = isConnected ? 'var(--success)' : status.enabled ? 'var(--warning)' : 'var(--text-tertiary)'

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '12px 18px', background: 'var(--bg-card)', border: `1px solid ${isConnected ? 'rgba(16,185,129,0.25)' : 'var(--border)'}`, borderRadius: 'var(--radius-lg)', marginBottom: 24 }}>
      <span style={{ width: 10, height: 10, borderRadius: '50%', background: color, boxShadow: isConnected ? `0 0 6px ${color}` : 'none', flexShrink: 0 }} />
      <div style={{ flex: 1 }}>
        <span style={{ fontWeight: 700, color }}>{isConnected ? 'Connected' : status.enabled ? 'Enabled (not tested)' : 'Disabled'}</span>
        {status.last_sync && <span style={{ marginLeft: 12, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Last sync: {fmtDate(status.last_sync)}</span>}
        {status.error && <span style={{ marginLeft: 12, fontSize: 'var(--text-xs)', color: 'var(--error)' }}>{status.error}</span>}
      </div>
      <button onClick={onSync} style={btnGhost}><Icon name="sync" size={14} />Sync Now</button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// ConfigTab
// ---------------------------------------------------------------------------

function ConfigTab() {
  const qc = useQueryClient()

  const configQ = useQuery({
    queryKey: ['ldap', 'config'],
    queryFn: ({ signal }) => api.get<LdapConfig & { success: boolean }>('/api/ldap/config', signal),
  })
  const statusQ = useQuery({
    queryKey: ['ldap', 'status'],
    queryFn: ({ signal }) => api.get<LdapStatus>('/api/ldap/status', signal),
    refetchInterval: 30_000,
  })

  const [cfg, setCfg] = useState<LdapConfig | null>(null)

  // Populate form once loaded
  const formCfg: LdapConfig = cfg ?? configQ.data ?? {}

  function set(k: keyof LdapConfig, v: unknown) {
    setCfg(prev => ({ ...(prev ?? configQ.data ?? {}), [k]: v }))
  }

  const save = useMutation({
    mutationFn: () => api.post('/api/ldap/config', formCfg),
    onSuccess: () => { toast.success('Configuration saved'); qc.invalidateQueries({ queryKey: ['ldap'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const test = useMutation({
    mutationFn: () => api.post<{ success: boolean; message?: string }>('/api/ldap/test', {}),
    onSuccess: data => { toast.success(data.message ?? 'Connection successful'); qc.invalidateQueries({ queryKey: ['ldap', 'status'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const sync = useMutation({
    mutationFn: () => api.post('/api/ldap/sync', {}),
    onSuccess: () => { toast.success('Sync triggered'); qc.invalidateQueries({ queryKey: ['ldap', 'status'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  if (configQ.isLoading) return <Skeleton height={360} />
  if (configQ.isError) return <ErrorState error={configQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['ldap', 'config'] })} />

  return (
    <>
      {statusQ.data && <StatusBar status={statusQ.data} onSync={() => sync.mutate()} />}

      <div style={{ display: 'grid', gap: 16 }}>
        {/* Enable toggle */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '14px 18px', background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)' }}>
          <input type="checkbox"
            checked={!!(formCfg.enabled)}
            onChange={e => set('enabled', e.target.checked ? 1 : 0)}
            style={{ width: 16, height: 16, accentColor: 'var(--primary)', cursor: 'pointer' }} />
          <div>
            <div style={{ fontWeight: 600 }}>Enable LDAP</div>
            <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Authenticate users against the directory server</div>
          </div>
        </div>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 120px', gap: 12 }}>
          <Field label="LDAP Host">
            <input value={formCfg.host ?? ''} onChange={e => set('host', e.target.value)} placeholder="ldap.example.com" style={inputStyle} />
          </Field>
          <Field label="Port">
            <input type="number" value={formCfg.port ?? 389} onChange={e => set('port', Number(e.target.value))} style={inputStyle} />
          </Field>
        </div>

        <Field label="Bind DN">
          <input value={formCfg.bind_dn ?? ''} onChange={e => set('bind_dn', e.target.value)} placeholder="cn=admin,dc=example,dc=com" style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} />
        </Field>

        <Field label="Bind Password">
          <input type="password" value={formCfg.bind_pass ?? ''} onChange={e => set('bind_pass', e.target.value)} style={inputStyle} autoComplete="new-password" />
        </Field>

        <Field label="Base DN">
          <input value={formCfg.base_dn ?? ''} onChange={e => set('base_dn', e.target.value)} placeholder="dc=example,dc=com" style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} />
        </Field>

        <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
          <Field label="User Filter">
            <input value={formCfg.user_filter ?? ''} onChange={e => set('user_filter', e.target.value)} placeholder="(objectClass=person)" style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} />
          </Field>
          <Field label="Group Filter">
            <input value={formCfg.group_filter ?? ''} onChange={e => set('group_filter', e.target.value)} placeholder="(objectClass=groupOfNames)" style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} />
          </Field>
        </div>

        <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
          <input type="checkbox" checked={!!formCfg.tls} onChange={e => set('tls', e.target.checked)}
            style={{ width: 15, height: 15, accentColor: 'var(--primary)' }} />
          <span style={{ fontSize: 'var(--text-sm)' }}>Use TLS (LDAPS)</span>
        </label>

        <div style={{ display: 'flex', gap: 8, paddingTop: 8 }}>
          <button onClick={() => save.mutate()} disabled={save.isPending} style={btnPrimary}>
            <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save'}
          </button>
          <button onClick={() => test.mutate()} disabled={test.isPending} style={btnGhost}>
            <Icon name="cable" size={15} />{test.isPending ? 'Testing…' : 'Test Connection'}
          </button>
        </div>
      </div>
    </>
  )
}

// ---------------------------------------------------------------------------
// MappingsTab
// ---------------------------------------------------------------------------

function MappingsTab() {
  const qc = useQueryClient()
  const [ldapGroup, setLdapGroup] = useState('')
  const [localRole, setLocalRole] = useState('user')

  const mappingsQ = useQuery({
    queryKey: ['ldap', 'mappings'],
    queryFn: ({ signal }) => api.get<{ success: boolean; mappings: Mapping[] }>('/api/ldap/mappings', signal),
  })

  const add = useMutation({
    mutationFn: () => api.post('/api/ldap/mappings', { ldap_group: ldapGroup, local_role: localRole }),
    onSuccess: () => { toast.success('Mapping added'); setLdapGroup(''); qc.invalidateQueries({ queryKey: ['ldap', 'mappings'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const remove = useMutation({
    mutationFn: (id: number | string) => api.delete('/api/ldap/mappings', { id }),
    onSuccess: () => { toast.success('Mapping removed'); qc.invalidateQueries({ queryKey: ['ldap', 'mappings'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const mappings = mappingsQ.data?.mappings ?? []

  return (
    <>
      <div style={{ display: 'flex', gap: 8, marginBottom: 20, flexWrap: 'wrap' }}>
        <input value={ldapGroup} onChange={e => setLdapGroup(e.target.value)} placeholder="LDAP group DN or name"
          style={{ ...inputStyle, flex: 1, minWidth: 200, fontFamily: 'var(--font-mono)' }} />
        <select value={localRole} onChange={e => setLocalRole(e.target.value)}
          style={{ ...inputStyle, width: 120 }}>
          {['admin', 'user', 'readonly'].map(r => <option key={r} value={r}>{r}</option>)}
        </select>
        <button onClick={() => add.mutate()} disabled={!ldapGroup.trim() || add.isPending} style={btnPrimary}>
          <Icon name="add" size={15} />{add.isPending ? 'Adding…' : 'Add Mapping'}
        </button>
      </div>

      {mappingsQ.isLoading && <Skeleton height={120} />}
      {mappingsQ.isError && <ErrorState error={mappingsQ.error} />}

      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {mappings.map(m => (
          <div key={String(m.id ?? m.ldap_group)} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 16px', background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
            <Icon name="account_tree" size={16} style={{ color: 'var(--primary)', flexShrink: 0 }} />
            <code style={{ flex: 1, fontSize: 'var(--text-xs)', fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>{m.ldap_group}</code>
            <Icon name="arrow_forward" size={14} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
            <span style={{ padding: '2px 8px', borderRadius: 'var(--radius-sm)', background: 'var(--primary-bg)', color: 'var(--primary)', fontSize: 'var(--text-xs)', fontWeight: 700 }}>{m.local_role}</span>
            <button onClick={() => remove.mutate(m.id ?? m.ldap_group)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', padding: 4, borderRadius: 'var(--radius-xs)', display: 'flex' }}
              onMouseEnter={e => (e.currentTarget.style.color = 'var(--error)')}
              onMouseLeave={e => (e.currentTarget.style.color = 'var(--text-tertiary)')}>
              <Icon name="close" size={15} />
            </button>
          </div>
        ))}
        {!mappingsQ.isLoading && mappings.length === 0 && (
          <div style={{ textAlign: 'center', padding: '32px 0', color: 'var(--text-tertiary)' }}>No group mappings configured</div>
        )}
      </div>
    </>
  )
}

// ---------------------------------------------------------------------------
// SyncLogTab
// ---------------------------------------------------------------------------

function SyncLogTab() {
  const qc = useQueryClient()

  const logQ = useQuery({
    queryKey: ['ldap', 'sync-log'],
    queryFn: ({ signal }) => api.get<{ success: boolean; entries: string[] }>('/api/ldap/sync-log', signal),
  })

  const cbQ = useQuery({
    queryKey: ['ldap', 'circuit-breaker'],
    queryFn: ({ signal }) => api.get<CircuitBreaker>('/api/ldap/circuit-breaker', signal),
  })

  const resetCB = useMutation({
    mutationFn: () => api.post('/api/ldap/circuit-breaker/reset', {}),
    onSuccess: () => { toast.success('Circuit breaker reset'); qc.invalidateQueries({ queryKey: ['ldap', 'circuit-breaker'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const cb = cbQ.data
  const cbColor = cb?.state === 'closed' ? 'var(--success)' : cb?.state === 'open' ? 'var(--error)' : 'var(--warning)'

  return (
    <>
      {/* Circuit breaker status */}
      {cb && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 16px', background: 'var(--bg-card)', border: `1px solid ${cbColor}30`, borderRadius: 'var(--radius-lg)', marginBottom: 20 }}>
          <Icon name="electrical_services" size={18} style={{ color: cbColor, flexShrink: 0 }} />
          <div style={{ flex: 1 }}>
            <span style={{ fontWeight: 700, color: cbColor, textTransform: 'capitalize' }}>Circuit Breaker: {cb.state}</span>
            {cb.failures !== undefined && <span style={{ marginLeft: 10, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{cb.failures} failure{cb.failures !== 1 ? 's' : ''}</span>}
          </div>
          {cb.state !== 'closed' && (
            <button onClick={() => resetCB.mutate()} disabled={resetCB.isPending} style={btnGhost}>
              <Icon name="restart_alt" size={14} />{resetCB.isPending ? 'Resetting…' : 'Reset'}
            </button>
          )}
        </div>
      )}

      {logQ.isLoading && <Skeleton height={200} />}
      {logQ.isError && <ErrorState error={logQ.error} />}
      {logQ.data && (
        <pre style={{ background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '16px 20px', fontFamily: 'var(--font-mono)', fontSize: 11, lineHeight: 1.7, overflow: 'auto', maxHeight: 420, margin: 0, color: 'rgba(255,255,255,0.7)', whiteSpace: 'pre-wrap' }}>
          {logQ.data.entries?.join('\n') || '(no sync log entries)'}
        </pre>
      )}
    </>
  )
}

// ---------------------------------------------------------------------------
// DirectoryPage
// ---------------------------------------------------------------------------

type Tab = 'config' | 'mappings' | 'log'

export function DirectoryPage() {
  const [tab, setTab] = useState<Tab>('config')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'config',   label: 'Configuration', icon: 'settings' },
    { id: 'mappings', label: 'Group Mappings', icon: 'account_tree' },
    { id: 'log',      label: 'Sync Log',       icon: 'history' },
  ]

  return (
    <div style={{ maxWidth: 860 }}>
      <div style={{ marginBottom: 28 }}>
        <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>Directory Service</h1>
        <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>LDAP integration for centralized authentication</p>
      </div>

      <div style={{ display: 'flex', gap: 4, marginBottom: 24, borderBottom: '1px solid var(--border)' }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} style={{ padding: '10px 20px', background: 'none', border: 'none', cursor: 'pointer', fontSize: 'var(--text-sm)', fontWeight: 600, color: tab === t.id ? 'var(--primary)' : 'var(--text-secondary)', borderBottom: tab === t.id ? '2px solid var(--primary)' : '2px solid transparent', marginBottom: -1, display: 'flex', alignItems: 'center', gap: 6 }}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'config'   && <ConfigTab />}
      {tab === 'mappings' && <MappingsTab />}
      {tab === 'log'      && <SyncLogTab />}
    </div>
  )
}
