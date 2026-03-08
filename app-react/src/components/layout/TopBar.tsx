/**
 * components/layout/TopBar.tsx
 *
 * Fixed top bar showing the current page title and user controls.
 */

import { useRouterState } from '@tanstack/react-router'
import { Icon } from '@/components/ui/Icon'
import { useAuthStore } from '@/stores/auth'
import { findNavEntry } from './navConfig'

interface TopBarProps {
  sidebarCollapsed: boolean
}

export function TopBar({ sidebarCollapsed }: TopBarProps) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const user = useAuthStore((s) => s.user)

  const entry = findNavEntry(pathname)
  const pageTitle = entry?.leaf.label ?? (pathname === '/' ? 'Dashboard' : pathname.slice(1))

  const sidebarWidth = sidebarCollapsed ? 'var(--sidebar-width-collapsed)' : 'var(--sidebar-width)'

  return (
    <header
      style={{
        position: 'fixed',
        top: 0,
        left: sidebarWidth,
        right: 0,
        height: 'var(--topbar-height)',
        background: 'rgba(10,10,10,0.97)',
        borderBottom: '1px solid var(--border)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        padding: '0 24px',
        zIndex: 40,
        backdropFilter: 'blur(8px)',
        transition: 'left 0.2s ease',
      }}
    >
      {/* Page title */}
      <h1
        style={{
          fontSize: 'var(--text-xl)',
          fontWeight: 700,
          letterSpacing: '-0.5px',
          color: 'var(--text)',
        }}
      >
        {pageTitle}
      </h1>

      {/* Right: user chip */}
      <div style={{ display: 'flex', alignItems: 'center', gap: '12px' }}>
        {user && (
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '8px',
              padding: '5px 12px',
              background: 'var(--surface)',
              border: '1px solid var(--border)',
              borderRadius: 'var(--radius-full)',
              fontSize: 'var(--text-sm)',
              color: 'var(--text-secondary)',
            }}
          >
            <Icon name="account_circle" size={18} style={{ color: 'var(--primary)' }} />
            <span>{user.username}</span>
            {user.role && user.role !== 'user' && (
              <span
                style={{
                  fontSize: 'var(--text-xs)',
                  background: 'var(--primary-bg)',
                  color: 'var(--primary)',
                  padding: '2px 6px',
                  borderRadius: 'var(--radius-xs)',
                  fontWeight: 600,
                  textTransform: 'uppercase',
                  letterSpacing: '0.5px',
                }}
              >
                {user.role}
              </span>
            )}
          </div>
        )}
      </div>
    </header>
  )
}
