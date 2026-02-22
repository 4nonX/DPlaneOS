package security

import (
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
			args:    []string{"list", "-H", "-o", "name,size,alloc,free,health"},
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
