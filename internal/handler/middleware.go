package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"marketplace/internal/security"
	"marketplace/internal/service"
	"net/http"
	"strings"
	"time"
)

type requestState struct{ user *security.Principal }
type requestStateKey struct{}
type responseStatus struct {
	http.ResponseWriter
	status int
}

func (w *responseStatus) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *responseStatus) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(data)
}
func (w *responseStatus) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func Audit(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := uuid.NewString()
		state := &requestState{}
		r = r.WithContext(context.WithValue(r.Context(), requestStateKey{}, state))
		w.Header().Set("X-Request-Id", id)
		writer := &responseStatus{ResponseWriter: w}
		var body any
		mutated := r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE"
		if mutated && r.Body != nil {
			original := r.Body
			data, err := io.ReadAll(io.LimitReader(original, (1<<20)+1))
			r.Body = struct {
				io.Reader
				io.Closer
			}{io.MultiReader(bytes.NewReader(data), original), original}
			if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &body) != nil {
				body = "[body omitted]"
			} else {
				body = redact(body, strings.HasPrefix(r.URL.Path, "/auth/"))
			}
		}
		defer func() {
			if recover() != nil {
				if writer.status == 0 {
					writeAPIError(writer, 500, "INTERNAL_ERROR", "Internal server error", nil)
				}
			}
			if writer.status == 0 {
				writer.status = 200
			}
			var user any
			if state.user != nil {
				user = state.user.ID.String()
			}
			attributes := []any{"request_id", id, "method", r.Method, "endpoint", r.URL.Path, "status_code", writer.status,
				"duration_ms", time.Since(start).Milliseconds(), "user_id", user, "timestamp", start.UTC().Format(time.RFC3339Nano)}
			if mutated {
				attributes = append(attributes, "request_body", body)
			}
			logger.Info("http_request", attributes...)
		}()
		next.ServeHTTP(writer, r)
	})
}
func redact(value any, auth bool) any {
	switch object := value.(type) {
	case map[string]any:
		for key, item := range object {
			lower := strings.ToLower(key)
			sensitive := auth && lower != "email"
			for _, word := range []string{"password", "token", "secret", "authorization", "cookie", "api_key", "private_key", "credential"} {
				sensitive = sensitive || strings.Contains(lower, word)
			}
			if sensitive {
				object[key] = "[REDACTED]"
			} else {
				object[key] = redact(item, false)
			}
		}
	case []any:
		for i, item := range object {
			object[i] = redact(item, auth)
		}
	case string:
		if auth {
			return "[REDACTED]"
		}
		if strings.HasPrefix(object, "eyJ") && strings.Count(object, ".") == 2 {
			return "[REDACTED]"
		}
	}
	return value
}
func Secure(next http.Handler, auth *service.Auth) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/register" || r.URL.Path == "/auth/login" || r.URL.Path == "/auth/refresh" {
			next.ServeHTTP(w, r)
			return
		}
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			writeAPIError(w, 401, "TOKEN_INVALID", "Invalid token", nil)
			return
		}
		actor, err := auth.Authenticate(r.Context(), parts[1])
		if err != nil {
			writeError(w, err)
			return
		}
		if state, ok := r.Context().Value(requestStateKey{}).(*requestState); ok {
			state.user = &actor
		}
		r = r.WithContext(security.WithPrincipal(r.Context(), actor))
		path := r.URL.Path
		denied := ((path == "/products" || strings.HasPrefix(path, "/products/")) && r.Method != "GET" && r.Method != "HEAD" && actor.Role == "USER") ||
			((path == "/orders" || strings.HasPrefix(path, "/orders/")) && actor.Role == "SELLER") ||
			(path == "/promo-codes" && actor.Role == "USER")
		if denied {
			writeAPIError(w, 403, "ACCESS_DENIED", "Access denied", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
