package bmc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// redfishClient performs authenticated Redfish API calls with pinned TLS.
type redfishClient struct {
	creds  Credentials
	pinned PinnedCert
}

// newRedfishClient creates a client using the stored TLS fingerprint.
// Returns an error if no fingerprint is stored (caller must enroll first).
func newRedfishClient(creds Credentials, pinned PinnedCert) (*redfishClient, error) {
	if pinned.Fingerprint == "" {
		return nil, fmt.Errorf("BMC certificate not yet enrolled: call EnrollCertificate first")
	}
	return &redfishClient{creds: creds, pinned: pinned}, nil
}

// EnrollCertificate performs a TOFU probe: connects to the Redfish root,
// captures the BMC certificate fingerprint, and returns it for storage.
// Call this once per BMC; store the returned PinnedCert in the database.
// Subsequent calls use newRedfishClient with the stored fingerprint.
func EnrollCertificate(ctx context.Context, creds Credentials) (PinnedCert, Info, error) {
	fpCh := make(chan string, 1)
	client := httpClientCapture(fpCh, 10)

	url := fmt.Sprintf("https://%s/redfish/v1/", creds.Host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return PinnedCert{}, Info{}, err
	}
	req.SetBasicAuth(creds.Username, creds.Password)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return PinnedCert{}, Info{}, fmt.Errorf("Redfish probe failed: %w", err)
	}
	defer resp.Body.Close()

	var fp string
	select {
	case fp = <-fpCh:
	default:
		return PinnedCert{}, Info{}, fmt.Errorf("no certificate presented by BMC")
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return PinnedCert{}, Info{}, fmt.Errorf("BMC authentication failed (check credentials)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PinnedCert{}, Info{}, fmt.Errorf("Redfish root returned HTTP %d", resp.StatusCode)
	}

	pin := PinnedCert{Fingerprint: fp, PinnedAt: time.Now()}

	// Parse the service root to identify vendor/model
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
		// Body may already be partially read; return the pin even if parsing fails
		return pin, Info{Protocol: ProtocolRedfish, Hostname: creds.Host, ReachableAt: time.Now()}, nil
	}

	info := Info{
		Protocol:    ProtocolRedfish,
		Hostname:    creds.Host,
		ReachableAt: time.Now(),
	}
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
	default:
		info.Vendor = "Generic"
		info.Model = "Redfish " + root.RedfishVersion
	}
	return pin, info, nil
}

