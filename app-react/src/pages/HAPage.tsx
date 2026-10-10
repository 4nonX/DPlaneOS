/**
 * pages/HAPage.tsx - High Availability (Design 0001, ADR-0009)
 *
 * Every node keeps its own database; paired nodes exchange configuration.
 * High availability is built from:
 *   - the cluster (Corosync) and its third vote      QuorumPanel
 *   - how the setup is protected                     ProtectionCard
 *   - storage groups: what moves between nodes       GroupsPanel
 *   - watchdog and power fencing settings            FencingSettings
 *   - optional Samba clustering                      CTDBConfig
 */

import { useState } from 'react'
import { QuorumPanel } from '@/components/ha/QuorumPanel'
import { GroupsPanel } from '@/components/ha/GroupsPanel'
import { ProtectionCard } from '@/components/ha/ProtectionCard'
import { FencingConfigForm, PDUConfigForm, SCSIFencingCard, WatchdogConfigForm } from '@/components/ha/FencingSettings'
import { CTDBConfig } from '@/components/ha/CTDBConfig'
import { Icon } from '@/components/ui/Icon'

export function HAPage() {
  const [showSettings, setShowSettings] = useState(false)
  return (
    <div style={{ maxWidth: 900 }}>
      <div style={{ marginBottom: 28 }}>
        <h1 className="page-title">High Availability</h1>
        <p className="page-subtitle">
          Pair nodes, form a cluster with a third vote, and put pools into storage groups that move between nodes.
          Every node keeps working on its own if it is cut off.
        </p>
      </div>

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
          Power fencing (IPMI/Redfish or a switched PDU) is an optional extra layer. Set these on every node.
        </p>
        {showSettings && (
          <div style={{ marginTop: 16, display: 'flex', flexDirection: 'column', gap: 16 }}>
            <WatchdogConfigForm />
            <FencingConfigForm />
            <PDUConfigForm />
            <SCSIFencingCard />
          </div>
        )}
      </div>

      <CTDBConfig />
    </div>
  )
}
