import { useState, useRef, type ReactNode } from 'react'

interface TooltipProps {
  content: ReactNode
  children: ReactNode
  position?: 'top' | 'bottom' | 'left' | 'right'
  delay?: number
}

export function Tooltip({ content, children, position = 'top', delay = 300 }: TooltipProps) {
  const [visible, setVisible] = useState(false)
  const timeoutRef = useRef<number>()
  const showTimeoutRef = useRef<number>()

  const handleMouseEnter = () => {
    showTimeoutRef.current = window.setTimeout(() => setVisible(true), delay)
  }

  const handleMouseLeave = () => {
    if (showTimeoutRef.current) clearTimeout(showTimeoutRef.current)
    timeoutRef.current = window.setTimeout(() => setVisible(false), 150)
  }

  return (
    <span 
      className="tooltip-wrapper"
      onMouseEnter={handleMouseEnter}
      onMouseLeave={handleMouseLeave}
    >
      {children}
      {visible && (
        <span className={`tooltip tooltip-${position}`}>
          {content}
        </span>
      )}
    </span>
  )
}
