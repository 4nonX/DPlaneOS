import {
  useState, useRef, useId,
  Children, cloneElement, isValidElement,
  type ReactNode, type ReactElement,
} from 'react'

interface TooltipProps {
  content: ReactNode
  children: ReactNode
  position?: 'top' | 'bottom' | 'left' | 'right'
  delay?: number
}

export function Tooltip({ content, children, position = 'top', delay = 300 }: TooltipProps) {
  const [visible, setVisible] = useState(false)
  const timeoutRef = useRef<number | undefined>(undefined)
  const showTimeoutRef = useRef<number | undefined>(undefined)
  const tooltipId = useId()

  const show = () => {
    showTimeoutRef.current = window.setTimeout(() => setVisible(true), delay)
  }

  const hide = () => {
    if (showTimeoutRef.current) clearTimeout(showTimeoutRef.current)
    timeoutRef.current = window.setTimeout(() => setVisible(false), 150)
  }

  // Inject aria-describedby into the trigger so screen readers announce the
  // tooltip text as supplementary description when the element is focused.
  // Merge with any existing aria-describedby rather than overwriting it.
  const child = Children.only(children)
  const existingDescribedBy = isValidElement(child)
    ? (child as ReactElement<{ 'aria-describedby'?: string }>).props['aria-describedby']
    : undefined
  const trigger = isValidElement(child)
    ? cloneElement(child as ReactElement<{ 'aria-describedby'?: string }>, {
        'aria-describedby': existingDescribedBy
          ? `${existingDescribedBy} ${tooltipId}`
          : tooltipId,
      })
    : child

  return (
    <span
      className="tooltip-wrapper"
      onMouseEnter={show}
      onMouseLeave={hide}
      onFocus={show}
      onBlur={hide}
    >
      {trigger}
      {visible && (
        <span id={tooltipId} className={`tooltip tooltip-${position}`} role="tooltip">
          {content}
        </span>
      )}
    </span>
  )
}
