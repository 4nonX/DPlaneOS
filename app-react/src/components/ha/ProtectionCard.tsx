/**
 * ProtectionCard - how this HA setup is protected (Design 0001 phase 3c, ADR-0009).
 *
 * One line per layer (third vote, watchdog, ZFS multihost, disk reservations,
 * power fencing) with what it adds or what is missing, in plain language.
 * Missing hardware is explained, never a blocker; only automatic failover
 * itself needs three votes and a fencing method.
 *
 * Calls: GET /api/ha/protection
 */

import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'

interface Protection {
  success: boolean
  layers: { name: string; state: 'ok' | 'warn' | 'off'; detail: string }[]
  auto_failover: boolean
  auto_failover_reason?: string
  failover_delay_secs: number
  self_fence_armed: boolean
}

const STATE = {
  ok:   { icon: 'check_circle', color: 'var(--success)', label: 'Active' },
  warn: { icon: 'warning',      color: 'var(--warning)', label: 'Limited' },
  off:  { icon: 'radio_button_unchecked', color: 'var(--text-tertiary)', label: 'Not in use' },
} as const

export function ProtectionCard() {
  const q = useQuery({
    queryKey: ['ha', 'protection'],
    queryFn: ({ signal }) => api.get<Protection>('/api/ha/protection', signal),
    refetchInterval: 15_000,
  })
  if (!q.data) return null
  const p = q.data
  return (
    <div className="card" style={{ marginBottom: 20 }}>
      <h3 style={{ marginTop: 0 }}>How this setup is protected</h3>
      {p.self_fence_armed && (
        <div role="alert" style={{ padding: '8px 12px', borderRadius: 8, background: 'var(--error-bg)', color: 'var(--error)', marginBottom: 10, fontSize: 'var(--text-sm)' }}>
          This node owns storage and has lost quorum: it no longer resets its watchdog and will restart shortly, so the other node can take over safely.
        </div>
      )}
      <table className="data-table" style={{ fontSize: 'var(--text-sm)' }}>
        <tbody>
          {p.layers.map(l => (
            <tr key={l.name}>
              <td style={{ whiteSpace: 'nowrap', verticalAlign: 'top' }}>
                <Icon name={STATE[l.state].icon} size={16} style={{ color: STATE[l.state].color, verticalAlign: 'middle' }} />{' '}
                <strong>{l.name}</strong>
              </td>
              <td style={{ color: 'var(--text-secondary)' }}>{l.detail}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p style={{ fontSize: 'var(--text-sm)', marginBottom: 0 }}>
        {p.auto_failover
          ? <>Automatic failover is <strong>on</strong>: if the owner of a storage group fails, another node takes over after about {p.failover_delay_secs} seconds (time for the failed node's watchdog to stop it).</>
          : <>Automatic failover is <strong>off</strong>: {p.auto_failover_reason}. Everything else works; if a node fails, use <em>Take over</em> on a storage group.</>}
      </p>
    </div>
  )
}
