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
        background: 'hsla(var(--hue-bg), 18%, 4%, 0.7)',
        borderBottom: '1px solid var(--border-subtle)',
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        padding: '0 32px',
        zIndex: 40,
        backdropFilter: 'var(--blur-glass)',
        boxShadow: '0 4px 30px rgba(0, 0, 0, 0.4)',
        transition: 'left var(--transition-bounce)'}}
    >
      {/* ── Left: breadcrumb + title ── */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
        <div style={{
          width: 30, height: 30, borderRadius: 8,
          background: 'var(--primary-bg)',
          border: '1px solid rgba(138,156,255,0.2)',
          display: 'flex', alignItems: 'center', justifyContent: 'center'}}>
          <Icon name={pageIcon} size={16} style={{ color: 'var(--primary)' }} />
        </div>

        <div>
          {groupLabel && (
            <div style={{
              fontSize: 'var(--text-2xs)', color: 'var(--text-tertiary)',
              fontWeight: 600, textTransform: 'uppercase', letterSpacing: '0.6px',
              lineHeight: 1, marginBottom: 3}}>
              {groupLabel}
            </div>
          )}
          <h1 style={{
            fontSize: 'var(--text-md)', fontWeight: 700,
            letterSpacing: '-0.3px', color: 'var(--text)', lineHeight: 1}}>
            {pageTitle}
          </h1>
        </div>
      </div>

      {/* ── Right: user chip ── */}
      {user && (
        <div style={{
          display: 'flex', alignItems: 'center', gap: 10,
          padding: '6px 16px 6px 8px',
          background: 'hsla(0,0%,100%,0.03)',
          border: '1px solid var(--border-subtle)',
          borderRadius: 'var(--radius-full)',
          cursor: 'pointer',
          transition: 'all var(--transition-fast)',
          boxShadow: 'inset 0 1px 0 hsla(0,0%,100%,0.02)'}}
        onMouseEnter={(e) => {
          e.currentTarget.style.background = 'hsla(0,0%,100%,0.08)'
          e.currentTarget.style.borderColor = 'var(--border)'
        }}
        onMouseLeave={(e) => {
          e.currentTarget.style.background = 'hsla(0,0%,100%,0.03)'
          e.currentTarget.style.borderColor = 'var(--border-subtle)'
        }}>
          <div style={{
            width: 26, height: 26, borderRadius: '50%',
            background: 'linear-gradient(135deg, var(--primary) 0%, hsl(260, 100%, 75%) 100%)',
            display: 'flex', alignItems: 'center', justifyContent: 'center',
            fontSize: 12, fontWeight: 800, color: '#000', flexShrink: 0,
            boxShadow: '0 2px 8px var(--primary-glow)'}}>
            {user.username[0].toUpperCase()}
          </div>
          <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text)', fontWeight: 600 }}>
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