func (c *redfishClient) get(ctx context.Context, path string, target any) error {
	hc, err := httpClientPinned(c.pinned, 15)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://%s%s", c.creds.Host, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.creds.Username, c.creds.Password)
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("BMC authentication failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, path)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (c *redfishClient) post(ctx context.Context, path string, body map[string]any) error {
	hc, err := httpClientPinned(c.pinned, 30)
	if err != nil {
		return err
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://%s%s", c.creds.Host, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		strings.NewReader(string(bodyBytes)))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.creds.Username, c.creds.Password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("BMC authentication failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, path)
	}
	return nil
}

// GetHealth fetches thermal (temperatures, fans) and power data via Redfish.
type odataLink struct {
	OdataID string `json:"@odata.id"`
}

// rootCollection returns the members of a collection linked from the
// service root ("Systems", "Chassis", "Managers"). The root holds only a
// link to each collection ({"@odata.id": "/redfish/v1/Systems"}); reading
// "Members" from the root itself (as before) found nothing on any BMC that
// follows the specification, so health and power control never worked.
func (c *redfishClient) rootCollection(ctx context.Context, name string) ([]string, error) {
	var root map[string]json.RawMessage
	if err := c.get(ctx, "/redfish/v1/", &root); err != nil {
		return nil, fmt.Errorf("Redfish root: %w", err)
	}
	var link odataLink
	if raw, ok := root[name]; !ok || json.Unmarshal(raw, &link) != nil || link.OdataID == "" {
		return nil, nil
	}
	return c.collectionMembers(ctx, link.OdataID)
}

func (c *redfishClient) collectionMembers(ctx context.Context, path string) ([]string, error) {
	var coll struct {
		Members []odataLink `json:"Members"`
	}
	if err := c.get(ctx, path, &coll); err != nil {
		return nil, err
	}
	var out []string
	for _, m := range coll.Members {
		if m.OdataID != "" {
			out = append(out, m.OdataID)
		}
	}
	return out, nil
}

func (c *redfishClient) GetHealth(ctx context.Context) (Health, error) {
	systems, err := c.rootCollection(ctx, "Systems")
	if err != nil {
		return Health{}, err
	}
	chassis, _ := c.rootCollection(ctx, "Chassis")

	h := Health{CollectedAt: time.Now(), OverallStatus: "ok", PowerState: PowerUnknown}

	// Fetch system power state
	if len(systems) > 0 {
		var sys struct {
			PowerState string `json:"PowerState"`
			Status     struct {
				Health string `json:"Health"`
			} `json:"Status"`
		}
		if err := c.get(ctx, systems[0], &sys); err == nil {
			switch strings.ToLower(sys.PowerState) {
			case "on":
				h.PowerState = PowerOn
			case "off", "poweredoff":
				h.PowerState = PowerOff
			default:
				h.PowerState = PowerUnknown
			}
			if strings.EqualFold(sys.Status.Health, "Warning") {
				h.OverallStatus = "warning"
			} else if strings.EqualFold(sys.Status.Health, "Critical") {
				h.OverallStatus = "critical"
			}
		}
	}

	// Fetch thermal and power from chassis
	for _, member := range chassis {
		// Thermal (temperatures + fans)
		var thermal struct {
			Temperatures []struct {
				Name                string  `json:"Name"`
				ReadingCelsius      float64 `json:"ReadingCelsius"`
				UpperThresholdFatal float64 `json:"UpperThresholdFatal"`
				Status              struct {
					Health string `json:"Health"`
				} `json:"Status"`
			} `json:"Temperatures"`
			Fans []struct {
				Name         string  `json:"Name"`
				Reading      float64 `json:"Reading"`
				ReadingUnits string  `json:"ReadingUnits"`
				Status       struct {
					Health string `json:"Health"`
				} `json:"Status"`
			} `json:"Fans"`
		}
		if err := c.get(ctx, member+"/Thermal", &thermal); err == nil {
			for _, t := range thermal.Temperatures {
				if t.ReadingCelsius == 0 {
					continue
				}
				s := sensorStatus(t.Status.Health)
				h.Sensors = append(h.Sensors, Sensor{
					Name:     t.Name,
					Value:    t.ReadingCelsius,
					Unit:     "Celsius",
					Status:   s,
					Category: "temperature",
				})
				if s == "critical" && h.OverallStatus != "critical" {
					h.OverallStatus = "critical"
				} else if s == "warning" && h.OverallStatus == "ok" {
					h.OverallStatus = "warning"
				}
			}
			for _, f := range thermal.Fans {
				unit := "RPM"
				if f.ReadingUnits != "" {
					unit = f.ReadingUnits
				}
				h.Sensors = append(h.Sensors, Sensor{
					Name:     f.Name,
					Value:    f.Reading,
					Unit:     unit,
					Status:   sensorStatus(f.Status.Health),
					Category: "fan",
				})
			}
		}

		// Power (PSU wattage)
		var power struct {
			PowerConsumedWatts float64 `json:"PowerConsumedWatts"`
			PowerSupplies      []struct {
				Name             string  `json:"Name"`
				PowerInputWatts  float64 `json:"PowerInputWatts"`
				LineInputVoltage float64 `json:"LineInputVoltage"`
				Status           struct {
					Health string `json:"Health"`
				} `json:"Status"`
			} `json:"PowerSupplies"`
		}
		if err := c.get(ctx, member+"/Power", &power); err == nil {
			if power.PowerConsumedWatts > 0 {
				h.Sensors = append(h.Sensors, Sensor{
					Name:     "System Power",
					Value:    power.PowerConsumedWatts,
					Unit:     "Watts",
					Status:   "ok",
					Category: "power",
				})
			}
			for _, psu := range power.PowerSupplies {
				s := sensorStatus(psu.Status.Health)
				if psu.PowerInputWatts > 0 {
					h.Sensors = append(h.Sensors, Sensor{
						Name:     psu.Name + " Input",
						Value:    psu.PowerInputWatts,
						Unit:     "Watts",
						Status:   s,
						Category: "power",
					})
				}
				if psu.LineInputVoltage > 0 {
					h.Sensors = append(h.Sensors, Sensor{
						Name:     psu.Name + " Voltage",
						Value:    psu.LineInputVoltage,
						Unit:     "Volts",
						Status:   s,
						Category: "voltage",
					})
				}
			}
		}
	}

	return h, nil
}

// GetEvents fetches recent BMC System Event Log entries via Redfish.
func (c *redfishClient) GetEvents(ctx context.Context, limit int) ([]Event, error) {
	// Log services hang off the system and the managers; the SEL is under the
	// system on most BMCs and under the manager on iDRAC (the former code
	// read /redfish/v1/Systems/1/..., which iDRAC does not have).
	var services []string
	var parents []string
	if s, err := c.rootCollection(ctx, "Systems"); err == nil {
		parents = append(parents, s...)
	}
	if m, err := c.rootCollection(ctx, "Managers"); err == nil {
		parents = append(parents, m...)
	}
	for _, p := range parents {
		if ls, err := c.collectionMembers(ctx, p+"/LogServices"); err == nil {
			services = append(services, ls...)
		}
	}
	// The SEL first, then any other log.
	sort.SliceStable(services, func(i, j int) bool {
		return strings.Contains(strings.ToLower(services[i]), "sel") && !strings.Contains(strings.ToLower(services[j]), "sel")
	})
	var lastErr error
	for _, svc := range services {
		var entries struct {
			Members []struct {
				ID         string `json:"Id"`
				Created    string `json:"Created"`
				Severity   string `json:"Severity"`
				Message    string `json:"Message"`
				SensorType string `json:"SensorType"`
			} `json:"Members"`
		}
		if err := c.get(ctx, svc+"/Entries", &entries); err != nil {
			lastErr = err
			continue
		}
		var events []Event
		for i, m := range entries.Members {
			if limit > 0 && i >= limit {
				break
			}
			ts, _ := time.Parse(time.RFC3339, m.Created)
			events = append(events, Event{
				ID:        m.ID,
				Timestamp: ts,
				Severity:  redfishSeverity(m.Severity),
				Message:   m.Message,
				Source:    m.SensorType,
			})
		}
		return events, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("the BMC lists no log services")
	}
	return nil, fmt.Errorf("could not access the event log: %v", lastErr)
}

// PowerAction sends a power management command via Redfish.
func (c *redfishClient) PowerAction(ctx context.Context, systemPath string, action PowerAction) error {
	resetType := redfishResetType(action)
	if resetType == "" {
		return fmt.Errorf("unsupported power action: %s", action)
	}
	return c.post(ctx, systemPath+"/Actions/ComputerSystem.Reset",
		map[string]any{"ResetType": resetType})
}

// FindSystemPath returns the OData path of the first system (e.g. /redfish/v1/Systems/1).
func (c *redfishClient) FindSystemPath(ctx context.Context) (string, error) {
	systems, err := c.rootCollection(ctx, "Systems")
	if err != nil {
		return "", err
	}
	if len(systems) == 0 {
		return "", fmt.Errorf("no systems found in the Redfish Systems collection")
	}
	return systems[0], nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

func sensorStatus(health string) string {
	switch strings.ToLower(health) {
	case "ok", "":
		return "ok"
	case "warning":
		return "warning"
	case "critical":
		return "critical"
	default:
		return "unknown"
	}
}

func redfishSeverity(s string) EventSeverity {
	switch strings.ToLower(s) {
	case "ok":
		return SeverityOK
	case "warning":
		return SeverityWarning
	case "critical":
		return SeverityCritical
	default:
		return SeverityInfo
	}
}

func redfishResetType(action PowerAction) string {
	switch action {
	case PowerActionOff:
		return "ForceOff"
	case PowerActionGracefulOff:
		return "GracefulShutdown"
	case PowerActionOn:
		return "On"
	case PowerActionReset:
		return "ForceRestart"
	case PowerActionGracefulReset:
		return "GracefulRestart"
	default:
		return ""
	}
}
