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
  onAction?: (action: string, vdev: VDev) => void
}

const healthColor = (h: string) =>
  h === 'ONLINE' ? 'var(--success)' : 
  h === 'DEGRADED' || h === 'DEGRADED' ? 'var(--warning)' : 
  h === 'REPLACING' || h === 'RESILVERING' ? 'var(--primary)' :
  'var(--error)'

const VDevItem = ({ vdev, level, onAction }: VDevItemProps) => {
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
                {vdev.type === 'disk' && vdev.state !== 'ONLINE' && (
                  <button className="btn btn-xs btn-ghost" title="Replace Disk" onClick={() => onAction('replace', vdev)}>
                    <Icon name="swap_horiz" size={14} />
                  </button>
                )}
                {/* Mirroring logic, detaching logic etc can be added here */}
             </div>
          )}
        </div>
      </div>

      {vdev.children && vdev.children.map((child, i) => (
        <VDevItem key={i} vdev={child} level={level + 1} onAction={onAction} />
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
                <VDevItem key={i} vdev={v} level={0} onAction={onAction} />
              ))}
            </div>
          </div>
        )
      })}
    </div>
  )
}
