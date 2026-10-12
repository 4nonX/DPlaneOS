package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/nixwriter"
)

// The firewall of an installed system is the system's own packet filter.
// The rules set here are saved with the system settings and applied at once
// (internal/sysapply writes them where the firewall reads them and reloads
// it): they need no rebuild and survive reboots and system updates.
//
// Model: everything is refused except the ports of the system's own
// services (the web interface, file sharing, SSH; listed as "system" in the
// status) and what an allow rule opens. Deny rules come first: a denied
// source cannot reach a port even if a system service or another rule
// opens it.

func firewallActive() bool {
	_, err := cmdutil.RunFast("systemctl_unit", "is-active", "--quiet", "firewall.service")
	return err == nil
}

var fwAcceptRe = regexp.MustCompile(`-p (tcp|udp)\b.*--dports? (\S+).* -j nixos-fw-accept`)

// systemOpenPorts lists the ports the system configuration opens for its
// own services ("80/tcp", "137:138/udp"), read from the running filter.
func systemOpenPorts() []string {
	out, err := cmdutil.RunFast("iptables", "-w", "-S", "nixos-fw")
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var ports []string
	for _, line := range strings.Split(string(out), "\n") {
		m := fwAcceptRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		for _, p := range strings.Split(m[2], ",") {
			key := p + "/" + m[1]
			if !seen[key] {
				seen[key] = true
				ports = append(ports, key)
			}
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.FieldsFunc(ports[i], func(r rune) bool { return r == ':' || r == '/' })[0])
		b, _ := strconv.Atoi(strings.FieldsFunc(ports[j], func(r rune) bool { return r == ':' || r == '/' })[0])
		if a != b {
			return a < b
		}
		return ports[i] < ports[j]
	})
	return ports
}

// getStatusNative answers GET /api/firewall/status on an installed system.
func (h *FirewallHandler) getStatusNative(w http.ResponseWriter, _ *http.Request) {
	state := NixWriter.State()
	rules := []map[string]any{}
	for i, r := range state.FirewallList() {
		from := r.From
		if from == "" {
			from = "Anywhere"
		}
		rules = append(rules, map[string]any{
			"id": i + 1, "action": r.Action, "port": r.Ports(), "proto": r.Proto, "from": from,
		})
	}
	status := "inactive"
	if firewallActive() {
		status = "active"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"success": true,
		"status":  status,
		// Switched off in the web interface (status then is "inactive"; any
		// other combination means the firewall failed to start or stop).
		"disabled":     state.FirewallDisabled,
		"rules":        rules,
		"system_ports": systemOpenPorts(),
	})
}

// setRuleNative answers POST /api/firewall/rule on an installed system.
func (h *FirewallHandler) setRuleNative(w http.ResponseWriter, r *http.Request, user string, action, portSpec, from, protoField string, ruleNum int) {
	start := time.Now()
	var err error
	what := action
	switch action {
	case "enable", "disable":
		err = NixWriter.SetFirewallDisabled(action == "disable")
		if err == nil && firewallActive() != (action == "enable") {
			err = fmt.Errorf("the firewall did not %s", map[string]string{"enable": "start", "disable": "stop"}[action])
		}
	case "allow", "deny":
		port, proto, ok := strings.Cut(portSpec, "/")
		if !ok {
			proto = protoField
		}
		first, last, convErr := nixwriter.ParseFirewallPorts(port)
		if convErr != nil {
			respondErrorSimple(w, convErr.Error(), http.StatusBadRequest)
			return
		}
		protos := []string{proto}
		if proto == "both" {
			protos = []string{"tcp", "udp"}
		}
		var rules []nixwriter.FirewallRule
		for _, pr := range protos {
			rule, normErr := nixwriter.FirewallRule{Action: action, Proto: pr, Port: first, PortEnd: last, From: from}.Normalize()
			if normErr != nil {
				respondErrorSimple(w, normErr.Error(), http.StatusBadRequest)
				return
			}
			rules = append(rules, rule)
		}
		what = fmt.Sprintf("%s %s/%s from %s", action, rules[0].Ports(), proto, orAny(rules[0].From))
		for _, rule := range rules {
			if err = NixWriter.AddFirewallRule(rule); err != nil {
				break
			}
		}
	case "delete":
		var removed nixwriter.FirewallRule
		removed, err = NixWriter.RemoveFirewallRule(ruleNum)
		if err == nil || removed.Port != 0 {
			what = fmt.Sprintf("delete %s %s/%s from %s", removed.Action, removed.Ports(), removed.Proto, orAny(removed.From))
		}
	default:
		respondErrorSimple(w, "Invalid action", http.StatusBadRequest)
		return
	}
	if err != nil {
		audit.LogAction("firewall", user, "Failed: "+what+": "+err.Error(), false, time.Since(start))
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	audit.LogAction("firewall", user, what, true, time.Since(start))
	respondOK(w, map[string]any{"success": true, "output": what})
}

func orAny(from string) string {
	if from == "" {
		return "anywhere"
	}
	return from
}
