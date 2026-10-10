/**
 * QuorumPanel - cluster quorum and the third vote (Design 0001 phase 3a).
 *
 * Form a Corosync cluster with a paired node and add votes: a QDevice (any
 * Linux machine with one pasted command, or another DPlaneOS system), a voter
 * (a Pi or mini PC running corosync as a full member), or more DPlaneOS
 * nodes; let this node serve as the QDevice of another cluster. Automatic
 * failover needs three votes (ADR-0009); the panel says so instead of hiding it.
 *
 * Calls: GET /api/quorum/status, GET /api/quorum/suggest, POST/DELETE
 * /api/quorum/cluster, POST /api/quorum/third-vote/code, DELETE
 * /api/quorum/third-vote, POST /api/quorum/witness/join,
 * GET /api/config/sync/status (paired nodes).
 */

import { useEffect, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ensureOk } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { useConfirm } from '@/components/ui/ConfirmDialog'
import { toast } from '@/hooks/useToast'
import { useSyncStatus } from '@/components/layout/NodeStateBanner'

interface QNode { nodeid: number; name: string; addr: string; node_key: string; voter?: boolean }
interface QStatus {
  success: boolean
  configured: boolean
  cluster: { cluster_name: string; nodes: QNode[]; qdevice?: { host: string; algorithm: string } } | null
  status: {
    running: boolean; quorate: boolean; expected_votes: number; total_votes: number; quorum_votes: number
    flags: string[] | null; members: { nodeid: number; votes: number; name: string; local: boolean }[] | null
    qdevice_alive: boolean; qdevice_votes: number
  }
  info: { configured: boolean; quorate: boolean; expected_votes: number; auto_failover: boolean; auto_failover_reason?: string }
  error: string
  witness: { active: boolean; clusters: { cluster_name: string; node_url: string; enrolled_at: string }[] }
}

type Res = { success: boolean; error?: string }

const box: React.CSSProperties = { padding: 8, background: 'var(--surface)', borderRadius: 6, wordBreak: 'break-all', fontFamily: 'var(--font-mono)', fontSize: 'var(--text-sm)', flex: 1 }

function Copy({ text }: { text: string }) {
  return (
    <button className="btn btn-ghost btn-sm" onClick={() => { void navigator.clipboard?.writeText(text); toast.success('Copied') }} aria-label="Copy">
      <Icon name="content_copy" size={14} />
    </button>
  )
}

function FormCluster() {
  const qc = useQueryClient()
  const sync = useSyncStatus()
  const peers = sync.data?.peers ?? []
  const [peer, setPeer] = useState('')
  // null = use the suggestion
  const [localEdit, setLocalAddr] = useState<string | null>(null)
  const [peerEdit, setPeerAddr] = useState<string | null>(null)

  const suggest = useQuery({
    queryKey: ['quorum', 'suggest', peer],
    enabled: !!peer,
    queryFn: ({ signal }) => api.get<{ success: boolean; local_addr?: string; peer_addr?: string }>(`/api/quorum/suggest?peer_id=${encodeURIComponent(peer)}`, signal),
  })
  const localAddr = localEdit ?? suggest.data?.local_addr ?? ''
  const peerAddr = peerEdit ?? suggest.data?.peer_addr ?? ''

  const form = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res>('/api/quorum/cluster', { peer_id: peer, local_addr: localAddr, peer_addr: peerAddr })),
    onSuccess: () => { toast.success('Cluster formed'); qc.invalidateQueries({ queryKey: ['quorum'] }) },
    onError: (e: Error) => toast.error(e.message),
  })

  if (peers.length === 0) {
    return (
      <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginBottom: 0 }}>
        First pair this node with the other node in <Link to="/config-sync">Configuration Sync</Link>; then form the cluster here.
      </p>
    )
  }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8, maxWidth: 520 }}>
      <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }} htmlFor="q-peer">Form a cluster with</label>
      <select id="q-peer" className="input" value={peer} onChange={e => { setPeer(e.target.value); setLocalAddr(null); setPeerAddr(null) }}>
        <option value="">Choose a paired node…</option>
        {peers.map(p => <option key={p.id} value={p.id}>{p.name} ({p.url})</option>)}
      </select>
      {peer && (
        <>
          <div style={{ display: 'flex', gap: 8 }}>
            <div style={{ flex: 1 }}>
              <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }} htmlFor="q-local">This node's cluster address</label>
              <input id="q-local" className="input" value={localAddr} onChange={e => setLocalAddr(e.target.value)} placeholder="10.0.0.1" />
            </div>
            <div style={{ flex: 1 }}>
              <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }} htmlFor="q-peer-addr">Other node's cluster address</label>
              <input id="q-peer-addr" className="input" value={peerAddr} onChange={e => setPeerAddr(e.target.value)} placeholder="10.0.0.2" />
            </div>
          </div>
          <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: 0 }}>
            Suggested from the route between the nodes. A dedicated or redundant network is best; UDP 5405 must be open between them.
          </p>
          <button className="btn btn-primary" onClick={() => form.mutate()} disabled={!localAddr || !peerAddr || form.isPending} style={{ alignSelf: 'flex-start' }}>
            <Icon name="hub" size={16} />{form.isPending ? 'Forming…' : 'Form cluster'}
          </button>
        </>
      )}
    </div>
  )
}

