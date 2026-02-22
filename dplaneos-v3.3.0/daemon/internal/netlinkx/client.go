// Package netlinkx provides a minimal Linux netlink/rtnetlink client.
//
// Why not vishvananda/netlink?
//   vishvananda/netlink requires golang.org/x/sys, which in turn adds CGO
//   build constraints and a large external dependency. For the ~15 ip(8)
//   calls D-PlaneOS makes (link add/del/set, addr add/replace, route replace),
//   raw rtnetlink via the stdlib syscall package is sufficient and keeps the
//   daemon dependency-free.
//
// Supported operations (covers all current ip(8) exec.Command calls):
//   - LinkList()                          → ip link show
//   - LinkSetUp(name)                     → ip link set NAME up
//   - LinkSetDown(name)                   → ip link set NAME down
//   - LinkSetMaster(slave, master)        → ip link set SLAVE master MASTER
//   - LinkAdd(LinkAttrs)                  → ip link add ... type vlan/bond
//   - LinkDel(name)                       → ip link delete NAME
//   - AddrAdd(iface, cidr)                → ip addr add CIDR dev IFACE
//   - AddrReplace(iface, cidr)            → ip addr replace CIDR dev IFACE
//   - AddrList(iface)                     → ip addr show [IFACE]
//   - RouteReplace(dst, gw, iface)        → ip route replace DST via GW dev IFACE
//   - RouteList()                         → ip route show
//
// Linux kernel minimum: 3.0 (rtnetlink stable API). All supported distros qualify.
package netlinkx

import (
	"encoding/binary"
	"os"
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

// ─────────────────────────────────────────────
//  Constants not exposed in stdlib syscall
// ─────────────────────────────────────────────

const (
	// Attribute types for IFLA_INFO_KIND
	IFLA_INFO_KIND = 1
	IFLA_INFO_DATA = 2
	IFLA_LINKINFO  = 18

	// VLAN attributes (nested under IFLA_INFO_DATA)
	IFLA_VLAN_ID = 1

	// Bond attributes
	IFLA_BOND_MODE = 1

	// Bond modes
	bondModeBalanceRR  = 0
	bondModeActiveBackup = 1
	bondModeBalanceXOR = 2
	bondModeBroadcast  = 3
	bondMode8023AD     = 4
	bondModeBalanceTLB = 5
	bondModeBalanceALB = 6

	// RTM flags
	rtmFlagReplace = 0x100 // NLM_F_REPLACE
	rtmFlagCreate  = 0x400 // NLM_F_CREATE
	rtmFlagAppend  = 0x800 // NLM_F_APPEND

	// Route table/protocol
	rtTableMain = 254
	rtProtoStatic = 4
	rtScopeLink   = 253
	rtScopeUniverse = 0
	rtTypeUnicast = 1

	// Address flags
	ifaFlagPermanent = 0x80
)

// ─────────────────────────────────────────────
//  Link types
// ─────────────────────────────────────────────

// LinkType identifies what kind of virtual link to create.
type LinkType int

const (
	LinkTypeVLAN LinkType = iota
	LinkTypeBond
)

// BondMode maps human-readable bond mode names to kernel integers.
var BondModes = map[string]int{
	"balance-rr":    bondModeBalanceRR,
	"active-backup": bondModeActiveBackup,
	"balance-xor":   bondModeBalanceXOR,
	"broadcast":     bondModeBroadcast,
	"802.3ad":       bondMode8023AD,
	"balance-tlb":   bondModeBalanceTLB,
	"balance-alb":   bondModeBalanceALB,
}

// LinkAttrs describes a link to create.
type LinkAttrs struct {
	Name       string   // interface name e.g. "eth0.100", "bond0"
	Type       LinkType // VLAN or Bond
	ParentName string   // parent interface for VLAN e.g. "eth0"
	VLANID     int      // 1–4094, for VLAN only
	BondMode   string   // e.g. "802.3ad", for Bond only
}

// LinkInfo is returned by LinkList.
type LinkInfo struct {
	Index int
	Name  string
	Flags net.Flags
	MTU   int
	Type  string // "ether", "loopback", "vlan", "bond", etc.
}

// AddrInfo is returned by AddrList.
type AddrInfo struct {
	IP    net.IP
	CIDR  *net.IPNet
	Label string
}

// RouteInfo is returned by RouteList.
type RouteInfo struct {
	Dst     *net.IPNet
	Gateway net.IP
	Iface   string
}

// ─────────────────────────────────────────────
//  Netlink socket helpers
// ─────────────────────────────────────────────

func nlSocket() (int, error) {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_ROUTE)
	if err != nil {
		return 0, fmt.Errorf("netlink socket: %w", err)
	}
	lsa := &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}
	if err := syscall.Bind(fd, lsa); err != nil {
		syscall.Close(fd)
		return 0, fmt.Errorf("netlink bind: %w", err)
	}
	return fd, nil
}

