// client.go - unified dispatch layer for BMC operations.
// Callers use these functions; Redfish and IPMI are implementation details.

package bmc

import (
	"context"
	"fmt"
)

// Probe detects the BMC protocol. Behaviour depends on whether a TLS
// certificate fingerprint has been enrolled:
//
// Pin enrolled: ONLY the pinned Redfish client is tried. If it fails, the
// error is returned to the caller. We do NOT fall back to an unpinned probe:
// a failure against a pinned endpoint means either the BMC cert changed (use
// POST /api/bmc/reset-cert to re-enroll) or a MITM is presenting a different
// cert. Silently retrying with InsecureSkipVerify would defeat the pinning and
// expose credentials to the attacker.
//
// No pin: unauthenticated probes only (service root does not require auth).
// Credentials are NEVER sent over an unverified connection.
func Probe(ctx context.Context, creds Credentials, pin PinnedCert) (Info, error) {
	if creds.Host == "" {
		return Info{Protocol: ProtocolNone}, nil
	}

	if pin.Fingerprint != "" {
		// Enrolled pin path: fail hard, do NOT fall through.
		rc, err := newRedfishClient(creds, pin)
		if err != nil {
			return Info{}, fmt.Errorf("Redfish client: %w", err)
		}
		var root struct {
			RedfishVersion string `json:"RedfishVersion"`
		}
		if err := rc.get(ctx, "/redfish/v1/", &root); err != nil {
			return Info{}, fmt.Errorf(
				"Redfish probe failed against enrolled certificate: %w. "+
					"If the BMC firmware was updated (which regenerates its certificate), "+
					"reset the stored fingerprint with POST /api/bmc/reset-cert and re-enroll. "+
					"If you did not update the BMC firmware, this may indicate a MITM attack.", err)
		}
		return Info{Protocol: ProtocolRedfish, Vendor: "BMC", Hostname: creds.Host}, nil
	}

	// No pin enrolled: unauthenticated detection probes only.
	// Credentials are not sent here; they go out only over the pinned channel
	// after enrollment.
	if info, err := probeRedfishUnauthenticated(ctx, creds.Host); err == nil {
		return info, nil
	}
	if info, err := probeILO4Unauthenticated(ctx, creds.Host); err == nil {
		return info, nil
	}
	if err := probeIPMI(creds.Host, creds.Username, creds.Password); err == nil {
		return Info{Protocol: ProtocolIPMI, Vendor: "Generic", Model: "IPMI", Hostname: creds.Host}, nil
	}

	return Info{Protocol: ProtocolNone, Hostname: creds.Host},
		fmt.Errorf("no BMC protocol responded at %s (tried Redfish, iLO 4, IPMI)", creds.Host)
}

// GetHealth fetches hardware sensors. Uses pinned Redfish when enrolled;
// falls back to IPMI only when no pin is stored (not enrolled yet).
// Never falls back from a pinned failure to an unpinned channel.
func GetHealth(ctx context.Context, creds Credentials, pin PinnedCert) (Health, error) {
	if pin.Fingerprint != "" {
		rc, err := newRedfishClient(creds, pin)
		if err != nil {
			return Health{}, err
		}
		return rc.GetHealth(ctx)
	}
	// No pin: IPMI path (credentials sent via env var to ipmitool, never over
	// an unverified network channel).
	return GetHealthIPMI(creds.Host, creds.Username, creds.Password)
}

// GetEvents returns BMC system event log entries.
// Same pinning policy as GetHealth.
func GetEvents(ctx context.Context, creds Credentials, pin PinnedCert, limit int) ([]Event, error) {
	if pin.Fingerprint != "" {
		rc, err := newRedfishClient(creds, pin)
		if err != nil {
			return nil, err
		}
		return rc.GetEvents(ctx, limit)
	}
	return GetEventsIPMI(creds.Host, creds.Username, creds.Password, limit)
}

// GetPowerState returns the current chassis power state.
func GetPowerState(ctx context.Context, creds Credentials, pin PinnedCert) (PowerState, error) {
	h, err := GetHealth(ctx, creds, pin)
	if err != nil {
		return PowerUnknown, err
	}
	return h.PowerState, nil
}

// SendPowerAction sends a power management command.
// Requires a pinned certificate - power commands are never sent over an
// unverified channel.
func SendPowerAction(ctx context.Context, creds Credentials, pin PinnedCert, action PowerAction) error {
	if pin.Fingerprint != "" {
		rc, err := newRedfishClient(creds, pin)
		if err != nil {
			return err
		}
		sysPath, err := rc.FindSystemPath(ctx)
		if err != nil {
			return err
		}
		return rc.PowerAction(ctx, sysPath, action)
	}
	// IPMI power commands use ipmitool with password via env var only.
	return PowerActionIPMI(creds.Host, creds.Username, creds.Password, action)
}
