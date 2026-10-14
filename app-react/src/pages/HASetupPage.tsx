/**
 * pages/HASetupPage.tsx - Guided setup (Design 0001 section 5.10, phase 4)
 *
 * Walks through a deployment step by step. Every step reads its state from
 * the system, so the guide can be left and resumed at any time, and a step
 * done elsewhere (another page, the other node) shows as done here.
 *
 *   Standalone: nothing to set up; history and rollback work as they are.
 *   High availability:
 *     1. Pair the nodes          GET /api/config/sync/status (peers)
 *     2. Form the cluster        GET /api/quorum/status (configured)
 *     3. Add a third vote        GET /api/quorum/status (expected votes ≥ 3)
 *     4. Protection              GET /api/ha/protection (fencing_ok)
 *     5. Storage group           GET /api/groups (a shared or replicated group)
 */

import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Skeleton } from '@/components/ui/LoadingSpinner'
import { useSyncStatus } from '@/components/layout/NodeStateBanner'
import { InviteCard, JoinCard } from '@/pages/ConfigSyncPage'
import { AddVote, FormCluster, type QStatus } from '@/components/ha/QuorumPanel'
import { CreateGroup, type GroupsResponse } from '@/components/ha/GroupsPanel'
import { ProtectionCard } from '@/components/ha/ProtectionCard'
import { WatchdogConfigForm } from '@/components/ha/FencingSettings'
import { SafetyChecks } from '@/components/ha/SafetyChecks'

type Profile = 'standalone' | 'ha'
type StepId = 'pair' | 'cluster' | 'vote' | 'protect' | 'group' | 'done'

interface Protection { success: boolean; fencing_ok: boolean; fencing_reason?: string; auto_failover: boolean; auto_failover_reason?: string }

const STEPS: { id: StepId; title: string }[] = [
  { id: 'pair', title: 'Pair the nodes' },
  { id: 'cluster', title: 'Form the cluster' },
  { id: 'vote', title: 'Add a third vote' },
  { id: 'protect', title: 'Protect against split brain' },
  { id: 'group', title: 'Create a storage group' },
  { id: 'done', title: 'Check the result' },
]

const hint: React.CSSProperties = { fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }

function useSetupState() {
  const sync = useSyncStatus()
  const quorum = useQuery({
    queryKey: ['quorum', 'status'],
    queryFn: ({ signal }) => api.get<QStatus>('/api/quorum/status', signal),
    refetchInterval: 5000,
  })
  const protection = useQuery({
    queryKey: ['ha', 'protection'],
    queryFn: ({ signal }) => api.get<Protection>('/api/ha/protection', signal),
    refetchInterval: 10_000,
  })
  const groups = useQuery({
    queryKey: ['groups'],
    queryFn: ({ signal }) => api.get<GroupsResponse>('/api/groups', signal),
    refetchInterval: 10_000,
  })
  const loading = sync.isLoading || quorum.isLoading || protection.isLoading || groups.isLoading
  const done: Record<StepId, boolean> = {
    pair: (sync.data?.peers.length ?? 0) > 0,
    cluster: !!quorum.data?.configured,
    vote: (quorum.data?.info.expected_votes ?? 0) >= 3,
    protect: !!protection.data?.fencing_ok,
    group: (groups.data?.groups ?? []).some(g => g.topology !== 'standalone'),
    done: false,
  }
  return { loading, done, sync: sync.data, quorum: quorum.data, protection: protection.data, groups: groups.data }
}

function StepPair() {
  return (
    <>
      <p style={hint}>
        Paired nodes exchange their configuration (datasets, shares, users, settings) and keep working on their own if the network fails.
        On one node create a join code; on the other enter it. Do this once per additional node.
      </p>
      <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
        <InviteCard />
        <JoinCard />
      </div>
    </>
  )
}

