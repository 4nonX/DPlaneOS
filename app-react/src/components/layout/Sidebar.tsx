/**
 * components/layout/Sidebar.tsx
 *
 * Collapsible sidebar navigation.
 * - Auto-expands the group containing the active route.
 * - Highlights the active leaf.
 * - Collapses to icon-only at narrow widths or when toggled.
 */

import { useState, useEffect } from 'react'
import { useRouter, useRouterState } from '@tanstack/react-router'
import { NAV, findNavEntry, type NavGroup, type NavLeaf } from './navConfig'
import { Icon } from '@/components/ui/Icon'
import { useAuthStore } from '@/stores/auth'
import { useWsStore } from '@/stores/ws'

interface SidebarProps {
  collapsed: boolean
  onToggle: () => void
}

export function Sidebar({ collapsed, onToggle }: SidebarProps) {
  const router = useRouter()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const user = useAuthStore((s) => s.user)
  const logout = useAuthStore((s) => s.logout)
  const wsStatus = useWsStore((s) => s.status)

  // Track which groups are expanded
  const activeEntry = findNavEntry(pathname)
  const [openGroups, setOpenGroups] = useState<Set<string>>(() => {
    const initial = new Set<string>()
    if (activeEntry?.groupId) initial.add(activeEntry.groupId)
    return initial
  })

  // Auto-expand group when route changes
  useEffect(() => {
    const entry = findNavEntry(pathname)
    if (entry?.groupId) {
      setOpenGroups((prev) => new Set(prev).add(entry.groupId!))
    }
  }, [pathname])

  function toggleGroup(id: string) {
    setOpenGroups((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  function navigate(route: string) {
    router.navigate({ to: route })
  }

  const wsColor =
    wsStatus === 'connected'    ? 'var(--success)' :
    wsStatus === 'connecting'   ? 'var(--warning)' :
                                  'var(--error)'

  return (
    <nav
      role="navigation"
      aria-label="Main navigation"
      style={{
        position: 'fixed',
        top: 0,
        left: 0,
        height: '100vh',
        width: collapsed ? 'var(--sidebar-width-collapsed)' : 'var(--sidebar-width)',
        background: 'rgba(10,10,10,0.98)',
        borderRight: '1px solid var(--border)',
        display: 'flex',
        flexDirection: 'column',
        transition: 'width 0.2s ease',
        overflow: 'hidden',
        zIndex: 50,
        backdropFilter: 'blur(8px)',
      }}
    >
      {/* Logo + collapse toggle */}
      <div
        style={{
          height: 'var(--topbar-height)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: collapsed ? 'center' : 'space-between',
          padding: collapsed ? '0' : '0 16px',
          borderBottom: '1px solid var(--border)',
          flexShrink: 0,
        }}
      >
        {!collapsed && (
          <span
            style={{
              fontWeight: 800,
              fontSize: 18,
              color: 'var(--primary)',
              letterSpacing: '-0.5px',
              cursor: 'pointer',
              whiteSpace: 'nowrap',
            }}
            onClick={() => navigate('/')}
          >
            D-PlaneOS
          </span>
        )}
        <button
          onClick={onToggle}
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          style={{
            background: 'none',
            border: 'none',
            cursor: 'pointer',
            color: 'var(--text-secondary)',
            display: 'flex',
            alignItems: 'center',
            padding: '6px',
            borderRadius: 'var(--radius-sm)',
          }}
        >
          <Icon name={collapsed ? 'menu_open' : 'menu'} size={20} />
        </button>
      </div>

      {/* Nav items — scrollable */}
      <div
        style={{
          flex: 1,
          overflowY: 'auto',
          overflowX: 'hidden',
          padding: '8px 0',
        }}
      >
        {NAV.map((item) => {
          if (item.kind === 'leaf') {
            const isActive = pathname === item.route
            return (
              <NavLeafItem
                key={item.id}
                leaf={item}
                isActive={isActive}
                collapsed={collapsed}
                onClick={() => navigate(item.route)}
              />
            )
          }

          // Group
          const group = item as NavGroup
          const isOpen = openGroups.has(group.id)
          const hasActiveChild = group.children.some((c) => c.route === pathname)

          return (
            <div key={group.id}>
              <button
                onClick={() => {
                  if (collapsed) return // collapsed: clicking navigates to first child
                  toggleGroup(group.id)
                }}
                aria-expanded={isOpen}
                style={{
                  width: '100%',
                  display: 'flex',
                  alignItems: 'center',
                  gap: '10px',
                  padding: collapsed ? '10px 0' : '9px 16px',
                  justifyContent: collapsed ? 'center' : 'flex-start',
                  background: hasActiveChild ? 'var(--primary-bg)' : 'none',
                  border: 'none',
                  cursor: 'pointer',
                  color: hasActiveChild ? 'var(--primary)' : 'var(--text-secondary)',
                  fontSize: 'var(--text-sm)',
                  fontFamily: 'var(--font-ui)',
                  fontWeight: hasActiveChild ? 600 : 400,
                  borderRadius: 0,
                  transition: 'background 0.15s, color 0.15s',
                }}
                onMouseEnter={(e) => {
                  if (!hasActiveChild) e.currentTarget.style.background = 'var(--surface)'
                  if (!hasActiveChild) e.currentTarget.style.color = 'var(--text)'
                }}
                onMouseLeave={(e) => {
                  if (!hasActiveChild) e.currentTarget.style.background = 'none'
                  if (!hasActiveChild) e.currentTarget.style.color = 'var(--text-secondary)'
                }}
              >
                <Icon name={group.icon} size={20} style={{ flexShrink: 0 }} />
                {!collapsed && (
                  <>
                    <span style={{ flex: 1, textAlign: 'left', whiteSpace: 'nowrap' }}>{group.label}</span>
                    <Icon
                      name="expand_more"
                      size={16}
                      style={{
                        transform: isOpen ? 'rotate(180deg)' : 'rotate(0deg)',
                        transition: 'transform 0.2s',
                        flexShrink: 0,
                      }}
                    />
                  </>
                )}
              </button>

              {/* Children — only visible when expanded and not collapsed */}
              {!collapsed && isOpen && (
                <div>
                  {group.children.map((child) => {
                    const isChildActive = pathname === child.route
                    return (
                      <NavLeafItem
                        key={child.id}
                        leaf={child}
                        isActive={isChildActive}
                        collapsed={false}
                        indent
                        onClick={() => navigate(child.route)}
                      />
                    )
                  })}
                </div>
              )}
            </div>
          )
        })}
      </div>

      {/* Bottom: connection indicator + user */}
      <div
        style={{
          borderTop: '1px solid var(--border)',
          padding: collapsed ? '12px 0' : '12px 16px',
          display: 'flex',
          flexDirection: 'column',
          gap: '8px',
          flexShrink: 0,
        }}
      >
        {/* WS status */}
        {!collapsed && (
          <div
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: '8px',
              fontSize: 'var(--text-xs)',
              color: 'var(--text-tertiary)',
            }}
          >
            <span
              style={{
                width: 7,
                height: 7,
                borderRadius: '50%',
                background: wsColor,
                flexShrink: 0,
                boxShadow: `0 0 6px ${wsColor}`,
              }}
            />
            {wsStatus === 'connected' ? 'Live' : wsStatus === 'connecting' ? 'Connecting…' : 'Disconnected'}
          </div>
        )}

        {/* User + logout */}
        <div
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: '8px',
            justifyContent: collapsed ? 'center' : 'flex-start',
          }}
        >
          {!collapsed && (
            <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', flex: 1, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
              {user?.username ?? '—'}
            </span>
          )}
          <button
            onClick={async () => {
              await logout()
              router.navigate({ to: '/login' })
            }}
            aria-label="Log out"
            title="Log out"
            style={{
              background: 'none',
              border: 'none',
              cursor: 'pointer',
              color: 'var(--text-tertiary)',
              display: 'flex',
              alignItems: 'center',
              padding: '4px',
              borderRadius: 'var(--radius-xs)',
            }}
          >
            <Icon name="logout" size={18} />
          </button>
        </div>
      </div>
    </nav>
  )
}

