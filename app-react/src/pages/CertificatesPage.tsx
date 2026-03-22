/**
 * pages/CertificatesPage.tsx - TLS Certificates (Phase 6)
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
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { useJob } from '@/hooks/useJob'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import { Modal } from '@/components/ui/Modal'
import type React from 'react'

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

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="field">
      <label className="field-label">{label}</label>
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
      return api.post('/api/certs/generate', {
        name: name.trim(),
        cn:   cn.trim(),
        days: Number(days) || 365,
        sans: sans.trim(), // Backend expects string (as seen in system_extended.go)
      })
    },
    onSuccess: () => { toast.success('Certificate generated'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title="Generate Self-Signed Certificate" onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        <Field label="Certificate Name" hint="Used as the filename on disk">
          <input value={name} onChange={e => setName(e.target.value)} placeholder="server" className="input" autoFocus />
        </Field>

        <Field label="Common Name (CN)" hint="Hostname or domain this certificate is for">
          <input value={cn} onChange={e => setCn(e.target.value)} placeholder="dplaneos.local"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </Field>

        <Field label="Subject Alternative Names" hint="Comma-separated DNS names or IPs (optional)">
          <input value={sans} onChange={e => setSans(e.target.value)} placeholder="192.168.1.100, dplaneos.local"
            className="input" style={{ fontFamily: 'var(--font-mono)' }} />
        </Field>

        <Field label="Validity (days)">
          <input type="number" value={days} onChange={e => setDays(e.target.value)} min={1} max={3650} className="input" style={{ width: 120 }} />
        </Field>

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button onClick={() => generate.mutate()} disabled={generate.isPending} className="btn btn-primary">
            <Icon name="verified_user" size={15} />{generate.isPending ? 'Generating…' : 'Generate'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ImportModal
// ---------------------------------------------------------------------------

function ImportModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState('')
  const [cert, setCert] = useState('')
  const [key, setKey]   = useState('')

  const importCert = useMutation({
    mutationFn: () => {
      if (!name.trim() || !cert.trim() || !key.trim()) throw new Error('All fields are required')
      return api.post('/api/certs/import', {
        name: name.trim(),
        cert: cert.trim(),
        key:  key.trim(),
      })
    },
    onSuccess: () => { toast.success('Certificate imported'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <Modal title="Import Certificate" onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        <Field label="Certificate Name" hint="Friendly name for this certificate">
          <input value={name} onChange={e => setName(e.target.value)} placeholder="my-custom-cert" className="input" autoFocus />
        </Field>

        <Field label="Certificate (PEM)" hint="Paste the .crt or .pem file content">
          <textarea 
            value={cert} 
            onChange={e => setCert(e.target.value)} 
            placeholder="-----BEGIN CERTIFICATE-----"
            className="input" 
            style={{ fontFamily: 'var(--font-mono)', minHeight: 120, fontSize: 'var(--text-2xs)' }} 
          />
        </Field>

        <Field label="Private Key" hint="Paste the .key file content">
          <textarea 
            value={key} 
            onChange={e => setKey(e.target.value)} 
            placeholder="-----BEGIN PRIVATE KEY-----"
            className="input" 
            style={{ fontFamily: 'var(--font-mono)', minHeight: 100, fontSize: 'var(--text-2xs)' }} 
          />
        </Field>

        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
          <button onClick={onClose} className="btn btn-ghost">Cancel</button>
          <button onClick={() => importCert.mutate()} disabled={importCert.isPending} className="btn btn-primary">
            <Icon name="upload" size={15} />{importCert.isPending ? 'Importing…' : 'Import'}
          </button>
        </div>
      </div>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// ACMEWizard
// ---------------------------------------------------------------------------

function ACMEWizard({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [name, setName]     = useState('le-cert')
  const [domain, setDomain] = useState('')
  const [email, setEmail]   = useState('')
  const [staging, setStaging] = useState(true)
  const [jobId, setJobId]   = useState<string | null>(null)
  const [verifying, setVerifying] = useState(false)
  const [verified, setVerified]   = useState(false)

  const { data: job, error: apiError } = useJob(jobId)
  const isDone   = job?.status === 'done'
  const isFailed = job?.status === 'failed'
  const error    = job?.error || apiError?.message

  const verifyProxy = async () => {
    if (!domain.trim()) {
      toast.error('Domain name is required to verify proxy')
      return
    }
    setVerifying(true)
    try {
      const resp = await api.get<{ success: boolean; message: string }>(`/api/system/certs/acme/check?domain=${domain.trim()}`)
      if (resp.success) {
        setVerified(true)
        toast.success(resp.message)
      }
    } catch (e: any) {
      toast.error(e.message || 'Proxy verification failed')
    } finally {
      setVerifying(false)
    }
  }

  const request = useMutation({
    mutationFn: async () => {
      if (!name.trim() || !domain.trim() || !email.trim()) throw new Error('Name, Domain, and Email are required')
      if (!verified && !staging) {
        if (!confirm('Proxy verification has not been completed. The ACME request might fail if port 80 is not correctly proxied to 8080. Proceed anyway?')) {
          throw new Error('Verification cancelled')
        }
      }
      const resp = await api.post<{ success: boolean; jobId: string }>('/api/certs/acme', {
        name: name.trim(),
        domain: domain.trim(),
        email: email.trim(),
        staging,
      })
      setJobId(resp.jobId)
      return resp
    },
    onError: (e: Error) => toast.error(e.message),
  })

  // Handle job completion
  if (isDone) {
    onDone()
    onClose()
    toast.success('ACME certificate obtained successfully')
  }

  const progressData = job?.progress as { status: string; message: string } | undefined

  return (
    <Modal title="Let's Encrypt / ACME Wizard" onClose={onClose}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
        <div className="alert alert-info" style={{ fontSize: 'var(--text-xs)', lineHeight: 1.5 }}>
          <Icon name="info" size={14} />
          <span>
            This wizard uses <strong>HTTP-01</strong> challenge. Port 80 must be open and pointing to this D-PlaneOS instance. 
            The daemon will start a challenge server on port 8080. 
            <strong> Ensure Nginx proxies /.well-known/acme-challenge/ to 8080.</strong>
          </span>
        </div>

        {jobId ? (
          <div style={{ padding: '20px 0', textAlign: 'center' }}>
            <div style={{ marginBottom: 16 }}>
              <div style={{ fontWeight: 600, marginBottom: 4 }}>{progressData?.message || 'Processing...'}</div>
              <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Job ID: {jobId}</div>
            </div>
            
            <div style={{ height: 4, background: 'var(--surface)', borderRadius: 2, overflow: 'hidden', marginBottom: 12 }}>
              <div style={{ 
                height: '100%', 
                width: isFailed ? '100%' : '100%', 
                background: isFailed ? 'var(--error)' : 'var(--primary)',
                transition: 'width 0.3s ease',
                animation: !isDone && !isFailed ? 'shimmer 2s infinite linear' : 'none'
              }} />
            </div>

            {isFailed && (
              <div style={{ color: 'var(--error)', fontSize: 'var(--text-sm)', background: 'var(--error-bg)', padding: 12, borderRadius: 'var(--radius-md)', border: '1px solid var(--error-border)' }}>
                <strong>Error:</strong> {error || 'ACME request failed'}
              </div>
            )}

            <div style={{ display: 'flex', gap: 8, justifyContent: 'center', marginTop: 24 }}>
               <button onClick={onClose} className="btn btn-ghost">Close</button>
            </div>
          </div>
        ) : (
          <>
            <Field label="Certificate Name">
              <input value={name} onChange={e => setName(e.target.value)} placeholder="le-cert" className="input" autoFocus />
            </Field>

            <div style={{ display: 'flex', gap: 10, alignItems: 'flex-end' }}>
              <div style={{ flex: 1 }}>
                <Field label="Domain Name" hint="e.g. nas.example.com">
                  <input value={domain} onChange={e => { setDomain(e.target.value); setVerified(false); }} placeholder="nas.yourdomain.com"
                    className="input" style={{ fontFamily: 'var(--font-mono)' }} />
                </Field>
              </div>
              <button 
                onClick={verifyProxy} 
                disabled={verifying || !domain.trim()} 
                className={`btn ${verified ? 'btn-success' : 'btn-ghost'}`}
                style={{ height: 42, marginBottom: 20 }}
              >
                <Icon name={verified ? 'check_circle' : 'router'} size={15} />
                {verifying ? 'Verifying...' : verified ? 'Verified' : 'Verify Proxy'}
              </button>
            </div>

            <Field label="Email Address" hint="For Let's Encrypt expiration notices">
              <input type="email" value={email} onChange={e => setEmail(e.target.value)} placeholder="admin@example.com" className="input" />
            </Field>

            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <input type="checkbox" id="acme-staging" checked={staging} onChange={e => setStaging(e.target.checked)} />
              <label htmlFor="acme-staging" style={{ fontSize: 'var(--text-sm)', cursor: 'pointer' }}>Use Staging Environment (Recommended)</label>
            </div>

            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
              <button onClick={onClose} className="btn btn-ghost">Cancel</button>
              <button onClick={() => request.mutate()} disabled={request.isPending} className="btn btn-primary">
                <Icon name="encrypted" size={15} />{request.isPending ? 'Requesting…' : 'Obtain Certificate'}
              </button>
            </div>
          </>
        )}
      </div>
    </Modal>
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
            {isActive  && <span className="badge badge-primary">ACTIVE</span>}
            {expired   && <span className="badge badge-error">EXPIRED</span>}
            {expiring  && !expired && <span className="badge badge-warning">EXPIRING SOON</span>}
          </div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            {cn     && <span>CN: {cn}</span>}
            {issuer && <span>Issuer: {issuer}</span>}
            {expiry && <span style={{ color: expired ? 'var(--error)' : expiring ? 'rgba(251,191,36,0.9)' : 'var(--text-tertiary)' }}>Expires: {expiry}</span>}
          </div>
        </div>

        <div style={{ display: 'flex', gap: 6, flexShrink: 0 }}>
          {!isActive && (
            <button onClick={onActivate} disabled={activating} className="btn btn-primary">
              <Icon name="check_circle" size={14} />{activating ? 'Activating…' : 'Activate'}
            </button>
          )}
          {details && (
            <button onClick={() => setExpanded(!expanded)} className="btn btn-ghost">
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
  const [showImport, setShowImport] = useState(false)
  const [showACME, setShowACME] = useState(false)

  const certsQ = useQuery({
    queryKey: ['certs', 'list'],
    queryFn:  ({ signal }) => api.get<CertsResponse>('/api/certs/list', signal),
  })

  const activate = useMutation({
    mutationFn: (name: string) => api.post('/api/certs/activate', { name }),
    onSuccess: () => { toast.success('Certificate activated - nginx reloaded'); qc.invalidateQueries({ queryKey: ['certs', 'list'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const certs     = certsQ.data?.certs ?? []
  const activeCert = certsQ.data?.active_cert ?? ''

  return (
    <div style={{ maxWidth: 860 }}>
      <div className="page-header">
        <h1 className="page-title">Certificates</h1>
        <p className="page-subtitle">TLS certificates for the web interface - self-signed or custom</p>
      </div>

      {/* Active cert info */}
      {activeCert && (
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 18px', background: 'var(--primary-bg)', border: '1px solid rgba(138,156,255,0.2)', borderRadius: 'var(--radius-lg)', marginBottom: 24, fontSize: 'var(--text-sm)' }}>
          <Icon name="https" size={16} style={{ color: 'var(--primary)', flexShrink: 0 }} />
          <span style={{ color: 'var(--text-secondary)' }}>Active certificate:</span>
          <span style={{ fontWeight: 700, fontFamily: 'var(--font-mono)', color: 'var(--primary)' }}>{activeCert}</span>
        </div>
      )}

      <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 10, marginBottom: 16 }}>
        <button onClick={() => setShowImport(true)} className="btn btn-ghost">
          <Icon name="upload" size={15} />Import
        </button>
        <button onClick={() => setShowACME(true)} className="btn btn-ghost">
          <Icon name="encrypted" size={15} />Let's Encrypt
        </button>
        <button onClick={() => setShowGenerate(true)} className="btn btn-primary">
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
          <div className="card" style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center', padding: '48px 0', gap: 12, borderRadius: 'var(--radius-lg)' }}>
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

      {showImport && (
        <ImportModal
          onClose={() => setShowImport(false)}
          onDone={() => qc.invalidateQueries({ queryKey: ['certs', 'list'] })}
        />
      )}

      {showACME && (
        <ACMEWizard
          onClose={() => setShowACME(false)}
          onDone={() => qc.invalidateQueries({ queryKey: ['certs', 'list'] })}
        />
      )}
    </div>
  )
}

