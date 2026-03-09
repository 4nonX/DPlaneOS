/**
 * pages/HardwarePage.tsx — Hardware Overview
 *
 * APIs:
 *   GET  /api/system/health              → RO filesystem, NTP
 *   GET  /api/system/disks               → disk list (lsblk) with pool usage
 *   GET  /api/zfs/smart                  → SMART health per disk
 *   POST /api/zfs/smart/test             → { device, type } → { success, output, estimate }
 *   GET  /api/zfs/smart/results?device=X → { success, device, results }
 *   POST /api/zfs/pool/replace           → { pool, old_disk, new_disk } → { success, job_id }
 *
 * WS events handled:
 *   hardwareEvent / diskAdded / diskRemoved → refresh disk list
 *   diskTempWarning → show banner, refresh SMART
 *   poolHealthChange → refresh disk list
 *   diskReplacementAvailable → pre-populate replace modal with faulted vdev suggestion
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
  type:        string   // HDD | SSD | NVMe | SAS | USB | unknown
  model:       string
  serial:      string
  in_use:      boolean
  mount_point: string
  // Extended fields (may be absent on older daemon versions)
  wwn?:        string
  by_id_path?: string   // full /dev/disk/by-id/... path
  pool_name?:  string   // pool this disk belongs to (if any)
  pool_health?: string  // ONLINE | DEGRADED | FAULTED | ...
  vdev_state?: string   // ONLINE | FAULTED | DEGRADED | ...
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

interface ReplaceResponse {
  success: boolean
  job_id?: string
  error?:  string
}

// diskReplacementAvailable WS payload
interface FaultedVdevSuggestion {
  pool:   string
  path:   string
  state:  string
}
interface ReplacementSuggestion {
  new_disk: {
    dev:   string
    by_id: string
    model: string
    size:  string
  }
  faulted_vdevs: FaultedVdevSuggestion[]
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Derive a human-readable type label and badge color from DiskInfo.type */
function diskTypeMeta(type: string): { label: string; color: string } {
  const t = (type ?? '').toUpperCase()
  if (t === 'NVME')  return { label: 'NVMe', color: 'var(--info)' }
  if (t === 'SSD')   return { label: 'SSD',  color: 'var(--primary)' }
  if (t === 'SAS')   return { label: 'SAS',  color: '#a78bfa' }
  if (t === 'USB')   return { label: 'USB',  color: 'var(--text-secondary)' }
  if (t === 'HDD')   return { label: 'HDD',  color: 'var(--text-secondary)' }
  return { label: type || 'Disk', color: 'var(--text-tertiary)' }
}

/** Extract the last path segment of a /dev/disk/by-id/... path */
function byIdShort(path: string): string {
  if (!path) return ''
  return path.split('/').pop() ?? path
}

