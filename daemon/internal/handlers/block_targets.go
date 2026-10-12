package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/iscsit"
	"dplaned/internal/nvmet"
	"dplaned/internal/sysapply"
)

// iSCSI targets and NVMe-oF exports: ZFS volumes offered as disks over the
// network.
//
// Both are kept as a saved list (internal/iscsit, internal/nvmet) that the
// kernel is brought in line with: after every change through this API, when
// the daemon starts (the kernel forgets its targets at a reboot), and by the
// storage groups, which serve a target on the node that owns its volume.
// The firewall is open for exactly the ports that are served.

// openServicePorts opens the ports of a service in the firewall (none:
// closes them). Only an installed system has a firewall to tell.
func openServicePorts(service string, ports []string) error {
	if !IsNixOS() {
		return nil
	}
	return (sysapply.Env{}).SetServicePorts(service, ports)
}

// zvolBytes returns the size of a volume from its block device (0 if it is
// not there).
func zvolBytes(zvol string) int64 {
	dev, err := filepath.EvalSymlinks("/dev/zvol/" + zvol)
	if err != nil {
		return 0
	}
	b, err := os.ReadFile(filepath.Join("/sys/class/block", filepath.Base(dev), "size"))
	if err != nil {
		return 0
	}
	sectors, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return sectors * 512
}

// volumeHere reports whether a volume's device exists on this node. Replaced
// in tests.
var volumeHere = func(zvol string) bool {
	st, err := os.Stat("/dev/zvol/" + zvol)
	return err == nil && st.Mode()&os.ModeDevice != 0
}

// aboutThis reports whether an error of applying a whole list concerns the
// named target (errors of other targets, e.g. one whose pool is not
// imported, must not fail a change to this one).
func aboutThis(err error, name string) bool {
	return err != nil && strings.Contains(err.Error(), name)
}

// ── iSCSI ────────────────────────────────────────────────────────────────────

var iscsiMu sync.Mutex

// ISCSICreateRequest is the body of a target create or update.
type ISCSICreateRequest struct {
	IQN         string `json:"iqn"`
	Zvol        string `json:"zvol"`        // the volume's dataset name (tank/lun0)
	BackingDev  string `json:"backing_dev"` // or its device path (/dev/zvol/tank/lun0)
	PortalIP    string `json:"portal_ip"`
	PortalPort  int    `json:"portal_port"`
	RequireCHAP bool   `json:"require_chap"`
}

// ISCSIACLRequest is the body of an access list change.
type ISCSIACLRequest struct {
	TargetIQN    string `json:"target_iqn"`
	InitiatorIQN string `json:"initiator_iqn"`
	CHAPUser     string `json:"chap_user,omitempty"`
	CHAPPass     string `json:"chap_pass,omitempty"`
}

func syncISCSIFirewall() error {
	var ports []string
	for _, p := range iscsit.PortalPorts() {
		ports = append(ports, fmt.Sprintf("tcp %d", p))
	}
	return openServicePorts("iscsi", ports)
}

// changeISCSITargets loads the saved list, lets fn change it, saves and
// applies it. When applying fails for the target that was changed, the
// previous list is put back (saved and applied): the list never says more
// than the system does.
func changeISCSITargets(iqn string, fn func([]iscsit.Target) ([]iscsit.Target, int, error)) (int, error) {
	iscsiMu.Lock()
	defer iscsiMu.Unlock()
	old, err := iscsit.Load(iscsit.TargetsFile)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	changed, code, err := fn(append([]iscsit.Target(nil), old...))
	if err != nil {
		return code, err
	}
	if err := iscsit.Save(iscsit.TargetsFile, changed); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("saving the iSCSI targets: %w", err)
	}
	if err := iscsit.ApplySaved(); err != nil {
		if aboutThis(err, iqn) {
			if serr := iscsit.Save(iscsit.TargetsFile, old); serr != nil {
				log.Printf("ISCSI: putting the previous list back: %v", serr)
			}
			if aerr := iscsit.ApplySaved(); aerr != nil {
				log.Printf("ISCSI: applying the previous list: %v", aerr)
			}
			return http.StatusInternalServerError, fmt.Errorf("%w (nothing was changed)", err)
		}
		log.Printf("ISCSI: %v", err)
	}
	if err := syncISCSIFirewall(); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("the change is made, but the firewall could not be updated: %w", err)
	}
	return http.StatusOK, nil
}

