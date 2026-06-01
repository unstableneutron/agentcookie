---
title: "feat: both Teslas auto-connect via agentcookie (bearer manifest + Snowflake signing)"
type: feat
status: active
date: 2026-05-31
repo: printing-press-library
plan_home: agentcookie/docs/plans (printing-press-library is public; PP plans are never committed there)
origin: "user: 'fix tesla cli ... the Tesla CLI doesn't know how to read [the synced auth]. Submit a PR to the printing press for the Tesla CLI to make it connect.' + 'I have two Teslas with different auth types, make sure both work' -> full control of both on the sink. Grounded in live state on this machine 2026-05-31."
---

# feat: both Teslas auto-connect via agentcookie (bearer manifest + Snowflake signing)

**Target repo:** `mvanhorn/printing-press-library` (the Tesla CLI at `library/devices/tesla`). This plan document lives in the private agentcookie repo because printing-press-library is public and PP plans are never committed there.

## Summary

Matt has two Teslas on one Tesla account: Stella (commands over plain REST) and Snowflake (signed-command-required). The Tesla CLI does not connect on the agentcookie sink even though the auth is synced, because of a single env-var name mismatch: `tesla-pp-cli` reads the bearer from `TESLA_AUTH_TOKEN` (`internal/config/config.go:77`) but agentcookie's secrets bus exports the synced bearer as `OAUTH_BEARER`. One Fleet API bearer covers both cars for reads, and covers Stella for commands too (REST). Snowflake's signed commands additionally need its EC private key plus the tesla-control signer on the sink.

The user wants full control of both cars on the sink. So this plan ships two things, both as Printing Press changes plus their agentcookie wiring:

1. A bearer manifest so `agentcookie discover` maps the synced bearer to `TESLA_AUTH_TOKEN`. This alone makes both cars read on the sink and Stella fully controllable (no tesla-pp-cli code change; the env read already exists).
2. The Snowflake signing path on the sink: sync the EC private key as a sealed secrets-bus item, point `tesla-pp-cli` at it via `TESLA_FLEET_KEY_FILE`, and make tesla-control + the signing relay available on the sink so signed commands to Snowflake work.

Security note carried through the whole plan: item 2 places a vehicle-command signing key on a headless machine, which means that machine (and anything that can run as the user on it) can send signed commands to the car. That is the explicit trade for full sink control and is gated behind the user's "full control" choice.

## Problem Frame

### Verified live state (this machine, 2026-05-31)

- Source fully authed: `~/.tesla/` holds `fleet-client-id`, `fleet-client-secret`, `fleet-token`, `fleet-token.refresh`, `snowflake-private.pem`/`snowflake-public.pem`; `~/.config/tesla-pp-cli/` holds `auth.json` + `config.toml`. The `snowflake-pp` Fleet developer app is registered; no new developer-app registration is needed.
- agentcookie is already syncing Tesla: `~/.agentcookie/secrets/tesla-pp-cli/secrets.env` exists with keys `OAUTH_BEARER`, `OAUTH_REFRESH`, `OAUTH_EXPIRES_AT`, `ISSUED_AT`. The private key is NOT currently in the bus.
- `tesla-pp-cli` reads `TESLA_AUTH_TOKEN` from the env as the bearer (`internal/config/config.go:77-79`, `110`), and also reads `TESLA_FLEET_KEY_FILE` for the signing key path (seen in the binary), shells out to `tesla-control` for signed commands, and runs a local signing relay (`relay-cert.pem`, `relay.port`).
- Two cars on one account: Stella responds to REST reads/commands ("vehicle responds to REST reads; commands will use plain REST"); Snowflake requires signed commands.
- No library CLI in printing-press-library has an `agentcookie.toml` manifest yet; Tesla is the first adopter of the v2 secrets-bus adoption standard.

### The two gaps

