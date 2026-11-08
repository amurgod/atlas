package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	base   string
	http   *http.Client
	apiKey string
}

func New(base string) *Client {
	return &Client{
		base: base,
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

type EmbedRequest struct {
	Texts []string `json:"texts"`
}

type EmbedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Model      string      `json:"model"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CompleteRequest struct {
	System    string    `json:"system"`
	Messages  []Message `json:"messages"`
	MaxTokens int       `json:"max_tokens"`
	Model     string    `json:"model,omitempty"`
	Provider  string    `json:"provider,omitempty"`
	Usage     string    `json:"usage,omitempty"`
}

type ProviderInfo struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type CompleteResponse struct {
	Text     string `json:"text"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
}

func (c *Client) Providers(ctx context.Context) ([]ProviderInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/providers", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, readGatewayErr(resp, "/v1/providers")
	}
	var out struct {
		Providers []ProviderInfo `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Providers, nil
}

func (c *Client) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("llm-gateway /v1/models: status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

func (c *Client) Embed(ctx context.Context, texts []string) (*EmbedResponse, error) {
	var out EmbedResponse
	if err := c.post(ctx, "/v1/embed", EmbedRequest{Texts: texts}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) Complete(ctx context.Context, req CompleteRequest) (*CompleteResponse, error) {
	if req.MaxTokens == 0 {
		req.MaxTokens = 1024
	}
	var out CompleteResponse
	if err := c.post(ctx, "/v1/complete", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return readGatewayErr(resp, path)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func readGatewayErr(resp *http.Response, path string) error {
	var body struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if json.Unmarshal(raw, &body) == nil && strings.TrimSpace(body.Error) != "" {
		return fmt.Errorf("%s", body.Error)
	}
	return fmt.Errorf("llm-gateway %s: status %d", path, resp.StatusCode)
}
