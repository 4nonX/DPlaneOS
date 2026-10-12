/**
 * PowerTargets - power fencing, one entry per node (ADR-0009).
 *
 * Before this node takes a storage group over from a node that stopped
 * answering, it can cut that node's power: through its BMC (IPMI or
 * Redfish) or a switched PDU. Each other node has its own entry here, and
 * each entry has a test that signs in and reads the state without switching
 * anything.
 *
 * Calls: GET /api/ha/power-targets, PUT/DELETE /api/ha/power-targets/{node},
 * POST /api/ha/power-targets/{node}/test, POST /api/ha/power-targets/{node}/enroll,
 * GET/POST /api/ha/fencing/configure (the random wait, the disk tolerance)
 */

import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

interface PowerTarget {
  node: string
  name: string
  unknown_node?: boolean
  kind: 'bmc' | 'pdu'
  enabled: boolean
  host: string
  username: string
  has_password: boolean
  tls_fingerprint: string
  off_url: string
  status_url: string
  http_method: string
  expected_status: number
  timeout_secs: number
}

interface TargetsResponse {
  success: boolean
  targets: PowerTarget[]
  nodes_without: { node: string; name: string }[]
}

interface TestResult { ok: boolean; power_state?: string; detail: string; confirms: string }

const EMPTY = (node: string, name: string): PowerTarget => ({
  node, name, kind: 'bmc', enabled: false, host: '', username: '', has_password: false, tls_fingerprint: '',
  off_url: '', status_url: '', http_method: 'GET', expected_status: 0, timeout_secs: 10,
})

