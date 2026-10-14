import { expect, test } from '@playwright/test'
import { MockApi } from './mockApi'

const now = new Date().toISOString()

function status(over: Record<string, unknown> = {}) {
  return {
    node_id: 'id', name: 'n', version: '7.1.0', mode: 'standalone', quorate: true, coordinator: false, maintenance: false,
    pools: [{ name: 'tank', health: 'ONLINE' }], groups_owned: [], conflicts: 0, proposals: 0, holds: 0, reported_at: now, ...over,
  }
}

function overview(over: Record<string, unknown> = {}) {
  return {
    success: true, settings: { enabled: true, name: 'acme' }, membership: { fleet: '', manager: '' },
    remotes: [
      { id: 1, name: 'berlin-1', url: 'https://berlin-1.lan', fingerprint: 'ab', labels: { site: 'berlin' }, node_id: 'b1', added_at: now, last_seen: now, status: status() },
      { id: 2, name: 'paris-1', url: 'https://paris-1.lan', fingerprint: 'cd', labels: {}, node_id: 'p1', added_at: now, last_seen: now,
        status: status({ pools: [{ name: 'tank', health: 'DEGRADED' }], holds: 1 }) },
      { id: 3, name: 'oslo-1', url: 'https://oslo-1.lan', fingerprint: 'ef', labels: {}, node_id: 'o1', added_at: now, last_error: 'connection refused' },
    ],
    resources: [{ kind: 'user', key: 'carol', payload: { username: 'carol', email: 'carol@acme.lan', role: 'user', active: true }, version: 2, updated_at: now, updated_by: 'admin' }],
    rollouts: [],
    ...over,
  }
}

test('switch the manager role on', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/fleet', overview({ settings: { enabled: false, name: '' }, remotes: [], resources: [],
    membership: { fleet: 'hq', manager: 'hq1', last_rollout: '4 users' } }))
  mock.on('PUT', '/api/fleet/settings', { success: true })
  await mock.install(page)
  await page.goto('/fleet')

  await expect(page.getByText(/belongs to the fleet/)).toContainText('hq')
  await page.getByLabel('Fleet name').fill('acme-storage')
  await page.getByRole('button', { name: 'Make this node the fleet manager' }).click()
  await expect.poll(() => mock.sent('PUT', '/api/fleet/settings')).toEqual([{ enabled: true, name: 'acme-storage' }])
})

test('inventory shows health; add a node', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/fleet', overview())
  mock.on('POST', '/api/fleet/remotes', { success: true, fingerprint: 'AB:CD:EF:01:23:45:67:89:AB:CD' })
  await mock.install(page)
  await page.goto('/fleet')

  const row = (name: string) => page.getByRole('row').filter({ hasText: name })
  await expect(row('berlin-1')).toContainText('Healthy')
  await expect(row('paris-1')).toContainText('Needs attention')
  await expect(row('paris-1')).toContainText('1 on hold')
  await expect(row('oslo-1')).toContainText('Not reachable')

  await page.getByRole('button', { name: 'Add node' }).click()
  const dlg = page.getByRole('dialog')
  await dlg.getByLabel('Name').fill('rome-1')
  await dlg.getByLabel('Address').fill('https://rome-1.lan')
  await dlg.getByLabel(/API token of that node/).fill('dpl_secret')
  await dlg.getByLabel(/Labels/).fill('site=rome, tier=canary')
  await dlg.getByRole('button', { name: 'Add', exact: true }).click()
  await expect.poll(() => mock.sent('POST', '/api/fleet/remotes')).toEqual([
    { name: 'rome-1', url: 'https://rome-1.lan', token: 'dpl_secret', labels: { site: 'rome', tier: 'canary' } }])
})

