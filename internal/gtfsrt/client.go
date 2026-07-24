package gtfsrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	gtfs "github.com/MobilityData/gtfs-realtime-bindings/golang/gtfs"
	"google.golang.org/protobuf/proto"
)

const (
	defaultRequestTimeout    = 30 * time.Second
	defaultMaxProtobufBytes  = int64(32 << 20)
	defaultMaxErrorBodyBytes = int64(64 << 10)
	maximumRetryAfter        = 5 * time.Minute
)

// ClientOptions controls transport, time, and response limits.
type ClientOptions struct {
	HTTPClient       *http.Client
	RequestTimeout   time.Duration
	MaxProtobufBytes int64
	MaxErrorBytes    int64
	Now              func() time.Time
}

// Client fetches Transport Victoria GTFS Realtime feeds using their catalog
// authentication metadata.
type Client struct {
	keyID            string
	http             *http.Client
	requestTimeout   time.Duration
	maxProtobufBytes int64
	maxErrorBytes    int64
	now              func() time.Time
	limiters         map[string]*endpointLimiter
	limiterMu        sync.Mutex
}

type endpointLimiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

func newEndpointLimiter(now time.Time) *endpointLimiter {
	return &endpointLimiter{tokens: 24, last: now}
}
func (l *endpointLimiter) wait(ctx context.Context) error {
	const ratePerSecond = 24.0 / 60.0
	for {
		l.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(l.last).Seconds()
		if elapsed > 0 {
			l.tokens = minimum(24, l.tokens+elapsed*ratePerSecond)
			l.last = now
		}
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		delay := time.Duration((1 - l.tokens) / ratePerSecond * float64(time.Second))
		l.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		}
	}
}
func minimum(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// New constructs a GTFS Realtime client using the catalog's documented KeyID
// subscription credential.
func New(keyID string) *Client {
	return NewWithOptions(keyID, ClientOptions{})
}

// NewWithOptions constructs an injectable, resource-bounded client.
func NewWithOptions(keyID string, opts ClientOptions) *Client {
	requestTimeout := opts.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	maxProtobufBytes := opts.MaxProtobufBytes
	if maxProtobufBytes <= 0 {
		maxProtobufBytes = defaultMaxProtobufBytes
	}
	maxErrorBytes := opts.MaxErrorBytes
	if maxErrorBytes <= 0 {
		maxErrorBytes = defaultMaxErrorBodyBytes
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		transport := &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     false,
			MaxIdleConns:          10,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		httpClient = &http.Client{Transport: transport}
	}

	return &Client{
		keyID:            strings.TrimSpace(keyID),
		http:             httpClient,
		requestTimeout:   requestTimeout,
		maxProtobufBytes: maxProtobufBytes,
		maxErrorBytes:    maxErrorBytes,
		now:              now,
		limiters:         make(map[string]*endpointLimiter),
	}
}

// FetchSnapshot fetches and normalizes one catalog feed. It performs at most
// one HTTP request and never changes authentication headers as a retry.
func (c *Client) FetchSnapshot(ctx context.Context, feed Feed) (snapshot *Snapshot, err error) {
	defer func() {
		err = sanitizeCredentialError(err, c.keyID)
	}()

	if err := validateFeed(feed); err != nil {
		return nil, err
	}
	canonical := feed.URL
	c.limiterMu.Lock()
	limiter := c.limiters[canonical]
	if limiter == nil {
		limiter = newEndpointLimiter(c.now())
		c.limiters[canonical] = limiter
	}
	c.limiterMu.Unlock()
	if err := limiter.wait(ctx); err != nil {
		return nil, &Error{Kind: ErrorCanceled, FeedID: feed.ID, Message: "request canceled", Err: err}
	}
	if feed.Authentication.Required && c.keyID == "" {
		return nil, &Error{Kind: ErrorAuthentication, FeedID: feed.ID, Message: "missing Open Data KeyID credential"}
	}

	requestCtx := ctx
	cancel := func() {}
	if c.requestTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, c.requestTimeout)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, feed.URL, nil)
	if err != nil {
		return nil, &Error{Kind: ErrorInvalidRequest, FeedID: feed.ID, Message: "constructing GTFS Realtime request", Err: safeTransportError(err)}
	}
	req.Header.Set("Accept", "application/x-protobuf")
	if c.keyID != "" {
		// Assign directly so the documented KeyID spelling is retained on the
		// wire instead of MIME-canonicalizing it to Keyid.
		req.Header[KeyIDHeader] = []string{c.keyID}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classifyTransportError(requestCtx, feed.ID, err)
	}
	defer resp.Body.Close()

	limit := c.maxProtobufBytes
	if resp.StatusCode != http.StatusOK && c.maxErrorBytes < limit {
		limit = c.maxErrorBytes
	}
	body, err := readBounded(resp.Body, limit)
	if err != nil {
		var netErr net.Error
		if requestCtx.Err() != nil || errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return nil, classifyTransportError(requestCtx, feed.ID, err)
		}
		message := "reading GTFS Realtime response"
		var limitErr *responseLimitError
		if errors.As(err, &limitErr) {
			message += ": " + limitErr.Error()
		}
		return nil, &Error{Kind: ErrorInvalidResponse, FeedID: feed.ID, StatusCode: resp.StatusCode, Message: message, Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(feed.ID, resp, c.now())
	}

	var message gtfs.FeedMessage
	if err := proto.Unmarshal(body, &message); err != nil {
		return nil, &Error{Kind: ErrorInvalidResponse, FeedID: feed.ID, StatusCode: resp.StatusCode, Message: "decoding GTFS Realtime protobuf", Err: err}
	}
	return NormalizeSnapshot(feed, &message, c.now()), nil
}

