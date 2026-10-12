import { test, expect } from '@playwright/test'
import { mockApi, newSystem } from './mock'

// The audit log is part of every system: the page lists what was recorded,
// whatever else is or is not installed.
test.describe('Audit log', () => {
  test('lists the entries, searches, filters failures and pages', async ({ page }) => {
    await mockApi(page, newSystem())
    const asked: string[] = []
    const entry = (id: number, over: object = {}) => ({
      id, timestamp: new Date(1_790_000_000_000 - id * 60_000).toISOString(), actor: 'admin', action: 'login',
      resource: '', details: 'Signed in', ip_address: '192.0.2.10', success: true, ...over,
    })
    await page.route('**/api/system/audit/verify-chain', route =>
      route.fulfill({ json: { success: true, valid: true } }))
    await page.route('**/api/system/audit/logs?*', route => {
      const q = new URL(route.request().url()).searchParams
      asked.push(q.toString())
      let logs = Array.from({ length: 130 }, (_, i) => entry(i + 1))
      logs[1] = entry(2, { actor: 'mallory', action: 'login_failed', details: 'Wrong password', success: false })
      if (q.get('level') === 'error') logs = logs.filter(l => !l.success)
      if (q.get('search')) logs = logs.filter(l => JSON.stringify(l).includes(q.get('search')!))
      const offset = Number(q.get('offset') ?? 0), limit = Number(q.get('limit') ?? 50)
      return route.fulfill({ json: { success: true, logs: logs.slice(offset, offset + limit), limit, offset } })
    })

    await page.goto('/audit')
    await expect(page.getByRole('main').getByRole('heading', { name: 'Audit Log' })).toBeVisible()
    await expect(page.getByText('Chain intact')).toBeVisible()
    await expect(page.getByText('100 entries shown')).toBeVisible()
    await expect(page.getByRole('cell', { name: 'mallory' })).toBeVisible()

    await page.getByRole('button', { name: 'Show older entries' }).click()
    await expect(page.getByText('130 entries shown')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Show older entries' })).toHaveCount(0)

    await page.getByLabel('Failures only').check()
    await expect(page.getByText('1 entry shown')).toBeVisible()
    await expect(page.getByRole('cell', { name: 'Wrong password' })).toBeVisible()
    await page.getByLabel('Failures only').uncheck()

    await page.getByLabel('Search the audit log').fill('mallory')
    await expect(page.getByText('1 entry shown')).toBeVisible()
    await page.getByLabel('Search the audit log').fill('nobody-by-that-name')
    await expect(page.getByText('No entries match.')).toBeVisible()

    expect(asked.some(a => a.includes('offset=100'))).toBe(true)
    expect(asked.some(a => a.includes('level=error'))).toBe(true)
    expect(asked.some(a => a.includes('search=mallory'))).toBe(true)
  })
})