// nlAttr builds a netlink attribute header + data, padded to 4-byte alignment.
func nlAttr(typ uint16, data []byte) []byte {
	length := 4 + len(data)
	padded := (length + 3) &^ 3
	buf := make([]byte, padded)
	binary.LittleEndian.PutUint16(buf[0:], uint16(length))
	binary.LittleEndian.PutUint16(buf[2:], typ)
	copy(buf[4:], data)
	return buf
}

func nlAttrStr(typ uint16, s string) []byte  { return nlAttr(typ, append([]byte(s), 0)) }
func nlAttrU16(typ uint16, v uint16) []byte  { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return nlAttr(typ, b) }
func nlAttrU32(typ uint16, v uint32) []byte  { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return nlAttr(typ, b) }
func nlAttrNested(typ uint16, inner []byte) []byte { return nlAttr(typ|0x8000, inner) } // NLA_F_NESTED

// sendrecv sends a netlink request and returns all response messages.
func sendrecv(fd int, msgType uint16, flags uint16, family uint8, payload []byte) ([]syscall.NetlinkMessage, error) {
	seq := uint32(1)
	msg := make([]byte, syscall.NLMSG_HDRLEN+len(payload))
	hdr := (*syscall.NlMsghdr)(unsafe.Pointer(&msg[0]))
	hdr.Len = uint32(len(msg))
	hdr.Type = msgType
	hdr.Flags = flags | syscall.NLM_F_REQUEST
	hdr.Seq = seq
	copy(msg[syscall.NLMSG_HDRLEN:], payload)

	dst := &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}
	if err := syscall.Sendto(fd, msg, 0, dst); err != nil {
		return nil, fmt.Errorf("netlink send: %w", err)
	}

	var msgs []syscall.NetlinkMessage
	buf := make([]byte, 65536)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return nil, fmt.Errorf("netlink recv: %w", err)
		}
		parsed, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return nil, fmt.Errorf("netlink parse: %w", err)
		}
		for _, m := range parsed {
			if m.Header.Type == syscall.NLMSG_DONE {
				return msgs, nil
			}
			if m.Header.Type == syscall.NLMSG_ERROR {
				if len(m.Data) < 4 {
					return nil, fmt.Errorf("netlink: NLMSG_ERROR with truncated payload (%d bytes)", len(m.Data))
				}
				e := (*syscall.NlMsgerr)(unsafe.Pointer(&m.Data[0]))
				if e.Error == 0 {
					return msgs, nil // ACK
				}
				return nil, fmt.Errorf("netlink error: %w", syscall.Errno(-e.Error))
			}
			msgs = append(msgs, m)
		}
		// If NLM_F_DUMP, keep reading; otherwise stop after first batch
		if flags&syscall.NLM_F_DUMP == 0 {
			return msgs, nil
		}
	}
}

// ifIndexByName returns the kernel interface index for a named interface.
func ifIndexByName(name string) (int, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return 0, fmt.Errorf("interface %q not found: %w", name, err)
	}
	return iface.Index, nil
}

// ─────────────────────────────────────────────
//  Link operations
// ─────────────────────────────────────────────

// LinkList returns all network interfaces.
func LinkList() ([]LinkInfo, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("link list: %w", err)
	}
	result := make([]LinkInfo, 0, len(ifaces))
	for _, i := range ifaces {
		result = append(result, LinkInfo{
			Index: i.Index,
			Name:  i.Name,
			Flags: i.Flags,
			MTU:   i.MTU,
		})
	}
	return result, nil
}

