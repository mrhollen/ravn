// Unit tests for web/src/modules/opencode.ts (Phase 2 batch B slice "opencode").
// Drives createOpenCodeModule() with a mutable stub state and monkey-patched
// api.getOpenCodeOptions — no DOM, no network. The root-level surface (the
// delegation shims) stays covered in test/app.test.ts; this file covers the
// moved internals: the agents/models read surface, the variant derivation
// (exact ref / model_id match, resolved_model fallback, deduplicated union),
// and fetchOpenCodeOptions — id gate, no-server-URL gate (with the open-modal
// unsaved-URL exception), per-project cache + force, silent error catch.

import test from "node:test";
import assert from "node:assert/strict";
import { createOpenCodeModule, variantsForModels } from "../src/modules/opencode";
import type { OpenCodeDeps } from "../src/modules/opencode";
import { api } from "../src/api";
import type { OpenCodeModelOption, OpenCodeOptionsResponse, ProjectWithCounts } from "../src/types";

// ------------------------------------------------------------------- harness
function options(partial: Partial<OpenCodeOptionsResponse> = {}): OpenCodeOptionsResponse {
  return {
    reachable: true,
    resolved_agent: "build",
    resolved_model: "",
    resolved_variant: "",
    agents: [],
    models: [],
    ...partial,
  };
}

function project(id: string, name: string, extra: Record<string, unknown> = {}): ProjectWithCounts {
  return { id, name, description: "", counts: { todo: 0, doing: 0, done: 0 }, archived: false, ...extra } as ProjectWithCounts;
}

/** One mutable state object wired into the deps accessors — the module reads
 *  and writes exactly through those, so assertions read straight off `state`. */
function makeHarness(init?: Record<string, any>) {
  const state: Record<string, any> = {
    selectedProjectId: "",
    projects: [] as ProjectWithCounts[],
    showProjectSettings: false,
    projectSettingsFormOpencodeUrl: "",
    openCodeOptions: null as OpenCodeOptionsResponse | null,
    openCodeOptionsProjectId: "",
    loadingOpenCodeOptions: false,
  };
  Object.assign(state, init);

  const deps: OpenCodeDeps = {
    selectedProjectId: () => state.selectedProjectId,
    projects: () => state.projects,
    showProjectSettings: () => state.showProjectSettings,
    projectSettingsFormOpencodeUrl: () => state.projectSettingsFormOpencodeUrl,
    openCodeOptions: () => state.openCodeOptions,
    openCodeOptionsProjectId: () => state.openCodeOptionsProjectId,
    setOpenCodeOptions: (o) => { state.openCodeOptions = o; },
    setOpenCodeOptionsProjectId: (pid) => { state.openCodeOptionsProjectId = pid; },
    setLoadingOpenCodeOptions: (v) => { state.loadingOpenCodeOptions = v; },
  };

  const mod = createOpenCodeModule(deps);
  return { state, mod };
}

/** Monkey-patch api.getOpenCodeOptions for the duration of a test. */
function patchGetOptions(impl: (pid: string) => Promise<unknown>) {
  const orig = (api as any).getOpenCodeOptions;
  (api as any).getOpenCodeOptions = impl;
  return () => { (api as any).getOpenCodeOptions = orig; };
}

const MODELS: OpenCodeModelOption[] = [
  { ref: "provider-a/model-1", provider_id: "provider-a", model_id: "model-1", model_name: "Model One", variants: ["low", "medium", "high"] },
  { ref: "provider-b/model-2", provider_id: "provider-b", model_id: "model-2", model_name: "Model Two", variants: ["high", "flash"] },
];

// ------------------------------------------------------------ pure derivation
test("variantsForModels: exact ref match returns that model's variants", () => {
  assert.deepEqual(variantsForModels(MODELS, "provider-a/model-1"), ["low", "medium", "high"]);
});

test("variantsForModels: model_id match when the ref does not match", () => {
  assert.deepEqual(variantsForModels(MODELS, "model-2"), ["high", "flash"]);
});

test("variantsForModels: exact match deduplicates but keeps empty strings verbatim (behavior parity)", () => {
  const models: OpenCodeModelOption[] = [
    { ref: "p/m", provider_id: "p", model_id: "m", model_name: "M", variants: ["a", "a", "", "b"] },
  ];
  assert.deepEqual(variantsForModels(models, "p/m"), ["a", "", "b"]);
});

