package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"unicode"

	"github.com/atlas/platform/pkg/auth"
	"github.com/atlas/platform/pkg/config"
	"github.com/atlas/platform/pkg/db"
	"github.com/atlas/platform/pkg/events"
	"github.com/atlas/platform/pkg/httpx"
	"github.com/atlas/platform/pkg/llmclient"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

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

	bus, err := events.Connect(ctx, config.Get("NATS_URL", "nats://nats:4222"), "knowledge")
	if err != nil {
		slog.Error("nats", "err", err)
		os.Exit(1)
	}
	defer bus.Close()

	h := &handler{
		pool: pool,
		bus:  bus,
		llm:  llmclient.New(config.Get("LLM_GATEWAY_URL", "http://llm-gateway:8080")),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	mux.HandleFunc("POST /v1/documents", httpx.RequirePerm(auth.PermKnowledgeWrite, h.ingest))
	mux.HandleFunc("GET /v1/documents", httpx.RequirePerm(auth.PermKnowledgeRead, h.listDocs))
	mux.HandleFunc("GET /v1/documents/{id}", httpx.RequirePerm(auth.PermKnowledgeRead, h.getDoc))
	mux.HandleFunc("DELETE /v1/documents/{id}", httpx.RequirePerm(auth.PermKnowledgeWrite, h.deleteDoc))
	mux.HandleFunc("POST /v1/search", httpx.RequirePerm(auth.PermKnowledgeRead, h.search))
	mux.HandleFunc("POST /v1/chat", httpx.RequirePerm(auth.PermKnowledgeRead, h.chat))
	mux.HandleFunc("POST /v1/chat/public", httpx.RequirePerm(auth.PermKnowledgeRead, h.publicChat))
	mux.HandleFunc("POST /v1/tools", httpx.RequirePerm(auth.PermKnowledgeRead, h.tools))

	if err := httpx.Serve(httpx.Middleware(config.Must("JWT_SECRET"))(mux)); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return db.ExecAll(ctx, pool,
		`CREATE EXTENSION IF NOT EXISTS vector`,
		`CREATE TABLE IF NOT EXISTS documents (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL,
			title TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'paste',
			content TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS chunks (
			id UUID PRIMARY KEY,
			document_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
			user_id UUID NOT NULL,
			ordinal INT NOT NULL,
			content TEXT NOT NULL,
			embedding vector(1024) NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS chunks_user_idx ON chunks (user_id)`,
	)
}

type handler struct {
	pool *pgxpool.Pool
	bus  *events.Bus
	llm  *llmclient.Client
}

