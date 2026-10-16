package domain

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Project is a named collection of tasks. Description is markdown and doubles
// as the agent onboarding document. Project deletion is out of scope for the
// MVP (cascade machinery exists in the schema regardless).
type Project struct {
	ID                ID
	Name              string // unique across all projects
	Description       string // markdown; empty allowed
	OpenCodeServerURL string // optional URL to OpenCode server (e.g. http://localhost:4444)
	OpenCodeDirectory string // optional worktree directory for OpenCode
	OpenCodeModel     string // optional model "providerID/modelID" for dispatched sessions; empty = server default
	Archived          bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// MaxProjectNameLen caps a project name's length in RUNES after trimming.
// Names are user-visible labels, not identifiers: anything longer is almost
// certainly a paste or an accident and must not be able to bloat event JSON or
// the UI. Mirrors MaxActorNameLen (actor.go). Every project name in this repo's
// unit tests, E2E fixtures, and seed data is well under the cap. Validation
// happens at WRITE paths only (Create/Update use cases), so legacy rows stored
// before the cap remain readable and mutable — see Project.Validate.
const MaxProjectNameLen = 100

// MaxProjectDescriptionLen caps a project description's size in BYTES.
// Descriptions are markdown onboarding documents: byte-bounded like task
// descriptions — generous for hand-written docs, bounded against pasted
// blobs. Empty is allowed (no description).
const MaxProjectDescriptionLen = 64 * 1024

// MaxOpenCodeDirectoryLen caps an OpenCode worktree directory's length in
// runes. Directories are deploy-specific paths; this is a light bound against
// pasted garbage, not a shape check — Validate deliberately does NOT require
// absolute paths (the deployment decides what a valid worktree is; the
// application layer's ValidateOpenCodeDirectory adds the abs-path + traversal
// rules for update input).
const MaxOpenCodeDirectoryLen = 512

// Validate reports whether p satisfies the Project field invariants:
//
//   - Name is non-empty (after trimming) and at most MaxProjectNameLen runes;
//   - Description is at most MaxProjectDescriptionLen bytes (empty allowed);
//   - OpenCodeServerURL, when non-empty, is a well-formed HTTP/HTTPS URL:
//     net/url parses it, its scheme is http or https (case-insensitive), and
//     its host is non-empty. Empty = no OpenCode server configured — allowed;
//   - OpenCodeDirectory, when non-empty, is at most MaxOpenCodeDirectoryLen
//     runes. Free-form on purpose: absolute paths are NOT required here;
//   - OpenCodeModel, when non-empty, looks like "providerID/modelID" in
//     EXACTLY the sense parseOpenCodeModel (internal/interface/http) requires
//     at dispatch time: a first slash with non-empty provider and model on
//     both sides; further slashes belong to the model ID. Empty = server
//     default — allowed.
//
// Errors wrap ErrInvalid, so use cases can return them verbatim and the REST
// layer maps them to 400. Validate is deliberately a pure predicate: it runs
// on WRITE paths only (Projects.Create validates the whole entity;
// Projects.Update validates each incoming field value before writing), so
// legacy rows stored before these invariants keep flowing through read paths
// untouched and their untouched legacy values are not re-litigated by an
// unrelated update.
func (p Project) Validate() error {
	if err := ValidateProjectName(p.Name); err != nil {
		return err
	}
	if err := ValidateProjectDescription(p.Description); err != nil {
		return err
	}
	if p.OpenCodeServerURL != "" {
		if err := validateOpenCodeServerURLShape(p.OpenCodeServerURL); err != nil {
			return err
		}
	}
	if runes := utf8.RuneCountInString(p.OpenCodeDirectory); runes > MaxOpenCodeDirectoryLen {
		return fmt.Errorf("%w: opencode directory is %d runes, max is %d", ErrInvalid, runes, MaxOpenCodeDirectoryLen)
	}
	if p.OpenCodeModel != "" {
		if err := ValidateOpenCodeModel(p.OpenCodeModel); err != nil {
			return err
		}
	}
	return nil
}

// ValidateProjectName reports whether name is a usable project name: non-empty
// after trimming and at most MaxProjectNameLen runes (multi-byte UTF-8 counts
// as one rune, mirroring Actor.Validate). Wraps ErrInvalid.
func ValidateProjectName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: project name must not be empty", ErrInvalid)
	}
	if runes := utf8.RuneCountInString(name); runes > MaxProjectNameLen {
		return fmt.Errorf("%w: project name is %d runes, max is %d", ErrInvalid, runes, MaxProjectNameLen)
	}
	return nil
}

// ValidateProjectDescription reports whether description is within
// MaxProjectDescriptionLen bytes (empty allowed). Called at WRITE paths only
// (Projects.Create via Validate; Projects.Update validates the incoming value
// before writing), so legacy rows stored before the cap remain readable and
// mutable. Wraps ErrInvalid.
func ValidateProjectDescription(description string) error {
	if n := len(description); n > MaxProjectDescriptionLen {
		return fmt.Errorf("%w: project description is %d bytes, max is %d", ErrInvalid, n, MaxProjectDescriptionLen)
	}
	return nil
}

// validateOpenCodeServerURLShape checks URL SHAPE only: net/url must parse it,
// the scheme must be http or https (case-insensitive), and the host must be
// non-empty. Deployment-aware security rules (prohibited private/loopback IPs
// unless allowlisted, cloud-metadata blocking, OPENCODE_ALLOWED_HOSTS) live in
// application.ValidateOpenCodeServerURL and are applied on top of this at the
// update write path and again at dispatch time.
func validateOpenCodeServerURLShape(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%w: invalid opencode server URL: %v", ErrInvalid, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("%w: opencode server URL scheme must be http or https, got %q", ErrInvalid, u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%w: opencode server URL host must not be empty", ErrInvalid)
	}
	return nil
}

// ValidateOpenCodeModel reports whether ref looks like "providerID/modelID" in
// EXACTLY the sense the OpenCode dispatch handler requires: parseOpenCodeModel
// (internal/interface/http/handlers_opencode.go) splits on the FIRST slash and
// fails iff that slash is missing, leading (empty provider), or trailing
// (empty model). Model IDs may themselves contain slashes
// ("openrouter/meta-llama/..." style refs) — mirror that here verbatim so a
// value accepted by Validate can always be parsed by dispatch. Empty = server
// default model, allowed. Wraps ErrInvalid.
func ValidateOpenCodeModel(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	i := strings.Index(ref, "/")
	if i <= 0 || i == len(ref)-1 {
		return fmt.Errorf("%w: model %q must look like \"providerID/modelID\"", ErrInvalid, ref)
	}
	return nil
}
