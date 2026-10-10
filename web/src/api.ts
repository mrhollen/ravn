// Minimal REST client for the crowban API (docs/API.md). No auth in the MVP
// tier; every mutation carries a self-declared actor. Errors are surfaced as
// ApiError carrying the exact JSON error body so callers can branch on the
// `error` field (e.g. "version_conflict" → rebase + retry once).

import type {
  AcRow,
  ActorInput,
  ApiErrorBody,
  ArtifactKind,
  ArtifactReq,
  ArtifactRow,
  Board,
  CommentReq,
  CommentRow,
  CreateProjectReq,
  CreateTaskReq,
  CreateTaskResponse,
  ListProjectsResponse,
  ListTasksParams,
  ListTasksResponse,
  NotesReq,
  Project,
  RelationRow,
  StartOpenCodeResponse,
  TaskDetail,
  TaskSummary,
  UpdateProjectReq,
  UpdateTaskReq,
} from "./types";

const BASE = "/api/v1";

/** Default deadline for a single API request (ms). Longer than this and the
 *  connection is considered dead: fetch() is aborted, every caller's `finally`
 *  runs (busy flags release), and errMsg() maps it to a friendly message.
 *  Chosen in the 15–30s band per task 01M3MMYBHECPH5Z5G4J. This timeout
 *  applies to every request that flows through request() — including the
 *  move path's 409-retry getTask re-read, which was unbounded before. The SSE
 *  EventSource connection does NOT flow through request() and is exempt by
 *  design (long-lived + auto-reconnecting). */
export const API_TIMEOUT_MS = 20_000;

// Mutable holder behind a setter so tests can shorten the deadline instead of
// waiting real seconds — same spirit as the timer-injectable helpers in
// refresh.ts / moves.ts. Production code never calls the setter.
let activeTimeoutMs = API_TIMEOUT_MS;
/** TEST-ONLY override for the per-request timeout (ms). Values ≤ 0 reset to
 *  the default. Exposed on the Alpine app purely so browser tests can exercise
 *  hang paths quickly. */
export function setApiTimeoutMsForTests(ms: number): void {
  activeTimeoutMs = ms > 0 ? ms : API_TIMEOUT_MS;
}

/** Thrown when OUR per-request timeout aborts a fetch. Kept distinct from a
 *  caller-initiated abort (whose AbortError must pass through untouched — the
 *  move flow owns its own shorter window in moves.ts and maps it to its
 *  specific "Move timed out" message, which stays more specific than ours). */
export class ApiTimeoutError extends Error {
  readonly timeoutMs: number;
  constructor(timeoutMs: number) {
    super(`request timed out after ${timeoutMs}ms`);
    this.name = "ApiTimeoutError";
    this.timeoutMs = timeoutMs;
  }
}

/** Merges an optional caller-supplied AbortSignal with a per-request timeout
 *  into ONE signal for fetch(), and lets the caller attribute which side won:
 *   - `signal`      — pass to fetch(); aborts when EITHER side fires.
 *   - `timedOut()`  — true only if OUR timer fired (set synchronously in the
 *                     timer callback before the abort).
 *   - `cleanup()`   — always call (clears the timer + caller listener).
 * Manual AbortController+setTimeout merge rather than AbortSignal.any()/
 * AbortSignal.timeout(): identical behavior on every target (including Node
 * test envs), and it preserves cause attribution — essential so a move's own
 * 10s abort is never misreported as the generic API timeout. */
export function withRequestTimeout(
  ms: number,
  callerSignal?: AbortSignal,
): { signal: AbortSignal; timedOut: () => boolean; cleanup: () => void } {
  const ctrl = new AbortController();
  let ours = false;
  const timer = setTimeout(() => {
    ours = true;
    ctrl.abort();
  }, ms);
  const onCallerAbort = (): void => ctrl.abort();
  if (callerSignal) {
    if (callerSignal.aborted) {
      // Caller already lost the race before we started — adopt their abort
      // immediately without attributing it to our timer.
      ctrl.abort();
    } else {
      callerSignal.addEventListener("abort", onCallerAbort, { once: true });
    }
  }
  return {
    signal: ctrl.signal,
    timedOut: () => ours,
    cleanup: () => {
      clearTimeout(timer);
      if (callerSignal && !callerSignal.aborted) callerSignal.removeEventListener("abort", onCallerAbort);
    },
  };
}

