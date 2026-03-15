/**
 * pages/PowerPage.tsx — Disk Power Management (Phase 6)
 *
 * Lists all detected disks with their current power state (active/standby/sleeping).
 * Per-disk spindown timeout (hdparm -S equivalent) plus an immediate spindown button.
 *
 * Calls:
 *   GET  /api/power/disks                         → { success, disks: Disk[], spindown: Record<device, timeout> }
 *   POST /api/power/spindown   { device, timeout } → set spindown timeout (0 = disabled)
 *   POST /api/power/spindown-now { device }        → immediate hdparm -y
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { Tooltip } from '@/components/ui/Tooltip'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface Disk {
  device:      string
  model?:      string
  size?:       string
  transport?:  string
  rotational?: string   // "1" = HDD, "0" = SSD
  power_state?: string  // active, standby, sleeping, unknown
  apm_level?:  string
}

interface PowerResponse {
  success:  boolean
  disks:    Disk[]
  spindown: Record<string, number>
}

// Spindown timeout options matching hdparm -S encoding
const SPINDOWN_OPTIONS: { label: string; value: number }[] = [
  { label: 'No spindown',   value: 0 },
  { label: '5 minutes',     value: 60 },
  { label: '10 minutes',    value: 120 },
  { label: '20 minutes',    value: 240 },
  { label: '30 minutes',    value: 241 },
  { label: '1 hour',        value: 242 },
  { label: '2 hours',       value: 244 },
]

// Power state badge
function PowerStateBadge({ state }: { state: string }) {
  const s = (state ?? 'unknown').toLowerCase()
  let color = 'var(--text-tertiary)'
  let bg    = 'var(--surface)'
  let border= 'var(--border)'
  if (s === 'active') { color = 'var(--success)'; bg = 'var(--success-bg)'; border = 'var(--success-border)' }
  else if (s === 'standby') { color = 'rgba(251,191,36,0.9)'; bg = 'rgba(251,191,36,0.08)'; border = 'rgba(251,191,36,0.3)' }
  else if (s === 'sleeping') { color = 'var(--text-secondary)'; bg = 'rgba(99,102,241,0.1)'; border = 'rgba(99,102,241,0.3)' }

  return (
    <span style={{ padding: '2px 8px', borderRadius: 'var(--radius-sm)', background: bg, border: `1px solid ${border}`, color, fontSize: 'var(--text-xs)', fontWeight: 700, textTransform: 'capitalize' }}>
      {s}
    </span>
  )
}

// ---------------------------------------------------------------------------
// DiskRow — individual disk with spindown select + spindown-now button
// ---------------------------------------------------------------------------

function DiskRow({ disk, savedTimeout, onSpindown, onSpindownNow, pending }: {
  disk:          Disk
  savedTimeout:  number
  onSpindown:    (device: string, timeout: number) => void
  onSpindownNow: (device: string) => void
  pending:       boolean
}) {
  const isHDD = disk.rotational === '1'
  const [timeout, setTimeout] = useState(savedTimeout)

  function handleTimeoutChange(val: number) {
    setTimeout(val)
    onSpindown(disk.device, val)
  }

  return (
    <div className="card" style={{ display: 'flex', alignItems: 'center', gap: 16, padding: '14px 20px', borderRadius: 'var(--radius-lg)' }}>
      {/* Icon */}
      <div className="card" style={{ background: 'var(--surface)',  width: 40, height: 40, borderRadius: 'var(--radius-md)', display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
        <Icon name={isHDD ? 'hard_drive' : 'memory'} size={22} style={{ color: isHDD ? 'var(--primary)' : 'rgba(99,102,241,0.9)' }} />
      </div>

      {/* Info */}
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 700, fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', marginBottom: 3 }}>
          {disk.device}
        </div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'flex', gap: 10, flexWrap: 'wrap' }}>
          {disk.model     && <span>{disk.model}</span>}
          {disk.size      && <span>{disk.size}</span>}
          {disk.transport && <span>{disk.transport}</span>}
          {disk.apm_level && <span>APM: {disk.apm_level}</span>}
          <span>{isHDD ? 'HDD' : 'SSD/NVMe'}</span>
        </div>
      </div>

      {/* Power state */}
      <PowerStateBadge state={disk.power_state ?? 'unknown'} />

      {/* Spindown select */}
      {isHDD && (
        <select value={timeout}
          onChange={e => handleTimeoutChange(Number(e.target.value))}
          disabled={pending}
          className="card" style={{ background: 'var(--surface)', borderRadius: 'var(--radius-sm)', padding: '6px 10px', color: 'var(--text)', fontSize: 'var(--text-xs)', outline: 'none', cursor: 'pointer' }}>
          {SPINDOWN_OPTIONS.map(o => (
            <option key={o.value} value={o.value}>{o.label}</option>
          ))}
        </select>
      )}

      {/* Spindown now */}
      {isHDD && (
        <Tooltip content="Spin down immediately">
          <button onClick={() => onSpindownNow(disk.device)} disabled={pending} className="btn btn-ghost">
            <Icon name="power_settings_new" size={14} />Spindown
          </button>
        </Tooltip>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// PowerPage
// ---------------------------------------------------------------------------

export function PowerPage() {
  const qc = useQueryClient()

  const disksQ = useQuery({
    queryKey: ['power', 'disks'],
    queryFn:  ({ signal }) => api.get<PowerResponse>('/api/power/disks', signal),
    refetchInterval: 30_000,
  })

  const spindownMut = useMutation({
    mutationFn: ({ device, timeout }: { device: string; timeout: number }) =>
      api.post('/api/power/spindown', { device, timeout }),
    onSuccess: () => { toast.success('Spindown timeout saved') },
    onError: (e: Error) => toast.error(e.message),
  })

  const spindownNowMut = useMutation({
    mutationFn: (device: string) => api.post('/api/power/spindown-now', { device }),
    onSuccess: (_data, device) => {
      toast.success(`Spindown sent to ${device}`)
      globalThis.setTimeout(() => qc.invalidateQueries({ queryKey: ['power', 'disks'] }), 2000)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const disks    = disksQ.data?.disks    ?? []
  const spindown = disksQ.data?.spindown ?? {}

  // Split into HDDs and SSDs
  const hdds = disks.filter(d => d.rotational === '1')
  const ssds = disks.filter(d => d.rotational !== '1')

  if (disksQ.isLoading) return <Skeleton height={300} />
  if (disksQ.isError)   return <ErrorState error={disksQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['power', 'disks'] })} />

  const pending = spindownMut.isPending || spindownNowMut.isPending

  return (
    <div style={{ maxWidth: 900 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 28 }}>
        <div className="page-header">
          <h1 className="page-title">Disk Power</h1>
          <p className="page-subtitle">Spindown timeouts and on-demand park for HDDs</p>
        </div>
        <button onClick={() => qc.invalidateQueries({ queryKey: ['power', 'disks'] })} className="btn btn-ghost">
          <Icon name="refresh" size={14} />Refresh
        </button>
      </div>

      {disks.length === 0 ? (
        <div className="card" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '60px 0', gap: 12, borderRadius: 'var(--radius-xl)' }}>
          <Icon name="storage" size={48} style={{ color: 'var(--text-tertiary)', opacity: 0.4 }} />
          <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No disks detected</div>
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 24 }}>
          {hdds.length > 0 && (
            <div>
              <div style={{ fontWeight: 700, marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
                <Icon name="hard_drive" size={18} style={{ color: 'var(--primary)' }} />
                HDDs ({hdds.length})
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                {hdds.map(disk => (
                  <DiskRow
                    key={disk.device}
                    disk={disk}
                    savedTimeout={spindown[disk.device] ?? 0}
                    onSpindown={(dev, t) => spindownMut.mutate({ device: dev, timeout: t })}
                    onSpindownNow={dev => spindownNowMut.mutate(dev)}
                    pending={pending}
                  />
                ))}
              </div>
            </div>
          )}

          {ssds.length > 0 && (
            <div>
              <div style={{ fontWeight: 700, marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
                <Icon name="memory" size={18} style={{ color: 'rgba(99,102,241,0.9)' }} />
                SSDs / NVMe ({ssds.length})
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                {ssds.map(disk => (
                  <DiskRow
                    key={disk.device}
                    disk={disk}
                    savedTimeout={0}
                    onSpindown={() => {}}
                    onSpindownNow={() => {}}
                    pending={false}
                  />
                ))}
              </div>
              <div style={{ marginTop: 8, padding: '10px 14px', background: 'var(--surface)', borderRadius: 'var(--radius-sm)', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', display: 'flex', gap: 7 }}>
                <Icon name="info" size={13} style={{ flexShrink: 0, marginTop: 1 }} />
                Spindown is not applicable to SSDs and NVMe drives
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
