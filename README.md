# Atlas

Personal knowledge + planner, split into Go microservices. The workspace chat is an Atlas side pane on the shell. Overview, Knowledge, and Planner stay in the left nav. Account and Sign out are in the options menu. The pane has its own WebSocket to **open-webui-engine**. The user picks a Provider. Completions go through the Atlas **llm-gateway**, which reads that Provider and uses the model and temperature marked `usage: chat`. Document bytes go to MinIO or S3. Embeddings go to **pgvector**. Reminders fan out over **NATS JetStream**.

```
React workspace
  / redirects to /app when signed in, otherwise /login
  pages: /login  /register  /app  /app/knowledge  /app/planner  /app/account
  chat pane (sibling of the routes, not a route)
        │  WebSocket /api/engine/v1/ws   { provider }
        ▼
nginx :8080
  ├─ user / knowledge / planner / notify
  ├─ open-webui-engine   RAG REST, chat sessions, the pane's WebSocket
  │     ├─ object store (MinIO / S3)
  │     ├─ pgvector
  │     └─ llm-gateway     Provider CRD, or Anthropic / Completions / local
  └─ ui
```

The official Open WebUI image still runs next to the engine (Compose publishes it on `:3000`). The workspace does not embed that UI. A visit to `/app/knowledge/chat` or `/app/knowledge/chat/:id` opens the pane and replaces the URL with `/app/knowledge`.

The first registered user is granted `admin`. JWTs carry roles and permissions; every service checks them independently.

## Chat pane

`KnowledgeChat` is mounted from `Workspace` only while the pane is open. The socket effect has empty dependencies, so moving between Overview, Knowledge, Planner, and Account does not reconnect. Closing the pane unmounts it and closes the socket. If the socket drops while the pane is open, it reconnects after 800ms.

Each send is `{ "type": "send", "chat_id", "content", "provider" }` on `GET /api/engine/v1/ws` (browser token is the `token` query param). The engine retrieves matching notes, then calls llm-gateway with that provider and `usage: chat`.

| Control | Values |
| --- | --- |
| Provider | `GET /api/engine/v1/providers`. One option per Provider resource (`openai`, `claude`, `grok`, `gemini` in the example file). The list is names only. API keys stay on the resource. |
| Session | Hidden until Sessions is opened. The row slides in horizontally. New chat, or an existing engine chat. The session id is React state, not a route. |

A named provider uses `spec.apiKey`, `spec.baseURL` when set, and the model whose `usage` is `chat`, including that model's temperature. A request with no provider still uses the env path: Anthropic Messages, an OpenAI-compatible Completions API, or the local fallback. `GET /v1/models` and `local-assistant` belong to that env path.

## Layout

| Path | What |
| --- | --- |
| `services/user` | Register, login, `/v1/me`, admin role grants |
| `services/knowledge` | Documents, search, LLM tools, a separate HTTP chat |
| `services/open-webui-engine` | Documents in object storage, pgvector search, chat sessions, WebSocket |
| `services/llm-gateway` | `/v1/complete`, `/v1/chat/completions`, `/v1/providers`, `/v1/models`, `/v1/embeddings` |
| `services/planner` | Tracks / notes / reminders, LLM assist, NATS consumers |
| `services/notification` | Delivery log; SES or Twilio when configured |
| `pkg/` | JWT, Postgres helper, NATS envelope, HTTP middleware, llm client |
| `ui/` | Vite + React. Chat pane lives in `ui/src/pages/KnowledgeChat.jsx` |
| `deploy/k8s/overlays/local` | Kind / local cluster. Ingress host is removed so any Host matches |
| `deploy/aws` | EKS + RDS + ECR Terraform and overlay |

## API (through the gateway)

| UI path | Service |
| --- | --- |
| `/api/user/v1/auth/register` `login` `/v1/me` `/v1/users` | user |
| `/api/engine/v1/adddoc` `removedoc` `/v1/docs` `/v1/search` `/v1/providers` `/v1/models` `/v1/chats` `/v1/ws` | open-webui-engine |
| `/api/knowledge/v1/tools` `/v1/chat` `/v1/documents` | knowledge |
| `/api/planner/v1/tracks` `/notes` `/reminders` `/assist` | planner |
| `/api/notify/v1/deliveries` | notification |

The pane uses the engine routes. Knowledge's `POST /v1/chat` is a separate HTTP completion and is not that socket. Document bytes go to an S3-compatible store; embeddings go to pgvector (`VECTOR_BACKEND=memory` loads them in-process). `llm-gateway` is not on the ingress.

Permissions: `knowledge:read/write`, `planner:read/write`, `notify:read`, `users:admin`. The socket requires `knowledge:read`.

## Local: Docker Compose (fastest)

Needs Docker. No Kubernetes required for this path.

```bash
cp .env.example .env          # optional Anthropic / Completions / Voyage keys
make compose-up
# workspace  http://127.0.0.1:8080
# engine     http://127.0.0.1:8080/api/engine/healthz
# Open WebUI http://127.0.0.1:3000
```

Without API keys the llm-gateway uses bag-of-words embeddings and a local completion. The chat dropdown lists Provider resources, so it stays empty until those resources exist.

