# Consuming synced cookies and secrets

agentcookie syncs your live browser sessions and per-CLI secrets from a source
machine to a headless sink. This doc covers the other half: how a tool on the
sink actually consumes what was synced, keychain-free.

The core rule: consumers shell out to agentcookie, they never import it. The
agentcookie module is private; published CLIs cannot depend on it (a
`private_dep_guard` test enforces this in cli-printing-press). Shelling out is
the same pattern CLIs already use for the `press-auth` companion.

## Why not just read Chrome's keychain

On macOS, an ad-hoc-signed Go binary (what `go install` produces for every PP
CLI) cannot be durably granted access to Chrome's Safe Storage keychain item.
That is why per-app push adapters exist, and why a CLI's `auth login --chrome`
path hangs on a headless sink with no one to click the Keychain prompt. The
consumption path below avoids the keychain entirely by reading agentcookie's
own plaintext stores.

## Cookies

agentcookie writes every synced cookie to a local plaintext sidecar
(`~/.agentcookie/cookies-plain.db`). Read a domain's cookies with one call:

```
agentcookie cookies --domain .amazon.com
# Cookie header: session-id=...; session-token=...; x-main=...

agentcookie cookies --domain .amazon.com --json
# [{"name":"session-token","value":"...","domain":".amazon.com",...}, ...]
```

Behavior:

- Universal. Any tool, any domain, regardless of how the CLI was built. Cookies
  need no per-tool configuration; a Cookie header is a Cookie header.
- Keychain-free. Reads the plaintext sidecar; never touches Chrome Safe Storage.
- Scoped. Matches the exact host and its subdomains, never look-alikes
  (`.amazon.com` matches `amazon.com` and `www.amazon.com`, never
  `evilamazon.com`).
- Blocklist-enforced. Honors the same opt-out blocklist the sink applies.
- Empty is not an error. A missing sidecar or no match prints nothing and exits
  0, so a consumer can fall through to its own auth path.

A CLI's auth step should try `agentcookie cookies` first (via `exec.LookPath`),
and fall back to its existing path when agentcookie is absent or returns
nothing. agentcookie is always a soft dependency: tools must work without it.

## Secrets

Per-CLI secrets sync to `~/.agentcookie/secrets/<cli>/`. Emit them as
shell-assignable lines:

```
eval "$(agentcookie secret env tesla-pp-cli)"
```

### Key-name mapping

A CLI reads its token from a specific env var (for example tesla-pp-cli reads
`TESLA_AUTH_TOKEN`). The secret may have been imported under a different name
(for example `OAUTH_BEARER` from an `auth.json`). Map the consumer's declared
name to the synced key with an alias, resolved live so it tracks refreshes:

```
agentcookie secret alias tesla-pp-cli TESLA_AUTH_TOKEN OAUTH_BEARER
agentcookie secret env   tesla-pp-cli
# ...
# OAUTH_BEARER=<live value>
# TESLA_AUTH_TOKEN=<same live value>
```

Aliases are explicit. agentcookie never guesses which synced key is the right
one (it will not pick a bearer over a refresh token for you). The mapping is a
deliberate, one-time operator action.

### Finding mismatches

`agentcookie discover` shows a COVERAGE column, and `agentcookie doctor` has a
`Secret coverage` check, that flag any CLI whose synced secret store does not
provide the auth env var it reads, with the exact `secret alias` command to fix
it. A CLI that reads its value in place (no explicit secret store) is shown as
`in-place` and is not flagged.

This per-CLI mapping is what makes secrets printing-press-aware: agentcookie can
only know a consumer wants `TESLA_AUTH_TOKEN` because the CLI declares it. The
authoritative mapping ultimately belongs in the per-CLI manifest the press
emits; the alias above is the operator-set bridge until then.

## Contract summary

| Surface | Command | Keychain | Per-tool config |
| --- | --- | --- | --- |
| Cookies | `agentcookie cookies --domain <d>` | none | none (universal) |
| Secrets | `eval "$(agentcookie secret env <cli>)"` | none | alias when names differ |

Per-app push adapters (`internal/sinkpush`) remain as a legacy fallback; the
read commands above are the supported, generic consumption path.
