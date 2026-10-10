# DPlaneOS Alerts and Authentication

This document covers notification configuration (SMTP, webhook, Telegram), alert event taxonomy, and user authentication security (TOTP two-factor authentication and backup codes).

---

## Alerting Overview

DPlaneOS sends alerts when system conditions cross configured thresholds or when critical events occur. Multiple delivery channels can be configured simultaneously - an event routes to all enabled channels.

```
Event occurs (ZFS fault, CPU threshold, login, etc.)
         │
Alert dispatcher
         │
   ┌─────┼─────┐
   ▼     ▼     ▼
 SMTP  Webhook  Telegram
(email) (Slack/Teams/etc.) (Telegram bot)
```

The dispatcher is non-blocking - if a channel fails to deliver, the others are still attempted and the failure is logged.

---

## SMTP (Email Alerts)

### Setup

Settings: System: Notifications: Email.

| Field | Description |
|-------|-------------|
| SMTP Host | Mail server hostname (e.g., `smtp.gmail.com`) |
| SMTP Port | Usually 587 (STARTTLS) or 465 (TLS) |
| Username | SMTP authentication username |
| Password | SMTP authentication password |
| From Address | The `From:` header for alert emails |
| To Address | Recipient address (one address; use a mailing list for multiple) |
| TLS | Enable TLS (enabled by default) |

### Test

Click **Send Test Email** after saving. A test message is sent immediately to confirm delivery.

### Via API

```
GET  /api/alerts/smtp

POST /api/alerts/smtp
{
  "host": "smtp.example.com",
  "port": 587,
  "username": "alerts@example.com",
  "password": "...",
  "from": "nas@example.com",
  "to": "ops@example.com",
  "tls": true,
  "enabled": true
}

POST /api/alerts/smtp/test
```

---

## Webhook (Slack, Teams, Discord, PagerDuty)

Webhooks POST a JSON payload to any HTTP endpoint when an alert fires. All major chat and incident management platforms support incoming webhooks.

### Setup

Settings: System: Notifications: Webhooks: Add Webhook.

| Field | Description |
|-------|-------------|
| URL | Webhook endpoint URL |
| Secret / Token | Optional: included as `X-DPlaneOS-Signature` header (HMAC-SHA256 of payload) |
| Method | POST (always) |
| Enabled | Toggle per-webhook |

### Payload Format

DPlaneOS sends this JSON structure to all webhook endpoints:

```json
{
  "event": "zfs.pool.degraded",
  "severity": "critical",
  "message": "Pool 'tank' is DEGRADED: 1 disk faulted",
  "detail": {
    "pool": "tank",
    "state": "DEGRADED",
    "vdev": "/dev/disk/by-id/ata-WDC_...",
    "vdev_state": "FAULTED"
  },
  "hostname": "nas-01",
  "timestamp": "2026-05-13T08:42:00Z",
  "version": "10.0.0"
}
```

### Platform-Specific Examples

**Slack (Incoming Webhook):**
Create an incoming webhook at `api.slack.com/apps`, copy the webhook URL, paste into DPlaneOS. No additional setup needed - the payload format is automatically adapted for Slack's `text` field.

**Microsoft Teams:**
In Teams, add a channel connector: Incoming Webhook. Copy the URL. DPlaneOS sends a generic JSON payload; Teams renders it as a card.

**Discord:**
In Discord server settings: Integrations: Webhooks. Append `/slack` to the webhook URL before pasting into DPlaneOS (Discord supports Slack-compatible payloads at that path).

**PagerDuty:**
Use PagerDuty's Events API v2 integration URL (`https://events.pagerduty.com/v2/enqueue`). Set the routing key as the token. DPlaneOS maps severity to PagerDuty `severity` field automatically.

**Generic / Custom:**
Any HTTP endpoint that accepts POST with JSON body. Use the signature header (`X-DPlaneOS-Signature`) to verify authenticity on your receiver.

### Via API

