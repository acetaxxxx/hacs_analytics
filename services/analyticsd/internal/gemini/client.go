// Package gemini adapts Google's official GenAI Go SDK to the report port.
package gemini

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"google.golang.org/genai"
)

const DefaultModel = "gemini-2.5-flash"

// ErrorCode is a stable classification used by the report retry scheduler.
type ErrorCode string

const (
	Transient      ErrorCode = "transient"
	RateLimited    ErrorCode = "rate_limited"
	Authentication ErrorCode = "authentication"
	InvalidRequest ErrorCode = "invalid_request"
	SchemaInvalid  ErrorCode = "schema_invalid"
	Unavailable    ErrorCode = "unavailable"
)

// Error carries safe failure metadata without exposing provider response bodies.
type Error struct {
	Code           ErrorCode
	Kind           string
	HTTPStatus     int
	ProviderStatus string
	CauseType      string
	Err            error
}

func (e *Error) Error() string {
	if e == nil || e.Err == nil {
		return string(e.Code)
	}
	return fmt.Sprintf("gemini %s: %v", e.Code, e.Err)
}
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Client calls the Gemini Developer API. The API key never leaves this type.
type Client struct {
	client *genai.Client
	model  string
}

func New(ctx context.Context, apiKey, model string) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("gemini API key is required")
	}
	if strings.TrimSpace(model) == "" {
		model = DefaultModel
	}
	if !strings.HasPrefix(model, "gemini-") || len(model) <= len("gemini-") || len(model) > 128 {
		return nil, errors.New("gemini model must be a valid gemini-* model name")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: apiKey, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return nil, fmt.Errorf("create gemini client: %w", err)
	}
	return &Client{client: client, model: model}, nil
}

func (c *Client) Model() string {
	if c == nil {
		return ""
	}
	return c.model
}

// Generate requests JSON mode. The report package validates the complete
// response and cross-checks evidence references after this call.
func (c *Client) Generate(ctx context.Context, prompt string) ([]byte, error) {
	if c == nil || c.client == nil {
		return nil, &Error{Code: Unavailable, Err: errors.New("gemini client is not configured")}
	}
	response, err := c.client.Models.GenerateContent(ctx, c.model, genai.Text(prompt), &genai.GenerateContentConfig{ResponseMIMEType: "application/json", MaxOutputTokens: 8192})
	if err != nil {
		return nil, classify(err)
	}
	if response == nil || strings.TrimSpace(response.Text()) == "" {
		return nil, &Error{Code: SchemaInvalid, Err: errors.New("empty Gemini response")}
	}
	return []byte(response.Text()), nil
}

func classify(err error) error {
	var apiError genai.APIError
	if errors.As(err, &apiError) {
		return classifyAPIError(rootErrorType(err), apiError.Code, apiError.Status)
	}
	var apiErrorPointer *genai.APIError
	if errors.As(err, &apiErrorPointer) && apiErrorPointer != nil {
		return classifyAPIError(rootErrorType(err), apiErrorPointer.Code, apiErrorPointer.Status)
	}

	causeType := rootErrorType(err)
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: Transient, Kind: "deadline_exceeded", CauseType: causeType, Err: errors.New("transient provider failure")}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		kind := "network_error"
		if networkError.Timeout() {
			kind = "network_timeout"
		}
		return &Error{Code: Transient, Kind: kind, CauseType: causeType, Err: errors.New("transient provider failure")}
	}

	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "401"), strings.Contains(message, "403"), strings.Contains(message, "api key"), strings.Contains(message, "unauthorized"):
		return &Error{Code: Authentication, Kind: "authentication", CauseType: causeType, Err: errors.New("provider authentication failed")}
	case strings.Contains(message, "400"), strings.Contains(message, "invalid argument"):
		return &Error{Code: InvalidRequest, Kind: "invalid_request", CauseType: causeType, Err: errors.New("provider rejected request")}
	case strings.Contains(message, "429"), strings.Contains(message, "resource exhausted"):
		return &Error{Code: RateLimited, Kind: "rate_limited", CauseType: causeType, Err: errors.New("provider rate limited request")}
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return &Error{Code: Transient, Kind: "timeout", CauseType: causeType, Err: errors.New("transient provider failure")}
	case strings.Contains(message, "connection"):
		return &Error{Code: Transient, Kind: "connection_error", CauseType: causeType, Err: errors.New("transient provider failure")}
	case strings.Contains(message, "503"), strings.Contains(message, "500"):
		return &Error{Code: Transient, Kind: "provider_5xx", CauseType: causeType, Err: errors.New("transient provider failure")}
	default:
		return &Error{Code: Unavailable, Kind: "provider_error", CauseType: causeType, Err: errors.New("provider request failed")}
	}
}

func classifyAPIError(causeType string, statusCode int, providerStatus string) error {
	code := Unavailable
	kind := "provider_http_error"
	switch {
	case statusCode == 401 || statusCode == 403:
		code = Authentication
		kind = "authentication"
	case statusCode == 400:
		code = InvalidRequest
		kind = "invalid_request"
	case statusCode == 429:
		code = RateLimited
		kind = "rate_limited"
	case statusCode == 500 || statusCode == 503:
		code = Transient
		kind = "provider_5xx"
	}
	return &Error{
		Code:           code,
		Kind:           kind,
		HTTPStatus:     statusCode,
		ProviderStatus: providerStatus,
		CauseType:      causeType,
		Err:            errors.New("provider request failed"),
	}
}

func rootErrorType(err error) string {
	for {
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			return fmt.Sprintf("%T", err)
		}
		err = unwrapped
	}
}
