// Client-side routing core (Phase 1 extraction from app.ts — frontend
// modularization epic): full-page task route parsing/building, board URL query
// state (parse + serialize), and the mobile viewport breakpoint. Pure string/
// number math — window.innerWidth is only read through a defaulted parameter;
// the history glue (pushState/replaceState/popstate listeners) stays in the
// composition root.

import { isSortKey, type SortDir, type SortKey } from "./sort";
import { STATUSES, type Status } from "./types";

function decodeRouteSegment(raw: string): string | null {
  try {
    const id = decodeURIComponent(raw);
    return id.trim() === "" ? null : id;
  } catch {
    return raw; // malformed %-sequence — use the raw segment rather than throw
  }
}

// ------------------------------------------------------ full-page task route
/** Parse the client-side full-page task route `/tasks/<id>` out of a pathname.
 *  Returns null for the board route ("") or anything that is not exactly one
 *  non-empty id segment (no nested slashes, no whitespace-only ids). The server
 *  already deep-links these paths to index.html (SPA fallback in
 *  cmd/ravn-server), so the pathname can be trusted as-is. */
export function taskRouteIdFromPath(pathname: string): string | null {
  const m = /^\/tasks\/([^/]+)$/.exec(pathname);
  return m ? decodeRouteSegment(m[1]) : null;
}

/** Pathname for task `id`'s full-page route. */
export function taskRoutePath(id: string): string {
  return "/tasks/" + encodeURIComponent(id);
}

// ---------------------------------------------------- project dashboard route
/** Parse the client-side project dashboard route `/projects/<id>/dashboard` out
 *  of a pathname. Returns null for anything that is not exactly one non-empty,
 *  non-whitespace project id segment under `/projects/<id>/dashboard`. */
export function dashboardRouteProjectIdFromPath(pathname: string): string | null {
  const m = /^\/projects\/([^/]+)\/dashboard$/.exec(pathname);
  return m ? decodeRouteSegment(m[1]) : null;
}

/** Pathname for project `projectId`'s dashboard route. */
export function dashboardRoutePath(projectId: string): string {
  return "/projects/" + encodeURIComponent(projectId) + "/dashboard";
}

// ---------------------------------------------------------------- URL query sync
export interface BoardQueryParams {
  project?: string;
  status?: Status;
  priority?: string;
  owner?: string;
  due?: string;
  unblocked?: boolean;
  blocked?: boolean;
  /** Expired/stalled filter active (client-side predicate, see isStalledExpired). */
  stalled?: boolean;
  /** "Needs attention" filter active (client-side predicate, see needsAttention). */
  attention?: boolean;
  q?: string;
  sort?: SortKey;
  dir?: SortDir;
}

/** Parse query parameters from a URL search string into board state. */
export function parseBoardQueryParams(search: string): BoardQueryParams {
  if (!search) return {};
  const s = search.startsWith("?") ? search.slice(1) : search;
  if (!s) return {};
  const params = new URLSearchParams(s);
  const result: BoardQueryParams = {};

  const project = params.get("project");
  if (project && project.trim() !== "") {
    result.project = project.trim();
  }

  const status = params.get("status");
  if (status) {
    const norm = status.trim().toLowerCase();
    if ((STATUSES as readonly string[]).includes(norm)) {
      result.status = norm as Status;
    }
  }

  const priority = params.get("priority");
  if (priority !== null && priority !== "" && priority.toLowerCase() !== "all") {
    const num = Number(priority);
    if (!isNaN(num) && num >= 0 && num <= 5) {
      result.priority = String(num);
    }
  }

  const owner = params.get("owner");
  if (owner && owner.trim() !== "" && owner.toLowerCase() !== "all") {
    result.owner = owner.trim();
  }

  const due = params.get("due");
  if (due && ["today", "overdue", "week"].includes(due.trim().toLowerCase())) {
    result.due = due.trim().toLowerCase();
  }

  const unblocked = params.get("unblocked");
  const blocked = params.get("blocked");
  const hasUnblocked = unblocked === "1" || unblocked === "true";
  const hasBlocked = blocked === "1" || blocked === "true";
  if (hasBlocked && hasUnblocked) {
    let last: "blocked" | "unblocked" = "blocked";
    for (const [k, v] of params.entries()) {
      if (k === "blocked" && (v === "1" || v === "true")) last = "blocked";
      else if (k === "unblocked" && (v === "1" || v === "true")) last = "unblocked";
    }
    if (last === "blocked") result.blocked = true;
    else result.unblocked = true;
  } else if (hasBlocked) {
    result.blocked = true;
  } else if (hasUnblocked) {
    result.unblocked = true;
  }

  const stalled = params.get("stalled");
  if (stalled === "1" || stalled === "true") {
    result.stalled = true;
  }

  const attention = params.get("attention");
  if (attention === "1" || attention === "true") {
    result.attention = true;
  }

  const q = params.get("q");
  if (q && q.trim() !== "") {
    result.q = q.trim();
  }

  const sort = params.get("sort");
  if (sort && isSortKey(sort) && sort !== "") {
    result.sort = sort;
  }

  const dir = params.get("dir");
  if (dir === "asc" || dir === "desc") {
    result.dir = dir;
  }

  return result;
}

/** Serialize board state into a URL query string (with leading '?'). */
export function buildBoardQueryString(params: BoardQueryParams): string {
  const q = new URLSearchParams();
  if (params.project && params.project.trim() !== "") {
    q.set("project", params.project.trim());
  }
  if (params.status) {
    const norm = params.status.trim().toLowerCase();
    if ((STATUSES as readonly string[]).includes(norm)) {
      q.set("status", norm);
    }
  }
  if (params.priority !== undefined && params.priority !== "" && params.priority !== "all") {
    q.set("priority", params.priority);
  }
  if (params.owner && params.owner.trim() !== "" && params.owner !== "all") {
    q.set("owner", params.owner.trim());
  }
  if (params.due && ["today", "overdue", "week"].includes(params.due.trim().toLowerCase())) {
    q.set("due", params.due.trim().toLowerCase());
  }
  if (params.blocked) {
    q.set("blocked", "1");
  } else if (params.unblocked) {
    q.set("unblocked", "1");
  }
  if (params.stalled) {
    q.set("stalled", "1");
  }
  if (params.attention) {
    q.set("attention", "1");
  }
  if (params.q && params.q.trim() !== "") {
    q.set("q", params.q.trim());
  }
  if (params.sort && isSortKey(params.sort)) {
    q.set("sort", params.sort);
    if (params.dir === "asc" || params.dir === "desc") {
      q.set("dir", params.dir);
    }
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

// ---------------------------------------------------------------- detail surface
/** Tailwind's `md` breakpoint (px). Below this width the task-detail view has
 *  no room for the centered modal, so card clicks open the FULL-PAGE route
 *  (/tasks/<id>) instead — same code path "Open full page" uses, keeping the
 *  URL deep-linkable and browser-back consistent on phones. */
export const MOBILE_DETAIL_MAX_WIDTH = 768;

/** True when the current viewport is below the md breakpoint (mobile).
 *  `width` defaults to window.innerWidth; passed explicitly in unit tests.
 *  Returns false without a window (Node) so the modal/desktop path is used. */
export function isMobileDetailViewport(width?: number): boolean {
  const w = width ?? (typeof window !== "undefined" ? window.innerWidth : Number.POSITIVE_INFINITY);
  return w < MOBILE_DETAIL_MAX_WIDTH;
}
