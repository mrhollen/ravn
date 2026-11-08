package sqlite

// scoping_predicates.go holds the SQL fragments for membership-scoped visibility
// (multi-user epic T4). They are used ONLY when a VisibilityScope filter is set:
// in global/default mode every List call passes the zero filter, so no fragment
// is ever appended and the generated query stays byte-identical to the
// pre-T4 statement (epic-level regression safety).
//
// Semantics (mirror of domain.VisibilityPolicy under MembershipScoping): a
// project is visible to an identity iff it OWNS the project
// (project.created_by = id) OR HOLDS any stored membership in it
// (a project_member row for viewer/editor/admin — i.e. "effective role != none",
// with Owner folded in from created_by). Tasks inherit their owning project's
// visibility, and comments/artifacts/events/tasks-inherit-from-parents follow
// the same rule one level up.
//
// A scope whose identity ID is zero (unknown/unresolved acting subject under
// membership scoping) renders these fragments with empty-string ids: no row has
// created_by = '' nor a membership for '', so the predicate denies everything —
// exactly the policy's "no relationships ⇒ sees nothing" rule, in SQL.

// projectVisibleExpr is the visibility predicate for one project row (table
// name "project", two positional args: identity id twice).
const projectVisibleExpr = `project.created_by = ? OR EXISTS (SELECT 1 FROM project_member pm WHERE pm.project_id = project.id AND pm.identity_id = ?)`

// taskProjectVisibleExpr is the same rule for one task row, joined through its
// owning project (two positional args: identity id twice).
const taskProjectVisibleExpr = `EXISTS (SELECT 1 FROM project p WHERE p.id = task.project_id AND (p.created_by = ? OR EXISTS (SELECT 1 FROM project_member pm WHERE pm.project_id = p.id AND pm.identity_id = ?)))`

// taskActivityExpr restricts a task row to ones the named actor ACTED ON: at
// least one comment, artifact, or logged event under that exact actor_name
// (three positional args: actor name thrice). Actor names are stored trimmed;
// callers trim before binding.
const taskActivityExpr = `(EXISTS (SELECT 1 FROM comment c WHERE c.task_id = task.id AND c.actor_name = ?) OR EXISTS (SELECT 1 FROM artifact a WHERE a.task_id = task.id AND a.actor_name = ?) OR EXISTS (SELECT 1 FROM event e WHERE e.task_id = task.id AND e.actor_name = ?))`
