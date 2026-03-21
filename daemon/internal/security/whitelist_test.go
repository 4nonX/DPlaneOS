package security

import (
	"strings"
	"testing"
)

func TestValidateCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmdName string
		args    []string
		wantErr bool
	}{
		{
			name:    "Valid zpool list",
			cmdName: "zpool_list",
			args:    []string{"list", "-H", "-o", "name,size,alloc,free,cap,health"},
			wantErr: false,
		},
		{
			name:    "Invalid command",
			cmdName: "rm_rf",
			args:    []string{"/"},
			wantErr: true,
		},
		{
			name:    "Valid dataset name",
			cmdName: "zfs_create",
			args:    []string{"create", "tank0/dataset"},
			wantErr: false,
		},
		{
			name:    "Invalid dataset name with spaces",
			cmdName: "zfs_create",
			args:    []string{"create", "tank0/data set"},
			wantErr: true,
		},
		{
			name:    "Command injection attempt",
			cmdName: "zpool_status",
			args:    []string{"status", "-P", "tank0; rm -rf /"},
			wantErr: true,
		},
		{
			name:    "Valid UPS query",
			cmdName: "upsc_query",
			args:    []string{"ups"},
			wantErr: false,
		},
		{
			name:    "Invalid UPS name with path traversal",
			cmdName: "upsc_query",
			args:    []string{"../../../etc/passwd"},
			wantErr: true,
		},
		{
			name:    "Valid zpool create with disk by-id",
			cmdName: "zpool_create",
			args:    []string{"create", "tank0", "/dev/disk/by-id/ata-ST8000VN004-2M2101_ZA160123"},
			wantErr: false,
		},
		{
			name:    "Invalid zpool create with bare device name",
			cmdName: "zpool_create",
			args:    []string{"create", "tank0", "sda"},
			wantErr: true,
		},
		{
			name:    "Valid zfs set mountpoint",
			cmdName: "zfs_set_property",
			args:    []string{"set", "mountpoint=/mnt/data", "tank0/dataset"},
			wantErr: false,
		},
		{
			name:    "Invalid zfs set mountpoint (dangerous path)",
			cmdName: "zfs_set_property",
			args:    []string{"set", "mountpoint=/root/.ssh", "tank0/dataset"},
			wantErr: true,
		},
		{
			name:    "Valid zfs set quota",
			cmdName: "zfs_set_property",
			args:    []string{"set", "quota=100G", "tank0/dataset"},
			wantErr: false,
		},
		{
			name:    "Invalid zfs set quota (missing unit)",
			cmdName: "zfs_set_property",
			args:    []string{"set", "quota=abc", "tank0/dataset"},
			wantErr: true,
		},
		{
			name:    "Valid ufw allow mixed order",
			cmdName: "ufw",
			args:    []string{"allow", "from", "192.168.1.0/24", "to", "any", "port", "80", "proto", "tcp"},
			wantErr: false,
		},
		{
			name:    "Invalid ufw (unauthorized keyword)",
			cmdName: "ufw",
			args:    []string{"allow", "dangerous_keyword"},
			wantErr: true,
		},
		{
			name:    "Valid ip route add",
			cmdName: "ip_route_modify",
			args:    []string{"route", "add", "10.0.0.0/24", "via", "192.168.1.1", "dev", "eth0"},
			wantErr: false,
		},
		{
			name:    "Valid openssl with subj",
			cmdName: "openssl",
			args:    []string{"req", "-newkey", "rsa:2048", "-nodes", "-keyout", "key.pem", "-x509", "-days", "365", "-out", "cert.pem", "-subj", "/CN=example.com"},
			wantErr: false,
		},
		{
			name:    "Invalid openssl subj",
			cmdName: "openssl",
			args:    []string{"-subj", "/CN=../../etc/passwd"},
			wantErr: true,
		},
		{
			name:    "Valid mkdir with space",
			cmdName: "mkdir",
			args:    []string{"-p", "/mnt/valid path"},
			wantErr: false,
		},
		{
			name:    "Invalid mkdir with traversal",
			cmdName: "mkdir",
			args:    []string{"-p", "/mnt/valid path/../../etc"},
			wantErr: true,
		},
		{
			name:    "Valid rm_recursive",
			cmdName: "rm_recursive",
			args:    []string{"-rf", "/tmp/temp_dir"},
			wantErr: false,
		},
		{
			name:    "Invalid rm_recursive traversal",
			cmdName: "rm_recursive",
			args:    []string{"-rf", "/mnt/data/../home"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCommand(tt.cmdName, tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCommand() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateDatasetName(t *testing.T) {
	tests := []struct {
		name    string
		dataset string
		wantErr bool
	}{
		{
			name:    "Valid simple dataset",
			dataset: "tank0/data",
			wantErr: false,
		},
		{
			name:    "Valid nested dataset",
			dataset: "tank0/data/backups/2024",
			wantErr: false,
		},
		{
			name:    "Invalid with spaces",
			dataset: "tank0/my data",
			wantErr: true,
		},
		{
			name:    "Invalid with special chars",
			dataset: "tank0/data$backup",
			wantErr: true,
		},
		{
			name:    "Empty name",
			dataset: "",
			wantErr: true,
		},
		{
			name:    "Too long (>255 chars)",
			dataset: "tank0/" + string(make([]byte, 300)),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDatasetName(tt.dataset)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDatasetName() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsValidSessionToken(t *testing.T) {
	tests := []struct {
		name  string
		token string
		want  bool
	}{
		{
			name:  "Valid token",
			token: "abcdef1234567890abcdef1234567890",
			want:  true,
		},
		{
			name:  "Too short",
			token: "short",
			want:  false,
		},
		{
			name:  "Too long",
			token: string(make([]byte, 150)),
			want:  false,
		},
		{
			name:  "Invalid characters",
			token: "abc123!@#$%^&*()",
			want:  false,
		},
		{
			name:  "Empty",
			token: "",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidSessionToken(tt.token); got != tt.want {
				t.Errorf("IsValidSessionToken() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSanitizeOutput(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
	}{
		{
			name:  "Password in output",
			input: "connection successful password=secret123",
			want:  "connection successful password=***",
		},
		{
			name:  "Token in output",
			input: "auth token=abc123def456",
			want:  "auth token=***",
		},
		{
			name:  "API key in output",
			input: "api key=sk-1234567890",
			want:  "api key=***",
		},
		{
			name:  "No sensitive data",
			input: "normal output without secrets",
			want:  "normal output without secrets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SanitizeOutput(tt.input); got != tt.want {
				t.Errorf("SanitizeOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidationBoundaries(t *testing.T) {
	longString := ""
	for i := 0; i < 300; i++ {
		longString += "a"
	}

	boundaryInputs := []string{"", "/", "\x00", "\n", longString}

	// Test ValidateCommand with boundary inputs
	for _, input := range boundaryInputs {
		t.Run("Boundary_"+input, func(t *testing.T) {
			// Test as command name
			if err := ValidateCommand(input, []string{"arg"}); err == nil {
				t.Errorf("ValidateCommand allowed boundary command name: %q", input)
			}
			// Test as argument
			if err := ValidateCommand("zfs_get", []string{"get", "-H", "-o", "value", input}); err == nil {
				// Some might be allowed if they match the pattern, but null bytes/newlines should ALWAYS fail
				if strings.Contains(input, "\x00") || strings.Contains(input, "\n") {
					t.Errorf("ValidateCommand allowed dangerous boundary argument: %q", input)
				}
			}
		})
	}

	// Test IsValidPath with boundary inputs
	for _, input := range boundaryInputs {
		t.Run("IsValidPath_"+input, func(t *testing.T) {
			if IsValidPath(input) {
				// "/" might be allowed depending on base paths, but generally null bytes/newlines should fail
				if strings.ContainsAny(input, "\x00\n") {
					t.Errorf("IsValidPath allowed dangerous input: %q", input)
				}
			}
		})
	}
}

func TestPathCleaner(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"Normal path", "/mnt/data", true},
		{"Traversal with dots", "/mnt/data/../../etc/passwd", false},
		{"Encoded dots (literal)", "/mnt/data/../etc", false},
		{"Deep traversal", "/mnt/data/././.././../etc/shadow", false},
		{"Canonical traversal", "/mnt/data/../../etc/passwd", false},
		{"Null byte in middle", "/mnt/data\x00/evil", false},
		{"Newline in middle", "/mnt/data\n/evil", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidPath(tt.path); got != tt.want {
				t.Errorf("IsValidPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
