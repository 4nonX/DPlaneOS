/**
 * A mock of the daemon's API for the browser tests: a small model of a
 * system (paired or not, cluster or not, ...) that answers the requests of
 * the High Availability page from its state and changes the state when the
 * page asks for a change, the way the daemon would.
 *
 * Everything the tests did not model answers { success: true }, so that the
 * rest of the interface (sidebar, top bar) loads.
 */
import type { Page, Route } from '@playwright/test'

export interface Sys {
  self: { key: string; name: string }
  peers: { key: string; name: string }[]
  cluster: boolean
  qdevice: { host: string; voting: boolean } | null
  qdeviceReachable: boolean
  watchdog: boolean
  groups: { name: string; topology: 'standalone' | 'shared' | 'replicated'; owner: string; candidates: string[]; pools: string[]; epoch: number }[]
  powerTargets: Record<string, { kind: 'bmc' | 'pdu'; enabled: boolean; host: string; username: string; has_password: boolean; works: boolean }>
  maintenance: { active: boolean; reason: string; set_by: string }
  peersInMaintenance: string[]
  /** every request that changed something: "METHOD path" and its body */
  calls: { call: string; body: any }[]
}

export function newSystem(over: Partial<Sys> = {}): Sys {
  return {
    self: { key: 'key-a', name: 'nas-a' },
    peers: [],
    cluster: false,
    qdevice: null,
    qdeviceReachable: true,
    watchdog: false,
    groups: [],
    powerTargets: {},
    maintenance: { active: false, reason: '', set_by: '' },
    peersInMaintenance: [],
    calls: [],
    ...over,
  }
}

const nameOf = (s: Sys, key: string) => [s.self, ...s.peers].find(n => n.key === key)?.name ?? key

function haMap(s: Sys) {
  const owns = (key: string) => s.groups.filter(g => g.owner === key).map(g => g.name)
  return {
    success: true,
    self: s.self.key,
    nodes: [
      { key: s.self.key, name: s.self.name, self: true, voter: false, state: 'online', maintenance: s.maintenance.active, owns: owns(s.self.key) },
      ...s.peers.map(p => ({
        key: p.key, name: p.name, self: false, voter: false, state: s.cluster ? 'online' : 'paired',
        maintenance: s.peersInMaintenance.includes(p.key), owns: owns(p.key),
        power_fencing: s.powerTargets[p.key] ? (s.powerTargets[p.key].enabled ? 'ok' : 'off') : '',
      })),
    ],
    groups: s.groups.map(g => ({
      name: g.name, topology: g.topology, owner: g.owner, owner_name: nameOf(s, g.owner), epoch: g.epoch,
      candidates: g.candidates.map(c => nameOf(s, c)), pools: g.pools,
      role: g.owner === s.self.key ? 'owner' : 'standby', serving: g.owner === s.self.key, problems: [],
      failover: { action: 'none' },
      replication: g.topology === 'replicated' && g.owner === s.self.key
        ? g.candidates.filter(c => c !== s.self.key).map(c => ({ peer: c, peer_name: nameOf(s, c), pool: g.pools[0], direction: 'out', lag_seconds: 75 }))
        : [],
    })),
    maintenance: { active: s.maintenance.active, reason: s.maintenance.reason, set_by: s.maintenance.set_by },
    quorum: {
      configured: s.cluster, quorate: s.cluster, expected_votes: s.cluster ? 2 + (s.qdevice ? 1 : 0) : 0,
      total_votes: s.cluster ? 2 + (s.qdevice?.voting ? 1 : 0) : 0,
      auto_failover: s.cluster && !!s.qdevice?.voting, auto_failover_reason: s.qdevice ? 'the third vote is not voting' : 'two votes only',
    },
    third_vote: s.cluster && s.qdevice ? { kind: 'qdevice', host: s.qdevice.host, voting: s.qdevice.voting } : { kind: 'none' },
    sync: { mode: s.peers.length ? 'connected' : 'standalone', waiting: 0, conflicts: 0 },
  }
}