function StepVote({ q, skipped, onSkip }: { q?: QStatus; skipped: boolean; onSkip: () => void }) {
  const [adding, setAdding] = useState(false)
  if (!q?.configured) return <p style={hint}>Form the cluster first.</p>
  return (
    <>
      <p style={hint}>
        With two votes, a network split leaves both nodes thinking they are in charge, so failover stays a manual decision.
        A third vote (a Raspberry Pi, a small VM, a cloud instance, another DPlaneOS system, or a third node) lets the cluster fail over automatically and safely.
      </p>
      <p style={{ fontSize: 'var(--text-sm)' }}>The cluster has <strong>{q.info.expected_votes}</strong> vote{q.info.expected_votes === 1 ? '' : 's'}.</p>
      <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
        <button className="btn btn-primary" onClick={() => setAdding(true)}><Icon name="how_to_vote" size={16} />Add a vote</button>
        {q.info.expected_votes < 3 && !skipped && (
          <button className="btn btn-ghost" onClick={onSkip}>Continue without: failover stays manual</button>
        )}
      </div>
      {skipped && q.info.expected_votes < 3 && (
        <p style={{ ...hint, marginTop: 8 }}>Skipped. If a node fails you take over on the other one with <em>Take over</em>; you can add a vote later.</p>
      )}
      {adding && <AddVote q={q} onClose={() => setAdding(false)} />}
    </>
  )
}

function StepProtect({ p }: { p?: Protection }) {
  return (
    <>
      <p style={hint}>
        Before a node takes a storage group over, the failed owner must be stopped. The baseline needs no extra hardware: the watchdog resets a node
        that lost quorum. Power fencing (IPMI or a switched PDU) is an optional extra layer.
      </p>
      {p && !p.fencing_ok && <p style={{ fontSize: 'var(--text-sm)', color: 'var(--warning)' }}>Not protected yet: {p.fencing_reason}.</p>}
      <ProtectionCard />
      <WatchdogConfigForm />
      <div style={{ marginTop: 16 }}><SafetyChecks /></div>
      <p style={hint}>Enable the watchdog on every node; power fencing and its tests are in <Link to="/ha">High Availability</Link> › Watchdog and power fencing.</p>
    </>
  )
}

function StepGroup({ groups }: { groups?: GroupsResponse }) {
  if (!groups) return null
  const existing = groups.groups.filter(g => g.topology !== 'standalone')
  return (
    <>
      <p style={hint}>
        A storage group is what moves between nodes: its pools with their shares, exports and apps. Pick the topology that matches your disks:
        <strong> shared storage</strong> when every node sees the same disks, <strong>replicated</strong> when each node has its own.
      </p>
      {existing.length > 0 && (
        <p style={{ fontSize: 'var(--text-sm)' }}>Existing: {existing.map(g => g.name).join(', ')}.</p>
      )}
      <CreateGroup data={groups} />
    </>
  )
}

function StepDone({ state }: { state: ReturnType<typeof useSetupState> }) {
  const p = state.protection
  return (
    <>
      <p style={hint}>
        {p?.auto_failover
          ? 'Automatic failover is on.'
          : `Automatic failover is off${p?.auto_failover_reason ? `: ${p.auto_failover_reason}` : ''}. The setup works; failover is a manual Take over.`}
      </p>
      <ul style={{ fontSize: 'var(--text-sm)' }}>
        <li><Link to="/cluster">Cluster Map</Link>: who owns which group and whether everything is in step.</li>
        <li><Link to="/ha">High Availability</Link>: planned moves, safety checks, fencing settings.</li>
        <li><Link to="/config-sync">Configuration Sync</Link>: conflicts, proposals and the conflict rules.</li>
      </ul>
    </>
  )
}