// linkSetFlags sets or clears interface flags via RTM_NEWLINK.
func linkSetFlags(name string, flagsSet, flagsClear uint32) error {
	idx, err := ifIndexByName(name)
	if err != nil {
		return err
	}
	fd, err := nlSocket()
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	// ifi_msg: family(1) + pad(1) + type(2) + index(4) + flags(4) + change(4)
	payload := make([]byte, 16)
	binary.LittleEndian.PutUint32(payload[4:], uint32(idx))
	binary.LittleEndian.PutUint32(payload[8:], flagsSet)
	binary.LittleEndian.PutUint32(payload[12:], flagsSet|flagsClear) // change mask

	_, err = sendrecv(fd, syscall.RTM_NEWLINK, syscall.NLM_F_ACK, 0, payload)
	return err
}

// LinkSetUp brings an interface up (ip link set NAME up).
func LinkSetUp(name string) error {
	return linkSetFlags(name, syscall.IFF_UP, 0)
}

// LinkSetDown brings an interface down (ip link set NAME down).
func LinkSetDown(name string) error {
	return linkSetFlags(name, 0, syscall.IFF_UP)
}

// LinkSetMaster sets the master (bond) interface for a slave (ip link set SLAVE master MASTER).
func LinkSetMaster(slaveName, masterName string) error {
	slaveIdx, err := ifIndexByName(slaveName)
	if err != nil {
		return err
	}
	masterIdx, err := ifIndexByName(masterName)
	if err != nil {
		return err
	}

	fd, err := nlSocket()
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	payload := make([]byte, 16)
	binary.LittleEndian.PutUint32(payload[4:], uint32(slaveIdx))
	payload = append(payload, nlAttrU32(syscall.IFLA_MASTER, uint32(masterIdx))...)

	_, err = sendrecv(fd, syscall.RTM_NEWLINK, syscall.NLM_F_ACK, 0, payload)
	return err
}

// LinkAdd creates a virtual link (ip link add ... type vlan|bond).
func LinkAdd(attrs LinkAttrs) error {
	if attrs.Name == "" {
		return fmt.Errorf("link name is required")
	}

	fd, err := nlSocket()
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	// ifi_msg header (16 bytes): family=AF_UNSPEC, type=0, index=0, flags=0, change=0
	header := make([]byte, 16)

	var payload []byte
	payload = append(payload, header...)
	payload = append(payload, nlAttrStr(syscall.IFLA_IFNAME, attrs.Name)...)

	switch attrs.Type {
	case LinkTypeVLAN:
		if attrs.ParentName == "" {
			return fmt.Errorf("parent interface required for VLAN")
		}
		if attrs.VLANID < 1 || attrs.VLANID > 4094 {
			return fmt.Errorf("VLAN ID must be 1–4094")
		}
		parentIdx, err := ifIndexByName(attrs.ParentName)
		if err != nil {
			return err
		}
		payload = append(payload, nlAttrU32(syscall.IFLA_LINK, uint32(parentIdx))...)

		vlanData := nlAttrU16(IFLA_VLAN_ID, uint16(attrs.VLANID))
		linkInfo := nlAttrStr(IFLA_INFO_KIND, "vlan")
		linkInfo = append(linkInfo, nlAttrNested(IFLA_INFO_DATA, vlanData)...)
		payload = append(payload, nlAttrNested(IFLA_LINKINFO, linkInfo)...)

	case LinkTypeBond:
		mode, ok := BondModes[attrs.BondMode]
		if !ok {
			return fmt.Errorf("unknown bond mode %q", attrs.BondMode)
		}
		bondData := nlAttrU8(IFLA_BOND_MODE, uint8(mode))
		linkInfo := nlAttrStr(IFLA_INFO_KIND, "bond")
		linkInfo = append(linkInfo, nlAttrNested(IFLA_INFO_DATA, bondData)...)
		payload = append(payload, nlAttrNested(IFLA_LINKINFO, linkInfo)...)

	default:
		return fmt.Errorf("unsupported link type")
	}

	flags := uint16(syscall.NLM_F_ACK | rtmFlagCreate)
	_, err = sendrecv(fd, syscall.RTM_NEWLINK, flags, 0, payload)
	return err
}

// LinkDel deletes a link (ip link delete NAME).
func LinkDel(name string) error {
	idx, err := ifIndexByName(name)
	if err != nil {
		return err
	}

	fd, err := nlSocket()
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	payload := make([]byte, 16)
	binary.LittleEndian.PutUint32(payload[4:], uint32(idx))

	_, err = sendrecv(fd, syscall.RTM_DELLINK, syscall.NLM_F_ACK, 0, payload)
	return err
}