/** True for the native abort DOMExceptions fetch rejects with ("AbortError" /
 *  "TimeoutError"). */
function isAbortLike(e: unknown): boolean {
  return e instanceof Error && (e.name === "AbortError" || e.name === "TimeoutError");
}

/** An HTTP error with the parsed crowban error body. */
export class ApiError extends Error {
  readonly status: number;
  readonly body: ApiErrorBody;

  constructor(status: number, body: ApiErrorBody) {
    super(body.message ?? `${status} ${body.error ?? "error"}`);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
  }

  /** True for the 409 that carries current_version + fresh `current` row. */
  static isVersionConflict(e: unknown): e is ApiError & {
    body: import("./types").VersionConflictBody;
  } {
    return (
      e instanceof ApiError &&
      e.status === 409 &&
      e.body.error === "version_conflict" &&
      typeof (e.body as import("./types").VersionConflictBody).current_version ===
        "number"
    );
  }

  static isClaimConflict(e: unknown): boolean {
    return e instanceof ApiError && e.status === 409 && e.body.error === "claim conflict";
  }

  static isParentConflict(e: unknown): boolean {
    return e instanceof ApiError && e.status === 409 && e.body.error === "parent conflict";
  }

  static isUnauthorized(e: unknown): boolean {
    return e instanceof ApiError && e.status === 401;
  }
}

export const LS_TOKEN = "crowban_token";

/** Retrieves the auth token from URL ?token= or localStorage. */
export function getAuthToken(): string {
  if (typeof window !== "undefined") {
    try {
      const params = new URLSearchParams(window.location.search);
      const urlToken = params.get("token");
      if (urlToken) {
        setAuthToken(urlToken);
        params.delete("token");
        const newQuery = params.toString();
        const newUrl =
          window.location.pathname +
          (newQuery ? `?${newQuery}` : "") +
          window.location.hash;
        window.history.replaceState({}, "", newUrl);
        return urlToken;
      }
    } catch {
      /* ignore */
    }
    try {
      return localStorage.getItem(LS_TOKEN) || "";
    } catch {
      /* private mode */
    }
  }
  return "";
}

/** Stores or clears the auth token in localStorage. */
export function setAuthToken(token: string): void {
  try {
    if (typeof localStorage === "undefined") return;
    const trimmed = token.trim();
    if (!trimmed) {
      localStorage.removeItem(LS_TOKEN);
    } else {
      localStorage.setItem(LS_TOKEN, trimmed);
    }
  } catch {
    /* private mode */
  }
}

/** Extracts a human-readable message from any thrown value. */
export function errMsg(e: unknown): string {
  if (e instanceof ApiError) return `${e.body.message ?? e.body.error ?? e.status}`;
  if (e instanceof ApiTimeoutError || isAbortLike(e)) {
    // Request aborted by OUR per-request timeout, or a raw AbortError/
    // TimeoutError from any other source. Callers that OWN an abort signal
    // (the move flow's own 10s window) map it to their specific message BEFORE
    // errMsg is ever consulted, so this generic text only ever appears for
    // timeouts no one else owns.
    return "Request timed out — please check your connection or server status.";
  }
  if (e instanceof TypeError) {
    // fetch network failure
    return `cannot reach the crowban server (${BASE}) — is it running?`;
  }
  if (e instanceof Error) return e.message;
  return String(e);
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getAuthToken();
  const isFormData = typeof FormData !== "undefined" && init?.body instanceof FormData;
  const headers: Record<string, string> = {
    ...(isFormData ? {} : { "Content-Type": "application/json" }),
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
    ...((init?.headers as Record<string, string>) ?? {}),
  };
  let res: Response;
  // Every request is bounded by the global timeout, merged with any caller-
  // supplied signal (updateTask's move-path window). Cause attribution: OUR
  // timer firing → ApiTimeoutError; the caller's own abort → the original
  // AbortError rethrown untouched so the signal owner can map it itself.
  const callerSignal = init?.signal ?? undefined;
  const guard = withRequestTimeout(activeTimeoutMs, callerSignal);
  try {
    res = await fetch(`${BASE}${path}`, {
      ...init,
      headers,
      signal: guard.signal,
    });
  } catch (e) {
    if (guard.timedOut()) throw new ApiTimeoutError(activeTimeoutMs);
    if (isAbortLike(e)) throw e; // caller-initiated abort — owner handles it
    if (e instanceof TypeError) throw e; // network failure, rethrown as-is by errMsg()
    throw new Error(String(e));
  } finally {
    guard.cleanup();
  }

  const ctype = res.headers.get("content-type") ?? "";
  const isJson = ctype.includes("application/json");

  if (!res.ok) {
    let body: ApiErrorBody;
    try {
      body = (isJson ? await res.json() : {}) as ApiErrorBody;
    } catch {
      body = { error: "internal", message: `unexpected response (${res.status})` };
    }
    if (!body.error) body.error = res.statusText || String(res.status);
    throw new ApiError(res.status, body);
  }

  if (res.status === 204) return undefined as T;
  if (!isJson) return undefined as T;
  return (await res.json()) as T;
}

