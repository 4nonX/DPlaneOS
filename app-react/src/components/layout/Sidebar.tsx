/**
 * components/layout/Sidebar.tsx
 *
 * Collapsible sidebar with group visual hierarchy,
 * active indicator, hover states, WS status glow.
 */

import { useState, useEffect } from 'react'
import { useRouter, useRouterState } from '@tanstack/react-router'
import { NAV, findNavEntry, type NavGroup, type NavLeaf } from './navConfig'
import { Icon } from '@/components/ui/Icon'
import { Tooltip } from '@/components/ui/Tooltip'
import { useAuthStore } from '@/stores/auth'
import { useWsStore } from '@/stores/ws'

interface SidebarProps {
  collapsed: boolean
  onToggle: () => void
}

export function Sidebar({ collapsed, onToggle }: SidebarProps) {
  const router   = useRouter()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const user     = useAuthStore((s) => s.user)
  const logout   = useAuthStore((s) => s.logout)
  const wsStatus = useWsStore((s) => s.status)

  const activeEntry = findNavEntry(pathname)
  const [openGroups, setOpenGroups] = useState<Set<string>>(() => {
    const s = new Set<string>()
    if (activeEntry?.groupId) s.add(activeEntry.groupId)
    return s
  })

  useEffect(() => {
    const entry = findNavEntry(pathname)
    if (entry?.groupId) setOpenGroups((prev) => new Set(prev).add(entry.groupId!))
  }, [pathname])

  function toggleGroup(id: string) {
    setOpenGroups((prev) => {
      const next = new Set(prev)
      next.has(id) ? next.delete(id) : next.add(id)
      return next
    })
  }

  function navigate(route: string) { router.navigate({ to: route }) }

  const wsClass = wsStatus === 'connected' ? 'online' : wsStatus === 'connecting' ? 'connecting' : 'error'
  const wsLabel = wsStatus === 'connected' ? 'Live' : wsStatus === 'connecting' ? 'Connecting…' : 'Disconnected'

  return (
    <nav
      role="navigation"
      aria-label="Main navigation"
      style={{
        position: 'fixed', top: 0, left: 0, height: '100vh',
        width: collapsed ? 'var(--sidebar-width-collapsed)' : 'var(--sidebar-width)',
        background: 'rgba(6,6,6,0.98)',
        borderRight: '1px solid var(--border)',
        display: 'flex', flexDirection: 'column',
        transition: 'width 0.22s cubic-bezier(0.4,0,0.2,1)',
        overflow: 'hidden', zIndex: 50,
        backdropFilter: 'blur(12px)',
      }}
    >
      {/* ── Logo + collapse toggle ── */}
      <div style={{
        height: 'var(--topbar-height)',
        display: 'flex', alignItems: 'center',
        justifyContent: collapsed ? 'center' : 'space-between',
        padding: collapsed ? '0' : '0 14px 0 18px',
        borderBottom: '1px solid var(--border)',
        flexShrink: 0,
      }}>
        {!collapsed && (
          <div
            onClick={() => navigate('/')}
            style={{ cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 8 }}
          >
            <div style={{
              width: 24, height: 24, borderRadius: 6,
              background: 'linear-gradient(135deg, var(--primary) 0%, #6b7fff 100%)',
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              flexShrink: 0, boxShadow: '0 0 10px rgba(138,156,255,0.3)',
            }}>
              <Icon name="dns" size={14} style={{ color: '#000' }} />
            </div>
            <span style={{
              fontWeight: 800, fontSize: 15,
              background: 'linear-gradient(90deg, #fff 0%, rgba(255,255,255,0.7) 100%)',
              WebkitBackgroundClip: 'text', WebkitTextFillColor: 'transparent',
              letterSpacing: '-0.3px', whiteSpace: 'nowrap',
            }}>
              D-PlaneOS
            </span>
          </div>
        )}
        <button
          onClick={onToggle}
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          style={{
            background: 'none', border: 'none', cursor: 'pointer',
            color: 'var(--text-tertiary)', display: 'flex', alignItems: 'center',
            padding: '6px', borderRadius: 'var(--radius-sm)',
            transition: 'color var(--transition-fast), background var(--transition-fast)',
          }}
          onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--text)'; e.currentTarget.style.background = 'var(--surface)'; }}
          onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-tertiary)'; e.currentTarget.style.background = 'none'; }}
        >
          <Icon name={collapsed ? 'menu_open' : 'menu'} size={20} />
        </button>
      </div>

      {/* ── Nav items ── */}
      <div style={{ flex: 1, overflowY: 'auto', overflowX: 'hidden', padding: '6px 0' }}>
        {NAV.map((item) => {
          if (item.kind === 'leaf') {
            return (
              <LeafItem key={item.id} leaf={item as NavLeaf}
                isActive={pathname === (item as NavLeaf).route}
                collapsed={collapsed}
                onClick={() => navigate((item as NavLeaf).route)} />
            )
          }

          const group = item as NavGroup
          const isOpen = openGroups.has(group.id)
          const hasActive = group.children.some((c) => c.route === pathname)

          return (
            <div key={group.id}>
              {/* Group header */}
              {!collapsed && (
                <button
                  onClick={() => toggleGroup(group.id)}
                  aria-expanded={isOpen}
                  style={{
                    width: '100%', display: 'flex', alignItems: 'center', gap: 9,
                    padding: '8px 16px 8px 18px',
                    background: hasActive ? 'rgba(138,156,255,0.06)' : 'none',
                    border: 'none', cursor: 'pointer',
                    color: hasActive ? 'var(--primary)' : 'var(--text-secondary)',
                    fontSize: 'var(--text-sm)', fontFamily: 'var(--font-ui)',
                    fontWeight: hasActive ? 600 : 500,
                    transition: 'background var(--transition-fast), color var(--transition-fast)',
                  }}
                  onMouseEnter={(e) => {
                    if (!hasActive) { e.currentTarget.style.background = 'var(--surface)'; e.currentTarget.style.color = 'var(--text)'; }
                  }}
                  onMouseLeave={(e) => {
                    if (!hasActive) { e.currentTarget.style.background = 'none'; e.currentTarget.style.color = 'var(--text-secondary)'; }
                  }}
                >
                  <Icon name={group.icon} size={17} style={{ flexShrink: 0, opacity: hasActive ? 1 : 0.7 }} />
                  <span style={{ flex: 1, textAlign: 'left', whiteSpace: 'nowrap', fontSize: 12,
                    textTransform: 'uppercase', letterSpacing: '0.6px', fontWeight: 700 }}>
                    {group.label}
                  </span>
                  <Icon name="expand_more" size={15} style={{
                    transform: isOpen ? 'rotate(180deg)' : 'rotate(0deg)',
                    transition: 'transform 0.2s', flexShrink: 0, opacity: 0.5,
                  }} />
                </button>
              )}

              {/* Collapsed group: show icon that navigates to first child */}
              {collapsed && (
                <button
                  onClick={() => navigate(group.children[0]?.route ?? '/')}
                  title={group.label}
                  style={{
                    width: '100%', display: 'flex', justifyContent: 'center',
                    padding: '10px 0', background: hasActive ? 'var(--primary-bg)' : 'none',
                    border: 'none', cursor: 'pointer',
                    color: hasActive ? 'var(--primary)' : 'var(--text-tertiary)',
                    transition: 'all var(--transition-fast)',
                  }}
                  onMouseEnter={(e) => { if (!hasActive) { e.currentTarget.style.background = 'var(--surface)'; e.currentTarget.style.color = 'var(--text)'; } }}
                  onMouseLeave={(e) => { if (!hasActive) { e.currentTarget.style.background = 'none'; e.currentTarget.style.color = 'var(--text-tertiary)'; } }}
                >
                  <Icon name={group.icon} size={20} />
                </button>
              )}

              {/* Children */}
              {!collapsed && isOpen && (
                <div>
                  {group.children.map((child) => (
                    <LeafItem key={child.id} leaf={child}
                      isActive={pathname === child.route}
                      collapsed={false} indent
                      onClick={() => navigate(child.route)} />
                  ))}
                </div>
              )}
            </div>
          )
        })}
      </div>

      {/* ── Footer: WS status + user ── */}
      <div style={{
        borderTop: '1px solid var(--border)',
        padding: collapsed ? '10px 0' : '10px 16px',
        display: 'flex', flexDirection: 'column', gap: 8, flexShrink: 0,
      }}>
        {/* WS dot */}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: collapsed ? 'center' : 'flex-start', gap: 8 }}>
          <span className={`status-dot ${wsClass}`} />
          {!collapsed && (
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>{wsLabel}</span>
          )}
        </div>

        {/* User + logout */}
        {!collapsed ? (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <div style={{
              width: 26, height: 26, borderRadius: '50%',
              background: 'linear-gradient(135deg, var(--primary) 0%, #6b7fff 100%)',
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              flexShrink: 0, fontSize: 11, fontWeight: 800, color: '#000',
            }}>
              {(user?.username ?? '?')[0].toUpperCase()}
            </div>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {user?.username ?? '—'}
              </div>
              {user?.role && user.role !== 'user' && (
                <div style={{ fontSize: 'var(--text-2xs)', color: 'var(--primary)', textTransform: 'uppercase', letterSpacing: '0.5px', fontWeight: 700 }}>
                  {user.role}
                </div>
              )}
            </div>
            <Tooltip content="Log out">
              <button
                onClick={async () => { await logout(); router.navigate({ to: '/login' }) }}
                aria-label="Log out"
                style={{
                  background: 'none', border: 'none', cursor: 'pointer',
                  color: 'var(--text-tertiary)', display: 'flex', alignItems: 'center',
                  padding: '4px', borderRadius: 'var(--radius-xs)',
                  transition: 'color var(--transition-fast)',
                }}
                onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--error)'; }}
                onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-tertiary)'; }}
              >
                <Icon name="logout" size={17} />
              </button>
            </Tooltip>
          </div>
        ) : (
          <Tooltip content="Log out">
            <button
              onClick={async () => { await logout(); router.navigate({ to: '/login' }) }}
              aria-label="Log out"
              style={{
                background: 'none', border: 'none', cursor: 'pointer',
                color: 'var(--text-tertiary)', display: 'flex', justifyContent: 'center',
                padding: '4px', borderRadius: 'var(--radius-xs)', width: '100%',
                transition: 'color var(--transition-fast)',
              }}
              onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--error)'; }}
              onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-tertiary)'; }}
            >
              <Icon name="logout" size={18} />
            </button>
          </Tooltip>
        )}
      </div>
    </nav>
  )
}

