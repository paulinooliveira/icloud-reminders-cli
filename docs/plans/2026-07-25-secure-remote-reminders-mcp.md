---
title: "Secure remote iCloud Reminders: CLI + local MCP + Hindsight-pattern remote"
date: 2026-07-25
status: APPROVED-DIRECTION — owner wants both phases in order; token auth like Hindsight; pending build
appetite: "Phase A one evening (LOCAL_GREEN). Phase B one weekend (REMOTE_GREEN). Build in order; do not skip A."
owner: paulino (orchestrator)
owned-scope: >
  docs/plans/2026-07-25-secure-remote-reminders-mcp.md
  (implementation slices declare narrow scope)
review:
  method: b-plan-review panel — [zen], [clean]+[leverage], architecture/failure/test, mandatory adversarial
  disposition: unanimous SHIP-WITH-CHANGES; owner then locked full A→B sequence with bearer-token remote
  empirical_probe_2026-07-25: |
    remindctl list --json ≈ 7.2s; show open --list Belo --json ≈ 7.3s / ~5.7MB / 12460 items.
    No server-side limit in remindctl 0.2.0 — caps are response-side; large-list calls stay slow.
decisions-locked:
  sequence: Phase A (LOCAL_GREEN) then Phase B (REMOTE_GREEN). Both are in scope; A must ship before B.
  goal: agents (local + other devices) get stable access; bearer token auth is enough (Hindsight-style)
  origin_host: Mac with EventKit/Reminders auth via remindctl
  surfaces: CLI + stdio MCP (A); loopback HTTP MCP + token edge + dedicated CF Tunnel (B)
  transport_auth: per-client Bearer tokens, hashed at rest, revoke by keys-file reload — same idea as Hindsight
  no_oauth_sso: interactive CF Access SSO not required; token is the primary remote auth
  no_verbatim_hindsight_proxy: reuse pattern (keys, loopback, tunnel, gates), NOT bank/retain domain code
  keys_schema_remote: id, key_hash, lists[], write, enabled — lists allowlist REQUIRED; write default false
  cli_preservation: one CLI entrypoint; absorb scripts/reminders.sh; no dual wrappers
  tool_surface_v1: lists, show, add, complete, get, status — edit/delete/list-mgmt NOT IN v1
  response_contract: list/show always return limit + total_count + has_more; never silent truncation
  subprocess_bounds: timeout + structured errors; resolve remindctl via PATH (fallback /usr/local/bin)
  tunnel: dedicated CF tunnel/LaunchAgent — never share ERP or Hindsight cloudflared
  package_default: package/cli name `reminders` unless owner renames later
decisions-open:
  hostname: reminders.belo.re vs tasks.belo.re vs personal zone
  phase_b_auth_placement: (preferred) thin adapted token edge in front of loopback MCP — same topology as Hindsight;
    fallback in-process Bearer on MCP HTTP if proxy streaming proves brittle; CF Access service tokens only as backup
  large_list_ops: default small limit + require --list for heavy shows; optional later cache out of v1
---

# Secure remote iCloud Reminders — Shape Up pitch

## Problem and bet

Today the repo is a one-file `remindctl` shim. Agents need:

1. A real **CLI** (keep).
2. A stable **MCP** surface.
3. The same **remote token access** pattern already proven on Hindsight, so other devices can connect without Tailscale drama.

**Bet:** ship a small Reminders service core, then expose it in order:

`CLI / stdio MCP (local)` → `token-authenticated remote MCP (Hindsight-style)`.

Bearer token is enough. No OAuth product. No redesign of the Hindsight security model — adapt it to lists/tools, not banks/`retain`.

## Appetite

| Phase | When | Outcome |
|---|---|---|
| **A — LOCAL_GREEN** | ~1 evening | package + CLI + stdio MCP + tests |
| **B — REMOTE_GREEN** | ~1 weekend after A | loopback HTTP MCP + token edge + dedicated tunnel + REM gates |

Stop triggers:

- EventKit off-Mac → impossible; stop.
- Unauth public MCP → no-go.
- Shared ERP/Hindsight tunnel → no-go.
- Token edge cannot carry MCP HTTP cleanly → try Hindsight fallback (CF Access service tokens); never open unauth.
- Scope into Hermes task-mirror / full EventKit rewrite → out.

## What already exists (reuse)

| Asset | Reuse |
|---|---|
| `remindctl` 0.2.0 | backend |
| `scripts/reminders.sh` | absorb into CLI |
| Hindsight keys + auth-edge + dedicated tunnel + verifier | **pattern** (token, hash, loopback, canary DNS, fail-closed gates) |
| `belo.re` CF account/zone | new **dedicated** tunnel only |

## Fat-marker (ordered)

```
PHASE A — local agents
  reminders CLI  ──┐
  stdio MCP      ──┴──► RemindersService ──► remindctl ──► Apple Reminders

PHASE B — remote agents (after A is green)
  agent + Bearer token
       │
       v
  https://<hostname>  ── dedicated CF Tunnel
       │
       v
  token edge (loopback)  ── validate key, lists allowlist, write flag
       │
       v
  loopback HTTP MCP (127.0.0.1) ──► same RemindersService
```

## Scope

### IN (full program, A then B)

**Phase A**

- `pyproject.toml`, `src/`, tests, README, `.gitignore`
- `RemindersService` over `remindctl` (timeouts, caps, `has_more`)
- CLI entry replacing shim
- stdio MCP, narrow tools
- client config examples (Codex/Claude/Hermes local)

**Phase B**

