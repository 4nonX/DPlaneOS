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
POST /api/alerts/smtp/config
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

For ZFS disk and pool events, the ZFS Event Daemon (ZED) can send Telegram alerts directly - bypassing the DPlaneOS daemon - which means disk fault alerts arrive even if the daemon is down.

Configure in `/etc/zfs/zed.d/dplaneos-notify.sh`:
```bash
# The ZED hook reads telegram_config from the DPlaneOS database.
# Enable via:
services.dplaneos.zed.telegramAlerts = true;
# in configuration.nix, then nixos-rebuild switch.
```

The ZED hook fires on: `checksum`, `data`, `io`, `pool_export_events`, `resilver_finish`, `scrub_finish`, `vdev_clear`, `vdev_online`.

### Via API

```
POST /api/alerts/telegram/config
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
| `zfs.pool.resilver_start` | Info | Resilver (rebuild) started after disk replacement |
| `zfs.pool.resilver_complete` | Info | Resilver completed |
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
| `ha.fencing.triggered` | Critical | STONITH fencing action triggered |

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

Use a backup code if your authenticator device is lost. After using a backup code, re-enroll TOTP immediately.

**To generate new backup codes** (invalidates all existing codes):
Settings: Account: Two-Factor Authentication: Regenerate Backup Codes.

Enter the current TOTP code or a backup code to confirm.

### Admin Reset (Account Recovery)

If a user loses access to both their authenticator and backup codes, an admin can reset their TOTP:

Settings: Users: select user: Reset 2FA.

This disables TOTP for the account and forces re-enrollment on next login. The action is logged in the audit trail.

The local admin (user ID 1) cannot have their TOTP reset by anyone else. If user 1 is locked out, use SSH access to the NAS and the `dplaneos-admin-reset` CLI tool (available when logged in as root via SSH).

### TOTP API

```
GET  /api/auth/totp/status           # current TOTP state for authenticated user
POST /api/auth/totp/setup            # initiate setup, returns QR URI and backup codes
POST /api/auth/totp/confirm          # confirm setup with a valid TOTP code
POST /api/auth/totp/disable          # disable TOTP (requires current TOTP code)
POST /api/auth/totp/regenerate-codes # generate new backup codes (requires TOTP code)

# Admin endpoints
POST /api/users/{id}/totp/reset      # admin: disable TOTP for user (requires roles:write)
```

### Login Flow with TOTP

```
1. POST /api/auth/login {"username": "alice", "password": "..."}
   Response: {"totp_required": true, "session_token": "<temp-token>"}

2. POST /api/auth/totp/verify {"code": "123456", "session_token": "<temp-token>"}
   Response: {"token": "<full-session-token>"}
   # Use this token for all subsequent requests
```

If the code is wrong: the request fails. After 5 consecutive failures, the account is temporarily locked (60 seconds). Persistent failures generate a `auth.login.locked` alert.

### Clock Synchronization

TOTP is time-based. If the NAS clock drifts significantly (more than 30 seconds), TOTP codes generated by the user's device will not match. The NAS uses NTP (configured in `state.yaml` under `system.ntp_servers`) to keep its clock synchronized.

If TOTP codes are consistently rejected despite correct setup, check NTP sync:
```bash
timedatectl status
# Should show: "System clock synchronized: yes"
```
