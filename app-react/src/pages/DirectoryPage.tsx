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

type ProviderType = 'openldap' | 'ad' | 'active-directory' | 'apple-od'

interface LdapConfig {
  host?:           string
  port?:           number
  bind_dn?:        string
  bind_pass?:      string
  base_dn?:        string
  enabled?:        number | boolean
  tls?:            boolean
  user_filter?:    string
  group_filter?:   string
  provider_type?:  ProviderType
  realm?:          string
  domain_joined?:  boolean
  user_id_attr?:   string
  user_name_attr?: string
  user_email_attr?: string
}

interface LdapStatus {
  success:       boolean
  enabled:       boolean
  last_test_ok?: boolean
  last_sync?:    string
  error?:        string
}

interface DirectoryStatus {
  provider_type:    string
  domain_joined:    boolean
  domain_joined_at: string | null
  realm:            string
  net_ads_info:     string
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

type Tab = 'config' | 'mappings' | 'log'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="field">
      <span className="field-label">{label}</span>
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
      <button onClick={onSync} className="btn btn-ghost"><Icon name="sync" size={14} />Sync Now</button>
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

  const adStatusQ = useQuery({
    queryKey: ['directory', 'status'],
    queryFn: ({ signal }) => api.get<{ success: boolean; data: DirectoryStatus }>('/api/directory/status', signal),
    enabled: !!configQ.data?.provider_type?.includes('ad'),
  })

  const [cfg, setCfg] = useState<LdapConfig | null>(null)
  const [joinCreds, setJoinCreds] = useState({ username: '', password: '' })

  const formCfg: LdapConfig = cfg ?? configQ.data ?? { provider_type: 'openldap' }

  function set(k: keyof LdapConfig, v: unknown) {
    let next = { ...(cfg ?? configQ.data ?? {}), [k]: v }
    
    // Apple Open Directory Presets
    if (k === 'provider_type' && v === 'apple-od') {
      next = {
        ...next,
        user_filter: '(objectClass=person)',
        group_filter: '(objectClass=group)',
        user_id_attr: 'uid',
        user_name_attr: 'cn',
        user_email_attr: 'mail',
      }
    }
    
    setCfg(next)
  }

