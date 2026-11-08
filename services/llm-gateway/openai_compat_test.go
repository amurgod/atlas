package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIChatCompletionsLocal(t *testing.T) {
	g := &gateway{dim: 8}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model":"atlas","messages":[{"role":"user","content":"How does retrieval work?"}]
	}`))
	rec := httptest.NewRecorder()
	g.chatCompletions(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) == 0 || !strings.Contains(out.Choices[0].Message.Content, "pgvector") {
		t.Fatalf("reply %+v", out)
	}
}

func TestOpenAIChatCompletionsStream(t *testing.T) {
	g := &gateway{dim: 8}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"stream":true,"messages":[{"role":"user","content":"hi"}]
	}`))
	rec := httptest.NewRecorder()
	g.chatCompletions(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	s := string(body)
	if !strings.Contains(s, "data:") || !strings.Contains(s, "[DONE]") {
		t.Fatalf("sse %q", s)
	}
}

func TestOpenAIModelsAndEmbeddings(t *testing.T) {
	g := &gateway{dim: 8}
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	g.listModels(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "local-assistant") {
		t.Fatalf("models %s", rec.Body.String())
	}

	ereq := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(`{"input":["hello"]}`))
	erec := httptest.NewRecorder()
	g.embeddings(erec, ereq)
	if erec.Code != 200 {
		t.Fatalf("embed status %d %s", erec.Code, erec.Body.String())
	}
	if !strings.Contains(erec.Body.String(), `"embedding"`) {
		t.Fatalf("embed body %s", erec.Body.String())
	}
}

func TestRequestedModelStaysLocal(t *testing.T) {
	g := &gateway{providerPref: "anthropic", anthropicKey: "test", anthropicModel: "claude-haiku"}
	text, model, provider, err := g.completeText(context.Background(), "You are Atlas", []chatMessage{{Role: "user", Content: "hi"}}, 32, "local-assistant", "", "")
	if err != nil || provider != "local" || model != "local-assistant" || text == "" {
		t.Fatalf("provider=%s model=%s err=%v text=%q", provider, model, err, text)
	}
}

func TestListModelsIncludesKeyedProviders(t *testing.T) {
	g := &gateway{anthropicKey: "a", anthropicModel: "claude-haiku", completionsKey: "b", completionsModel: "gpt-4o-mini"}
	rec := httptest.NewRecorder()
	g.listModels(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	body := rec.Body.String()
	for _, id := range []string{"local-assistant", "claude-haiku", "gpt-4o-mini"} {
		if !strings.Contains(body, id) {
			t.Fatalf("missing %s in %s", id, body)
		}
	}
}

func TestOpenaiContentParts(t *testing.T) {
	got := openaiContent([]any{map[string]any{"type": "text", "text": "hello"}})
	if got != "hello" {
		t.Fatalf("got %q", got)
	}
}