type VotePath = 'qdevice' | 'voter' | 'dplane-qdevice' | 'dplane-node'

const PATHS: { id: VotePath; title: string; good: string; limits: string }[] = [
  {
    id: 'qdevice', title: 'QDevice on a small Linux machine',
    good: 'Raspberry Pi, mini PC, VM, NAS, cloud instance. Very light; one machine can serve several clusters; works over a slow or distant link (another site, a VPN).',
    limits: 'Only votes. Not recommended with an odd number of members. Needs TCP 5403 from every node.',
  },
  {
    id: 'voter', title: 'Voter: a Pi or mini PC as a full member',
    good: 'A real Corosync member with its own vote. Good for three or more members on one LAN.',
    limits: 'Needs a low-latency LAN (like the nodes) and corosync 3. It pulls its configuration from the node it joined; if that node is replaced, run the command again.',
  },
  {
    id: 'dplane-qdevice', title: 'Another DPlaneOS system as QDevice',
    good: 'Uses a DPlaneOS machine you already have (for example at another site) as the vote server, set up from its web interface.',
    limits: 'It must stay reachable on TCP 5403; it only votes, its storage is not involved.',
  },
  {
    id: 'dplane-node', title: 'A third DPlaneOS node (n+1)',
    good: 'A full node: votes, can own storage groups and take over from the others. Three DPlaneOS nodes need no other vote.',
    limits: 'Needs the hardware of a node. Pair it first in Configuration Sync. With four nodes, add a QDevice again.',
  },
]

