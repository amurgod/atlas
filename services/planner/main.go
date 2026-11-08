package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/atlas/platform/pkg/auth"
	"github.com/atlas/platform/pkg/config"
	"github.com/atlas/platform/pkg/db"
	"github.com/atlas/platform/pkg/events"
	"github.com/atlas/platform/pkg/httpx"
	"github.com/atlas/platform/pkg/llmclient"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx := context.Background()

	pool, err := db.Connect(ctx, config.Must("DATABASE_URL"), false)
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := migrate(ctx, pool); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	bus, err := events.Connect(ctx, config.Get("NATS_URL", "nats://nats:4222"), "planner")
	if err != nil {
		slog.Error("nats", "err", err)
		os.Exit(1)
	}
	defer bus.Close()

	h := &handler{
		pool: pool,
		bus:  bus,
		llm:  llmclient.New(config.Get("LLM_GATEWAY_URL", "http://llm-gateway:8080")),
	}

	if _, err := bus.Subscribe("planner-user-created", events.UserCreated, h.onUserCreated); err != nil {
		slog.Error("subscribe user.created", "err", err)
		os.Exit(1)
	}
	if _, err := bus.Subscribe("planner-doc-ingested", events.DocumentIngested, h.onDocumentIngested); err != nil {
		slog.Error("subscribe document.ingested", "err", err)
		os.Exit(1)
	}

	go h.scanDue(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	mux.HandleFunc("GET /v1/tracks", httpx.RequirePerm(auth.PermPlannerRead, h.listTracks))
	mux.HandleFunc("POST /v1/tracks", httpx.RequirePerm(auth.PermPlannerWrite, h.createTrack))
	mux.HandleFunc("PATCH /v1/tracks/{id}", httpx.RequirePerm(auth.PermPlannerWrite, h.updateTrack))
	mux.HandleFunc("DELETE /v1/tracks/{id}", httpx.RequirePerm(auth.PermPlannerWrite, h.deleteTrack))
	mux.HandleFunc("GET /v1/notes", httpx.RequirePerm(auth.PermPlannerRead, h.listNotes))
	mux.HandleFunc("POST /v1/notes", httpx.RequirePerm(auth.PermPlannerWrite, h.createNote))
	mux.HandleFunc("DELETE /v1/notes/{id}", httpx.RequirePerm(auth.PermPlannerWrite, h.deleteNote))
	mux.HandleFunc("GET /v1/reminders", httpx.RequirePerm(auth.PermPlannerRead, h.listReminders))
	mux.HandleFunc("POST /v1/reminders", httpx.RequirePerm(auth.PermPlannerWrite, h.createReminder))
	mux.HandleFunc("DELETE /v1/reminders/{id}", httpx.RequirePerm(auth.PermPlannerWrite, h.deleteReminder))
	mux.HandleFunc("POST /v1/assist", httpx.RequirePerm(auth.PermPlannerWrite, h.assist))

	if err := httpx.Serve(httpx.Middleware(config.Must("JWT_SECRET"))(mux)); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return db.ExecAll(ctx, pool,
		`CREATE TABLE IF NOT EXISTS tracks (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL,
			title TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS notes (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL,
			track_id UUID REFERENCES tracks(id) ON DELETE SET NULL,
			body TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS reminders (
			id UUID PRIMARY KEY,
			user_id UUID NOT NULL,
			track_id UUID REFERENCES tracks(id) ON DELETE SET NULL,
			title TEXT NOT NULL,
			body TEXT NOT NULL DEFAULT '',
			due_at TIMESTAMPTZ NOT NULL,
			channel TEXT NOT NULL DEFAULT 'email',
			destination TEXT NOT NULL,
			fired_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS reminders_due_idx ON reminders (due_at) WHERE fired_at IS NULL`,
	)
}

type handler struct {
	pool *pgxpool.Pool
	bus  *events.Bus
	llm  *llmclient.Client
}

