/**
 * A stateful fake of the HA and configuration-sync API: pairing, cluster,
 * votes, watchdog, storage groups, the cluster map and maintenance. POSTs
 * change the state the next GETs report, so a test can walk a whole setup.
 */

import type { MockApi } from './mockApi'

export interface Peer { id: string; name: string; url: string }
export interface Group {
  name: string; topology: 'standalone' | 'shared' | 'replicated'; pools: string[]; candidates: string[]
  owner: string; epoch: number; interval_secs: number; auto_failover: boolean
}
export interface HAState {
  self: { id: string; name: string }
  peers: Peer[]
  cluster: null | { name: string; nodes: { id: number; name: string; addr: string; key: string; voter?: boolean }[]; qdevice?: string }
  watchdog: boolean
  pools: string[]
  groups: Group[]
  maintenance: Record<string, boolean>
  proposals: { id: number; peer_id: string; peer_name: string; scope: string; kind: string; key: string; payload: Record<string, unknown> | null; reason: string; created_at: string; changes: { path: string; old: unknown; new: unknown }[] }[]
}

export function freshNode(): HAState {
  return {
    self: { id: 'ka', name: 'nas1' }, peers: [], cluster: null, watchdog: false,
    pools: ['tank'], groups: [], maintenance: {}, proposals: [],
  }
}

/** Two paired nodes in a quorate cluster with a third vote and the watchdog on. */
export function readyPair(): HAState {
  const s = freshNode()
  s.peers = [{ id: 'kb', name: 'nas2', url: 'https://nas2.lan' }]
  s.cluster = {
    name: 'dplane-abc', qdevice: '10.0.0.9',
    nodes: [{ id: 1, name: 'nas1', addr: '10.0.0.1', key: 'ka' }, { id: 2, name: 'nas2', addr: '10.0.0.2', key: 'kb' }],
  }
  s.watchdog = true
  return s
}

const votes = (s: HAState) => (s.cluster ? s.cluster.nodes.length + (s.cluster.qdevice ? 1 : 0) : 0)

