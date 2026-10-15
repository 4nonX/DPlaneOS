/**
 * A mocked daemon for browser tests. Tests register handlers per method and
 * path; every request is recorded so a test can check what the page sent.
 * Requests nobody handles answer {"success": true} (shell widgets that a test
 * does not care about) and are listed in `unhandled` so a test can assert on
 * them when they matter.
 */

import type { Page, Request } from '@playwright/test'

export type Handler = (req: { body: any; query: URLSearchParams; params: Record<string, string> }) => unknown | Promise<unknown>

export interface Call { method: string; path: string; body: any }

export class MockApi {
  calls: Call[] = []
  unhandled: string[] = []
  private routes: { method: string; re: RegExp; keys: string[]; fn: Handler }[] = []

  /** on('GET', '/api/groups/:name', fn) */
  on(method: string, pattern: string, fn: Handler | object) {
    const keys: string[] = []
    const re = new RegExp('^' + pattern.replace(/[.?]/g, '\\$&').replace(/:(\w+)/g, (_, k) => { keys.push(k); return '([^/]+)' }) + '$')
    this.routes.unshift({ method, re, keys, fn: typeof fn === 'function' ? fn as Handler : () => fn })
    return this
  }

  /** The bodies sent to method+path, in order. */
  sent(method: string, path: string) {
    return this.calls.filter(c => c.method === method && c.path === path).map(c => c.body)
  }

  async install(page: Page) {
    // A logged-in admin session.
    await page.addInitScript(() => {
      sessionStorage.setItem('session_id', 'e2e-session')
      sessionStorage.setItem('username', 'admin')
    })
    this.on('GET', '/api/csrf', { success: true, csrf_token: 'e2e-csrf' })
    this.on('GET', '/api/auth/check', { success: true, authenticated: true })
    this.on('GET', '/api/auth/session', {
      success: true,
      user: { id: 1, username: 'admin', email: 'admin@example.lan', role: 'admin', must_change_password: false, source: 'local' },
    })
    await page.route(url => new URL(url).pathname.startsWith('/api/'), async route => {
      const req: Request = route.request()
      const url = new URL(req.url())
      let body: any = null
      const raw = req.postData()
      if (raw) {
        try { body = JSON.parse(raw) } catch { body = raw }
      }
      this.calls.push({ method: req.method(), path: url.pathname, body })
      for (const r of this.routes) {
        if (r.method !== req.method()) continue
        const m = r.re.exec(url.pathname)
        if (!m) continue
        const params: Record<string, string> = {}
        r.keys.forEach((k, i) => { params[k] = decodeURIComponent(m[i + 1]) })
        const res = await r.fn({ body, query: url.searchParams, params })
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(res) })
        return
      }
      this.unhandled.push(`${req.method()} ${url.pathname}`)
      await route.fulfill({ status: 200, contentType: 'application/json', body: '{"success":true}' })
    })
    // No live updates in tests: accept the WebSocket and never send.
    await page.routeWebSocket(url => new URL(url).pathname.startsWith('/ws'), () => {})
  }
}
