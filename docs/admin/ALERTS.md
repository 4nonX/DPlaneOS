# DPlaneOS Alerts and Authentication

This document covers notification configuration (SMTP, webhook, Telegram), alert event taxonomy, and user authentication security (TOTP two-factor authentication and backup codes).

---

## Alerting Overview

DPlaneOS sends an alert when a pool's health changes, when ZFS reports a fault, and when a background job (replication, snapshot, update, backup, stack update) fails or recovers. Every alert goes to the web interface (live, over WebSocket) and to the configured channels:

```
Event (pool health, ZED, capacity, SMART, UPS, jobs)
         │
DispatchAlert (level, event, resource, message)
         │
   ┌─────┼──────────┐
   ▼     ▼          ▼
Webhooks  SMTP     Telegram
(every    (warning and critical only)
 level)
```

The same alert (level, event and resource) is sent again only after 30 minutes: one scrub of a damaged pool otherwise produced one mail per damaged block. A channel that fails does not stop the others; the failure is logged.

Configure the channels under **System › Alerts** (tabs Telegram, SMTP / Email, Webhooks).

---

## SMTP (Email Alerts)

### Setup

System › Alerts → **SMTP / Email**.

| Field | Description |
|-------|-------------|
| SMTP Host | Mail server hostname (e.g., `smtp.gmail.com`) |
| SMTP Port | Usually 587 (STARTTLS) or 465 (TLS) |
| Username | SMTP authentication username |
| Password | SMTP authentication password (stored encrypted) |
| From Address | The `From:` header for alert emails |
| To Address | Recipients, comma-separated |
| TLS | Enable TLS (enabled by default) |

Only warning and critical alerts are mailed. The subject is `[DPlaneOS] <event>: <resource>`.

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
  "to": "ops@example.com, oncall@example.com",
  "tls": true,
  "enabled": true
}

POST /api/alerts/smtp/test
```

---

## Webhooks (Slack, Teams, Discord, ntfy, any HTTP endpoint)

Webhooks POST each alert to an HTTP endpoint. Every level is sent (info, warning, critical), filtered by the webhook's event list.

### Setup

System › Alerts → **Webhooks** → add a webhook.

| Field | Description |
|-------|-------------|
| Name, URL | The endpoint |
| Secret header / value | Optional: a header sent with every request (for example `Authorization: Bearer ...`, or a token header your receiver checks) |
| Headers | Optional further headers |
| Content type | Default `application/json` |
| Body template | Optional: the body to send instead of the default JSON, with `{{event}}`, `{{severity}}`, `{{message}}`, `{{pool}}` (the resource), `{{hostname}}` and `{{timestamp}}`; in a JSON template the values are JSON-escaped |
| Events | Optional: the events to send (empty: all) |
| Enabled | Toggle per webhook |

### Default payload

Without a body template, DPlaneOS sends:

```json
{
  "event": "pool.degraded",
  "hostname": "nas-01",
  "severity": "warning",
  "message": "Pool 'tank' is DEGRADED ...",
  "timestamp": "2026-10-23T08:42:00Z",
  "data": { "pool": "tank", "resource": "tank" }
}
```

### Platform examples

**Slack** (incoming webhook) and **Discord** (append `/slack` to the webhook URL): body template `{"text": "{{severity}} {{event}} on {{hostname}}: {{message}}"}`.

**Microsoft Teams** (incoming webhook or workflow): a body template in the card format your Teams endpoint expects, using the same placeholders.

**ntfy, Gotify, generic receivers:** the default JSON, or a plain-text body template with content type `text/plain`.

### Via API

```
GET    /api/alerts/webhooks
POST   /api/alerts/webhooks
{"name": "slack", "url": "https://hooks.slack.com/...", "body_template": "{\"text\": \"{{message}}\"}", "enabled": true}

DELETE /api/alerts/webhooks/{id}

POST   /api/alerts/webhooks/{id}/test
```

---

## Telegram

### Setup

1. Create a bot via `@BotFather` on Telegram: `/newbot` - copy the API token
2. Start a conversation with your bot (or add it to a group)
3. Get your chat ID: `https://api.telegram.org/bot<TOKEN>/getUpdates` after sending a message to the bot
4. In DPlaneOS: System › Alerts → **Telegram**

| Field | Description |
|-------|-------------|
| Bot Token | The token from @BotFather (stored encrypted) |
| Chat ID | Your chat ID or group chat ID (negative number for groups) |
| Enabled | Toggle |

Only warning and critical alerts are sent to Telegram.

### Via API

```
GET  /api/alerts/telegram

POST /api/alerts/telegram
{"bot_token": "...", "chat_id": "...", "enabled": true}

POST /api/alerts/telegram/test
```

### ZED Integration (ZFS Event Daemon)

ZFS events reach the daemon through the ZED hook (`/etc/zfs/zed.d/all-dplaneos-notify.sh`, installed by the NixOS module) and the socket `/run/dplaneos/dplaneos.sock`. They are shown live in the web interface (`zfs.event.<subclass>`), and the warning and error ones are sent as alerts (`zfs.<severity>.<subclass>`). If the daemon is down, ZED events are logged to syslog only.

---

## Alert Events