/** Strip /dev/ prefix */
function devName(devPath: string): string {
  return devPath.replace(/^\/dev\//, '')
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
// ReplaceDiskModal
// ---------------------------------------------------------------------------

interface ReplaceDiskModalProps {
  pool:            string
  oldDisk:         DiskInfo
  freeDisk:        DiskInfo[]
  onClose:         () => void
  onSuccess:       () => void
  /** When set (from diskReplacementAvailable WS event), pre-select this disk */
  suggestedNewDev?: string
}

function ReplaceDiskModal({
  pool, oldDisk, freeDisk, onClose, onSuccess, suggestedNewDev,
}: ReplaceDiskModalProps) {
  const [selected, setSelected] = useState<string>(
    suggestedNewDev ?? freeDisk[0]?.name ?? ''
  )
  const [jobId, setJobId] = useState<string | null>(null)

  const replaceMutation = useMutation({
    mutationFn: () =>
      api.post<ReplaceResponse>('/api/zfs/pool/replace', {
        pool,
        old_disk: `/dev/${oldDisk.name}`,
        new_disk: `/dev/${selected}`,
      }),
    onSuccess: (data) => {
      if (data.success) {
        setJobId(data.job_id ?? null)
        toast.success(`zpool replace started for ${pool}`)
        onSuccess()
      } else {
        toast.error(data.error ?? 'Replace failed')
      }
    },
    onError: (err: Error) => toast.error(`Replace failed: ${err.message}`),
  })

  const newDisk = freeDisk.find(d => d.name === selected)

  return (
    <Modal
      title={
        <>
          Replace Disk in{' '}
          <span style={{ color: 'var(--primary)', fontFamily: 'var(--font-mono)' }}>{pool}</span>
        </>
      }
      onClose={onClose}
    >
      {jobId ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div className="alert alert-success">
            <Icon name="check_circle" size={16} />
            Replace operation started (job: <code style={{ fontFamily: 'var(--font-mono)' }}>{jobId}</code>).
            ZFS resilver will begin automatically.
          </div>
          <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            Monitor resilver progress on the ZFS Storage page.
          </p>
          <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
            <button className="btn btn-primary" onClick={onClose}>Done</button>
          </div>
        </div>
      ) : (
        <>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            {/* Failed disk summary */}
            <div style={{
              background: 'var(--error-bg)', border: '1px solid var(--error-border)',
              borderRadius: 'var(--radius-md)', padding: '12px 14px',
              display: 'flex', alignItems: 'center', gap: 10,
            }}>
              <Icon name="hard_drive" size={20} style={{ color: 'var(--error)', flexShrink: 0 }} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontWeight: 600, fontSize: 'var(--text-sm)', color: 'var(--error)' }}>
                  Failed: /dev/{oldDisk.name}
                </div>
                <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)', marginTop: 2 }}>
                  {oldDisk.model && <span>{oldDisk.model} · </span>}
                  {oldDisk.size && <span>{oldDisk.size}</span>}
                  {oldDisk.vdev_state && (
                    <span style={{ marginLeft: 8, color: 'var(--error)', textTransform: 'uppercase' }}>
                      {oldDisk.vdev_state}
                    </span>
                  )}
                </div>
              </div>
            </div>

            {/* Replacement disk selector */}
            {freeDisk.length === 0 ? (
              <div className="alert alert-warning">
                <Icon name="warning" size={16} />
                No free disks available. Connect a replacement disk first.
              </div>
            ) : (
              <label className="field">
                <span className="field-label">Replacement Disk</span>
                <select
                  value={selected}
                  onChange={e => setSelected(e.target.value)}
                  className="input"
                >
                  {freeDisk.map(d => (
                    <option key={d.name} value={d.name}>
                      /dev/{d.name} — {d.model || 'Unknown'} ({d.size})
                      {suggestedNewDev === d.name ? ' ✓ Suggested' : ''}
                    </option>
                  ))}
                </select>
                {suggestedNewDev && freeDisk.some(d => d.name === suggestedNewDev) && (
                  <div style={{ fontSize: 'var(--text-xs)', color: 'var(--info)', marginTop: 4, display: 'flex', alignItems: 'center', gap: 4 }}>
                    <Icon name="lightbulb" size={12} />
                    Auto-suggested based on newly connected disk
                  </div>
                )}
              </label>
            )}

            {/* Command preview */}
            {selected && (
              <div style={{
                background: 'var(--surface)', border: '1px solid var(--border)',
                borderRadius: 'var(--radius-sm)', padding: '10px 14px',
                fontSize: 'var(--text-xs)', fontFamily: 'var(--font-mono)',
                color: 'var(--text-secondary)',
              }}>
                <div style={{ fontSize: 'var(--text-2xs)', textTransform: 'uppercase', letterSpacing: '0.5px', color: 'var(--text-tertiary)', marginBottom: 4 }}>
                  Command that will run:
                </div>
                zpool replace {pool} /dev/{oldDisk.name} /dev/{selected}
              </div>
            )}

            {/* New disk info */}
            {newDisk && (
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', display: 'flex', gap: 12, flexWrap: 'wrap' }}>
                {newDisk.serial && (
                  <span>Serial: <span style={{ fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>{newDisk.serial}</span></span>
                )}
                {newDisk.wwn && (
                  <span>WWN: <span style={{ fontFamily: 'var(--font-mono)', color: 'var(--text-secondary)' }}>{newDisk.wwn}</span></span>
                )}
              </div>
            )}
          </div>

          <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 20 }}>
            <button className="btn btn-ghost" onClick={onClose} disabled={replaceMutation.isPending}>
              Cancel
            </button>
            <button
              className="btn btn-danger"
              onClick={() => replaceMutation.mutate()}
              disabled={replaceMutation.isPending || !selected || freeDisk.length === 0}
            >
              {replaceMutation.isPending ? (
                <><Spinner size={14} /> Replacing…</>
              ) : (
                <>
                  <Icon name="swap_horiz" size={15} />
                  Replace Disk
                </>
              )}
            </button>
          </div>
        </>
      )}
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// DiskRow
// ---------------------------------------------------------------------------

