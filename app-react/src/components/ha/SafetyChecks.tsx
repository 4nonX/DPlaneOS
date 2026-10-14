/**
 * SafetyChecks - test the parts failover depends on (Design 0001 section
 * 5.10, phase 4), without changing anything: the third vote, power fencing
 * (BMC status query; PDU reachability) and, per replicated group, the copies
 * (in the storage group list). Disk reservations have their own probe in the
 * fencing settings.
 *
 * Calls: POST /api/cluster/test/{qdevice|fencing|pdu}
 */

import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

type Kind = 'qdevice' | 'fencing' | 'pdu'
interface Result { ok: boolean; detail: string }

const TESTS: { kind: Kind; label: string; what: string }[] = [
  { kind: 'qdevice', label: 'Third vote (QDevice)', what: 'Connects to the QDevice on TCP 5403 and checks that it votes for this node.' },
  { kind: 'fencing', label: 'Power fencing: BMC', what: 'Asks the other node\'s BMC for its power state with the stored credentials. Nothing is switched.' },
  { kind: 'pdu', label: 'Power fencing: PDU', what: 'Checks that the PDU answers. The outlet URL itself is not called: that would cut the power.' },
]

export function SafetyChecks() {
  const [results, setResults] = useState<Partial<Record<Kind, Result>>>({})
  const run = useMutation({
    mutationFn: async (kind: Kind) => ({ kind, r: ensureOk(await api.post<{ success: boolean; error?: string; result: Result }>(`/api/cluster/test/${kind}`, {})).result }),
    onSuccess: ({ kind, r }) => setResults(prev => ({ ...prev, [kind]: r })),
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div className="card" style={{ marginBottom: 20 }}>
      <h3 style={{ marginTop: 0 }}>Safety checks</h3>
      <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        Check from this node that what failover relies on actually works. None of these tests changes anything. Run them on every node.
      </p>
      <table className="data-table" style={{ fontSize: 'var(--text-sm)' }}>
        <tbody>
          {TESTS.map(t => {
            const r = results[t.kind]
            return (
              <tr key={t.kind}>
                <td style={{ verticalAlign: 'top', whiteSpace: 'nowrap' }}>
                  <button className="btn btn-ghost btn-sm" disabled={run.isPending && run.variables === t.kind} onClick={() => run.mutate(t.kind)}>
                    <Icon name="play_circle" size={14} />{t.label}
                  </button>
                </td>
                <td>
                  <div style={{ color: 'var(--text-tertiary)' }}>{t.what}</div>
                  {r && (
                    <div role="status" style={{ color: r.ok ? 'var(--success)' : 'var(--error)', marginTop: 4 }}>
                      <Icon name={r.ok ? 'check_circle' : 'error'} size={14} /> {r.detail}
                    </div>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', marginBottom: 0 }}>
        Replication is tested per group (Storage groups › Test replication); disk reservations with the probe under Watchdog and power fencing.
      </p>
    </div>
  )
}
