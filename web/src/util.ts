// Small presentation helpers: relative times, due dates, priority colors,
// actor labels. All pure functions — no state.

import { STATUS_LABEL } from "./types";
import type { ActorRef, Status, TaskSummary } from "./types";

export function statusLabel(s: Status): string {
  return STATUS_LABEL[s] ?? s;
}

export function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean);
  if (parts.length === 0) return "?";
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase();
  return (parts[0][0] + parts[1][0]).toUpperCase();
}

/** "ada" or "runner-2 (agent)" — empty/anonymous actors degrade to the type. */
export function actorLabel(a?: ActorRef | null): string {
  if (!a) return "";
  const name = a.name?.trim();
  if (!name) return a.type ? `${a.type} (no name)` : "unknown";
  return a.type === "agent" ? `${name} (agent)` : name;
}

export function relTime(iso: string | undefined): string {
  if (!iso) return "";
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return iso;
  const diff = Date.now() - then;
  const s = Math.round(diff / 1000);
  if (s < 45) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.round(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.round(h / 24);
  if (d < 14) return `${d}d ago`;
  return fmtDate(iso);
}

/** "Sep 30" — with the year when it isn't the current one. LOCAL time: use for
 *  point-in-time timestamps (created_at/updated_at/events), NOT for due dates,
 *  which are calendar days (see fmtDueDate). */
export function fmtDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const opts: Intl.DateTimeFormatOptions =
    d.getFullYear() === new Date().getFullYear()
      ? { month: "short", day: "numeric" }
      : { month: "short", day: "numeric", year: "numeric" };
  return d.toLocaleDateString(undefined, opts);
}

export function timeOfDay(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

// ------------------------------------------------------------------- due dates
// Due dates are CALENDAR days, not instants. This app stores them as RFC3339
// UTC midnight (see dateInputToIso), so the encoded day is ALWAYS readable via
// the UTC getters — in every browser timezone. Every consumer that treats a
// due date as a day goes through these helpers so cards, the editor and
// re-saves always agree.

function pad2(n: number): string {
  return String(n).padStart(2, "0");
}

/** Calendar day of a stored due_at as "YYYY-MM-DD" (UTC components). "" if the
 *  value is not parseable. For our own T00:00:00Z values this is exactly the
 *  day that was picked in the date input — regardless of browser TZ. */
export function dueCalendarDate(dueAt: string): string {
  const d = new Date(dueAt);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getUTCFullYear()}-${pad2(d.getUTCMonth() + 1)}-${pad2(d.getUTCDate())}`;
}

/** Today's LOCAL calendar day as "YYYY-MM-DD" — the day the user is on right now. */
export function localCalendarToday(): string {
  const n = new Date();
  return `${n.getFullYear()}-${pad2(n.getMonth() + 1)}-${pad2(n.getDate())}`;
}

/** Calendar-day label for DUE dates: "Oct 1" (+ year when not the current one),
 *  identical in every browser timezone. */
export function fmtDueDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  // Re-anchor on an instant whose UTC fields ARE the calendar date, then format
  // in UTC — immune to the browser's own offset.
  const anchor = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate()));
  const opts: Intl.DateTimeFormatOptions =
    d.getUTCFullYear() === new Date().getFullYear()
      ? { month: "short", day: "numeric" }
      : { month: "short", day: "numeric", year: "numeric" };
  return anchor.toLocaleDateString(undefined, { ...opts, timeZone: "UTC" });
}

/** Due label for cards: date (+ year), timezone-stable. */
export function dueLabel(iso: string | undefined): string {
  return iso ? fmtDueDate(iso) : "";
}

/** Overdue ⇔ the due day is strictly before today's LOCAL calendar day — a task
 *  due today stays on time for its whole due day and only flags from the next
 *  local day onward. */
export function isOverdue(t: Pick<TaskSummary, "due_at" | "status">): boolean {
  if (!t.due_at || t.status === "done") return false;
  const due = dueCalendarDate(t.due_at);
  if (!due) return false;
  return due < localCalendarToday();
}

function humanDuration(ms: number): string {
  const m = Math.max(1, Math.round(ms / 60000));
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60 ? `${m % 60}m` : ""}`.trim();
  return `${Math.floor(h / 24)}d`;
}

