/**
 * components/ui/JobIndicator.tsx
 *
 * Global floating indicator for active background tasks.
 * Appears in the TopBar and allows re-opening the streaming console.
 */

import { useState, useEffect } from 'react'
import { Icon } from './Icon'
import { JobConsole } from './JobConsole'
import { useJobStore } from '@/stores/jobs'
import { useWsStore } from '@/stores/ws'

export function JobIndicator() {
  const { activeJobId, activeJobLabel } = useJobStore()
  const [showConsole, setShowConsole] = useState(false)
  const [lastProgress, setLastProgress] = useState<number | null>(null)
  
  const wsOn = useWsStore((s) => s.on)

  // Listen for progress updates even when console is closed
  useEffect(() => {
    return wsOn('jobProgress', (msg) => {
      if (msg.job_id === activeJobId) {
        if (msg.data?.progress !== undefined) {
          setLastProgress(msg.data.progress)
        }
        if (msg.data?.status === 'done' || msg.data?.status === 'failed') {
          // Keep active for a few seconds to show completion, then reset
          setTimeout(() => {
             // Only clear if another job haven't started
          }, 5000)
        }
      }
    })
  }, [activeJobId, wsOn])

  if (!activeJobId) return null

  return (
    <>
      <div 
        onClick={() => setShowConsole(true)}
        style={{
          display: 'flex', alignItems: 'center', gap: 10,
          background: 'rgba(138,156,255,0.08)',
          border: '1px solid rgba(138,156,255,0.2)',
          padding: '6px 14px', borderRadius: 99,
          cursor: 'pointer', transition: 'all 0.2s',
          boxShadow: '0 0 15px rgba(138,156,255,0.1)'
        }}
        onMouseEnter={(e) => {
          e.currentTarget.style.background = 'rgba(138,156,255,0.12)'
          e.currentTarget.style.borderColor = 'rgba(138,156,255,0.4)'
        }}
        onMouseLeave={(e) => {
          e.currentTarget.style.background = 'rgba(138,156,255,0.08)'
          e.currentTarget.style.borderColor = 'rgba(138,156,255,0.2)'
        }}
      >
        <div style={{ position: 'relative', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <Icon 
            name="autorenew" 
            size={16} 
            style={{ 
              color: 'var(--primary)', 
              animation: 'spin 2s linear infinite' 
            }} 
          />
        </div>
        
        <div style={{ display: 'flex', flexDirection: 'column', lineHeight: 1.1 }}>
          <span style={{ fontSize: 10, fontWeight: 700, color: 'var(--text-secondary)', textTransform: 'uppercase', letterSpacing: '0.4px' }}>
            {activeJobLabel || 'Active Task'}
          </span>
          <span style={{ fontSize: 9, color: 'var(--primary)', fontWeight: 600 }}>
            {lastProgress !== null ? `${lastProgress}% complete` : 'Streaming logs...'}
          </span>
        </div>

        <div style={{ marginLeft: 4, opacity: 0.5 }}>
          <Icon name="open_in_full" size={12} />
        </div>
      </div>

      {showConsole && (
        <JobConsole 
          jobId={activeJobId} 
          title={activeJobLabel}
          onClose={() => setShowConsole(false)} 
        />
      )}
    </>
  )
}
