/**
 * routes/index.tsx — D-PlaneOS v4 Router
 *
 * All routes declared inline (no makeRoute helper) so TanStack Router
 * can infer exact path literals and provide type-safe navigation.
 *
 * Protected routes are children of `protectedRoute` which runs an auth
 * guard via beforeLoad — redirects to /login if not authenticated.
 */

import React from 'react'
import {
  createRouter,
  createRoute,
  createRootRoute,
  redirect,
  Outlet,
} from '@tanstack/react-router'
import { useAuthStore } from '@/stores/auth'
import { AppShell }          from '@/components/layout/AppShell'
import { LoginPage }         from '@/pages/LoginPage'
import { SetupWizardPage }   from '@/pages/SetupWizardPage'
import { DashboardPage }     from '@/pages/DashboardPage'
import { ReportingPage }     from '@/pages/ReportingPage'
import { HardwarePage }      from '@/pages/HardwarePage'
import { LogsPage }          from '@/pages/LogsPage'
import { MonitoringPage }    from '@/pages/MonitoringPage'
import { PoolsPage }         from '@/pages/PoolsPage'
import { SharesPage }        from '@/pages/SharesPage'
import { NFSPage }           from '@/pages/NFSPage'
import { SnapshotSchedulerPage } from '@/pages/SnapshotSchedulerPage'
import { ReplicationPage }   from '@/pages/ReplicationPage'
import { FilesPage }         from '@/pages/FilesPage'
import { QuotasPage }        from '@/pages/QuotasPage'
import { ACLPage }           from '@/pages/ACLPage'
import { ISCSIPage }         from '@/pages/ISCSIPage'
import { CloudSyncPage }     from '@/pages/CloudSyncPage'
import { DockerPage }        from '@/pages/DockerPage'
import { ModulesPage }       from '@/pages/ModulesPage'
import { GitSyncPage }       from '@/pages/GitSyncPage'
import { GitOpsPage }        from '@/pages/GitOpsPage'
import { NetworkPage }       from '@/pages/NetworkPage'
import { RemovableMediaPage} from '@/pages/RemovableMediaPage'
import { UsersPage }         from '@/pages/UsersPage'
import { DirectoryPage }     from '@/pages/DirectoryPage'
import { SecurityPage }      from '@/pages/SecurityPage'
import { AuditPage }         from '@/pages/AuditPage'
import { FirewallPage }      from '@/pages/FirewallPage'
import { CertificatesPage }  from '@/pages/CertificatesPage'
import { SettingsPage }      from '@/pages/SettingsPage'
import { UpdatesPage }       from '@/pages/UpdatesPage'
import { AlertsPage }        from '@/pages/AlertsPage'
import { UPSPage }           from '@/pages/UPSPage'
import { PowerPage }         from '@/pages/PowerPage'
import { IPMIPage }          from '@/pages/IPMIPage'
import { HAPage }            from '@/pages/HAPage'
import { SupportPage }       from '@/pages/SupportPage'
import { TerminalPage }      from '@/pages/TerminalPage'
import { SandboxPage }       from '@/pages/SandboxPage'
import { DelegationPage }    from '@/pages/DelegationPage'
import { getPluginRoutes }   from '@/plugins'

// Silence unused React import warning (needed for JSX in some tsx files)
void React

// ── Root ────────────────────────────────────────────────────────────────────

const rootRoute = createRootRoute({ component: Outlet })

// ── Public ──────────────────────────────────────────────────────────────────

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/login',
  component: LoginPage,
  beforeLoad: () => {
    if (useAuthStore.getState().isAuthenticated) throw redirect({ to: '/' })
  },
})

const setupRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/setup',
  component: SetupWizardPage,
})

// ── Protected layout ────────────────────────────────────────────────────────

const protectedRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: 'protected',
  component: AppShell,
  beforeLoad: () => {
    if (!useAuthStore.getState().isAuthenticated) throw redirect({ to: '/login' })
  },
})

// ── Protected children ──────────────────────────────────────────────────────

