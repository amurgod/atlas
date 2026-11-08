package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/atlas/platform/pkg/httpx"
)

func (g *gateway) listModels(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   g.availableModels(),
	})
}

func (g *gateway) availableModels() []map[string]any {
	now := time.Now().Unix()
	out := []map[string]any{{
		"id":       "local-assistant",
		"object":   "model",
		"created":  now,
		"owned_by": "atlas",
	}}
	if g.anthropicKey != "" && g.anthropicModel != "" {
		out = append(out, map[string]any{
			"id": g.anthropicModel, "object": "model", "created": now, "owned_by": "anthropic",
		})
	}
	if g.completionsKey != "" && g.completionsModel != "" && g.completionsModel != g.anthropicModel {
		out = append(out, map[string]any{
			"id": g.completionsModel, "object": "model", "created": now, "owned_by": "openai",
		})
	}
	return out
}

func (g *gateway) chatCompletions(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Model    string `json:"model"`
		Stream   bool   `json:"stream"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
		MaxTokens int    `json:"max_tokens"`
		Provider  string `json:"provider"`
		Usage     string `json:"usage"`
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&in); err != nil || len(in.Messages) == 0 {
		httpx.Error(w, http.StatusBadRequest, "messages required")
		return
	}
	var system string
	var msgs []chatMessage
	for _, m := range in.Messages {
		content := openaiContent(m.Content)
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "system" {
			if system != "" {
				system += "\n"
			}
			system += content
			continue
		}
		if role != "assistant" {
			role = "user"
		}
		msgs = append(msgs, chatMessage{Role: role, Content: content})
	}
	if len(msgs) == 0 {
		httpx.Error(w, http.StatusBadRequest, "messages required")
		return
	}
	text, model, provider, err := g.completeText(r.Context(), system, msgs, in.MaxTokens, in.Model, in.Provider, in.Usage)
	if err != nil {
		slog.Error("chat.completions", "provider", provider, "err", err)
		msg := "completion failed"
		if se, ok := err.(statusErr); ok && se.s != "" {
			msg = se.s
		}
		httpx.Error(w, http.StatusBadGateway, msg)
		return
	}
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	if in.Stream {
		writeChatSSE(w, id, model, text)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
	})
}

func (g *gateway) embeddings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Input any    `json:"input"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "input required")
		return
	}
	texts := openaiInput(in.Input)
	if len(texts) == 0 {
		httpx.Error(w, http.StatusBadRequest, "input required")
		return
	}
	vecs, model, err := g.embedTexts(r.Context(), texts)
	if err != nil {
		slog.Error("embeddings", "err", err)
		httpx.Error(w, http.StatusBadGateway, "embedding failed")
		return
	}
	data := make([]map[string]any, len(vecs))
	for i, v := range vecs {
		data[i] = map[string]any{
			"object":    "embedding",
			"embedding": v,
			"index":     i,
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
		"model":  model,
		"usage":  map[string]int{"prompt_tokens": 0, "total_tokens": 0},
	})
}

func openaiContent(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var b strings.Builder
		for _, part := range t {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if s, ok := m["text"].(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}

func openaiInput(v any) []string {
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, item := range t {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				continue
			}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}

func writeChatSSE(w http.ResponseWriter, id, model, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	encode := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	encode(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]string{"role": "assistant", "content": text},
		}},
	})
	encode(map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]string{},
			"finish_reason": "stop",
		}},
	})
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}
