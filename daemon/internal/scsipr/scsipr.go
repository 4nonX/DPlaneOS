package scsipr

import "fmt"

// ReservationType is the SCSI-3 persistent reservation type byte.
// We use WRITE EXCLUSIVE, REGISTRANTS ONLY (0x05) so that:
//   - All registered nodes can read.
//   - Only the reservation holder can write.
//   - An unregistered intruder cannot access the disk at all.
type ReservationType byte

const (
	// ResvWriteExclusiveRegistrantsOnly prevents any unregistered node from
	// issuing write commands. This is the standard type for shared-storage HA.
	ResvWriteExclusiveRegistrantsOnly ReservationType = 0x05
)

// RegistrationKey is an 8-byte key uniquely identifying a node.
// Derived from /etc/machine-id via SHA-256, first 8 bytes.
type RegistrationKey [8]byte

// String returns the key as a hex string for logging.
func (k RegistrationKey) String() string {
	return fmt.Sprintf("%016x", [8]byte(k))
}

// PRStatus holds the current reservation state for a device.
type PRStatus struct {
	// Keys is the list of all registered keys on the device.
	Keys []RegistrationKey
	// Reserved is true if a PERSISTENT RESERVE OUT reservation is held.
	Reserved bool
	// HolderKey is the key holding the reservation (zero value if not reserved).
	HolderKey RegistrationKey
}

// SCSIError wraps SG_IO ioctl errors with the SCSI sense data.
type SCSIError struct {
	Op    string
	Code  int
	Sense []byte
}

func (e *SCSIError) Error() string {
	if len(e.Sense) >= 3 {
		return fmt.Sprintf("scsipr %s: sense key=0x%02x asc=0x%02x ascq=0x%02x",
			e.Op, e.Sense[0]&0x0f, e.Sense[1], e.Sense[2])
	}
	return fmt.Sprintf("scsipr %s: code=%d", e.Op, e.Code)
}
