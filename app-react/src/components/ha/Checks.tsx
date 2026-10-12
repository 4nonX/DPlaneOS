/**
 * Checks - "does it work now?" buttons of the High Availability page
 * (Design 0001, 5.10: safety checks). Each asks the system to try the thing
 * and shows what came back; none of them changes anything.
 *
 * Calls: POST /api/quorum/third-vote/test, POST /api/groups/{name}/replicate/check
 */

import { useMutation } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'

export interface CheckLine { name: string; ok: boolean; detail: string }

/** The findings of a check, one per line. */
export function CheckResult({ lines, error }: { lines?: CheckLine[]; error?: string }) {
  if (error) {
    return <div role="status" className="alert alert-error" style={{ fontSize: 'var(--text-sm)', marginTop: 8 }}><Icon name="error" size={16} /><span>{error}</span></div>
  }
  if (!lines) return null
  return (
    <ul role="status" style={{ listStyle: 'none', padding: 0, margin: '8px 0 0', fontSize: 'var(--text-sm)' }}>
      {lines.map(l => (
        <li key={l.name + l.detail} style={{ display: 'flex', gap: 8, alignItems: 'flex-start', padding: '3px 0' }}>
          <Icon name={l.ok ? 'check_circle' : 'error'} size={16} style={{ color: l.ok ? 'var(--success)' : 'var(--error)', flexShrink: 0, marginTop: 2 }} />
          <span><strong>{l.name}:</strong> {l.detail}</span>
        </li>
      ))}
    </ul>
  )
}

interface ThirdVoteAnswer { success: boolean; ok: boolean; kind: string; steps: CheckLine[] }

export function useThirdVoteTest() {
  return useMutation({
    mutationFn: () => api.post<ThirdVoteAnswer>('/api/quorum/third-vote/test', {}),
  })
}

/** Button and result: is the third vote reachable and voting? */
export function ThirdVoteTest() {
  const test = useThirdVoteTest()
  return (
    <div>
      <button className="btn btn-ghost" onClick={() => test.mutate()} disabled={test.isPending}
        title="Checks now that the vote which allows automatic failover is reachable and voting">
        <Icon name="fact_check" size={16} />{test.isPending ? 'Testing…' : 'Test the third vote'}
      </button>
      <CheckResult lines={test.data?.steps} error={(test.error as Error | null)?.message} />
    </div>
  )
}

interface ReplCheckRow { target: string; target_name: string; pool: string; ok: boolean; mode?: string; base?: string; detail: string }
interface ReplCheckAnswer { success: boolean; ok: boolean; error?: string; checks: ReplCheckRow[] }

export function useReplicationCheck() {
  return useMutation({
    mutationFn: async (group: string) => ensureOk(await api.post<ReplCheckAnswer>(`/api/groups/${encodeURIComponent(group)}/replicate/check`, {})),
  })
}

export function replicationLines(a?: ReplCheckAnswer): CheckLine[] | undefined {
  return a?.checks.map(c => ({ name: `${c.pool} → ${c.target_name}`, ok: c.ok, detail: c.detail }))
}