- Gap A (both cars, reads + Stella commands): name mismatch `OAUTH_BEARER` vs `TESLA_AUTH_TOKEN`. Fixed by the bearer manifest.
- Gap B (Snowflake signed commands on the sink): the private key, the tesla-control signer, and the relay are not present on the sink. Fixed by syncing the sealed key + pointing the CLI at it + ensuring tesla-control/relay run on the sink.

## Requirements

- R1. On a sink with Tesla synced, `tesla-pp-cli` authenticates from the agentcookie-synced bearer with no manual `set-token` / `auth login` / paste.
- R2. The bearer bridge ships as an `agentcookie.toml` manifest in the Tesla CLI so `agentcookie discover` auto-wires `TESLA_AUTH_TOKEN <- OAUTH_BEARER` for every user.
- R3. No tesla-pp-cli code change for the bearer path (the `env:TESLA_AUTH_TOKEN` read already exists). Any signing-path change is additive and minimal.
- R4. Both cars read/status on the sink from the one synced bearer; Stella's REST commands work on the sink.
- R5. Snowflake's signed commands work on the sink: its EC private key is synced as a sealed secrets-bus item (mode 0600), `tesla-pp-cli` finds it via `TESLA_FLEET_KEY_FILE`, and tesla-control + the relay run on the sink.
- R6. The signing key is never written world-readable and is carried only over the existing encrypted bus with sealing on; the security trade (signing key on a headless sink) is documented.
- R7. Verified live on the sink: a read on each car returns data, a Stella REST command executes, and a Snowflake signed command executes.

## Key Technical Decisions

### KTD1: Bearer via manifest, not a local alias and not CLI code

The one-off equivalent is `agentcookie secret alias tesla-pp-cli TESLA_AUTH_TOKEN OAUTH_BEARER` on this machine; it helps nobody else. Shipping `agentcookie.toml` makes `agentcookie discover` wire it for everyone. The CLI already reads `TESLA_AUTH_TOKEN`, so no Go change for the bearer (R3).

### KTD2: One bearer covers both cars; refresh stays on the source

Both cars are on one Tesla account, so the single synced bearer authenticates reads for both and Stella's REST commands. The source holds the refresh token + client secret and auto-refreshes, re-syncing a fresh bearer continuously, so the sink needs no refresh credentials and that long-lived material stays on the source.

### KTD3: Snowflake signing key synced as a sealed bus item, CLI points at it via TESLA_FLEET_KEY_FILE

To control Snowflake from the sink, the EC private key (`snowflake-private.pem`) must be on the sink. Carry it through the existing encrypted bus as a sealed item (the v0.12 master-key sealed twin, mode 0600), materialize it to a sink path, and map `TESLA_FLEET_KEY_FILE` to that path in the manifest so `tesla-pp-cli` uses it for signing. The secrets bus today carries `KEY=VALUE`; a PEM is multiline, so the carrier shape (base64-into-a-key materialized to a file on the sink, vs a native file-sync if the bus supports it) is resolved against the secrets-bus spec at implementation time.

### KTD4: tesla-control + relay must exist on the sink

Signed commands shell out to `tesla-control` and run a local signing relay. The sink must have the `tesla-control` binary and be able to start the relay. Whether tesla-pp-cli bundles/launches this or expects a separately installed `tesla-control` is confirmed against the CLI's signed-command path during implementation; the plan provisions whatever that path requires.

### KTD5: Security trade is explicit and gated

A signing key on a headless sink means that machine can command the car. This is the deliberate cost of "full control of both" and is documented in the runbook and the manifest comments. Sealing + 0600 + tailnet-only sync bound the exposure; they do not eliminate the fact that the sink can now sign.

## Implementation Units

### U1. Bearer manifest: both cars read, Stella controllable

**Goal:** Ship an `agentcookie.toml` in the Tesla CLI mapping the synced bearer to `TESLA_AUTH_TOKEN`, so `agentcookie discover` auto-wires it and both cars read on the sink (Stella also commands).

