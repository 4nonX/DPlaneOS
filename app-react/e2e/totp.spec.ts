import { expect, test } from '@playwright/test'
import { MockApi } from './mockApi'

// A 1×1 PNG: the daemon draws the real QR code.
const PNG = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=='

test('2FA setup shows the QR code drawn by the daemon, and nothing else is asked for it', async ({ page }) => {
  const external: string[] = []
  page.on('request', r => { if (!r.url().startsWith('http://localhost') && !r.url().startsWith('http://127.0.0.1') && !r.url().startsWith('data:')) external.push(r.url()) })
  const mock = new MockApi()
  mock.on('GET', '/api/auth/totp/setup', { success: true, enabled: false, secret: 'JBSWY3DPEHPK3PXP',
    otpauth_uri: 'otpauth://totp/DPlaneOS:admin?secret=JBSWY3DPEHPK3PXP&issuer=DPlaneOS', qr_png: PNG })
  await mock.install(page)
  await page.goto('/security')
  await page.getByRole('button', { name: '2FA / TOTP' }).click()
  await page.getByRole('button', { name: 'Setup TOTP' }).click()

  await expect(page.getByAltText('TOTP QR Code')).toHaveAttribute('src', PNG)
  await expect(page.getByText('JBSW Y3DP EHPK 3PXP')).toBeVisible() // the manual entry key, in groups of four
  expect(external, 'the TOTP secret must not leave the machine').toEqual([])
})
