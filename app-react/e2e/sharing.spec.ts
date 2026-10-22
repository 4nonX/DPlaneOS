import { expect, test } from '@playwright/test'
import { MockApi } from './mockApi'

test('users show whether they can use file sharing', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/rbac/users', { success: true, users: [
    { id: 1, username: 'admin', role: 'admin', active: true, created_at: '', file_sharing: { uid: 3000, smb: true } },
    { id: 2, username: 'bob', role: 'user', active: true, created_at: '', file_sharing: { uid: 3001, smb: false, note: 'set the password again to use SMB' } },
    { id: 3, username: 'carol', role: 'user', active: false, created_at: '', file_sharing: { uid: 3002, smb: false, note: 'the account is disabled' } },
  ] })
  await mock.install(page)
  await page.goto('/users')

  const row = (n: string) => page.getByRole('row').filter({ hasText: n })
  await expect(row('admin')).toContainText('SMB and NFS (UID 3000)')
  await expect(row('bob')).toContainText('set the password again to use SMB')
  await expect(row('carol')).toContainText('Disabled')
})

test('a new share gives its users access to the folder', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/shares/list', { success: true, shares: [] })
  mock.on('POST', '/api/shares', { success: true, id: 1, access: 'access given to alice, group media' })
  await mock.install(page)
  await page.goto('/shares')

  await page.getByRole('button', { name: 'Add Share' }).click()
  await page.getByPlaceholder('e.g. media').fill('media')
  await page.getByPlaceholder('/tank/media').fill('/tank/media')
  await page.getByPlaceholder('alice bob @media').fill('alice @media')
  await expect(page.getByText('Give these users access to the folder')).toBeVisible()
  await page.getByRole('button', { name: 'Create Share' }).click()
  await expect.poll(() => (mock.sent('POST', '/api/shares')[0] as { grant_access?: boolean } | undefined)?.grant_access).toBe(true)
  await expect(page.getByText('Folder: access given to alice, group media')).toBeVisible()
})