function TargetForm({ initial, isNew, onDone }: { initial: PowerTarget; isNew: boolean; onDone: () => void }) {
  const qc = useQueryClient()
  const [t, setT] = useState<PowerTarget>(initial)
  const [password, setPassword] = useState('')
  const [result, setResult] = useState<TestResult | null>(null)
  const set = <K extends keyof PowerTarget>(k: K, v: PowerTarget[K]) => { setT(p => ({ ...p, [k]: v })); setResult(null) }
  const refresh = () => { qc.invalidateQueries({ queryKey: ['ha', 'power-targets'] }); qc.invalidateQueries({ queryKey: ['ha'] }) }
  const path = `/api/ha/power-targets/${encodeURIComponent(t.node)}`

  const save = useMutation({
    mutationFn: async () => ensureOk(await api.put<{ success: boolean; error?: string }>(path, {
      ...t, ...(password !== '' ? { password } : {}),
    })),
    onSuccess: () => { toast.success(`Saved for ${t.name}. Use Test to see that it works.`); setPassword(''); refresh(); if (isNew) onDone() },
    onError: (e: Error) => toast.error(e.message),
  })
  const test = useMutation({
    mutationFn: () => api.post<{ success: boolean; result: TestResult }>(`${path}/test`, {}),
    onSuccess: (r) => setResult(r.result),
    onError: (e: Error) => setResult({ ok: false, detail: e.message, confirms: '' }),
  })
  const pin = useMutation({
    mutationFn: async (reset: boolean) => ensureOk(await api.post<{ success: boolean; error?: string; fingerprint?: string }>(`${path}/enroll`, { reset })),
    onSuccess: (_r, reset) => { toast.success(reset ? 'Certificate forgotten' : 'Certificate pinned'); refresh() },
    onError: (e: Error) => toast.error(e.message),
  })
  const remove = useMutation({
    mutationFn: () => api.delete(path),
    onSuccess: () => { toast.success(`Power fencing removed for ${t.name}`); refresh(); onDone() },
    onError: (e: Error) => toast.error(e.message),
  })

  const dirty = JSON.stringify(t) !== JSON.stringify(initial) || password !== ''
  return (
    <div style={{ border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', padding: '14px 16px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 12, flexWrap: 'wrap' }}>
        <Icon name="dns" size={18} style={{ color: t.enabled ? 'var(--success)' : 'var(--text-tertiary)' }} />
        <strong>{t.name}</strong>
        {t.unknown_node && <span className="badge badge-warning">no longer paired with this node</span>}
        <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
          how <em>this</em> node switches <strong>{t.name}</strong> off
        </span>
        <label style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 6, fontSize: 'var(--text-sm)' }}>
          <input type="checkbox" checked={t.enabled} onChange={e => set('enabled', e.target.checked)} />
          Use before a takeover
        </label>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '140px 1fr 1fr 1fr', gap: 12, marginBottom: 12 }}>
        <label className="field">
          <span className="field-label">Through</span>
          <select value={t.kind} onChange={e => set('kind', e.target.value as 'bmc' | 'pdu')} className="input">
            <option value="bmc">its BMC</option>
            <option value="pdu">a switched PDU</option>
          </select>
        </label>
        {t.kind === 'bmc' ? (
          <label className="field">
            <span className="field-label">BMC address of {t.name}</span>
            <input value={t.host} onChange={e => set('host', e.target.value)} placeholder="10.0.0.12" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
          </label>
        ) : (
          <label className="field">
            <span className="field-label">Address that switches its outlet off</span>
            <input value={t.off_url} onChange={e => set('off_url', e.target.value)} placeholder="https://pdu.lan/outlet/3/off" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
          </label>
        )}
        <label className="field">
          <span className="field-label">User name</span>
          <input value={t.username} onChange={e => set('username', e.target.value)} autoComplete="off" className="input" />
        </label>
        <label className="field">
          <span className="field-label">Password</span>
          <input type="password" value={password} onChange={e => { setPassword(e.target.value); setResult(null) }} autoComplete="new-password"
            placeholder={t.has_password ? 'stored; leave empty to keep' : ''} className="input" />
        </label>
      </div>

      {t.kind === 'pdu' && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 110px 130px 110px', gap: 12, marginBottom: 12 }}>
          <label className="field">
            <span className="field-label">Status address (for the test; switches nothing)</span>
            <input value={t.status_url} onChange={e => set('status_url', e.target.value)} placeholder="https://pdu.lan/outlet/3" className="input" style={{ fontFamily: 'var(--font-mono)' }} />
          </label>
          <label className="field">
            <span className="field-label">Method</span>
            <select value={t.http_method} onChange={e => set('http_method', e.target.value)} className="input">
              <option>GET</option><option>POST</option>
            </select>
          </label>
          <label className="field">
            <span className="field-label">Expected answer</span>
            <input type="number" min={0} max={599} value={t.expected_status} onChange={e => set('expected_status', parseInt(e.target.value) || 0)} className="input" title="0: any 2xx" />
          </label>
          <label className="field">
            <span className="field-label">Timeout (s)</span>
            <input type="number" min={1} max={120} value={t.timeout_secs} onChange={e => set('timeout_secs', parseInt(e.target.value) || 10)} className="input" />
          </label>
        </div>
      )}

      {t.kind === 'bmc' && !isNew && (
        <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', margin: '0 0 12px' }}>
          {t.tls_fingerprint
            ? <>Redfish with a pinned certificate ({t.tls_fingerprint.slice(0, 23)}…): sign-in and power-off go only to the machine that showed it.{' '}
                <button className="btn btn-ghost" style={{ padding: 0, fontSize: 'var(--text-xs)' }} onClick={() => pin.mutate(true)} disabled={pin.isPending}>Forget it</button> (after a BMC firmware update)</>
            : <>Used over IPMI. If the BMC speaks Redfish,{' '}
                <button className="btn btn-ghost" style={{ padding: 0, fontSize: 'var(--text-xs)' }} onClick={() => pin.mutate(false)} disabled={pin.isPending || dirty}>pin its certificate</button>{' '}
                to use that instead.</>}
        </p>
      )}

      {result && (
        <div role="status" className={`alert ${result.ok ? 'alert-success' : 'alert-error'}`} style={{ marginBottom: 12, fontSize: 'var(--text-sm)' }}>
          <Icon name={result.ok ? 'check_circle' : 'error'} size={16} />
          <span>{result.detail}</span>
        </div>
      )}

      <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
        <button onClick={() => save.mutate()} disabled={save.isPending || !dirty} className="btn btn-primary">
          <Icon name="save" size={15} />{save.isPending ? 'Saving…' : 'Save'}
        </button>
        {!isNew && (
          <button onClick={() => test.mutate()} disabled={test.isPending || dirty} className="btn btn-ghost"
            title={dirty ? 'Save first: the test uses what is stored' : 'Signs in and reads the state. Nothing is switched.'}>
            <Icon name="fact_check" size={15} />{test.isPending ? 'Testing…' : 'Test'}
          </button>
        )}
        {isNew
          ? <button onClick={onDone} className="btn btn-ghost">Cancel</button>
          : <button onClick={() => remove.mutate()} disabled={remove.isPending} className="btn btn-ghost" style={{ marginLeft: 'auto', color: 'var(--error)' }}>Remove</button>}
      </div>
    </div>
  )
}