func iscsiTargetOf(req ISCSICreateRequest) iscsit.Target {
	zvol := req.Zvol
	if zvol == "" {
		zvol = req.BackingDev
	}
	return iscsit.Target{IQN: req.IQN, Zvol: zvol, PortalIP: req.PortalIP, PortalPort: req.PortalPort, RequireCHAP: req.RequireCHAP}
}

// GetISCSITargets lists the targets with what the kernel says about them.
// GET /api/iscsi/targets
func GetISCSITargets(w http.ResponseWriter, r *http.Request) {
	list, err := iscsit.Load(iscsit.TargetsFile)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	targets := []map[string]any{}
	for _, t := range list {
		acls := []map[string]any{}
		for _, a := range t.ACLs {
			acls = append(acls, map[string]any{"initiator_iqn": a.InitiatorIQN, "chap_user": a.CHAPUser})
		}
		targets = append(targets, map[string]any{
			"iqn":          t.IQN,
			"zvol":         t.Zvol,
			"size":         zvolBytes(t.Zvol),
			"portal_ip":    t.PortalIP,
			"portal_port":  t.PortalPort,
			"require_chap": t.RequireCHAP,
			"acls":         acls,
			// Served by this node right now. A target on a volume of a
			// storage group is served by the node that owns the group.
			"active":   iscsit.Active(t.IQN),
			"sessions": iscsit.Sessions(t.IQN),
		})
	}
	respondOK(w, map[string]any{"success": true, "targets": targets})
}

// CreateISCSITarget creates a target with one LUN.
// POST /api/iscsi/targets
func CreateISCSITarget(w http.ResponseWriter, r *http.Request) {
	var req ISCSICreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	t := iscsiTargetOf(req)
	if t.Zvol == "" {
		respondErrorSimple(w, "zvol (or backing_dev) is required", http.StatusBadRequest)
		return
	}
	if err := iscsit.Validate(&t); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !volumeHere(t.Zvol) {
		respondErrorSimple(w, "volume "+t.Zvol+" does not exist on this node (create it under Storage, Block Volumes; a volume of a storage group is on the node that owns the group)", http.StatusBadRequest)
		return
	}
	code, err := changeISCSITargets(t.IQN, func(all []iscsit.Target) ([]iscsit.Target, int, error) {
		for _, e := range all {
			if e.IQN == t.IQN {
				return nil, http.StatusConflict, fmt.Errorf("target %s already exists", t.IQN)
			}
			if e.Zvol == t.Zvol {
				return nil, http.StatusConflict, fmt.Errorf("volume %s is already offered by target %s", t.Zvol, e.IQN)
			}
		}
		return append(all, t), 0, nil
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), code)
		return
	}
	if !t.RequireCHAP {
		log.Printf("SECURITY NOTICE: iSCSI target %s created without CHAP: only the initiator names of its access list are checked", t.IQN)
	}
	audit.LogActivity(r.Header.Get("X-User"), "iscsi_target_create", map[string]any{"iqn": t.IQN, "zvol": t.Zvol, "require_chap": t.RequireCHAP})
	respondOK(w, map[string]any{"success": true, "message": "iSCSI target created", "iqn": t.IQN})
}

// UpdateISCSITarget changes a target's volume, address or authentication
// mode. Its initiators are disconnected while it is rebuilt; its access
// list stays.
// POST /api/iscsi/targets/update
func UpdateISCSITarget(w http.ResponseWriter, r *http.Request) {
	var req ISCSICreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	t := iscsiTargetOf(req)
	if t.Zvol == "" {
		respondErrorSimple(w, "zvol (or backing_dev) is required", http.StatusBadRequest)
		return
	}
	if err := iscsit.Validate(&t); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !volumeHere(t.Zvol) {
		respondErrorSimple(w, "volume "+t.Zvol+" does not exist on this node", http.StatusBadRequest)
		return
	}
	code, err := changeISCSITargets(t.IQN, func(all []iscsit.Target) ([]iscsit.Target, int, error) {
		found := -1
		for i, e := range all {
			if e.IQN == t.IQN {
				found = i
			} else if e.Zvol == t.Zvol {
				return nil, http.StatusConflict, fmt.Errorf("volume %s is already offered by target %s", t.Zvol, e.IQN)
			}
		}
		if found < 0 {
			return nil, http.StatusNotFound, fmt.Errorf("target %s not found", t.IQN)
		}
		t.ACLs = all[found].ACLs
		if err := iscsit.Validate(&t); err != nil { // CHAP now required: every initiator needs a secret
			return nil, http.StatusBadRequest, err
		}
		all[found] = t
		return all, 0, nil
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), code)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "iscsi_target_update", map[string]any{"iqn": t.IQN, "zvol": t.Zvol, "require_chap": t.RequireCHAP})
	respondOK(w, map[string]any{"success": true, "message": "iSCSI target updated", "iqn": t.IQN})
}

