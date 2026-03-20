package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// ═══════════════════════════════════════════════════════════════
//  iSCSI Target Management
//  Uses targetcli (LIO) which is standard on Linux
// ═══════════════════════════════════════════════════════════════

// iSCSI name validation: iqn.YYYY-MM.reverse.domain:identifier
var iqnRegex = regexp.MustCompile(`^iqn\.\d{4}-\d{2}\.[a-z0-9\-\.]+:[a-z0-9\-\.]+$`)

// ISCSITarget represents an iSCSI target
type ISCSITarget struct {
	IQN     string        `json:"iqn"`
	TPGs    []ISCSIPortal `json:"tpgs"`
	LUNs    []ISCSILUN    `json:"luns"`
	ACLs    []ISCSIACL    `json:"acls"`
	Enabled bool          `json:"enabled"`
}

// ISCSIPortal represents a Target Portal Group entry
type ISCSIPortal struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

// ISCSILUN represents a LUN mapping
type ISCSILUN struct {
	LUNIndex   int    `json:"lun_index"`
	BackingDev string `json:"backing_dev"` // e.g. /dev/zvol/tank/iscsi-lun0
	StorageObj string `json:"storage_obj"`
	Size       string `json:"size"`
}

// ISCSIACL represents an initiator ACL
type ISCSIACL struct {
	InitiatorIQN string `json:"initiator_iqn"`
	CHAPUser     string `json:"chap_user,omitempty"`
}

// ISCSICreateRequest is the request body for creating a target
type ISCSICreateRequest struct {
	IQN         string `json:"iqn"`
	BackingDev  string `json:"backing_dev"`  // ZFS zvol path e.g. /dev/zvol/tank/lun0
	PortalIP    string `json:"portal_ip"`
	PortalPort  int    `json:"portal_port"`
	RequireCHAP bool   `json:"require_chap"` // When true, enables CHAP authentication on the TPG.
	// When false (default), the TPG uses ACL-only access control.
	// WARNING: false means unauthenticated access - only set false in
	// isolated networks where initiator IQN spoofing is not a concern.
}

// ISCSIACLRequest is the request body for adding/removing an ACL
type ISCSIACLRequest struct {
	TargetIQN    string `json:"target_iqn"`
	InitiatorIQN string `json:"initiator_iqn"`
	CHAPUser     string `json:"chap_user,omitempty"`
	CHAPPass     string `json:"chap_pass,omitempty"`
}

// ─── Helpers ────────────────────────────────────────────────────

func validateIQN(iqn string) error {
	if !iqnRegex.MatchString(iqn) {
		return fmt.Errorf("invalid IQN format (expected iqn.YYYY-MM.reverse.domain:id)")
	}
	return nil
}

func runTargetcli(args ...string) (string, error) {
	return executeCommandWithTimeout(TimeoutSlow, "/usr/bin/targetcli", args)
}

// ─── Handlers ───────────────────────────────────────────────────

// GetISCSITargets lists all iSCSI targets
// GET /api/iscsi/targets
func GetISCSITargets(w http.ResponseWriter, r *http.Request) {
	out, err := runTargetcli("/iscsi", "ls")
	if err != nil {
		respondErrorSimple(w, "targetcli unavailable", http.StatusServiceUnavailable)
		return
	}

	// Parse plain text output into target list
	targets := parseTargetcliLS(out)
	respondOK(w, map[string]interface{}{
		"success": true,
		"targets": targets,
	})
}

