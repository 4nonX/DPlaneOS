import { useState, useRef, type ReactNode } from 'react'

interface PopoverRow {
  label: string
  value: ReactNode
}

interface PopoverProps {
  children: ReactNode
  title?: ReactNode
  content?: PopoverRow[]
  position?: 'top' | 'bottom' | 'left' | 'right'
  delay?: number
}

export function Popover({ children, title, content, position = 'top', delay = 200 }: PopoverProps) {
  const [visible, setVisible] = useState(false)
  const timeoutRef = useRef<number | undefined>(undefined)

  const handleMouseEnter = () => {
    timeoutRef.current = window.setTimeout(() => setVisible(true), delay)
  }

  const handleMouseLeave = () => {
    if (timeoutRef.current) clearTimeout(timeoutRef.current)
    timeoutRef.current = window.setTimeout(() => setVisible(false), 150)
  }

  return (
    <span 
      className="popover-wrapper"
      onMouseEnter={handleMouseEnter}
      onMouseLeave={handleMouseLeave}
    >
      {children}
      {visible && (
        <div className={`popover popover-${position}`}>
          {title && <div className="popover-title">{title}</div>}
          {content && content.map((row, i) => (
            <div key={i} className="popover-row">
              <span className="popover-label">{row.label}</span>
              <span className="popover-value">{row.value}</span>
            </div>
          ))}
        </div>
      )}
    </span>
  )
}
