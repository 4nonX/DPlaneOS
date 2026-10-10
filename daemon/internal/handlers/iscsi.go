package handlers

import (
	"encoding/json"
	"fmt"
	"net"
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

// ALUAState is the ALUA target port group access state for an iSCSI target.
// Values per SPC-5 §6.9.4 and Linux LIO configfs documentation.
type ALUAState int

const (
	ALUAActiveOptimized    ALUAState = 0 // Active/Optimized  - primary HA node
	ALUAActiveNonOptimized ALUAState = 1 // Active/Non-Optimized - secondary path
	ALUAStandby            ALUAState = 2 // Standby  - HA standby node
	ALUAUnavailable        ALUAState = 3 // Unavailable
)

// ISCSICreateRequest is the request body for creating a target
type ISCSICreateRequest struct {
	IQN         string `json:"iqn"`
	BackingDev  string `json:"backing_dev"` // ZFS zvol path e.g. /dev/zvol/tank/lun0
	Zvol        string `json:"zvol"`        // or the volume's dataset name (tank/lun0), as the web UI sends it
	PortalIP    string `json:"portal_ip"`
	PortalPort  int    `json:"portal_port"`
	RequireCHAP bool   `json:"require_chap"` // When true, enables CHAP authentication on the TPG.
	// When false (default), the TPG uses ACL-only access control.
	// WARNING: false means unauthenticated access - only set false in
	// isolated networks where initiator IQN spoofing is not a concern.
	ALUAEnabled bool      `json:"alua_enabled,omitempty"` // Enable ALUA (TPGS) on this target
	ALUAState   ALUAState `json:"alua_state,omitempty"`   // Initial ALUA access state
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

// targetcli is not in the command whitelist; its arguments are checked here.
var (
	zvolDevRe  = regexp.MustCompile(`^/dev/zvol/[A-Za-z0-9][A-Za-z0-9_.:/-]*$`)
	chapUserRe = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,64}$`)
	chapPassRe = regexp.MustCompile(`^[\x21-\x7e]{12,255}$`) // printable, no spaces (targetcli joins its arguments)
)

// validateBackingDev allows only ZFS volumes: anything else (a whole disk,
// the system disk) must not be exported over the network by this API.
// backingDev returns the device of the request: backing_dev, or the zvol
// dataset name turned into its /dev/zvol path.
func (req ISCSICreateRequest) backingDev() string {
	if req.BackingDev == "" && req.Zvol != "" {
		return "/dev/zvol/" + strings.TrimPrefix(req.Zvol, "/")
	}
	return req.BackingDev
}

func validateBackingDev(dev string) error {
	if !zvolDevRe.MatchString(dev) || strings.Contains(dev, "..") {
		return fmt.Errorf("backing_dev must be a ZFS volume (/dev/zvol/<pool>/<volume>)")
	}
	return nil
}

func validateCHAP(user, pass string) error {
	if user == "" && pass == "" {
		return nil
	}
	if !chapUserRe.MatchString(user) {
		return fmt.Errorf("invalid CHAP user (letters, digits, . _ @ : -; up to 64)")
	}
	if !chapPassRe.MatchString(pass) {
		return fmt.Errorf("CHAP secret must be 12 to 255 printable characters without spaces")
	}
	return nil
}

func runTargetcli(args ...string) (string, error) {
	return executeCommandWithTimeout(TimeoutSlow, "targetcli", args)
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
	respondOK(w, map[string]any{
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
	req.BackingDev = req.backingDev()
	if req.BackingDev == "" {
		respondErrorSimple(w, "backing_dev (or zvol) is required", http.StatusBadRequest)
		return
	}
	if err := validateBackingDev(req.BackingDev); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.PortalPort == 0 {
		req.PortalPort = 3260
	}
	if req.PortalPort < 1 || req.PortalPort > 65535 {
		respondErrorSimple(w, "invalid portal_port", http.StatusBadRequest)
		return
	}
	if req.PortalIP == "" {
		req.PortalIP = "0.0.0.0"
	}
	if net.ParseIP(req.PortalIP) == nil {
		respondErrorSimple(w, "portal_ip must be an IP address", http.StatusBadRequest)
		return
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

	// A half-built target is removed again (target and storage object).
	cleanup := func() {
		runTargetcli("/iscsi", "delete", req.IQN)                //nolint
		runTargetcli("/backstores/block", "delete", storageName) //nolint
	}

	// Create LUN
	tpgPath := fmt.Sprintf("/iscsi/%s/tpg1", req.IQN)
	if _, err := runTargetcli(tpgPath+"/luns", "create", "/backstores/block/"+storageName); err != nil {
		cleanup()
		respondErrorSimple(w, "Failed to create LUN", http.StatusInternalServerError)
		return
	}

	// Set portal
	portalAddr := fmt.Sprintf("%s:%d", req.PortalIP, req.PortalPort)
	runTargetcli(tpgPath+"/portals", "delete", "0.0.0.0", "3260") //nolint - remove default portal
	if _, err := runTargetcli(tpgPath+"/portals", "create", portalAddr); err != nil {
		cleanup()
		respondErrorSimple(w, "Failed to set portal", http.StatusInternalServerError)
		return
	}

	// Configure authentication on the TPG.
	// authentication=1 requires CHAP credentials before any initiator can log in.
	// authentication=0 relies solely on ACL (initiator IQN) matching - this is
	// weaker because IQNs can be spoofed. Operators must explicitly opt out of CHAP
	// by setting require_chap=false in their request; they cannot silently get it.
	// Authentication is security-relevant: a target whose mode could not be
	// set is not left running.
	authMode := "authentication=0"
	if req.RequireCHAP {
		authMode = "authentication=1"
	} else {
		// Operator explicitly chose ACL-only. Log so this is auditable.
		fmt.Printf("SECURITY NOTICE: iSCSI target %s created with authentication=0 (CHAP disabled). "+
			"Ensure network-level isolation and that initiator IQNs cannot be spoofed.\n", req.IQN)
	}
	if _, err := runTargetcli(tpgPath, "set", "attribute", authMode); err != nil {
		cleanup()
		respondErrorSimple(w, "Failed to set the authentication mode; the target was removed", http.StatusInternalServerError)
		return
	}
	if _, err := runTargetcli(tpgPath, "enable"); err != nil {
		cleanup()
		respondErrorSimple(w, "Failed to enable the target; it was removed", http.StatusInternalServerError)
		return
	}

	// ALUA (Asymmetric Logical Unit Access / TPGS) configuration.
	// When enabled, LIO exposes a Target Port Group so multi-path initiators can
	// determine which controller owns the active path and fall back gracefully on HA
	// failover. The primary node should use ALUAActiveOptimized (0); the standby
	// node should use ALUAStandby (2).
	if req.ALUAEnabled {
		if err := configureALUA(tpgPath, req.ALUAState); err != nil {
			// ALUA is best-effort - log the failure but do not tear down the target.
			fmt.Printf("ALUA CONFIG WARNING: %v - target created without ALUA\n", err)
		}
	}

	// Save config: without it the target is gone after a reboot.
	if _, err := runTargetcli("/", "saveconfig"); err != nil {
		respondOK(w, map[string]any{"success": false, "iqn": req.IQN,
			"error": "The target is running, but saving the iSCSI configuration failed: it is lost at the next reboot"})
		return
	}

	respondOK(w, map[string]any{
		"success":      true,
		"message":      "iSCSI target created",
		"iqn":          req.IQN,
		"alua_enabled": req.ALUAEnabled,
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
	req.BackingDev = req.backingDev()
	if req.BackingDev == "" {
		respondErrorSimple(w, "backing_dev (or zvol) is required", http.StatusBadRequest)
		return
	}
	if err := validateBackingDev(req.BackingDev); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
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

	if _, err := runTargetcli("/", "saveconfig"); err != nil {
		respondOK(w, map[string]any{"success": false, "iqn": req.IQN,
			"error": "The target is updated, but saving the iSCSI configuration failed: the change is lost at the next reboot"})
		return
	}

	respondOK(w, map[string]any{
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

	respondOK(w, map[string]any{
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
	respondOK(w, map[string]any{
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
	if err := validateCHAP(req.CHAPUser, req.CHAPPass); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	aclPath := fmt.Sprintf("/iscsi/%s/tpg1/acls", req.TargetIQN)
	if _, err := runTargetcli(aclPath, "create", req.InitiatorIQN); err != nil {
		respondErrorSimple(w, "Failed to add ACL", http.StatusInternalServerError)
		return
	}

	// Optional CHAP
	// CHAP that silently failed would leave the initiator allowed without
	// the authentication the admin asked for: undo the ACL instead.
	if req.CHAPUser != "" {
		initiatorPath := aclPath + "/" + req.InitiatorIQN
		_, err1 := runTargetcli(initiatorPath, "set", "auth", "userid="+req.CHAPUser)
		_, err2 := runTargetcli(initiatorPath, "set", "auth", "password="+req.CHAPPass)
		if err1 != nil || err2 != nil {
			runTargetcli(aclPath, "delete", req.InitiatorIQN) //nolint
			respondErrorSimple(w, "Failed to set CHAP credentials; the ACL was not added", http.StatusInternalServerError)
			return
		}
	}

	if _, err := runTargetcli("/", "saveconfig"); err != nil {
		respondOK(w, map[string]any{"success": false, "error": "The ACL is active, but saving the iSCSI configuration failed: it is lost at the next reboot"})
		return
	}
	respondOK(w, map[string]any{
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

	respondOK(w, map[string]any{
		"success": true,
		"message": "ACL removed",
	})
}

// GetISCSIStatus returns overall iSCSI service status
// GET /api/iscsi/status
func GetISCSIStatus(w http.ResponseWriter, r *http.Request) {
	out, err := executeCommandWithTimeout(TimeoutFast, "systemctl", []string{"is-active", "target"})
	active := err == nil && strings.TrimSpace(out) == "active"

	targetCount := 0
	if ls, err := runTargetcli("/iscsi", "ls"); err == nil {
		targetCount = strings.Count(ls, "iqn.")
	}

	respondOK(w, map[string]any{
		"success":      true,
		"service":      map[string]any{"active": active},
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

// SetISCSIALUAState changes the ALUA access state for a target's TPG.
// This is called during HA failover to flip the standby node from Standby (2)
// to Active/Optimized (0) and the former primary to Standby (2).
//
// POST /api/iscsi/targets/alua
func SetISCSIALUAState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TargetIQN string    `json:"target_iqn"`
		ALUAState ALUAState `json:"alua_state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := validateIQN(req.TargetIQN); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid target", err)
		return
	}

	tpgPath := fmt.Sprintf("/iscsi/%s/tpg1", req.TargetIQN)
	if err := configureALUA(tpgPath, req.ALUAState); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runTargetcli("/", "saveconfig") //nolint

	respondOK(w, map[string]any{
		"success":    true,
		"alua_state": int(req.ALUAState),
	})
}