// nlAttrU8 builds a netlink attribute for a uint8 value.
func nlAttrU8(typ uint16, v uint8) []byte { return nlAttr(typ, []byte{v}) }

// ─────────────────────────────────────────────
//  Address operations
// ─────────────────────────────────────────────

// addrOp performs RTM_NEWADDR with given flags (create, replace, etc.)
func addrOp(ifaceName, cidr string, nlmFlags uint16) error {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
	}
	ip = ip.To4()
	if ip == nil {
		return fmt.Errorf("only IPv4 is supported")
	}

	idx, err := ifIndexByName(ifaceName)
	if err != nil {
		return err
	}

	ones, _ := ipnet.Mask.Size()

	fd, err := nlSocket()
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	// ifa_msg: family(1) + prefixlen(1) + flags(1) + scope(1) + index(4)
	header := []byte{
		syscall.AF_INET,          // family
		byte(ones),               // prefixlen
		ifaFlagPermanent,         // flags
		rtScopeUniverse,          // scope
		0, 0, 0, 0,               // index (4 bytes LE)
	}
	binary.LittleEndian.PutUint32(header[4:], uint32(idx))

	payload := header
	payload = append(payload, nlAttr(syscall.IFA_LOCAL, ip)...)
	payload = append(payload, nlAttr(syscall.IFA_ADDRESS, ipnet.IP.To4())...)

	_, err = sendrecv(fd, syscall.RTM_NEWADDR, nlmFlags|syscall.NLM_F_ACK, syscall.AF_INET, payload)
	return err
}

// AddrAdd adds an IP address to an interface (ip addr add CIDR dev IFACE).
func AddrAdd(ifaceName, cidr string) error {
	return addrOp(ifaceName, cidr, rtmFlagCreate)
}

// AddrReplace replaces the IP address on an interface (ip addr replace CIDR dev IFACE).
// Semantics match ip(8): removes all existing IPv4 addresses on the interface,
// then assigns the new address. Uses RTM_DELADDR + RTM_NEWADDR.
func AddrReplace(ifaceName, cidr string) error {
	// Remove existing addresses on this interface first
	existingAddrs, _ := AddrList(ifaceName)
	for _, a := range existingAddrs {
		if a.IP.To4() != nil {
			_ = addrDel(ifaceName, a.CIDR.String()) // best-effort; don't fail if already gone
		}
	}
	return addrOp(ifaceName, cidr, rtmFlagCreate)
}

// addrDel removes an IP address from an interface via RTM_DELADDR.
func addrDel(ifaceName, cidr string) error {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}
	ip = ip.To4()
	if ip == nil {
		return nil // IPv6 not supported, skip
	}
	idx, err := ifIndexByName(ifaceName)
	if err != nil {
		return err
	}
	ones, _ := ipnet.Mask.Size()
	fd, err := nlSocket()
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	header := []byte{
		syscall.AF_INET, byte(ones), 0, rtScopeUniverse,
		0, 0, 0, 0, // index
	}
	binary.LittleEndian.PutUint32(header[4:], uint32(idx))
	payload := header
	payload = append(payload, nlAttr(syscall.IFA_LOCAL, ip)...)
	_, err = sendrecv(fd, syscall.RTM_DELADDR, syscall.NLM_F_ACK, syscall.AF_INET, payload)
	return err
}

// AddrList returns the addresses assigned to an interface.
// Uses stdlib net.InterfaceByName which reads /proc/net — no syscall needed.
func AddrList(ifaceName string) ([]AddrInfo, error) {
	var ifaces []net.Interface
	if ifaceName != "" {
		iface, err := net.InterfaceByName(ifaceName)
		if err != nil {
			return nil, fmt.Errorf("interface %q not found: %w", ifaceName, err)
		}
		ifaces = []net.Interface{*iface}
	} else {
		var err error
		ifaces, err = net.Interfaces()
		if err != nil {
			return nil, fmt.Errorf("list interfaces: %w", err)
		}
	}

	var result []AddrInfo
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			switch v := a.(type) {
			case *net.IPNet:
				result = append(result, AddrInfo{IP: v.IP, CIDR: v})
			}
		}
	}
	return result, nil
}

// ─────────────────────────────────────────────
//  Route operations
// ─────────────────────────────────────────────

