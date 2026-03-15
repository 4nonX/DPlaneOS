/**
 * pages/IPMIPage.tsx — IPMI / Hardware Sensors (Phase 6)
 *
 * Reads IPMI sensors (temperature, fan speed, voltage) from the daemon.
 * Read-only monitoring view — no mutations.
 *
 * Calls:
 *   GET  /api/system/ipmi   → { success, sensors: { temp:[], fan:[], voltage:[] } }
 */

import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface Sensor {
  name:    string
  value?:  string | number
  unit?:   string
  status?: string   // ok, warning, critical, na
}

interface IPMIResponse {
  success: boolean
  sensors: {
    temp?:    Sensor[]
    fan?:     Sensor[]
    voltage?: Sensor[]
  }
}

// ---------------------------------------------------------------------------
// Sensor status helpers
// ---------------------------------------------------------------------------

function sensorColor(status: string | undefined): string {
  const s = (status ?? 'ok').toLowerCase()
  if (s === 'critical') return 'var(--error)'
  if (s === 'warning')  return 'rgba(251,191,36,0.9)'
  if (s === 'na')       return 'var(--text-tertiary)'
  return 'var(--success)'
}

function sensorBg(status: string | undefined): string {
  const s = (status ?? 'ok').toLowerCase()
  if (s === 'critical') return 'var(--error-bg)'
  if (s === 'warning')  return 'rgba(251,191,36,0.08)'
  if (s === 'na')       return 'var(--surface)'
  return 'var(--success-bg)'
}

function sensorBorder(status: string | undefined): string {
  const s = (status ?? 'ok').toLowerCase()
  if (s === 'critical') return 'var(--error-border)'
  if (s === 'warning')  return 'rgba(251,191,36,0.3)'
  if (s === 'na')       return 'var(--border)'
  return 'var(--success-border)'
}

// ---------------------------------------------------------------------------
// SensorCard — single reading
// ---------------------------------------------------------------------------

function SensorCard({ sensor }: { sensor: Sensor }) {
  const val    = sensor.value !== undefined && sensor.value !== null ? String(sensor.value) : 'N/A'
  const unit   = sensor.unit ?? ''
  const color  = sensorColor(sensor.status)
  const status = (sensor.status ?? 'ok').toLowerCase()

  return (
    <div style={{ background: 'var(--bg-card)', border: `1px solid ${sensorBorder(sensor.status)}`, borderRadius: 'var(--radius-lg)', padding: '16px 18px', display: 'flex', alignItems: 'center', gap: 14 }}>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontWeight: 600, marginBottom: 4 }}>{sensor.name}</div>
        <div style={{ fontFamily: 'var(--font-mono)', fontWeight: 700, fontSize: 'var(--text-lg)', color, lineHeight: 1 }}>
          {val}<span style={{ fontSize: 'var(--text-xs)', marginLeft: 3, fontWeight: 400, color: 'var(--text-secondary)' }}>{unit}</span>
        </div>
      </div>
      {status !== 'na' && (
        <div style={{ width: 32, height: 32, borderRadius: 'var(--radius-sm)', background: sensorBg(sensor.status), border: `1px solid ${sensorBorder(sensor.status)}`, display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
          <Icon name={status === 'critical' ? 'error' : status === 'warning' ? 'warning' : 'check_circle'} size={16} style={{ color }} />
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Sensor section
// ---------------------------------------------------------------------------

function SensorSection({ icon, title, sensors, emptyMsg }: {
  icon:     string
  title:    string
  sensors:  Sensor[]
  emptyMsg: string
}) {
  if (!sensors || sensors.length === 0) {
    return (
      <div>
        <div style={{ fontWeight: 700, marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
          <Icon name={icon} size={18} style={{ color: 'var(--primary)' }} />{title}
        </div>
        <div className="card" style={{ padding: '24px', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)', borderRadius: 'var(--radius-lg)' }}>
          {emptyMsg}
        </div>
      </div>
    )
  }

  const criticalCount = sensors.filter(s => (s.status ?? 'ok').toLowerCase() === 'critical').length
  const warningCount  = sensors.filter(s => (s.status ?? 'ok').toLowerCase() === 'warning').length

  return (
    <div>
      <div style={{ fontWeight: 700, marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
        <Icon name={icon} size={18} style={{ color: 'var(--primary)' }} />
        {title}
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>({sensors.length})</span>
        {criticalCount > 0 && (
          <span className="badge badge-error">
            {criticalCount} CRITICAL
          </span>
        )}
        {warningCount > 0 && (
          <span className="badge badge-warning">
            {warningCount} WARNING
          </span>
        )}
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))', gap: 10 }}>
        {sensors.map((s, i) => <SensorCard key={`${s.name}-${i}`} sensor={s} />)}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// IPMIPage
// ---------------------------------------------------------------------------

export function IPMIPage() {
  const qc = useQueryClient()

  const ipmiQ = useQuery({
    queryKey: ['system', 'ipmi'],
    queryFn:  ({ signal }) => api.get<IPMIResponse>('/api/system/ipmi', signal),
    refetchInterval: 30_000,
  })

  const sensors = ipmiQ.data?.sensors ?? {}
  const allSensors = [...(sensors.temp ?? []), ...(sensors.fan ?? []), ...(sensors.voltage ?? [])]
  const hasCritical = allSensors.some(s => (s.status ?? 'ok').toLowerCase() === 'critical')

  if (ipmiQ.isLoading) return <Skeleton height={400} />
  if (ipmiQ.isError)   return <ErrorState error={ipmiQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['system', 'ipmi'] })} />

  return (
    <div style={{ maxWidth: 1000 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 28 }}>
        <div>
          <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>IPMI Sensors</h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>Temperature, fan speed and voltage readings from BMC</p>
        </div>
        <button onClick={() => qc.invalidateQueries({ queryKey: ['system', 'ipmi'] })} className="btn btn-ghost">
          <Icon name="refresh" size={14} />Refresh
        </button>
      </div>

      {hasCritical && (
        <div className="alert alert-error" style={{ marginBottom: 24 }}>
          <Icon name="error" size={18} style={{ flexShrink: 0 }} />
          <span style={{ fontWeight: 700 }}>Critical sensor readings detected — immediate attention required</span>
        </div>
      )}

      {allSensors.length === 0 ? (
        <div className="card" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '60px 0', gap: 12, borderRadius: 'var(--radius-xl)' }}>
          <Icon name="developer_board" size={48} style={{ color: 'var(--text-tertiary)', opacity: 0.4 }} />
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No IPMI sensors detected</div>
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)', maxWidth: 340, textAlign: 'center' }}>
            IPMI requires a BMC (Baseboard Management Controller). Virtual machines and consumer hardware typically do not have one.
          </div>
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 28 }}>
          <SensorSection icon="thermostat"    title="Temperature" sensors={sensors.temp ?? []} emptyMsg="No temperature sensors" />
          <SensorSection icon="mode_fan"      title="Fan Speeds"  sensors={sensors.fan  ?? []} emptyMsg="No fan speed sensors" />
          <SensorSection icon="bolt"          title="Voltages"    sensors={sensors.voltage ?? []} emptyMsg="No voltage sensors" />
        </div>
      )}
    </div>
  )
}