function post<T>(path: string, payload: unknown): Promise<T> {
  const body = typeof FormData !== "undefined" && payload instanceof FormData
    ? payload
    : JSON.stringify(payload);
  return request<T>(path, { method: "POST", body });
}

function put<T>(path: string, payload: unknown): Promise<T> {
  return request<T>(path, { method: "PUT", body: JSON.stringify(payload) });
}

export const api = {
  // --- projects -------------------------------------------------------------
  listProjects(): Promise<ListProjectsResponse> {
    return request<ListProjectsResponse>("/projects");
  },

  createProject(req: CreateProjectReq): Promise<Project> {
    return post<Project>("/projects", req);
  },

  getProject(id: string): Promise<Project> {
    return request<Project>(`/projects/${id}`);
  },

  updateProject(id: string, req: UpdateProjectReq): Promise<Project> {
    return put<Project>(`/projects/${id}`, req);
  },

  /** Whole-project board snapshot in one round-trip. */
  getBoard(projectId: string): Promise<Board> {
    return request<Board>(`/projects/${projectId}/board`);
  },

  // --- tasks ------------------------------------------------------------------
  listTasks(params?: ListTasksParams): Promise<ListTasksResponse> {
    const qs = new URLSearchParams();
    if (params) {
      for (const [k, v] of Object.entries(params)) {
        if (v === undefined || v === null || v === "") continue;
        qs.set(k, String(v));
      }
    }
    const s = qs.toString();
    return request<ListTasksResponse>(`/tasks${s ? `?${s}` : ""}`);
  },

  createTask(req: CreateTaskReq): Promise<CreateTaskResponse> {
    return post<CreateTaskResponse>("/tasks", req);
  },

  /** One-call resume payload. comment_limit/event_limit are clamped server-side. */
  getTask(id: string, opts?: { commentLimit?: number; eventLimit?: number }): Promise<TaskDetail> {
    const qs = new URLSearchParams();
    if (opts?.commentLimit) qs.set("comment_limit", String(opts.commentLimit));
    if (opts?.eventLimit) qs.set("event_limit", String(opts.eventLimit));
    const s = qs.toString();
    return request<TaskDetail>(`/tasks/${id}${s ? `?${s}` : ""}`);
  },

  /** Version-guarded partial update; stale version → 409 version_conflict.
   *  `opts.signal` lets a CALLER bound its own deadline to this request (the
   *  move flow passes its 10s window from moves.ts). The signal is MERGED with
   *  the global per-request timeout (API_TIMEOUT_MS) — whichever fires first
   *  aborts the fetch, and cause attribution in request() keeps the caller's
   *  AbortError distinct from our ApiTimeoutError, so a move's own timeout
   *  still produces its specific "Move timed out" message rather than the
   *  generic one. */
  updateTask(id: string, req: UpdateTaskReq, opts?: { signal?: AbortSignal }): Promise<TaskSummary> {
    return request<TaskSummary>(`/tasks/${id}`, {
      method: "PATCH",
      body: JSON.stringify(req),
      ...(opts?.signal ? { signal: opts.signal } : {}),
    });
  },

  /** Whole-blob notes replace (no version field); rare concurrent-write 409. */
  putNotes(id: string, req: NotesReq): Promise<TaskSummary> {
    return put<TaskSummary>(`/tasks/${id}/notes`, req);
  },

  toggleAc(taskId: string, seq: number, actor: ActorInput, done: boolean, note?: string): Promise<AcRow> {
    const body: import("./types").AcToggleReq = { actor, done };
    if (note) body.note = note;
    return request<AcRow>(`/tasks/${taskId}/acceptance-criteria/${seq}`, {
      method: "PATCH",
      body: JSON.stringify(body),
    });
  },

  addAc(taskId: string, text: string, actor: ActorInput): Promise<AcRow> {
    return post<AcRow>(`/tasks/${taskId}/acceptance-criteria`, { actor, text });
  },

  editAcText(taskId: string, seq: number, text: string, actor: ActorInput): Promise<AcRow> {
    return request<AcRow>(`/tasks/${taskId}/acceptance-criteria/${seq}`, {
      method: "PATCH",
      body: JSON.stringify({ actor, text }),
    });
  },

  deleteAc(taskId: string, seq: number, actor: ActorInput): Promise<void> {
    return request<void>(`/tasks/${taskId}/acceptance-criteria/${seq}`, {
      method: "DELETE",
      headers: {
        "X-Actor-Name": actor.name,
        ...(actor.type ? { "X-Actor-Type": actor.type } : {}),
      },
      body: JSON.stringify({ actor }),
    });
  },

  addComment(taskId: string, req: CommentReq): Promise<CommentRow> {
    return post<CommentRow>(`/tasks/${taskId}/comments`, req);
  },

  listComments(taskId: string, opts?: { limit?: number; offset?: number }): Promise<{ comments: CommentRow[]; total: number }> {
    const qs = new URLSearchParams();
    if (opts?.limit !== undefined) qs.set("limit", String(opts.limit));
    if (opts?.offset !== undefined) qs.set("offset", String(opts.offset));
    const s = qs.toString();
    return request<{ comments: CommentRow[]; total: number }>(`/tasks/${taskId}/comments${s ? `?${s}` : ""}`);
  },

  addArtifact(taskId: string, kind: ArtifactKind, ref: string, summary: string | undefined, actor: ActorInput): Promise<ArtifactRow> {
    const body: ArtifactReq = { actor, kind, ref };
    if (summary) body.summary = summary;
    return post<ArtifactRow>(`/tasks/${taskId}/artifacts`, body);
  },

  uploadArtifact(taskId: string, file: File, kind: ArtifactKind, summary: string | undefined, actor: ActorInput): Promise<ArtifactRow> {
    const fd = new FormData();
    fd.append("file", file);
    fd.append("kind", kind);
    if (summary) fd.append("summary", summary);
    fd.append("actor_name", actor.name);
    fd.append("actor_type", actor.type || "human");
    fd.append("actor", JSON.stringify(actor));
    return post<ArtifactRow>(`/tasks/${taskId}/artifacts`, fd);
  },

  // --- opencode autonomous integration --------------------------------------
  startOpenCode(taskId: string): Promise<StartOpenCodeResponse> {
    return post<StartOpenCodeResponse>(`/tasks/${taskId}/opencode/start`, {});
  },

  // --- collaboration endpoints (agents use these via MCP; the UI is read-only
  //     for claims by design — kept here for completeness) ------------------------
  claimTask(id: string, actor: ActorInput, ttlSec?: number): Promise<TaskSummary> {
    const body: { actor: ActorInput; ttl_sec?: number } = { actor };
    if (ttlSec && ttlSec > 0) body.ttl_sec = ttlSec;
    return post<TaskSummary>(`/tasks/${id}/claim`, body);
  },

  releaseTask(id: string, actor: ActorInput, note?: string): Promise<TaskSummary> {
    const body: { actor: ActorInput; note?: string } = { actor };
    if (note) body.note = note;
    return post<TaskSummary>(`/tasks/${id}/release`, body);
  },

  heartbeatTask(id: string, actor: ActorInput): Promise<TaskSummary> {
    return post<TaskSummary>(`/tasks/${id}/heartbeat`, { actor });
  },

  // --- relations -------------------------------------------------------------
  addRelation(taskId: string, otherId: string, kind: string, actor: ActorInput): Promise<RelationRow> {
    return post<RelationRow>(`/tasks/${taskId}/relations`, {
      actor,
      other_id: otherId,
      kind,
    });
  },

  removeRelation(taskId: string, otherId: string, kind: string, actor: ActorInput): Promise<void> {
    return request<void>(`/tasks/${taskId}/relations/${otherId}/${kind}`, {
      method: "DELETE",
      headers: {
        "X-Actor-Name": actor.name,
        ...(actor.type ? { "X-Actor-Type": actor.type } : {}),
      },
      body: JSON.stringify({ actor }),
    });
  },

  getAuthToken,
  setAuthToken,
};

