package handlers

import "testing"

func TestHasMountPoint(t *testing.T) {
	tests := []struct {
		name string
		dev  blockDevice
		want bool
	}{
		{
			name: "disk without mounts",
			dev:  blockDevice{Name: "sdb", Type: "disk"},
			want: false,
		},
		{
			name: "disk with direct mount",
			dev:  blockDevice{Name: "sdb", Type: "disk", MountPoint: "/data"},
			want: true,
		},
		{
			name: "disk with mounted partition child",
			dev: blockDevice{
				Name: "sda",
				Type: "disk",
				Children: []blockDevice{
					{Name: "sda1", Type: "part", MountPoint: "/boot"},
				},
			},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasMountPoint(tc.dev); got != tc.want {
				t.Fatalf("hasMountPoint() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiskNameInZpoolStatus(t *testing.T) {
	status := `
  pool: tank
 state: ONLINE
config:

	NAME                        STATE     READ WRITE CKSUM
	tank                        ONLINE       0     0     0
	  raidz1-0                  ONLINE       0     0     0
	    /dev/disk/by-id/ata-sda ONLINE       0     0     0
	    /dev/disk/by-id/ata-sdaa ONLINE      0     0     0
`

	if !diskNameInZpoolStatus(status, "sda") {
		t.Fatalf("expected sda to match")
	}
	if !diskNameInZpoolStatus(status, "sdaa") {
		t.Fatalf("expected sdaa to match")
	}
	if diskNameInZpoolStatus(status, "sdb") {
		t.Fatalf("did not expect sdb to match")
	}
}
