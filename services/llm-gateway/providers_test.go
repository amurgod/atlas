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

func floatPtr(v float64) *float64 { return &v }

func TestModelForUsage(t *testing.T) {
	p := Provider{Name: "openai", Models: []ProviderModel{
		{Name: "sol", Usage: "reasoning", Temperature: floatPtr(0.5)},
		{Name: "luns", Usage: "chat", Temperature: floatPtr(0.2)},
	}}
	chat, ok := p.modelFor("chat")
	if !ok || chat.Name != "luns" || chat.Temperature == nil || *chat.Temperature != 0.2 {
		t.Fatalf("chat model %+v ok=%v", chat, ok)
	}
	reason, ok := p.modelFor("reasoning")
	if !ok || reason.Name != "sol" || reason.Temperature == nil || *reason.Temperature != 0.5 {
		t.Fatalf("reasoning model %+v ok=%v", reason, ok)
	}
	if _, ok := p.modelFor("embed"); ok {
		t.Fatal("unexpected embed model")
	}
}

func TestCompleteFromProviderPicksChatModel(t *testing.T) {
	var got struct {
		Model       string  `json:"model"`
		Temperature float64 `json:"temperature"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("auth %s", r.Header.Get("Authorization"))
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer srv.Close()

	g := &gateway{
		http: srv.Client(),
		providers: staticProviders{{
			Name:    "openai",
			Type:    "openai",
			APIKey:  "sk-test",
			BaseURL: srv.URL + "/v1",
			Models: []ProviderModel{
				{Name: "sol", Usage: "reasoning", Temperature: floatPtr(0.5)},
				{Name: "luns", Usage: "chat", Temperature: floatPtr(0.2)},
			},
		}},
	}
	text, model, provider, err := g.completeText(context.Background(), "sys", []chatMessage{{Role: "user", Content: "hi"}}, 32, "", "openai", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if text != "ok" || model != "luns" || provider != "openai" {
		t.Fatalf("text=%q model=%s provider=%s", text, model, provider)
	}
	if got.Model != "luns" || got.Temperature != 0.2 {
		t.Fatalf("upstream model=%s temp=%v", got.Model, got.Temperature)
	}
}

func TestCompleteReportsMissingProviderKey(t *testing.T) {
	g := &gateway{providers: staticProviders{{
		Name: "openai", Type: "openai",
		Models: []ProviderModel{{Name: "gpt-4o-mini", Usage: "chat"}},
	}}}
	req := httptest.NewRequest(http.MethodPost, "/v1/complete", strings.NewReader(`{"provider":"openai","usage":"chat","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	g.complete(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "provider openai has no apiKey" {
		t.Fatalf("error %q", body.Error)
	}
}

func TestCanonicalProviderType(t *testing.T) {
	if canonicalProviderType("claude") != "anthropic" || canonicalProviderType("grok") != "grok" || canonicalProviderType("gemini") != "gemini" {
		t.Fatal(canonicalProviderType("claude"), canonicalProviderType("grok"), canonicalProviderType("gemini"))
	}
}
