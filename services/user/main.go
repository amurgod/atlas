package main

import (
	"context"
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
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
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

	bus, err := events.Connect(ctx, config.Get("NATS_URL", "nats://nats:4222"), "user")
	if err != nil {
		slog.Error("nats", "err", err)
		os.Exit(1)
	}
	defer bus.Close()

	h := &handler{
		pool:   pool,
		bus:    bus,
		secret: config.Must("JWT_SECRET"),
		ttl:    config.Duration("TOKEN_TTL", 24*time.Hour),
		cost:   config.Int("BCRYPT_COST", 12),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", httpx.Healthz)
	mux.HandleFunc("POST /v1/auth/register", h.register)
	mux.HandleFunc("POST /v1/auth/login", h.login)
	mux.HandleFunc("GET /v1/me", httpx.RequireAuth(h.me))
	mux.HandleFunc("PATCH /v1/me", httpx.RequireAuth(h.updateMe))
	mux.HandleFunc("GET /v1/users", httpx.RequirePerm(auth.PermUsersAdmin, h.listUsers))
	mux.HandleFunc("POST /v1/users/{id}/roles", httpx.RequirePerm(auth.PermUsersAdmin, h.addRole))
	mux.HandleFunc("DELETE /v1/users/{id}/roles/{role}", httpx.RequirePerm(auth.PermUsersAdmin, h.removeRole))

	if err := httpx.Serve(httpx.Middleware(h.secret)(mux)); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return db.ExecAll(ctx, pool,
		`CREATE TABLE IF NOT EXISTS users (
			id UUID PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			display_name TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS user_roles (
			user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			role TEXT NOT NULL,
			PRIMARY KEY (user_id, role)
		)`,
	)
}

type handler struct {
	pool   *pgxpool.Pool
	bus    *events.Bus
	secret string
	ttl    time.Duration
	cost   int
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type userDTO struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Roles     []string  `json:"roles"`
	Perms     []string  `json:"perms"`
	CreatedAt time.Time `json:"created_at"`
	Token     string    `json:"token,omitempty"`
}

func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Name = strings.TrimSpace(in.Name)
	if in.Email == "" || !strings.Contains(in.Email, "@") {
		httpx.Error(w, http.StatusBadRequest, "valid email required")
		return
	}
	if len(in.Password) < 8 {
		httpx.Error(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if in.Name == "" {
		in.Name = strings.Split(in.Email, "@")[0]
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), h.cost)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not hash password")
		return
	}

	id := uuid.New()
	ctx := r.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	defer tx.Rollback(ctx)

	var count int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	roles := []string{auth.RoleUser}
	if count == 0 {
		roles = append(roles, auth.RoleAdmin)
	}

	_, err = tx.Exec(ctx, `INSERT INTO users (id, email, password_hash, display_name) VALUES ($1,$2,$3,$4)`,
		id, in.Email, string(hash), in.Name)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			httpx.Error(w, http.StatusConflict, "email already registered")
			return
		}
		httpx.Error(w, http.StatusInternalServerError, "could not create user")
		return
	}
	for _, role := range roles {
		if _, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role) VALUES ($1,$2)`, id, role); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "could not assign role")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not create user")
		return
	}

	_ = h.bus.Publish(ctx, events.UserCreated, events.UserCreatedPayload{
		UserID: id.String(),
		Email:  in.Email,
		Name:   in.Name,
	})
	h.respondToken(w, http.StatusCreated, id.String(), in.Email, in.Name, roles, time.Now())
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	u, roles, err := h.loadByEmail(r.Context(), in.Email)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.hash), []byte(in.Password)) != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.respondToken(w, http.StatusOK, u.id, u.email, u.name, roles, u.created)
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	c := httpx.ClaimsFrom(r.Context())
	u, roles, err := h.loadByID(r.Context(), c.Subject)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "user not found")
		return
	}
	httpx.JSON(w, http.StatusOK, userDTO{
		ID: u.id, Email: u.email, Name: u.name, Roles: roles,
		Perms: auth.PermsForRoles(roles), CreatedAt: u.created,
	})
}

func (h *handler) updateMe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := httpx.Decode(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpx.Error(w, http.StatusBadRequest, "name required")
		return
	}
	id := httpx.UserID(r.Context())
	_, err := h.pool.Exec(r.Context(), `UPDATE users SET display_name=$2 WHERE id=$1`, id, strings.TrimSpace(in.Name))
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "update failed")
		return
	}
	h.me(w, r)
}

func (h *handler) listUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `SELECT id, email, display_name, created_at FROM users ORDER BY created_at`)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	var out []userDTO
	for rows.Next() {
		var u userDTO
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.CreatedAt); err != nil {
			httpx.Error(w, http.StatusInternalServerError, "db error")
			return
		}
		roles, _ := h.roles(r.Context(), u.ID)
		u.Roles = roles
		u.Perms = auth.PermsForRoles(roles)
		out = append(out, u)
	}
	if out == nil {
		out = []userDTO{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"users": out})
}

func (h *handler) addRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Role string `json:"role"`
	}
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.Role = strings.TrimSpace(in.Role)
	if in.Role != auth.RoleUser && in.Role != auth.RoleAdmin {
		httpx.Error(w, http.StatusBadRequest, "role must be user or admin")
		return
	}
	_, err := h.pool.Exec(r.Context(), `INSERT INTO user_roles (user_id, role) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, in.Role)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not add role")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) removeRole(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	role := r.PathValue("role")
	if role == auth.RoleUser {
		httpx.Error(w, http.StatusBadRequest, "cannot remove the base user role")
		return
	}
	_, err := h.pool.Exec(r.Context(), `DELETE FROM user_roles WHERE user_id=$1 AND role=$2`, id, role)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not remove role")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) respondToken(w http.ResponseWriter, status int, id, email, name string, roles []string, created time.Time) {
	token, err := auth.Issue(h.secret, id, email, name, roles, h.ttl)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	httpx.JSON(w, status, userDTO{
		ID: id, Email: email, Name: name, Roles: roles,
		Perms: auth.PermsForRoles(roles), CreatedAt: created, Token: token,
	})
}

type record struct {
	id, email, name, hash string
	created               time.Time
}

func (h *handler) loadByEmail(ctx context.Context, email string) (record, []string, error) {
	var u record
	err := h.pool.QueryRow(ctx, `SELECT id::text, email, display_name, password_hash, created_at FROM users WHERE email=$1`, email).
		Scan(&u.id, &u.email, &u.name, &u.hash, &u.created)
	if err != nil {
		return u, nil, err
	}
	roles, err := h.roles(ctx, u.id)
	return u, roles, err
}

func (h *handler) loadByID(ctx context.Context, id string) (record, []string, error) {
	var u record
	err := h.pool.QueryRow(ctx, `SELECT id::text, email, display_name, password_hash, created_at FROM users WHERE id=$1`, id).
		Scan(&u.id, &u.email, &u.name, &u.hash, &u.created)
	if err != nil {
		if err == pgx.ErrNoRows {
			return u, nil, err
		}
		return u, nil, err
	}
	roles, err := h.roles(ctx, u.id)
	return u, roles, err
}

func (h *handler) roles(ctx context.Context, userID string) ([]string, error) {
	rows, err := h.pool.Query(ctx, `SELECT role FROM user_roles WHERE user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}
