/**
 * components/ui/GlobalSearch.tsx
 *
 * Cmd/Ctrl+K command palette.
 *
 * Empty state:  recent pages (localStorage) + quick-action items
 * Active query: scored results grouped by category with section headers
 *
 * Scoring: exact > word-prefix > acronym > substring > subsequence
 * Sources: nav items (instant), pools/datasets/containers/shares (API),
 *          HA nodes (React Query cache - zero extra requests)
 *
 * Recents: last 5 navigations via palette stored in localStorage
 *
 * ARIA: role=dialog > role=combobox input + role=listbox results
 *       focus trap, aria-activedescendant, aria-live result count
 */

import { useState, useEffect, useRef, useId, useCallback, useMemo } from 'react'
import { createPortal } from 'react-dom'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from './Icon'
import { NAV } from '@/components/layout/navConfig'
import type { NavLeaf } from '@/components/layout/navConfig'

// ─── Types ────────────────────────────────────────────────────────────────────

type ResultKind = 'recent' | 'action' | 'nav' | 'pool' | 'dataset' | 'container' | 'share' | 'ha-node'

interface SearchResult {
  id:     string
  kind:   ResultKind
  label:  string
  sub:    string
  icon:   string
  route:  string
  score?: number
}

interface ZFSDataset  { name: string; used: string; avail: string; mountpoint: string; quota: string }
interface ZFSPool     { name: string; health: string; capacity: string; size: string }
interface Container   { id: string; name: string; state: string; image: string }
interface Share       { name: string; path: string }
interface HANode      { id: string; name?: string; role?: string; state?: string }
interface HAStatus    { cluster?: { peers?: HANode[]; local_node?: HANode } }

interface RecentEntry { route: string; label: string; icon: string }

// ─── Constants ────────────────────────────────────────────────────────────────

const RECENT_KEY  = 'dplane:palette:recents'
const MAX_RECENTS = 5

const SECTION_ORDER: ResultKind[] = ['recent', 'action', 'nav', 'pool', 'dataset', 'container', 'share', 'ha-node']

const SECTION_LABELS: Record<ResultKind, string> = {
  recent:     'Recent',
  action:     'Quick Actions',
  nav:        'Pages',
  pool:       'Pools',
  dataset:    'Datasets',
  container:  'Containers',
  share:      'Shares',
  'ha-node':  'High Availability',
}

const QUICK_ACTIONS: SearchResult[] = [
  { id: 'action:pool-create',   kind: 'action', label: 'Create ZFS Pool',    sub: 'Add a new storage pool',          icon: 'add_circle',         route: '/pools',        score: 0 },
  { id: 'action:share-create',  kind: 'action', label: 'Create SMB Share',   sub: 'Share a folder over the network', icon: 'folder_shared',      route: '/shares',       score: 0 },
  { id: 'action:snapshot',      kind: 'action', label: 'Schedule Snapshot',  sub: 'Automated ZFS snapshots',         icon: 'schedule',           route: '/snapshots',    score: 0 },
  { id: 'action:user-add',      kind: 'action', label: 'Add User',           sub: 'Create a new user account',       icon: 'person_add',         route: '/users',        score: 0 },
  { id: 'action:docker-run',    kind: 'action', label: 'Deploy Container',   sub: 'Launch a Docker stack',           icon: 'deployed_code',      route: '/docker',       score: 0 },
  { id: 'action:ha-setup',      kind: 'action', label: 'HA Setup Wizard',    sub: 'Configure high availability',     icon: 'device_hub',         route: '/ha',           score: 0 },
  { id: 'action:gitops',        kind: 'action', label: 'GitOps Apply',       sub: 'Reconcile declarative state',     icon: 'account_tree',       route: '/gitops',       score: 0 },
  { id: 'action:terminal',      kind: 'action', label: 'Open Terminal',      sub: 'System shell access',             icon: 'terminal',           route: '/terminal',     score: 0 },
]

const FOCUSABLE = 'a[href],button:not([disabled]),input:not([disabled]),[tabindex]:not([tabindex="-1"])'

// ─── Recent page helpers ──────────────────────────────────────────────────────

