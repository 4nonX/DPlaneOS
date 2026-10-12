/**
 * pages/HAPage.tsx - High Availability (Design 0001, ADR-0009)
 *
 * Every node keeps its own database; paired nodes exchange configuration.
 * High availability is built from:
 *   - the guided setup                               SetupWizard
 *   - the map: nodes, groups, maintenance mode       ClusterMap
 *   - the cluster (Corosync) and its third vote      QuorumPanel
 *   - how the setup is protected                     ProtectionCard
 *   - storage groups: what moves between nodes       GroupsPanel
 *   - watchdog and power fencing settings            FencingSettings
 */

import { useState } from 'react'
import { ClusterMap } from '@/components/ha/ClusterMap'
import { SetupWizard } from '@/components/ha/SetupWizard'
import { QuorumPanel } from '@/components/ha/QuorumPanel'
import { GroupsPanel } from '@/components/ha/GroupsPanel'
import { ProtectionCard } from '@/components/ha/ProtectionCard'
import { HardwareCheckCard, SCSIFencingCard, WatchdogConfigForm } from '@/components/ha/FencingSettings'
import { PowerFencingCard } from '@/components/ha/PowerTargets'
import { Icon } from '@/components/ui/Icon'

export function HAPage() {
  const [showSettings, setShowSettings] = useState(false)
  const [wizard, setWizard] = useState(false)
  return (
    <div style={{ maxWidth: 900 }}>
      <div style={{ marginBottom: 28, display: 'flex', alignItems: 'flex-end', gap: 16 }}>
        <div style={{ flex: 1 }}>
          <h1 className="page-title">High Availability</h1>
          <p className="page-subtitle">
            Pair nodes, form a cluster with a third vote, and put pools into storage groups that move between nodes.
            Every node keeps working on its own if it is cut off.
          </p>
        </div>
        <button className="btn btn-primary" onClick={() => setWizard(true)} data-testid="open-wizard">
          <Icon name="auto_fix_high" size={16} />Set up high availability
        </button>
      </div>
      {wizard && <SetupWizard onClose={() => setWizard(false)} />}

      <ClusterMap />
      <QuorumPanel />
      <ProtectionCard />
      <GroupsPanel />

      <div className="card" style={{ marginBottom: 20 }}>
        <button className="btn btn-ghost" style={{ padding: 0 }} onClick={() => setShowSettings(!showSettings)} aria-expanded={showSettings}>
          <Icon name={showSettings ? 'expand_less' : 'expand_more'} size={18} />
          <strong>Watchdog and power fencing</strong>
        </button>
        <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', margin: '6px 0 0' }}>
          The watchdog lets a storage owner that lost quorum reset itself, so another node can take over without reaching it.
          Power fencing (the other node's BMC, or a switched PDU) is an optional extra layer, with a test for each node. Set these on every node.
        </p>
        {showSettings && (
          <div style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 16 }}>
            <HardwareCheckCard />
            <WatchdogConfigForm />
            <PowerFencingCard />
            <SCSIFencingCard />
          </div>
        )}
      </div>

    </div>
  )
}
