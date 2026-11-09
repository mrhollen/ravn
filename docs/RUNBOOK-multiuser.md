# Runbook — multi-user authentication & membership scoping

Operational guide for enabling, running and rolling back the multi-user epic
(T1–T9). Reference material: the authoritative variable table lives in
[ARCHITECTURE.md §9](ARCHITECTURE.md#9-multi-user--authentication); every wire
contract (request/response bodies, 401/403 wordings) is in [API.md](API.md).

Nothing here changes your data. The only schema change of the whole epic is the
additive migration `0011_multiuser_identity.sql` (four new tables + indexes);
reverting environment variables never requires touching the database.

---

## 0. Before you start

1. **Take a backup first.** Zero-downtime hot backup — see README "Backups":
   - live endpoint: `curl -X POST -H "Authorization: Bearer $RAVN_TOKEN" -d '{"dest":"<abs path inside the backup base dir>"}' http://<host>:<port>/api/v1/admin/backup` (gated like every `/api/v1/*` route in all auth tiers — under multiuser a session or api_token bearer works too), or
   - script: `make backup`, or the one-shot CLI `bin/ravn-server -backup <path>`.
   The endpoint restricts the destination to an absolute path inside the backup
   base directory (`RAVN_BACKUP_DIR`, default `<data dir>/backups`), never
   overwrites an existing target (409), and scripted backups are verified with
   `PRAGMA integrity_check`.
2. **Note your current auth posture** (`RAVN_TOKEN` set or not) — the steps
   below assume you may already be on shared-token auth, but work from any
   starting tier because every knob defaults to "today's behavior" when empty.

---

## 1. Enable multiuser auth (shared token kept as operator backdoor)

Set in your environment / `.env` / compose:

```dotenv
RAVN_AUTH=multiuser
RAVN_TOKEN=<keep your existing shared secret>   # now acts as the privileged operator
RAVN_SESSION_TTL=24h                            # optional; default 24h
```

Restart the server. Boot checks (all **fail fast — exit before any port bind**,
with a one-line reason on stderr): unknown `RAVN_AUTH` value, non-positive or
unparseable `RAVN_SESSION_TTL`, `RAVN_SCOPING=membership` with no auth at all.

Verify the posture:

```bash
# public login route works but rejects bad credentials (unknown AND wrong-password are indistinguishable):
curl -s -X POST localhost:PORT/api/v1/auth/login -d '{"username":"nobody","password":"wrongpass"}'
# → 401 {"error":"unauthorized","message":"invalid username or password"}

# the shared operator still works on everything, incl. account provisioning:
curl -s -H "Authorization: Bearer $RAVN_TOKEN" localhost:PORT/api/v1/projects
```

> Keeping `RAVN_TOKEN` set during rollout is what makes provisioning possible:
> `POST /auth/accounts` and `/auth/tokens` are **operator-only** — a session or
> api_token caller gets `403 "operator access required"`. Once you later remove
> the shared token (step 5), there is **no REST route left to create accounts or
> mint tokens**: plan provisioning before hardening, or temporarily restore the
> token.

## 2. Provision your first human account

```bash
curl -s -X POST localhost:PORT/api/v1/auth/accounts \
  -H "Authorization: Bearer $RAVN_TOKEN" \
  -d '{"name":"ada","kind":"human","password":"<min-8-chars>"}'
# → 201 {"identity":{…},"created":true}   (200 + created:false on an existing name = password reset)
```

Log in from the web UI (or `POST /auth/login`) — the returned session token is
shown **exactly once** and only its hash is stored. Sessions live for
`RAVN_SESSION_TTL`; `DELETE /auth/session` with a session bearer revokes it
(idempotent). Wrong/unknown/disabled logins are deliberately indistinguishable,
so brute-force the password, not the username.

## 3. Mint per-agent api_tokens and re-point agents

```bash
curl -s -X POST localhost:PORT/api/v1/auth/tokens \
  -H "Authorization: Bearer $RAVN_TOKEN" \
  -d '{"name":"builder-agent","kind":"agent","label":"ci-deploy"}'
# → 201 {"token":"<shown once>","meta":{…}}
```

Store the secret where the agent reads `RAVN_TOKEN` (the MCP container forwards
it verbatim as the bearer — it works equally well with an api_token in that
slot). The identity row is created automatically if absent. Inventory and
revocation:

- list metadata (secrets never appear): `GET /api/v1/auth/tokens?name=builder-agent`
- disable by durable id: `DELETE /api/v1/auth/tokens/{id}` → 204, idempotent —
  this is the only "deletion"; the row stays for audit.

From now on each agent acts as **its own identity**: attribution names are its
stored name and a declared actor naming someone else is ignored (anti-spoofing).
Human UI users log in with password sessions; agents keep long-lived api_tokens.

## 4. (Optional) Enable membership scoping

```dotenv
RAVN_SCOPING=membership
# RAVN_IDENTITY_OPEN=auto   # default: credential principals MAY create projects
```

Restart, then verify: the web UI and API now hide non-member projects per caller;
unresolvable identities see empty lists (fail-closed) and write nothing. The
shared operator is **exempt in every mode** — it keeps seeing and doing
everything (deliberate administrative backdoor; also why step 4 boots with only
a **warning** when `RAVN_AUTH=token`: there, every admitted request IS the
operator, so scoping does nothing until real credentials are in use).

Role model: **viewer** (read) < **editor** (write) < **admin** (destructive +
member management), plus a derived **owner** — the identity stored in
`project.created_by`, above admin, never assignable through the API.

Seeding membership for existing data:

- Projects created by credential principals (step 3 onward) already carry
  `created_by`; their creator is the owner automatically.
- **Legacy projects have no `created_by`** — they are ownerless. Nobody can be
  made their owner; manage them through stored memberships added by the shared
  operator (`POST /projects/{id}/members`, role `admin`), or create fresh
  projects under the new regime and migrate work over.

Day-to-day membership management (all `RAVN_SCOPING=membership` only — in any
other deployment every verb, including GET, answers
`403 reason=membership_scoping_disabled`):

| Want to… | Call | Gate |
|---|---|---|
| see who can touch a project | `GET /projects/{id}/members` | any visible member (read line) |
| add / re-grant a member | `POST /projects/{id}/members {"username":"ada","role":"editor"}` | admin-or-owner; target must be an existing **enabled** identity (no auto-provision); cannot grant at or below your own role; disabled identities cannot be granted/demoted |
| change a member's role | `PATCH /projects/{id}/members/ada {"role":"admin"}` | same; 409 if it would leave an ownerless project with zero admins |
| remove a member | `DELETE /projects/{id}/members/ada` | same; removing the owner only deletes their stored row — derived ownership survives |

## 5. (Optional, hardening) Remove the shared token

Only after every human has a working session and every agent has its api_token:

```dotenv
RAVN_TOKEN=            # or delete the line; RAVN_AUTH=multiuser stays
```

Trade-offs to accept knowingly:

- **Provisioning routes close forever** (until you restore a token): no REST
  path to create accounts, reset passwords, mint or revoke api_tokens. Rotation
  is then manual — disable the old token's access by removing it from agent
  config and re-minting requires temporary operator access.
- **Root `GET /metrics` becomes unguarded** (the metrics gate keys off a set
  token; `/api/v1/metrics` stays behind the API auth chain). Re-gate externally
  if you need it private, or keep the token for this reason alone.
- The last "see-all without login" is gone: anonymous requests to any gated
  route now get the multiuser `401`.

---

## Rollback

Revert the environment variables (in reverse order of enablement) and restart:

| Revert | Set | Effect |
|---|---|---|
| drop membership scoping | `RAVN_SCOPING=` (empty) | see-all for everyone again; membership tables remain, unused. Byte-identical responses are guaranteed in global mode (`TestScoping_GlobalModeByteIdenticalToToken`) |
| drop multiuser auth | `RAVN_AUTH=token` (+ restore `RAVN_TOKEN`) or `RAVN_AUTH=` (empty → today's resolution: token if a token is set, else off) | credentials stop being resolved; declared actors flow again. Session/api_token rows remain inert (hashes only — they reveal nothing and cost nothing) |

Nothing written under the new regime is lost: attribution names, `created_by`
ownership and membership rows persist and simply stop gating while scoping is
off. Re-enabling later restores exactly those relationships.

---

## Failure modes cheat-sheet

| Symptom | Cause | Fix |
|---|---|---|
| server exits at boot: `…is not one of off, token, multiuser` (or the `global, membership` / `auto, strict, true, false` twins) | typo in `RAVN_AUTH` / `RAVN_SCOPING` / `RAVN_IDENTITY_OPEN` | correct the value — deliberately fail-fast, there is no fallback mode |
| boot exit: session TTL message | `RAVN_SESSION_TTL` empty-but-typed (e.g. `"0"`), negative or not a Go duration (`"24 hours"` ✗) | use e.g. `24h`, `1h30m`; unset for the 24h default |
| boot exit: membership scoping without auth | `RAVN_SCOPING=membership` with `RAVN_AUTH=off` (or off-resolving) | enable a real auth tier or drop the scoping knob |
| startup **warning**: membership scoping with shared-token mode only | `RAVN_SCOPING=membership` + bare token auth | expected if mid-rollout; finish migrating callers to sessions/api_tokens (step 3–4) |
| `401 "present a valid session token or api_token secret…"` on every route | multiuser with no resolvable bearer (typo'd/rotated secret, expired session) | re-login / re-mint; remember secrets are shown once |
| `403 "operator access required"` on `/auth/accounts` or `/auth/tokens` | presenting a session/api_token to an operator-only route | use the shared `RAVN_TOKEN` bearer — by design per-user credentials cannot provision |
| `403 reason=membership_scoping_disabled` on members routes | scoping not enabled in this deployment | set `RAVN_SCOPING=membership` (or stop calling the routes) |
| everything lists empty for a logged-in user | fail-closed: identity disabled, or no membership/ownership anywhere yet | check `GET /auth/session`; add memberships (step 4) — an enabled credential with no project relationships legitimately sees nothing |
