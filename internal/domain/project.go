package domain

import "time"

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