  const save = useMutation({
    mutationFn: () => api.post('/api/ldap/config', { ...formCfg, host: formCfg.host, bind_password: formCfg.bind_pass }),
    onSuccess: () => { toast.success('Configuration saved'); qc.invalidateQueries({ queryKey: ['ldap'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const join = useMutation({
    mutationFn: () => api.post('/api/directory/join', {
      username: joinCreds.username,
      password: joinCreds.password,
      domain: formCfg.realm,
      domain_controller: formCfg.host
    }),
    onSuccess: () => { 
      toast.success('Successfully joined domain'); 
      qc.invalidateQueries({ queryKey: ['directory'] });
      setJoinCreds({ username: '', password: '' });
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const test = useMutation({
    mutationFn: () => api.post<{ success: boolean; message?: string }>('/api/ldap/test', {}),
    onSuccess: data => { toast.success(data.message ?? 'Connection successful'); qc.invalidateQueries({ queryKey: ['ldap', 'status'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  if (configQ.isLoading) return <Skeleton height={360} />
  if (configQ.isError) return <ErrorState error={configQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['ldap', 'config'] })} />

  const isAD = formCfg.provider_type?.includes('ad')

  return (
    <>
      {statusQ.data && <StatusBar status={statusQ.data} onSync={() => api.post('/api/ldap/sync', {})} />}

      <div style={{ display: 'grid', gap: 24 }}>
        <div className="card" style={{ padding: 20 }}>
          <h3 style={{ marginBottom: 16, fontSize: 'var(--text-lg)', fontWeight: 600 }}>Service Provider</h3>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 12 }}>
            {[
              { id: 'openldap', label: 'OpenLDAP', icon: 'settings_input_component' },
              { id: 'ad',       label: 'Active Directory', icon: 'window' },
              { id: 'apple-od', label: 'Apple Open Directory', icon: 'apple' },
            ].map(p => (
              <button key={p.id}
                onClick={() => set('provider_type', p.id as ProviderType)}
                className={`card selectable${formCfg.provider_type === p.id ? ' active' : ''}`}
                style={{ padding: '16px 12px', textAlign: 'center', border: '2px solid transparent', transition: 'all 0.2s' }}>
                <Icon name={p.icon} size={24} style={{ marginBottom: 8, color: formCfg.provider_type === p.id ? 'var(--primary)' : 'var(--text-tertiary)' }} />
                <div style={{ fontWeight: 600, fontSize: 'var(--text-xs)' }}>{p.label}</div>
              </button>
            ))}
          </div>
        </div>

        {isAD && (
          <div className="card" style={{ padding: 20, borderLeft: '4px solid var(--primary)' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 16 }}>
              <Icon name="verified_user" size={20} style={{ color: 'var(--primary)' }} />
              <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 600, margin: 0 }}>Active Directory Domain Join</h3>
            </div>
            
            {adStatusQ.data?.data.domain_joined ? (
              <div style={{ background: 'var(--bg-success-subtle)', padding: 12, borderRadius: 'var(--radius-sm)', marginBottom: 16 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: 'var(--success)', fontWeight: 600 }}>
                  <Icon name="check_circle" size={16} /> Joined to {adStatusQ.data.data.realm}
                </div>
                {adStatusQ.data.data.domain_joined_at && (
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 4 }}>
                    Joined on {new Date(adStatusQ.data.data.domain_joined_at).toLocaleString()}
                  </div>
                )}
              </div>
            ) : (
              <div style={{ display: 'grid', gap: 12 }}>
                <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 8 }}>
                  To enable Kerberos authentication for SMB shares, D-PlaneOS must join the AD domain.
                  The system clock must be NTP-synchronized before joining.
                </p>
                <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
                  <Field label="Domain Admin User">
                    <input value={joinCreds.username} onChange={e => setJoinCreds(p => ({ ...p, username: e.target.value }))} placeholder="administrator" className="input" />
                  </Field>
                  <Field label="Domain Admin Password">
                    <input type="password" value={joinCreds.password} onChange={e => setJoinCreds(p => ({ ...p, password: e.target.value }))} className="input" />
                  </Field>
                </div>
                <button onClick={() => join.mutate()} disabled={join.isPending || !joinCreds.username || !joinCreds.password} className="btn btn-primary" style={{ justifySelf: 'start' }}>
                  {join.isPending ? 'Joining Domain...' : 'Join Domain'}
                </button>
              </div>
            )}
            
            {adStatusQ.data?.data.net_ads_info && (
              <details style={{ marginTop: 12 }}>
                <summary style={{ fontSize: 'var(--text-xs)', cursor: 'pointer', color: 'var(--text-tertiary)' }}>Advanced Status Info</summary>
                <pre style={{ fontSize: 10, background: 'var(--bg-code)', padding: 8, borderRadius: 4, marginTop: 4, whiteSpace: 'pre-wrap' }}>
                  {adStatusQ.data.data.net_ads_info}
                </pre>
              </details>
            )}
          </div>
        )}

        <div className="card" style={{ padding: 20 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 20 }}>
            <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 600, margin: 0 }}>Connection Settings</h3>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
              <input type="checkbox" checked={!!formCfg.enabled} onChange={e => set('enabled', e.target.checked ? 1 : 0)}
                style={{ width: 16, height: 16, accentColor: 'var(--primary)' }} />
              <span style={{ fontWeight: 600 }}>Enable Authentication</span>
            </label>
          </div>

          <div style={{ display: 'grid', gap: 16 }}>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 120px', gap: 12 }}>
              <Field label={isAD ? "Domain Controller (FQDN/IP)" : "Server Host"}>
                <input value={formCfg.host ?? ''} onChange={e => set('host', e.target.value)} placeholder={isAD ? "dc1.example.com" : "ldap.example.com"} className="input" />
              </Field>
              <Field label="Port">
                <input type="number" value={formCfg.port ?? 389} onChange={e => set('port', Number(e.target.value))} className="input" />
              </Field>
            </div>

            {isAD ? (
              <Field label="AD Domain / Realm">
                <input value={formCfg.realm ?? ''} onChange={e => set('realm', e.target.value)} placeholder="EXAMPLE.COM" className="input" style={{ textTransform: 'uppercase' }} />
              </Field>
            ) : (
              <Field label="Base DN">
                <input value={formCfg.base_dn ?? ''} onChange={e => set('base_dn', e.target.value)} placeholder="dc=example,dc=com" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
              </Field>
            )}

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
              <Field label="Bind DN">
                <input value={formCfg.bind_dn ?? ''} onChange={e => set('bind_dn', e.target.value)} placeholder={isAD ? "user@example.com" : "cn=admin,dc=example,dc=com"} className="input" />
              </Field>
              <Field label="Bind Password">
                <input type="password" value={formCfg.bind_pass ?? ''} onChange={e => set('bind_pass', e.target.value)} className="input" autoComplete="new-password" />
              </Field>
            </div>

            <details>
              <summary style={{ cursor: 'pointer', padding: '10px 0', fontSize: 'var(--text-sm)', fontWeight: 600 }}>Advanced Attribute Mapping</summary>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, paddingTop: 12 }}>
                <Field label="User Filter">
                  <input value={formCfg.user_filter ?? ''} onChange={e => set('user_filter', e.target.value)} className="input" style={{ fontFamily: 'var(--font-mono)' }} />
                </Field>
                <Field label="Group Filter">
                  <input value={formCfg.group_filter ?? ''} onChange={e => set('group_filter', e.target.value)} className="input" style={{ fontFamily: 'var(--font-mono)' }} />
                </Field>
                <Field label="User ID Attribute">
                  <input value={formCfg.user_id_attr ?? ''} onChange={e => set('user_id_attr', e.target.value)} className="input" />
                </Field>
                <Field label="Display Name Attribute">
                  <input value={formCfg.user_name_attr ?? ''} onChange={e => set('user_name_attr', e.target.value)} className="input" />
                </Field>
              </div>
            </details>

            <div style={{ display: 'flex', gap: 12, paddingTop: 12 }}>
              <button onClick={() => save.mutate()} disabled={save.isPending} className="btn btn-primary" style={{ minWidth: 120 }}>
                <Icon name="save" size={16} />{save.isPending ? 'Saving...' : 'Save Config'}
              </button>
              {!isAD && (
                <button onClick={() => test.mutate()} disabled={test.isPending} className="btn btn-ghost">
                  <Icon name="cable" size={16} />{test.isPending ? 'Testing...' : 'Test Connection'}
                </button>
              )}
            </div>
          </div>
        </div>
      </div>
    </>
  )
}

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
      <div className="card" style={{ padding: 20, marginBottom: 20 }}>
        <h3 style={{ marginBottom: 16, fontSize: 'var(--text-lg)', fontWeight: 600 }}>Add New Mapping</h3>
        <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
          <input value={ldapGroup} onChange={e => setLdapGroup(e.target.value)} placeholder="LDAP Group Name or DN"
            className="input" style={{ flex: 1, minWidth: 240, fontFamily: 'var(--font-mono)' }} />
          <select value={localRole} onChange={e => setLocalRole(e.target.value)}
            className="input" style={{ width: 140 }}>
            {['admin', 'operator', 'user', 'viewer'].map(r => <option key={r} value={r}>{r}</option>)}
          </select>
          <button onClick={() => add.mutate()} disabled={!ldapGroup.trim() || add.isPending} className="btn btn-primary">
            <Icon name="add" size={16} />{add.isPending ? 'Adding...' : 'Add Mapping'}
          </button>
        </div>
      </div>

