# D-PlaneOS v3.3.0 Release Notes

**Release Date:** 2026-02-22  
**Type:** UX/Security Hardening Release

---

## Overview

v3.3.0 is a focused UX hardening release addressing the "Silent Requirement" pattern
across the entire WebUI - situations where backend rules were strict but the frontend
gave no guidance until after a failed submission.

All changes are backward-compatible. No database migrations required.

---

## Fixes & Improvements

### 🔐 Password UX - All Platforms Unified

**Backend (Go - `auth.go`, `users_groups.go`)**
- `ChangePassword`, `CreateUser`, and admin `UpdateUser` (password reset) now all
  use the same `validatePasswordStrength()` function - eliminating validation
  discrepancies between paths.
- All password inputs now `strings.TrimSpace()` before validation, catching
  accidental leading/trailing whitespace from copy-paste (invisible characters that
  previously caused silent failures).

**Frontend (all password forms)**
- **Real-time strength checklist** (`password-strength.js`) - as the user types, a
  live checklist shows which requirements are met (length, uppercase, lowercase,
  digit, special character), turning green as each rule passes. Rules mirror the
  backend exactly.
- **Show/hide password toggle** - all password fields now have an eye-icon button
  to reveal/hide the input. Uses `Material Symbols Rounded` icon for M3 consistency.
- **Confirm-match indicator** - password confirmation fields show a live
  "✓ Passwords match / ✗ Passwords do not match" indicator.
- **Client-side pre-validation** - password forms validate locally before
  submitting, providing instant feedback without a round-trip to the server.

**Affected pages:** `login.html`, `users.html` (change own password, create user,
edit user / admin reset), `setup-wizard.html`.

---

### 🔔 Notifications - All Dismissible

- **All toast notifications** (both `DPlaneUI.toast()` and `EnhancedUI.toast()`)
  now include a `×` dismiss button, matching Material Design 3 snackbar guidance.
- Toasts are positioned in the **top-right** corner (88px from top, 24px from
  right) consistently across both toast implementations - previously they appeared
  at the bottom-center and top-right respectively, creating visual inconsistency.
- Toasts **pause auto-dismiss on hover** and dismiss 2s after mouse-leave.
- The visual style is now fully unified: glassmorphism background, left accent
  border per type, M3 shape tokens.

---

### ⚠️ Unsaved-Changes Guard

- New `unsaved-changes.js` module provides an **M3-styled banner** at the bottom
  of the screen when the user has made changes that haven't been saved yet.
- Banner is **user-dismissible** via a "Dismiss" button.
- Browser `beforeunload` prompt fires if the user tries to navigate away.
- Applied to: `network.html`, `settings.html`.
- Save actions call `guard.markSaved()` to clear the banner after success.

---

### 🛡️ Double-Click / Double-Submit Protection

- **Network apply** (`applyNetworkChanges`) disables all apply buttons for the
  duration of the API call, preventing duplicate `networkd` restarts.
- **Settings save** (`saveSettings`) disables the save button during the request.
- Both re-enable on completion (success or error via `finally` block).

---

### 🎨 Design Consistency

- All new widgets (`password-strength.js`, `unsaved-changes.js`) use CSS custom
  properties from `m3-tokens.css` and `design-tokens.css` - no hard-coded colors.
- Material Symbols Rounded icons used throughout (no emoji substitutes).
- `login.html` error state changed from plain text to a styled error card with
  icon, consistent with other page error presentations.

---

## Files Changed

### New Files
- `app/assets/js/password-strength.js` - Password strength widget module
- `app/assets/js/unsaved-changes.js` - Unsaved changes guard module
- `RELEASE-NOTES-v3.3.0.md` - This file

### Modified Files
- `daemon/internal/handlers/auth.go` - TrimSpace + unified validation
- `daemon/internal/handlers/users_groups.go` - TrimSpace + validatePasswordStrength in all paths
- `app/assets/js/ui-components.js` - Dismissible toasts, unified positioning
- `app/assets/js/enhanced-ui.js` - Dismissible toasts, unified positioning
- `app/pages/login.html` - Show/hide toggle, M3 error card
- `app/pages/users.html` - Password strength in all modals, client-side validation
- `app/pages/setup-wizard.html` - PasswordStrength module, client-side pre-validation
- `app/pages/network.html` - Unsaved-changes guard, double-click protection
- `app/pages/settings.html` - Unsaved-changes guard, double-click protection
- `VERSION` - 3.3.0 → 3.3.0

---

## Upgrade Notes

Drop-in upgrade. No configuration changes required.

```bash
# Standard OTA path
bash /opt/dplaneos/scripts/upgrade-with-rollback.sh
```