export function HASetupPage() {
  const state = useSetupState()
  const [profile, setProfile] = useState<Profile | null>(null)
  const [skippedVote, setSkippedVote] = useState(false)
  const [picked, setPicked] = useState<StepId | null>(null)

  if (state.loading) return <div className="page-container"><Skeleton height={320} /></div>
  const inHA = state.done.pair || state.done.cluster
  const chosen: Profile | null = profile ?? (inHA ? 'ha' : null)

  const isDone = (id: StepId) => state.done[id] || (id === 'vote' && skippedVote)
  const firstOpen = STEPS.find(s => !isDone(s.id))?.id ?? 'done'
  const current = picked ?? firstOpen

  return (
    <div className="page-container" style={{ maxWidth: 1000 }}>
      <header className="page-header">
        <h1 className="page-title">Guided setup</h1>
        <p className="page-subtitle">Set up this system step by step. You can leave at any time; finished steps stay finished.</p>
      </header>

      {chosen === null && (
        <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
          <button className="card" style={{ flex: '1 1 280px', textAlign: 'left', cursor: 'pointer' }} onClick={() => setProfile('standalone')}>
            <h3 style={{ marginTop: 0 }}><Icon name="dns" size={18} /> One node</h3>
            <p style={hint}>A single system. Nothing to add: change history, rollback and state.yaml export work as they are. You can pair more nodes later.</p>
          </button>
          <button className="card" style={{ flex: '1 1 280px', textAlign: 'left', cursor: 'pointer' }} onClick={() => setProfile('ha')}>
            <h3 style={{ marginTop: 0 }}><Icon name="device_hub" size={18} /> Two or more nodes with failover</h3>
            <p style={hint}>Pair nodes, form a cluster with a third vote and put pools into storage groups that move to another node if one fails.</p>
          </button>
        </div>
      )}

      {chosen === 'standalone' && (
        <div className="card">
          <h3 style={{ marginTop: 0 }}>Nothing else to set up</h3>
          <p style={hint}>
            This node records every configuration change (<Link to="/history">Change History</Link>) and can export it as state.yaml.
            To add a second node later, open this guide again and choose two or more nodes.
          </p>
          <button className="btn btn-ghost" onClick={() => setProfile(null)}>Back</button>
        </div>
      )}

      {chosen === 'ha' && (
        <div style={{ display: 'flex', gap: 20, alignItems: 'flex-start', flexWrap: 'wrap' }}>
          <nav aria-label="Steps" style={{ flex: '0 0 230px' }}>
            <ol style={{ listStyle: 'none', padding: 0, margin: 0, display: 'flex', flexDirection: 'column', gap: 4 }}>
              {STEPS.map((s, i) => (
                <li key={s.id}>
                  <button className={`btn btn-sm ${current === s.id ? 'btn-primary' : 'btn-ghost'}`} style={{ width: '100%', justifyContent: 'flex-start' }}
                    aria-current={current === s.id ? 'step' : undefined} onClick={() => setPicked(s.id)}>
                    <Icon name={isDone(s.id) ? 'check_circle' : 'radio_button_unchecked'} size={16}
                      style={{ color: isDone(s.id) && current !== s.id ? 'var(--success)' : undefined }} />
                    {i + 1}. {s.title}
                  </button>
                </li>
              ))}
            </ol>
          </nav>
          <section className="card" style={{ flex: '1 1 500px', minWidth: 0 }} aria-labelledby="step-title">
            <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
              <h2 id="step-title" style={{ margin: 0, fontSize: 'var(--text-lg)' }}>{STEPS.find(s => s.id === current)!.title}</h2>
              {isDone(current) && current !== 'done' && <span className="badge badge-success">Done</span>}
            </div>
            {current === 'pair' && <StepPair />}
            {current === 'cluster' && (state.done.pair ? <><p style={hint}>The nodes agree on who is in charge through Corosync. Choose the paired node and the addresses they use to reach each other.</p>{state.done.cluster ? <p style={{ fontSize: 'var(--text-sm)' }}>Cluster <strong>{state.quorum?.cluster?.cluster_name}</strong> is formed.</p> : <FormCluster />}</> : <p style={hint}>Pair the nodes first.</p>)}
            {current === 'vote' && <StepVote q={state.quorum} skipped={skippedVote} onSkip={() => { setSkippedVote(true); setPicked(null) }} />}
            {current === 'protect' && <StepProtect p={state.protection} />}
            {current === 'group' && <StepGroup groups={state.groups} />}
            {current === 'done' && <StepDone state={state} />}
            <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 20 }}>
              <button className="btn btn-ghost" disabled={current === STEPS[0].id}
                onClick={() => setPicked(STEPS[Math.max(0, STEPS.findIndex(s => s.id === current) - 1)].id)}>Back</button>
              {current !== 'done' && (
                <button className="btn btn-ghost" onClick={() => setPicked(STEPS[STEPS.findIndex(s => s.id === current) + 1].id)}>
                  {isDone(current) ? 'Next' : 'Skip for now'}
                </button>
              )}
            </div>
          </section>
        </div>
      )}
    </div>
  )
}