## Local: Kubernetes (Kind)

Compose keeps host port **8080**. The Kind ingress is published on **0.0.0.0:18082** (HTTPS **18443**) so both can run, and so a machine on the LAN can open the cluster by IP. The local overlay removes the ingress `host`, so `atlas.local` is not required.

```bash
make deploy-local
# http://<this-host>:18082
```

That target installs `kind` into `./bin` if neither `./bin/kind` nor `kind` is on `PATH`, creates the `atlas` cluster, installs ingress-nginx, builds images, `kind load`s them, and applies `deploy/k8s/overlays/local`. The binary it downloads is `kind-linux-amd64`.

On macOS use `make deploy-local-mac`. It installs `kind-darwin-arm64` or `kind-darwin-amd64` into `./bin` (and replaces a leftover Linux `bin/kind` that cannot run), then follows the same load and apply steps. Docker Desktop, OrbStack, or Colima must already be running, and `kubectl` must be on `PATH`. The app is at http://127.0.0.1:18082.

The Provider CRD is installed with the overlay. Example providers are not, so a fresh cluster has an empty dropdown until:

```bash
kubectl apply -f deploy/k8s/examples/providers.yaml
```

Those four resources (`openai`, `claude`, `grok`, `gemini`) ship with an empty `apiKey`. Set `spec.apiKey` on the one you select. A send before that returns `provider <name> has no apiKey`. Local Kind uses the Chainguard MinIO image (`cgr.dev/chainguard/minio`).

`kind load` of several images in one command can fail on attestation blobs. Load one image at a time. Third-party images that are not a single-platform local image (`nginx`, `nats`, `pgvector`, `minio`, `open-webui`) import with `docker save IMAGE | docker exec -i atlas-control-plane ctr -n k8s.io images import -`.

Tear down with `make undeploy` and `kind delete cluster --name atlas`.

## AWS

Step-by-step: [deploy/aws/README.md](deploy/aws/README.md).

Short version: `terraform apply` in `deploy/aws/terraform` creates EKS, RDS Postgres 16, ECR, and an IRSA role for SES. Push images, fill `deploy/k8s/overlays/aws/secrets.env` plus the `ACCOUNT` / ACM / IRSA placeholders, install the AWS Load Balancer Controller, then `kubectl apply -k deploy/k8s/overlays/aws`.

## Messaging

| Subject | Producer | Consumer |
| --- | --- | --- |
| `atlas.user.created` | user | planner (Inbox track + welcome mail) |
| `atlas.knowledge.document.ingested` | knowledge | planner (Inbox note) |
| `atlas.planner.reminder.due` | planner ticker | (audit) |
| `atlas.notification.requested` | planner | notification |
| `atlas.notification.delivered` | notification | (audit) |

## Configuration

| Variable | Purpose |
| --- | --- |
| `DATABASE_URL` | Postgres DSN |
| `NATS_URL` | JetStream |
| `JWT_SECRET` | HS256, ≥ 32 characters, shared by all HTTP services |
| `LLM_GATEWAY_URL` | knowledge, planner, and open-webui-engine |
| `ANTHROPIC_API_KEY` | Claude via Anthropic Messages (`/v1/messages`). Also adds the model to `/v1/models` |
| `ANTHROPIC_BASE_URL` | Default `https://api.anthropic.com` (LiteLLM / proxy allowed) |
| `ANTHROPIC_MODEL` | Default `claude-haiku-4-5-20251001` |
| `COMPLETIONS_API_KEY` / `OPENAI_API_KEY` | OpenAI-compatible Chat Completions (or `/v1/completions`). Also adds the model to `/v1/models` |
| `COMPLETIONS_BASE_URL` | Default `https://api.openai.com/v1` |
| `COMPLETIONS_MODEL` | Default `gpt-4o-mini` |
| `COMPLETIONS_API` | `chat` (default) or `completions` |
| `COMPLETION_PROVIDER` | `auto` (Anthropic if keyed, else Completions, else local), or `anthropic` / `openai` / `local`. A request for `local-assistant` stays local. Used when the request does not name a Provider |
| Provider `spec.apiKey` | Key for that provider. Chat uses the model with `usage: chat` |
| `VOYAGE_API_KEY` | live embeddings; empty → local bag-of-words |
| `VECTOR_BACKEND` | `pgvector` or `memory` |
| `NOTIFY_MODE` | `log` (default), `ses`, or `twilio` |
| `SES_FROM` | verified identity when `NOTIFY_MODE=ses` |
| `TWILIO_SID` `TWILIO_TOKEN` `TWILIO_FROM` | SMS |

## Development notes

- `make test` / `make build` run against the Go module at the repo root.
- Embedding dimension is 1024 (`voyage-3` and the local bag-of-words path). Changing `EMBED_DIM` requires a matching `vector(N)` column.
- Planner reminder scans use `FOR UPDATE SKIP LOCKED` so a future replica does not double-send.
- The gateway nginx route for `/api/engine/v1/ws` upgrades the connection and sets a long read timeout. Other engine routes use `/api/engine/`.
