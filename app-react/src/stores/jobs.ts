/**
 * stores/jobs.ts
 *
 * Tracks the globally "Active" job for the HUD / JobIndicator.
 */
import { create } from 'zustand'

interface JobsState {
  activeJobId: string | null
  activeJobLabel: string
  setActiveJob: (id: string | null, label?: string) => void
}

export const useJobStore = create<JobsState>((set) => ({
  activeJobId: null,
  activeJobLabel: '',
  setActiveJob: (id, label = '') => set({ activeJobId: id, activeJobLabel: label }),
}))
