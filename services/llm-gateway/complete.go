package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/atlas/platform/pkg/httpx"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (g *gateway) completionMode() string {
	switch strings.ToLower(strings.TrimSpace(g.providerPref)) {
	case "anthropic":
		return "anthropic"
	case "openai", "completions", "chat":
		if strings.EqualFold(g.completionsAPI, "completions") {
			return "openai-completions"
		}
		return "openai"
	case "local":
		return "local"
	}
	if g.anthropicKey != "" {
		return "anthropic"
	}
	if g.completionsKey != "" {
		if strings.EqualFold(g.completionsAPI, "completions") {
			return "openai-completions"
		}
		return "openai"
	}
	return "local"
}

func (g *gateway) publicModelName() string {
	switch g.completionMode() {
	case "anthropic":
		return g.anthropicModel
	case "openai", "openai-completions":
		return g.completionsModel
	default:
		return "local-assistant"
	}
}

func (g *gateway) completeText(ctx context.Context, system string, messages []chatMessage, maxTokens int, requestedModel, providerName, usage string) (text, model, provider string, err error) {
	if maxTokens <= 0 {
		maxTokens = 1024
	}
	if len(messages) == 0 {
		return "", "", "", statusErr{s: "messages required"}
	}
	if strings.TrimSpace(providerName) != "" {
		return g.completeFromProvider(ctx, providerName, usage, system, messages, maxTokens)
	}
	provider = g.providerFor(requestedModel)
	switch provider {
	case "anthropic":
		text, err = g.anthropicComplete(ctx, system, messages, maxTokens)
		model = g.anthropicModel
	case "openai":
		text, err = g.openaiChatComplete(ctx, system, messages, maxTokens)
		model = g.completionsModel
	case "openai-completions":
		text, err = g.openaiCompletions(ctx, system, messages, maxTokens)
		model = g.completionsModel
	default:
		last := messages[len(messages)-1].Content
		return localComplete(system, last), "local-assistant", "local", nil
	}
	return text, model, provider, err
}

func (g *gateway) providerFor(requested string) string {
	req := strings.TrimSpace(requested)
	switch {
	case req == "":
		return g.completionMode()
	case req == "local-assistant" || strings.EqualFold(req, "local"):
		return "local"
	case g.anthropicKey != "" && req == g.anthropicModel:
		return "anthropic"
	case g.completionsKey != "" && req == g.completionsModel:
		if strings.EqualFold(g.completionsAPI, "completions") {
			return "openai-completions"
		}
		return "openai"
	default:
		return g.completionMode()
	}
}

func (g *gateway) complete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		System    string        `json:"system"`
		Messages  []chatMessage `json:"messages"`
		MaxTokens int           `json:"max_tokens"`
		Model     string        `json:"model"`
		Provider  string        `json:"provider"`
		Usage     string        `json:"usage"`
	}
	if err := httpx.Decode(r, &in); err != nil || len(in.Messages) == 0 {
		httpx.Error(w, http.StatusBadRequest, "messages required")
		return
	}
	text, model, provider, err := g.completeText(r.Context(), in.System, in.Messages, in.MaxTokens, in.Model, in.Provider, in.Usage)
	if err != nil {
		slog.Error("complete", "provider", provider, "err", err)
		msg := "completion failed"
		if se, ok := err.(statusErr); ok && se.s != "" {
			msg = se.s
		}
		httpx.Error(w, http.StatusBadGateway, msg)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"text":     text,
		"model":    model,
		"provider": provider,
	})
}

func (g *gateway) anthropicComplete(ctx context.Context, system string, messages []chatMessage, maxTokens int) (string, error) {
	if g.anthropicKey == "" {
		return "", statusErr{s: "anthropic: missing ANTHROPIC_API_KEY"}
	}
	mapped := make([]chatMessage, 0, len(messages))
	for _, m := range messages {
		role := m.Role
		if role != "user" && role != "assistant" {
			role = "user"
		}
		mapped = append(mapped, chatMessage{Role: role, Content: m.Content})
	}
	payload, _ := json.Marshal(map[string]any{
		"model":      g.anthropicModel,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   mapped,
	})
	url := resolveAPIURL(g.anthropicBase, "/v1/messages")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", g.anthropicKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", errStatus("anthropic", resp.StatusCode, body)
	}
	var parsed struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, c := range parsed.Content {
		b.WriteString(c.Text)
	}
	return b.String(), nil
}

func (g *gateway) openaiChatComplete(ctx context.Context, system string, messages []chatMessage, maxTokens int) (string, error) {
	if g.completionsKey == "" {
		return "", statusErr{s: "openai: missing COMPLETIONS_API_KEY or OPENAI_API_KEY"}
	}
	out := make([]chatMessage, 0, len(messages)+1)
	if strings.TrimSpace(system) != "" {
		out = append(out, chatMessage{Role: "system", Content: system})
	}
	for _, m := range messages {
		role := m.Role
		if role != "user" && role != "assistant" && role != "system" {
			role = "user"
		}
		out = append(out, chatMessage{Role: role, Content: m.Content})
	}
	payload, _ := json.Marshal(map[string]any{
		"model":      g.completionsModel,
		"max_tokens": maxTokens,
		"messages":   out,
	})
	url := resolveAPIURL(g.completionsBase, "/chat/completions")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.completionsKey)
	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", errStatus("openai", resp.StatusCode, body)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", statusErr{s: "openai: empty choices"}
	}
	text := parsed.Choices[0].Message.Content
	if text == "" {
		text = parsed.Choices[0].Text
	}
	return text, nil
}

func (g *gateway) openaiCompletions(ctx context.Context, system string, messages []chatMessage, maxTokens int) (string, error) {
	if g.completionsKey == "" {
		return "", statusErr{s: "openai: missing COMPLETIONS_API_KEY or OPENAI_API_KEY"}
	}
	payload, _ := json.Marshal(map[string]any{
		"model":      g.completionsModel,
		"max_tokens": maxTokens,
		"prompt":     flattenPrompt(system, messages),
	})
	url := resolveAPIURL(g.completionsBase, "/completions")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.completionsKey)
	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", errStatus("openai", resp.StatusCode, body)
	}
	var parsed struct {
		Choices []struct {
			Text string `json:"text"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if len(parsed.Choices) == 0 {
		return "", statusErr{s: "openai: empty choices"}
	}
	return strings.TrimSpace(parsed.Choices[0].Text), nil
}

func flattenPrompt(system string, messages []chatMessage) string {
	var b strings.Builder
	if s := strings.TrimSpace(system); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	for _, m := range messages {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		b.WriteString(role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	b.WriteString("assistant:")
	return b.String()
}

// resolveAPIURL joins a vendor base URL with a path. If base is already the
// full endpoint (messages / chat/completions / completions), it is used as-is.
func resolveAPIURL(base, path string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	p := path
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if b == "" {
		return p
	}
	if strings.HasSuffix(b, p) {
		return b
	}
	for _, suf := range []string{"/chat/completions", "/completions", "/v1/messages", "/messages"} {
		if strings.HasSuffix(b, suf) {
			return b
		}
	}
	if strings.HasSuffix(b, "/v1") && strings.HasPrefix(p, "/v1/") {
		return b + strings.TrimPrefix(p, "/v1")
	}
	if strings.HasSuffix(b, "/v1") {
		return b + p
	}
	if strings.HasPrefix(p, "/v1/") {
		return b + p
	}
	return b + "/v1" + p
}
