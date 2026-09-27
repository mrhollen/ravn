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
  TaskDetail,
  TaskSummary,
  UpdateTaskReq,
} from "./types";

const BASE = "/api/v1";

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
}

/** Extracts a human-readable message from any thrown value. */
export function errMsg(e: unknown): string {
  if (e instanceof ApiError) return `${e.body.message ?? e.body.error ?? e.status}`;
  if (e instanceof TypeError) {
    // fetch network failure
    return `cannot reach the crowban server (${BASE}) — is it running?`;
  }
  if (e instanceof Error) return e.message;
  return String(e);
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  let res: Response;
  try {
    res = await fetch(`${BASE}${path}`, {
      ...init,
      headers: { "Content-Type": "application/json", ...(init?.headers ?? {}) },
    });
  } catch (e) {
    if (e instanceof TypeError) throw e; // rethrown as-is by errMsg()
    throw new Error(String(e));
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
  return request<T>(path, { method: "POST", body: JSON.stringify(payload) });
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

  /** Version-guarded partial update; stale version → 409 version_conflict. */
  updateTask(id: string, req: UpdateTaskReq): Promise<TaskSummary> {
    return request<TaskSummary>(`/tasks/${id}`, {
      method: "PATCH",
      body: JSON.stringify(req),
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

  addComment(taskId: string, req: CommentReq): Promise<CommentRow> {
    return post<CommentRow>(`/tasks/${taskId}/comments`, req);
  },

  addArtifact(taskId: string, kind: ArtifactKind, ref: string, summary: string | undefined, actor: ActorInput): Promise<ArtifactRow> {
    const body: ArtifactReq = { actor, kind, ref };
    if (summary) body.summary = summary;
    return post<ArtifactRow>(`/tasks/${taskId}/artifacts`, body);
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
};
