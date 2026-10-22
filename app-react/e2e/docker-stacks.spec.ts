import { expect, test } from '@playwright/test'
import { MockApi } from './mockApi'

const now = new Date().toISOString()

function stacks() {
  return {
    success: true, count: 2, stacks_dir: '/var/lib/dplaneos/stacks',
    stacks: [
      { name: 'media', path: '/var/lib/dplaneos/stacks/media', status: 'running', services: [{ Service: 'plex', State: 'running' }], file_size: 10, created_at: now, updated_at: now },
      { name: 'web', path: '/var/lib/dplaneos/stacks/web', status: 'partial', services: [{ Service: 'app', State: 'running' }, { Service: 'worker', State: 'exited' }], file_size: 10, created_at: now, updated_at: now },
    ],
  }
}

function updates() {
  return {
    success: true,
    settings: { media: { auto_update: false } },
    stacks: {
      media: {
        checked_at: now, update_available: true,
        services: [{ service: 'plex', image: 'lscr.io/linuxserver/plex:latest', update_available: true, remote_digest: 'sha256:new' }],
      },
      web: {
        checked_at: now, update_available: false, services: [],
        last_update: { at: now, result: { rolled_back: true, error: 'not up: app (timed out after 90s)', snapshots: ['tank/web@pre-update-web-1'] } },
      },
    },
  }
}

test('stack updates: badges, last result and settings', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/docker/stacks', stacks())
  mock.on('GET', '/api/docker/stacks/updates', updates())
  mock.on('GET', '/api/docker/stacks/yaml', { success: true, name: 'media', yaml: 'services: {}', env: '', path: '' })
  let auto = false
  mock.on('GET', '/api/docker/stacks/updates', () => {
    const u = updates()
    u.settings.media.auto_update = auto
    return u
  })
  mock.on('PUT', '/api/docker/stacks/settings', (req: { body: { auto_update: boolean } }) => {
    auto = req.body.auto_update
    return { success: true }
  })
  await mock.install(page)
  await page.goto('/docker')
  await page.getByRole('button', { name: 'Compose' }).click()

  // The list marks a stack with new images and one whose update was rolled back.
  const item = (name: string) => page.getByRole('button').filter({ hasText: name }).first()
  await expect(item('media')).toContainText('UPDATE')
  await expect(item('web')).toContainText('ROLLED BACK')
  await expect(item('web')).toContainText('partial')

  await item('web').click()
  await expect(page.getByText(/failed and rolled back to the previous images/)).toBeVisible()

  await item('media').click()
  await expect(page.getByText('Updates available')).toBeVisible()
  await expect(page.getByText('new image', { exact: true })).toBeVisible()
  await page.getByLabel('Auto-update').click()
  await expect(page.getByLabel('Auto-update')).toBeChecked()
  await expect.poll(() => mock.sent('PUT', '/api/docker/stacks/settings')).toEqual([
    { name: 'media', auto_update: true, snapshot: true, rollback_data: false }])
})
