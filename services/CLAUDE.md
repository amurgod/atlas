# Go services

## Commands (run from the repo root)

```bash
make build                                        # compile all six services
make test                                         # go test ./...; needs no Postgres, NATS, or network
go test ./services/llm-gateway -run TestName -v   # single test
go vet ./...
gofmt -l <changed .go files>                      # must print nothing
make tidy                                         # go mod tidy
```

Language level is Go 1.22 (`go.mod`, `services/Dockerfile`). Don't use newer features.

## Service conventions

- **Skeleton**: JSON `slog` default, `db.Connect`, `migrate`, `events.Connect`, a `handler` struct holding dependencies, then `http.NewServeMux`. Startup failures: `slog.Error` and `os.Exit(1)`.
- **Serving**: `httpx.Serve(httpx.Middleware(secret)(mux))`. `open-webui-engine` builds its own `http.Server` with no write timeout, which its WebSocket needs.
- **Routes**: method patterns (`"POST /v1/tracks"`), versioned under `/v1`, plus `GET /healthz`.
- **Auth**: wrap every new route in `httpx.RequirePerm` or `httpx.RequireAuth` unless the user explicitly marks it public. Middleware alone does not reject a request.
- **Existing exceptions**: `user` register/login, the engine's `/v1/ws` (authenticates inside the handler), and all of `llm-gateway` (internal, never expose it through nginx or the ingress).
- **Responses**: `httpx.JSON` and `httpx.Error`. 201 on create, 204 on delete. Lists are `out := []T{}` under a named key, such as `{"tracks": [...]}`, so they serialize as `[]`.
- **Requests**: `httpx.Decode` into an inline struct, then trim and validate. `_ = httpx.Decode` only where the body is optional.
- **Logging**: `slog` only, as a message plus key/values (`"err", err`). Never log tokens, keys, or document content.

## Data

- **Schema**: no migration files. Add idempotent statements to the service's `migrate` func, run through `db.ExecAll` at startup. `CREATE TABLE IF NOT EXISTS` won't alter an existing table, so changes need `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`.
- **Ownership**: all services share one Postgres database, and each owns its tables. Never query another service's tables.
- **Queries**: parameterized (`$1`), and always scoped by `user_id`. Treat zero affected rows as 404.
- **Identity in the engine**: use `uid(ctx)`, which covers both HTTP and WebSocket requests, not `httpx.UserID`.
- **Replica safety**: planner reminder scans use `FOR UPDATE SKIP LOCKED`. Keep any scheduled scan safe to run on multiple replicas.

## Events and LLM calls

- Event handlers must be idempotent. Returning an error Naks the message for redelivery.
- Always check `bus.Publish` and log failures. Existing `_ = bus.Publish` calls are legacy.
- Call models only through `pkg/llmclient`. A named Provider (a Kubernetes custom resource) uses its key and the model marked with the requested `usage`. With no Provider, `COMPLETION_PROVIDER` selects Anthropic, an OpenAI-compatible API, or the local fallback.
- Outside Kubernetes the Provider list is always empty, so the named-Provider flow needs Kind. In unit tests, inject `staticProviders` through `gateway.providers`.

## Two knowledge stacks

`open-webui-engine` powers the workspace chat and receives new work. `knowledge` is maintenance-only. They have separate tables and data. `open-webui-engine` is a custom Go service, not the official Open WebUI container that runs beside it.

## Go rules (new code; don't retrofit unrelated code)

- **Wrapping**: `fmt.Errorf("doing x: %w", err)` when an error crosses a function or package boundary.
- **5xx**: log the cause with `slog.Error`, and return a fixed message. Never send `err.Error()` to a client; existing sites are legacy.
- **Status codes**: use typed errors and `errors.As` (see `statusErr` in `llm-gateway`). Never match on error strings.
- **Discarding errors**: `_ =` only for best-effort cleanup and socket writes.
- **Context**: `ctx` is the first parameter, and handlers pass `r.Context()` all the way down. `context.Background()` belongs only in `main`, tickers, event handlers, and detached work with a timeout. Never store a context in a struct.
- **Thin handlers**: decode, validate, call a helper, respond. SQL goes in helper methods, in files split by concern as in `open-webui-engine` (`chats.go`, `docs.go`).
- **Interfaces**: define them where they're used, for real seams such as `ObjectStore`, `VectorIndex`, and `providerLister`.
- **Tests**: stdlib `testing` and `httptest`, same package as the code. Table-driven with a `cases` slice when there are multiple inputs (`complete_test.go`). Fake upstreams with `httptest.NewServer`; no real Postgres, NATS, or network.
- **Comments**: sparse, and only to explain a non-obvious reason.

## Verify before done

1. `gofmt -l` on changed files prints nothing.
2. `go vet ./...` is clean.
3. `go test ./...` passes.
4. `make build` succeeds.

## Suggested improvements

- No Go linter is configured (for example `golangci-lint`).
- `user`, `planner`, and `notification` have no tests.
- Five files already fail `gofmt`: `services/notification/main.go`, `services/open-webui-engine/{store,vectors,ws}.go`, and `pkg/events/events.go`.