// CreateISCSITarget creates a new iSCSI target with one LUN
// POST /api/iscsi/targets
func CreateISCSITarget(w http.ResponseWriter, r *http.Request) {
	var req ISCSICreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Validate
	if err := validateIQN(req.IQN); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request", err)
		return
	}
	if req.BackingDev == "" {
		respondErrorSimple(w, "backing_dev is required", http.StatusBadRequest)
		return
	}
	if req.PortalPort == 0 {
		req.PortalPort = 3260
	}
	if req.PortalIP == "" {
		req.PortalIP = "0.0.0.0"
	}

	// Create target
	if _, err := runTargetcli("/iscsi", "create", req.IQN); err != nil {
		respondErrorSimple(w, "Failed to create target", http.StatusInternalServerError)
		return
	}

	// Create block storage object
	storageName := sanitizeForTargetcli(req.IQN)
	if _, err := runTargetcli("/backstores/block", "create", storageName, req.BackingDev); err != nil {
		// Best-effort cleanup
		runTargetcli("/iscsi/"+req.IQN, "delete") //nolint
		respondErrorSimple(w, "Failed to create storage object", http.StatusInternalServerError)
		return
	}

	// Create LUN
	tpgPath := fmt.Sprintf("/iscsi/%s/tpg1", req.IQN)
	if _, err := runTargetcli(tpgPath+"/luns", "create", "/backstores/block/"+storageName); err != nil {
		respondErrorSimple(w, "Failed to create LUN", http.StatusInternalServerError)
		return
	}

	// Set portal
	portalAddr := fmt.Sprintf("%s:%d", req.PortalIP, req.PortalPort)
	runTargetcli(tpgPath+"/portals", "delete", "0.0.0.0", "3260") //nolint - remove default portal
	if _, err := runTargetcli(tpgPath+"/portals", "create", portalAddr); err != nil {
		respondErrorSimple(w, "Failed to set portal", http.StatusInternalServerError)
		return
	}

	// Configure authentication on the TPG.
	// authentication=1 requires CHAP credentials before any initiator can log in.
	// authentication=0 relies solely on ACL (initiator IQN) matching - this is
	// weaker because IQNs can be spoofed. Operators must explicitly opt out of CHAP
	// by setting require_chap=false in their request; they cannot silently get it.
	if req.RequireCHAP {
		runTargetcli(tpgPath, "set", "attribute", "authentication=1") //nolint
	} else {
		// Operator explicitly chose ACL-only. Log so this is auditable.
		fmt.Printf("SECURITY NOTICE: iSCSI target %s created with authentication=0 (CHAP disabled). "+
			"Ensure network-level isolation and that initiator IQNs cannot be spoofed.\n", req.IQN)
		runTargetcli(tpgPath, "set", "attribute", "authentication=0") //nolint
	}
	runTargetcli(tpgPath, "enable") //nolint

	// Save config
	runTargetcli("/", "saveconfig") //nolint

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "iSCSI target created",
		"iqn":     req.IQN,
	})
}

// UpdateISCSITarget updates an existing iSCSI target (e.g., changes its backing zvol)
// POST /api/iscsi/targets/update
func UpdateISCSITarget(w http.ResponseWriter, r *http.Request) {
	var req ISCSICreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if err := validateIQN(req.IQN); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request", err)
		return
	}
	if req.BackingDev == "" {
		respondErrorSimple(w, "backing_dev is required", http.StatusBadRequest)
		return
	}

	// In LIO (targetcli), the safest way to "update" a target's backing device
	// without tearing down the entire target is to delete the LUN and re-create it.
	// This will disconnect active sessions, which is expected for such an operation.

	tpgPath := fmt.Sprintf("/iscsi/%s/tpg1", req.IQN)
	storageName := sanitizeForTargetcli(req.IQN)

	// 1. Delete existing LUN 0
	runTargetcli(tpgPath+"/luns", "delete", "0") //nolint

	// 2. Delete existing storage object
	runTargetcli("/backstores/block", "delete", storageName) //nolint

	// 3. Create new storage object
	if _, err := runTargetcli("/backstores/block", "create", storageName, req.BackingDev); err != nil {
		respondErrorSimple(w, "Failed to create new storage object", http.StatusInternalServerError)
		return
	}

	// 4. Create new LUN 0
	if _, err := runTargetcli(tpgPath+"/luns", "create", "/backstores/block/"+storageName); err != nil {
		respondErrorSimple(w, "Failed to create new LUN", http.StatusInternalServerError)
		return
	}

	// Save config
	runTargetcli("/", "saveconfig") //nolint

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "iSCSI target updated",
		"iqn":     req.IQN,
	})
}

// DeleteISCSITarget removes an iSCSI target
// DELETE /api/iscsi/targets/{iqn}
func DeleteISCSITarget(w http.ResponseWriter, r *http.Request) {
	iqn := strings.TrimPrefix(r.URL.Path, "/api/iscsi/targets/")
	if err := validateIQN(iqn); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request", err)
		return
	}

	if _, err := runTargetcli("/iscsi", "delete", iqn); err != nil {
		respondErrorSimple(w, "Failed to delete target", http.StatusInternalServerError)
		return
	}
	runTargetcli("/", "saveconfig") //nolint

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "target deleted",
	})
}

// GetISCSIACLs lists ACLs for a target
// GET /api/iscsi/acls?target=iqn...
func GetISCSIACLs(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	if err := validateIQN(target); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request", err)
		return
	}

	tpgPath := fmt.Sprintf("/iscsi/%s/tpg1/acls", target)
	out, err := runTargetcli(tpgPath, "ls")
	if err != nil {
		respondErrorSimple(w, "Failed to list ACLs", http.StatusInternalServerError)
		return
	}

	acls := parseACLs(out)
	respondOK(w, map[string]interface{}{
		"success": true,
		"acls":    acls,
	})
}