// DeleteISCSITarget removes a target and its access list; the volume and
// its data stay.
// DELETE /api/iscsi/targets/{iqn}
func DeleteISCSITarget(w http.ResponseWriter, r *http.Request) {
	iqn := strings.TrimPrefix(r.URL.Path, "/api/iscsi/targets/")
	if err := iscsit.ValidIQN(iqn); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	code, err := changeISCSITargets(iqn, func(all []iscsit.Target) ([]iscsit.Target, int, error) {
		out := make([]iscsit.Target, 0, len(all))
		for _, e := range all {
			if e.IQN != iqn {
				out = append(out, e)
			}
		}
		if len(out) == len(all) {
			return nil, http.StatusNotFound, fmt.Errorf("target %s not found", iqn)
		}
		return out, 0, nil
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), code)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "iscsi_target_delete", map[string]any{"iqn": iqn})
	respondOK(w, map[string]any{"success": true, "message": "target deleted"})
}

// GetISCSIACLs lists the initiators that may use a target (all targets
// without ?target=). CHAP secrets are not returned.
// GET /api/iscsi/acls?target=iqn...
func GetISCSIACLs(w http.ResponseWriter, r *http.Request) {
	only := r.URL.Query().Get("target")
	if only != "" {
		if err := iscsit.ValidIQN(only); err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	list, err := iscsit.Load(iscsit.TargetsFile)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	acls := []map[string]any{}
	for _, t := range list {
		if only != "" && t.IQN != only {
			continue
		}
		for _, a := range t.ACLs {
			acls = append(acls, map[string]any{
				"target_iqn": t.IQN, "initiator_iqn": a.InitiatorIQN, "chap_user": a.CHAPUser,
			})
		}
	}
	respondOK(w, map[string]any{"success": true, "acls": acls})
}

// AddISCSIACL allows an initiator to use a target (or changes its CHAP
// credentials).
// POST /api/iscsi/acls
func AddISCSIACL(w http.ResponseWriter, r *http.Request) {
	var req ISCSIACLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := iscsit.ValidIQN(req.TargetIQN); err != nil {
		respondErrorSimple(w, "target: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := iscsit.ValidIQN(req.InitiatorIQN); err != nil {
		respondErrorSimple(w, "initiator: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := iscsit.ValidCHAP(req.CHAPUser, req.CHAPPass); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	acl := iscsit.ACL{InitiatorIQN: req.InitiatorIQN, CHAPUser: req.CHAPUser, CHAPPass: req.CHAPPass}
	code, err := changeISCSITargets(req.TargetIQN, func(all []iscsit.Target) ([]iscsit.Target, int, error) {
		for i := range all {
			if all[i].IQN != req.TargetIQN {
				continue
			}
			if all[i].RequireCHAP && acl.CHAPUser == "" {
				return nil, http.StatusBadRequest, fmt.Errorf("target %s requires CHAP: give the initiator a CHAP user and secret", req.TargetIQN)
			}
			acls := make([]iscsit.ACL, 0, len(all[i].ACLs)+1)
			for _, a := range all[i].ACLs {
				if a.InitiatorIQN != acl.InitiatorIQN {
					acls = append(acls, a)
				}
			}
			all[i].ACLs = append(acls, acl)
			return all, 0, nil
		}
		return nil, http.StatusNotFound, fmt.Errorf("target %s not found", req.TargetIQN)
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), code)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "iscsi_acl_add", map[string]any{"iqn": req.TargetIQN, "initiator": req.InitiatorIQN, "chap": req.CHAPUser != ""})
	respondOK(w, map[string]any{"success": true, "message": "ACL added"})
}

// DeleteISCSIACL takes an initiator off a target's access list (and
// disconnects it).
// DELETE /api/iscsi/acls
func DeleteISCSIACL(w http.ResponseWriter, r *http.Request) {
	var req ISCSIACLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := iscsit.ValidIQN(req.TargetIQN); err != nil {
		respondErrorSimple(w, "target: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := iscsit.ValidIQN(req.InitiatorIQN); err != nil {
		respondErrorSimple(w, "initiator: "+err.Error(), http.StatusBadRequest)
		return
	}
	code, err := changeISCSITargets(req.TargetIQN, func(all []iscsit.Target) ([]iscsit.Target, int, error) {
		for i := range all {
			if all[i].IQN != req.TargetIQN {
				continue
			}
			acls := make([]iscsit.ACL, 0, len(all[i].ACLs))
			for _, a := range all[i].ACLs {
				if a.InitiatorIQN != req.InitiatorIQN {
					acls = append(acls, a)
				}
			}
			if len(acls) == len(all[i].ACLs) {
				return nil, http.StatusNotFound, fmt.Errorf("initiator %s is not on the access list of %s", req.InitiatorIQN, req.TargetIQN)
			}
			all[i].ACLs = acls
			return all, 0, nil
		}
		return nil, http.StatusNotFound, fmt.Errorf("target %s not found", req.TargetIQN)
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), code)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "iscsi_acl_remove", map[string]any{"iqn": req.TargetIQN, "initiator": req.InitiatorIQN})
	respondOK(w, map[string]any{"success": true, "message": "ACL removed"})
}

// GetISCSIStatus says how many targets are configured, how many of them
// this node serves, and how many initiators are connected.
// GET /api/iscsi/status
func GetISCSIStatus(w http.ResponseWriter, r *http.Request) {
	list, err := iscsit.Load(iscsit.TargetsFile)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	served, sessions := 0, 0
	for _, t := range list {
		if iscsit.Active(t.IQN) {
			served++
			sessions += iscsit.Sessions(t.IQN)
		}
	}
	respondOK(w, map[string]any{
		"success":      true,
		"target_count": len(list),
		"served":       served,
		"sessions":     sessions,
	})
}

// GetISCSIZvolList lists the ZFS volumes that can be offered.
// GET /api/iscsi/zvols
func GetISCSIZvolList(w http.ResponseWriter, r *http.Request) {
	out, err := executeCommandWithTimeout(TimeoutFast, "zfs",
		[]string{"list", "-t", "volume", "-H", "-o", "name,volsize"})
	if err != nil {
		respondOK(w, map[string]any{"success": true, "zvols": []any{}})
		return
	}
	zvols := []map[string]string{}
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
	respondOK(w, map[string]any{"success": true, "zvols": zvols})
}

// ── NVMe-oF ──────────────────────────────────────────────────────────────────

var nvmeMu sync.Mutex

func syncNVMeFirewall() error {
	// The ports the kernel listens on.
	seen := map[string]bool{}
	matches, _ := filepath.Glob(filepath.Join(nvmet.ConfigfsRoot, "nvmet", "ports", "*", "addr_trsvcid"))
	for _, m := range matches {
		if b, err := os.ReadFile(m); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 {
				seen[fmt.Sprintf("tcp %d", p)] = true
			}
		}
	}
	ports := make([]string, 0, len(seen))
	for p := range seen {
		ports = append(ports, p)
	}
	sort.Strings(ports)
	return openServicePorts("nvmeof", ports)
}

// changeNVMeExports is changeISCSITargets for the NVMe-oF exports.
func changeNVMeExports(nqn string, fn func([]nvmet.Export) ([]nvmet.Export, int, error)) (int, error) {
	nvmeMu.Lock()
	defer nvmeMu.Unlock()
	old, err := nvmet.LoadExports(nvmet.TargetsFile)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	changed, code, err := fn(append([]nvmet.Export(nil), old...))
	if err != nil {
		return code, err
	}
	if err := nvmet.SaveExports(nvmet.TargetsFile, changed); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("saving the NVMe-oF exports: %w", err)
	}
	if err := nvmet.ApplySaved(); err != nil {
		if aboutThis(err, nqn) {
			if serr := nvmet.SaveExports(nvmet.TargetsFile, old); serr != nil {
				log.Printf("NVMEOF: putting the previous list back: %v", serr)
			}
			if aerr := nvmet.ApplySaved(); aerr != nil {
				log.Printf("NVMEOF: applying the previous list: %v", aerr)
			}
			return http.StatusInternalServerError, fmt.Errorf("%w (nothing was changed)", err)
		}
		log.Printf("NVMEOF: %v", err)
	}
	if err := syncNVMeFirewall(); err != nil {
		return http.StatusInternalServerError, fmt.Errorf("the change is made, but the firewall could not be updated: %w", err)
	}
	return http.StatusOK, nil
}

// GetNVMeTargetStatus reports whether the kernel's NVMe target is there.
// GET /api/nvmet/status
func GetNVMeTargetStatus(w http.ResponseWriter, r *http.Request) {
	_, _ = executeCommandWithTimeout(TimeoutSlow, "modprobe", []string{"nvmet"})
	_, _ = executeCommandWithTimeout(TimeoutSlow, "modprobe", []string{"nvmet-tcp"})
	root := filepath.Join(nvmet.ConfigfsRoot, "nvmet")
	st, err := os.Stat(root)
	respondOK(w, map[string]any{
		"success":    true,
		"ready":      err == nil && st.IsDir(),
		"nvmet_root": root,
	})
}

// nvmeServed reports whether the kernel serves a subsystem.
func nvmeServed(nqn string) bool {
	st, err := os.Stat(filepath.Join(nvmet.ConfigfsRoot, "nvmet", "subsystems", nqn))
	return err == nil && st.IsDir()
}

// ListNVMeTargets returns the exports with whether this node serves them.
// GET /api/nvmet/targets
func ListNVMeTargets(w http.ResponseWriter, r *http.Request) {
	list, err := nvmet.LoadExports(nvmet.TargetsFile)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	targets := []map[string]any{}
	for _, e := range list {
		hosts := e.HostNQNs
		if hosts == nil {
			hosts = []string{}
		}
		targets = append(targets, map[string]any{
			"subsystem_nqn":  e.SubsystemNQN,
			"zvol":           e.Zvol,
			"size":           zvolBytes(e.Zvol),
			"transport":      e.Transport,
			"listen_addr":    e.ListenAddr,
			"listen_port":    e.ListenPort,
			"namespace_id":   e.NamespaceID,
			"allow_any_host": e.AllowAnyHost,
			"host_nqns":      hosts,
			"active":         nvmeServed(e.SubsystemNQN),
		})
	}
	respondOK(w, map[string]any{"success": true, "targets": targets, "count": len(targets)})
}

// ListNVMeZvols lists the ZFS volumes that can be exported.
// GET /api/nvmet/zvols
func ListNVMeZvols(w http.ResponseWriter, r *http.Request) {
	GetISCSIZvolList(w, r)
}

func decodeNVMeExport(w http.ResponseWriter, r *http.Request) (nvmet.Export, bool) {
	var req nvmet.Export
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return req, false
	}
	if err := nvmet.ValidateSpec(&req); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return req, false
	}
	if !volumeHere(req.Zvol) {
		respondErrorSimple(w, "volume "+req.Zvol+" does not exist on this node (create it under Storage, Block Volumes; a volume of a storage group is on the node that owns the group)", http.StatusBadRequest)
		return req, false
	}
	return req, true
}

