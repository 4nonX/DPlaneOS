/**
 * components/ui/ConfirmDialog.tsx
 *
 * Modern inline confirmation dialog — replaces window.confirm() entirely.
 *
 * Usage (hook-based):
 *
 *   const { confirm, ConfirmDialog } = useConfirm()
 *
 *   // In JSX:
 *   <ConfirmDialog />
 *
 *   // To trigger:
 *   if (await confirm({ title: 'Delete pool?', message: 'This cannot be undone.', danger: true })) {
 *     deletePool()
 *   }
 *
 * Enhanced with:
 *   - contextInfo: Show additional info (size, child count, etc.)
 *   - confirmText: Require typing specific text to confirm (for destructive ops)
 *
 * Renders into a proper Modal with the existing design system.
 * Uses the same .btn, .modal, .modal-overlay classes as the rest of the UI.
 */

import { useState, useCallback, useRef, useEffect } from 'react'
import { Icon } from './Icon'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface ConfirmOptions {
  title: string
  message?: string
  confirmLabel?: string
  cancelLabel?: string
  danger?: boolean
  /** Additional context info to display */
  contextInfo?: { label: string; value: string }[]
  /** Require typing specific text to confirm */
  confirmText?: string
}

type ConfirmState = {
  open: boolean
  opts: ConfirmOptions
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

export function useConfirm() {
  const [state, setState] = useState<ConfirmState>({
    open: false,
    opts: { title: '' },
  })

  const resolveRef = useRef<(value: boolean) => void>(() => {})
  const inputRef = useRef<HTMLInputElement>(null)

  const confirm = useCallback((opts: ConfirmOptions): Promise<boolean> => {
    setState({ open: true, opts })
    return new Promise<boolean>((resolve) => {
      resolveRef.current = resolve
    })
  }, [])

  const handleConfirm = useCallback(() => {
    const { confirmText } = state.opts
    if (confirmText) {
      const inputVal = inputRef.current?.value
      if (inputVal !== confirmText) return // Don't close
    }
    setState((s) => ({ ...s, open: false }))
    resolveRef.current(true)
  }, [state.opts])

  const handleCancel = useCallback(() => {
    setState((s) => ({ ...s, open: false }))
    resolveRef.current(false)
  }, [])

  // Focus input when dialog opens with confirmText
  useEffect(() => {
    if (state.open && state.opts.confirmText && inputRef.current) {
      setTimeout(() => inputRef.current?.focus(), 100)
    }
  }, [state.open, state.opts.confirmText])

  const ConfirmDialog = useCallback(() => {
    if (!state.open) return null
    const { 
      title, 
      message, 
      confirmLabel = 'Confirm', 
      cancelLabel = 'Cancel', 
      danger = false,
      contextInfo,
      confirmText 
    } = state.opts

    const matchesConfirm = !confirmText || inputRef.current?.value === confirmText

    return (
      <div
        className="modal-overlay"
        onClick={(e) => e.target === e.currentTarget && handleCancel()}
        style={{ zIndex: 'var(--z-overlay)' } as React.CSSProperties}
      >
        <div className={`modal ${danger ? 'modal-danger' : ''}`} style={{ gap: 20, minWidth: 360 }}>
          {/* Icon + title */}
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14 }}>
            <div style={{
              width: 44, height: 44, borderRadius: 'var(--radius-md)', flexShrink: 0,
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              background: danger ? 'var(--error-bg)' : 'var(--warning-bg)',
              border: `1px solid ${danger ? 'var(--error-border)' : 'var(--warning-border)'}`}}>
              <Icon
                name={danger ? 'delete_forever' : 'help'}
                size={22}
                style={{ color: danger ? 'var(--error)' : 'var(--warning)' }}
              />
            </div>
            <div style={{ flex: 1 }}>
              <div className="modal-title" style={{ 
                fontSize: 'var(--text-base)', 
                marginBottom: (message || contextInfo) ? 8 : 0,
                color: danger ? 'var(--error)' : undefined
              }}>
                {title}
              </div>
              {message && (
                <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', lineHeight: 1.5 }}>
                  {message}
                </div>
              )}
              
              {/* Context info */}
              {contextInfo && contextInfo.length > 0 && (
                <div style={{ 
                  marginTop: 12, 
                  padding: 12, 
                  background: 'var(--surface)', 
                  borderRadius: 'var(--radius-sm)',
                  border: '1px solid var(--border)'
                }}>
                  {contextInfo.map((info, i) => (
                    <div key={i} style={{ 
                      display: 'flex', 
                      justifyContent: 'space-between', 
                      fontSize: 'var(--text-xs)',
                      padding: '2px 0'
                    }}>
                      <span style={{ color: 'var(--text-tertiary)' }}>{info.label}</span>
                      <span style={{ color: 'var(--text)', fontFamily: 'var(--font-mono)', fontWeight: 500 }}>
                        {info.value}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>

          {/* Typed confirmation for dangerous ops */}
          {confirmText && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              <label style={{ fontSize: 'var(--text-xs)', color: 'var(--text-secondary)' }}>
                Type <code style={{ 
                  fontFamily: 'var(--font-mono)', 
                  background: 'var(--surface)',
                  padding: '2px 6px',
                  borderRadius: 4,
                  color: 'var(--primary)'
                }}>{confirmText}</code> to confirm
              </label>
              <input
                ref={inputRef}
                type="text"
                className="input"
                placeholder={`Type "${confirmText}"`}
                style={{ fontFamily: 'var(--font-mono)' }}
              />
            </div>
          )}

          {/* Actions */}
          <div className="modal-footer">
            <button className="btn btn-ghost btn-sm" onClick={handleCancel}>
              {cancelLabel}
            </button>
            <button
              className={`btn btn-sm ${danger ? 'btn-danger' : 'btn-primary'}`}
              onClick={handleConfirm}
              disabled={!!confirmText && !matchesConfirm}
              autoFocus={!confirmText}
              style={{ opacity: confirmText && !matchesConfirm ? 0.5 : 1 }}
            >
              <Icon name={danger ? 'delete' : 'check'} size={14} />
              {confirmLabel}
            </button>
          </div>
        </div>
      </div>
    )
  }, [state, handleConfirm, handleCancel])

  return { confirm, ConfirmDialog }
}