type track struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type note struct {
	ID        string    `json:"id"`
	TrackID   *string   `json:"track_id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

type reminder struct {
	ID          string     `json:"id"`
	TrackID     *string    `json:"track_id"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	DueAt       time.Time  `json:"due_at"`
	Channel     string     `json:"channel"`
	Destination string     `json:"destination"`
	FiredAt     *time.Time `json:"fired_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (h *handler) listTracks(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(),
		`SELECT id::text, title, status, created_at FROM tracks WHERE user_id=$1 ORDER BY created_at`,
		httpx.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	out := []track{}
	for rows.Next() {
		var t track
		if err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.CreatedAt); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "db error")
			return
		}
		out = append(out, t)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"tracks": out})
}

func (h *handler) createTrack(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title string `json:"title"`
	}
	if err := httpx.Decode(r, &in); err != nil || strings.TrimSpace(in.Title) == "" {
		httpx.Error(w, http.StatusBadRequest, "title required")
		return
	}
	t, err := h.insertTrack(r.Context(), httpx.UserID(r.Context()), strings.TrimSpace(in.Title))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not create track")
		return
	}
	httpx.JSON(w, http.StatusCreated, t)
}

func (h *handler) updateTrack(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title  *string `json:"title"`
		Status *string `json:"status"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	tag, err := h.pool.Exec(r.Context(),
		`UPDATE tracks SET
			title = COALESCE($3, title),
			status = COALESCE($4, status)
		 WHERE id=$1 AND user_id=$2`,
		r.PathValue("id"), httpx.UserID(r.Context()), in.Title, in.Status)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "track not found")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) deleteTrack(w http.ResponseWriter, r *http.Request) {
	tag, err := h.pool.Exec(r.Context(), `DELETE FROM tracks WHERE id=$1 AND user_id=$2`,
		r.PathValue("id"), httpx.UserID(r.Context()))
	if err != nil || tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "track not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) listNotes(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(),
		`SELECT id::text, track_id::text, body, created_at FROM notes WHERE user_id=$1 ORDER BY created_at DESC`,
		httpx.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	out := []note{}
	for rows.Next() {
		var n note
		var trackID *string
		if err := rows.Scan(&n.ID, &trackID, &n.Body, &n.CreatedAt); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "db error")
			return
		}
		if trackID != nil && *trackID != "" {
			n.TrackID = trackID
		}
		out = append(out, n)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"notes": out})
}

func (h *handler) createNote(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TrackID *string `json:"track_id"`
		Body    string  `json:"body"`
	}
	if err := httpx.Decode(r, &in); err != nil || strings.TrimSpace(in.Body) == "" {
		httpx.Error(w, http.StatusBadRequest, "body required")
		return
	}
	n, err := h.insertNote(r.Context(), httpx.UserID(r.Context()), in.TrackID, strings.TrimSpace(in.Body))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not create note")
		return
	}
	httpx.JSON(w, http.StatusCreated, n)
}

func (h *handler) deleteNote(w http.ResponseWriter, r *http.Request) {
	tag, err := h.pool.Exec(r.Context(), `DELETE FROM notes WHERE id=$1 AND user_id=$2`,
		r.PathValue("id"), httpx.UserID(r.Context()))
	if err != nil || tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "note not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) listReminders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(),
		`SELECT id::text, track_id::text, title, body, due_at, channel, destination, fired_at, created_at
		 FROM reminders WHERE user_id=$1 ORDER BY due_at`,
		httpx.UserID(r.Context()))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	out := []reminder{}
	for rows.Next() {
		var rem reminder
		var trackID *string
		if err := rows.Scan(&rem.ID, &trackID, &rem.Title, &rem.Body, &rem.DueAt, &rem.Channel, &rem.Destination, &rem.FiredAt, &rem.CreatedAt); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "db error")
			return
		}
		rem.TrackID = trackID
		out = append(out, rem)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"reminders": out})
}

