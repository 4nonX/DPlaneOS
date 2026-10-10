/**
 * ADDomainsTab - additional Active Directory domains (other forests), each
 * with its own UID/GID range.
 *
 *   GET    /api/ldap/domains                      → { data: Domain[] }
 *   POST   /api/ldap/domains {name, realm, server, idmap_backend, idmap_low, idmap_high}
 *   DELETE /api/ldap/domains/{name}
 *   POST   /api/ldap/domains/{name}/join  {username, password, ou?} → { job_id }
 *   POST   /api/ldap/domains/{name}/leave {username, password}      → { job_id }
 *   GET    /api/ldap/domains/{name}/status        → { joined, message?, winbind_ping_error? }
 */
import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { useJob } from '@/hooks/useJob'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { ErrorState } from '@/components/ui/ErrorState'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'

interface Domain {
  id: number; name: string; realm: string; server: string
  idmap_backend: string; idmap_low: number; idmap_high: number
  domain_joined: boolean; domain_joined_at?: string; kinit_ok: boolean; enabled: boolean
}

function AddDomainModal({ existing, onClose, onDone }: { existing: Domain[]; onClose: () => void; onDone: () => void }) {
  // The next free range after the existing ones.
  const nextLow = existing.reduce((m, d) => Math.max(m, d.idmap_high + 1), 100000)
  const [f, setF] = useState({ name: '', realm: '', server: '', idmap_backend: 'rid', idmap_low: nextLow, idmap_high: nextLow + 99999 })
  const create = useMutation({
    mutationFn: async () => ensureOk(await api.post<{ success: boolean }>('/api/ldap/domains', f)),
    onSuccess: () => { toast.success(`Domain ${f.name.toUpperCase()} registered`); onDone(); onClose() },
    onError: (e: Error) => toast.error(e.message),
  })
  const set = (k: keyof typeof f, v: string | number) => setF(p => ({ ...p, [k]: v }))
  return (
    <Modal title="Add an Active Directory domain" onClose={onClose}>
      <label className="field"><span className="field-label">NetBIOS domain name</span>
        <input className="input" value={f.name} onChange={e => set('name', e.target.value.toUpperCase())} placeholder="PARTNER" autoFocus /></label>
      <label className="field"><span className="field-label">Kerberos realm</span>
        <input className="input" value={f.realm} onChange={e => set('realm', e.target.value.toUpperCase())} placeholder="PARTNER.EXAMPLE.COM" /></label>
      <label className="field"><span className="field-label">Domain controller (optional; found through DNS when empty)</span>
        <input className="input" value={f.server} onChange={e => set('server', e.target.value)} placeholder="dc1.partner.example.com" /></label>
      <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr 1fr', gap: 12 }}>
        <label className="field"><span className="field-label">ID mapping</span>
          <select className="input" value={f.idmap_backend} onChange={e => set('idmap_backend', e.target.value)}>
            <option value="rid">rid (from the SID)</option>
            <option value="ad">ad (uidNumber in AD)</option>
            <option value="autorid">autorid</option>
            <option value="tdb">tdb</option>
          </select></label>
        <label className="field"><span className="field-label">First ID</span>
          <input type="number" className="input" value={f.idmap_low} onChange={e => set('idmap_low', Number(e.target.value))} /></label>
        <label className="field"><span className="field-label">Last ID</span>
          <input type="number" className="input" value={f.idmap_high} onChange={e => set('idmap_high', Number(e.target.value))} /></label>
      </div>
      <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Every domain needs its own ID range; ranges must not overlap.</div>
      <div style={{ display: 'flex', gap: 10 }}>
        <button className="btn btn-primary" disabled={create.isPending || !f.name || !f.realm} onClick={() => create.mutate()}>Add domain</button>
        <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
      </div>
    </Modal>
  )
}

