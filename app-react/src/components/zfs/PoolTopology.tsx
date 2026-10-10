import { Icon } from '@/components/ui/Icon'

export interface VDev {
  name: string
  type: string // mirror, raidz, replacing, disk, spare
  state: string
  read?: string
  write?: string
  cksum?: string
  notes?: string
  progress?: string // e.g. "12.5"
  children?: VDev[]
}

export interface PoolTopology {
  name: string
  state: string
  status: string
  scan?: string
  groups: Record<string, VDev[]>
}

interface VDevItemProps {
  vdev: VDev
  level: number
  group: string          // data, special, logs, cache, spare
  parentType?: string    // type of the enclosing vdev (mirror, raidz, ...)
  onAction?: (action: string, vdev: VDev) => void
}

// Device actions (onAction names): replace, raidz-expand, offline, online,
// detach (a mirror/spare member), remove (cache, log or spare device),
// attach (add a mirror disk to a data disk).

const healthColor = (h: string) =>
  h === 'ONLINE' ? 'var(--success)' : 
  h === 'DEGRADED' || h === 'DEGRADED' ? 'var(--warning)' : 
  h === 'REPLACING' || h === 'RESILVERING' ? 'var(--primary)' :
  'var(--error)'

const VDevItem = ({ vdev, level, group, parentType, onAction }: VDevItemProps) => {
  const isDisk = vdev.type === 'disk'
  const inMirror = parentType === 'mirror' || parentType === 'replacing' || parentType === 'spare'
  const removable = isDisk && level === 0 && (group === 'cache' || group === 'logs' || group === 'spare')
  const attachable = isDisk && group === 'data' && (level === 0 || parentType === 'mirror')
  const isStructural = vdev.type === 'mirror' || vdev.type === 'raidz' || vdev.type === 'replacing'

  return (
    <div style={{ marginLeft: level > 0 ? 24 : 0, marginTop: 4 }}>
      <div style={{ 
        display: 'flex', 
        alignItems: 'center', 
        gap: 12, 
        padding: '6px 10px',
        borderRadius: 'var(--radius-sm)',
        background: isStructural ? 'rgba(255,255,255,0.03)' : 'transparent',
        border: isStructural ? '1px solid var(--border-subtle)' : 'none',
        fontSize: 'var(--text-sm)'
      }}>
        <Icon 
          name={isStructural ? 'folder_special' : 'storage'} 
          size={16} 
          style={{ color: isStructural ? 'var(--text-tertiary)' : 'var(--text-secondary)' }}
        />
        
        <div style={{ flex: 1, display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{ fontWeight: isStructural ? 600 : 400, fontFamily: 'var(--font-mono)' }}>
            {vdev.name}
          </span>
          {vdev.type && vdev.type !== 'disk' && (
            <span style={{ 
              fontSize: '10px', 
              textTransform: 'uppercase', 
              background: 'var(--border-subtle)', 
              padding: '0 4px', 
              borderRadius: 2,
              color: 'var(--text-tertiary)'
            }}>
              {vdev.type}
            </span>
          )}
        </div>

        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          {vdev.notes && (
            <span style={{ fontSize: 'var(--text-xs)', color: 'var(--warning)', fontStyle: 'italic' }}>
              {vdev.notes}
            </span>
          )}
          {vdev.progress && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 80 }}>
               <div style={{ flex: 1, height: 4, background: 'rgba(255,255,255,0.1)', borderRadius: 2, overflow: 'hidden' }}>
                  <div style={{ height: '100%', width: `${vdev.progress}%`, background: 'var(--primary)' }} />
               </div>
               <span style={{ fontSize: '10px', color: 'var(--primary)', fontWeight: 600 }}>{vdev.progress}%</span>
            </div>
          )}
          <span style={{ 
            color: healthColor(vdev.state), 
            fontWeight: 600, 
            fontSize: 'var(--text-xs)',
            display: 'flex',
            alignItems: 'center',
            gap: 4
          }}>
            <Icon name={vdev.state === 'ONLINE' ? 'check_circle' : 'warning'} size={12} />
            {vdev.state}
          </span>

          {onAction && (
             <div className="vdev-actions" style={{ display: 'flex', gap: 4 }}>
                {isDisk && vdev.state !== 'ONLINE' && (
                  <button className="btn btn-xs btn-ghost" title="Replace Disk" onClick={() => onAction('replace', vdev)}>
                    <Icon name="swap_horiz" size={14} />
                  </button>
                )}
                {isDisk && vdev.state === 'OFFLINE' && (
                  <button className="btn btn-xs btn-ghost" title="Bring online" onClick={() => onAction('online', vdev)}>
                    <Icon name="play_circle" size={14} />
                  </button>
                )}
                {isDisk && vdev.state === 'ONLINE' && group !== 'cache' && group !== 'spare' && (
                  <button className="btn btn-xs btn-ghost" title="Take offline" onClick={() => onAction('offline', vdev)}>
                    <Icon name="pause_circle" size={14} />
                  </button>
                )}
                {isDisk && inMirror && (
                  <button className="btn btn-xs btn-ghost" title="Detach from mirror" onClick={() => onAction('detach', vdev)}>
                    <Icon name="link_off" size={14} />
                  </button>
                )}
                {removable && (
                  <button className="btn btn-xs btn-ghost" title="Remove device from pool" onClick={() => onAction('remove', vdev)}>
                    <Icon name="remove_circle" size={14} />
                  </button>
                )}
                {attachable && (
                  <button className="btn btn-xs btn-ghost" title="Attach a mirror disk" onClick={() => onAction('attach', vdev)}>
                    <Icon name="add_link" size={14} />
                  </button>
                )}
                {vdev.type === 'raidz' && (
                  <button className="btn btn-xs btn-ghost" title="Expand VDEV (add disk)" onClick={() => onAction('raidz-expand', vdev)}>
                    <Icon name="add_circle" size={14} />
                  </button>
                )}
             </div>
          )}
        </div>
      </div>

      {vdev.children && vdev.children.map((child, i) => (
        <VDevItem key={i} vdev={child} level={level + 1} group={group} parentType={vdev.type} onAction={onAction} />
      ))}
    </div>
  )
}

export const PoolTopologyView = ({ topology, onAction }: { topology: PoolTopology, onAction?: (action: string, vdev: VDev) => void }) => {
  const groups = ['data', 'special', 'logs', 'cache', 'spare']
  
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
      {groups.map(group => {
        const vdevs = topology.groups[group]
        if (!vdevs || vdevs.length === 0) return null
        
        return (
          <div key={group}>
            <div style={{ 
              fontSize: 'var(--text-xs)', 
              fontWeight: 700, 
              color: 'var(--text-tertiary)', 
              textTransform: 'uppercase', 
              letterSpacing: '0.5px',
              marginBottom: 8,
              display: 'flex',
              alignItems: 'center',
              gap: 8
            }}>
              <div style={{ width: 4, height: 4, background: 'var(--primary)', borderRadius: '50%' }} />
              {group} VDEVs
            </div>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              {vdevs.map((v, i) => (
                <VDevItem key={i} vdev={v} level={0} group={group} onAction={onAction} />
              ))}
            </div>
          </div>
        )
      })}
    </div>
  )
}
