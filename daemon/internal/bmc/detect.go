package bmc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// probeRedfishUnauthenticated probes the Redfish service root WITHOUT sending
// credentials. The service root (/redfish/v1/) does not require authentication
// per the Redfish specification - it contains only version and link metadata.
// Credentials must never be transmitted over an unverified (unpinned) connection.
func probeRedfishUnauthenticated(ctx context.Context, host string) (Info, error) {
	fpCh := make(chan string, 1)
	client := httpClientCapture(fpCh, 8)

	url := fmt.Sprintf("https://%s/redfish/v1/", host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Info{}, err
	}
	// No BasicAuth - credentials are never sent over an unverified connection.
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return Info{}, fmt.Errorf("auth failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Info{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var root struct {
		RedfishVersion string `json:"RedfishVersion"`
		Vendor         string `json:"Vendor"`
		Product        string `json:"Product"`
		Oem            struct {
			Hpe struct {
				Manager []struct {
					ManagerType     string `json:"ManagerType"`
					FirmwareVersion string `json:"FirmwareVersion"`
				} `json:"Manager"`
			} `json:"Hpe"`
		} `json:"Oem"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil {
		return Info{Protocol: ProtocolRedfish, Hostname: host, ReachableAt: time.Now()}, nil
	}

	info := Info{Protocol: ProtocolRedfish, Hostname: host, ReachableAt: time.Now()}
	product := strings.ToLower(root.Product + root.Vendor)
	switch {
	case strings.Contains(product, "ilo") || len(root.Oem.Hpe.Manager) > 0:
		info.Vendor = "HPE"
		if len(root.Oem.Hpe.Manager) > 0 {
			info.Model = root.Oem.Hpe.Manager[0].ManagerType
			info.FirmwareVersion = root.Oem.Hpe.Manager[0].FirmwareVersion
		}
	case strings.Contains(product, "idrac") || strings.Contains(product, "dell"):
		info.Vendor = "Dell"
		info.Model = "iDRAC"
	case strings.Contains(product, "lenovo") || strings.Contains(product, "xcc"):
		info.Vendor = "Lenovo"
		info.Model = "XCC"
	default:
		info.Vendor = "Generic"
		info.Model = "Redfish " + root.RedfishVersion
	}
	return info, nil
}

// probeILO4Unauthenticated attempts to connect to the iLO 4 legacy REST root
// WITHOUT sending credentials. The iLO 4 service root (/rest/v1/) returns
// its Type field without requiring authentication.
func probeILO4Unauthenticated(ctx context.Context, host string) (Info, error) {
	fpCh := make(chan string, 1)
	client := httpClientCapture(fpCh, 8)

	url := fmt.Sprintf("https://%s/rest/v1/", host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Info{}, err
	}
	// No BasicAuth - detection probe only, no credentials over unverified channel.
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return Info{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Info{}, fmt.Errorf("not iLO 4")
	}

	var root struct {
		Type string `json:"Type"`
		Oem  struct {
			Hp struct {
				Manager []struct {
					ManagerFirmwareVersion string `json:"ManagerFirmwareVersion"`
				} `json:"Manager"`
			} `json:"Hp"`
		} `json:"Oem"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&root); err != nil || !strings.HasPrefix(root.Type, "ServiceRoot.") {
		return Info{}, fmt.Errorf("not iLO 4")
	}

	info := Info{
		Protocol:    ProtocolILO4,
		Vendor:      "HPE",
		Model:       "iLO 4",
		Hostname:    host,
		ReachableAt: time.Now(),
	}
	if len(root.Oem.Hp.Manager) > 0 {
		info.FirmwareVersion = root.Oem.Hp.Manager[0].ManagerFirmwareVersion
	}
	return info, nil
}

// probeIPMI runs a quick ipmitool mc info to verify IPMI LAN reachability.
// probeIPMI asks the BMC for its controller info over IPMI 2.0 (lanplus). It
// needs the credentials: BMCs refuse anonymous RMCP+ sessions, so the former
// credential-less probe never detected an IPMI-only BMC.
func probeIPMI(host, user, password string) error {
	return ipmiRun(8, host, user, password, []string{"mc", "info"})
}
