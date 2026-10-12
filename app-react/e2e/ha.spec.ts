import { test, expect } from '@playwright/test'
import { mockApi, newSystem } from './mock'

const B = { key: 'key-b', name: 'nas-b' }

test.describe('Setup wizard', () => {
  test('asks what is wanted; a system on its own needs nothing', async ({ page }) => {
    const sys = newSystem()
    await mockApi(page, sys)
    await page.goto('/ha')
    await page.getByTestId('open-wizard').click()

    await expect(page.getByTestId('wizard-goal-alone')).toBeVisible()
    await expect(page.getByTestId('wizard-goal-pair')).toBeVisible()
    await expect(page.getByTestId('wizard-goal-join')).toBeVisible()

    await page.getByTestId('wizard-goal-alone').click()
    await expect(page.getByText('Nothing has to be set up')).toBeVisible()
    // Choosing this changed nothing on the system.
    expect(sys.calls).toEqual([])
  })

  test('leads through the steps in order and follows the state of the system', async ({ page }) => {
    const sys = newSystem()
    await mockApi(page, sys)
    await page.goto('/ha')
    await page.getByTestId('open-wizard').click()
    await page.getByTestId('wizard-goal-pair').click()

    // Not paired yet: the first step is open, with both ways to pair.
    const body = page.getByTestId('wizard-body')
    await expect(body.getByRole('heading', { name: '1. Pair the two systems' })).toBeVisible()
    await expect(page.getByTestId('wizard-step-1')).toHaveAttribute('aria-current', 'step')
    await body.getByRole('button', { name: /join code/i }).first().click()
    await expect(body.getByText('JOIN-CODE-1234')).toBeVisible()

    // The other system joins (done there): the wizard notices and moves on.
    sys.peers.push(B)
    await expect(body.getByRole('heading', { name: '2. Form the cluster' })).toBeVisible()

    // Forming the cluster through the form of the page.
    await body.getByLabel('Form a cluster with').selectOption('key-b')
    await expect(body.getByLabel("This node's cluster address")).toHaveValue('10.0.0.1')
    await body.getByRole('button', { name: 'Form cluster' }).click()
    await expect(body.getByRole('heading', { name: '3. Add a third vote' })).toBeVisible()
    expect(sys.calls.find(c => c.call === 'POST /api/quorum/cluster')?.body).toEqual({ peer_id: 'key-b', local_addr: '10.0.0.1', peer_addr: '10.0.0.2' })

    // The step says what the third vote is for and what is missing without it.
    await expect(body.getByText(/taken over by hand, not by itself/)).toBeVisible()
    // A vote server is added (on that machine): next step.
    sys.qdevice = { host: '10.0.0.9', voting: true }
    await expect(body.getByRole('heading', { name: '4. Watchdog and power fencing' })).toBeVisible()

    // Steps that are done show it in the list.
    for (const n of [1, 2, 3]) {
      await expect(page.getByTestId(`wizard-step-${n}`).locator('svg, span').first()).toBeVisible()
    }
  })

  test('opened again, it continues where the system is', async ({ page }) => {
    const sys = newSystem({ peers: [B], cluster: true, qdevice: { host: '10.0.0.9', voting: true }, watchdog: true })
    await mockApi(page, sys)
    await page.goto('/ha')
    await page.getByTestId('open-wizard').click()
    await page.getByTestId('wizard-goal-pair').click()
    // Paired, cluster, third vote and watchdog are there: the group is next.
    await expect(page.getByTestId('wizard-body').getByRole('heading', { name: '5. Put pools into a storage group' })).toBeVisible()
    await expect(page.getByTestId('wizard-body').getByText(/loses nothing on a takeover/)).toBeVisible()
  })

  test('the last step runs the checks and says what a failure means', async ({ page }) => {
    const sys = newSystem({
      peers: [B], cluster: true, qdevice: { host: '10.0.0.9', voting: false }, qdeviceReachable: true, watchdog: true,
      groups: [{ name: 'vault', topology: 'replicated', owner: 'key-a', candidates: ['key-a', 'key-b'], pools: ['tank'], epoch: 1 }],
      powerTargets: { 'key-b': { kind: 'bmc', enabled: true, host: '10.0.0.12', username: 'admin', has_password: true, works: false } },
    })
    await mockApi(page, sys)
    await page.goto('/ha')
    await page.getByTestId('open-wizard').click()
    await page.getByTestId('wizard-goal-pair').click()
    await page.getByTestId('wizard-step-6').click()
    await page.getByTestId('wizard-run-checks').click()

    const body = page.getByTestId('wizard-body')
    // Reachable is not voting: both are shown, one passes and one fails.
    await expect(body.getByText('This node connects to the vote server at 10.0.0.9:5403.')).toBeVisible()
    await expect(body.getByText('The vote server is configured but does not vote.')).toBeVisible()
    // A BMC that does not answer is a failed check, with what happened.
    await expect(body.getByText(/Power fencing of nas-b/)).toBeVisible()
    await expect(body.getByText(/connection timed out/)).toBeVisible()
    // Replication: what the next run would do.
    await expect(body.getByText(/vault: tank → nas-b/)).toBeVisible()
    // And in plain words what follows: no automatic failover.
    await expect(body.getByText('What happens when a node fails')).toBeVisible()
    await expect(body.getByText(/Nothing by itself/)).toBeVisible()
    // The checks changed nothing: only test requests were made.
    expect(sys.calls.map(c => c.call).filter(c => !c.endsWith('/test') && !c.endsWith('/check'))).toEqual([])
  })
})

