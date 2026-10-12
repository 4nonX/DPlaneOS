# Enterprise License Setup Guide

> **Status: not part of the released images, and not tested.** This file
> describes the vendor's integration of a separate, closed program (the
> Compliance Engine). The images of a release are built without the
> licence public key, so they have no License tab and no Compliance entry,
> and `POST /api/system/license/activate` answers "Licensing not
> configured". Known gaps of the integration itself, for whoever builds it:
> the sidecar needs `/etc/dplaneos-compliance/token` and
> `/var/log/dplaneos-compliance/`, which nothing creates; it is started at
> activation only, not after a restart; the downloaded program is not
> verified beyond the licence that names it; the page expects it on port
> 9001, which the firewall and the web server's content policy do not
> open; only an x86_64 download is named.

End-to-end integration of DPlaneOS with Compliance Engine licensing system.

## Architecture Overview

DPlaneOS (Community Edition) includes a minimal, unobtrusive license field. When a valid license is provided:
- DPlaneOS automatically downloads the Compliance Engine from a private repository
- Compliance Engine installs and runs as a sidecar on port 9001
- Enterprise menu items appear in the sidebar
- On expiration, features automatically disappear

No DPlaneOS source code mentions Compliance Engine except for this license integration.

## Setup Steps

### 1. Generate Keypair (One-Time, Secure)

You generate a keypair and keep the private key secure. Users get the public key embedded in their DPlaneOS.

```bash
# Generate keypair
go run daemon/cmd/dplane-gen-keypair/main.go --out ~/.dplane-keys

# Output:
# Keys generated:
#   Private key: ~/.dplane-keys/license.key.private (chmod 600)
#   Public key:  ~/.dplane-keys/license.key.public
#
# Public key (for embedding in daemon):
# <base64-public-key>
```

Save the private key securely (encrypted backup, HSM, etc.). The public key goes into the DPlaneOS source.

### 2. Embed Public Key in DPlaneOS Source

Edit the license handler to include your public key:

```bash
# Read public key
cat ~/.dplane-keys/license.key.public

# Edit daemon/internal/handlers/license_handler.go
# Replace the licensePublicKeyBase64 initialization
```

Or set it at runtime via environment variable:

```bash
export DPLANE_LICENSE_PUBLIC_KEY="$(cat ~/.dplane-keys/license.key.public)"
```

### 3. Generate a License for a Customer

```bash
dplane-license-gen \
  --customer "Acme Corp" \
  --audits 100 \
  --expires "2027-12-31" \
  --ce-repo "https://github.com/yourusername/DPlane-Compliance-Engine-Releases" \
  --ce-version "v6.1.0" \
  --ce-token "ghp_xxxxxxxxxxxx" \
  --private-key ~/.dplane-keys/license.key.private \
  --out license-acme.txt
```

Output: A license key ready to email to the customer.

```
base64_sig.base64_payload
```

### 4. Customer Activates License

1. Customer installs DPlaneOS Community Edition
2. Opens Settings > System > License
3. Pastes license key
4. Clicks "Activate"
5. DPlaneOS:
   - Validates Ed25519 signature offline (no external calls)
   - Downloads CE code from private repo using embedded access token
   - Installs CE sidecar
   - Starts CE daemon on port 9001
   - Adds "Compliance" to Security sidebar
6. Customer can now use Compliance Engine at /compliance

### 5. License Expiration (Automatic)

DPlaneOS checks license every hour:
- If expired: stops CE daemon, disables CE routes
- "Compliance" menu disappears automatically
- Settings > License shows "Expired" with option to enter renewal key

## Components Implemented

### DPlaneOS Backend
- `daemon/internal/handlers/license_handler.go` - License validation, CE code download, daemon control
- `daemon/internal/database/migrations/00011_enterprise_license.sql` - License and usage tracking tables
- `daemon/cmd/dplaned/main.go` - License expiration checker (hourly)