func (h *handler) createReminder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		TrackID     *string `json:"track_id"`
		Title       string  `json:"title"`
		Body        string  `json:"body"`
		DueAt       string  `json:"due_at"`
		Channel     string  `json:"channel"`
		Destination string  `json:"destination"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	due, err := time.Parse(time.RFC3339, in.DueAt)
	if err != nil {
		httpx.Error(w, http.StatusBadRequest, "due_at must be RFC3339")
		return
	}
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Destination) == "" {
		httpx.Error(w, http.StatusBadRequest, "title and destination required")
		return
	}
	if in.Channel == "" {
		in.Channel = "email"
	}
	if in.Channel != "email" && in.Channel != "sms" {
		httpx.Error(w, http.StatusBadRequest, "channel must be email or sms")
		return
	}
	rem, err := h.insertReminder(r.Context(), httpx.UserID(r.Context()), in.TrackID, strings.TrimSpace(in.Title), in.Body, due, in.Channel, in.Destination)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not create reminder")
		return
	}
	httpx.JSON(w, http.StatusCreated, rem)
}

func (h *handler) deleteReminder(w http.ResponseWriter, r *http.Request) {
	tag, err := h.pool.Exec(r.Context(), `DELETE FROM reminders WHERE id=$1 AND user_id=$2`,
		r.PathValue("id"), httpx.UserID(r.Context()))
	if err != nil || tag.RowsAffected() == 0 {
		httpx.Error(w, http.StatusNotFound, "reminder not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) assist(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text        string `json:"text"`
		Destination string `json:"destination"`
	}
	if err := httpx.Decode(r, &in); err != nil || strings.TrimSpace(in.Text) == "" {
		httpx.Error(w, http.StatusBadRequest, "text required")
		return
	}
	claims := httpx.ClaimsFrom(r.Context())
	dest := strings.TrimSpace(in.Destination)
	if dest == "" {
		dest = claims.Email
	}
	out, err := h.llm.Complete(r.Context(), llmclient.CompleteRequest{
		System: `Extract planning items from the user's text. Return JSON only:
{"track":"optional track name or empty","note":"optional note or empty","reminder":{"title":"","due_at":"RFC3339 or empty","body":""}}
If no due date is present, leave reminder.title empty. Use ISO-8601 timestamps.`,
		Messages:  []llmclient.Message{{Role: "user", Content: in.Text}},
		MaxTokens: 400,
	})
	if err != nil {
		httpx.Error(w, http.StatusBadGateway, "completion failed")
		return
	}
	parsed := extractAssistJSON(out.Text)
	userID := claims.Subject
	result := map[string]any{"model": out.Model, "raw": out.Text}

	var trackID *string
	if title, _ := parsed["track"].(string); strings.TrimSpace(title) != "" {
		t, err := h.insertTrack(r.Context(), userID, strings.TrimSpace(title))
		if err == nil {
			trackID = &t.ID
			result["track"] = t
		}
	}
	if noteBody, _ := parsed["note"].(string); strings.TrimSpace(noteBody) != "" {
		n, err := h.insertNote(r.Context(), userID, trackID, strings.TrimSpace(noteBody))
		if err == nil {
			result["note"] = n
		}
	}
	if rem, ok := parsed["reminder"].(map[string]any); ok {
		title, _ := rem["title"].(string)
		dueStr, _ := rem["due_at"].(string)
		body, _ := rem["body"].(string)
		if strings.TrimSpace(title) != "" && strings.TrimSpace(dueStr) != "" {
			if due, err := time.Parse(time.RFC3339, dueStr); err == nil {
				created, err := h.insertReminder(r.Context(), userID, trackID, title, body, due, "email", dest)
				if err == nil {
					result["reminder"] = created
				}
			}
		}
	}
	httpx.JSON(w, http.StatusOK, result)
}

func extractAssistJSON(text string) map[string]any {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	out := map[string]any{}
	if start < 0 || end <= start {
		return out
	}
	_ = json.Unmarshal([]byte(text[start:end+1]), &out)
	return out
}

func (h *handler) insertTrack(ctx context.Context, userID, title string) (track, error) {
	t := track{ID: uuid.NewString(), Title: title, Status: "active", CreatedAt: time.Now().UTC()}
	_, err := h.pool.Exec(ctx, `INSERT INTO tracks (id, user_id, title, status, created_at) VALUES ($1,$2,$3,$4,$5)`,
		t.ID, userID, t.Title, t.Status, t.CreatedAt)
	return t, err
}

func (h *handler) insertNote(ctx context.Context, userID string, trackID *string, body string) (note, error) {
	n := note{ID: uuid.NewString(), TrackID: trackID, Body: body, CreatedAt: time.Now().UTC()}
	_, err := h.pool.Exec(ctx, `INSERT INTO notes (id, user_id, track_id, body, created_at) VALUES ($1,$2,$3,$4,$5)`,
		n.ID, userID, trackID, n.Body, n.CreatedAt)
	return n, err
}

func (h *handler) insertReminder(ctx context.Context, userID string, trackID *string, title, body string, due time.Time, channel, dest string) (reminder, error) {
	rem := reminder{
		ID: uuid.NewString(), TrackID: trackID, Title: title, Body: body,
		DueAt: due, Channel: channel, Destination: dest, CreatedAt: time.Now().UTC(),
	}
	_, err := h.pool.Exec(ctx,
		`INSERT INTO reminders (id, user_id, track_id, title, body, due_at, channel, destination, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		rem.ID, userID, trackID, rem.Title, rem.Body, rem.DueAt, rem.Channel, rem.Destination, rem.CreatedAt)
	return rem, err
}

