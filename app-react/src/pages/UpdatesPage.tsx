/**
 * pages/UpdatesPage.tsx — System Updates
 *
 * For Debian/Ubuntu: shows available apt packages, apply-all and apply-security buttons.
 * For NixOS: links to the NixOS rebuild workflow in Settings.
 *
 * Calls:
 *   GET  /api/system/preflight                  → preflight checks
 *   GET  /api/system/updates/daemon-version     → current + latest daemon version
 *   GET  /api/system/updates/check              → { job_id } — run apt-get update + list upgradable
 *   POST /api/system/updates/apply              → { job_id } — apt-get upgrade -y
 *   POST /api/system/updates/apply-security     → { job_id } — security-only upgrade
 *   GET  /api/jobs/{id}                         → job status polling
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { Tooltip } from '@/components/ui/Tooltip'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface PreflightCheck    { name: string; status: 'pass'|'warn'|'fail'|string; message: string }
interface PreflightResponse { success: boolean; status: 'pass'|'warn'|'fail'|string; checks: PreflightCheck[] }
interface NixOSDetectResponse { success: boolean; is_nixos: boolean; message?: string }

interface VersionResponse {
  success:          boolean
  current_version:  string
  latest_version:   string
  update_available: boolean
  release_url:      string
  error?:           string
}

interface JobCheckResponse { success: boolean; job_id: string }

interface UpgradablePackage {
  name:            string
  new_version:     string
  current_version: string
  security:        boolean
}

interface JobResult {
  status:    'running' | 'done' | 'failed' | string
  result?:   { packages?: UpgradablePackage[]; package_count?: number; output?: string; duration_ms?: number }
  error?:    string
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function checkIcon(s: string) { return s === 'pass' ? 'check_circle' : s === 'warn' ? 'warning' : 'error' }
function checkColor(s: string) { return s === 'pass' ? 'var(--success)' : s === 'warn' ? 'rgba(251,191,36,0.9)' : 'var(--error)' }
function checkBg(s: string)    { return s === 'pass' ? 'var(--success-bg)' : s === 'warn' ? 'rgba(251,191,36,0.08)' : 'var(--error-bg)' }
function checkBorder(s: string){ return s === 'pass' ? 'var(--success-border)' : s === 'warn' ? 'rgba(251,191,36,0.25)' : 'var(--error-border)' }

// ---------------------------------------------------------------------------
// UpdatesPage
// ---------------------------------------------------------------------------

export function UpdatesPage() {
  const qc       = useQueryClient()
  const navigate = useNavigate()

  // Job state — used for check, apply-all, and apply-security
  const [activeJobId,    setActiveJobId]    = useState<string | null>(null)
  const [activeJobLabel, setActiveJobLabel] = useState<string>('')
  const [packages,       setPackages]       = useState<UpgradablePackage[] | null>(null)
  const [applyOutput,    setApplyOutput]    = useState<string | null>(null)

  // ── Static queries ────────────────────────────────────────────────────────

  const preflightQ = useQuery({
    queryKey: ['system', 'preflight'],
    queryFn:  ({ signal }) => api.get<PreflightResponse>('/api/system/preflight', signal),
  })

  const nixosQ = useQuery({
    queryKey: ['nixos', 'detect'],
    queryFn:  ({ signal }) => api.get<NixOSDetectResponse>('/api/nixos/detect', signal),
    staleTime: 60 * 60_000, // OS type doesn't change — cache for 1 hour
  })

  const versionQ = useQuery({
    queryKey: ['system', 'updates', 'version'],
    queryFn:  ({ signal }) => api.get<VersionResponse>('/api/system/updates/daemon-version', signal),
    staleTime: 5 * 60_000,
  })

  // ── Job polling (used for all three job-based operations) ─────────────────

  const jobQ = useQuery({
    queryKey:  ['jobs', activeJobId],
    queryFn:   ({ signal }) => api.get<JobResult>(`/api/jobs/${activeJobId}`, signal),
    enabled:   !!activeJobId,
    refetchInterval: (data) =>
      data?.state?.data?.status === 'running' || data?.state?.data?.status === undefined ? 2000 : false,
  })

  const jobDone   = jobQ.data?.status === 'done'
  const jobFailed = jobQ.data?.status === 'failed'
  const jobRunning = !!activeJobId && !jobDone && !jobFailed

  // When a job finishes, extract results
  if (jobDone && jobQ.data?.result && activeJobLabel === 'check') {
    const pkgs = jobQ.data.result.packages ?? null
    if (pkgs !== null && packages === null) {
      setPackages(pkgs)
      setActiveJobId(null)
    }
  }
  if ((jobDone || jobFailed) && activeJobLabel !== 'check' && jobQ.data && applyOutput === null) {
    setApplyOutput(jobDone ? (jobQ.data.result?.output ?? 'Done.') : (jobQ.data.error ?? 'Failed.'))
    setActiveJobId(null)
    if (jobDone) toast.success(`${activeJobLabel} completed`)
    else         toast.error(`${activeJobLabel} failed`)
    qc.invalidateQueries({ queryKey: ['system', 'preflight'] })
  }

  // ── Mutations ─────────────────────────────────────────────────────────────

  const checkM = useMutation({
    mutationFn: () => api.get<JobCheckResponse>('/api/system/updates/check'),
    onSuccess: (data) => {
      setPackages(null)
      setApplyOutput(null)
      setActiveJobLabel('check')
      setActiveJobId(data.job_id)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const applyAllM = useMutation({
    mutationFn: () => api.post<JobCheckResponse>('/api/system/updates/apply'),
    onSuccess: (data) => {
      setApplyOutput(null)
      setActiveJobLabel('Full upgrade')
      setActiveJobId(data.job_id)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const applySecM = useMutation({
    mutationFn: () => api.post<JobCheckResponse>('/api/system/updates/apply-security'),
    onSuccess: (data) => {
      setApplyOutput(null)
      setActiveJobLabel('Security upgrade')
      setActiveJobId(data.job_id)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const busy = checkM.isPending || applyAllM.isPending || applySecM.isPending || jobRunning

  // ── Derived ───────────────────────────────────────────────────────────────

  const checks      = preflightQ.data?.checks ?? []
  const overall     = preflightQ.data?.status ?? 'pass'
  const isNixOS     = nixosQ.data?.is_nixos === true
  const secCount    = packages?.filter(p => p.security).length ?? 0
  const totalCount  = packages?.length ?? 0

  return (
    <div style={{ maxWidth: 860 }}>
      <div className="page-header">
        <h1 className="page-title">System Updates</h1>
        <p className="page-subtitle">Apply operating system and security updates</p>
      </div>

      {/* ── Daemon version banner ── */}
      {versionQ.data?.success && (
        <div style={{
          display: 'flex', alignItems: 'center', gap: 14, padding: '14px 20px',
          background: versionQ.data.update_available ? 'rgba(251,191,36,0.08)' : 'var(--bg-card)',
          border: `1px solid ${versionQ.data.update_available ? 'rgba(251,191,36,0.3)' : 'var(--border)'}`,
          borderRadius: 'var(--radius-lg)', marginBottom: 20,
        }}>
          <Icon name={versionQ.data.update_available ? 'new_releases' : 'verified'} size={20}
            style={{ color: versionQ.data.update_available ? 'rgba(251,191,36,0.9)' : 'var(--success)', flexShrink: 0 }} />
          <div style={{ flex: 1 }}>
            <span style={{ fontWeight: 700 }}>D-PlaneOS {versionQ.data.current_version}</span>
            {versionQ.data.update_available
              ? <span style={{ marginLeft: 10, color: 'rgba(251,191,36,0.9)', fontSize: 'var(--text-sm)' }}>
                  → {versionQ.data.latest_version} available
                </span>
              : <span style={{ marginLeft: 10, color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>up to date</span>
            }
          </div>
          {versionQ.data.update_available && versionQ.data.release_url && (
            <a href={versionQ.data.release_url} target="_blank" rel="noreferrer" className="btn btn-ghost" style={{ fontSize: 'var(--text-xs)' }}>
              <Icon name="open_in_new" size={13} />Release notes
            </a>
          )}
        </div>
      )}

      {/* ── NixOS callout ── */}
      {isNixOS ? (
        <div style={{
          display: 'flex', alignItems: 'center', gap: 16, padding: '18px 22px',
          background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)',
        }}>
          <Icon name="terminal" size={24} style={{ color: 'var(--primary)', flexShrink: 0 }} />
          <div style={{ flex: 1 }}>
            <div style={{ fontWeight: 700, marginBottom: 3 }}>NixOS detected</div>
            <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
              Use <strong>nixos-rebuild switch</strong> via the Settings page to apply configuration and package updates.
            </div>
          </div>
          <button onClick={() => navigate({ to: '/settings' })} className="btn btn-primary">
            <Icon name="settings" size={15} />Open Settings
          </button>
        </div>
      ) : (
        /* ── Debian/Ubuntu apt update UI ── */
        <>
          {/* Action row */}
          <div style={{ display: 'flex', gap: 10, marginBottom: 20, flexWrap: 'wrap' }}>
            <button
              onClick={() => checkM.mutate()}
              disabled={busy}
              className="btn btn-ghost"
            >
              <Icon name="search" size={14} />
              {jobRunning && activeJobLabel === 'check' ? 'Checking…' : 'Check for updates'}
            </button>
            <Tooltip content={totalCount === 0 ? 'Run "Check for updates" first' : ''}><button
              onClick={() => applySecM.mutate()}
              disabled={busy || totalCount === 0}
              className="btn btn-primary"
            >
              <Icon name="security" size={14} />
              {jobRunning && activeJobLabel === 'Security upgrade'
                ? 'Applying security…'
                : `Apply security updates${secCount > 0 ? ` (${secCount})` : ''}`}
            </button></Tooltip>
            <Tooltip content={totalCount === 0 ? 'Run "Check for updates" first' : ''}><button
              onClick={() => applyAllM.mutate()}
              disabled={busy || totalCount === 0}
              className="btn btn-ghost"
            >
              <Icon name="system_update" size={14} />
              {jobRunning && activeJobLabel === 'Full upgrade'
                ? 'Upgrading…'
                : `Apply all updates${totalCount > 0 ? ` (${totalCount})` : ''}`}
            </button></Tooltip>
          </div>

          {/* Running job indicator */}
          {jobRunning && (
            <div style={{
              display: 'flex', alignItems: 'center', gap: 10, padding: '12px 16px',
              background: 'var(--bg-card)', border: '1px solid var(--border)',
              borderRadius: 'var(--radius-lg)', marginBottom: 16,
            }}>
              <Icon name="autorenew" size={18} style={{ color: 'var(--primary)', animation: 'spin 1s linear infinite' }} />
              <span style={{ fontSize: 'var(--text-sm)' }}>{activeJobLabel}… this may take a minute</span>
            </div>
          )}

          {/* Apply output */}
          {applyOutput && (
            <div style={{ marginBottom: 20 }}>
              <div style={{ fontWeight: 700, marginBottom: 8, display: 'flex', alignItems: 'center', gap: 8 }}>
                <Icon name={jobFailed ? 'error' : 'check_circle'} size={16}
                  style={{ color: jobFailed ? 'var(--error)' : 'var(--success)' }} />
                {activeJobLabel} output
                <button onClick={() => setApplyOutput(null)} style={{ marginLeft: 'auto', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', display: 'flex' }}>
                  <Icon name="close" size={15} />
                </button>
              </div>
              <pre style={{
                background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)',
                padding: '12px 14px', fontFamily: 'var(--font-mono)', fontSize: 11, lineHeight: 1.6,
                overflow: 'auto', maxHeight: 300, margin: 0, color: 'rgba(255,255,255,0.75)', whiteSpace: 'pre-wrap',
              }}>
                {applyOutput}
              </pre>
            </div>
          )}

          {/* Package list */}
          {packages !== null && (
            <div>
              <div style={{ fontWeight: 700, marginBottom: 12, display: 'flex', alignItems: 'center', gap: 8 }}>
                <Icon name="inventory_2" size={16} style={{ color: 'var(--primary)' }} />
                {totalCount === 0
                  ? 'System is up to date'
                  : <>{totalCount} package{totalCount !== 1 ? 's' : ''} available{secCount > 0 ? ` (${secCount} security)` : ''}</>
                }
              </div>
              {packages.length > 0 && (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
                  {packages.map(p => (
                    <div key={p.name} style={{
                      display: 'flex', alignItems: 'center', gap: 12, padding: '9px 14px',
                      background: p.security ? 'rgba(239,68,68,0.06)' : 'var(--bg-card)',
                      border: `1px solid ${p.security ? 'var(--error-border)' : 'var(--border)'}`,
                      borderRadius: 'var(--radius-md)',
                    }}>
                      {p.security && <Icon name="security" size={14} style={{ color: 'var(--error)', flexShrink: 0 }} />}
                      <span style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', fontWeight: 600, flex: 1 }}>{p.name}</span>
                      <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)' }}>
                        {p.current_version} → {p.new_version}
                      </span>
                      {p.security && (
                        <span style={{ padding: '1px 6px', borderRadius: 'var(--radius-xs)', background: 'rgba(239,68,68,0.15)', color: 'var(--error)', fontSize: 10, fontWeight: 700 }}>
                          SECURITY
                        </span>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}

          {/* Preflight checks (collapsed below) */}
          {!preflightQ.isLoading && !preflightQ.isError && checks.length > 0 && (
            <div style={{ marginTop: 28 }}>
              <div style={{ fontWeight: 700, marginBottom: 10, display: 'flex', alignItems: 'center', gap: 8 }}>
                <Icon name={checkIcon(overall)} size={16} style={{ color: checkColor(overall) }} />
                System readiness
                <button
                  onClick={() => qc.invalidateQueries({ queryKey: ['system', 'preflight'] })}
                  className="btn btn-ghost"
                  style={{ marginLeft: 'auto', fontSize: 'var(--text-xs)', padding: '3px 8px' }}
                >
                  <Icon name="refresh" size={13} />Re-run
                </button>
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
                {checks.map((c, i) => (
                  <div key={i} style={{
                    display: 'flex', alignItems: 'flex-start', gap: 12, padding: '12px 16px',
                    background: checkBg(c.status), border: `1px solid ${checkBorder(c.status)}`,
                    borderRadius: 'var(--radius-md)',
                  }}>
                    <Icon name={checkIcon(c.status)} size={18} style={{ color: checkColor(c.status), flexShrink: 0, marginTop: 1 }} />
                    <div style={{ flex: 1 }}>
                      <div style={{ fontWeight: 700, fontSize: 'var(--text-sm)', marginBottom: 2 }}>{c.name}</div>
                      <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', fontFamily: 'var(--font-mono)', lineHeight: 1.5 }}>{c.message}</div>
                    </div>
                    <span style={{
                      padding: '2px 7px', borderRadius: 'var(--radius-xs)',
                      background: checkBg(c.status), border: `1px solid ${checkBorder(c.status)}`,
                      color: checkColor(c.status), fontSize: 10, fontWeight: 700, textTransform: 'uppercase', flexShrink: 0,
                    }}>{c.status}</span>
                  </div>
                ))}
              </div>
            </div>
          )}
          {preflightQ.isLoading && <Skeleton height={200} style={{ marginTop: 20 }} />}
          {preflightQ.isError && <ErrorState error={preflightQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['system', 'preflight'] })} />}
        </>
      )}
    </div>
  )
}