func validateFeed(feed Feed) error {
	if strings.TrimSpace(feed.ID) == "" {
		return &Error{Kind: ErrorInvalidRequest, Message: "feed ID is required"}
	}
	parsed, err := url.Parse(feed.URL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return &Error{Kind: ErrorInvalidRequest, FeedID: feed.ID, Message: "feed URL must be absolute HTTP(S)"}
	}
	if feed.Authentication.Header != KeyIDHeader {
		return &Error{Kind: ErrorInvalidRequest, FeedID: feed.ID, Message: "unsupported feed authentication contract"}
	}
	return nil
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("invalid response limit %d", limit)
	}
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, &responseLimitError{limit: limit}
	}
	return body, nil
}

type responseLimitError struct {
	limit int64
}

func (e *responseLimitError) Error() string {
	return fmt.Sprintf("response exceeds %d-byte limit", e.limit)
}

func statusError(feedID string, resp *http.Response, now time.Time) error {
	// Upstream error bodies are deliberately not exposed. Some gateways echo
	// request headers, which would turn a KeyID into a CLI warning or JSON field.
	message := http.StatusText(resp.StatusCode)
	if message == "" {
		message = "unexpected upstream HTTP status"
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
		FeedID:     feedID,
		StatusCode: resp.StatusCode,
		Message:    message,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), now),
	}
}

func classifyTransportError(ctx context.Context, feedID string, err error) error {
	switch {
	case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return &Error{Kind: ErrorCanceled, FeedID: feedID, Message: "request canceled", Err: context.Canceled}
	case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return &Error{Kind: ErrorTimeout, FeedID: feedID, Message: "request timed out", Err: context.DeadlineExceeded}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Kind: ErrorTimeout, FeedID: feedID, Message: "request timed out", Err: errors.New("network timeout")}
	}
	return &Error{Kind: ErrorTransport, FeedID: feedID, Message: "request failed", Err: safeTransportError(err)}
}

func safeTransportError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return errors.New(urlErr.Err.Error())
	}
	return errors.New(err.Error())
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
	if wait > maximumRetryAfter {
		return maximumRetryAfter
	}
	return wait
}
