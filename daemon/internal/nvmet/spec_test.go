package nvmet

import "testing"

func TestValidateSpec_NQN(t *testing.T) {
	e := &Export{
		SubsystemNQN: "nqn.2024-01.io.dplane:testvol",
		Zvol:         "tank/v",
		Transport:    "tcp",
		AllowAnyHost: true,
	}
	if err := ValidateSpec(e); err != nil {
		t.Fatal(err)
	}
	e2 := &Export{SubsystemNQN: "bad", Zvol: "tank/v", AllowAnyHost: true}
	if ValidateSpec(e2) == nil {
		t.Fatal("expected error")
	}
}

func TestValidateSpec_HostRequired(t *testing.T) {
	e := &Export{
		SubsystemNQN: "nqn.2024-01.io.dplane:x",
		Zvol:         "tank/v",
		AllowAnyHost: false,
	}
	if ValidateSpec(e) == nil {
		t.Fatal("expected error for missing host_nqns")
	}
}