// CreateNVMeTarget adds an export.
// POST /api/nvmet/targets
func CreateNVMeTarget(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeNVMeExport(w, r)
	if !ok {
		return
	}
	code, err := changeNVMeExports(req.SubsystemNQN, func(all []nvmet.Export) ([]nvmet.Export, int, error) {
		for _, e := range all {
			if e.SubsystemNQN == req.SubsystemNQN {
				return nil, http.StatusConflict, fmt.Errorf("export %s already exists", req.SubsystemNQN)
			}
			if e.Zvol == req.Zvol {
				return nil, http.StatusConflict, fmt.Errorf("volume %s is already exported as %s", req.Zvol, e.SubsystemNQN)
			}
		}
		return append(all, req), 0, nil
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), code)
		return
	}
	if req.AllowAnyHost {
		log.Printf("SECURITY NOTICE: NVMe-oF export %s created for any host: every machine that reaches port %d can use it", req.SubsystemNQN, req.ListenPort)
	}
	audit.LogActivity(r.Header.Get("X-User"), "nvmeof_export_create", map[string]any{"nqn": req.SubsystemNQN, "zvol": req.Zvol, "any_host": req.AllowAnyHost})
	respondOK(w, map[string]any{"success": true, "message": "NVMe-oF export created", "subsystem_nqn": req.SubsystemNQN})
}