// configureALUA enables TPGS on a TPG and sets the ALUA access state.
// LIO exposes ALUA via targetcli's "alua" subtree. The default_tg_pt_gp group
// is created automatically; we set its alua_access_state to the requested value.
func configureALUA(tpgPath string, state ALUAState) error {
	// Enable Target Port Group Support on the TPG.
	if _, err := runTargetcli(tpgPath, "set", "attribute", "alua_support=1"); err != nil {
		return fmt.Errorf("enable ALUA on %s: %w", tpgPath, err)
	}
	// Set the access state on the default_tg_pt_gp ALUA group.
	aluaPath := fmt.Sprintf("%s/alua/default_tg_pt_gp", tpgPath)
	stateStr := fmt.Sprintf("alua_access_state=%d", int(state))
	if _, err := runTargetcli(aluaPath, "set", stateStr); err != nil {
		return fmt.Errorf("set ALUA state on %s: %w", aluaPath, err)
	}
	return nil
}

// GetISCSIZvolList returns ZFS zvols suitable for iSCSI backing
// GET /api/iscsi/zvols
func GetISCSIZvolList(w http.ResponseWriter, r *http.Request) {
	out, err := executeCommandWithTimeout(TimeoutFast, "zfs",
		[]string{"list", "-t", "volume", "-H", "-o", "name,volsize"})
	if err != nil {
		respondOK(w, map[string]any{"success": true, "zvols": []any{}})
		return
	}

	var zvols []map[string]string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			zvols = append(zvols, map[string]string{
				"name": parts[0],
				"size": parts[1],
				"dev":  "/dev/zvol/" + parts[0],
			})
		}
	}

	respondOK(w, map[string]any{
		"success": true,
		"zvols":   zvols,
	})
}