- loopback HTTP MCP origin
- token/key store (hashed, 0600, atomic reload) — Hindsight-style
- auth edge adapted for Reminders tools (`add`/`complete` = write; `lists[]` allowlist)
- dedicated Cloudflare Tunnel + LaunchAgent templates
- canary hostname → REMOTE_GREEN → prod DNS last
- `scripts/verify-remote-reminders.sh` + remote runbook + rollback

### NOT IN

- Raw EventKit rewrite; Linux origin; multi-Mac HA
- Shared tunnel with ERP/Hindsight
- Interactive SSO as primary
- edit/delete/list-mgmt in v1
- Hermes↔Reminders sync redesign
- Shared company auth library (optional later; don’t block)
- Secrets in git

## Tool surface (v1)

| Tool | Local | Remote default |
|---|---|---|
| `lists` | yes | if list allowed / metadata only as designed |
| `show` | yes + limit | yes + limit + list allowlist |
| `get` | yes | yes if list allowed |
| `add` | yes | write key only |
| `complete` | yes | write key only |
| `status` | yes | yes |
| edit/delete/list mgmt | no | no |

Hard rules:

- default `limit` low (e.g. 50); never unbounded Belo dump to agents
- MCP uses IDs, not tty indexes
- auth failures and missing EventKit access are structured errors

## Work slices (order)

### Phase A — LOCAL_GREEN

| Slice | Owns | Depends |
|---|---|---|
| A0 package+service+CLI | `pyproject`, `src/icloud_reminders/{service,cli}`, retire dual shim | — |
| A1 stdio MCP | MCP tools + local client examples | A0 |
| A2 tests | fake remindctl; timeout; truncation metadata; write policy object; legacy CLI snapshot | A0/A1 |
| A3 docs local | README: install, authorize, CLI, stdio MCP | A2 |

**Exit A:** REM.0–REM.3 green. Local agents usable. Then start B.

### Phase B — REMOTE_GREEN

| Slice | Owns | Depends |
|---|---|---|
| B0 transport spike | prove MCP HTTP + Bearer edge (or document fallback) | A done |
| B1 keys+auth edge | keys schema `lists[]`/`write`; detect write tools `add`/`complete`; fail closed | B0 |
| B2 loopback HTTP MCP | 127.0.0.1 only + LaunchAgent | A1, B1 |
| B3 dedicated tunnel | templates, canary hostname → edge | B1/B2 |
| B4 verifier+runbook | `verify-remote-reminders.sh`, rollback, key lifecycle | B3 |
| B5 prod DNS | production CNAME only after REMOTE_GREEN | B4 |

**Exit B:** REM.0–REM.8 green on canary; then prod DNS.

## Definition of Done — gates

Fail-closed. Skip = FAIL.

| Gate | Phase | CLAIM |
|---|---|---|
| REM.0 | A | CLI works authorized |
| REM.1 | A | responses capped; `has_more`/`total_count` present |
| REM.2 | A | stdio MCP lists/show/add/complete round-trip on canary list |
| REM.3 | A | missing binary / unauthorized EventKit fails loud |
| REM.4 | B | HTTP MCP loopback-only |
| REM.5 | B | no/wrong token → 401; no reminder payload leak |
| REM.6 | B | only allowlisted routes; origin ports not public |
| REM.7 | B | read-only key cannot write; list allowlist enforced; revoke works |
| REM.8 | B | stop tunnel → local still green; reboot/LaunchAgent survives |

## Failure modes (load-bearing)

| Failure | Handle |
|---|---|
| remindctl hang / TCC | subprocess timeout + clear error |
| huge list latency (~7s Belo) | require list filter; small default limit; document residual slowness |
| token stolen | revoke hash line + reload |
| write on read-only key | deny at auth edge by tool name |
| wrong list on key | deny via `lists[]` |
| tunnel/MCP stream break | stop; CF Access service-token fallback — not unauth |

## Test spine

**A:** service parse/limit/timeout; MCP schemas; policy write tools; CLI legacy snapshot; optional live `mcp-canary` list.

**B:** unauth/wrong token; write denied; list allowlist; canary non-leak; route matrix; revoke; off-LAN verifier packet.

## Rabbit holes / no-gos

- Second EventKit client “for purity”
- One cloudflared for ERP+memory+reminders
- Default remote key with all lists + write
- Silent truncation
- Verbatim Hindsight `retain`/bank proxy

## Build order (after this plan)

1. **A0 → A1 → A2 → A3** (one package owner; MECE)
2. **B0 spike** (short; evidence only)
3. **B1 → B2 → B3 → B4 → B5**

Parallel only inside a phase when scopes don’t overlap (e.g. B3 templates while B1 tests if files disjoint).

## Rollback

- **B:** delete DNS + stop tunnel + stop edge; CLI/stdio remain.
- **A:** uninstall local package/agents; no remote surface left if B rolled back first.
- **Keys:** delete line, atomic write, reload.

## Owner direction captured (2026-07-25)

- Want **both phases, in order**
- Goal: agents connect stably
- **Token auth like Hindsight is enough**
- Keep CLI
- Remote is part of the plan, not a maybe

## Still open (small)

1. Hostname under `belo.re` (or personal)
2. Final auth placement if B0 finds streaming issues (thin edge preferred)
3. Exact default limit number (suggest 50)

## Review synthesis (short)

Panel said SHIP-WITH-CHANGES: don’t copy Hindsight bank proxy; lock list allowlist + read-only defaults; timeout/caps/`has_more`; split A then B. Owner accepted full A→B with token remote. This doc is the build pitch.
