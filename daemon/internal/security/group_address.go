package security

import (
	"fmt"
	"net"
	"regexp"
)

var groupIfaceRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,15}$`)

// validateGroupAddress: addr add|del <cidr> dev <iface> (a storage group's
// floating address).
func validateGroupAddress(args []string) error {
	if len(args) != 5 || args[0] != "addr" || (args[1] != "add" && args[1] != "del") || args[3] != "dev" {
		return fmt.Errorf("group_address must be: addr add|del <cidr> dev <interface>")
	}
	if _, _, err := net.ParseCIDR(args[2]); err != nil {
		return fmt.Errorf("group_address: invalid address %q", args[2])
	}
	if !groupIfaceRe.MatchString(args[4]) {
		return fmt.Errorf("group_address: invalid interface %q", args[4])
	}
	return nil
}

// validateGroupArping: -U -c 3 -I <iface> <ipv4> (gratuitous ARP).
func validateGroupArping(args []string) error {
	if len(args) != 6 || args[0] != "-U" || args[1] != "-c" || args[2] != "3" || args[3] != "-I" {
		return fmt.Errorf("group_arping must be: -U -c 3 -I <interface> <ipv4>")
	}
	if !groupIfaceRe.MatchString(args[4]) {
		return fmt.Errorf("group_arping: invalid interface %q", args[4])
	}
	if ip := net.ParseIP(args[5]); ip == nil || ip.To4() == nil {
		return fmt.Errorf("group_arping: invalid IPv4 address %q", args[5])
	}
	return nil
}
