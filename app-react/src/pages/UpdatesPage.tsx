/**
 * pages/UpdatesPage.tsx — System Preflight & Updates (Phase 8)
 *
 * Runs the system preflight checks (ZFS, Docker, Samba, NFS, disk, memory)
 * and shows overall system readiness. If on NixOS, links out to Settings → NixOS
 * for the actual rebuild workflow.
 *
 * Calls:
 *   GET /api/system/preflight  → { success, status: "pass"|"warn"|"fail", checks: Check[] }
 */

import type React from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface PreflightCheck {
  name:    string
  status:  'pass' | 'warn' | 'fail' | string
  message: string
}

interface PreflightResponse {
  success: boolean
  status:  'pass' | 'warn' | 'fail' | string
  checks:  PreflightCheck[]
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const btnGhost: React.CSSProperties = {
  padding: '8px 14px', background: 'var(--surface)', color: 'var(--text-secondary)',
  border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 500, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnPrimary: React.CSSProperties = {
  padding: '9px 20px', background: 'var(--primary)', color: '#000',
  border: 'none', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 700, display: 'inline-flex', alignItems: 'center', gap: 6,
}

// ---------------------------------------------------------------------------
// Check status helpers
// ---------------------------------------------------------------------------

function checkIcon(status: string): string {
  if (status === 'pass') return 'check_circle'
  if (status === 'warn') return 'warning'
  if (status === 'fail') return 'error'
  return 'help'
}

function checkColor(status: string): string {
  if (status === 'pass') return 'var(--success)'
  if (status === 'warn') return 'rgba(251,191,36,0.9)'
  if (status === 'fail') return 'var(--error)'
  return 'var(--text-tertiary)'
}

function checkBg(status: string): string {
  if (status === 'pass') return 'var(--success-bg)'
  if (status === 'warn') return 'rgba(251,191,36,0.08)'
  if (status === 'fail') return 'var(--error-bg)'
  return 'var(--surface)'
}

function checkBorder(status: string): string {
  if (status === 'pass') return 'var(--success-border)'
  if (status === 'warn') return 'rgba(251,191,36,0.25)'
  if (status === 'fail') return 'var(--error-border)'
  return 'var(--border)'
}

function overallLabel(status: string): string {
  if (status === 'pass') return 'All checks passed'
  if (status === 'warn') return 'Some checks have warnings'
  if (status === 'fail') return 'Critical checks failed'
  return 'Unknown status'
}

// ---------------------------------------------------------------------------
// CheckRow
// ---------------------------------------------------------------------------

function CheckRow({ check }: { check: PreflightCheck }) {
  return (
    <div style={{
      display: 'flex', alignItems: 'flex-start', gap: 14, padding: '14px 18px',
      background: checkBg(check.status),
      border: `1px solid ${checkBorder(check.status)}`,
      borderRadius: 'var(--radius-md)',
    }}>
      <Icon name={checkIcon(check.status)} size={20}
        style={{ color: checkColor(check.status), flexShrink: 0, marginTop: 1 }} />
      <div style={{ flex: 1, minWidth: 0 }}>
        <div style={{ fontWeight: 700, fontSize: 'var(--text-sm)', marginBottom: 3 }}>{check.name}</div>
        <div style={{
          fontSize: 'var(--text-xs)', color: 'var(--text-secondary)',
          fontFamily: 'var(--font-mono)', lineHeight: 1.5,
          wordBreak: 'break-all',
        }}>
          {check.message}
        </div>
      </div>
      <span style={{
        padding: '2px 8px', borderRadius: 'var(--radius-xs)',
        background: checkBg(check.status), border: `1px solid ${checkBorder(check.status)}`,
        color: checkColor(check.status),
        fontSize: 10, fontWeight: 700, letterSpacing: '0.3px', textTransform: 'uppercase', flexShrink: 0,
      }}>
        {check.status}
      </span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// UpdatesPage
// ---------------------------------------------------------------------------

export function UpdatesPage() {
  const qc       = useQueryClient()
  const navigate = useNavigate()

  const preflightQ = useQuery({
    queryKey: ['system', 'preflight'],
    queryFn:  ({ signal }) => api.get<PreflightResponse>('/api/system/preflight', signal),
  })

  const overall = preflightQ.data?.status ?? 'pass'
  const checks  = preflightQ.data?.checks ?? []

  const failCount = checks.filter(c => c.status === 'fail').length
  const warnCount = checks.filter(c => c.status === 'warn').length
  const passCount = checks.filter(c => c.status === 'pass').length

  return (
    <div style={{ maxWidth: 860 }}>
      <div style={{ marginBottom: 28 }}>
        <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>
          System Readiness
        </h1>
        <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>
          Preflight checks for system dependencies and resources
        </p>
      </div>

      {preflightQ.isLoading && <Skeleton height={360} />}
      {preflightQ.isError   && (
        <ErrorState error={preflightQ.error}
          onRetry={() => qc.invalidateQueries({ queryKey: ['system', 'preflight'] })} />
      )}

      {!preflightQ.isLoading && !preflightQ.isError && preflightQ.data && (
        <>
          {/* Overall status banner */}
          <div style={{
            display: 'flex', alignItems: 'center', gap: 16, padding: '20px 24px',
            background: checkBg(overall),
            border: `1px solid ${checkBorder(overall)}`,
            borderRadius: 'var(--radius-xl)', marginBottom: 24,
          }}>
            <div style={{
              width: 52, height: 52, borderRadius: 'var(--radius-md)',
              background: overall === 'pass' ? 'rgba(16,185,129,0.15)' : overall === 'fail' ? 'rgba(239,68,68,0.15)' : 'rgba(251,191,36,0.15)',
              border: `1px solid ${checkBorder(overall)}`,
              display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0,
            }}>
              <Icon name={checkIcon(overall)} size={28} style={{ color: checkColor(overall) }} />
            </div>
            <div style={{ flex: 1 }}>
              <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)', color: checkColor(overall), marginBottom: 4 }}>
                {overallLabel(overall)}
              </div>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'flex', gap: 14 }}>
                {passCount > 0 && <span style={{ color: 'var(--success)' }}>✓ {passCount} passed</span>}
                {warnCount > 0 && <span style={{ color: 'rgba(251,191,36,0.9)' }}>⚠ {warnCount} warnings</span>}
                {failCount > 0 && <span style={{ color: 'var(--error)' }}>✗ {failCount} failed</span>}
              </div>
            </div>
            <div style={{ display: 'flex', gap: 8, flexShrink: 0 }}>
              <button
                onClick={() => qc.invalidateQueries({ queryKey: ['system', 'preflight'] })}
                style={btnGhost}
              >
                <Icon name="refresh" size={14} />Re-run
              </button>
            </div>
          </div>

          {/* Individual checks */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 8, marginBottom: 24 }}>
            {checks.map((check, i) => (
              <CheckRow key={`${check.name}-${i}`} check={check} />
            ))}
            {checks.length === 0 && (
              <div style={{ textAlign: 'center', padding: '40px 0', color: 'var(--text-tertiary)' }}>
                No checks returned from daemon
              </div>
            )}
          </div>

          {/* NixOS rebuild callout */}
          <div style={{
            display: 'flex', alignItems: 'center', gap: 16, padding: '18px 22px',
            background: 'var(--bg-card)', border: '1px solid var(--border)',
            borderRadius: 'var(--radius-xl)',
          }}>
            <Icon name="terminal" size={24} style={{ color: 'var(--primary)', flexShrink: 0 }} />
            <div style={{ flex: 1 }}>
              <div style={{ fontWeight: 700, marginBottom: 3 }}>Apply system updates</div>
              <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
                On NixOS, use <strong>nixos-rebuild switch</strong> via the Settings page to apply
                configuration and package updates.
              </div>
            </div>
            <button
              onClick={() => navigate({ to: '/settings' })}
              style={btnPrimary}
            >
              <Icon name="settings" size={15} />Open Settings
            </button>
          </div>
        </>
      )}
    </div>
  )
}