function FencingTuning() {
  const qc = useQueryClient()
  const q = useQuery({
    queryKey: ['ha', 'fencing'],
    queryFn: ({ signal }) => api.get<{ success: boolean; config: { jitter_max_ms: number; disk_fault_tolerance_pct: number } }>('/api/ha/fencing/configure', signal),
  })
  const [dirty, setDirty] = useState<{ jitter?: number; tol?: number }>({})
  const jitter = dirty.jitter ?? q.data?.config.jitter_max_ms ?? 3000
  const tol = dirty.tol ?? q.data?.config.disk_fault_tolerance_pct ?? 10
  const save = useMutation({
    mutationFn: () => api.post('/api/ha/fencing/configure', { jitter_max_ms: jitter, disk_fault_tolerance_pct: tol }),
    onSuccess: () => { toast.success('Saved'); setDirty({}); qc.invalidateQueries({ queryKey: ['ha', 'fencing'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '180px 180px 1fr auto', gap: 12, alignItems: 'end' }}>
      <label className="field">
        <span className="field-label">Random wait before a power-off (ms)</span>
        <input type="number" min={0} max={30000} step={500} value={jitter}
          onChange={e => setDirty(p => ({ ...p, jitter: Math.min(30000, Math.max(0, parseInt(e.target.value) || 0)) }))} className="input" />
      </label>
      <label className="field">
        <span className="field-label">Disk tolerance for reservations (%)</span>
        <input type="number" min={0} max={50} value={tol}
          onChange={e => setDirty(p => ({ ...p, tol: Math.min(50, Math.max(0, parseInt(e.target.value) || 0)) }))} className="input" />
      </label>
      <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: 0, paddingBottom: 6 }}>
        The wait keeps two nodes that decide at the same moment from switching each other off. The tolerance is the share of a
        pool's disks that may fail to be reserved before the import of the pool is refused (0: every disk must be reserved).
      </p>
      <button onClick={() => save.mutate()} disabled={save.isPending || (dirty.jitter === undefined && dirty.tol === undefined)} className="btn btn-ghost">Save</button>
    </div>
  )
}

export function PowerFencingCard() {
  const q = useQuery({
    queryKey: ['ha', 'power-targets'],
    queryFn: ({ signal }) => api.get<TargetsResponse>('/api/ha/power-targets', signal),
  })
  const [adding, setAdding] = useState<{ node: string; name: string } | null>(null)
  const targets = q.data?.targets ?? []
  const without = q.data?.nodes_without ?? []
  return (
    <div className="card" style={{ borderRadius: 'var(--radius-lg)', padding: '20px 24px' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 6 }}>
        <Icon name="power_settings_new" size={24} style={{ color: 'var(--text-tertiary)' }} />
        <div>
          <div style={{ fontWeight: 700 }}>Power fencing</div>
          <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
            Optional. Before this node takes a storage group over from a node that stopped answering, it switches that node off.
          </div>
        </div>
      </div>
      <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', margin: '8px 0 14px' }}>
        One entry per other node, on every node that may take over. A node without an entry is never switched off by this
        one: a takeover from it relies on the watchdog. The BMC of <em>this</em> system (sensors, power buttons) is set under
        Hardware, IPMI.
      </p>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 12, marginBottom: 14 }}>
        {targets.map(t => <TargetForm key={t.node + JSON.stringify(t)} initial={t} isNew={false} onDone={() => {}} />)}
        {adding && <TargetForm initial={EMPTY(adding.node, adding.name)} isNew onDone={() => setAdding(null)} />}
        {!q.isLoading && targets.length === 0 && without.length === 0 && !adding && (
          <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-tertiary)' }}>
            This node is not paired with another node yet (System, Configuration Sync): there is nobody to switch off.
          </div>
        )}
      </div>

      {without.length > 0 && !adding && (
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap', marginBottom: 14 }}>
          <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>Not configured for:</span>
          {without.map(n => (
            <button key={n.node} className="btn btn-ghost" onClick={() => setAdding(n)}>
              <Icon name="add" size={14} />{n.name}
            </button>
          ))}
        </div>
      )}

      <FencingTuning />
    </div>
  )
}