function readRecents(): RecentEntry[] {
  try {
    const raw = localStorage.getItem(RECENT_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.slice(0, MAX_RECENTS)
  } catch { return [] }
}

function writeRecent(entry: RecentEntry): void {
  try {
    const prev = readRecents().filter(r => r.route !== entry.route)
    localStorage.setItem(RECENT_KEY, JSON.stringify([entry, ...prev].slice(0, MAX_RECENTS)))
  } catch { /* localStorage unavailable */ }
}

// ─── Scoring ─────────────────────────────────────────────────────────────────

function scoreMatch(query: string, text: string): number {
  if (!query || !text) return 0
  const q = query.toLowerCase()
  const t = text.toLowerCase()

  // Exact match
  if (t === q) return 100

  // Full prefix match
  if (t.startsWith(q)) return 85

  // Word/token boundary matches
  const words = t.split(/[\s/_.-]+/).filter(Boolean)
  if (words.some(w => w.startsWith(q))) return 72

  // Acronym: first letter of each word
  const acronym = words.map(w => w[0] ?? '').join('')
  if (acronym === q) return 68
  if (acronym.startsWith(q)) return 60
  if (acronym.includes(q)) return 48

  // Substring anywhere
  if (t.includes(q)) return 40

  // Subsequence: all chars appear in order
  let qi = 0
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] === q[qi]) qi++
  }
  if (qi === q.length) return 20

  return 0
}

// ─── Nav helpers ─────────────────────────────────────────────────────────────

function flatNavLeaves(): NavLeaf[] {
  const leaves: NavLeaf[] = []
  for (const item of NAV) {
    if (item.kind === 'leaf')  leaves.push(item)
    if (item.kind === 'group') item.children.forEach(c => leaves.push(c))
  }
  return leaves
}

// ─── GlobalSearch ─────────────────────────────────────────────────────────────

interface GlobalSearchProps {
  onClose: () => void
}

