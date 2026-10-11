//go:build linux

package scsipr

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// SG_IO ioctl number for the Linux SCSI generic interface (works on
// /dev/sgN and on SCSI block devices).
const sgIO = 0x2285

const (
	scsiPRIN  = 0x5e // PERSISTENT RESERVE IN
	scsiPROUT = 0x5f // PERSISTENT RESERVE OUT

	prinReadKeys        = 0x00
	prinReadReservation = 0x01

	proutRegister          = 0x00
	proutReserve           = 0x01
	proutRelease           = 0x02
	proutPreempt           = 0x04
	proutPreemptAndAbort   = 0x05
	proutRegisterIgnoreKey = 0x06

	// typeWriteExclusiveRegistrantsOnly: every registered node may write, no
	// other node may. A node is shut out by removing its registration.
	typeWriteExclusiveRegistrantsOnly = 0x05
)

// sgIOHdr is struct sg_io_hdr from <scsi/sg.h>.
type sgIOHdr struct {
	interfaceID    int32
	dxferDirection int32
	cmdLen         uint8
	mxSbLen        uint8
	iovecCount     uint16
	dxferLen       uint32
	dxferp         unsafe.Pointer
	cmdp           unsafe.Pointer
	sbp            unsafe.Pointer
	timeout        uint32
	flags          uint32
	packID         int32
	usrPtr         unsafe.Pointer
	status         uint8
	maskedStatus   uint8
	msgStatus      uint8
	sbLenWr        uint8
	hostStatus     uint16
	driverStatus   uint16
	resid          int32
	duration       uint32
	info           uint32
}

const (
	sgDxferFromDev = -3
	sgDxferToDev   = -2

	// driverSense: the driver status only says that sense data is there.
	driverSense = 0x08
)

// command runs one SCSI command on the device. The kernel reads and writes
// the buffers through the pointers in the header, so they are pinned for
// the duration of the call (the Go runtime may otherwise move them).
func command(device, op string, cdb, data []byte, dir int32) ([]byte, error) {
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer f.Close()

	sense := make([]byte, 64)
	hdr := &sgIOHdr{
		interfaceID:    int32('S'),
		dxferDirection: dir,
		cmdLen:         uint8(len(cdb)),
		mxSbLen:        uint8(len(sense)),
		dxferLen:       uint32(len(data)),
		timeout:        10000, // milliseconds
	}
	var pin runtime.Pinner
	defer pin.Unpin()
	pin.Pin(&cdb[0])
	pin.Pin(&sense[0])
	hdr.cmdp = unsafe.Pointer(&cdb[0])
	hdr.sbp = unsafe.Pointer(&sense[0])
	if len(data) > 0 {
		pin.Pin(&data[0])
		hdr.dxferp = unsafe.Pointer(&data[0])
	}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), sgIO, uintptr(unsafe.Pointer(hdr)))
	runtime.KeepAlive(hdr)
	if errno != 0 {
		if errno == unix.ENOTTY || errno == unix.EINVAL || errno == unix.ENOSYS {
			return nil, fmt.Errorf("%s on %s: %w", op, device, ErrNotSCSI)
		}
		return nil, fmt.Errorf("%s on %s: %w", op, device, errno)
	}
	if hdr.status != 0 || hdr.hostStatus != 0 || hdr.driverStatus&^driverSense != 0 {
		e := &SCSIError{Op: op + " on " + device, Status: int(hdr.status), Host: int(hdr.hostStatus), Driver: int(hdr.driverStatus)}
		if n := int(hdr.sbLenWr); n > 0 && n <= len(sense) {
			e.Key, e.ASC, e.ASCQ = parseSense(sense[:n])
		}
		return nil, e
	}
	if n := len(data) - int(hdr.resid); dir == sgDxferFromDev && n >= 0 && n <= len(data) {
		return data[:n], nil
	}
	return data, nil
}

