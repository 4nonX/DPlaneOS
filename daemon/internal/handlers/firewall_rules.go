package handlers

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"slices"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/firewall"
)

// FirewallRules are the firewall rules made in the web UI on NixOS
// (internal/firewall). Before, a rule was only noted for the next rebuild, a
// source address was dropped (the port opened to everyone), delete and deny
// of a port the system opens did nothing, and "Sync" wiped the list.
var FirewallRules = firewall.New()

// RestoreFirewallRules runs at daemon start on NixOS: a change that was not
// confirmed is reverted, and the saved rules are loaded (they are also
// loaded at boot by dplaneos-firewall.service).
func RestoreFirewallRules() {
	if !onNixOS() {
		return
	}
	if FirewallRules.HasPending() {
		log.Printf("FIREWALL: a change was not confirmed before the daemon stopped; reverting it")
		if err := FirewallRules.RevertPending(); err != nil {
			log.Printf("FIREWALL: revert: %v", err)
		}
	}
	if err := FirewallRules.Sync(); err != nil {
		log.Printf("FIREWALL: loading the rules: %v", err)
	}
}

func firewallStatusNixOS(w http.ResponseWriter) {
	rules := FirewallRules.Rules()
	out := make([]map[string]any, 0, len(rules))
	for i, r := range rules {
		from := r.From
		if from == "" {
			from = "Anywhere"
		}
		out = append(out, map[string]any{"id": i + 1, "action": r.Action, "port": r.Port, "proto": r.Proto, "from": from, "comment": r.Comment})
	}
	status := "inactive"
	if _, err := cmdutil.RunFast("systemctl_firewall_active", "is-active", "--quiet", "firewall.service"); err == nil {
		status = "active"
	}
	resp := map[string]any{
		"success":      true,
		"status":       status,
		"managed_by":   "nixos",
		"rules":        out,
		"rules_loaded": FirewallRules.Active(),
	}
	if sp, err := firewall.ReadSystemPorts(); err == nil {
		resp["system_ports"] = sp
	}
	respondOK(w, resp)
}

// firewallRuleNixOS: allow/deny add a rule, delete removes rule_num, update
// replaces rule_num. Every change is confirmed like a network change.
func firewallRuleNixOS(w http.ResponseWriter, r *http.Request, action string, rule firewall.Rule, num int) {
	rules := FirewallRules.Rules()
	var what string
	switch action {
	case "enable", "disable":
		respondErrorSimple(w, "The firewall is part of the system configuration (networking.firewall.enable); rules here apply on top of it", http.StatusBadRequest)
		return
	case "allow", "deny":
		rule.Action = action
		if err := rule.Normalize(); err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
		rules = append(rules, rule)
		what = fmt.Sprintf("firewall: %s %s/%s", rule.Action, rule.Port, rule.Proto)
	case "delete", "update":
		if num < 1 || num > len(rules) {
			respondErrorSimple(w, "No such rule", http.StatusBadRequest)
			return
		}
		if action == "delete" {
			what = fmt.Sprintf("firewall: delete rule %d (%s %s/%s)", num, rules[num-1].Action, rules[num-1].Port, rules[num-1].Proto)
			rules = slices.Delete(rules, num-1, num)
		} else {
			if err := rule.Normalize(); err != nil {
				respondErrorSimple(w, err.Error(), http.StatusBadRequest)
				return
			}
			rules[num-1] = rule
			what = fmt.Sprintf("firewall: rule %d → %s %s/%s", num, rule.Action, rule.Port, rule.Proto)
		}
	default:
		respondErrorSimple(w, "Invalid action", http.StatusBadRequest)
		return
	}
	start := time.Now()
	err := applyConfirmable(what,
		func(time.Time) error { return FirewallRules.ApplyPending(rules) },
		FirewallRules.Confirm, FirewallRules.RevertPending)
	audit.LogAction("firewall", r.Header.Get("X-User"), what, err == nil, time.Since(start))
	switch {
	case errors.Is(err, errChangePending), errors.Is(err, firewall.ErrPending):
		respondErrorSimple(w, errChangePending.Error(), http.StatusConflict)
	case err != nil:
		respondOK(w, map[string]any{"success": false, "error": err.Error()})
	default:
		respondOK(w, map[string]any{"success": true, "message": what + ": applied; confirm to keep it",
			"revert_seconds": int(networkConfirmWindow.Seconds())})
	}
}
