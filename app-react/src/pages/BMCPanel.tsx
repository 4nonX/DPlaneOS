/**
 * BMCPanel - out-of-band management through the node's BMC (Redfish or
 * IPMI), shown on the IPMI page.
 *
 * Calls:
 *   GET  /api/bmc/info, /api/bmc/power, /api/bmc/events?limit=
 *   POST /api/bmc/power {action}, /api/bmc/enroll, /api/bmc/reset-cert
 *
 * Credentials come from the power fencing settings (High Availability page).
 */
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'
import { fmtDateTime } from '@/lib/fmt'

interface BMCInfo { protocol: string; vendor?: string; model?: string; firmware_version?: string; hostname?: string }
interface BMCEvent { id: string; timestamp: string; severity: string; message: string; source?: string }

const ACTIONS: { action: string; label: string; icon: string; danger: boolean; text: string }[] = [
  { action: 'on',             label: 'Power on',       icon: 'power',              danger: false, text: 'Switch the machine on.' },
  { action: 'graceful_reset', label: 'Restart',        icon: 'restart_alt',        danger: true,  text: 'Ask the operating system to restart.' },
  { action: 'graceful_off',   label: 'Shut down',      icon: 'power_settings_new', danger: true,  text: 'Ask the operating system to shut down.' },
  { action: 'reset',          label: 'Hard reset',     icon: 'bolt',               danger: true,  text: 'Reset the machine immediately, like the reset button: unsaved data is lost.' },
  { action: 'off',            label: 'Power off',      icon: 'power_off',          danger: true,  text: 'Cut power immediately: unsaved data is lost and pools may need a scrub.' },
]

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

      {infoQ.isLoading && <Skeleton height={80} />}
      {!infoQ.isLoading && !configured && (
        <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
          {infoQ.data?.error || (infoQ.error as Error | null)?.message || 'No BMC configured.'}{' '}
          Set the BMC address and credentials under High Availability › Watchdog and power fencing; they are used here as well.
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
                  if (await confirm({ title: `${a.label}?`, message: `${a.text} This acts on the machine this BMC belongs to, usually this NAS itself.`, danger: a.danger, confirmLabel: a.label })) power.mutate(a.action)
                }}>
                <Icon name={a.icon} size={14} />{a.label}
              </button>
            ))}
          </div>

          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
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
