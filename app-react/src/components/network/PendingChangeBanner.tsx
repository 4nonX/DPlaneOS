/**
 * components/network/PendingChangeBanner.tsx - a network or firewall change
 * that waits for confirmation.
 *
 * Changes apply at once and are reverted unless confirmed within the window.
 * The state comes from the server, so the banner also shows after opening the
 * UI on a new address.
 *
 * Calls:
 *   GET  /api/network/pending → { pending, change, seconds_left }
 *   POST /api/network/confirm
 *   POST /api/network/revert
 */

import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

interface PendingResponse { success: boolean; pending: boolean; change?: string; seconds_left?: number }

export const pendingKey = ['network', 'pending']

/** Call after a change was applied: the banner appears at once. */
export function useChangeApplied() {
  const qc = useQueryClient()
  return (message?: string) => {
    toast.success(message ?? 'Applied - confirm to keep it')
    qc.invalidateQueries({ queryKey: pendingKey })
  }
}

export function PendingChangeBanner({ onSettled }: { onSettled?: () => void }) {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: pendingKey,
    queryFn: ({ signal }) => api.get<PendingResponse>('/api/network/pending', signal),
    refetchInterval: d => (d.state.data?.pending ? 3_000 : 15_000),
  })
  // Seconds left, counted down locally between polls.
  const deadline = q.data?.pending ? q.dataUpdatedAt + (q.data.seconds_left ?? 0) * 1000 : null
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    if (deadline === null) return
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [deadline])
  const left = deadline === null ? null : Math.max(0, Math.round((deadline - now) / 1000))

  const done = () => {
    qc.invalidateQueries({ queryKey: pendingKey })
    qc.invalidateQueries({ queryKey: ['network'] })
    qc.invalidateQueries({ queryKey: ['firewall'] })
    onSettled?.()
  }
  const keep = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success?: boolean; error?: string }>('/api/network/confirm', {})),
    onSuccess: () => { toast.success('Change kept'); done() },
    onError: (e: Error) => { toast.error(e.message); done() },
  })
  const revert = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success?: boolean; error?: string }>('/api/network/revert', {})),
    onSuccess: () => { toast.success('Change reverted'); done() },
    onError: (e: Error) => { toast.error(e.message); done() },
  })

  if (!q.data?.pending) return null
  return (
    <div role="alert" style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '14px 20px', background: 'var(--warning-bg)', border: '1px solid var(--warning-border)', borderRadius: 'var(--radius-lg)', marginBottom: 20 }}>
      <Icon name="timer" size={22} style={{ color: 'var(--warning)', flexShrink: 0 }} />
      <div style={{ flex: 1 }}>
        <div style={{ fontWeight: 700, color: 'var(--warning)' }}>Confirm the change: {q.data.change}</div>
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
          It is reverted in {left ?? q.data.seconds_left}s unless you keep it. If the address changed, open the interface on the new address and confirm there.
        </div>
      </div>
      <button onClick={() => keep.mutate()} disabled={keep.isPending || revert.isPending} className="btn btn-primary" style={{ background: 'var(--warning)' }}>
        <Icon name="check" size={15} />Keep
      </button>
      <button onClick={() => revert.mutate()} disabled={keep.isPending || revert.isPending} className="btn btn-ghost">Revert now</button>
    </div>
  )
}
