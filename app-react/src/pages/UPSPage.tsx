/**
 * pages/UPSPage.tsx — UPS Management (Phase 6)
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

function ConfigPanel({ initial }: { initial: UPSResponse }) {
  const qc = useQueryClient()
  const [driver,   setDriver]   = useState(initial.driver   ?? 'usbhid-ups')
  const [host,     setHost]     = useState(initial.host     ?? 'localhost')
  const [port,     setPort]     = useState(String(initial.port ?? 3493))
  const [name,     setName]     = useState(initial.name     ?? 'ups')
  const [shutLevel,setShutLevel]= useState(String(initial.shutdown_level ?? 10))

  const save = useMutation({
    mutationFn: () => api.post('/api/system/ups', {
      driver, host, port: Number(port), name, shutdown_level: Number(shutLevel),
    }),
    onSuccess: () => { toast.success('UPS configuration saved'); qc.invalidateQueries({ queryKey: ['system', 'ups'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px', marginTop: 24 }}>
      <div style={{ fontWeight: 700, marginBottom: 16, display: 'flex', alignItems: 'center', gap: 8 }}>
        <Icon name="settings" size={18} style={{ color: 'var(--primary)' }} />Configure NUT Connection
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 80px', gap: 14, marginBottom: 14 }}>
        <label className="field">
          <span className="field-label">Driver</span>
          <select value={driver} onChange={e => setDriver(e.target.value)} className="input" style={{ appearance: 'none' }}>
            {['usbhid-ups', 'blazer_usb', 'blazer_ser', 'snmp-ups', 'nutdrv_qx'].map(d => (
              <option key={d} value={d}>{d}</option>
            ))}
          </select>
        </label>
        <label className="field">
          <span className="field-label">NUT Host</span>
          <input value={host} onChange={e => setHost(e.target.value)} placeholder="localhost" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Port</span>
          <input type="number" value={port} onChange={e => setPort(e.target.value)} className="input" />
        </label>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 120px', gap: 14, marginBottom: 16 }}>
        <label className="field">
          <span className="field-label">UPS Name</span>
          <input value={name} onChange={e => setName(e.target.value)} placeholder="ups" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </label>
        <label className="field">
          <span className="field-label">Shutdown %</span>
          <input type="number" value={shutLevel} onChange={e => setShutLevel(e.target.value)} min={5} max={95} className="input" />
        </label>
      </div>
      <button onClick={() => save.mutate()} disabled={save.isPending} className="btn btn-primary">
        <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save Config'}
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

      {showConfig && upsQ.data && <ConfigPanel initial={upsQ.data} />}
    </div>
  )
}
