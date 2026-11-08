package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/atlas/platform/pkg/httpx"
)

// Provider is one atlas.platform Provider resource. A chat send names the
// provider; the model and temperature come from the entry whose usage matches
// the call ("chat" for the drawer).
type Provider struct {
	Name    string          `json:"name"`
	Type    string          `json:"type"`
	APIKey  string          `json:"apiKey,omitempty"`
	BaseURL string          `json:"baseURL,omitempty"`
	Models  []ProviderModel `json:"models"`
}

type ProviderModel struct {
	Name        string   `json:"name"`
	Usage       string   `json:"usage"`
	Temperature *float64 `json:"temperature,omitempty"`
}

type providerView struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type providerLister interface {
	List(context.Context) ([]Provider, error)
}

type staticProviders []Provider

func (s staticProviders) List(context.Context) ([]Provider, error) {
	return append([]Provider(nil), s...), nil
}

func (p Provider) modelFor(usage string) (ProviderModel, bool) {
	want := strings.ToLower(strings.TrimSpace(usage))
	if want == "" {
		want = "chat"
	}
	for _, m := range p.Models {
		if strings.EqualFold(strings.TrimSpace(m.Usage), want) && strings.TrimSpace(m.Name) != "" {
			return m, true
		}
	}
	return ProviderModel{}, false
}

func (p Provider) baseOrDefault() string {
	if b := strings.TrimSpace(p.BaseURL); b != "" {
		return b
	}
	switch canonicalProviderType(p.Type) {
	case "anthropic":
		return "https://api.anthropic.com"
	case "grok":
		return "https://api.x.ai/v1"
	case "gemini":
		return "https://generativelanguage.googleapis.com/v1beta/openai"
	default:
		return "https://api.openai.com/v1"
	}
}

func canonicalProviderType(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "claude", "anthropic":
		return "anthropic"
	case "grok", "xai", "x.ai":
		return "grok"
	case "gemini", "google":
		return "gemini"
	case "openai":
		return "openai"
	default:
		return ""
	}
}

func findProvider(list []Provider, name string) (Provider, bool) {
	name = strings.TrimSpace(name)
	for _, p := range list {
		if p.Name == name {
			return p, true
		}
	}
	for _, p := range list {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Provider{}, false
}

func (g *gateway) providerRecords(ctx context.Context) ([]Provider, error) {
	if g.providers != nil {
		return g.providers.List(ctx)
	}
	return listClusterProviders(ctx)
}

func (g *gateway) listProviders(w http.ResponseWriter, r *http.Request) {
	recs, err := g.providerRecords(r.Context())
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	out := make([]providerView, 0, len(recs))
	for _, p := range recs {
		out = append(out, providerView{ID: p.Name, Type: canonicalProviderType(p.Type)})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"providers": out})
}

func (g *gateway) completeFromProvider(ctx context.Context, name, usage, system string, messages []chatMessage, maxTokens int) (text, model, provider string, err error) {
	recs, err := g.providerRecords(ctx)
	if err != nil {
		return "", "", "", err
	}
	p, ok := findProvider(recs, name)
	if !ok {
		return "", "", "", statusErr{s: "unknown provider " + strings.TrimSpace(name)}
	}
	if strings.TrimSpace(usage) == "" {
		usage = "chat"
	}
	chosen, ok := p.modelFor(usage)
	if !ok {
		return "", "", p.Name, statusErr{s: "provider " + p.Name + " has no model for usage " + usage}
	}
	if strings.TrimSpace(p.APIKey) == "" {
		return "", "", p.Name, statusErr{s: "provider " + p.Name + " has no apiKey"}
	}
	kind := canonicalProviderType(p.Type)
	switch kind {
	case "anthropic":
		text, err = g.anthropicCall(ctx, p.APIKey, p.baseOrDefault(), chosen.Name, chosen.Temperature, system, messages, maxTokens)
	case "openai", "grok", "gemini":
		text, err = g.openAICall(ctx, kind, p.APIKey, p.baseOrDefault(), chosen.Name, chosen.Temperature, system, messages, maxTokens)
	default:
		err = statusErr{s: "unsupported provider type " + p.Type}
	}
	return text, chosen.Name, p.Name, err
}

func (g *gateway) anthropicCall(ctx context.Context, key, base, model string, temp *float64, system string, messages []chatMessage, maxTokens int) (string, error) {
	mapped := make([]chatMessage, 0, len(messages))
	for _, m := range messages {
		role := m.Role
		if role != "user" && role != "assistant" {
			role = "user"
		}
		mapped = append(mapped, chatMessage{Role: role, Content: m.Content})
	}
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   mapped,
	}
	if temp != nil {
		body["temperature"] = *temp
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveAPIURL(base, "/v1/messages"), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")
	return g.readText(ctx, req, "anthropic", func(raw []byte) (string, error) {
		var parsed struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", err
		}
		var b strings.Builder
		for _, c := range parsed.Content {
			b.WriteString(c.Text)
		}
		return b.String(), nil
	})
}

func (g *gateway) openAICall(ctx context.Context, vendor, key, base, model string, temp *float64, system string, messages []chatMessage, maxTokens int) (string, error) {
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
	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   out,
	}
	if temp != nil {
		body["temperature"] = *temp
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveAPIURL(base, "/chat/completions"), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	return g.readText(ctx, req, vendor, func(raw []byte) (string, error) {
		var parsed struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				Text string `json:"text"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return "", err
		}
		if len(parsed.Choices) == 0 {
			return "", statusErr{s: vendor + ": empty choices"}
		}
		text := parsed.Choices[0].Message.Content
		if text == "" {
			text = parsed.Choices[0].Text
		}
		return text, nil
	})
}

func (g *gateway) readText(ctx context.Context, req *http.Request, vendor string, parse func([]byte) (string, error)) (string, error) {
	resp, err := g.http.Do(req.WithContext(ctx))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", errStatus(vendor, resp.StatusCode, raw)
	}
	return parse(raw)
}

func listClusterProviders(ctx context.Context) ([]Provider, error) {
	host := os.Getenv("KUBERNETES_SERVICE_HOST")
	port := os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, nil
	}
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return nil, err
	}
	caPEM, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("provider list: cluster CA")
	}
	nsB, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return nil, err
	}
	ns := strings.TrimSpace(string(nsB))
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			MinVersion: tls.VersionTLS12,
		}},
	}
	url := fmt.Sprintf("https://%s:%s/apis/atlas.platform/v1/namespaces/%s/providers", host, port, ns)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode >= 300 {
		return nil, statusErr{s: "list providers: " + resp.Status}
	}
	var listed struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec Provider `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, err
	}
	out := make([]Provider, 0, len(listed.Items))
	for _, item := range listed.Items {
		p := item.Spec
		p.Name = item.Metadata.Name
		if p.Name == "" {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}