**Requirements:** R1, R2, R3, R4.

**Dependencies:** none.

**Files:**
- `library/devices/tesla/agentcookie.toml` (create) — v2 adoption manifest; schema authority `docs/spec-agentcookie-secrets-bus-v2-adoption.md` (agentcookie repo). Declare `TESLA_AUTH_TOKEN <- OAUTH_BEARER`.
- `library/devices/tesla/README.md` (modify if present) — note Tesla auth auto-flows from agentcookie on a sink.

**Approach:** Read the v2 spec for the exact manifest shape; author the bearer mapping; mirror the spec worked example; do not invent fields. Bearer-only in this unit (signing key added in U3).

**Execution note:** Confirm the manifest schema against the agentcookie v2 spec before writing; Tesla is the first manifest in the repo.

**Test scenarios:**
- `agentcookie discover` detects the Tesla CLI and reports `TESLA_AUTH_TOKEN <- OAUTH_BEARER`, no error, no prior secret-coverage MISMATCH.
- Manifest is valid TOML with all spec-required fields.

**Verification:** `agentcookie discover` surfaces the Tesla bearer mapping and validates against the v2 spec.

---

### U3. Snowflake signing material on the sink

**Goal:** Make the Snowflake EC private key available on the sink as a sealed bus item and point `tesla-pp-cli` at it via `TESLA_FLEET_KEY_FILE`, so the CLI can sign commands for Snowflake there.

**Requirements:** R5, R6.

**Dependencies:** U1.

**Files:**
- `library/devices/tesla/agentcookie.toml` (modify) — add the signing-key item: carry `snowflake-private.pem` as a sealed bus item and map `TESLA_FLEET_KEY_FILE` to the materialized sink path.
- agentcookie side (no code expected): use the existing sealed-secret + file materialization path; if the bus cannot natively carry a file, encode the PEM into a sealed key and have the manifest/materialization write it to a 0600 file on the sink. Confirm against `docs/spec-agentcookie-secrets-bus-v1.md` / `-v2-adoption.md` and `docs/runbook-secrets-bus-adoption.md`.

**Approach:** Resolve the PEM-carrier shape against the secrets-bus spec (native file vs base64-into-sealed-key materialized to a file). Ensure the materialized key is mode 0600 and only ever written under `~/.agentcookie/`. Map `TESLA_FLEET_KEY_FILE` so `tesla-pp-cli` finds it with zero manual steps.

**Execution note:** Verify the exact key-file path/env contract `tesla-pp-cli` expects (`TESLA_FLEET_KEY_FILE` and any `[fleet] public_key_domain` interplay) from the CLI source before finalizing the mapping.

**Test scenarios:**
- The synced key materializes to a 0600 file on the sink at the mapped path.
- `tesla-pp-cli` reports the signing key present (e.g. `auth fleet-status` / doctor shows key available), sourced from the synced item, no manual copy.
- The key value on the sink equals the source key (round-trip integrity), and the file is never group/world readable.

**Verification:** On the sink, `tesla-pp-cli` sees the Snowflake signing key via `TESLA_FLEET_KEY_FILE` with no manual step, 0600.

---

### U4. tesla-control + signing relay available on the sink

**Goal:** Ensure the sink can actually execute a signed command: the `tesla-control` signer and the local relay run there.

**Requirements:** R5.

**Dependencies:** U3.

**Files:** depends on how `tesla-pp-cli` invokes signing (confirmed during execution). If the CLI bundles/launches `tesla-control` and the relay, this may be runtime-provisioning only; if it expects a separately installed binary, document/automate that install in the Tesla CLI README/runbook.

**Approach:** Trace the CLI's signed-command path (`tesla-control %s failed`, `relay-cert.pem`, `relay.port`, `relay_stop`) to determine what must be present on the sink, then provision it (install/launch). Prefer the CLI driving it over manual setup.