export function installHA(mock: MockApi, s: HAState) {
  mock.on('GET', '/api/config/sync/status', () => ({
    success: true,
    status: {
      node_id: s.self.id, name: s.self.name, mode: s.peers.length ? 'connected' : 'standalone', waiting: 0,
      conflicts: 0, pending: 0, writer: true,
      peers: s.peers.map(p => ({ ...p, fingerprint_display: 'AB:CD', last_contact: new Date().toISOString(), unreachable_since: null, last_error: '', reachable: true, waiting: 0 })),
    },
  }))
  mock.on('POST', '/api/config/peers/token', { success: true, token: 'dpj_e2e_token', expires_at: new Date(Date.now() + 900_000).toISOString() })
  mock.on('POST', '/api/config/peers/preview', {
    success: true,
    preview: { node_id: 'kb', name: 'nas2', fingerprint: 'AB:CD:EF', counts: { add: 1, same: 3 }, items: [{ kind: 'user', key: 'bob', action: 'add' }] },
  })
  mock.on('POST', '/api/config/peers/join', ({ body }) => {
    s.peers.push({ id: 'kb', name: 'nas2', url: body.url })
    return { success: true }
  })
  mock.on('GET', '/api/config/conflicts', () => ({ success: true, conflicts: [], pending: [], proposals: s.proposals, policies: { group: 'auto', cluster: 'auto' } }))
  mock.on('POST', '/api/config/proposals/:id/apply', ({ params }) => {
    s.proposals = s.proposals.filter(p => String(p.id) !== params.id)
    return { success: true, result: { applied: ['user/bob'] } }
  })
  mock.on('POST', '/api/config/proposals/:id/dismiss', ({ params }) => {
    s.proposals = s.proposals.filter(p => String(p.id) !== params.id)
    return { success: true }
  })

  mock.on('GET', '/api/quorum/status', () => {
    const c = s.cluster
    const v = votes(s)
    return {
      success: true, configured: !!c, error: '',
      cluster: c ? { cluster_name: c.name, nodes: c.nodes.map(n => ({ nodeid: n.id, name: n.name, addr: n.addr, node_key: n.key, voter: n.voter })), qdevice: c.qdevice ? { host: c.qdevice, algorithm: 'ffsplit' } : undefined } : null,
      status: {
        running: !!c, quorate: !!c, expected_votes: v, total_votes: v, quorum_votes: Math.floor(v / 2) + 1, flags: [],
        members: c ? c.nodes.map(n => ({ nodeid: n.id, votes: 1, name: n.addr, local: n.key === s.self.id })) : [],
        qdevice_alive: !!c?.qdevice, qdevice_votes: c?.qdevice ? 1 : 0,
      },
      info: { configured: !!c, quorate: !!c, expected_votes: v, auto_failover: v >= 3, auto_failover_reason: v >= 3 ? undefined : 'no third vote' },
      witness: { active: false, clusters: [] },
    }
  })
  mock.on('GET', '/api/quorum/suggest', { success: true, local_addr: '10.0.0.1', peer_addr: '10.0.0.2' })
  mock.on('POST', '/api/quorum/cluster', ({ body }) => {
    const peer = s.peers.find(p => p.id === body.peer_id)!
    s.cluster = { name: 'dplane-abc', nodes: [{ id: 1, name: s.self.name, addr: body.local_addr, key: s.self.id }, { id: 2, name: peer.name, addr: body.peer_addr, key: peer.id }] }
    return { success: true }
  })
  mock.on('POST', '/api/quorum/third-vote/code', { success: true, code: 'dpq_e2e', expires_at: new Date(Date.now() + 1_800_000).toISOString() })

  mock.on('GET', '/api/ha/watchdog/configure', () => ({ success: true, config: { enable: s.watchdog, device: '/dev/watchdog', timeout_secs: 30, pet_interval_sec: 10 } }))
  mock.on('POST', '/api/ha/watchdog/configure', ({ body }) => { s.watchdog = !!body.enable; return { success: true } })
  mock.on('GET', '/api/ha/protection', () => {
    const v = votes(s)
    return {
      success: true,
      layers: [
        { name: 'Third vote', state: v >= 3 ? 'ok' : 'off', detail: `${v} votes` },
        { name: 'Watchdog self-fence', state: s.watchdog ? 'ok' : 'off', detail: s.watchdog ? 'On' : 'Off' },
      ],
      auto_failover: v >= 3 && s.watchdog,
      auto_failover_reason: v < 3 ? 'no third vote' : s.watchdog ? undefined : 'enable the watchdog',
      failover_delay_secs: 45, self_fence_armed: false,
      fencing_ok: s.watchdog, fencing_reason: s.watchdog ? '' : 'enable the watchdog (System › High Availability) or configure IPMI/PDU power fencing',
    }
  })
  mock.on('GET', '/api/ha/hardware/detect', { success: true, provisional_path: 'replicated', provisional_reason: 'No SAS/SCSI pool disks found.', probe_required: false, watchdog_available: true })
  mock.on('POST', '/api/cluster/test/:kind', ({ params }) => ({
    success: true,
    result: params.kind === 'qdevice'
      ? (s.cluster?.qdevice ? { ok: true, detail: `${s.cluster.qdevice}:5403 answers on TCP 5403 and votes for this node` } : { ok: false, detail: 'the cluster has no QDevice' })
      : { ok: false, detail: 'power fencing is not configured: BMC address, user and password are needed' },
  }))

  const members = () => s.cluster ? s.cluster.nodes.filter(n => !n.voter).map(n => ({ key: n.key, name: n.name })) : [{ key: s.self.id, name: s.self.name }]
  mock.on('GET', '/api/groups', () => ({
    success: true, self: s.self.id,
    names: Object.fromEntries([[s.self.id, s.self.name], ...s.peers.map(p => [p.id, p.name])]),
    members: members(),
    imported_pools: s.pools,
    interfaces: ['eth0'],
    groups: s.groups.map(g => ({
      ...g, pools: g.pools.map(p => ({ name: p, guid: '' })), updated_at: new Date().toISOString(),
      role: g.owner === s.self.id ? 'owner' : g.candidates.includes(s.self.id) ? 'standby' : 'other',
      can_write: g.owner === s.self.id, problems: [], failover: { action: 'none' }, replication: [],
      resources: { stacks: [], nvme: [] },
    })),
  }))
  mock.on('POST', '/api/groups', ({ body }) => {
    s.groups.push({
      name: body.name, topology: body.topology, pools: body.pools, owner: s.self.id, epoch: 1,
      candidates: [s.self.id, ...body.candidates.filter((c: string) => c !== s.self.id)],
      interval_secs: body.interval_secs, auto_failover: body.auto_failover,
    })
    return { success: true }
  })

  mock.on('GET', '/api/cluster/map', () => ({
    success: true,
    map: {
      self: s.self.id, mode: s.peers.length ? 'connected' : 'standalone',
      nodes: [
        { key: s.self.id, name: s.self.name, addr: s.cluster?.nodes[0].addr, self: true, voter: false, in_cluster: !!s.cluster, paired: false, online: !!s.cluster, reachable: true, waiting: 0, maintenance: !!s.maintenance[s.self.id], coordinator: !!s.cluster },
        ...s.peers.map(p => ({ key: p.id, name: p.name, addr: s.cluster?.nodes.find(n => n.key === p.id)?.addr, self: false, voter: false, in_cluster: !!s.cluster, paired: true, online: !!s.cluster, reachable: true, waiting: 0, maintenance: !!s.maintenance[p.id], coordinator: false })),
      ],
      groups: s.groups.map(g => ({
        name: g.name, topology: g.topology, owner: g.owner, epoch: g.epoch, candidates: g.candidates, pools: g.pools,
        serving: g.owner === s.self.id, problems: [], interval_secs: g.interval_secs,
        replication: g.topology === 'replicated' && g.owner === s.self.id ? g.candidates.filter(c => c !== s.self.id).flatMap(t => g.pools.map(p => ({ pool: p, target: t, last_ok_at: new Date(Date.now() - 120_000).toISOString(), lag_secs: 120 }))) : [],
      })),
      coordinator: s.cluster ? s.self.id : undefined, term: s.cluster ? 33 : undefined,
      quorum: {
        configured: !!s.cluster, cluster_name: s.cluster?.name, quorate: !!s.cluster, expected_votes: votes(s), total_votes: votes(s),
        auto_failover: votes(s) >= 3 && s.watchdog, auto_failover_reason: votes(s) >= 3 ? undefined : 'no third vote', qdevice: s.cluster?.qdevice, qdevice_voting: !!s.cluster?.qdevice,
      },
      conflicts: 0, pending: 0, proposals: s.proposals.length,
    },
  }))
  mock.on('POST', '/api/cluster/maintenance', ({ body }) => {
    const moves = s.groups.filter(g => g.owner === s.self.id).map(g => {
      if (g.topology === 'standalone') return { group: g.name, result: 'stays', detail: 'standalone group: it has no other node' }
      const target = g.candidates.find(c => c !== s.self.id && !s.maintenance[c])
      if (!target) return { group: g.name, result: 'failed', detail: 'no other candidate is online and out of maintenance' }
      g.owner = target; g.epoch++
      return { group: g.name, target, result: 'moved' }
    })
    if (moves.some(m => m.result === 'failed')) return { success: false, error: 'cannot enter maintenance', moves, maintenance: { enabled: false } }
    s.maintenance[s.self.id] = true
    return { success: true, moves, maintenance: { enabled: true, reason: body.reason } }
  })
  mock.on('DELETE', '/api/cluster/maintenance', () => { s.maintenance[s.self.id] = false; return { success: true } })
}
