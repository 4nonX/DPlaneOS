//go:build !linux

package scsipr

import "errors"

var errUnsupported = errors.New("SCSI-3 persistent reservations are only supported on Linux")

func DeriveKey() (RegistrationKey, error)                  { return RegistrationKey{}, errUnsupported }
func Register(device string, key RegistrationKey) error    { return errUnsupported }
func Reserve(device string, key RegistrationKey) error     { return errUnsupported }
func Release(device string, key RegistrationKey) error     { return errUnsupported }
func Preempt(device, target string, ourKey, victimKey RegistrationKey) error { return errUnsupported }
func ReadKeys(device string) (PRStatus, error)             { return PRStatus{}, errUnsupported }
