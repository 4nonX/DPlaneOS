/**
 * SetupWizard - the guided way to high availability (Design 0001, 5.10).
 *
 * It asks what is wanted and leads through the steps in order. Every step
 * is the same form the page has further down, so there is one way of doing
 * each thing; the wizard adds the order, says what each step is for and
 * what follows from it, and shows from the state of the system which steps
 * are done. It can be closed and opened again at any point.
 *
 * Reads: GET /api/ha/map, /api/quorum/status, /api/groups, /api/ha/protection,
 * /api/ha/power-targets. The last step runs the checks (Checks.tsx,
 * POST /api/ha/power-targets/{node}/test).
 */

import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Icon } from '@/components/ui/Icon'
import { Modal } from '@/components/ui/Modal'
import { InviteCard, JoinCard } from '@/pages/ConfigSyncPage'
import { AddVote, FormCluster, type QStatus } from '@/components/ha/QuorumPanel'
import { CreateGroup, type GroupsResponse } from '@/components/ha/GroupsPanel'
import { WatchdogConfigForm } from '@/components/ha/FencingSettings'
import { PowerFencingCard } from '@/components/ha/PowerTargets'
import { CheckResult, replicationLines, useReplicationCheck, useThirdVoteTest, type CheckLine } from '@/components/ha/Checks'
import type { ClusterMapData } from '@/components/ha/ClusterMap'

type Goal = 'alone' | 'pair' | 'join'

interface Protection {
  layers: { name: string; state: 'ok' | 'warn' | 'off'; detail: string }[]
  auto_failover: boolean
  auto_failover_reason?: string
  failover_delay_secs: number
}
interface Targets { targets: { node: string; name: string; enabled: boolean }[] }

export interface WizardState {
  paired: boolean
  cluster: boolean
  thirdVote: boolean
  watchdog: boolean
  group: boolean
}

/** What is done, read from the state of the system. */
export function wizardState(map?: ClusterMapData, protection?: Protection): WizardState {
  const others = (map?.nodes ?? []).filter(n => !n.self && !n.voter)
  return {
    paired: others.length > 0,
    cluster: !!map?.quorum.configured,
    thirdVote: !!map && map.third_vote.kind !== 'none',
    watchdog: (protection?.layers.find(l => l.name.startsWith('Watchdog'))?.state ?? 'off') !== 'off',
    group: (map?.groups ?? []).some(g => g.topology !== 'standalone'),
  }
}

const GOALS: { id: Goal; icon: string; title: string; text: string }[] = [
  { id: 'alone', icon: 'dns', title: 'This system alone',
    text: 'One system. If it fails, storage is down until it is back. Nothing to set up here.' },
  { id: 'pair', icon: 'swap_horiz', title: 'Two systems, one takes over',
    text: 'A second system takes the storage over when this one fails: with a small third vote, by itself.' },
  { id: 'join', icon: 'group_add', title: 'Add this system to an existing cluster',
    text: 'Other systems already form a cluster; this one becomes a member.' },
]

function StepRow({ n, title, done, active, onClick }: { n: number; title: string; done: boolean; active: boolean; onClick: () => void }) {
  return (
    <button onClick={onClick} data-testid={`wizard-step-${n}`} aria-current={active ? 'step' : undefined}
      style={{ display: 'flex', alignItems: 'center', gap: 8, width: '100%', textAlign: 'left', padding: '8px 10px', border: 'none', cursor: 'pointer',
        borderRadius: 'var(--radius-md)', background: active ? 'var(--surface)' : 'transparent', color: 'inherit', font: 'inherit' }}>
      <Icon name={done ? 'check_circle' : 'radio_button_unchecked'} size={18} style={{ color: done ? 'var(--success)' : 'var(--text-tertiary)', flexShrink: 0 }} />
      <span style={{ fontSize: 'var(--text-sm)', fontWeight: active ? 700 : 400 }}>{n}. {title}</span>
    </button>
  )
}

function Explain({ children }: { children: React.ReactNode }) {
  return <p style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)', marginTop: 0 }}>{children}</p>
}

function Done({ children }: { children: React.ReactNode }) {
  return (
    <div role="status" className="alert alert-success" style={{ fontSize: 'var(--text-sm)', marginBottom: 12 }}>
      <Icon name="check_circle" size={16} /><span>{children}</span>
    </div>
  )
}

