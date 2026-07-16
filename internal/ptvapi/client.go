package ptvapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRequestTimeout   = 30 * time.Second
	defaultMaxResponseBytes = int64(16 << 20)
	defaultMaxErrorBytes    = int64(64 << 10)
	maxRetryAfter           = 5 * time.Minute
)

// ClientOptions controls transport and resource limits. A supplied HTTPClient
// is used as-is, allowing deterministic local contract tests.
type ClientOptions struct {
	HTTPClient       *http.Client
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	MaxErrorBytes    int64
}

// Client is a signed HTTP client for the PTV Timetable API v3.
type Client struct {
	baseURL          string
	apiKey           string
	devID            string
	http             *http.Client
	requestTimeout   time.Duration
	maxResponseBytes int64
	maxErrorBytes    int64
}

// New constructs a Client with bounded requests and explicit transport
// timeouts. Use NewWithOptions to inject a local HTTP client in tests.
func New(baseURL, apiKey, devID string) *Client {
	return NewWithOptions(baseURL, apiKey, devID, ClientOptions{})
}

// NewWithOptions constructs a Client with explicit transport and resource
// limits. Zero-valued limits select conservative defaults.
func NewWithOptions(baseURL, apiKey, devID string, opts ClientOptions) *Client {
	requestTimeout := opts.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	maxResponseBytes := opts.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultMaxResponseBytes
	}
	maxErrorBytes := opts.MaxErrorBytes
	if maxErrorBytes <= 0 {
		maxErrorBytes = defaultMaxErrorBytes
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		transport := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		httpClient = &http.Client{Transport: transport}
	}

	return &Client{
		baseURL:          strings.TrimRight(baseURL, "/"),
		apiKey:           apiKey,
		devID:            devID,
		http:             httpClient,
		requestTimeout:   requestTimeout,
		maxResponseBytes: maxResponseBytes,
		maxErrorBytes:    maxErrorBytes,
	}
}

// get performs a signed GET request against path and decodes its JSON body.
// It never includes the signed URL in returned errors.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	if !strings.HasPrefix(path, "/v3/") {
		return &Error{Kind: ErrorInvalidRequest, Message: "PTV API path must start with /v3/"}
	}

	requestCtx := ctx
	cancel := func() {}
	if c.requestTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.requestTimeout)
	}
	defer cancel()

	signed := buildSignedURL(c.baseURL, c.apiKey, c.devID, path, query)
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, signed, nil)
	if err != nil {
		return &Error{Kind: ErrorInvalidRequest, Message: "constructing PTV API request", Err: safeTransportError(err)}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return classifyTransportError(requestCtx, err)
	}
	defer resp.Body.Close()

	limit := c.maxResponseBytes
	if resp.StatusCode != http.StatusOK && c.maxErrorBytes < limit {
		limit = c.maxErrorBytes
	}
	body, err := readLimited(resp.Body, limit)
	if err != nil {
		var netErr net.Error
		if requestCtx.Err() != nil || errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return classifyTransportError(requestCtx, err)
		}
		return &Error{Kind: ErrorInvalidResponse, StatusCode: resp.StatusCode, Message: "reading PTV API response: " + sanitizeMessage(err.Error()), Err: err}
	}

	if resp.StatusCode != http.StatusOK {
		return statusError(resp, body)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &Error{Kind: ErrorInvalidResponse, StatusCode: resp.StatusCode, Message: "decoding PTV API response", Err: err}
	}
	return nil
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("invalid response limit %d", limit)
	}
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("response exceeds %d-byte limit", limit)
	}
	return body, nil
}

func statusError(resp *http.Response, body []byte) error {
	message := ""
	var apiError ErrorResponse
	if json.Unmarshal(body, &apiError) == nil {
		message = strings.TrimSpace(apiError.Message)
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	message = sanitizeMessage(message)
	if len(message) > 300 {
		message = message[:300]
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}

	kind := ErrorHTTP
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		kind = ErrorInvalidRequest
	case http.StatusUnauthorized, http.StatusForbidden:
		kind = ErrorAuthentication
	case http.StatusNotFound:
		kind = ErrorNotFound
	case http.StatusTooManyRequests:
		kind = ErrorRateLimit
	default:
		if resp.StatusCode >= 500 {
			kind = ErrorUpstream
		}
	}
	return &Error{
		Kind:       kind,
		StatusCode: resp.StatusCode,
		Message:    message,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
	}
}

func classifyTransportError(ctx context.Context, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return &Error{Kind: ErrorCanceled, Message: "PTV API request canceled", Err: context.Canceled}
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return &Error{Kind: ErrorTimeout, Message: "PTV API request timed out", Err: context.DeadlineExceeded}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Kind: ErrorTimeout, Message: "PTV API request timed out", Err: errors.New("network timeout")}
	}
	return &Error{Kind: ErrorTransport, Message: "PTV API request failed", Err: safeTransportError(err)}
}

func safeTransportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return errors.New(sanitizeMessage(urlErr.Err.Error()))
	}
	return errors.New(sanitizeMessage(err.Error()))
}

func sanitizeMessage(message string) string {
	message = strings.TrimSpace(message)
	for _, key := range []string{"signature", "devid"} {
		message = redactQueryValue(message, key)
	}
	return message
}

func redactQueryValue(message, key string) string {
	needle := key + "="
	searchFrom := 0
	for {
		relativeStart := strings.Index(strings.ToLower(message[searchFrom:]), needle)
		if relativeStart < 0 {
			return message
		}
		start := searchFrom + relativeStart
		valueStart := start + len(needle)
		valueEnd := len(message)
		for i := valueStart; i < len(message); i++ {
			switch message[i] {
			case '&', ' ', '\t', '\r', '\n', ';':
				valueEnd = i
				i = len(message)
			}
		}
		message = message[:valueStart] + "[REDACTED]" + message[valueEnd:]
		searchFrom = valueStart + len("[REDACTED]")
	}
}

func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	var wait time.Duration
	if seconds, err := strconv.Atoi(raw); err == nil {
		if seconds > 0 {
			wait = time.Duration(seconds) * time.Second
		}
	} else if when, err := http.ParseTime(raw); err == nil && when.After(now) {
		wait = when.Sub(now)
	}
	if wait > maxRetryAfter {
		return maxRetryAfter
	}
	return wait
}
