package ptvapi

import (
	"errors"
	"fmt"
	"time"
)

// ErrorKind classifies a PTV API failure without requiring callers to parse
// user-facing error text.
type ErrorKind string

const (
	ErrorCanceled        ErrorKind = "canceled"
	ErrorTimeout         ErrorKind = "timeout"
	ErrorAuthentication  ErrorKind = "authentication"
	ErrorRateLimit       ErrorKind = "rate_limit"
	ErrorNotFound        ErrorKind = "not_found"
	ErrorInvalidRequest  ErrorKind = "invalid_request"
	ErrorInvalidResponse ErrorKind = "invalid_response"
	ErrorTransport       ErrorKind = "transport"
	ErrorUpstream        ErrorKind = "upstream"
	ErrorHTTP            ErrorKind = "http"
)

// Error is a typed, sanitized PTV API error. It intentionally stores no
// request URL because signed PTV URLs contain credentials.
type Error struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "PTV API error"
	}
	prefix := "PTV API"
	if e.StatusCode != 0 {
		prefix = fmt.Sprintf("PTV API error (%d)", e.StatusCode)
	}
	if e.Message == "" {
		return prefix
	}
	return prefix + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// IsKind reports whether err contains a PTV API Error with kind.
func IsKind(err error, kind ErrorKind) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Kind == kind
}
