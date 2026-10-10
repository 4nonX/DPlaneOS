/**
 * PowerDialog - restart or shut down the NAS (POST /api/system/reboot,
 * /api/system/poweroff; system:admin).
 */
import { useState } from 'react'
import { useMutation } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Modal } from '@/components/ui/Modal'
import { Icon } from '@/components/ui/Icon'
import { toast } from '@/hooks/useToast'

type Action = 'reboot' | 'poweroff'

export function PowerDialog({ onClose }: { onClose: () => void }) {
  const [chosen, setChosen] = useState<Action | null>(null)
  const [done, setDone] = useState<Action | null>(null)

  const run = useMutation({
    mutationFn: (a: Action) => api.post<{ success: boolean; message?: string }>(`/api/system/${a}`, {}),
    onSuccess: (_, a) => setDone(a),
    onError: (e: Error) => toast.error(e.message),
  })

  if (done) {
    return (
      <Modal title={done === 'reboot' ? 'Restarting' : 'Shutting down'} onClose={onClose} size="sm">
        <p style={{ margin: 0, color: 'var(--text-secondary)' }}>
          {done === 'reboot'
            ? 'The system is restarting. Reload this page in a minute or two.'
            : 'The system is shutting down. It has to be switched on again at the machine (or through its BMC).'}
        </p>
      </Modal>
    )
  }

  return (
    <Modal title="Power" onClose={onClose} size="sm">
      <p style={{ margin: 0, color: 'var(--text-secondary)', fontSize: 'var(--text-sm)' }}>
        Shares, apps and replication stop while the system is down. Storage groups with a standby move to the other node.
      </p>
      <div style={{ display: 'flex', gap: 10 }}>
        <button onClick={() => setChosen('reboot')} className={`btn ${chosen === 'reboot' ? 'btn-primary' : 'btn-ghost'}`} style={{ flex: 1 }}>
          <Icon name="restart_alt" size={16} />Restart
        </button>
        <button onClick={() => setChosen('poweroff')} className={`btn ${chosen === 'poweroff' ? 'btn-danger' : 'btn-ghost'}`} style={{ flex: 1 }}>
          <Icon name="power_settings_new" size={16} />Shut down
        </button>
      </div>
      <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
        <button onClick={onClose} className="btn btn-ghost">Cancel</button>
        <button onClick={() => chosen && run.mutate(chosen)} disabled={!chosen || run.isPending}
          className={`btn ${chosen === 'poweroff' ? 'btn-danger' : 'btn-primary'}`}>
          {run.isPending ? 'Working…' : chosen === 'poweroff' ? 'Shut down now' : chosen === 'reboot' ? 'Restart now' : 'Choose an action'}
        </button>
      </div>
    </Modal>
  )
}