// RouteReplace replaces or adds a route (ip route replace DST via GW dev IFACE).
// Pass dst="" for the default route (0.0.0.0/0).
func RouteReplace(dst, gateway, ifaceName string) error {
	var dstIP net.IP
	var dstNet *net.IPNet

	if dst == "" || dst == "default" {
		dstIP = net.IPv4(0, 0, 0, 0).To4()
		_, dstNet, _ = net.ParseCIDR("0.0.0.0/0")
	} else {
		var err error
		dstIP, dstNet, err = net.ParseCIDR(dst)
		if err != nil {
			return fmt.Errorf("invalid destination %q: %w", dst, err)
		}
		dstIP = dstIP.To4()
	}
	_ = dstNet

	gwIP := net.ParseIP(gateway).To4()
	if gwIP == nil {
		return fmt.Errorf("invalid gateway %q", gateway)
	}

	idx, err := ifIndexByName(ifaceName)
	if err != nil {
		return err
	}

	ones, _ := dstNet.Mask.Size()

	fd, err := nlSocket()
	if err != nil {
		return err
	}
	defer syscall.Close(fd)

	// rtmsg: family(1) + dst_len(1) + src_len(1) + tos(1) + table(1) + protocol(1) + scope(1) + type(1) + flags(4)
	header := []byte{
		syscall.AF_INET,   // family
		byte(ones),        // dst_len
		0,                 // src_len
		0,                 // tos
		rtTableMain,       // table
		rtProtoStatic,     // protocol
		rtScopeUniverse,   // scope
		rtTypeUnicast,     // type
		0, 0, 0, 0,        // flags
	}

	payload := header
	payload = append(payload, nlAttr(syscall.RTA_DST, dstIP)...)
	payload = append(payload, nlAttr(syscall.RTA_GATEWAY, gwIP)...)
	payload = append(payload, nlAttrU32(syscall.RTA_OIF, uint32(idx))...)

	flags := uint16(syscall.NLM_F_ACK | rtmFlagCreate | rtmFlagReplace)
	_, err = sendrecv(fd, syscall.RTM_NEWROUTE, flags, syscall.AF_INET, payload)
	return err
}

// RouteList returns the current IPv4 routing table by reading /proc/net/route.
// Format: Iface, Destination, Gateway (all hex, host byte order).
func RouteList() ([]RouteInfo, error) {
	data, err := readFile("/proc/net/route")
	if err != nil {
		return nil, fmt.Errorf("route list: %w", err)
	}

	var routes []RouteInfo
	lines := splitLines(data)
	for i, line := range lines {
		if i == 0 || line == "" {
			continue // skip header
		}
		fields := splitFields(line)
		if len(fields) < 8 {
			continue
		}
		iface := fields[0]
		dst := hexToIP(fields[1])
		gw := hexToIP(fields[2])
		mask := hexToMask(fields[7])

		_, dstNet, err := net.ParseCIDR(dst.String() + "/" + fmt.Sprintf("%d", maskBits(mask)))
		if err != nil {
			continue
		}
		routes = append(routes, RouteInfo{Dst: dstNet, Gateway: gw, Iface: iface})
	}
	return routes, nil
}

// ─────────────────────────────────────────────
//  Proc helpers for route reading
// ─────────────────────────────────────────────

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitFields(s string) []string {
	var fields []string
	inField := false
	start := 0
	for i, c := range s {
		if c == ' ' || c == '\t' {
			if inField {
				fields = append(fields, s[start:i])
				inField = false
			}
		} else {
			if !inField {
				start = i
				inField = true
			}
		}
	}
	if inField {
		fields = append(fields, s[start:])
	}
	return fields
}

// hexToIP converts /proc/net/route hex (little-endian) to net.IP.
func hexToIP(hex string) net.IP {
	if len(hex) != 8 {
		return net.IPv4(0, 0, 0, 0)
	}
	var v uint32
	fmt.Sscanf(hex, "%X", &v)
	return net.IP{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

func hexToMask(hex string) net.IPMask {
	if len(hex) != 8 {
		return net.IPMask{0, 0, 0, 0}
	}
	var v uint32
	fmt.Sscanf(hex, "%X", &v)
	return net.IPMask{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

func maskBits(mask net.IPMask) int {
	ones, _ := mask.Size()
	return ones
}
