package nixwriter

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// FirewallRule is one rule set in the web interface. Deny rules take
// precedence over allow rules, whatever the order they were added in;
// everything no rule allows (and no system service needs) is refused.
type FirewallRule struct {
	Action string `json:"action"` // "allow" or "deny"
	Proto  string `json:"proto"`  // "tcp" or "udp"
	Port   int    `json:"port"`   // 1-65535
	// PortEnd, when set, makes the rule cover the range Port to PortEnd.
	PortEnd int    `json:"port_end,omitempty"`
	From    string `json:"from,omitempty"` // source address or network; "" for any
}

// Normalize validates the rule and puts it in its canonical form.
func (r FirewallRule) Normalize() (FirewallRule, error) {
	if r.Action != "allow" && r.Action != "deny" {
		return r, fmt.Errorf("action must be allow or deny, not %q", r.Action)
	}
	if r.Proto == "" {
		r.Proto = "tcp"
	}
	if r.Proto != "tcp" && r.Proto != "udp" {
		return r, fmt.Errorf("protocol must be tcp or udp, not %q", r.Proto)
	}
	if r.Port < 1 || r.Port > 65535 {
		return r, fmt.Errorf("port %d is out of range 1-65535", r.Port)
	}
	if r.PortEnd == r.Port {
		r.PortEnd = 0
	}
	if r.PortEnd != 0 && (r.PortEnd < r.Port || r.PortEnd > 65535) {
		return r, fmt.Errorf("port range %d:%d is not valid", r.Port, r.PortEnd)
	}
	from := strings.TrimSpace(r.From)
	switch strings.ToLower(from) {
	case "", "any", "anywhere":
		r.From = ""
		return r, nil
	}
	if ip := net.ParseIP(from); ip != nil {
		r.From = ip.String()
		return r, nil
	}
	_, ipnet, err := net.ParseCIDR(from)
	if err != nil {
		return r, fmt.Errorf("source %q is neither an address nor a network (e.g. 192.168.1.0/24)", from)
	}
	r.From = ipnet.String()
	return r, nil
}

// FirewallList returns every rule: the stored rules and the ports open to
// everyone (FirewallTCP, FirewallUDP), deny rules first. The position in
// this list (from 1) is the rule number the web interface shows.
func (s DPlaneState) FirewallList() []FirewallRule {
	var deny, allow []FirewallRule
	for _, r := range s.FirewallRules {
		if r.Action == "deny" {
			deny = append(deny, r)
		} else {
			allow = append(allow, r)
		}
	}
	for _, p := range s.FirewallTCP {
		allow = append(allow, FirewallRule{Action: "allow", Proto: "tcp", Port: p})
	}
	for _, p := range s.FirewallUDP {
		allow = append(allow, FirewallRule{Action: "allow", Proto: "udp", Port: p})
	}
	return append(deny, allow...)
}

// setFirewallListLocked stores a list of rules: allow rules without source
// go to the port lists, everything else to FirewallRules. Duplicates are
// dropped.
func (w *Writer) setFirewallListLocked(rules []FirewallRule) {
	seen := map[FirewallRule]bool{}
	var tcp, udp []int
	var rest []FirewallRule
	for _, r := range rules {
		if seen[r] {
			continue
		}
		seen[r] = true
		switch {
		case r.Action == "allow" && r.From == "" && r.PortEnd == 0 && r.Proto == "tcp":
			tcp = append(tcp, r.Port)
		case r.Action == "allow" && r.From == "" && r.PortEnd == 0 && r.Proto == "udp":
			udp = append(udp, r.Port)
		default:
			rest = append(rest, r)
		}
	}
	sort.Ints(tcp)
	sort.Ints(udp)
	w.state.FirewallTCP, w.state.FirewallUDP, w.state.FirewallRules = tcp, udp, rest
}

// AddFirewallRule adds a rule (adding one that exists changes nothing).
func (w *Writer) AddFirewallRule(r FirewallRule) error {
	r, err := r.Normalize()
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.setFirewallListLocked(append(w.state.FirewallList(), r))
	return w.flushLocked()
}

// RemoveFirewallRule removes rule number n of FirewallList (from 1) and
// returns it.
func (w *Writer) RemoveFirewallRule(n int) (FirewallRule, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	list := w.state.FirewallList()
	if n < 1 || n > len(list) {
		return FirewallRule{}, fmt.Errorf("there is no rule number %d", n)
	}
	removed := list[n-1]
	w.setFirewallListLocked(append(list[:n-1:n-1], list[n:]...))
	return removed, w.flushLocked()
}

// SetFirewallRules replaces the rules that are not plain open ports
// (those are set with SetFirewallPorts).
func (w *Writer) SetFirewallRules(rules []FirewallRule) error {
	var norm []FirewallRule
	for _, r := range rules {
		n, err := r.Normalize()
		if err != nil {
			return err
		}
		norm = append(norm, n)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var list []FirewallRule
	list = append(list, norm...)
	for _, p := range w.state.FirewallTCP {
		list = append(list, FirewallRule{Action: "allow", Proto: "tcp", Port: p})
	}
	for _, p := range w.state.FirewallUDP {
		list = append(list, FirewallRule{Action: "allow", Proto: "udp", Port: p})
	}
	w.setFirewallListLocked(list)
	return w.flushLocked()
}

// SetFirewallDisabled switches the firewall off (true) or on.
func (w *Writer) SetFirewallDisabled(disabled bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.FirewallDisabled = disabled
	return w.flushLocked()
}

// Ports is the rule's port, or its range as "first:last".
func (r FirewallRule) Ports() string {
	if r.PortEnd != 0 {
		return fmt.Sprintf("%d:%d", r.Port, r.PortEnd)
	}
	return fmt.Sprint(r.Port)
}

// ParseFirewallPorts reads "80", "8000:8100" or "8000-8100".
func ParseFirewallPorts(s string) (first, last int, err error) {
	s = strings.TrimSpace(s)
	a, b, ranged := strings.Cut(strings.ReplaceAll(s, "-", ":"), ":")
	if _, err := fmt.Sscanf(a, "%d", &first); err != nil || fmt.Sprint(first) != a {
		return 0, 0, fmt.Errorf("port %q is not a number or a range like 8000:8100", s)
	}
	if ranged {
		if _, err := fmt.Sscanf(b, "%d", &last); err != nil || fmt.Sprint(last) != b {
			return 0, 0, fmt.Errorf("port %q is not a number or a range like 8000:8100", s)
		}
	}
	return first, last, nil
}
