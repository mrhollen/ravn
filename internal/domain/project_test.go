package domain

import (
	"errors"
	"strings"
	"testing"
)

// project_test.go: unit tests for Project field invariants. The read path must
// stay tolerant of legacy rows, so these tests pin the WRITE-path gates: every
// value here is what Projects.Create / Projects.Update would persist.

func validProject() Project {
	return Project{
		ID:                "01TESTPROJECTID",
		Name:              "Demo Project",
		Description:       "onboarding doc",
		OpenCodeServerURL: "http://localhost:4444",
		OpenCodeDirectory: "/workspace/project",
		OpenCodeModel:     "llama.cpp/Qwen Nano",
	}
}

func TestProjectValidate_Valid(t *testing.T) {
	p := validProject()
	if err := p.Validate(); err != nil {
		t.Fatalf("valid project rejected: %v", err)
	}

	// Every optional field may be empty (server defaults apply at dispatch).
	bare := Project{ID: "01BAREPROJECTID", Name: "Bare"}
	if err := bare.Validate(); err != nil {
		t.Fatalf("project with all optional fields empty rejected: %v", err)
	}
}

func TestProjectValidate_Name(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid short", "Demo Project", true},
		{"exactly at cap", strings.Repeat("a", MaxProjectNameLen), true},
		{"multi-byte at cap counts runes not bytes", strings.Repeat("é", MaxProjectNameLen), true},
		{"empty", "", false},
		{"whitespace only", "   \t\n", false},
		{"one over cap", strings.Repeat("a", MaxProjectNameLen+1), false},
		{"multi-byte one over cap", strings.Repeat("é", MaxProjectNameLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validProject()
			p.Name = tc.in
			err := p.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

func TestProjectValidate_Description(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid short markdown", "## Onboarding\nServer-side work goes here.", true},
		{"empty allowed", "", true},
		{"exactly at cap", strings.Repeat("a", MaxProjectDescriptionLen), true},
		{"multi-byte string measured in bytes: half the cap in runes = at cap", strings.Repeat("é", MaxProjectDescriptionLen/2), true},
		{"one byte over cap", strings.Repeat("a", MaxProjectDescriptionLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validProject()
			p.Description = tc.in
			err := p.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

func TestProjectValidate_ServerURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"http with port", "http://localhost:4444", true},
		{"https with path and port", "https://opencode.example.com:8443/server/x", true},
		{"uppercase scheme", "HTTP://EXAMPLE.COM", true},
		{"empty means not configured", "", true},
		{"javascript scheme rejected", "javascript:alert(1)", false},
		{"ftp scheme rejected", "ftp://opencode.example.com", false},
		{"missing host rejected", "http://", false},
		{"schemeless rejected", "not a url at all", false},
		{"unterminated ipv6 bracket rejected", "http://[::1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validProject()
			p.OpenCodeServerURL = tc.in
			err := p.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

func TestProjectValidate_Directory(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"absolute path", "/workspace", true},
		{"relative allowed at domain level", "some/relative/dir", true},
		{"exactly at cap", strings.Repeat("d", MaxOpenCodeDirectoryLen), true},
		{"empty means not configured", "", true},
		{"one over cap", strings.Repeat("d", MaxOpenCodeDirectoryLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validProject()
			p.OpenCodeDirectory = tc.in
			err := p.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

// TestProjectValidate_Model pins the exact parseOpenCodeModel semantics: split
// on the FIRST slash; both halves non-empty; further slashes belong to the
// model ID. Any divergence would accept (or reject) a value that dispatch
// later fails (or succeeds) on.
func TestProjectValidate_Model(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid simple", "openai/gpt-4o", true},
		{"valid with space in model id", "llama.cpp/Qwen Nano", true},
		{"valid multiple slashes in model id", "openrouter/meta-llama/llama-3.1-70b-instruct", true},
		{"empty means server default", "", true},
		{"no slash rejected", "gpt-4o", false},
		{"leading slash rejected (empty provider)", "/model", false},
		{"trailing slash rejected (empty model)", "provider/", false},
		{"lone slash rejected", "/", false},
		{"double leading slash rejected", "//double", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validProject()
			p.OpenCodeModel = tc.in
			err := p.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

func TestValidateProjectName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid", "Demo Project", true},
		{"trimmed to empty rejected", "  ", false},
		{"exactly at cap", strings.Repeat("a", MaxProjectNameLen), true},
		{"one over cap", strings.Repeat("a", MaxProjectNameLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProjectName(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateProjectName(%q) = %v, want nil", tc.in, err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateProjectName(%q) = %v, want wrapped domain.ErrInvalid", tc.in, err)
			}
		})
	}
}

func TestValidateProjectDescription(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid", "onboarding doc", true},
		{"empty allowed (clears the description)", "", true},
		{"exactly at cap", strings.Repeat("a", MaxProjectDescriptionLen), true},
		{"one over cap", strings.Repeat("a", MaxProjectDescriptionLen+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateProjectDescription(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateProjectDescription() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateProjectDescription() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

func TestValidateOpenCodeModel(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"empty allowed (server default)", "", true},
		{"valid", "llama.cpp/Qwen Nano", true},
		{"valid nested model id", "openrouter/meta-llama/llama-3.1-70b-instruct", true},
		{"no slash rejected", "gpt-4o", false},
		{"leading slash rejected", "/model", false},
		{"trailing slash rejected", "provider/", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOpenCodeModel(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateOpenCodeModel(%q) = %v, want nil", tc.in, err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateOpenCodeModel(%q) = %v, want wrapped domain.ErrInvalid", tc.in, err)
			}
		})
	}
}

func TestProjectValidate_Agent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid simple", "build", true},
		{"valid custom", "frontend-expert", true},
		{"empty allowed", "", true},
		{"whitespace allowed (clears/empty)", "   ", true},
		{"exactly at cap", strings.Repeat("a", MaxOpenCodeAgentLen), true},
		{"multi-byte at cap counts runes", strings.Repeat("🤖", MaxOpenCodeAgentLen), true},
		{"one over cap", strings.Repeat("a", MaxOpenCodeAgentLen+1), false},
		{"contains newline control char", "build\nagent", false},
		{"contains null control char", "build\x00agent", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validProject()
			p.OpenCodeAgent = tc.in
			err := p.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

func TestProjectValidate_Variant(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid simple", "thinking", true},
		{"valid high", "high", true},
		{"empty allowed", "", true},
		{"whitespace allowed (clears/empty)", "   ", true},
		{"exactly at cap", strings.Repeat("v", MaxOpenCodeVariantLen), true},
		{"multi-byte at cap counts runes", strings.Repeat("⚡", MaxOpenCodeVariantLen), true},
		{"one over cap", strings.Repeat("v", MaxOpenCodeVariantLen+1), false},
		{"contains carriage return control char", "high\rvariant", false},
		{"contains bell control char", "thinking\x07", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := validProject()
			p.OpenCodeVariant = tc.in
			err := p.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() = %v, want wrapped domain.ErrInvalid", err)
			}
		})
	}
}

func TestValidateOpenCodeAgent(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"empty allowed", "", true},
		{"whitespace allowed", "   \t\n", true},
		{"valid name", "build", true},
		{"valid hyphens and underscores", "agent_1-test", true},
		{"exactly at cap", strings.Repeat("x", MaxOpenCodeAgentLen), true},
		{"one over cap", strings.Repeat("x", MaxOpenCodeAgentLen+1), false},
		{"control character tab inside string", "build\tagent", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOpenCodeAgent(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateOpenCodeAgent(%q) = %v, want nil", tc.in, err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateOpenCodeAgent(%q) = %v, want wrapped domain.ErrInvalid", tc.in, err)
			}
		})
	}
}

func TestValidateOpenCodeVariant(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"empty allowed", "", true},
		{"whitespace allowed", "  ", true},
		{"valid variant", "low", true},
		{"valid complex", "variant-4k-context", true},
		{"exactly at cap", strings.Repeat("y", MaxOpenCodeVariantLen), true},
		{"one over cap", strings.Repeat("y", MaxOpenCodeVariantLen+1), false},
		{"control character inside", "var\x1b[0miant", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOpenCodeVariant(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("ValidateOpenCodeVariant(%q) = %v, want nil", tc.in, err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("ValidateOpenCodeVariant(%q) = %v, want wrapped domain.ErrInvalid", tc.in, err)
			}
		})
	}
}
