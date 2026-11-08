package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/atlas/platform/pkg/auth"
	"github.com/atlas/platform/pkg/config"
	"github.com/atlas/platform/pkg/db"
	"github.com/atlas/platform/pkg/events"
	"github.com/atlas/platform/pkg/httpx"
	"github.com/atlas/platform/pkg/llmclient"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ctxKey string

const userKey ctxKey = "engine-user"

func withUser(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userKey, id)
}

func uid(ctx context.Context) string {
	if v, ok := ctx.Value(userKey).(string); ok && v != "" {
		return v
	}
	return httpx.UserID(ctx)
}

type handler struct {
	pool    *pgxpool.Pool
	bus     *events.Bus
	llm     *llmclient.Client
	objects ObjectStore
	vectors VectorIndex
	webui   *webuiClient
	jwt     string
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx := context.Background()

	pool, err := db.Connect(ctx, config.Must("DATABASE_URL"), true)
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := migrate(ctx, pool); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	bus, err := events.Connect(ctx, config.Get("NATS_URL", "nats://nats:4222"), "open-webui-engine")
	if err != nil {
		slog.Error("nats", "err", err)
		os.Exit(1)
	}
	defer bus.Close()

	objects, err := newObjectStore(ctx)
	if err != nil {
		slog.Error("object store", "err", err)
		os.Exit(1)
	}

	vectors := newVectorIndex(strings.ToLower(config.Get("VECTOR_BACKEND", "pgvector")), pool)
	if err := vectors.Load(ctx); err != nil {
		slog.Error("vector load", "err", err)
		os.Exit(1)
	}

	h := &handler{
		pool:    pool,
		bus:     bus,
		llm:     llmclient.New(config.Get("LLM_GATEWAY_URL", "http://llm-gateway:8080")),
		objects: objects,
		vectors: vectors,
		webui:   newWebUIClient(config.Get("OPEN_WEBUI_URL", "http://127.0.0.1:8080"), config.Get("OPEN_WEBUI_API_KEY", "")),
		jwt:     config.Must("JWT_SECRET"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("POST /v1/adddoc", httpx.RequirePerm(auth.PermKnowledgeWrite, h.addDoc))
	mux.HandleFunc("POST /v1/removedoc", httpx.RequirePerm(auth.PermKnowledgeWrite, h.removeDoc))
	mux.HandleFunc("DELETE /v1/removedoc/{id}", httpx.RequirePerm(auth.PermKnowledgeWrite, h.removeDoc))
	mux.HandleFunc("DELETE /v1/docs/{id}", httpx.RequirePerm(auth.PermKnowledgeWrite, h.removeDoc))
	mux.HandleFunc("GET /v1/docs", httpx.RequirePerm(auth.PermKnowledgeRead, h.listDocs))
	mux.HandleFunc("GET /v1/docs/{id}", httpx.RequirePerm(auth.PermKnowledgeRead, h.getDoc))
	mux.HandleFunc("POST /v1/search", httpx.RequirePerm(auth.PermKnowledgeRead, h.searchDocs))
	mux.HandleFunc("GET /v1/models", httpx.RequirePerm(auth.PermKnowledgeRead, h.listModels))
	mux.HandleFunc("GET /v1/providers", httpx.RequirePerm(auth.PermKnowledgeRead, h.listProviders))
	mux.HandleFunc("GET /v1/chats", httpx.RequirePerm(auth.PermKnowledgeRead, h.listChats))
	mux.HandleFunc("POST /v1/chats", httpx.RequirePerm(auth.PermKnowledgeWrite, h.createChat))
	mux.HandleFunc("GET /v1/chats/{id}", httpx.RequirePerm(auth.PermKnowledgeRead, h.getChat))
	mux.HandleFunc("DELETE /v1/chats/{id}", httpx.RequirePerm(auth.PermKnowledgeWrite, h.removeChat))
	mux.HandleFunc("GET /v1/ws", h.serveWS)

	addr := config.Get("HTTP_ADDR", ":8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           httpx.Middleware(h.jwt)(mux),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return context.Background()
		},
	}
	slog.Info("listening", "addr", addr, "objects", objects.Kind(), "vectors", vectors.Kind())
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return db.ExecAll(ctx, pool,
		`CREATE EXTENSION IF NOT EXISTS vector`,
		`CREATE TABLE IF NOT EXISTS engine_documents (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL,
			title TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'paste',
			content_type TEXT NOT NULL DEFAULT 'text/plain',
			s3_key TEXT NOT NULL,
			bytes INT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS engine_chunks (
			id UUID PRIMARY KEY,
			document_id UUID NOT NULL REFERENCES engine_documents(id) ON DELETE CASCADE,
			user_id UUID NOT NULL,
			ordinal INT NOT NULL,
			content TEXT NOT NULL,
			embedding vector(1024) NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS engine_chats (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL,
			title TEXT NOT NULL DEFAULT 'New chat',
			messages JSONB NOT NULL DEFAULT '[]',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS engine_documents_user_idx ON engine_documents (user_id)`,
		`CREATE INDEX IF NOT EXISTS engine_chunks_user_idx ON engine_chunks (user_id)`,
		`CREATE INDEX IF NOT EXISTS engine_chats_user_idx ON engine_chats (user_id, updated_at DESC)`,
	)
}

func (h *handler) healthz(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"engine":        "open-webui-engine",
		"objects":       h.objects.Kind(),
		"vectors":       h.vectors.Kind(),
		"open_webui_up": h.webui != nil && h.webui.healthy(r.Context()),
	})
}

func (h *handler) listProviders(w http.ResponseWriter, r *http.Request) {
	list, err := h.llm.Providers(r.Context())
	if err != nil {
		slog.Error("providers", "err", err)
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	if list == nil {
		list = []llmclient.ProviderInfo{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"providers": list})
}

func (h *handler) listModels(w http.ResponseWriter, r *http.Request) {
	ids, err := h.llm.Models(r.Context())
	if err != nil || len(ids) == 0 {
		slog.Error("models", "err", err)
		ids = []string{"local-assistant"}
	}
	models := make([]map[string]string, 0, len(ids))
	for _, id := range ids {
		models = append(models, map[string]string{"id": id})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"models": models,
		"modes":  []string{"chat", "agent"},
	})
}

func (h *handler) serveWS(w http.ResponseWriter, r *http.Request) {
	claims := httpx.ClaimsFrom(r.Context())
	if claims == nil {
		parsed, err := auth.Parse(h.jwt, r.URL.Query().Get("token"))
		if err != nil {
			httpx.Error(w, http.StatusUnauthorized, "authentication required")
			return
		}
		claims = parsed
	}
	if !auth.HasPerm(claims.Perms, auth.PermKnowledgeRead) {
		httpx.Error(w, http.StatusForbidden, "missing permission "+auth.PermKnowledgeRead)
		return
	}
	h.chatSocket(w, r.WithContext(withUser(r.Context(), claims.Subject)))
}