```
GET  /api/alerts/webhooks
POST /api/alerts/webhooks
{"url": "https://hooks.slack.com/...", "enabled": true}

DELETE /api/alerts/webhooks/{id}

POST /api/alerts/webhooks/{id}/test
```

---

## Telegram

For direct Telegram notifications, DPlaneOS can send messages to a Telegram bot.

### Setup

1. Create a bot via `@BotFather` on Telegram: `/newbot` - copy the API token
2. Start a conversation with your bot (or add it to a group)
3. Get your chat ID: `https://api.telegram.org/bot<TOKEN>/getUpdates` after sending a message to the bot
4. In DPlaneOS: Settings: System: Notifications: Telegram

| Field | Description |
|-------|-------------|
| Bot Token | The token from @BotFather |
| Chat ID | Your chat ID or group chat ID (negative number for groups) |
| Enabled | Toggle |

### ZED Integration (ZFS Event Daemon)

For ZFS disk and pool events, the ZFS Event Daemon (ZED) sends events to the DPlaneOS daemon via Unix socket (`/run/dplaneos/dplaneos.sock`). The daemon then dispatches them to all configured alert channels (SMTP, Telegram, webhooks). If the daemon is down, ZED events are logged to syslog only.

The ZED hook is installed automatically by the NixOS module (`services.zfs.zed.d/all-dplaneos-notify.sh`). No additional configuration is required.

`zed_listener.go` dispatches ZED events to typed WebSocket events and forwards warning/error severity events to all configured alert channels. Handled subclasses: `scrub_start`, `scrub_finish`, `scrub_abort`, `resilver_start`, `resilver_finish`, `trim_start`, `trim_finish`, `trim_abort`, `vdev_clear`, `vdev_online`, `pool_import`, `data_loss`, `deadman`, `statechange`, `checksum`, `io`, `pool_destroy`, `vdev_remove`, `device_removal`. All other subclasses emit a generic `zfs.event.<subclass>` WebSocket event.

### Via API

```
GET  /api/alerts/telegram

POST /api/alerts/telegram
{"bot_token": "...", "chat_id": "...", "enabled": true}

POST /api/alerts/telegram/test
```

---

## Alert Event Taxonomy

DPlaneOS generates alerts for the following event categories:

### Storage Events

| Event | Severity | Description |
|-------|----------|-------------|
| `zfs.pool.degraded` | Critical | A pool has entered DEGRADED state (one or more faulted devices) |
| `zfs.pool.faulted` | Critical | A pool has entered FAULTED state (offline) |
| `zfs.pool.scrub_error` | Warning | Scrub completed with errors (data integrity issues) |
| `zfs.pool.scrub_complete` | Info | Scrub finished successfully |
| `scrub_aborted` | Warning | In-progress scrub was aborted |
| `zfs.pool.resilver_start` | Info | Resilver (rebuild) started after disk replacement |
| `zfs.pool.resilver_complete` | Info | Resilver completed |
| `trim_started` | Info | TRIM operation started on a pool |
| `zfs.trim.progress` | Info | TRIM in-progress update (percent done, ETA, bytes trimmed) |
| `trim_completed` | Info | TRIM finished successfully |
| `trim_aborted` | Warning | In-progress TRIM was aborted |
| `vdev_errors_cleared` | Info | Error counters on a vdev were cleared (`zpool clear`) |
| `vdev_recovered` | Info | A vdev that was offline or faulted has come back online |
| `pool_imported` | Info | A pool was imported (e.g., after hot-plug or system startup) |
| `zfs.data_loss` | Error | ZFS kernel reported a data loss event on the pool |
| `zfs.deadman` | Error | ZFS I/O deadman timeout fired (pool I/O hung) |
| `zfs.checksum_errors` | Warning | Checksum errors detected on a vdev |
| `zfs.io_errors` | Error | I/O errors detected on a vdev |
| `zfs.dataset.quota_warn` | Warning | Dataset usage above 80% of quota |
| `zfs.dataset.quota_critical` | Critical | Dataset usage above 95% of quota |
| `storage.disk.faulted` | Critical | A disk has been removed or reported errors above threshold |
| `storage.smart.fail` | Critical | SMART pre-fail attribute crossed threshold |