// ---------------------------------------------------------------------------
// LeafItem
// ---------------------------------------------------------------------------

interface LeafItemProps {
  leaf: NavLeaf
  isActive: boolean
  collapsed: boolean
  indent?: boolean
  onClick: () => void
}

function LeafItem({ leaf, isActive, collapsed, indent = false, onClick }: LeafItemProps) {
  return (
    <button
      onClick={onClick}
      title={collapsed ? leaf.label : undefined}
      aria-current={isActive ? 'page' : undefined}
      style={{
        width: '100%', display: 'flex', alignItems: 'center', gap: 10,
        padding: collapsed ? '10px 0' : indent ? '7px 16px 7px 36px' : '8px 16px 8px 18px',
        justifyContent: collapsed ? 'center' : 'flex-start',
        background: isActive ? 'rgba(138,156,255,0.09)' : 'none',
        border: 'none',
        borderLeft: isActive && !collapsed ? '2px solid var(--primary)' : '2px solid transparent',
        cursor: 'pointer',
        color: isActive ? 'var(--primary)' : 'var(--text-secondary)',
        fontSize: 'var(--text-sm)', fontFamily: 'var(--font-ui)',
        fontWeight: isActive ? 600 : 400,
        transition: 'background var(--transition-fast), color var(--transition-fast)',
        whiteSpace: 'nowrap',
      }}
      onMouseEnter={(e) => {
        if (!isActive) { e.currentTarget.style.background = 'var(--surface)'; e.currentTarget.style.color = 'var(--text)'; }
      }}
      onMouseLeave={(e) => {
        if (!isActive) { e.currentTarget.style.background = 'none'; e.currentTarget.style.color = 'var(--text-secondary)'; }
      }}
    >
      <Icon name={leaf.icon} size={collapsed ? 21 : 17} style={{ flexShrink: 0, opacity: isActive ? 1 : 0.75 }} />
      {!collapsed && <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{leaf.label}</span>}
    </button>
  )
}
