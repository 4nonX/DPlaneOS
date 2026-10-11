//go:build !linux

package scsipr

func Register(device string, key RegistrationKey) error           { return ErrNotSCSI }
func Unregister(device string, key RegistrationKey) error         { return ErrNotSCSI }
func Reserve(device string, key RegistrationKey) error            { return ErrNotSCSI }
func Release(device string, key RegistrationKey) error            { return ErrNotSCSI }
func Preempt(device string, ourKey, victim RegistrationKey) error { return ErrNotSCSI }
func ReadKeys(device string) (PRStatus, error)                    { return PRStatus{}, ErrNotSCSI }
func SupportsReservations(device string) error                    { return ErrNotSCSI }
