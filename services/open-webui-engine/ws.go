package main

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/atlas/platform/pkg/llmclient"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

type wsIn struct {
	Type    string `json:"type"`
	ChatID  string `json:"chat_id"`
	Content string `json:"content"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
	Provider string `json:"provider"`
}

type wsOut struct {
	Type    string   `json:"type"`
	Chat    *chatRow `json:"chat,omitempty"`
	Content string   `json:"content,omitempty"`
	Error   string   `json:"error,omitempty"`
	Sources []Hit    `json:"sources,omitempty"`
	Message string   `json:"message,omitempty"`
}

func (h *handler) chatSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx := r.Context()
	userID := uid(ctx)

	for {
		var in wsIn
		if err := conn.ReadJSON(&in); err != nil {
			return
		}
		if strings.ToLower(in.Type) != "send" {
			_ = conn.WriteJSON(wsOut{Type: "error", Error: "unknown type"})
			continue
		}
		in.Content = strings.TrimSpace(in.Content)
		if in.Content == "" {
			_ = conn.WriteJSON(wsOut{Type: "error", Error: "content required"})
			continue
		}
		chat, err := h.prepareChat(ctx, userID, in.ChatID)
		if err != nil {
			_ = conn.WriteJSON(wsOut{Type: "error", Error: "chat not found"})
			continue
		}
		_ = conn.WriteJSON(wsOut{Type: "chat", Chat: &chatRow{ID: chat.ID, Title: chat.Title, UpdatedAt: chat.UpdatedAt}})

		var hits []Hit
		if vec := firstEmbed(ctx, h, in.Content); len(vec) > 0 {
			hits, _ = h.vectors.Search(ctx, userID, vec, 6)
		}
		providerName := strings.TrimSpace(in.Provider)
		reply, err := h.completeChat(ctx, chat, in.Content, providerName, hits, func(delta string) error {
			return conn.WriteJSON(wsOut{Type: "token", Content: delta})
		})
		if err != nil {
			slog.Error("engine chat", "err", err)
			_ = conn.WriteJSON(wsOut{Type: "error", Error: err.Error()})
			continue
		}
		_ = conn.WriteJSON(wsOut{Type: "done", Message: reply, Sources: hits, Chat: chat})
	}
}

func (h *handler) prepareChat(ctx context.Context, userID, id string) (*chatRow, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return h.insertChat(ctx, userID, "New chat", nil)
	}
	return h.loadChat(ctx, userID, id)
}

func firstEmbed(ctx context.Context, h *handler, text string) []float32 {
	emb, err := h.llm.Embed(ctx, []string{text})
	if err != nil || len(emb.Embeddings) == 0 {
		return nil
	}
	return emb.Embeddings[0]
}

func (h *handler) completeChat(ctx context.Context, chat *chatRow, userText, providerName string, hits []Hit, onDelta func(string) error) (string, error) {
	chat.Messages = append(chat.Messages, chatMessage{Role: "user", Content: userText})
	if err := h.saveChat(ctx, uid(ctx), chat); err != nil {
		slog.Error("save chat", "err", err)
	}
	system := "You are Atlas chat. Answer from the user's documents when they are relevant. If no document applies, say so briefly and still help."
	if len(hits) > 0 {
		var b strings.Builder
		b.WriteString(system)
		b.WriteString("\n\nRetrieved context:\n")
		for i, hit := range hits {
			if i >= 6 {
				break
			}
			b.WriteString("- ")
			b.WriteString(hit.Title)
			b.WriteString(": ")
			b.WriteString(hit.Content)
			b.WriteByte('\n')
		}
		system = b.String()
	}
	history := make([]llmclient.Message, 0, len(chat.Messages))
	for _, m := range chat.Messages {
		history = append(history, llmclient.Message{Role: m.Role, Content: m.Content})
	}
	out, err := h.llm.ChatStream(ctx, llmclient.CompleteRequest{
		System:    system,
		Messages:  history,
		MaxTokens: 1024,
		Provider:  providerName,
		Usage:     "chat",
	}, onDelta)
	if err != nil {
		out, err = h.llm.Complete(ctx, llmclient.CompleteRequest{
			System:    system,
			Messages:  history,
			MaxTokens: 1024,
			Provider:  providerName,
			Usage:     "chat",
		})
		if err != nil {
			return "", err
		}
		if onDelta != nil && out.Text != "" {
			_ = onDelta(out.Text)
		}
	}
	chat.Messages = append(chat.Messages, chatMessage{Role: "assistant", Content: out.Text})
	if err := h.saveChat(ctx, uid(ctx), chat); err != nil {
		slog.Error("save chat", "err", err)
	}
	return out.Text, nil
}
