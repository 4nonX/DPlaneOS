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
