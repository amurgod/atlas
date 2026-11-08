package main

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

type webuiClient struct {
	base   string
	http   *http.Client
	apiKey string
}

func newWebUIClient(base, apiKey string) *webuiClient {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return nil
	}
	return &webuiClient{
		base:   base,
		apiKey: apiKey,
		http:   &http.Client{Timeout: 8 * time.Second},
	}
}

func (c *webuiClient) healthy(ctx context.Context) bool {
	if c == nil {
		return false
	}
	for _, path := range []string{"/health", "/healthz", "/"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
		if err != nil {
			continue
		}
		resp, err := c.http.Do(req)
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode < 500 {
			return true
		}
	}
	return false
}

type owuiChat struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func (c *webuiClient) listChats(ctx context.Context) ([]owuiChat, error) {
	if c == nil {
		return nil, fmt.Errorf("open-webui not configured")
	}
	var out []owuiChat
	if err := c.get(ctx, "/api/v1/chats/", &out); err != nil {
		var wrapped struct {
			Chats []owuiChat `json:"chats"`
		}
		if err2 := c.get(ctx, "/api/v1/chats/", &wrapped); err2 != nil {
			return nil, err
		}
		return wrapped.Chats, nil
	}
	return out, nil
}

func (c *webuiClient) putChat(ctx context.Context, id, title string, messages []chatMessage) error {
	if c == nil {
		return nil
	}
	body := map[string]any{
		"chat": map[string]any{
			"id":       id,
			"title":    title,
			"messages": messages,
		},
	}
	if err := c.post(ctx, "/api/v1/chats/new", body, nil); err != nil {
		return c.post(ctx, "/api/v1/chats/"+id, body, nil)
	}
	return nil
}

func (c *webuiClient) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("open-webui GET %s: %d", path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *webuiClient) post(ctx context.Context, path string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.auth(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("open-webui POST %s: %d", path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *webuiClient) auth(req *http.Request) {
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
}