// UpdateNVMeTarget replaces an export. Changing its hosts keeps the
// connected initiators; changing its volume or address reconnects them.
// PUT /api/nvmet/targets
func UpdateNVMeTarget(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeNVMeExport(w, r)
	if !ok {
		return
	}
	code, err := changeNVMeExports(req.SubsystemNQN, func(all []nvmet.Export) ([]nvmet.Export, int, error) {
		found := -1
		for i, e := range all {
			if e.SubsystemNQN == req.SubsystemNQN {
				found = i
			} else if e.Zvol == req.Zvol {
				return nil, http.StatusConflict, fmt.Errorf("volume %s is already exported as %s", req.Zvol, e.SubsystemNQN)
			}
		}
		if found < 0 {
			return nil, http.StatusNotFound, fmt.Errorf("export %s not found", req.SubsystemNQN)
		}
		all[found] = req
		return all, 0, nil
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), code)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "nvmeof_export_update", map[string]any{"nqn": req.SubsystemNQN, "zvol": req.Zvol, "any_host": req.AllowAnyHost})
	respondOK(w, map[string]any{"success": true, "message": "NVMe-oF export updated"})
}

// DeleteNVMeTarget removes an export; the volume and its data stay.
// DELETE /api/nvmet/targets?subsystem_nqn=nqn....
func DeleteNVMeTarget(w http.ResponseWriter, r *http.Request) {
	nqn := strings.TrimSpace(r.URL.Query().Get("subsystem_nqn"))
	if nqn == "" {
		respondErrorSimple(w, "subsystem_nqn query parameter required", http.StatusBadRequest)
		return
	}
	code, err := changeNVMeExports(nqn, func(all []nvmet.Export) ([]nvmet.Export, int, error) {
		out := make([]nvmet.Export, 0, len(all))
		for _, e := range all {
			if e.SubsystemNQN != nqn {
				out = append(out, e)
			}
		}
		if len(out) == len(all) {
			return nil, http.StatusNotFound, fmt.Errorf("export %s not found", nqn)
		}
		return out, 0, nil
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), code)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "nvmeof_export_delete", map[string]any{"nqn": nqn})
	respondOK(w, map[string]any{"success": true, "message": "NVMe-oF export removed"})
}

