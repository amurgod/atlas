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

func TestResolveAPIURL(t *testing.T) {
	cases := []struct {
		base, path, want string
	}{
		{"https://api.anthropic.com", "/v1/messages", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/v1", "/v1/messages", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/v1/messages", "/v1/messages", "https://api.anthropic.com/v1/messages"},
		{"https://api.openai.com/v1", "/chat/completions", "https://api.openai.com/v1/chat/completions"},
		{"https://api.openai.com", "/chat/completions", "https://api.openai.com/v1/chat/completions"},
		{"http://litellm:4000/v1/chat/completions", "/chat/completions", "http://litellm:4000/v1/chat/completions"},
		{"http://proxy:8080/v1", "/completions", "http://proxy:8080/v1/completions"},
	}
	for _, c := range cases {
		got := resolveAPIURL(c.base, c.path)
		if got != c.want {
			t.Errorf("resolveAPIURL(%q, %q)=%q want %q", c.base, c.path, got, c.want)
		}
	}
}

func TestCompletionModeAuto(t *testing.T) {
	if got := (&gateway{providerPref: "auto", anthropicKey: "sk"}).completionMode(); got != "anthropic" {
		t.Fatalf("anthropic key should win auto, got %s", got)
	}
	if got := (&gateway{providerPref: "auto", completionsKey: "sk"}).completionMode(); got != "openai" {
		t.Fatalf("completions key should select openai, got %s", got)
	}
	if got := (&gateway{providerPref: "auto", completionsKey: "sk", completionsAPI: "completions"}).completionMode(); got != "openai-completions" {
		t.Fatalf("got %s", got)
	}
	if got := (&gateway{providerPref: "openai", anthropicKey: "sk", completionsKey: "sk"}).completionMode(); got != "openai" {
		t.Fatalf("explicit openai should win, got %s", got)
	}
	if got := (&gateway{}).completionMode(); got != "local" {
		t.Fatalf("empty config should be local, got %s", got)
	}
}

func TestAnthropicComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "ak" {
			t.Errorf("missing api key")
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"claude-test"`) {
			t.Errorf("payload %s", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]string{{"type": "text", "text": "hello from claude"}},
		})
	}))
	defer srv.Close()
	g := &gateway{
		http:           srv.Client(),
		anthropicKey:   "ak",
		anthropicBase:  srv.URL,
		anthropicModel: "claude-test",
	}
	got, err := g.anthropicComplete(context.Background(), "sys", []chatMessage{{Role: "user", Content: "hi"}}, 64)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello from claude" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenAIChatComplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk") {
			t.Errorf("auth %s", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "hello from gpt"}},
			},
		})
	}))
	defer srv.Close()
	g := &gateway{
		http:             srv.Client(),
		completionsKey:   "sk",
		completionsBase:  srv.URL,
		completionsModel: "gpt-test",
	}
	got, err := g.openaiChatComplete(context.Background(), "sys", []chatMessage{{Role: "user", Content: "hi"}}, 64)
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello from gpt" {
		t.Fatalf("got %q", got)
	}
}

func TestOpenAICompletions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/completions" {
			t.Errorf("path %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		prompt, _ := payload["prompt"].(string)
		if !strings.Contains(prompt, "user: hi") {
			t.Errorf("prompt %q", prompt)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]string{{"text": "  legacy reply  "}},
		})
	}))
	defer srv.Close()
	g := &gateway{
		http:             srv.Client(),
		completionsKey:   "sk",
		completionsBase:  srv.URL + "/v1",
		completionsModel: "davinci",
	}
	got, err := g.openaiCompletions(context.Background(), "be helpful", []chatMessage{{Role: "user", Content: "hi"}}, 32)
	if err != nil {
		t.Fatal(err)
	}
	if got != "legacy reply" {
		t.Fatalf("got %q", got)
	}
}

func TestFlattenPrompt(t *testing.T) {
	got := flattenPrompt("Be brief", []chatMessage{{Role: "user", Content: "hi"}})
	if !strings.Contains(got, "Be brief") || !strings.Contains(got, "user: hi") || !strings.HasSuffix(strings.TrimSpace(got), "assistant:") {
		t.Fatalf("unexpected prompt: %q", got)
	}
}