func (h *handler) ingest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Source  string `json:"source"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	in.Content = strings.TrimSpace(in.Content)
	if in.Title == "" || in.Content == "" {
		httpx.Error(w, http.StatusBadRequest, "title and content required")
		return
	}
	if in.Source == "" {
		in.Source = "paste"
	}
	stored, err := h.storeDocument(r.Context(), httpx.UserID(r.Context()), in.Title, in.Content, in.Source)
	if err != nil {
		slog.Error("ingest", "err", err)
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "embedding") {
			status = http.StatusBadGateway
		}
		httpx.Error(w, status, err.Error())
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"id": stored.ID, "title": stored.Title, "chunks": stored.Chunks, "embedding_model": stored.Model,
	})
}

type storedDoc struct {
	ID     uuid.UUID
	Title  string
	Chunks int
	Model  string
}

func (h *handler) storeDocument(ctx context.Context, userID, title, content, source string) (*storedDoc, error) {
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)
	if title == "" {
		title = "untitled"
	}
	if content == "" {
		return nil, errMessage("title and content required")
	}
	if source == "" {
		source = "paste"
	}
	parts := chunkText(content, 900)
	emb, err := h.llm.Embed(ctx, parts)
	if err != nil {
		slog.Error("embed", "err", err)
		return nil, errMessage("embedding failed")
	}
	if len(emb.Embeddings) != len(parts) {
		return nil, errMessage("embedding count mismatch")
	}

	docID := uuid.New()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, errMessage("db error")
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `INSERT INTO documents (id, user_id, title, source, content) VALUES ($1,$2,$3,$4,$5)`,
		docID, userID, title, source, content)
	if err != nil {
		return nil, errMessage("could not store document")
	}
	for i, part := range parts {
		_, err = tx.Exec(ctx, `INSERT INTO chunks (id, document_id, user_id, ordinal, content, embedding) VALUES ($1,$2,$3,$4,$5,$6)`,
			uuid.New(), docID, userID, i, part, pgvector.NewVector(emb.Embeddings[i]))
		if err != nil {
			return nil, errMessage("could not store chunks")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, errMessage("could not store document")
	}
	_ = h.bus.Publish(ctx, events.DocumentIngested, events.DocumentIngestedPayload{
		UserID: userID, DocumentID: docID.String(), Title: title,
	})
	return &storedDoc{ID: docID, Title: title, Chunks: len(parts), Model: emb.Model}, nil
}

type errMessage string

func (e errMessage) Error() string { return string(e) }

func (h *handler) listDocs(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(),
		`SELECT id, title, source, created_at FROM documents WHERE user_id=$1 ORDER BY created_at DESC`,
		httpx.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type doc struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Source    string `json:"source"`
		CreatedAt any    `json:"created_at"`
	}
	out := []doc{}
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.ID, &d.Title, &d.Source, &d.CreatedAt); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "db error")
			return
		}
		out = append(out, d)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"documents": out})
}

func (h *handler) getDoc(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var title, source, content string
	var created any
	err := h.pool.QueryRow(r.Context(),
		`SELECT title, source, content, created_at FROM documents WHERE id=$1 AND user_id=$2`,
		id, httpx.UserID(r.Context()),
	).Scan(&title, &source, &content, &created)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "document not found")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id": id, "title": title, "source": source, "content": content, "created_at": created,
	})
}

func (h *handler) deleteDoc(w http.ResponseWriter, r *http.Request) {
	tag, err := h.pool.Exec(r.Context(), `DELETE FROM documents WHERE id=$1 AND user_id=$2`,
		r.PathValue("id"), httpx.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "document not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) search(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := httpx.Decode(r, &in); err != nil || strings.TrimSpace(in.Query) == "" {
		httpx.Error(w, http.StatusBadRequest, "query required")
		return
	}
	if in.Limit <= 0 || in.Limit > 20 {
		in.Limit = 8
	}
	hits, err := h.retrieve(r.Context(), httpx.UserID(r.Context()), in.Query, in.Limit)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "search failed")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"hits": hits})
}

func (h *handler) tools(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Tool string `json:"tool"`
		Text string `json:"text"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Tool = strings.ToLower(strings.TrimSpace(in.Tool))
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" {
		httpx.Error(w, http.StatusBadRequest, "text required")
		return
	}
	var system string
	switch in.Tool {
	case "summarize":
		system = "Summarize the text into 4 short bullet points. No preamble."
	case "rewrite":
		system = "Rewrite the text so it is clearer and more direct. Keep the meaning. Return only the rewrite."
	case "extract_tasks":
		system = "Extract actionable tasks from the text as a numbered list. If none, say so."
	default:
		httpx.Error(w, http.StatusBadRequest, "tool must be summarize, rewrite, or extract_tasks")
		return
	}
	out, err := h.llm.Complete(r.Context(), llmclient.CompleteRequest{
		System:    system,
		Messages:  []llmclient.Message{{Role: "user", Content: in.Text}},
		MaxTokens: 600,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "completion failed")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"tool": in.Tool, "result": out.Text, "model": out.Model})
}

type hit struct {
	DocumentID string  `json:"document_id"`
	Title      string  `json:"title"`
	Content    string  `json:"content"`
	Distance   float64 `json:"distance"`
}

func (h *handler) retrieve(ctx context.Context, userID, query string, limit int) ([]hit, error) {
	emb, err := h.llm.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	if len(emb.Embeddings) == 0 {
		return []hit{}, nil
	}
	rows, err := h.pool.Query(ctx, `
		SELECT d.id::text, d.title, c.content, (c.embedding <=> $2) AS dist
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE c.user_id = $1
		ORDER BY c.embedding <=> $2
		LIMIT $3`,
		userID, pgvector.NewVector(emb.Embeddings[0]), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hits := []hit{}
	for rows.Next() {
		var x hit
		if err := rows.Scan(&x.DocumentID, &x.Title, &x.Content, &x.Distance); err != nil {
			return nil, err
		}
		hits = append(hits, x)
	}
	return hits, rows.Err()
}

func chunkText(text string, max int) []string {
	paras := strings.Split(text, "\n")
	var chunks []string
	var buf strings.Builder
	flush := func() {
		s := strings.TrimSpace(buf.String())
		if s != "" {
			chunks = append(chunks, s)
		}
		buf.Reset()
	}
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if buf.Len()+len(p)+1 > max && buf.Len() > 0 {
			flush()
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		if len(p) <= max {
			buf.WriteString(p)
			continue
		}
		for _, sent := range splitRunes(p, max) {
			if buf.Len()+len(sent)+1 > max && buf.Len() > 0 {
				flush()
			}
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(sent)
		}
	}
	flush()
	if len(chunks) == 0 {
		return []string{strings.TrimSpace(text)}
	}
	return chunks
}

func splitRunes(s string, max int) []string {
	if len(s) <= max {
		return []string{s}
	}
	var out []string
	for len(s) > max {
		cut := max
		for cut > max/2 && cut < len(s) && !unicode.IsSpace(rune(s[cut-1])) {
			cut--
		}
		out = append(out, strings.TrimSpace(s[:cut]))
		s = strings.TrimSpace(s[cut:])
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
