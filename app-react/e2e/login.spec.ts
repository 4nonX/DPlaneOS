import { expect, test } from '@playwright/test'
import { MockApi, Status } from './mockApi'

// The daemon's CSRF rules: /api/csrf needs a session (401 without one) and
// gives it a new token each call; a change is accepted only with the
// session's current token.
function daemonCsrf(mock: MockApi) {
  let n = 0
  const current: Record<string, string> = {}
  mock.on('GET', '/api/csrf', ({ headers }) => {
    const sid = headers['x-session-id']
    if (!sid) return new Status(401, { success: false, error: 'No session' })
    current[sid] = `tok-${sid}-${++n}`
    return { success: true, csrf_token: current[sid] }
  })
  return {
    /** Another tab fetched a token: the one this page holds is stale. */
    rotate: (sid: string) => { current[sid] = `tok-${sid}-${++n}` },
    check: (headers: Record<string, string>) => headers['x-csrf-token'] === current[headers['x-session-id']],
  }
}

async function signIn(mock: MockApi, page: import('@playwright/test').Page) {
  mock.on('GET', '/api/system/status', { success: true, first_run: false })
  mock.on('GET', '/api/auth/oidc/info', { success: true, enabled: false })
  mock.on('POST', '/api/auth/login', { success: true, session_id: 's1', username: 'admin' })
  mock.on('GET', '/api/nixos/diff-intent', { changes: [] })
  mock.on('GET', '/api/system/settings', { success: true, settings: { hostname: 'nas', timezone: 'UTC', motd: '' } })
  await mock.install(page, { loggedIn: false })
  await page.goto('/login')

  await page.locator('input[autocomplete="username"]').fill('admin')
  await page.locator('input[autocomplete="current-password"]').fill('Secret-Pass-1')
  const button = page.getByRole('button', { name: 'Sign In', exact: true })
  await expect(button).toBeEnabled()
  await button.click()
  await expect(page).not.toHaveURL(/\/login/)
  const login = mock.calls.find(c => c.method === 'POST' && c.path === '/api/auth/login')!
  expect(login.headers['x-csrf-token']).toBeUndefined()

  // Into the app without a reload, as a user clicks through.
  await page.evaluate(() => {
    window.history.pushState({}, '', '/settings')
    window.dispatchEvent(new PopStateEvent('popstate'))
  })
}

const saves = (mock: MockApi) => mock.calls.filter(c => c.method === 'POST' && c.path === '/api/system/settings')

test('sign in, then save a setting without reloading the page', async ({ page }) => {
  const mock = new MockApi()
  const csrf = daemonCsrf(mock)
  mock.on('POST', '/api/system/settings', ({ headers }) =>
    csrf.check(headers) ? { success: true } : new Status(403, { error: 'Forbidden (Invalid CSRF token)' }))
  await signIn(mock, page)

  await page.getByRole('button', { name: 'Save Settings' }).click()
  await expect(page.getByText(/Settings (staged|stored)/)).toBeVisible()
  expect(saves(mock).map(c => c.headers['x-session-id'])).toEqual(['s1'])
})

test('a token another tab replaced is fetched again and the change sent once more', async ({ page }) => {
  const mock = new MockApi()
  const csrf = daemonCsrf(mock)
  mock.on('POST', '/api/system/settings', ({ headers }) =>
    csrf.check(headers) ? { success: true } : new Status(403, { error: 'Forbidden (Invalid CSRF token)' }))
  await signIn(mock, page)

  await page.getByRole('button', { name: 'Save Settings' }).click()
  await expect(page.getByText(/Settings (staged|stored)/)).toBeVisible()
  csrf.rotate('s1')
  await page.getByRole('button', { name: 'Save Settings' }).click()
  await expect.poll(() => saves(mock).length).toBe(3)
  const tokens = saves(mock).map(c => c.headers['x-csrf-token'])
  expect(tokens[1]).toBe(tokens[0]) // stale, refused
  expect(tokens[2]).not.toBe(tokens[1]) // fetched again, accepted
  await expect(page.getByText('Forbidden (Invalid CSRF token)')).toHaveCount(0)
})