**Test scenarios:** `Test expectation: none -- provisioning/integration; behavioral proof is U2.`
- The relay starts on the sink and `tesla-control` is invocable.

**Verification:** A dry-run signed command path on the sink reaches the signer without a missing-binary/relay error.

---

### U2. Verify live on the sink: both cars fully work

**Goal:** Prove the outcome end to end on the sink: both cars read, Stella executes a REST command, Snowflake executes a signed command, all from synced material with no manual auth.

**Requirements:** R4, R5, R7.

**Dependencies:** U1, U3, U4.

**Files:** none in-repo (runtime verification on the live sink).

**Approach:** On the sink with everything synced, run: a read/status on each car (both via the synced bearer, `auth_source=env:TESLA_AUTH_TOKEN`); a benign Stella REST command; a benign Snowflake signed command (e.g. a low-risk, observable, reversible action). Use the immediate `agentcookie secret alias` to prove the bearer mechanism before the manifest PR ships, to de-risk U1.

**Test scenarios:** `Test expectation: none -- runtime launch verification.`
- Read on each car returns real data from the synced bearer; no manual token entry.
- A Stella REST command executes and the car reflects it.
- A Snowflake signed command executes via the synced key + relay and the car reflects it.

**Verification:** On the sink, both cars are fully controllable using only agentcookie-synced material.

## Scope Boundaries

### Deferred to Follow-Up Work

- Sink-side self-refresh of an expired bearer (map refresh + client secret) — only if the source-refreshes model proves insufficient.
- Generalizing the manifest + sealed-key pattern to other device/OAuth CLIs — Tesla is the first adopter.

### Out of scope

- Any tesla-pp-cli Go change for the bearer path (env read already exists). Signing-path changes are additive and minimal if needed.
- Re-registering a Tesla developer app (already done: `snowflake-pp`).

## Risks and Dependencies

- **Signing key on a headless sink (security).** The sink can command Snowflake once the key is there. Mitigation: sealed bus item, 0600, tailnet-only sync, documented trade (KTD5). This is a deliberate, user-chosen exposure, not an accident.
- **PEM does not fit the KEY=VALUE bus.** Resolve the carrier shape against the secrets-bus spec (KTD3); do not hand-roll a path that writes the key world-readable or outside `~/.agentcookie/`.
- **tesla-control/relay provisioning.** Signed commands need the signer + relay on the sink; if the CLI does not self-provision, U4 must install it. Confirm the CLI's path before committing the approach.
- **Bearer expiry between syncs.** A read can briefly fail if the bearer expires before the source re-syncs; the source auto-refreshes continuously. Note in the README.
- **First manifest in the repo.** No in-repo example; match the agentcookie v2 spec exactly and validate with `agentcookie discover`.

## Sources and Research

- Live state (2026-05-31): `~/.tesla/` creds incl. `snowflake-private.pem`; `~/.config/tesla-pp-cli/{auth.json,config.toml}`; `~/.agentcookie/secrets/tesla-pp-cli/secrets.env` (`OAUTH_BEARER`/`OAUTH_REFRESH`/`OAUTH_EXPIRES_AT`/`ISSUED_AT`).
- Tesla CLI source: `printing-press-library` `library/devices/tesla/internal/config/config.go` (`env:TESLA_AUTH_TOKEN` at 77-79/110; `[fleet]` block; `TESLA_FLEET_KEY_FILE`, `tesla-control`, relay strings in the binary).
- agentcookie specs: `docs/spec-agentcookie-secrets-bus-v1.md`, `docs/spec-agentcookie-secrets-bus-v2-adoption.md`, `docs/runbook-secrets-bus-adoption.md` (manifest format, sealed items, discovery).
- Memory: tesla-pp-cli auth bridge; Snowflake internet command path (`snowflake-pp` app, `~/.tesla/fleet-*`, signed-cmd-req vs Stella REST); Tesla Fleet API pricing.
