//go:build linux

package scsipr

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// SG_IO ioctl number for the Linux SCSI generic driver.
const sgIO = 0x2285

// SCSI command codes
const (
	scsiPRIN  = 0x5e // PERSISTENT RESERVE IN
	scsiPROUT = 0x5f // PERSISTENT RESERVE OUT
)

// PRIN service actions
const (
	prinReadKeys        = 0x00
	prinReadReservation = 0x01
)

// PROUT service actions
const (
	proutRegister             = 0x00
	proutReserve              = 0x01
	proutRelease              = 0x02
	proutPreempt              = 0x04
	proutPreemptAndAbort      = 0x05
	proutRegisterAndIgnoreKey = 0x06
)

// sgIOHdr maps to struct sg_io_hdr from <scsi/sg.h>.
// Using fixed-size integers to match the C struct layout exactly.
type sgIOHdr struct {
	interfaceID    int32
	dxferDirection int32
	cmdLen         uint8
	mxSbLen        uint8
	iovecCount     uint16
	dxferLen       uint32
	dxferp         uintptr
	cmdp           uintptr
	sbp            uintptr
	timeout        uint32
	flags          uint32
	packID         int32
	_              uintptr // usr_ptr
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
	sgDxferNone    = -1
)

// sgIO issues a raw SG_IO ioctl on the open file descriptor.
func sgIOIoctl(fd uintptr, cmd []byte, data []byte, dir int32) ([]byte, error) {
	sense := make([]byte, 32)
	hdr := sgIOHdr{
		interfaceID:    int32('S'),
		dxferDirection: dir,
		cmdLen:         uint8(len(cmd)),
		mxSbLen:        uint8(len(sense)),
		dxferLen:       uint32(len(data)),
		timeout:        5000, // 5 second timeout
	}
	if len(cmd) > 0 {
		hdr.cmdp = uintptr(unsafe.Pointer(&cmd[0]))
	}
	if len(sense) > 0 {
		hdr.sbp = uintptr(unsafe.Pointer(&sense[0]))
	}
	if len(data) > 0 {
		hdr.dxferp = uintptr(unsafe.Pointer(&data[0]))
	}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, sgIO, uintptr(unsafe.Pointer(&hdr)))
	if errno != 0 {
		return nil, fmt.Errorf("SG_IO ioctl: %w", errno)
	}
	if hdr.status != 0 {
		// Extract fixed-format sense bytes [2]=sense key, [12]=ASC, [13]=ASCQ
		s := make([]byte, 3)
		if len(sense) >= 14 {
			s[0] = sense[2]
			s[1] = sense[12]
			s[2] = sense[13]
		}
		return nil, &SCSIError{Op: "status", Code: int(hdr.status), Sense: s}
	}
	return data, nil
}

// openSGDev opens a /dev/sg* or /dev/sd* device for SG_IO.
func openSGDev(device string) (uintptr, func(), error) {
	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return 0, nil, fmt.Errorf("open %s: %w", device, err)
	}
	return f.Fd(), func() { f.Close() }, nil
}

