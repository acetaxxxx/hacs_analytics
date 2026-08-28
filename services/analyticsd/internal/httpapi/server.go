// Package httpapi exposes the sidecar's versioned, LAN-only HTTP boundary.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/acetaxxxx/hacs_analytics/services/analyticsd/internal/store"
)

const (
	defaultContractMajor = "1"
	defaultMaxBodyBytes  = 1 << 20
	requestIDHeader      = "X-Request-ID"
	contractHeader       = "X-Homekeeper-Contract-Version"
)

var entityIDPattern = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

// Readiness is the smallest dependency the HTTP layer needs to expose
// database readiness.
type Readiness interface {
	Ping(context.Context) error
}

// IngestStore provides database readiness and event/heartbeat persistence.
type IngestStore interface {
	Readiness
	IngestEvents(ctx context.Context, requestID string, batch store.EventBatch) (store.IngestResult, error)
	IngestHeartbeat(ctx context.Context, heartbeat store.Heartbeat, tolerance time.Duration) (store.HeartbeatResult, error)
}

// Config configures the HTTP boundary. Secrets are never included in a
// response or an error.
type Config struct {
	SharedToken      string
	ContractMajor    string
	MaxBodyBytes     int64
	GeminiConfigured bool
}

// Health is the public readiness response consumed by container probes and
// the Home Assistant integration.
type Health struct {
	Status           string `json:"status"`
	Database         string `json:"database"`
	GeminiConfigured bool   `json:"gemini_configured"`
}

// Liveness is deliberately independent of the database and Gemini API.
type Liveness struct {
	Status string `json:"status"`
}

type errorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

type server struct {
	config Config
	store  IngestStore
}

// NewServer validates the HTTP boundary configuration and returns an
// http.Handler owner. The first supported contract is major version 1.
func NewServer(config Config, st IngestStore) (*server, error) {
	if strings.TrimSpace(config.SharedToken) == "" {
		return nil, errors.New("shared token is required")
	}
	if config.ContractMajor == "" {
		config.ContractMajor = defaultContractMajor
	}
	if config.ContractMajor != defaultContractMajor {
		return nil, fmt.Errorf("unsupported contract major %q", config.ContractMajor)
	}
	if config.MaxBodyBytes == 0 {
		config.MaxBodyBytes = defaultMaxBodyBytes
	}
	if config.MaxBodyBytes < 1 {
		return nil, errors.New("max body bytes must be positive")
	}
	return &server{config: config, store: st}, nil
}

// Handler returns the complete HTTP handler for the sidecar.
func (s *server) Handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodPost {
		writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
		return
	}

	switch request.URL.Path {
	case "/api/v1/health/live":
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
			return
		}
		writeJSON(writer, http.StatusOK, Liveness{Status: "ok"})
		return
	case "/api/v1/health":
		if request.Method != http.MethodGet {
			writeError(writer, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
			return
		}
		s.handleHealth(writer, request)
		return
	}

	requestID, ok := s.authenticate(writer, request)
	if !ok {
		return
	}
	if request.ContentLength > s.config.MaxBodyBytes {
		writeError(writer, http.StatusRequestEntityTooLarge, "body_too_large", "request body is too large", requestID)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, s.config.MaxBodyBytes)

	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/ingest/events":
		s.handleIngestEvents(writer, request, requestID)
		return
	case request.Method == http.MethodPost && request.URL.Path == "/api/v1/ingest/heartbeat":
		s.handleIngestHeartbeat(writer, request, requestID)
		return
	}

	writeError(writer, http.StatusNotFound, "not_found", "resource not found", requestID)
}

func (s *server) authenticate(writer http.ResponseWriter, request *http.Request) (string, bool) {
	requestID := request.Header.Get(requestIDHeader)
	if requestID == "" || len(requestID) > 128 {
		writeError(writer, http.StatusBadRequest, "invalid_request_id", "X-Request-ID is required", requestID)
		return "", false
	}
	if request.Header.Get(contractHeader) != s.config.ContractMajor {
		writeError(writer, http.StatusConflict, "unsupported_contract", "unsupported contract version", requestID)
		return requestID, false
	}

	const bearerPrefix = "Bearer "
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, bearerPrefix) {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "invalid authorization", requestID)
		return requestID, false
	}
	token := strings.TrimSpace(strings.TrimPrefix(authorization, bearerPrefix))
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.config.SharedToken)) != 1 {
		writeError(writer, http.StatusUnauthorized, "unauthorized", "invalid authorization", requestID)
		return requestID, false
	}
	return requestID, true
}

func (s *server) handleHealth(writer http.ResponseWriter, request *http.Request) {
	ready := s.store == nil || s.store.Ping(request.Context()) == nil
	health := Health{
		Status:           "ok",
		Database:         "ready",
		GeminiConfigured: s.config.GeminiConfigured,
	}
	status := http.StatusOK
	if !ready {
		health.Status = "not_ready"
		health.Database = "unavailable"
		status = http.StatusServiceUnavailable
	}
	writeJSON(writer, status, health)
}

type ingestEventJSON struct {
	EventID        *string        `json:"event_id"`
	ObservedAt     *string        `json:"observed_at"`
	EntityID       *string        `json:"entity_id"`
	Kind           *string        `json:"kind"`
	OldState       *string        `json:"old_state"`
	NewState       *string        `json:"new_state"`
	NumericValue   *float64       `json:"numeric_value"`
	Unit           *string        `json:"unit"`
	Metadata       map[string]any `json:"metadata"`
	ProfileVersion *int           `json:"profile_version"`
}

type eventBatchJSON struct {
	SourceInstance *string           `json:"source_instance"`
	SentAt         *string           `json:"sent_at"`
	Events         []ingestEventJSON `json:"events"`
}

