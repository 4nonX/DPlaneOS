import { useState, type ReactNode } from 'react'

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

  return (
    <span 
      className="popover-wrapper"
      onMouseEnter={() => setVisible(true)}
      onMouseLeave={() => setTimeout(() => setVisible(false), 150)}
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
