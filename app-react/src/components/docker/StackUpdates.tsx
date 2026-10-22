/**
 * components/docker/StackUpdates.tsx - image update status, update settings
 * and last update result of compose stacks.
 *
 * Calls:
 *   GET  /api/docker/stacks/updates        → { stacks: {name: StackUpdates}, settings: {name: StackSettings} }
 *   POST /api/docker/stacks/updates/check  → { job_id }
 *   PUT  /api/docker/stacks/settings       { name, auto_update, snapshot, rollback_data }
 */

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

interface ServiceUpdate {
  service: string
  image: string
  update_available: boolean
  remote_digest?: string
  failed_before?: boolean
  note?: string
}
interface StackUpdateResult {
  updated?: string[]
  up_to_date?: boolean
  rolled_back?: boolean
  snapshots?: string[]
  error?: string
}
interface StackUpdates {
  checked_at?: string
  update_available?: boolean
  services?: ServiceUpdate[]
  last_update?: { at: string; result: StackUpdateResult }
}
interface StackSettings { auto_update?: boolean; snapshot?: boolean; rollback_data?: boolean }
interface UpdatesResponse {
  success: boolean
  stacks: Record<string, StackUpdates>
  settings: Record<string, StackSettings>
}

export function useStackUpdates() {
  return useQuery({
    queryKey: ['docker', 'stacks', 'updates'],
    queryFn: ({ signal }) => api.get<UpdatesResponse>('/api/docker/stacks/updates', signal),
    refetchInterval: 60_000,
  })
}

/** A small "update" mark for the stack list. */
export function StackUpdateBadge({ name }: { name: string }) {
  const q = useStackUpdates()
  const su = q.data?.stacks?.[name]
  const rolledBack = su?.last_update?.result?.rolled_back
  if (rolledBack) {
    return <span title="The last update failed and was rolled back" style={{ color: 'var(--warning)', fontSize: 'var(--text-2xs)', fontWeight: 700 }}>ROLLED BACK</span>
  }
  if (!su?.update_available) return null
  return <span title="New images are available" style={{ color: 'var(--primary)', fontSize: 'var(--text-2xs)', fontWeight: 700 }}>UPDATE</span>
}

/** Update status, settings and last result of one stack. */
export function StackUpdatePanel({ name }: { name: string }) {
  const qc = useQueryClient()
  const q = useStackUpdates()
  const su = q.data?.stacks?.[name]
  const settings: StackSettings = q.data?.settings?.[name] ?? {}
  const snapshot = settings.snapshot ?? true

  const check = useMutation({
    mutationFn: () => api.post<{ success: boolean; job_id: string }>('/api/docker/stacks/updates/check', {}).then(ensureOk),
    onSuccess: () => {
      toast.success('Checking the registries…')
      setTimeout(() => qc.invalidateQueries({ queryKey: ['docker', 'stacks', 'updates'] }), 5000)
    },
    onError: (e: Error) => toast.error(e.message),
  })
  const key = ['docker', 'stacks', 'updates']
  const save = useMutation({
    mutationFn: (s: StackSettings) =>
      api.put<{ success: boolean; error?: string }>('/api/docker/stacks/settings', { name, ...s }).then(ensureOk),
    // The toggle shows the new value at once; an error puts the old one back.
    onMutate: (s: StackSettings) => {
      const prev = qc.getQueryData<UpdatesResponse>(key)
      if (prev) qc.setQueryData<UpdatesResponse>(key, { ...prev, settings: { ...prev.settings, [name]: s } })
      return { prev }
    },
    onError: (e: Error, _s, ctx) => {
      if (ctx?.prev) qc.setQueryData(key, ctx.prev)
      toast.error(e.message)
    },
    onSettled: () => qc.invalidateQueries({ queryKey: key }),
  })
  const set = (patch: StackSettings) => save.mutate({ auto_update: !!settings.auto_update, snapshot, rollback_data: !!settings.rollback_data, ...patch })

  const last = su?.last_update
  return (
    <div style={{ padding: '10px 20px', borderBottom: '1px solid var(--border)', fontSize: 'var(--text-xs)', display: 'flex', flexDirection: 'column', gap: 8 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
        <span style={{ fontWeight: 700, display: 'flex', alignItems: 'center', gap: 5 }}>
          <Icon name="system_update" size={13} />
          {su?.update_available ? <span style={{ color: 'var(--primary)' }}>Updates available</span>
            : su?.checked_at ? 'Images up to date' : 'Not checked yet'}
        </span>
        {su?.checked_at && <span style={{ color: 'var(--text-tertiary)' }}>checked {new Date(su.checked_at).toLocaleString()}</span>}
        <button onClick={() => check.mutate()} disabled={check.isPending} className="btn btn-ghost" style={{ padding: '2px 8px', fontSize: 'var(--text-xs)' }}>
          <Icon name="refresh" size={12} />Check now
        </button>
        <span style={{ flex: 1 }} />
        <label style={{ display: 'flex', alignItems: 'center', gap: 5, cursor: 'pointer' }} title="Update automatically when the registry has new images (checked every 6 hours); a failed update is rolled back and that image is not tried again">
          <input type="checkbox" checked={!!settings.auto_update} onChange={e => set({ auto_update: e.target.checked })} />Auto-update
        </label>
        <label style={{ display: 'flex', alignItems: 'center', gap: 5, cursor: 'pointer' }} title="Snapshot the ZFS datasets this stack mounts before each update">
          <input type="checkbox" checked={snapshot} onChange={e => set({ snapshot: e.target.checked })} />Snapshot first
        </label>
        <label style={{ display: 'flex', alignItems: 'center', gap: 5, cursor: 'pointer' }} title="When an update fails, also roll the data back to the snapshot (what the app wrote since the update is lost)">
          <input type="checkbox" checked={!!settings.rollback_data} disabled={!snapshot} onChange={e => set({ rollback_data: e.target.checked })} />Roll data back on failure
        </label>
      </div>

      {su?.services && su.services.some(s => s.update_available || s.note) && (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2, color: 'var(--text-secondary)' }}>
          {su.services.filter(s => s.update_available || s.note).map(s => (
            <div key={s.service} style={{ fontFamily: 'var(--font-mono)' }}>
              {s.service}: {s.image}{' '}
              {s.update_available && !s.failed_before && <span style={{ color: 'var(--primary)' }}>new image</span>}
              {s.failed_before && <span style={{ color: 'var(--warning)' }}>new image failed before (not auto-updated)</span>}
              {s.note && <span style={{ color: 'var(--text-tertiary)' }}> · {s.note}</span>}
            </div>
          ))}
        </div>
      )}

      {last && (
        <div style={{ color: last.result.error ? (last.result.rolled_back ? 'var(--warning)' : 'var(--error)') : 'var(--text-tertiary)' }}>
          Last update {new Date(last.at).toLocaleString()}:{' '}
          {last.result.error
            ? (last.result.rolled_back ? `failed and rolled back to the previous images (${last.result.error})` : `failed: ${last.result.error}`)
            : last.result.up_to_date ? 'nothing new' : `updated ${(last.result.updated ?? []).join(', ')}`}
          {last.result.snapshots?.length ? <> · snapshot {last.result.snapshots.join(', ')}</> : null}
        </div>
      )}
    </div>
  )
}