// ApplyNVMeExports makes the kernel serve exactly these exports and opens
// the firewall for them (used by the storage groups).
func ApplyNVMeExports(list []nvmet.Export) error {
	err := nvmet.Apply(list)
	if ferr := syncNVMeFirewall(); ferr != nil && err == nil {
		err = fmt.Errorf("firewall: %w", ferr)
	}
	return err
}

// ApplyISCSITargets makes the kernel serve exactly these targets and opens
// the firewall for them (used by the storage groups).
func ApplyISCSITargets(list []iscsit.Target) error {
	err := iscsit.Apply(list)
	if ferr := syncISCSIFirewall(); ferr != nil && err == nil {
		err = fmt.Errorf("firewall: %w", ferr)
	}
	return err
}

// ── After a restart ──────────────────────────────────────────────────────────

// RestoreBlockTargets serves the saved iSCSI targets and NVMe-oF exports
// again after a reboot (the kernel forgets them). When the daemon was only
// restarted, the kernel still has them and nothing is touched: connected
// initiators stay connected. Volumes appear a moment after their pool is
// imported, so a failed attempt is repeated for a while; after that the
// storage groups' loop keeps trying for the targets on their pools.
func RestoreBlockTargets() {
	for name, apply := range map[string]func() error{"ISCSI": iscsit.ApplySaved, "NVMEOF": nvmet.ApplySaved} {
		var err error
		for attempt := 0; attempt < 6; attempt++ {
			if err = apply(); err == nil {
				break
			}
			time.Sleep(10 * time.Second)
		}
		if err != nil {
			log.Printf("%s: not everything that is configured could be served: %v", name, err)
		}
	}
	if err := syncISCSIFirewall(); err != nil {
		log.Printf("ISCSI: firewall: %v", err)
	}
	if err := syncNVMeFirewall(); err != nil {
		log.Printf("NVMEOF: firewall: %v", err)
	}
}
