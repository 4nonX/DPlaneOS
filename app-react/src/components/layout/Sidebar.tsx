/**
 * components/layout/Sidebar.tsx
 *
 * Collapsible sidebar with group visual hierarchy,
 * active indicator, hover states, WS status glow.
 */

import { useState } from 'react'
import { useRouter, useRouterState } from '@tanstack/react-router'
import { NAV, findNavEntry, type NavGroup, type NavLeaf } from './navConfig'
import { Icon } from '@/components/ui/Icon'
import { Tooltip } from '@/components/ui/Tooltip'
import { useAuthStore } from '@/stores/auth'
import { useWsStore } from '@/stores/ws'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'

interface SidebarProps {
  collapsed: boolean
  onToggle: () => void
  isMobile?: boolean
  mobileMenuOpen?: boolean
  onMobileMenuClose?: () => void
}

interface LicenseStatus {
  active: boolean
}

export function Sidebar({ collapsed, onToggle, isMobile: isMobileProp, mobileMenuOpen, onMobileMenuClose }: SidebarProps) {
  const router   = useRouter()
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const user     = useAuthStore((s) => s.user)
  const logout   = useAuthStore((s) => s.logout)
  const wsStatus = useWsStore((s) => s.status)
  const isMobile = isMobileProp ?? window.innerWidth < 768

  const licenseQ = useQuery({
    queryKey: ['license', 'status'],
    queryFn: ({ signal }) => api.get<LicenseStatus>('/api/system/license/status', signal),
    staleTime: 30000,
  })

  const activeEntry = findNavEntry(pathname)
  // manuallyOpenedGroups tracks groups the user has explicitly toggled open/closed.
  // The active group is always open regardless (derived inline below).
  const [manuallyOpenedGroups, setManuallyOpenedGroups] = useState<Set<string>>(() => new Set<string>())

  // Always keep the active group open without needing an effect
  const openGroups = new Set(manuallyOpenedGroups)
  if (activeEntry?.groupId) openGroups.add(activeEntry.groupId)

  function toggleGroup(id: string) {
    setManuallyOpenedGroups((prev) => {
      const next = new Set(prev)
      // If this is the active group, only allow closing if it is in the manual set
      if (next.has(id)) { next.delete(id) } else { next.add(id) }
      return next
    })
  }

  function navigate(route: string) { router.navigate({ to: route }) }

  const wsClass = wsStatus === 'connected' ? 'online' : wsStatus === 'connecting' ? 'connecting' : 'error'
  const wsLabel = wsStatus === 'connected' ? 'Live' : wsStatus === 'connecting' ? 'Connecting…' : 'Disconnected'

  return (
    <>
      {isMobile && (
        <div
          onClick={onMobileMenuClose}
          style={{
            position: 'fixed', inset: 0, zIndex: 'calc(var(--z-topbar) - 1)',
            background: 'rgba(0, 0, 0, 0.4)',
            opacity: mobileMenuOpen ? 1 : 0,
            pointerEvents: mobileMenuOpen ? 'auto' : 'none',
            transition: 'opacity var(--transition-fast)',
          }}
        />
      )}
      <nav
        role="navigation"
        aria-label="Main navigation"
        style={{
          position: 'fixed', top: 0, left: isMobile ? (mobileMenuOpen ? 0 : '-100%') : 0,
          height: '100vh',
          width: isMobile ? '260px' : (collapsed ? 'var(--sidebar-width-collapsed)' : 'var(--sidebar-width)'),
          background: 'hsla(var(--hue-bg), 18%, 2%, 0.8)',
          borderRight: '1px solid var(--border-subtle)',
          display: 'flex', flexDirection: 'column',
          transition: isMobile ? 'left var(--transition-fast)' : 'width var(--transition-bounce)',
          overflow: 'hidden', zIndex: isMobile ? 'calc(var(--z-topbar) + 1)' : 'var(--z-topbar)',
          backdropFilter: 'var(--blur-glass)'}}
      >
      {/* ── Logo + collapse toggle ── */}
      <div style={{
        height: 'var(--topbar-height)',
        display: 'flex', alignItems: 'center',
        justifyContent: collapsed ? 'center' : 'space-between',
        padding: collapsed ? '0' : '0 14px 0 18px',
        borderBottom: '1px solid var(--border)',
        flexShrink: 0}}>
        {!collapsed && (
          <button
            onClick={() => navigate('/')}
            aria-label="Go to Dashboard"
            style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 8 }}
          >
            <div style={{
              width: 24, height: 24, borderRadius: 6,
              background: 'linear-gradient(135deg, var(--primary) 0%, hsl(260, 50%, 58%) 100%)',
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              flexShrink: 0, boxShadow: 'var(--shadow-sm)'}}>
              <Icon name="dns" size={14} style={{ color: 'var(--text-on-primary)' }} />
            </div>
            <span style={{
              fontWeight: 800, fontSize: 15,
              background: 'linear-gradient(90deg, #fff 0%, rgba(255,255,255,0.7) 100%)',
              WebkitBackgroundClip: 'text', WebkitTextFillColor: 'transparent',
              letterSpacing: '-0.3px', whiteSpace: 'nowrap'}}>
              DPlaneOS
            </span>
          </button>
        )}
        <button
          onClick={onToggle}
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          style={{
            background: 'none', border: 'none', cursor: 'pointer',
            color: 'var(--text-tertiary)', display: 'flex', alignItems: 'center',
            padding: '6px', borderRadius: 'var(--radius-sm)',
            transition: 'color var(--transition-fast), background var(--transition-fast)'}}
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
            const leaf = item as NavLeaf
            const isEnterprise = leaf.enterprise === true
            if (isEnterprise && !licenseQ.data?.active) return null

            return (
              <LeafItem key={leaf.id} leaf={leaf}
                isActive={pathname === leaf.route}
                collapsed={collapsed}
                onClick={() => navigate(leaf.route)} />
            )
          }

          const group = item as NavGroup
          const filteredChildren = group.children.filter((c) => {
            const isEnterprise = c.enterprise === true
            return !isEnterprise || licenseQ.data?.active
          })
          const isOpen = openGroups.has(group.id)
          const hasActive = filteredChildren.some((c) => c.route === pathname)

          return (
            <div key={group.id}>
              {/* Group header */}
              {!collapsed && (
                <button
                  onClick={() => toggleGroup(group.id)}
                  aria-expanded={isOpen}
                  aria-controls={`nav-group-${group.id}`}
                  style={{
                    width: '100%', display: 'flex', alignItems: 'center', gap: 9,
                    padding: '8px 16px 8px 18px',
                    background: hasActive ? 'var(--primary-bg)' : 'transparent',
                    border: 'none', cursor: 'pointer',
                    color: hasActive ? 'var(--primary)' : 'var(--text-secondary)',
                    fontSize: 'var(--text-sm)', fontFamily: 'var(--font-ui)',
                    fontWeight: hasActive ? 600 : 500,
                    transition: 'all var(--transition-fast)'}}
                  onMouseEnter={(e) => {
                    if (!hasActive) { e.currentTarget.style.background = 'hsla(0,0%,100%,0.04)'; e.currentTarget.style.color = 'var(--text)'; }
                  }}
                  onMouseLeave={(e) => {
                    if (!hasActive) { e.currentTarget.style.background = 'transparent'; e.currentTarget.style.color = 'var(--text-secondary)'; }
                  }}
                >
                  <Icon name={group.icon} size={17} style={{ flexShrink: 0, opacity: hasActive ? 1 : 0.7 }} />
                  <span style={{ flex: 1, textAlign: 'left', whiteSpace: 'nowrap', fontSize: 12,
                    textTransform: 'uppercase', letterSpacing: '0.6px', fontWeight: 700 }}>
                    {group.label}
                  </span>
                  <Icon name="expand_more" size={15} style={{
                    transform: isOpen ? 'rotate(180deg)' : 'rotate(0deg)',
                    transition: 'transform var(--transition-bounce)', flexShrink: 0, opacity: 0.5}} />
                </button>
              )}

              {/* Collapsed group: show icon that navigates to first child */}
              {collapsed && (
                <Tooltip content={group.label} position="right" fill>
                  <button
                    onClick={() => navigate(group.children[0]?.route ?? '/')}
                    aria-label={group.label}
                    style={{
                      width: '100%', display: 'flex', justifyContent: 'center',
                      padding: '10px 0', background: hasActive ? 'var(--primary-bg)' : 'none',
                      border: 'none', cursor: 'pointer',
                      color: hasActive ? 'var(--primary)' : 'var(--text-tertiary)',
                      transition: 'all var(--transition-fast)'}}
                    onMouseEnter={(e) => { if (!hasActive) { e.currentTarget.style.background = 'var(--surface)'; e.currentTarget.style.color = 'var(--text)'; } }}
                    onMouseLeave={(e) => { if (!hasActive) { e.currentTarget.style.background = 'none'; e.currentTarget.style.color = 'var(--text-tertiary)'; } }}
                  >
                    <Icon name={group.icon} size={20} />
                  </button>
                </Tooltip>
              )}

              {/* Children */}
              {!collapsed && isOpen && (
                <div id={`nav-group-${group.id}`}>
                  {filteredChildren.map((child) => (
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

      {/* ── ZFS Events Widget ── */}
      {!collapsed && <ZFSEventsWidget />}

      {/* ── Footer: WS status + user ── */}
      <div style={{
        borderTop: '1px solid var(--border)',
        padding: collapsed ? '10px 0' : '10px 16px',
        display: 'flex', flexDirection: 'column', gap: 8, flexShrink: 0}}>
        {/* WS dot */}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: collapsed ? 'center' : 'flex-start', gap: 8 }}>
          <span className={`status-dot ${wsClass}`} aria-hidden="true" />
          <span style={collapsed ? {
            position: 'absolute', width: 1, height: 1, padding: 0, margin: -1,
            overflow: 'hidden', clip: 'rect(0,0,0,0)', whiteSpace: 'nowrap', border: 0
          } : { fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>
            {wsLabel}
          </span>
        </div>

        {/* User + logout */}
        {!collapsed ? (
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <div style={{
              width: 26, height: 26, borderRadius: '50%',
              background: 'linear-gradient(135deg, var(--primary) 0%, hsl(260, 50%, 58%) 100%)',
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              flexShrink: 0, fontSize: 11, fontWeight: 700, color: 'var(--text-on-primary)'}}>
              {(user?.username ?? '?')[0].toUpperCase()}
            </div>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ fontSize: 'var(--text-sm)', fontWeight: 600, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {user?.username ?? '-'}
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
                  transition: 'color var(--transition-fast)'}}
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
                transition: 'color var(--transition-fast)'}}
              onMouseEnter={(e) => { e.currentTarget.style.color = 'var(--error)'; }}
              onMouseLeave={(e) => { e.currentTarget.style.color = 'var(--text-tertiary)'; }}
            >
              <Icon name="logout" size={18} />
            </button>
          </Tooltip>
        )}
      </div>
      </nav>
    </>
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
  const btn = (
    <button
      onClick={onClick}
      aria-current={isActive ? 'page' : undefined}
      aria-label={collapsed ? leaf.label : undefined}
      style={{
        width: '100%', display: 'flex', alignItems: 'center', gap: collapsed ? 0 : 10,
        padding: collapsed ? 0 : indent ? '7px 16px 7px 36px' : '8px 16px 8px 18px',
        justifyContent: collapsed ? 'center' : 'flex-start',
        background: isActive ? 'var(--primary-bg)' : 'transparent',
        border: 'none',
        borderLeft: !collapsed && isActive ? '2px solid var(--primary)' : !collapsed ? '2px solid transparent' : 'none',
        cursor: 'pointer',
        color: isActive ? 'var(--primary)' : 'var(--text-secondary)',
        fontSize: 'var(--text-sm)', fontFamily: 'var(--font-ui)',
        fontWeight: isActive ? 600 : 400,
        transition: 'all var(--transition-fast)',
        whiteSpace: 'nowrap',
        boxShadow: isActive && collapsed ? 'inset 2px 0 0 var(--primary)' : 'none'}}
      onMouseEnter={(e) => {
        if (!isActive) { e.currentTarget.style.background = 'hsla(0,0%,100%,0.04)'; e.currentTarget.style.color = 'var(--text)'; }
      }}
      onMouseLeave={(e) => {
        if (!isActive) { e.currentTarget.style.background = 'transparent'; e.currentTarget.style.color = 'var(--text-secondary)'; }
      }}
    >
      <Icon name={leaf.icon} size={collapsed ? 21 : 17} style={{ flexShrink: 0, opacity: isActive ? 1 : 0.65 }} />
      {!collapsed && <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{leaf.label}</span>}
    </button>
  )

  return collapsed ? (
    <Tooltip content={leaf.label} position="right" fill>{btn}</Tooltip>
  ) : btn
}

// ---------------------------------------------------------------------------
// ZFSEventsWidget
// ---------------------------------------------------------------------------

// Maps a raw ZFS event string to a short human-readable label and icon.
function parseZFSEvent(raw: string): { label: string; icon: string; color: string } {
  const r = raw.toLowerCase()
  if (r.includes('scrub_finish') || r.includes('scrub.finish')) return { label: 'Scrub complete', icon: 'verified', color: 'var(--success)' }
  if (r.includes('scrub_start') || r.includes('scrub.start'))   return { label: 'Scrub started',  icon: 'search',   color: 'var(--primary)' }
  if (r.includes('resilver_finish'))                             return { label: 'Resilver done',  icon: 'build',    color: 'var(--success)' }
  if (r.includes('resilver'))                                    return { label: 'Resilvering',    icon: 'build',    color: 'var(--warning)' }
  if (r.includes('import'))                                      return { label: 'Pool imported',  icon: 'input',    color: 'var(--primary)' }
  if (r.includes('export'))                                      return { label: 'Pool exported',  icon: 'output',   color: 'var(--text-secondary)' }
  if (r.includes('fault') || r.includes('fail') || r.includes('error')) return { label: 'Fault detected', icon: 'warning', color: 'var(--error)' }
  if (r.includes('trim'))                                        return { label: 'TRIM event',     icon: 'layers_clear', color: 'var(--primary)' }
  return { label: 'ZFS event', icon: 'history', color: 'var(--text-tertiary)' }
}

function ZFSEventsWidget() {
  const eventsQ = useQuery({
    queryKey: ['zfs', 'events'],
    queryFn: ({ signal }) => api.get<{ success: boolean; events: { raw: string }[] }>('/api/zfs/events?count=4', signal),
    refetchInterval: 10_000,
  })

  const events = eventsQ.data?.events ?? []

  return (
    <div style={{
      margin: '6px 12px',
      padding: '10px 12px',
      background: 'rgba(255,255,255,0.02)',
      border: '1px solid var(--border-subtle)',
      borderRadius: 'var(--radius-md)',
      display: 'flex',
      flexDirection: 'column',
      gap: 6
    }}>
      <div style={{
        fontSize: 'var(--text-3xs)',
        fontWeight: 700,
        color: 'var(--text-tertiary)',
        textTransform: 'uppercase',
        letterSpacing: '0.5px'
      }}>
        ZFS Activity
      </div>

      {events.length === 0 ? (
        <div style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', opacity: 0.6 }}>
          No recent events
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          {events.map((ev, i) => {
            const { label, icon, color } = parseZFSEvent(ev.raw)
            return (
              <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <Icon name={icon} size={12} style={{ color, flexShrink: 0, opacity: 0.85 }} />
                <span style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)', lineHeight: 1.3 }}>
                  {label}
                </span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

