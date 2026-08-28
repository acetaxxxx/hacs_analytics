package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeReadiness struct {
	err error
}

func (f fakeReadiness) Ping(context.Context) error { return f.err }

func TestHealthReportsReadinessAndGeminiConfiguration(t *testing.T) {
	server, err := NewServer(Config{
		SharedToken:      "secret",
		GeminiConfigured: true,
	}, fakeReadiness{})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	resp := httptest.NewRecorder()
	server.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", resp.Code, http.StatusOK)
	}
	var got Health
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if got.Status != "ok" || got.Database != "ready" || !got.GeminiConfigured {
		t.Fatalf("unexpected health response: %+v", got)
	}

	notReady, err := NewServer(Config{SharedToken: "secret"}, fakeReadiness{err: errors.New("down")})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	resp = httptest.NewRecorder()
	notReady.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status = %d, want %d", resp.Code, http.StatusServiceUnavailable)
	}
}

func TestLivenessDoesNotRequireDatabase(t *testing.T) {
	server, err := NewServer(Config{SharedToken: "secret"}, fakeReadiness{err: errors.New("down")})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	resp := httptest.NewRecorder()
	server.Handler().ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/api/v1/health/live", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("liveness status = %d, want %d", resp.Code, http.StatusOK)
	}
}

func TestProtectedEndpointRequiresAuthRequestIDAndContract(t *testing.T) {
	server, err := NewServer(Config{SharedToken: "secret"}, fakeReadiness{})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	request := func(token, requestID, contract string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/events", strings.NewReader(`{}`))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if requestID != "" {
			req.Header.Set(requestIDHeader, requestID)
		}
		if contract != "" {
			req.Header.Set(contractHeader, contract)
		}
		resp := httptest.NewRecorder()
		server.Handler().ServeHTTP(resp, req)
		return resp
	}

	if got := request("", "req-1", "1").Code; got != http.StatusUnauthorized {
		t.Fatalf("missing auth status = %d", got)
	}
	if got := request("secret", "", "1").Code; got != http.StatusBadRequest {
		t.Fatalf("missing request ID status = %d", got)
	}
	if got := request("secret", "req-1", "2").Code; got != http.StatusConflict {
		t.Fatalf("wrong contract status = %d", got)
	}
	if got := request("secret", "req-1", "1").Code; got != http.StatusNotImplemented {
		t.Fatalf("reserved endpoint status = %d, want %d", got, http.StatusNotImplemented)
	}
}

func TestProtectedEndpointRejectsBodiesOverLimit(t *testing.T) {
	server, err := NewServer(Config{SharedToken: "secret", MaxBodyBytes: 4}, fakeReadiness{})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/events", strings.NewReader("12345"))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set(requestIDHeader, "req-1")
	req.Header.Set(contractHeader, "1")
	resp := httptest.NewRecorder()
	server.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body limit status = %d, want %d", resp.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestNewServerRejectsUnsafeConfiguration(t *testing.T) {
	checks := []Config{
		{ContractMajor: "1", MaxBodyBytes: 4},
		{SharedToken: "secret", ContractMajor: "0"},
		{SharedToken: "secret", MaxBodyBytes: -1},
	}
	for _, config := range checks {
		if _, err := NewServer(config, fakeReadiness{}); err == nil {
			t.Fatalf("NewServer(%+v) unexpectedly succeeded", config)
		}
	}
}
