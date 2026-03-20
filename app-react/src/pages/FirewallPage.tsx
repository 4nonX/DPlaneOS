/**
 * pages/FirewallPage.tsx - Firewall (Phase 6)
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
          {active ? 'nftables rules are applied and protecting the system' : 'No firewall rules are active - all traffic is allowed'}
        </div>
      </div>
      <button onClick={onToggle} disabled={pending} className={active ? 'btn btn-danger' : 'btn btn-primary'}>
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
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '18px 22px', marginBottom: 24 }}>
      <div style={{ fontWeight: 700, marginBottom: 14 }}>Add Rule</div>
      <div style={{ display: 'grid', gridTemplateColumns: '100px 100px 1fr 1fr auto', gap: 10, alignItems: 'end' }}>
        <label className="field">
          <span className="field-label">Action</span>
          <select value={action} onChange={e => setAction(e.target.value as 'allow' | 'deny')} className="input" style={{ appearance: 'none' }}>
            <option value="allow">Allow</option>
            <option value="deny">Deny</option>
          </select>
        </label>

        <label className="field">
          <span className="field-label">Protocol</span>
          <select value={proto} onChange={e => setProto(e.target.value)} className="input" style={{ appearance: 'none' }}>
            <option value="tcp">tcp</option>
            <option value="udp">udp</option>
            <option value="both">tcp+udp</option>
          </select>
        </label>

        <label className="field">
          <span className="field-label">Port / Range</span>
          <input value={port} onChange={e => setPort(e.target.value)} placeholder="80  or  8000:8100"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} onKeyDown={e => e.key === 'Enter' && submit()} />
        </label>

        <label className="field">
          <span className="field-label">Source IP (optional)</span>
          <input value={from} onChange={e => setFrom(e.target.value)} placeholder="192.168.1.0/24 or any"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} onKeyDown={e => e.key === 'Enter' && submit()} />
        </label>

        <button onClick={submit} disabled={pending} className="btn btn-primary" style={{ alignSelf: 'flex-end' }}>
          <Icon name="add" size={15} />{pending ? 'Adding…' : 'Add'}
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// FirewallPage
// ---------------------------------------------------------------------------

function EditRuleModal({ rule, onClose, onSave, pending }: { rule: FirewallRule; onClose: () => void; onSave: (r: { action: string; port: string; from: string; proto: string }) => void; pending: boolean }) {
  const [action, setAction] = useState<'allow' | 'deny'>(rule.action)
  const [port,   setPort]   = useState(rule.port || '')
  const [from,   setFrom]   = useState(rule.from || '')
  const [proto,  setProto]  = useState(rule.proto || 'tcp')

  function submit() {
    if (!port.trim()) { toast.error('Port is required'); return }
    onSave({ action, port: port.trim(), from: from.trim(), proto })
  }

  return (
    <div style={{ position: 'fixed', top: 0, left: 0, right: 0, bottom: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 100, backdropFilter: 'blur(4px)' }}>
      <div className="card" style={{ width: 450, padding: 24, borderRadius: 'var(--radius-xl)', boxShadow: '0 20px 25px -5px rgba(0,0,0,0.3)' }}>
        <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)', marginBottom: 20, display: 'flex', alignItems: 'center', gap: 10 }}>
          <Icon name="edit" size={20} style={{ color: 'var(--primary)' }} />
          Edit Firewall Rule
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <label className="field">
            <span className="field-label">Action</span>
            <select value={action} onChange={e => setAction(e.target.value as 'allow' | 'deny')} className="input">
              <option value="allow">Allow</option>
              <option value="deny">Deny</option>
            </select>
          </label>

          <label className="field">
            <span className="field-label">Protocol</span>
            <select value={proto} onChange={e => setProto(e.target.value)} className="input">
              <option value="tcp">tcp</option>
              <option value="udp">udp</option>
              <option value="both">tcp+udp</option>
            </select>
          </label>

          <label className="field">
            <span className="field-label">Port / Range</span>
            <input value={port} onChange={e => setPort(e.target.value)} placeholder="80" className="input" />
          </label>

          <label className="field">
            <span className="field-label">Source IP (optional)</span>
            <input value={from} onChange={e => setFrom(e.target.value)} placeholder="any" className="input" />
          </label>
        </div>

        <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', marginTop: 24 }}>
          <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button onClick={submit} disabled={pending} className="btn btn-primary">
            {pending ? 'Saving...' : 'Save Changes'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// FirewallPage
// ---------------------------------------------------------------------------

export function FirewallPage() {
  const qc = useQueryClient()
  const [editingRule, setEditingRule] = useState<FirewallRule | null>(null)

  const statusQ = useQuery({
    queryKey: ['firewall', 'status'],
    queryFn:  ({ signal }) => api.get<FirewallStatus>('/api/firewall/status', signal),
    refetchInterval: 15_000,
  })

  const rulesMut = useMutation({
    mutationFn: (body: Record<string, unknown>) => api.post('/api/firewall/rule', body),
    onSuccess: () => {
      toast.success('Rule applied')
      qc.invalidateQueries({ queryKey: ['firewall', 'status'] })
      setEditingRule(null)
    },
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

  function handleEditSave(updated: { action: string; port: string; from: string; proto: string }) {
    if (!editingRule) return
    // To "edit" a ufw rule, we must delete the old one by ID and add the new one
    // But since the backend SetRule for "delete" takes rule_num (id), we can do it in two steps
    // or sequential mutations. For simplicity, we sequence them.
    rulesMut.mutateAsync({ action: 'delete', rule_num: editingRule.id }).then(() => {
      rulesMut.mutate({ action: updated.action, port: updated.port, from: updated.from || undefined, proto: updated.proto })
    })
  }

  return (
    <div style={{ maxWidth: 900 }}>
      {editingRule && (
        <EditRuleModal
          rule={editingRule}
          onClose={() => setEditingRule(null)}
          onSave={handleEditSave}
          pending={rulesMut.isPending}
        />
      )}

      <div className="page-header">
        <h1 className="page-title">Firewall</h1>
        <p className="page-subtitle">nftables rules - allow and deny traffic by port and source</p>
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
          <button onClick={() => qc.invalidateQueries({ queryKey: ['firewall', 'status'] })} className="btn btn-ghost">
            <Icon name="refresh" size={14} />Refresh
          </button>
          <button onClick={() => syncMut.mutate()} disabled={syncMut.isPending} className="btn btn-ghost">
            <Icon name="sync" size={14} />{syncMut.isPending ? 'Syncing…' : 'Sync to NixOS'}
          </button>
        </div>
      </div>

      {rules.length > 0 ? (
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
          <table className="data-table">
            <thead>
              <tr style={{ background: 'rgba(255,255,255,0.03)' }}>
                {['Action', 'Protocol', 'Port', 'Source', ''].map(h => (
                  <th key={h}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rules.map((rule, idx) => (
                <tr key={rule.id ?? idx}
                  onMouseEnter={e => (e.currentTarget.style.background = 'rgba(255,255,255,0.02)')}
                  onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}>
                  <td>
                    <span className={rule.action === 'allow' ? 'badge badge-success' : 'badge badge-error'}>
                      {rule.action.toUpperCase()}
                    </span>
                  </td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                    {rule.proto ?? 'tcp'}
                  </td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', fontWeight: 600 }}>
                    {rule.port ?? '-'}
                  </td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                    {rule.from ?? 'any'}
                  </td>
                  <td style={{ textAlign: 'right' }}>
                    <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                      <button onClick={() => setEditingRule(rule)}
                        disabled={rulesMut.isPending}
                        style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', padding: 4, borderRadius: 'var(--radius-xs)', display: 'inline-flex' }}
                        onMouseEnter={e => (e.currentTarget.style.color = 'var(--primary)')}
                        onMouseLeave={e => (e.currentTarget.style.color = 'var(--text-tertiary)')}>
                        <Icon name="edit" size={16} />
                      </button>
                      <button onClick={() => rulesMut.mutate({ action: 'delete', rule_num: rule.id })}
                        disabled={rulesMut.isPending}
                        style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', padding: 4, borderRadius: 'var(--radius-xs)', display: 'inline-flex' }}
                        onMouseEnter={e => (e.currentTarget.style.color = 'var(--error)')}
                        onMouseLeave={e => (e.currentTarget.style.color = 'var(--text-tertiary)')}>
                        <Icon name="delete" size={16} />
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="card" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '48px 0', gap: 12, borderRadius: 'var(--radius-lg)' }}>
          <Icon name="shield" size={40} style={{ color: 'var(--text-tertiary)', opacity: 0.4 }} />
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No rules configured</div>
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>Add a rule above to control traffic</div>
        </div>
      )}
    </div>
  )
}


