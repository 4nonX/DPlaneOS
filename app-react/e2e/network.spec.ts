import { expect, test } from '@playwright/test'
import { MockApi } from './mockApi'

function network() {
  return {
    success: true, routes: [],
    dns: { nameservers: ['192.168.1.1'], search: ['.'] },
    interfaces: [
      { name: 'eno1', ip: '192.168.1.20', netmask: '255.255.255.0', gateway: '192.168.1.1', primary: true, up: true, type: 'ethernet', dhcp: false, managed: true },
      { name: 'eno2', up: true, type: 'ethernet', dhcp: true, managed: false },
      { name: 'eno3', up: true, type: 'ethernet', member_of: 'bond0' },
      { name: 'veth12ab', up: true, type: 'ethernet' },
    ],
  }
}

test('interfaces: static stays static; a change waits for confirmation', async ({ page }) => {
  const mock = new MockApi()
  let pending = false
  mock.on('GET', '/api/system/network', network())
  mock.on('GET', '/api/network/pending', () => pending
    ? { success: true, pending: true, change: 'eno1: 192.168.1.21/24', seconds_left: 80 }
    : { success: true, pending: false })
  mock.on('PUT', '/api/system/network', () => { pending = true; return { success: true, revert_seconds: 90 } })
  mock.on('POST', '/api/network/confirm', () => { pending = false; return { success: true } })
  await mock.install(page)
  await page.goto('/network')

  await expect(page.getByText('veth12ab')).toHaveCount(0) // container plumbing hidden
  await expect(page.getByText('IN BOND0')).toBeVisible()

  await page.getByRole('button', { name: 'Configure' }).first().click()
  // A static interface opens as static (it used to open with DHCP ticked).
  await expect(page.getByRole('checkbox')).not.toBeChecked()
  await page.getByPlaceholder('192.168.1.100').fill('192.168.1.21')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect.poll(() => mock.sent('PUT', '/api/system/network')).toEqual([
    { action: 'configure', interface: 'eno1', dhcp: false, ip: '192.168.1.21', netmask: '255.255.255.0', gateway: '192.168.1.1' }])

  const banner = page.getByRole('alert').filter({ hasText: 'Confirm the change' })
  await expect(banner).toContainText('eno1: 192.168.1.21/24')
  await banner.getByRole('button', { name: 'Keep' }).click()
  await expect.poll(() => mock.sent('POST', '/api/network/confirm').length).toBe(1)
  await expect(banner).toHaveCount(0)
})

test('VLAN with an address; DNS with search domains', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/system/network', network())
  mock.on('GET', '/api/network/pending', { success: true, pending: false })
  mock.on('GET', '/api/network/vlan', { success: true, vlans: [
    { name: 'eno1.30', parent: 'eno1', vlan_id: 30, managed: true, up: true },
    { name: 'eno2.9', managed: false, up: true },
  ] })
  mock.on('POST', '/api/network/vlan', { success: true })
  mock.on('PUT', '/api/system/network', { success: true })
  await mock.install(page)
  await page.goto('/network')

  await page.getByRole('button', { name: 'VLANs' }).click()
  await expect(page.getByRole('row').filter({ hasText: 'eno2.9' })).toContainText('system configuration')
  await page.getByRole('combobox').first().selectOption('eno1')
  await page.getByPlaceholder('10', { exact: true }).fill('40')
  await page.getByPlaceholder('10.0.10.5/24').fill('10.0.40.5/24')
  await page.getByRole('button', { name: 'Create VLAN' }).click()
  await expect.poll(() => mock.sent('POST', '/api/network/vlan')).toEqual([{ parent: 'eno1', vlan_id: 40, ip: '10.0.40.5/24', dhcp: false }])

  await page.getByRole('button', { name: 'DNS & NTP' }).click()
  await expect(page.getByPlaceholder('192.168.1.1, 9.9.9.9')).toHaveValue('192.168.1.1')
  await page.getByPlaceholder('home.lan').fill('home.lan')
  await page.getByRole('button', { name: 'Save' }).first().click()
  await expect.poll(() => mock.sent('PUT', '/api/system/network')).toEqual([{ action: 'set_dns', nameservers: ['192.168.1.1'], search: ['home.lan'] }])
})

test('firewall on NixOS: system ports, rules with a source, edit in one change', async ({ page }) => {
  const mock = new MockApi()
  mock.on('GET', '/api/network/pending', { success: true, pending: false })
  mock.on('GET', '/api/firewall/status', {
    success: true, status: 'active', managed_by: 'nixos', rules_loaded: true,
    system_ports: { enabled: true, tcp: [22, 80, 443, 445], udp: [137], tcp_ranges: [{ from: 40000, to: 40100 }], udp_ranges: [], trusted_interfaces: ['lo'] },
    rules: [{ id: 1, action: 'deny', port: '445', proto: 'tcp', from: '192.168.50.0/24' }],
  })
  mock.on('POST', '/api/firewall/rule', { success: true, message: 'firewall: applied; confirm to keep it' })
  await mock.install(page)
  await page.goto('/firewall')

  await expect(page.getByText('22, 80, 443, 445, 40000-40100')).toBeVisible()
  await expect(page.getByRole('button', { name: /^(Enable|Disable)$/ })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /Sync/ })).toHaveCount(0)

  await page.getByPlaceholder('80  or  8000-8100').first().fill('8096')
  await page.getByPlaceholder('192.168.1.0/24 (empty = any)').first().fill('192.168.1.0/24')
  await page.getByRole('button', { name: 'Add Rule' }).click()
  await expect.poll(() => mock.sent('POST', '/api/firewall/rule')).toEqual([{ action: 'allow', port: '8096', proto: 'tcp', from: '192.168.1.0/24' }])

  await page.getByRole('button', { name: 'Edit rule 1' }).click()
  await page.getByRole('dialog').getByPlaceholder('192.168.1.0/24 (empty = any)').fill('192.168.60.0/24')
  await page.getByRole('dialog').getByRole('button', { name: 'Save' }).click()
  await expect.poll(() => mock.sent('POST', '/api/firewall/rule').length).toBe(2)
  expect(mock.sent('POST', '/api/firewall/rule')[1]).toEqual({ action: 'update', rule_num: 1, new_action: 'deny', port: '445', proto: 'tcp', from: '192.168.60.0/24' })
})