      {mappingsQ.isLoading && <Skeleton height={120} />}
      {mappingsQ.isError && <ErrorState error={mappingsQ.error} />}

      <div style={{ display: 'grid', gap: 10 }}>
        {mappings.map(m => (
          <div key={String(m.id ?? m.ldap_group)} className="card" style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '14px 18px' }}>
            <Icon name="account_tree" size={18} style={{ color: 'var(--primary)', flexShrink: 0 }} />
            <div style={{ flex: 1 }}>
              <code style={{ fontSize: 'var(--text-sm)', fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>{m.ldap_group}</code>
            </div>
            <Icon name="arrow_forward" size={14} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
            <span className="badge badge-primary" style={{ minWidth: 80, textAlign: 'center' }}>{m.local_role}</span>
            <button onClick={() => remove.mutate(m.id ?? m.ldap_group)} className="btn btn-icon-ghost" style={{ color: 'var(--error)' }}>
              <Icon name="delete" size={18} />
            </button>
          </div>
        ))}
        {!mappingsQ.isLoading && mappings.length === 0 && (
          <div style={{ textAlign: 'center', padding: '48px 0', color: 'var(--text-tertiary)', background: 'var(--bg-card)', borderRadius: 'var(--radius-lg)', border: '1px dashed var(--border)' }}>
            No group-to-role mappings configured yet.
          </div>
        )}
      </div>
    </>
  )
}

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
      {cb && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '14px 18px', background: 'var(--bg-card)', border: `1px solid ${cbColor}30`, borderRadius: 'var(--radius-lg)', marginBottom: 24 }}>
          <Icon name="electrical_services" size={20} style={{ color: cbColor, flexShrink: 0 }} />
          <div style={{ flex: 1 }}>
            <span style={{ fontWeight: 700, color: cbColor, textTransform: 'capitalize', fontSize: 'var(--text-md)' }}>Circuit Breaker: {cb.state}</span>
            {cb.failures !== undefined && <span style={{ marginLeft: 12, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{cb.failures} failure{cb.failures !== 1 ? 's' : ''} detected</span>}
          </div>
          {cb.state !== 'closed' && (
            <button onClick={() => resetCB.mutate()} disabled={resetCB.isPending} className="btn btn-ghost">
              <Icon name="restart_alt" size={16} />Reset Breaker
            </button>
          )}
        </div>
      )}

      <div className="card" style={{ background: 'var(--bg-code)', padding: 0, borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
        <div style={{ padding: '10px 18px', borderBottom: '1px solid var(--border)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>LAST SYNC LOGS</span>
          <button onClick={() => qc.invalidateQueries({ queryKey: ['ldap', 'sync-log'] })} className="btn btn-icon-ghost"><Icon name="refresh" size={14} /></button>
        </div>
        <pre style={{ margin: 0, padding: 20, fontFamily: 'var(--font-mono)', fontSize: 13, lineHeight: 1.6, overflow: 'auto', maxHeight: 500, color: '#aab2c0', whiteSpace: 'pre-wrap' }}>
          {logQ.isLoading ? 'Loading logs...' : (logQ.data?.entries?.join('\n') || '(no sync log entries found)')}
        </pre>
      </div>
    </>
  )
}

export function DirectoryPage() {
  const [tab, setTab] = useState<Tab>('config')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'config',   label: 'Directory Setup', icon: 'settings' },
    { id: 'mappings', label: 'Role Mappings', icon: 'account_tree' },
    { id: 'log',      label: 'Diagnostics',    icon: 'history' },
  ]

  return (
    <div style={{ maxWidth: 960 }}>
      <div className="page-header" style={{ marginBottom: 32 }}>
        <h1 className="page-title">Directory Services</h1>
        <p className="page-subtitle">Enterprise identity integration for Active Directory, OpenLDAP and Apple Open Directory</p>
      </div>

      <div className="tabs-underline" style={{ marginBottom: 32 }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} className={`tab-underline${tab === t.id ? ' active' : ''}`}>
            <Icon name={t.icon} size={18} />{t.label}
          </button>
        ))}
      </div>

      <div style={{ animation: 'fadeIn 0.3s ease-in-out' }}>
        {tab === 'config'   && <ConfigTab />}
        {tab === 'mappings' && <MappingsTab />}
        {tab === 'log'      && <SyncLogTab />}
      </div>
    </div>
  )
}