export function GlobalSearch({ onClose }: GlobalSearchProps) {
  const [query, setQuery]       = useState('')
  const [activeIdx, setActiveIdx] = useState(0)

  function handleQueryChange(q: string) {
    setQuery(q)
    setActiveIdx(0)
  }
  const inputRef   = useRef<HTMLInputElement>(null)
  const listRef    = useRef<HTMLUListElement>(null)
  const panelRef   = useRef<HTMLDivElement>(null)
  const navigate   = useNavigate()
  const qc         = useQueryClient()
  const dialogId   = useId()
  const listId     = useId()
  const liveId     = useId()

  const enabled = query.trim().length >= 2

  // ── API queries (only fire at 2+ chars) ──
  const datasetsQ = useQuery({
    queryKey: ['search', 'datasets'],
    queryFn: ({ signal }) => api.get<{ success: boolean; data: ZFSDataset[] }>('/api/zfs/datasets', signal),
    enabled,
    staleTime: 30_000,
  })
  const poolsQ = useQuery({
    queryKey: ['search', 'pools'],
    queryFn: ({ signal }) => api.get<{ success: boolean; pools?: ZFSPool[]; data?: ZFSPool[] }>('/api/zfs/pools', signal),
    enabled,
    staleTime: 30_000,
  })
  const containersQ = useQuery({
    queryKey: ['search', 'containers'],
    queryFn: ({ signal }) => api.get<{ containers: Container[] }>('/api/docker/containers', signal),
    enabled,
    staleTime: 15_000,
  })
  const sharesQ = useQuery({
    queryKey: ['search', 'shares'],
    queryFn: ({ signal }) => api.get<{ success: boolean; shares: Share[] }>('/api/shares/list', signal),
    enabled,
    staleTime: 30_000,
  })

  // ── HA nodes from cache (zero extra requests) ──
  const haNodes = useMemo<HANode[]>(() => {
    const cached = qc.getQueryData<HAStatus>(['ha', 'status'])
    return cached?.cluster?.peers ?? []
  }, [qc])

  // ── Recents (localStorage) ──
  const recents = useMemo<SearchResult[]>(() => {
    return readRecents().map((r, i) => ({
      id:    `recent:${i}:${r.route}`,
      kind:  'recent' as ResultKind,
      label: r.label,
      sub:   r.route,
      icon:  r.icon,
      route: r.route,
      score: 0,
    }))
  }, []) // computed once on open; recents don't change while palette is visible

  // ── Result computation ──
  const { allResults, sections } = useMemo<{
    allResults: SearchResult[]
    sections: Array<{ kind: ResultKind; label: string; items: SearchResult[] }>
  }>(() => {
    const q = query.trim().toLowerCase()

    // Empty query: show recents + quick actions
    if (!q) {
      const bucket = new Map<ResultKind, SearchResult[]>([
        ['recent', recents],
        ['action', QUICK_ACTIONS],
      ])
      const all = [...recents, ...QUICK_ACTIONS]
      const secs = SECTION_ORDER
        .map(k => ({ kind: k, label: SECTION_LABELS[k], items: bucket.get(k) ?? [] }))
        .filter(s => s.items.length > 0)
      return { allResults: all, sections: secs }
    }

    // Score and collect by kind
    const byKind = new Map<ResultKind, SearchResult[]>()

    function add(result: SearchResult, score: number) {
      if (score <= 0) return
      const r = { ...result, score }
      const list = byKind.get(r.kind) ?? []
      list.push(r)
      byKind.set(r.kind, list)
    }

    // Nav items (instant, no API)
    for (const leaf of flatNavLeaves()) {
      const score = Math.max(scoreMatch(q, leaf.label), scoreMatch(q, leaf.id))
      add({
        id: `nav:${leaf.id}`, kind: 'nav',
        label: leaf.label, sub: 'Navigate',
        icon: leaf.icon, route: leaf.route,
      }, score)
    }

    // Quick actions (match label + sub)
    for (const action of QUICK_ACTIONS) {
      const score = Math.max(scoreMatch(q, action.label), scoreMatch(q, action.sub))
      add(action, score)
    }

    // API-backed (only at 2+ chars)
    if (enabled) {
      // Pools
      for (const p of poolsQ.data?.pools ?? poolsQ.data?.data ?? []) {
        const score = Math.max(scoreMatch(q, p.name), scoreMatch(q, p.health))
        add({
          id: `pool:${p.name}`, kind: 'pool',
          label: p.name,
          sub: `Pool · ${p.health}${p.capacity ? ` · ${p.capacity}` : ''}`,
          icon: 'water', route: '/pools',
        }, score)
      }

      // Datasets
      for (const d of (datasetsQ.data?.data ?? []).slice(0, 40)) {
        const score = Math.max(scoreMatch(q, d.name), scoreMatch(q, d.name.split('/').pop() ?? ''))
        add({
          id: `dataset:${d.name}`, kind: 'dataset',
          label: d.name.split('/').pop() ?? d.name,
          sub: d.name,
          icon: 'dataset', route: '/datasets',
        }, score)
      }

      // Containers
      for (const c of containersQ.data?.containers ?? []) {
        const score = Math.max(scoreMatch(q, c.name), scoreMatch(q, c.image))
        add({
          id: `container:${c.id}`, kind: 'container',
          label: c.name,
          sub: c.image,
          icon: 'developer_board', route: '/docker',
        }, score)
      }

      // Shares
      for (const s of sharesQ.data?.shares ?? []) {
        const score = Math.max(scoreMatch(q, s.name), scoreMatch(q, s.path))
        add({
          id: `share:${s.name}`, kind: 'share',
          label: s.name,
          sub: s.path,
          icon: 'folder_shared', route: '/shares',
        }, score)
      }
    }

    // HA nodes (from cache - always available if HA page was visited)
    for (const node of haNodes) {
      const score = Math.max(scoreMatch(q, node.name ?? node.id), scoreMatch(q, node.id))
      add({
        id: `ha-node:${node.id}`, kind: 'ha-node',
        label: node.name ?? node.id,
        sub: `${node.role ?? 'unknown'} · ${node.state ?? 'unknown'}`,
        icon: 'computer', route: '/ha',
      }, score)
    }

    // Sort each kind by score desc, cap at 6 items each
    const secs = SECTION_ORDER
      .map(k => {
        const items = (byKind.get(k) ?? [])
          .sort((a, b) => (b.score ?? 0) - (a.score ?? 0))
          .slice(0, 6)
        return { kind: k, label: SECTION_LABELS[k], items }
      })
      .filter(s => s.items.length > 0)

    const all = secs.flatMap(s => s.items)
    return { allResults: all, sections: secs }
  }, [query, enabled, recents, haNodes,
      datasetsQ.data, poolsQ.data, containersQ.data, sharesQ.data])

  // ── Flatten sections to a flat index for keyboard navigation ──
  const flatResults = allResults // sections already flattened

  // Clamp active index when results shrink - derived inline, no effect needed
  const clampedActiveIdx = flatResults.length === 0 ? 0 : Math.min(activeIdx, flatResults.length - 1)

  // Focus input on mount
  useEffect(() => { inputRef.current?.focus() }, [])

  // Scroll active item into view
  useEffect(() => {
    const item = listRef.current?.querySelector<HTMLElement>(`[data-flat-idx="${clampedActiveIdx}"]`)
    item?.scrollIntoView({ block: 'nearest' })
  }, [clampedActiveIdx])

  const select = useCallback((result: SearchResult) => {
    // Persist to recents for non-action/non-recent entries
    if (result.kind !== 'recent') {
      writeRecent({ route: result.route, label: result.label, icon: result.icon })
    }
    navigate({ to: result.route as never })
    onClose()
  }, [navigate, onClose])

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') { onClose(); return }
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActiveIdx(i => Math.min(i + 1, flatResults.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActiveIdx(i => Math.max(i - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const r = flatResults[clampedActiveIdx]
      if (r) select(r)
    }
  }

  // Focus trap
  function handlePanelKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    if (e.key !== 'Tab') return
    const focusable = Array.from(panelRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [])
    if (!focusable.length) return
    const [first, last] = [focusable[0], focusable[focusable.length - 1]]
    if (e.shiftKey) { if (document.activeElement === first) { e.preventDefault(); last.focus() } }
    else            { if (document.activeElement === last)  { e.preventDefault(); first.focus() } }
  }

  const isLoading = enabled && (
    datasetsQ.isFetching || poolsQ.isFetching ||
    containersQ.isFetching || sharesQ.isFetching
  )

  const activeId = flatResults[clampedActiveIdx] ? `${listId}-opt-${clampedActiveIdx}` : undefined

  // Build a flat index counter across sections for aria and keyboard tracking
  let flatIdx = 0

  const modalRoot = document.getElementById('modal-root')
  if (!modalRoot) return null

  return createPortal(
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 'var(--z-overlay)',
        background: 'rgba(0,0,0,0.6)',
        backdropFilter: 'blur(4px)',
        display: 'flex', alignItems: 'flex-start', justifyContent: 'center',
        paddingTop: 'clamp(60px, 12vh, 140px)',
      }}
      onClick={e => e.target === e.currentTarget && onClose()}
    >
      <div
        ref={panelRef}
        id={dialogId}
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        onKeyDown={handlePanelKeyDown}
        style={{
          width: '100%', maxWidth: 620,
          background: 'var(--surface)',
          border: '1px solid var(--border)',
          borderRadius: 'var(--radius-xl)',
          boxShadow: 'var(--shadow-xl)',
          overflow: 'hidden',
        }}
      >
        {/* ── Search input ── */}
        <div style={{
          display: 'flex', alignItems: 'center', gap: 12,
          padding: '14px 18px',
          borderBottom: sections.length > 0 ? '1px solid var(--border-subtle)' : '1px solid transparent',
        }}>
          <Icon
            name={isLoading ? 'sync' : 'search'}
            size={20}
            style={{
              color: 'var(--text-tertiary)', flexShrink: 0,
              animation: isLoading ? 'spin 1s linear infinite' : 'none',
            }}
            aria-hidden="true"
          />
          <input
            ref={inputRef}
            role="combobox"
            aria-expanded={flatResults.length > 0}
            aria-controls={listId}
            aria-activedescendant={activeId}
            aria-label="Search pages, pools, containers, shares, or type a command"
            aria-autocomplete="list"
            value={query}
            onChange={e => handleQueryChange(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Search or run a command…"
            style={{
              flex: 1, background: 'none', border: 'none', outline: 'none',
              fontSize: 'var(--text-md)', color: 'var(--text)',
              fontFamily: 'inherit',
            }}
            autoComplete="off"
            spellCheck={false}
          />
          <kbd style={{
            padding: '2px 6px', borderRadius: 4, fontSize: 11,
            background: 'var(--bg-card)', border: '1px solid var(--border)',
            color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', flexShrink: 0,
          }}>
            Esc
          </kbd>
        </div>

        {/* ── Grouped results ── */}
        {sections.length > 0 && (
          <ul
            ref={listRef}
            id={listId}
            role="listbox"
            aria-label="Command palette results"
            style={{ listStyle: 'none', margin: 0, padding: '6px 0', maxHeight: 440, overflowY: 'auto' }}
          >
            {sections.map(section => {
              const sectionStart = flatIdx
              return (
                <li key={section.kind} role="presentation">
                  {/* Section header */}
                  <div style={{
                    padding: '6px 18px 3px',
                    fontSize: 'var(--text-2xs)',
                    fontWeight: 700,
                    color: 'var(--text-tertiary)',
                    textTransform: 'uppercase',
                    letterSpacing: '0.07em',
                    // Separator above all sections except the first
                    ...(sectionStart > 0 ? { borderTop: '1px solid var(--border-subtle)', paddingTop: 10, marginTop: 4 } : {}),
                  }}>
                    {section.label}
                  </div>

                  {/* Section items */}
                  {section.items.map(r => {
                    const myIdx = flatIdx++
                    const isActive = myIdx === clampedActiveIdx
                    return (
                      <li
                        key={r.id}
                        id={`${listId}-opt-${myIdx}`}
                        role="option"
                        aria-selected={isActive}
                        data-flat-idx={myIdx}
                        onClick={() => select(r)}
                        onMouseEnter={() => setActiveIdx(myIdx)}
                        style={{
                          display: 'flex', alignItems: 'center', gap: 12,
                          padding: '9px 18px', cursor: 'pointer',
                          background: isActive ? 'var(--primary-bg)' : 'transparent',
                          transition: 'background 0.08s',
                          outline: 'none',
                        }}
                      >
                        {/* Icon badge */}
                        <span style={{
                          width: 30, height: 30, borderRadius: 'var(--radius-sm)', flexShrink: 0,
                          background: isActive
                            ? 'hsla(var(--hue-primary),100%,72%,.15)'
                            : r.kind === 'action' ? 'var(--success-bg)'
                            : r.kind === 'recent' ? 'var(--surface)'
                            : 'var(--bg-card)',
                          border: `1px solid ${isActive
                            ? 'hsla(var(--hue-primary),100%,72%,.3)'
                            : r.kind === 'action' ? 'var(--success-border)'
                            : 'var(--border)'}`,
                          display: 'flex', alignItems: 'center', justifyContent: 'center',
                        }} aria-hidden="true">
                          <Icon
                            name={r.kind === 'recent' ? 'history' : r.icon}
                            size={15}
                            style={{
                              color: isActive ? 'var(--primary)'
                                : r.kind === 'action' ? 'var(--success)'
                                : 'var(--text-tertiary)',
                            }}
                          />
                        </span>

                        {/* Label + subtitle */}
                        <span style={{ flex: 1, minWidth: 0 }}>
                          <span style={{
                            display: 'block', fontWeight: 600, fontSize: 'var(--text-sm)',
                            color: 'var(--text)',
                            whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
                          }}>
                            {r.label}
                          </span>
                          <span style={{
                            display: 'block', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)',
                            whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
                          }}>
                            {r.sub}
                          </span>
                        </span>

                        {/* Active: show Enter hint */}
                        {isActive && (
                          <kbd style={{
                            padding: '2px 6px', borderRadius: 4, fontSize: 10,
                            background: 'var(--bg-card)', border: '1px solid var(--border)',
                            color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', flexShrink: 0,
                          }} aria-hidden="true">
                            Enter
                          </kbd>
                        )}
                      </li>
                    )
                  })}
                </li>
              )
            })}
          </ul>
        )}

        {/* ── Empty-query hint (no recents yet) ── */}
        {!query.trim() && sections.length === 0 && (
          <div style={{ padding: '28px 18px', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
            Start typing to search pages, pools, containers and more
          </div>
        )}

        {/* ── No results for active query ── */}
        {query.trim() && flatResults.length === 0 && !isLoading && (
          <div style={{ padding: '28px 18px', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
            No results for <strong style={{ color: 'var(--text-secondary)' }}>"{query}"</strong>
          </div>
        )}

        {/* ── Footer hints ── */}
        <div style={{
          padding: '10px 18px',
          borderTop: sections.length > 0 ? '1px solid var(--border-subtle)' : 'none',
          display: 'flex', gap: 16, fontSize: 11, color: 'var(--text-tertiary)',
        }}>
          {[['↑↓', 'Navigate'], ['Enter', 'Open'], ['Esc', 'Close']].map(([k, v]) => (
            <span key={k} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
              <kbd style={{
                padding: '1px 5px', borderRadius: 3,
                background: 'var(--bg-card)', border: '1px solid var(--border)',
                fontFamily: 'var(--font-mono)',
              }} aria-hidden="true">{k}</kbd>
              {v}
            </span>
          ))}
          {flatResults.length > 0 && (
            <span style={{ marginLeft: 'auto' }}>{flatResults.length} result{flatResults.length !== 1 ? 's' : ''}</span>
          )}
        </div>
      </div>

      {/* Live region for screen reader announcements */}
      <div
        id={liveId}
        aria-live="polite"
        aria-atomic="true"
        style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0,0,0,0)', whiteSpace: 'nowrap' }}
      >
        {query.trim() && !isLoading
          ? `${flatResults.length} result${flatResults.length !== 1 ? 's' : ''} found`
          : ''}
      </div>
    </div>,
    modalRoot,
  )
}