// ---------------------------------------------------------------------------
// NavLeafItem
// ---------------------------------------------------------------------------

interface NavLeafItemProps {
  leaf: NavLeaf
  isActive: boolean
  collapsed: boolean
  indent?: boolean
  onClick: () => void
}

function NavLeafItem({ leaf, isActive, collapsed, indent = false, onClick }: NavLeafItemProps) {
  return (
    <button
      onClick={onClick}
      title={collapsed ? leaf.label : undefined}
      aria-current={isActive ? 'page' : undefined}
      style={{
        width: '100%',
        display: 'flex',
        alignItems: 'center',
        gap: '10px',
        padding: collapsed ? '10px 0' : indent ? '7px 16px 7px 42px' : '9px 16px',
        justifyContent: collapsed ? 'center' : 'flex-start',
        background: isActive ? 'var(--primary-bg)' : 'none',
        border: 'none',
        borderLeft: isActive && !collapsed ? '2px solid var(--primary)' : '2px solid transparent',
        cursor: 'pointer',
        color: isActive ? 'var(--primary)' : 'var(--text-secondary)',
        fontSize: 'var(--text-sm)',
        fontFamily: 'var(--font-ui)',
        fontWeight: isActive ? 600 : 400,
        transition: 'background 0.15s, color 0.15s',
        whiteSpace: 'nowrap',
      }}
      onMouseEnter={(e) => {
        if (!isActive) {
          e.currentTarget.style.background = 'var(--surface)'
          e.currentTarget.style.color = 'var(--text)'
        }
      }}
      onMouseLeave={(e) => {
        if (!isActive) {
          e.currentTarget.style.background = 'none'
          e.currentTarget.style.color = 'var(--text-secondary)'
        }
      }}
    >
      <Icon name={leaf.icon} size={collapsed ? 22 : 18} style={{ flexShrink: 0 }} />
      {!collapsed && <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{leaf.label}</span>}
    </button>
  )
}
