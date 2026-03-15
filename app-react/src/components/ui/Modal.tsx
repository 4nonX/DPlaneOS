/**
 * components/ui/Modal.tsx — Shared modal dialog component
 *
 * Usage:
 *   <Modal title="Edit User" onClose={handleClose}>
 *     …content…
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
import { createPortal } from 'react-dom'
import { Icon } from './Icon'

interface ModalProps {
  title?: ReactNode
  onClose: () => void
  children: ReactNode
  size?: 'sm' | 'md' | 'lg'
}

export function Modal({ title, onClose, children, size = 'md' }: ModalProps) {
  const sizeClass = size === 'sm' ? 'modal-sm' : size === 'lg' ? 'modal-lg' : ''
  const modalRoot = document.getElementById('modal-root')

  if (!modalRoot) return null

  return createPortal(
    <div
      className="modal-overlay"
      onClick={e => e.target === e.currentTarget && onClose()}
    >
      <div className={`modal ${sizeClass}`}>
        <div className="modal-header">
          {title && <div className="modal-title">{title}</div>}
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
