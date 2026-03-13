package security

import (
	"testing"
)

func TestIsValidPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{
			name: "Valid simple path",
			path: "/mnt/storage",
			want: true,
		},
		{
			name: "Valid nested path",
			path: "/mnt/storage/photos/vacation",
			want: true,
		},
		{
			name: "Path traversal attempt",
			path: "/mnt/storage/../../etc/passwd",
			want: false,
		},
		{
			name: "Path traversal attempt 2",
			path: "/mnt/../etc/passwd",
			want: false,
		},
		{
			name: "Absolute path outside allowed",
			path: "/etc/passwd",
			want: false,
		},
		{
			name: "Empty path",
			path: "",
			want: false,
		},
		{
			name: "Valid tank path",
			path: "/tank/backups",
			want: true,
		},
		{
			name: "Path with dot",
			path: "/mnt/storage/./hidden/file",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidPath(tt.path)
			if got != tt.want {
				t.Errorf("IsValidPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsSafeFilename(t *testing.T) {
	tests := []struct {
		name string
		file string
		want bool
	}{
		{
			name: "Valid filename",
			file: "document.pdf",
			want: true,
		},
		{
			name: "Valid with path",
			file: "folder/document.pdf",
			want: true,
		},
		{
			name: "Path traversal in filename",
			file: "../../../etc/passwd",
			want: false,
		},
		{
			name: "Hidden file",
			file: ".hidden",
			want: true,
		},
		{
			name: "Path separator in filename",
			file: "file/thing.txt",
			want: false,
		},
		{
			name: "Null byte injection",
			file: "file\x00.txt",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSafeFilename(tt.file)
			if got != tt.want {
				t.Errorf("IsSafeFilename(%q) = %v, want %v", tt.file, got, tt.want)
			}
		})
	}
}
