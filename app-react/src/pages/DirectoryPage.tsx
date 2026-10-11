import { useState } from 'react'
import { ADDomainsTab } from '@/components/directory/ADDomainsTab'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { fmtDateTime } from '@/lib/fmt'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface DirectoryUser { dn: string; username: string; full_name: string; email: string; groups: string[]; roles: string[] }

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
  /** Create the account of a directory user at the first sign-in. */
  jit_provisioning?: number
  /** Role of directory users whose groups map to none. */
  default_role?:   string
  /** Seconds between scheduled syncs; 0: only when asked. */
  sync_interval?:  number
}

// The LDAP API's names (server, use_tls, bind_password) differ from the
// form's; converted when loading and saving.
interface LdapConfigApi extends Omit<LdapConfig, 'host' | 'tls' | 'bind_pass'> {
  server?: string
  use_tls?: number
}

function fromLdapApi(c: LdapConfigApi): LdapConfig {
  const { server, use_tls, ...rest } = c
  return { ...rest, host: server, tls: !!use_tls }
}

function toLdapApi(c: LdapConfig) {
  const { host, tls, bind_pass, domain_joined: _joined, ...rest } = c
  return {
    ...rest,
    server: host ?? '',
    use_tls: tls ? 1 : 0,
    enabled: c.enabled ? 1 : 0,
    bind_password: bind_pass ?? '', // empty keeps the stored password
  }
}

// The LDAP endpoints wrap their payload: { success, data, error }.
interface LdapWrapped<T> { success: boolean; data?: T; error?: string }
interface LdapStatusApi {
  enabled: number; last_test_at?: string; last_test_ok?: number; last_test_msg?: string
  last_sync_at?: string; last_sync_ok?: number; last_sync_count?: number; last_sync_msg?: string
}
interface SyncLogEntry {
  id: number; sync_type: string; success: number; users_synced: number; users_created: number
  users_updated: number; users_disabled: number; error_msg?: string; duration_ms: number; created_at: string
}

interface LdapStatus {
  success:       boolean
  enabled:       boolean
  last_test_ok?: boolean
  last_sync?:    string
  error?:        string
}

interface DirectoryStatus {
  provider_type:     string
  domain_joined:     boolean
  domain_joined_at?: string | null
  domain?:           string
  realm?:            string
  /** false: the domain does not accept this system's account at the moment */
  trust_ok?:         boolean
  /** false: this system's Samba cannot join a domain */
  supported?:        boolean
}

interface Mapping {
  id?:        number | string
  ldap_group: string
  role_name:  string
}

interface CircuitBreaker {
  success: boolean
  state:   'closed' | 'open' | 'half-open'
  failures?: number
}

type Tab = 'config' | 'mappings' | 'log' | 'domains'

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
  return fmtDateTime(s)
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