// DeriveKey derives a unique 8-byte SCSI-3 PR key from /etc/machine-id.
// Uses SHA-256 of the machine-id content; first 8 bytes become the key.
// This ensures the key is stable across reboots, unique per host, and
// not guessable without filesystem access.
func DeriveKey() (RegistrationKey, error) {
	raw, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		return RegistrationKey{}, fmt.Errorf("read machine-id: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	digest := sha256.Sum256([]byte(id))
	var key RegistrationKey
	copy(key[:], digest[:8])
	return key, nil
}

// Register sends PERSISTENT RESERVE OUT - REGISTER with APTPL=1.
// APTPL (Activate Persist Through Power Loss) ensures the registration
// survives a power cycle without requiring the host to re-register.
// The current key parameter is 0 (first-time registration).
func Register(device string, key RegistrationKey) error {
	fd, close, err := openSGDev(device)
	if err != nil {
		return err
	}
	defer close()

	// PROUT parameter list: 24 bytes
	// Bytes 0-7:  reservation key (current, 0 for new registration)
	// Bytes 8-15: service action reservation key (our key)
	// Byte 20:    APTPL bit (bit 0)
	param := make([]byte, 24)
	copy(param[8:16], key[:])
	param[20] = 0x01 // APTPL = 1

	// CDB: PERSISTENT RESERVE OUT
	// Byte 0: 0x5F
	// Byte 1: service action (REGISTER = 0x00) in bits [4:0]
	// Byte 2: reserved
	// Byte 3: type (0) | scope (0)
	// Bytes 4-6: reserved
	// Bytes 7-8: parameter list length (24 = 0x0018)
	// Byte 9: control (0)
	cdb := []byte{scsiPROUT, proutRegister, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x18, 0x00}
	_, err = sgIOIoctl(fd, cdb, param, sgDxferToDev)
	return err
}

// Reserve sends PERSISTENT RESERVE OUT - RESERVE.
// Type is WRITE EXCLUSIVE, REGISTRANTS ONLY (0x05).
func Reserve(device string, key RegistrationKey) error {
	fd, close, err := openSGDev(device)
	if err != nil {
		return err
	}
	defer close()

	param := make([]byte, 24)
	copy(param[0:8], key[:]) // reservation key = our key

	// Byte 3: scope (0x00 = LU_SCOPE) | type (0x05 = WRITE EXCLUSIVE, REGISTRANTS ONLY)
	// Packed as (scope << 4) | type = 0x05
	cdb := []byte{scsiPROUT, proutReserve, 0x00, 0x05, 0x00, 0x00, 0x00, 0x00, 0x18, 0x00}
	_, err = sgIOIoctl(fd, cdb, param, sgDxferToDev)
	return err
}

// Release sends PERSISTENT RESERVE OUT - RELEASE.
// The reservation holder releases its reservation; other registrations remain.
func Release(device string, key RegistrationKey) error {
	fd, close, err := openSGDev(device)
	if err != nil {
		return err
	}
	defer close()

	param := make([]byte, 24)
	copy(param[0:8], key[:])

	// Same type byte as Reserve (must match the type used to acquire the reservation)
	cdb := []byte{scsiPROUT, proutRelease, 0x00, 0x05, 0x00, 0x00, 0x00, 0x00, 0x18, 0x00}
	_, err = sgIOIoctl(fd, cdb, param, sgDxferToDev)
	return err
}

// Preempt sends PERSISTENT RESERVE OUT - PREEMPT.
// Used by an incoming primary to evict the current holder (victimKey)
// and claim the reservation. This is the core STONITH mechanism for
// shared-disk HA: the new primary atomically takes the reservation,
// making the old primary's writes fail immediately.
func Preempt(device string, ourKey, victimKey RegistrationKey) error {
	fd, close, err := openSGDev(device)
	if err != nil {
		return err
	}
	defer close()

	param := make([]byte, 24)
	copy(param[0:8], ourKey[:])     // reservation key = our key
	copy(param[8:16], victimKey[:]) // service action reservation key = victim's key

	// PREEMPT with same type = WRITE EXCLUSIVE, REGISTRANTS ONLY (0x05)
	cdb := []byte{scsiPROUT, proutPreempt, 0x00, 0x05, 0x00, 0x00, 0x00, 0x00, 0x18, 0x00}
	_, err = sgIOIoctl(fd, cdb, param, sgDxferToDev)
	return err
}

// SupportsReservations probes a device for SCSI-3 Persistent Reservation support
// by performing a full PROUT round-trip: register our key, verify it appears in
// PRIN READ KEYS, then unregister. All three steps must succeed.
//
// This is the only trustworthy check. PRIN-only tests (sg_persist --in -k) produce
// false positives: a drive that answers READ KEYS may still reject REGISTER, because
// PROUT is a write-side command that SAT layers commonly leave unimplemented. A
// false positive here routes users to the shared-storage path and silently
// fails at the moment of a real failover.
//
// The test is safe: we register and immediately unregister using a test key
// (0xdeadbeefcafe0001) that is distinct from any operational key, so there is no
// interference with a running cluster. If the device is already part of a live
// cluster that holds a reservation, our register will still succeed (REGISTER with
// APTPL=0 is always allowed for unregistered keys), and we clean up immediately.
func SupportsReservations(device string) error {
	testKey := RegistrationKey{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0x00, 0x01}

	fd, closeFn, err := openSGDev(device)
	if err != nil {
		return fmt.Errorf("open %s: %w", device, err)
	}
	defer closeFn()

	// Step 1: PROUT REGISTER - register testKey with APTPL=0.
	param := make([]byte, 24)
	copy(param[8:16], testKey[:]) // service action reservation key = testKey
	// APTPL=0 so the test registration does not persist through power loss.
	cdb := []byte{scsiPROUT, proutRegister, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x18, 0x00}
	if _, err := sgIOIoctl(fd, cdb, param, sgDxferToDev); err != nil {
		return fmt.Errorf("PROUT REGISTER failed (drive does not support PR write commands): %w", err)
	}

	// Step 2: PRIN READ KEYS - verify testKey appears in the registration list.
	buf := make([]byte, 512)
	readKeysCDB := []byte{scsiPRIN, prinReadKeys, 0x00, 0x00, 0x00, 0x00, 0x00, byte(len(buf) >> 8), byte(len(buf)), 0x00}
	data, err := sgIOIoctl(fd, readKeysCDB, buf, sgDxferFromDev)
	found := false
	if err == nil && len(data) >= 8 {
		addLen := int(data[4])<<24 | int(data[5])<<16 | int(data[6])<<8 | int(data[7])
		keys := data[8:]
		if len(keys) > addLen {
			keys = keys[:addLen]
		}
		for len(keys) >= 8 {
			var k RegistrationKey
			copy(k[:], keys[:8])
			if k == testKey {
				found = true
				break
			}
			keys = keys[8:]
		}
	}

	// Step 3: PROUT REGISTER with key=0 to unregister testKey (cleanup).
	unregParam := make([]byte, 24)
	copy(unregParam[0:8], testKey[:]) // reservation key = testKey (current)
	// service action key = 0 means unregister
	unregCDB := []byte{scsiPROUT, proutRegister, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x18, 0x00}
	if _, err := sgIOIoctl(fd, unregCDB, unregParam, sgDxferToDev); err != nil {
		log.Printf("SCSIPR: cleanup unregister failed on %s (test key may remain until next probe): %v", device, err)
	}

	if !found {
		return fmt.Errorf("PROUT REGISTER succeeded but key not visible in PRIN READ KEYS - unreliable SAT translation")
	}
	return nil
}

// ReadKeys sends PERSISTENT RESERVE IN - READ KEYS and READ RESERVATION
// to return the current registration state of the device.
func ReadKeys(device string) (PRStatus, error) {
	fd, close, err := openSGDev(device)
	if err != nil {
		return PRStatus{}, err
	}
	defer close()

	// READ KEYS: 4096 byte buffer is generous; typical NAS clusters have < 8 nodes
	buf := make([]byte, 4096)
	// CDB: PRIN, READ KEYS (0x00), length in bytes 7-8
	cdb := []byte{scsiPRIN, prinReadKeys, 0x00, 0x00, 0x00, 0x00, 0x00, byte(len(buf) >> 8), byte(len(buf)), 0x00}
	data, err := sgIOIoctl(fd, cdb, buf, sgDxferFromDev)
	if err != nil {
		return PRStatus{}, fmt.Errorf("PRIN READ KEYS: %w", err)
	}

	var status PRStatus

	// Parse READ KEYS response:
	// Bytes 0-3: PRGENERATION (ignored)
	// Bytes 4-7: ADDITIONAL LENGTH (total bytes of key list)
	if len(data) < 8 {
		return status, nil
	}
	addLen := binary.BigEndian.Uint32(data[4:8])
	keyBytes := data[8:]
	if uint32(len(keyBytes)) > addLen {
		keyBytes = keyBytes[:addLen]
	}
	for len(keyBytes) >= 8 {
		var k RegistrationKey
		copy(k[:], keyBytes[:8])
		status.Keys = append(status.Keys, k)
		keyBytes = keyBytes[8:]
	}

	// READ RESERVATION: check if there is an active reservation and who holds it
	resBuf := make([]byte, 256)
	resCDB := []byte{scsiPRIN, prinReadReservation, 0x00, 0x00, 0x00, 0x00, 0x00, byte(len(resBuf) >> 8), byte(len(resBuf)), 0x00}
	resData, err := sgIOIoctl(fd, resCDB, resBuf, sgDxferFromDev)
	if err != nil {
		// Non-fatal: if we can read keys but not reservations, return keys only
		return status, nil
	}
	// Bytes 4-7: ADDITIONAL LENGTH of reservation data
	if len(resData) >= 8 {
		resAddLen := binary.BigEndian.Uint32(resData[4:8])
		if resAddLen >= 16 {
			// Reservation descriptor: bytes 8-15 = reservation key
			status.Reserved = true
			copy(status.HolderKey[:], resData[8:16])
		}
	}

	return status, nil
}
