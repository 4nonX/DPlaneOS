/**
 * pages/SharesPage.tsx — SMB/NFS Shares (Phase 2)
 *
 * Calls (matching daemon routes exactly):
 *   GET    /api/shares/list
 *   GET    /api/shares
 *   POST   /api/shares          (create)
 *   DELETE /api/shares          (delete, body: { name })
 *   POST   /api/shares/smb/reload
 *   POST   /api/shares/smb/test
 *   POST   /api/shares/nfs/reload
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

interface Share {
  name:        string
  path:        string
  comment:     string
  readonly:    boolean
  guestok:     boolean
  browseable:  boolean
  validusers?: string
}

interface SharesListResponse { success: boolean; shares?: Share[]; data?: Share[] }

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const inputStyle: React.CSSProperties = {
  background: 'var(--surface)', border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)', padding: '9px 13px',
  color: 'var(--text)', fontSize: 'var(--text-sm)', width: '100%',
  fontFamily: 'var(--font-ui)', outline: 'none', boxSizing: 'border-box',
}
const btnPrimary: React.CSSProperties = {
  padding: '9px 20px', background: 'var(--primary)', color: '#000',
  border: 'none', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 600,
}
const btnGhost: React.CSSProperties = {
  padding: '9px 16px', background: 'var(--surface)', color: 'var(--text-secondary)',
  border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 500, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnDanger: React.CSSProperties = {
  padding: '7px 14px', background: 'var(--error-bg)', border: '1px solid var(--error-border)',
  borderRadius: 'var(--radius-sm)', cursor: 'pointer', color: 'var(--error)',
  fontSize: 'var(--text-xs)', fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 5,
}

// ---------------------------------------------------------------------------
// CreateShareModal
// ---------------------------------------------------------------------------

function CreateShareModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [name, setName] = useState('')
  const [path, setPath] = useState('')
  const [comment, setComment] = useState('')
  const [readonly, setReadonly] = useState(false)
  const [guestok, setGuestok] = useState(false)
  const [validusers, setValidusers] = useState('')

  const mutation = useMutation({
    mutationFn: () => api.post('/api/shares', { name, path, comment, readonly, guestok, browseable: true, validusers }),
    onSuccess: () => { toast.success(`Share "${name}" created`); onCreated(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })

  function submit() {
    if (!name.trim()) { toast.error('Share name required'); return }
    if (!path.trim()) { toast.error('Path required'); return }
    if (!path.startsWith('/')) { toast.error('Path must be absolute (start with /)'); return }
    mutation.mutate()
  }

  return (
    <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 200 }}
      onClick={e => e.target === e.currentTarget && onClose()}>
      <div style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 32, width: 480, maxWidth: '90vw' }}>
        <h3 style={{ fontSize: 'var(--text-lg)', fontWeight: 700, marginBottom: 24 }}>Create SMB Share</h3>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Share Name</span>
            <input value={name} onChange={e => setName(e.target.value)} placeholder="e.g. media" style={inputStyle} autoFocus
              onKeyDown={e => e.key === 'Enter' && submit()} />
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Path</span>
            <input value={path} onChange={e => setPath(e.target.value)} placeholder="/tank/media" style={inputStyle} />
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Comment (optional)</span>
            <input value={comment} onChange={e => setComment(e.target.value)} placeholder="Media library" style={inputStyle} />
          </label>
          <label style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Valid Users (optional, space-separated)</span>
            <input value={validusers} onChange={e => setValidusers(e.target.value)} placeholder="alice bob @media" style={inputStyle} />
          </label>
          <div style={{ display: 'flex', gap: 24 }}>
            <CheckRow label="Read-only" checked={readonly} onChange={setReadonly} />
            <CheckRow label="Guest access" checked={guestok} onChange={setGuestok} />
          </div>
        </div>
        <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', marginTop: 24 }}>
          <button onClick={onClose} style={btnGhost}>Cancel</button>
          <button onClick={submit} disabled={mutation.isPending} style={btnPrimary}>
            {mutation.isPending ? 'Creating…' : 'Create Share'}
          </button>
        </div>
      </div>
    </div>
  )
}

function CheckRow({ label, checked, onChange }: { label: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
      <input type="checkbox" checked={checked} onChange={e => onChange(e.target.checked)}
        style={{ accentColor: 'var(--primary)', width: 16, height: 16 }} />
      {label}
    </label>
  )
}

// ---------------------------------------------------------------------------
// ShareCard
// ---------------------------------------------------------------------------

function ShareCard({ share, onDeleted }: { share: Share; onDeleted: () => void }) {
  const [confirming, setConfirming] = useState(false)

  const deleteMutation = useMutation({
    mutationFn: () => api.delete('/api/shares', { name: share.name }),
    onSuccess: () => { toast.success(`Share "${share.name}" deleted`); onDeleted() },
    onError: (e: Error) => toast.error(e.message),
  })

  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: '20px 24px', display: 'flex', alignItems: 'flex-start', gap: 16 }}>
      <Icon name="folder_shared" size={28} style={{ color: 'var(--primary)', flexShrink: 0, marginTop: 2 }} />
      <div style={{ flex: 1 }}>
        <div style={{ fontSize: 'var(--text-md)', fontWeight: 700, marginBottom: 4 }}>{share.name}</div>
        <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', marginBottom: 8 }}>{share.path}</div>
        {share.comment && <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 8 }}>{share.comment}</div>}
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          {share.readonly && <Badge label="Read-only" color="var(--warning)" />}
          {share.guestok && <Badge label="Guest OK" color="var(--info)" />}
          {share.validusers && <Badge label={`Users: ${share.validusers}`} color="var(--text-tertiary)" />}
        </div>
      </div>
      <div style={{ display: 'flex', gap: 8, flexShrink: 0 }}>
        {!confirming
          ? <button style={btnDanger} onClick={() => setConfirming(true)}><Icon name="delete" size={14} />Delete</button>
          : <>
              <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', alignSelf: 'center' }}>Sure?</span>
              <button style={btnDanger} onClick={() => deleteMutation.mutate()} disabled={deleteMutation.isPending}>
                {deleteMutation.isPending ? '…' : 'Yes'}
              </button>
              <button style={btnGhost} onClick={() => setConfirming(false)}>No</button>
            </>
        }
      </div>
    </div>
  )
}

function Badge({ label, color }: { label: string; color: string }) {
  return (
    <span style={{ padding: '2px 8px', borderRadius: 'var(--radius-full)', background: `${color}18`, border: `1px solid ${color}30`, color, fontSize: 'var(--text-2xs)', fontWeight: 600 }}>
      {label}
    </span>
  )
}

// ---------------------------------------------------------------------------
// SharesPage
// ---------------------------------------------------------------------------

export function SharesPage() {
  const qc = useQueryClient()
  const [showCreate, setShowCreate] = useState(false)

  const sharesQ = useQuery({
    queryKey: ['shares', 'list'],
    queryFn: ({ signal }) => api.get<SharesListResponse>('/api/shares/list', signal),
    refetchInterval: 30_000,
  })

  const smbReload = useMutation({
    mutationFn: () => api.post('/api/shares/smb/reload', {}),
    onSuccess: () => toast.success('SMB config reloaded'),
    onError: (e: Error) => toast.error(e.message),
  })
  const smbTest = useMutation({
    mutationFn: () => api.post<{ success: boolean; output?: string; error?: string }>('/api/shares/smb/test', {}),
    onSuccess: (data) => {
      if (data.success) toast.success('SMB config OK')
      else toast.error(`SMB test failed: ${data.error || 'unknown'}`)
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const nfsReload = useMutation({
    mutationFn: () => api.post('/api/shares/nfs/reload', {}),
    onSuccess: () => toast.success('NFS exports reloaded'),
    onError: (e: Error) => toast.error(e.message),
  })

  const shares = sharesQ.data?.shares ?? sharesQ.data?.data ?? []
  function refresh() { qc.invalidateQueries({ queryKey: ['shares', 'list'] }) }

  return (
    <div style={{ maxWidth: 1000 }}>
      <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', marginBottom: 32 }}>
        <div>
          <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>Shares</h1>
          <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>SMB network shares</p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          <button onClick={() => smbTest.mutate()} disabled={smbTest.isPending} style={btnGhost} title="Test SMB config">
            <Icon name="bug_report" size={16} />{smbTest.isPending ? 'Testing…' : 'Test Config'}
          </button>
          <button onClick={() => smbReload.mutate()} disabled={smbReload.isPending} style={btnGhost} title="Reload SMB">
            <Icon name="restart_alt" size={16} />{smbReload.isPending ? 'Reloading…' : 'Reload SMB'}
          </button>
          <button onClick={() => nfsReload.mutate()} disabled={nfsReload.isPending} style={btnGhost} title="Reload NFS exports">
            <Icon name="sync" size={16} />{nfsReload.isPending ? 'Reloading…' : 'Reload NFS'}
          </button>
          <button onClick={() => setShowCreate(true)} style={btnPrimary}>
            <Icon name="add" size={16} /> Add Share
          </button>
        </div>
      </div>

      {sharesQ.isLoading && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          {[0, 1, 2].map(i => <Skeleton key={i} height={100} style={{ borderRadius: 'var(--radius-lg)' }} />)}
        </div>
      )}
      {sharesQ.isError && <ErrorState error={sharesQ.error} onRetry={refresh} />}
      {!sharesQ.isLoading && !sharesQ.isError && shares.length === 0 && (
        <div style={{ textAlign: 'center', padding: '64px 24px', border: '1px dashed var(--border)', borderRadius: 'var(--radius-xl)', color: 'var(--text-tertiary)' }}>
          <Icon name="folder_shared" size={48} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          <div style={{ fontSize: 'var(--text-lg)', fontWeight: 600 }}>No shares configured</div>
          <div style={{ fontSize: 'var(--text-sm)', marginTop: 6 }}>Create a share to access data over the network</div>
        </div>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {shares.map(share => (
          <ShareCard key={share.name} share={share} onDeleted={refresh} />
        ))}
      </div>

      {showCreate && (
        <CreateShareModal onClose={() => setShowCreate(false)} onCreated={refresh} />
      )}
    </div>
  )
}
