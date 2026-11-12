/** Acceptance-criteria drawer section — state + handlers (Phase 2, batch C).
 *
 * Extracted from app.ts as a plain factory module. The 9 bound state fields
 * are template-bound in index.html so they stay flat plain root fields on the
 * Alpine object; this module reaches them through deps and owns no closure-
 * internal state of its own (the task-edit draft snapshot is the only
 * precedent for owning derived state, and none of this behavior needs one).
 * Handlers are moved VERBATIM — `this.` rewritten to closures over the deps
 * accessors, otherwise byte-identical. app.ts keeps thin delegation shims so
 * every index.html reference (including the openTask/closeDetail cascade
 * writes of editingAcSeq/newAcText as plain fields) resolves unchanged.
 *
 * The four display helpers (acDoneCount/acRingInner/acProgressLabel/
 * completedLabel) are thin reads over the pure src/ac-math.ts functions —
 * they ride here so the section's public surface is one module, and app.test.ts
 * asserts them through the shims exactly as before.
 */
import { api, errMsg } from "../api";
import type { AcRow, ActorInput, TaskDetail } from "../types";
import { parseChecklistText } from "../util";
// Aliased imports — the local functions below keep the public names (the
// shims in app.ts call them by those exact names), so a bare import would be
// shadowed and recurse into itself. Same aliasing as app.ts used to need.
import {
  acDoneCount as acDoneFor,
  acProgressLabel as acProgressLabelFor,
  acRingInner as acRingFor,
  completedLabel as acCompletedLabelFor,
} from "../ac-math";

export interface AcsDeps {
  // -- state-in ---------------------------------------------------------
  detail: () => TaskDetail | null;
  acBusySeq: () => number;
  newAcText: () => string;
  addingAc: () => boolean;
  editingAcSeq: () => number;
  editingAcText: () => string;
  savingAcSeq: () => number;
  deletingAcSeq: () => number;
  reorderingAcs: () => boolean;
  draggedAcSeq: () => number;

  // -- state-out --------------------------------------------------------
  setDetail: (t: TaskDetail | null) => void;
  setAcBusySeq: (v: number) => void;
  setNewAcText: (v: string) => void;
  setAddingAc: (v: boolean) => void;
  setEditingAcSeq: (v: number) => void;
  setEditingAcText: (v: string) => void;
  setSavingAcSeq: (v: number) => void;
  setDeletingAcSeq: (v: number) => void;
  setReorderingAcs: (v: boolean) => void;
  setDraggedAcSeq: (v: number) => void;

  // -- hooks ------------------------------------------------------------
  toast: (msg: string, kind?: "error" | "info") => number;
  actor: () => ActorInput;
}

