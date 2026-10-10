/**
 * pages/UPSPage.tsx - UPS Management (Phase 6)
 *
 * Displays UPS status (battery charge, runtime, load, voltages) and
 * allows configuring the UPS monitoring daemon connection.
 *
 * Calls:
 *   GET  /api/system/ups           → { success, data: UPSData }
 *   POST /api/system/ups           → { driver, host, port, name, shutdown_level } → save config
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

interface UPSData {
  status?:          string   // "OL" online, "OB" on battery, "LB" low battery
  battery_charge?:  string   // e.g. "95%"
  battery_runtime?: string   // e.g. "47 min"
  load?:            string   // e.g. "28%"
  input_voltage?:   string   // e.g. "230"
  output_voltage?:  string   // e.g. "230"
  model?:           string
  manufacturer?:    string
  serial?:          string
  firmware?:        string
  ups_status?:      string
}

interface UPSResponse {
  success: boolean
  data?:   UPSData
  // config fields may also be present
  driver?: string
  host?:   string
  port?:   number
  name?:   string
  shutdown_level?: number
}

// ---------------------------------------------------------------------------
// Battery gauge
// ---------------------------------------------------------------------------

function BatteryGauge({ charge }: { charge: number }) {
  const pct = Math.min(100, Math.max(0, charge))
  const color = pct > 60 ? 'var(--success)' : pct > 20 ? 'rgba(251,191,36,0.9)' : 'var(--error)'
  return (
    <div style={{ width: '100%', height: 12, background: 'var(--surface)', borderRadius: 6, overflow: 'hidden', border: '1px solid var(--border)' }}>
      <div style={{ width: `${pct}%`, height: '100%', background: color, borderRadius: 6, transition: 'width 0.5s ease' }} />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Stat card
// ---------------------------------------------------------------------------

function StatCard({ icon, label, value, sub, color }: { icon: string; label: string; value: string; sub?: string; color?: string }) {
  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', textAlign: 'center', display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 8 }}>
      <Icon name={icon} size={32} style={{ color: color ?? 'var(--primary)', marginBottom: 4 }} />
      <div style={{ fontSize: 28, fontWeight: 700, fontFamily: 'var(--font-mono)', color: color ?? 'var(--text)', lineHeight: 1 }}>{value}</div>
      <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: '0.5px', fontWeight: 600 }}>{label}</div>
      {sub && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{sub}</div>}
    </div>
  )
}

// ---------------------------------------------------------------------------
// UPS Status display
// ---------------------------------------------------------------------------

function parseCharge(s: string | undefined): number {
  if (!s) return 0
  return parseFloat(s.replace('%', '')) || 0
}

function statusInfo(s: string | undefined): { label: string; color: string } {
  if (!s) return { label: 'Unknown', color: 'var(--text-tertiary)' }
  const u = s.toUpperCase()
  if (u.includes('OB')) return { label: 'On Battery', color: 'var(--error)' }
  if (u.includes('LB')) return { label: 'Low Battery', color: 'var(--error)' }
  if (u.includes('OL')) return { label: 'Online',      color: 'var(--success)' }
  if (u.includes('CHRG')) return { label: 'Charging',  color: 'rgba(251,191,36,0.9)' }
  return { label: s, color: 'var(--text-secondary)' }
}

// ---------------------------------------------------------------------------
// ConfigPanel
// ---------------------------------------------------------------------------

interface UPSPolicy { success: boolean; action: 'shutdown' | 'hibernate'; threshold: number; grace: number }

// The shutdown policy is what the API stores (POST /api/system/ups takes
// action, threshold, grace). The NUT connection (driver, port, UPS name) is
// part of the NixOS configuration: services.dplaneos.ups.
function ConfigPanel() {
  const qc = useQueryClient()
  const policyQ = useQuery({
    queryKey: ['system', 'ups', 'policy'],
    queryFn: ({ signal }) => api.get<UPSPolicy>('/api/system/ups/policy', signal),
  })
  if (policyQ.isLoading) return <Skeleton height={160} />
  if (policyQ.isError || !policyQ.data) return <ErrorState error={policyQ.error} onRetry={() => policyQ.refetch()} />
  return <PolicyForm initial={policyQ.data} onSaved={() => qc.invalidateQueries({ queryKey: ['system', 'ups'] })} />
}

function PolicyForm({ initial, onSaved }: { initial: UPSPolicy; onSaved: () => void }) {
  const [action, setAction] = useState(initial.action)
  const [threshold, setThreshold] = useState(String(initial.threshold))
  const [grace, setGrace] = useState(String(initial.grace))

  const save = useMutation({
    mutationFn: () => {
      const t = Number(threshold), g = Number(grace)
      if (!(t >= 1 && t <= 99)) throw new Error('Shutdown level must be 1 to 99 %')
      if (!(g >= 0 && g <= 600)) throw new Error('Grace period must be 0 to 600 seconds')
      return api.post('/api/system/ups', { action, threshold: t, grace: g })
    },
    onSuccess: () => { toast.success('UPS shutdown policy saved'); onSaved() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24 }}>
      <div style={{ fontWeight: 700, marginBottom: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
        <Icon name="settings" size={18} style={{ color: 'var(--primary)' }} />Shutdown Policy
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(160px, 1fr))', gap: 14, marginBottom: 14 }}>
        <label className="field">
          <span className="field-label">On low battery</span>
          <select value={action} onChange={e => setAction(e.target.value as UPSPolicy['action'])} className="input" style={{ appearance: 'none' }}>
            <option value="shutdown">Shut down</option>
            <option value="hibernate">Hibernate</option>
          </select>
        </label>
        <label className="field">
          <span className="field-label">Shutdown at battery %</span>
          <input type="number" value={threshold} onChange={e => setThreshold(e.target.value)} min={1} max={99} className="input" />
        </label>
        <label className="field">
          <span className="field-label">Grace period (seconds)</span>
          <input type="number" value={grace} onChange={e => setGrace(e.target.value)} min={0} max={600} className="input" />
        </label>
      </div>
      <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: '0 0 16px' }}>
        The UPS connection (driver, port, UPS name) is set in the NixOS configuration under
        <code style={{ margin: '0 4px' }}>services.dplaneos.ups</code>. The policy applies with the next pending NixOS changes.
      </p>
      <button onClick={() => save.mutate()} disabled={save.isPending} className="btn btn-primary">
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Policy'}
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// UPSPage
// ---------------------------------------------------------------------------

export function UPSPage() {
  const qc = useQueryClient()
  const [showConfig, setShowConfig] = useState(false)

  const upsQ = useQuery({
    queryKey: ['system', 'ups'],
    queryFn:  ({ signal }) => api.get<UPSResponse>('/api/system/ups', signal),
    refetchInterval: 15_000,
  })

  const upsData = upsQ.data?.data
  const charge  = parseCharge(upsData?.battery_charge)
  const { label: statusLabel, color: statusColor } = statusInfo(upsData?.status)

  if (upsQ.isLoading) return <Skeleton height={320} />
  if (upsQ.isError)   return <ErrorState error={upsQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['system', 'ups'] })} />

  const hasData = !!upsData

  return (
    <div style={{ maxWidth: 860 }}>
      <div className="page-header">
        <h1 className="page-title">UPS Management</h1>
        <p className="page-subtitle">Monitor battery status and configure auto-shutdown</p>
      </div>
      <div style={{ display: 'flex', gap: 8, marginBottom: 28 }}>
        <button onClick={() => qc.invalidateQueries({ queryKey: ['system', 'ups'] })} className="btn btn-ghost">
          <Icon name="refresh" size={14} />Refresh
        </button>
        <button onClick={() => setShowConfig(!showConfig)} className="btn btn-ghost">
          <Icon name="settings" size={14} />{showConfig ? 'Hide Config' : 'Configure'}
        </button>
      </div>

      {!hasData ? (
        <div className="card" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '60px 0', gap: 16, borderRadius: 'var(--radius-xl)' }}>
          <Icon name="battery_unknown" size={56} style={{ color: 'var(--text-tertiary)', opacity: 0.4 }} />
          <div style={{ fontWeight: 700, color: 'var(--text-secondary)', fontSize: 'var(--text-lg)' }}>No UPS detected</div>
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)', maxWidth: 380, textAlign: 'center' }}>
            Connect a UPS device and ensure NUT (Network UPS Tools) is installed and configured.
          </div>
          <button onClick={() => setShowConfig(true)} className="btn btn-primary"><Icon name="settings" size={15} />Configure NUT</button>
        </div>
      ) : (
        <>
          {/* Top stat cards */}
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 16, marginBottom: 24 }}>
            <StatCard icon="battery_charging_full" label="Battery Charge"
              value={upsData.battery_charge ?? 'N/A'} color={charge > 60 ? 'var(--success)' : charge > 20 ? 'rgba(251,191,36,0.9)' : 'var(--error)'} />
            <StatCard icon="schedule"               label="Runtime"         value={upsData.battery_runtime ?? 'N/A'} />
            <StatCard icon="power"                  label="Status"          value={statusLabel} color={statusColor} />
          </div>

          {/* Battery gauge */}
          {upsData.battery_charge && (
            <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '16px 22px', marginBottom: 20 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8 }}>
                <span style={{ fontSize: 'var(--text-sm)', fontWeight: 600 }}>Battery Level</span>
                <span style={{ fontSize: 'var(--text-sm)', fontWeight: 700, color: charge > 60 ? 'var(--success)' : 'var(--error)' }}>{upsData.battery_charge}</span>
              </div>
              <BatteryGauge charge={charge} />
            </div>
          )}

          {/* UPS info grid */}
          <div className="card" style={{ borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
            <div style={{ padding: '14px 20px', borderBottom: '1px solid var(--border)', fontWeight: 700 }}>UPS Details</div>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 0 }}>
              {[
                ['Model',          upsData.model],
                ['Manufacturer',   upsData.manufacturer],
                ['Serial',         upsData.serial],
                ['Firmware',       upsData.firmware],
                ['Load',           upsData.load],
                ['Input Voltage',  upsData.input_voltage  ? `${upsData.input_voltage}V`  : undefined],
                ['Output Voltage', upsData.output_voltage ? `${upsData.output_voltage}V` : undefined],
                ['Status Code',    upsData.ups_status ?? upsData.status],
              ].filter(([, v]) => !!v).map(([label, value], i) => (
                <div key={label as string} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '12px 20px', borderBottom: '1px solid var(--border)', background: i % 2 === 0 ? 'transparent' : 'rgba(255,255,255,0.01)' }}>
                  <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontWeight: 600, minWidth: 100 }}>{label as string}</span>
                  <span style={{ fontSize: 'var(--text-sm)', fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>{value as string}</span>
                </div>
              ))}
            </div>
          </div>
        </>
      )}

      {showConfig && <ConfigPanel />}
    </div>
  )
}