/** The last step: try everything that a takeover depends on. */
function FinalChecks({ map, protection, targets }: { map: ClusterMapData; protection?: Protection; targets: Targets['targets'] }) {
  const vote = useThirdVoteTest()
  const repl = useReplicationCheck()
  const [power, setPower] = useState<CheckLine[] | null>(null)
  const [replLines, setReplLines] = useState<CheckLine[] | null>(null)
  const replicated = map.groups.filter(g => g.topology === 'replicated' && g.role === 'owner')
  const enabled = targets.filter(t => t.enabled)

  const run = useMutation({
    mutationFn: async () => {
      vote.mutate()
      const lines: CheckLine[] = []
      for (const t of enabled) {
        try {
          const r = await api.post<{ result: { ok: boolean; detail: string } }>(`/api/ha/power-targets/${encodeURIComponent(t.node)}/test`, {})
          lines.push({ name: `Power fencing of ${t.name}`, ok: r.result.ok, detail: r.result.detail })
        } catch (e) {
          lines.push({ name: `Power fencing of ${t.name}`, ok: false, detail: (e as Error).message })
        }
      }
      setPower(lines)
      const rl: CheckLine[] = []
      for (const g of replicated) {
        try {
          rl.push(...(replicationLines(await repl.mutateAsync(g.name)) ?? []).map(l => ({ ...l, name: `${g.name}: ${l.name}` })))
        } catch (e) {
          rl.push({ name: g.name, ok: false, detail: (e as Error).message })
        }
      }
      setReplLines(rl)
    },
  })

  return (
    <div>
      <Explain>
        These try, now, what a takeover depends on. Nothing is switched off and nothing is moved.
      </Explain>
      <button className="btn btn-primary" onClick={() => run.mutate()} disabled={run.isPending} data-testid="wizard-run-checks">
        <Icon name="fact_check" size={16} />{run.isPending ? 'Checking…' : 'Run the checks'}
      </button>
      <CheckResult lines={vote.data?.steps} error={(vote.error as Error | null)?.message} />
      {power && power.length > 0 && <CheckResult lines={power} />}
      {power && enabled.length === 0 && (
        <CheckResult lines={[{ name: 'Power fencing', ok: true, detail: 'Not in use (optional). A takeover relies on the watchdog of the node that failed.' }]} />
      )}
      {replLines && replLines.length > 0 && <CheckResult lines={replLines} />}

      {protection && (
        <div style={{ marginTop: 16, padding: '12px 14px', borderRadius: 'var(--radius-md)', background: protection.auto_failover ? 'var(--success-bg)' : 'var(--warning-bg)', fontSize: 'var(--text-sm)' }}>
          <strong>What happens when a node fails</strong>
          <div style={{ marginTop: 4 }}>
            {protection.auto_failover
              ? <>The other node takes its storage groups over by itself, about {protection.failover_delay_secs} seconds after the failed node dropped out: the time that node needs to have reset itself. Clients reconnect to the floating address.</>
              : <>Nothing by itself: {protection.auto_failover_reason}. You take the groups over on the other node (Storage groups, Take over) after making sure the failed node is off.</>}
          </div>
          <div style={{ marginTop: 6, color: 'var(--text-secondary)' }}>
            Replicated groups lose what was written since the last replication (the interval of the group). Shared-storage groups lose nothing that was written to disk.
          </div>
        </div>
      )}
    </div>
  )
}

