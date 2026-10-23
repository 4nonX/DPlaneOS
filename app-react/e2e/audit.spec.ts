import { expect, test } from '@playwright/test'
import { MockApi } from './mockApi'

test('the audit log lists its entries', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/system/audit/logs', { success: true, limit: 100, offset: 0, logs: [
    { id: 2, timestamp: '2026-10-23T10:00:00Z', actor: 'admin', action: 'share_create', resource: 'media', details: '', ip_address: '10.0.0.5', success: true },
    { id: 1, timestamp: '2026-10-23T09:00:00Z', actor: 'bob', action: 'login', resource: 'session', details: '', ip_address: '10.0.0.6', success: false },
  ] })
  await mock.install(page)
  await page.goto('/audit')

  const row = (n: string) => page.getByRole('row').filter({ hasText: n })
  await expect(row('share_create')).toContainText('admin')
  await expect(row('share_create')).toContainText('Success')
  await expect(row('login')).toContainText('Failed')
  await expect(page.getByText('Showing the last 2 events.')).toBeVisible()
})
