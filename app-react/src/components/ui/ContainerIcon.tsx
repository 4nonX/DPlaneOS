/**
 * ContainerIcon — resolves and renders a container's icon.
 *
 * Resolution priority:
 *   1. dplaneos.icon label on the container (set in docker-compose.yaml)
 *      - If the value starts with "http", "https://", or "/" → treated as a URL (img tag)
 *      - If the value ends with ".svg", ".png", ".webp", ".jpg", ".jpeg", ".gif" → served
 *        from /api/assets/custom-icons/<value>  (air-gap safe local file)
 *      - Otherwise → treated as a Material Symbol name (Icon component)
 *   2. Built-in image-name mapping  (iconMap prop, from GET /api/docker/icon-map)
 *   3. Generic fallback: "deployed_code" Material Symbol
 *
 * Usage:
 *   <ContainerIcon image="jellyfin/jellyfin:latest" labels={container.Labels} size={28} />
 *
 * Setting a custom icon via docker-compose.yaml:
 *   labels:
 *     dplaneos.icon: jellyfin          # Material Symbol name
 *     dplaneos.icon: mylogo.svg        # file in /var/lib/dplaneos/custom_icons/
 *     dplaneos.icon: https://example.com/logo.png  # remote URL
 */

import { useState } from 'react'
import { Icon } from '@/components/ui/Icon'
import type { IconMapEntry } from '@/lib/iconTypes'

export type { IconMapEntry }

interface ContainerIconProps {
  image: string
  labels?: Record<string, string>
  iconMap?: IconMapEntry[]
  size?: number
  className?: string
}

// Image extensions that should be served from /api/assets/custom-icons/
const IMAGE_EXTS = ['.svg', '.png', '.webp', '.jpg', '.jpeg', '.gif']

function isImageFile(s: string): boolean {
  const lower = s.toLowerCase()
  return IMAGE_EXTS.some(ext => lower.endsWith(ext))
}

function isURL(s: string): boolean {
  return s.startsWith('http://') || s.startsWith('https://') || s.startsWith('/')
}

function resolveIcon(
  image: string,
  labels: Record<string, string> | undefined,
  iconMap: IconMapEntry[] | undefined
): { type: 'material' | 'img'; value: string } {
  // 1. Check dplaneos.icon label
  const labelIcon = labels?.['dplaneos.icon']?.trim()
  if (labelIcon) {
    if (isURL(labelIcon)) {
      return { type: 'img', value: labelIcon }
    }
    if (isImageFile(labelIcon)) {
      return { type: 'img', value: `/api/assets/custom-icons/${encodeURIComponent(labelIcon)}` }
    }
    // Material Symbol name
    return { type: 'material', value: labelIcon }
  }

  // 2. Built-in image-name mapping.
  // Match against the full lowercased image string (e.g. "ghcr.io/home-assistant/home-assistant:latest").
  // This subsumes matching against the stripped name part and avoids the dead-code redundancy
  // of checking both independently.
  if (iconMap?.length) {
    const imageLower = image.toLowerCase()
    for (const entry of iconMap) {
      if (imageLower.includes(entry.match)) {
        return { type: 'material', value: entry.icon }
      }
    }
  }

  // 3. Fallback
  return { type: 'material', value: 'deployed_code' }
}

export function ContainerIcon({ image, labels, iconMap, size = 24, className }: ContainerIconProps) {
  const resolved = resolveIcon(image, labels, iconMap)
  const [imgError, setImgError] = useState(false)

  if (resolved.type === 'img' && !imgError) {
    return (
      <img
        src={resolved.value}
        alt=""
        width={size}
        height={size}
        className={className}
        style={{ objectFit: 'contain', borderRadius: 4, flexShrink: 0 }}
        onError={() => setImgError(true)}
      />
    )
  }

  // Material Symbol fallback (also used when img fails to load)
  const symbolName = imgError ? 'deployed_code' : resolved.value
  return (
    <Icon
      name={symbolName}
      size={size}
      className={className}
      style={{ color: 'var(--primary)', flexShrink: 0 }}
    />
  )
}
