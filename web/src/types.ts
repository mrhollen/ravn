// Wire-contract types for the ravn REST API (docs/API.md is the source of
// truth; these mirror internal/interface/http/dto.go). Optional fields are
// OMITTED on the wire when unset — never null.

export type Status = "todo" | "doing" | "done";
export const STATUSES: readonly Status[] = ["todo", "doing", "done"];

export const STATUS_LABEL: Record<Status, string> = {
  todo: "To Do",
  doing: "Doing",
  done: "Done",
};

export type CommentKind =
  | "progress"
  | "question"
  | "decision"
  | "handoff"
  | "note";
export const COMMENT_KINDS: readonly CommentKind[] = [
  "progress",
  "question",
  "decision",
  "handoff",
  "note",
];

export type ArtifactKind =
  | "log"
  | "screenshot"
  | "command-output"
  | "trace"
  | "url"
  | "other";
export const ARTIFACT_KINDS: readonly ArtifactKind[] = [
  "log",
  "screenshot",
  "command-output",
  "trace",
  "url",
  "other",
];

// Stored relation kinds on the wire. The tool-boundary alias "parent" never
// appears in a persisted row (stored as the inverse child edge).
export type RelationKind = "child" | "blocked-by" | "duplicate-of" | "related";

export type ToolRelationKind =
  | "parent"
  | "child"
  | "blocked-by"
  | "blocking"
  | "duplicate-of"
  | "related";

/** Actor reference as embedded in rows. `name` can be "" for special holders
 *  (e.g. a no-lease claim-conflict holder `{ "type": "" }`). */
export interface ActorRef {
  type: string;
  name: string;
}

/** The self-declared actor every mutation body carries. `type` is optional
 *  and defaults to "human" server-side. */
export interface ActorInput {
  name: string;
  type?: "human" | "agent";
}

// --- row shapes -------------------------------------------------------------

export interface StatusCounts {
  todo: number;
  doing: number;
  done: number;
}

export interface Project {
  id: string;
  name: string;
  description?: string;
  opencode_server_url?: string;
  opencode_directory?: string;
  opencode_model?: string;
  opencode_agent?: string;
  opencode_variant?: string;
  archived?: boolean;
  created_at: string;
  updated_at: string;
}

/** GET /projects row: project + per-status OPEN task counts. */
export interface ProjectWithCounts extends Project {
  task_counts: StatusCounts;
}

export interface ListProjectsResponse {
  projects: ProjectWithCounts[];
  total: number;
}

/** Compact task row returned by every list/board/create/update response. */
export interface TaskSummary {
  id: string;
  title: string;
  status: Status;
  priority: number; // 0 (highest) – 5 (lowest)
  due_at?: string; // RFC3339, omitted when unset
  assignee?: ActorRef; // durable owner, omitted when unset
  claimed_by?: ActorRef; // ephemeral lease holder, omitted when unclaimed
  claim_expires_at?: string; // omitted while unclaimed
  version: number;
  created_at: string;
  updated_at: string;
  // Board-snapshot card badges (omitted/absent on list + create/update rows):
  ac_total?: number; // acceptance-criteria count, omitted when none
  ac_done?: number; // how many of them are done
  comment_count?: number; // comment-thread length, omitted when zero
  subtask_total?: number; // subtask count, omitted when none
  subtask_done?: number; // how many of them are done
  blockers?: BlockerRef[]; // open blockers for this task (omitted when empty)
}

export interface BlockerRef {
  id: string;
  title: string;
  status: Status;
}

/** Acceptance criterion (seq is 1-based). */
export interface AcRow {
  seq: number;
  text: string;
  done: boolean;
  note?: string;
  completed_by?: ActorRef;
  completed_at?: string;
}

export interface CommentRow {
  id: string;
  kind: CommentKind;
  body: string; // markdown
  actor: ActorRef;
  created_at: string;
}

export interface ListCommentsResponse {
  comments: CommentRow[];
  total: number;
  limit: number;
  offset: number;
}

export interface ArtifactRow {
  id: string;
  kind: ArtifactKind;
  ref: string; // URL or filesystem path
  summary?: string;
  actor: ActorRef;
  created_at: string;
}

/** A raw edge touching a task in either direction (no edge id on the wire). */
export interface RelationRow {
  task_id: string;
  other_task_id: string;
  kind: RelationKind;
  created_at: string;
}

/** Activity-log entry; before/after are compact JSON strings, omitted when empty. */
export interface EventRow {
  id: string;
  kind: string; // created|updated|status-changed|claimed|released|heartbeat|assigned|noted|commented|ac-toggled|artifact-added|relation-changed
  actor: ActorRef;
  before?: string;
  after?: string;
  at: string;
}

export type SseConnectionStatus = "connected" | "connecting" | "disconnected";

/** Realtime event payload streamed over SSE (GET /projects/{id}/events). */
export interface RealtimeEventPayload {
  event_id?: string;
  project_id?: string;
  task_id?: string;
  kind: string;
  status?: string;
  actor?: ActorRef;
  timestamp: string;
}


/** GET /tasks/{id} — the one-call resume payload (summary fields flattened in). */
export interface TaskDetail extends TaskSummary {
  project_id: string;
  description: string; // markdown
  notes: string; // durable resume scratchpad
  archived: boolean;
  blocked: boolean; // derived at read time from open blocked-by edges
  blockers: BlockerRef[]; // [] when none — never null
  claimed_at?: string; // omitted while unclaimed
  claim_ttl_seconds?: number; // omitted/0 while unclaimed
  completed_at?: string;
  acceptance_criteria: AcRow[]; // [] when none
  comments: CommentRow[]; // newest first (tail)
  artifacts: ArtifactRow[]; // oldest first
  relations: RelationRow[]; // edges touching this task, either direction
  events: EventRow[]; // newest first (tail)
}

