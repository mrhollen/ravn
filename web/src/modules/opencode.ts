// OpenCode options module — Phase 2 batch B slice "opencode" of the frontend
// modularization epic. Owns, moved from app.ts:
//   1. the cached GET /projects/{id}/opencode/options state read surface —
//      availableOpenCodeAgents / availableOpenCodeModels and the variant
//      derivation (exact ref or model_id match, resolved_model fallback,
//      deduplicated union across all models otherwise);
//   2. fetchOpenCodeOptions — the per-project cache gate, the no-server-URL
//      early-out (an unsaved URL in the open settings modal still counts), and
//      the silent catch (a refused / offline OpenCode must not toast).
// Every body moved verbatim from app.ts except `this.` → closure/deps renames
// (see docs/FRONTEND_ARCHITECTURE.md §2).
//
// State-ownership note (Alpine constraint): every template-bound name
// (openCodeOptions, openCodeOptionsProjectId, loadingOpenCodeOptions) still
// lives on the flat component root, touched here exclusively through the typed
// accessors on {@link OpenCodeDeps}. The cache is plain root state — no
// closure-internal fields exist in this module.

import { api } from "../api";
import type {
  OpenCodeAgentOption,
  OpenCodeModelOption,
  OpenCodeOptionsResponse,
  ProjectWithCounts,
} from "../types";

// ----------------------------------------------------------------------- deps
export interface OpenCodeDeps {
  // state-in
  selectedProjectId: () => string;
  projects: () => ProjectWithCounts[];
  /** Whether the project-settings modal is open (part of the no-server-URL
   *  gate — an unsaved URL must still allow a fetch). */
  showProjectSettings: () => boolean;
  /** opencode_server_url of the settings form (the gate lets an unsaved URL
   *  count while the modal is open). */
  projectSettingsFormOpencodeUrl: () => string;
  openCodeOptions: () => OpenCodeOptionsResponse | null;
  openCodeOptionsProjectId: () => string;
  // state-out
  setOpenCodeOptions: (o: OpenCodeOptionsResponse | null) => void;
  setOpenCodeOptionsProjectId: (pid: string) => void;
  setLoadingOpenCodeOptions: (v: boolean) => void;
}

// ------------------------------------------------------------------- helpers
/** Variant list for the target model, or the deduplicated union across every
 *  model when no single model matches — the derivation tail of
 *  availableOpenCodeVariantsFor, exported for direct unit testing. */
export function variantsForModels(models: OpenCodeModelOption[], targetRef: string): string[] {
  if (targetRef) {
    const found = models.find((m) => m.ref === targetRef || m.model_id === targetRef);
    if (found && Array.isArray(found.variants)) {
      return [...new Set(found.variants)];
    }
  }
  const union = new Set<string>();
  for (const m of models) {
    if (Array.isArray(m.variants)) {
      for (const v of m.variants) {
        if (v) union.add(v);
      }
    }
  }
  return [...union];
}

// --------------------------------------------------------------------- module
export function createOpenCodeModule(deps: OpenCodeDeps) {
  function availableOpenCodeAgents(): OpenCodeAgentOption[] {
    return deps.openCodeOptions()?.agents ?? [];
  }

  function availableOpenCodeModels(): OpenCodeModelOption[] {
    return deps.openCodeOptions()?.models ?? [];
  }

  function availableOpenCodeVariantsFor(modelRef?: string): string[] {
    const models = availableOpenCodeModels();
    if (!models.length) return [];
    const target = (modelRef ?? "").trim() || (deps.openCodeOptions()?.resolved_model ?? "").trim();
    return variantsForModels(models, target);
  }

  async function fetchOpenCodeOptions(projectId?: string, force = false): Promise<OpenCodeOptionsResponse | null> {
    const pid = projectId || deps.selectedProjectId();
    if (!pid) return null;
    const p = deps.projects().find((x) => x.id === pid);
    if (p && !p.opencode_server_url && (!deps.showProjectSettings() || !deps.projectSettingsFormOpencodeUrl())) {
      deps.setOpenCodeOptions(null);
      return null;
    }
    if (!force && deps.openCodeOptions() && deps.openCodeOptionsProjectId() === pid) {
      return deps.openCodeOptions();
    }
    deps.setLoadingOpenCodeOptions(true);
    try {
      const res = await api.getOpenCodeOptions(pid);
      deps.setOpenCodeOptions(res);
      deps.setOpenCodeOptionsProjectId(pid);
      return res;
    } catch {
      deps.setOpenCodeOptions(null);
      return null;
    } finally {
      deps.setLoadingOpenCodeOptions(false);
    }
  }

  return { availableOpenCodeAgents, availableOpenCodeModels, availableOpenCodeVariantsFor, fetchOpenCodeOptions };
}

export type OpenCodeModule = ReturnType<typeof createOpenCodeModule>;
