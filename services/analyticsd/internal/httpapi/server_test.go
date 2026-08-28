package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/store"
)

type fakeStore struct {
	pingErr          error
	ingestEventsRes  store.IngestResult
	ingestEventsErr  error
	ingestHeartRes   store.HeartbeatResult
	ingestHeartErr   error
	lastEventBatch   store.EventBatch
	lastHeartbeat    store.Heartbeat
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func (f *fakeStore) IngestEvents(ctx context.Context, requestID string, batch store.EventBatch) (store.IngestResult, error) {
	f.lastEventBatch = batch
	if f.ingestEventsErr != nil {
		return store.IngestResult{}, f.ingestEventsErr
	}
	res := f.ingestEventsRes
	res.RequestID = requestID
	return res, nil
}

func (f *fakeStore) IngestHeartbeat(ctx context.Context, heartbeat store.Heartbeat, tolerance time.Duration) (store.HeartbeatResult, error) {
	f.lastHeartbeat = heartbeat
	if f.ingestHeartErr != nil {
		return store.HeartbeatResult{}, f.ingestHeartErr
	}
	return f.ingestHeartRes, nil
}

func TestHealthReportsReadinessAndGeminiConfiguration(t *testing.T) {
	server, err := NewServer(Config{
		SharedToken:      "secret",
		GeminiConfigured: true,
	}, &fakeStore{})
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

	notReady, err := NewServer(Config{SharedToken: "secret"}, &fakeStore{pingErr: errors.New("down")})
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
	server, err := NewServer(Config{SharedToken: "secret"}, &fakeStore{pingErr: errors.New("down")})
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
	server, err := NewServer(Config{SharedToken: "secret"}, &fakeStore{})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	request := func(token, requestID, contract string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/events", strings.NewReader(`{
			"source_instance": "ha-1",
			"sent_at": "2026-08-28T12:00:00Z",
			"events": []
		}`))
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
	if got := request("secret", "req-1", "1").Code; got != http.StatusOK {
		t.Fatalf("valid auth status = %d, want %d", got, http.StatusOK)
	}
}

func TestProtectedEndpointRejectsBodiesOverLimit(t *testing.T) {
	server, err := NewServer(Config{SharedToken: "secret", MaxBodyBytes: 4}, &fakeStore{})
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

func TestIngestEventsValidation(t *testing.T) {
	st := &fakeStore{
		ingestEventsRes: store.IngestResult{Accepted: 1, Duplicates: 0, Ignored: 0},
	}
	server, err := NewServer(Config{SharedToken: "secret"}, st)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	send := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/events", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(requestIDHeader, "req-test-events")
		req.Header.Set(contractHeader, "1")
		resp := httptest.NewRecorder()
		server.Handler().ServeHTTP(resp, req)
		return resp
	}

	// Valid payload
	validPayload := `{
		"source_instance": "ha-main",
		"sent_at": "2026-08-28T12:00:00Z",
		"events": [
			{
				"event_id": "evt-1",
				"observed_at": "2026-08-28T12:00:00Z",
				"entity_id": "sensor.living_room_temperature",
				"kind": "state_change",
				"new_state": "23.5",
				"numeric_value": 23.5,
				"unit": "°C",
				"metadata": {"friendly_name": "Living Room Temp"},
				"profile_version": 1
			}
		]
	}`
	resp := send(validPayload)
	if resp.Code != http.StatusOK {
		t.Fatalf("valid ingest status = %d, want %d; body = %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	var res store.IngestResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode ingest result: %v", err)
	}
	if res.Accepted != 1 || res.RequestID != "req-test-events" {
		t.Fatalf("unexpected ingest result: %+v", res)
	}

	// Invalid payloads
	invalidCases := []string{
		`{}`,                                // missing required fields
		`{"source_instance":"","sent_at":"2026-08-28T12:00:00Z","events":[]}`, // empty source_instance
		`{"source_instance":"ha","sent_at":"invalid-date","events":[]}`,      // invalid date
		`{"source_instance":"ha","sent_at":"2026-08-28T12:00:00Z","events":[{"event_id":"e1","observed_at":"2026-08-28T12:00:00Z","entity_id":"INVALID_ENTITY","kind":"state_change","new_state":"on","metadata":{}}]}`, // bad entity_id pattern
		`{"source_instance":"ha","sent_at":"2026-08-28T12:00:00Z","events":[{"event_id":"e1","observed_at":"2026-08-28T12:00:00Z","entity_id":"sensor.temp","kind":"unknown_kind","new_state":"on","metadata":{}}]}`, // bad kind
		`{"source_instance":"ha","sent_at":"2026-08-28T12:00:00Z","events":[{"event_id":"e1","observed_at":"2026-08-28T12:00:00Z","entity_id":"sensor.temp","kind":"state_change","new_state":"on","metadata":{"nested":{"a":1}}}]}`, // nested metadata
	}

	for i, c := range invalidCases {
		r := send(c)
		if r.Code != http.StatusBadRequest {
			t.Fatalf("case %d: expected 400 Bad Request, got %d, body: %s", i, r.Code, r.Body.String())
		}
	}
}

func TestIngestHeartbeatEndpoint(t *testing.T) {
	st := &fakeStore{
		ingestHeartRes: store.HeartbeatResult{Status: "healthy", GapDetected: false},
	}
	server, err := NewServer(Config{SharedToken: "secret"}, st)
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	send := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/heartbeat", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set(requestIDHeader, "req-hb")
		req.Header.Set(contractHeader, "1")
		resp := httptest.NewRecorder()
		server.Handler().ServeHTTP(resp, req)
		return resp
	}

	// Valid heartbeat
	resp := send(`{"source_instance": "ha-1", "observed_at": "2026-08-28T12:00:00Z"}`)
	if resp.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, want %d", resp.Code, http.StatusOK)
	}
	var res store.HeartbeatResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatalf("decode heartbeat response: %v", err)
	}
	if res.Status != "healthy" || res.GapDetected {
		t.Fatalf("unexpected heartbeat result: %+v", res)
	}

	// Invalid heartbeat
	if got := send(`{"source_instance": ""}`).Code; got != http.StatusBadRequest {
		t.Fatalf("invalid heartbeat status = %d, want 400", got)
	}
}

func TestNewServerRejectsUnsafeConfiguration(t *testing.T) {
	checks := []Config{
		{ContractMajor: "1", MaxBodyBytes: 4},
		{SharedToken: "secret", ContractMajor: "0"},
		{SharedToken: "secret", MaxBodyBytes: -1},
	}
	for _, config := range checks {
		if _, err := NewServer(config, &fakeStore{}); err == nil {
			t.Fatalf("NewServer(%+v) unexpectedly succeeded", config)
		}
	}
}
