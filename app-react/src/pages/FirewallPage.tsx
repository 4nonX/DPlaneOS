/**
 * pages/FirewallPage.tsx - Firewall
 *
 * On NixOS the firewall itself is part of the system configuration (it opens
 * the ports the configured services need, shown read-only). Rules made here
 * apply at once on top of it, in order (the first that matches decides): allow
 * opens a port, optionally only for a source; deny blocks one, also a port
 * the system opens. Every change is reverted unless confirmed
 * (PendingChangeBanner), so a rule that locks you out undoes itself.
 * Elsewhere the page drives ufw.
 *
 * Calls:
 *   GET  /api/firewall/status → { success, status, managed_by?: "nixos", rules: Rule[], system_ports?, rules_loaded? }
 *   POST /api/firewall/rule   { action: allow|deny, port, proto, from?, comment? }
 *                             { action: delete, rule_num } | { action: update, rule_num, new_action, port, proto, from?, comment? }
 *                             { action: enable|disable } (ufw only)
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { PendingChangeBanner, useChangeApplied } from '@/components/network/PendingChangeBanner'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface FirewallRule {
  id:       number
  action:   'allow' | 'deny'
  port?:    string
  from?:    string
  proto?:   string
  comment?: string
}

interface PortRange { from: number; to: number }
interface SystemPorts { enabled: boolean; tcp: number[]; udp: number[]; tcp_ranges: PortRange[]; udp_ranges: PortRange[]; trusted_interfaces: string[] }

interface FirewallStatus {
  success:       boolean
  status:        'active' | 'inactive' | string
  managed_by?:   'nixos'
  rules?:        FirewallRule[]
  system_ports?: SystemPorts
  rules_loaded?: boolean
}

type RuleInput = { action: 'allow' | 'deny'; port: string; from: string; proto: string; comment: string }

// ---------------------------------------------------------------------------
// Rule form (add and edit)
// ---------------------------------------------------------------------------

function RuleFields({ value, onChange, onEnter }: { value: RuleInput; onChange: (v: RuleInput) => void; onEnter?: () => void }) {
  const set = (p: Partial<RuleInput>) => onChange({ ...value, ...p })
  const enter = (e: React.KeyboardEvent) => { if (e.key === 'Enter') onEnter?.() }
  return (
    <>
      <label className="field">
        <span className="field-label">Action</span>
        <select value={value.action} onChange={e => set({ action: e.target.value as 'allow' | 'deny' })} className="input" style={{ appearance: 'none' }}>
          <option value="allow">Allow</option>
          <option value="deny">Deny</option>
        </select>
      </label>
      <label className="field">
        <span className="field-label">Protocol</span>
        <select value={value.proto} onChange={e => set({ proto: e.target.value })} className="input" style={{ appearance: 'none' }}>
          <option value="tcp">tcp</option>
          <option value="udp">udp</option>
          <option value="both">tcp+udp</option>
        </select>
      </label>
      <label className="field">
        <span className="field-label">Port / Range</span>
        <input value={value.port} onChange={e => set({ port: e.target.value })} onKeyDown={enter} placeholder="80  or  8000-8100"
          className="input" style={{ fontFamily: 'var(--font-mono)' }} />
      </label>
      <label className="field">
        <span className="field-label">Source (optional)</span>
        <input value={value.from} onChange={e => set({ from: e.target.value })} onKeyDown={enter} placeholder="192.168.1.0/24 (empty = any)"
          className="input" style={{ fontFamily: 'var(--font-mono)' }} />
      </label>
    </>
  )
}

const emptyRule: RuleInput = { action: 'allow', port: '', from: '', proto: 'tcp', comment: '' }

function EditRuleModal({ rule, onClose, onSave, pending }: { rule: FirewallRule; onClose: () => void; onSave: (r: RuleInput) => void; pending: boolean }) {
  const [v, setV] = useState<RuleInput>({
    action: rule.action, port: rule.port ?? '', proto: rule.proto ?? 'tcp', comment: rule.comment ?? '',
    from: rule.from && rule.from !== 'Anywhere' ? rule.from : '',
  })
  return (
    <div role="dialog" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 'var(--z-modal)', backdropFilter: 'blur(4px)' }}>
      <div className="card" style={{ width: 450, padding: 24, borderRadius: 'var(--radius-xl)' }}>
        <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)', marginBottom: 20, display: 'flex', alignItems: 'center', gap: 10 }}>
          <Icon name="edit" size={20} style={{ color: 'var(--primary)' }} />Edit rule #{rule.id}
        </div>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <RuleFields value={v} onChange={setV} />
        </div>
        <div style={{ display: 'flex', gap: 10, justifyContent: 'flex-end', marginTop: 24 }}>
          <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button onClick={() => v.port.trim() ? onSave(v) : toast.error('Port is required')} disabled={pending} className="btn btn-primary">
            {pending ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  )
}

function portList(ports: number[], ranges: PortRange[]): string {
  return [...ports.map(String), ...ranges.map(r => `${r.from}-${r.to}`)].join(', ') || 'none'
}

// ---------------------------------------------------------------------------
// FirewallPage
// ---------------------------------------------------------------------------

export function FirewallPage() {
  const qc = useQueryClient()
  const applied = useChangeApplied()
  const [editingRule, setEditingRule] = useState<FirewallRule | null>(null)
  const [draft, setDraft] = useState<RuleInput>(emptyRule)
  const { confirm, ConfirmDialog } = useConfirm()

  const statusQ = useQuery({
    queryKey: ['firewall', 'status'],
    queryFn:  ({ signal }) => api.get<FirewallStatus>('/api/firewall/status', signal),
    refetchInterval: 15_000,
  })
  const nixos = statusQ.data?.managed_by === 'nixos'

  const rulesMut = useMutation({
    mutationFn: async (body: Record<string, unknown>) =>
      ensureOk(await api.post<{ success?: boolean; error?: string; message?: string }>('/api/firewall/rule', body)),
    onSuccess: r => {
      qc.invalidateQueries({ queryKey: ['firewall', 'status'] })
      if (nixos) applied(r.message ?? 'Applied - confirm to keep it')
      else toast.success('Firewall updated')
      setEditingRule(null)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  if (statusQ.isLoading) return <Skeleton height={320} />
  if (statusQ.isError)   return <ErrorState error={statusQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['firewall', 'status'] })} />

  const isActive = statusQ.data?.status === 'active'
  const rules    = statusQ.data?.rules ?? []
  const sys      = statusQ.data?.system_ports

  function add() {
    if (!draft.port.trim()) { toast.error('Port is required'); return }
    rulesMut.mutate({ action: draft.action, port: draft.port.trim(), proto: draft.proto, from: draft.from.trim() || undefined })
    setDraft(d => ({ ...d, port: '', from: '' }))
  }

  return (
    <div style={{ maxWidth: 900 }}>
      {editingRule && (
        <EditRuleModal rule={editingRule} onClose={() => setEditingRule(null)} pending={rulesMut.isPending}
          onSave={v => nixos
            ? rulesMut.mutate({ action: 'update', rule_num: editingRule.id, new_action: v.action, port: v.port.trim(), proto: v.proto, from: v.from.trim() || undefined })
            // ufw has no update: delete, then add
            : rulesMut.mutateAsync({ action: 'delete', rule_num: editingRule.id }).then(() =>
                rulesMut.mutate({ action: v.action, port: v.port.trim(), proto: v.proto, from: v.from.trim() || undefined }))}
        />
      )}

      <div className="page-header">
        <h1 className="page-title">Firewall</h1>
        <p className="page-subtitle">Allow and deny traffic by port and source</p>
      </div>

      <PendingChangeBanner />

      {/* Status */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 16, padding: '16px 22px', background: 'var(--bg-card)', border: `1px solid ${isActive ? 'var(--success-border)' : 'var(--error-border)'}`, borderRadius: 'var(--radius-xl)', marginBottom: 24 }}>
        <Icon name="shield" size={24} style={{ color: isActive ? 'var(--success)' : 'var(--error)' }} />
        <div style={{ flex: 1 }}>
          <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)', color: isActive ? 'var(--success)' : 'var(--error)' }}>
            Firewall {isActive ? 'active' : 'inactive'}
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            {nixos
              ? 'Incoming connections are refused unless the system configuration or a rule below allows them.'
              : isActive ? 'ufw rules are applied' : 'No firewall rules are active - all traffic is allowed'}
          </div>
        </div>
        {!nixos && (
          <button onClick={() => rulesMut.mutate({ action: isActive ? 'disable' : 'enable' })} disabled={rulesMut.isPending} className={isActive ? 'btn btn-danger' : 'btn btn-primary'}>
            {isActive ? 'Disable' : 'Enable'}
          </button>
        )}
      </div>

      {nixos && sys && (
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '16px 22px', marginBottom: 24, fontSize: 'var(--text-sm)' }}>
          <div style={{ fontWeight: 700, marginBottom: 8 }}>Opened by the system configuration</div>
          <div style={{ display: 'grid', gridTemplateColumns: '60px 1fr', gap: 6, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>
            <span style={{ color: 'var(--text-tertiary)' }}>TCP</span><span>{portList(sys.tcp, sys.tcp_ranges)}</span>
            <span style={{ color: 'var(--text-tertiary)' }}>UDP</span><span>{portList(sys.udp, sys.udp_ranges)}</span>
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 8 }}>
            These follow the services that are enabled. To limit one to some networks, add an allow rule for those networks and then a deny rule for the port.
          </div>
        </div>
      )}

      {/* Add rule */}
      <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '18px 22px', marginBottom: 24 }}>
        <div style={{ fontWeight: 700, marginBottom: 14 }}>Add Rule</div>
        <div style={{ display: 'grid', gridTemplateColumns: '100px 110px 1fr 1fr auto', gap: 10, alignItems: 'end' }}>
          <RuleFields value={draft} onChange={setDraft} onEnter={add} />
          <button onClick={add} disabled={rulesMut.isPending} className="btn btn-primary">
            <Icon name="add" size={15} />{rulesMut.isPending ? 'Adding…' : 'Add Rule'}
          </button>
        </div>
        {nixos && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 10 }}>Rules apply at once, in this order, and are reverted unless you confirm them.</div>}
      </div>

      {/* Rules */}
      <div style={{ marginBottom: 14, display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div style={{ fontWeight: 700 }}>Rules ({rules.length})</div>
        <button onClick={() => qc.invalidateQueries({ queryKey: ['firewall', 'status'] })} className="btn btn-ghost">
          <Icon name="refresh" size={14} />Refresh
        </button>
      </div>
      {nixos && statusQ.data?.rules_loaded === false && rules.length > 0 && (
        <div className="alert alert-error" style={{ marginBottom: 12 }}>The rules are not loaded in the kernel; check the daemon log.</div>
      )}

      {rules.length > 0 ? (
        <div className="card" style={{ borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
          <table className="data-table">
            <thead>
              <tr>{['#', 'Action', 'Protocol', 'Port', 'Source', ''].map(h => <th key={h}>{h}</th>)}</tr>
            </thead>
            <tbody>
              {rules.map(rule => (
                <tr key={rule.id}>
                  <td style={{ color: 'var(--text-tertiary)' }}>{rule.id}</td>
                  <td><span className={rule.action === 'allow' ? 'badge badge-success' : 'badge badge-error'}>{rule.action.toUpperCase()}</span></td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>{rule.proto === 'both' ? 'tcp+udp' : rule.proto ?? 'tcp'}</td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', fontWeight: 600 }}>{rule.port ?? '-'}</td>
                  <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>{rule.from || 'Anywhere'}</td>
                  <td style={{ textAlign: 'right' }}>
                    <div style={{ display: 'flex', gap: 4, justifyContent: 'flex-end' }}>
                      <button onClick={() => setEditingRule(rule)} disabled={rulesMut.isPending} className="btn btn-ghost btn-xs" aria-label={`Edit rule ${rule.id}`}>
                        <Icon name="edit" size={16} />
                      </button>
                      <button onClick={async () => { if (await confirm({ title: `Delete rule #${rule.id}?`, message: 'Traffic matching this rule is then handled by the next rule or the system configuration.', danger: true, confirmLabel: 'Delete Rule' })) { rulesMut.mutate({ action: 'delete', rule_num: rule.id }) } }}
                        disabled={rulesMut.isPending} className="btn btn-ghost btn-xs" aria-label={`Delete rule ${rule.id}`}>
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
        <div className="card" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', padding: '48px 0', gap: 12, borderRadius: 'var(--radius-lg)' }}>
          <Icon name="shield" size={40} style={{ color: 'var(--text-tertiary)', opacity: 0.4 }} />
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No rules</div>
        </div>
      )}
      <ConfirmDialog />
    </div>
  )
}