### DPlaneOS Frontend
- `app-react/src/pages/SettingsPage.tsx` - License input form (Settings > System > License)
- `app-react/src/pages/CompliancePage.tsx` - Compliance Engine iframe (routes to sidecar on port 9001)
- `app-react/src/components/layout/navConfig.ts` - License-gated "Compliance" menu item
- `app-react/src/components/layout/Sidebar.tsx` - Filters enterprise items based on license status
- `app-react/src/routes/index.tsx` - /compliance route registration

### DPlaneOS CLI Tools
- `cmd/dplane-gen-keypair/main.go` - Generates Ed25519 keypair for signing licenses
- `cmd/dplane-license-gen/main.go` - Generates signed license keys (from Compliance Engine repo)

### API Endpoints
- `POST /api/system/license/activate` - Activate license key
- `GET /api/system/license/status` - Check license status

### Database
- `enterprise_license` - Current license, customer, audit limits, expiration
- `enterprise_audit_usage` - Track report generations for compliance

## Environment Variables

```bash
# Required (or can be hardcoded in source):
DPLANE_LICENSE_PUBLIC_KEY="base64-encoded-ed25519-public-key"

# Optional (user sets when needed):
CE_API_URL="http://localhost:9000/api"
CE_API_TOKEN="from-license-payload"
```

## Security Model

### License Key Format
```
base64(ed25519_signature).base64(json_payload)
```

Payload includes:
- Customer name
- Audit limit (1 to unlimited)
- Expiration date (RFC3339 or "never")
- CE repo URL
- CE version
- CE access token (GitHub PAT for private repo)
- Issued date

### Private Key
- Kept offline and secure (user's responsibility)
- Used to sign licenses
- Not in any repository

### Public Key
- Embedded in DPlaneOS daemon
- Used to verify signatures offline
- No external API calls for verification

### CE Code Distribution
- Stored in private GitHub releases
- Downloaded only on license activation
- Access token included in license payload

## End-to-End Flow Example

```
1. User receives license email with key: "abc123def456.xyz789..."
2. Pastes into Settings > License
3. DPlaneOS validates signature using embedded public key
4. Extracts repo URL, version, access token
5. Downloads tarball from GitHub using token
6. Installs to /opt/dplane-compliance/
7. Creates /etc/dplaneos-compliance/token with CE token
8. Starts dplane-compliance sidecar
9. Adds Compliance to sidebar
10. User navigates to /compliance
11. CompliancePage embeds iframe to http://localhost:9001
12. Compliance Engine loads fully functional in sidebar context

On expiration:
1. Hourly check finds expired date
2. Stops dplane-compliance daemon
3. Disables routes to /compliance
4. Settings > License shows "Expired" with renewal prompt
5. "Compliance" disappears from sidebar automatically
6. Customer can paste renewal key to re-activate
```

## Testing

### Generate Test Keys

```bash
go run cmd/dplane-gen-keypair/main.go --out /tmp/test-keys
export DPLANE_LICENSE_PUBLIC_KEY="$(cat /tmp/test-keys/license.key.public)"
```

### Generate Test License

```bash
go run daemon/cmd/dplane-license-gen/main.go \
  --customer "Test Corp" \
  --audits unlimited \
  --expires never \
  --ce-repo "https://github.com/yourusername/DPlane-Compliance-Engine-Releases" \
  --ce-version "v6.1.0" \
  --ce-token "test-token-here" \
  --private-key /tmp/test-keys/license.key.private
```

### Activate in UI

1. Start DPlaneOS with public key set
2. Navigate to Settings > System > License
3. Paste generated license key
4. Click "Activate"
5. Check that Compliance Engine downloads and appears in sidebar

## Audit Trail

All operations logged:
- License activation attempts (with customer name, timestamp)
- License validation failures
- CE code download success/failure
- Daemon startup/shutdown
- License expiration checks
- Report generation count and size

Logged to daemon stdout and /var/log/dplaneos-compliance/sidecar.log.

## No Hardcoded Bullshit

- URLs: Loaded from license, not hardcoded
- Credentials: From license payload, not in code
- Customer info: From license, not hardcoded
- Expiration dates: From license, dynamically checked
- CE version: From license, customer controls what they get

Everything is configurable at license generation time. DPlaneOS itself is completely agnostic to customer details.
