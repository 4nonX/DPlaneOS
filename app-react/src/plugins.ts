import type { NavItem } from './components/layout/navConfig'

// Open-source version has no injected enterprise plugins
export function pluginNavInject(_nav: NavItem[]) {}

export function getPluginRoutes(_protectedRoute: any): any[] {
  return []
}
