/**
 * EnclosuresSection - drive bays of SES enclosures (JBODs, backplanes) with
 * their locate LEDs. Renders nothing on machines without an enclosure.
 *
 *   GET /api/enclosure                              → { enclosures: Enclosure[] }
 *   PUT /api/enclosure/{id}/slot/{index}/locate     { locate } → { ok, locate }
 *   GET /api/enclosure/{id}/ses-status              → { device, elements: [{type, descriptor, status, details?}] }
 */
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { toast } from '@/hooks/useToast'

interface Slot { index: number; name: string; status: string; locate: boolean; fault: boolean; type?: string; device?: string }
interface Enclosure { id: string; sys_path: string; slots: Slot[] }

interface SESElement { type: string; descriptor: string; status: string; details?: string }

/** Everything the enclosure reports about itself: power supplies, fans, temperatures, bays. */
function SESModal({ id, onClose }: { id: string; onClose: () => void }) {
  const q = useQuery({
    queryKey: ['hardware', 'enclosure-ses', id],
    queryFn: ({ signal }) => api.get<{ device: string; elements: SESElement[] }>(`/api/enclosure/${encodeURIComponent(id)}/ses-status`, signal),
    retry: false,
  })
  const elements = q.data?.elements ?? []
  const bad = (st: string) => !/^(ok|not installed|unsupported)$/i.test(st.trim())
  return (
    <Modal title={`Enclosure ${id}`} onClose={onClose} size="lg">
      {q.isLoading && <Skeleton height={160} />}
      {q.isError && <div style={{ color: 'var(--error)', fontSize: 'var(--text-sm)' }}>{(q.error as Error).message}</div>}
      {q.data && (
        <>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Read from {q.data.device} (SCSI Enclosure Services)</div>
          <div style={{ maxHeight: '60vh', overflowY: 'auto' }}>
            <table className="table" style={{ width: '100%' }}>
              <thead><tr><th>Element</th><th>Name</th><th>Status</th><th>Details</th></tr></thead>
              <tbody>
                {elements.map((e, i) => (
                  <tr key={i}>
                    <td>{e.type}</td>
                    <td>{e.descriptor || '-'}</td>
                    <td style={{ fontWeight: 600, color: bad(e.status) ? 'var(--warning)' : undefined }}>{e.status}</td>
                    <td style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>{e.details || ''}</td>
                  </tr>
                ))}
                {elements.length === 0 && <tr><td colSpan={4} style={{ textAlign: 'center', color: 'var(--text-tertiary)' }}>The enclosure reports no elements.</td></tr>}
              </tbody>
            </table>
          </div>
        </>
      )}
    </Modal>
  )
}

export function EnclosuresSection() {
  const qc = useQueryClient()
  const [ses, setSes] = useState<string | null>(null)
  const q = useQuery({
    queryKey: ['hardware', 'enclosures'],
    queryFn: ({ signal }) => api.get<{ enclosures: Enclosure[] }>('/api/enclosure', signal),
    refetchInterval: 30_000,
  })
  const locate = useMutation({
    mutationFn: (v: { enc: string; slot: Slot; on: boolean }) =>
      api.put<{ ok: boolean; locate: boolean }>(`/api/enclosure/${encodeURIComponent(v.enc)}/slot/${v.slot.index}/locate`, { locate: v.on }),
    onSuccess: (_r, v) => {
      toast.success(`Locate LED of ${v.slot.name} ${v.on ? 'on' : 'off'}`)
      qc.invalidateQueries({ queryKey: ['hardware', 'enclosures'] })
    },
    onError: (e: Error) => toast.error(e.message),
  })

  const enclosures = q.data?.enclosures ?? []
  if (enclosures.length === 0) return null

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 24, marginTop: 24 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 16 }}>
        <Icon name="dns" size={18} style={{ color: 'var(--primary)' }} />
        <span style={{ fontWeight: 700, fontSize: 'var(--text-md)' }}>Drive enclosures</span>
      </div>
      {enclosures.map(enc => (
        <div key={enc.id} style={{ marginBottom: 16 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 8 }}>
            <span style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
              Enclosure {enc.id} · {enc.slots.length} bays
            </span>
            <button className="btn btn-xs btn-ghost" style={{ marginLeft: 'auto' }} onClick={() => setSes(enc.id)}
              title="Power supplies, fans and temperatures as the enclosure reports them">
              <Icon name="monitor_heart" size={14} />Enclosure status
            </button>
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))', gap: 8 }}>
            {enc.slots.map(slot => {
              const empty = !slot.device
              const color = slot.fault ? 'var(--error)' : slot.locate ? 'var(--primary)' : empty ? 'var(--text-tertiary)' : 'var(--success)'
              return (
                <div key={slot.index} style={{ border: `1px solid ${slot.fault || slot.locate ? color : 'var(--border)'}`, borderRadius: 'var(--radius-md)', padding: '8px 10px', display: 'flex', flexDirection: 'column', gap: 4 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <div style={{ width: 8, height: 8, borderRadius: '50%', background: color, flexShrink: 0 }} />
                    <span style={{ fontWeight: 600, fontSize: 'var(--text-sm)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{slot.name}</span>
                  </div>
                  <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                    {slot.device ? `/dev/${slot.device}` : 'empty'}{slot.fault ? ' · fault' : ''}
                  </div>
                  <button className="btn btn-xs btn-ghost" disabled={locate.isPending}
                    style={{ justifyContent: 'flex-start', color: slot.locate ? 'var(--primary)' : undefined }}
                    onClick={() => locate.mutate({ enc: enc.id, slot, on: !slot.locate })}>
                    <Icon name={slot.locate ? 'lightbulb' : 'light_off'} size={14} />{slot.locate ? 'Locating: turn off' : 'Locate'}
                  </button>
                </div>
              )
            })}
          </div>
        </div>
      ))}
      {ses && <SESModal id={ses} onClose={() => setSes(null)} />}
    </div>
  )
}
