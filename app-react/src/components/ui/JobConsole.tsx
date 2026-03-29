/**
 * components/ui/JobConsole.tsx
 *
 * Real-time streaming console for background jobs using xterm.js.
 * Fetches log history on mount and streams live lines via WebSocket.
 */

import { useEffect, useRef, useState } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

import { api } from '@/lib/api'
import { useWsStore } from '@/stores/ws'
import { Icon } from './Icon'
import { Modal } from './Modal'

interface JobConsoleProps {
  jobId: string
  title?: string
  onClose: () => void
}

interface JobResponse {
  id:     string
  status: string
  logs?:  string[]
  error?: string
}

export function JobConsole({ jobId, title = 'Task Console', onClose }: JobConsoleProps) {
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  
  const wsOn = useWsStore((s) => s.on)
  const [isReady, setIsReady] = useState(false)
  const [jobStatus, setJobStatus] = useState<string>('running')

  // Initialize Terminal
  useEffect(() => {
    if (!containerRef.current) return

    const term = new Terminal({
      theme: {
        background:   '#0a0b10', // Deep space
        foreground:   '#e2e8f0',
        cursor:       '#a78bfa',
        selectionBackground: 'rgba(167,139,250,0.3)',
        black:        '#1e293b',
        red:          '#f87171',
        green:        '#4ade80',
        yellow:       '#fbbf24',
        blue:         '#60a5fa',
        magenta:      '#c084fc',
        cyan:         '#22d3ee',
        white:        '#e2e8f0',
      },
      fontFamily: '"JetBrains Mono Variable", monospace',
      fontSize: 12,
      lineHeight: 1.4,
      scrollback: 2000,
      convertEol: true, // Handle \n vs \r\n automatically
      disableStdin: true, // Only for output
    })

    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(containerRef.current)
    fit.fit()

    termRef.current = term
    fitRef.current = fit

    // Phase 1: Background replay (catch up)
    api.get<JobResponse>(`/api/jobs/${jobId}`)
      .then(data => {
        if (data.logs) {
          data.logs.forEach(line => term.write(line + '\r\n'))
        }
        setJobStatus(data.status)
        setIsReady(true)
      })
      .catch(err => {
        term.write(`\x1b[31m[Error] Failed to fetch job history: ${err.message}\x1b[0m\r\n`)
      })

    // Resize handling
    const ro = new ResizeObserver(() => fit.fit())
    ro.observe(containerRef.current)

    return () => {
      ro.disconnect()
      term.dispose()
    }
  }, [jobId])

  // Phase 2: Live streaming
  useEffect(() => {
    if (!isReady) return

    return wsOn('jobLog', (msg) => {
      if (msg.job_id === jobId) {
        termRef.current?.write(msg.line + '\r\n')
      }
    })
  }, [isReady, jobId, wsOn])

  // Phase 3: Completion monitoring
  useEffect(() => {
    if (!isReady) return

    return wsOn('jobProgress', (msg) => {
      if (msg.job_id === jobId) {
        // Sync status if it changed
        if (msg.data && typeof msg.data === 'object' && 'status' in msg.data) {
          setJobStatus(msg.data.status)
        }
      }
    })
  }, [isReady, jobId, wsOn])

  return (
    <Modal
      title={
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <Icon name="terminal" size={18} style={{ color: 'var(--primary)' }} />
          <span>{title}</span>
          <span style={{ 
            fontSize: 10, padding: '2px 6px', borderRadius: 4, 
            background: jobStatus === 'running' ? 'rgba(59,130,246,0.1)' : 'rgba(16,185,129,0.1)',
            border: `1px solid ${jobStatus === 'running' ? 'var(--info-border)' : 'var(--success-border)'}`,
            color: jobStatus === 'running' ? 'var(--info)' : 'var(--success)',
            textTransform: 'uppercase', fontWeight: 700 
          }}>
            {jobStatus}
          </span>
        </div>
      }
      size="lg"
      onClose={onClose}
    >
      <div style={{ padding: '0 4px 12px' }}>
        <div 
          ref={containerRef} 
          style={{ 
            height: '55vh', 
            background: '#0a0b10', 
            borderRadius: 'var(--radius-md)', 
            padding: 8,
            border: '1px solid rgba(255,255,255,0.05)',
            overflow: 'hidden'
          }} 
        />
        
        <div style={{ 
          marginTop: 12, display: 'flex', alignItems: 'center', 
          gap: 12, fontSize: 'var(--text-xs)', color: 'var(--text-tertiary)' 
        }}>
          <Icon name="info" size={14} style={{ color: 'var(--primary)' }} />
          <span>Console is streaming live. You can close this window to multi-task; the task will continue in the background.</span>
        </div>
      </div>
    </Modal>
  )
}
