package main

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"

	"github.com/atlas/platform/pkg/auth"
	"github.com/atlas/platform/pkg/httpx"
	"github.com/atlas/platform/pkg/llmclient"
)

type chatDoc struct {
	Title   string `json:"title"`
	Content string `json:"content"`
}

type chatReq struct {
	Message   string              `json:"message"`
	History   []llmclient.Message `json:"history"`
	Documents []chatDoc           `json:"documents"`
	Persist   bool                `json:"persist"`
}

const (
	maxChatDocs     = 8
	maxChatDocRunes = 200000
	maxChatHistory  = 8
	maxAttachChunks = 32
)

func (h *handler) chat(w http.ResponseWriter, r *http.Request) {
	h.answer(w, r, true)
}

func (h *handler) publicChat(w http.ResponseWriter, r *http.Request) {
	h.answer(w, r, false)
}

func (h *handler) answer(w http.ResponseWriter, r *http.Request, allowLibrary bool) {
	in, err := decodeChat(r)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	userID := httpx.UserID(ctx)

	if in.Persist {
		if !allowLibrary {
			httpx.Error(w, http.StatusUnauthorized, "sign in to save documents")
			return
		}
		c := httpx.ClaimsFrom(ctx)
		if c == nil || !auth.HasPerm(c.Perms, auth.PermKnowledgeWrite) {
			httpx.Error(w, http.StatusForbidden, "missing permission "+auth.PermKnowledgeWrite)
			return
		}
		for _, d := range in.Documents {
			if _, err := h.storeDocument(ctx, userID, d.Title, d.Content, "chat"); err != nil {
				slog.Error("persist", "err", err)
				httpx.Error(w, http.StatusBadGateway, "could not store document")
				return
			}
		}
	}

	var hits []hit
	if len(in.Documents) > 0 && !in.Persist {
		hits, err = h.retrieveAttached(ctx, in.Message, in.Documents, 6)
		if err != nil {
			slog.Error("attach-retrieve", "err", err)
			httpx.Error(w, http.StatusBadGateway, "retrieval failed")
			return
		}
	}
	if allowLibrary && userID != "" {
		lib, err := h.retrieve(ctx, userID, in.Message, 6)
		if err != nil {
			slog.Error("library-retrieve", "err", err)
			httpx.Error(w, http.StatusBadGateway, "retrieval failed")
			return
		}
		hits = mergeHits(hits, lib, 6)
	}

	system, userMsg := buildChatPrompt(in.Message, hits, allowLibrary)
	msgs := sanitizeHistory(in.History)
	msgs = append(msgs, llmclient.Message{Role: "user", Content: userMsg})
	out, err := h.llm.Complete(ctx, llmclient.CompleteRequest{
		System:    system,
		Messages:  msgs,
		MaxTokens: 1024,
	})
	if err != nil {
		slog.Error("complete", "err", err)
		httpx.Error(w, http.StatusBadGateway, "completion failed")
		return
	}
	if hits == nil {
		hits = []hit{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"reply":    out.Text,
		"model":    out.Model,
		"provider": out.Provider,
		"rag":      len(hits) > 0,
		"sources":  hits,
	})
}

type decodeError string

func (e decodeError) Error() string { return string(e) }

func decodeChat(r *http.Request) (chatReq, error) {
	var in chatReq
	if err := httpx.Decode(r, &in); err != nil {
		return chatReq{}, decodeError("invalid json")
	}
	in.Message = strings.TrimSpace(in.Message)
	if in.Message == "" {
		return chatReq{}, decodeError("message required")
	}
	docs := make([]chatDoc, 0, len(in.Documents))
	for _, d := range in.Documents {
		d.Title = strings.TrimSpace(d.Title)
		d.Content = strings.TrimSpace(d.Content)
		if d.Content == "" {
			continue
		}
		if d.Title == "" {
			d.Title = "attached"
		}
		if len([]rune(d.Content)) > maxChatDocRunes {
			d.Content = string([]rune(d.Content)[:maxChatDocRunes])
		}
		docs = append(docs, d)
		if len(docs) >= maxChatDocs {
			break
		}
	}
	in.Documents = docs
	if in.Persist && len(in.Documents) == 0 {
		return chatReq{}, decodeError("documents required when persist is true")
	}
	in.History = sanitizeHistory(in.History)
	return in, nil
}