function AddVote({ q, onClose }: { q: QStatus; onClose: () => void }) {
  const qc = useQueryClient()
  const [path, setPath] = useState<VotePath>(q.cluster?.qdevice ? 'dplane-node' : 'qdevice')
  const [code, setCode] = useState<{ code: string; expires_at: string } | null>(null)
  const create = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res & { code: string; expires_at: string }>('/api/quorum/third-vote/code', {})),
    onSuccess: r => setCode({ code: r.code, expires_at: r.expires_at }),
    onError: (e: Error) => toast.error(e.message),
  })
  useEffect(() => { create.mutate() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Watch for the new vote to arrive: a QDevice, or one more member.
  const before = { members: q.cluster?.nodes.length ?? 0, qdevice: !!q.cluster?.qdevice }
  const st = useQuery({
    queryKey: ['quorum', 'status'],
    queryFn: ({ signal }) => api.get<QStatus>('/api/quorum/status', signal),
    refetchInterval: 3000,
  })
  const done = !!st.data?.cluster && ((!before.qdevice && !!st.data.cluster.qdevice) || st.data.cluster.nodes.length > before.members)
  useEffect(() => {
    if (done) { toast.success('Vote added'); qc.invalidateQueries({ queryKey: ['ha'] }); onClose() }
  }, [done]) // eslint-disable-line react-hooks/exhaustive-deps

  const sync = useSyncStatus()
  const inCluster = new Set((q.cluster?.nodes ?? []).map(n => n.node_key))
  const candidates = (sync.data?.peers ?? []).filter(p => !inCluster.has(p.id))
  const [peer, setPeer] = useState('')
  const suggest = useQuery({
    queryKey: ['quorum', 'suggest', peer],
    enabled: !!peer,
    queryFn: ({ signal }) => api.get<{ success: boolean; peer_addr?: string }>(`/api/quorum/suggest?peer_id=${encodeURIComponent(peer)}`, signal),
  })
  const [addrEdit, setAddr] = useState<string | null>(null)
  const peerAddr = addrEdit ?? suggest.data?.peer_addr ?? ''
  const addNode = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res>('/api/quorum/nodes', { peer_id: peer, peer_addr: peerAddr })),
    onError: (e: Error) => toast.error(e.message),
  })

  const origin = window.location.origin
  const script = `curl -fsSk ${origin}/api/quorum/witness-setup.sh | sudo sh -s --`
  const p = PATHS.find(x => x.id === path)!
  return (
    <Modal title="Add a vote or a node" onClose={onClose} size="lg">
      <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        A cluster fails over automatically only when a majority of votes can tell a failed node from a broken network.
        Two nodes need one more vote; pick what fits your hardware. None of these stores data.
      </p>
      <div role="tablist" style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginBottom: 12 }}>
        {PATHS.map(x => (
          <button key={x.id} role="tab" aria-selected={path === x.id} className={`btn btn-sm ${path === x.id ? 'btn-primary' : 'btn-ghost'}`} onClick={() => setPath(x.id)}>{x.title}</button>
        ))}
      </div>
      <div style={{ fontSize: 'var(--text-sm)', display: 'grid', gridTemplateColumns: 'auto 1fr', gap: '4px 10px', marginBottom: 12 }}>
        <Icon name="thumb_up" size={16} style={{ color: 'var(--success)' }} /><span>{p.good}</span>
        <Icon name="info" size={16} style={{ color: 'var(--text-tertiary)' }} /><span>{p.limits}</span>
      </div>
      {path !== 'dplane-node' && !code && <p>Creating a code…</p>}
      {path === 'qdevice' && code && (
        <section>
          <p style={{ fontSize: 'var(--text-sm)', margin: '0 0 6px' }}>Run this one command on the machine (Debian, Ubuntu, Raspberry Pi OS, Fedora, RHEL, openSUSE, Alpine). It installs corosync-qnetd, opens the port and registers itself.</p>
          <div style={{ display: 'flex', gap: 6, alignItems: 'flex-start' }}><code style={box}>{`${script} ${origin} ${code.code}`}</code><Copy text={`${script} ${origin} ${code.code}`} /></div>
        </section>
      )}
      {path === 'voter' && code && (
        <section>
          <p style={{ fontSize: 'var(--text-sm)', margin: '0 0 6px' }}>Run this on the Pi or mini PC (same network as the nodes). It installs corosync, joins the cluster and keeps its configuration up to date.</p>
          <div style={{ display: 'flex', gap: 6, alignItems: 'flex-start' }}><code style={box}>{`${script} --voter ${origin} ${code.code}`}</code><Copy text={`${script} --voter ${origin} ${code.code}`} /></div>
        </section>
      )}
      {path === 'dplane-qdevice' && code && (
        <section>
          <p style={{ fontSize: 'var(--text-sm)', margin: '0 0 6px' }}>On the other system, open System › High Availability › <em>Serve as third vote</em> and enter:</p>
          <div style={{ display: 'flex', gap: 6, marginBottom: 6 }}><code style={box}>{origin}</code><Copy text={origin} /></div>
          <div style={{ display: 'flex', gap: 6 }}><code style={box}>{code.code}</code><Copy text={code.code} /></div>
        </section>
      )}
      {path === 'dplane-node' && (
        candidates.length === 0
          ? <p style={{ fontSize: 'var(--text-sm)' }}>Pair the new node with this one in <Link to="/config-sync">Configuration Sync</Link> first; it then appears here.</p>
          : (
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'flex-end' }}>
              <label style={{ flex: '2 1 220px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Paired node
                <select className="input" value={peer} onChange={e => { setPeer(e.target.value); setAddr(null) }}>
                  <option value="">Choose…</option>
                  {candidates.map(c => <option key={c.id} value={c.id}>{c.name} ({c.url})</option>)}
                </select>
              </label>
              <label style={{ flex: '1 1 160px', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>Its cluster address
                <input className="input" value={peerAddr} onChange={e => setAddr(e.target.value)} placeholder="10.0.0.3" />
              </label>
              <button className="btn btn-primary" disabled={!peer || !peerAddr || addNode.isPending} onClick={() => addNode.mutate()}>
                <Icon name="add" size={16} />{addNode.isPending ? 'Adding…' : 'Add to cluster'}
              </button>
            </div>
          )
      )}
      {path !== 'dplane-node' && code && (
        <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: '12px 0 0' }}>
          The code works once and expires at {new Date(code.expires_at).toLocaleTimeString()}. This window closes by itself when the vote has registered.
        </p>
      )}
    </Modal>
  )
}

