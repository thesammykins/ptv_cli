package gtfsrt

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrorKind classifies a GTFS Realtime failure without text parsing.
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

// Error is a typed GTFS Realtime client error.
type Error struct {
	Kind       ErrorKind
	FeedID     string
	StatusCode int
	Message    string
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "GTFS Realtime error"
	}
	prefix := "GTFS Realtime"
	if e.FeedID != "" {
		prefix += " " + e.FeedID
	}
	if e.StatusCode != 0 {
		prefix += fmt.Sprintf(" error (%d)", e.StatusCode)
	}
	if e.Message == "" {
		return prefix
	}
	return prefix + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// IsKind reports whether err contains a GTFS Realtime Error with kind.
func IsKind(err error, kind ErrorKind) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.Kind == kind
}

// sanitizeCredentialError is the final boundary for every client failure. It
// prevents a gateway, transport, decoder, or caller-controlled feed field from
// reflecting the configured KeyID through Error(), Unwrap(), or JSON encoding.
func sanitizeCredentialError(err error, credential string) error {
	if err == nil || credential == "" {
		return err
	}

	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return errors.New(redactCredential(err.Error(), credential))
	}

	sanitized := *apiErr
	sanitized.FeedID = redactCredential(sanitized.FeedID, credential)
	sanitized.Message = redactCredential(sanitized.Message, credential)
	sanitized.Err = sanitizeCredentialCause(sanitized.Err, credential)
	return &sanitized
}

func sanitizeCredentialCause(err error, credential string) error {
	if err == nil {
		return nil
	}
	// Preserve cancellation identity while discarding any wrapping text that
	// may have reflected a request header.
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return errors.New(redactCredential(err.Error(), credential))
	}
}

func redactCredential(value, credential string) string {
	return strings.ReplaceAll(value, credential, "[redacted]")
}
