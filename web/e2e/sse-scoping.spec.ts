import { test, expect } from './fixtures';

/**
 * Multi-user epic T5 — SSE per-actor scoping (end-to-end proof, AC1/AC4).
 *
 * Boots a DEDICATED ravn-server (isolatedServer) in membership mode with multiuser auth,
 * then — entirely through the HTTP API (Node `fetch`, no UI) — proves that:
 *   1. a NON-MEMBER of the target project is rejected with 403 and receives NO event
 *      frames (not even replayable ones) on BOTH streaming endpoints; and
 *   2. the project OWNER streams the identical endpoint/cursor normally (200
 *      text/event-stream), i.e. members/global users are unaffected.
 *
 * The same (project, cursor) pair getting opposite outcomes is only possible because of the
 * T5 visibility gate on `streamEvents` — which resolves identity and enforces project
 * visibility BEFORE opening the stream or replaying a single event. This runs against the
 * real ravn-server binary with RAVN_AUTH=multiuser + RAVN_SCOPING=membership, mirroring the
 * Go integration test in internal/interface/http/events_scoping_test.go at the E2E layer.
 */

const SHARED_TOKEN = 'e2e-sse-scoping-operator-token';

test.use({ isolatedServerEnv: { RAVN_AUTH: 'multiuser', RAVN_TOKEN: SHARED_TOKEN, RAVN_SCOPING: 'membership' } });

async function createAccount(base: string, name: string) {
  const res = await fetch(`${base}/api/v1/auth/accounts`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${SHARED_TOKEN}` },
    body: JSON.stringify({ name, password: `${name}-password-1234` }),
  });
  expect(res.status, `create account ${name}`).toBe(201);
}

async function login(base: string, username: string): Promise<string> {
  const res = await fetch(`${base}/api/v1/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password: `${username}-password-1234` }),
  });
  expect(res.status, `login ${username}`).toBe(200);
  const out = (await res.json()) as { token: string };
  return out.token;
}

test('T5 SSE: non-member gets 403 with no event leakage while the owner streams', async ({ isolatedServer }) => {
  const base = isolatedServer.url;

  // Provision two identities; alice will own the room, bob has no relationship at all.
  await createAccount(base, 'alice');
  await createAccount(base, 'bob');
  const tokAlice = await login(base, 'alice');
  const tokBob = await login(base, 'bob');

  // alice creates (and thus owns) the project; a task seeds a durable event for replay.
  const projRes = await fetch(`${base}/api/v1/projects`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${tokAlice}` },
    body: JSON.stringify({ name: 'Scoped SSE Room', description: 'T5 e2e', actor: { type: 'human', name: 'alice' } }),
  });
  expect(projRes.status).toBe(201);
  const project = (await projRes.json()) as { id: string };

  const taskRes = await fetch(`${base}/api/v1/tasks`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${tokAlice}` },
    body: JSON.stringify({ project_id: project.id, title: 'seeded work', actor: { type: 'human', name: 'alice' } }),
  });
  expect(taskRes.status).toBe(201);
  const task = ((await taskRes.json()) as { task: { id: string } }).task;

  // A realistic Last-Event-ID cursor: the id of a durable event that exists in this project.
  const detailRes = await fetch(`${base}/api/v1/tasks/${task.id}`, { headers: { Authorization: `Bearer ${tokAlice}` } });
  expect(detailRes.status).toBe(200);
  const detail = (await detailRes.json()) as { events: Array<{ id: string }> };
  expect(detail.events.length, 'a created event exists to replay').toBeGreaterThan(0);
  const cursor = detail.events[0].id;

  // ---------------------------------------------------------------------------
  // NON-MEMBER (bob): both streaming endpoints must 403 with NO event leakage — no
  // text/event-stream, no "id:"/"data:" frames, even though replay data exists.
  // ---------------------------------------------------------------------------
  for (const path of [
    `/api/v1/projects/${project.id}/events`,
    `/api/v1/events?project_id=${project.id}`,
  ]) {
    const res = await fetch(`${base}${path}`, {
      headers: { Authorization: `Bearer ${tokBob}`, 'Last-Event-ID': cursor },
    });
    expect(res.status, `non-member SSE ${path}`).toBe(403);
    expect(res.headers.get('content-type') ?? '', `SSE must never open for a non-member (${path})`).not.toContain('text/event-stream');
    const text = await res.text();
    expect(text, '403 is the scoping denial envelope').toContain('forbidden');
    expect(text, `no replayed/live frame leaked via ${path}`).not.toContain('data: ');
    expect(text).not.toContain(': connected');
  }

  // ---------------------------------------------------------------------------
  // OWNER (alice): the SAME endpoint streams (200 text/event-stream) — we cancel right
  // after reading the headers so an open stream never blocks the test.
  // ---------------------------------------------------------------------------
  const ctrl = new AbortController();
  const ownerRes = await fetch(`${base}/api/v1/projects/${project.id}/events`, {
    headers: { Authorization: `Bearer ${tokAlice}`, 'Last-Event-ID': cursor },
    signal: ctrl.signal,
  });
  expect(ownerRes.status, 'owner may stream the project').toBe(200);
  expect(ownerRes.headers.get('content-type')).toContain('text/event-stream');
  ctrl.abort(); // stop reading the open stream
});
