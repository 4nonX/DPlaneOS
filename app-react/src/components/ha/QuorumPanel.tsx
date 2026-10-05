/**
 * QuorumPanel - cluster quorum and the third vote (Design 0001 phase 3a).
 *
 * Form a Corosync cluster with a paired node, add a third vote (another
 * DPlaneOS system, or any Linux machine with one pasted command), and let this
 * node serve as the third vote of another cluster. Automatic failover needs
 * three votes (ADR-0009); the panel says so instead of hiding it.
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

interface QNode { nodeid: number; name: string; addr: string; node_key: string }
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

function AddThirdVote({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const [code, setCode] = useState<{ code: string; expires_at: string } | null>(null)
  const create = useMutation({
    mutationFn: async () => ensureOk(await api.post<Res & { code: string; expires_at: string }>('/api/quorum/third-vote/code', {})),
    onSuccess: r => setCode({ code: r.code, expires_at: r.expires_at }),
    onError: (e: Error) => toast.error(e.message),
  })
  useEffect(() => { create.mutate() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Watch for the third vote to arrive.
  const st = useQuery({
    queryKey: ['quorum', 'status'],
    queryFn: ({ signal }) => api.get<QStatus>('/api/quorum/status', signal),
    refetchInterval: 3000,
  })
  const done = !!st.data?.cluster?.qdevice
  useEffect(() => {
    if (done) { toast.success('Third vote added'); qc.invalidateQueries({ queryKey: ['ha'] }); onClose() }
  }, [done]) // eslint-disable-line react-hooks/exhaustive-deps

  const origin = window.location.origin
  const command = code ? `curl -fsSk ${origin}/api/quorum/witness-setup.sh | sudo sh -s -- ${origin} ${code.code}` : ''
  return (
    <Modal title="Add a third vote" onClose={onClose} size="lg">
      <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>
        A third vote lets the cluster tell a failed node from a broken network, so it can fail over automatically.
        It only needs to be small and always on, and reachable from both nodes on TCP 5403. It stores no data.
      </p>
      {!code ? <p>Creating a code…</p> : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
          <section>
            <h4 style={{ margin: '0 0 6px' }}>Any Linux machine: Raspberry Pi, VM, server, cloud instance</h4>
            <p style={{ fontSize: 'var(--text-sm)', margin: '0 0 6px' }}>Run this one command on it (Debian, Ubuntu, Raspberry Pi OS, Fedora, RHEL, openSUSE). It installs the vote service, opens the port and registers itself.</p>
            <div style={{ display: 'flex', gap: 6, alignItems: 'flex-start' }}><code style={box}>{command}</code><Copy text={command} /></div>
          </section>
          <section>
            <h4 style={{ margin: '0 0 6px' }}>Another DPlaneOS system</h4>
            <p style={{ fontSize: 'var(--text-sm)', margin: '0 0 6px' }}>On it, open System › High Availability › <em>Serve as third vote</em> and enter:</p>
            <div style={{ display: 'flex', gap: 6, marginBottom: 6 }}><code style={box}>{origin}</code><Copy text={origin} /></div>
            <div style={{ display: 'flex', gap: 6 }}><code style={box}>{code.code}</code><Copy text={code.code} /></div>
          </section>
          <p style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', margin: 0 }}>
            The code works once and expires at {new Date(code.expires_at).toLocaleTimeString()}. This window closes by itself when the third vote has registered.
          </p>
        </div>
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
              <thead><tr><th>Member</th><th>Address</th><th>Status</th></tr></thead>
              <tbody>
                {d.cluster!.nodes.map(n => {
                  const m = members.find(x => x.nodeid === n.nodeid)
                  return (
                    <tr key={n.nodeid}>
                      <td>{n.name}{m?.local ? ' (this node)' : ''}</td>
                      <td style={{ fontFamily: 'var(--font-mono)' }}>{n.addr}</td>
                      <td>{m ? <span className="badge badge-success">Online</span> : <span className="badge badge-error">Not reachable</span>}</td>
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
                ? 'Automatic failover is possible: the cluster has three votes and this node is in the quorate part.'
                : <>Automatic failover is off: {d.info.auto_failover_reason}. {!d.cluster!.qdevice && 'Everything else works normally; if a node fails, you take over on the other node manually.'}</>}
            </div>
            {d.error && <p style={{ color: 'var(--error)', fontSize: 'var(--text-sm)' }}>{d.error}</p>}

            <div style={{ display: 'flex', gap: 8, marginTop: 12, flexWrap: 'wrap' }}>
              {!d.cluster!.qdevice
                ? <button className="btn btn-primary" onClick={() => setAdding(true)}><Icon name="how_to_vote" size={16} />Add a third vote</button>
                : <button className="btn btn-ghost" onClick={async () => {
                    if (await confirm({ title: 'Remove the third vote?', message: 'Automatic failover stops; the cluster keeps running with two votes.', confirmLabel: 'Remove', danger: true })) removeVote.mutate()
                  }}><Icon name="close" size={16} />Remove third vote</button>}
              <button className="btn btn-ghost" style={{ marginLeft: 'auto', color: 'var(--error)' }} onClick={async () => {
                if (await confirm({ title: 'Remove the cluster?', message: 'Corosync stops on all members and HA falls back to the heartbeat check. Storage and configuration are not touched.', confirmLabel: 'Remove cluster', danger: true })) dissolve.mutate()
              }}><Icon name="link_off" size={16} />Remove cluster</button>
            </div>
          </>
        )}
      </div>
      {!d.configured && <ServeAsThirdVote q={d} />}
      {d.configured && d.witness.clusters.length > 0 && <ServeAsThirdVote q={d} />}
      {adding && <AddThirdVote onClose={() => setAdding(false)} />}
      <ConfirmDialog />
    </div>
  )
}
