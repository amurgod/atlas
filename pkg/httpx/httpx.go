package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/atlas/platform/pkg/auth"
	"github.com/atlas/platform/pkg/config"
	"github.com/google/uuid"
)

type ctxKey string

const (
	claimsKey ctxKey = "claims"
	reqIDKey  ctxKey = "reqid"
)

func ClaimsFrom(ctx context.Context) *auth.Claims {
	c, _ := ctx.Value(claimsKey).(*auth.Claims)
	return c
}

func UserID(ctx context.Context) string {
	c := ClaimsFrom(ctx)
	if c == nil {
		return ""
	}
	return c.Subject
}

func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}

func Decode(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func Middleware(secret string) func(http.Handler) http.Handler {
	origins := config.Get("CORS_ORIGINS", "*")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", origins)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			reqID := r.Header.Get("X-Request-Id")
			if reqID == "" {
				reqID = uuid.NewString()
			}
			w.Header().Set("X-Request-Id", reqID)
			ctx := context.WithValue(r.Context(), reqIDKey, reqID)

			header := r.Header.Get("Authorization")
			if header != "" && secret != "" {
				if claims, err := auth.Parse(secret, header); err == nil {
					ctx = context.WithValue(ctx, claimsKey, claims)
				}
			}
			start := time.Now()
			next.ServeHTTP(w, r.WithContext(ctx))
			slog.Info("http",
				"method", r.Method,
				"path", r.URL.Path,
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", reqID,
			)
		})
	}
}

func RequireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ClaimsFrom(r.Context()) == nil {
			Error(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r)
	}
}

func RequirePerm(perm string, next http.HandlerFunc) http.HandlerFunc {
	return RequireAuth(func(w http.ResponseWriter, r *http.Request) {
		c := ClaimsFrom(r.Context())
		if !auth.HasPerm(c.Perms, perm) {
			Error(w, http.StatusForbidden, "missing permission "+perm)
			return
		}
		next(w, r)
	})
}

func Healthz(w http.ResponseWriter, r *http.Request) {
	JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func Serve(handler http.Handler) error {
	addr := config.Get("HTTP_ADDR", ":8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       90 * time.Second,
		BaseContext: func(net.Listener) context.Context {
			return context.Background()
		},
	}
	slog.Info("listening", "addr", addr)
	return srv.ListenAndServe()
}

func Bearer(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("Authorization"))
}
