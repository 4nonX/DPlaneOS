/**
 * components/layout/TopBar.tsx
 *
 * Fixed top bar with page icon, title, breadcrumb group, and user chip.
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
  const user     = useAuthStore((s) => s.user)

  const entry     = findNavEntry(pathname)
  const pageTitle = entry?.leaf.label ?? (pathname === '/' ? 'Dashboard' : pathname.slice(1))
  const pageIcon  = entry?.leaf.icon  ?? 'home'
  const groupLabel = entry?.groupLabel

  return (
    <header
      style={{
        position: 'fixed', top: 0,
        left: sidebarCollapsed ? 'var(--sidebar-width-collapsed)' : 'var(--sidebar-width)',
        right: 0, height: 'var(--topbar-height)',
        background: 'rgba(8,8,8,0.96)',
        borderBottom: '1px solid var(--border)',
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        padding: '0 24px',
        zIndex: 40,
        backdropFilter: 'blur(12px)',
        transition: 'left 0.22s cubic-bezier(0.4,0,0.2,1)',
      }}
    >
      {/* ── Left: breadcrumb + title ── */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <div style={{
          width: 30, height: 30, borderRadius: 8,
          background: 'var(--primary-bg)',
          border: '1px solid rgba(138,156,255,0.2)',
          display: 'flex', alignItems: 'center', justifyContent: 'center',
        }}>
          <Icon name={pageIcon} size={16} style={{ color: 'var(--primary)' }} />
        </div>

        <div>
          {groupLabel && (
            <div style={{
              fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)',
              fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.6px',
              lineHeight: 1, marginBottom: 3,
            }}>
              {groupLabel}
            </div>
          )}
          <h1 style={{
            fontSize: 'var(--text-md)', fontWeight: 700,
            letterSpacing: '-0.3px', color: 'var(--text)', lineHeight: 1,
          }}>
            {pageTitle}
          </h1>
        </div>
      </div>

      {/* ── Right: user chip ── */}
      {user && (
        <div style={{
          display: 'flex', alignItems: 'center', gap: 8,
          padding: '5px 12px 5px 8px',
          background: 'var(--surface)',
          border: '1px solid var(--border)',
          borderRadius: 'var(--radius-full)',
        }}>
          <div style={{
            width: 22, height: 22, borderRadius: '50%',
            background: 'linear-gradient(135deg, var(--primary) 0%, #6b7fff 100%)',
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            fontSize: 10, fontWeight: 800, color: '#000', flexShrink: 0,
          }}>
            {user.username[0].toUpperCase()}
          </div>
          <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', fontWeight: 500 }}>
            {user.username}
          </span>
          {user.role && user.role !== 'user' && (
            <span className="badge badge-primary" style={{ fontSize: 9 }}>
              {user.role}
            </span>
          )}
        </div>
      )}
    </header>
  )
}
