// Package httpapi exposes the sidecar's versioned, LAN-only HTTP boundary.
package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	defaultContractMajor = "1"
	defaultMaxBodyBytes  = 1 << 20
	requestIDHeader      = "X-Request-ID"
	contractHeader       = "X-Homekeeper-Contract-Version"
)

// Readiness is the smallest dependency the HTTP layer needs to expose
// database readiness. The store adapter satisfies this interface.
type Readiness interface {
	Ping(context.Context) error
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
	config    Config
	readiness Readiness
}

// NewServer validates the HTTP boundary configuration and returns an
// http.Handler owner. The first supported contract is major version 1.
func NewServer(config Config, readiness Readiness) (*server, error) {
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
	return &server{config: config, readiness: readiness}, nil
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

	// The endpoint is intentionally reserved until the ingest ticket adds its
	// schema and transaction semantics. Keeping the route visible avoids a
	// misleading 404 during staged deployment.
	if request.Method == http.MethodPost && request.URL.Path == "/api/v1/ingest/events" {
		writeError(writer, http.StatusNotImplemented, "not_implemented", "endpoint is not implemented yet", requestID)
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
	ready := s.readiness == nil || s.readiness.Ping(request.Context()) == nil
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