func sanitizeHistory(in []llmclient.Message) []llmclient.Message {
	out := make([]llmclient.Message, 0, maxChatHistory)
	for _, m := range in {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if role != "user" && role != "assistant" {
			role = "user"
		}
		out = append(out, llmclient.Message{Role: role, Content: content})
	}
	if len(out) > maxChatHistory {
		out = out[len(out)-maxChatHistory:]
	}
	return out
}

func buildChatPrompt(question string, hits []hit, signedIn bool) (system, userMsg string) {
	if len(hits) > 0 {
		system = "You are Atlas, a retrieval-augmented assistant. Answer using the provided sources. If the sources do not contain the answer, say so. Be concise."
		var ctxb strings.Builder
		for i, h := range hits {
			ctxb.WriteString("Source ")
			ctxb.WriteString(itoa(i + 1))
			ctxb.WriteString(" (")
			ctxb.WriteString(h.Title)
			ctxb.WriteString("):\n")
			ctxb.WriteString(h.Content)
			ctxb.WriteString("\n\n")
		}
		return system, "Sources:\n" + ctxb.String() + "Question:\n" + question
	}
	system = "You are Atlas, a helpful assistant. Answer the user's question clearly. If they have not provided documents, you may still help, and mention they can attach or ingest a document for grounded retrieval."
	if signedIn {
		system += " The user is signed in; their library is searched automatically when it contains relevant chunks."
	}
	return system, question
}

func (h *handler) retrieveAttached(ctx context.Context, query string, docs []chatDoc, limit int) ([]hit, error) {
	var parts []string
	var meta []hit
	for _, d := range docs {
		for _, chunk := range chunkText(d.Content, 900) {
			parts = append(parts, chunk)
			meta = append(meta, hit{DocumentID: "attached", Title: d.Title, Content: chunk})
			if len(parts) >= maxAttachChunks {
				break
			}
		}
		if len(parts) >= maxAttachChunks {
			break
		}
	}
	if len(parts) == 0 {
		return []hit{}, nil
	}
	texts := make([]string, 0, 1+len(parts))
	texts = append(texts, query)
	texts = append(texts, parts...)
	emb, err := h.llm.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	if len(emb.Embeddings) < 1+len(parts) {
		return []hit{}, nil
	}
	q := emb.Embeddings[0]
	for i := range meta {
		meta[i].Distance = cosineDistance(q, emb.Embeddings[i+1])
	}
	sort.Slice(meta, func(i, j int) bool { return meta[i].Distance < meta[j].Distance })
	if limit <= 0 || limit > len(meta) {
		limit = len(meta)
	}
	return meta[:limit], nil
}

func mergeHits(a, b []hit, limit int) []hit {
	best := map[string]hit{}
	add := func(h hit) {
		key := h.DocumentID + "\n" + h.Title + "\n" + h.Content
		if prev, ok := best[key]; ok && prev.Distance <= h.Distance {
			return
		}
		best[key] = h
	}
	for _, h := range a {
		add(h)
	}
	for _, h := range b {
		add(h)
	}
	out := make([]hit, 0, len(best))
	for _, h := range best {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Distance < out[j].Distance })
	if limit <= 0 || limit > len(out) {
		return out
	}
	return out[:limit]
}

func cosineDistance(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot, na, nb float64
	for i := 0; i < n; i++ {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 1
	}
	sim := dot / (math.Sqrt(na) * math.Sqrt(nb))
	return 1 - sim
}