function ServeAsThirdVote({ q }: { q: QStatus }) {
  const qc = useQueryClient()
  const [url, setUrl] = useState('')
  const [code, setCode] = useState('')
  const join = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res & { cluster_name: string }>('/api/quorum/witness/join', { node_url: url, code })),
    onSuccess: r => { toast.success(`Now the third vote of cluster ${r.cluster_name}`); setCode(''); qc.invalidateQueries({ queryKey: ['quorum'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  return (
    <div className="card" style={{ marginTop: 12 }}>
      <h3 style={{ marginTop: 0 }}>Serve as third vote</h3>
      <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        Let this system provide the third vote for another cluster (e.g. a pair at another site). It needs to be reachable from that cluster's nodes on TCP 5403; its own storage is not involved.
      </p>
      {q.witness.clusters.length > 0 && (
        <ul style={{ fontSize: 'var(--text-sm)' }}>
          {q.witness.clusters.map(c => <li key={c.cluster_name}><strong>{c.cluster_name}</strong> ({c.node_url}) {q.witness.active ? '' : '— service not running'}</li>)}
        </ul>
      )}
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <input className="input" style={{ flex: '2 1 240px' }} placeholder="Cluster node address, e.g. https://nas1.lan" value={url} onChange={e => setUrl(e.target.value)} aria-label="Cluster node address" />
        <input className="input" style={{ flex: '1 1 180px' }} placeholder="dpq_…" value={code} onChange={e => setCode(e.target.value.trim())} aria-label="Code" />
        <button className="btn btn-ghost" onClick={() => join.mutate()} disabled={!url || !code || join.isPending}>
          <Icon name="how_to_vote" size={16} />{join.isPending ? 'Registering…' : 'Register'}
        </button>
      </div>
    </div>
  )
}

export function QuorumPanel() {
  const qc = useQueryClient()
  const { confirm, ConfirmDialog } = useConfirm()
  const [adding, setAdding] = useState(false)
  const q = useQuery({
    queryKey: ['quorum', 'status'],
    queryFn: ({ signal }) => api.get<QStatus>('/api/quorum/status', signal),
    refetchInterval: 10_000,
  })
  const removeVote = useMutation({
    mutationFn: async () => ensureOk(await api.delete<Res>('/api/quorum/third-vote')),
    onSuccess: () => { toast.success('Third vote removed'); qc.invalidateQueries({ queryKey: ['quorum'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const removeNode = useMutation({
    mutationFn: async (name: string) => ensureOk(await api.delete<Res>(`/api/quorum/nodes/${encodeURIComponent(name)}`)),
    onSuccess: () => { toast.success('Member removed'); qc.invalidateQueries({ queryKey: ['quorum'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  const dissolve = useMutation({
    mutationFn: async () => ensureOk(await api.delete<Res>('/api/quorum/cluster')),
    onSuccess: () => { toast.success('Cluster removed'); qc.invalidateQueries({ queryKey: ['quorum'] }) },
    onError: (e: Error) => toast.error(e.message),
  })
  if (!q.data) return null
  const d = q.data
  const st = d.status
  const members = st.members ?? []

  return (
    <div style={{ marginBottom: 20 }}>
      <div className="card">
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <h3 style={{ margin: 0 }}>Cluster quorum</h3>
          {d.configured && (st.running
            ? <span className={`badge ${st.quorate ? 'badge-success' : 'badge-error'}`}>{st.quorate ? 'Quorate' : 'No quorum'}</span>
            : <span className="badge badge-error">Corosync not running</span>)}
          {d.configured && <span style={{ color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>{d.cluster?.cluster_name} · {st.total_votes} of {d.info.expected_votes} votes</span>}
        </div>

        {!d.configured ? (
          <>
            <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
              Nodes in a cluster agree on who is in charge (Corosync, as used by Proxmox). Two nodes plus a small third vote can fail over automatically and safely.
            </p>
            <FormCluster />
          </>
        ) : (
          <>
            <table className="data-table" style={{ fontSize: 'var(--text-sm)', marginTop: 10 }}>
              <thead><tr><th>Member</th><th>Address</th><th>Status</th><th /></tr></thead>
              <tbody>
                {d.cluster!.nodes.map(n => {
                  const m = members.find(x => x.nodeid === n.nodeid)
                  return (
                    <tr key={n.nodeid}>
                      <td>{n.name}{m?.local ? ' (this node)' : ''}{n.voter && <span className="badge" style={{ marginLeft: 6 }} title="Runs only corosync: votes, never owns storage">Voter</span>}</td>
                      <td style={{ fontFamily: 'var(--font-mono)' }}>{n.addr}</td>
                      <td>{m ? <span className="badge badge-success">Online</span> : <span className="badge badge-error">Not reachable</span>}</td>
                      <td style={{ textAlign: 'right' }}>
                        {!m?.local && (d.cluster!.nodes.filter(x => !x.voter).length > 2 || n.voter) && (
                          <button className="btn btn-ghost btn-sm" aria-label={`Remove ${n.name}`} onClick={async () => {
                            if (await confirm({ title: `Remove ${n.name} from the cluster?`, message: n.voter ? 'It stops voting; then run "systemctl disable --now corosync dplaneos-voter-sync.timer" on it.' : 'Corosync stops on it; move its storage groups to another node first.', confirmLabel: 'Remove', danger: true })) removeNode.mutate(n.name)
                          }}><Icon name="person_remove" size={14} /></button>
                        )}
                      </td>
                    </tr>
                  )
                })}
                {d.cluster!.qdevice && (
                  <tr>
                    <td>Third vote</td>
                    <td style={{ fontFamily: 'var(--font-mono)' }}>{d.cluster!.qdevice.host}</td>
                    <td>{st.qdevice_alive ? <span className="badge badge-success">Voting</span> : <span className="badge badge-warning" title="The nodes cannot reach it on TCP 5403, or it does not vote for this partition">Not voting</span>}</td>
                  </tr>
                )}
              </tbody>
            </table>

            <div style={{ marginTop: 12, padding: '10px 12px', borderRadius: 8, background: d.info.auto_failover ? 'var(--success-bg)' : 'var(--warning-bg)', fontSize: 'var(--text-sm)' }}>
              <Icon name={d.info.auto_failover ? 'check_circle' : 'info'} size={16} />{' '}
              {d.info.auto_failover
                ? `Automatic failover is possible: the cluster has ${d.info.expected_votes} votes and this node is in the quorate part.`
                : <>Automatic failover is off: {d.info.auto_failover_reason}. {!d.cluster!.qdevice && 'Everything else works normally; if a node fails, you take over on the other node manually.'}</>}
            </div>
            {d.error && <p style={{ color: 'var(--error)', fontSize: 'var(--text-sm)' }}>{d.error}</p>}

            <div style={{ display: 'flex', gap: 8, marginTop: 12, flexWrap: 'wrap' }}>
              <button className={`btn ${d.info.expected_votes < 3 ? 'btn-primary' : 'btn-ghost'}`} onClick={() => setAdding(true)}><Icon name="how_to_vote" size={16} />{d.info.expected_votes < 3 ? 'Add a third vote' : 'Add a vote or node'}</button>
              {d.cluster!.qdevice && <button className="btn btn-ghost" onClick={async () => {
                if (await confirm({ title: 'Remove the QDevice?', message: d.cluster!.nodes.length === 2 ? 'Automatic failover stops; the cluster keeps running with two votes.' : 'The members keep their own votes.', confirmLabel: 'Remove', danger: true })) removeVote.mutate()
              }}><Icon name="close" size={16} />Remove QDevice</button>}
              <button className="btn btn-ghost" style={{ marginLeft: 'auto', color: 'var(--error)' }} onClick={async () => {
                if (await confirm({ title: 'Remove the cluster?', message: 'Corosync stops on all members: storage groups no longer fail over and the watchdog stops guarding them. Storage and configuration are not touched.', confirmLabel: 'Remove cluster', danger: true })) dissolve.mutate()
              }}><Icon name="link_off" size={16} />Remove cluster</button>
            </div>
          </>
        )}
      </div>
      {!d.configured && <ServeAsThirdVote q={d} />}
      {d.configured && d.witness.clusters.length > 0 && <ServeAsThirdVote q={d} />}
      {adding && <AddVote q={d} onClose={() => setAdding(false)} />}
      <ConfirmDialog />
    </div>
  )
}
