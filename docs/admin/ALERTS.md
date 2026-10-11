# DPlaneOS Alerts and Authentication

This document covers the notification channels (e-mail, webhooks, Telegram), the alerts the system raises, and sign-in security (TOTP two-factor authentication and backup codes).

---

## Alerting Overview

When something happens that an operator should hear about, DPlaneOS raises an alert and sends it to every channel that is set up:

| Channel | Receives |
|---|---|
| Web interface (notification bell) | every alert, while someone is signed in |
| Webhooks | every alert, or the events chosen for the webhook |
| E-mail | alerts of level *warning* and *critical* |
| Telegram | alerts of level *critical* |

Sending does not block: if one channel fails, the others are still tried and the failure is logged. Webhooks are retried a few times.

Channels are set up on the **Alerts** page.

---

## E-mail (SMTP)

| Field | Description |
|-------|-------------|
| SMTP Host | Mail server hostname (e.g., `smtp.gmail.com`) |
| SMTP Port | Usually 587 (STARTTLS) or 465 (TLS) |
| Username | SMTP authentication username |
| Password | SMTP authentication password |
| From Address | The `From:` header of alert mails |
| To Address | Recipient; several addresses separated by commas |
| TLS | Enabled by default |

**Send Test Email** sends a message at once to confirm delivery.

```
GET  /api/alerts/smtp
POST /api/alerts/smtp
{"host": "smtp.example.com", "port": 587, "username": "alerts@example.com", "password": "...",
 "from": "nas@example.com", "to": "ops@example.com", "tls": true, "enabled": true}
POST /api/alerts/smtp/test
```

---

## Webhooks

A webhook POSTs each alert to an HTTP endpoint: a chat tool's incoming webhook, an incident system, or your own receiver.

| Field | Description |
|-------|-------------|
| Name | A label |
| URL | The endpoint. Alerts are sent with POST |
| Authentication header | Optional: one header (name and value) sent with every alert, e.g. `Authorization: Bearer ...` |
| Events | Which alerts this webhook gets. Nothing checked: all of them |
| Body template | Optional: the body to send instead of the default JSON |

### What is sent

Without a body template:

```json
{
  "event": "pool.degraded",
  "severity": "warning",
  "message": "Pool 'tank' is DEGRADED - a disk has failed. Replace it soon.",
  "hostname": "nas-01",
  "timestamp": "2026-05-13T08:42:00Z",
  "data": { "pool": "tank" }
}
```

Many receivers expect their own format (Slack and Discord want a `text` or `content` field, PagerDuty its event format). Give the webhook a body template for that; the variables `{{event}}`, `{{severity}}`, `{{message}}` and `{{timestamp}}` are replaced:

```
Slack:    {"text": "{{severity}}: {{message}}"}
Discord:  {"content": "{{severity}}: {{message}}"}
```

There is no signature on the payload: use the authentication header, and an HTTPS endpoint, to make sure alerts come from your system.

```
GET    /api/alerts/events                 the alerts that exist (see below)
GET    /api/alerts/webhooks
POST   /api/alerts/webhooks
{"name": "ops chat", "url": "https://hooks.example.com/...", "enabled": true,
 "secret_header": "Authorization", "secret_value": "Bearer ...",
 "body_template": "", "events": "[\"zfs.*\",\"replication.failed\"]"}
DELETE /api/alerts/webhooks/{id}
POST   /api/alerts/webhooks/{id}/test
```

---

## Telegram

1. Create a bot with `@BotFather` (`/newbot`) and copy its token
2. Start a conversation with the bot (or add it to a group)
3. Get the chat ID: `https://api.telegram.org/bot<TOKEN>/getUpdates` after sending the bot a message
4. Enter both on the Alerts page

```
GET  /api/alerts/telegram
POST /api/alerts/telegram
{"bot_token": "...", "chat_id": "...", "enabled": true}
POST /api/alerts/telegram/test
```

---

## The alerts

This is the complete list; `GET /api/alerts/events` returns the same. An event that is not listed is not sent.

| Event | Level | Raised when |
|-------|-------|-------------|
| `zfs.*` | warning or critical | ZFS reports a problem with a pool or one of its devices: a state change (degraded, faulted, removed), checksum or I/O errors, data errors found by a scrub. The exact event is `zfs.<level>.<ZFS event class>`, for example `zfs.critical.statechange` |
| `pool.critical` | critical | A pool is suspended or unavailable, or can no longer be written to |
| `pool.degraded` | warning | A pool is degraded: a device has failed and should be replaced |
| `pool.resumed` | warning | A suspended pool was resumed after its missing device came back |
| `pool.imported` | info | A known pool was imported after its device came back |
| `capacity.warning` | warning | A pool is filling up |
| `capacity.critical` | critical | A pool is nearly full |
| `capacity.emergency` | critical | A pool is so full that writes will fail soon |
| `disk.smart_fail` | critical | A disk reports failing health (SMART), or a monitored attribute crossed its threshold |
| `system.resources` | warning | The system disk, memory or file handles are running out (or are fine again) |
| `replication.failed` | critical | A scheduled replication failed |
| `replication.completed` | info | A scheduled replication completed |
| `replication.restore_complete` | info | A restore from a replication target finished |
| `ha.takeover` | critical | A storage group is now served by another node than before |
| `ha.takeover_failed` | critical | The owner of a storage group is gone and this node could not take the group over |
| `ha.fenced` | critical | Another node has taken the disks of a pool this node served: this node has been shut out |
| `ups.on_battery` | warning | The UPS runs on battery: mains power is out |
| `ups.low_battery` | critical | The UPS battery is low; the system shuts down as configured |
| `ups.online` | info | Mains power is back |
| `ups.unreachable` | warning | The UPS no longer answers |
| `update.installed` | info | A system update was installed and kept |
| `update.reverted` | critical | A system update was removed again; the system runs the previous version |
| `gitops.applied` | info | A new commit of the configuration repository was applied automatically |
| `gitops.apply_held` | warning | A new commit was not applied automatically: it deletes something, needs an approval or carries a high-risk change. It waits on the GitOps page |
| `gitops.apply_failed` | critical | Applying a new commit automatically failed |
| `auth.failed_burst` | warning | Repeated failed sign-in attempts from one address |
| `directory.trust_lost` | critical | The Active Directory domain no longer accepts this system's account: domain users cannot open shares |
| `directory.trust_restored` | info | The Active Directory domain accepts this system's account again |

ZFS events reach the daemon from the ZFS event daemon (ZED) through a hook that the system installs. If the DPlaneOS daemon is not running, ZED events are only in the system log.

Sign-ins, permission denials and changes to users and roles are not alerts: they are recorded in the audit log (Audit page).

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