function quorumStatus(s: Sys) {
  const nodes = [s.self, ...s.peers].map((n, i) => ({ nodeid: i + 1, name: n.name, addr: `10.0.0.${i + 1}`, node_key: n.key }))
  return {
    success: true, configured: s.cluster,
    cluster: s.cluster ? { cluster_name: 'dplane', nodes, qdevice: s.qdevice ? { host: s.qdevice.host, algorithm: 'ffsplit' } : undefined } : null,
    status: {
      running: s.cluster, quorate: s.cluster, expected_votes: 2 + (s.qdevice ? 1 : 0), total_votes: 2 + (s.qdevice?.voting ? 1 : 0), quorum_votes: 2,
      flags: [], members: s.cluster ? nodes.map(n => ({ nodeid: n.nodeid, votes: 1, name: n.name, local: n.node_key === s.self.key })) : [],
      qdevice_alive: !!s.qdevice?.voting, qdevice_votes: s.qdevice?.voting ? 1 : 0,
    },
    info: { configured: s.cluster, quorate: s.cluster, expected_votes: 2 + (s.qdevice ? 1 : 0), auto_failover: s.cluster && !!s.qdevice?.voting, auto_failover_reason: 'two votes only' },
    error: '', witness: { active: false, clusters: [] },
  }
}

function maintenancePlan(s: Sys) {
  return s.groups.filter(g => g.owner === s.self.key).map(g => {
    if (g.topology === 'standalone') {
      return { group: g.name, action: 'stays', detail: 'Standalone: this group exists on this node only. It stays here and is not available while the node is down.', done: false }
    }
    const target = g.candidates.find(c => c !== s.self.key && !s.peersInMaintenance.includes(c))
    if (!target) {
      return { group: g.name, action: 'blocked', detail: `No other node can take this group now: ${nameOf(s, g.candidates.find(c => c !== s.self.key) ?? '')} is in maintenance.`, done: false }
    }
    return { group: g.name, action: 'move', target, target_name: nameOf(s, target), detail: `Moves to ${nameOf(s, target)}: stopped here, then started there.`, done: false }
  })
}