export function createAcsModule(deps: AcsDeps) {
  // -------------------------------------------------------- acceptance crits

  /** Toggle one criterion; the 200 response carries the updated AC row, which
   *  is merged back into detail (authoritative). `ev` is the change event —
   *  on a FAILED toggle it lets us revert the native checkbox: the click flips
   *  its .checked before we know the server said no, and ac.done is never
   *  mutated on failure, so Alpine's :checked binding has no diff to re-sync
   *  and would otherwise leave the box visually checked (split state). */
  async function toggleAc(ac: AcRow, ev?: Event): Promise<void> {
    const t = deps.detail();
    if (!t || deps.acBusySeq() !== 0) return;
    deps.setAcBusySeq(ac.seq);
    try {
      const updated = await api.toggleAc(t.id, ac.seq, deps.actor(), !ac.done);
      const d = deps.detail(); // may have been refreshed meanwhile — only patch ours
      if (d && d.id === t.id) {
        deps.setDetail({
          ...d,
          acceptance_criteria: d.acceptance_criteria.map((x) =>
            x.seq === updated.seq ? updated : x,
          ),
        });
      }
    } catch (e) {
      // ac.done is still its pre-click value — put the DOM back in line with it.
      // Duck-typed on purpose: unit tests drive this path with a plain fake
      // event object (no DOM globals exist under node:test).
      const el = ev?.target as unknown as { checked?: boolean } | null;
      if (el && typeof el.checked === "boolean") el.checked = ac.done;
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setAcBusySeq(0);
    }
  }

  async function addAc(): Promise<void> {
    const t = deps.detail();
    const lines = parseChecklistText(deps.newAcText());
    if (!t || lines.length === 0 || deps.addingAc()) return;
    deps.setAddingAc(true);
    try {
      for (const line of lines) {
        const fresh = await api.addAc(t.id, line, deps.actor());
        const d = deps.detail();
        if (d && d.id === t.id) {
          deps.setDetail({
            ...d,
            acceptance_criteria: [...d.acceptance_criteria, fresh],
          });
        }
      }
      deps.setNewAcText("");
      if (lines.length > 1) {
        deps.toast(`${lines.length} criteria added`);
      }
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setAddingAc(false);
    }
  }

  async function handleAcPaste(e: ClipboardEvent): Promise<void> {
    const raw = e.clipboardData?.getData("text") || "";
    if (!raw.includes("\n")) return;
    e.preventDefault();
    const lines = parseChecklistText(raw);
    if (lines.length === 0) return;
    const t = deps.detail();
    if (!t || deps.addingAc()) return;
    deps.setAddingAc(true);
    try {
      for (const line of lines) {
        const fresh = await api.addAc(t.id, line, deps.actor());
        const d = deps.detail();
        if (d && d.id === t.id) {
          deps.setDetail({
            ...d,
            acceptance_criteria: [...d.acceptance_criteria, fresh],
          });
        }
      }
      deps.setNewAcText("");
      deps.toast(`${lines.length} criteria added`);
    } catch (err) {
      deps.toast(errMsg(err), "error");
    } finally {
      deps.setAddingAc(false);
    }
  }

  async function moveAc(seq: number, direction: "up" | "down"): Promise<void> {
    const t = deps.detail();
    if (!t || deps.reorderingAcs()) return;
    const acs = [...t.acceptance_criteria];
    const index = acs.findIndex((x) => x.seq === seq);
    if (index === -1) return;
    const targetIndex = direction === "up" ? index - 1 : index + 1;
    if (targetIndex < 0 || targetIndex >= acs.length) return;

    const temp = acs[index];
    acs[index] = acs[targetIndex];
    acs[targetIndex] = temp;

    const newSeqs = acs.map((x) => x.seq);
    deps.setReorderingAcs(true);
    try {
      const updated = await api.reorderAcs(t.id, newSeqs, deps.actor());
      const d = deps.detail();
      if (d && d.id === t.id) {
        deps.setDetail({
          ...d,
          acceptance_criteria: updated,
        });
      }
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setReorderingAcs(false);
    }
  }

  async function dropAc(targetSeq: number): Promise<void> {
    const sourceSeq = deps.draggedAcSeq();
    deps.setDraggedAcSeq(0);
    if (!sourceSeq || sourceSeq === targetSeq) return;
    const t = deps.detail();
    if (!t || deps.reorderingAcs()) return;
    const acs = [...t.acceptance_criteria];
    const sourceIdx = acs.findIndex((x) => x.seq === sourceSeq);
    const targetIdx = acs.findIndex((x) => x.seq === targetSeq);
    if (sourceIdx === -1 || targetIdx === -1) return;
    const [item] = acs.splice(sourceIdx, 1);
    acs.splice(targetIdx, 0, item);
    const newSeqs = acs.map((x) => x.seq);
    deps.setReorderingAcs(true);
    try {
      const updated = await api.reorderAcs(t.id, newSeqs, deps.actor());
      const d = deps.detail();
      if (d && d.id === t.id) {
        deps.setDetail({
          ...d,
          acceptance_criteria: updated,
        });
      }
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setReorderingAcs(false);
    }
  }

  function startEditingAc(ac: AcRow): void {
    deps.setEditingAcSeq(ac.seq);
    deps.setEditingAcText(ac.text);
  }

  function cancelEditingAc(): void {
    deps.setEditingAcSeq(0);
    deps.setEditingAcText("");
  }

  async function saveAcEdit(seq: number): Promise<void> {
    const t = deps.detail();
    const text = deps.editingAcText().trim();
    if (!t || !text || deps.savingAcSeq() !== 0) return;
    deps.setSavingAcSeq(seq);
    try {
      const updated = await api.editAcText(t.id, seq, text, deps.actor());
      const d = deps.detail();
      if (d && d.id === t.id) {
        deps.setDetail({
          ...d,
          acceptance_criteria: d.acceptance_criteria.map((x) =>
            x.seq === seq ? updated : x,
          ),
        });
      }
      deps.setEditingAcSeq(0);
      deps.setEditingAcText("");
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setSavingAcSeq(0);
    }
  }

  async function deleteAc(seq: number): Promise<void> {
    const t = deps.detail();
    if (!t || deps.deletingAcSeq() !== 0) return;
    deps.setDeletingAcSeq(seq);
    try {
      await api.deleteAc(t.id, seq, deps.actor());
      const d = deps.detail();
      if (d && d.id === t.id) {
        deps.setDetail({
          ...d,
          acceptance_criteria: d.acceptance_criteria.filter((x) => x.seq !== seq),
        });
      }
      if (deps.editingAcSeq() === seq) {
        cancelEditingAc();
      }
      deps.toast("Acceptance criterion removed");
    } catch (e) {
      deps.toast(errMsg(e), "error");
    } finally {
      deps.setDeletingAcSeq(0);
    }
  }

  function acDoneCount(): number {
    return acDoneFor(deps.detail()?.acceptance_criteria ?? []);
  }

  /** AC progress ring inner SVG (geometry + slot math in src/ac-math.ts). */
  function acRingInner(): string {
    return acRingFor(deps.detail()?.acceptance_criteria ?? []);
  }

  function acProgressLabel(): string {
    return acProgressLabelFor(deps.detail()?.acceptance_criteria ?? []);
  }

  function completedLabel(ac: AcRow): string {
    return acCompletedLabelFor(ac);
  }

  return {
    toggleAc,
    addAc,
    handleAcPaste,
    moveAc,
    dropAc,
    startEditingAc,
    cancelEditingAc,
    saveAcEdit,
    deleteAc,
    acDoneCount,
    acRingInner,
    acProgressLabel,
    completedLabel,
  };
}

export type AcsModule = ReturnType<typeof createAcsModule>;