test.describe('Cluster map and maintenance', () => {
  test('shows the nodes, who owns what, and how far the copies are behind', async ({ page }) => {
    const sys = newSystem({
      peers: [B], cluster: true, qdevice: { host: '10.0.0.9', voting: true },
      groups: [
        { name: 'vault', topology: 'replicated', owner: 'key-a', candidates: ['key-a', 'key-b'], pools: ['tank'], epoch: 3 },
        { name: 'archive', topology: 'shared', owner: 'key-b', candidates: ['key-b', 'key-a'], pools: ['cold'], epoch: 7 },
      ],
      powerTargets: { 'key-b': { kind: 'bmc', enabled: true, host: '10.0.0.12', username: 'admin', has_password: true, works: true } },
      peersInMaintenance: ['key-b'],
    })
    await mockApi(page, sys)
    await page.goto('/ha')

    const a = page.getByTestId('map-node-nas-a')
    await expect(a.getByText('this node')).toBeVisible()
    await expect(a.getByText('owns vault')).toBeVisible()
    const b = page.getByTestId('map-node-nas-b')
    await expect(b.getByText('Maintenance')).toBeVisible()
    await expect(b.getByText('Power fencing')).toBeVisible()
    await expect(b.getByText('owns archive')).toBeVisible()
    await expect(page.getByTestId('map-third-vote').getByText('Voting')).toBeVisible()

    const vault = page.getByTestId('map-group-vault')
    await expect(vault.getByText('Serving here')).toBeVisible()
    await expect(vault.getByText('copy on nas-b: 75 s ago')).toBeVisible()
    const archive = page.getByTestId('map-group-archive')
    await expect(archive.getByText('epoch 7')).toBeVisible()
    await expect(archive.getByText('Standby here')).toBeVisible()
  })

  test('maintenance shows what will move before anything moves, then does it', async ({ page }) => {
    const sys = newSystem({
      peers: [B], cluster: true, qdevice: { host: '10.0.0.9', voting: true },
      groups: [
        { name: 'vault', topology: 'shared', owner: 'key-a', candidates: ['key-a', 'key-b'], pools: ['tank'], epoch: 3 },
        { name: 'scratch', topology: 'standalone', owner: 'key-a', candidates: ['key-a'], pools: ['cold'], epoch: 1 },
      ],
    })
    await mockApi(page, sys)
    await page.goto('/ha')
    await page.getByRole('button', { name: 'Maintenance…' }).click()

    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText('Moves to nas-b: stopped here, then started there.')).toBeVisible()
    await expect(dialog.getByText(/It stays here and is not available while the node is down/)).toBeVisible()
    // Looking at the plan changed nothing.
    expect(sys.calls).toEqual([])

    await dialog.getByPlaceholder('Firmware update').fill('Replacing a fan')
    await dialog.getByRole('button', { name: 'Enter maintenance' }).click()
    await expect(dialog.getByText('This node is in maintenance.')).toBeVisible()
    expect(sys.calls).toEqual([{ call: 'POST /api/ha/maintenance', body: { reason: 'Replacing a fan', force: false } }])
    await dialog.getByRole('button', { name: 'Close', exact: true }).click()

    // The map says so, and the group is with the other node.
    await expect(page.getByText(/This node is in maintenance: Replacing a fan/)).toBeVisible()
    await expect(page.getByTestId('map-node-nas-b').getByText('owns vault')).toBeVisible()

    await page.getByRole('button', { name: 'Leave maintenance' }).click()
    await expect(page.getByRole('button', { name: 'Maintenance…' })).toBeVisible()
    // Nothing came back by itself.
    await expect(page.getByTestId('map-node-nas-b').getByText('owns vault')).toBeVisible()
  })

  test('a group with nowhere to go blocks maintenance, unless asked to enter anyway', async ({ page }) => {
    const sys = newSystem({
      peers: [B], cluster: true, peersInMaintenance: ['key-b'],
      groups: [{ name: 'vault', topology: 'shared', owner: 'key-a', candidates: ['key-a', 'key-b'], pools: ['tank'], epoch: 3 }],
    })
    await mockApi(page, sys)
    await page.goto('/ha')
    await page.getByRole('button', { name: 'Maintenance…' }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByText(/No other node can take this group now: nas-b is in maintenance/)).toBeVisible()
    await expect(dialog.getByRole('button', { name: 'Enter maintenance' })).toBeDisabled()
    await expect(dialog.getByText('Already in maintenance: nas-b.')).toBeVisible()

    await dialog.getByRole('button', { name: /Enter anyway/ }).click()
    await expect(dialog.getByText('This node is in maintenance.')).toBeVisible()
    expect(sys.calls[0].body.force).toBe(true)
    // The group stayed.
    expect(sys.groups[0].owner).toBe('key-a')
  })
})

