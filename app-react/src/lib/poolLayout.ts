/**
 * lib/poolLayout.ts - automatic disk selection for new ZFS pools.
 *
 * Pure functions, no React: the Create Pool modal picks a layout, a disk size
 * group, a width and a vdev count, and this module chooses the disks and
 * estimates capacity.
 */

/** A disk as returned by GET /api/system/disks (daemon DiskInfo). */
export interface DiskInfo {
  name: string
  dev_path: string
  by_id_path: string
  size: string
  size_bytes: number
  type: string // NVMe | SSD | HDD | SAS | USB
  model: string
  serial: string
  in_use: boolean
  pool_name?: string
}

export type DataLayout = 'stripe' | 'mirror' | 'raidz1' | 'raidz2' | 'raidz3'

export const LAYOUTS: { value: DataLayout; label: string; minWidth: number }[] = [
  { value: 'mirror', label: 'Mirror', minWidth: 2 },
  { value: 'raidz1', label: 'RAID-Z1 (1 parity disk)', minWidth: 3 },
  { value: 'raidz2', label: 'RAID-Z2 (2 parity disks)', minWidth: 4 },
  { value: 'raidz3', label: 'RAID-Z3 (3 parity disks)', minWidth: 5 },
  { value: 'stripe', label: 'Stripe (no redundancy)', minWidth: 1 },
]

/** Stable identifier sent to the API: by-id when available. */
export function diskId(d: DiskInfo): string {
  return d.by_id_path || d.dev_path
}

export function minWidth(layout: DataLayout): number {
  return LAYOUTS.find(l => l.value === layout)!.minWidth
}

/** Disks a single vdev can lose without data loss. */
export function faultTolerance(layout: DataLayout, width: number): number {
  switch (layout) {
    case 'stripe': return 0
    case 'mirror': return width - 1
    case 'raidz1': return 1
    case 'raidz2': return 2
    case 'raidz3': return 3
  }
}

/** API vdev type for a layout (the daemon accepts raidz for RAID-Z1). */
export function vdevType(layout: DataLayout): string {
  return layout === 'raidz1' ? 'raidz' : layout
}

export interface SizeGroup {
  key: string
  type: string
  sizeBytes: number
  label: string
  count: number
}

/** Free disks grouped by type and exact size, largest group first. */
export function sizeGroups(disks: DiskInfo[]): SizeGroup[] {
  const groups = new Map<string, SizeGroup>()
  for (const d of disks) {
    if (d.in_use || !d.size_bytes) continue
    const key = `${d.type}:${d.size_bytes}`
    const g = groups.get(key)
    if (g) g.count++
    else groups.set(key, { key, type: d.type, sizeBytes: d.size_bytes, label: `${formatBytes(d.size_bytes)} ${d.type}`, count: 1 })
  }
  return [...groups.values()].sort((a, b) => b.count - a.count || b.sizeBytes - a.sizeBytes)
}

/**
 * Free disks eligible for a size group: same type, and either the exact size
 * or (treatAsMinimum) at least that size. Smallest first, so larger disks are
 * only used when needed.
 */
export function candidates(disks: DiskInfo[], group: SizeGroup, treatAsMinimum: boolean): DiskInfo[] {
  return disks
    .filter(d => !d.in_use && d.type === group.type &&
      (treatAsMinimum ? d.size_bytes >= group.sizeBytes : d.size_bytes === group.sizeBytes))
    .sort((a, b) => a.size_bytes - b.size_bytes || a.name.localeCompare(b.name))
}

export function maxVdevs(candidateCount: number, width: number): number {
  return width > 0 ? Math.floor(candidateCount / width) : 0
}

/** Split the first width*vdevs candidates into vdevs. Empty if not enough disks. */
export function selectVdevs(cands: DiskInfo[], width: number, vdevs: number): DiskInfo[][] {
  if (width < 1 || vdevs < 1 || cands.length < width * vdevs) return []
  return Array.from({ length: vdevs }, (_, i) => cands.slice(i * width, (i + 1) * width))
}

/**
 * Approximate usable bytes before ZFS metadata, slop space and RAID-Z
 * padding. Each vdev is limited by its smallest disk.
 */
export function usableBytes(layout: DataLayout, vdevs: DiskInfo[][]): number {
  return vdevs.reduce((sum, v) => {
    if (v.length === 0) return sum
    const smallest = Math.min(...v.map(d => d.size_bytes))
    const dataDisks = layout === 'stripe' ? v.length
      : layout === 'mirror' ? 1
      : v.length - faultTolerance(layout, v.length)
    return sum + smallest * dataDisks
  }, 0)
}

/** Binary units, as ZFS reports them (TiB, GiB). */
export function formatBytes(bytes: number): string {
  if (!bytes) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB']
  let i = 0
  let v = bytes
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`
}
