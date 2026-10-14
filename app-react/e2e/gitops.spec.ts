import { expect, test, type Page } from '@playwright/test'
import { MockApi } from './mockApi'

const HEAD = 'a1b2c3d4e5f60718293a4b5c6d7e8f9012345678'
const GOOD = '0f1e2d3c4b5a69788796a5b4c3d2e1f001234567'

function settings(over: Record<string, unknown> = {}) {
  return {
    enabled: false, remotes: [], branch: 'main', layout: 'scoped', state_path: 'state.yaml',
    categories: { storage: true, access: true, apps: true, identity: true, protection: true, system: true },
    policy: 'direct', provider: 'none', provider_api_url: '', provider_repo: '', provider_cred: '',
    auto_apply: false, prune: false, self_heal: false, verify_signatures: false, allowed_signers: '', gpg_keys: '',
    sign_commits: false, interval_secs: 300, version: 3, node: 'nas1', webhook_secret_set: false,
    webhook_path: '/api/gitops/webhook', nixos_repo_id: null, ...over,
  }
}

function scope(over: Record<string, unknown>) {
  return { scope: 'cluster', path: 'cluster/state.yaml', owner: true, synced_sha: GOOD, push: [], pull: [], conflicts: [],
    declined: false, checked_at: new Date().toISOString(), ...over }
}

async function open(page: Page, mock: MockApi) {
  mock.on('GET', '/api/git-sync/credentials', { success: true, credentials: [{ id: 1, name: 'forge', auth_type: 'token' }, { id: 2, name: 'deploy', auth_type: 'ssh' }] })
  mock.on('GET', '/api/git-sync/repos', { success: true, repos: [] })
  await mock.install(page)
  await page.goto('/gitops')
}

test('set up the configuration repository', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/gitops/settings', { success: true, settings: settings() })
  mock.on('GET', '/api/gitops/status', { success: true, status: { enabled: false, scopes: [], remotes: [] }, holds: [] })
  mock.on('PUT', '/api/gitops/settings', { success: true, webhook_secret: 'f00dfeed' })
  await open(page, mock)

  await page.getByText('Keep the configuration in a Git repository').click()
  await page.getByRole('button', { name: 'Add remote' }).click()
  await page.getByLabel('Remote 1 URL').fill('https://git.lan/infra/nas.git')
  await page.getByLabel('Remote 1 fetch credential').selectOption('deploy')
  await page.getByLabel('Remote 1 push credential').selectOption('forge')
  await page.getByText('Open a pull request').click()
  await page.getByLabel('Provider').selectOption('gitea')
  await expect(page.getByLabel('API URL')).toHaveValue('https://git.example.lan/api/v1')
  await page.getByLabel('API URL').fill('https://git.lan/api/v1')
  await page.getByLabel('Repository on the provider').fill('infra/nas')
  await page.getByLabel('API token').selectOption('forge')
  await page.getByRole('button', { name: 'Save with a new webhook secret' }).click()

  await expect.poll(() => mock.sent('PUT', '/api/gitops/settings').length).toBe(1)
  expect(mock.sent('PUT', '/api/gitops/settings')[0]).toMatchObject({
    enabled: true, policy: 'pull_request', provider: 'gitea', provider_api_url: 'https://git.lan/api/v1',
    provider_repo: 'infra/nas', provider_cred: 'forge', webhook_secret: 'generate', auto_apply: false, prune: false,
    remotes: [{ url: 'https://git.lan/infra/nas.git', fetch_cred: 'deploy', push_cred: 'forge' }],
  })
  await expect(page.getByText('f00dfeed')).toBeVisible() // the new secret, shown once
})

test('changes from the repository wait for approval; holds offer resume and revert', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/gitops/settings', { success: true, settings: settings({ enabled: true, remotes: [{ url: 'https://git.lan/x.git', fetch_cred: '', push_cred: '' }] }) })
  mock.on('GET', '/api/gitops/status', {
    success: true, holds: [],
    status: {
      enabled: true, head: HEAD, checked_at: new Date().toISOString(), next_run: new Date().toISOString(), remotes: [{ url: 'https://git.lan/x.git', head: HEAD }],
      scopes: [
        scope({ pull: [{ id: 'user/bob', kind: 'user', key: 'bob', action: 'pull', deletion: false }, { id: 'group/old', kind: 'group', key: 'old', action: 'pull', deletion: true }] }),
        scope({ scope: 'group:data', path: 'groups/data/state.yaml', hold: { id: 4, scope: 'group:data', reason: 'applying commit a1b2c3d4e5f6 failed: share/media: path does not exist', commit_sha: HEAD, good_sha: GOOD, since: new Date().toISOString(), set_by: 'nas1' } }),
        scope({ scope: 'node:nas2', path: 'nodes/nas2/state.yaml', owner: false, owner_reason: "another node's own settings" }),
      ],
    },
  })
  mock.on('POST', '/api/gitops/approve', { success: true })
  mock.on('POST', '/api/gitops/holds/:scope/resume', { success: true })
  mock.on('POST', '/api/gitops/holds/:scope/revert', { success: true })
  await open(page, mock)

  const cluster = page.locator('.card').filter({ hasText: 'Cluster (shared by all nodes)' })
  await expect(cluster.getByText('user/bob')).toBeVisible()
  await expect(cluster.getByText('(deleted there)')).toBeVisible()
  await cluster.getByRole('button', { name: `Apply from ${HEAD.slice(0, 12)}` }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Apply' }).click()
  await expect.poll(() => mock.sent('POST', '/api/gitops/approve')).toEqual([{ scope: 'cluster', sha: HEAD }])

  const group = page.locator('.card').filter({ hasText: 'Storage group data' })
  await expect(group.getByRole('alert')).toContainText('path does not exist')
  await group.getByRole('button', { name: 'Resume (try again)' }).click()
  await expect.poll(() => mock.calls.filter(c => c.path === '/api/gitops/holds/group%3Adata/resume' || c.path === '/api/gitops/holds/group:data/resume').length).toBe(1)
  await group.getByRole('button', { name: `Revert to ${GOOD.slice(0, 12)}` }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Revert' }).click()
  await expect.poll(() => mock.calls.filter(c => c.path.endsWith('/revert')).length).toBe(1)

  await expect(page.locator('.card').filter({ hasText: 'Node nas2' }).getByText('Written by another node')).toBeVisible()
})

test('a declined pull request can be replaced by the repository version', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/gitops/settings', { success: true, settings: settings({ enabled: true, policy: 'pull_request', provider: 'github' }) })
  mock.on('GET', '/api/gitops/status', { success: true, holds: [], status: {
    enabled: true, head: HEAD, checked_at: new Date().toISOString(), remotes: [],
    scopes: [scope({ push: ['user/alice'], declined: true })],
  } })
  mock.on('POST', '/api/gitops/take', { success: true })
  await open(page, mock)
  await expect(page.getByText('was closed without merging')).toBeVisible()
  await page.getByRole('button', { name: "Take the repository's version" }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Take it' }).click()
  await expect.poll(() => mock.sent('POST', '/api/gitops/take')).toEqual([{ scope: 'cluster' }])
})
