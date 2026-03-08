/**
 * pages/FilesPage.tsx — File Explorer (Phase 4)
 *
 * Calls (matching daemon routes exactly):
 *   GET  /api/files/list?path=         → { success, path, files: FileEntry[] }
 *   POST /api/files/rename             → { old_path, new_name }
 *   POST /api/files/copy               → { source, destination }
 *   POST /api/files/mkdir              → { path }
 *   POST /api/files/delete             → { path }
 *   POST /api/files/chown              → { path, owner, group }
 *   POST /api/files/chmod              → { path, mode }
 *   POST /api/files/upload             → multipart (file, path, filename, fileSize)
 *   GET  /api/trash/list
 *   POST /api/trash/move               → { path }
 *   POST /api/trash/restore            → { name }
 *   POST /api/trash/empty
 */

import { useState, useRef } from 'react'
import type React from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, getSessionId, getUsername, getCsrfToken } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { ErrorState } from '@/components/ui/ErrorState'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface FileEntry {
  name:        string
  path:        string
  is_dir:      boolean
  size:        number
  mtime:       string
  owner?:      string
  group?:      string
  permissions?: string
  mode?:       string
}

interface FilesListResponse { success: boolean; path: string; files: FileEntry[] }

interface TrashEntry { name: string; original_path: string; deleted_at?: string }
interface TrashListResponse { success: boolean; items: TrashEntry[] }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function fmtSize(bytes: number): string {
  if (bytes <= 0) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), u.length - 1)
  return `${(bytes / Math.pow(1024, i)).toFixed(1)} ${u[i]}`
}

