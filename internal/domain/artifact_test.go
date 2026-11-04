package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestArtifact_Validate(t *testing.T) {
	now := time.Now()
	actor := Actor{Type: ActorHuman, Name: "tester"}

	tests := []struct {
		name    string
		art     Artifact
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid url artifact with https",
			art: Artifact{
				ID:        "ART-1",
				TaskID:    "TASK-1",
				Kind:      ArtifactURL,
				Ref:       "https://github.com/mrhollen/ravn",
				Summary:   "Ravn GitHub repository",
				Actor:     actor,
				CreatedAt: now,
			},
			wantErr: false,
		},
		{
			name: "valid url artifact with http and port",
			art: Artifact{
				ID:        "ART-2",
				TaskID:    "TASK-1",
				Kind:      ArtifactURL,
				Ref:       "http://localhost:8080/metrics",
				Summary:   "Local metrics endpoint",
				Actor:     actor,
				CreatedAt: now,
			},
			wantErr: false,
		},
		{
			name: "valid log artifact with local ref and empty summary",
			art: Artifact{
				ID:        "ART-3",
				TaskID:    "TASK-1",
				Kind:      ArtifactLog,
				Ref:       "/var/log/ravn.log",
				Summary:   "",
				Actor:     actor,
				CreatedAt: now,
			},
			wantErr: false,
		},
		{
			name: "unknown kind",
			art: Artifact{
				ID:      "ART-4",
				TaskID:  "TASK-1",
				Kind:    ArtifactKind("invalid-kind"),
				Ref:     "some-ref",
				Summary: "summary",
			},
			wantErr: true,
			errMsg:  "unknown artifact kind",
		},
		{
			name: "empty ref",
			art: Artifact{
				ID:      "ART-5",
				TaskID:  "TASK-1",
				Kind:    ArtifactLog,
				Ref:     "   ",
				Summary: "summary",
			},
			wantErr: true,
			errMsg:  "artifact ref must not be empty",
		},
		{
			name: "url kind with ftp scheme",
			art: Artifact{
				ID:      "ART-6",
				TaskID:  "TASK-1",
				Kind:    ArtifactURL,
				Ref:     "ftp://ftp.example.com/dump.tar",
				Summary: "FTP dump",
			},
			wantErr: true,
			errMsg:  "must be a valid http or https URL",
		},
		{
			name: "url kind with file scheme",
			art: Artifact{
				ID:      "ART-7",
				TaskID:  "TASK-1",
				Kind:    ArtifactURL,
				Ref:     "file:///etc/passwd",
				Summary: "passwd file",
			},
			wantErr: true,
			errMsg:  "must be a valid http or https URL",
		},
		{
			name: "url kind with local path instead of URL",
			art: Artifact{
				ID:      "ART-8",
				TaskID:  "TASK-1",
				Kind:    ArtifactURL,
				Ref:     "/var/data/results.csv",
				Summary: "local file path",
			},
			wantErr: true,
			errMsg:  "must be a valid http or https URL",
		},
		{
			name: "url kind with scheme but missing host",
			art: Artifact{
				ID:      "ART-9",
				TaskID:  "TASK-1",
				Kind:    ArtifactURL,
				Ref:     "https://",
				Summary: "scheme only",
			},
			wantErr: true,
			errMsg:  "must be a valid http or https URL",
		},
		{
			name: "summary with newline \\n",
			art: Artifact{
				ID:      "ART-10",
				TaskID:  "TASK-1",
				Kind:    ArtifactScreenshot,
				Ref:     "/artifacts/screen.png",
				Summary: "line 1\nline 2",
			},
			wantErr: true,
			errMsg:  "must be a single line, found newline",
		},
		{
			name: "summary with carriage return \\r",
			art: Artifact{
				ID:      "ART-11",
				TaskID:  "TASK-1",
				Kind:    ArtifactScreenshot,
				Ref:     "/artifacts/screen.png",
				Summary: "line 1\rline 2",
			},
			wantErr: true,
			errMsg:  "must be a single line, found newline",
		},
		{
			name: "summary exceeding max length",
			art: Artifact{
				ID:      "ART-12",
				TaskID:  "TASK-1",
				Kind:    ArtifactCommandOutput,
				Ref:     "/artifacts/out.txt",
				Summary: strings.Repeat("a", MaxArtifactSummaryLen+1),
			},
			wantErr: true,
			errMsg:  "max is 500",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.art.Validate()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Validate() expected error, got nil")
				}
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("Validate() err = %v, want ErrInvalid", err)
				}
				if tc.errMsg != "" && !strings.Contains(err.Error(), tc.errMsg) {
					t.Errorf("Validate() err = %q, want it to contain %q", err.Error(), tc.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() unexpected error: %v", err)
			}
		})
	}
}
