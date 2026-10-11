// Package scsipr issues SCSI-3 persistent reservation commands.
//
// A node registers a key on a disk and takes the reservation "write
// exclusive, registrants only": from then on the disk itself refuses writes
// from every node whose key is not registered. Taking a disk over from a
// node that is hung or cut off means removing that node's key (preempt):
// its writes fail from that moment, whatever state it is in.
package scsipr

import (
	"crypto/sha256"
	"errors"
	"fmt"
)

// RegistrationKey is the 8-byte key a node registers on a disk.
type RegistrationKey [8]byte

// String returns the key as a hex string.
func (k RegistrationKey) String() string {
	return fmt.Sprintf("%016x", [8]byte(k))
}

// KeyFor derives a node's key from its node ID: stable for the life of the
// node, and every other node can compute it (to say who holds a disk).
func KeyFor(nodeID string) RegistrationKey {
	digest := sha256.Sum256([]byte("dplaneos-disk-reservation:" + nodeID))
	var key RegistrationKey
	copy(key[:], digest[:8])
	if key == (RegistrationKey{}) {
		key[7] = 1 // the zero key means "not registered"
	}
	return key
}

// PRStatus is the reservation state of a device.
type PRStatus struct {
	// Keys are the registered keys.
	Keys []RegistrationKey
	// Reserved reports whether a reservation is held, and by which key.
	Reserved  bool
	HolderKey RegistrationKey
}

// Has reports whether key is registered.
func (s PRStatus) Has(key RegistrationKey) bool {
	for _, k := range s.Keys {
		if k == key {
			return true
		}
	}
	return false
}

// SCSI status and sense values used to tell failures apart.
const (
	statusCheckCondition      = 0x02
	statusReservationConflict = 0x18
	senseIllegalRequest       = 0x05
)

// SCSIError is a command the device or the transport did not complete.
type SCSIError struct {
	Op     string
	Status int // SCSI status byte
	Host   int // transport (host adapter) status
	Driver int
	// Sense key, additional sense code and qualifier (valid with CHECK CONDITION).
	Key, ASC, ASCQ byte
}

func (e *SCSIError) Error() string {
	switch {
	case e.Status == statusReservationConflict:
		return fmt.Sprintf("%s: reservation conflict (another node holds the disk)", e.Op)
	case e.Status == statusCheckCondition:
		return fmt.Sprintf("%s: sense key=0x%02x asc=0x%02x ascq=0x%02x", e.Op, e.Key, e.ASC, e.ASCQ)
	case e.Host != 0 || e.Driver != 0:
		return fmt.Sprintf("%s: transport error (host=0x%02x driver=0x%02x)", e.Op, e.Host, e.Driver)
	}
	return fmt.Sprintf("%s: status=0x%02x", e.Op, e.Status)
}

// ErrNotSCSI: the device does not take SCSI commands at all (virtio, NVMe).
var ErrNotSCSI = errors.New("not a SCSI device")

// IsUnsupported reports whether err means the device has no persistent
// reservations (as opposed to having them and refusing this command).
func IsUnsupported(err error) bool {
	if errors.Is(err, ErrNotSCSI) {
		return true
	}
	var se *SCSIError
	return errors.As(err, &se) && se.Status == statusCheckCondition && se.Key == senseIllegalRequest
}

// IsConflict reports whether err is a reservation conflict: the device
// refused the command because of another node's reservation, or because
// this node's key is not registered (any more).
func IsConflict(err error) bool {
	var se *SCSIError
	return errors.As(err, &se) && se.Status == statusReservationConflict
}

// parseSense extracts sense key, ASC and ASCQ from fixed or descriptor
// format sense data.
func parseSense(sense []byte) (key, asc, ascq byte) {
	if len(sense) < 4 {
		return 0, 0, 0
	}
	switch sense[0] & 0x7f {
	case 0x72, 0x73: // descriptor format
		return sense[1] & 0x0f, sense[2], sense[3]
	default: // 0x70, 0x71: fixed format
		if len(sense) < 14 {
			return sense[2] & 0x0f, 0, 0
		}
		return sense[2] & 0x0f, sense[12], sense[13]
	}
}

// parseKeys reads the key list of a PERSISTENT RESERVE IN / READ KEYS reply.
func parseKeys(data []byte) []RegistrationKey {
	if len(data) < 8 {
		return nil
	}
	n := int(data[4])<<24 | int(data[5])<<16 | int(data[6])<<8 | int(data[7])
	list := data[8:]
	if len(list) > n {
		list = list[:n]
	}
	var keys []RegistrationKey
	for len(list) >= 8 {
		var k RegistrationKey
		copy(k[:], list[:8])
		keys = append(keys, k)
		list = list[8:]
	}
	return keys
}

// parseReservation reads a PERSISTENT RESERVE IN / READ RESERVATION reply.
func parseReservation(data []byte) (reserved bool, holder RegistrationKey) {
	if len(data) < 8 {
		return false, holder
	}
	n := int(data[4])<<24 | int(data[5])<<16 | int(data[6])<<8 | int(data[7])
	if n < 16 || len(data) < 16 {
		return false, holder
	}
	copy(holder[:], data[8:16])
	return true, holder
}