### System Events

| Event | Severity | Description |
|-------|----------|-------------|
| `system.cpu.high` | Warning | CPU usage above configured threshold (default 90%) |
| `system.memory.high` | Warning | Memory usage above configured threshold (default 90%) |
| `system.temperature.high` | Warning | CPU or disk temperature above threshold |
| `system.load.high` | Warning | 5-minute load average above `nproc * 2` |
| `system.update.available` | Info | A new DPlaneOS version is available |
| `system.update.applied` | Info | OTA update completed successfully |
| `system.update.reverted` | Critical | OTA health check failed; system reverted to previous version |

### Security Events

| Event | Severity | Description |
|-------|----------|-------------|
| `auth.login.success` | Info | Successful login (configurable - off by default to reduce noise) |
| `auth.login.failed` | Warning | Failed login attempt |
| `auth.login.locked` | Warning | Account locked after repeated failures |
| `auth.totp.disabled` | Warning | TOTP disabled for an account |
| `auth.permission.denied` | Warning | API request denied due to insufficient permissions |
| `user.created` | Info | User account created |
| `user.deleted` | Info | User account deleted |
| `role.assigned` | Info | Role assignment changed |

### Services Events

| Event | Severity | Description |
|-------|----------|-------------|
| `docker.container.stopped` | Warning | A container that was running has stopped unexpectedly |
| `docker.container.oom` | Critical | Container killed by OOM (out of memory) |
| `replication.failed` | Warning | Scheduled ZFS replication task failed |
| `replication.success` | Info | ZFS replication completed (configurable - off by default) |
| `gitops.drift_detected` | Warning | Live state diverged from state.yaml |
| `gitops.apply_failed` | Critical | GitOps apply operation failed |
| `ha.failover` | Critical | HA failover occurred (expected or unexpected) |
| `ha.node.unreachable` | Critical | HA peer node is not responding |

### Alert Thresholds

Configure thresholds in Monitoring: Settings.

| Metric | Default threshold | Field |
|--------|------------------|-------|
| CPU usage | 90% | `cpu_warn_pct` |
| Memory usage | 90% | `mem_warn_pct` |
| Dataset usage warn | 80% of quota | `dataset_warn_pct` |
| Dataset usage critical | 95% of quota | `dataset_crit_pct` |
| Load average | 2 * nproc | `load_factor` |
| Temperature | 65°C CPU, 55°C disk | `temp_cpu_warn`, `temp_disk_warn` |

---

## TOTP Two-Factor Authentication

TOTP (Time-based One-Time Password) adds a second factor to login. After entering a password, the user must also enter a 6-digit code generated by an authenticator app.

