package uevent

import "testing"

func TestParse(t *testing.T) {
	msg := []byte("add@/devices/pci0000:00/0000:00:1f.2/ata2/host1/target1:0:0/1:0:0:0/block/sdb\x00" +
		"ACTION=add\x00DEVPATH=/devices/pci0000:00/0000:00:1f.2/ata2/host1/target1:0:0/1:0:0:0/block/sdb\x00" +
		"SUBSYSTEM=block\x00MAJOR=8\x00MINOR=16\x00DEVNAME=sdb\x00DEVTYPE=disk\x00DISKSEQ=12\x00SEQNUM=4321\x00")
	ev, ok := Parse(msg)
	if !ok || ev.Action != "add" || ev.Subsystem != "block" || ev.DevType != "disk" || ev.DevName != "sdb" || ev.Env["SEQNUM"] != "4321" {
		t.Fatalf("kernel event: %+v %v", ev, ok)
	}
	for name, bad := range map[string][]byte{
		"udev's own message": []byte("libudev\x00\xfe\xed\xca\xfe\x00\x00\x00\x00ACTION=add\x00DEVPATH=/x\x00"),
		"no header":          []byte("ACTION=add\x00DEVPATH=/devices/x\x00"),
		"header only":        []byte("add@/devices/x\x00"),
		"empty":              {},
		"no action":          []byte("add@/devices/x\x00DEVPATH=/devices/x\x00"),
	} {
		if _, ok := Parse(bad); ok {
			t.Errorf("%s must not parse", name)
		}
	}
}
