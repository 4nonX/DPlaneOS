/**
 * EnclosuresSection - drive bays of SES enclosures (JBODs, backplanes) with
 * their locate LEDs. Renders nothing on machines without an enclosure.
 *
 *   GET /api/enclosure                              → { enclosures: Enclosure[] }
 *   PUT /api/enclosure/{id}/slot/{index}/locate     { locate } → { ok, locate }
 */
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

interface Slot { index: number; name: string; status: string; locate: boolean; fault: boolean; type?: string; device?: string }
interface Enclosure { id: string; sys_path: string; slots: Slot[] }

export function EnclosuresSection() {
  const qc = useQueryClient()
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
          <div style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 8 }}>
            Enclosure {enc.id} · {enc.slots.length} bays
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
    </div>
  )
}