function JoinLeaveModal({ domain, mode, onClose, onDone }: { domain: Domain; mode: 'join' | 'leave'; onClose: () => void; onDone: () => void }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [ou, setOu] = useState('')
  const [jobId, setJobId] = useState<string | null>(null)
  const job = useJob(jobId)
  const start = useMutation({
    mutationFn: async () => {
      const r = await api.post<{ job_id?: string; error?: string }>(`/api/ldap/domains/${encodeURIComponent(domain.name)}/${mode}`,
        mode === 'join' ? { username, password, ou } : { username, password })
      if (!r.job_id) throw new Error(r.error ?? 'Could not start')
      return r.job_id
    },
    onSuccess: id => { setJobId(id); setPassword('') },
    onError: (e: Error) => toast.error(e.message),
  })
  const finished = job.data?.status === 'done' || job.data?.status === 'failed'
  return (
    <Modal title={`${mode === 'join' ? 'Join' : 'Leave'} ${domain.name}`} onClose={() => { if (finished) onDone(); onClose() }}>
      {!jobId ? (
        <>
          <p style={{ margin: 0, fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
            An account of {domain.realm} that may {mode === 'join' ? 'join computers to the domain' : 'remove the computer account'}. The password is used once and not stored.
          </p>
          <label className="field"><span className="field-label">User name (without the domain)</span>
            <input className="input" value={username} onChange={e => setUsername(e.target.value)} autoFocus /></label>
          <label className="field"><span className="field-label">Password</span>
            <input type="password" className="input" value={password} onChange={e => setPassword(e.target.value)} /></label>
          {mode === 'join' && (
            <label className="field"><span className="field-label">Organizational unit (optional)</span>
              <input className="input" value={ou} onChange={e => setOu(e.target.value)} placeholder="Servers/NAS" /></label>
          )}
          <div style={{ display: 'flex', gap: 10 }}>
            <button className="btn btn-primary" disabled={start.isPending || !username || !password} onClick={() => start.mutate()}>
              {mode === 'join' ? 'Join domain' : 'Leave domain'}
            </button>
            <button className="btn btn-ghost" onClick={onClose}>Cancel</button>
          </div>
        </>
      ) : (
        <>
          {!finished && <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}><Icon name="progress_activity" size={16} />Working…</div>}
          {job.data?.status === 'done' && <div style={{ color: 'var(--success)', fontWeight: 600 }}>{mode === 'join' ? 'Joined.' : 'Left the domain.'}</div>}
          {job.data?.status === 'failed' && <div style={{ color: 'var(--error)' }}>{job.data.error}</div>}
          {finished && <button className="btn btn-primary" onClick={() => { onDone(); onClose() }}>Close</button>}
        </>
      )}
    </Modal>
  )
}

export function ADDomainsTab() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [adding, setAdding] = useState(false)
  const [action, setAction] = useState<{ domain: Domain; mode: 'join' | 'leave' } | null>(null)
  const q = useQuery({
    queryKey: ['ldap', 'domains'],
    queryFn: ({ signal }) => api.get<{ success: boolean; data: Domain[] | null }>('/api/ldap/domains', signal),
  })
  const refresh = () => qc.invalidateQueries({ queryKey: ['ldap', 'domains'] })
  const remove = useMutation({
    mutationFn: async (name: string) => ensureOk(await api.delete<{ success: boolean }>(`/api/ldap/domains/${encodeURIComponent(name)}`)),
    onSuccess: () => { toast.success('Domain removed'); refresh() },
    onError: (e: Error) => toast.error(e.message),
  })
  const check = useMutation({
    mutationFn: (name: string) => api.get<{ joined: boolean; message?: string; winbind_ping_error?: string }>(`/api/ldap/domains/${encodeURIComponent(name)}/status`),
    onSuccess: (r, name) => {
      if (!r.joined) toast.error(r.message ?? `${name} is not joined`)
      else if (r.winbind_ping_error) toast.error(r.winbind_ping_error)
      else toast.success(`${name}: joined, winbind responds`)
    },
    onError: (e: Error) => toast.error(e.message),
  })

  if (q.isLoading) return <Skeleton height={160} />
  if (q.isError) return <ErrorState error={q.error} onRetry={refresh} />
  const domains = q.data?.data ?? []

  return (
    <div className="card" style={{ borderRadius: 'var(--radius-xl)', padding: 20 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
        <span style={{ fontWeight: 700 }}>Additional Active Directory domains</span>
        <button className="btn btn-primary btn-sm" style={{ marginLeft: 'auto' }} onClick={() => setAdding(true)}><Icon name="add" size={14} />Add domain</button>
      </div>
      <p style={{ margin: '0 0 12px', fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        For users of further domains or forests besides the one under Directory Setup. Each domain maps its users to its own range of user IDs.
      </p>
      <div style={{ overflowX: 'auto' }}>
        <table className="table" style={{ width: '100%' }}>
          <thead><tr><th>Domain</th><th>Realm</th><th>ID mapping</th><th>State</th><th /></tr></thead>
          <tbody>
            {domains.map(d => (
              <tr key={d.id}>
                <td style={{ fontWeight: 600 }}>{d.name}</td>
                <td style={{ fontFamily: 'var(--font-mono)', fontSize: 'var(--text-xs)' }}>{d.realm}</td>
                <td style={{ fontSize: 'var(--text-xs)' }}>{d.idmap_backend} · {d.idmap_low}-{d.idmap_high}</td>
                <td><span style={{ color: d.domain_joined ? 'var(--success)' : 'var(--text-tertiary)', fontWeight: 600 }}>{d.domain_joined ? 'Joined' : 'Not joined'}</span></td>
                <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                  {d.domain_joined ? (
                    <>
                      <button className="btn btn-xs btn-ghost" disabled={check.isPending} onClick={() => check.mutate(d.name)}><Icon name="network_check" size={14} />Check</button>
                      <button className="btn btn-xs btn-ghost" onClick={() => setAction({ domain: d, mode: 'leave' })}><Icon name="logout" size={14} />Leave</button>
                    </>
                  ) : (
                    <>
                      <button className="btn btn-xs btn-ghost" onClick={() => setAction({ domain: d, mode: 'join' })}><Icon name="login" size={14} />Join</button>
                      <button className="btn btn-xs btn-ghost" style={{ color: 'var(--error)' }} disabled={remove.isPending}
                        onClick={async () => { if (await confirm({ title: `Remove ${d.name}?`, message: 'The domain registration and its ID range are removed.', confirmLabel: 'Remove', danger: true })) remove.mutate(d.name) }}>
                        <Icon name="delete" size={14} />Remove
                      </button>
                    </>
                  )}
                </td>
              </tr>
            ))}
            {domains.length === 0 && <tr><td colSpan={5} style={{ textAlign: 'center', color: 'var(--text-tertiary)' }}>No additional domains.</td></tr>}
          </tbody>
        </table>
      </div>
      {adding && <AddDomainModal existing={domains} onClose={() => setAdding(false)} onDone={refresh} />}
      {action && <JoinLeaveModal domain={action.domain} mode={action.mode} onClose={() => setAction(null)} onDone={refresh} />}
      <ConfirmDialog />
    </div>
  )
}
