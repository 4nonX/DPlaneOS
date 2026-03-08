/**
 * pages/HardwarePage.tsx — Hardware Overview
 *
 * APIs:
 *   GET /api/system/health            → RO filesystem, NTP
 *   GET /api/system/disks             → disk list (lsblk) with pool usage
 *   GET /api/zfs/smart                → SMART health per disk
 */

import { useState, useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { ErrorState } from '@/components/ui/ErrorState'
import { LoadingState, Skeleton } from '@/components/ui/LoadingSpinner'
import { Icon } from '@/components/ui/Icon'
import { useWsStore } from '@/stores/ws'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface DiskInfo {
  name:        string
  size:        string
  type:        string
  model:       string
  serial:      string
  in_use:      boolean
  mount_point: string
}

interface DisksResponse {
  disks: DiskInfo[]
}

interface SMARTDisk {
  device:       string
  error?:       string
  smart_status?: { passed: boolean }
  temperature?:  { current: number }
  temp_warning?: string
  power_on_hours?: { hours: number }
  ata_smart_attributes?: { table: SMARTAttr[] }
}

interface SMARTAttr {
  id:          number
  name:        string
  value:       number
  worst:       number
  thresh:      number
  raw:         { value: number; string: string }
}

interface SMARTResponse {
  success: boolean
  disks:   SMARTDisk[]
}

// ---------------------------------------------------------------------------
// DiskRow
// ---------------------------------------------------------------------------

function DiskRow({ disk, smart }: { disk: DiskInfo; smart?: SMARTDisk }) {
  const passed     = smart?.smart_status?.passed
  const temp       = smart?.temperature?.current
  const tempWarn   = smart?.temp_warning
  const hours      = smart?.power_on_hours?.hours
  const hasError   = !!smart?.error

  return (
    <div style={{
      padding: '14px 16px', background: 'rgba(255,255,255,0.02)',
      border: '1px solid var(--border-subtle)', borderRadius: 'var(--radius-md)',
      display: 'flex', alignItems: 'center', gap: 16,
    }}>
      {/* Disk icon */}
      <Icon
        name={disk.type === 'SSD' || disk.type === 'NVMe' ? 'memory' : 'hard_drive'}
        size={32} style={{ color: 'var(--primary)', flexShrink: 0 }}
      />

      {/* Info */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 600, fontSize: 'var(--text-md)', marginBottom: 2 }}>
          /dev/{disk.name}
          {disk.model && (
            <span style={{ fontWeight: 400, color: 'var(--text-secondary)', marginLeft: 8, fontSize: 'var(--text-sm)' }}>
              {disk.model}
            </span>
          )}
        </div>
        <div style={{ display: 'flex', gap: 16, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', flexWrap: 'wrap' }}>
          <span>{disk.size}</span>
          {disk.type && <span>{disk.type}</span>}
          {disk.serial && <span style={{ fontFamily: 'var(--font-mono)' }}>{disk.serial}</span>}
          {hours !== undefined && <span>{hours.toLocaleString()}h power-on</span>}
        </div>
      </div>

      {/* Temperature */}
      {temp !== undefined && (
        <div style={{
          display: 'flex', alignItems: 'center', gap: 4,
          color: tempWarn === 'critical' ? 'var(--error)' : tempWarn === 'warning' ? 'var(--warning)' : 'var(--text-secondary)',
          fontSize: 'var(--text-sm)', flexShrink: 0,
        }}>
          <Icon name="thermostat" size={16} />
          {temp}°C
        </div>
      )}

      {/* In-use badge */}
      <span style={{
        padding: '3px 8px', borderRadius: 'var(--radius-xs)', fontSize: 'var(--text-xs)', fontWeight: 600,
        background: disk.in_use ? 'var(--primary-bg)' : 'var(--surface)',
        color: disk.in_use ? 'var(--primary)' : 'var(--text-secondary)',
        border: `1px solid ${disk.in_use ? 'rgba(138,156,255,0.25)' : 'var(--border)'}`,
        flexShrink: 0,
      }}>
        {disk.in_use ? 'In use' : 'Free'}
      </span>

      {/* SMART status */}
      {hasError ? (
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', flexShrink: 0 }}>SMART N/A</span>
      ) : smart ? (
        <div style={{ display: 'flex', alignItems: 'center', gap: 4, flexShrink: 0 }}>
          <Icon
            name={passed ? 'check_circle' : 'error'}
            size={18}
            style={{ color: passed ? 'var(--success)' : 'var(--error)' }}
          />
          <span style={{ fontSize: 'var(--text-xs)', color: passed ? 'var(--success)' : 'var(--error)', fontWeight: 600 }}>
            {passed ? 'SMART OK' : 'SMART FAIL'}
          </span>
        </div>
      ) : null}
    </div>
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function HardwarePage() {
  const qc   = useQueryClient()
  const wsOn = useWsStore((s) => s.on)

  // WS: disk temperature warning → immediate SMART refresh + banner
  const [tempWarning, setTempWarning] = useState<{ device: string; temp: number } | null>(null)

  useEffect(() => {
    return wsOn('diskTempWarning', (data) => {
      const d = data as { device?: string; temp?: number }
      if (d?.device) setTempWarning({ device: d.device, temp: d.temp ?? 0 })
      qc.invalidateQueries({ queryKey: ['zfs', 'smart'] })
    })
  }, [wsOn, qc])

  const disksQ = useQuery({
    queryKey: ['system', 'disks'],
    queryFn: ({ signal }) => api.get<DisksResponse>('/api/system/disks', signal),
    refetchInterval: 60_000,
  })

  const smartQ = useQuery({
    queryKey: ['zfs', 'smart'],
    queryFn: ({ signal }) => api.get<SMARTResponse>('/api/zfs/smart', signal),
    refetchInterval: 120_000,
  })

  const healthQ = useQuery({
    queryKey: ['system', 'health'],
    queryFn: ({ signal }) => api.get<{ success: boolean; ntp_synced: boolean; filesystem_ro: boolean; ro_partitions: string[] }>('/api/system/health', signal),
    refetchInterval: 120_000,
  })

  const disks = disksQ.data?.disks ?? []

  // Index SMART data by device name for fast lookup
  const smartByDevice: Record<string, SMARTDisk> = {}
  for (const s of smartQ.data?.disks ?? []) {
    smartByDevice[s.device] = s
  }

  const anySmartFail = (smartQ.data?.disks ?? []).some(
    d => !d.error && d.smart_status && !d.smart_status.passed
  )

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 24, maxWidth: 1100 }}>

      {/* Disk temperature warning banner (from WS) */}
      {tempWarning && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 18px',
          background: 'rgba(239,68,68,0.08)', border: '1px solid rgba(239,68,68,0.3)',
          borderRadius: 'var(--radius-md)', fontSize: 'var(--text-sm)', color: 'var(--error)' }}>
          <Icon name="device_thermostat" size={18} />
          <span>
            <strong>/dev/{tempWarning.device}</strong> temperature warning: {tempWarning.temp}°C
          </span>
          <button
            onClick={() => setTempWarning(null)}
            style={{ marginLeft: 'auto', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--error)', display: 'flex' }}
          >
            <Icon name="close" size={16} />
          </button>
        </div>
      )}

      {/* Health alerts */}
      {healthQ.data?.filesystem_ro && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 18px',
          background: 'var(--error-bg)', border: '1px solid var(--error-border)',
          borderRadius: 'var(--radius-md)', fontSize: 'var(--text-sm)', color: 'var(--error)' }}>
          <Icon name="error" size={18} />
          Read-only filesystem detected: {healthQ.data.ro_partitions.join(', ')}
        </div>
      )}

      {!healthQ.data?.ntp_synced && healthQ.data !== undefined && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 18px',
          background: 'var(--warning-bg)', border: '1px solid var(--warning-border)',
          borderRadius: 'var(--radius-md)', fontSize: 'var(--text-sm)', color: 'var(--warning)' }}>
          <Icon name="schedule" size={18} />
          NTP clock not synchronised — certificate validation and logging may be affected
        </div>
      )}

      {anySmartFail && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 18px',
          background: 'var(--error-bg)', border: '1px solid var(--error-border)',
          borderRadius: 'var(--radius-md)', fontSize: 'var(--text-sm)', color: 'var(--error)' }}>
          <Icon name="hard_drive" size={18} />
          One or more disks are reporting SMART failure — immediate attention required
        </div>
      )}

      {/* Disk list */}
      <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 24 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 20 }}>
          <Icon name="hard_drive" size={18} style={{ color: 'var(--primary)' }} />
          <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>Physical Disks</span>
          {!disksQ.isLoading && (
            <span style={{ marginLeft: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              {disks.length} found
            </span>
          )}
        </div>

        {disksQ.isLoading && <LoadingState message="Scanning disks…" />}
        {disksQ.isError && <ErrorState error={disksQ.error} onRetry={() => disksQ.refetch()} />}
        {!disksQ.isLoading && disks.length === 0 && (
          <div style={{ padding: '24px 0', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
            No disks detected
          </div>
        )}

        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          {disks.map(d => (
            <DiskRow key={d.name} disk={d} smart={smartByDevice[d.name]} />
          ))}
        </div>
      </div>

      {/* SMART detail table for failed or warned disks */}
      {smartQ.data && smartQ.data.disks.some(d => d.ata_smart_attributes?.table.length) && (
        <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 24 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 20 }}>
            <Icon name="analytics" size={18} style={{ color: 'var(--primary)' }} />
            <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>SMART Attributes</span>
          </div>

          {smartQ.data.disks.map(d => {
            const attrs = d.ata_smart_attributes?.table ?? []
            if (!attrs.length) return null
            // Only show key attributes: reallocated, pending, uncorrectable, temperature
            const keyIds = new Set([1, 5, 187, 188, 190, 194, 196, 197, 198, 199])
            const filtered = attrs.filter(a => keyIds.has(a.id))
            if (!filtered.length) return null

            return (
              <div key={d.device} style={{ marginBottom: 20 }}>
                <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)', marginBottom: 10, fontFamily: 'var(--font-mono)' }}>
                  /dev/{d.device}
                </div>
                <div style={{ overflowX: 'auto' }}>
                  <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 'var(--text-xs)', fontFamily: 'var(--font-mono)' }}>
                    <thead>
                      <tr style={{ borderBottom: '1px solid var(--border)' }}>
                        {['ID','Attribute','Value','Worst','Threshold','Raw'].map(h => (
                          <th key={h} style={{ padding: '6px 12px', textAlign: 'left', color: 'var(--text-tertiary)', fontWeight: 500 }}>{h}</th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {filtered.map(a => {
                        const isBad = a.value <= a.thresh
                        return (
                          <tr key={a.id} style={{ borderBottom: '1px solid var(--border-subtle)' }}>
                            <td style={{ padding: '6px 12px', color: 'var(--text-tertiary)' }}>{a.id}</td>
                            <td style={{ padding: '6px 12px', color: isBad ? 'var(--error)' : 'var(--text)' }}>{a.name}</td>
                            <td style={{ padding: '6px 12px', color: isBad ? 'var(--error)' : 'var(--success)' }}>{a.value}</td>
                            <td style={{ padding: '6px 12px' }}>{a.worst}</td>
                            <td style={{ padding: '6px 12px', color: 'var(--text-tertiary)' }}>{a.thresh}</td>
                            <td style={{ padding: '6px 12px' }}>{a.raw.string}</td>
                          </tr>
                        )
                      })}
                    </tbody>
                  </table>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {/* SMART loading / error */}
      {smartQ.isLoading && <Skeleton height={80} borderRadius="var(--radius-xl)" />}
      {smartQ.isError && <ErrorState error={smartQ.error} title="SMART data unavailable" />}
    </div>
  )
}