export async function mockApi(page: Page, s: Sys) {
  await page.addInitScript(() => {
    sessionStorage.setItem('session_id', 'e2e-session')
    sessionStorage.setItem('username', 'admin')
  })
  const json = (route: Route, body: unknown, status = 200) =>
    route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })

  await page.route('**/api/**', async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    const path = url.pathname
    const method = req.method()
    let body: any = {}
    try { body = req.postDataJSON() ?? {} } catch { /* no body */ }
    if (method !== 'GET') s.calls.push({ call: `${method} ${path}`, body })
    const key = `${method} ${path}`

    // ── session ───────────────────────────────────────────────────────
    if (key === 'GET /api/csrf') return json(route, { success: true, csrf_token: 'e2e-csrf' })
    if (key === 'GET /api/auth/check') return json(route, { authenticated: true, username: 'admin' })
    if (key === 'GET /api/auth/session') {
      return json(route, { success: true, user: { id: 1, username: 'admin', email: '', role: 'admin', must_change_password: false, source: 'local' } })
    }

    // ── pairing ───────────────────────────────────────────────────────
    if (key === 'GET /api/config/sync/status') {
      return json(route, { success: true, status: {
        node_id: s.self.key, name: s.self.name, mode: s.peers.length ? 'connected' : 'standalone', waiting: 0, conflicts: 0, pending: 0, writer: true,
        peers: s.peers.map(p => ({ id: p.key, name: p.name, url: `https://${p.name}.lan`, fingerprint_display: 'AB:CD', reachable: true, last_contact: new Date().toISOString(), behind: 0 })),
      } })
    }
    if (key === 'POST /api/config/peers/token') return json(route, { success: true, token: 'JOIN-CODE-1234', expires_at: new Date(Date.now() + 9e5).toISOString() })

    // ── cluster ───────────────────────────────────────────────────────
    if (key === 'GET /api/quorum/status') return json(route, quorumStatus(s))
    if (key === 'GET /api/quorum/suggest') return json(route, { success: true, local_addr: '10.0.0.1', peer_addr: '10.0.0.2' })
    if (key === 'POST /api/quorum/cluster') {
      if (!body.local_addr || !body.peer_addr || !s.peers.some(p => p.key === body.peer_id)) return json(route, { success: false, error: 'addresses and a paired node are needed' })
      s.cluster = true
      return json(route, { success: true })
    }
    if (key === 'POST /api/quorum/third-vote/code') return json(route, { success: true, code: 'dpq_e2e', expires_at: new Date(Date.now() + 18e5).toISOString() })
    if (key === 'POST /api/quorum/third-vote/test') {
      if (!s.cluster) return json(route, { success: true, ok: false, kind: 'none', steps: [{ name: 'Cluster', ok: false, detail: 'No cluster is formed on this node.' }] })
      if (!s.qdevice) return json(route, { success: true, ok: false, kind: 'member', steps: [{ name: 'Third vote', ok: false, detail: 'The cluster has two votes and no third one: failover stays a manual takeover.' }] })
      const steps = [
        s.qdeviceReachable
          ? { name: 'Reachable', ok: true, detail: `This node connects to the vote server at ${s.qdevice.host}:5403.` }
          : { name: 'Reachable', ok: false, detail: `This node cannot connect to the vote server at ${s.qdevice.host}:5403: connection refused` },
        s.qdevice.voting
          ? { name: 'Voting', ok: true, detail: 'The vote server votes: 3 of 3 votes are present.' }
          : { name: 'Voting', ok: false, detail: 'The vote server is configured but does not vote.' },
      ]
      return json(route, { success: true, ok: steps.every(x => x.ok), kind: 'qdevice', steps })
    }

    // ── protection ────────────────────────────────────────────────────
    if (key === 'GET /api/ha/protection') {
      const auto = s.cluster && !!s.qdevice?.voting && s.watchdog
      return json(route, { success: true, layers: [
        { name: 'Third vote', state: s.qdevice?.voting ? 'ok' : 'off', detail: '' },
        { name: 'Watchdog self-fence', state: s.watchdog ? 'ok' : 'off', detail: s.watchdog ? 'On (hardware watchdog, 30s).' : 'Off.' },
      ], auto_failover: auto, auto_failover_reason: auto ? '' : 'no third vote', failover_delay_secs: 45, self_fence_armed: false })
    }
    if (key === 'GET /api/ha/watchdog/configure') return json(route, { success: true, config: { enable: s.watchdog, device: '/dev/watchdog', timeout_secs: 30, pet_interval_sec: 10 } })
    if (key === 'POST /api/ha/watchdog/configure') { s.watchdog = !!body.enable; return json(route, { success: true }) }
    if (key === 'GET /api/ha/hardware/detect') return json(route, { success: true, watchdog: { present: true, device: '/dev/watchdog', identity: 'iTCO_wdt' }, scsi_pool_disks: [], pools_checked: [] })
    if (key === 'GET /api/ha/scsi/status') return json(route, { success: true, active: false, pools: [] })
    if (key === 'GET /api/ha/fencing/configure') return json(route, { success: true, config: { jitter_max_ms: 3000, disk_fault_tolerance_pct: 10 } })

    // ── power fencing ─────────────────────────────────────────────────
    if (key === 'GET /api/ha/power-targets') {
      const targets = Object.entries(s.powerTargets).map(([node, t]) => ({
        node, name: nameOf(s, node), kind: t.kind, enabled: t.enabled, host: t.host, username: t.username, has_password: t.has_password,
        tls_fingerprint: '', off_url: '', status_url: '', http_method: 'GET', expected_status: 0, timeout_secs: 10,
      }))
      return json(route, { success: true, targets, nodes_without: s.peers.filter(p => !s.powerTargets[p.key]).map(p => ({ node: p.key, name: p.name })) })
    }
    const pt = path.match(/^\/api\/ha\/power-targets\/([^/]+)(\/test|\/enroll)?$/)
    if (pt) {
      const node = decodeURIComponent(pt[1])
      if (method === 'PUT') {
        const old = s.powerTargets[node]
        s.powerTargets[node] = { kind: body.kind, enabled: !!body.enabled, host: body.host, username: body.username,
          has_password: 'password' in body ? !!body.password : !!old?.has_password, works: old?.works ?? true }
        return json(route, { success: true, message: 'Saved. Use Test to see that it works.' })
      }
      if (method === 'DELETE') { delete s.powerTargets[node]; return json(route, { success: true }) }
      if (pt[2] === '/test') {
        const t = s.powerTargets[node]
        return json(route, { success: true, result: t?.works
          ? { ok: true, power_state: 'on', confirms: 'sign-in and power state', detail: `Signed in to the BMC at ${t.host}; the machine is on. Nothing was switched.` }
          : { ok: false, confirms: '', detail: `The BMC at ${t?.host} did not answer the question for the power state: connection timed out` } })
      }
    }

    // ── groups ────────────────────────────────────────────────────────
    if (key === 'GET /api/groups') {
      return json(route, {
        success: true, self: s.self.key,
        names: Object.fromEntries([s.self, ...s.peers].map(n => [n.key, n.name])),
        members: s.cluster ? [s.self, ...s.peers] : [s.self],
        imported_pools: ['tank', 'cold'], interfaces: ['eth0'],
        groups: s.groups.map(g => ({
          ...g, pools: g.pools.map(p => ({ name: p, guid: '1' })), updated_at: new Date().toISOString(),
          role: g.owner === s.self.key ? 'owner' : 'standby', can_write: g.owner === s.self.key, problems: [], failover: { action: 'none' },
          interval_secs: 300, auto_failover: false, replication: [], resources: { stacks: [], nvme: [], iscsi: [] },
        })),
      })
    }
    if (key === 'POST /api/groups') {
      s.groups.push({ name: body.name, topology: body.topology, owner: s.self.key, candidates: [s.self.key, ...(body.candidates ?? [])], pools: body.pools ?? [], epoch: 1 })
      return json(route, { success: true })
    }
    const rc = path.match(/^\/api\/groups\/([^/]+)\/replicate\/check$/)
    if (rc && method === 'POST') {
      const g = s.groups.find(x => x.name === decodeURIComponent(rc[1]))!
      return json(route, { success: true, ok: true, checks: g.candidates.filter(c => c !== s.self.key).map(c => ({
        target: c, target_name: nameOf(s, c), pool: g.pools[0], ok: true, mode: 'incremental', base: 'dplane-1-1', detail: 'The next run sends the changes since dplane-1-1.' })) })
    }

    // ── map and maintenance ───────────────────────────────────────────
    if (key === 'GET /api/ha/map') return json(route, haMap(s))
    if (key === 'GET /api/ha/maintenance') {
      return json(route, { success: true, maintenance: s.maintenance, plan: maintenancePlan(s), others_in_maintenance: s.peersInMaintenance.map(k => nameOf(s, k)) })
    }
    if (key === 'POST /api/ha/maintenance') {
      const plan = maintenancePlan(s)
      const blocked = plan.find(p => p.action === 'blocked')
      if (blocked && !body.force) return json(route, { success: false, blocked: true, error: `a group cannot be moved to another node: ${blocked.group}`, plan })
      s.maintenance = { active: true, reason: body.reason ?? '', set_by: 'admin' }
      for (const step of plan) {
        if (step.action === 'move') { s.groups.find(g => g.name === step.group)!.owner = (step as any).target; step.done = true }
      }
      return json(route, { success: true, message: 'This node is in maintenance.', plan, not_moved: 0 })
    }
    if (key === 'DELETE /api/ha/maintenance') {
      s.maintenance = { active: false, reason: '', set_by: '' }
      return json(route, { success: true, message: 'This node left maintenance. Groups are not moved back by themselves.' })
    }

    if (key === 'GET /api/system/license/status') return json(route, { configured: false, active: false })
    return json(route, { success: true })
  })
}