function fmtDate(s: string): string {
  if (!s) return '—'
  try { return new Date(s).toLocaleDateString('de-DE', { day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' }) }
  catch { return s }
}

function fileIcon(entry: FileEntry): string {
  if (entry.is_dir) return 'folder'
  const ext = entry.name.split('.').pop()?.toLowerCase() ?? ''
  if (['jpg','jpeg','png','gif','webp','svg'].includes(ext)) return 'image'
  if (['mp4','mkv','avi','mov','webm'].includes(ext)) return 'movie'
  if (['mp3','flac','wav','ogg','m4a'].includes(ext)) return 'audio_file'
  if (['zip','tar','gz','bz2','7z','rar'].includes(ext)) return 'folder_zip'
  if (['pdf'].includes(ext)) return 'picture_as_pdf'
  if (['txt','md','log','conf','yaml','yml','json','toml','sh','py','go','ts','tsx'].includes(ext)) return 'description'
  return 'insert_drive_file'
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
  padding: '8px 16px', background: 'var(--primary)', color: '#000',
  border: 'none', borderRadius: 'var(--radius-sm)', cursor: 'pointer',
  fontSize: 'var(--text-sm)', fontWeight: 700, display: 'inline-flex', alignItems: 'center', gap: 6,
}
const btnDanger: React.CSSProperties = {
  padding: '6px 12px', background: 'var(--error-bg)', border: '1px solid var(--error-border)',
  borderRadius: 'var(--radius-sm)', cursor: 'pointer', color: 'var(--error)',
  fontSize: 'var(--text-xs)', fontWeight: 600, display: 'inline-flex', alignItems: 'center', gap: 4,
}
const inputStyle: React.CSSProperties = {
  background: 'var(--surface)', border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)', padding: '8px 12px',
  color: 'var(--text)', fontSize: 'var(--text-sm)', width: '100%',
  fontFamily: 'var(--font-ui)', outline: 'none', boxSizing: 'border-box',
}

// ---------------------------------------------------------------------------
// ContextMenu
// ---------------------------------------------------------------------------

interface CtxMenuState { x: number; y: number; entry: FileEntry }

function ContextMenu({ state, onClose, onAction }: {
  state: CtxMenuState
  onClose: () => void
  onAction: (action: string, entry: FileEntry) => void
}) {
  const items = [
    { label: 'Rename', icon: 'edit', action: 'rename' },
    { label: 'Copy', icon: 'content_copy', action: 'copy' },
    { label: 'Change Owner', icon: 'manage_accounts', action: 'chown' },
    { label: 'Change Mode', icon: 'lock', action: 'chmod' },
    { label: 'Move to Trash', icon: 'delete', action: 'trash', danger: true },
    { label: 'Delete (permanent)', icon: 'close', action: 'delete', danger: true },
  ]
  return (
    <>
      <div style={{ position: 'fixed', inset: 0, zIndex: 149 }} onClick={onClose} />
      <div style={{
        position: 'fixed', left: state.x, top: state.y, zIndex: 150,
        background: 'var(--bg-elevated)', border: '1px solid var(--border)',
        borderRadius: 'var(--radius-md)', boxShadow: '0 8px 32px rgba(0,0,0,0.4)',
        padding: '4px 0', minWidth: 200,
      }}>
        <div style={{ padding: '8px 14px 6px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', borderBottom: '1px solid var(--border)', marginBottom: 4 }}>
          {state.entry.name}
        </div>
        {items.map(item => (
          <button key={item.action}
            onClick={() => { onAction(item.action, state.entry); onClose() }}
            style={{ width: '100%', background: 'none', border: 'none', cursor: 'pointer', padding: '8px 14px', display: 'flex', alignItems: 'center', gap: 9, fontSize: 'var(--text-sm)', color: item.danger ? 'var(--error)' : 'var(--text)', textAlign: 'left', transition: 'background 0.1s' }}
            onMouseEnter={e => (e.currentTarget.style.background = 'var(--surface-hover)')}
            onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
          >
            <Icon name={item.icon} size={15} />
            {item.label}
          </button>
        ))}
      </div>
    </>
  )
}

// ---------------------------------------------------------------------------
// Modals
// ---------------------------------------------------------------------------

function RenameModal({ entry, onClose, onDone }: { entry: FileEntry; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState(entry.name)
  const mutation = useMutation({
    mutationFn: () => api.post('/api/files/rename', { old_path: entry.path, new_name: name }),
    onSuccess: () => { toast.success('Renamed'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <Modal title="Rename" onClose={onClose}>
      <input value={name} onChange={e => setName(e.target.value)} style={inputStyle} autoFocus
        onKeyDown={e => e.key === 'Enter' && mutation.mutate()} />
      <ModalFooter onClose={onClose} onConfirm={() => mutation.mutate()} loading={mutation.isPending} label="Rename" />
    </Modal>
  )
}

function MkdirModal({ currentPath, onClose, onDone }: { currentPath: string; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState('')
  const mutation = useMutation({
    mutationFn: () => api.post('/api/files/mkdir', { path: `${currentPath.replace(/\/$/, '')}/${name}` }),
    onSuccess: () => { toast.success('Folder created'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <Modal title="New Folder" onClose={onClose}>
      <input value={name} onChange={e => setName(e.target.value)} placeholder="Folder name" style={inputStyle} autoFocus
        onKeyDown={e => e.key === 'Enter' && mutation.mutate()} />
      <ModalFooter onClose={onClose} onConfirm={() => mutation.mutate()} loading={mutation.isPending} label="Create" />
    </Modal>
  )
}

function ChownModal({ entry, onClose, onDone }: { entry: FileEntry; onClose: () => void; onDone: () => void }) {
  const [owner, setOwner] = useState(entry.owner ?? '')
  const [group, setGroup] = useState(entry.group ?? '')
  const mutation = useMutation({
    mutationFn: () => api.post('/api/files/chown', { path: entry.path, owner, group }),
    onSuccess: () => { toast.success('Ownership changed'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <Modal title={`Change Owner — ${entry.name}`} onClose={onClose}>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Owner</span>
          <input value={owner} onChange={e => setOwner(e.target.value)} placeholder="root" style={inputStyle} />
        </label>
        <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
          <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Group</span>
          <input value={group} onChange={e => setGroup(e.target.value)} placeholder="root" style={inputStyle} />
        </label>
      </div>
      <ModalFooter onClose={onClose} onConfirm={() => mutation.mutate()} loading={mutation.isPending} label="Apply" />
    </Modal>
  )
}

function ChmodModal({ entry, onClose, onDone }: { entry: FileEntry; onClose: () => void; onDone: () => void }) {
  const [mode, setMode] = useState(entry.mode ?? '755')
  const mutation = useMutation({
    mutationFn: () => api.post('/api/files/chmod', { path: entry.path, mode }),
    onSuccess: () => { toast.success('Permissions changed'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <Modal title={`Change Mode — ${entry.name}`} onClose={onClose}>
      <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Octal mode</span>
        <input value={mode} onChange={e => setMode(e.target.value)} placeholder="755" style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} autoFocus
          onKeyDown={e => e.key === 'Enter' && mutation.mutate()} />
      </label>
      <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginTop: 4 }}>
        Common: 755 (dir), 644 (file), 600 (private), 777 (world-writable)
      </div>
      <ModalFooter onClose={onClose} onConfirm={() => mutation.mutate()} loading={mutation.isPending} label="Apply" />
    </Modal>
  )
}

// Generic modal frame
function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  return (
    <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 200 }}
      onClick={e => e.target === e.currentTarget && onClose()}>
      <div style={{ background: 'var(--bg-elevated)', border: '1px solid var(--border)', borderRadius: 'var(--radius-xl)', padding: 28, width: 440, maxWidth: '90vw', display: 'flex', flexDirection: 'column', gap: 16 }}>
        <div style={{ fontWeight: 700, fontSize: 'var(--text-lg)' }}>{title}</div>
        {children}
      </div>
    </div>
  )
}

function ModalFooter({ onClose, onConfirm, loading, label }: { onClose: () => void; onConfirm: () => void; loading: boolean; label: string }) {
  return (
    <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
      <button onClick={onClose} style={btnGhost}>Cancel</button>
      <button onClick={onConfirm} disabled={loading} style={btnPrimary}>{loading ? 'Working…' : label}</button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Upload panel
// ---------------------------------------------------------------------------

function UploadPanel({ currentPath, onDone }: { currentPath: string; onDone: () => void }) {
  const [files, setFiles] = useState<File[]>([])
  const [progress, setProgress] = useState<Record<string, number>>({})
  const [done, setDone] = useState<Set<string>>(new Set())
  const inputRef = useRef<HTMLInputElement>(null)

  const CHUNK = 10 * 1024 * 1024 // 10 MB

  async function upload(file: File) {
    const totalChunks = Math.ceil(file.size / CHUNK)
    for (let i = 0; i < totalChunks; i++) {
      const start = i * CHUNK
      const end = Math.min(start + CHUNK, file.size)
      const chunk = file.slice(start, end)
      const fd = new FormData()
      fd.append('file', chunk, file.name)
      fd.append('filename', file.name)
      fd.append('path', `${currentPath.replace(/\/$/, '')}/${file.name}`)
      fd.append('fileSize', String(file.size))
      fd.append('chunk', String(i))
      fd.append('totalChunks', String(totalChunks))

      const headers: Record<string,string> = {}
      const sid = getSessionId(); if (sid) headers['X-Session-ID'] = sid
      const usr = getUsername(); if (usr) headers['X-User'] = usr
      headers['X-CSRF-Token'] = getCsrfToken()

      const res = await fetch('/api/files/upload', { method: 'POST', headers, body: fd })
      const data = await res.json()
      if (!data.success) throw new Error(data.error || 'Chunk upload failed')
      setProgress(p => ({ ...p, [file.name]: Math.round((end / file.size) * 100) }))
    }
    setDone(d => new Set([...d, file.name]))
  }

  async function startAll() {
    for (const file of files) {
      try { await upload(file) }
      catch (e: unknown) { toast.error(`${file.name}: ${(e as Error).message}`) }
    }
    onDone()
  }

  return (
    <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', padding: 16, marginBottom: 12 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: files.length ? 12 : 0 }}>
        <input ref={inputRef} type="file" multiple style={{ display: 'none' }}
          onChange={e => setFiles(f => [...f, ...Array.from(e.target.files ?? [])]) } />
        <button onClick={() => inputRef.current?.click()} style={btnGhost}><Icon name="upload_file" size={15} />Select Files</button>
        {files.length > 0 && <button onClick={startAll} style={btnPrimary}><Icon name="upload" size={15} />Upload {files.length} file{files.length > 1 ? 's' : ''}</button>}
      </div>
      {files.map(f => (
        <div key={f.name} style={{ display: 'flex', alignItems: 'center', gap: 10, marginTop: 8 }}>
          <Icon name="insert_drive_file" size={14} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
          <span style={{ flex: 1, fontSize: 'var(--text-xs)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{f.name}</span>
          <span style={{ fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)', flexShrink: 0 }}>{fmtSize(f.size)}</span>
          {progress[f.name] !== undefined && (
            <div style={{ width: 80, height: 4, background: 'var(--surface)', borderRadius: 999, overflow: 'hidden', flexShrink: 0 }}>
              <div style={{ height: '100%', width: `${progress[f.name]}%`, background: done.has(f.name) ? 'var(--success)' : 'var(--primary)', borderRadius: 999 }} />
            </div>
          )}
          {done.has(f.name) && <Icon name="check_circle" size={14} style={{ color: 'var(--success)', flexShrink: 0 }} />}
        </div>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// TrashTab
// ---------------------------------------------------------------------------

function TrashTab() {
  const qc = useQueryClient()
  const trashQ = useQuery({
    queryKey: ['trash', 'list'],
    queryFn: ({ signal }) => api.get<TrashListResponse>('/api/trash/list', signal),
  })
  const restore = useMutation({
    mutationFn: (name: string) => api.post('/api/trash/restore', { name }),
    onSuccess: () => { toast.success('Restored'); qc.invalidateQueries({ queryKey: ['trash', 'list'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const empty = useMutation({
    mutationFn: () => api.post('/api/trash/empty', {}),
    onSuccess: () => { toast.success('Trash emptied'); qc.invalidateQueries({ queryKey: ['trash', 'list'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  const items = trashQ.data?.items ?? []

  if (trashQ.isLoading) return <Skeleton height={200} />
  if (trashQ.isError) return <ErrorState error={trashQ.error} onRetry={() => qc.invalidateQueries({ queryKey: ['trash', 'list'] })} />

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{items.length} item{items.length !== 1 ? 's' : ''} in trash</span>
        {items.length > 0 && (
          <button onClick={() => empty.mutate()} disabled={empty.isPending} style={btnDanger}>
            <Icon name="delete_forever" size={14} />{empty.isPending ? 'Emptying…' : 'Empty Trash'}
          </button>
        )}
      </div>
      {items.length === 0 && (
        <div style={{ textAlign: 'center', padding: '48px 0', color: 'var(--text-tertiary)' }}>
          <Icon name="delete" size={40} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          Trash is empty
        </div>
      )}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        {items.map(item => (
          <div key={item.name} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '10px 14px', background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
            <Icon name="insert_drive_file" size={16} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{item.name}</div>
              {item.original_path && <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)' }}>{item.original_path}</div>}
            </div>
            {item.deleted_at && <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', flexShrink: 0 }}>{fmtDate(item.deleted_at)}</span>}
            <button onClick={() => restore.mutate(item.name)} style={btnGhost}><Icon name="restore" size={14} />Restore</button>
          </div>
        ))}
      </div>
    </>
  )
}

// ---------------------------------------------------------------------------
// FileBrowser (main tab)
// ---------------------------------------------------------------------------

function FileBrowser() {
  const qc = useQueryClient()
  const [path, setPath] = useState('/mnt')
  const [inputPath, setInputPath] = useState('/mnt')
  const [showUpload, setShowUpload] = useState(false)
  const [showMkdir, setShowMkdir] = useState(false)
  const [ctx, setCtx] = useState<CtxMenuState | null>(null)
  const [modal, setModal] = useState<{ type: string; entry: FileEntry } | null>(null)

  const filesQ = useQuery({
    queryKey: ['files', 'list', path],
    queryFn: ({ signal }) => api.get<FilesListResponse>(`/api/files/list?path=${encodeURIComponent(path)}`, signal),
  })

  const deleteMutation = useMutation({
    mutationFn: (p: string) => api.post('/api/files/delete', { path: p }),
    onSuccess: () => { toast.success('Deleted'); qc.invalidateQueries({ queryKey: ['files', 'list', path] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const trashMutation = useMutation({
    mutationFn: (p: string) => api.post('/api/trash/move', { path: p }),
    onSuccess: () => { toast.success('Moved to trash'); qc.invalidateQueries({ queryKey: ['files', 'list', path] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  function navigate(p: string) { setPath(p); setInputPath(p) }

  function upDir() {
    const parts = path.replace(/\/$/, '').split('/')
    if (parts.length <= 2) return
    navigate(parts.slice(0, -1).join('/') || '/')
  }

  const breadcrumbs = path.split('/').filter(Boolean)

  function handleCtx(e: React.MouseEvent, entry: FileEntry) {
    e.preventDefault()
    setCtx({ x: e.clientX, y: e.clientY, entry })
  }

  function handleAction(action: string, entry: FileEntry) {
    if (action === 'trash') { trashMutation.mutate(entry.path); return }
    if (action === 'delete') {
      if (window.confirm(`Permanently delete "${entry.name}"?`)) deleteMutation.mutate(entry.path)
      return
    }
    setModal({ type: action, entry })
  }

  function refresh() { qc.invalidateQueries({ queryKey: ['files', 'list', path] }) }

  const files = filesQ.data?.files ?? []
  const dirs = files.filter(f => f.is_dir).sort((a, b) => a.name.localeCompare(b.name))
  const regular = files.filter(f => !f.is_dir).sort((a, b) => a.name.localeCompare(b.name))
  const sorted = [...dirs, ...regular]

  return (
    <>
      {/* Toolbar */}
      <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 12 }}>
        <button onClick={upDir} style={btnGhost} title="Up" disabled={path === '/' || path === '/mnt'}>
          <Icon name="arrow_upward" size={15} />
        </button>
        <button onClick={refresh} style={btnGhost} title="Refresh"><Icon name="refresh" size={15} /></button>

        {/* Breadcrumb / path input */}
        <form onSubmit={e => { e.preventDefault(); navigate(inputPath) }} style={{ flex: 1 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 6, background: 'var(--surface)', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)', padding: '0 10px', height: 36 }}>
            <Icon name="folder" size={14} style={{ color: 'var(--text-tertiary)', flexShrink: 0 }} />
            {/* Breadcrumbs */}
            <div style={{ display: 'flex', alignItems: 'center', gap: 2, flexShrink: 0 }}>
              <button type="button" onClick={() => navigate('/')} style={{ background: 'none', border: 'none', cursor: 'pointer', color: 'var(--text-secondary)', fontSize: 'var(--text-sm)', padding: '0 2px' }}>/</button>
              {breadcrumbs.map((part, i) => {
                const p = '/' + breadcrumbs.slice(0, i + 1).join('/')
                return (
                  <span key={p} style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                    <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>/</span>
                    <button type="button" onClick={() => navigate(p)} style={{ background: 'none', border: 'none', cursor: 'pointer', color: i === breadcrumbs.length - 1 ? 'var(--text)' : 'var(--text-secondary)', fontSize: 'var(--text-sm)', fontWeight: i === breadcrumbs.length - 1 ? 600 : 400, padding: '0 2px' }}>
                      {part}
                    </button>
                  </span>
                )
              })}
            </div>
            <input value={inputPath} onChange={e => setInputPath(e.target.value)}
              style={{ flex: 1, background: 'none', border: 'none', outline: 'none', color: 'transparent', fontSize: 'var(--text-sm)', fontFamily: 'var(--font-mono)', caretColor: 'var(--text)' }}
              onFocus={e => (e.target.style.color = 'var(--text)')}
              onBlur={e => (e.target.style.color = 'transparent')}
            />
          </div>
        </form>

        <button onClick={() => setShowMkdir(true)} style={btnGhost}><Icon name="create_new_folder" size={15} />New Folder</button>
        <button onClick={() => setShowUpload(u => !u)} style={showUpload ? { ...btnGhost, borderColor: 'var(--primary)', color: 'var(--primary)' } : btnGhost}>
          <Icon name="upload" size={15} />Upload
        </button>
      </div>

      {/* Upload panel */}
      {showUpload && <UploadPanel currentPath={path} onDone={refresh} />}

      {/* File list */}
      {filesQ.isLoading && <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>{[0,1,2,3].map(i => <Skeleton key={i} height={44} style={{ borderRadius: 'var(--radius-sm)' }} />)}</div>}
      {filesQ.isError && <ErrorState error={filesQ.error} onRetry={refresh} />}
      {!filesQ.isLoading && !filesQ.isError && sorted.length === 0 && (
        <div style={{ textAlign: 'center', padding: '48px 0', color: 'var(--text-tertiary)' }}>
          <Icon name="folder_open" size={40} style={{ opacity: 0.3, display: 'block', margin: '0 auto 12px' }} />
          Empty directory
        </div>
      )}

      {sorted.length > 0 && (
        <div style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 'var(--radius-lg)', overflow: 'hidden' }}>
          <table style={{ width: '100%', borderCollapse: 'collapse' }}>
            <thead>
              <tr style={{ background: 'rgba(255,255,255,0.03)' }}>
                {['Name', 'Size', 'Owner', 'Mode', 'Modified'].map(h => (
                  <th key={h} style={{ padding: '9px 14px', textAlign: 'left', fontSize: 'var(--text-2xs)', fontWeight: 700, color: 'var(--text-tertiary)', textTransform: 'uppercase', letterSpacing: '0.5px', borderBottom: '1px solid var(--border)' }}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody>
              {sorted.map(entry => (
                <tr key={entry.path}
                  onDoubleClick={() => entry.is_dir && navigate(entry.path)}
                  onContextMenu={e => handleCtx(e, entry)}
                  style={{ borderBottom: '1px solid var(--border)', cursor: entry.is_dir ? 'pointer' : 'default', transition: 'background 0.1s' }}
                  onMouseEnter={e => (e.currentTarget.style.background = 'var(--bg-card-hover)')}
                  onMouseLeave={e => (e.currentTarget.style.background = 'transparent')}
                >
                  <td style={{ padding: '10px 14px' }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 9 }}>
                      <Icon name={fileIcon(entry)} size={17} style={{ color: entry.is_dir ? 'var(--primary)' : 'var(--text-tertiary)', flexShrink: 0 }} />
                      <span style={{ fontSize: 'var(--text-sm)', fontWeight: entry.is_dir ? 600 : 400 }}>{entry.name}</span>
                    </div>
                  </td>
                  <td style={{ padding: '10px 14px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', whiteSpace: 'nowrap' }}>
                    {entry.is_dir ? '—' : fmtSize(entry.size)}
                  </td>
                  <td style={{ padding: '10px 14px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', whiteSpace: 'nowrap' }}>
                    {entry.owner ?? '—'}{entry.group ? `:${entry.group}` : ''}
                  </td>
                  <td style={{ padding: '10px 14px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)' }}>
                    {entry.permissions ?? entry.mode ?? '—'}
                  </td>
                  <td style={{ padding: '10px 14px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', whiteSpace: 'nowrap' }}>
                    {fmtDate(entry.mtime)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {/* Context menu */}
      {ctx && <ContextMenu state={ctx} onClose={() => setCtx(null)} onAction={handleAction} />}

      {/* Modals */}
      {modal?.type === 'rename' && <RenameModal entry={modal.entry} onClose={() => setModal(null)} onDone={refresh} />}
      {modal?.type === 'chown'  && <ChownModal  entry={modal.entry} onClose={() => setModal(null)} onDone={refresh} />}
      {modal?.type === 'chmod'  && <ChmodModal  entry={modal.entry} onClose={() => setModal(null)} onDone={refresh} />}
      {modal?.type === 'copy'   && (
        <Modal title={`Copy — ${modal.entry.name}`} onClose={() => setModal(null)}>
          <CopyForm entry={modal.entry} onClose={() => setModal(null)} onDone={refresh} />
        </Modal>
      )}

      {/* Mkdir */}
      {showMkdir && <MkdirModal currentPath={path} onClose={() => setShowMkdir(false)} onDone={refresh} />}
    </>
  )
}

function CopyForm({ entry, onClose, onDone }: { entry: FileEntry; onClose: () => void; onDone: () => void }) {
  const [dest, setDest] = useState(entry.path + '_copy')
  const mutation = useMutation({
    mutationFn: () => api.post('/api/files/copy', { source: entry.path, destination: dest }),
    onSuccess: () => { toast.success('Copied'); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <>
      <label style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>Destination path</span>
        <input value={dest} onChange={e => setDest(e.target.value)} style={{ ...inputStyle, fontFamily: 'var(--font-mono)' }} autoFocus />
      </label>
      <ModalFooter onClose={onClose} onConfirm={() => mutation.mutate()} loading={mutation.isPending} label="Copy" />
    </>
  )
}

// ---------------------------------------------------------------------------
// FilesPage
// ---------------------------------------------------------------------------

type Tab = 'browser' | 'trash'

export function FilesPage() {
  const [tab, setTab] = useState<Tab>('browser')

  const TABS: { id: Tab; label: string; icon: string }[] = [
    { id: 'browser', label: 'File Browser', icon: 'folder_open' },
    { id: 'trash', label: 'Trash', icon: 'delete' },
  ]

  return (
    <div style={{ maxWidth: 1100 }}>
      <div style={{ marginBottom: 28 }}>
        <h1 style={{ fontSize: 'var(--text-3xl)', fontWeight: 700, letterSpacing: '-1px', marginBottom: 6 }}>Files</h1>
        <p style={{ color: 'var(--text-secondary)', fontSize: 'var(--text-md)' }}>Browse, upload, rename, manage permissions</p>
      </div>

      <div style={{ display: 'flex', gap: 4, marginBottom: 24, borderBottom: '1px solid var(--border)' }}>
        {TABS.map(t => (
          <button key={t.id} onClick={() => setTab(t.id)} style={{ padding: '10px 20px', background: 'none', border: 'none', cursor: 'pointer', fontSize: 'var(--text-sm)', fontWeight: 600, color: tab === t.id ? 'var(--primary)' : 'var(--text-secondary)', borderBottom: tab === t.id ? '2px solid var(--primary)' : '2px solid transparent', marginBottom: -1, display: 'flex', alignItems: 'center', gap: 6 }}>
            <Icon name={t.icon} size={16} />{t.label}
          </button>
        ))}
      </div>

      {tab === 'browser' && <FileBrowser />}
      {tab === 'trash'   && <TrashTab />}
    </div>
  )
}
