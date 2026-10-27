import { expect, test } from '@playwright/test'
import { MockApi, Status } from './mockApi'

// The daemon's CSRF rules: /api/csrf needs a session (401 without one) and
// gives it a new token each call; a change is accepted only with the
// session's current token.
function daemonCsrf(mock: MockApi) {
  let n = 0
  const current: Record<string, string> = {}
  let gate: Promise<void> = Promise.resolve()
  mock.on('GET', '/api/csrf', async ({ headers }) => {
    await gate
    const sid = headers['x-session-id']
    if (!sid) return new Status(401, { success: false, error: 'No session' })
    current[sid] = `tok-${sid}-${++n}`
    return { success: true, csrf_token: current[sid] }
  })
  return {
    /** Another tab fetched a token: the one this page holds is stale. */
    rotate: (sid: string) => { current[sid] = `tok-${sid}-${++n}` },
    /** Holds token requests until the returned function is called. */
    hold: () => { let release = () => {}; gate = new Promise(r => { release = r }); return release },
    check: (headers: Record<string, string>) => {
      const sid = headers['x-session-id'], token = headers['x-csrf-token']
      return !!sid && !!token && token === current[sid]
    },
  }
}

const refused = () => new Status(403, { error: 'Forbidden (Invalid CSRF token)' })

async function signIn(mock: MockApi, page: import('@playwright/test').Page) {
  mock.on('GET', '/api/system/status', { success: true, first_run: false })
  mock.on('GET', '/api/auth/oidc/info', { success: true, enabled: false })
  mock.on('POST', '/api/auth/login', { success: true, session_id: 's1', username: 'admin' })
  mock.on('GET', '/api/nixos/diff-intent', { changes: [] })
  mock.on('GET', '/api/system/settings', { success: true, settings: { hostname: 'nas', timezone: 'UTC', motd: '' } })
  // The support page.
  mock.on('GET', '/api/health', { success: true, overall: 'ok', subsystems: {} })
  mock.on('GET', '/api/system/audit/verify-chain', { success: true, valid: true, checked: 0 })
  mock.on('GET', '/api/rbac/roles', { success: true, roles: [] })
  mock.on('GET', '/api/zfs/encryption/list', { success: true, datasets: [] })
  mock.on('GET', '/api/nixos/pre-upgrade-snapshots', { success: true, snapshots: [] })
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
    csrf.check(headers) ? { success: true } : refused())
  await signIn(mock, page)

  await page.getByRole('button', { name: 'Save Settings' }).click()
  await expect(page.getByText(/Settings (staged|stored)/)).toBeVisible()
  expect(saves(mock).map(c => c.headers['x-session-id'])).toEqual(['s1'])
})

test('a token another tab replaced is fetched again and the change sent once more', async ({ page }) => {
  const mock = new MockApi()
  const csrf = daemonCsrf(mock)
  mock.on('POST', '/api/system/settings', ({ headers }) =>
    csrf.check(headers) ? { success: true } : refused())
  await signIn(mock, page)

  await page.getByRole('button', { name: 'Save Settings' }).click()
  await expect(page.getByText(/Settings (staged|stored)/)).toBeVisible()
  csrf.rotate('s1')
  await page.getByRole('button', { name: 'Save Settings' }).click()
  await expect.poll(() => saves(mock).length).toBe(3)
  const tokens = saves(mock).map(c => c.headers['x-csrf-token'])
  expect(tokens[1]).toBe(tokens[0]) // stale, refused
  expect(tokens[2]).not.toBe(tokens[1]) // fetched again, accepted
  expect(saves(mock).map(c => c.headers['x-session-id'])).toEqual(['s1', 's1', 's1'])
  await expect(page.getByText('Forbidden (Invalid CSRF token)')).toHaveCount(0)
})

const go = (page: import('@playwright/test').Page, path: string) => page.evaluate(p => {
  window.history.pushState({}, '', p)
  window.dispatchEvent(new PopStateEvent('popstate'))
}, path)

const bundles = (mock: MockApi) => mock.calls.filter(c => c.method === 'POST' && c.path === '/api/system/support-bundle')

test('a download made outside the API client also fetches a replaced token again', async ({ page }) => {
  const mock = new MockApi()
  const csrf = daemonCsrf(mock)
  mock.on('POST', '/api/system/settings', ({ headers }) => csrf.check(headers) ? { success: true } : refused())
  mock.on('POST', '/api/system/support-bundle', ({ headers }) => csrf.check(headers) ? 'bundle' : refused())
  await signIn(mock, page)
  await page.getByRole('button', { name: 'Save Settings' }).click()
  await expect(page.getByText(/Settings (staged|stored)/)).toBeVisible()

  csrf.rotate('s1')
  await go(page, '/support')
  await page.getByRole('button', { name: 'Download Bundle' }).click()
  await expect(page.getByText('Support bundle downloaded')).toBeVisible()
  const sent = bundles(mock).map(c => c.headers['x-csrf-token'])
  expect(sent).toHaveLength(2)
  expect(sent[1]).not.toBe(sent[0])
})

test('changes started together share one token request', async ({ page }) => {
  const mock = new MockApi()
  const csrf = daemonCsrf(mock)
  mock.on('POST', '/api/system/settings', ({ headers }) => csrf.check(headers) ? { success: true } : refused())
  mock.on('POST', '/api/system/support-bundle', ({ headers }) => csrf.check(headers) ? 'bundle' : refused())
  await signIn(mock, page)

  const release = csrf.hold()
  await page.getByRole('button', { name: 'Save Settings' }).click() // waits for the token
  await go(page, '/support')
  await page.getByRole('button', { name: 'Download Bundle' }).click() // waits for the same token
  await expect.poll(() => mock.calls.filter(c => c.path === '/api/csrf').length).toBe(1)
  release()
  await expect(page.getByText('Support bundle downloaded')).toBeVisible()
  await expect.poll(() => saves(mock).length).toBe(1)
  expect(mock.calls.filter(c => c.path === '/api/csrf')).toHaveLength(1)
  expect(bundles(mock)).toHaveLength(1)
  expect(bundles(mock)[0].headers['x-csrf-token']).toBe(saves(mock)[0].headers['x-csrf-token'])
})
