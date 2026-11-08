# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repo map

| Path | What | Rules |
|---|---|---|
| `services/` | Six Go microservices, one module | [services/CLAUDE.md](services/CLAUDE.md) |
| `pkg/` | Go libraries shared by the services | [pkg/CLAUDE.md](pkg/CLAUDE.md) |
| `ui/` | Vite + React workspace | [ui/CLAUDE.md](ui/CLAUDE.md) |
| `deploy/`, `docker-compose.yml`, Dockerfiles | Compose, Kustomize, Terraform, nginx | [deploy/CLAUDE.md](deploy/CLAUDE.md) |

`README.md` has the env var reference, API route table, and deployment walkthroughs.

## Run the whole system

```bash
cp .env.example .env     # optional model keys; the stack runs without them
make compose-up          # workspace :8080, Open WebUI :3000
make compose-down        # also deletes volumes (-v), so all local data
make deploy-local-mac    # Kind on :18082 (Linux: make deploy-local)
make undeploy
```

With no API keys, the gateway falls back to bag-of-words embeddings and local completions.

## How the parts connect

- **Browser to services**: nginx strips the prefix and forwards. `/api/user`, `/api/knowledge`, `/api/planner`, `/api/notify`, `/api/engine`; everything else goes to `ui`.
- **Ports**: every Go service listens on `:8080` inside its container, set by `HTTP_ADDR`. Only nginx is published.
- **Auth**: `user` issues HS256 JWTs carrying roles and permissions; every service verifies them with the shared `JWT_SECRET`. The UI sends `Authorization: Bearer`, and the WebSocket uses a `token` query param.
- **JSON contract**: there are no generated or shared types. `ui/src/api.js` mirrors the Go structs by hand, using snake_case fields. Errors are `{"error": "message"}`.
- **Strict bodies**: `httpx.Decode` rejects unknown fields, so a UI payload must match its Go struct exactly. Change both sides together.
- **Events**: services communicate asynchronously over NATS JetStream. Subjects are defined in `pkg/events`.
- **LLM**: all model calls go through the internal `llm-gateway`, reached via `LLM_GATEWAY_URL`.
- **Shared infra env**: `DATABASE_URL`, `NATS_URL`, `JWT_SECRET`, `LLM_GATEWAY_URL`.

## Cross-cutting rules

- **Git**: work on a branch off `main`. Commit subjects are short and imperative.
- **Secrets**: env vars only, never in code or committed config. The sole exception is throwaway local-dev values in `docker-compose.yml` and `deploy/k8s/overlays/local`.
- **Dependencies**: ask before adding any Go module, npm package, container image, or Terraform module.
- **Scope**: stay inside the area being changed unless a contract spans areas.

## Keep in sync

- **Gateway routes**: `deploy/gateway/default.conf` (Compose) and `deploy/k8s/base/gateway-default.conf` (Kubernetes) are identical copies. Change both.
- **Adding a service**: the `build` and `images` targets and `kind-load` loop in the `Makefile`, `docker-compose.yml`, `deploy/k8s/base`, and both gateway configs.
- **Embedding dimension**: `EMBED_DIM` (1024, in `docker-compose.yml` and `deploy/k8s/base/configmap.yaml`) must match the `vector(1024)` columns in `knowledge` and `open-webui-engine`.

## Suggested improvements

- No CI exists, so every "verify before done" step is manual.
- No pre-commit hooks.
