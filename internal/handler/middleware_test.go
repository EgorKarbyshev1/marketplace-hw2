package handler

import (
	"bytes"
	"encoding/json"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuditMasksSecretsAndPreservesBody(t *testing.T) {
	var logs bytes.Buffer
	payload := `{"email":"user@example.test","password":"never-log-this-password","refresh_token":"never-log-this-token","nested":{"secret":"never-log-secret"}}`
	router := Audit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != payload {
			t.Error("request body changed")
		}
		w.WriteHeader(401)
	}), slog.New(slog.NewJSONHandler(&logs, nil)))
	ids := map[string]bool{}
	for range 2 {
		response := httptest.NewRecorder()
		request := httptest.NewRequest("POST", "/auth/login", strings.NewReader(payload))
		request.Header.Set("X-Request-Id", "untrusted")
		router.ServeHTTP(response, request)
		id := response.Header().Get("X-Request-Id")
		if _, err := uuid.Parse(id); err != nil || ids[id] {
			t.Fatal("request id is invalid or reused")
		}
		ids[id] = true
	}
	if strings.Contains(logs.String(), "never-log") {
		t.Fatalf("secret in log: %s", logs.String())
	}
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("duplicate request logs: %d", len(lines))
	}
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"request_id", "method", "endpoint", "status_code", "duration_ms", "user_id", "timestamp", "request_body"} {
			if _, ok := event[field]; !ok {
				t.Errorf("missing %s", field)
			}
		}
		if event["status_code"] != float64(401) || event["user_id"] != nil {
			t.Fatal("incorrect log status or user")
		}
	}
}
func TestAuditMalformedAndPanic(t *testing.T) {
	var logs bytes.Buffer
	handler := Audit(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("internal") }), slog.New(slog.NewJSONHandler(&logs, nil)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("PUT", "/products", strings.NewReader(`{"password":"never-log"} trailing`)))
	if response.Code != 500 || strings.Contains(logs.String(), "never-log") {
		t.Fatal("panic or body redaction failed")
	}
	var event map[string]any
	if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event["status_code"] != float64(500) {
		t.Fatal("incorrect panic status")
	}
}