/** Short claim-lease hint for cards: "until 15:53" or "lease expired". */
export function claimHint(t: Pick<TaskSummary, "claimed_by" | "claim_expires_at">): string {
  if (!t.claimed_by || !t.claim_expires_at) return t.claimed_by ? "(no expiry)" : "";
  const diff = Date.parse(t.claim_expires_at) - Date.now();
  if (Number.isNaN(diff)) return "";
  if (diff <= 0) return "lease expired";
  return `until ${timeOfDay(t.claim_expires_at)} (${humanDuration(diff)})`;
}

/** Whether a claim lease has EXPIRED. Mirrors EXACTLY the "lease expired" branch of
 *  {@link claimHint} (claimed AND expiring at/before `nowMs`) so the muted "expired"
 *  styling and the "lease expired" text can never disagree: unclaimed, or a claim
 *  with no recorded expiry, is never "expired"; an unparseable expiry is not. */
export function isClaimExpired(
  t: Pick<TaskSummary, "claimed_by" | "claim_expires_at">,
  nowMs = Date.now(),
): boolean {
  if (!t.claimed_by || !t.claim_expires_at) return false; // unclaimed, or claimed-without-expiry => never "expired"
  const diff = Date.parse(t.claim_expires_at) - nowMs;
  if (Number.isNaN(diff)) return false;
  return diff <= 0;
}

/** Idle threshold for stalled detection: a `doing` task whose last update is
 *  OLDER than this window counts as abandoned even when its claim state alone
 *  doesn't say so (e.g. never claimed, or lease long gone). */
export const STALLED_IDLE_MS = 60 * 60 * 1000; // 60 minutes

/** "Stalled-expired" predicate (epic 01M5XSNR8VWZTP66GT7ABNSJC1): a task is
 *  stalled when it sits in `doing` AND its claim lease has lapsed
 *  ({@link isClaimExpired}) OR it has not been touched for longer than
 *  {@link STALLED_IDLE_MS} (idle, from `updated_at`). todo/done tasks are
 *  NEVER stalled. `nowMs` is injectable so the live UI (app.nowTick) and tests
 *  evaluate the same predicate deterministically — mirroring isClaimExpired(). */
export function isStalledExpired(
  t: Pick<TaskSummary, "status" | "updated_at" | "claimed_by" | "claim_expires_at">,
  nowMs = Date.now(),
): boolean {
  if (t.status !== "doing") return false;
  if (isClaimExpired(t, nowMs)) return true;
  const updated = Date.parse(t.updated_at);
  if (Number.isNaN(updated)) return false; // unparseable timestamp → can't establish idle age
  return nowMs - updated > STALLED_IDLE_MS;
}

/** "Needs attention" predicate (epic child 01M5XTAYKJGP878ZEZB7TJW4ZB): a task
 *  needs attention when it is OPEN (`todo`/`doing` — done tasks are NEVER
 *  attention) AND its newest comment's kind is `question` (awaiting a human
 *  answer) or `handoff` (awaiting review). Routine kinds (progress/note/
 *  decision) and threads with no comments at all (field omitted) never match. */
export function needsAttention(
  t: Pick<TaskSummary, "status" | "last_comment_kind">,
): boolean {
  if (t.status === "done") return false;
  return t.last_comment_kind === "question" || t.last_comment_kind === "handoff";
}

/** Real-time MM:SS countdown until lease expiry for the claim capsule. */
export function formatClaimCountdown(claimExpiresAt?: string | null, nowMs = Date.now()): string {
  if (!claimExpiresAt) return "";
  const target = Date.parse(claimExpiresAt);
  if (Number.isNaN(target)) return "";
  const diffMs = target - nowMs;
  if (diffMs <= 0) return "0:00";
  const totalSec = Math.floor(diffMs / 1000);
  const m = Math.floor(totalSec / 60);
  const s = totalSec % 60;
  return `${m}:${s < 10 ? "0" : ""}${s}`;
}

/** Priority badge classes — p0 (red/urgent) through p5 (zinc/trivial). */
const PRIORITY_CLASSES = [
  "bg-red-100 text-red-700 ring-red-200 dark:bg-red-950 dark:text-red-300 dark:ring-red-800",
  "bg-orange-100 text-orange-700 ring-orange-200 dark:bg-orange-950 dark:text-orange-300 dark:ring-orange-800",
  "bg-amber-100 text-amber-800 ring-amber-200 dark:bg-amber-950 dark:text-amber-300 dark:ring-amber-800",
  "bg-lime-100 text-lime-800 ring-lime-200 dark:bg-lime-950 dark:text-lime-300 dark:ring-lime-800",
  "bg-sky-100 text-sky-700 ring-sky-200 dark:bg-sky-950 dark:text-sky-300 dark:ring-sky-800",
  "bg-zinc-100 text-zinc-600 ring-zinc-300 dark:bg-zinc-800 dark:text-zinc-300 dark:ring-zinc-700",
];