const dashboardRoute  = createRoute({ getParentRoute: () => protectedRoute, path: '/',              component: DashboardPage })
const poolsRoute      = createRoute({ getParentRoute: () => protectedRoute, path: '/pools',         component: PoolsPage })
const sharesRoute     = createRoute({ getParentRoute: () => protectedRoute, path: '/shares',        component: SharesPage })
const nfsRoute        = createRoute({ getParentRoute: () => protectedRoute, path: '/nfs',           component: NFSPage })
const snapshotsRoute  = createRoute({ getParentRoute: () => protectedRoute, path: '/snapshots',     component: SnapshotSchedulerPage })
const replRoute       = createRoute({ getParentRoute: () => protectedRoute, path: '/replication',   component: ReplicationPage })
const filesRoute      = createRoute({ getParentRoute: () => protectedRoute, path: '/files',         component: FilesPage })
const quotasRoute     = createRoute({ getParentRoute: () => protectedRoute, path: '/quotas',        component: QuotasPage })
const aclRoute        = createRoute({
  getParentRoute: () => protectedRoute,
  path: '/acl',
  validateSearch: (search: Record<string, unknown>): { path?: string } => ({
    path: typeof search.path === 'string' ? search.path : undefined,
  }),
  component: ACLPage,
})
const iscsiRoute      = createRoute({ getParentRoute: () => protectedRoute, path: '/iscsi',         component: ISCSIPage })
const cloudRoute      = createRoute({ getParentRoute: () => protectedRoute, path: '/cloud-sync',    component: CloudSyncPage })
const sandboxRoute    = createRoute({ getParentRoute: () => protectedRoute, path: '/sandbox',       component: SandboxPage })
const delegationRoute = createRoute({ getParentRoute: () => protectedRoute, path: '/delegation',    component: DelegationPage })
const dockerRoute     = createRoute({ getParentRoute: () => protectedRoute, path: '/docker',        component: DockerPage })
const modulesRoute    = createRoute({ getParentRoute: () => protectedRoute, path: '/modules',       component: ModulesPage })
const gitSyncRoute    = createRoute({ getParentRoute: () => protectedRoute, path: '/git-sync',      component: GitSyncPage })
const gitOpsRoute     = createRoute({ getParentRoute: () => protectedRoute, path: '/gitops',        component: GitOpsPage })
const networkRoute    = createRoute({ getParentRoute: () => protectedRoute, path: '/network',       component: NetworkPage })
const removableRoute  = createRoute({ getParentRoute: () => protectedRoute, path: '/removable',     component: RemovableMediaPage })
const usersRoute      = createRoute({ getParentRoute: () => protectedRoute, path: '/users',         component: UsersPage })
const directoryRoute  = createRoute({ getParentRoute: () => protectedRoute, path: '/directory',     component: DirectoryPage })
const securityRoute   = createRoute({ getParentRoute: () => protectedRoute, path: '/security',      component: SecurityPage })
const auditRoute      = createRoute({ getParentRoute: () => protectedRoute, path: '/audit',         component: AuditPage })
const firewallRoute   = createRoute({ getParentRoute: () => protectedRoute, path: '/firewall',      component: FirewallPage })
const certsRoute      = createRoute({ getParentRoute: () => protectedRoute, path: '/certificates',  component: CertificatesPage })
const settingsRoute   = createRoute({ getParentRoute: () => protectedRoute, path: '/settings',      component: SettingsPage })
const updatesRoute    = createRoute({ getParentRoute: () => protectedRoute, path: '/updates',       component: UpdatesPage })
const alertsRoute     = createRoute({ getParentRoute: () => protectedRoute, path: '/alerts',        component: AlertsPage })
const upsRoute        = createRoute({ getParentRoute: () => protectedRoute, path: '/ups',           component: UPSPage })
const powerRoute      = createRoute({ getParentRoute: () => protectedRoute, path: '/power',         component: PowerPage })
const hardwareRoute   = createRoute({ getParentRoute: () => protectedRoute, path: '/hardware',      component: HardwarePage })
const ipmiRoute       = createRoute({ getParentRoute: () => protectedRoute, path: '/ipmi',          component: IPMIPage })
const haRoute         = createRoute({ getParentRoute: () => protectedRoute, path: '/ha',            component: HAPage })
const monitoringRoute = createRoute({ getParentRoute: () => protectedRoute, path: '/monitoring',    component: MonitoringPage })
const logsRoute       = createRoute({ getParentRoute: () => protectedRoute, path: '/logs',          component: LogsPage })
const reportingRoute  = createRoute({ getParentRoute: () => protectedRoute, path: '/reporting',     component: ReportingPage })
const supportRoute    = createRoute({ getParentRoute: () => protectedRoute, path: '/support',       component: SupportPage })
const terminalRoute   = createRoute({ getParentRoute: () => protectedRoute, path: '/terminal',      component: TerminalPage })

// ── Route tree ───────────────────────────────────────────────────────────────

const routeTree = rootRoute.addChildren([
  loginRoute,
  setupRoute,
  protectedRoute.addChildren([
    dashboardRoute, poolsRoute, sharesRoute, nfsRoute, snapshotsRoute, replRoute,
    filesRoute, quotasRoute, aclRoute, iscsiRoute, cloudRoute,
    sandboxRoute, delegationRoute,
    dockerRoute, modulesRoute, gitSyncRoute, gitOpsRoute,
    networkRoute, removableRoute,
    usersRoute, directoryRoute,
    securityRoute, auditRoute, firewallRoute, certsRoute,
    settingsRoute, updatesRoute, alertsRoute, upsRoute, powerRoute,
    hardwareRoute, ipmiRoute, haRoute, monitoringRoute, logsRoute,
    reportingRoute, supportRoute, terminalRoute,
    ...getPluginRoutes(protectedRoute),
  ]),
])

// ── Router instance ──────────────────────────────────────────────────────────

export const router = createRouter({ routeTree, defaultPreload: 'intent' })

declare module '@tanstack/react-router' {
  interface Register { router: typeof router }
}
