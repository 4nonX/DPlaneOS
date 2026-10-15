import { expect, test } from '@playwright/test'
import { MockApi } from './mockApi'
import { freshNode, installHA, readyPair, type HAState } from './haFixture'

async function open(page: import('@playwright/test').Page, state: HAState, path: string) {
  const mock = new MockApi()
  installHA(mock, state)
  await mock.install(page)
  await page.goto(path)
  return mock
}

const step = (page: import('@playwright/test').Page) => page.getByRole('heading', { level: 2 })

test.describe('Guided setup', () => {
  test('a fresh node chooses its profile; one node needs nothing else', async ({ page }) => {
    await open(page, freshNode(), '/ha/setup')
    await page.getByRole('button', { name: /One node/ }).click()
    await expect(page.getByText('Nothing else to set up')).toBeVisible()
    await page.getByRole('button', { name: 'Back' }).click()
    await page.getByRole('button', { name: /Two or more nodes with failover/ }).click()
    await expect(step(page)).toHaveText('Pair the nodes')
  })

  test('walks from pairing to a storage group, each step done through the API', async ({ page }) => {
    const state = freshNode()
    const mock = await open(page, state, '/ha/setup')
    await page.getByRole('button', { name: /Two or more nodes with failover/ }).click()

    // 1. Pair: join the other node after previewing the merge.
    await expect(step(page)).toHaveText('Pair the nodes')
    await page.getByLabel("Other node's address").fill('https://nas2.lan')
    await page.getByLabel('Join code from the other node').fill('dpj_e2e_token')
    await page.getByRole('button', { name: 'Preview the merge' }).click()
    await expect(page.getByText('Added here: 1')).toBeVisible()
    await page.getByRole('button', { name: 'Pair with nas2' }).click()
    expect(mock.sent('POST', '/api/config/peers/join')[0]).toMatchObject({ url: 'https://nas2.lan', token: 'dpj_e2e_token' })

    // 2. Form the cluster: the addresses come from the suggestion.
    await expect(step(page)).toHaveText('Form the cluster')
    await page.getByLabel('Form a cluster with').selectOption('kb')
    await expect(page.getByLabel("This node's cluster address")).toHaveValue('10.0.0.1')
    await page.getByRole('button', { name: 'Form cluster' }).click()
    await expect.poll(() => mock.sent('POST', '/api/quorum/cluster')).toEqual([{ peer_id: 'kb', local_addr: '10.0.0.1', peer_addr: '10.0.0.2' }])

    // 3. Third vote: two votes; continue without one (manual takeover).
    await expect(step(page)).toHaveText('Add a third vote')
    await expect(page.getByText('The cluster has 2 votes.')).toBeVisible()
    await page.getByRole('button', { name: /Continue without/ }).click()

    // 4. Protection: arm the watchdog.
    await expect(step(page)).toHaveText('Protect against split brain')
    await expect(page.getByText(/Not protected yet/)).toBeVisible()
    await page.getByRole('combobox').filter({ hasText: 'Disabled' }).selectOption('yes')
    await page.getByRole('button', { name: 'Save Watchdog Config' }).click()
    expect(mock.sent('POST', '/api/ha/watchdog/configure')[0]).toMatchObject({ enable: true, timeout_secs: 30, pet_interval_sec: 10 })

    // 5. Storage group: a replicated group of tank on both nodes.
    await expect(step(page)).toHaveText('Create a storage group')
    await page.getByLabel('Group name').fill('data')
    await page.getByLabel('Topology').selectOption('replicated')
    await expect(page.getByText(/Changes since the last replication are lost/)).toBeVisible()
    await page.getByRole('checkbox', { name: 'tank' }).check()
    await page.getByRole('button', { name: 'Create group' }).click()
    await expect.poll(() => mock.sent('POST', '/api/groups')[0]).toMatchObject({ name: 'data', topology: 'replicated', pools: ['tank'], candidates: ['kb'], interval_secs: 300 })

    // Done: failover is manual (no third vote) and says so.
    await expect(step(page)).toHaveText('Check the result')
    await expect(page.getByText(/Automatic failover is off: no third vote/)).toBeVisible()
    expect(state.groups[0].candidates).toEqual(['ka', 'kb'])
  })

  test('resumes at the first open step and offers the safety checks', async ({ page }) => {
    const state = readyPair()
    await open(page, state, '/ha/setup')
    // Paired, clustered with a QDevice, watchdog on: only the group is missing.
    await expect(step(page)).toHaveText('Create a storage group')
    for (const name of ['1. Pair the nodes', '2. Form the cluster', '3. Add a third vote', '4. Protect against split brain']) {
      await expect(page.getByRole('button', { name })).toBeVisible()
    }
    await page.getByRole('button', { name: '4. Protect against split brain' }).click()
    await expect(page.getByText('Done', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Third vote (QDevice)' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'votes for this node' })).toBeVisible()
  })
})

test.describe('Cluster map', () => {
  test('shows owners and standbys per group and moves groups away for maintenance', async ({ page }) => {
    const state = readyPair()
    state.groups.push({ name: 'data', topology: 'shared', pools: ['tank'], candidates: ['ka', 'kb'], owner: 'ka', epoch: 3, interval_secs: 300, auto_failover: false })
    const mock = await open(page, state, '/cluster')

    await expect(page.getByRole('heading', { name: 'Cluster Map' })).toBeVisible()
    const row = page.getByRole('row').filter({ hasText: 'data' })
    await expect(row.getByText('Owner')).toBeVisible()
    await expect(row.getByText('Standby 2')).toBeVisible()
    await expect(row.getByText('Serving')).toBeVisible()
    await expect(page.getByText('Coordinator', { exact: true })).toBeVisible()

    await page.getByRole('button', { name: 'Start maintenance…' }).click()
    await expect(page.getByRole('dialog')).toContainText('These groups move to another node first')
    await page.getByLabel('Reason (shown to other administrators)').fill('firmware')
    await page.getByRole('button', { name: 'Start maintenance', exact: true }).click()
    expect(mock.sent('POST', '/api/cluster/maintenance')[0]).toEqual({ reason: 'firmware' })
    await expect(page.getByText('moved to nas2')).toBeVisible()
    await expect(row.getByText('Standby 1')).toBeVisible() // this node is now the standby
    await expect(page.getByRole('button', { name: 'End maintenance' })).toBeVisible()
  })

  test('a standalone node says how to set up failover', async ({ page }) => {
    await open(page, freshNode(), '/cluster')
    await expect(page.getByText(/No cluster: storage groups cannot fail over/)).toBeVisible()
    await expect(page.getByRole('button', { name: 'Start maintenance…' })).toHaveCount(0)
  })
})

test.describe('Proposals', () => {
  test('a change that lost a conflict can be applied again or dismissed', async ({ page }) => {
    const state = readyPair()
    state.proposals = [
      { id: 7, peer_id: 'kb', peer_name: 'nas2', scope: 'cluster', kind: 'user', key: 'bob', payload: { username: 'bob', role: 'operator' },
        reason: 'the version written by the cluster coordinator (term 33) wins', created_at: new Date().toISOString(),
        changes: [{ path: 'role', old: 'user', new: 'operator' }] },
      { id: 8, peer_id: 'kb', peer_name: 'nas2', scope: 'cluster', kind: 'group', key: 'staff', payload: null,
        reason: 'the version written by the cluster coordinator (term 33) wins', created_at: new Date().toISOString(), changes: [] },
    ]
    const mock = await open(page, state, '/config-sync')
    await expect(page.getByRole('heading', { name: 'Your changes that lost a conflict' })).toBeVisible()
    const bob = page.locator('.card').filter({ hasText: 'User "bob"' })
    await expect(bob.getByRole('cell', { name: 'operator' })).toBeVisible()
    await bob.getByRole('button', { name: 'Apply again' }).click()
    await page.getByRole('dialog').getByRole('button', { name: 'Apply' }).click()
    await expect.poll(() => mock.calls.filter(c => c.path === '/api/config/proposals/7/apply').length).toBe(1)
    await expect(bob).toHaveCount(0)

    const staff = page.locator('.card').filter({ hasText: 'Group "staff"' })
    await expect(staff.getByText('Your change was to delete it.')).toBeVisible()
    await staff.getByRole('button', { name: 'Dismiss' }).click()
    await expect.poll(() => mock.calls.filter(c => c.path === '/api/config/proposals/8/dismiss').length).toBe(1)
    await expect(page.getByRole('heading', { name: 'Your changes that lost a conflict' })).toHaveCount(0)
    // The conflict rules are shown for paired nodes.
    await expect(page.getByRole('heading', { name: 'When the same thing was changed on two nodes' })).toBeVisible()
  })
})