test("variantsForModels: union path skips falsy variant entries", () => {
  const models: OpenCodeModelOption[] = [
    { ref: "p/a", provider_id: "p", model_id: "a", model_name: "A", variants: ["x", ""] },
    { ref: "p/b", provider_id: "p", model_id: "b", model_name: "B", variants: ["y"] },
  ];
  assert.deepEqual(variantsForModels(models, "nope"), ["x", "y"]);
});

test("variantsForModels: unknown target → deduplicated union across all models (order of first appearance)", () => {
  // "high" appears in both models; the union must keep it once.
  assert.deepEqual(variantsForModels(MODELS, "provider-x/none"), ["low", "medium", "high", "flash"]);
});

test("variantsForModels: empty target → union (resolved_model fallback is applied by the caller)", () => {
  assert.deepEqual(variantsForModels(MODELS, ""), ["low", "medium", "high", "flash"]);
});

test("availableOpenCodeAgents / availableOpenCodeModels: passthrough and empty defaults", () => {
  const { mod } = makeHarness();
  assert.deepEqual(mod.availableOpenCodeAgents(), []);
  assert.deepEqual(mod.availableOpenCodeModels(), []);
  assert.deepEqual(mod.availableOpenCodeVariantsFor("any"), []);

  const h2 = makeHarness({ openCodeOptions: options({ agents: [{ name: "build", mode: "primary" }], models: MODELS }) });
  assert.equal(h2.mod.availableOpenCodeAgents().length, 1);
  assert.equal(h2.mod.availableOpenCodeModels().length, 2);
});

test("availableOpenCodeVariantsFor: empty/undefined ref falls back to resolved_model", () => {
  const h = makeHarness({
    openCodeOptions: options({ resolved_model: "provider-a/model-1", models: MODELS }),
  });
  assert.deepEqual(h.mod.availableOpenCodeVariantsFor(""), ["low", "medium", "high"]);
  assert.deepEqual(h.mod.availableOpenCodeVariantsFor(undefined), ["low", "medium", "high"]);
});

test("availableOpenCodeVariantsFor: unknown ref returns the deduplicated union", () => {
  const h = makeHarness({ openCodeOptions: options({ models: MODELS }) });
  assert.deepEqual(h.mod.availableOpenCodeVariantsFor("unknown-ref"), ["low", "medium", "high", "flash"]);
});

// --------------------------------------------------------------- fetch paths
test("fetchOpenCodeOptions: no project id (arg or selection) → null, no api call", async () => {
  let called = false;
  const restore = patchGetOptions(async () => { called = true; return options(); });
  try {
    const { state, mod } = makeHarness();
    const res = await mod.fetchOpenCodeOptions();
    assert.equal(res, null);
    assert.equal(called, false);
    assert.equal(state.loadingOpenCodeOptions, false);
  } finally {
    restore();
  }
});

test("fetchOpenCodeOptions: no-server-URL gate — closed modal clears options and returns null without an api call", async () => {
  let called = false;
  const restore = patchGetOptions(async () => { called = true; return options(); });
  try {
    const h = makeHarness({
      projects: [project("p1", "P1")], // no opencode_server_url
      openCodeOptions: options({ agents: [{ name: "stale", mode: "primary" }] }),
    });
    const res = await h.mod.fetchOpenCodeOptions("p1");
    assert.equal(res, null);
    assert.equal(called, false);
    assert.equal(h.state.openCodeOptions, null, "stale cache must be cleared by the gate");
  } finally {
    restore();
  }
});

test("fetchOpenCodeOptions: gate passes when the modal is open with an unsaved URL in the form", async () => {
  let called = false;
  const restore = patchGetOptions(async () => { called = true; return options({ reachable: true }); });
  try {
    const h = makeHarness({
      projects: [project("p1", "P1")], // saved row has no url…
      showProjectSettings: true,
      projectSettingsFormOpencodeUrl: "http://opencode.local:4096", // …but the form does
    });
    const res = await h.mod.fetchOpenCodeOptions("p1");
    assert.equal(called, true);
    assert.equal(res?.reachable, true);
  } finally {
    restore();
  }
});

test("fetchOpenCodeOptions: gate passes when the project row already has a saved URL", async () => {
  let called = false;
  const restore = patchGetOptions(async () => { called = true; return options({ reachable: true }); });
  try {
    const h = makeHarness({
      projects: [project("p1", "P1", { opencode_server_url: "http://saved:4096" })],
    });
    const res = await h.mod.fetchOpenCodeOptions("p1");
    assert.equal(called, true);
    assert.equal(res?.reachable, true);
  } finally {
    restore();
  }
});

