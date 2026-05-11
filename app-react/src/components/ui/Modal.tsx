/**
 * components/ui/Modal.tsx - Shared modal dialog component
 *
 * Usage:
 *   <Modal title="Edit User" onClose={handleClose}>
 *     ...content...
 *     <div className="modal-footer">
 *       <button className="btn btn-ghost" onClick={handleClose}>Cancel</button>
 *       <button className="btn btn-primary" onClick={handleSave}>Save</button>
 *     </div>
 *   </Modal>
 *
 * Size variants: size="sm" | "md" (default) | "lg"
 * Clicking the backdrop calls onClose.
 */

import type { ReactNode } from 'react'
import type React from 'react'
import { useRef, useEffect, useId } from 'react'
import { createPortal } from 'react-dom'
import { Icon } from './Icon'

interface ModalProps {
  title?: ReactNode
  onClose: () => void
  children: ReactNode
  size?: 'sm' | 'md' | 'lg'
}

const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[contenteditable="true"]',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

export function Modal({ title, onClose, children, size = 'md' }: ModalProps) {
  const sizeClass = size === 'sm' ? 'modal-sm' : size === 'lg' ? 'modal-lg' : ''
  const modalRoot = document.getElementById('modal-root')
  const panelRef = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const prevFocusRef = useRef<Element | null>(null)
  // Stable ref so the window listener never needs to be re-registered when
  // the parent re-renders and produces a new onClose function reference.
  const onCloseRef = useRef(onClose)
  useEffect(() => { onCloseRef.current = onClose }, [onClose])

  useEffect(() => {
    prevFocusRef.current = document.activeElement
    const first = panelRef.current?.querySelector<HTMLElement>(FOCUSABLE)
    first?.focus()

    function handleEscape(e: KeyboardEvent) {
      if (e.key === 'Escape') onCloseRef.current()
    }
    window.addEventListener('keydown', handleEscape)

    return () => {
      window.removeEventListener('keydown', handleEscape)
      ;(prevFocusRef.current as HTMLElement | null)?.focus()
    }
  }, []) // intentionally empty: stable via onCloseRef

  function handleKeyDown(e: React.KeyboardEvent<HTMLDivElement>) {
    // Escape is handled by the window listener above.
    if (e.key !== 'Tab') return
    const focusable = Array.from(panelRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? [])
    if (focusable.length === 0) return
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    if (e.shiftKey) {
      if (document.activeElement === first) { e.preventDefault(); last.focus() }
    } else {
      if (document.activeElement === last) { e.preventDefault(); first.focus() }
    }
  }

  if (!modalRoot) return null

  return createPortal(
    <div
      className="modal-overlay"
      onClick={e => e.target === e.currentTarget && onClose()}
    >
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={title ? titleId : undefined}
        className={`modal ${sizeClass}`}
        onKeyDown={handleKeyDown}
      >
        <div className="modal-header">
          {title && <div id={titleId} className="modal-title">{title}</div>}
          <button className="modal-close" onClick={onClose} aria-label="Close modal">
            <Icon name="close" size={20} />
          </button>
        </div>
        {children}
      </div>
    </div>,
    modalRoot
  )
}