test.describe('Power fencing', () => {
  test('one entry per node; the password is sent only when typed; the test shows what came back', async ({ page }) => {
    const sys = newSystem({ peers: [B, { key: 'key-c', name: 'nas-c' }], cluster: true })
    await mockApi(page, sys)
    await page.goto('/ha')
    await page.getByRole('button', { name: /Watchdog and power fencing/ }).click()

    // Nothing configured: both nodes are offered.
    await expect(page.getByText('Not configured for:')).toBeVisible()
    await page.getByRole('button', { name: 'nas-c', exact: true }).click()
    await page.getByLabel('BMC address of nas-c').fill('10.0.0.13')
    await page.getByLabel('User name').fill('ADMIN')
    await page.getByLabel('Password').fill('s3cret')
    await page.getByLabel('Use before a takeover').check()
    await page.getByRole('button', { name: 'Save', exact: true }).first().click()

    await expect.poll(() => sys.powerTargets['key-c']?.host).toBe('10.0.0.13')
    const put = sys.calls.find(c => c.call === 'PUT /api/ha/power-targets/key-c')!
    expect(put.body).toMatchObject({ kind: 'bmc', enabled: true, host: '10.0.0.13', username: 'ADMIN', password: 's3cret' })
    // The entry is for nas-c and for nobody else.
    expect(sys.powerTargets['key-b']).toBeUndefined()

    // Stored: the form shows the entry, without the password.
    await expect(page.getByPlaceholder('stored; leave empty to keep')).toBeVisible()
    await page.getByRole('button', { name: 'Test', exact: true }).click()
    await expect(page.getByText('Signed in to the BMC at 10.0.0.13; the machine is on. Nothing was switched.')).toBeVisible()

    // A change without typing the password keeps the stored one.
    sys.calls.length = 0
    await page.getByLabel('User name').fill('root')
    await page.getByRole('button', { name: 'Save', exact: true }).first().click()
    await expect.poll(() => sys.powerTargets['key-c']?.username).toBe('root')
    expect('password' in sys.calls[0].body).toBe(false)
    expect(sys.powerTargets['key-c'].has_password).toBe(true)

    // A BMC that does not answer: the test says so.
    sys.powerTargets['key-c'].works = false
    await page.getByRole('button', { name: 'Test', exact: true }).click()
    await expect(page.getByText(/did not answer the question for the power state: connection timed out/)).toBeVisible()
  })
})
