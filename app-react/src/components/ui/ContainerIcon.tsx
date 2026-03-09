/**
 * ContainerIcon — resolves and renders a container's icon.
 *
 * Resolution priority:
 *   1. dplaneos.icon label on the container (set in docker-compose.yaml)
 *      - If the value starts with "http" or "/" → treated as a URL (img tag)
 *      - If the value ends with ".svg", ".png", ".webp" → served from
 *        /api/assets/custom-icons/<value>  (air-gap safe local file)
 *      - Otherwise → treated as a Material Symbol name (Icon component)
 *   2. Built-in image-name mapping  (iconMap prop, from GET /api/docker/icon-map)
 *   3. Generic fallback: "deployed_code" Material Symbol
 *
 * Usage:
 *   <ContainerIcon image="jellyfin/jellyfin:latest" labels={container.labels} size={28} />
 */

import { useState } from 'react'
import { Icon } from '@/components/ui/Icon'

interface ContainerIconProps {
  image: string
  labels?: Record<string, string>
  iconMap?: IconMapEntry[]
  size?: number
  className?: string
}

interface IconMapEntry {
  match: string
  icon: string
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

  // 2. Built-in image-name mapping
  if (iconMap?.length) {
    const imageLower = image.toLowerCase()
    // Strip registry prefix: ghcr.io/user/image:tag → image
    const namePart = imageLower.split('/').pop()?.split(':')[0] ?? imageLower
    for (const entry of iconMap) {
      if (nameLower(namePart).includes(entry.match) || nameLower(imageLower).includes(entry.match)) {
        return { type: 'material', value: entry.icon }
      }
    }
  }

  // 3. Fallback
  return { type: 'material', value: 'deployed_code' }
}

function nameLower(s: string): string {
  return s.toLowerCase()
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