test("fetchOpenCodeOptions: unknown pid (not in the projects list) skips the gate and fetches", async () => {
  let called = false;
  const restore = patchGetOptions(async () => { called = true; return options({ reachable: true }); });
  try {
    const h = makeHarness({ projects: [project("other", "Other")] });
    const res = await h.mod.fetchOpenCodeOptions("p-missing");
    assert.equal(called, true);
    assert.equal(res?.reachable, true);
  } finally {
    restore();
  }
});

test("fetchOpenCodeOptions: success stores the response + owning project id and returns it", async () => {
  const res = options({ reachable: true });
  const restore = patchGetOptions(async () => res);
  try {
    const h = makeHarness({ projects: [project("p1", "P1", { opencode_server_url: "http://x" })] });
    const out = await h.mod.fetchOpenCodeOptions("p1");
    assert.equal(out, res);
    assert.equal(h.state.openCodeOptions, res);
    assert.equal(h.state.openCodeOptionsProjectId, "p1");
    assert.equal(h.state.loadingOpenCodeOptions, false);
  } finally {
    restore();
  }
});

test("fetchOpenCodeOptions: error is silent — null result, cache cleared, loading off", async () => {
  const restore = patchGetOptions(async () => { throw new Error("connection refused"); });
  try {
    const h = makeHarness({ projects: [project("p1", "P1", { opencode_server_url: "http://x" })] });
    const out = await h.mod.fetchOpenCodeOptions("p1");
    assert.equal(out, null);
    assert.equal(h.state.openCodeOptions, null);
    assert.equal(h.state.loadingOpenCodeOptions, false);
  } finally {
    restore();
  }
});

test("fetchOpenCodeOptions: per-project cache — same pid without force returns the cached object without an api call", async () => {
  const first = options({ reachable: true });
  let calls = 0;
  const restore = patchGetOptions(async () => { calls++; return first; });
  try {
    const h = makeHarness({ projects: [project("p1", "P1", { opencode_server_url: "http://x" })] });
    await h.mod.fetchOpenCodeOptions("p1");
    const second = await h.mod.fetchOpenCodeOptions("p1");
    assert.equal(calls, 1);
    assert.equal(second, first, "cache hit must return the same object");
  } finally {
    restore();
  }
});

test("fetchOpenCodeOptions: force=true bypasses the cache", async () => {
  let calls = 0;
  const restore = patchGetOptions(async () => { calls++; return options({ reachable: true }); });
  try {
    const h = makeHarness({ projects: [project("p1", "P1", { opencode_server_url: "http://x" })] });
    await h.mod.fetchOpenCodeOptions("p1");
    await h.mod.fetchOpenCodeOptions("p1", true);
    assert.equal(calls, 2);
  } finally {
    restore();
  }
});

test("fetchOpenCodeOptions: a different pid refetches even with a warm cache for another project", async () => {
  let calls = 0;
  const restore = patchGetOptions(async (pid) => { calls++; return options({ reachable: true }); });
  try {
    const h = makeHarness({
      projects: [
        project("p1", "P1", { opencode_server_url: "http://a" }),
        project("p2", "P2", { opencode_server_url: "http://b" }),
      ],
    });
    await h.mod.fetchOpenCodeOptions("p1");
    const out = await h.mod.fetchOpenCodeOptions("p2");
    assert.equal(calls, 2);
    assert.equal(h.state.openCodeOptionsProjectId, "p2");
    assert.equal(out?.reachable, true);
  } finally {
    restore();
  }
});

test("fetchOpenCodeOptions: loading flag is true while the request is in flight", async () => {
  let release!: (v: OpenCodeOptionsResponse) => void;
  const gate = new Promise<OpenCodeOptionsResponse>((resolve) => { release = resolve; });
  const restore = patchGetOptions(async () => gate);
  try {
    const h = makeHarness({ projects: [project("p1", "P1", { opencode_server_url: "http://x" })] });
    const pending = h.mod.fetchOpenCodeOptions("p1");
    assert.equal(h.state.loadingOpenCodeOptions, true, "loading on while in flight");
    release(options({ reachable: true }));
    await pending;
    assert.equal(h.state.loadingOpenCodeOptions, false, "loading off after settle");
  } finally {
    restore();
  }
});
