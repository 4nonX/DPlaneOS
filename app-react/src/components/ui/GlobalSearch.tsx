/**
 * components/ui/GlobalSearch.tsx
 *
 * Cmd/Ctrl+K command palette. Searches nav items instantly, then queries
 * ZFS datasets, pools, Docker containers, and SMB shares from the API.
 *
 * ARIA: role="dialog" > role="combobox" input + role="listbox" results.
 * Focus trap, aria-activedescendant, aria-live result count.
 */

import { useState, useEffect, useRef, useId, useCallback, useMemo } from 'react'
import { createPortal } from 'react-dom'
import { useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from './Icon'
import { NAV } from '@/components/layout/navConfig'
import type { NavLeaf } from '@/components/layout/navConfig'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type ResultKind = 'nav' | 'dataset' | 'pool' | 'container' | 'share'

interface SearchResult {
  id: string
  kind: ResultKind
  label: string
  sub: string
  icon: string
  route: string
}

interface ZFSDataset  { name: string; used: string; avail: string; mountpoint: string; quota: string }
interface ZFSPool     { name: string; health: string; capacity: string; size: string }
interface Container   { id: string; name: string; state: string; image: string }
interface Share       { name: string; path: string }

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const FOCUSABLE = 'a[href],button:not([disabled]),input:not([disabled]),[tabindex]:not([tabindex="-1"])'

function flatNavLeaves(): NavLeaf[] {
  const leaves: NavLeaf[] = []
  for (const item of NAV) {
    if (item.kind === 'leaf')  leaves.push(item)
    if (item.kind === 'group') item.children.forEach(c => leaves.push(c))
  }
  return leaves
}

function matchNav(query: string): SearchResult[] {
  const q = query.toLowerCase()
  return flatNavLeaves()
    .filter(l => l.label.toLowerCase().includes(q))
    .map(l => ({
      id: `nav:${l.id}`,
      kind: 'nav' as ResultKind,
      label: l.label,
      sub: 'Navigation',
      icon: l.icon,
      route: l.route,
    }))
}

// ---------------------------------------------------------------------------
// GlobalSearch
// ---------------------------------------------------------------------------

interface GlobalSearchProps {
  onClose: () => void
}

export function GlobalSearch({ onClose }: GlobalSearchProps) {
  const [query, setQuery] = useState('')
  const [activeIdx, setActiveIdx] = useState(0)
  const inputRef   = useRef<HTMLInputElement>(null)
  const listRef    = useRef<HTMLUListElement>(null)
  const panelRef   = useRef<HTMLDivElement>(null)
  const navigate   = useNavigate()
  const dialogId   = useId()
  const listId     = useId()
  const liveId     = useId()

  const enabled = query.trim().length >= 2

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

  const results = useMemo<SearchResult[]>(() => {
    const q = query.trim().toLowerCase()
    if (!q) return []

    const nav = matchNav(q)

    if (!enabled) return nav

    const datasets = (datasetsQ.data?.data ?? [])
      .filter(d => d.name.toLowerCase().includes(q))
      .slice(0, 6)
      .map<SearchResult>(d => ({
        id: `dataset:${d.name}`,
        kind: 'dataset',
        label: d.name.split('/').pop() ?? d.name,
        sub: d.name,
        icon: 'dataset',
        route: '/datasets',
      }))

    const pools = ((poolsQ.data?.pools ?? poolsQ.data?.data) ?? [])
      .filter(p => p.name.toLowerCase().includes(q))
      .map<SearchResult>(p => ({
        id: `pool:${p.name}`,
        kind: 'pool',
        label: p.name,
        sub: `Pool - ${p.health} - ${p.capacity} used`,
        icon: 'water',
        route: '/pools',
      }))

    const containers = (containersQ.data?.containers ?? [])
      .filter(c => c.name.toLowerCase().includes(q) || c.image.toLowerCase().includes(q))
      .slice(0, 6)
      .map<SearchResult>(c => ({
        id: `container:${c.id}`,
        kind: 'container',
        label: c.name,
        sub: `Container - ${c.image}`,
        icon: 'developer_board',
        route: '/docker',
      }))

    const shares = (sharesQ.data?.shares ?? [])
      .filter(s => s.name.toLowerCase().includes(q) || s.path.toLowerCase().includes(q))
      .map<SearchResult>(s => ({
        id: `share:${s.name}`,
        kind: 'share',
        label: s.name,
        sub: s.path,
        icon: 'folder_shared',
        route: '/shares',
      }))

    return [...nav, ...pools, ...datasets, ...containers, ...shares]
  }, [query, enabled, datasetsQ.data, poolsQ.data, containersQ.data, sharesQ.data])

  // Reset active index when results change
  useEffect(() => { setActiveIdx(0) }, [results.length])

  // Focus input on mount
  useEffect(() => { inputRef.current?.focus() }, [])

  // Scroll active item into view
  useEffect(() => {
    const item = listRef.current?.querySelector<HTMLElement>(`[data-idx="${activeIdx}"]`)
    item?.scrollIntoView({ block: 'nearest' })
  }, [activeIdx])

  const select = useCallback((result: SearchResult) => {
    navigate({ to: result.route as never })
    onClose()
  }, [navigate, onClose])

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === 'Escape') { onClose(); return }
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setActiveIdx(i => Math.min(i + 1, results.length - 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setActiveIdx(i => Math.max(i - 1, 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (results[activeIdx]) select(results[activeIdx])
    }
  }

  // Focus trap
  function handlePanelKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    if (e.key !== 'Tab') return
    const focusable = Array.from(panelRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [])
    if (!focusable.length) return
    const first = focusable[0], last = focusable[focusable.length - 1]
    if (e.shiftKey) { if (document.activeElement === first) { e.preventDefault(); last.focus() } }
    else            { if (document.activeElement === last)  { e.preventDefault(); first.focus() } }
  }

  const activeId = results[activeIdx] ? `${listId}-opt-${activeIdx}` : undefined
  const isLoading = enabled && (datasetsQ.isFetching || poolsQ.isFetching || containersQ.isFetching || sharesQ.isFetching)

  const modalRoot = document.getElementById('modal-root')
  if (!modalRoot) return null

  return createPortal(
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 9000,
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
        aria-label="Global search"
        onKeyDown={handlePanelKeyDown}
        style={{
          width: '100%', maxWidth: 600,
          background: 'var(--surface)',
          border: '1px solid var(--border)',
          borderRadius: 'var(--radius-xl)',
          boxShadow: 'var(--shadow-xl)',
          overflow: 'hidden',
        }}
      >
        {/* Search input */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '14px 18px', borderBottom: `1px solid ${results.length ? 'var(--border-subtle)' : 'transparent'}` }}>
          <Icon name={isLoading ? 'sync' : 'search'} size={20} style={{ color: 'var(--text-tertiary)', flexShrink: 0, animation: isLoading ? 'spin 1s linear infinite' : 'none' }} aria-hidden="true" />
          <input
            ref={inputRef}
            role="combobox"
            aria-expanded={results.length > 0}
            aria-controls={listId}
            aria-activedescendant={activeId}
            aria-label="Search pages, datasets, containers, shares"
            aria-autocomplete="list"
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Search pages, datasets, containers, shares…"
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
            color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)',
            flexShrink: 0,
          }}>
            Esc
          </kbd>
        </div>

        {/* Results */}
        {results.length > 0 && (
          <ul
            ref={listRef}
            id={listId}
            role="listbox"
            aria-label="Search results"
            style={{ listStyle: 'none', margin: 0, padding: '6px 0', maxHeight: 400, overflowY: 'auto' }}
          >
            {results.map((r, i) => (
              <li
                key={r.id}
                id={`${listId}-opt-${i}`}
                role="option"
                aria-selected={i === activeIdx}
                data-idx={i}
                onClick={() => select(r)}
                onMouseEnter={() => setActiveIdx(i)}
                style={{
                  display: 'flex', alignItems: 'center', gap: 12,
                  padding: '10px 18px', cursor: 'pointer',
                  background: i === activeIdx ? 'var(--primary-bg)' : 'transparent',
                  transition: 'background 0.1s',
                  outline: 'none',
                }}
              >
                <span style={{
                  width: 32, height: 32, borderRadius: 'var(--radius-sm)',
                  background: i === activeIdx ? 'hsla(var(--hue-primary),100%,72%,.15)' : 'var(--bg-card)',
                  border: `1px solid ${i === activeIdx ? 'hsla(var(--hue-primary),100%,72%,.25)' : 'var(--border)'}`,
                  display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0,
                }} aria-hidden="true">
                  <Icon name={r.icon} size={16} style={{ color: i === activeIdx ? 'var(--primary)' : 'var(--text-tertiary)' }} />
                </span>
                <span style={{ flex: 1, minWidth: 0 }}>
                  <span style={{ display: 'block', fontWeight: 600, fontSize: 'var(--text-sm)', color: 'var(--text)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                    {r.label}
                  </span>
                  <span style={{ display: 'block', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                    {r.sub}
                  </span>
                </span>
                {i === activeIdx && (
                  <kbd style={{ padding: '2px 6px', borderRadius: 4, fontSize: 10, background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--text-tertiary)', fontFamily: 'var(--font-mono)', flexShrink: 0 }} aria-hidden="true">
                    Enter
                  </kbd>
                )}
              </li>
            ))}
          </ul>
        )}

        {/* Empty state */}
        {query.trim() && results.length === 0 && !isLoading && (
          <div style={{ padding: '28px 18px', textAlign: 'center', color: 'var(--text-tertiary)', fontSize: 'var(--text-sm)' }}>
            No results for <strong style={{ color: 'var(--text-secondary)' }}>"{query}"</strong>
          </div>
        )}

        {/* Hint footer */}
        {!query.trim() && (
          <div style={{ padding: '12px 18px', display: 'flex', gap: 16, fontSize: 11, color: 'var(--text-tertiary)' }}>
            {[['↑↓', 'Navigate'], ['Enter', 'Go'], ['Esc', 'Close']].map(([k, v]) => (
              <span key={k} style={{ display: 'flex', alignItems: 'center', gap: 4 }}>
                <kbd style={{ padding: '1px 5px', borderRadius: 3, background: 'var(--bg-card)', border: '1px solid var(--border)', fontFamily: 'var(--font-mono)' }} aria-hidden="true">{k}</kbd>
                {v}
              </span>
            ))}
          </div>
        )}
      </div>

      {/* Live region for screen reader result count announcements */}
      <div id={liveId} aria-live="polite" aria-atomic="true" style={{ position: 'absolute', width: 1, height: 1, overflow: 'hidden', clip: 'rect(0,0,0,0)', whiteSpace: 'nowrap' }}>
        {query.trim() && !isLoading ? `${results.length} result${results.length !== 1 ? 's' : ''} found` : ''}
      </div>
    </div>,
    modalRoot,
  )
}
