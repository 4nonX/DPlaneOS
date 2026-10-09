package groups

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"time"

	"dplaned/internal/cmdutil"
)

// Floating address (Design 0001 phase 3e).
//
// A group may have an address that clients use for its shares and exports.
// It is on the owner's interface while the owner serves the group, and moves
// with the group (planned move, failover, takeover); this replaces the
// keepalived VIP of the Patroni-based HA setup.

var ifaceRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,15}$`)

func validAddress(cidr, iface string) error {
	if cidr == "" && iface == "" {
		return nil
	}
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("address %q: use the form 192.168.1.50/24", cidr)
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("address %q cannot be used as a floating address", cidr)
	}
	if !ifaceRe.MatchString(iface) {
		return fmt.Errorf("interface %q is not a valid interface name", iface)
	}
	return nil
}

// AddrOps manage the floating address; replaced in tests.
type AddrOps struct {
	Present  func(iface, cidr string) (bool, error)
	Add      func(iface, cidr string) error
	Del      func(iface, cidr string) error
	Announce func(iface, cidr string) error
}

// DefaultAddrOps use ip(8) and arping(8).
var DefaultAddrOps = AddrOps{
	Present: func(iface, cidr string) (bool, error) {
		want, _, err := net.ParseCIDR(cidr)
		if err != nil {
			return false, err
		}
		ifc, err := net.InterfaceByName(iface)
		if err != nil {
			return false, fmt.Errorf("interface %s: %w", iface, err)
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			return false, err
		}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(want) {
				return true, nil
			}
		}
		return false, nil
	},
	Add: func(iface, cidr string) error {
		if out, err := cmdutil.RunFast("group_address", "addr", "add", cidr, "dev", iface); err != nil {
			return fmt.Errorf("ip addr add %s dev %s: %v: %s", cidr, iface, err, bytes.TrimSpace(out))
		}
		return nil
	},
	Del: func(iface, cidr string) error {
		if out, err := cmdutil.RunFast("group_address", "addr", "del", cidr, "dev", iface); err != nil {
			return fmt.Errorf("ip addr del %s dev %s: %v: %s", cidr, iface, err, bytes.TrimSpace(out))
		}
		return nil
	},
	// Announce tells the network the address moved (gratuitous ARP), so
	// clients and switches do not wait for their ARP caches to expire.
	Announce: func(iface, cidr string) error {
		ip, _, _ := net.ParseCIDR(cidr)
		if ip.To4() == nil {
			return nil // IPv6: the kernel sends unsolicited neighbour advertisements
		}
		if out, err := cmdutil.RunMedium("group_arping", "-U", "-c", "3", "-I", iface, ip.String()); err != nil {
			return fmt.Errorf("arping %s: %v: %s", ip, err, bytes.TrimSpace(out))
		}
		return nil
	},
}

// holdAddress puts g's address on this node (owner) or takes it off.
func (m *Manager) holdAddress(g Group, hold bool) error {
	key := g.Interface + "|" + g.Address
	m.fmu.Lock()
	if m.addrApplied == nil {
		m.addrApplied = map[string]string{}
	}
	prev := m.addrApplied[g.Name]
	m.fmu.Unlock()
	var errs []error
	// The address changed: the old one goes.
	if prev != "" && prev != key {
		var piface, pcidr string
		for i := range prev {
			if prev[i] == '|' {
				piface, pcidr = prev[:i], prev[i+1:]
				break
			}
		}
		if ok, _ := m.addr.Present(piface, pcidr); ok {
			if err := m.addr.Del(piface, pcidr); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if g.Address == "" {
		m.fmu.Lock()
		delete(m.addrApplied, g.Name)
		m.fmu.Unlock()
		return errors.Join(errs...)
	}
	present, err := m.addr.Present(g.Interface, g.Address)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	switch {
	case hold && !present:
		if err := m.addr.Add(g.Interface, g.Address); err != nil {
			return errors.Join(append(errs, err)...)
		}
		log.Printf("GROUPS: %s: floating address %s is now on %s", g.Name, g.Address, g.Interface)
		if err := m.addr.Announce(g.Interface, g.Address); err != nil {
			errs = append(errs, err)
		}
	case !hold && present:
		if err := m.addr.Del(g.Interface, g.Address); err != nil {
			return errors.Join(append(errs, err)...)
		}
		log.Printf("GROUPS: %s: floating address %s removed from %s", g.Name, g.Address, g.Interface)
	}
	m.fmu.Lock()
	if hold {
		m.addrApplied[g.Name] = key
	} else {
		delete(m.addrApplied, g.Name)
	}
	m.fmu.Unlock()
	return errors.Join(errs...)
}

// SetAddress changes a group's floating address (on the owner).
func (m *Manager) SetAddress(name, cidr, iface string) (*Group, error) {
	if err := validAddress(cidr, iface); err != nil {
		return nil, err
	}
	m.mu.Lock()
	g, err := Get(m.db, name)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if g.Owner != m.self() {
		m.mu.Unlock()
		return nil, fmt.Errorf("change the address on the owner of %s", name)
	}
	ng := *g
	ng.Address, ng.Interface = cidr, iface
	ng.Version++
	ng.UpdatedAt, ng.UpdatedBy = time.Now(), m.self()
	if err := put(m.db, ng); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	m.mu.Unlock()
	for _, e := range m.pushAll(ng) {
		log.Printf("GROUPS: %s: %v (retried by pulling)", name, e)
	}
	go m.ActivateTick()
	return Get(m.db, name)
}