export function SetupWizard({ onClose }: { onClose: () => void }) {
  const [goal, setGoal] = useState<Goal | null>(null)
  const [picked, setPicked] = useState<number | null>(null)
  const [addingVote, setAddingVote] = useState(false)

  const mapQ = useQuery({ queryKey: ['ha', 'map'], queryFn: ({ signal }) => api.get<ClusterMapData>('/api/ha/map', signal), refetchInterval: 5_000 })
  const quorumQ = useQuery({ queryKey: ['quorum', 'status'], queryFn: ({ signal }) => api.get<QStatus>('/api/quorum/status', signal), refetchInterval: 5_000 })
  const groupsQ = useQuery({ queryKey: ['groups'], queryFn: ({ signal }) => api.get<GroupsResponse>('/api/groups', signal), refetchInterval: 10_000 })
  const protQ = useQuery({ queryKey: ['ha', 'protection'], queryFn: ({ signal }) => api.get<Protection>('/api/ha/protection', signal), refetchInterval: 10_000 })
  const targetsQ = useQuery({ queryKey: ['ha', 'power-targets'], queryFn: ({ signal }) => api.get<Targets>('/api/ha/power-targets', signal) })

  const map = mapQ.data
  const st = wizardState(map, protQ.data)

  // The forms of the steps read their own data (the paired nodes, the
  // cluster, the groups) at their own pace. When the system moves on, they
  // are told at once: a step must not open on a form that still shows the
  // state before it.
  const qc = useQueryClient()
  const progress = `${st.paired}${st.cluster}${st.thirdVote}${st.watchdog}${st.group}${map?.nodes.length ?? 0}`
  useEffect(() => {
    qc.invalidateQueries({ queryKey: ['config', 'sync', 'status'] })
    qc.invalidateQueries({ queryKey: ['quorum'] })
    qc.invalidateQueries({ queryKey: ['groups'] })
    qc.invalidateQueries({ queryKey: ['ha', 'power-targets'] })
  }, [progress, qc])

  if (!goal) {
    return (
      <Modal title="Set up high availability" onClose={onClose} size="lg">
        <div style={{ padding: '0 24px 20px' }}>
          <Explain>What should happen when this system fails?</Explain>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
            {GOALS.map(g => (
              <button key={g.id} onClick={() => setGoal(g.id)} data-testid={`wizard-goal-${g.id}`}
                style={{ display: 'flex', gap: 12, alignItems: 'flex-start', textAlign: 'left', padding: '14px 16px', cursor: 'pointer', font: 'inherit', color: 'inherit',
                  border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', background: 'transparent' }}>
                <Icon name={g.icon} size={22} style={{ color: 'var(--primary)', flexShrink: 0, marginTop: 2 }} />
                <span><strong>{g.title}</strong><div style={{ fontSize: 'var(--text-sm)', color: 'var(--text-secondary)' }}>{g.text}</div></span>
              </button>
            ))}
          </div>
        </div>
      </Modal>
    )
  }

  if (goal === 'alone') {
    return (
      <Modal title="This system alone" onClose={onClose} size="lg">
        <div style={{ padding: '0 24px 8px' }}>
          <Explain>
            Nothing has to be set up: a system on its own needs no cluster, no third vote and no storage groups. Its pools are
            imported when it starts and served until it stops.
          </Explain>
          <Explain>
            What protects the data on one system is the layout of its pools (mirrors, RAID-Z), snapshots, and a copy somewhere
            else: Data Protection, Replication and Backup. A second system can be added later without reinstalling anything:
            open this again and choose "Two systems".
          </Explain>
          {st.paired && <div className="alert alert-warning" style={{ fontSize: 'var(--text-sm)' }}><Icon name="info" size={16} /><span>This system is already paired with another one. Pairing alone only keeps their configuration in step; it moves no storage.</span></div>}
        </div>
        <div className="modal-footer">
          <button className="btn btn-ghost" onClick={() => setGoal(null)}>Back</button>
          <button className="btn btn-primary" onClick={onClose}>Done</button>
        </div>
      </Modal>
    )
  }

  const steps = goal === 'pair'
    ? [
        { title: 'Pair the two systems', done: st.paired },
        { title: 'Form the cluster', done: st.cluster },
        { title: 'Add a third vote', done: st.thirdVote },
        { title: 'Watchdog and power fencing', done: st.watchdog },
        { title: 'Put pools into a storage group', done: st.group },
        { title: 'Check that it works', done: false },
      ]
    : [
        { title: 'Pair with a member of the cluster', done: st.paired },
        { title: 'Have a member add this system', done: st.cluster },
        { title: 'Watchdog and power fencing', done: st.watchdog },
        { title: 'Check that it works', done: false },
      ]
  const firstOpen = steps.findIndex(s => !s.done)
  const current = picked ?? (firstOpen === -1 ? steps.length - 1 : firstOpen)
  const title = steps[current].title
  const next = () => setPicked(Math.min(steps.length - 1, current + 1))

  let body: React.ReactNode = null
  switch (title) {
    case 'Pair the two systems':
    case 'Pair with a member of the cluster':
      body = (
        <>
          <Explain>
            Paired systems trust each other and keep their configuration in step: users, shares, settings. One of them makes a
            code, the other enters it together with the first one's address. {goal === 'join' && 'Pair with any one member of the cluster.'}
          </Explain>
          {st.paired
            ? <Done>Paired with {map!.nodes.filter(n => !n.self && !n.voter).map(n => n.name).join(', ')}.</Done>
            : <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}><InviteCard /><JoinCard /></div>}
        </>
      )
      break
    case 'Form the cluster':
      body = (
        <>
          <Explain>
            In a cluster the systems agree on who is in charge, over a network link between them. Use a link of its own where
            you can: it carries only a heartbeat, but everything depends on it.
          </Explain>
          {st.cluster ? <Done>The cluster is formed.</Done> : st.paired ? <FormCluster /> : <Explain>Pair the systems first (step 1).</Explain>}
        </>
      )
      break
    case 'Have a member add this system':
      body = (
        <>
          <Explain>
            On a system that is already a member, open System, High Availability, Cluster quorum and choose <strong>Add a vote
            or node</strong>, then this system. A cluster is changed from the inside: that is why it is done there.
          </Explain>
          {st.cluster ? <Done>This system is a member of the cluster.</Done> : <Explain>Waiting: this page notices when it is done.</Explain>}
        </>
      )
      break
    case 'Add a third vote':
      body = (
        <>
          <Explain>
            Two systems cannot tell "the other one died" from "the cable between us broke": both would take the storage. A
            third vote decides. It stores nothing and serves nobody: a Raspberry Pi, a small virtual machine, or another
            DPlaneOS system. <strong>Without it</strong> everything works, but a failed system is taken over by hand, not by itself.
          </Explain>
          {st.thirdVote
            ? <Done>The cluster has a third vote.</Done>
            : !st.cluster ? <Explain>Form the cluster first (step 2).</Explain>
            : <button className="btn btn-primary" onClick={() => setAddingVote(true)}><Icon name="how_to_vote" size={16} />Add a third vote</button>}
        </>
      )
      break
    case 'Watchdog and power fencing':
      body = (
        <>
          <Explain>
            Before one system takes storage over, the other must be sure to have stopped writing. The <strong>watchdog</strong> does
            that without any network: a system that owns storage and loses the cluster stops resetting it and the board restarts
            the machine. Switch it on on every system. <strong>Power fencing</strong> is an optional second way: this system
            switches the other one off through its BMC or a PDU.
          </Explain>
          {st.watchdog && <Done>The watchdog is on on this system. Do the same on the other one.</Done>}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            <WatchdogConfigForm />
            <PowerFencingCard />
          </div>
        </>
      )
      break
    case 'Put pools into a storage group':
      body = (
        <>
          <Explain>
            A storage group is what moves between the systems: pools with their shares, exports and apps, and an address the
            clients use. <strong>Shared storage</strong> (both systems see the same disks) loses nothing on a takeover.
            <strong> Replicated</strong> (each has its own disks) loses what was written since the last replication.
          </Explain>
          {st.group && <Done>There is a storage group that can move: {map!.groups.filter(g => g.topology !== 'standalone').map(g => g.name).join(', ')}.</Done>}
          {groupsQ.data ? <CreateGroup data={groupsQ.data} /> : null}
        </>
      )
      break
    case 'Check that it works':
      body = map ? <FinalChecks map={map} protection={protQ.data} targets={targetsQ.data?.targets ?? []} /> : null
      break
  }

  return (
    <Modal title={goal === 'pair' ? 'Two systems, one takes over' : 'Add this system to a cluster'} onClose={onClose} size="xl">
      <div style={{ display: 'grid', gridTemplateColumns: '230px minmax(0, 1fr)', gap: 20, padding: '0 24px 8px', minHeight: 360 }}>
        <nav aria-label="Steps" style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
          {steps.map((s, i) => <StepRow key={s.title} n={i + 1} title={s.title} done={s.done} active={i === current} onClick={() => setPicked(i)} />)}
        </nav>
        <div data-testid="wizard-body">
          <h3 style={{ marginTop: 0 }}>{current + 1}. {title}</h3>
          {body}
        </div>
      </div>
      <div className="modal-footer">
        <button className="btn btn-ghost" onClick={() => { setGoal(null); setPicked(null) }}>Back to the start</button>
        <span style={{ flex: 1 }} />
        {current < steps.length - 1
          ? <button className="btn btn-primary" onClick={next} data-testid="wizard-next">{steps[current].done ? 'Next' : 'Skip for now'}<Icon name="arrow_forward" size={15} /></button>
          : <button className="btn btn-primary" onClick={onClose}>Close</button>}
      </div>
      {addingVote && quorumQ.data && <AddVote q={quorumQ.data} onClose={() => setAddingVote(false)} />}
    </Modal>
  )
}
