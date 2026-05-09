/**
 * pages/FileSharesPage.tsx - Manage shareable file download links
 *
 * API:
 *   GET    /api/file-shares         → list all shares
 *   POST   /api/file-shares         → create share
 *   DELETE /api/file-shares/{id}    → revoke share
 *   GET    /api/s/{token}           → public info (no auth)
 *   GET    /api/s/{token}/download  → public download (no auth)
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { Modal } from '@/components/ui/Modal'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { Spinner } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'
import type React from 'react'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface FileShare {
  id: string
  token: string
  path: string
  filename: string
  created_by: string
  created_at: string
  expires_at?: string
  has_password: boolean
  max_downloads: number
  download_count: number
  revoked: boolean
}

interface SharesResponse {
  success: boolean
  shares: FileShare[]
}

interface CreateResponse {
  success: boolean
  share: FileShare
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fmtDate(s: string): string {
  try { return new Date(s).toLocaleString() }
  catch { return s }
}

function shareStatus(s: FileShare): { label: string; color: string } {
  if (s.revoked) return { label: 'Revoked', color: 'var(--error)' }
  if (s.expires_at && new Date(s.expires_at) < new Date()) return { label: 'Expired', color: 'var(--text-tertiary)' }
  if (s.max_downloads > 0 && s.download_count >= s.max_downloads) return { label: 'Limit reached', color: 'var(--text-tertiary)' }
  return { label: 'Active', color: 'var(--success)' }
}

function shareUrl(token: string): string {
  return `${window.location.origin}/api/s/${token}/download`
}

// ---------------------------------------------------------------------------
// Create modal
// ---------------------------------------------------------------------------

interface CreateShareModalProps {
  onClose: () => void
  onCreated: (share: FileShare) => void
}

function CreateShareModal({ onClose, onCreated }: CreateShareModalProps) {
  const [path, setPath]               = useState('')
  const [expiresHours, setExpires]    = useState<number>(72)
  const [neverExpires, setNever]      = useState(false)
  const [password, setPassword]       = useState('')
  const [maxDownloads, setMaxDl]      = useState<number>(0)
  const [showPw, setShowPw]           = useState(false)

  const mut = useMutation({
    mutationFn: () => api.post<CreateResponse>('/api/file-shares', {
      path,
      expires_in_hours: neverExpires ? 0 : expiresHours,
      password,
      max_downloads: maxDownloads,
    }),
    onSuccess: (res) => {
      toast.success('Share link created')
      onCreated(res.share)
      onClose()
    },
    onError: (e: Error) => toast.error(e.message),
  })

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!path.trim()) { toast.error('File path is required'); return }
    mut.mutate()
  }

  return (
    <Modal title="Create Share Link" onClose={onClose}>
      <form onSubmit={handleSubmit}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>File path</label>
            <input className="input" type="text" placeholder="/mnt/tank/documents/report.pdf"
              value={path} onChange={e => setPath(e.target.value)} disabled={mut.isPending}
              autoFocus style={{ fontFamily: 'var(--font-mono)', fontSize: 13 }} />
            <p style={{ marginTop: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              Absolute path to the file to share. You can also right-click any file in the File Explorer.
            </p>
          </div>

          <div>
            <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10, cursor: 'pointer', userSelect: 'none' }}>
              <input type="checkbox" checked={neverExpires} disabled={mut.isPending}
                onChange={e => setNever(e.target.checked)} />
              <span style={{ fontSize: 'var(--text-sm)', fontWeight: 500 }}>Never expires</span>
            </label>
            {!neverExpires && (
              <div>
                <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>Expires after (hours)</label>
                <input className="input" type="number" min={1} max={87600}
                  value={expiresHours} onChange={e => setExpires(Number(e.target.value))} disabled={mut.isPending} />
                <p style={{ marginTop: 4, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
                  Common: 24h (1 day), 168h (1 week), 720h (30 days)
                </p>
              </div>
            )}
          </div>

          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>
              Download limit <span style={{ color: 'var(--text-tertiary)', fontWeight: 400 }}>(0 = unlimited)</span>
            </label>
            <input className="input" type="number" min={0}
              value={maxDownloads} onChange={e => setMaxDl(Number(e.target.value))} disabled={mut.isPending} />
          </div>

          <div>
            <label style={{ display: 'block', fontSize: 'var(--text-sm)', fontWeight: 500, marginBottom: 6 }}>
              Password <span style={{ color: 'var(--text-tertiary)', fontWeight: 400 }}>(optional)</span>
            </label>
            <div style={{ position: 'relative' }}>
              <input className="input" type={showPw ? 'text' : 'password'} placeholder="Leave blank for no password"
                value={password} onChange={e => setPassword(e.target.value)} disabled={mut.isPending}
                style={{ paddingRight: 40 }} />
              <button type="button" onClick={() => setShowPw(p => !p)}
                style={{ position: 'absolute', right: 10, top: '50%', transform: 'translateY(-50%)', background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-tertiary)', padding: 0 }}>
                <Icon name={showPw ? 'visibility_off' : 'visibility'} size={16} />
              </button>
            </div>
          </div>
        </div>

        <div style={{ display: 'flex', justifyContent: 'flex-end', gap: 10, marginTop: 24 }}>
          <button type="button" className="btn btn-ghost" onClick={onClose} disabled={mut.isPending}>Cancel</button>
          <button type="submit" className="btn btn-primary" disabled={mut.isPending}>
            {mut.isPending
              ? <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Spinner size={14} color="rgba(0,0,0,0.7)" /> Creating…</span>
              : <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Icon name="link" size={15} /> Create Link</span>
            }
          </button>
        </div>
      </form>
    </Modal>
  )
}

// ---------------------------------------------------------------------------
// Link created banner
// ---------------------------------------------------------------------------

function NewLinkBanner({ share, onDismiss }: { share: FileShare; onDismiss: () => void }) {
  const url = shareUrl(share.token)
  return (
    <div className="alert alert-success" style={{ marginBottom: 24, display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 16, flexWrap: 'wrap' }}>
      <div>
        <div style={{ fontWeight: 600, marginBottom: 4 }}>Share link created for {share.filename}</div>
        <div style={{ fontFamily: 'var(--font-mono)', fontSize: 13, wordBreak: 'break-all' }}>{url}</div>
      </div>
      <div style={{ display: 'flex', gap: 8, flexShrink: 0 }}>
        <button className="btn btn-ghost btn-sm" onClick={() => { navigator.clipboard.writeText(url); toast.success('Copied to clipboard') }}>
          <Icon name="content_copy" size={14} /> Copy
        </button>
        <button className="btn btn-ghost btn-sm" onClick={onDismiss}>
          <Icon name="close" size={14} />
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export function FileSharesPage() {
  const qc = useQueryClient()
  const [showCreate, setShowCreate]   = useState(false)
  const [newShare, setNewShare]       = useState<FileShare | null>(null)
  const { confirm, ConfirmDialog }    = useConfirm()

  const { data, isLoading, error, refetch } = useQuery<SharesResponse>({
    queryKey: ['file-shares'],
    queryFn: () => api.get<SharesResponse>('/api/file-shares'),
    refetchInterval: 30000,
  })

  const revokeMut = useMutation({
    mutationFn: (id: string) => api.delete(`/api/file-shares/${id}`),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['file-shares'] })
      toast.success('Share link revoked')
    },
    onError: (e: Error) => toast.error(e.message),
  })

  async function handleRevoke(s: FileShare) {
    const ok = await confirm({
      title: 'Revoke share link?',
      message: `The download link for "${s.filename}" will be permanently disabled. Anyone with the URL will no longer be able to download the file.`,
      danger: true,
      confirmLabel: 'Revoke',
    })
    if (ok) revokeMut.mutate(s.id)
  }

  const shares = data?.shares ?? []
  const active  = shares.filter(s => !s.revoked && !(s.expires_at && new Date(s.expires_at) < new Date()) && !(s.max_downloads > 0 && s.download_count >= s.max_downloads))
  const expired = shares.filter(s => s.revoked || (s.expires_at && new Date(s.expires_at) < new Date()) || (s.max_downloads > 0 && s.download_count >= s.max_downloads))

  return (
    <div className="page-container">
      <header className="page-header">
        <div>
          <h1 className="page-title">File Share Links</h1>
          <p className="page-subtitle">Create time-limited, optionally password-protected download links for individual files.</p>
        </div>
        <button className="btn btn-primary" onClick={() => setShowCreate(true)}>
          <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}><Icon name="add_link" size={16} /> New Share Link</span>
        </button>
      </header>

      {newShare && <NewLinkBanner share={newShare} onDismiss={() => setNewShare(null)} />}

      {isLoading ? (
        <div className="card" style={{ padding: 24 }}>
          {[0, 1, 2].map(i => <Skeleton key={i} style={{ height: 48, marginBottom: 10 }} />)}
        </div>
      ) : error ? (
        <ErrorState error={error} title="Failed to load share links" onRetry={() => refetch()} />
      ) : shares.length === 0 ? (
        <div className="empty-state">
          <Icon name="link_off" size={48} className="empty-state-icon" />
          <h3 className="empty-state-title">No share links yet</h3>
          <p className="empty-state-body">
            Create a link to let others download a specific file without needing a D-PlaneOS account.
            You can also create links by right-clicking any file in the File Explorer.
          </p>
        </div>
      ) : (
        <>
          {active.length > 0 && (
            <div className="card" style={{ padding: 0, overflow: 'hidden', marginBottom: 24 }}>
              <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--border)' }}>
                <h2 style={{ fontSize: 'var(--text-base)', fontWeight: 700, margin: 0 }}>Active links</h2>
              </div>
              <ShareTable shares={active} onRevoke={handleRevoke} canRevoke={!revokeMut.isPending} />
            </div>
          )}

          {expired.length > 0 && (
            <div className="card" style={{ padding: 0, overflow: 'hidden' }}>
              <div style={{ padding: '16px 20px', borderBottom: '1px solid var(--border)' }}>
                <h2 style={{ fontSize: 'var(--text-base)', fontWeight: 700, margin: 0 }}>Expired / revoked</h2>
              </div>
              <ShareTable shares={expired} onRevoke={handleRevoke} canRevoke={false} />
            </div>
          )}
        </>
      )}

      {showCreate && (
        <CreateShareModal
          onClose={() => setShowCreate(false)}
          onCreated={(s) => {
            qc.invalidateQueries({ queryKey: ['file-shares'] })
            setNewShare(s)
          }}
        />
      )}

      <ConfirmDialog />
    </div>
  )
}

// ---------------------------------------------------------------------------
// Share table (shared between active / expired sections)
// ---------------------------------------------------------------------------

function ShareTable({ shares, onRevoke, canRevoke }: {
  shares: FileShare[]
  onRevoke: (s: FileShare) => void
  canRevoke: boolean
}) {
  return (
    <table className="data-table">
      <thead>
        <tr>
          <th>File</th>
          <th>Path</th>
          <th>Created by</th>
          <th>Expires</th>
          <th>Downloads</th>
          <th>Status</th>
          <th style={{ textAlign: 'right' }}>Actions</th>
        </tr>
      </thead>
      <tbody>
        {shares.map(s => {
          const status = shareStatus(s)
          const url = shareUrl(s.token)
          return (
            <tr key={s.id}>
              <td>
                <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                  <Icon name="insert_drive_file" size={14} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
                  <span style={{ fontWeight: 500 }}>{s.filename}</span>
                  {s.has_password && (
                    <Icon name="lock" size={13} style={{ color: 'var(--text-tertiary)' }} />
                  )}
                </div>
              </td>
              <td style={{ fontFamily: 'var(--font-mono)', fontSize: 12, color: 'var(--text-secondary)', maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {s.path}
              </td>
              <td style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{s.created_by}</td>
              <td style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', whiteSpace: 'nowrap' }}>
                {s.expires_at ? fmtDate(s.expires_at) : <span style={{ color: 'var(--text-tertiary)' }}>Never</span>}
              </td>
              <td style={{ fontSize: 'var(--text-sm)', whiteSpace: 'nowrap' }}>
                {s.download_count}
                {s.max_downloads > 0 && <span style={{ color: 'var(--text-tertiary)' }}> / {s.max_downloads}</span>}
              </td>
              <td>
                <span style={{ fontSize: 'var(--text-sm)', fontWeight: 600, color: status.color }}>{status.label}</span>
              </td>
              <td style={{ textAlign: 'right' }}>
                <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 4 }}>
                  {!s.revoked && (
                    <button className="btn btn-ghost btn-xs" style={{ padding: '4px 8px' }}
                      onClick={() => { navigator.clipboard.writeText(url); toast.success('Copied') }}
                      title="Copy link">
                      <Icon name="content_copy" size={13} />
                    </button>
                  )}
                  {canRevoke && !s.revoked && (
                    <button className="btn btn-ghost btn-xs" style={{ padding: '4px 8px', color: 'var(--error)' }}
                      onClick={() => onRevoke(s)} title="Revoke link">
                      <Icon name="link_off" size={13} />
                    </button>
                  )}
                </div>
              </td>
            </tr>
          )
        })}
      </tbody>
    </table>
  )
}
