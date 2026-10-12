/**
 * BMCPanel - out-of-band management through the node's BMC (Redfish or
 * IPMI), shown on the IPMI page.
 *
 * Calls:
 *   GET  /api/bmc/info, /api/bmc/power, /api/bmc/events?limit=
 *   GET  /api/bmc/health → { success, health: { overall_status, sensors: [{name, value, unit, status, category}] } }
 *   POST /api/bmc/power {action}, /api/bmc/enroll, /api/bmc/reset-cert
 *   GET/PUT /api/bmc/config: the address and sign-in of this system's own BMC
 *
 * (How this node powers another node off is a different setting: High
 * Availability, power fencing.)
 */
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'
import { fmtDateTime } from '@/lib/fmt'

interface BMCInfo { protocol: string; vendor?: string; model?: string; firmware_version?: string; hostname?: string }
interface BMCSensor { name: string; value: number; unit: string; status: string; category: string }
interface BMCHealth { overall_status: string; sensors: BMCSensor[] | null; collected_at?: string }
interface BMCEvent { id: string; timestamp: string; severity: string; message: string; source?: string }

const STATUS_COLOR: Record<string, string> = { ok: 'var(--success)', warning: 'var(--warning)', critical: 'var(--error)' }
const SENSOR_UNIT: Record<string, string> = { Celsius: '°C', Watts: 'W', Volts: 'V' }
const CATEGORIES: [string, string][] = [['temperature', 'Temperatures'], ['fan', 'Fans'], ['power', 'Power'], ['voltage', 'Voltages'], ['other', 'Other']]

const ACTIONS: { action: string; label: string; icon: string; danger: boolean; text: string }[] = [
  { action: 'on',             label: 'Power on',       icon: 'power',              danger: false, text: 'Switch the machine on.' },
  { action: 'graceful_reset', label: 'Restart',        icon: 'restart_alt',        danger: true,  text: 'Ask the operating system to restart.' },
  { action: 'graceful_off',   label: 'Shut down',      icon: 'power_settings_new', danger: true,  text: 'Ask the operating system to shut down.' },
  { action: 'reset',          label: 'Hard reset',     icon: 'bolt',               danger: true,  text: 'Reset the machine immediately, like the reset button: unsaved data is lost.' },
  { action: 'off',            label: 'Power off',      icon: 'power_off',          danger: true,  text: 'Cut power immediately: unsaved data is lost and pools may need a scrub.' },
]

interface BMCConfig { host: string; username: string; has_password: boolean; tls_fingerprint: string }

/** The address and sign-in of this system's own BMC. */
function BMCSettings({ onSaved }: { onSaved: () => void }) {
  const q = useQuery({
    queryKey: ['bmc', 'config'],
    queryFn: ({ signal }) => api.get<{ success: boolean; config: BMCConfig }>('/api/bmc/config', signal),
  })
  const [dirty, setDirty] = useState<{ host?: string; username?: string; password?: string }>({})
  const host = dirty.host ?? q.data?.config.host ?? ''
  const username = dirty.username ?? q.data?.config.username ?? ''
  const save = useMutation({
    mutationFn: async () => ensureOk(await api.put<{ success: boolean; error?: string; message?: string }>('/api/bmc/config', {
      host: host.trim(), username: username.trim(), ...(dirty.password ? { password: dirty.password } : {}),
    })),
    onSuccess: (r) => { toast.success(r.message || 'Saved'); setDirty({}); onSaved() },
    onError: (e: Error) => toast.error(e.message),
  })
  const changed = dirty.host !== undefined || dirty.username !== undefined || !!dirty.password
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr auto', gap: 12, alignItems: 'end', marginBottom: 18 }}>
      <label className="field">
        <span className="field-label">Address of this system's BMC</span>
        <input value={host} onChange={e => setDirty(p => ({ ...p, host: e.target.value }))} placeholder="10.0.0.11" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
      </label>
      <label className="field">
        <span className="field-label">User name</span>
        <input value={username} onChange={e => setDirty(p => ({ ...p, username: e.target.value }))} autoComplete="off" className="input" />
      </label>
      <label className="field">
        <span className="field-label">Password</span>
        <input type="password" value={dirty.password ?? ''} onChange={e => setDirty(p => ({ ...p, password: e.target.value }))} autoComplete="new-password"
          placeholder={q.data?.config.has_password ? 'stored; leave empty to keep' : ''} className="input" />
      </label>
      <button onClick={() => save.mutate()} disabled={save.isPending || !changed} className="btn btn-primary" title="An empty address removes the settings">
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save'}
      </button>
    </div>
  )
}