function ConfigTab({ onOpenMembership }: { onOpenMembership: () => void }) {
  const qc = useQueryClient()

  const configQ = useQuery({
    queryKey: ['ldap', 'config'],
    queryFn: async ({ signal }) => fromLdapApi((await api.get<LdapWrapped<LdapConfigApi>>('/api/ldap/config', signal)).data ?? {}),
  })
  
  const statusQ = useQuery({
    queryKey: ['ldap', 'status'],
    queryFn: async ({ signal }): Promise<LdapStatus> => {
      const r = await api.get<LdapWrapped<LdapStatusApi>>('/api/ldap/status', signal)
      const d = r.data
      return {
        success: r.success,
        enabled: d?.enabled === 1,
        last_test_ok: d ? d.last_test_ok === 1 : undefined,
        last_sync: d?.last_sync_at || undefined,
        error: r.error || (d && d.last_sync_at && d.last_sync_ok !== 1 ? d.last_sync_msg : undefined),
      }
    },
    refetchInterval: 30_000,
  })

  const adStatusQ = useQuery({
    queryKey: ['directory', 'status'],
    queryFn: ({ signal }) => api.get<{ success: boolean; data: DirectoryStatus }>('/api/directory/status', signal),
    enabled: !!configQ.data?.provider_type?.includes('ad'),
  })

  const [cfg, setCfg] = useState<LdapConfig | null>(null)

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
    mutationFn: () => api.post('/api/ldap/config', toLdapApi(formCfg)),
    onSuccess: () => { toast.success('Configuration saved'); qc.invalidateQueries({ queryKey: ['ldap'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const test = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; error?: string; data?: { message?: string } }>('/api/ldap/test', {})),
    onSuccess: data => { toast.success(data.data?.message ?? 'Connection successful'); qc.invalidateQueries({ queryKey: ['ldap', 'status'] }) },
    onError: (e: Error) => { toast.error(e.message); qc.invalidateQueries({ queryKey: ['ldap', 'status'] }) },
  })

  // What the directory returns for one user: the entry, its groups and the roles a login would get.
  const [lookupName, setLookupName] = useState('')
  const lookup = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean; error?: string; data?: DirectoryUser }>('/api/ldap/search-user', { username: lookupName.trim() })),
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
              <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 600, margin: 0 }}>Active Directory membership</h3>
            </div>
            
            {adStatusQ.data?.data.domain_joined ? (
              <div style={{ background: 'var(--bg-success-subtle)', padding: 12, borderRadius: 'var(--radius-sm)', marginBottom: 12 }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: adStatusQ.data.data.trust_ok === false ? 'var(--error)' : 'var(--success)', fontWeight: 600 }}>
                  <Icon name={adStatusQ.data.data.trust_ok === false ? 'error' : 'check_circle'} size={16} />
                  {adStatusQ.data.data.trust_ok === false
                    ? `Member of ${adStatusQ.data.data.realm}, but the domain does not accept this system's account at the moment`
                    : `Member of ${adStatusQ.data.data.realm}`}
                </div>
                {adStatusQ.data.data.domain_joined_at && (
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', marginTop: 4 }}>
                    Joined on {new Date(adStatusQ.data.data.domain_joined_at).toLocaleString()}
                  </div>
                )}
              </div>
            ) : (
              <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', margin: '0 0 12px' }}>
                {adStatusQ.data?.data.supported === false
                  ? 'This system cannot join a domain: its Samba was built without Active Directory support.'
                  : 'Not a member of a domain. The settings below let domain users sign in to this web interface. To let them use the SMB shares with their domain accounts, the system joins the domain.'}
              </p>
            )}
            <button className="btn btn-ghost" onClick={onOpenMembership}>
              <Icon name="domain_add" size={16} />{adStatusQ.data?.data.domain_joined ? 'Domain membership' : 'Join a domain'}
            </button>
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

            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12 }}>
              <Field label="Sync with the directory">
                <select className="input" value={String(formCfg.sync_interval ?? 3600)} onChange={e => set('sync_interval', Number(e.target.value))}>
                  <option value="0">Only when I click Sync</option>
                  <option value="900">Every 15 minutes</option>
                  <option value="3600">Every hour</option>
                  <option value="21600">Every 6 hours</option>
                  <option value="86400">Once a day</option>
                </select>
              </Field>
              <Field label="Role without a group mapping">
                <select className="input" value={formCfg.default_role ?? ''} onChange={e => set('default_role', e.target.value)}>
                  <option value="">None (no access)</option>
                  <option value="viewer">Viewer</option>
                  <option value="user">User</option>
                  <option value="operator">Operator</option>
                </select>
              </Field>
            </div>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: 'var(--text-sm)' }}>
              <input type="checkbox" checked={!!formCfg.jit_provisioning} onChange={e => set('jit_provisioning', e.target.checked ? 1 : 0)} />
              Create the account of a directory user at the first sign-in (without waiting for a sync)
            </label>

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

        <div className="card" style={{ padding: 20 }}>
          <h3 style={{ marginBottom: 6, fontSize: 'var(--text-lg)', fontWeight: 600 }}>Look up a user</h3>
          <p style={{ margin: '0 0 12px', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            Searches the directory with the saved settings and shows what a login of this user would get. Use it to check the user filter and the group mappings.
          </p>
          <form style={{ display: 'flex', gap: 8 }} onSubmit={e => { e.preventDefault(); if (lookupName.trim()) lookup.mutate() }}>
            <input value={lookupName} onChange={e => setLookupName(e.target.value)} placeholder="User name" className="input" style={{ maxWidth: 280 }} aria-label="User name to look up" />
            <button type="submit" className="btn btn-ghost" disabled={lookup.isPending || !lookupName.trim()}>
              <Icon name="person_search" size={16} />{lookup.isPending ? 'Searching…' : 'Look up'}
            </button>
          </form>
          {lookup.data?.data && (
            <div style={{ marginTop: 14, display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '6px 16px', fontSize: 'var(--text-sm)' }}>
              {([
                ['User name', lookup.data.data.username],
                ['Full name', lookup.data.data.full_name || '-'],
                ['Email', lookup.data.data.email || '-'],
                ['Entry', lookup.data.data.dn],
                ['Groups', lookup.data.data.groups.length ? lookup.data.data.groups.join(', ') : 'none found'],
                ['Roles on login', lookup.data.data.roles.length ? lookup.data.data.roles.join(', ') : 'none'],
              ] as [string, string][]).map(([k, v]) => (
                <div key={k} style={{ display: 'contents' }}>
                  <span style={{ color: 'var(--text-tertiary)' }}>{k}</span>
                  <span style={{ wordBreak: 'break-all' }}>{v}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </>
  )
}

function MappingsTab() {
  const qc = useQueryClient()
  const [ldapGroup, setLdapGroup] = useState('')
  const [localRole, setLocalRole] = useState('user')
  const { confirm, ConfirmDialog } = useConfirm()

  const mappingsQ = useQuery({
    queryKey: ['ldap', 'mappings'],
    queryFn: async ({ signal }) => ({ mappings: (await api.get<LdapWrapped<Mapping[]>>('/api/ldap/mappings', signal)).data ?? [] }),
  })

  const add = useMutation({
    mutationFn: () => api.post('/api/ldap/mappings', { ldap_group: ldapGroup, role_name: localRole }),
    onSuccess: () => { toast.success('Mapping added'); setLdapGroup(''); qc.invalidateQueries({ queryKey: ['ldap', 'mappings'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const remove = useMutation({
    mutationFn: (id: number | string) => api.delete(`/api/ldap/mappings?id=${id}`),
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
            <span className="badge badge-primary" style={{ minWidth: 80, textAlign: 'center' }}>{m.role_name}</span>
            <button onClick={async () => { if (await confirm({ title: 'Remove group mapping?', message: 'This LDAP group will no longer map to a local role.', danger: true, confirmLabel: 'Remove' })) { remove.mutate(m.id ?? m.ldap_group) } }} className="btn btn-icon-ghost" style={{ color: 'var(--error)' }}>
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
      <ConfirmDialog />
    </>
  )
}

function SyncLogTab() {
  const qc = useQueryClient()
  const logQ = useQuery({
    queryKey: ['ldap', 'sync-log'],
    queryFn: async ({ signal }) => {
      const r = await api.get<LdapWrapped<SyncLogEntry[]>>('/api/ldap/sync-log', signal)
      return {
        entries: (r.data ?? []).map(e =>
          `${e.created_at}  ${e.sync_type}  ${e.success ? 'ok' : 'FAILED'}  ${e.users_synced} users` +
          ` (+${e.users_created} new, ${e.users_updated} updated, ${e.users_disabled} disabled)  ${e.duration_ms} ms` +
          (e.error_msg ? `  ${e.error_msg}` : '')),
      }
    },
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
            <span style={{ fontWeight: 700, color: cbColor, fontSize: 'var(--text-md)' }}>
              {cb.state === 'closed' ? 'Directory logins: server reachable' : cb.state === 'open' ? 'Directory logins paused: the server did not answer' : 'Directory logins: trying the server again'}
            </span>
            <span style={{ marginLeft: 12, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              {cb.state === 'closed'
                ? (cb.failures ? `${cb.failures} failed connection${cb.failures !== 1 ? 's' : ''} in a row` : '')
                : 'Directory users cannot sign in until it answers; local accounts work.'}
            </span>
          </div>
          {cb.state !== 'closed' && (
            <button onClick={() => resetCB.mutate()} disabled={resetCB.isPending} className="btn btn-ghost" title="Try the directory server again at the next login">
              <Icon name="restart_alt" size={16} />Try again now
            </button>
          )}
        </div>
      )}

      <div className="card" style={{ background: 'var(--bg-code)', padding: 0, borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
        <div style={{ padding: '10px 18px', borderBottom: '1px solid var(--border)', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>LAST SYNC LOGS</span>
          <button onClick={() => qc.invalidateQueries({ queryKey: ['ldap', 'sync-log'] })} className="btn btn-icon-ghost"><Icon name="refresh" size={14} /></button>
        </div>
        <pre style={{ margin: 0, padding: 20, fontFamily: 'var(--font-mono)', fontSize: 13, lineHeight: 1.6, overflow: 'auto', maxHeight: 500, color: 'var(--text-secondary)', whiteSpace: 'pre-wrap' }}>
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
    { id: 'domains',  label: 'Domain Membership', icon: 'domain_add' },
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
        {tab === 'config'   && <ConfigTab onOpenMembership={() => setTab('domains')} />}
        {tab === 'mappings' && <MappingsTab />}
        {tab === 'domains'  && <ADDomainsTab />}
        {tab === 'log'      && <SyncLogTab />}
      </div>
    </div>
  )
}