func (h *handler) inboxTrackID(ctx context.Context, userID string) (string, error) {
	var id string
	err := h.pool.QueryRow(ctx, `SELECT id::text FROM tracks WHERE user_id=$1 AND title='Inbox' ORDER BY created_at LIMIT 1`, userID).Scan(&id)
	if err == nil {
		return id, nil
	}
	t, err := h.insertTrack(ctx, userID, "Inbox")
	return t.ID, err
}

func (h *handler) onUserCreated(env events.Envelope) error {
	p, err := events.Decode[events.UserCreatedPayload](env)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if _, err := h.insertTrack(ctx, p.UserID, "Inbox"); err != nil {
		return err
	}
	_, err = h.insertNote(ctx, p.UserID, nil, "Welcome to Atlas. Store a note in Knowledge, then ask about it in Chat.")
	_ = h.bus.Publish(ctx, events.NotificationRequested, events.NotificationRequestedPayload{
		UserID:      p.UserID,
		Channel:     "email",
		Destination: p.Email,
		Subject:     "Welcome to Atlas",
		Body:        "Your account is ready. The first user is granted admin so you can manage roles.",
	})
	return err
}

func (h *handler) onDocumentIngested(env events.Envelope) error {
	p, err := events.Decode[events.DocumentIngestedPayload](env)
	if err != nil {
		return err
	}
	ctx := context.Background()
	trackID, err := h.inboxTrackID(ctx, p.UserID)
	if err != nil {
		return err
	}
	_, err = h.insertNote(ctx, p.UserID, &trackID, "Stored note: "+p.Title)
	return err
}

func (h *handler) scanDue(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.fireDue(ctx)
		}
	}
}

func (h *handler) fireDue(ctx context.Context) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		slog.Error("scan reminders begin", "err", err)
		return
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		SELECT id::text, user_id::text, title, body, channel, destination
		FROM reminders
		WHERE fired_at IS NULL AND due_at <= now()
		ORDER BY due_at
		FOR UPDATE SKIP LOCKED
		LIMIT 50`)
	if err != nil {
		slog.Error("scan reminders", "err", err)
		return
	}
	type due struct {
		id, userID, title, body, channel, dest string
	}
	var items []due
	for rows.Next() {
		var d due
		if err := rows.Scan(&d.id, &d.userID, &d.title, &d.body, &d.channel, &d.dest); err != nil {
			rows.Close()
			slog.Error("scan reminder row", "err", err)
			return
		}
		items = append(items, d)
	}
	rows.Close()
	for _, d := range items {
		payload := events.ReminderDuePayload{
			UserID: d.userID, ReminderID: d.id, Title: d.title,
			Channel: d.channel, Destination: d.dest, Body: d.body,
		}
		if err := h.bus.Publish(ctx, events.ReminderDue, payload); err != nil {
			slog.Error("publish reminder.due", "err", err)
			continue
		}
		if err := h.bus.Publish(ctx, events.NotificationRequested, events.NotificationRequestedPayload{
			UserID: d.userID, Channel: d.channel, Destination: d.dest,
			Subject: "Reminder: " + d.title, Body: d.body,
		}); err != nil {
			slog.Error("publish notification", "err", err)
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE reminders SET fired_at=now() WHERE id=$1`, d.id); err != nil {
			slog.Error("mark fired", "err", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		slog.Error("commit reminders", "err", err)
	}
}