| Event | Level | When |
|-------|-------|------|
| `pool.degraded` | warning | A pool became DEGRADED |
| `pool.critical` | critical | A pool became FAULTED, UNAVAIL, SUSPENDED or REMOVED, or the pool list cannot be read |
| `pool.recovered` | info | A pool is ONLINE again |
| `pool.scrub_error`, `pool.resilver_error` | warning | A scrub or resilver ended with errors |
| `pool.never_scrubbed` | warning | A pool older than 40 days has never been scrubbed |
| `zfs.<severity>.<subclass>` | warning, error | A ZFS event from ZED (checksum and I/O errors, data loss, deadman, ...) |
| `capacity.warning`, `capacity.critical`, `capacity.emergency` | warning, critical | A pool is 80 %, 90 % or 95 % full |
| `disk.smart_fail` | warning, critical | S.M.A.R.T. predicts a disk failure |
| `ups.<event>` | info to critical | UPS events from NUT (on battery, low battery, back online, ...) |
| `replication.failed`, `replication.recovered` | warning, info | A replication job failed, or runs again after failing |
| `replication.restore_complete` | info | A restore from a replica finished |
| `snapshot.failed` | warning | A scheduled snapshot failed |
| `docker.stopped` | critical | Docker was stopped because a pool failed (containers would write into empty mountpoints) |
| `docker.auto_updated`, `docker.auto_update_rolled_back`, `docker.auto_update_failed` | info, warning, critical | Automatic stack updates |
| `gitsync.deploy_failed` | warning | Deploying a stack from a Git repository failed |
| `upgrade.applied`, `upgrade.rolled_back` | info, critical | A system update was kept, or rolled back after its check |
| `config.backup_failed` | warning | The daily configuration backup could not be written |
| `config.restored`, `config.restore_failed` | info, critical | A configuration restore was applied, or failed |
| `certificate.renewal_failed` | warning | An ACME certificate could not be renewed |

Holds of the Git configuration repository (`gitops.hold`) are shown in the web interface and on the Git page.

The thresholds are fixed: pool capacity at 80 %, 90 % and 95 %; pool health and S.M.A.R.T. as ZFS and the disks report them.

---

## TOTP Two-Factor Authentication

TOTP (Time-based One-Time Password) adds a second factor to login. After entering a password, the user must also enter a 6-digit code generated by an authenticator app.

DPlaneOS implements RFC 6238 TOTP: 6-digit codes, 30-second period, SHA-1 HMAC, with clock tolerance of ±1 step (allows up to 30 seconds of clock skew between the NAS and the user's device).

### Enabling TOTP for Your Account

1. Security › Security Settings → tab **2FA / TOTP** → enable
2. Scan the QR code with an authenticator app (Google Authenticator, Authy, 1Password, Bitwarden, etc.)
3. Enter the current 6-digit code to confirm the setup
4. Copy and store the 8 backup codes shown - these are shown only once

TOTP is per account; each user enables it for themselves. Some operations require a session that logged in with TOTP (AAL2, see below), so administrators should enable it.

### Backup Codes

On TOTP setup, 8 single-use backup codes are generated. Each code:
- Is 8 hexadecimal characters
- Can only be used once
- Is stored as a bcrypt hash (the plaintext is shown only during setup)
- Bypasses the TOTP requirement entirely

Use a backup code if your authenticator device is lost. After using a backup code, re-enroll TOTP immediately (delete and re-setup via `DELETE /api/auth/totp/setup` followed by `POST`).

### Admin Reset (Account Recovery)

There is no in-app admin path for resetting another user's TOTP. If a user loses both their authenticator and all backup codes, turn it off on the console:

```bash
sudo dplaneos-recovery   # option 6: Turn Off a User's Two-Factor Authentication
```

(Only clearing `totp_secrets` is not enough: the login checks `users.totp_enabled`.)

This forces re-enrollment on the user's next login.

If the local admin is locked out, use the console or SSH and `sudo dplaneos-recovery` (option 5: Reset a User's Password; option 6 for a lost 2FA device).

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

After TOTP verification, the session's `aal` field is upgraded to 2 (Authentication Assurance Level 2). Operations with elevated risk require AAL2: pool destroy, resetting another user's password, fencing, CTDB and watchdog configuration, BMC enrolment, power control and certificate reset, and the iSCSI ALUA state. A session authenticated with password only (AAL1) is rejected at those endpoints with HTTP 403 and `action: "enable_totp"`.

### Clock Synchronization

TOTP is time-based. If the NAS clock drifts significantly (more than 30 seconds), TOTP codes generated by the user's device will not match. The NAS uses NTP (configured in `state.yaml` under `system.ntp_servers`) to keep its clock synchronized.

If TOTP codes are consistently rejected despite correct setup, check NTP sync:
```bash
timedatectl status
# Should show: "System clock synchronized: yes"
```

### SCRAM-SHA-512 Authentication

SCRAM (Salted Challenge Response Authentication Mechanism, RFC 5802) is an alternative authentication path using a two-round challenge/response protocol with PBKDF2-SHA512 key derivation (100,000 iterations). The raw password is never transmitted after the initial key setup.

SCRAM keys are derived automatically alongside bcrypt on every password change. No explicit configuration is required.

**Login flow:**

1. `POST /api/auth/scram/challenge` with `{"username": "alice", "client_nonce": "<random>"}`
   - Returns `{"challenge_id": "...", "server_nonce": "...", "salt": "...", "iterations": 100000}`
2. Client computes ClientProof per RFC 5802 and calls `POST /api/auth/scram/verify` with `{"challenge_id": "...", "client_proof": "<base64>"}`
   - Returns `{"token": "<session>", "server_proof": "<base64>"}` on success
   - Returns `{"requires_totp": true, "pending_token": "..."}` if TOTP is enabled

The SCRAM path applies TOTP gating, must-change-password restrictions, and AAL2 enforcement identically to the bcrypt path. Challenges expire after 2 minutes.
