/**
 * pages/HardwarePage.tsx — Hardware Overview
 *
 * APIs:
 *   GET  /api/system/health              → RO filesystem, NTP
 *   GET  /api/system/disks               → disk list (lsblk) with pool usage
 *   GET  /api/zfs/smart                  → SMART health per disk
 *   POST /api/zfs/smart/test             → { device, type } → { success, output, estimate }
 *   GET  /api/zfs/smart/results?device=X → { success, device, results }
 */

import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { ErrorState } from '@/components/ui/ErrorState'
import { LoadingState, Skeleton, Spinner } from '@/components/ui/LoadingSpinner'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { useWsStore } from '@/stores/ws'
import { toast } from '@/hooks/useToast'

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

interface SMARTTestResponse {
  success:  boolean
  device?:  string
  type?:    string
  estimate?: string
  output?:  string
  error?:   string
}

interface SMARTResultsResponse {
  success:  boolean
  device?:  string
  results?: string
  error?:   string
}

// ---------------------------------------------------------------------------
// SmartResultsModal
// ---------------------------------------------------------------------------

function SmartResultsModal({ device, onClose }: { device: string; onClose: () => void }) {
  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['smart', 'results', device],
    queryFn: ({ signal }) =>
      api.get<SMARTResultsResponse>(`/api/zfs/smart/results?device=${encodeURIComponent(device)}`, signal),
  })

  return (
    <Modal title={`SMART Test Results — /dev/${device}`} onClose={onClose} size="lg">
      <div style={{ padding: '0 0 4px' }}>
        {isLoading && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '16px 0', color: 'var(--text-secondary)', fontSize: 'var(--text-sm)' }}>
            <Spinner size={16} />
            Loading results…
          </div>
        )}
        {isError && <ErrorState error={error} title="Failed to load results" />}
        {data && !data.success && (
          <div className="alert alert-error">
            <Icon name="error" size={16} />
            {data.error ?? 'Failed to retrieve results'}
          </div>
        )}
        {data?.success && (
          <pre style={{
            fontFamily: 'var(--font-mono)',
            fontSize: 'var(--text-xs)',
            color: 'var(--text-secondary)',
            background: 'var(--surface)',
            border: '1px solid var(--border)',
            borderRadius: 'var(--radius-sm)',
            padding: '14px 16px',
            overflowX: 'auto',
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
            maxHeight: 420,
            overflowY: 'auto',
            margin: 0,
          }}>
            {data.results || '(no results)'}
          </pre>
        )}
      </div>
      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={onClose}>Close</button>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// DiskRow
// ---------------------------------------------------------------------------

interface DiskRowProps {
  disk: DiskInfo
  smart?: SMARTDisk
  onShortTest: () => void
  onLongTest:  () => void
  onViewResults: () => void
  isTestRunning: boolean
  testResult: SMARTTestResponse | null
}

