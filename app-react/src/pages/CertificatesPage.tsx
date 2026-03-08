/**
 * pages/CertificatesPage.tsx — TLS Certificates (Phase 6)
 *
 * Lists installed certs, shows which is active, allows generating self-signed
 * and activating any installed cert (triggers nginx reload in daemon).
 *
 * Calls:
 *   GET  /api/certs/list                          → { success, certs: Cert[], active_cert: string }
 *   POST /api/certs/generate  { name, cn, days, sans }
 *   POST /api/certs/activate  { name }            → activates + reloads nginx
 */

import { useState } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface Cert {
  name:     string
  details?: string    // raw multi-line text from openssl x509 -text
  subject?: string    // parsed from details
  expires?: string    // parsed from details
  issuer?:  string    // parsed from details
}

interface CertsResponse {
  success:     boolean
  certs:       Cert[]
  active_cert: string
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const btnGhost: React.CSSProperties = {
  padding: '7px 13px', background: 'var(--surface)', color: 'var(--text-secondary)',
  border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 500, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnPrimary: React.CSSProperties = {
  padding: '8px 18px', background: 'var(--primary)', color: '#000',
  border: 'none', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 700, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const inputStyle: React.CSSProperties = {
  background: 'var(--surface)', border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)', padding: '8px 12px',
  color: 'var(--text)', fontSize: 'var(--text-sm)', width: '100%',
  fontFamily: 'var(--font-ui)', outline: 'none', boxSizing: 'border-box',
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
      <label style={{ fontSize: 'var(--text-xs)', fontWeight: 600, color: 'var(--text-secondary)' }}>{label}</label>
      {children}
      {hint && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{hint}</span>}
    </div>
  )
}

// Extract a single value from raw openssl output
function parseDetail(raw: string, key: string): string {
  const re = new RegExp(key + '\\s*=\\s*([^\\n,]+)', 'i')
  return raw.match(re)?.[1]?.trim() ?? ''
}

function parseCN(raw: string): string {
  return parseDetail(raw, 'CN') || parseDetail(raw, 'Common Name')
}

function parseExpiry(raw: string): string {
  const m = raw.match(/Not After\s*:\s*([^\n]+)/i)
  return m?.[1]?.trim() ?? ''
}

function isExpiringSoon(expiryStr: string): boolean {
  if (!expiryStr) return false
  try {
    const exp = new Date(expiryStr)
    const daysLeft = (exp.getTime() - Date.now()) / 86_400_000
    return daysLeft < 30
  } catch { return false }
}

function isExpired(expiryStr: string): boolean {
  if (!expiryStr) return false
  try { return new Date(expiryStr) < new Date() }
  catch { return false }
}

// ---------------------------------------------------------------------------
// GenerateModal
// ---------------------------------------------------------------------------

function GenerateModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [name, setName]   = useState('server')
  const [cn,   setCn]     = useState('')
  const [days, setDays]   = useState('365')
  const [sans, setSans]   = useState('')