/** GET /projects/{id}/board — whole project in one round-trip. */
export interface Board {
  project_id: string;
  counts: StatusCounts;
  tasks: Record<Status, TaskSummary[]>; // all three keys always present ([] when empty)
}

/** POST /tasks → 201 body: fresh row + advisory duplicate candidates. */
export interface CreateTaskResponse {
  task: TaskSummary;
  possible_duplicates: TaskSummary[]; // [] when none — advisory only
}

// --- request bodies ----------------------------------------------------------

export interface CreateProjectReq {
  actor: ActorInput;
  name: string; // required
  description?: string; // markdown
}

export interface UpdateProjectReq {
  actor: ActorInput;
  name?: string;
  description?: string;
  opencode_server_url?: string;
  opencode_directory?: string;
  opencode_model?: string;
  opencode_agent?: string;
  opencode_variant?: string;
  archived?: boolean;
}

export type UpdateProjectInput = UpdateProjectReq;

export interface OpenCodeAgentOption {
  name: string;
  mode?: string;
  model?: string;
  variant?: string;
}

export interface OpenCodeModelOption {
  ref: string; // "providerID/modelID"
  provider_id: string;
  provider_name?: string;
  model_id: string;
  model_name?: string;
  variants: string[]; // e.g. ["low", "medium", "xhigh"]
}

export interface OpenCodeOptionsResponse {
  reachable: boolean;
  resolved_agent: string;
  resolved_model: string;
  resolved_variant: string;
  agents: OpenCodeAgentOption[];
  models: OpenCodeModelOption[];
}

export interface StartOpenCodeInput {
  agent?: string;
  model?: string;
  variant?: string;
}

export interface StartOpenCodeResponse {
  session_id: string;
  session_url: string;
  agent?: string;
  model?: string;
  variant?: string;
}

export interface CreateTaskReq {
  actor: ActorInput;
  title: string; // required
  description?: string; // markdown
  project_id: string; // required; must exist
  priority?: number; // 0–5, default 0
  due_at?: string; // RFC3339
  assignee?: ActorInput;
  acceptance_criteria?: string[]; // become seq 1..n
}

export interface UpdateTaskReq {
  actor: ActorInput;
  version: number; // REQUIRED — stored version last read; stale → 409
  title?: string | null;
  description?: string | null;
  status?: Status;
  priority?: number; // 0–5
  due_at?: string; // RFC3339 to set…
  clear_due?: boolean; // …or clear the deadline
  assignee?: ActorInput; // durable ownership…
  clear_assignee?: boolean; // …or clear it
  archived?: boolean;
  override_blockers?: boolean;
}

export interface NotesReq {
  actor: ActorInput;
  notes: string; // whole-blob replace; NO version field in the request
}

export interface AcToggleReq {
  actor: ActorInput;
  done: boolean;
  note?: string;
}

export interface AcCreateReq {
  actor: ActorInput;
  text: string;
}

export interface AcEditReq {
  actor: ActorInput;
  text: string;
}

export interface AcDeleteReq {
  actor: ActorInput;
}

export interface CommentReq {
  actor: ActorInput;
  kind: CommentKind;
  body: string; // markdown, non-empty after trim
}

export interface ArtifactReq {
  actor: ActorInput;
  kind: ArtifactKind;
  ref: string; // URL or path, non-empty after trim
  summary?: string;
}

// --- list_tasks (subset of filters the UI uses) -------------------------------

export interface ListTasksParams {
  project_id?: string;
  status?: Status;
  assignee_name?: string;
  unblocked?: boolean; // true = keep only tasks with no open blocker
  search?: string;
  priority?: number;
  due_before?: string;
  created_after?: string;
  archived?: boolean; // true = ONLY archived tasks (default lists open ones)
  limit?: number; // default 20, max 100 (clamped)
  offset?: number; // default 0
}

export interface ListTasksResponse {
  tasks: TaskSummary[];
  total: number; // matching rows before paging
  limit: number; // effective after clamping
  offset: number;
}

// --- error bodies --------------------------------------------------------------

/** Base shape of every /api/v1/* error body. `message` is the agent-readable
 *  recovery instruction — surface it verbatim in the UI. */
export interface ApiErrorBody {
  error: string; // "invalid input" | "not found" | "version_conflict" | "claim conflict" | "not claimed" | "parent conflict" | "internal"
  message?: string;
  resource?: string; // "task" | "project"
  id?: string;
}

/** 409 version_conflict — carries everything needed to rebase without a read. */
export interface VersionConflictBody extends ApiErrorBody {
  error: "version_conflict";
  current_version: number;
  updated_at?: string;
  current: TaskSummary; // fresh task summary row
}

/** 409 claim conflict — holder + expiry ARE the recovery instruction.
 *  `holder` ALWAYS names a live foreign lease; a task with no lease at all
 *  gets 409 `not claimed` instead (see NotClaimedBody). */
export interface ClaimConflictBody extends ApiErrorBody {
  error: "claim conflict";
  holder: ActorRef;
  expires_at?: string;
}

/** 409 not claimed — the task has no claim lease at all (nobody to wait for);
 *  recovery is claim first. */
export interface NotClaimedBody extends ApiErrorBody {
  error: "not claimed";
  resource: "task";
  id: string;
}

/** 409 parent conflict — a task may have at most one parent. */
export interface ParentConflictBody extends ApiErrorBody {
  error: "parent conflict";
}