// prout sends PERSISTENT RESERVE OUT with the given service action.
func prout(device, op string, action, typ byte, key, saKey RegistrationKey, aptpl bool) error {
	param := make([]byte, 24)
	copy(param[0:8], key[:])
	copy(param[8:16], saKey[:])
	if aptpl {
		param[20] = 0x01 // keep the registration through a power loss of the disk
	}
	cdb := []byte{scsiPROUT, action & 0x1f, typ & 0x0f, 0, 0, 0, 0, 0, byte(len(param)), 0}
	_, err := command(device, op, cdb, param, sgDxferToDev)
	return err
}

// Register registers key from this node, whatever key (if any) this node
// had registered before. The registration is kept through a power loss of
// the disk where the disk can do that.
func Register(device string, key RegistrationKey) error {
	err := prout(device, "register", proutRegisterIgnoreKey, 0, RegistrationKey{}, key, true)
	if err != nil && IsUnsupported(err) {
		// Some disks have reservations but cannot keep them through a power
		// loss; registering without that is still worth having.
		if err2 := prout(device, "register", proutRegisterIgnoreKey, 0, RegistrationKey{}, key, false); err2 == nil {
			return nil
		}
	}
	return err
}

// Unregister removes this node's registration (and with it a reservation it
// holds alone).
func Unregister(device string, key RegistrationKey) error {
	return prout(device, "unregister", proutRegister, 0, key, RegistrationKey{}, false)
}

// Reserve takes the reservation for the registered nodes.
func Reserve(device string, key RegistrationKey) error {
	return prout(device, "reserve", proutReserve, typeWriteExclusiveRegistrantsOnly, key, RegistrationKey{}, false)
}

// Release gives the reservation up; registrations stay.
func Release(device string, key RegistrationKey) error {
	return prout(device, "release", proutRelease, typeWriteExclusiveRegistrantsOnly, key, RegistrationKey{}, false)
}

// Preempt removes the registration of victim and, if victim held the
// reservation, takes it. Commands of the victim that are still queued in
// the disk are aborted where the disk supports that.
func Preempt(device string, ourKey, victim RegistrationKey) error {
	err := prout(device, "preempt", proutPreemptAndAbort, typeWriteExclusiveRegistrantsOnly, ourKey, victim, false)
	if err != nil && IsUnsupported(err) {
		return prout(device, "preempt", proutPreempt, typeWriteExclusiveRegistrantsOnly, ourKey, victim, false)
	}
	return err
}

// ReadKeys returns the registered keys and the reservation of the device.
func ReadKeys(device string) (PRStatus, error) {
	var st PRStatus
	buf := make([]byte, 4096)
	cdb := []byte{scsiPRIN, prinReadKeys, 0, 0, 0, 0, 0, byte(len(buf) >> 8), byte(len(buf)), 0}
	data, err := command(device, "read keys", cdb, buf, sgDxferFromDev)
	if err != nil {
		return st, err
	}
	st.Keys = parseKeys(data)

	buf = make([]byte, 256)
	cdb = []byte{scsiPRIN, prinReadReservation, 0, 0, 0, 0, 0, byte(len(buf) >> 8), byte(len(buf)), 0}
	data, err = command(device, "read reservation", cdb, buf, sgDxferFromDev)
	if err != nil {
		return st, err
	}
	st.Reserved, st.HolderKey = parseReservation(data)
	return st, nil
}

// SupportsReservations checks that a device really has persistent
// reservations. A device that already has registered keys does. Otherwise a
// test key is registered, looked for, and removed again: reading keys alone
// proves nothing, bridges answer that and then refuse to register.
func SupportsReservations(device string) error {
	st, err := ReadKeys(device)
	if err != nil {
		return err
	}
	if len(st.Keys) > 0 {
		return nil // in use
	}
	testKey := RegistrationKey{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0x00, 0x01}
	if err := prout(device, "register", proutRegisterIgnoreKey, 0, RegistrationKey{}, testKey, false); err != nil {
		return err
	}
	st, err = ReadKeys(device)
	unregErr := Unregister(device, testKey)
	switch {
	case err != nil:
		return err
	case !st.Has(testKey):
		return errors.New("the device accepted a key but does not list it: its reservations cannot be relied on")
	case unregErr != nil:
		return fmt.Errorf("the test key could not be removed again: %w", unregErr)
	}
	return nil
}