test('fleet-wide group and a canary rollout', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/fleet', overview())
  mock.on('PUT', '/api/fleet/resources', { success: true })
  mock.on('POST', '/api/fleet/rollouts', { success: true, id: 7 })
  await mock.install(page)
  await page.goto('/fleet')

  await page.getByRole('button', { name: 'Add', exact: true }).click()
  let dlg = page.getByRole('dialog')
  await dlg.getByLabel('Kind').selectOption('group')
  await dlg.getByLabel('Name').fill('staff')
  await dlg.getByLabel('GID').fill('3000')
  await dlg.getByLabel(/Members/).fill('carol, dave')
  await dlg.getByRole('button', { name: 'Save' }).click()
  await expect.poll(() => mock.sent('PUT', '/api/fleet/resources')).toEqual([
    { kind: 'group', key: 'staff', payload: { name: 'staff', description: '', gid: 3000, members: ['carol', 'dave'] } }])

  await expect(dlg).toBeHidden()
  await page.getByRole('button', { name: 'New rollout' }).click()
  dlg = page.getByRole('dialog')
  await dlg.getByLabel('Name').fill('carol everywhere')
  await dlg.getByLabel('Soak minutes').fill('30')
  await dlg.getByRole('button', { name: 'Start rollout' }).click()
  await expect.poll(() => mock.sent('POST', '/api/fleet/rollouts')).toEqual([{
    name: 'carol everywhere', resources: [{ kind: 'user', key: 'carol' }],
    waves: [{ name: 'canary', remotes: [1] }, { name: 'everything else', remotes: [2, 3] }], soak_secs: 1800, auto_advance: true,
  }])
})

test('a paused rollout shows why and can be continued', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/fleet', overview({ rollouts: [{
    id: 7, name: 'carol everywhere', state: 'paused', resources: [{ kind: 'user', key: 'carol', version: 2 }],
    waves: [{ name: 'canary', remotes: [1] }, { name: 'everything else', remotes: [2, 3] }], current_wave: 1, soak_secs: 0,
    auto_advance: true, message: 'oslo-1 failed 3 times: connection refused', created_by: 'admin', created_at: now,
    targets: [
      { remote_id: 1, name: 'berlin-1', wave: 0, state: 'done', attempts: 0, applied_at: now },
      { remote_id: 3, name: 'oslo-1', wave: 1, state: 'failed', attempts: 3, detail: 'connection refused' },
      { remote_id: 2, name: 'paris-1', wave: 1, state: 'done', attempts: 0, applied_at: now },
    ],
  }] }))
  mock.on('POST', '/api/fleet/rollouts/:id/:action', { success: true })
  await mock.install(page)
  await page.goto('/fleet')

  await expect(page.getByText('oslo-1 failed 3 times: connection refused')).toBeVisible()
  await expect(page.getByText('oslo-1: failed')).toBeVisible()
  await page.getByRole('button', { name: 'Continue' }).click()
  await expect.poll(() => mock.calls.some(c => c.method === 'POST' && c.path === '/api/fleet/rollouts/7/continue')).toBe(true)
})

test('a silent fleet manager shows a banner on the remote; leaving the fleet', async ({ page }) => {
  const mock = new MockApi()
  const fleet = { fleet: 'acme', manager: 'hq1', last_contact_at: '2026-10-14T08:00:00Z', unreachable: true }
  mock.on('GET', '/api/config/sync/status', { success: true, status: { node_id: 'n', name: 'nas1', mode: 'standalone', waiting: 0, conflicts: 0, pending: 0, writer: true, peers: [] }, fleet })
  mock.on('GET', '/api/fleet', overview({ settings: { enabled: false, name: '' }, remotes: [], resources: [], membership: fleet }))
  mock.on('DELETE', '/api/fleet/membership', { success: true })
  await mock.install(page)
  await page.goto('/fleet')

  await expect(page.getByRole('status').filter({ hasText: 'Fleet manager unreachable' })).toContainText('hq1 of fleet acme')
  await page.getByRole('button', { name: 'Leave fleet' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Leave fleet' }).click()
  await expect.poll(() => mock.calls.some(c => c.method === 'DELETE' && c.path === '/api/fleet/membership')).toBe(true)
})
