/**
 * stores/storage.ts - D-PlaneOS Storage Store
 *
 * Manages global storage UI state, such as the active pool selection.
 * Persists the selected pool to localStorage so it survives page refreshes.
 */

import { create } from 'zustand'
import { persist, createJSONStorage } from 'zustand/middleware'

interface StorageState {
  activePool: string | null
  setActivePool: (pool: string | null) => void
}

export const useStorageStore = create<StorageState>()(
  persist(
    (set) => ({
      activePool: null,
      setActivePool: (pool) => set({ activePool: pool }),
    }),
    {
      name: 'dplane-storage-state', // localStorage key
      storage: createJSONStorage(() => localStorage),
    }
  )
)

