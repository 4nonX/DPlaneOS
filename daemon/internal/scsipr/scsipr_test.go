package scsipr

import (
	"errors"
	"fmt"
	"testing"
)

func TestKeyFor(t *testing.T) {
	a, b := KeyFor("node-a"), KeyFor("node-b")
	if a == b || a == (RegistrationKey{}) || a != KeyFor("node-a") || len(a.String()) != 16 {
		t.Errorf("keys: %s %s", a, b)
	}
}

func TestParse(t *testing.T) {
	k1, k2 := RegistrationKey{1, 2, 3, 4, 5, 6, 7, 8}, RegistrationKey{9, 9, 9, 9, 9, 9, 9, 9}
	reply := append([]byte{0, 0, 0, 7, 0, 0, 0, 16}, append(k1[:], k2[:]...)...)
	if keys := parseKeys(reply); len(keys) != 2 || keys[0] != k1 || keys[1] != k2 {
		t.Errorf("keys: %v", keys)
	}
	// The list length says one key; what follows in the buffer is not one.
	if keys := parseKeys(append([]byte{0, 0, 0, 7, 0, 0, 0, 8}, append(k1[:], k2[:]...)...)); len(keys) != 1 {
		t.Errorf("keys beyond the list length: %v", keys)
	}
	if keys := parseKeys([]byte{0, 0, 0, 1, 0, 0, 0, 0}); len(keys) != 0 {
		t.Errorf("no keys: %v", keys)
	}

	res := append([]byte{0, 0, 0, 7, 0, 0, 0, 16}, k2[:]...)
	res = append(res, 0, 0, 0, 0, 0, 0x05, 0, 0)
	if ok, holder := parseReservation(res); !ok || holder != k2 {
		t.Errorf("reservation: %v %s", ok, holder)
	}
	if ok, _ := parseReservation([]byte{0, 0, 0, 7, 0, 0, 0, 0}); ok {
		t.Error("no reservation reported as one")
	}

	fixed := make([]byte, 18)
	fixed[0], fixed[2], fixed[12], fixed[13] = 0x70, 0x05, 0x20, 0x00
	if k, asc, _ := parseSense(fixed); k != 5 || asc != 0x20 {
		t.Errorf("fixed sense: %x %x", k, asc)
	}
	if k, asc, ascq := parseSense([]byte{0x72, 0x05, 0x24, 0x01, 0, 0, 0, 0}); k != 5 || asc != 0x24 || ascq != 1 {
		t.Errorf("descriptor sense: %x %x %x", k, asc, ascq)
	}
}

func TestErrorKinds(t *testing.T) {
	illegal := fmt.Errorf("wrapped: %w", &SCSIError{Op: "register", Status: 0x02, Key: 0x05, ASC: 0x20})
	conflict := &SCSIError{Op: "reserve", Status: 0x18}
	medium := &SCSIError{Op: "register", Status: 0x02, Key: 0x03}
	if !IsUnsupported(illegal) || !IsUnsupported(fmt.Errorf("x: %w", ErrNotSCSI)) || IsUnsupported(conflict) || IsUnsupported(medium) || IsUnsupported(errors.New("eio")) {
		t.Error("IsUnsupported")
	}
	if !IsConflict(conflict) || IsConflict(illegal) || IsConflict(nil) {
		t.Error("IsConflict")
	}
}
