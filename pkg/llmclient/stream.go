package llmclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// ChatStream calls the gateway OpenAI-compatible SSE endpoint and invokes onDelta
// for each content chunk. onDelta may be nil.
func (c *Client) ChatStream(ctx context.Context, req CompleteRequest, onDelta func(string) error) (*CompleteResponse, error) {
	if req.MaxTokens == 0 {
		req.MaxTokens = 1024
	}
	msgs := make([]map[string]string, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.System) != "" {
		msgs = append(msgs, map[string]string{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}
	body := map[string]any{
		"messages":   msgs,
		"stream":     true,
		"max_tokens": req.MaxTokens,
	}
	if strings.TrimSpace(req.Model) != "" {
		body["model"] = req.Model
	}
	if strings.TrimSpace(req.Provider) != "" {
		body["provider"] = req.Provider
	}
	if strings.TrimSpace(req.Usage) != "" {
		body["usage"] = req.Usage
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, readGatewayErr(resp, "/v1/chat/completions")
	}

	var text strings.Builder
	model := ""
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		piece := chunk.Choices[0].Delta.Content
		if piece == "" {
			continue
		}
		text.WriteString(piece)
		if onDelta != nil {
			if err := onDelta(piece); err != nil {
				return nil, err
			}
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		return nil, err
	}
	return &CompleteResponse{Text: text.String(), Model: model, Provider: "gateway"}, nil
}
