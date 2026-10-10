/**
 * CustomIconsButton - upload icons for the dplaneos.icon compose label.
 *
 *   GET  /api/assets/custom-icons/list → { icons: string[] }
 *   POST /api/assets/custom-icons      (multipart: file)
 */
import { useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api, getSessionId, getUsername, getCsrfToken } from '@/lib/api'
import { Modal } from '@/components/ui/Modal'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

export function CustomIconsButton() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const input = useRef<HTMLInputElement>(null)
  const iconsQ = useQuery({
    queryKey: ['docker', 'custom-icons'],
    queryFn: ({ signal }) => api.get<{ success: boolean; icons: string[] }>('/api/assets/custom-icons/list', signal),
    enabled: open,
  })

  async function upload(file: File) {
    setBusy(true)
    try {
      const fd = new FormData()
      fd.append('file', file, file.name)
      const headers: Record<string, string> = { 'X-CSRF-Token': getCsrfToken() }
      const sid = getSessionId(); if (sid) headers['X-Session-ID'] = sid
      const usr = getUsername(); if (usr) headers['X-User'] = usr
      const res = await fetch('/api/assets/custom-icons', { method: 'POST', headers, body: fd })
      const data = await res.json().catch(() => ({}))
      if (!res.ok || !data.success) throw new Error(data.error || `Upload failed (HTTP ${res.status})`)
      toast.success(`Icon ${data.name} uploaded: use dplaneos.icon: ${data.name}`)
      qc.invalidateQueries({ queryKey: ['docker', 'custom-icons'] })
    } catch (e) {
      toast.error((e as Error).message)
    } finally {
      setBusy(false)
      if (input.current) input.current.value = ''
    }
  }

  return (
    <>
      <button className="btn btn-ghost btn-sm" onClick={() => setOpen(true)}><Icon name="image" size={14} />App icons</button>
      {open && (
        <Modal title="App icons" onClose={() => setOpen(false)}>
          <p style={{ margin: 0, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            Upload an SVG, PNG or WebP (max 1 MB), then set <code>dplaneos.icon: name.svg</code> as a label of the service in its compose file.
          </p>
          <input ref={input} type="file" accept=".svg,.png,.webp" style={{ display: 'none' }}
            onChange={e => { const f = e.target.files?.[0]; if (f) void upload(f) }} />
          <button className="btn btn-primary" disabled={busy} onClick={() => input.current?.click()}>
            <Icon name="upload" size={14} />{busy ? 'Uploading…' : 'Upload icon'}
          </button>
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10 }}>
            {(iconsQ.data?.icons ?? []).map(n => (
              <div key={n} style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 4, width: 84 }}>
                <img src={`/api/assets/custom-icons/${encodeURIComponent(n)}`} alt={n} style={{ width: 40, height: 40, objectFit: 'contain' }} />
                <code style={{ fontSize: 'var(--text-2xs)', wordBreak: 'break-all', textAlign: 'center' }}>{n}</code>
              </div>
            ))}
            {iconsQ.data && iconsQ.data.icons.length === 0 && <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>No custom icons yet.</span>}
          </div>
        </Modal>
      )}
    </>
  )
}
