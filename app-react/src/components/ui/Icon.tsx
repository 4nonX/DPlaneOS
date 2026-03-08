/**
 * components/ui/Icon.tsx
 *
 * Material Symbols Rounded wrapper.
 * Icon names are the Material Symbols ligature strings
 * (e.g. "dashboard", "storage", "deployed_code").
 */

import type { CSSProperties } from 'react'

interface IconProps {
  name: string
  size?: number | string
  className?: string
  style?: CSSProperties
  /** aria-hidden defaults to true since icons are decorative */
  decorative?: boolean
  label?: string
}

export function Icon({ name, size = 20, className = '', style, decorative = true, label }: IconProps) {
  return (
    <span
      className={`ms ${className}`}
      style={{ fontSize: size, ...style }}
      aria-hidden={decorative ? true : undefined}
      aria-label={!decorative ? label : undefined}
      role={!decorative ? 'img' : undefined}
    >
      {name}
    </span>
  )
}