export function BMCPanel() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [limit, setLimit] = useState(50)

  const infoQ = useQuery({
    queryKey: ['bmc', 'info'],
    queryFn: ({ signal }) => api.get<{ success: boolean; info?: BMCInfo; error?: string }>('/api/bmc/info', signal),
    retry: false,
  })
  const configured = !!infoQ.data?.success
  const powerQ = useQuery({
    queryKey: ['bmc', 'power'],
    queryFn: ({ signal }) => api.get<{ success: boolean; power_state?: string; error?: string }>('/api/bmc/power', signal),
    enabled: configured, refetchInterval: 30_000, retry: false,
  })
  const eventsQ = useQuery({
    queryKey: ['bmc', 'events', limit],
    queryFn: ({ signal }) => api.get<{ success: boolean; events?: BMCEvent[]; error?: string }>(`/api/bmc/events?limit=${limit}`, signal),
    enabled: configured, retry: false,
  })

  const healthQ = useQuery({
    queryKey: ['bmc', 'health'],
    queryFn: ({ signal }) => api.get<{ success: boolean; health?: BMCHealth; error?: string }>('/api/bmc/health', signal),
    enabled: configured, refetchInterval: 60_000, retry: false,
  })

  const power = useMutation({
    mutationFn: (action: string) => api.post<{ success: boolean; error?: string }>('/api/bmc/power', { action }),
    onSuccess: (r) => { if (r.success) toast.success('Power command sent'); else toast.error(r.error ?? 'Failed'); qc.invalidateQueries({ queryKey: ['bmc', 'power'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const enroll = useMutation({
    mutationFn: () => api.post<{ success: boolean; fingerprint?: string; error?: string }>('/api/bmc/enroll', {}),
    onSuccess: (r) => { if (r.success) toast.success(`Certificate pinned: ${r.fingerprint}`); else toast.error(r.error ?? 'Enrollment failed'); qc.invalidateQueries({ queryKey: ['bmc'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const resetCert = useMutation({
    mutationFn: () => api.post('/api/bmc/reset-cert', {}),
    onSuccess: () => { toast.success('Pinned certificate cleared: enroll again'); qc.invalidateQueries({ queryKey: ['bmc'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const info = infoQ.data?.info
  const state = powerQ.data?.power_state
  const health = healthQ.data?.success ? healthQ.data.health : undefined
  const sensors = health?.sensors ?? []
  const healthError = healthQ.data && !healthQ.data.success ? healthQ.data.error : (healthQ.error as Error | null)?.message

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 24, marginTop: 28 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16 }}>
        <Icon name="settings_remote" size={18} style={{ color: 'var(--primary)' }} />
        <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>Out-of-band management (BMC)</span>
        <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
          {/* Redfish BMCs need this first, before the other calls work. */}
          <button onClick={() => enroll.mutate()} disabled={enroll.isPending || infoQ.isLoading} className="btn btn-ghost btn-sm" title="Connect once and pin the BMC's TLS certificate (Redfish)">
            <Icon name="verified_user" size={14} />Enroll certificate
          </button>
          {configured && (
            <button onClick={async () => { if (await confirm({ title: 'Clear the pinned certificate?', message: 'Use this after a BMC firmware update changed its certificate, then enroll again.', confirmLabel: 'Clear' })) resetCert.mutate() }}
              disabled={resetCert.isPending} className="btn btn-ghost btn-sm">
              <Icon name="key_off" size={14} />Reset certificate
            </button>
          )}
        </div>
      </div>

      <BMCSettings onSaved={() => qc.invalidateQueries({ queryKey: ['bmc'] })} />

      {infoQ.isLoading && <Skeleton height={80} />}
      {!infoQ.isLoading && !configured && (
        <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          {infoQ.data?.error || (infoQ.error as Error | null)?.message || 'No BMC configured.'}
        </div>
      )}

      {configured && info && (
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: 12, marginBottom: 18 }}>
          {[['Protocol', info.protocol], ['Vendor', info.vendor], ['Model', info.model], ['Firmware', info.firmware_version], ['Address', info.hostname], ['Power', state ?? (powerQ.isLoading ? '…' : powerQ.data?.error ?? 'unknown')]].map(([k, v]) => (
            <div key={k}>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{k}</div>
              <div style={{ fontWeight: 600 }}>{v || '-'}</div>
            </div>
          ))}
        </div>
      )}

      {configured && (
        <>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8, marginBottom: 20 }}>
            {ACTIONS.map(a => (
              <button key={a.action} disabled={power.isPending}
                className={`btn ${a.danger ? 'btn-ghost' : 'btn-primary'}`} style={a.danger ? { color: 'var(--error)' } : undefined}
                onClick={async () => {
                  if (await confirm({ title: `${a.label}?`, message: `${a.text} This acts on this system itself, through its BMC.`, danger: a.danger, confirmLabel: a.label })) power.mutate(a.action)
                }}>
                <Icon name={a.icon} size={14} />{a.label}
              </button>
            ))}
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
            <span style={{ fontWeight: 700 }}>Sensors</span>
            {health && (
              <span style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: STATUS_COLOR[health.overall_status] ?? 'var(--text-tertiary)' }}>
                {health.overall_status}
              </span>
            )}
            <button className="btn btn-ghost btn-sm" style={{ marginLeft: 'auto' }} disabled={healthQ.isFetching} onClick={() => healthQ.refetch()}>
              <Icon name="refresh" size={14} />{healthQ.isFetching ? 'Reading…' : 'Refresh'}
            </button>
          </div>
          {healthQ.isLoading && <Skeleton height={80} />}
          {healthError && <div style={{ color: 'var(--error)', fontSize: 'var(--text-sm)', marginBottom: 16 }}>{healthError}</div>}
          {health && sensors.length === 0 && <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)', marginBottom: 16 }}>The BMC reports no sensors.</div>}
          {CATEGORIES.map(([cat, label]) => {
            const list = sensors.filter(x => (CATEGORIES.some(c => c[0] === x.category) ? x.category : 'other') === cat)
            if (list.length === 0) return null
            return (
              <div key={cat} style={{ marginBottom: 14 }}>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 6 }}>{label}</div>
                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(170px, 1fr))', gap: 8 }}>
                  {list.map(x => (
                    <div key={cat + x.name} style={{ border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', padding: '6px 10px', fontSize: 'var(--text-sm)' }}>
                      <div style={{ color: 'var(--text-secondary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={x.name}>{x.name}</div>
                      <div style={{ fontWeight: 600, color: x.status === 'ok' ? undefined : STATUS_COLOR[x.status] }}>
                        {Number.isInteger(x.value) ? x.value : x.value.toFixed(1)} {SENSOR_UNIT[x.unit] ?? x.unit}
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            )
          })}

          <div style={{ display: 'flex', alignItems: 'center', gap: 8, margin: '20px 0 8px' }}>
            <span style={{ fontWeight: 700 }}>System event log</span>
            <select value={limit} onChange={e => setLimit(Number(e.target.value))} className="input" style={{ width: 'auto', marginLeft: 'auto' }}>
              {[25, 50, 200].map(n => <option key={n} value={n}>last {n}</option>)}
            </select>
          </div>
          {eventsQ.isLoading && <Skeleton height={120} />}
          {eventsQ.data && !eventsQ.data.success && <div style={{ color: 'var(--error)', fontSize: 'var(--text-sm)' }}>{eventsQ.data.error}</div>}
          {eventsQ.data?.events && (
            <div style={{ maxHeight: 320, overflowY: 'auto' }}>
              <table className="table" style={{ width: '100%' }}>
                <thead><tr><th>Time</th><th>Severity</th><th>Source</th><th>Message</th></tr></thead>
                <tbody>
                  {eventsQ.data.events.map(ev => (
                    <tr key={ev.id}>
                      <td style={{ whiteSpace: 'nowrap' }}>{fmtDateTime(ev.timestamp)}</td>
                      <td style={{ color: ev.severity === 'critical' ? 'var(--error)' : ev.severity === 'warning' ? 'var(--warning)' : undefined }}>{ev.severity}</td>
                      <td>{ev.source || '-'}</td>
                      <td>{ev.message}</td>
                    </tr>
                  ))}
                  {eventsQ.data.events.length === 0 && <tr><td colSpan={4} style={{ textAlign: 'center', color: 'var(--text-tertiary)' }}>No events</td></tr>}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
      <ConfirmDialog />
    </div>
  )
}
