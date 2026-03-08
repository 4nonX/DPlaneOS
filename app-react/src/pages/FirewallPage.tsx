/**
 * pages/FirewallPage.tsx — Firewall (Phase 6)
 *
 * Shows current nftables/iptables status and allows adding allow/deny rules.
 * The daemon uses a simple rule model: action, port, from (optional).
 * "Sync to Nix" writes rules to NixOS configuration.
 *
 * Calls:
 *   GET  /api/firewall/status           → { success, status: "active"|"inactive", rules?: Rule[] }
 *   POST /api/firewall/rule             → { action: "enable"|"disable"|"allow"|"deny", port?, from? }
 *   POST /api/firewall/sync             → commit rules to NixOS config
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

interface FirewallRule {
  id?:      number | string
  action:   'allow' | 'deny'
  port?:    string
  from?:    string
  proto?:   string
  comment?: string
}

interface FirewallStatus {
  success: boolean
  status:  'active' | 'inactive' | string
  rules?:  FirewallRule[]
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const btnGhost: React.CSSProperties = {
  padding: '7px 13px', background: 'var(--surface)', color: 'var(--text-secondary)',
  border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 500, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnPrimary: React.CSSProperties = {
  padding: '8px 18px', background: 'var(--primary)', color: '#000',
  border: 'none', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 700, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnDanger: React.CSSProperties = {
  padding: '7px 14px', background: 'var(--error-bg)', border: '1px solid var(--error-border)',
  borderRadius: 'var(--radius-sm)', cursor: 'pointer', color: 'var(--error)',
  fontSize: 'var(--text-sm)', fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const inputStyle: React.CSSProperties = {
  background: 'var(--surface)', border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)', padding: '8px 12px',
  color: 'var(--text)', fontSize: 'var(--text-sm)', width: '100%',
  fontFamily: 'var(--font-ui)', outline: 'none', boxSizing: 'border-box',
}

// ---------------------------------------------------------------------------
// Status banner
// ---------------------------------------------------------------------------

function StatusBanner({ active, onToggle, pending }: { active: boolean; onToggle: () => void; pending: boolean }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 16, padding: '16px 22px', background: 'var(--bg-card)', border: `1px solid ${active ? 'rgba(16,185,129,0.25)' : 'rgba(239,68,68,0.25)'}`, borderRadius: 'var(--radius-xl)', marginBottom: 24 }}>
      <div style={{ width: 48, height: 48, borderRadius: 'var(--radius-md)', background: active ? 'rgba(16,185,129,0.1)' : 'var(--error-bg)', border: `1px solid ${active ? 'rgba(16,185,129,0.25)' : 'var(--error-border)'}`, display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
        <Icon name={active ? 'shield' : 'shield_with_heart'} size={24} style={{ color: active ? 'var(--success)' : 'var(--error)' }} />
      </div>
      <div style={{ flex: 1 }}>
        <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)', color: active ? 'var(--success)' : 'var(--error)' }}>
          Firewall {active ? 'Active' : 'Inactive'}
        </div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
          {active ? 'nftables rules are applied and protecting the system' : 'No firewall rules are active — all traffic is allowed'}
        </div>
      </div>
      <button onClick={onToggle} disabled={pending} style={active ? btnDanger : btnPrimary}>
        <Icon name={active ? 'shield_x' : 'shield'} size={15} />
        {pending ? (active ? 'Disabling…' : 'Enabling…') : (active ? 'Disable' : 'Enable')}
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// AddRuleForm
// ---------------------------------------------------------------------------

function AddRuleForm({ onAdd, pending }: { onAdd: (rule: { action: string; port: string; from: string; proto: string }) => void; pending: boolean }) {
  const [action, setAction] = useState<'allow' | 'deny'>('allow')
  const [port,   setPort]   = useState('')
  const [from,   setFrom]   = useState('')
  const [proto,  setProto]  = useState('tcp')

  function submit() {
    if (!port.trim()) { toast.error('Port is required'); return }
    onAdd({ action, port: port.trim(), from: from.trim(), proto })
    setPort(''); setFrom('')
  }

  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '18px 22px', marginBottom: 24 }}>
      <div style={{ fontWeight: 700, marginBottom: 14 }}>Add Rule</div>
      <div style={{ display: 'grid', gridTemplateColumns: '100px 100px 1fr 1fr auto', gap: 10, alignItems: 'end' }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontWeight: 600 }}>Action</span>
          <select value={action} onChange={e => setAction(e.target.value as 'allow' | 'deny')} style={{ ...inputStyle, appearance: 'none' }}>
            <option value="allow">Allow</option>
            <option value="deny">Deny</option>
          </select>
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontWeight: 600 }}>Protocol</span>
          <select value={proto} onChange={e => setProto(e.target.value)} style={{ ...inputStyle, appearance: 'none' }}>
            <option value="tcp">tcp</option>
            <option value="udp">udp</option>
            <option value="both">tcp+udp</option>
          </select>
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontWeight: 600 }}>Port / Range</span>
          <input value={port} onChange={e => setPort(e.target.value)} placeholder="80  or  8000:8100"
            style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} onKeyDown={e => e.key === 'Enter' && submit()} />
        </label>

        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontWeight: 600 }}>Source IP (optional)</span>
          <input value={from} onChange={e => setFrom(e.target.value)} placeholder="192.168.1.0/24 or any"
            style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} onKeyDown={e => e.key === 'Enter' && submit()} />
        </label>

        <button onClick={submit} disabled={pending} style={{ ...btnPrimary, alignSelf: 'flex-end' }}>
          <Icon name="add" size={15} />{pending ? 'Adding…' : 'Add'}
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// FirewallPage
// ---------------------------------------------------------------------------

export function FirewallPage() {
  const qc = useQueryClient()

  const statusQ = useQuery({
    queryKey: ['firewall', 'status'],
    queryFn:  ({ signal }) => api.get<FirewallStatus>('/api/firewall/status', signal),
    refetchInterval: 15_000,
  })

  const rulesMut = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('/api/firewall/rule', body),
    onSuccess: () => { toast.success('Rule applied'); qc.invalidateQueries({ queryKey: ['firewall', 'status'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const syncMut = useMutation({
    mutationFn: () => api.post('/api/firewall/sync', {}),
    onSuccess: () => toast.success('Firewall synced to NixOS configuration'),
    onError: (e: Error) => toast.error(e.message),
  })

  const isActive = statusQ.data?.status === 'active'
  const rules    = statusQ.data?.rules ?? []

  if (statusQ.isLoading) return <Skeleton height={320} />
  if (statusQ.isError)   return <ErrorState error={statusQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['firewall', 'status'] })} />

  return (
    <div style={{ maxWidth: 900 }}>
      <div style={{ marginBottom: 28 }}>
        <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>Firewall</h1>
        <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>nftables rules — allow and deny traffic by port and source</p>
      </div>

      <StatusBanner
        active={isActive}
        onToggle={() => rulesMut.mutate({ action: isActive ? 'disable' : 'enable' })}
        pending={rulesMut.isPending}
      />

      <AddRuleForm
        onAdd={r => rulesMut.mutate({ action: r.action, port: r.port, from: r.from || undefined, proto: r.proto })}
        pending={rulesMut.isPending}
      />

      {/* Rules table */}
      <div style={{ marginBottom: 14, display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div style={{ fontWeight: 700 }}>Active Rules ({rules.length})</div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button onClick={() => qc.invalidateQueries({ queryKey: ['firewall', 'status'] })} style={btnGhost}>
            <Icon name="refresh" size={14} />Refresh
          </button>
          <button onClick={() => syncMut.mutate()} disabled={syncMut.isPending} style={btnGhost}>
            <Icon name="sync" size={14} />{syncMut.isPending ? 'Syncing…' : 'Sync to NixOS'}
          </button>
        </div>
      </div>

      {rules.length > 0 ? (
        <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead>
              <tr style={{ background: 'rgba(255,255,255,0.03)' }}>
                {['Action', 'Protocol', 'Port', 'Source', ''].map(h => (
                  <th key={h} style={{ padding: '10px 16px', textAlign: 'left', fontSize: 'var(--text-2xs)', fontWeight: 700, color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', borderBottom: '1px solid var(--border)' }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rules.map((rule, idx) => (
                <tr key={rule.id ?? idx} style={{ borderBottom: '1px solid var(--border)' }}
                  onMouseEnter={e => (e.currentTarget.style.background = 'rgba(255,255,255,0.02)')}
                  onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
                  <td style={{ padding: '11px 16px' }}>
                    <span style={{ padding: '3px 10px', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', fontWeight: 700,
                      background: rule.action === 'allow' ? 'var(--success-bg)' : 'var(--error-bg)',
                      border:     rule.action === 'allow' ? '1px solid var(--success-border)' : '1px solid var(--error-border)',
                      color:      rule.action === 'allow' ? 'var(--success)' : 'var(--error)' }}>
                      {rule.action.toUpperCase()}
                    </span>
                  </td>
                  <td style={{ padding: '11px 16px', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                    {rule.proto ?? 'tcp'}
                  </td>
                  <td style={{ padding: '11px 16px', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', fontWeight: 600 }}>
                    {rule.port ?? '—'}
                  </td>
                  <td style={{ padding: '11px 16px', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                    {rule.from ?? 'any'}
                  </td>
                  <td style={{ padding: '11px 16px', textAlign: 'right' }}>
                    <button onClick={() => rulesMut.mutate({ action: 'remove', id: rule.id, port: rule.port, from: rule.from })}
                      disabled={rulesMut.isPending}
                      style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', padding: 4, borderRadius: 'var(--radius-xs)', display: 'inline-flex' }}
                      onMouseEnter={e => (e.currentTarget.style.color = 'var(--error)')}
                      onMouseLeave={e => (e.currentTarget.style.color = 'var(--text-tertiary)')}>
                      <Icon name="delete" size={16} />
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '48px 0', gap: 12, background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)' }}>
          <Icon name="shield" size={40} style={{ color: 'var(--text-tertiary)', opacity: 0.4 }} />
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No rules configured</div>
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>Add a rule above to control traffic</div>
        </div>
      )}
    </div>
  )
}
