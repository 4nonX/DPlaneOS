//go:build !linux

package netlinkx

import (
	"fmt"
	"net"
)

// LinkType identifies what kind of virtual link to create.
type LinkType int

const (
	LinkTypeVLAN LinkType = iota
	LinkTypeBond
)

var BondModes = map[string]int{
	"balance-rr":    0,
	"active-backup": 1,
}

type LinkAttrs struct {
	Name       string
	Type       LinkType
	ParentName string
	VLANID     int
	BondMode   string
}

type LinkInfo struct {
	Index int
	Name  string
	Flags net.Flags
	MTU   int
	Type  string
}

type AddrInfo struct {
	IP    net.IP
	CIDR  *net.IPNet
	Label string
}

type RouteInfo struct {
	Dst     *net.IPNet
	Gateway net.IP
	Iface   string
}

func LinkList() ([]LinkInfo, error) {
	return nil, fmt.Errorf("netlink not supported on this platform")
}

func LinkSetUp(name string) error {
	return fmt.Errorf("netlink not supported on this platform")
}

func LinkSetDown(name string) error {
	return fmt.Errorf("netlink not supported on this platform")
}

func LinkSetMaster(slaveName, masterName string) error {
	return fmt.Errorf("netlink not supported on this platform")
}

func LinkAdd(attrs LinkAttrs) error {
	return fmt.Errorf("netlink not supported on this platform")
}

func LinkDel(name string) error {
	return fmt.Errorf("netlink not supported on this platform")
}

func AddrAdd(ifaceName, cidr string) error {
	return fmt.Errorf("netlink not supported on this platform")
}

func AddrReplace(ifaceName, cidr string) error {
	return fmt.Errorf("netlink not supported on this platform")
}

func AddrList(ifaceName string) ([]AddrInfo, error) {
	return nil, fmt.Errorf("netlink not supported on this platform")
}

func RouteReplace(dst, gateway, ifaceName string) error {
	return fmt.Errorf("netlink not supported on this platform")
}

func RouteList() ([]RouteInfo, error) {
	return nil, fmt.Errorf("netlink not supported on this platform")
}
