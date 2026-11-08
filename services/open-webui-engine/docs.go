package main

import (
	"context"
	"net/http"
	"strings"

	"github.com/atlas/platform/pkg/events"
	"github.com/atlas/platform/pkg/httpx"
	"github.com/google/uuid"
)

func (h *handler) addDoc(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title   string `json:"title"`
		Content string `json:"content"`
		Source  string `json:"source"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	stored, err := h.storeDoc(r.Context(), uid(r.Context()), in.Title, in.Content, in.Source)
	if err != nil {
		msg := err.Error()
		status := http.StatusInternalServerError
		if strings.Contains(msg, "required") {
			status = http.StatusBadRequest
		} else if strings.Contains(msg, "embedding") {
			status = http.StatusBadGateway
		}
		httpx.Error(w, status, msg)
		return
	}
	httpx.JSON(w, http.StatusCreated, stored)
}

func (h *handler) removeDoc(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		var in struct {
			ID string `json:"id"`
		}
		_ = httpx.Decode(r, &in)
		id = strings.TrimSpace(in.ID)
	}
	if id == "" {
		httpx.Error(w, http.StatusBadRequest, "id required")
		return
	}
	if err := h.deleteDoc(r.Context(), uid(r.Context()), id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			httpx.Error(w, http.StatusNotFound, "document not found")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not remove document")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) listDocs(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(),
		`SELECT id::text, title, source, bytes, created_at FROM engine_documents WHERE user_id=$1 ORDER BY created_at DESC`,
		uid(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	type doc struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Source    string `json:"source"`
		Bytes     int    `json:"bytes"`
		CreatedAt any    `json:"created_at"`
	}
	out := []doc{}
	for rows.Next() {
		var d doc
		if err := rows.Scan(&d.ID, &d.Title, &d.Source, &d.Bytes, &d.CreatedAt); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "db error")
			return
		}
		out = append(out, d)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"documents": out})
}

func (h *handler) getDoc(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var title, source, key string
	var created any
	err := h.pool.QueryRow(r.Context(),
		`SELECT title, source, s3_key, created_at FROM engine_documents WHERE id=$1 AND user_id=$2`,
		id, uid(r.Context())).Scan(&title, &source, &key, &created)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "document not found")
		return
	}
	body, err := h.objects.Get(r.Context(), key)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "object store get failed")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id": id, "title": title, "source": source, "content": string(body), "created_at": created,
	})
}

func (h *handler) searchDocs(w http.ResponseWriter, r *http.Request) {
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
	emb, err := h.llm.Embed(r.Context(), []string{in.Query})
	if err != nil || len(emb.Embeddings) == 0 {
		httpx.Error(w, http.StatusBadGateway, "embedding failed")
		return
	}
	hits, err := h.vectors.Search(r.Context(), uid(r.Context()), emb.Embeddings[0], in.Limit)
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "search failed")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"hits": hits})
}

type storedDoc struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Chunks int    `json:"chunks"`
	S3Key  string `json:"s3_key"`
	Model  string `json:"embedding_model"`
}

func (h *handler) storeDoc(ctx context.Context, userID, title, content, source string) (*storedDoc, error) {
	title = strings.TrimSpace(title)
	content = strings.TrimSpace(content)
	if title == "" || content == "" {
		return nil, errMessage("title and content required")
	}
	if source == "" {
		source = "paste"
	}
	parts := chunkText(content, 900)
	emb, err := h.llm.Embed(ctx, parts)
	if err != nil {
		return nil, errMessage("embedding failed")
	}
	if len(emb.Embeddings) != len(parts) {
		return nil, errMessage("embedding count mismatch")
	}
	docID := uuid.New()
	key := userID + "/" + docID.String() + ".txt"
	if err := h.objects.Put(ctx, key, "text/plain; charset=utf-8", []byte(content)); err != nil {
		return nil, errMessage("object store put failed")
	}
	_, err = h.pool.Exec(ctx, `
		INSERT INTO engine_documents (id, user_id, title, source, content_type, s3_key, bytes)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		docID, userID, title, source, "text/plain", key, len(content))
	if err != nil {
		_ = h.objects.Delete(ctx, key)
		return nil, errMessage("could not store document metadata")
	}
	chunks := make([]storedChunk, 0, len(parts))
	for i, part := range parts {
		chunks = append(chunks, storedChunk{
			ID:         uuid.New(),
			DocumentID: docID,
			UserID:     userID,
			Ordinal:    i,
			Content:    part,
			Embedding:  emb.Embeddings[i],
			Title:      title,
		})
	}
	if err := h.vectors.Upsert(ctx, chunks); err != nil {
		_, _ = h.pool.Exec(ctx, `DELETE FROM engine_documents WHERE id=$1`, docID)
		_ = h.objects.Delete(ctx, key)
		return nil, errMessage("could not store embeddings")
	}
	if h.bus != nil {
		_ = h.bus.Publish(ctx, events.DocumentIngested, events.DocumentIngestedPayload{
			UserID: userID, DocumentID: docID.String(), Title: title,
		})
	}
	return &storedDoc{ID: docID.String(), Title: title, Chunks: len(parts), S3Key: key, Model: emb.Model}, nil
}

func (h *handler) deleteDoc(ctx context.Context, userID, id string) error {
	var key string
	err := h.pool.QueryRow(ctx, `SELECT s3_key FROM engine_documents WHERE id=$1 AND user_id=$2`, id, userID).Scan(&key)
	if err != nil {
		return errMessage("not found")
	}
	_ = h.vectors.DeleteDocument(ctx, userID, id)
	_, err = h.pool.Exec(ctx, `DELETE FROM engine_documents WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return err
	}
	_ = h.objects.Delete(ctx, key)
	return nil
}

type errMessage string

func (e errMessage) Error() string { return string(e) }