export function priorityClass(p: number): string {
  const i = Math.max(0, Math.min(5, Math.trunc(p)));
  return PRIORITY_CLASSES[i] ?? PRIORITY_CLASSES[5];
}

const PRIORITY_NAMES = ["urgent", "high", "medium", "low", "background", "trivial"];

export function priorityName(p: number): string {
  const i = Math.max(0, Math.min(5, Math.trunc(p)));
  return PRIORITY_NAMES[i] ?? "";
}

const COMMENT_KIND_CLASSES: Record<string, string> = {
  progress: "bg-sky-100 text-sky-700 dark:bg-sky-950 dark:text-sky-300",
  question: "bg-violet-100 text-violet-700 dark:bg-violet-950 dark:text-violet-300",
  decision: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
  handoff: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
  note: "bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300",
};

export function commentKindClass(kind: string): string {
  return COMMENT_KIND_CLASSES[kind] ?? "bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300";
}

const ARTIFACT_KIND_CLASSES: Record<string, string> = {
  url: "bg-sky-100 text-sky-700 dark:bg-sky-950 dark:text-sky-300",
  log: "bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300",
  screenshot: "bg-pink-100 text-pink-700 dark:bg-pink-950 dark:text-pink-300",
  "command-output": "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
  trace: "bg-indigo-100 text-indigo-700 dark:bg-indigo-950 dark:text-indigo-300",
  other: "bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300",
};

export function artifactKindClass(kind: string): string {
  return ARTIFACT_KIND_CLASSES[kind] ?? "bg-zinc-100 text-zinc-600 dark:bg-zinc-800 dark:text-zinc-300";
}

/** Truncated ULID for display ("01J3ZK8W…"). */
export function shortId(id: string): string {
  return id.length > 10 ? `${id.slice(0, 8)}…` : id;
}

/** <input type="date"> value ("2026-10-01") → RFC3339 UTC midnight, or undefined. */
export function dateInputToIso(value: string): string | undefined {
  const v = value.trim();
  if (!v) return undefined;
  const iso = `${v}T00:00:00Z`;
  return Number.isNaN(Date.parse(iso)) ? undefined : iso;
}

/** Acceptance-criteria textarea (one per line) → non-empty trimmed lines. */
export function splitLines(s: string): string[] {
  return s
    .split(/\r?\n/)
    .map((l) => l.trim())
    .filter(Boolean);
}

// ----------------------------------------------------------- safe storage
// Safe localStorage wrapper with in-memory map fallback.
// Sandboxed iframes, Safari private browsing, or storage-disabled browsers throw
// DOMException: SecurityError upon accessing window.localStorage. This wrapper
// guarantees that reads and writes degrade gracefully to an in-memory Map rather
// than throwing and halting application initialization.
const memoryStorage = new Map<string, string>();

function getLocalStorage(): Storage | null {
  try {
    return typeof window !== "undefined" && window.localStorage ? window.localStorage : null;
  } catch {
    return null;
  }
}

export const safeStorage = {
  getItem(key: string): string | null {
    try {
      const ls = getLocalStorage();
      if (ls) {
        const val = ls.getItem(key);
        if (val !== null) return val;
      }
    } catch {
      /* storage blocked */
    }
    return memoryStorage.get(key) ?? null;
  },

  setItem(key: string, value: string): void {
    try {
      const ls = getLocalStorage();
      if (ls) {
        ls.setItem(key, value);
        memoryStorage.set(key, value);
        return;
      }
    } catch {
      /* storage blocked or quota exceeded */
    }
    memoryStorage.set(key, value);
  },

  removeItem(key: string): void {
    try {
      const ls = getLocalStorage();
      if (ls) {
        ls.removeItem(key);
      }
    } catch {
      /* storage blocked */
    }
    memoryStorage.delete(key);
  },

  clear(): void {
    try {
      const ls = getLocalStorage();
      if (ls) {
        ls.clear();
      }
    } catch {
      /* storage blocked */
    }
    memoryStorage.clear();
  },
};