func (s *server) handleIngestEvents(writer http.ResponseWriter, request *http.Request, requestID string) {
	var body eventBatchJSON
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "malformed JSON body", requestID)
		return
	}

	if body.SourceInstance == nil || len(*body.SourceInstance) < 1 || len(*body.SourceInstance) > 128 {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "invalid source_instance", requestID)
		return
	}
	if body.SentAt == nil {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "sent_at is required", requestID)
		return
	}
	sentAt, err := time.Parse(time.RFC3339, *body.SentAt)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "invalid sent_at timestamp", requestID)
		return
	}
	if body.Events == nil {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "events list is required", requestID)
		return
	}
	if len(body.Events) > 100 {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "batch exceeds limit of 100 events", requestID)
		return
	}

	events := make([]store.IngestEvent, 0, len(body.Events))
	for _, raw := range body.Events {
		if raw.EventID == nil || len(*raw.EventID) < 1 || len(*raw.EventID) > 128 {
			writeError(writer, http.StatusBadRequest, "invalid_payload", "invalid event_id", requestID)
			return
		}
		if raw.ObservedAt == nil {
			writeError(writer, http.StatusBadRequest, "invalid_payload", "observed_at is required", requestID)
			return
		}
		observedAt, err := time.Parse(time.RFC3339, *raw.ObservedAt)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "invalid_payload", "invalid observed_at timestamp", requestID)
			return
		}
		if raw.EntityID == nil || !entityIDPattern.MatchString(*raw.EntityID) {
			writeError(writer, http.StatusBadRequest, "invalid_payload", "invalid entity_id format", requestID)
			return
		}
		if raw.Kind == nil || (*raw.Kind != "state_change" && *raw.Kind != "snapshot") {
			writeError(writer, http.StatusBadRequest, "invalid_payload", "invalid kind enum", requestID)
			return
		}
		if raw.NewState == nil || len(*raw.NewState) > 256 {
			writeError(writer, http.StatusBadRequest, "invalid_payload", "invalid new_state", requestID)
			return
		}
		if raw.OldState != nil && len(*raw.OldState) > 256 {
			writeError(writer, http.StatusBadRequest, "invalid_payload", "old_state exceeds 256 characters", requestID)
			return
		}
		if raw.NumericValue != nil {
			val := *raw.NumericValue
			if math.IsNaN(val) || math.IsInf(val, 0) || val < -1e15 || val > 1e15 {
				writeError(writer, http.StatusBadRequest, "invalid_payload", "numeric_value out of range", requestID)
				return
			}
		}
		if raw.Unit != nil && len(*raw.Unit) > 64 {
			writeError(writer, http.StatusBadRequest, "invalid_payload", "unit exceeds 64 characters", requestID)
			return
		}
		if raw.Metadata == nil {
			writeError(writer, http.StatusBadRequest, "invalid_payload", "metadata object is required", requestID)
			return
		}
		if len(raw.Metadata) > 32 {
			writeError(writer, http.StatusBadRequest, "invalid_payload", "metadata exceeds 32 keys", requestID)
			return
		}
		for _, val := range raw.Metadata {
			if val == nil {
				continue
			}
			switch val.(type) {
			case string, float64, bool:
			default:
				writeError(writer, http.StatusBadRequest, "invalid_payload", "nested metadata values not allowed", requestID)
				return
			}
		}
		profileVersion := 1
		if raw.ProfileVersion != nil {
			if *raw.ProfileVersion < 1 {
				writeError(writer, http.StatusBadRequest, "invalid_payload", "profile_version must be >= 1", requestID)
				return
			}
			profileVersion = *raw.ProfileVersion
		}

		events = append(events, store.IngestEvent{
			EventID:        *raw.EventID,
			ObservedAt:     observedAt,
			EntityID:       *raw.EntityID,
			Kind:           *raw.Kind,
			OldState:       raw.OldState,
			NewState:       *raw.NewState,
			NumericValue:   raw.NumericValue,
			Unit:           raw.Unit,
			Metadata:       raw.Metadata,
			ProfileVersion: profileVersion,
		})
	}

	batch := store.EventBatch{
		SourceInstance: *body.SourceInstance,
		SentAt:         sentAt,
		Events:         events,
	}

	result, err := s.store.IngestEvents(request.Context(), requestID, batch)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "failed to ingest events", requestID)
		return
	}

	writeJSON(writer, http.StatusOK, result)
}

type heartbeatJSON struct {
	SourceInstance *string `json:"source_instance"`
	ObservedAt     *string `json:"observed_at"`
}

func (s *server) handleIngestHeartbeat(writer http.ResponseWriter, request *http.Request, requestID string) {
	var body heartbeatJSON
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "malformed JSON body", requestID)
		return
	}

	if body.SourceInstance == nil || len(*body.SourceInstance) < 1 || len(*body.SourceInstance) > 128 {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "invalid source_instance", requestID)
		return
	}
	if body.ObservedAt == nil {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "observed_at is required", requestID)
		return
	}
	observedAt, err := time.Parse(time.RFC3339, *body.ObservedAt)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_payload", "invalid observed_at timestamp", requestID)
		return
	}

	result, err := s.store.IngestHeartbeat(request.Context(), store.Heartbeat{
		SourceInstance: *body.SourceInstance,
		ObservedAt:     observedAt,
	}, 90*time.Second)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal_error", "failed to record heartbeat", requestID)
		return
	}

	writeJSON(writer, http.StatusOK, result)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message, requestID string) {
	writeJSON(writer, status, errorResponse{
		Code:      code,
		Message:   message,
		RequestID: requestID,
	})
}
