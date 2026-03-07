## v3.2.1 — XSS Sanitisation

**Release date:** 2026-02-21  
**Type:** Security patch  
**Compatibility:** Drop-in replacement for v3.2.0

---

### What changed

This is a security patch release. No new features, no breaking changes, no schema migrations.

**Frontend XSS sanitisation** — All server-sourced string values interpolated into `innerHTML` are now passed through an `esc()` / `escapeHtml()` sanitiser before insertion. This closes the T5 (Cross-Site Scripting) gap identified in the threat model audit.

Affected files:
- `app/assets/js/alert-system.js` — `alert.title`, `alert.message`, `alert.alert_id`
- `app/pages/audit.html` — log table: username, action, details, IP address
- `app/pages/docker.html` — sandbox list: container id, name, image, state
- `app/pages/iscsi.html` — error messages
- `app/pages/pools.html` — quota display: dataset name, mountpoint
- `app/pages/ups.html` — UPS hardware fields: model, manufacturer, serial, load, voltage
- `app/pages/reporting.html` — ZFS event fields: timestamp, class, description, message
- `app/pages/system-updates.html` — error messages from diff and watchdog endpoints

The Go daemon binary is functionally identical to v3.2.0 — only the version string and frontend files changed.

---

### Upgrading

```bash
# Stop daemon
sudo systemctl stop dplaned

# Replace binary
sudo install -m 755 dplaned-v3.2.1-linux-amd64 /opt/dplaneos/daemon/dplaned

# Replace frontend files (extract tarball, copy app/ directory)
sudo cp -r app/* /opt/dplaneos/app/

# Start daemon
sudo systemctl start dplaned
```

No configuration changes required. No database migration required.

---

### SHA256
See `SHA256SUMS.txt` attached to this release.
