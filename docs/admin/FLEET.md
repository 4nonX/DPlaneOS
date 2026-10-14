# Fleet

Many DPlaneOS nodes in one view: their health, and users, groups and shared
settings rolled out to all of them, canary first. The design is
[ADR-0005](../design/adr/ADR-0005-fleet-overlay.md) and section 5.7 of
[Design 0001](../design/0001-distributed-state-gitops-ha.md).

The fleet is a **loose overlay**, not a big cluster:

- Any node can be the **fleet manager**; it is a role you switch on (System › Fleet).
- The nodes it manages (**remotes**) are standalone nodes or members of HA
  clusters. They do not know about each other and never depend on the
  manager: when the manager is down or unreachable, every remote keeps
  working, including changes made on it. Only new fleet rollouts wait.
- There is no consensus across the fleet. A rollout is a sequence of ordinary
  API calls from the manager to each remote.

## Set up

1. On the node that will manage the fleet: System › Fleet, enter a name for
   the fleet, **Make this node the fleet manager**.
2. On each node to add: Security › API tokens, create a token with **admin**
   scope. It is shown once.
3. On the manager: **Add node** with a name, the node's address
   (`https://nas-berlin-1.example.lan`) and the token. Labels such as
   `site=berlin, tier=canary` are optional.

The manager contacts the node right away; a node that does not answer with
the token is not added. Its TLS certificate is remembered (the fingerprint is
shown): afterwards a different certificate is refused, so nobody can stand in
for the node. If a node gets a new certificate on purpose, remove it from the
fleet and add it again.

Removing a node from the fleet only makes the manager forget it and its
token. Nothing changes on the node: on the node, **Leave fleet** (System ›
Fleet) stops it from expecting the manager, and revoking the token there
stops the manager from making changes.

Every inventory call tells the node which fleet and manager is calling. A
node that has not heard from its manager for five minutes shows the banner
**Fleet manager unreachable** on every page. It is only information: the
node works as before, and only new fleet changes wait.

## Inventory

The manager reads every node's report each minute (**Refresh** reads it now):
mode (standalone, connected, isolated), cluster, quorum, whether it is the
cluster's coordinator, pools and their health, storage groups it owns,
maintenance mode, conflicts, proposals and Git holds. A node is shown as
**Healthy** when all pools are `ONLINE`, it is not isolated, has no holds or
conflicts and its cluster (if any) has quorum. A node that cannot be reached
shows the error.

## Fleet-wide configuration

Under **Fleet-wide configuration** you keep the fleet's version of:

| Kind | What |
|---|---|
| User | user name, email, role, active; optionally an initial password as bcrypt hash |
| Group | name, GID, description, members |
| Shared settings | time zone, DNS and NTP servers, firewall ports, Samba globals, SSH (all of them together); **Start from this node's settings** fills them in |

Shares, datasets, pools, apps and networking are never fleet-wide: they
belong to a node or a storage group.

Saving a fleet resource changes nothing on the nodes. **Delete on nodes**
marks it for deletion, which the next rollout that includes it carries out.
**Stop managing** removes it from the fleet; the nodes keep what they have.

### On the node

The node applies what it receives like a change from its cluster
coordinator (section 5.5 of the design: *the overlay wins, and the local
change is kept as a proposal*):

- The fleet's version is applied and recorded in the node's revision log
  with origin `overlay` (Configuration History).
- If the resource was changed on the node since the last fleet rollout, that
  change is not lost: it is kept as a **proposal** in Configuration Sync, from
  where it can be applied again or dismissed.
- A password is set only when the fleet creates a user. Passwords changed on
  the node are never overwritten.
- Plain-text passwords are refused; only bcrypt hashes are accepted.
- The fleet's name, the manager and the last rollout are shown on the node's
  Fleet page.

On a cluster, the fleet sends the configuration to the cluster's
coordinator only; the other members receive it through the cluster's own
synchronization. The rollout shows the other members as *applied through
<coordinator>*.

## Rollouts

**New rollout** chooses what to roll out and in which waves: the nodes
ticked as canary come first, all others follow in a second wave. After a
wave the rollout waits the soak time, then continues by itself, or waits
for **Continue** if automatic continuation is off.

The manager applies to up to eight nodes at a time. A node that fails is
tried again on the next pass (every ten seconds); after three failed
attempts it is marked failed and the rollout **pauses**, showing which node
failed and why. Nothing is reverted, on that node or the others. Fix the
cause and **Continue** (failed nodes are tried again) or **Cancel**.

**Pause** stops a running rollout after the current pass.

## API

All endpoints need an administrator (session or API token with admin scope).

| On the manager | |
|---|---|
| `GET /api/fleet` | settings, membership, remotes with inventory, fleet resources, recent rollouts |
| `PUT /api/fleet/settings` | `{enabled, name}` |
| `POST /api/fleet/remotes` | `{name, url, token, labels}` |
| `PUT /api/fleet/remotes/{id}/labels` | `{labels}` |
| `DELETE /api/fleet/remotes/{id}` | forget a remote |
| `POST /api/fleet/poll` | read the inventory now |
| `PUT /api/fleet/resources` | `{kind, key, payload}`; `payload: null` deletes on the nodes |
| `DELETE /api/fleet/resources/{kind}/{key}` | stop managing it |
| `GET /api/fleet/local/{kind}/{key}` | this node's version, as a starting point |
| `POST /api/fleet/rollouts` | `{name, resources: [{kind, key}], waves: [{name, remotes: [id]}], soak_secs, auto_advance}` |
| `POST /api/fleet/rollouts/{id}/continue`, `/pause`, `/cancel` | |

| On every node | |
|---|---|
| `GET /api/fleet/node-status` | the inventory report; with the headers `X-DPlane-Fleet` and `X-DPlane-Fleet-Manager` the call is recorded as the manager's contact |
| `DELETE /api/fleet/membership` | leave the fleet |
| `POST /api/fleet/apply` | `{fleet, manager, rollout, resources: [{kind, key, payload}]}`; answers per resource `applied`, `unchanged` or `failed`, and `proposal: true` when a local change was kept |

## Failure behaviour

| What fails | Remotes | Manager |
|---|---|---|
| Manager down or unreachable | keep working, local changes as usual; after five minutes the banner "Fleet manager unreachable" | rollouts continue where they stopped when it is back |
| A remote unreachable | the others are not affected | shown as not reachable; a rollout pauses after three attempts |
| A remote's certificate changed | — | refused (pin mismatch) until removed and added again |
| Fleet change and local change to the same user | fleet wins; local change kept as proposal | rollout reports "local change kept as proposal" |

Tested in CI by `.github/scripts/fleet-overlay-test.sh`: a manager and five
remotes, each a daemon with its own database.

## Not (yet) part of the fleet

- The fleet-wide configuration is not kept in Git. Each remote can keep its
  own configuration in a repository (System › Git); what the fleet applied
  is written there like any other change.
- Software updates are not rolled out by the fleet.
