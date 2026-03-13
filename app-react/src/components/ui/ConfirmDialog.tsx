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
 * Renders into a proper Modal with the existing design system.
 * Uses the same .btn, .modal, .modal-overlay classes as the rest of the UI.
 */

import { useState, useCallback, useRef } from 'react'
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
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

export function useConfirm() {
  const [state, setState] = useState<{
    open: boolean
    opts: ConfirmOptions
  }>({
    open: false,
    opts: { title: '' },
  })

  const resolveRef = useRef<(value: boolean) => void>(() => {})

  const confirm = useCallback((opts: ConfirmOptions): Promise<boolean> => {
    setState({ open: true, opts })
    return new Promise<boolean>((resolve) => {
      resolveRef.current = resolve
    })
  }, [])

  const handleConfirm = useCallback(() => {
    setState((s) => ({ ...s, open: false }))
    resolveRef.current(true)
  }, [])

  const handleCancel = useCallback(() => {
    setState((s) => ({ ...s, open: false }))
    resolveRef.current(false)
  }, [])

  const ConfirmDialog = useCallback(() => {
    if (!state.open) return null
    const { title, message, confirmLabel = 'Confirm', cancelLabel = 'Cancel', danger = false } = state.opts

    return (
      <div
        className="modal-overlay"
        onClick={(e) => e.target === e.currentTarget && handleCancel()}
        style={{ zIndex: 'var(--z-overlay)' } as React.CSSProperties}
      >
        <div className="modal modal-sm" style={{ gap: 20 }}>
          {/* Icon + title */}
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 14 }}>
            <div style={{
              width: 40, height: 40, borderRadius: 'var(--radius-md)', flexShrink: 0,
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              background: danger ? 'var(--error-bg)' : 'var(--warning-bg)',
              border: `1px solid ${danger ? 'var(--error-border)' : 'var(--warning-border)'}`,
            }}>
              <Icon
                name={danger ? 'delete_forever' : 'help'}
                size={20}
                style={{ color: danger ? 'var(--error)' : 'var(--warning)' }}
              />
            </div>
            <div>
              <div className="modal-title" style={{ fontSize: 'var(--text-base)', marginBottom: message ? 4 : 0 }}>
                {title}
              </div>
              {message && (
                <div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', lineHeight: 1.5 }}>
                  {message}
                </div>
              )}
            </div>
          </div>

          {/* Actions */}
          <div className="modal-footer">
            <button className="btn btn-ghost btn-sm" onClick={handleCancel}>
              {cancelLabel}
            </button>
            <button
              className={`btn btn-sm ${danger ? 'btn-danger' : 'btn-primary'}`}
              onClick={handleConfirm}
              autoFocus
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