interface DiskRowProps {
  disk:          DiskInfo
  smart?:        SMARTDisk
  onShortTest:   () => void
  onLongTest:    () => void
  onViewResults: () => void
  onReplace?:    () => void
  isTestRunning: boolean
  testResult:    SMARTTestResponse | null
}

function DiskRow({
  disk, smart,
  onShortTest, onLongTest, onViewResults, onReplace,
  isTestRunning, testResult,
}: DiskRowProps) {
  const passed     = smart?.smart_status?.passed
  const temp       = smart?.temperature?.current
  const tempWarn   = smart?.temp_warning
  const hours      = smart?.power_on_hours?.hours
  const hasError   = !!smart?.error

  const typeMeta   = diskTypeMeta(disk.type)
  const byIdSeg    = disk.by_id_path ? byIdShort(disk.by_id_path) : null
  const isFaulted  = disk.vdev_state === 'FAULTED' || (!smart?.error && smart && passed === false)
  const poolDeg    = disk.pool_health === 'DEGRADED' || disk.pool_health === 'FAULTED'
  const showReplace = (isFaulted || poolDeg) && !!onReplace

  return (
    <div style={{
      padding: '14px 16px', background: 'rgba(255,255,255,0.02)',
      border: `1px solid ${isFaulted ? 'var(--error-border)' : 'var(--border-subtle)'}`,
      borderRadius: 'var(--radius-md)',
      borderLeft: isFaulted ? '3px solid var(--error)' : undefined,
    }}>
      {/* Main row */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap' }}>
        {/* Disk icon */}
        <Icon
          name={disk.type === 'SSD' || disk.type === 'NVMe' ? 'memory' : 'hard_drive'}
          size={32}
          style={{ color: isFaulted ? 'var(--error)' : 'var(--primary)', flexShrink: 0 }}
        />

        {/* Info */}
        <div style={{ flex: 1, minWidth: 0 }}>
          {/* Name + model */}
          <div style={{ fontWeight: 600, fontSize: 'var(--text-md)', marginBottom: 2 }}>
            /dev/{disk.name}
            {disk.model && (
              <span style={{ fontWeight: 400, color: 'var(--text-secondary)', marginLeft: 8, fontSize: 'var(--text-sm)' }}>
                {disk.model}
              </span>
            )}
          </div>

          {/* Meta row: size, serial, hours */}
          <div style={{ display: 'flex', gap: 16, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', flexWrap: 'wrap' }}>
            <span>{disk.size}</span>
            {disk.serial && (
              <span style={{ fontFamily: 'var(--font-mono)' }}>{disk.serial}</span>
            )}
            {hours !== undefined && <span>{hours.toLocaleString()}h power-on</span>}
          </div>

          {/* Stable-path row: WWN + by-id */}
          {(disk.wwn || byIdSeg) && (
            <div style={{ display: 'flex', gap: 12, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 3, flexWrap: 'wrap' }}>
              {disk.wwn && (
                <span
                  title={disk.wwn}
                  style={{ fontFamily: 'var(--font-mono)', maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'inline-block', cursor: 'default' }}
                >
                  WWN: {disk.wwn}
                </span>
              )}
              {byIdSeg && (
                <span
                  title={disk.by_id_path}
                  style={{ fontFamily: 'var(--font-mono)', maxWidth: 260, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', display: 'inline-block', cursor: 'default' }}
                >
                  {byIdSeg}
                </span>
              )}
            </div>
          )}
        </div>

        {/* Type badge */}
        <span style={{
          padding: '3px 8px', borderRadius: 'var(--radius-xs)',
          fontSize: 'var(--text-xs)', fontWeight: 600, flexShrink: 0,
          background: `${typeMeta.color}18`,
          color: typeMeta.color,
          border: `1px solid ${typeMeta.color}40`,
        }}>
          {typeMeta.label}
        </span>

        {/* Temperature */}
        {temp !== undefined && (
          <div style={{
            display: 'flex', alignItems: 'center', gap: 4, flexShrink: 0,
            color: tempWarn === 'critical' ? 'var(--error)' : tempWarn === 'warning' ? 'var(--warning)' : 'var(--text-secondary)',
            fontSize: 'var(--text-sm)',
          }}>
            <Icon name="thermostat" size={16} />
            {temp}°C
          </div>
        )}

        {/* Pool membership badge */}
        {disk.pool_name && (
          <a
            href="#pools-section"
            onClick={e => {
              e.preventDefault()
              document.getElementById('pools-section')?.scrollIntoView({ behavior: 'smooth' })
            }}
            style={{
              padding: '3px 8px', borderRadius: 'var(--radius-xs)',
              fontSize: 'var(--text-xs)', fontWeight: 600, flexShrink: 0,
              background: poolDeg ? 'var(--warning-bg)' : 'var(--primary-bg)',
              color: poolDeg ? 'var(--warning)' : 'var(--primary)',
              border: `1px solid ${poolDeg ? 'var(--warning-border)' : 'rgba(138,156,255,0.25)'}`,
              textDecoration: 'none', display: 'flex', alignItems: 'center', gap: 4,
            }}
            title={`Pool: ${disk.pool_name} (${disk.pool_health ?? 'unknown'})`}
          >
            <Icon name="storage" size={12} />
            {disk.pool_name}
            {poolDeg && <Icon name="warning" size={11} />}
          </a>
        )}

        {/* In-use badge (shown only when no pool membership) */}
        {!disk.pool_name && (
          <span style={{
            padding: '3px 8px', borderRadius: 'var(--radius-xs)', fontSize: 'var(--text-xs)', fontWeight: 600,
            background: disk.in_use ? 'var(--primary-bg)' : 'var(--surface)',
            color: disk.in_use ? 'var(--primary)' : 'var(--text-secondary)',
            border: `1px solid ${disk.in_use ? 'rgba(138,156,255,0.25)' : 'var(--border)'}`,
            flexShrink: 0,
          }}>
            {disk.in_use ? 'In use' : 'Free'}
          </span>
        )}

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

        {/* Replace button */}
        {showReplace && (
          <button
            className="btn btn-danger"
            onClick={onReplace}
            title={`Replace this faulted disk in pool ${disk.pool_name}`}
            style={{ fontSize: 'var(--text-xs)', padding: '5px 12px', flexShrink: 0, display: 'flex', alignItems: 'center', gap: 5 }}
          >
            <Icon name="swap_horiz" size={14} />
            Replace
          </button>
        )}

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

      {/* Test result banner */}
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

  // WS: disk add/remove → refetch disk list
  useEffect(() => {
    const unsubAdded = wsOn('hardwareEvent', (data) => {
      const d = data as { event?: string; action?: string; model?: string; device?: string }
      const action = d.event ?? d.action ?? ''
      if (action === 'diskAdded' || action === 'disk_added') {
        toast.success(`Disk connected: ${d.model ?? d.device ?? 'new disk'}`)
        qc.invalidateQueries({ queryKey: ['system', 'disks'] })
      } else if (action === 'diskRemoved' || action === 'disk_removed') {
        toast.info(`Disk removed: ${d.device ?? 'unknown'}`)
        qc.invalidateQueries({ queryKey: ['system', 'disks'] })
      }
    })
    return unsubAdded
  }, [wsOn, qc])

  useEffect(() => {
    return wsOn('diskTempWarning', (data) => {
      const d = data as { device?: string; temp?: number }
      if (d?.device) setTempWarning({ device: d.device, temp: d.temp ?? 0 })
      toast.warning(`Temperature warning: /dev/${d.device} at ${d.temp ?? '?'}°C`)
      qc.invalidateQueries({ queryKey: ['zfs', 'smart'] })
    })
  }, [wsOn, qc])

  // WS: pool health changed → refetch pools + disks (pool_health on DiskInfo may change)
  useEffect(() => {
    return wsOn('poolHealthChange', () => {
      qc.invalidateQueries({ queryKey: ['system', 'disks'] })
    })
  }, [wsOn, qc])

  // WS: diskReplacementAvailable → pre-populate the replace modal
  useEffect(() => {
    return wsOn('diskReplacementAvailable', (data) => {
      const suggestion = data as ReplacementSuggestion
      if (!suggestion?.faulted_vdevs?.length || !suggestion?.new_disk) return

      const fv = suggestion.faulted_vdevs[0]
      // Find the faulted disk in the current disk list by matching /dev/ path or by_id
      qc.invalidateQueries({ queryKey: ['system', 'disks'] })

      // Show a toast that also describes the suggestion
      toast.info(
        `New disk connected — suggested to replace faulted ${fv.path} in pool ${fv.pool}`,
        8000
      )

      // Store the suggestion so the UI can open the modal when disks reload
      setReplacementSuggestion(suggestion)
    })
  }, [wsOn, qc])

  // Per-disk test state
  const [runningDevice, setRunningDevice]           = useState<string | null>(null)
  const [testResults, setTestResults]               = useState<Record<string, SMARTTestResponse>>({})
  const [resultsModalDevice, setResultsModalDevice] = useState<string | null>(null)

  // Replace modal state
  const [replaceTarget, setReplaceTarget]           = useState<DiskInfo | null>(null)
  const [suggestedNewDev, setSuggestedNewDev]       = useState<string | null>(null)

  // Pending replacement suggestion from WS (resolved after disk list refreshes)
  const [replacementSuggestion, setReplacementSuggestion] = useState<ReplacementSuggestion | null>(null)

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
  const freeDisk = disks.filter(d => !d.in_use)

  // When disk list refreshes, try to resolve any pending replacement suggestion
  useEffect(() => {
    if (!replacementSuggestion || disks.length === 0) return

    const fv = replacementSuggestion.faulted_vdevs[0]
    const newDiskDev = devName(replacementSuggestion.new_disk.dev)

    // Find the faulted disk object by matching its dev path or by_id against the faulted vdev path
    const faultedDisk = disks.find(d => {
      const dPath = '/dev/' + d.name
      const dById = d.by_id_path ?? ''
      return dPath === fv.path || dById === fv.path || d.name === fv.path
    })

    if (faultedDisk) {
      setSuggestedNewDev(newDiskDev)
      setReplaceTarget(faultedDisk)
      setReplacementSuggestion(null)
    }
  }, [disks, replacementSuggestion])

  // Index SMART data by device name for fast lookup
  const smartByDevice: Record<string, SMARTDisk> = {}
  for (const s of smartQ.data?.disks ?? []) {
    smartByDevice[s.device] = s
  }

  const anySmartFail = (smartQ.data?.disks ?? []).some(
    d => !d.error && d.smart_status && !d.smart_status.passed
  )

  const anyPoolDegraded = disks.some(
    d => d.pool_health === 'DEGRADED' || d.pool_health === 'FAULTED'
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

      {anyPoolDegraded && (
        <div className="alert alert-warning">
          <Icon name="warning" size={18} />
          One or more pools are DEGRADED or have FAULTED vdevs — use the Replace button to begin resilver
        </div>
      )}

      {/* Disk list */}
      <div
        id="pools-section"
        style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 24 }}
      >
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
              onReplace={d.pool_name ? () => { setReplaceTarget(d); setSuggestedNewDev(null) } : undefined}
            />
          ))}
        </div>
      </div>

      {/* SMART detail table for disks with ATA attributes */}
      {smartQ.data && smartQ.data.disks.some(d => d.ata_smart_attributes?.table.length) && (
        <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 24 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 20 }}>
            <Icon name="analytics" size={18} style={{ color: 'var(--primary)' }} />
            <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>SMART Attributes</span>
          </div>

          {smartQ.data.disks.map(d => {
            const attrs = d.ata_smart_attributes?.table ?? []
            if (!attrs.length) return null
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

      {/* Replace Disk Modal */}
      {replaceTarget && (
        <ReplaceDiskModal
          pool={replaceTarget.pool_name!}
          oldDisk={replaceTarget}
          freeDisk={freeDisk}
          suggestedNewDev={suggestedNewDev ?? undefined}
          onClose={() => { setReplaceTarget(null); setSuggestedNewDev(null) }}
          onSuccess={() => {
            qc.invalidateQueries({ queryKey: ['system', 'disks'] })
            qc.invalidateQueries({ queryKey: ['zfs', 'pools'] })
            setReplaceTarget(null)
            setSuggestedNewDev(null)
          }}
        />
      )}
    </div>
  )
}
