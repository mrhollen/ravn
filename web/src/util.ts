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

/** "Sep 30" — with the year when it isn't the current one. */
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

/** Due label for cards: date (+ year). */
export function dueLabel(iso: string | undefined): string {
  return iso ? fmtDate(iso) : "";
}

export function isOverdue(t: Pick<TaskSummary, "due_at" | "status">): boolean {
  if (!t.due_at || t.status === "done") return false;
  const due = Date.parse(t.due_at);
  if (Number.isNaN(due)) return false;
  return due < Date.now();
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