DPlaneOS implements RFC 6238 TOTP: 6-digit codes, 30-second period, SHA-1 HMAC, with clock tolerance of ±1 step (allows up to 30 seconds of clock skew between the NAS and the user's device).

### Enabling TOTP for Your Account

1. Settings: Account: Two-Factor Authentication: Enable
2. Scan the QR code with an authenticator app (Google Authenticator, Authy, 1Password, Bitwarden, etc.)
3. Enter the current 6-digit code to confirm the setup
4. Copy and store the 8 backup codes shown - these are shown only once

TOTP is per-account. An admin can require TOTP for all users in Settings: Security.

### Enforcing TOTP Org-Wide

Settings: Security: Require 2FA: Enable.

When enabled:
- Existing users without TOTP are prompted to enroll on next login
- New users are required to set up TOTP before accessing any other page
- The local admin (user ID 1) is exempt from org-wide enforcement so the account is always recoverable

### Backup Codes

On TOTP setup, 8 single-use backup codes are generated. Each code:
- Is 8 characters (alphanumeric, case-insensitive)
- Can only be used once
- Is stored as a bcrypt hash (the plaintext is shown only during setup)
- Bypasses the TOTP requirement entirely

Use a backup code if your authenticator device is lost. After using a backup code, re-enroll TOTP immediately (delete and re-setup via `DELETE /api/auth/totp/setup` followed by `POST`).

### Admin Reset (Account Recovery)

There is no in-app admin path for resetting another user's TOTP. If a non-admin user loses both their authenticator and all backup codes, an admin must disable TOTP directly in the database:

```sql
UPDATE totp_secrets SET enabled = 0 WHERE user_id = <id>;
```

This forces re-enrollment on the user's next login.

If user 1 (the local admin) is locked out, use SSH access to the NAS and the `dplaneos-recovery` CLI tool (option 5: Reset Admin Password, available when logged in as root via SSH).

### TOTP API

```
GET    /api/auth/totp/setup   # get current TOTP state and QR URI (when not yet enabled)
POST   /api/auth/totp/setup   # confirm setup with a valid 6-digit TOTP code; returns backup codes
DELETE /api/auth/totp/setup   # disable TOTP (requires password + current TOTP code in body)

POST   /api/auth/totp/verify  # step 2 of login: exchange pending_token + TOTP code for a session
```

### Login Flow with TOTP

```
1. POST /api/auth/login {"username": "alice", "password": "..."}
   Response: {"requires_totp": true, "pending_token": "<temp-token>"}

2. POST /api/auth/totp/verify {"code": "123456", "pending_token": "<temp-token>"}
   Response: {"session_id": "<full-session-token>"}
   # Use this session_id for all subsequent requests
```

If the code is wrong: the request fails with 401. There is no per-attempt lockout at the TOTP step. Rate limiting applies at the password step (`POST /api/auth/login`) and is IP-based with exponential backoff: delays grow from 2 seconds after the second failure to a 30-second cap after six or more failures. The counter resets after 15 minutes of inactivity.

After TOTP verification, the session's `aal` field is upgraded to 2 (Authentication Assurance Level 2). Operations with elevated risk (pool destroy, password reset, fencing configuration, ALUA state flip) require AAL2. A session authenticated with password only (AAL1) is rejected at those endpoints with HTTP 403 and `action: "enable_totp"`.

### Clock Synchronization

TOTP is time-based. If the NAS clock drifts significantly (more than 30 seconds), TOTP codes generated by the user's device will not match. The NAS uses NTP (configured in `state.yaml` under `system.ntp_servers`) to keep its clock synchronized.

If TOTP codes are consistently rejected despite correct setup, check NTP sync:
```bash
timedatectl status
# Should show: "System clock synchronized: yes"
```

### SCRAM-SHA-512 Authentication (v14.0.0)

SCRAM (Salted Challenge Response Authentication Mechanism, RFC 5802) is an alternative authentication path using a two-round challenge/response protocol with PBKDF2-SHA512 key derivation (100,000 iterations). The raw password is never transmitted after the initial key setup.

SCRAM keys are derived automatically alongside bcrypt on every password change. No explicit configuration is required.

**Login flow:**

1. `POST /api/auth/scram/challenge` with `{"username": "alice", "client_nonce": "<random>"}`
   - Returns `{"challenge_id": "...", "server_nonce": "...", "salt": "...", "iterations": 100000}`
2. Client computes ClientProof per RFC 5802 and calls `POST /api/auth/scram/verify` with `{"challenge_id": "...", "client_proof": "<base64>"}`
   - Returns `{"token": "<session>", "server_proof": "<base64>"}` on success
   - Returns `{"requires_totp": true, "pending_token": "..."}` if TOTP is enabled

The SCRAM path applies TOTP gating, must-change-password restrictions, and AAL2 enforcement identically to the bcrypt path. Challenges expire after 2 minutes.
