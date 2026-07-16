package gtfs

import (
	"errors"
	"net/url"
	"strings"
)

// RedactSourceURL returns a user-safe representation of a GTFS source URL.
// Requests and persisted source keys retain the exact configured URL; only
// user-visible output retains only the origin. Paths may themselves contain
// signed tokens, so they are confidential at the same boundary as query data.
func RedactSourceURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "[redacted GTFS source]"
	}
	parsed.User = nil
	parsed.Path = ""
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

type sanitizedSourceError struct {
	message string
	cause   error
}

func (e *sanitizedSourceError) Error() string { return e.message }
func (e *sanitizedSourceError) Unwrap() error { return e.cause }

// sanitizeSourceError retains the original error chain for cancellation and
// typed-error checks while preventing an HTTP client's URL wrapper from
// exposing a credential-bearing source URL.
func sanitizeSourceError(err error, sourceURL string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		message = urlErr.Err.Error()
	}
	message = redactSourceText(message, sourceURL)
	if strings.TrimSpace(message) == "" {
		message = "GTFS source request failed"
	}
	return &sanitizedSourceError{message: message, cause: err}
}

func redactSourceText(message, sourceURL string) string {
	redacted := RedactSourceURL(sourceURL)
	if sourceURL != "" {
		message = strings.ReplaceAll(message, sourceURL, redacted)
	}
	parsed, err := url.Parse(sourceURL)
	if err != nil {
		return message
	}
	if parsed.User != nil {
		message = redactSourceValue(message, parsed.User.Username())
		if password, ok := parsed.User.Password(); ok {
			message = redactSourceValue(message, password)
		}
	}
	if path := parsed.EscapedPath(); path != "" && path != "/" {
		message = strings.ReplaceAll(message, path, "[REDACTED]")
	}
	if path := parsed.Path; path != "" && path != "/" {
		message = strings.ReplaceAll(message, path, "[REDACTED]")
	}
	for _, values := range parsed.Query() {
		for _, value := range values {
			message = redactSourceValue(message, value)
		}
	}
	return message
}

func redactSourceValue(message, value string) string {
	if value == "" {
		return message
	}
	for _, candidate := range []string{value, url.QueryEscape(value), url.PathEscape(value)} {
		message = strings.ReplaceAll(message, candidate, "[REDACTED]")
	}
	return message
}
