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
  if (e instanceof TypeError) {
    // fetch network failure
    return `cannot reach the crowban server (${BASE}) — is it running?`;
  }
  if (e instanceof Error) return e.message;
  return String(e);
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = getAuthToken();
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    ...(token ? { Authorization: `Bearer ${token}` } : {}),
    ...((init?.headers as Record<string, string>) ?? {}),
  };
  let res: Response;
  try {
    res = await fetch(`${BASE}${path}`, {
      ...init,
      headers,
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
      body: JSON.stringify({ actor }),
    });
  },

  getAuthToken,
  setAuthToken,
};

