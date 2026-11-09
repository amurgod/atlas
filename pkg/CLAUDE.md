# Shared Go libraries

Commands, Go rules, and the verify steps come from the services file:

@../services/CLAUDE.md

Always run the whole-module checks here, because every service depends on these packages.

## What each package owns

| Package | Owns | Watch out for |
|---|---|---|
| `auth` | JWT issue/parse, roles, `rolePerms`, permission constants | Issued tokens keep their old permissions until they expire |
| `config` | Env access: `Get`, `Must`, `Int`, `Duration`, `Bool` | `Must` panics at startup; a malformed `Int`, `Duration`, or `Bool` silently uses the fallback |
| `db` | Postgres connection with retry, `ExecAll` for startup schema | Pass `withVector=true` only for services that use pgvector |
| `events` | NATS bus, envelope, subject constants, payload types | Stream `ATLAS` (`atlas.>`, 7-day retention); consumers are durable with `DeliverNew`, so they miss events published before they first subscribe |
| `httpx` | Middleware, auth wrappers, `JSON`, `Error`, `Decode`, `Serve` | Middleware attaches claims but rejects nothing; `Decode` rejects unknown fields |
| `llmclient` | HTTP client for `llm-gateway` | 90-second timeout; `Complete` defaults `MaxTokens` to 1024 |

## Rules

- These packages are contracts. Prefer additive changes, and update every caller in the same change when a signature must change.
- No service-specific logic, and no imports from `services/`.
- A new event needs a subject constant and a payload struct in `events`. Consumers decode with `events.Decode[T]`.
- A new permission goes in `auth`, as a constant and in `rolePerms`.
- `llmclient` request and response types mirror `llm-gateway`'s handlers by hand, so change both together.
- Add a test whenever you change a package's behavior.

## Suggested improvements

- `pkg/` has no tests, including for `auth` and `httpx`, which every service relies on for access control.
