/**
 * KeyboardHelpModal - lists all registered global keyboard shortcuts.
 * Triggered by the `?` key. Accessible: role=dialog, focus trap, Escape closes.
 */

import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { Icon } from './Icon'

interface ShortcutEntry {
  keys: string[]
  desc: string
}

interface ShortcutSection {
  title: string
  shortcuts: ShortcutEntry[]
}

const SECTIONS: ShortcutSection[] = [
  {
    title: 'Interface',
    shortcuts: [
      { keys: ['Ctrl', 'K'],    desc: 'Open global search' },
      { keys: ['?'],            desc: 'Show keyboard shortcuts' },
      { keys: ['Esc'],          desc: 'Close dialog / search' },
    ],
  },
  {
    title: 'Navigate (g + key)',
    shortcuts: [
      { keys: ['g', 'h'],  desc: 'Dashboard' },
      { keys: ['g', 'p'],  desc: 'ZFS Pools' },
      { keys: ['g', 'd'],  desc: 'Datasets' },
      { keys: ['g', 's'],  desc: 'Settings' },
      { keys: ['g', 'n'],  desc: 'Network' },
      { keys: ['g', 'l'],  desc: 'Logs' },
      { keys: ['g', 'c'],  desc: 'Docker' },
      { keys: ['g', 'f'],  desc: 'File Explorer' },
      { keys: ['g', 'r'],  desc: 'Reporting' },
      { keys: ['g', 'u'],  desc: 'System Updates' },
    ],
  },
]

function Kbd({ k }: { k: string }) {
  return (
    <kbd style={{
      display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
      minWidth: 22, height: 20, padding: '0 5px',
      background: 'var(--bg)', border: '1px solid var(--border)',
      borderBottomWidth: 2, borderRadius: 4,
      fontSize: 'var(--text-2xs)', fontFamily: 'var(--font-mono)',
      fontWeight: 700, color: 'var(--text-secondary)',
      boxShadow: '0 1px 1px rgba(0,0,0,0.2)',
      userSelect: 'none',
    }}>
      {k}
    </kbd>
  )
}

export function KeyboardHelpModal({ onClose }: { onClose: () => void }) {
  const dialogRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    function handler(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  useEffect(() => {
    const first = dialogRef.current?.querySelector<HTMLElement>('button')
    first?.focus()
  }, [])

  const portal = document.getElementById('modal-root') ?? document.body

  return createPortal(
    <div
      style={{
        position: 'fixed', inset: 0, zIndex: 9000,
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        background: 'rgba(0,0,0,0.55)', backdropFilter: 'blur(4px)',
        animation: 'fadeIn 0.15s ease',
      }}
      onClick={e => { if (e.target === e.currentTarget) onClose() }}
      aria-hidden="true"
    >
      <div
        ref={dialogRef}
        role="dialog"
        aria-modal="true"
        aria-label="Keyboard shortcuts"
        aria-hidden="false"
        style={{
          width: 460, maxWidth: '90vw', maxHeight: '80vh', overflow: 'auto',
          background: 'var(--bg-elevated)', backdropFilter: 'var(--blur-glass)',
          border: '1px solid var(--border-highlight)', borderRadius: 'var(--radius-xl)',
          boxShadow: 'var(--shadow-xl)', padding: '22px 24px',
          animation: 'slideUp 0.2s ease',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 20 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Icon name="keyboard" size={18} style={{ color: 'var(--primary)' }} />
            <span style={{ fontSize: 'var(--text-md)', fontWeight: 700 }}>Keyboard Shortcuts</span>
          </div>
          <button onClick={onClose} className="btn btn-ghost btn-xs" aria-label="Close shortcuts">
            <Icon name="close" size={14} />
          </button>
        </div>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
          {SECTIONS.map(sec => (
            <section key={sec.title}>
              <div style={{
                fontSize: 'var(--text-xs)', fontWeight: 700, color: 'var(--text-tertiary)',
                textTransform: 'uppercase', letterSpacing: '0.7px', marginBottom: 10,
              }}>
                {sec.title}
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
                {sec.shortcuts.map((s, i) => (
                  <div key={i} style={{
                    display: 'flex', alignItems: 'center', gap: 12,
                    padding: '6px 8px', borderRadius: 'var(--radius-sm)',
                  }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 4, minWidth: 100, flexShrink: 0 }}>
                      {s.keys.map((k, ki) => (
                        <>
                          <Kbd key={k} k={k} />
                          {ki < s.keys.length - 1 && (
                            <span key={`sep-${ki}`} style={{ fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' }}>then</span>
                          )}
                        </>
                      ))}
                    </div>
                    <span style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{s.desc}</span>
                  </div>
                ))}
              </div>
            </section>
          ))}
        </div>

        <div style={{ marginTop: 20, paddingTop: 14, borderTop: '1px solid var(--border-subtle)', fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)', textAlign: 'center' }}>
          Press <Kbd k="?" /> again or <Kbd k="Esc" /> to close
        </div>
      </div>
    </div>,
    portal,
  )
}
