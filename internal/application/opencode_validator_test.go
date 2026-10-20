package application

import (
	"errors"
	"testing"

	"github.com/mrhollen/ravn/internal/domain"
)

func TestValidateOpenCodeServerURL(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		allowedHosts string
		allowPrivate string
		wantErr      bool
	}{
		{
			name:    "empty allowed",
			url:     "",
			wantErr: false,
		},
		{
			name:    "public https allowed",
			url:     "https://opencode.example.com",
			wantErr: false,
		},
		{
			name:    "public http allowed",
			url:     "http://api.opencode.ai:4444",
			wantErr: false,
		},
		{
			name:    "scheme file rejected",
			url:     "file:///etc/passwd",
			wantErr: true,
		},
		{
			name:    "scheme ftp rejected",
			url:     "ftp://example.com/foo",
			wantErr: true,
		},
		{
			name:    "cloud metadata 169.254.169.254 rejected",
			url:     "http://169.254.169.254/latest/meta-data/",
			wantErr: true,
		},
		{
			name:    "link local IPv4 rejected",
			url:     "http://169.254.10.20:8080",
			wantErr: true,
		},
		{
			name:    "metadata hostname rejected",
			url:     "http://metadata.google.internal/computeMetadata/v1",
			wantErr: true,
		},
		{
			name:    "instance-data hostname rejected",
			url:     "http://instance-data/latest/meta-data/",
			wantErr: true,
		},
		{
			name:    "loopback 127.0.0.1 rejected by default",
			url:     "http://127.0.0.1:4444",
			wantErr: true,
		},
		{
			name:    "localhost rejected by default",
			url:     "http://localhost:4444",
			wantErr: true,
		},
		{
			name:    "private 10.0.0.5 rejected by default",
			url:     "http://10.0.0.5:4444",
			wantErr: true,
		},
		{
			name:    "private 192.168.1.1 rejected by default",
			url:     "http://192.168.1.1:8080",
			wantErr: true,
		},
		{
			name:    "private 172.16.0.1 rejected by default",
			url:     "http://172.16.0.1:3000",
			wantErr: true,
		},
		{
			name:         "allowed via OPENCODE_ALLOWED_HOSTS exact",
			url:          "http://10.0.0.5:4444",
			allowedHosts: "10.0.0.5:4444",
			wantErr:      false,
		},
		{
			name:         "allowed via OPENCODE_ALLOWED_HOSTS hostname",
			url:          "http://localhost:4444",
			allowedHosts: "localhost",
			wantErr:      false,
		},
		{
			name:         "allowed via OPENCODE_ALLOWED_HOSTS CIDR",
			url:          "http://10.0.0.5:4444",
			allowedHosts: "10.0.0.0/8",
			wantErr:      false,
		},
		{
			name:         "metadata still blocked even when private IP allowlist does not include it",
			url:          "http://169.254.169.254/latest/meta-data/",
			allowedHosts: "10.0.0.5:4444",
			wantErr:      true,
		},
		{
			name:         "allow private IPs flag enables 10.0.0.5",
			url:          "http://10.0.0.5:4444",
			allowPrivate: "true",
			wantErr:      false,
		},
		{
			name:         "allow private IPs flag enables loopback",
			url:          "http://127.0.0.1:4444",
			allowPrivate: "true",
			wantErr:      false,
		},
		{
			name:         "allow private IPs flag enables localhost",
			url:          "http://localhost:4444",
			allowPrivate: "true",
			wantErr:      false,
		},
		{
			name:         "allow private IPs flag does NOT allow cloud metadata",
			url:          "http://169.254.169.254/latest/meta-data/",
			allowPrivate: "true",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OPENCODE_ALLOWED_HOSTS", tt.allowedHosts)
			t.Setenv("OPENCODE_ALLOW_PRIVATE_IPS", tt.allowPrivate)

			err := ValidateOpenCodeServerURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateOpenCodeServerURL(%q) err = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("expected error wrapping domain.ErrInvalid, got %v", err)
			}
		})
	}
}

func TestValidateOpenCodeDirectory(t *testing.T) {
	tests := []struct {
		name        string
		dir         string
		allowedDirs string
		wantErr     bool
	}{
		{
			name:    "empty allowed",
			dir:     "",
			wantErr: false,
		},
		{
			name:    "absolute path allowed",
			dir:     "/workspace/project",
			wantErr: false,
		},
		{
			name:    "tmp path allowed",
			dir:     "/tmp/mock-worktree",
			wantErr: false,
		},
		{
			name:    "relative path rejected",
			dir:     "relative/workspace",
			wantErr: true,
		},
		{
			name:    "path traversal simple rejected",
			dir:     "../../etc/passwd",
			wantErr: true,
		},
		{
			name:    "path traversal embedded rejected",
			dir:     "/home/user/../../etc/passwd",
			wantErr: true,
		},
		{
			name:        "allowed directory constraint satisfied",
			dir:         "/workspace/project",
			allowedDirs: "/workspace,/tmp",
			wantErr:     false,
		},
		{
			name:        "allowed directory constraint violated",
			dir:         "/etc/shadow",
			allowedDirs: "/workspace,/tmp",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OPENCODE_ALLOWED_DIRECTORIES", tt.allowedDirs)

			err := ValidateOpenCodeDirectory(tt.dir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateOpenCodeDirectory(%q) err = %v, wantErr %v", tt.dir, err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, domain.ErrInvalid) {
				t.Errorf("expected error wrapping domain.ErrInvalid, got %v", err)
			}
		})
	}
}
