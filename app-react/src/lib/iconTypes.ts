/**
 * Shared icon map types - used by ContainerIcon, DockerPage, and ModulesPage.
 * Single source of truth; import from here rather than redefining locally.
 */

export interface IconMapEntry {
  match: string  // lowercased substring of image/stack name
  icon: string   // Material Symbol name
}

export interface IconMapResponse {
  success: boolean
  map: IconMapEntry[]
}