function DiskRow({
  disk, smart,
  onShortTest, onLongTest, onViewResults,
  isTestRunning, testResult,
}: DiskRowProps) {
  const passed     = smart?.smart_status?.passed
  const temp       = smart?.temperature?.current
  const tempWarn   = smart?.temp_warning
  const hours      = smart?.power_on_hours?.hours
  const hasError   = !!smart?.error

  return (
    <div style={{
      padding: '14px 16px', background: 'rgba(255,255,255,0.02)',
      border: '1px solid var(--border-subtle)', borderRadius: 'var(--radius-md)',
    }}>
      {/* Main row */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 16 }}>
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

        {/* SMART test buttons */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, flexShrink: 0 }}>
          {isTestRunning ? (
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
              <Spinner size={14} />
              <span>Running…</span>
            </div>
          ) : (
            <>
              <button
                className="btn btn-ghost"
                onClick={onShortTest}
                title="Run short SMART self-test (~2 min)"
                style={{ fontSize: 'var(--text-xs)', padding: '4px 10px' }}
              >
                Short Test
              </button>
              <button
                className="btn btn-ghost"
                onClick={onLongTest}
                title="Run long SMART self-test (hours)"
                style={{ fontSize: 'var(--text-xs)', padding: '4px 10px' }}
              >
                Long Test
              </button>
              <button
                className="btn btn-ghost"
                onClick={onViewResults}
                title="View last SMART test results"
                style={{ fontSize: 'var(--text-xs)', padding: '4px 10px' }}
              >
                <Icon name="analytics" size={14} style={{ marginRight: 4 }} />
                View Results
              </button>
            </>
          )}
        </div>
      </div>

      {/* Test result banner (shown inline under this disk row) */}
      {testResult && !isTestRunning && (
        <div
          className={testResult.success ? 'alert alert-success' : 'alert alert-error'}
          style={{ marginTop: 10, fontSize: 'var(--text-xs)' }}
        >
          <Icon name={testResult.success ? 'check_circle' : 'error'} size={14} />
          {testResult.success
            ? `${testResult.type === 'long' ? 'Long' : 'Short'} test started — estimated duration: ${testResult.estimate ?? 'unknown'}`
            : testResult.error ?? 'Test failed'}
        </div>
      )}
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

  // Per-disk test state: which disk is running, results per disk
  const [runningDevice, setRunningDevice]   = useState<string | null>(null)
  const [testResults, setTestResults]       = useState<Record<string, SMARTTestResponse>>({})
  const [resultsModalDevice, setResultsModalDevice] = useState<string | null>(null)

  const smartTestMutation = useMutation({
    mutationFn: (vars: { device: string; type: 'short' | 'long' }) =>
      api.post<SMARTTestResponse>('/api/zfs/smart/test', vars),
    onMutate: (vars) => {
      setRunningDevice(vars.device)
      setTestResults((prev) => {
        const next = { ...prev }
        delete next[vars.device]
        return next
      })
    },
    onSuccess: (data, vars) => {
      setRunningDevice(null)
      setTestResults((prev) => ({ ...prev, [vars.device]: data }))
      if (data.success) {
        toast.success(`SMART ${vars.type} test started on /dev/${vars.device}`)
      } else {
        toast.error(`SMART test failed: ${data.error ?? 'Unknown error'}`)
      }
    },
    onError: (err, vars) => {
      setRunningDevice(null)
      toast.error(`Failed to start SMART test on /dev/${vars.device}: ${(err as Error).message}`)
    },
  })

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

      <div className="page-header">
        <h1 className="page-title">Hardware</h1>
        <p className="page-subtitle">Disks, SMART health, and system component status</p>
      </div>

      {/* Disk temperature warning banner (from WS) */}
      {tempWarning && (
        <div className="alert alert-error">
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
        <div className="alert alert-error">
          <Icon name="error" size={18} />
          Read-only filesystem detected: {healthQ.data.ro_partitions.join(', ')}
        </div>
      )}

      {!healthQ.data?.ntp_synced && healthQ.data !== undefined && (
        <div className="alert alert-warning">
          <Icon name="schedule" size={18} />
          NTP clock not synchronised — certificate validation and logging may be affected
        </div>
      )}

      {anySmartFail && (
        <div className="alert alert-error">
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
            <DiskRow
              key={d.name}
              disk={d}
              smart={smartByDevice[d.name]}
              isTestRunning={runningDevice === d.name}
              testResult={testResults[d.name] ?? null}
              onShortTest={() => smartTestMutation.mutate({ device: d.name, type: 'short' })}
              onLongTest={() => smartTestMutation.mutate({ device: d.name, type: 'long' })}
              onViewResults={() => setResultsModalDevice(d.name)}
            />
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
                  <table className="data-table" style={{ fontSize: 'var(--text-xs)', fontFamily: 'var(--font-mono)' }}>
                    <thead>
                      <tr>
                        {['ID','Attribute','Value','Worst','Threshold','Raw'].map(h => (
                          <th key={h}>{h}</th>
                        ))}
                      </tr>
                    </thead>
                    <tbody>
                      {filtered.map(a => {
                        const isBad = a.value <= a.thresh
                        return (
                          <tr key={a.id}>
                            <td style={{ color: 'var(--text-tertiary)' }}>{a.id}</td>
                            <td style={{ color: isBad ? 'var(--error)' : 'var(--text)' }}>{a.name}</td>
                            <td style={{ color: isBad ? 'var(--error)' : 'var(--success)' }}>{a.value}</td>
                            <td>{a.worst}</td>
                            <td style={{ color: 'var(--text-tertiary)' }}>{a.thresh}</td>
                            <td>{a.raw.string}</td>
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

      {/* SMART Results Modal */}
      {resultsModalDevice && (
        <SmartResultsModal
          device={resultsModalDevice}
          onClose={() => setResultsModalDevice(null)}
        />
      )}
    </div>
  )
}
