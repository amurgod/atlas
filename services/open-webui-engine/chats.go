package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/atlas/platform/pkg/httpx"
	"github.com/google/uuid"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRow struct {
	ID        string        `json:"id"`
	Title     string        `json:"title"`
	Messages  []chatMessage `json:"messages,omitempty"`
	UpdatedAt time.Time     `json:"updated_at"`
}

func (h *handler) listChats(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(),
		`SELECT id::text, title, updated_at FROM engine_chats WHERE user_id=$1 ORDER BY updated_at DESC`,
		httpx.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	out := []chatRow{}
	for rows.Next() {
		var c chatRow
		if err := rows.Scan(&c.ID, &c.Title, &c.UpdatedAt); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "db error")
			return
		}
		out = append(out, c)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"chats": out})
}

func (h *handler) createChat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
	}
	_ = httpx.Decode(r, &in)
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = "New chat"
	}
	c, err := h.insertChat(r.Context(), httpx.UserID(r.Context()), title, nil)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not create chat")
		return
	}
	httpx.JSON(w, http.StatusCreated, c)
}

func (h *handler) getChat(w http.ResponseWriter, r *http.Request) {
	c, err := h.loadChat(r.Context(), httpx.UserID(r.Context()), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "chat not found")
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (h *handler) removeChat(w http.ResponseWriter, r *http.Request) {
	tag, err := h.pool.Exec(r.Context(), `DELETE FROM engine_chats WHERE id=$1 AND user_id=$2`,
		r.PathValue("id"), httpx.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "chat not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) insertChat(ctx context.Context, userID, title string, messages []chatMessage) (*chatRow, error) {
	if messages == nil {
		messages = []chatMessage{}
	}
	id := uuid.New()
	raw, _ := json.Marshal(messages)
	var updated time.Time
	err := h.pool.QueryRow(ctx, `
		INSERT INTO engine_chats (id, user_id, title, messages) VALUES ($1,$2,$3,$4)
		RETURNING updated_at`,
		id, userID, title, raw).Scan(&updated)
	if err != nil {
		return nil, err
	}
	row := &chatRow{ID: id.String(), Title: title, Messages: messages, UpdatedAt: updated}
	go h.syncWebUI(row)
	return row, nil
}

func (h *handler) loadChat(ctx context.Context, userID, id string) (*chatRow, error) {
	var c chatRow
	var raw []byte
	err := h.pool.QueryRow(ctx,
		`SELECT id::text, title, messages, updated_at FROM engine_chats WHERE id=$1 AND user_id=$2`,
		id, userID).Scan(&c.ID, &c.Title, &raw, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &c.Messages); err != nil {
		c.Messages = []chatMessage{}
	}
	return &c, nil
}

func (h *handler) saveChat(ctx context.Context, userID string, c *chatRow) error {
	if strings.TrimSpace(c.Title) == "" || c.Title == "New chat" {
		for _, m := range c.Messages {
			if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
				c.Title = truncate(m.Content, 48)
				break
			}
		}
	}
	raw, _ := json.Marshal(c.Messages)
	err := h.pool.QueryRow(ctx, `
		UPDATE engine_chats SET title=$3, messages=$4, updated_at=now()
		WHERE id=$1 AND user_id=$2 RETURNING updated_at`,
		c.ID, userID, c.Title, raw).Scan(&c.UpdatedAt)
	if err == nil {
		go h.syncWebUI(c)
	}
	return err
}

func (h *handler) syncWebUI(c *chatRow) {
	if h.webui == nil || c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = h.webui.putChat(ctx, c.ID, c.Title, c.Messages)
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
