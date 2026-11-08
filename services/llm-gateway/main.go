package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/atlas/platform/pkg/config"
	"github.com/atlas/platform/pkg/httpx"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	completionsKey := config.Get("COMPLETIONS_API_KEY", "")
	if completionsKey == "" {
		completionsKey = config.Get("OPENAI_API_KEY", "")
	}
	h := &gateway{
		anthropicKey:     config.Get("ANTHROPIC_API_KEY", ""),
		anthropicModel:   config.Get("ANTHROPIC_MODEL", "claude-haiku-4-5-20251001"),
		anthropicBase:    config.Get("ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
		voyageKey:        config.Get("VOYAGE_API_KEY", ""),
		voyageModel:      config.Get("VOYAGE_MODEL", "voyage-3"),
		completionsKey:   completionsKey,
		completionsBase:  config.Get("COMPLETIONS_BASE_URL", "https://api.openai.com/v1"),
		completionsModel: config.Get("COMPLETIONS_MODEL", "gpt-4o-mini"),
		completionsAPI:   config.Get("COMPLETIONS_API", "chat"),
		providerPref:     config.Get("COMPLETION_PROVIDER", "auto"),
		dim:              config.Int("EMBED_DIM", 1024),
		http:             &http.Client{Timeout: 90 * time.Second},
	}
	slog.Info("llm-gateway", "completion_mode", h.completionMode(), "embedding_mode", h.embeddingMode())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, http.StatusOK, map[string]any{
			"status":          "ok",
			"completion_mode": h.completionMode(),
			"embedding_mode":  h.embeddingMode(),
		})
	})
	mux.HandleFunc("POST /v1/embed", h.embed)
	mux.HandleFunc("POST /v1/complete", h.complete)
	mux.HandleFunc("GET /v1/models", h.listModels)
	mux.HandleFunc("GET /v1/providers", h.listProviders)
	mux.HandleFunc("POST /v1/chat/completions", h.chatCompletions)
	mux.HandleFunc("POST /v1/embeddings", h.embeddings)

	if err := httpx.Serve(httpx.Middleware("")(mux)); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

type gateway struct {
	anthropicKey     string
	anthropicModel   string
	anthropicBase    string
	voyageKey        string
	voyageModel      string
	completionsKey   string
	completionsBase  string
	completionsModel string
	completionsAPI   string
	providerPref     string
	dim              int
	http             *http.Client
	providers        providerLister
}

func (g *gateway) embeddingMode() string {
	if g.voyageKey != "" {
		return "voyage"
	}
	return "local-bow"
}

func (g *gateway) embedTexts(ctx context.Context, texts []string) ([][]float32, string, error) {
	if len(texts) == 0 {
		return nil, "", statusErr{s: "texts required"}
	}
	if g.voyageKey != "" {
		vecs, err := g.voyageEmbed(ctx, texts)
		if err != nil {
			return nil, "", err
		}
		return vecs, g.voyageModel, nil
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = bagOfWords(t, g.dim)
	}
	return out, "local-bow", nil
}

func (g *gateway) embed(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Texts []string `json:"texts"`
	}
	if err := httpx.Decode(r, &in); err != nil || len(in.Texts) == 0 {
		httpx.Error(w, http.StatusBadRequest, "texts required")
		return
	}
	vecs, model, err := g.embedTexts(r.Context(), in.Texts)
	if err != nil {
		slog.Error("embed", "err", err)
		httpx.Error(w, http.StatusBadGateway, "embedding failed")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"embeddings": vecs, "model": model})
}

func (g *gateway) voyageEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	payload, _ := json.Marshal(map[string]any{
		"input": texts,
		"model": g.voyageModel,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.voyageai.com/v1/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.voyageKey)
	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, errStatus("voyage", resp.StatusCode, body)
	}
	var parsed struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([][]float32, len(parsed.Data))
	for i, d := range parsed.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

type statusErr struct{ s string }

func (e statusErr) Error() string { return e.s }

func errStatus(vendor string, code int, body []byte) error {
	return statusErr{s: vendor + " status " + http.StatusText(code) + ": " + truncate(string(body), 300)}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func bagOfWords(text string, dim int) []float32 {
	vec := make([]float32, dim)
	for _, tok := range tokenize(text) {
		h := fnv32(tok)
		vec[int(h)%dim] += 1
	}
	var norm float32
	for _, v := range vec {
		norm += v * v
	}
	if norm == 0 {
		return vec
	}
	inv := 1 / sqrt(norm)
	for i := range vec {
		vec[i] *= inv
	}
	return vec
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	var b strings.Builder
	var out []string
	flush := func() {
		if b.Len() == 0 {
			return
		}
		out = append(out, b.String())
		b.Reset()
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

func fnv32(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

func sqrt(v float32) float32 {
	// Newton for float32 sqrt; enough for unit-normalized embeddings.
	if v <= 0 {
		return 0
	}
	x := v
	for i := 0; i < 8; i++ {
		x = 0.5 * (x + v/x)
	}
	return x
}