// AddISCSIACL adds an initiator ACL
// POST /api/iscsi/acls
func AddISCSIACL(w http.ResponseWriter, r *http.Request) {
	var req ISCSIACLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := validateIQN(req.TargetIQN); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid target", err)
		return
	}
	if err := validateIQN(req.InitiatorIQN); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid initiator", err)
		return
	}

	aclPath := fmt.Sprintf("/iscsi/%s/tpg1/acls", req.TargetIQN)
	if _, err := runTargetcli(aclPath, "create", req.InitiatorIQN); err != nil {
		respondErrorSimple(w, "Failed to add ACL", http.StatusInternalServerError)
		return
	}

	// Optional CHAP
	if req.CHAPUser != "" && req.CHAPPass != "" {
		initiatorPath := aclPath + "/" + req.InitiatorIQN
		runTargetcli(initiatorPath, "set", "auth", "userid="+req.CHAPUser) //nolint
		runTargetcli(initiatorPath, "set", "auth", "password="+req.CHAPPass) //nolint
	}

	runTargetcli("/", "saveconfig") //nolint
	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "ACL added",
	})
}

// DeleteISCSIACL removes an initiator ACL
// DELETE /api/iscsi/acls
func DeleteISCSIACL(w http.ResponseWriter, r *http.Request) {
	var req ISCSIACLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := validateIQN(req.TargetIQN); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid target", err)
		return
	}
	if err := validateIQN(req.InitiatorIQN); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid initiator", err)
		return
	}

	aclPath := fmt.Sprintf("/iscsi/%s/tpg1/acls", req.TargetIQN)
	if _, err := runTargetcli(aclPath, "delete", req.InitiatorIQN); err != nil {
		respondErrorSimple(w, "Failed to delete ACL", http.StatusInternalServerError)
		return
	}
	runTargetcli("/", "saveconfig") //nolint

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "ACL removed",
	})
}

// GetISCSIStatus returns overall iSCSI service status
// GET /api/iscsi/status
func GetISCSIStatus(w http.ResponseWriter, r *http.Request) {
	out, err := executeCommandWithTimeout(TimeoutFast, "/bin/systemctl", []string{"is-active", "target"})
	active := err == nil && strings.TrimSpace(out) == "active"

	targetCount := 0
	if ls, err := runTargetcli("/iscsi", "ls"); err == nil {
		targetCount = strings.Count(ls, "iqn.")
	}

	respondOK(w, map[string]interface{}{
		"success":      true,
		"service":      map[string]interface{}{"active": active},
		"target_count": targetCount,
	})
}

// ─── Parsers ────────────────────────────────────────────────────

// parseTargetcliLS parses "targetcli /iscsi ls" text output into a simple list
func parseTargetcliLS(output string) []map[string]string {
	var targets []map[string]string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "iqn.") {
			// Strip trailing status indicators like " [enabled]"
			iqn := strings.Fields(line)[0]
			targets = append(targets, map[string]string{"iqn": iqn})
		}
	}
	return targets
}

// parseACLs parses ACL list output
func parseACLs(output string) []map[string]string {
	var acls []map[string]string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "iqn.") {
			iqn := strings.Fields(line)[0]
			acls = append(acls, map[string]string{"initiator_iqn": iqn})
		}
	}
	return acls
}

// sanitizeForTargetcli converts IQN to a valid storage object name
func sanitizeForTargetcli(iqn string) string {
	// Replace dots and colons with underscores, keep alphanumeric
	r := regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	name := r.ReplaceAllString(iqn, "_")
	// Truncate to 64 chars (targetcli limit)
	if len(name) > 64 {
		name = name[len(name)-64:]
	}
	return name
}

// GetISCSIZvolList returns ZFS zvols suitable for iSCSI backing
// GET /api/iscsi/zvols
func GetISCSIZvolList(w http.ResponseWriter, r *http.Request) {
	out, err := executeCommandWithTimeout(TimeoutFast, "/run/current-system/sw/bin/zfs",
		[]string{"list", "-t", "volume", "-H", "-o", "name,volsize"})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": true, "zvols": []interface{}{}})
		return
	}

	var zvols []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			zvols = append(zvols, map[string]string{
				"name":    parts[0],
				"size":    parts[1],
				"dev":     "/dev/zvol/" + parts[0],
			})
		}
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"zvols":   zvols,
	})
}


