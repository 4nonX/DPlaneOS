/**
 * components/layout/navConfig.ts
 *
 * Single source of truth for the sidebar navigation structure.
 * Matches the existing nav-shared.js page map exactly.
 * Routes match the TanStack Router route definitions in routes/index.tsx.
 */

export interface NavLeaf {
  kind: 'leaf'
  id: string
  label: string
  icon: string
  route: string
}

export interface NavGroup {
  kind: 'group'
  id: string
  label: string
  icon: string
  children: NavLeaf[]
}

export type NavItem = NavLeaf | NavGroup

import { pluginNavInject } from '../../plugins'

// ... existing code ...

const initialNav: NavItem[] = [
  {
    kind: 'leaf',
    id: 'dashboard',
    label: 'Dashboard',
    icon: 'dashboard',
    route: '/',
  },
  {
    kind: 'group',
    id: 'storage',
    label: 'Storage',
    icon: 'database',
    children: [
      { kind: 'leaf', id: 'pools',        label: 'ZFS Pools',         icon: 'water',              route: '/pools' },
      { kind: 'leaf', id: 'shares',       label: 'Shares',            icon: 'folder_shared',      route: '/shares' },
      { kind: 'leaf', id: 'nfs',          label: 'NFS Exports',       icon: 'lan',                route: '/nfs' },
      { kind: 'leaf', id: 'snapshots',    label: 'Snapshot Scheduler',icon: 'schedule',           route: '/snapshots' },
      { kind: 'leaf', id: 'replication',  label: 'Replication',       icon: 'sync',               route: '/replication' },
      { kind: 'leaf', id: 'files',        label: 'File Explorer',     icon: 'folder_open',        route: '/files' },
      { kind: 'leaf', id: 'quotas',       label: 'Quotas',            icon: 'pie_chart',          route: '/quotas' },
      { kind: 'leaf', id: 'acl',          label: 'ACL Manager',       icon: 'admin_panel_settings',route: '/acl' },
      { kind: 'leaf', id: 'iscsi',        label: 'iSCSI Targets',     icon: 'storage',            route: '/iscsi' },
      { kind: 'leaf', id: 'cloud-sync',   label: 'Cloud Sync',        icon: 'cloud_sync',         route: '/cloud-sync' },
      { kind: 'leaf', id: 'sandbox',      label: 'Sandbox',           icon: 'science',            route: '/sandbox' },
      { kind: 'leaf', id: 'delegation',   label: 'ZFS Delegation',    icon: 'lock_open',          route: '/delegation' },
    ],
  },
  {
    kind: 'group',
    id: 'compute',
    label: 'Compute',
    icon: 'deployed_code',
    children: [
      { kind: 'leaf', id: 'docker',    label: 'Docker',       icon: 'developer_board', route: '/docker' },
      { kind: 'leaf', id: 'modules',   label: 'App Modules',  icon: 'extension',       route: '/modules' },
      { kind: 'leaf', id: 'git-sync',  label: 'Git Sync',     icon: 'merge',           route: '/git-sync' },
      { kind: 'leaf', id: 'gitops',    label: 'GitOps State', icon: 'account_tree',    route: '/gitops' },
    ],
  },
  {
    kind: 'group',
    id: 'network',
    label: 'Network',
    icon: 'lan',
    children: [
      { kind: 'leaf', id: 'network',   label: 'Network',       icon: 'settings_ethernet', route: '/network' },
      { kind: 'leaf', id: 'removable', label: 'Removable Media', icon: 'usb',             route: '/removable' },
    ],
  },
  {
    kind: 'group',
    id: 'identity',
    label: 'Identity',
    icon: 'manage_accounts',
    children: [
      { kind: 'leaf', id: 'users',     label: 'Users & Groups',    icon: 'group',  route: '/users' },
      { kind: 'leaf', id: 'directory', label: 'Directory Service', icon: 'domain', route: '/directory' },
    ],
  },
  {
    kind: 'group',
    id: 'security',
    label: 'Security',
    icon: 'security',
    children: [
      { kind: 'leaf', id: 'security',     label: 'Security Settings', icon: 'shield',             route: '/security' },
      { kind: 'leaf', id: 'firewall',     label: 'Firewall',          icon: 'local_fire_department', route: '/firewall' },
      { kind: 'leaf', id: 'certificates', label: 'Certificates',      icon: 'verified_user',      route: '/certificates' },
    ],
  },
  {
    kind: 'group',
    id: 'system',
    label: 'System',
    icon: 'tune',
    children: [
      { kind: 'leaf', id: 'settings',   label: 'Settings',         icon: 'tune',                  route: '/settings' },
      { kind: 'leaf', id: 'updates',    label: 'System Updates',   icon: 'upgrade',               route: '/updates' },
      { kind: 'leaf', id: 'logs',       label: 'Logs',             icon: 'description',           route: '/logs' },
      { kind: 'leaf', id: 'reporting',  label: 'Reporting',        icon: 'monitoring',            route: '/reporting' },
      { kind: 'leaf', id: 'alerts',     label: 'Alerts',           icon: 'notifications_active',  route: '/alerts' },
      { kind: 'leaf', id: 'ups',        label: 'UPS',              icon: 'battery_charging_full', route: '/ups' },
      { kind: 'leaf', id: 'power',      label: 'Power Management', icon: 'power_settings_new',    route: '/power' },
      { kind: 'leaf', id: 'hardware',   label: 'Hardware',         icon: 'memory',                route: '/hardware' },
      { kind: 'leaf', id: 'ipmi',       label: 'IPMI',             icon: 'devices_other',         route: '/ipmi' },
      { kind: 'leaf', id: 'ha',         label: 'HA Cluster',       icon: 'device_hub',            route: '/ha' },
      { kind: 'leaf', id: 'monitoring', label: 'Monitoring',       icon: 'monitor_heart',         route: '/monitoring' },
      { kind: 'leaf', id: 'support',    label: 'Support',          icon: 'support_agent',         route: '/support' },
      { kind: 'leaf', id: 'terminal',   label: 'Terminal',         icon: 'terminal',               route: '/terminal' },
    ],
  },
]

export const NAV = [...initialNav]
pluginNavInject(NAV)

/**
 * Returns the NavLeaf for a given route path, plus its parent group id (if any).
 * Used by the sidebar to auto-expand the active group and highlight the active leaf.
 */
export function findNavEntry(route: string): { leaf: NavLeaf; groupId: string | null; groupLabel: string | null } | null {
  for (const item of NAV) {
    if (item.kind === 'leaf' && item.route === route) {
      return { leaf: item, groupId: null, groupLabel: null }
    }
    if (item.kind === 'group') {
      for (const child of item.children) {
        if (child.route === route) {
          return { leaf: child, groupId: item.id, groupLabel: item.label }
        }
      }
    }
  }
  return null
}