  const generate = useMutation({
    mutationFn: () => {
      if (!name.trim() || !cn.trim()) throw new Error('Name and Common Name are required')
      const sanList = sans.split(',').map(s => s.trim()).filter(Boolean)
      return api.post('/api/certs/generate', {
        name: name.trim(),
        cn:   cn.trim(),
        days: Number(days) || 365,
        sans: sanList,
      })
    },
    onSuccess: () => { toast.success('Certificate generated'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.65)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 200 }}
      onClick={e => e.target === e.currentTarget && onClose()}>
      <div style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 28, width: 500, maxWidth: '92vw', display: 'flex', flexDirection: 'column', gap: 18 }}>
        <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>Generate Self-Signed Certificate</div>

        <Field label="Certificate Name" hint="Used as the filename on disk">
          <input value={name} onChange={e => setName(e.target.value)} placeholder="server" style={inputStyle} autoFocus />
        </Field>

        <Field label="Common Name (CN)" hint="Hostname or domain this certificate is for">
          <input value={cn} onChange={e => setCn(e.target.value)} placeholder="dplaneos.local"
            style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} />
        </Field>

        <Field label="Subject Alternative Names" hint="Comma-separated DNS names or IPs (optional)">
          <input value={sans} onChange={e => setSans(e.target.value)} placeholder="192.168.1.100, dplaneos.local"
            style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} />
        </Field>

        <Field label="Validity (days)">
          <input type="number" value={days} onChange={e => setDays(e.target.value)} min={1} max={3650} style={{ ...inputStyle, width: 120 }} />
        </Field>

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button onClick={onClose} style={btnGhost}>Cancel</button>
          <button onClick={() => generate.mutate()} disabled={generate.isPending} style={btnPrimary}>
            <Icon name="verified_user" size={15} />{generate.isPending ? 'Generating…' : 'Generate'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// CertCard
// ---------------------------------------------------------------------------

function CertCard({ cert, isActive, onActivate, activating }: {
  cert:       Cert
  isActive:   boolean
  onActivate: () => void
  activating: boolean
}) {
  const [expanded, setExpanded] = useState(false)

  const details  = cert.details ?? ''
  const cn       = cert.subject ?? (parseCN(details) || cert.name)
  const expiry   = cert.expires ?? parseExpiry(details)
  const issuer   = cert.issuer ?? (parseDetail(details, 'Issuer') || 'Self-signed')
  const expired  = isExpired(expiry)
  const expiring = !expired && isExpiringSoon(expiry)

  return (
    <div style={{ background: 'var(--bg-card)', border: `1px solid ${isActive ? 'rgba(138,156,255,0.3)' : expired ? 'var(--error-border)' : expiring ? 'rgba(251,191,36,0.3)' : 'var(--border)'}`, borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '16px 20px' }}>
        <div style={{ width: 42, height: 42, borderRadius: 'var(--radius-md)', background: isActive ? 'var(--primary-bg)' : expired ? 'var(--error-bg)' : 'var(--surface)', border: `1px solid ${isActive ? 'rgba(138,156,255,0.25)' : expired ? 'var(--error-border)' : 'var(--border)'}`, display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
          <Icon name="verified_user" size={22} style={{ color: isActive ? 'var(--primary)' : expired ? 'var(--error)' : 'var(--text-tertiary)' }} />
        </div>

        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 3 }}>
            <span style={{ fontWeight: 700 }}>{cert.name}</span>
            {isActive  && <span style={{ padding: '1px 7px', borderRadius: 'var(--radius-xs)', background: 'var(--primary-bg)', color: 'var(--primary)', fontSize: 10, fontWeight: 700 }}>ACTIVE</span>}
            {expired   && <span style={{ padding: '1px 7px', borderRadius: 'var(--radius-xs)', background: 'var(--error-bg)', color: 'var(--error)', fontSize: 10, fontWeight: 700 }}>EXPIRED</span>}
            {expiring  && !expired && <span style={{ padding: '1px 7px', borderRadius: 'var(--radius-xs)', background: 'rgba(251,191,36,0.1)', color: 'rgba(251,191,36,0.9)', fontSize: 10, fontWeight: 700 }}>EXPIRING SOON</span>}
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            {cn     && <span>CN: {cn}</span>}
            {issuer && <span>Issuer: {issuer}</span>}
            {expiry && <span style={{ color: expired ? 'var(--error)' : expiring ? 'rgba(251,191,36,0.9)' : 'var(--text-tertiary)' }}>Expires: {expiry}</span>}
          </div>
        </div>

        <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
          {!isActive && (
            <button onClick={onActivate} disabled={activating} style={btnPrimary}>
              <Icon name="check_circle" size={14} />{activating ? 'Activating…' : 'Activate'}
            </button>
          )}
          {details && (
            <button onClick={() => setExpanded(!expanded)} style={btnGhost}>
              <Icon name={expanded ? 'expand_less' : 'expand_more'} size={15} />
            </button>
          )}
        </div>
      </div>

      {expanded && details && (
        <div style={{ borderTop: '1px solid var(--border)', padding: '0 20px 16px' }}>
          <pre style={{ marginTop: 14, background: 'var(--surface)', borderRadius: 'var(--radius-sm)', padding: '10px 14px', fontFamily: 'var(--font-mono)', fontSize: 10, color: 'rgba(255,255,255,0.55)', whiteSpace: 'pre-wrap', overflow: 'auto', maxHeight: 200, margin: 0, lineHeight: 1.6 }}>
            {details}
          </pre>
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// CertificatesPage
// ---------------------------------------------------------------------------

export function CertificatesPage() {
  const qc = useQueryClient()
  const [showGenerate, setShowGenerate] = useState(false)

  const certsQ = useQuery({
    queryKey: ['certs', 'list'],
    queryFn:  ({ signal }) => api.get<CertsResponse>('/api/certs/list', signal),
  })

  const activate = useMutation({
    mutationFn: (name: string) => api.post('/api/certs/activate', { name }),
    onSuccess: () => { toast.success('Certificate activated — nginx reloaded'); qc.invalidateQueries({ queryKey: ['certs', 'list'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const certs     = certsQ.data?.certs ?? []
  const activeCert = certsQ.data?.active_cert ?? ''

  return (
    <div style={{ maxWidth: 860 }}>
      <div style={{ marginBottom: 28 }}>
        <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>Certificates</h1>
        <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>TLS certificates for the web interface — self-signed or custom</p>
      </div>

      {/* Active cert info */}
      {activeCert && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 18px', background: 'var(--primary-bg)', border: '1px solid rgba(138,156,255,0.2)', borderRadius: 'var(--radius-lg)', marginBottom: 24, fontSize: 'var(--text-sm)' }}>
          <Icon name="https" size={16} style={{ color: 'var(--primary)', flexShrink: 0 }} />
          <span style={{ color: 'var(--text-secondary)' }}>Active certificate:</span>
          <span style={{ fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--primary)' }}>{activeCert}</span>
        </div>
      )}

      <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 16 }}>
        <button onClick={() => setShowGenerate(true)} style={btnPrimary}>
          <Icon name="add" size={15} />Generate Self-Signed
        </button>
      </div>

      {certsQ.isLoading && <Skeleton height={250} />}
      {certsQ.isError   && <ErrorState error={certsQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['certs', 'list'] })} />}

      <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
        {certs.map(cert => (
          <CertCard
            key={cert.name}
            cert={cert}
            isActive={activeCert.includes(cert.name)}
            onActivate={() => activate.mutate(cert.name)}
            activating={activate.isPending}
          />
        ))}
        {!certsQ.isLoading && certs.length === 0 && (
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '48px 0', gap: 12, background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)' }}>
            <Icon name="verified_user" size={40} style={{ color: 'var(--text-tertiary)', opacity: 0.4 }} />
            <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No certificates installed</div>
            <div style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-xs)' }}>Generate a self-signed certificate to get started</div>
          </div>
        )}
      </div>

      {showGenerate && (
        <GenerateModal
          onClose={() => setShowGenerate(false)}
          onDone={() => qc.invalidateQueries({ queryKey: ['certs', 'list'] })}
        />
      )}
    </div>
  )
}
